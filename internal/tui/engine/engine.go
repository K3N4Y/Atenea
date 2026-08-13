package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/checkpoint"
	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/event"
	"github.com/K3N4Y/atenea/internal/host"
	"github.com/K3N4Y/atenea/internal/learning"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/mcpclient"
	"github.com/K3N4Y/atenea/internal/paths"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/session/runner"
	"github.com/K3N4Y/atenea/internal/session/subagent"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/wiring"
)

// Config describes the headless agent assembly: the workspace root, the LLM
// provider and the durable store.
type Config struct {
	Identity      paths.Identity
	Root          string
	Provider      llm.Provider
	Store         session.Store
	LearningStore learning.Store
	Models        ModelService
	Checkpoints   checkpoint.Store
	EditSettings  func(model, sessionID string) (editmode.Config, error)
	// Sitting is the per-process agent state the engine rewires into every build
	// instead of rebuilding — the permission gate and grants, the prompt inbox, the
	// turn lifecycle and the read snapshots. It belongs to whoever owns the sitting,
	// which is internal/host when a real entrypoint is driving; nil assembles one
	// through host.NewSitting, so an engine driven on its own is a complete host of
	// one.
	Sitting *host.Sitting
}

type UndoResult struct {
	Prompt session.Prompt
	Events []session.SessionEvent
}
type CheckpointResult struct {
	ID string
}

type RewindResult struct {
	CheckpointID string
	Events       []session.SessionEvent
}

type ResumeResult struct {
	SessionID string
	Events    []session.SessionEvent
	Mode      session.Mode
	History   []string
}

var (
	ErrWorkspaceDiverged   = errors.New("workspace changed after the prompt; undo refused")
	ErrResumeActiveRun     = errors.New("stop the active run before resuming another session")
	ErrSessionNotResumable = errors.New("session is not resumable in this workspace")
)

type ModelService interface {
	Active() providerconfig.Active
	Catalog() []providerconfig.ProviderModels
	Refresh(context.Context) ([]providerconfig.ProviderModels, error)
	Select(context.Context, string, string) (providerconfig.Active, error)
}

type RoleModelService interface {
	ResolveAgentModel(context.Context, string, string) (llm.Provider, error)
}

type AgentModelService interface {
	AgentModel(string) (providerconfig.AgentModelSelection, bool)
	EffectiveAgentModel(string, string) (providerconfig.AgentModelSelection, bool)
	SetAgentModel(context.Context, string, providerconfig.AgentModelSelection) error
	ClearAgentModel(string) error
}

type SelectionPreferences interface {
	ReasoningEffort() llm.ReasoningEffort
	SetReasoningEffort(llm.ReasoningEffort) error
}

type ModelsRefreshedMsg struct {
	Providers []providerconfig.ProviderModels
	Err       string
}

// ConnectService is the optional surface a ModelService can implement to
// support /connect. providerconfig.Service does; fakes that do not care about
// connection simply omit it.
//
// It carries both kinds of connection because they are one command: the panel
// asks Connectable which kind a provider is and then drives the matching pair,
// and a service that offered only one of the two would leave half the catalog
// unconnectable through a surface that lists all of it.
type ConnectService interface {
	Connectable() []providerconfig.ConnectableProvider
	Connect(ctx context.Context, providerID, apiKey string) (providerconfig.Active, error)
	StartDeviceLogin(ctx context.Context, providerID string) (providerconfig.DeviceLogin, error)
	AwaitDeviceLogin(ctx context.Context, providerID string) (providerconfig.Active, error)
	// CancelDeviceLoginAttempt takes the attempt to retire, not just the provider:
	// the panel abandons attempts it started and may get to one after the user has a
	// second code on screen, which the blunt version would cancel instead.
	CancelDeviceLoginAttempt(providerID string, attempt uint64)
}

var _ RoleModelService = (*providerconfig.Service)(nil)
var _ AgentModelService = (*providerconfig.Service)(nil)

// Engine is the headless agent: it assembles runner + tools + permissions
// without Wails and publishes the durable session events on a Bubble Tea message
// channel. The assembly itself lives in wiring.Build (the same source of truth
// the Wails app uses); what is wired here is only the boundary, Bus -> TUI
// channel.
type Engine struct {
	events    chan tea.Msg
	reasoning *llm.ReasoningSelection
	// inbox, gate, grants and agent come from the sitting: they outlive every
	// rewire, so the user's permission answers and the turn lifecycle are not
	// dropped when the wiring is rebuilt.
	inbox      session.Inbox
	gate       *permission.MemoryGate
	grants     *permission.SessionGrants
	autoAccept *permission.AutoAcceptModes
	yolo       *permission.YoloMode
	agent      *agent.Service
	ctx        context.Context
	cancel     context.CancelFunc

	learning *learning.Service
	// runner, glob and tools are the mutable pieces of the assembly: rewire (on an MCP connect or disconnect) replaces them, so they are read under mu. glob feeds the composer's @-menu of files (the mirror of App.ListProjectFiles); tools is the catalog the TUI asks about a tool it only knows by name.
	runner   *runner.Runner
	glob     *tool.GlobTool
	tools    tool.Catalog
	agents   []agent.Def
	assembly wiring.Built

	// wiring is the base config of the assembly; rewire reuses it with the MCPTools in
	// force. mcp is the engine's manager of local (stdio) MCP servers; the declared
	// servers come from <root>/.mcp.json and are re-read on every listing, so an edit
	// to the file shows up without a restart.
	wiring         wiring.Config
	taskSupervisor *subagent.Supervisor
	mcp            *mcpclient.Manager

	// root and store mirror a.workspace.Root()/a.store in the Wails app: the workspace
	// root and the store DECORATED with EmittingStore (the same one wiring.Build
	// receives). send uses them to record Session.Cwd on the first prompt of each
	// session. Immutable after New, so they are read without mu.
	root             string
	store            session.Store
	undoStore        session.UndoStore
	checkpoints      checkpoint.Store
	models           ModelService
	refreshingModels bool

	resumeMu           sync.Mutex
	mu                 sync.Mutex
	pendingCompactions map[string]bool
	compacting         map[string]bool
	pendingRewinds     map[string]bool

	lifecycleMu  sync.Mutex
	shuttingDown bool
	shutdownDone chan struct{}
	shutdownOnce sync.Once
	compactions  sync.WaitGroup
}

