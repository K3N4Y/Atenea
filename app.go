package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"atenea/internal/agent"
	"atenea/internal/command"
	"atenea/internal/event"
	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/session/prompt"
	"atenea/internal/session/runner"
	"atenea/internal/session/subagent"
	"atenea/internal/skill"
	"atenea/internal/tool"
	"atenea/internal/tool/hashline"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	outputLimit = 32 * 1024

	// openRouterBaseURL es el gateway OpenAI-compatible que se usa para pruebas.
	openRouterBaseURL = "https://openrouter.ai/api/v1"
	// defaultModel es el modelo por defecto en OpenRouter; override por OPENROUTER_MODEL.
	defaultModel = "openrouter/free"
)

// App cablea el loop del agente (M1..M8) a la app Wails: arranca/corta Run desde
// el frontend y reenvia el log durable por el Bus. La logica del loop no cambia.
type App struct {
	ctx      context.Context // ctx de Wails; lo fija startup. Solo lo usa la EmitFunc real.
	inbox    session.Inbox
	runner   *runner.Runner
	bus      *event.Bus
	store    session.Store                 // lectura del historial para la sidebar (ListSessions/SessionHistory)
	gate     *session.MemoryPermissionGate // ask-before-run: the UI resolves via ResolveToolPermission
	glob     *tool.GlobTool                // listado de archivos del workspace para el @-menu del composer (ListProjectFiles)
	commands *command.Set                  // slash-commands del composer (ListCommands + expansion en SendPrompt)

	mu    sync.Mutex
	runs  map[string]*runHandle   // sessionID -> corrida en vuelo (identidad por puntero)
	modes map[string]session.Mode // sessionID -> modo (normal/plan); guardado con mu como runs
	wg    sync.WaitGroup          // los tests esperan a las corridas; la UI es fire-and-forget
}

// runHandle identifica una corrida en vuelo. Se compara por puntero porque
// context.CancelFunc no es comparable.
type runHandle struct{ cancel context.CancelFunc }

