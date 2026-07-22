package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"atenea/internal/agent"
	"atenea/internal/checkpoint"
	"atenea/internal/command"
	"atenea/internal/event"
	"atenea/internal/llm"
	"atenea/internal/mcpclient"
	"atenea/internal/providerconfig"
	"atenea/internal/session"
	"atenea/internal/session/runner"
	"atenea/internal/tool"
	"atenea/internal/wiring"
)

// Config describe el ensamblado del agente headless: la raiz del
// workspace, el proveedor LLM, el store durable y el modo local.
type Config struct {
	Root        string
	Provider    llm.Provider
	Store       session.Store
	Local       bool
	Models      ModelService
	Checkpoints checkpoint.Store
}

type UndoResult struct {
	Prompt string
	Events []session.SessionEvent
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

type ModelsRefreshedMsg struct {
	Providers []providerconfig.ProviderModels
	Err       string
}

// ConnectService is the optional surface a ModelService can implement to
// support /connect. providerconfig.Service does; fakes that do not care about
// connection simply omit it.
type ConnectService interface {
	Connectable() []providerconfig.ConnectableProvider
	Connect(ctx context.Context, providerID, apiKey string) (providerconfig.Active, error)
}

// Engine es el agente headless que arma runner + tools + permisos sin Wails y
// publica los eventos durables de la sesion por un canal de mensajes Bubble Tea.
// El ensamblado real vive en wiring.Build (la misma fuente de verdad que la app
// Wails); aqui solo se cablea la frontera: Bus -> canal de la TUI.
type Engine struct {
	events chan tea.Msg
	inbox  session.Inbox
	gate   *session.MemoryPermissionGate
	agent  *agent.Service
	ctx    context.Context
	cancel context.CancelFunc

	// runner y glob son las piezas mutables del ensamblado: rewire (al conectar
	// o desconectar un MCP) las reemplaza, asi que se leen bajo mu. glob alimenta
	// el @-menu de archivos del composer (espejo de App.ListProjectFiles).
	runner *runner.Runner
	glob   *tool.GlobTool

	// wiring es la config base del ensamblado; rewire la reusa con las MCPTools
	// vigentes. mcp es el manager de servidores MCP locales (stdio) del engine;
	// los servers declarados salen de <root>/.mcp.json y se releen en cada
	// listado, asi una edicion del archivo se refleja sin reiniciar.
	wiring wiring.Config
	mcp    *mcpclient.Manager

	// root y store espejan a.workspaceRoot()/a.store en la app Wails: la raiz
	// del workspace y el store DECORADO con EmittingStore (el mismo que recibe
	// wiring.Build). send los usa para grabar Session.Cwd en el primer prompt
	// de cada sesion. Inmutables tras New: se leen sin mu.
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

	lifecycleMu  sync.Mutex
	shuttingDown bool
	shutdownDone chan struct{}
	shutdownOnce sync.Once
	compactions  sync.WaitGroup
}

// New arma el engine a partir de la configuracion: una EmitFunc que
// puentea los SessionEvent durables del Bus al canal de la TUI, el store
// decorado con EmittingStore sobre ese bus, y el agente completo via
// wiring.Build (tools, skills, subagentes, runner con ask-before-run).
func New(cfg Config) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		// Buffer generoso: amortigua rafagas de deltas mientras la TUI drena.
		events:             make(chan tea.Msg, 256),
		inbox:              session.NewMemoryInbox(),
		gate:               session.NewMemoryPermissionGate(),
		pendingCompactions: map[string]bool{},
		compacting:         map[string]bool{},
		ctx:                ctx,
		cancel:             cancel,
		shutdownDone:       make(chan struct{}),
	}
	// La frontera: donde la app Wails emite a runtime.EventsEmit, aqui el evento
	// durable va al canal de la TUI. El send bloqueante es deliberado: la TUI
	// drena el canal en continuo, asi no se pierden eventos bajo rafagas.
	emit := func(name string, data ...interface{}) {
		if len(data) == 0 {
			return
		}
		if ev, ok := data[0].(session.SessionEvent); ok {
			e.sendEvent(EventMsg(ev))
		}
	}
	bus := event.NewBus(emit)
	e.root = cfg.Root
	e.undoStore, _ = cfg.Store.(session.UndoStore)
	e.store = event.NewEmittingStore(cfg.Store, bus)
	e.agent = agent.NewService(e.inbox)
	e.checkpoints = cfg.Checkpoints
	e.models = cfg.Models
	e.mcp = mcpclient.NewManager(cfg.Root)
	e.wiring = wiring.Config{
		Root:     cfg.Root,
		Provider: cfg.Provider,
		Store:    e.store,
		Inbox:    e.inbox,
		Gate:     e.gate,
		Snaps:    tool.NewSessionSnapshots(),
		Bus:      bus,
		Local:    cfg.Local,
		NextID:   wiring.NewIDGen(),
		Mode:     e.agent.Mode,
	}
	e.rewire()
	return e
}

