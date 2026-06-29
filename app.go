package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
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
	"atenea/internal/terminal"
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
	emit     event.EmitFunc                // la misma frontera que usa el bus; la tab Terminal empuja su salida por aca
	store    session.Store                 // lectura del historial para la sidebar (ListSessions/SessionHistory)
	gate     *session.MemoryPermissionGate // ask-before-run: the UI resolves via ResolveToolPermission
	glob     *tool.GlobTool                // listado de archivos del workspace para el @-menu del composer (ListProjectFiles)
	commands *command.Set                  // slash-commands del composer (ListCommands + expansion en SendPrompt)
	provider llm.Provider                  // el modelo; lo reusa el titler para resumir el primer mensaje
	snaps    *tool.SessionSnapshots        // read-state por sesion; sobrevive a los cambios de workspace (se crea una vez)
	root     string                        // raiz del workspace; mutable via SetWorkspace, leida con mu (workspaceRoot)

	// titler genera el titulo de una sesion a partir de su primer mensaje. nil
	// (default en tests) = sin auto-title: la sidebar cae al primer mensaje.
	// NewApp lo cablea contra el provider real; un titler vacio ("") tambien cae
	// al fallback.
	titler func(firstMessage string) string

	mu    sync.Mutex
	runs  map[string]*runHandle   // sessionID -> corrida en vuelo (identidad por puntero)
	modes map[string]session.Mode // sessionID -> modo (normal/plan); guardado con mu como runs
	wg    sync.WaitGroup          // los tests esperan a las corridas; la UI es fire-and-forget

	term *terminal.Manager // las tabs Terminal: varias sesiones pty vivas por id
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
	a.provider = provider
	a.emit = emit
	a.bus = event.NewBus(emit)
	a.term = terminal.NewManager()
	emitting := event.NewEmittingStore(store, a.bus)
	a.store = emitting // las lecturas del historial delegan en inner sin emitir
	a.inbox = session.NewMemoryInbox()
	// read, write y edit comparten snapshots por sesion: read graba hash + lineas
	// vistas, edit lo lee para anclar ediciones, write crea archivos nuevos con su
	// snapshot. El read-state es por sesion (no por carpeta): se crea una vez y
	// sobrevive a los cambios de workspace.
	a.snaps = tool.NewSessionSnapshots()
	// Ask-before-run: bash is the only gated tool for now. The UI approves/denies
	// each command via ResolveToolPermission. El gate no depende del root: se crea
	// una vez y wire lo recablea en cada runner.
	a.gate = session.NewMemoryPermissionGate()
	// La raiz inicial es el cwd del proceso; SetWorkspace la cambia en vivo.
	root, err := os.Getwd()
	if err != nil {
		root = "."
	}
	a.wire(root)
	return a
}

