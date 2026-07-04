package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"atenea/internal/llm"
	"atenea/internal/session"
)

// turnProvider implementa llm.Provider con un guion POR TURNO: la i-esima
// llamada a Stream reproduce el i-esimo guion. Contrasta con llm.FakeProvider,
// que repite el mismo guion en cada Stream (loop infinito si el guion pide
// tools). Si los guiones se acaban, emite un turno de solo StepEnded para que
// la corrida cierre limpia. Protegido con mutex: el runner llama Stream desde
// su propia goroutine.
type turnProvider struct {
	mu    sync.Mutex
	turns [][]llm.Event
	next  int
}

var _ llm.Provider = (*turnProvider)(nil)

func newTurnProvider(turns ...[]llm.Event) *turnProvider {
	return &turnProvider{turns: turns}
}

func (p *turnProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	script := []llm.Event{{Kind: llm.StepEnded}}
	if p.next < len(p.turns) {
		script = p.turns[p.next]
		p.next++
	}
	p.mu.Unlock()

	out := make(chan llm.Event)
	go func() {
		defer close(out)
		for _, ev := range script {
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out, nil
}

// nextMsg saca el siguiente mensaje del canal del engine, con timeout generoso
// para no ser flaky. Falla el test si el canal se cierra o vence el timeout.
func nextMsg(t *testing.T, ch <-chan tea.Msg, timeout time.Duration) tea.Msg {
	t.Helper()
	select {
	case <-time.After(timeout):
		t.Fatalf("timeout de %v esperando el siguiente mensaje del engine", timeout)
		return nil
	case msg, ok := <-ch:
		if !ok {
			t.Fatal("canal del engine cerrado antes de tiempo")
		}
		return msg
	}
}

// resolveUntilStopped entrega la decision del permiso via el API publico del
// engine, reintentando en segundo plano hasta que el test lo detenga. El
// reintento elimina una carrera real: el runner publica
// Tool.Permission.Requested ANTES de que gate.Ask registre la solicitud, asi
// que una entrega unica podria adelantarse al registro y perderse (el gate
// descarta decisiones sin Ask pendiente). Reintentar es inocuo: la entrega
// efectiva retira la solicitud del gate y los reintentos posteriores son no-op.
func resolveUntilStopped(e *Engine, sessionID, callID string, approved bool) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			e.ResolvePermission(sessionID, callID, approved)
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

// gatedBashTurns arma el guion de dos turnos del escenario ask-before-run: el
// turno 1 pide la tool gateada bash con ese comando y el turno 2 responde texto.
func gatedBashTurns(command string) [][]llm.Event {
	input, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		panic(err) // un map[string]string siempre marshalea
	}
	return [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: input},
			{Kind: llm.StepEnded},
		},
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "listo"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}
}

// collectUntilRunDone consume el canal del engine en el goroutine del test:
// acumula los EventMsg hasta ver el RunDoneMsg y los devuelve, tomando cada
// mensaje con nextMsg (que falla si el canal se cierra o vence el timeout).
// onEvent (opcional) se invoca con cada evento al llegar; los tests lo usan
// para reaccionar a mitad de corrida (resolver un permiso, detener la sesion).
func collectUntilRunDone(t *testing.T, ch <-chan tea.Msg, timeout time.Duration, onEvent func(session.SessionEvent)) ([]session.SessionEvent, RunDoneMsg) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var events []session.SessionEvent
	for {
		switch m := nextMsg(t, ch, time.Until(deadline)).(type) {
		case EventMsg:
			ev := session.SessionEvent(m)
			events = append(events, ev)
			if onEvent != nil {
				onEvent(ev)
			}
		case RunDoneMsg:
			return events, m
		default:
			t.Fatalf("mensaje inesperado en el canal del engine: %T", m)
		}
	}
}

// lastEvent devuelve el ultimo evento con ese Kind y CallID, o nil si no llego.
func lastEvent(events []session.SessionEvent, kind session.EventKind, callID string) *session.SessionEvent {
	var found *session.SessionEvent
	for i, ev := range events {
		if ev.Kind == kind && ev.CallID == callID {
			found = &events[i]
		}
	}
	return found
}

