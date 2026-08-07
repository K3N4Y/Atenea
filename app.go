package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/K3N4Y/atenea/internal/agent"
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
	"github.com/K3N4Y/atenea/internal/terminal"
	"github.com/K3N4Y/atenea/internal/wailssession"
	"github.com/K3N4Y/atenea/internal/wailsworkspace"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ProviderEntry is one row of the model picker: a provider the user has
// configured, the models it offers, and what the UI can do about it. It mirrors
// providerconfig's catalog rather than adding a model of its own — the shape the
// frontend needs is a projection, which is this adapter's whole job.
type ProviderEntry struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Models []string `json:"models"`
	// BuiltIn marks the providers that ship with atenea, which are the ones the UI
	// must not offer to remove: they would be back at the next launch.
	BuiltIn bool `json:"builtIn"`
	// Connectable is whether a credential can be stored for this provider from
	// here. Connected is whether one already is.
	Connectable bool `json:"connectable"`
	Connected   bool `json:"connected"`
	// ConnectKind is HOW it is connected: "api_key" takes a pasted secret,
	// "device_code" takes a code approved elsewhere. The UI reads it instead of
	// inferring from the id, so a catalog change cannot leave it drawing a password
	// field for a provider that has no password.
	ConnectKind string `json:"connectKind"`
}

// DeviceLogin is a pending device-code login as the settings panel shows it: what
// to open, what to type there, and when the code dies.
//
// ExpiresAt is RFC3339, and the panel renders it as the clock time the code stops
// being approvable — the same sentence the terminal writes, so a user with both
// open is not told two different things about one code. It is empty when the
// authorization server named no expiry, and then neither host says anything: a
// deadline invented downstream would be worse than silence.
type DeviceLogin struct {
	ProviderID      string `json:"providerID"`
	ProviderName    string `json:"providerName"`
	UserCode        string `json:"userCode"`
	VerificationURI string `json:"verificationURI"`
	ExpiresAt       string `json:"expiresAt"`
}

// ActiveProvider is the selection the UI shows and sizes the context bar with.
// ContextWindow is what the active adapter declares for this model, and 0 means
// it declares none — the UI shows tokens without a percentage rather than
// scaling against a number nobody vouched for.
type ActiveProvider struct {
	ProviderID    string `json:"providerID"`
	ProviderName  string `json:"providerName"`
	Model         string `json:"model"`
	ContextWindow int    `json:"contextWindow"`
}

// App wires the agent loop (M1..M8) to the Wails app: it starts and cancels Run
// from the frontend and forwards the durable log over the Bus. The loop's logic
// does not change.
type App struct {
	ctx             context.Context // Wails ctx, set by startup. Only the real EmitFunc reads it.
	inbox           session.Inbox
	bus             *event.Bus
	emit            event.EmitFunc          // the same boundary the bus uses; the Terminal tab pushes its output through it
	gate            *permission.MemoryGate  // ask-before-run: the UI resolves via ResolveToolPermission
	agent           *agent.Service          // the headless turn lifecycle shared with the TUI
	providers       *providerconfig.Service // the same catalog, credentials and selection the TUI holds
	workspace       *wailsworkspace.Manager // root, wiring, glob and MCP published as one serialized configuration
	sessions        *wailssession.Manager   // durable history, initial metadata, titles and deletion
	learning        *learning.Service
	learningInitErr error

	term *terminal.Manager // the Terminal tabs: several live pty sessions, one per id
}

