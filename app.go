package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"atenea/internal/agent"
	"atenea/internal/command"
	"atenea/internal/event"
	"atenea/internal/llm"
	"atenea/internal/mcpclient"
	"atenea/internal/session"
	"atenea/internal/skill"
	"atenea/internal/terminal"
	"atenea/internal/tool"
	"atenea/internal/wiring"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	// openRouterBaseURL es el gateway OpenAI-compatible que se usa para pruebas.
	openRouterBaseURL = "https://openrouter.ai/api/v1"
	// defaultModel es el modelo por defecto en OpenRouter; override por OPENROUTER_MODEL.
	defaultModel = "openrouter/free"

	// providerKind* son los tipos de provider que el selector de la UI puede elegir.
	// openrouter usa la API key del entorno; local apunta a un endpoint OpenAI-
	// compatible (LM Studio, Ollama) sin secreto; demo es el fake sin red.
	providerKindOpenRouter = "openrouter"
	providerKindLocal      = "local"
	providerKindDemo       = "demo"
	// localPlaceholderKey es la "api key" que se manda a un endpoint local: LM Studio
	// y Ollama no la exigen, pero el cliente OpenAI quiere algo no vacio.
	localPlaceholderKey = "local"
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
	ctx      context.Context // ctx de Wails; lo fija startup. Solo lo usa la EmitFunc real.
	inbox    session.Inbox
	bus      *event.Bus
	emit     event.EmitFunc                // la misma frontera que usa el bus; la tab Terminal empuja su salida por aca
	store    session.Store                 // lectura del historial para la sidebar (ListSessions/SessionHistory)
	gate     *session.MemoryPermissionGate // ask-before-run: the UI resolves via ResolveToolPermission
	glob     *tool.GlobTool                // listado de archivos del workspace para el @-menu del composer (ListProjectFiles)
	agent    *agent.Service                // ciclo headless compartido con la TUI
	provider llm.Provider                  // el modelo; mutable via SetProvider, leido con mu (currentProvider). Lo reusa el titler
	snaps    *tool.SessionSnapshots        // read-state por sesion; sobrevive a los cambios de workspace (se crea una vez)
	root     string                        // raiz del workspace; mutable via SetWorkspace, leida con mu (workspaceRoot)

	// versioner es el store crudo si sabe exponer su data_version (SQLite sobre
	// archivo); startup lanza con el un watcher que emite "sessions:changed"
	// cuando OTRO proceso (la TUI) escribe la base compartida. nil (MemoryStore)
	// = sin watcher. watchInterval es el periodo de sondeo; los tests lo acortan.
	versioner     event.DataVersioner
	watchInterval time.Duration

	// providerCfg es la config del provider activo (kind/baseURL/model) que la UI
	// elige y lee; mutable via SetProvider bajo mu. newProvider construye el provider
	// real desde una config; es inyectable para que los tests verifiquen el
	// re-cableado con un fake en vez de tocar la red (default buildProvider).
	providerCfg ProviderConfig
	newProvider func(ProviderConfig) llm.Provider

	// titler genera el titulo de una sesion a partir de su primer mensaje. nil
	// (default en tests) = sin auto-title: la sidebar cae al primer mensaje.
	// NewApp lo cablea contra el provider real; un titler vacio ("") tambien cae
	// al fallback.
	titler func(firstMessage string) string

	mu sync.Mutex
	// lifecycleMu hace atomico el swap root/glob/runner respecto de la admision
	// de prompts, sin mezclar ese bloqueo con el estado general de App.
	lifecycleMu sync.Mutex

	term *terminal.Manager // las tabs Terminal: varias sesiones pty vivas por id
	mcp  *mcpclient.Manager
}

