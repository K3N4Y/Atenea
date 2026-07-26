package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/event"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/mcpclient"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/skill"
	"github.com/K3N4Y/atenea/internal/terminal"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/wailssession"
	"github.com/K3N4Y/atenea/internal/wailsworkspace"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// offlineProviderID names the fake the app falls back to with no credential
// anywhere. It is not in any catalog, so nothing can ever select it — the app only
// lands on it, and the UI has an id that matches no row, which is how the model
// panel knows to say that replies are canned.
const offlineProviderID = "demo"

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
	// Connectable is whether an API key can be stored for this provider from here.
	// Connected is whether one already is.
	Connectable bool `json:"connectable"`
	Connected   bool `json:"connected"`
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

// App cablea el loop del agente (M1..M8) a la app Wails: arranca/corta Run desde
// el frontend y reenvia el log durable por el Bus. La logica del loop no cambia.
type App struct {
	ctx       context.Context // ctx de Wails; lo fija startup. Solo lo usa la EmitFunc real.
	inbox     session.Inbox
	bus       *event.Bus
	emit      event.EmitFunc          // la misma frontera que usa el bus; la tab Terminal empuja su salida por aca
	gate      *permission.MemoryGate  // ask-before-run: the UI resolves via ResolveToolPermission
	agent     *agent.Service          // ciclo headless compartido con la TUI
	providers *providerconfig.Service // el mismo catalogo, credenciales y seleccion que la TUI
	workspace *wailsworkspace.Manager // root, wiring, glob y MCP publicados como un snapshot serializado
	sessions  *wailssession.Manager   // historial durable, metadata inicial, titulos y borrado

	term *terminal.Manager // las tabs Terminal: varias sesiones pty vivas por id
}

// newAppWithStore arma la app sobre un store, el servicio de providers y la
// frontera (emit) inyectados. El store se decora con EmittingStore (puente Store
// -> UI) y el cableado del agente (tools, skills, subagentes, runner) se delega
// al modulo wailsworkspace.
// Es el punto unico de ensamblado: los tests lo llaman con un MemoryStore y un
// servicio sin archivo, produccion via NewApp (SQLite + providers.json real).
func newAppWithStore(store session.Store, providers *providerconfig.Service, emit event.EmitFunc) *App {
	a := &App{providers: providers}
	// El watcher del data_version se ancla al store CRUDO (antes de decorarlo):
	// solo el SQLiteStore sobre archivo sabe exponerlo; un MemoryStore no, y la
	// app queda sin watcher (no hay otro proceso posible sobre memoria).
	var versioner event.DataVersioner
	if v, ok := store.(event.DataVersioner); ok {
		versioner = v
	}
	a.emit = emit
	a.bus = event.NewBus(emit)
	a.term = terminal.NewManager()
	emitting := event.NewEmittingStore(store, a.bus)
	a.inbox = session.NewMemoryInbox()
	a.agent = agent.NewService(a.inbox)
	// read, write y edit comparten snapshots por sesion: read graba hash + lineas
	// vistas, edit lo lee para anclar ediciones, write crea archivos nuevos con su
	// snapshot. El read-state es por sesion (no por carpeta): se crea una vez y
	// sobrevive a los cambios de workspace.
	snaps := tool.NewSessionSnapshots()
	// Ask-before-run: the fixed policy (wiring.askPolicy) gates bash, write,
	// edit and web_fetch. The UI approves/denies each call via
	// ResolveToolPermission. The gate does not depend on the root: it is
	// created once and wailsworkspace rewires it into every runner.
	a.gate = permission.NewMemoryGate()
	// La raiz inicial es el cwd del proceso; SetWorkspace la cambia en vivo.
	root, err := os.Getwd()
	if err != nil {
		root = "."
	}
	a.workspace = wailsworkspace.New(wailsworkspace.Config{
		Root: root,
		// El handle switchable es estable: elegir otro modelo cambia a que delega,
		// no que se cablea, asi que el rebuild solo existe para cortar las corridas
		// que venian del modelo anterior.
		Provider:    a.providers.Provider(),
		LocalPrompt: func() bool { return a.providers.Active().LocalModels },
		Store:       emitting, Inbox: a.inbox, Gate: a.gate, Snapshots: snaps, Bus: a.bus, Agent: a.agent,
	})
	a.sessions = wailssession.New(wailssession.Config{
		Store: emitting, Root: a.workspace.Root, Forget: a.agent.Forget,
		Versioner: versioner, Emit: emit, WatchPeriod: time.Second,
	})
	return a
}

