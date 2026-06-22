package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	"atenea/internal/event"
	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/session/runner"
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
	ctx    context.Context // ctx de Wails; lo fija startup. Solo lo usa la EmitFunc real.
	inbox  session.Inbox
	runner *runner.Runner
	bus    *event.Bus

	mu   sync.Mutex
	runs map[string]*runHandle // sessionID -> corrida en vuelo (identidad por puntero)
	wg   sync.WaitGroup        // los tests esperan a las corridas; la UI es fire-and-forget
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
	a := &App{runs: map[string]*runHandle{}}
	a.bus = event.NewBus(emit)
	emitting := event.NewEmittingStore(store, a.bus)
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
	registry := tool.NewRegistry(tool.NewOutputStore(outputLimit), tool.Echo{},
		tool.NewReadToolWithSnapshotProvider(root, snaps), tool.NewWriteToolWithSnapshotProvider(root, snaps),
		tool.NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, snaps))
	a.runner = runner.NewRunner(emitting, a.inbox, provider, registry,
		tool.Permissions{"echo": true, "read": true, "write": true, "edit": true}, newIDGen())
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

// chooseProvider elige el provider real: si hay OPENROUTER_API_KEY usa el adaptador
// OpenAI-compatible contra OpenRouter (modelo de OPENROUTER_MODEL o defaultModel);
// si no, el demoProvider (fake) para que `wails dev` muestre streaming sin red.
func chooseProvider() llm.Provider {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		log.Print("atenea: sin OPENROUTER_API_KEY; usando provider de demo (sin red)")
		return demoProvider()
	}
	model := os.Getenv("OPENROUTER_MODEL")
	if model == "" {
		model = defaultModel
	}
	return llm.NewOpenAIProvider(key, openRouterBaseURL, model)
}

// startup guarda el ctx de Wails (lo usa la EmitFunc real).
func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// SendPrompt admite el texto como prompt en cola y arranca Run en una goroutine.
// Es el binding que el frontend llama al enviar.
func (a *App) SendPrompt(sessionID, text string) error {
	if err := a.inbox.Admit(context.Background(), sessionID,
		session.Prompt{Text: text}, session.DeliveryQueue); err != nil {
		return err
	}
	a.start(sessionID)
	return nil
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