// wire arma (o re-arma) todo el cableado anclado a root: las file/exec tools, el
// glob del @-menu, las skills y sus slash-commands, el catalogo de subagentes y un
// runner nuevo con el system prompt apuntando a root. Lo construye fuera del lock y
// publica los punteros mutables (root, glob, commands, runner) bajo mu en un swap
// corto, asi un SetWorkspace en vivo no compite con las lecturas. Lo llama el
// constructor (root = cwd) y SetWorkspace (root nuevo).
func (a *App) wire(root string) {
	// El @-menu de archivos del composer lista el workspace via este glob
	// (ListProjectFiles). Comparte la raiz con las file tools; reusa el searcher
	// de ripgrep ya probado (respeta .gitignore, excluye .git).
	glob := tool.NewGlobTool(root)
	// Skills al estilo opencode (disclosure progresivo): se descubren bajo las rutas
	// del proyecto Y las globales del home (skillDirs). Sus metadatos van en el system
	// prompt (skill.Format), la tool skill carga el cuerpo bajo demanda, y de cada una
	// se deriva un slash-command. Un fallo de descubrimiento no es fatal: sin skills.
	skills, err := skill.Discover(skillDirs(root)...)
	if err != nil {
		log.Printf("atenea: no se pudieron descubrir las skills: %v", err)
	}
	skillsBlock := skill.Format(skills)
	// Slash-commands del composer, derivados de las skills (un "/<name>" por skill).
	commands := command.New(command.FromSkills(skills))
	// Subagentes: catalogo = built-in (explore read-only, general full) mas los .md
	// del workspace (.atenea/agents propio, .agents/agents estandar; el propio override
	// al homonimo). Un fallo de descubrimiento no es fatal: quedan los built-in.
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
		tool.NewReadToolWithSnapshotProvider(root, a.snaps), tool.NewWriteToolWithSnapshotProvider(root, a.snaps),
		tool.NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, a.snaps),
		tool.NewGlobTool(root), tool.NewGrepToolWithSnapshotProvider(root, a.snaps),
		tool.NewBashTool(root))
	// La tool task levanta subagentes hijos. nextID propio (thread-safe) porque
	// varios subagentes pueden correr en paralelo (cap de concurrencia interno).
	taskTool := subagent.NewTaskTool(agentDefs, a.provider, childRegistry, newIDGen())
	// present_plan se registra para que el runner pueda ejecutarla, pero NO entra
	// en los Permissions normales: solo se anuncia en plan-mode (SetPlanMode).
	registry := tool.NewRegistry(tool.NewOutputStore(outputLimit), tool.Echo{},
		tool.NewReadToolWithSnapshotProvider(root, a.snaps), tool.NewWriteToolWithSnapshotProvider(root, a.snaps),
		tool.NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, a.snaps),
		tool.NewGlobTool(root), tool.NewGrepToolWithSnapshotProvider(root, a.snaps),
		tool.NewBashTool(root), tool.NewPresentPlanTool(root), tool.NewSkillTool(skills), taskTool,
		tool.NewWebFetchTool(a.provider), tool.TodoWriteTool{})
	r := runner.NewRunner(a.store, a.inbox, a.provider, registry,
		tool.Permissions{"echo": true, "read": true, "write": true, "edit": true, "glob": true, "grep": true, "bash": true, "skill": true, "task": true, "web_fetch": true, "todo_write": true},
		newIDGen())
	r.SetSystemPrompt(systemPromptBuilder(root, skillsBlock))
	r.SetPermissionGate(a.gate, func(c tool.Call) bool { return c.Name == "bash" })
	// Plan-mode: investigacion de solo lectura mas present_plan (sin write/edit/bash/
	// echo). El hook de modo decide por sesion; SetMode/SetPlanMode toman efecto solo
	// cuando modeFor reporta ModePlan.
	r.SetMode(a.modeFor)
	r.SetPlanMode(planSystemPromptBuilder(root, skillsBlock),
		tool.Permissions{"read": true, "glob": true, "grep": true, "present_plan": true, "skill": true})

	a.mu.Lock()
	a.root = root
	a.glob = glob
	a.commands = commands
	a.runner = r
	a.mu.Unlock()
}

// workspaceRoot devuelve la raiz vigente bajo mu (SetWorkspace la cambia en vivo).
func (a *App) workspaceRoot() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.root
}

// currentGlob y currentCommands devuelven los punteros vigentes bajo mu; wire los
// reemplaza al cambiar de workspace, asi las lecturas no compiten con el swap.
func (a *App) currentGlob() *tool.GlobTool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.glob
}

func (a *App) currentCommands() *command.Set {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.commands
}

