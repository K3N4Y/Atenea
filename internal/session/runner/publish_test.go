package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"atenea/internal/llm"
	"atenea/internal/session"
)

// recordingAppender es un spy de eventAppender: captura cada SessionEvent que el
// publisher apende, asignandole un Seq incremental. Asi el test afirma la
// secuencia y el payload de los eventos durables sin un Store real.
type recordingAppender struct {
	events []session.SessionEvent
	seq    session.Seq
}

func (r *recordingAppender) AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error) {
	r.seq++
	ev.SessionID = sessionID
	ev.Seq = r.seq
	r.events = append(r.events, ev)
	return r.seq, nil
}

// publishAll drena un guion de llm.Event por el publisher, fallando el test si
// algun Publish devuelve error. Concentra el caso comun (guion que no espera
// error); los tests del camino de error siguen llamando Publish a mano.
func publishAll(t *testing.T, p *Publisher, evs ...llm.Event) {
	t.Helper()
	ctx := context.Background()
	for _, ev := range evs {
		if err := p.Publish(ctx, ev); err != nil {
			t.Fatalf("Publish(%v) error inesperado: %v", ev.Kind, err)
		}
	}
}

// TestPublisher_TextBlockEmitsDeltasAndFinalConcatenatedText alimenta un bloque
// de texto (Started, Delta, Delta, Ended) y afirma que el publisher persiste los
// cuatro eventos con su Kind, que los deltas llevan su fragmento en Text sin
// materializar Message, y que Text.Ended lleva el texto completo concatenado y
// el Message coalescido del asistente con el assistantMessageID del turno.
func TestPublisher_TextBlockEmitsDeltasAndFinalConcatenatedText(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1")

	publishAll(t, p,
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "Hola, "},
		llm.Event{Kind: llm.TextDelta, Text: "mundo"},
		llm.Event{Kind: llm.TextEnded},
	)

	wantKinds := []session.EventKind{
		session.KindTextStarted,
		session.KindTextDelta,
		session.KindTextDelta,
		session.KindTextEnded,
	}
	if len(spy.events) != len(wantKinds) {
		t.Fatalf("eventos persistidos = %d, quiero %d", len(spy.events), len(wantKinds))
	}
	for i, want := range wantKinds {
		if got := spy.events[i].Kind; got != want {
			t.Errorf("events[%d].Kind = %q, quiero %q", i, got, want)
		}
	}

	// Los dos deltas llevan su fragmento en Text y no materializan Message.
	deltas := []struct {
		idx  int
		text string
	}{
		{1, "Hola, "},
		{2, "mundo"},
	}
	for _, d := range deltas {
		ev := spy.events[d.idx]
		if ev.Text != d.text {
			t.Errorf("events[%d].Text = %q, quiero %q", d.idx, ev.Text, d.text)
		}
		if ev.Message != nil {
			t.Errorf("events[%d].Message = %+v, quiero nil", d.idx, ev.Message)
		}
	}

	// Text.Ended lleva el texto completo concatenado y el Message coalescido.
	ended := spy.events[3]
	if ended.Text != "Hola, mundo" {
		t.Errorf("Text.Ended.Text = %q, quiero %q", ended.Text, "Hola, mundo")
	}
	wantMsg := &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "Hola, mundo"}
	if ended.Message == nil {
		t.Fatalf("Text.Ended.Message = nil, quiero %+v", wantMsg)
	}
	if *ended.Message != *wantMsg {
		t.Errorf("Text.Ended.Message = %+v, quiero %+v", *ended.Message, *wantMsg)
	}
}