// New assembles the engine from the configuration: an EmitFunc bridging the Bus's
// durable SessionEvents to the TUI channel, the store decorated with
// EmittingStore over that bus, and the whole agent through wiring.Build (tools,
// skills, subagents, runner with ask-before-run).
func New(cfg Config) *Engine {
	sitting := cfg.Sitting
	if sitting == nil {
		sitting = host.NewSitting()
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		// Generous buffer: it absorbs bursts of deltas while the TUI drains.
		events:             make(chan tea.Msg, 256),
		inbox:              sitting.Inbox,
		gate:               sitting.Gate,
		grants:             sitting.Grants,
		autoAccept:         sitting.AutoAccept,
		yolo:               sitting.Yolo,
		reasoning:          sitting.Reasoning,
		agent:              sitting.Agent,
		pendingCompactions: map[string]bool{},
		compacting:         map[string]bool{},
		pendingRewinds:     map[string]bool{},
		ctx:                ctx,
		cancel:             cancel,
		shutdownDone:       make(chan struct{}),
	}
	// The boundary: where the Wails app emits to runtime.EventsEmit, here the
	// durable event goes to the TUI channel. The blocking send is deliberate: the
	// TUI drains the channel continuously, so no event is lost under a burst.
	emit := func(name string, data ...interface{}) {
		if len(data) == 0 {
			return
		}
		if ev, ok := data[0].(session.SessionEvent); ok {
			e.sendEvent(EventMsg(ev))
		}
		if ev, ok := data[0].(tool.PreviewEvent); ok {
			e.sendEvent(PreviewMsg(ev))
		}
	}
	bus := event.NewBus(emit)
	e.root = cfg.Root
	e.undoStore, _ = cfg.Store.(session.UndoStore)
	e.store = event.NewEmittingStore(cfg.Store, bus)
	e.checkpoints = cfg.Checkpoints
	e.models = cfg.Models
	learningStore := cfg.LearningStore
	if learningStore == nil {
		learningStore = learning.NewMemoryStore()
	}
	e.learning = learning.NewService(e.ctx, learningStore, e.store, cfg.Provider, func(workspace string) {
		e.sendEvent(LearningChangedMsg{Workspace: workspace})
	})
	if err := e.learning.Recover(e.ctx); err != nil {
		log.Printf("atenea: could not recover learning runs: %v", err)
	}
	if preferences, ok := cfg.Models.(SelectionPreferences); ok {
		if err := e.reasoning.Set(preferences.ReasoningEffort()); err != nil {
			log.Printf("atenea: could not load reasoning effort: %v", err)
		}
	}
	e.mcp = mcpclient.NewManagerWithRuntime(cfg.Root, cfg.Identity, cfg.Provider, func() string {
		if cfg.Models == nil {
			return ""
		}
		return cfg.Models.Active().Model
	})
	ids := wiring.NewIDGen()
	e.taskSupervisor = subagent.NewSupervisor(ids)
	var roleProvider subagent.ProviderResolver
	if models, ok := cfg.Models.(RoleModelService); ok {
		roleProvider = func(ctx context.Context, def agent.Def) (llm.Provider, error) {
			return models.ResolveAgentModel(ctx, def.Name, def.Model)
		}
	}
	e.wiring = wiring.Config{
		Root:          cfg.Root,
		Provider:      cfg.Provider,
		Store:         e.store,
		Inbox:         e.inbox,
		Gate:          e.gate,
		Grants:        e.grants,
		AutoAccept:    e.autoAccept,
		Yolo:          e.yolo,
		Reasoning:     func() *llm.ReasoningPreference { return e.reasoning.Get() },
		Snaps:         sitting.Snapshots,
		Bus:           bus,
		ChildActivity: true,
		LocalPrompt:   e.localModels,
		Checkpoint:    e.checkpointFromTool,
		Rewind:        e.rewindFromTool,
		NextID:        ids,
		Mode:          e.agent.Mode,
		RAHEnabled: func(sessionID string) bool {
			return e.agent.Mode(sessionID) == session.ModeRAH
		},
		LSP:            true,
		RoleProvider:   roleProvider,
		TaskSupervisor: e.taskSupervisor,
		EditSettings:   cfg.EditSettings,
		LessonSection: func(_ string, latestPrompt string) string {
			lessons, err := e.learning.Lessons(e.ctx, e.root)
			if err != nil {
				log.Printf("atenea: could not load workspace lessons: %v", err)
				return ""
			}
			return learning.RenderLessons(learning.Select(latestPrompt, lessons))
		},
	}
	e.rewire()
	if configs, err := mcpclient.LoadConfig(cfg.Root); err == nil {
		e.mcp.Start(configs, e.rewire)
	}
	return e
}

// rewire re-assembles the agent with the MCP tools in force and publishes the swap:
// the same move wailsworkspace makes — build outside the locks, swap the mutable
// pieces under mu, and Configure inside lifecycleMu so it does not race prompt
// admission or shutdown.
func (e *Engine) rewire() {
	cfg := e.wiring
	cfg.MCPTools = e.mcp.Tools()
	cfg.PersistentGrants = e.mcp.PermissionRules()
	built := wiring.Build(cfg)
	e.lifecycleMu.Lock()
	e.mu.Lock()
	previous := e.assembly
	e.assembly = built
	e.runner = built.Runner
	e.glob = built.Glob
	e.tools = built.Tools
	e.agents = cloneAgentCatalog(built.Agents)
	e.mu.Unlock()
	commands := append(built.Commands.List(), localCommands(e.yolo.Authorized())...)
	commands = append(commands, e.mcp.Commands()...)
	commandSet, err := command.NewChecked(commands, e.mcp.Mentions()...)
	if err != nil {
		// A discovered skill or MCP prompt cannot take over a local command. Keep
		// the host usable with its local catalog and make the collision explicit.
		log.Printf("atenea: could not register slash commands: %v", err)
		commandSet = command.New(localCommands(e.yolo.Authorized()), e.mcp.Mentions()...)
	}
	e.agent.Configure(built.Runner, commandSet)
	previous.Close()
	e.lifecycleMu.Unlock()
}