// rewire re-ensambla el agente con las tools MCP vigentes y publica el swap:
// el mismo movimiento que App.wire (build fuera de los locks, swap de las
// piezas mutables bajo mu y Configure dentro de lifecycleMu para no competir
// con la admision de prompts ni el shutdown).
func (e *Engine) rewire() {
	cfg := e.wiring
	cfg.MCPTools = e.mcp.Tools()
	built := wiring.Build(cfg)
	e.lifecycleMu.Lock()
	e.mu.Lock()
	e.runner = built.Runner
	e.glob = built.Glob
	e.mu.Unlock()
	e.agent.Configure(built.Runner, built.Commands)
	e.lifecycleMu.Unlock()
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
	return cloneProviderModels(providers)
}

func (e *Engine) CurrentModel() providerconfig.Active {
	if e.models == nil {
		return providerconfig.Active{}
	}
	return e.models.Active()
}

func (e *Engine) SelectModel(providerID, model string) (providerconfig.Active, error) {
	if e.models == nil {
		return providerconfig.Active{}, errors.New("model selection is unavailable")
	}
	return e.models.Select(context.Background(), providerID, model)
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
		msg := ModelsRefreshedMsg{Providers: cloneProviderModels(providers)}
		if err != nil {
			msg.Err = err.Error()
		}
		e.sendEvent(msg)
	}()
}

func cloneProviderModels(in []providerconfig.ProviderModels) []providerconfig.ProviderModels {
	out := make([]providerconfig.ProviderModels, len(in))
	for i, provider := range in {
		out[i] = provider
		out[i].Models = append([]string(nil), provider.Models...)
	}
	return out
}

// Commands lista los slash-commands disponibles (nombre + descripcion) para el
// menu "/" del composer, ordenados por nombre (espejo de App.ListCommands).
func (e *Engine) Commands() []command.Command {
	commands := e.agent.Commands()
	commands = append(commands,
		command.Command{Name: "resume", Description: "Resume a TUI session in this workspace"},
		command.Command{Name: "undo", Description: "Undo the last prompt and its file changes"},
	)
	sort.Slice(commands, func(i, j int) bool { return commands[i].Name < commands[j].Name })
	return commands
}

// ProjectFiles lista los archivos del workspace (rutas relativas a la raiz,
// respetando .gitignore y excluyendo .git) para el @-menu de archivos del
// composer, acotado por el limite del glob (espejo de App.ListProjectFiles).
func (e *Engine) ProjectFiles() ([]string, error) {
	glob := e.currentGlob()
	files, _, err := glob.Files(context.Background(), "", ".", glob.MaxLimit)
	if err != nil {
		return nil, err
	}
	return files, nil
}

// currentGlob y currentRunner leen las piezas mutables bajo mu: rewire las
// reemplaza al conectar o desconectar un MCP.
func (e *Engine) currentGlob() *tool.GlobTool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.glob
}

func (e *Engine) currentRunner() *runner.Runner {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runner
}

// PromptHistory reconstruye los ultimos prompts literales enviados por TUI.
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

// ListResumeSessions devuelve las sesiones TUI resumibles del workspace actual
// en el mismo orden de recencia entregado por el store.
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

// ResumeSessionByID carga exactamente una sesion resumible del workspace y
// persiste el modo restaurado como el modo vigente de la sesion.
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
		}
	}
	return mode
}