// NewApp arma la app de produccion: store SQLite durable y el servicio de
// providers sobre los archivos compartidos con la TUI. La EmitFunc cierra sobre a
// para leer a.ctx (que startup fija despues): emitir antes de startup pasa un ctx
// nil, pero la UI no llama SendPrompt antes de cargar.
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
	a = newAppWithStore(openStore(), openProviderService(), emit)
	// Auto-title: el primer mensaje de cada sesion se resume con el provider real.
	// Solo en produccion; los tests dejan titler nil para no doblar las llamadas al
	// provider en cada envio. Lee provider y modelo vigentes (SetProvider puede
	// cambiarlos en vivo) desde el snapshot del manager.
	a.sessions.SetTitler(wailssession.ProviderTitler(func() (llm.Provider, string) {
		return a.currentProvider(), a.currentModel()
	}))
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

// openProviderService abre el servicio de providers de produccion: providers.json,
// el cache de modelos y las credenciales en sus rutas por defecto — las MISMAS que
// la TUI, asi que elegir un modelo en cualquiera de las dos lo cambia en las dos.
// Ningun fallo es fatal: el servicio vuelve usable (sirviendo el fallback) y solo
// la seleccion falla, con el motivo en el log.
func openProviderService() *providerconfig.Service {
	fallback, fromEnvironment := providerconfig.DefaultFallback(offlineSnapshot())
	if !fromEnvironment {
		log.Print("atenea: no provider API key in the environment; falling back to the stored selection or the offline demo")
	}
	providers, err := providerconfig.OpenDefault(context.Background(), fallback)
	if err != nil {
		log.Printf("atenea: provider config: %v", err)
	}
	return providers
}

// ProviderCatalog lista los providers configurados con sus modelos y su estado de
// credencial: es lo que pinta el selector de modelo del panel de ajustes.
func (a *App) ProviderCatalog() []ProviderEntry {
	return a.providerEntries(a.providers.Catalog())
}

// RefreshModels vuelve a pedir el catalogo de modelos a cada endpoint que soporta
// descubrimiento y devuelve el catalogo resultante. El error es una advertencia:
// los endpoints que si respondieron ya estan en la respuesta.
func (a *App) RefreshModels() ([]ProviderEntry, error) {
	providers, err := a.providers.Refresh(context.Background())
	return a.providerEntries(providers), err
}

// providerEntries proyecta el catalogo al selector, cruzandolo con lo que /connect
// puede gestionar: el catalogo sabe de modelos y el store de credenciales sabe de
// keys, y la fila necesita las dos cosas.
func (a *App) providerEntries(providers []providerconfig.ProviderModels) []ProviderEntry {
	connectable := map[string]bool{}
	for _, provider := range a.providers.Connectable() {
		connectable[provider.ID] = provider.Connected
	}
	entries := make([]ProviderEntry, 0, len(providers))
	for _, provider := range providers {
		connected, isConnectable := connectable[provider.ID]
		entries = append(entries, ProviderEntry{
			ID: provider.ID, Name: provider.Name, Models: provider.Models,
			BuiltIn: provider.BuiltIn, Connectable: isConnectable, Connected: connected,
		})
	}
	return entries
}

// ActiveProvider expone la seleccion vigente y la ventana de contexto que el
// adapter declara para ese modelo, para que la UI dimensione la barra de contexto
// sin mantener su propia tabla de ventanas.
func (a *App) ActiveProvider() ActiveProvider {
	active := a.providers.Active()
	window := 0
	if capabilities, ok := llm.ActiveCapabilities(a.providers.Provider()); ok {
		window, _ = capabilities.ContextWindow(active.Model)
	}
	return ActiveProvider{ProviderID: active.ProviderID, ProviderName: active.ProviderName, Model: active.Model, ContextWindow: window}
}

// SelectModel cambia el provider/modelo activo en vivo: reconstruye el adapter,
// corta las corridas en vuelo (venian del modelo anterior) y persiste la seleccion
// en el providers.json compartido. Es el binding que el frontend llama al elegir un
// modelo del selector.
func (a *App) SelectModel(providerID, model string) error {
	return a.workspace.Reconfigure(func() error {
		_, err := a.providers.Select(context.Background(), providerID, model)
		return err
	})
}

// ConnectProvider valida una API key contra el provider y la guarda en el store de
// credenciales compartido con la TUI. Si no habia nada seleccionado, deja el
// provider activo en su primer modelo.
func (a *App) ConnectProvider(providerID, apiKey string) error {
	return a.workspace.Reconfigure(func() error {
		_, err := a.providers.Connect(context.Background(), providerID, apiKey)
		return err
	})
}

