package main

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"

	"atenea/internal/event"
	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/session/runner"
	"atenea/internal/tool"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const outputLimit = 32 * 1024

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

// newApp arma la app con la frontera (emit) y el provider inyectados, para que
// los tests usen un emit fake y un provider guionado. El store real-pero-fake es
// MemoryStore decorado por EmittingStore (puente Store -> UI); el registry trae
// el builtin echo. M10 cambia provider/store por los reales sin tocar esto.
func newApp(provider llm.Provider, emit event.EmitFunc) *App {
	a := &App{runs: map[string]*runHandle{}}
	a.bus = event.NewBus(emit)
	store := event.NewEmittingStore(session.NewMemoryStore(), a.bus)
	a.inbox = session.NewMemoryInbox()
	registry := tool.NewRegistry(tool.NewOutputStore(outputLimit), tool.Echo{})
	a.runner = runner.NewRunner(store, a.inbox, provider, registry,
		tool.Permissions{"echo": true}, newIDGen())
	return a
}

// NewApp arma la app de produccion. La EmitFunc cierra sobre a para leer a.ctx
// (que startup fija despues): emitir antes de startup pasa un ctx nil, pero la UI
// no llama SendPrompt antes de cargar.
func NewApp() *App {
	var a *App
	emit := func(name string, data ...interface{}) {
		runtime.EventsEmit(a.ctx, name, data...)
	}
	a = newApp(demoProvider(), emit)
	return a
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