// newAppWithHost assembles the app over an already-assembled host and the
// injected boundary (emit). The host owns everything above the wiring — the
// store, the provider service and the sitting — so this function is only the
// Wails-shaped part of the assembly: decorating the store with EmittingStore
// (Store -> UI bridge) and handing the workspace manager what it rebuilds with.
func newAppWithHost(h *host.Host, emit event.EmitFunc) *App {
	a := &App{providers: h.Providers, gate: h.Gate, inbox: h.Inbox, agent: h.Agent}
	// The data_version watcher is anchored to the RAW store (before decorating it):
	// only the file-backed SQLiteStore can expose it; a MemoryStore cannot, and then
	// the app runs without a watcher (nothing else can be writing to memory).
	var versioner event.DataVersioner
	if v, ok := h.Store.(event.DataVersioner); ok {
		versioner = v
	}
	a.emit = emit
	a.bus = event.NewBus(emit)
	a.term = terminal.NewManager()
	emitting := event.NewEmittingStore(h.Store, a.bus)
	a.learning = learning.NewService(context.Background(), learning.NewMemoryStore(), h.Store, h.Providers.Provider(), func(workspace string) {
		if a.emit != nil {
			a.emit("learning:changed", workspace)
		}
	})
	a.workspace = wailsworkspace.New(wailsworkspace.Config{
		Root: h.Root, Identity: h.Identity,
		// The switchable handle is stable: choosing another model changes what it
		// delegates to, not what is wired, so the rebuild exists only to cut the runs
		// that came from the previous model.
		Provider:    a.providers.Provider(),
		LocalPrompt: func() bool { return a.providers.Active().LocalModels },
		Store:       emitting, Bus: a.bus, Sitting: h.Sitting,
		LessonSection: func(workspace, latestPrompt string) string {
			lessons, err := a.learning.Lessons(context.Background(), workspace)
			if err != nil {
				return ""
			}
			return learning.RenderLessons(learning.Select(latestPrompt, lessons))
		},
	})
	a.sessions = wailssession.New(wailssession.Config{
		Store: emitting, Root: a.workspace.Root, Forget: a.agent.Forget,
		Versioner: versioner, Emit: emit, WatchPeriod: time.Second,
	})
	return a
}

// NewApp assembles the production app over the real host. The EmitFunc closes
// over a so it can read a.ctx (which startup sets afterwards): emitting before
// startup passes a nil ctx, but the UI does not call SendPrompt before it loads.
func NewApp(h *host.Host) *App {
	var a *App
	emit := func(name string, data ...interface{}) {
		runtime.EventsEmit(a.ctx, name, data...)
	}
	a = newAppWithHost(h, emit)
	// Production replaces the in-memory test store with private durable state.
	if path, err := paths.Learning(); err == nil {
		if store, openErr := learning.OpenFileStore(path); openErr == nil {
			a.learning.Close()
			a.learning = learning.NewService(context.Background(), store, h.Store, h.Providers.Provider(), func(workspace string) { emit("learning:changed", workspace) })
		} else {
			a.learningInitErr = fmt.Errorf("open durable learning store: %w", openErr)
		}
	} else {
		a.learningInitErr = fmt.Errorf("resolve durable learning store: %w", err)
	}
	// Auto-title: the first message of each session is summarized with the real
	// provider. Production only; tests leave titler nil so a send does not double the
	// calls to the provider. It reads the provider and model in force (they can change
	// live) rather than capturing them.
	a.sessions.SetTitler(wailssession.ProviderTitler(func() (llm.Provider, string) {
		return a.currentProvider(), a.currentModel()
	}))
	return a
}

// ProviderCatalog lists the configured providers with their models and credential
// state: it is what the settings panel's model picker paints.
func (a *App) ProviderCatalog() []ProviderEntry {
	return a.providerEntries(a.providers.Catalog())
}

// RefreshModels re-asks every endpoint that supports discovery for its model
// catalog and returns the result. The error is a warning: the endpoints that did
// answer are already in the response.
func (a *App) RefreshModels() ([]ProviderEntry, error) {
	providers, err := a.providers.Refresh(context.Background())
	return a.providerEntries(providers), err
}

// providerEntries projects the catalog onto the picker, crossed with what /connect
// can manage: the catalog knows about models and the credential store knows about
// keys, and a row needs both.
func (a *App) providerEntries(providers []providerconfig.ProviderModels) []ProviderEntry {
	connectable := map[string]providerconfig.ConnectableProvider{}
	for _, provider := range a.providers.Connectable() {
		connectable[provider.ID] = provider
	}
	entries := make([]ProviderEntry, 0, len(providers))
	for _, provider := range providers {
		connection, isConnectable := connectable[provider.ID]
		entries = append(entries, ProviderEntry{
			ID: provider.ID, Name: provider.Name, Models: provider.Models,
			BuiltIn: provider.BuiltIn, Connectable: isConnectable,
			Connected: connection.Connected, ConnectKind: connection.Kind,
		})
	}
	return entries
}

// ActiveProvider exposes the selection in force and the context window the adapter
// declares for that model, so the UI can size the context bar without keeping a
// window table of its own.
func (a *App) ActiveProvider() ActiveProvider {
	active := a.providers.Active()
	window := 0
	if capabilities, ok := llm.ActiveCapabilities(a.providers.Provider()); ok {
		window, _ = capabilities.ContextWindow(active.Model)
	}
	return ActiveProvider{ProviderID: active.ProviderID, ProviderName: active.ProviderName, Model: active.Model, ContextWindow: window}
}

