package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
// de texto (Started, Delta, Delta, Ended) seguido de Step.Ended y afirma que el
// publisher persiste los cinco eventos con su Kind, que los deltas llevan su
// fragmento en Text sin materializar Message, y que Text.Ended lleva el texto
// completo concatenado pero SIN Message. El Message coalescido del asistente
// (con el assistantMessageID del turno) se materializa al cerrar el turno, en
// Step.Ended, no en Text.Ended: un turno produce un solo Message de assistant.
func TestPublisher_TextBlockEmitsDeltasAndFinalConcatenatedText(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1", 0)

	publishAll(t, p,
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "Hola, "},
		llm.Event{Kind: llm.TextDelta, Text: "mundo"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)

	wantKinds := []session.EventKind{
		session.KindTextStarted,
		session.KindTextDelta,
		session.KindTextDelta,
		session.KindTextEnded,
		session.KindStepEnded,
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

	// Text.Ended lleva el texto completo concatenado pero NO materializa Message:
	// el Message del assistant se coalesce al cerrar el turno (Step.Ended).
	ended := spy.events[3]
	if ended.Text != "Hola, mundo" {
		t.Errorf("Text.Ended.Text = %q, quiero %q", ended.Text, "Hola, mundo")
	}
	if ended.Message != nil {
		t.Errorf("Text.Ended.Message = %+v, quiero nil (se materializa en Step.Ended)", ended.Message)
	}

	// Step.Ended lleva el Message coalescido del asistente con el texto del turno.
	step := spy.events[4]
	wantMsg := &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "Hola, mundo"}
	if step.Message == nil {
		t.Fatalf("Step.Ended.Message = nil, quiero %+v", wantMsg)
	}
	if !reflect.DeepEqual(*step.Message, *wantMsg) {
		t.Errorf("Step.Ended.Message = %+v, quiero %+v", *step.Message, *wantMsg)
	}
}

