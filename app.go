package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"atenea/internal/agent"
	"atenea/internal/command"
	"atenea/internal/event"
	"atenea/internal/llm"
	"atenea/internal/mcpclient"
	"atenea/internal/providerconfig"
	"atenea/internal/session"
	"atenea/internal/skill"
	"atenea/internal/terminal"
	"atenea/internal/tool"
	"atenea/internal/wailsprovider"
	"atenea/internal/wailsworkspace"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	// openRouterBaseURL es el gateway OpenAI-compatible que se usa para pruebas.
	openRouterBaseURL = wailsprovider.OpenRouterBaseURL
	// defaultModel es el modelo por defecto en OpenRouter; override por OPENROUTER_MODEL.
	defaultModel = wailsprovider.DefaultModel

	// providerKind* son los tipos de provider que el selector de la UI puede elegir.
	// openrouter usa la API key del entorno; local apunta a un endpoint OpenAI-
	// compatible (LM Studio, Ollama) sin secreto; demo es el fake sin red.
	providerKindOpenRouter = wailsprovider.KindOpenRouter
	providerKindLocal      = wailsprovider.KindLocal
	providerKindDemo       = wailsprovider.KindDemo
)

// ProviderConfig es la configuracion del modelo activo que la UI elige y lee. No
// lleva secretos: la key de OpenRouter sigue viniendo del entorno; un provider local
// no necesita key. Se persiste en el frontend (localStorage) y se re-aplica al
// arrancar via SetProvider, igual que la carpeta de trabajo. Es comparable a
// proposito (solo strings) para que los tests verifiquen "no cambio" con !=.
type ProviderConfig struct {
	Kind    string `json:"kind"`
	BaseURL string `json:"baseURL"`
	Model   string `json:"model"`
}

// App cablea el loop del agente (M1..M8) a la app Wails: arranca/corta Run desde
// el frontend y reenvia el log durable por el Bus. La logica del loop no cambia.
type App struct {
	ctx       context.Context // ctx de Wails; lo fija startup. Solo lo usa la EmitFunc real.
	inbox     session.Inbox
	bus       *event.Bus
	emit      event.EmitFunc                // la misma frontera que usa el bus; la tab Terminal empuja su salida por aca
	store     session.Store                 // lectura del historial para la sidebar (ListSessions/SessionHistory)
	gate      *session.MemoryPermissionGate // ask-before-run: the UI resolves via ResolveToolPermission
	agent     *agent.Service                // ciclo headless compartido con la TUI
	providers *wailsprovider.Manager        // provider/config atomicos; SetProvider publica snapshots completos
	workspace *wailsworkspace.Manager       // root, wiring, glob y MCP publicados como un snapshot serializado

	// versioner es el store crudo si sabe exponer su data_version (SQLite sobre
	// archivo); startup lanza con el un watcher que emite "sessions:changed"
	// cuando OTRO proceso (la TUI) escribe la base compartida. nil (MemoryStore)
	// = sin watcher. watchInterval es el periodo de sondeo; los tests lo acortan.
	versioner     event.DataVersioner
	watchInterval time.Duration

	// titler genera el titulo de una sesion a partir de su primer mensaje. nil
	// (default en tests) = sin auto-title: la sidebar cae al primer mensaje.
	// NewApp lo cablea contra el provider real; un titler vacio ("") tambien cae
	// al fallback.
	titler func(firstMessage string) string

	term *terminal.Manager // las tabs Terminal: varias sesiones pty vivas por id
}