func TestEngine_StreamsSessionEventsAndSignalsRunDone(t *testing.T) {
	// Un turno de solo texto: el guion completo de una corrida limpia.
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "Hola desde el engine"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)

	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: fake, Store: session.NewMemoryStore()})

	if err := e.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt(s1, hola) = %v, se esperaba nil", err)
	}

	events, done := collectUntilRunDone(t, e.Events(), 5*time.Second, nil)

	var sawUserPrompt bool // (a) el prompt promovido a mensaje user durable
	var sawTextDelta bool  // (b) al menos un Text.Delta
	var deltas strings.Builder
	var sawStepEnded bool // (c) el cierre del turno
	for _, ev := range events {
		if ev.Message != nil && ev.Message.Role == session.RoleUser && ev.Message.Text == "hola" {
			sawUserPrompt = true
		}
		if ev.Kind == session.KindTextDelta {
			sawTextDelta = true
			deltas.WriteString(ev.Text)
		}
		if ev.Kind == session.KindStepEnded {
			sawStepEnded = true
		}
	}

	if !sawUserPrompt {
		t.Errorf("no llego el mensaje user promovido con Text %q entre %d eventos", "hola", len(events))
	}
	if !sawTextDelta {
		t.Errorf("no llego ningun evento %s", session.KindTextDelta)
	} else if got := deltas.String(); !strings.Contains(got, "Hola desde el engine") {
		t.Errorf("texto acumulado de %s = %q, debe contener %q", session.KindTextDelta, got, "Hola desde el engine")
	}
	if !sawStepEnded {
		t.Errorf("no llego ningun evento %s", session.KindStepEnded)
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, se esperaba corrida limpia (Err vacio)", done.Err)
	}
}

func TestEngine_GatedBashApprovedRunsAndSettles(t *testing.T) {
	provider := newTurnProvider(gatedBashTurns("echo hola-gate")...)
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if err := e.SendPrompt("s1", "corre el comando"); err != nil {
		t.Fatalf("SendPrompt(s1, corre el comando) = %v, se esperaba nil", err)
	}

	// Al ver la solicitud de permiso, el usuario APRUEBA la tool.
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			t.Cleanup(resolveUntilStopped(e, ev.SessionID, "c1", true))
		}
	})

	success := lastEvent(events, session.KindToolSuccess, "c1")
	if success == nil {
		t.Fatalf("no llego ningun %s de c1 entre %d eventos: la tool aprobada debe ejecutarse y asentarse", session.KindToolSuccess, len(events))
	}
	if !strings.Contains(success.Text, "hola-gate") {
		t.Errorf("Tool.Success de c1 con Text = %q, debe contener %q (bash ejecuto de verdad)", success.Text, "hola-gate")
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, se esperaba corrida limpia (Err vacio)", done.Err)
	}
}

func TestEngine_GatedBashDeniedFailsWithoutRunning(t *testing.T) {
	root := t.TempDir()
	forbidden := filepath.Join(root, "no-debe-existir")
	provider := newTurnProvider(gatedBashTurns("touch " + forbidden)...)
	e := NewEngine(EngineConfig{Root: root, Provider: provider, Store: session.NewMemoryStore()})

	if err := e.SendPrompt("s1", "corre el comando"); err != nil {
		t.Fatalf("SendPrompt(s1, corre el comando) = %v, se esperaba nil", err)
	}

	// Al ver la solicitud de permiso, el usuario DENIEGA la tool.
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			t.Cleanup(resolveUntilStopped(e, ev.SessionID, "c1", false))
		}
	})

	if ev := lastEvent(events, session.KindToolSuccess, "c1"); ev != nil {
		t.Fatalf("llego %s de c1 con Text %q: una tool denegada NO debe ejecutarse", session.KindToolSuccess, ev.Text)
	}
	failed := lastEvent(events, session.KindToolFailed, "c1")
	if failed == nil {
		t.Fatalf("no llego ningun %s de c1 entre %d eventos: la denegacion debe asentar la tool como fallida", session.KindToolFailed, len(events))
	}
	if !strings.Contains(strings.ToLower(failed.Error), "deni") {
		t.Errorf("Tool.Failed de c1 con Error = %q, debe mencionar la denegacion", failed.Error)
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, la denegacion no es un fallo de la corrida (Err vacio)", done.Err)
	}
	// La prueba dura de que bash NO corrio: el archivo que el comando tocaria
	// no debe existir tras el fin de la corrida.
	if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%q) = %v, el archivo no debe existir: la tool denegada no debe ejecutar el comando", forbidden, err)
	}
}

func TestEngine_StopUnblocksPendingPermission(t *testing.T) {
	// Un solo turno: la tool gateada queda esperando aprobacion para siempre;
	// Stop debe desbloquearla y cerrar la corrida limpia.
	provider := newTurnProvider([]llm.Event{
		{Kind: llm.StepStarted},
		{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"echo bloqueado"}`)},
		{Kind: llm.StepEnded},
	})
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if err := e.SendPrompt("s1", "corre el comando"); err != nil {
		t.Fatalf("SendPrompt(s1, corre el comando) = %v, se esperaba nil", err)
	}

	// En vez de decidir, el usuario detiene la corrida.
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			e.Stop("s1")
		}
	})

	if lastEvent(events, session.KindToolFailed, "c1") == nil {
		t.Errorf("no llego ningun %s de c1: Stop debe asentar la call pendiente como interrumpida", session.KindToolFailed)
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, una cancelacion deliberada es cierre limpio (Err vacio)", done.Err)
	}
}