// TestPublisher_ReasoningBuffersLikeText alimenta un bloque de razonamiento
// (Started, Delta, Delta, Ended) y afirma que el publisher lo bufferiza igual
// que el texto: persiste los cuatro eventos con su Kind Reasoning.*, y
// Reasoning.Ended lleva el texto completo concatenado. A diferencia del texto,
// el razonamiento NO se proyecta como mensaje: Reasoning.Ended deja Message ==
// nil (no es contenido conversacional de la proyeccion).
func TestPublisher_ReasoningBuffersLikeText(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1")

	publishAll(t, p,
		llm.Event{Kind: llm.ReasoningStarted},
		llm.Event{Kind: llm.ReasoningDelta, Text: "pien"},
		llm.Event{Kind: llm.ReasoningDelta, Text: "so"},
		llm.Event{Kind: llm.ReasoningEnded},
	)

	wantKinds := []session.EventKind{
		session.KindReasoningStarted,
		session.KindReasoningDelta,
		session.KindReasoningDelta,
		session.KindReasoningEnded,
	}
	if len(spy.events) != len(wantKinds) {
		t.Fatalf("eventos persistidos = %d, quiero %d", len(spy.events), len(wantKinds))
	}
	for i, want := range wantKinds {
		if got := spy.events[i].Kind; got != want {
			t.Errorf("events[%d].Kind = %q, quiero %q", i, got, want)
		}
	}

	// Reasoning.Ended lleva el razonamiento completo concatenado y no materializa
	// Message: el razonamiento no entra en la proyeccion.
	ended := spy.events[3]
	if ended.Text != "pienso" {
		t.Errorf("Reasoning.Ended.Text = %q, quiero %q", ended.Text, "pienso")
	}
	if ended.Message != nil {
		t.Errorf("Reasoning.Ended.Message = %+v, quiero nil (el razonamiento no se proyecta)", ended.Message)
	}
}

// TestPublisher_StepEndedCarriesUsageTokens alimenta StepStarted + StepEnded con
// tokens y afirma que el evento Step.Ended persistido lleva un *session.Usage no
// nil con los cinco campos copiados desde llm.Usage (espejo cruzando la
// frontera, sin acoplar session a llm).
func TestPublisher_StepEndedCarriesUsageTokens(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1")

	publishAll(t, p,
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded, Usage: &llm.Usage{
			InputTokens:      10,
			OutputTokens:     20,
			ReasoningTokens:  5,
			CacheReadTokens:  3,
			CacheWriteTokens: 1,
		}},
	)

	if len(spy.events) != 2 {
		t.Fatalf("eventos persistidos = %d, quiero 2", len(spy.events))
	}
	if got := spy.events[0].Kind; got != session.KindStepStarted {
		t.Errorf("events[0].Kind = %q, quiero %q", got, session.KindStepStarted)
	}

	ended := spy.events[1]
	if ended.Kind != session.KindStepEnded {
		t.Errorf("events[1].Kind = %q, quiero %q", ended.Kind, session.KindStepEnded)
	}
	if ended.Usage == nil {
		t.Fatalf("Step.Ended.Usage = nil, quiero los cinco tokens")
	}
	want := session.Usage{
		InputTokens:      10,
		OutputTokens:     20,
		ReasoningTokens:  5,
		CacheReadTokens:  3,
		CacheWriteTokens: 1,
	}
	if *ended.Usage != want {
		t.Errorf("Step.Ended.Usage = %+v, quiero %+v", *ended.Usage, want)
	}
}

// TestPublisher_StepEndedWithoutUsageIsNil afirma el borde complementario: un
// StepEnded sin tokens (llm.Usage nil) deja el Step.Ended persistido con Usage
// == nil. Asi se descarta una implementacion que materialice un Usage vacio en
// lugar de propagar el nil.
func TestPublisher_StepEndedWithoutUsageIsNil(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1")

	publishAll(t, p, llm.Event{Kind: llm.StepEnded})

	if len(spy.events) != 1 {
		t.Fatalf("eventos persistidos = %d, quiero 1", len(spy.events))
	}
	ended := spy.events[0]
	if ended.Kind != session.KindStepEnded {
		t.Errorf("Kind = %q, quiero %q", ended.Kind, session.KindStepEnded)
	}
	if ended.Usage != nil {
		t.Errorf("Step.Ended.Usage = %+v, quiero nil", ended.Usage)
	}
}