// newAppWithStore arma la app sobre un store, un provider y la frontera (emit)
// inyectados. El store se decora con EmittingStore (puente Store -> UI) y se le
// pasa al Runner; el registry trae el builtin echo. Es el punto unico de ensamblado:
// los tests lo llaman via newApp (MemoryStore + provider fake) y produccion via
// NewApp (SQLiteStore + provider real).
func newAppWithStore(store session.Store, provider llm.Provider, emit event.EmitFunc) *App {
	a := &App{runs: map[string]*runHandle{}, modes: map[string]session.Mode{}}
	a.bus = event.NewBus(emit)
	emitting := event.NewEmittingStore(store, a.bus)
	a.store = emitting // las lecturas del historial delegan en inner sin emitir
	a.inbox = session.NewMemoryInbox()
	// El read ancla su sandbox en la raiz del workspace; en v1 es el cwd del
	// proceso (no hay aun seleccion de proyecto en la UI). read, write y edit
	// comparten root y snapshots por sesion: read graba hash + lineas vistas, edit
	// lo lee para anclar ediciones, y write crea archivos nuevos con su snapshot.
	snaps := tool.NewSessionSnapshots()
	root, err := os.Getwd()
	if err != nil {
		root = "."
	}
	// El @-menu de archivos del composer lista el workspace via este glob
	// (ListProjectFiles). Comparte la raiz con las file tools; reusa el searcher
	// de ripgrep ya probado (respeta .gitignore, excluye .git).
	a.glob = tool.NewGlobTool(root)
	// Skills al estilo opencode (disclosure progresivo): se descubren una vez bajo
	// <root>/.atenea/skills (propio), <root>/.agents/skills (el estandar entre
	// agentes) y <root>/.claude/skills (donde Claude Code -y este repo- las guarda).
	// Sus metadatos van en el system prompt (skill.Format), la tool skill carga el
	// cuerpo bajo demanda, y de cada una se deriva un slash-command. .atenea/skills
	// va primero: ante un nombre duplicado, la skill propia override a las estandar.
	// Un fallo de descubrimiento no es fatal: el agente arranca sin skills.
	skills, err := skill.Discover(
		filepath.Join(root, ".atenea", "skills"),
		filepath.Join(root, ".agents", "skills"),
		filepath.Join(root, ".claude", "skills"),
	)
	if err != nil {
		log.Printf("atenea: no se pudieron descubrir las skills: %v", err)
	}
	skillsBlock := skill.Format(skills)
	// Slash-commands del composer: hoy se derivan de las skills (un "/<name>" por
	// skill que instruye al agente a usarla), pero el registro es general: agregar
	// /commit u otro comando es agregar un command.Command con su plantilla. El menu
	// los lista via ListCommands; SendPrompt expande el "/name args" antes de enviar.
	a.commands = command.New(command.FromSkills(skills))
	// Subagentes: catalogo = built-in (explore read-only, general full) mas los
	// .md del workspace (.atenea/agents propio, .agents/agents estandar; el propio
	// override al homonimo). Un fallo de descubrimiento no es fatal: quedan los built-in.
	agentDefs, err := agent.Catalog(
		filepath.Join(root, ".atenea", "agents"),
		filepath.Join(root, ".agents", "agents"),
	)
	if err != nil {
		log.Printf("atenea: no se pudieron descubrir los subagentes: %v", err)
	}
	// Registry de los subagentes: las mismas tools de archivo/busqueda/exec, acotadas
	// por def.Tools de cada agente (un explore read-only solo recibe read/grep/glob).
	// Sin la tool task: los subagentes no anidan en el wiring real.
	childRegistry := tool.NewRegistry(tool.NewOutputStore(outputLimit),
		tool.NewReadToolWithSnapshotProvider(root, snaps), tool.NewWriteToolWithSnapshotProvider(root, snaps),
		tool.NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, snaps),
		tool.NewGlobTool(root), tool.NewGrepToolWithSnapshotProvider(root, snaps),
		tool.NewBashTool(root))
	// La tool task levanta subagentes hijos. nextID propio (thread-safe) porque
	// varios subagentes pueden correr en paralelo (cap de concurrencia interno).
	taskTool := subagent.NewTaskTool(agentDefs, provider, childRegistry, newIDGen())
	// present_plan se registra para que el runner pueda ejecutarla, pero NO entra
	// en los Permissions normales: solo se anuncia en plan-mode (SetPlanMode).
	registry := tool.NewRegistry(tool.NewOutputStore(outputLimit), tool.Echo{},
		tool.NewReadToolWithSnapshotProvider(root, snaps), tool.NewWriteToolWithSnapshotProvider(root, snaps),
		tool.NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, snaps),
		tool.NewGlobTool(root), tool.NewGrepToolWithSnapshotProvider(root, snaps),
		tool.NewBashTool(root), tool.NewPresentPlanTool(root), tool.NewSkillTool(skills), taskTool,
		tool.NewWebFetchTool(provider))
	a.runner = runner.NewRunner(emitting, a.inbox, provider, registry,
		tool.Permissions{"echo": true, "read": true, "write": true, "edit": true, "glob": true, "grep": true, "bash": true, "skill": true, "task": true, "web_fetch": true},
		newIDGen())
	a.runner.SetSystemPrompt(systemPromptBuilder(root, skillsBlock))
	// Ask-before-run: bash is the only gated tool for now. The UI approves/denies
	// each command via ResolveToolPermission before it runs on the real machine.
	a.gate = session.NewMemoryPermissionGate()
	a.runner.SetPermissionGate(a.gate, func(c tool.Call) bool { return c.Name == "bash" })
	// Plan-mode: investigacion de solo lectura mas present_plan (sin write/edit/bash/
	// echo). El hook de modo decide por sesion; SetMode/SetPlanMode toman efecto
	// solo cuando modeFor reporta ModePlan.
	a.runner.SetMode(a.modeFor)
	a.runner.SetPlanMode(planSystemPromptBuilder(root, skillsBlock),
		tool.Permissions{"read": true, "glob": true, "grep": true, "present_plan": true, "skill": true})
	return a
}

