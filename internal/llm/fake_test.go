package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// drain consume out con `for ev := range` hasta que el channel cierra y devuelve
// los eventos en orden. Que el bucle termine solo es la prueba de que Stream
// cerro el channel: si lo olvidara, drain se colgaria. Encapsula el patron de
// acumulacion que repiten los tests del fake.
func drain(out <-chan Event) []Event {
	var got []Event
	for ev := range out {
		got = append(got, ev)
	}
	return got
}

// TestFakeProvider_StreamEmitsScriptedEventsThenClosesChannel guiona un turno
// realista (Step/Reasoning/Text/Tool/StepEnded) y comprueba el contrato central
// del provider: Stream emite los eventos del guion en orden, con sus campos
// intactos, por un channel que CIERRA al terminar. Drenar con `for ev := range`
// hasta que el bucle termina solo demuestra el cierre del channel; comparar lo
// recibido contra el guion con reflect.DeepEqual demuestra orden y fidelidad.
func TestFakeProvider_StreamEmitsScriptedEventsThenClosesChannel(t *testing.T) {
	script := []Event{
		{Kind: StepStarted},

		{Kind: ReasoningStarted},
		{Kind: ReasoningDelta, Text: "pienso "},
		{Kind: ReasoningDelta, Text: "un poco"},
		{Kind: ReasoningEnded},

		{Kind: TextStarted},
		{Kind: TextDelta, Text: "Hola, "},
		{Kind: TextDelta, Text: "mundo"},
		{Kind: TextEnded},

		{
			Kind:     ToolCall,
			CallID:   "call-1",
			ToolName: "read_file",
			Input:    json.RawMessage(`{"path":"main.go"}`),
		},
		{Kind: ToolInputStarted, CallID: "call-1"},
		{Kind: ToolInputDelta, CallID: "call-1", Input: json.RawMessage(`{"path":`)},
		{Kind: ToolInputDelta, CallID: "call-1", Input: json.RawMessage(`"main.go"}`)},
		{Kind: ToolInputEnded, CallID: "call-1"},

		{Kind: StepEnded, Usage: &Usage{
			InputTokens:      120,
			OutputTokens:     34,
			ReasoningTokens:  12,
			CacheReadTokens:  8,
			CacheWriteTokens: 4,
		}},
	}

	fake := NewFakeProvider(script...)

	out, err := fake.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	if !reflect.DeepEqual(got, script) {
		t.Errorf("eventos recibidos no coinciden con el guion\n got: %#v\nwant: %#v", got, script)
	}
}

// TestFakeProvider_StreamEmptyScriptClosesImmediately cubre el caso del guion
// vacio (seccion 6 "Guion vacio"): Stream abre y cierra el channel sin emitir
// nada. Drenar con `for range` debe entregar CERO eventos y terminar solo, sin
// bloquearse. Tumba un fake que olvide cerrar el channel cuando no hay guion (el
// `for range` colgaria) o que fabrique eventos de la nada.
func TestFakeProvider_StreamEmptyScriptClosesImmediately(t *testing.T) {
	fake := NewFakeProvider()

	out, err := fake.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	count := len(drain(out))

	if count != 0 {
		t.Errorf("guion vacio entrego %d eventos, se esperaban 0", count)
	}
}