// newAppWithStore arma la app sobre un store, un provider y la frontera (emit)
// inyectados. El store se decora con EmittingStore (puente Store -> UI) y el
// cableado del agente (tools, skills, subagentes, runner) se delega en wire.
// Es el punto unico de ensamblado: los tests lo llaman via newApp (MemoryStore +
// provider fake) y produccion via NewApp (SQLiteStore + provider real).
func newAppWithStore(store session.Store, provider llm.Provider, emit event.EmitFunc) *App {
	a := &App{}
	a.provider = provider
	// El watcher del data_version se ancla al store CRUDO (antes de decorarlo):
	// solo el SQLiteStore sobre archivo sabe exponerlo; un MemoryStore no, y la
	// app queda sin watcher (no hay otro proceso posible sobre memoria).
	a.watchInterval = 1 * time.Second
	if v, ok := store.(event.DataVersioner); ok {
		a.versioner = v
	}
	// newProvider arma el provider real desde una config; SetProvider lo usa para
	// reconstruir en vivo. Los tests lo sobreescriben con un fake.
	a.newProvider = buildProvider
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
	a.mcp = mcpclient.NewManager(root)
	a.wire(root)
	return a
}

// wire arma (o re-arma) todo el cableado anclado a root delegando en
// wiring.Build (la unica fuente de verdad del ensamblado, compartida con el
// engine headless de la TUI). Construye fuera del lock y publica los punteros
// mutables (root y glob) bajo mu y reconfigura el servicio en el mismo swap,
// asi SetWorkspace en vivo no compite con las lecturas. Lo llama el constructor
// (root = cwd) y SetWorkspace (root nuevo).
func (a *App) wire(root string) {
	a.mcp.SetRoot(root)
	// El provider vigente se lee bajo mu (SetProvider puede cambiarlo): un snapshot
	// para que el cableado nuevo (runner, taskTool, web_fetch) quede anclado a un
	// unico provider sin competir con el swap. SetProvider lo fija ANTES de llamar wire.
	provider := a.currentProvider()
	// local marca si el provider vigente es un endpoint local (LM Studio, Ollama):
	// decide el prompt base (function-calling explicito) en vez de la persona code-gen
	// del default. Se lee bajo mu, como el provider; SetProvider fija el kind ANTES de
	// llamar wire.
	local := a.currentProviderKind() == providerKindLocal
	built := wiring.Build(wiring.Config{
		Root:     root,
		Provider: provider,
		Store:    a.store, // ya decorado con EmittingStore en newAppWithStore
		Inbox:    a.inbox,
		Gate:     a.gate,
		Snaps:    a.snaps,
		Bus:      a.bus,
		Local:    local,
		NextID:   wiring.NewIDGen(),
		Mode:     a.agent.Mode,
		MCPTools: a.mcp.Tools(),
	})

	a.lifecycleMu.Lock()
	a.mu.Lock()
	a.root = root
	a.glob = built.Glob
	a.mu.Unlock()
	a.agent.Configure(built.Runner, built.Commands)
	a.lifecycleMu.Unlock()
}

// workspaceRoot devuelve la raiz vigente bajo mu (SetWorkspace la cambia en vivo).
func (a *App) workspaceRoot() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.root
}

// currentGlob devuelve el puntero vigente bajo mu; wire lo reemplaza al cambiar
// de workspace, asi las lecturas no compiten con el swap.
func (a *App) currentGlob() *tool.GlobTool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.glob
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
	cfg := initialProviderConfig()
	a = newAppWithStore(openStore(), buildProvider(cfg), emit)
	a.providerCfg = cfg // seed: Model()/ProviderConfig() reflejan la config inicial
	// Auto-title: el primer mensaje de cada sesion se resume con el provider real.
	// Solo en produccion; los tests dejan titler nil para no doblar las llamadas al
	// provider en cada envio. Lee provider y modelo vigentes (SetProvider puede
	// cambiarlos en vivo) bajo mu.
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