// SelectModel changes the active provider/model live: it rebuilds the adapter, cuts
// the runs in flight (they came from the previous model) and persists the selection
// in the shared providers.json. It is the binding the frontend calls when a model is
// chosen in the picker.
func (a *App) SelectModel(providerID, model string) error {
	return a.workspace.Reconfigure(func() error {
		_, err := a.providers.Select(context.Background(), providerID, model)
		return err
	})
}

// ConnectProvider validates an API key against the provider and stores it in the
// credential store shared with the TUI. With nothing selected yet, it leaves the
// provider active on its first model.
func (a *App) ConnectProvider(providerID, apiKey string) error {
	return a.workspace.Reconfigure(func() error {
		_, err := a.providers.Connect(context.Background(), providerID, apiKey)
		return err
	})
}

// StartProviderLogin begins a device-code login and returns the code to show. It
// is the binding the settings panel calls for a provider whose credential is a
// login: there is no key to paste, so the UI's job is to show where to go and what
// to type there.
//
// It does not wait. Painting the code and waiting for the user are two calls
// because a binding that did both would hold the frontend for as long as a human
// takes, and the code would arrive with the answer.
func (a *App) StartProviderLogin(providerID string) (DeviceLogin, error) {
	login, err := a.providers.StartDeviceLogin(context.Background(), providerID)
	if err != nil {
		return DeviceLogin{}, err
	}
	return deviceLogin(login), nil
}

// AwaitProviderLogin waits for the user to approve the code and then rebuilds the
// workspace, which is what cuts the runs that came from the previous credential.
// The wait itself happens outside Reconfigure: a human takes minutes, and holding
// the workspace lock for one would freeze every turn in the window.
//
// A cancelled login comes back as no error at all. The settings panel paints
// whatever this rejects with in red, and the user pressing Cancel is not something
// to report back to them as a failure.
func (a *App) AwaitProviderLogin(providerID string) error {
	if _, err := a.providers.AwaitDeviceLogin(context.Background(), providerID); err != nil {
		if errors.Is(err, providerconfig.ErrLoginCancelled) {
			return nil
		}
		return err
	}
	return a.workspace.Reconfigure(func() error { return nil })
}

// CancelProviderLogin abandons a pending login — what closing the form means.
// Idempotent, so the UI may call it on its way out without checking.
func (a *App) CancelProviderLogin(providerID string) {
	a.providers.CancelDeviceLogin(providerID)
}

// OpenLoginPage opens the verification page of a pending login in the user's
// browser. It is an affordance and nothing depends on it: the code and the URL are
// on screen, which is the whole point of the device flow — the machine running
// atenea may have no browser at all.
//
// It takes a provider id rather than a URL so the frontend cannot ask this process
// to open somewhere the authorization server did not name.
func (a *App) OpenLoginPage(providerID string) error {
	login, ok := a.providers.PendingDeviceLogin(providerID)
	if !ok {
		return providerconfig.ErrNoPendingLogin
	}
	runtime.BrowserOpenURL(a.ctx, login.VerificationURI)
	return nil
}

func deviceLogin(login providerconfig.DeviceLogin) DeviceLogin {
	out := DeviceLogin{
		ProviderID:      login.ProviderID,
		ProviderName:    login.ProviderName,
		UserCode:        login.UserCode,
		VerificationURI: login.VerificationURI,
	}
	if !login.ExpiresAt.IsZero() {
		out.ExpiresAt = login.ExpiresAt.Format(time.RFC3339)
	}
	return out
}

// DeclareEndpoint adds a user's OpenAI-compatible endpoint (LM Studio, Ollama) to
// the shared providers.json and returns its id. It does not activate it: choosing it
// is SelectModel. model is optional — without it the endpoint is ready for
// RefreshModels to discover its catalog.
func (a *App) DeclareEndpoint(name, baseURL, model string) (string, error) {
	id := endpointID(name)
	if id == "" {
		return "", fmt.Errorf("endpoint name %q has no letters or digits to build an id from", name)
	}
	endpoint := providerconfig.Provider{
		ID:      id,
		Name:    strings.TrimSpace(name),
		Type:    providerconfig.OpenAICompatible,
		BaseURL: strings.TrimSpace(baseURL),
		// A user's endpoint runs local models: their ids are arbitrary, so the prompt
		// cannot be routed by model family.
		LocalModels: true,
	}
	if model = strings.TrimSpace(model); model != "" {
		endpoint.Models = []string{model}
	}
	if err := a.providers.Declare(endpoint); err != nil {
		return "", err
	}
	return id, nil
}