// TestFakeProvider_CanceledCtxCutsStream cubre la cancelacion como cut
// determinista (seccion 6 "Cancelacion (cut determinista)"): con un ctx ya
// cancelado antes de llamar a Stream, el chequeo ctx.Err() al tope de la primera
// iteracion corta antes de emitir. Drenar con `for range` debe entregar CERO
// eventos (estrictamente menos que len(script)) y el channel debe cerrar: el
// bucle termina solo y ninguna goroutine queda colgada. Pensado para -race.
func TestFakeProvider_CanceledCtxCutsStream(t *testing.T) {
	script := []Event{
		{Kind: StepStarted},
		{Kind: TextStarted},
		{Kind: TextDelta, Text: "no deberia llegar"},
		{Kind: TextEnded},
		{Kind: StepEnded, Usage: &Usage{}},
	}

	fake := NewFakeProvider(script...)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out, err := fake.Stream(ctx, Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	count := len(drain(out))

	if count >= len(script) {
		t.Errorf("ctx cancelado entrego %d eventos (>= len(script)=%d); el stream no se corto", count, len(script))
	}
}

// TestFakeProvider_StreamPreservesToolAndUsageFields cubre la fidelidad de los
// campos (seccion 6 "Fidelidad"): cada Event llega con CallID, ToolName, Input y
// Usage intactos; el fake no fabrica ni descarta campos. Guiona un ToolCall con
// Input json.RawMessage y un StepEnded con *Usage, drena, y assertea campo por
// campo. Tumba un fake debil que rellene un struct nuevo o pierda el puntero.
func TestFakeProvider_StreamPreservesToolAndUsageFields(t *testing.T) {
	input := json.RawMessage(`{"path":"main.go","limit":42}`)
	usage := &Usage{
		InputTokens:      120,
		OutputTokens:     34,
		ReasoningTokens:  12,
		CacheReadTokens:  8,
		CacheWriteTokens: 4,
	}
	script := []Event{
		{
			Kind:     ToolCall,
			CallID:   "call-42",
			ToolName: "read_file",
			Input:    input,
		},
		{Kind: StepEnded, Usage: usage},
	}

	fake := NewFakeProvider(script...)

	out, err := fake.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	if len(got) != 2 {
		t.Fatalf("se esperaban 2 eventos, llegaron %d: %#v", len(got), got)
	}

	tool := got[0]
	if tool.Kind != ToolCall {
		t.Errorf("Kind del tool call = %v, want %v", tool.Kind, ToolCall)
	}
	if tool.CallID != "call-42" {
		t.Errorf("CallID = %q, want %q", tool.CallID, "call-42")
	}
	if tool.ToolName != "read_file" {
		t.Errorf("ToolName = %q, want %q", tool.ToolName, "read_file")
	}
	if !bytes.Equal(tool.Input, input) {
		t.Errorf("Input = %q, want %q", string(tool.Input), string(input))
	}

	step := got[1]
	if step.Kind != StepEnded {
		t.Errorf("Kind del step ended = %v, want %v", step.Kind, StepEnded)
	}
	if step.Usage == nil {
		t.Fatalf("Usage es nil; se esperaba el puntero del guion")
	}
	if *step.Usage != *usage {
		t.Errorf("Usage = %+v, want %+v", *step.Usage, *usage)
	}
}

// TestFakeProvider_StreamIsReplayable cubre la inmutabilidad del guion (seccion 5
// "Guion inmutable" y criterio de aceptacion 7): el mismo FakeProvider llamado a
// Stream dos veces entrega el MISMO guion ambas veces. Drena dos veces y compara
// con reflect.DeepEqual. Tumba un fake que mute o consuma p.Script entre llamadas
// (lo necesita el loop multi-turno de M6).
func TestFakeProvider_StreamIsReplayable(t *testing.T) {
	script := []Event{
		{Kind: StepStarted},
		{Kind: TextStarted},
		{Kind: TextDelta, Text: "hola"},
		{Kind: TextEnded},
		{Kind: StepEnded, Usage: &Usage{InputTokens: 1, OutputTokens: 2}},
	}

	fake := NewFakeProvider(script...)

	streamOnce := func() []Event {
		out, err := fake.Stream(context.Background(), Request{})
		if err != nil {
			t.Fatalf("Stream devolvio error: %v", err)
		}
		return drain(out)
	}

	first := streamOnce()
	second := streamOnce()

	if !reflect.DeepEqual(first, second) {
		t.Errorf("las dos llamadas a Stream difieren\nfirst:  %#v\nsecond: %#v", first, second)
	}
	if !reflect.DeepEqual(first, script) {
		t.Errorf("la primera llamada no coincide con el guion\n got: %#v\nwant: %#v", first, script)
	}
}