// BatchEnvironment returns a short-lived recursive-harness capability for a
// process launched by this engine. Desktop and terminal entrypoints can use it
// without assuming their own executable implements the hidden client command.
func (e *Engine) BatchEnvironment(ctx context.Context) []string {
	e.mu.Lock()
	batchEnv := e.assembly.BatchEnv
	e.mu.Unlock()
	if batchEnv == nil {
		return nil
	}
	return batchEnv(ctx)
}

// MCPServers lists the servers declared in <root>/.mcp.json merged with the
// live connection state. The file is re-read on every call so edits show up
// the next time the picker opens or refreshes.
func (e *Engine) MCPServers() ([]mcpclient.ServerStatus, error) {
	configs, err := mcpclient.LoadConfig(e.root)
	if err != nil {
		return nil, err
	}
	return mcpclient.Merge(configs, e.mcp.Status()), nil
}

// ConnectMCPServer starts the declared server, discovers its tools, and
// rebuilds the runner registry so the next turn can call them.
func (e *Engine) ConnectMCPServer(name string) error {
	configs, err := mcpclient.LoadConfig(e.root)
	if err != nil {
		return err
	}
	for _, config := range configs {
		if config.Name != name {
			continue
		}
		if _, err := e.mcp.Connect(e.ctx, config); err != nil {
			return err
		}
		e.rewire()
		return nil
	}
	return fmt.Errorf("MCP server %q is not declared in %s", name, mcpclient.ConfigFile)
}

// DisconnectMCPServer stops the server and rebuilds the runner registry
// without its tools. Idempotent, like the manager.
func (e *Engine) DisconnectMCPServer(name string) error {
	if err := e.mcp.Disconnect(name); err != nil {
		return err
	}
	e.rewire()
	return nil
}

func (e *Engine) ModelCatalog() []providerconfig.ProviderModels {
	if e.models == nil {
		return nil
	}
	providers := e.models.Catalog()
	return providerconfig.CloneProviderModels(providers)
}

func (e *Engine) AgentCatalog() []agent.Def {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneAgentCatalog(e.agents)
}

func cloneAgentCatalog(defs []agent.Def) []agent.Def {
	cloned := append([]agent.Def(nil), defs...)
	for i := range cloned {
		cloned[i].Tools = append([]string(nil), cloned[i].Tools...)
	}
	return cloned
}

func (e *Engine) AgentModel(name string) (providerconfig.AgentModelSelection, bool) {
	service, ok := e.models.(AgentModelService)
	if !ok {
		return providerconfig.AgentModelSelection{}, false
	}
	return service.AgentModel(name)
}

func (e *Engine) EffectiveAgentModel(name, manifestModel string) (providerconfig.AgentModelSelection, bool) {
	service, ok := e.models.(AgentModelService)
	if !ok {
		return providerconfig.AgentModelSelection{}, false
	}
	return service.EffectiveAgentModel(name, manifestModel)
}

func (e *Engine) SetAgentModel(ctx context.Context, name string, selection providerconfig.AgentModelSelection) error {
	service, ok := e.models.(AgentModelService)
	if !ok {
		return errors.New("agent model selection is unavailable")
	}
	return service.SetAgentModel(ctx, name, selection)
}

func (e *Engine) ClearAgentModel(name string) error {
	service, ok := e.models.(AgentModelService)
	if !ok {
		return errors.New("agent model selection is unavailable")
	}
	return service.ClearAgentModel(name)
}

// ModelCapabilities is what the adapter currently serving turns declares about
// itself — above all the context window of the model it is running. It resolves
// through the switchable provider on every call rather than caching: /model
// swaps the adapter underneath, and a cached answer would describe the one that
// is gone. e.wiring.Provider is written once in New, so it needs no lock.
func (e *Engine) ModelCapabilities() (llm.Capabilities, bool) {
	return llm.ActiveCapabilities(e.wiring.Provider)
}

func (e *Engine) CurrentModel() providerconfig.Active {
	if e.models == nil {
		return providerconfig.Active{}
	}
	return e.models.Active()
}

// localModels is the question wiring asks once per turn: does the provider about
// to serve it run models on this machine (LM Studio, Ollama)? e.models is written
// once in New, and the service behind it is safe for concurrent readers.
func (e *Engine) localModels() bool { return e.CurrentModel().LocalModels }

func (e *Engine) SelectModel(providerID, model string) (providerconfig.Active, error) {
	if e.models == nil {
		return providerconfig.Active{}, errors.New("model selection is unavailable")
	}
	previous := e.models.Active()
	active, err := e.models.Select(context.Background(), providerID, model)
	if err != nil {
		return active, err
	}
	effort := llm.ReasoningEffort("")
	if previous.ProviderID == providerID && previous.Model == model {
		effort = e.reasoning.Effort()
	}
	if preferences, ok := e.models.(SelectionPreferences); ok {
		effort = preferences.ReasoningEffort()
	}
	if err := e.reasoning.Set(effort); err != nil {
		return active, err
	}
	return active, nil
}

func (e *Engine) ReasoningEffort() llm.ReasoningEffort {
	return e.reasoning.Effort()
}

func (e *Engine) SetReasoningEffort(effort llm.ReasoningEffort) error {
	if preferences, ok := e.models.(SelectionPreferences); ok {
		if err := preferences.SetReasoningEffort(effort); err != nil {
			return err
		}
		effort = preferences.ReasoningEffort()
	}
	return e.reasoning.Set(effort)
}

// ConnectableProviders lists the providers /connect can manage, or nil when
// the model service does not support connections.
func (e *Engine) ConnectableProviders() []providerconfig.ConnectableProvider {
	service, ok := e.models.(ConnectService)
	if !ok {
		return nil
	}
	return service.Connectable()
}