// SendPrompt encola un prompt del usuario por el camino normal y devuelve la
// sesion activa junto con el runID. Para /new exacto crea y devuelve una sesion
// durable nueva sin corrida; en cualquier otro caso conserva sessionID. Fija
// modo normal primero: una sesion que estaba en plan-mode vuelve a las tools
// normales al enviar.
func (e *Engine) SendPrompt(sessionID, text string) (RunHandle, error) {
	e.resumeMu.Lock()
	defer e.resumeMu.Unlock()

	if text == "/new" {
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
	if text == "/compact" {
		e.requestCompaction(sessionID)
		return RunHandle{SessionID: sessionID}, nil
	}
	run, err := e.agent.Send(sessionID, text, e.turnHooks(sessionID, text, session.ModeNormal))
	if err != nil {
		return RunHandle{}, err
	}
	return RunHandle{SessionID: sessionID, RunID: run.ID}, nil
}

// SendPlanPrompt encola el prompt en plan-mode: investigacion de solo lectura
// mas present_plan, con el contrato de plan-mode en el system prompt. Fija
// ModePlan antes de arrancar (espejo de App.SendPlanPrompt).
func (e *Engine) SendPlanPrompt(sessionID, text string) (RunHandle, error) {
	e.resumeMu.Lock()
	defer e.resumeMu.Unlock()

	run, err := e.agent.SendPlan(sessionID, text, e.turnHooks(sessionID, text, session.ModePlan))
	return RunHandle{SessionID: sessionID, RunID: run.ID}, err
}

// turnHooks conserva las responsabilidades exclusivas de la TUI alrededor del
// ciclo compartido: CWD, checkpoints, historial literal, RunDoneMsg y compactado.
func (e *Engine) turnHooks(sessionID, composerPrompt string, mode session.Mode) agent.Hooks {
	checkpointID := ""
	return agent.Hooks{
		BeforeAdmit: func() error {
			var before checkpoint.Tree
			if composerPrompt != "" && e.checkpoints != nil {
				var err error
				before, err = e.checkpoints.Capture(context.Background(), e.root)
				if err != nil {
					return err
				}
			}
			if _, err := e.store.LoadSession(context.Background(), sessionID); err != nil {
				if _, err := e.store.AppendEvent(context.Background(), sessionID,
					session.SessionEvent{Kind: session.KindSessionCwd, Text: e.root}); err != nil {
					log.Printf("atenea: no se pudo guardar la carpeta de %s: %v", sessionID, err)
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
					Checkpoint: &session.PromptCheckpoint{ID: checkpointID, Prompt: composerPrompt, BeforeTree: string(before)},
				}); err != nil {
					return err
				}
			}
			return nil
		},
		AfterAdmit: func() {
			if composerPrompt == "" {
				return
			}
			if _, err := e.store.AppendEvent(context.Background(), sessionID,
				session.SessionEvent{Kind: session.KindComposerPrompt, Text: composerPrompt}); err != nil {
				log.Printf("atenea: no se pudo guardar el prompt en el historial de %s: %v", sessionID, err)
			}
		},
		AfterRun: func(result agent.RunResult) {
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
		result = UndoResult{Prompt: boundary.Prompt, Events: events}
		return nil
	})
	return result, err
}

// AcceptPlan acepta y ejecuta el plan: vuelve la sesion a modo normal y
// promueve el prompt fijo de implementacion como prompt del usuario,
// arrancando la corrida (espejo de App.AcceptPlan).
func (e *Engine) AcceptPlan(sessionID string) (RunHandle, error) {
	e.resumeMu.Lock()
	defer e.resumeMu.Unlock()

	run, err := e.agent.AcceptPlan(sessionID, e.turnHooks(sessionID, "", session.ModeNormal))
	return RunHandle{SessionID: sessionID, RunID: run.ID}, err
}

// ResolvePermission resuelve una solicitud de permiso pendiente (ask-before-run).
func (e *Engine) ResolvePermission(sessionID, callID string, approved bool) {
	e.gate.Resolve(sessionID, callID, approved)
}

// Stop interrumpe la corrida en curso de la sesion. No-op si no corre.
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
		e.lifecycleMu.Unlock()
		go func() {
			e.agent.Wait()
			e.compactions.Wait()
			// Con las corridas ya detenidas, cerrar los MCP mata sus subprocesos.
			e.mcp.Close()
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

// clear saca del mapa la corrida h solo si sigue siendo la vigente (un
// SendPrompt mas nuevo pudo reemplazarla).
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

// Events entrega los EventMsg durables de la corrida y un RunDoneMsg al
// terminar cada corrida.
func (e *Engine) Events() <-chan tea.Msg { return e.events }