// newAppWithStore arma la app sobre un store, un provider y la frontera (emit)
// inyectados. El store se decora con EmittingStore (puente Store -> UI) y el
// cableado del agente (tools, skills, subagentes, runner) se delega al modulo
// wailsworkspace.
// Es el punto unico de ensamblado: los tests lo llaman via newApp (MemoryStore +
// provider fake) y produccion via NewApp (SQLiteStore + provider real).
func newAppWithStore(store session.Store, provider llm.Provider, emit event.EmitFunc, providerConfigs ...wailsprovider.Config) *App {
	a := &App{}
	credentials := defaultCredentialStore()
	providerConfig := wailsprovider.Config{}
	if len(providerConfigs) > 0 {
		providerConfig = providerConfigs[0]
	}
	a.providers = wailsprovider.New(provider, providerConfig, func(cfg wailsprovider.Config) llm.Provider {
		return wailsprovider.Build(cfg, os.Getenv, credentials, demoProvider())
	}, os.Getenv, credentials, nil)
	// El watcher del data_version se ancla al store CRUDO (antes de decorarlo):
	// solo el SQLiteStore sobre archivo sabe exponerlo; un MemoryStore no, y la
	// app queda sin watcher (no hay otro proceso posible sobre memoria).
	a.watchInterval = 1 * time.Second
	if v, ok := store.(event.DataVersioner); ok {
		a.versioner = v
	}
	a.emit = emit
	a.bus = event.NewBus(emit)
	a.term = terminal.NewManager()
	emitting := event.NewEmittingStore(store, a.bus)
	a.store = emitting // las lecturas del historial delegan en inner sin emitir
	a.inbox = session.NewMemoryInbox()
	a.agent = agent.NewService(a.inbox)
	// read, write y edit comparten snapshots por sesion: read graba hash + lineas
	// vistas, edit lo lee para anclar ediciones, write crea archivos nuevos con su
	// snapshot. El read-state es por sesion (no por carpeta): se crea una vez y
	// sobrevive a los cambios de workspace.
	snaps := tool.NewSessionSnapshots()
	// Ask-before-run: bash is the only gated tool for now. The UI approves/denies
	// each command via ResolveToolPermission. El gate no depende del root: se crea
	// una vez y wailsworkspace lo recablea en cada runner.
	a.gate = session.NewMemoryPermissionGate()
	// La raiz inicial es el cwd del proceso; SetWorkspace la cambia en vivo.
	root, err := os.Getwd()
	if err != nil {
		root = "."
	}
	a.workspace = wailsworkspace.New(wailsworkspace.Config{
		Root: root,
		ProviderState: func() wailsworkspace.ProviderState {
			state := a.providers.Snapshot()
			return wailsworkspace.ProviderState{Provider: state.Provider, Local: state.Local}
		},
		Store: a.store, Inbox: a.inbox, Gate: a.gate, Snapshots: snaps, Bus: a.bus, Agent: a.agent,
	})
	return a
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
	credentials := defaultCredentialStore()
	cfg := wailsprovider.InitialConfig(os.Getenv, credentials)
	if cfg.Kind == providerKindDemo {
		log.Print("atenea: no OPENROUTER_API_KEY and no /connect credential; using the demo provider (no network)")
	}
	a = newAppWithStore(openStore(), wailsprovider.Build(cfg, os.Getenv, credentials, demoProvider()), emit, cfg)
	// Auto-title: el primer mensaje de cada sesion se resume con el provider real.
	// Solo en produccion; los tests dejan titler nil para no doblar las llamadas al
	// provider en cada envio. Lee provider y modelo vigentes (SetProvider puede
	// cambiarlos en vivo) desde el snapshot del manager.
	a.titler = func(message string) string {
		return titleFromProvider(a.currentProvider(), a.currentModel(), message)
	}
	return a
}

// openStore abre el SQLite COMPARTIDO con la TUI via session.OpenDefault (la
// fuente unica de la ruta y la apertura: ambos procesos ven las mismas
// sesiones). Si falla (disco/permiso), OpenDefault ya devolvio el fallback en
// memoria usable: la app abre igual, solo que sin persistencia. El cierre del
// store se delega al ciclo de vida del proceso.
func openStore() session.Store {
	store, err := session.OpenDefault()
	if err != nil {
		log.Printf("atenea: no se pudo abrir SQLite (%v); usando store en memoria", err)
	}
	return store
}