// ForgetProvider removes a user-declared endpoint from providers.json.
func (a *App) ForgetProvider(providerID string) error {
	return a.providers.Forget(providerID)
}

// endpointID derives a stable id from the name the user typed, so the form asks for
// one thing instead of two. Anything that is not a letter or a digit becomes a dash,
// and the dashes at either end are dropped.
func endpointID(name string) string {
	var id strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			id.WriteRune(r)
		default:
			id.WriteRune('-')
		}
	}
	return strings.Trim(id.String(), "-")
}

// ListModels returns the catalog of an OpenAI-compatible endpoint (GET
// baseURL/models) BEFORE it is declared, so the "add endpoint" form can offer the
// models that are there instead of asking the user to recall them. No secret: a
// local endpoint requires no key.
func (a *App) ListModels(baseURL string) ([]string, error) {
	return llm.ListModels(context.Background(), baseURL, "")
}

// currentProvider is the switchable handle: it always delegates to the adapter in
// force.
func (a *App) currentProvider() llm.Provider { return a.providers.Provider() }

// currentModel is the model of the selection in force.
func (a *App) currentModel() string { return a.providers.Active().Model }

// startup keeps the Wails ctx (the real EmitFunc reads it) and, when the store
// exposes its data_version, starts the watcher that emits "sessions:changed" as soon
// as ANOTHER process (the TUI) writes new or updated sessions into the shared SQLite;
// the sidebar re-asks ListSessions on it. The Wails ctx is cancelled when the app
// closes, which shuts the watcher down.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if err := a.learning.Recover(context.Background()); err != nil {
		a.learningInitErr = fmt.Errorf("recover durable learning runs: %w", err)
	}
	a.sessions.Watch(ctx)
}

// SendPrompt admits the text as a queued prompt and starts Run in a goroutine. It is
// the binding the frontend calls on send. It sets normal mode first: a session that
// was in plan mode goes back to the normal tools when a prompt is sent.
func (a *App) SendPrompt(sessionID, text string) error {
	turn := a.sessions.Turn(sessionID, text)
	return a.workspace.Admit(func() error {
		_, err := a.agent.Send(sessionID, session.Prompt{Text: text}, a.turnHooks(sessionID, turn))
		return err
	})
}

// QueueLearning captures a stable durable session cut and returns before the
// independent provider request completes.
func (a *App) QueueLearning(sessionID string) (learning.Run, error) {
	if a.learningInitErr != nil {
		return learning.Run{}, a.learningInitErr
	}
	return a.learning.Enqueue(context.Background(), a.workspace.Root(), sessionID)
}

func (a *App) AuditLearning() ([]learning.Run, error) {
	if a.learningInitErr != nil {
		return nil, a.learningInitErr
	}
	return a.learning.Audit(context.Background(), a.workspace.Root())
}

func (a *App) ApprovedLessons() ([]learning.Lesson, error) {
	if a.learningInitErr != nil {
		return nil, a.learningInitErr
	}
	return a.learning.Lessons(context.Background(), a.workspace.Root())
}

func (a *App) ApproveLearning(runID string, candidate learning.Candidate) (learning.Lesson, error) {
	return a.learning.Approve(context.Background(), runID, candidate)
}

func (a *App) RejectLearning(runID string) error {
	return a.learning.Reject(context.Background(), runID)
}
func (a *App) CancelLearning(runID string) error {
	return a.learning.Cancel(context.Background(), runID)
}
func (a *App) RetryLearning(runID string) (learning.Run, error) {
	return a.learning.Retry(context.Background(), runID)
}
func (a *App) SetLessonEnabled(lessonID string, enabled bool) error {
	return a.learning.SetLessonEnabled(context.Background(), lessonID, enabled)
}
func (a *App) DeleteLesson(lessonID string) error {
	return a.learning.DeleteLesson(context.Background(), lessonID)
}