// ConnectProvider validates and stores an API key for the provider, activating
// it when nothing else is selected. Blocking: the TUI calls it from a tea.Cmd.
func (e *Engine) ConnectProvider(providerID, apiKey string) (providerconfig.Active, error) {
	service, ok := e.models.(ConnectService)
	if !ok {
		return providerconfig.Active{}, errors.New("provider connection is unavailable")
	}
	return service.Connect(e.ctx, providerID, apiKey)
}

// StartDeviceLogin mints the code the user approves elsewhere, for a provider
// whose credential is a login rather than a key. Blocking, like ConnectProvider:
// the TUI calls it from a tea.Cmd.
func (e *Engine) StartDeviceLogin(providerID string) (providerconfig.DeviceLogin, error) {
	service, ok := e.models.(ConnectService)
	if !ok {
		return providerconfig.DeviceLogin{}, errors.New("provider connection is unavailable")
	}
	return service.StartDeviceLogin(e.ctx, providerID)
}

// AwaitDeviceLogin waits for the user to approve the code. It blocks for as long
// as a human takes, so the caller runs it off the UI goroutine; e.ctx is what ends
// it when the process shuts down.
func (e *Engine) AwaitDeviceLogin(providerID string) (providerconfig.Active, error) {
	service, ok := e.models.(ConnectService)
	if !ok {
		return providerconfig.Active{}, errors.New("provider connection is unavailable")
	}
	return service.AwaitDeviceLogin(e.ctx, providerID)
}

// CancelDeviceLogin abandons the attempt the panel started, which is what closing
// it means. Idempotent, and a no-op when the provider has moved on to a newer
// attempt: a code that already resolved is not there to cancel, and one the user
// is looking at is not this caller's to retire.
func (e *Engine) CancelDeviceLogin(providerID string, attempt uint64) {
	if service, ok := e.models.(ConnectService); ok {
		service.CancelDeviceLoginAttempt(providerID, attempt)
	}
}

func (e *Engine) RefreshModels() {
	e.mu.Lock()
	if e.models == nil || e.refreshingModels {
		e.mu.Unlock()
		return
	}
	e.refreshingModels = true
	e.mu.Unlock()
	go func() {
		providers, err := e.models.Refresh(e.ctx)
		e.mu.Lock()
		e.refreshingModels = false
		e.mu.Unlock()
		msg := ModelsRefreshedMsg{Providers: providerconfig.CloneProviderModels(providers)}
		if err != nil {
			msg.Err = err.Error()
		}
		e.sendEvent(msg)
	}()
}

// Commands lists the available slash commands (name + description) for the composer's "/" menu, sorted by name (the mirror of App.ListCommands).
func (e *Engine) Commands() []command.Command {
	return e.agent.Commands()
}

func (e *Engine) SetAutoAccept(sessionID string, enabled bool) { e.autoAccept.Set(sessionID, enabled) }
func (e *Engine) AutoAcceptEnabled(sessionID string) bool      { return e.autoAccept.Enabled(sessionID) }
func (e *Engine) YoloAuthorized() bool                         { return e.yolo.Authorized() }
func (e *Engine) YoloEnabled() bool                            { return e.yolo.Enabled() }
func (e *Engine) SetYolo(enabled bool) bool                    { return e.yolo.Set(enabled) }

func localCommands(yoloAuthorized bool) []command.Command {
	commands := []command.Command{
		{Name: "help", Description: "Show keys, menus and approvals", BuiltIn: true},
		{Name: "compact", Description: "Compact conversation context", BuiltIn: true},
		{Name: "checkpoint", Description: "Save an explicit conversation and workspace checkpoint", BuiltIn: true},
		{Name: "connect", Description: "Connect a provider by API key or ChatGPT login", BuiltIn: true},
		{Name: "agents", Description: "Configure provider and model per subagent", BuiltIn: true},
		{Name: "rah", Description: "Run one prompt with Recursive Agent Harness enabled", BuiltIn: true},
		{Name: "learn", Description: "Learn from the current conversation", BuiltIn: true},
		{Name: "learned", Description: "Review learned workspace guidance", BuiltIn: true},
		{Name: "mcp", Description: "Toggle MCP servers on or off", BuiltIn: true},
		{Name: "variants", Description: llm.ReasoningCommandDescription, BuiltIn: true},
		{Name: "model", Description: "Select provider and model", BuiltIn: true},
		{Name: "new", Description: "Start a new session", BuiltIn: true},
		{Name: "mode", Description: "Show safe auto-accept mode", BuiltIn: true},
		{Name: "mode:auto-accept", Description: "Auto-accept safe workspace edits", BuiltIn: true},
		{Name: "mode:ask", Description: "Ask before workspace edits", BuiltIn: true},
		{Name: "resume", Description: "Resume a TUI session in this workspace", BuiltIn: true},
		{Name: "rewind", Description: "Rewind to the latest explicit checkpoint", BuiltIn: true},
		{Name: "skills", Description: "Select a skill", BuiltIn: true},
		{Name: "undo", Description: "Undo the last prompt and its file changes", BuiltIn: true},
	}
	if yoloAuthorized {
		commands = append(commands, command.Command{Name: "mode:yolo", Description: "Skip almost all tool permission prompts", BuiltIn: true})
	}
	return commands
}

// ProjectFiles lists the workspace files (paths relative to the root, honoring .gitignore and excluding .git) for the composer's @-menu, bounded by the glob's limit (the mirror of App.ListProjectFiles).
func (e *Engine) ProjectFiles() ([]string, error) {
	glob := e.currentGlob()
	files, _, err := glob.Files(context.Background(), "", ".", glob.MaxLimit)
	if err != nil {
		return nil, err
	}
	return append(files, e.mcp.ResourceNames()...), nil
}

// currentGlob and currentRunner read the mutable pieces under mu: rewire replaces
// them on an MCP connect or disconnect.
func (e *Engine) currentGlob() *tool.GlobTool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.glob
}

// ToolCatalog exposes the registry the runner settles calls against, so the
// presentation layer can ask a tool about itself — what a finished call may have
// changed, what granting one would authorize, how to render one — instead of
// deciding by name. Read under mu because a rewire replaces it; the value itself
// is immutable, so the caller may keep it for the length of one operation but
// should ask again for the next.
func (e *Engine) ToolCatalog() tool.Catalog {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tools
}