// TestPublisher_ToolCallAndInputDeltasCoalesce alimenta una tool call y sus
// fragmentos de input (ToolCall + Input.Delta + Input.Delta + Input.Ended) y
// afirma que el publisher persiste los cuatro eventos con su Kind, que
// Tool.Called lleva CallID/ToolName, y que Tool.Input.Ended lleva el input JSON
// completo concatenado a partir de los deltas.
func TestPublisher_ToolCallAndInputDeltasCoalesce(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1")

	publishAll(t, p,
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "read", Input: nil},
		llm.Event{Kind: llm.ToolInputDelta, CallID: "c1", Input: json.RawMessage(`{"path":`)},
		llm.Event{Kind: llm.ToolInputDelta, CallID: "c1", Input: json.RawMessage(`"/x"}`)},
		llm.Event{Kind: llm.ToolInputEnded, CallID: "c1"},
	)

	wantKinds := []session.EventKind{
		session.KindToolCalled,
		session.KindToolInputDelta,
		session.KindToolInputDelta,
		session.KindToolInputEnded,
	}
	if len(spy.events) != len(wantKinds) {
		t.Fatalf("eventos persistidos = %d, quiero %d", len(spy.events), len(wantKinds))
	}
	for i, want := range wantKinds {
		if got := spy.events[i].Kind; got != want {
			t.Errorf("events[%d].Kind = %q, quiero %q", i, got, want)
		}
	}

	// Tool.Called identifica la tool call por CallID y ToolName.
	called := spy.events[0]
	if called.CallID != "c1" {
		t.Errorf("Tool.Called.CallID = %q, quiero %q", called.CallID, "c1")
	}
	if called.ToolName != "read" {
		t.Errorf("Tool.Called.ToolName = %q, quiero %q", called.ToolName, "read")
	}

	// Tool.Input.Ended lleva el input completo concatenado de los deltas.
	ended := spy.events[3]
	wantInput := []byte(`{"path":"/x"}`)
	if !bytes.Equal(ended.Input, wantInput) {
		t.Errorf("Tool.Input.Ended.Input = %q, quiero %q", string(ended.Input), string(wantInput))
	}
}

// TestPublisher_ProjectsCoalescedAssistantMessage es la atadura M3 <-> M1: con el
// MemoryStore real como appender, publica un turno (StepStarted, un bloque de
// texto en deltas, StepEnded) y afirma que la proyeccion Messages devuelve
// EXACTAMENTE un mensaje del asistente con el texto coalescido y el
// assistantMessageID del turno. Demuestra que los deltas (Message == nil) no
// contaminan la proyeccion y que el coalescido del publisher aterriza en el fold
// de M1 sin tocarlo.
func TestPublisher_ProjectsCoalescedAssistantMessage(t *testing.T) {
	store := session.NewMemoryStore()
	p := NewPublisher(store, "s1", "a1")

	publishAll(t, p,
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "a"},
		llm.Event{Kind: llm.TextDelta, Text: "b"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)

	msgs, err := store.Messages(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("mensajes proyectados = %d, quiero 1 (los deltas no deben contaminar)", len(msgs))
	}
	got := msgs[0]
	if got.ID != "a1" {
		t.Errorf("Message.ID = %q, quiero %q", got.ID, "a1")
	}
	if got.Role != session.RoleAssistant {
		t.Errorf("Message.Role = %q, quiero %q", got.Role, session.RoleAssistant)
	}
	if got.Text != "ab" {
		t.Errorf("Message.Text = %q, quiero %q", got.Text, "ab")
	}
}

// errBoom es el error fijo que devuelve failingAppender, para afirmar que Publish
// lo propaga sin envolverlo.
var errBoom = errors.New("boom")

// failingAppender es un eventAppender que siempre falla: prueba el camino de
// error de Publish (el publisher solo propaga lo que devuelve AppendEvent).
type failingAppender struct{}

func (failingAppender) AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error) {
	return 0, errBoom
}

// TestPublisher_AppendErrorPropagates afirma que si el store falla al apender,
// Publish devuelve ese mismo error (sin tragarlo ni reemplazarlo).
func TestPublisher_AppendErrorPropagates(t *testing.T) {
	p := NewPublisher(failingAppender{}, "s1", "a1")
	ctx := context.Background()

	err := p.Publish(ctx, llm.Event{Kind: llm.StepStarted})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Publish error = %v, quiero %v", err, errBoom)
	}
}