// promptSetup anchors the shared system-prompt setup at root: it detects whether
// root is a git repo and loads the repo instructions (AGENTS.md/CLAUDE.md) once,
// then returns a per-call Env factory (date computed per call so it does not go
// stale in a long session) plus the loaded instructions. Both the normal and the
// plan-mode builders reuse it; they differ only in which pure prompt function
// (prompt.Build vs prompt.BuildPlan) they call.
func promptSetup(root string) (env func() prompt.Env, instructions string) {
	_, gitErr := os.Stat(filepath.Join(root, ".git"))
	isGit := gitErr == nil
	instructions, err := prompt.LoadInstructions(root, root)
	if err != nil {
		log.Printf("atenea: no se pudieron cargar las instrucciones del repo: %v", err)
	}
	env = func() prompt.Env {
		return prompt.Env{
			WorkingDir:   root,
			WorktreeRoot: root,
			IsGitRepo:    isGit,
			Platform:     goruntime.GOOS,
			Date:         time.Now().Format("2006-01-02"),
		}
	}
	return env, instructions
}

// systemPromptBuilder builds the normal-mode system prompt builder anchored at
// root: per turn composes the base prompt (chosen by model family) + the <env>
// block with today's date + the skills block (skills are discovered once at
// assembly and passed in formatted), over the shared promptSetup.
func systemPromptBuilder(root, skills string) func(model string) string {
	env, instructions := promptSetup(root)
	return func(model string) string {
		return prompt.Build(model, env(), instructions, skills)
	}
}

// planSystemPromptBuilder builds the plan-mode system prompt builder: same shape
// as systemPromptBuilder but uses prompt.BuildPlan, which appends the plan-mode
// contract (present_plan) on top of the base prompt.
func planSystemPromptBuilder(root, skills string) func(model string) string {
	env, instructions := promptSetup(root)
	return func(model string) string {
		return prompt.BuildPlan(model, env(), instructions, skills)
	}
}

// newApp arma la app con un MemoryStore (no durable) y el provider/emit inyectados.
// Lo usan los tests: store en memoria y provider guionado, sin tocar disco ni red.
func newApp(provider llm.Provider, emit event.EmitFunc) *App {
	return newAppWithStore(session.NewMemoryStore(), provider, emit)
}

// NewApp arma la app de produccion: store SQLite durable y provider real (OpenRouter
// si hay API key; si no, el demo sin red). La EmitFunc cierra sobre a para leer a.ctx
// (que startup fija despues): emitir antes de startup pasa un ctx nil, pero la UI no
// llama SendPrompt antes de cargar.
func NewApp() *App {
	var a *App
	emit := func(name string, data ...interface{}) {
		runtime.EventsEmit(a.ctx, name, data...)
	}
	a = newAppWithStore(openStore(), chooseProvider(), emit)
	return a
}

// openStore abre el SQLiteStore durable en el directorio de datos de la app. Si
// falla (disco/permiso), cae a un MemoryStore para que la app igual abra (sin
// persistencia). El cierre del store se delega al ciclo de vida del proceso.
func openStore() session.Store {
	store, err := session.NewSQLiteStore(dbPath())
	if err != nil {
		log.Printf("atenea: no se pudo abrir SQLite (%v); usando store en memoria", err)
		return session.NewMemoryStore()
	}
	return store
}

// dbPath resuelve la ruta de la DB: ATENEA_DB si esta seteada (util en dev), si no
// <UserConfigDir>/atenea/atenea.db (creando el directorio). Cae a "atenea.db" en el
// cwd si no hay directorio de config.
func dbPath() string {
	if p := os.Getenv("ATENEA_DB"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "atenea.db"
	}
	appDir := filepath.Join(dir, "atenea")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return "atenea.db"
	}
	return filepath.Join(appDir, "atenea.db")
}

// resolveModel resuelve el modelo activo: OPENROUTER_MODEL si esta seteado, si no
// defaultModel. Unico punto del fallback, compartido por chooseProvider (el provider
// real) y Model (lo que ve la UI) para que ambos coincidan.
func resolveModel() string {
	if model := os.Getenv("OPENROUTER_MODEL"); model != "" {
		return model
	}
	return defaultModel
}