func (e *Engine) currentRunner() *runner.Runner {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runner
}

// PromptHistory reconstructs the last literal prompts sent from the TUI.
func (e *Engine) PromptHistory() ([]string, error) {
	ctx := context.Background()
	sessions, err := e.store.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	history := make([]string, 0, HistoryLimit)
	for _, summary := range sessions {
		if len(history) >= HistoryLimit {
			break
		}
		if !strings.HasPrefix(summary.ID, "tui-") {
			continue
		}
		events, err := e.store.Events(ctx, summary.ID, 0)
		if err != nil {
			return nil, err
		}
		prompts := make([]string, 0)
		foundComposerPrompts := false
		for _, event := range events {
			if event.Kind == session.KindComposerPrompt {
				foundComposerPrompts = true
				prompts = append(prompts, event.Text)
			}
		}
		if !foundComposerPrompts {
			messages, err := e.store.Messages(ctx, summary.ID, 0)
			if err != nil {
				return nil, err
			}
			for _, message := range messages {
				if message.Role == session.RoleUser && message.Text != agent.AcceptPlanPrompt {
					prompts = append(prompts, message.Text)
				}
			}
		}
		history = append(prompts, history...)
	}
	if len(history) > HistoryLimit {
		history = history[len(history)-HistoryLimit:]
	}
	return history, nil
}