// DeclareEndpoint agrega un endpoint OpenAI-compatible del usuario (LM Studio,
// Ollama) al providers.json compartido y devuelve su id. No lo activa: elegirlo es
// SelectModel. model es opcional — sin el, el endpoint queda listo para que
// RefreshModels descubra su catalogo.
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
		// Un endpoint del usuario corre modelos locales: sus ids son arbitrarios, asi
		// que el prompt no puede enrutarse por familia de modelo.
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

// ForgetProvider quita del providers.json un endpoint declarado por el usuario.
func (a *App) ForgetProvider(providerID string) error {
	return a.providers.Forget(providerID)
}

// endpointID deriva un id estable del nombre que escribio el usuario, para que el
// formulario pida un dato en vez de dos. Cualquier cosa que no sea letra o digito
// se vuelve un guion, y los guiones de los extremos se caen.
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

// ListModels devuelve el catalogo de un endpoint OpenAI-compatible (GET
// baseURL/models) ANTES de declararlo, para que el formulario de "agregar
// endpoint" pueda ofrecer los modelos que hay en vez de pedirlos de memoria. Sin
// secreto: un endpoint local no exige key.
func (a *App) ListModels(baseURL string) ([]string, error) {
	return llm.ListModels(context.Background(), baseURL, "")
}

// currentProvider es el handle switchable: siempre delega al adapter vigente.
func (a *App) currentProvider() llm.Provider { return a.providers.Provider() }

// currentModel es el modelo de la seleccion vigente.
func (a *App) currentModel() string { return a.providers.Active().Model }

// startup guarda el ctx de Wails (lo usa la EmitFunc real) y, si el store
// expone su data_version, lanza el watcher que emite "sessions:changed" cuando
// OTRO proceso (la TUI) escribe sesiones nuevas/actualizadas en el SQLite
// compartido; la sidebar re-pide ListSessions al recibirlo. El ctx de Wails se
// cancela al cerrar la app, lo que apaga el watcher.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.sessions.Watch(ctx)
}

// SendPrompt admite el texto como prompt en cola y arranca Run en una goroutine.
// Es el binding que el frontend llama al enviar. Fija modo normal primero: una
// sesion que estaba en plan-mode vuelve a las tools normales al enviar.
func (a *App) SendPrompt(sessionID, text string) error {
	turn := a.sessions.Turn(sessionID, text)
	return a.workspace.Admit(func() error {
		_, err := a.agent.Send(sessionID, text, a.turnHooks(sessionID, turn))
		return err
	})
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
	turn := a.sessions.Turn(sessionID, text)
	return a.workspace.Admit(func() error {
		_, err := a.agent.SendPlan(sessionID, text, a.turnHooks(sessionID, turn))
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
	return a.sessions.List(context.Background())
}

// SessionHistory devuelve el log durable completo de una sesion (los mismos
// SessionEvent que viajan por el bus en vivo) para que el frontend lo reproduzca
// y rehidrate la conversacion. Es el binding que el frontend llama al abrir una
// sesion del historial.
func (a *App) SessionHistory(sessionID string) ([]session.SessionEvent, error) {
	return a.sessions.History(context.Background(), sessionID)
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
	return a.sessions.Delete(context.Background(), sessionID)
}

// Stop cancela la corrida en vuelo de sessionID (boton stop). No-op si no corre.
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

// wait bloquea hasta que terminen las corridas en vuelo. Solo lo usan los tests
// para ser deterministas sin sleep; la UI no lo llama.
func (a *App) wait() { a.agent.Wait() }

// offlineSnapshot es con lo que arranca la app cuando no hay credencial en
// ninguna parte: el fake sin red, presentado como cualquier otro provider para
// que la UI pueda decir en que esta en vez de mostrar respuestas inventadas como
// si fueran de un modelo.
func offlineSnapshot() llm.ProviderSnapshot {
	return llm.ProviderSnapshot{
		ProviderID:   offlineProviderID,
		ProviderName: "Demo",
		BaseURL:      "demo://local",
		Model:        "demo",
		Provider:     demoProvider(),
	}
}

// demoProvider arma un FakeProvider con un guion corto (texto + Step.Ended) para
// que `wails dev` muestre streaming sin red.
func demoProvider() llm.Provider {
	return llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "Hola desde atenea."},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
}