// PermissionMode handles the exact local /mode commands without recording a turn.
func (a *App) PermissionMode(sessionID, command string) (string, error) {
	sitting := a.workspace.Sitting()
	switch command {
	case "/mode:auto-accept":
		sitting.Yolo.Set(false)
		sitting.AutoAccept.Set(sessionID, true)
	case "/mode:ask":
		sitting.Yolo.Set(false)
		sitting.AutoAccept.Set(sessionID, false)
	case "/mode:yolo":
		if !sitting.Yolo.Authorized() || !sitting.Yolo.Set(true) {
			return "", errors.New("YOLO mode requires launching with --yolo")
		}
	case "/mode":
	default:
		return "", fmt.Errorf("unknown permission mode command")
	}
	if sitting.Yolo.Enabled() {
		return "yolo", nil
	}
	if sitting.AutoAccept.Enabled(sessionID) {
		return "auto-accept", nil
	}
	return "ask", nil
}

// ReasoningEffort returns the shared model effort preference; an empty value is
// the provider default.
func (a *App) ReasoningEffort() string {
	return string(a.workspace.Sitting().Reasoning.Effort())
}

// SetReasoningEffort updates the preference used by subsequent turns in both
// desktop and TUI wiring.
func (a *App) SetReasoningEffort(effort string) error {
	return a.workspace.Sitting().Reasoning.Set(llm.ReasoningEffort(effort))
}

// Workspace returns the working folder in force. The UI shows it in the sidebar and
// uses it to decide whether opening a chat from another folder changes the
// workspace.
func (a *App) Workspace() string { return a.workspace.Root() }

// SetWorkspace changes the working folder live: it validates that path is a folder,
// cuts the runs in flight (they pointed at the old root) and rewires every
// tool/skill/subagent/prompt to the new one. New sessions capture this folder. It is
// the binding the frontend calls when a folder is chosen or changed.
func (a *App) SetWorkspace(path string) error {
	return a.workspace.SetRoot(path)
}

// ConnectMCP starts a local stdio MCP server and makes its discovered tools
// available to subsequent agent turns.
func (a *App) ConnectMCP(config mcpclient.ServerConfig) (mcpclient.ServerStatus, error) {
	return a.workspace.ConnectMCP(context.Background(), config)
}

// DisconnectMCP removes a local MCP server and its tools from future turns.
func (a *App) DisconnectMCP(name string) error {
	return a.workspace.DisconnectMCP(name)
}

// ListMCPs returns every declared MCP server (the global config merged with
// the workspace .mcp.json) overlaid with its live connection state.
func (a *App) ListMCPs() ([]mcpclient.ServerStatus, error) {
	configs, err := mcpclient.LoadConfig(a.workspace.Root())
	if err != nil {
		return nil, err
	}
	return mcpclient.Merge(configs, a.workspace.MCPStatus()), nil
}

// SaveMCPConfig persists a server into the global MCP config
// (~/.config/atenea/mcp.json), so the desktop app and the TUI share it.
func (a *App) SaveMCPConfig(config mcpclient.ServerConfig) error {
	return mcpclient.UpsertGlobalConfig(config)
}

// RemoveMCPConfig disconnects the server (idempotent) and deletes it from the
// global MCP config. A server declared in the workspace .mcp.json cannot be
// removed from here: the error points at the file to edit. A name nothing
// declares is not an error — the panel may be acting on a list an edit outside
// the app already invalidated.
func (a *App) RemoveMCPConfig(name string) error {
	if err := a.workspace.DisconnectMCP(name); err != nil {
		return err
	}
	_, err := mcpclient.RemoveGlobalConfig(a.workspace.Root(), name)
	return err
}

// SelectWorkspace opens the native folder dialog and, if the user picks one, sets it
// with SetWorkspace; it returns the resulting folder. It is the Wails boundary (it
// needs the ctx and the GUI), not testable headless; the testable logic lives in
// SetWorkspace. If the user cancels (path ""), the folder is left as it was.
func (a *App) SelectWorkspace() (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Elegir carpeta de trabajo"})
	if err != nil {
		return a.workspace.Root(), err
	}
	if dir == "" {
		return a.workspace.Root(), nil // cancelled
	}
	if err := a.SetWorkspace(dir); err != nil {
		return a.workspace.Root(), err
	}
	return dir, nil
}