func TestPublisher_StepStartedCarriesEstimatedInputTokens(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1", 1_234)

	publishAll(t, p, llm.Event{Kind: llm.StepStarted})

	if len(spy.events) != 1 {
		t.Fatalf("persisted events = %d, want 1", len(spy.events))
	}
	got := spy.events[0]
	if got.Kind != session.KindStepStarted || got.Usage == nil || got.Usage.InputTokens != 1_234 {
		t.Fatalf("Step.Started = %+v, want estimated input tokens 1234", got)
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
	p := NewPublisher(spy, "s1", "a1", 0)

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

// TestPublisher_ToolSuccessCarriesDiffNotInMessage afirma que el diff (solo-UI)
// viaja en SessionEvent.Diff pero NO en Message.Text: el modelo lee la proyeccion
// (Message), asi que el diff no entra a su contexto ni consume tokens. El evento
// debe seguir siendo json.Marshal-able (frontera Wails).
func TestPublisher_ToolSuccessCarriesDiffNotInMessage(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1", 0)

	if err := p.ToolSuccess(context.Background(), "c1", "out", "DIFF"); err != nil {
		t.Fatalf("ToolSuccess: %v", err)
	}

	if len(spy.events) != 1 {
		t.Fatalf("eventos = %d, quiero 1", len(spy.events))
	}
	ev := spy.events[0]
	if ev.Diff != "DIFF" {
		t.Errorf("ev.Diff = %q, quiero %q", ev.Diff, "DIFF")
	}
	if ev.Text != "out" {
		t.Errorf("ev.Text = %q, quiero %q", ev.Text, "out")
	}
	if ev.Message == nil || ev.Message.Text != "out" {
		t.Fatalf("Message.Text = %v, quiero %q (diff NO debe estar aqui)", ev.Message, "out")
	}
	if _, err := json.Marshal(ev); err != nil {
		t.Errorf("json.Marshal(Tool.Success con Diff): %v", err)
	}
}

// TestPublisher_ToolSuccessEmptyDiff: sin diff (bash) el campo queda vacio.
func TestPublisher_ToolSuccessEmptyDiff(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1", 0)

	if err := p.ToolSuccess(context.Background(), "c1", "ok", ""); err != nil {
		t.Fatalf("ToolSuccess: %v", err)
	}
	if spy.events[0].Diff != "" {
		t.Errorf("ev.Diff = %q, quiero vacio", spy.events[0].Diff)
	}
}

// TestPublisher_StepEndedCarriesUsageTokens alimenta StepStarted + StepEnded con
// tokens y afirma que el evento Step.Ended persistido lleva un *session.Usage no
// nil con los cinco campos copiados desde llm.Usage (espejo cruzando la
// frontera, sin acoplar session a llm).
func TestPublisher_StepEndedCarriesUsageTokens(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1", 0)

	publishAll(t, p,
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded, Usage: &llm.Usage{
			InputTokens:          10,
			OutputTokens:         20,
			ReasoningTokens:      5,
			CacheReadTokens:      3,
			CacheWriteTokens:     1,
			CacheableInputTokens: 14,
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
		InputTokens:          10,
		OutputTokens:         20,
		ReasoningTokens:      5,
		CacheReadTokens:      3,
		CacheWriteTokens:     1,
		CacheableInputTokens: 14,
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
	p := NewPublisher(spy, "s1", "a1", 0)

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
	p := NewPublisher(spy, "s1", "a1", 0)

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

// TestPublisher_ToolInputDeltaEventsAreJSONMarshalable fija la invariante de la
// frontera Wails: runtime.EventsEmit serializa cada SessionEvent con json.Marshal,
// asi que TODO evento emitido debe ser marshalable. Los fragmentos de input de una
// tool call llegan crudos y partidos (p. ej. `{"path":"` y `new.go"}`), cada uno
// JSON invalido por si mismo; si viajan en el campo json.RawMessage Input, Marshal
// revienta con "error calling MarshalJSON for type json.RawMessage". El test
// alimenta esos fragmentos y exige que cada SessionEvent emitido se marshalee sin
// error, y que el fragmento siga coalesciendo en Tool.Input.Ended.Input.
func TestPublisher_ToolInputDeltaEventsAreJSONMarshalable(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1", 0)

	publishAll(t, p,
		llm.Event{Kind: llm.ToolInputStarted, CallID: "c1"},
		llm.Event{Kind: llm.ToolInputDelta, CallID: "c1", Input: json.RawMessage(`{"path":"`)},
		llm.Event{Kind: llm.ToolInputDelta, CallID: "c1", Input: json.RawMessage(`new.go"}`)},
		llm.Event{Kind: llm.ToolInputEnded, CallID: "c1"},
	)

	for i, ev := range spy.events {
		if _, err := json.Marshal(ev); err != nil {
			t.Errorf("json.Marshal(events[%d] kind=%q): %v", i, ev.Kind, err)
		}
	}

	// El fragmento crudo viaja en Text (string seguro de marshalear), no en Input.
	delta := spy.events[1]
	if delta.Kind != session.KindToolInputDelta {
		t.Fatalf("events[1].Kind = %q, quiero %q", delta.Kind, session.KindToolInputDelta)
	}
	if delta.Text != `{"path":"` {
		t.Errorf("Tool.Input.Delta.Text = %q, quiero %q", delta.Text, `{"path":"`)
	}
	if len(delta.Input) != 0 {
		t.Errorf("Tool.Input.Delta.Input = %q, quiero vacio", string(delta.Input))
	}

	// Pese a viajar en Text, los fragmentos siguen coalesciendo en Tool.Input.Ended.
	ended := spy.events[3]
	wantInput := []byte(`{"path":"new.go"}`)
	if !bytes.Equal(ended.Input, wantInput) {
		t.Errorf("Tool.Input.Ended.Input = %q, quiero %q", string(ended.Input), string(wantInput))
	}
}

// TestPublisher_ToolInputDeltaFragmentWithSpecialCharsMarshalsLossless triangula
// con un fragmento que contiene caracteres que JSON debe re-escapar (comillas,
// backslash, salto de linea): al viajar en Text y marshalearse, json.Marshal los
// escapa y el round-trip recupera el fragmento crudo exacto; y el coalescido en
// Tool.Input.Ended conserva los bytes originales sin escapar.
func TestPublisher_ToolInputDeltaFragmentWithSpecialCharsMarshalsLossless(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1", 0)

	frag := `{"content":"a\"b\n` // comilla escapada + salto: invalido suelto, con chars especiales
	publishAll(t, p,
		llm.Event{Kind: llm.ToolInputStarted, CallID: "c1"},
		llm.Event{Kind: llm.ToolInputDelta, CallID: "c1", Input: json.RawMessage(frag)},
		llm.Event{Kind: llm.ToolInputDelta, CallID: "c1", Input: json.RawMessage(`c"}`)},
		llm.Event{Kind: llm.ToolInputEnded, CallID: "c1"},
	)

	delta := spy.events[1]
	raw, err := json.Marshal(delta)
	if err != nil {
		t.Fatalf("json.Marshal(Tool.Input.Delta): %v", err)
	}
	var back session.SessionEvent
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal round-trip: %v", err)
	}
	if back.Text != frag {
		t.Errorf("round-trip Text = %q, quiero %q", back.Text, frag)
	}

	ended := spy.events[3]
	wantInput := []byte(frag + `c"}`)
	if !bytes.Equal(ended.Input, wantInput) {
		t.Errorf("Tool.Input.Ended.Input = %q, quiero %q", string(ended.Input), string(wantInput))
	}
}

// TestPublisher_ToolCalledKeepsCompleteInputAndMarshals fija el otro lado: el JSON
// completo de una tool call SI viaja en Input (json.RawMessage) y, por ser valido,
// el evento Tool.Called se marshalea sin error y conserva el JSON crudo. Guarda
// que el fix solo saco de Input el fragmento del delta, no el input completo.
func TestPublisher_ToolCalledKeepsCompleteInputAndMarshals(t *testing.T) {
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s1", "a1", 0)

	full := json.RawMessage(`{"path":"new.go","content":"hi"}`)
	publishAll(t, p, llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "write", Input: full})

	called := spy.events[0]
	if called.Kind != session.KindToolCalled {
		t.Fatalf("Kind = %q, quiero %q", called.Kind, session.KindToolCalled)
	}
	if !bytes.Equal(called.Input, full) {
		t.Errorf("Tool.Called.Input = %q, quiero %q", string(called.Input), string(full))
	}
	if _, err := json.Marshal(called); err != nil {
		t.Errorf("json.Marshal(Tool.Called): %v", err)
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
	p := NewPublisher(store, "s1", "a1", 0)

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

// TestPublisher_ProjectsAssistantToolCallsAndToolResultID fija el round-trip de
// una tool call en la proyeccion: con el MemoryStore real como appender, publica
// un turno solo-tools (sin texto) y luego el resultado de la tool, y afirma que
// Messages proyecta DOS mensajes emparejados: primero el assistant con su tool
// call, luego el role tool con el tool_call_id que la empareja. Hoy el assistant
// con tool calls no se proyecta (un turno solo-tools no materializa Message de
// assistant) y el resultado no guarda su tool_call_id, asi que la proyeccion
// pierde el emparejamiento que el proveedor necesita.
func TestPublisher_ProjectsAssistantToolCallsAndToolResultID(t *testing.T) {
	store := session.NewMemoryStore()
	p := NewPublisher(store, "s1", "a1", 0)

	publishAll(t, p,
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "call_1", ToolName: "read", Input: json.RawMessage(`{"path":"foo.go"}`)},
		llm.Event{Kind: llm.StepEnded},
	)
	if err := p.ToolSuccess(context.Background(), "call_1", "contenido", ""); err != nil {
		t.Fatalf("ToolSuccess error inesperado: %v", err)
	}

	msgs, err := store.Messages(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("mensajes proyectados = %d, quiero 2 (assistant con tool call + tool result), got %+v", len(msgs), msgs)
	}

	asst := msgs[0]
	if asst.Role != session.RoleAssistant {
		t.Fatalf("msgs[0].Role = %q, quiero %q (el assistant con tool calls va primero)", asst.Role, session.RoleAssistant)
	}
	if len(asst.ToolCalls) != 1 {
		t.Fatalf("msgs[0].ToolCalls = %+v, quiero 1 tool call", asst.ToolCalls)
	}
	wantCall := session.ToolCall{ID: "call_1", Name: "read", Arguments: `{"path":"foo.go"}`}
	if !reflect.DeepEqual(asst.ToolCalls[0], wantCall) {
		t.Fatalf("msgs[0].ToolCalls[0] = %+v, quiero %+v", asst.ToolCalls[0], wantCall)
	}

	tool := msgs[1]
	if tool.Role != session.RoleTool {
		t.Fatalf("msgs[1].Role = %q, quiero %q (el resultado de la tool va segundo)", tool.Role, session.RoleTool)
	}
	if tool.ToolCallID != "call_1" {
		t.Fatalf("msgs[1].ToolCallID = %q, quiero %q", tool.ToolCallID, "call_1")
	}
	if tool.Text != "contenido" {
		t.Fatalf("msgs[1].Text = %q, quiero %q", tool.Text, "contenido")
	}

	// El pegamento: el tool result se empareja con la tool call del assistant.
	if tool.ToolCallID != asst.ToolCalls[0].ID {
		t.Fatalf("emparejamiento roto: msgs[1].ToolCallID = %q, msgs[0].ToolCalls[0].ID = %q", tool.ToolCallID, asst.ToolCalls[0].ID)
	}
}

// TestPublisher_CoalescesTextAndToolCallsIntoOneAssistantMessage fija que un turno
// con texto Y tool calls produce UN SOLO mensaje de assistant que lleva ambos: el
// texto coalescido y la tool call. Con el MemoryStore real como appender, publica
// StepStarted, un bloque de texto en deltas y una tool call, y afirma que Messages
// proyecta exactamente un mensaje del asistente con Text y ToolCalls juntos. Tumba
// un regreso a "dos mensajes separados" (texto y tools por separado) o a "texto XOR
// tools" (uno pisa al otro).
func TestPublisher_CoalescesTextAndToolCallsIntoOneAssistantMessage(t *testing.T) {
	store := session.NewMemoryStore()
	p := NewPublisher(store, "s1", "a1", 0)

	publishAll(t, p,
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "voy a leer "},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.ToolCall, CallID: "call_1", ToolName: "read", Input: json.RawMessage(`{"path":"foo.go"}`)},
		llm.Event{Kind: llm.StepEnded},
	)

	msgs, err := store.Messages(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("mensajes proyectados = %d, quiero 1 (texto y tools en UN solo assistant), got %+v", len(msgs), msgs)
	}

	got := msgs[0]
	if got.Role != session.RoleAssistant {
		t.Errorf("Message.Role = %q, quiero %q", got.Role, session.RoleAssistant)
	}
	if got.Text != "voy a leer " {
		t.Errorf("Message.Text = %q, quiero %q", got.Text, "voy a leer ")
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("Message.ToolCalls = %+v, quiero 1 tool call", got.ToolCalls)
	}
	wantCall := session.ToolCall{ID: "call_1", Name: "read", Arguments: `{"path":"foo.go"}`}
	if !reflect.DeepEqual(got.ToolCalls[0], wantCall) {
		t.Errorf("Message.ToolCalls[0] = %+v, quiero %+v", got.ToolCalls[0], wantCall)
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
	p := NewPublisher(failingAppender{}, "s1", "a1", 0)
	ctx := context.Background()

	err := p.Publish(ctx, llm.Event{Kind: llm.StepStarted})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Publish error = %v, quiero %v", err, errBoom)
	}
}