// resolveModel resuelve el modelo por defecto desde el entorno: OPENROUTER_MODEL si
// esta seteado, si no defaultModel. Es el fallback que usan initialProviderConfig (la
// config inicial) y currentModel (cuando la config activa no fija modelo) para que la
// UI y el provider coincidan.
func resolveModel() string {
	if model := os.Getenv("OPENROUTER_MODEL"); model != "" {
		return model
	}
	return defaultModel
}

// initialProviderConfig deriva la config del provider al arrancar desde el entorno:
// si hay OPENROUTER_API_KEY, OpenRouter con su modelo; si no, el demo (fake sin red)
// para que `wails dev` muestre streaming sin configurar nada. El frontend puede
// re-aplicar una config local persistida via SetProvider despues.
func initialProviderConfig() ProviderConfig {
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		log.Print("atenea: sin OPENROUTER_API_KEY; usando provider de demo (sin red)")
		return ProviderConfig{Kind: providerKindDemo, Model: defaultModel}
	}
	return ProviderConfig{Kind: providerKindOpenRouter, BaseURL: openRouterBaseURL, Model: resolveModel()}
}

// buildProvider arma el provider real desde una config. local usa el adaptador
// OpenAI-compatible SIN el campo reasoning de OpenRouter (LM Studio, Ollama no lo
// entienden) y una key placeholder; openrouter usa la key del entorno; cualquier otro
// kind (demo o vacio) cae al fake sin red. Es el default de a.newProvider.
func buildProvider(cfg ProviderConfig) llm.Provider {
	switch cfg.Kind {
	case providerKindLocal:
		return llm.NewOpenAIProvider(localPlaceholderKey, cfg.BaseURL, cfg.Model, llm.WithoutOpenRouterReasoning())
	case providerKindOpenRouter:
		return llm.NewOpenAIProvider(os.Getenv("OPENROUTER_API_KEY"), cfg.BaseURL, cfg.Model)
	default:
		return demoProvider()
	}
}

// normalizeProviderConfig valida y completa la config que pide la UI. openrouter
// fuerza siempre su baseURL e ignora el entrante: la UI no lo ofrece configurar y
// puede arrastrar el del local al cambiar de proveedor (tambien sanea configs viejas
// persistidas que restoreProvider re-aplica al arrancar). local exige un baseURL
// http(s) y un modelo (lo elige el usuario del catalogo). Un kind desconocido es un
// error. Mantener la validacion aca (no en SetProvider) la hace testeable sin tocar
// el estado.
func normalizeProviderConfig(kind, baseURL, model string) (ProviderConfig, error) {
	switch kind {
	case providerKindOpenRouter:
		baseURL = openRouterBaseURL
		if model == "" {
			model = defaultModel
		}
		return ProviderConfig{Kind: kind, BaseURL: baseURL, Model: model}, nil
	case providerKindLocal:
		if err := validateBaseURL(baseURL); err != nil {
			return ProviderConfig{}, err
		}
		if strings.TrimSpace(model) == "" {
			return ProviderConfig{}, fmt.Errorf("provider local: falta el modelo")
		}
		return ProviderConfig{Kind: kind, BaseURL: baseURL, Model: model}, nil
	default:
		return ProviderConfig{}, fmt.Errorf("provider desconocido: %q (usa %q o %q)", kind, providerKindOpenRouter, providerKindLocal)
	}
}

// validateBaseURL exige un baseURL http(s) con host (p.ej. http://localhost:1234/v1):
// sin el, el provider local no tiene a donde apuntar.
func validateBaseURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("provider local: falta el baseURL (p.ej. http://localhost:1234/v1)")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("provider local: baseURL invalido %q (espera http(s)://host:puerto/v1)", raw)
	}
	return nil
}

// Model expone el modelo activo para que la UI dimensione la barra de contexto por
// modelo. Lee la config vigente (SetProvider puede cambiarla en vivo).
func (a *App) Model() string { return a.currentModel() }

// ProviderConfig expone la config del provider activo (kind/baseURL/model) para que
// el selector de la UI muestre lo elegido. Es el binding que el frontend lee al montar.
func (a *App) ProviderConfig() ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.providerCfg
}