// defaultCredentialStore opens the credential store shared with the TUI.
func defaultCredentialStore() providerconfig.CredentialStore {
	return providerconfig.NewFileCredentialStore(providerconfig.DefaultCredentialsPath())
}

// Model expone el modelo activo para que la UI dimensione la barra de contexto por
// modelo. Lee la config vigente (SetProvider puede cambiarla en vivo).
func (a *App) Model() string { return a.currentModel() }

// ProviderConfig expone la config del provider activo (kind/baseURL/model) para que
// el selector de la UI muestre lo elegido. Es el binding que el frontend lee al montar.
func (a *App) ProviderConfig() ProviderConfig {
	cfg := a.providers.Snapshot().Config
	return ProviderConfig{Kind: cfg.Kind, BaseURL: cfg.BaseURL, Model: cfg.Model}
}

// SetProvider cambia el provider en vivo: valida/completa la config, reconstruye el
// provider, corta las corridas en vuelo (usaban el modelo viejo) y recablea todas las
// tools/subagentes/web_fetch al provider nuevo, igual que SetWorkspace con el root. Es
// el binding que el frontend llama al elegir OpenRouter o un endpoint local.
func (a *App) SetProvider(kind, baseURL, model string) error {
	return a.workspace.Reconfigure(func() error {
		_, err := a.providers.Set(kind, baseURL, model)
		return err
	})
}

// ListModels devuelve el catalogo de modelos de un endpoint OpenAI-compatible (GET
// baseURL/models) para poblar el dropdown del selector. Sin secreto: los locales no
// exigen key. Es el binding que el frontend llama al escribir el baseURL.
func (a *App) ListModels(baseURL string) ([]string, error) {
	// OpenRouter's /models endpoint is public, but sending the resolved key
	// keeps the listing consistent with the chat path (and with any
	// account-scoped catalog the provider may expose).
	return a.providers.ListModels(context.Background(), baseURL)
}

// currentProvider reads the provider from the manager's atomic snapshot.
func (a *App) currentProvider() llm.Provider {
	return a.providers.Snapshot().Provider
}

// currentModel devuelve el modelo de la config vigente, o el default del entorno si
// la config no lo fija (caso de los tests con provider inyectado y cfg vacia).
func (a *App) currentModel() string {
	model := a.providers.Snapshot().Config.Model
	if model != "" {
		return model
	}
	return wailsprovider.ResolveModel(os.Getenv)
}

// startup guarda el ctx de Wails (lo usa la EmitFunc real) y, si el store
// expone su data_version, lanza el watcher que emite "sessions:changed" cuando
// OTRO proceso (la TUI) escribe sesiones nuevas/actualizadas en el SQLite
// compartido; la sidebar re-pide ListSessions al recibirlo. El ctx de Wails se
// cancela al cerrar la app, lo que apaga el watcher.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if a.versioner != nil {
		go event.WatchStore(ctx, a.versioner, a.watchInterval, func() {
			a.emit("sessions:changed")
		})
	}
}

// SendPrompt admite el texto como prompt en cola y arranca Run en una goroutine.
// Es el binding que el frontend llama al enviar. Fija modo normal primero: una
// sesion que estaba en plan-mode vuelve a las tools normales al enviar.
func (a *App) SendPrompt(sessionID, text string) error {
	job := a.titleJob(sessionID, text)
	return a.workspace.Admit(func() error {
		_, err := a.agent.Send(sessionID, text, a.turnHooks(sessionID, job))
		return err
	})
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
		session.SessionEvent{Kind: session.KindSessionCwd, Text: a.workspace.Root()}); err != nil {
		log.Printf("atenea: no se pudo guardar la carpeta de %s: %v", sessionID, err)
	}
}

// Workspace devuelve la carpeta de trabajo vigente. La UI la muestra en la sidebar
// y la usa para decidir si abrir un chat de otra carpeta cambia el workspace.
func (a *App) Workspace() string { return a.workspace.Root() }