// skillDirs returns the directories scanned for skills, project (workspace root)
// first and then global (the user's home), so a project skill overrides a global one
// with the same name (skill.Discover is first-wins). Under each base it looks at
// .atenea/skills (atenea's own), .agents/skills (the tool-agnostic standard shared
// across agents) and .claude/skills (Claude Code). Las skills globales viven asi en
// ~/.agents/skills, ~/.claude/skills, etc. Si no se puede resolver el home, quedan
// solo las del proyecto. Rutas identicas (p.ej. si el root ES el home) se deduplican
// para no recorrer el mismo arbol dos veces.
func skillDirs(root string) []string {
	subdirs := []string{
		filepath.Join(".atenea", "skills"),
		filepath.Join(".agents", "skills"),
		filepath.Join(".claude", "skills"),
	}
	bases := []string{root}
	if home, herr := os.UserHomeDir(); herr != nil {
		log.Printf("atenea: no se pudo resolver el home para skills globales: %v", herr)
	} else if home != "" {
		bases = append(bases, home)
	}
	var dirs []string
	seen := map[string]bool{}
	for _, base := range bases {
		for _, sub := range subdirs {
			dir := filepath.Join(base, sub)
			if seen[dir] {
				continue
			}
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
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
	// Skills built-in: materializar a ~/.atenea/skills (ruta que skillDirs ya escanea)
	// las skills que viajan embebidas en el binario, antes de descubrir. Asi vienen "de
	// fabrica" tras instalar, sin que el usuario copie nada. No es fatal: si falla, la
	// app arranca igual con las skills que haya en disco.
	if home, herr := os.UserHomeDir(); herr != nil {
		log.Printf("atenea: no se pudo resolver el home para extraer skills built-in: %v", herr)
	} else if eerr := skill.ExtractBuiltins(filepath.Join(home, ".atenea", "skills")); eerr != nil {
		log.Printf("atenea: no se pudieron extraer las skills built-in: %v", eerr)
	}
	a = newAppWithStore(openStore(), chooseProvider(), emit)
	// Auto-title: el primer mensaje de cada sesion se resume con el provider real.
	// Solo en produccion; los tests dejan titler nil para no doblar las llamadas al
	// provider en cada envio.
	a.titler = func(message string) string {
		return titleFromProvider(a.provider, resolveModel(), message)
	}
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
	job := a.titleJob(sessionID, text)
	a.captureCwd(sessionID)
	if err := a.inbox.Admit(context.Background(), sessionID,
		session.Prompt{Text: a.expandCommand(text)}, session.DeliveryQueue); err != nil {
		return err
	}
	a.start(sessionID, job)
	return nil
}

// expandCommand resuelve un slash-command ("/name args") al prompt expandido
// segun el registro; el texto que no es un comando registrado pasa sin cambios.
// Lo comparten SendPrompt y SendPlanPrompt para un comportamiento consistente.
func (a *App) expandCommand(text string) string {
	if expanded, ok := a.currentCommands().Resolve(text); ok {
		return expanded
	}
	return text
}

// auxTurnTimeout acota los turnos aislados auxiliares (titulo de sesion, mensaje de
// commit). Sin deadline, un SSE colgado deja la goroutine viva para siempre con el
// body abierto; estos turnos son cortos, asi que un limite acotado es seguro.
const auxTurnTimeout = 30 * time.Second

// titleSystemPrompt instruye al modelo a devolver SOLO un titulo corto del primer
// mensaje, sin adornos. El recorte a 80 runes lo hace la proyeccion Sessions.
const titleSystemPrompt = "Genera un titulo muy corto (maximo 6 palabras) para una conversacion que empieza con el mensaje del usuario. Responde SOLO con el titulo, en el idioma del mensaje, sin comillas, sin punto final y sin prefijos."

// titleJob devuelve la tarea de titulado para el PRIMER mensaje de una sesion
// nueva, o nil si no aplica (sin titler cableado, o la sesion ya existe). El
// chequeo de "sesion nueva" es sincrono y debe correr ANTES de arrancar Run (que
// crea la sesion al promover el prompt). La tarea se ejecuta DESPUES del turno, no
// en paralelo: el titulado comparte el provider con el turno y, lanzado a la vez,
// le compite la peticion (en proveedores que encolan/limitan peticiones
// concurrentes el turno se atrasa y el streaming en vivo no aparece a tiempo).
// Si el titulo sale vacio no persiste nada y la sidebar cae al primer mensaje.
func (a *App) titleJob(sessionID, message string) func() {
	if a.titler == nil {
		return nil
	}
	if _, err := a.store.LoadSession(context.Background(), sessionID); err == nil {
		return nil // la sesion ya existe: no es el primer mensaje
	}
	return func() {
		title := strings.TrimSpace(a.titler(message))
		if title == "" {
			return
		}
		if _, err := a.store.AppendEvent(context.Background(), sessionID,
			session.SessionEvent{Kind: session.KindSessionTitle, Text: title}); err != nil {
			log.Printf("atenea: no se pudo guardar el titulo de %s: %v", sessionID, err)
		}
	}
}

// titleFromProvider abre un turno aislado contra el provider (system de titulado +
// el primer mensaje) y concatena el texto del stream como titulo, recortando
// espacios. "" si el stream falla o no produce texto: el caller cae al fallback.
func titleFromProvider(p llm.Provider, model, message string) string {
	ctx, cancel := context.WithTimeout(context.Background(), auxTurnTimeout)
	defer cancel()
	out, err := p.Stream(ctx, llm.Request{
		Model:    model,
		System:   titleSystemPrompt,
		Messages: []llm.Message{{Role: "user", Text: message}},
	})
	if err != nil {
		return ""
	}
	var b strings.Builder
	for ev := range out {
		if ev.Kind == llm.TextDelta {
			b.WriteString(ev.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// captureCwd graba la carpeta de trabajo vigente como Session.Cwd la PRIMERA vez
// que se manda un prompt a la sesion (cuando aun no existe en el store). La sidebar
// agrupa los chats por esa carpeta. Debe correr DESPUES del chequeo de "sesion
// nueva" de titleJob (ambos miran LoadSession) y ANTES de admitir el prompt, asi el
// Session.Cwd queda de primero en el log. Idempotente: en la sesion ya existente no
// hace nada. Un fallo al grabar no corta el envio: la carpeta solo afecta la sidebar.
func (a *App) captureCwd(sessionID string) {
	if _, err := a.store.LoadSession(context.Background(), sessionID); err == nil {
		return // la sesion ya existe: no es el primer prompt
	}
	if _, err := a.store.AppendEvent(context.Background(), sessionID,
		session.SessionEvent{Kind: session.KindSessionCwd, Text: a.workspaceRoot()}); err != nil {
		log.Printf("atenea: no se pudo guardar la carpeta de %s: %v", sessionID, err)
	}
}

// Workspace devuelve la carpeta de trabajo vigente. La UI la muestra en la sidebar
// y la usa para decidir si abrir un chat de otra carpeta cambia el workspace.
func (a *App) Workspace() string { return a.workspaceRoot() }

// SetWorkspace cambia la carpeta de trabajo en vivo: valida que path sea una
// carpeta, corta las corridas en vuelo (apuntaban al root viejo) y recablea todas
// las tools/skills/subagentes/prompt al root nuevo. Las sesiones nuevas capturan
// esta carpeta. Es el binding que el frontend llama al elegir o cambiar de carpeta.
func (a *App) SetWorkspace(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("workspace invalido: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace invalido: %s no es una carpeta", path)
	}
	a.cancelAllRuns()
	a.wire(path)
	return nil
}

// SelectWorkspace abre el dialogo nativo de carpeta y, si el usuario elige una, la
// fija con SetWorkspace; devuelve la carpeta vigente resultante. Es la frontera
// Wails (necesita el ctx y el GUI), no testeable headless; la logica testeable vive
// en SetWorkspace. Si el usuario cancela (path ""), deja la carpeta como estaba.
func (a *App) SelectWorkspace() (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Elegir carpeta de trabajo"})
	if err != nil {
		return a.workspaceRoot(), err
	}
	if dir == "" {
		return a.workspaceRoot(), nil // cancelado
	}
	if err := a.SetWorkspace(dir); err != nil {
		return a.workspaceRoot(), err
	}
	return dir, nil
}

// cancelAllRuns corta toda corrida en vuelo. ponytail: las cancela pero no las
// espera; cada runner viejo termina solo en su ctx cancelado mientras el nuevo ya
// atiende los envios. Lo usa SetWorkspace antes de recablear.
func (a *App) cancelAllRuns() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, h := range a.runs {
		h.cancel()
	}
}

// SendPlanPrompt admite el texto en plan-mode: investigacion de solo lectura mas
// present_plan, con el contrato de plan-mode en el system prompt. Fija ModePlan
// antes de arrancar. Es el binding que el frontend llama al planear una feature.
func (a *App) SendPlanPrompt(sessionID, text string) error {
	a.setMode(sessionID, session.ModePlan)
	job := a.titleJob(sessionID, text)
	a.captureCwd(sessionID)
	if err := a.inbox.Admit(context.Background(), sessionID,
		session.Prompt{Text: a.expandCommand(text)}, session.DeliveryQueue); err != nil {
		return err
	}
	a.start(sessionID, job)
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
	a.start(sessionID, nil) // ejecutar un plan nunca es el primer mensaje: sin titulado
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
	g := a.currentGlob()
	files, _, err := g.Files(context.Background(), "", ".", g.MaxLimit)
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
// afterRun (opcional) corre tras el turno en la misma goroutine: lo usa el titulado
// del primer mensaje para no competirle el proveedor al turno (ver titleJob).
func (a *App) start(sessionID string, afterRun func()) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &runHandle{cancel: cancel}
	a.mu.Lock()
	if old := a.runs[sessionID]; old != nil {
		old.cancel() // una corrida previa de la misma sesion no debe quedar viva
	}
	a.runs[sessionID] = h
	r := a.runner // capturado bajo el lock: SetWorkspace puede cambiarlo en vuelo
	a.mu.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer a.clear(sessionID, h)
		if err := r.Run(ctx, sessionID, false); err != nil {
			// A deliberate cancellation (Stop, workspace change, follow-up) makes Run
			// return context.Canceled/DeadlineExceeded: that is a clean shutdown, not
			// a failure, so do NOT publish it as a red error in the UI.
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				a.bus.PublishError(sessionID, err)
			}
		}
		if afterRun != nil {
			afterRun()
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