// chooseProvider elige el provider real: si hay OPENROUTER_API_KEY usa el adaptador
// OpenAI-compatible contra OpenRouter (modelo de OPENROUTER_MODEL o defaultModel);
// si no, el demoProvider (fake) para que `wails dev` muestre streaming sin red.
func chooseProvider() llm.Provider {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		log.Print("atenea: sin OPENROUTER_API_KEY; usando provider de demo (sin red)")
		return demoProvider()
	}
	return llm.NewOpenAIProvider(key, openRouterBaseURL, resolveModel())
}

// Model expone el modelo activo (OPENROUTER_MODEL o defaultModel) para que la UI dimensione la barra de contexto por modelo.
func (a *App) Model() string { return resolveModel() }

// startup guarda el ctx de Wails (lo usa la EmitFunc real).
func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// acceptPlanPrompt es el prompt fijo que AcceptPlan promueve para ejecutar el
// plan aprobado: vuelve a modo normal e instruye al agente a implementarlo.
const acceptPlanPrompt = "El plan fue aprobado. Implementalo ahora paso a paso siguiendo el plan."

// modeFor devuelve el modo de la sesion (normal/plan). Lo consulta el runner cada
// turno via el hook SetMode. Guarda a.modes con mu, igual que runs.
func (a *App) modeFor(sessionID string) session.Mode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.modes[sessionID]
}

// setMode fija el modo de la sesion. Se llama SIEMPRE antes de start (adquisiciones
// de lock separadas, nunca anidadas) porque start tambien toma a.mu.
func (a *App) setMode(sessionID string, m session.Mode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.modes[sessionID] = m
}

// SendPrompt admite el texto como prompt en cola y arranca Run en una goroutine.
// Es el binding que el frontend llama al enviar. Fija modo normal primero: una
// sesion que estaba en plan-mode vuelve a las tools normales al enviar.
func (a *App) SendPrompt(sessionID, text string) error {
	a.setMode(sessionID, session.ModeNormal)
	if err := a.inbox.Admit(context.Background(), sessionID,
		session.Prompt{Text: a.expandCommand(text)}, session.DeliveryQueue); err != nil {
		return err
	}
	a.start(sessionID)
	return nil
}

// expandCommand resuelve un slash-command ("/name args") al prompt expandido
// segun el registro; el texto que no es un comando registrado pasa sin cambios.
// Lo comparten SendPrompt y SendPlanPrompt para un comportamiento consistente.
func (a *App) expandCommand(text string) string {
	if expanded, ok := a.commands.Resolve(text); ok {
		return expanded
	}
	return text
}

// SendPlanPrompt admite el texto en plan-mode: investigacion de solo lectura mas
// present_plan, con el contrato de plan-mode en el system prompt. Fija ModePlan
// antes de arrancar. Es el binding que el frontend llama al planear una feature.
func (a *App) SendPlanPrompt(sessionID, text string) error {
	a.setMode(sessionID, session.ModePlan)
	if err := a.inbox.Admit(context.Background(), sessionID,
		session.Prompt{Text: a.expandCommand(text)}, session.DeliveryQueue); err != nil {
		return err
	}
	a.start(sessionID)
	return nil
}

// AcceptPlan acepta y ejecuta el plan: vuelve a modo normal y promueve el prompt
// fijo de implementacion como prompt del usuario. Es el binding que el frontend
// llama al aprobar un plan presentado.
func (a *App) AcceptPlan(sessionID string) error {
	a.setMode(sessionID, session.ModeNormal)
	if err := a.inbox.Admit(context.Background(), sessionID,
		session.Prompt{Text: acceptPlanPrompt}, session.DeliveryQueue); err != nil {
		return err
	}
	a.start(sessionID)
	return nil
}

// ListSessions devuelve el historial de chats para la sidebar: un resumen por
// sesion (ID + Title del primer prompt), mas reciente primero. Es el binding que
// el frontend llama al montar la vista. Lee del store durable sin emitir.
func (a *App) ListSessions() ([]session.SessionSummary, error) {
	return a.store.Sessions(context.Background())
}