// SetProvider cambia el provider en vivo: valida/completa la config, reconstruye el
// provider, corta las corridas en vuelo (usaban el modelo viejo) y recablea todas las
// tools/subagentes/web_fetch al provider nuevo, igual que SetWorkspace con el root. Es
// el binding que el frontend llama al elegir OpenRouter o un endpoint local.
func (a *App) SetProvider(kind, baseURL, model string) error {
	cfg, err := normalizeProviderConfig(kind, baseURL, model)
	if err != nil {
		return err
	}
	prov := a.newProvider(cfg)
	a.mu.Lock()
	a.provider = prov
	a.providerCfg = cfg
	a.mu.Unlock()
	a.wire(a.workspaceRoot())
	return nil
}

// ListModels devuelve el catalogo de modelos de un endpoint OpenAI-compatible (GET
// baseURL/models) para poblar el dropdown del selector. Sin secreto: los locales no
// exigen key. Es el binding que el frontend llama al escribir el baseURL.
func (a *App) ListModels(baseURL string) ([]string, error) {
	return llm.ListModels(context.Background(), baseURL, "")
}

// currentProvider devuelve el provider vigente bajo mu (SetProvider lo cambia en vivo).
func (a *App) currentProvider() llm.Provider {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.provider
}

// currentProviderKind devuelve el kind de la config vigente bajo mu (openrouter/local/
// demo). wire lo usa para elegir el prompt base local.
func (a *App) currentProviderKind() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.providerCfg.Kind
}

// currentModel devuelve el modelo de la config vigente, o el default del entorno si
// la config no lo fija (caso de los tests con provider inyectado y cfg vacia).
func (a *App) currentModel() string {
	a.mu.Lock()
	model := a.providerCfg.Model
	a.mu.Unlock()
	if model != "" {
		return model
	}
	return resolveModel()
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
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	_, err := a.agent.Send(sessionID, text, a.turnHooks(sessionID, job))
	return err
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
	a.wire(path)
	return nil
}

// ConnectMCP starts a local stdio MCP server and makes its discovered tools
// available to subsequent agent turns.
func (a *App) ConnectMCP(config mcpclient.ServerConfig) (mcpclient.ServerStatus, error) {
	status, err := a.mcp.Connect(context.Background(), config)
	if err != nil {
		return mcpclient.ServerStatus{}, err
	}
	a.wire(a.workspaceRoot())
	return status, nil
}

// DisconnectMCP removes a local MCP server and its tools from future turns.
func (a *App) DisconnectMCP(name string) error {
	if err := a.mcp.Disconnect(name); err != nil {
		return err
	}
	a.wire(a.workspaceRoot())
	return nil
}

// ListMCPs returns all currently connected local MCP servers.
func (a *App) ListMCPs() []mcpclient.ServerStatus { return a.mcp.Status() }

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
func (a *App) cancelAllRuns() { a.agent.StopAll() }

// SendPlanPrompt admite el texto en plan-mode: investigacion de solo lectura mas
// present_plan, con el contrato de plan-mode en el system prompt. Fija ModePlan
// antes de arrancar. Es el binding que el frontend llama al planear una feature.
func (a *App) SendPlanPrompt(sessionID, text string) error {
	job := a.titleJob(sessionID, text)
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	_, err := a.agent.SendPlan(sessionID, text, a.turnHooks(sessionID, job))
	return err
}

// AcceptPlan acepta y ejecuta el plan: vuelve a modo normal y promueve el prompt
// fijo de implementacion como prompt del usuario. Es el binding que el frontend
// llama al aprobar un plan presentado.
func (a *App) AcceptPlan(sessionID string) error {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	_, err := a.agent.AcceptPlan(sessionID, a.turnHooks(sessionID, nil))
	return err
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
	return a.agent.Commands(), nil
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