// SetWorkspace cambia la carpeta de trabajo en vivo: valida que path sea una
// carpeta, corta las corridas en vuelo (apuntaban al root viejo) y recablea todas
// las tools/skills/subagentes/prompt al root nuevo. Las sesiones nuevas capturan
// esta carpeta. Es el binding que el frontend llama al elegir o cambiar de carpeta.
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
// removed from here: the error points at the file to edit.
func (a *App) RemoveMCPConfig(name string) error {
	if err := a.workspace.DisconnectMCP(name); err != nil {
		return err
	}
	removed, err := mcpclient.RemoveGlobalConfig(name)
	if err != nil || removed {
		return err
	}
	configs, err := mcpclient.LoadConfig(a.workspace.Root())
	if err != nil {
		return err
	}
	for _, config := range configs {
		if config.Name == name {
			return fmt.Errorf("MCP %q is declared in the workspace %s; edit that file to remove it", name, mcpclient.ConfigFile)
		}
	}
	return nil
}

// SelectWorkspace abre el dialogo nativo de carpeta y, si el usuario elige una, la
// fija con SetWorkspace; devuelve la carpeta vigente resultante. Es la frontera
// Wails (necesita el ctx y el GUI), no testeable headless; la logica testeable vive
// en SetWorkspace. Si el usuario cancela (path ""), deja la carpeta como estaba.
func (a *App) SelectWorkspace() (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Elegir carpeta de trabajo"})
	if err != nil {
		return a.workspace.Root(), err
	}
	if dir == "" {
		return a.workspace.Root(), nil // cancelado
	}
	if err := a.SetWorkspace(dir); err != nil {
		return a.workspace.Root(), err
	}
	return dir, nil
}

// SendPlanPrompt admite el texto en plan-mode: investigacion de solo lectura mas
// present_plan, con el contrato de plan-mode en el system prompt. Fija ModePlan
// antes de arrancar. Es el binding que el frontend llama al planear una feature.
func (a *App) SendPlanPrompt(sessionID, text string) error {
	job := a.titleJob(sessionID, text)
	return a.workspace.Admit(func() error {
		_, err := a.agent.SendPlan(sessionID, text, a.turnHooks(sessionID, job))
		return err
	})
}

// AcceptPlan acepta y ejecuta el plan: vuelve a modo normal y promueve el prompt
// fijo de implementacion como prompt del usuario. Es el binding que el frontend
// llama al aprobar un plan presentado.
func (a *App) AcceptPlan(sessionID string) error {
	return a.workspace.Admit(func() error {
		_, err := a.agent.AcceptPlan(sessionID, a.turnHooks(sessionID, nil))
		return err
	})
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
	return a.workspace.Files(context.Background())
}

// ListCommands lista los slash-commands disponibles (nombre + descripcion) para
// el menu del composer, ordenados por nombre. El frontend filtra/ordena en cliente
// conforme el usuario escribe tras "/"; al enviar, el backend expande el comando.
func (a *App) ListCommands() ([]command.Command, error) {
	return a.workspace.Commands(), nil
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
	a.agent.Forget(sessionID)
	return a.store.DeleteSession(context.Background(), sessionID)
}

// Stop cancela la corrida en vuelo de sessionID (boton stop). No-op si no corre.
func (a *App) Stop(sessionID string) {
	a.agent.Stop(sessionID)
}

func (a *App) turnHooks(sessionID string, afterRun func()) agent.Hooks {
	return agent.Hooks{
		BeforeAdmit: func() error {
			a.captureCwd(sessionID)
			return nil
		},
		AfterRun: func(result agent.RunResult) {
			if result.Err != nil {
				a.bus.PublishError(sessionID, result.Err)
			}
			if result.Current && afterRun != nil {
				afterRun()
			}
		},
	}
}

// wait bloquea hasta que terminen las corridas en vuelo. Solo lo usan los tests
// para ser deterministas sin sleep; la UI no lo llama.
func (a *App) wait() { a.agent.Wait() }

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