// SessionHistory devuelve el log durable completo de una sesion (los mismos
// SessionEvent que viajan por el bus en vivo) para que el frontend lo reproduzca
// y rehidrate la conversacion. Es el binding que el frontend llama al abrir una
// sesion del historial.
func (a *App) SessionHistory(sessionID string) ([]session.SessionEvent, error) {
	return a.store.Events(context.Background(), sessionID, 0)
}

// ListProjectFiles lista los archivos del workspace (rutas relativas a la raiz,
// respetando .gitignore y excluyendo .git) para el @-menu de archivos del
// composer. El frontend filtra/ordena en cliente conforme el usuario escribe;
// aqui se devuelve el listado completo, acotado por el limite del glob.
func (a *App) ListProjectFiles() ([]string, error) {
	files, _, err := a.glob.Files(context.Background(), "", ".", a.glob.MaxLimit)
	if err != nil {
		return nil, err
	}
	return files, nil
}

// ListCommands lista los slash-commands disponibles (nombre + descripcion) para
// el menu del composer, ordenados por nombre. El frontend filtra/ordena en cliente
// conforme el usuario escribe tras "/"; al enviar, el backend expande el comando.
func (a *App) ListCommands() ([]command.Command, error) {
	return a.commands.List(), nil
}

// ResolveToolPermission delivers the user's decision on a gated tool call
// (ask-before-run) to the runner: approved=true lets it run, false denies it.
// It is the binding the frontend calls on Approve/Deny. No-op if the callID
// no longer has a pending request (double click or cancelled run).
func (a *App) ResolveToolPermission(sessionID, callID string, approved bool) {
	a.gate.Resolve(sessionID, callID, approved)
}

// DeleteSession borra una conversacion del historial: corta la corrida en vuelo
// de la sesion (si la hay), olvida su modo, y borra su log durable del store. Es
// el binding que el frontend llama al borrar un chat de la sidebar.
func (a *App) DeleteSession(sessionID string) error {
	a.mu.Lock()
	h := a.runs[sessionID]
	delete(a.modes, sessionID)
	a.mu.Unlock()
	if h != nil {
		h.cancel()
	}
	return a.store.DeleteSession(context.Background(), sessionID)
}

// Stop cancela la corrida en vuelo de sessionID (boton stop). No-op si no corre.
func (a *App) Stop(sessionID string) {
	a.mu.Lock()
	h := a.runs[sessionID]
	a.mu.Unlock()
	if h != nil {
		h.cancel()
	}
}

// start lanza Run con un ctx cancelable registrado por sesion y lo limpia al
// terminar; reenvia por PublishError el error duro con que Run corta la actividad.
func (a *App) start(sessionID string) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &runHandle{cancel: cancel}
	a.mu.Lock()
	if old := a.runs[sessionID]; old != nil {
		old.cancel() // una corrida previa de la misma sesion no debe quedar viva
	}
	a.runs[sessionID] = h
	a.mu.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer a.clear(sessionID, h)
		if err := a.runner.Run(ctx, sessionID, false); err != nil {
			a.bus.PublishError(sessionID, err)
		}
	}()
}

// clear saca del mapa la corrida h solo si sigue siendo la vigente (un start mas
// nuevo pudo reemplazarla).
func (a *App) clear(sessionID string, h *runHandle) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runs[sessionID] == h {
		delete(a.runs, sessionID)
	}
}

// wait bloquea hasta que terminen las corridas en vuelo. Solo lo usan los tests
// para ser deterministas sin sleep; la UI no lo llama.
func (a *App) wait() { a.wg.Wait() }

// newIDGen devuelve un generador de assistantMessageID real: un contador atomico
// con prefijo, unico por proceso (suficiente con MemoryStore, que se reinicia con
// la app). Un ID estable entre reinicios llega con el store persistente de M10.
func newIDGen() func() string {
	var n uint64
	return func() string {
		return "msg-" + strconv.FormatUint(atomic.AddUint64(&n, 1), 10)
	}
}

// demoProvider arma un FakeProvider con un guion corto (texto + Step.Ended) para
// que `wails dev` muestre streaming sin red. M10 lo cambia por el provider real.
func demoProvider() llm.Provider {
	return llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "Hola desde atenea."},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
}