// SendPlanPrompt admits the text in plan mode: read-only research plus present_plan,
// with the plan-mode contract in the system prompt. It sets ModePlan before starting.
// It is the binding the frontend calls to plan a feature.
func (a *App) SendPlanPrompt(sessionID, text string) error {
	turn := a.sessions.Turn(sessionID, text)
	return a.workspace.Admit(func() error {
		_, err := a.agent.SendPlan(sessionID, session.Prompt{Text: text}, a.turnHooks(sessionID, turn))
		return err
	})
}

// AcceptPlan accepts and executes the plan: it returns to normal mode and promotes
// the fixed implementation prompt as the user's prompt. It is the binding the
// frontend calls when a presented plan is approved.
func (a *App) AcceptPlan(sessionID string) error {
	return a.workspace.Admit(func() error {
		_, err := a.agent.AcceptPlan(sessionID, a.turnHooks(sessionID, nil))
		return err
	})
}

// ListSessions returns the chat history for the sidebar: one summary per session (ID
// + Title from the first prompt), most recent first. It is the binding the frontend
// calls when it mounts the view. It reads the durable store without emitting.
func (a *App) ListSessions() ([]session.SessionSummary, error) {
	return a.sessions.List(context.Background())
}

// SessionHistory returns the complete durable log of a session (the same
// SessionEvents that travel the bus live) so the frontend can replay it and
// rehydrate the conversation. It is the binding the frontend calls when a session
// from the history is opened.
func (a *App) SessionHistory(sessionID string) ([]session.SessionEvent, error) {
	return a.sessions.History(context.Background(), sessionID)
}

// ListProjectFiles lists the workspace files (paths relative to the root, honoring
// .gitignore and excluding .git) for the composer's @-menu. The frontend filters and
// sorts client-side as the user types; what is returned here is the whole listing,
// bounded by the glob's limit.
func (a *App) ListProjectFiles() ([]string, error) {
	return a.workspace.Files(context.Background())
}

// ListCommands lists the available slash commands (name + description) for the
// composer menu, sorted by name. The frontend filters and sorts client-side as the
// user types after "/"; on send, the backend expands the command.
func (a *App) ListCommands() ([]command.Command, error) {
	commands := a.workspace.Commands()
	commands = append(commands,
		command.Command{Name: "learn", Description: "Extract a lesson from this session", BuiltIn: true},
		command.Command{Name: "learned", Description: "Open the learning audit", BuiltIn: true},
		command.Command{Name: "mode", Description: "Show safe auto-accept mode", BuiltIn: true},
		command.Command{Name: "mode:auto-accept", Description: "Auto-accept safe workspace edits", BuiltIn: true},
		command.Command{Name: "mode:ask", Description: "Ask before workspace edits", BuiltIn: true},
		command.Command{Name: "reasoning", Description: llm.ReasoningCommandDescription, BuiltIn: true},
	)
	return commands, nil
}

// ResolveToolPermission delivers the user's decision on a gated tool call
// (ask-before-run) to the runner: approved=true lets it run, false denies it.
// It is the binding the frontend calls on Approve/Deny. No-op if the callID
// no longer has a pending request (double click or cancelled run).
func (a *App) ResolveToolPermission(sessionID, callID string, approved bool) {
	a.gate.Resolve(sessionID, callID, approved)
}

// DeleteSession deletes a conversation from the history: it cuts the session's run in
// flight (if any), forgets its mode, and deletes its durable log from the store. It is
// the binding the frontend calls when a chat is deleted from the sidebar.
func (a *App) DeleteSession(sessionID string) error {
	a.workspace.Sitting().AutoAccept.Set(sessionID, false)
	return a.sessions.Delete(context.Background(), sessionID)
}

// Stop cancels the run in flight for sessionID (the stop button). No-op if none is
// running.
func (a *App) Stop(sessionID string) {
	a.agent.Stop(sessionID)
}

func (a *App) turnHooks(sessionID string, turn *wailssession.Turn) agent.Hooks {
	return agent.Hooks{
		BeforeAdmit: func() error {
			if turn != nil {
				return turn.BeforeAdmit()
			}
			return nil
		},
		AfterRun: func(result agent.RunResult) {
			if result.Err != nil {
				a.bus.PublishError(sessionID, result.Err)
			}
			if turn != nil {
				turn.AfterRun(result.Current)
			}
		},
	}
}

// wait blocks until the runs in flight have finished. Only the tests use it, to
// be deterministic without sleeping; the UI never calls it.
func (a *App) wait() { a.agent.Wait() }
