package tui

import (
	"context"
	"errors"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"atenea/internal/event"
	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/session/runner"
	"atenea/internal/tool"
	"atenea/internal/wiring"
)

// EngineConfig describe el ensamblado del agente headless: la raiz del
// workspace, el proveedor LLM, el store durable y el modo local.
type EngineConfig struct {
	Root     string
	Provider llm.Provider
	Store    session.Store
	Local    bool
}

// Engine es el agente headless que arma runner + tools + permisos sin Wails y
// publica los eventos durables de la sesion por un canal de mensajes Bubble Tea.
// El ensamblado real vive en wiring.Build (la misma fuente de verdad que la app
// Wails); aqui solo se cablea la frontera: Bus -> canal de la TUI.
type Engine struct {
	events chan tea.Msg
	inbox  session.Inbox
	gate   *session.MemoryPermissionGate
	runner *runner.Runner

	mu    sync.Mutex
	runs  map[string]*engineRun   // sessionID -> corrida en vuelo (identidad por puntero)
	modes map[string]session.Mode // sessionID -> modo (normal/plan); guardado con mu como runs
}

// engineRun identifica una corrida en vuelo. Se compara por puntero porque
// context.CancelFunc no es comparable.
type engineRun struct{ cancel context.CancelFunc }

// Fija en compilacion que Engine satisface la interface Agent de la TUI.
var _ Agent = (*Engine)(nil)

// NewEngine arma el engine a partir de la configuracion: una EmitFunc que
// puentea los SessionEvent durables del Bus al canal de la TUI, el store
// decorado con EmittingStore sobre ese bus, y el agente completo via
// wiring.Build (tools, skills, subagentes, runner con ask-before-run).
func NewEngine(cfg EngineConfig) *Engine {
	e := &Engine{
		// Buffer generoso: amortigua rafagas de deltas mientras la TUI drena.
		events: make(chan tea.Msg, 256),
		inbox:  session.NewMemoryInbox(),
		gate:   session.NewMemoryPermissionGate(),
		runs:   map[string]*engineRun{},
		modes:  map[string]session.Mode{},
	}
	// La frontera: donde la app Wails emite a runtime.EventsEmit, aqui el evento
	// durable va al canal de la TUI. El send bloqueante es deliberado: la TUI
	// drena el canal en continuo, asi no se pierden eventos bajo rafagas.
	emit := func(name string, data ...interface{}) {
		if len(data) == 0 {
			return
		}
		if ev, ok := data[0].(session.SessionEvent); ok {
			e.events <- EventMsg(ev)
		}
	}
	bus := event.NewBus(emit)
	built := wiring.Build(wiring.Config{
		Root:     cfg.Root,
		Provider: cfg.Provider,
		Store:    event.NewEmittingStore(cfg.Store, bus),
		Inbox:    e.inbox,
		Gate:     e.gate,
		Snaps:    tool.NewSessionSnapshots(),
		Bus:      bus,
		Local:    cfg.Local,
		NextID:   wiring.NewIDGen(),
		Mode:     e.modeFor, // el runner consulta el modo por sesion al inicio de cada turno
	})
	e.runner = built.Runner
	return e
}

// modeFor devuelve el modo de la sesion (normal/plan). Es el hook Mode de
// wiring.Build: el runner lo consulta al inicio de cada turno. Guarda e.modes
// con mu, igual que runs (espejo de App.modeFor).
func (e *Engine) modeFor(sessionID string) session.Mode {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.modes[sessionID]
}

// setMode fija el modo de la sesion. Se llama SIEMPRE antes de send
// (adquisiciones de lock separadas, nunca anidadas) porque send tambien toma
// e.mu (espejo de App.setMode).
func (e *Engine) setMode(sessionID string, m session.Mode) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.modes[sessionID] = m
}

// SendPrompt encola un prompt del usuario por el camino normal. Fija modo
// normal primero: una sesion que estaba en plan-mode vuelve a las tools
// normales al enviar (el modo es por envio, como en la app Wails).
func (e *Engine) SendPrompt(sessionID, text string) error {
	e.setMode(sessionID, session.ModeNormal)
	return e.send(sessionID, text)
}

// SendPlanPrompt encola el prompt en plan-mode: investigacion de solo lectura
// mas present_plan, con el contrato de plan-mode en el system prompt. Fija
// ModePlan antes de arrancar (espejo de App.SendPlanPrompt).
func (e *Engine) SendPlanPrompt(sessionID, text string) error {
	e.setMode(sessionID, session.ModePlan)
	return e.send(sessionID, text)
}

// send encola el prompt y dispara la corrida en una goroutine con un ctx
// cancelable registrado por sesion (espejo de App.start): si habia una corrida
// previa de la misma sesion, se cancela. Al terminar la corrida se limpia el
// handle y se publica RunDoneMsg; una cancelacion deliberada (Stop, follow-up)
// es un cierre limpio, no un error. Es el camino comun de SendPrompt y
// SendPlanPrompt: el modo de la sesion ya quedo fijado antes de llamarlo.
func (e *Engine) send(sessionID, text string) error {
	if err := e.inbox.Admit(context.Background(), sessionID,
		session.Prompt{Text: text}, session.DeliveryQueue); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &engineRun{cancel: cancel}
	e.mu.Lock()
	if old := e.runs[sessionID]; old != nil {
		old.cancel() // una corrida previa de la misma sesion no debe quedar viva
	}
	e.runs[sessionID] = h
	e.mu.Unlock()

	go func() {
		err := e.runner.Run(ctx, sessionID, false)
		e.clear(sessionID, h)
		msg := ""
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			msg = err.Error()
		}
		e.events <- RunDoneMsg{Err: msg}
	}()
	return nil
}

// acceptPlanPrompt es el prompt fijo de implementacion del plan aprobado (espejo del const homonimo de app.go).
const acceptPlanPrompt = "El plan fue aprobado. Implementalo ahora paso a paso siguiendo el plan."

// AcceptPlan acepta y ejecuta el plan: vuelve la sesion a modo normal y
// promueve el prompt fijo de implementacion como prompt del usuario,
// arrancando la corrida (espejo de App.AcceptPlan).
func (e *Engine) AcceptPlan(sessionID string) error {
	e.setMode(sessionID, session.ModeNormal)
	return e.send(sessionID, acceptPlanPrompt)
}

// ResolvePermission resuelve una solicitud de permiso pendiente (ask-before-run).
func (e *Engine) ResolvePermission(sessionID, callID string, approved bool) {
	e.gate.Resolve(sessionID, callID, approved)
}

// Stop interrumpe la corrida en curso de la sesion. No-op si no corre.
func (e *Engine) Stop(sessionID string) {
	e.mu.Lock()
	h := e.runs[sessionID]
	e.mu.Unlock()
	if h != nil {
		h.cancel()
	}
}

// clear saca del mapa la corrida h solo si sigue siendo la vigente (un
// SendPrompt mas nuevo pudo reemplazarla).
func (e *Engine) clear(sessionID string, h *engineRun) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runs[sessionID] == h {
		delete(e.runs, sessionID)
	}
}

// Events entrega los EventMsg durables de la corrida y un RunDoneMsg al
// terminar cada corrida.
func (e *Engine) Events() <-chan tea.Msg { return e.events }