// NewSessionID reserves a fresh session ID. Every launch starts with an empty
// conversation; previous sessions stay reachable through /resume.
func (e *Engine) NewSessionID() string {
	return "tui-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// ListResumeSessions returns the resumable TUI sessions of the current workspace, in
// the same recency order the store delivers.
func (e *Engine) ListResumeSessions(currentSessionID string) ([]session.SessionSummary, error) {
	e.resumeMu.Lock()
	defer e.resumeMu.Unlock()
	return e.listResumeSessionsUnlocked(currentSessionID)
}

func (e *Engine) listResumeSessionsUnlocked(currentSessionID string) ([]session.SessionSummary, error) {
	if e.agent.Running(currentSessionID) {
		return nil, ErrResumeActiveRun
	}
	return e.resumeSessions(context.Background())
}

func (e *Engine) resumeSessions(ctx context.Context) ([]session.SessionSummary, error) {
	summaries, err := e.store.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	root, err := workspaceDirectoryInfo(e.root)
	if err != nil {
		return nil, err
	}
	out := make([]session.SessionSummary, 0, len(summaries))
	for _, summary := range summaries {
		if !strings.HasPrefix(summary.ID, "tui-") || summary.Cwd == "" {
			continue
		}
		cwd, err := workspaceDirectoryInfo(summary.Cwd)
		if err == nil && os.SameFile(root, cwd) {
			out = append(out, summary)
		}
	}
	return out, nil
}

func workspaceDirectoryInfo(path string) (os.FileInfo, error) {
	if path == "" {
		return nil, errors.New("workspace path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("workspace path is not a directory")
	}
	return info, nil
}

// ResumeSessionByID loads exactly one resumable session of the workspace and persists
// the restored mode as the session's mode in force.
func (e *Engine) ResumeSessionByID(currentSessionID, targetSessionID string) (ResumeResult, error) {
	e.resumeMu.Lock()
	defer e.resumeMu.Unlock()
	return e.resumeSessionByIDUnlocked(currentSessionID, targetSessionID)
}

func (e *Engine) resumeSessionByIDUnlocked(currentSessionID, targetSessionID string) (ResumeResult, error) {
	if e.agent.Running(currentSessionID) || e.agent.Running(targetSessionID) {
		return ResumeResult{}, ErrResumeActiveRun
	}
	summaries, err := e.listResumeSessionsUnlocked(currentSessionID)
	if err != nil {
		return ResumeResult{}, err
	}
	allowed := false
	for _, summary := range summaries {
		if summary.ID == targetSessionID {
			allowed = true
			break
		}
	}
	if !allowed {
		return ResumeResult{}, ErrSessionNotResumable
	}
	events, err := e.store.Events(context.Background(), targetSessionID, 0)
	if err != nil {
		return ResumeResult{}, err
	}
	history := resumeHistory(events)
	mode := modeFromEvents(events)
	if _, err := e.store.AppendEvent(context.Background(), targetSessionID,
		session.SessionEvent{Kind: session.KindSessionMode, Text: string(mode)}); err != nil {
		return ResumeResult{}, err
	}
	return ResumeResult{SessionID: targetSessionID, Events: events, Mode: mode, History: history}, nil
}

func resumeHistory(events []session.SessionEvent) []string {
	history := make([]string, 0, HistoryLimit)
	pendingMarkers := make([]string, 0)
	for _, event := range events {
		if event.Kind == session.KindComposerPrompt {
			pendingMarkers = append(pendingMarkers, event.Text)
			continue
		}
		if event.Message == nil || event.Message.Role != session.RoleUser || event.Message.Text == agent.AcceptPlanPrompt {
			continue
		}
		text := event.Message.Text
		if index := slices.Index(pendingMarkers, text); index >= 0 {
			history = append(history, pendingMarkers[:index+1]...)
			pendingMarkers = pendingMarkers[index+1:]
			continue
		}
		history = append(history, text)
	}
	history = append(history, pendingMarkers...)
	if len(history) > HistoryLimit {
		history = history[len(history)-HistoryLimit:]
	}
	return history
}

func modeFromEvents(events []session.SessionEvent) session.Mode {
	mode := session.ModeNormal
	for _, event := range events {
		if event.Kind != session.KindSessionMode {
			continue
		}
		switch session.Mode(event.Text) {
		case session.ModeNormal:
			mode = session.ModeNormal
		case session.ModePlan:
			mode = session.ModePlan
		// RAH is intentionally one-turn and is never restored on resume.
		case session.ModeRAH:
			mode = session.ModeNormal
		}
	}
	return mode
}

// SendPrompt queues a user prompt through the normal path and returns the active
// session along with the runID. For exactly "/new" it creates and returns a new
// durable session with no run; in every other case it keeps sessionID. It sets normal
// mode first: a session that was in plan mode goes back to the normal tools on
// send.
func (e *Engine) SendPrompt(sessionID string, prompt session.Prompt) (RunHandle, error) {
	e.resumeMu.Lock()
	defer e.resumeMu.Unlock()

	if prompt.Text == "/new" {
		// A run still streaming into the old session would keep appending
		// durable events after the new session is created, leaving the old
		// session with the latest activity: on restart, ResumeSession would
		// bring the old conversation back. Stop it and wait for its completion
		// hook so the new session ends up as the most recent one.
		if run, ok := e.agent.Stop(sessionID); ok {
			<-run.Done()
		}
		newSessionID := e.NewSessionID()
		_, err := e.store.AppendEvent(context.Background(), newSessionID,
			session.SessionEvent{Kind: session.KindSessionCwd, Text: e.root})
		if err != nil {
			return RunHandle{}, err
		}
		return RunHandle{SessionID: newSessionID}, nil
	}
	if prompt.Text == "/compact" {
		e.requestCompaction(sessionID)
		return RunHandle{SessionID: sessionID}, nil
	}
	run, err := e.agent.Send(sessionID, prompt, e.turnHooks(sessionID, prompt, session.ModeNormal))
	if err != nil {
		return RunHandle{}, err
	}
	return RunHandle{SessionID: sessionID, RunID: run.ID}, nil
}

// SendRAHPrompt runs one explicitly activated recursive-harness turn. The local
// /rah command is the only interactive path that calls it.
func (e *Engine) SendRAHPrompt(sessionID string, prompt session.Prompt) (RunHandle, error) {
	e.resumeMu.Lock()
	defer e.resumeMu.Unlock()

	e.agent.SetMode(sessionID, session.ModeRAH)
	run, err := e.agent.SendRAH(sessionID, prompt, e.turnHooks(sessionID, prompt, session.ModeRAH))
	if err != nil {
		e.agent.SetMode(sessionID, session.ModeNormal)
		return RunHandle{}, err
	}
	return RunHandle{SessionID: sessionID, RunID: run.ID}, nil
}

// RetryPrompt reruns the failed turn without adding a duplicate user message.
func (e *Engine) RetryPrompt(sessionID string) (RunHandle, error) {
	run, err := e.agent.Retry(sessionID, e.turnHooks(sessionID, session.Prompt{}, e.agent.Mode(sessionID)))
	return RunHandle{SessionID: sessionID, RunID: run.ID}, err
}

// SendPlanPrompt queues the prompt in plan mode: read-only research plus
// present_plan, with the plan-mode contract in the system prompt. It sets ModePlan
// before starting (the mirror of App.SendPlanPrompt).
func (e *Engine) SendPlanPrompt(sessionID string, prompt session.Prompt) (RunHandle, error) {
	e.resumeMu.Lock()
	defer e.resumeMu.Unlock()

	run, err := e.agent.SendPlan(sessionID, prompt, e.turnHooks(sessionID, prompt, session.ModePlan))
	return RunHandle{SessionID: sessionID, RunID: run.ID}, err
}

// turnHooks keeps the responsibilities that are the TUI's alone around the shared
// lifecycle: CWD, checkpoints, the literal history, RunDoneMsg and compaction.
func (e *Engine) turnHooks(sessionID string, composerPrompt session.Prompt, mode session.Mode) agent.Hooks {
	checkpointID := ""
	return agent.Hooks{
		BeforeAdmit: func() error {
			var before checkpoint.Tree
			if composerPrompt.Text != "" && e.checkpoints != nil {
				var err error
				before, err = e.checkpoints.Capture(context.Background(), e.root)
				if err != nil && !errors.Is(err, checkpoint.ErrGitWorkspace) {
					return err
				}
			}
			if _, err := e.store.LoadSession(context.Background(), sessionID); err != nil {
				if _, err := e.store.AppendEvent(context.Background(), sessionID,
					session.SessionEvent{Kind: session.KindSessionCwd, Text: e.root}); err != nil {
					log.Printf("atenea: could not save the folder of %s: %v", sessionID, err)
				}
			}
			if _, err := e.store.AppendEvent(context.Background(), sessionID,
				session.SessionEvent{Kind: session.KindSessionMode, Text: string(mode)}); err != nil {
				return err
			}
			if before != "" {
				checkpointID = "checkpoint-" + strconv.FormatInt(time.Now().UnixNano(), 10)
				if _, err := e.store.AppendEvent(context.Background(), sessionID, session.SessionEvent{
					Kind:       session.KindPromptCheckpointStarted,
					Checkpoint: &session.PromptCheckpoint{ID: checkpointID, Prompt: composerPrompt.Text, PromptImages: composerPrompt.Images, BeforeTree: string(before)},
				}); err != nil {
					return err
				}
			}
			return nil
		},
		AfterAdmit: func() {
			if composerPrompt.Text == "" {
				return
			}
			if _, err := e.store.AppendEvent(context.Background(), sessionID,
				session.SessionEvent{Kind: session.KindComposerPrompt, Text: composerPrompt.Text}); err != nil {
				log.Printf("atenea: could not save the prompt in the history of %s: %v", sessionID, err)
			}
		},
		AfterRun: func(result agent.RunResult) {
			if result.Mode == session.ModeRAH && result.Current {
				e.agent.SetMode(sessionID, session.ModeNormal)
			}
			err := result.Err
			if checkpointID != "" {
				after, captureErr := e.checkpoints.Capture(context.Background(), e.root)
				if captureErr == nil {
					_, captureErr = e.store.AppendEvent(context.Background(), sessionID, session.SessionEvent{
						Kind:       session.KindPromptCheckpointFinished,
						Checkpoint: &session.PromptCheckpoint{ID: checkpointID, AfterTree: string(after)},
					})
				}
				if err == nil {
					err = captureErr
				}
			}
			if e.takePendingRewind(sessionID) {
				_, rewindErr := e.rewind(sessionID)
				if err == nil {
					err = rewindErr
				}
			}
			compact := e.finishRun(sessionID, result.Current)
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			e.sendEvent(RunDoneMsg{SessionID: sessionID, RunID: result.ID, Err: msg})
			if compact {
				e.startCompaction(sessionID)
			}
		},
	}
}

func (e *Engine) Checkpoint(sessionID string) (CheckpointResult, error) {
	if e.checkpoints == nil {
		return CheckpointResult{}, session.ErrNothingToUndo
	}
	if run, ok := e.agent.Stop(sessionID); ok {
		<-run.Done()
	}
	var result CheckpointResult
	err := e.agent.Synchronize(sessionID, func() error {
		id, err := e.createCheckpoint(sessionID, "")
		result.ID = id
		return err
	})
	return result, err
}

func (e *Engine) Rewind(sessionID string) (RewindResult, error) {
	if e.checkpoints == nil {
		return RewindResult{}, session.ErrNothingToUndo
	}
	if run, ok := e.agent.Stop(sessionID); ok {
		<-run.Done()
	}
	var result RewindResult
	err := e.agent.Synchronize(sessionID, func() error {
		var err error
		result, err = e.rewind(sessionID)
		return err
	})
	return result, err
}

func (e *Engine) createCheckpoint(sessionID, originCallID string) (string, error) {
	if e.checkpoints == nil {
		return "", session.ErrNothingToUndo
	}
	if _, err := e.store.LoadSession(context.Background(), sessionID); err != nil {
		if !errors.Is(err, session.ErrSessionNotFound) {
			return "", err
		}
		if _, err := e.store.AppendEvent(context.Background(), sessionID, session.SessionEvent{Kind: session.KindSessionCwd, Text: e.root}); err != nil {
			return "", err
		}
	}
	tree, err := e.checkpoints.Capture(context.Background(), e.root)
	if err != nil {
		return "", err
	}
	id := session.ExplicitCheckpointID(strconv.FormatInt(time.Now().UnixNano(), 10))
	_, err = e.store.AppendEvent(context.Background(), sessionID, session.SessionEvent{
		Kind: session.KindPromptCheckpointStarted, Checkpoint: &session.PromptCheckpoint{ID: id, BeforeTree: string(tree), OriginCallID: originCallID},
	})
	return id, err
}
func (e *Engine) checkpointFromTool(sessionID, callID string) (string, error) {
	return e.createCheckpoint(sessionID, callID)
}

func (e *Engine) rewindFromTool(sessionID string) (string, error) {
	events, err := e.store.Events(context.Background(), sessionID, 0)
	if err != nil {
		return "", err
	}
	boundary, err := session.LatestExplicitCheckpoint(events)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	e.pendingRewinds[sessionID] = true
	e.mu.Unlock()
	return boundary.ID, nil
}

func (e *Engine) takePendingRewind(sessionID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	pending := e.pendingRewinds[sessionID]
	delete(e.pendingRewinds, sessionID)
	return pending
}

func (e *Engine) rewind(sessionID string) (RewindResult, error) {
	events, err := e.store.Events(context.Background(), sessionID, 0)
	if err != nil {
		return RewindResult{}, err
	}
	boundary, err := session.LatestExplicitCheckpoint(events)
	if err != nil {
		return RewindResult{}, err
	}
	current, err := e.checkpoints.Capture(context.Background(), e.root)
	if err != nil {
		return RewindResult{}, err
	}
	if err := e.checkpoints.Restore(context.Background(), e.root, checkpoint.Tree(boundary.BeforeTree)); err != nil {
		return RewindResult{}, err
	}
	if _, err := e.store.AppendEvent(context.Background(), sessionID, session.SessionEvent{
		Kind: session.KindPromptCheckpointReverted, Checkpoint: &session.PromptCheckpoint{ID: boundary.ID},
	}); err != nil {
		if restoreErr := e.checkpoints.Restore(context.Background(), e.root, current); restoreErr != nil {
			return RewindResult{}, errors.Join(err, restoreErr)
		}
		return RewindResult{}, err
	}
	events, err = e.store.Events(context.Background(), sessionID, 0)
	if err != nil {
		return RewindResult{}, err
	}
	return RewindResult{CheckpointID: boundary.ID, Events: events}, nil
}

func (e *Engine) Undo(sessionID string) (UndoResult, error) {
	if e.undoStore == nil || e.checkpoints == nil {
		return UndoResult{}, session.ErrNothingToUndo
	}
	if run, ok := e.agent.Stop(sessionID); ok {
		<-run.Done()
	}
	var result UndoResult
	err := e.agent.Synchronize(sessionID, func() error {
		boundary, err := e.undoStore.LatestPromptCheckpoint(context.Background(), sessionID)
		if err != nil {
			return err
		}
		if boundary.AfterTree != "" {
			current, err := e.checkpoints.Capture(context.Background(), e.root)
			if err != nil {
				return err
			}
			if string(current) != boundary.AfterTree {
				return ErrWorkspaceDiverged
			}
		}
		if err := e.checkpoints.Restore(context.Background(), e.root, checkpoint.Tree(boundary.BeforeTree)); err != nil {
			return err
		}
		if _, err := e.store.AppendEvent(context.Background(), sessionID, session.SessionEvent{
			Kind:       session.KindPromptCheckpointReverted,
			Checkpoint: &session.PromptCheckpoint{ID: boundary.ID},
		}); err != nil {
			if boundary.AfterTree != "" {
				if restoreErr := e.checkpoints.Restore(context.Background(), e.root, checkpoint.Tree(boundary.AfterTree)); restoreErr != nil {
					return errors.Join(err, restoreErr)
				}
			}
			return err
		}
		events, err := e.store.Events(context.Background(), sessionID, 0)
		if err != nil {
			return err
		}
		result = UndoResult{Prompt: session.Prompt{Text: boundary.Prompt, Images: boundary.PromptImages}, Events: events}
		return nil
	})
	return result, err
}

// AcceptPlan accepts and executes the plan: it returns the session to normal mode and
// promotes the fixed implementation prompt as the user's prompt, starting the run (the
// mirror of App.AcceptPlan).
func (e *Engine) AcceptPlan(sessionID string) (RunHandle, error) {
	e.resumeMu.Lock()
	defer e.resumeMu.Unlock()

	run, err := e.agent.AcceptPlan(sessionID, e.turnHooks(sessionID, session.Prompt{}, session.ModeNormal))
	return RunHandle{SessionID: sessionID, RunID: run.ID}, err
}

// ResolvePermission settles a pending ask-before-run request with the user's
// verdict. AllowedSession also records the grant — derived from the request the
// gate is blocking on, not from what the UI holds — BEFORE releasing the call,
// so the policy already sees it when the next call of the same shape arrives.
func (e *Engine) ResolvePermission(sessionID, callID string, verdict permission.Verdict) {
	if verdict == permission.AllowedSession {
		if request, ok := e.gate.Pending(sessionID, callID); ok {
			call := tool.Call{ID: callID, Name: request.ToolName, Input: request.Input}
			if rule, grantable := permission.RuleFor(e.ToolCatalog(), call); grantable {
				e.grants.Grant(sessionID, rule)
			}
		}
	}
	e.gate.Resolve(sessionID, callID, verdict.Approved())
}

// Learn captures the durable session through its current cut and queues an
// independent learning extraction for this workspace.
func (e *Engine) Learn(sessionID string) (learning.Run, error) {
	return e.learning.Enqueue(e.ctx, e.root, sessionID)
}

// LearningAudit returns both the extraction audit and approved lesson catalog
// from one workspace snapshot request.
func (e *Engine) LearningAudit() ([]learning.Run, []learning.Lesson, error) {
	runs, err := e.learning.Audit(e.ctx, e.root)
	if err != nil {
		return nil, nil, err
	}
	lessons, err := e.learning.Lessons(e.ctx, e.root)
	return runs, lessons, err
}

func (e *Engine) ApproveLearning(runID string, candidate learning.Candidate) (learning.Lesson, error) {
	return e.learning.Approve(e.ctx, runID, candidate)
}

func (e *Engine) RejectLearning(runID string) error {
	return e.learning.Reject(e.ctx, runID)
}

func (e *Engine) CancelLearning(runID string) error {
	return e.learning.Cancel(e.ctx, runID)
}

func (e *Engine) RetryLearning(runID string) (learning.Run, error) {
	return e.learning.Retry(e.ctx, runID)
}

func (e *Engine) SetLessonEnabled(lessonID string, enabled bool) error {
	return e.learning.SetLessonEnabled(e.ctx, lessonID, enabled)
}

func (e *Engine) DeleteLesson(lessonID string) error {
	return e.learning.DeleteLesson(e.ctx, lessonID)
}

// Stop interrupts the session's run in progress. No-op if none is running.
func (e *Engine) Stop(sessionID string) {
	e.agent.Stop(sessionID)
}

// Shutdown cancels background work and waits until runs and compactions have
// stopped, so the caller can safely close the shared store afterwards.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.shutdownOnce.Do(func() {
		e.lifecycleMu.Lock()
		e.shuttingDown = true
		e.cancel()
		e.agent.StopAll()
		e.taskSupervisor.Close()
		e.lifecycleMu.Unlock()
		go func() {
			e.agent.Wait()
			e.compactions.Wait()
			e.learning.Close()
			// With the runs already stopped, closing the MCPs kills their subprocesses.
			e.mcp.Close()
			e.mu.Lock()
			e.assembly.Close()
			e.assembly = wiring.Built{}
			e.mu.Unlock()
			close(e.shutdownDone)
		}()
	})
	select {
	case <-e.shutdownDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finishRun clears the session's compaction claim only when the finished run was
// still the current one (a newer SendPrompt may have replaced it).
func (e *Engine) finishRun(sessionID string, current bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !current {
		return false
	}
	if !e.pendingCompactions[sessionID] || e.compacting[sessionID] {
		return false
	}
	delete(e.pendingCompactions, sessionID)
	e.compacting[sessionID] = true
	return true
}

func (e *Engine) requestCompaction(sessionID string) {
	e.mu.Lock()
	if e.pendingCompactions[sessionID] || e.compacting[sessionID] {
		e.mu.Unlock()
		return
	}
	if e.agent.Running(sessionID) {
		e.pendingCompactions[sessionID] = true
		e.mu.Unlock()
		e.sendEvent(CompactionStatusMsg{SessionID: sessionID, State: CompactionQueued})
		return
	}
	e.compacting[sessionID] = true
	e.mu.Unlock()

	e.startCompaction(sessionID)
}

func (e *Engine) startCompaction(sessionID string) {
	e.lifecycleMu.Lock()
	if e.shuttingDown {
		e.lifecycleMu.Unlock()
		// Release the compacting slot we claimed in requestCompaction; otherwise
		// the key leaks and every later requestCompaction for this session is a
		// silent no-op against the guard.
		e.mu.Lock()
		delete(e.compacting, sessionID)
		e.mu.Unlock()
		return
	}
	e.compactions.Add(1)
	e.lifecycleMu.Unlock()
	go func() {
		defer e.compactions.Done()
		_ = e.agent.Synchronize(sessionID, func() error {
			e.compactLocked(sessionID)
			return nil
		})
	}()
}

func (e *Engine) compactLocked(sessionID string) {
	e.sendEvent(CompactionStatusMsg{SessionID: sessionID, State: CompactionRunning})
	err := e.currentRunner().CompactNow(e.ctx, sessionID)
	switch {
	case errors.Is(err, session.ErrNoCompactableHistory):
		e.sendEvent(CompactionStatusMsg{SessionID: sessionID, State: CompactionNotNeeded})
	case err != nil:
		e.sendEvent(CompactionStatusMsg{SessionID: sessionID, State: CompactionFailed, Err: err.Error()})
	}
	e.mu.Lock()
	delete(e.compacting, sessionID)
	e.mu.Unlock()
}

func (e *Engine) sendEvent(msg tea.Msg) {
	select {
	case <-e.ctx.Done():
		return
	default:
	}
	select {
	case e.events <- msg:
	case <-e.ctx.Done():
	}
}

// Events delivers the run's durable EventMsgs and one RunDoneMsg as each run
// finishes.
func (e *Engine) Events() <-chan tea.Msg { return e.events }
