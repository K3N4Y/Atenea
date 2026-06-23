package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestOpenAIProvider_StreamEmitsTextDelta cubre el comportamiento central del
// provider real OpenAI-compatible: habla con un endpoint de chat completions via
// streaming SSE y traduce el texto incremental a llm.Event de Kind TextDelta. El
// servidor de prueba (httptest) devuelve un stream SSE estilo OpenAI con un chunk
// que trae delta.content == "Hola" y un chunk final con finish_reason "stop"; el
// base URL inyectado evita tocar la red real. Drenar con `for ev := range out`
// hasta que el bucle termina demuestra ademas que el provider CIERRA el channel.
// Aqui solo se asserta el TextDelta; StepStarted/TextStarted/TextEnded/StepEnded
// quedan para TRIANGULATE.
func TestOpenAIProvider_StreamEmitsTextDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hola\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	found := false
	for _, ev := range got {
		if ev.Kind == TextDelta && ev.Text == "Hola" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no se encontro un evento TextDelta con Text==%q; eventos recibidos: %#v", "Hola", got)
	}
}

// TestOpenAIProvider_StreamEmitsReasoningDelta cubre el comportamiento central del
// razonamiento: cuando el SSE trae un chunk con el campo extendido de OpenRouter
// delta.reasoning (un string), el provider debe traducirlo a un llm.Event de Kind
// ReasoningDelta con Text igual a ese string. Hoy el provider solo lee
// delta.content e ignora delta.reasoning, asi que el ThinkingBlock del frontend
// nunca se ve. El servidor de prueba (httptest) devuelve un stream SSE con un chunk
// que trae delta.reasoning == "Pensando en la respuesta", luego un chunk con
// delta.content para realismo, y un chunk final con finish_reason "stop". Aqui solo
// se asserta el ReasoningDelta; el bracketing Started/Ended y el orden relativo al
// texto quedan para TRIANGULATE, igual que TextDelta solo cubre el delta central.
func TestOpenAIProvider_StreamEmitsReasoningDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning\":\"Pensando en la respuesta\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hola\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	found := false
	for _, ev := range got {
		if ev.Kind == ReasoningDelta && ev.Text == "Pensando en la respuesta" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no se encontro un evento ReasoningDelta con Text==%q; eventos recibidos: %#v", "Pensando en la respuesta", got)
	}
}

// TestOpenAIProvider_StreamBracketsReasoningThenText exige el bracketing COMPLETO
// del razonamiento, espejando el del texto y respetando el orden: el razonamiento
// va ANTES del texto. StepStarted abre el turno; el primer delta.reasoning abre el
// bloque con ReasoningStarted (una sola vez); cada delta.reasoning no vacio emite un
// ReasoningDelta con su fragmento; cuando empieza el delta.content se CIERRA el
// razonamiento con ReasoningEnded ANTES de abrir el texto con TextStarted; cada
// delta.content emite un TextDelta; el finish_reason cierra el texto con TextEnded;
// y StepEnded cierra el turno. La secuencia exacta de Kinds tumba la impl actual,
// que emitia ReasoningDelta sueltos sin Started/Ended ni orden definido.
func TestOpenAIProvider_StreamBracketsReasoningThenText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning\":\"Pensando\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\" mas\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hola\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" mundo\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	wantKinds := []EventKind{
		StepStarted,
		ReasoningStarted, ReasoningDelta, ReasoningDelta, ReasoningEnded,
		TextStarted, TextDelta, TextDelta, TextEnded,
		StepEnded,
	}
	if len(got) != len(wantKinds) {
		t.Fatalf("cantidad de eventos: got %d, want %d; eventos: %#v", len(got), len(wantKinds), got)
	}
	for i, want := range wantKinds {
		if got[i].Kind != want {
			t.Fatalf("evento[%d].Kind: got %v, want %v; secuencia: %#v", i, got[i].Kind, want, got)
		}
	}

	if got[2].Text != "Pensando" {
		t.Fatalf("primer ReasoningDelta.Text: got %q, want %q", got[2].Text, "Pensando")
	}
	if got[3].Text != " mas" {
		t.Fatalf("segundo ReasoningDelta.Text: got %q, want %q", got[3].Text, " mas")
	}
	if got[6].Text != "Hola" {
		t.Fatalf("primer TextDelta.Text: got %q, want %q", got[6].Text, "Hola")
	}
	if got[7].Text != " mundo" {
		t.Fatalf("segundo TextDelta.Text: got %q, want %q", got[7].Text, " mundo")
	}
}

// TestOpenAIProvider_StreamRequestsReasoning exige que Stream pida el razonamiento a
// OpenRouter: el body del POST debe llevar un campo top-level `reasoning` que sea un
// objeto con `enabled == true`. Sin esto OpenRouter no emite delta.reasoning y el
// ThinkingBlock nunca tiene contenido. El handler captura el body del POST y el test
// navega el JSON crudo, igual que TestOpenAIProvider_StreamSendsMappedRequest. Esto
// tumba la impl actual, que no manda reasoning en el request.
func TestOpenAIProvider_StreamRequestsReasoning(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}
	drain(out)

	if body == nil {
		t.Fatalf("el handler no capturo el body del POST")
	}

	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("no se pudo parsear el body enviado: %v; body: %s", err, body)
	}

	reasoning, ok := sent["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning: got %#v, want un objeto", sent["reasoning"])
	}
	if reasoning["enabled"] != true {
		t.Fatalf("reasoning.enabled: got %v, want true", reasoning["enabled"])
	}
}

// TestOpenAIProvider_StreamBracketsTextTurn exige el bracketing COMPLETO de un
// turno de texto: StepStarted abre el turno antes de cualquier delta; el primer
// delta.content abre el bloque con TextStarted; cada delta no vacio emite un
// TextDelta con su fragmento; el finish_reason cierra el bloque con TextEnded; y
// StepEnded cierra el turno cargando el Usage del chunk final (choices vacio,
// stream_options.include_usage). La secuencia exacta de Kinds y el Usage tumban
// la impl minima, que solo emitia TextDelta sueltos.
func TestOpenAIProvider_StreamBracketsTextTurn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hola\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" mundo\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	wantKinds := []EventKind{StepStarted, TextStarted, TextDelta, TextDelta, TextEnded, StepEnded}
	if len(got) != len(wantKinds) {
		t.Fatalf("cantidad de eventos: got %d, want %d; eventos: %#v", len(got), len(wantKinds), got)
	}
	for i, want := range wantKinds {
		if got[i].Kind != want {
			t.Fatalf("evento[%d].Kind: got %v, want %v; secuencia: %#v", i, got[i].Kind, want, got)
		}
	}

	if got[2].Text != "Hola" {
		t.Fatalf("primer TextDelta.Text: got %q, want %q", got[2].Text, "Hola")
	}
	if got[3].Text != " mundo" {
		t.Fatalf("segundo TextDelta.Text: got %q, want %q", got[3].Text, " mundo")
	}

	stepEnded := got[len(got)-1]
	if stepEnded.Usage == nil {
		t.Fatalf("StepEnded.Usage es nil; se esperaba el usage del chunk final")
	}
	if stepEnded.Usage.InputTokens != 10 {
		t.Fatalf("StepEnded.Usage.InputTokens: got %d, want %d", stepEnded.Usage.InputTokens, 10)
	}
	if stepEnded.Usage.OutputTokens != 5 {
		t.Fatalf("StepEnded.Usage.OutputTokens: got %d, want %d", stepEnded.Usage.OutputTokens, 5)
	}
}

// TestOpenAIProvider_StreamEmitsStepFailedOnError exige que un fallo del stream
// (status 500) se traduzca a un evento StepFailed y que NO se cierre el turno con
// StepEnded: un turno que fallo no debe parecer exitoso. Esto tumba cualquier impl
// que ignore stream.Err() o que emita StepEnded incondicionalmente.
func TestOpenAIProvider_StreamEmitsStepFailedOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	foundFailed := false
	for _, ev := range got {
		if ev.Kind == StepFailed {
			foundFailed = true
		}
		if ev.Kind == StepEnded {
			t.Fatalf("no se esperaba StepEnded tras un fallo; eventos: %#v", got)
		}
	}
	if !foundFailed {
		t.Fatalf("no se encontro un evento StepFailed; eventos: %#v", got)
	}
}

// TestOpenAIProvider_StreamEmitsToolCall exige que Stream parsee los tool_calls
// del stream OpenAI/OpenRouter y emita un llm.Event de Kind ToolCall con el CallID,
// el ToolName y el Input JSON completo. La forma del stream es la del SDK: el primer
// delta de un index trae id y function.name con arguments vacio; los siguientes solo
// fragmentos de function.arguments; el turno cierra con finish_reason "tool_calls".
// El provider debe acumular los fragmentos de arguments hasta tener el JSON entero.
// Las lineas SSE usan raw string literals (backticks) para que las comillas escapadas
// del JSON de arguments queden literales sin pelear con el doble escaping de Go. Esto
// tumba la impl actual, que ignora delta.tool_calls (TODO(tool-calls)) y nunca emite
// un ToolCall. Aqui solo se asserta el ToolCall central; el bracketing ToolInput
// Started/Delta/Ended queda para TRIANGULATE.
func TestOpenAIProvider_StreamEmitsToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"foo.go\"}"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	var toolCall *Event
	for i := range got {
		if got[i].Kind == ToolCall {
			toolCall = &got[i]
			break
		}
	}
	if toolCall == nil {
		t.Fatalf("no se encontro un evento ToolCall; eventos recibidos: %#v", got)
	}
	if toolCall.CallID != "call_1" {
		t.Fatalf("ToolCall.CallID: got %q, want %q", toolCall.CallID, "call_1")
	}
	if toolCall.ToolName != "read" {
		t.Fatalf("ToolCall.ToolName: got %q, want %q", toolCall.ToolName, "read")
	}
	if string(toolCall.Input) != `{"path":"foo.go"}` {
		t.Fatalf("ToolCall.Input: got %q, want %q", string(toolCall.Input), `{"path":"foo.go"}`)
	}
}

// TestOpenAIProvider_StreamEmitsToolInputDeltas exige que el provider streamee el
// input incremental de una tool call ademas del ToolCall final: al primer fragmento
// de un index emite ToolInputStarted con el CallID; por cada fragmento crudo de
// function.arguments emite un ToolInputDelta con el CallID y ese fragmento literal en
// Input; al cerrar el turno emite ToolInputEnded con el CallID; y conserva el ToolCall
// final con el Input JSON completo. La subsecuencia filtrada de los eventos de la tool
// debe ser ToolInputStarted -> ToolInputDelta(s) -> ToolInputEnded -> ToolCall, con los
// Delta trayendo los fragmentos crudos tal como llegaron en el stream. Esto tumba la
// impl actual, que solo emite el ToolCall completo al final sin ToolInput*. Las lineas
// SSE usan raw string literals para dejar literales las comillas escapadas del JSON.
func TestOpenAIProvider_StreamEmitsToolInputDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"foo.go\"}"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	// Filtra solo los eventos relacionados a la tool, conservando su orden.
	var toolEvents []Event
	for _, ev := range got {
		switch ev.Kind {
		case ToolInputStarted, ToolInputDelta, ToolInputEnded, ToolCall:
			toolEvents = append(toolEvents, ev)
		}
	}

	wantKinds := []EventKind{ToolInputStarted, ToolInputDelta, ToolInputDelta, ToolInputEnded, ToolCall}
	if len(toolEvents) != len(wantKinds) {
		t.Fatalf("cantidad de eventos de tool: got %d, want %d; eventos: %#v", len(toolEvents), len(wantKinds), got)
	}
	for i, want := range wantKinds {
		if toolEvents[i].Kind != want {
			t.Fatalf("evento de tool[%d].Kind: got %v, want %v; subsecuencia: %#v", i, toolEvents[i].Kind, want, toolEvents)
		}
	}

	if toolEvents[0].CallID != "call_1" {
		t.Fatalf("ToolInputStarted.CallID: got %q, want %q", toolEvents[0].CallID, "call_1")
	}

	d1 := toolEvents[1]
	if d1.CallID != "call_1" {
		t.Fatalf("primer ToolInputDelta.CallID: got %q, want %q", d1.CallID, "call_1")
	}
	if string(d1.Input) != `{"path":` {
		t.Fatalf("primer ToolInputDelta.Input: got %q, want %q", string(d1.Input), `{"path":`)
	}

	d2 := toolEvents[2]
	if d2.CallID != "call_1" {
		t.Fatalf("segundo ToolInputDelta.CallID: got %q, want %q", d2.CallID, "call_1")
	}
	if string(d2.Input) != `"foo.go"}` {
		t.Fatalf("segundo ToolInputDelta.Input: got %q, want %q", string(d2.Input), `"foo.go"}`)
	}

	if toolEvents[3].CallID != "call_1" {
		t.Fatalf("ToolInputEnded.CallID: got %q, want %q", toolEvents[3].CallID, "call_1")
	}

	tc := toolEvents[4]
	if tc.CallID != "call_1" {
		t.Fatalf("ToolCall.CallID: got %q, want %q", tc.CallID, "call_1")
	}
	if tc.ToolName != "read" {
		t.Fatalf("ToolCall.ToolName: got %q, want %q", tc.ToolName, "read")
	}
	if string(tc.Input) != `{"path":"foo.go"}` {
		t.Fatalf("ToolCall.Input: got %q, want %q", string(tc.Input), `{"path":"foo.go"}`)
	}
}

// TestOpenAIProvider_StreamBracketsToolCallTurn exige el bracketing COMPLETO de un
// turno de SOLO tool call (sin texto): StepStarted abre el turno; el input incremental
// se streamea con ToolInputStarted -> ToolInputDelta(s) -> ToolInputEnded; el ToolCall
// se emite tras cerrar el stream y antes de StepEnded; y StepEnded carga el Usage del
// chunk final. Aqui los args llegan completos en un solo delta, asi que hay un solo
// ToolInputDelta. La secuencia exacta de Kinds tumba una impl que abra TextStarted/Ended
// sin contenido, que emita el ToolCall en el lugar equivocado, que no streamee el input
// o que se trague el Usage. Las lineas SSE usan raw string literals para dejar literales
// las comillas escapadas del JSON de arguments.
func TestOpenAIProvider_StreamBracketsToolCallTurn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"foo.go\"}"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	wantKinds := []EventKind{StepStarted, ToolInputStarted, ToolInputDelta, ToolInputEnded, ToolCall, StepEnded}
	if len(got) != len(wantKinds) {
		t.Fatalf("cantidad de eventos: got %d, want %d; eventos: %#v", len(got), len(wantKinds), got)
	}
	for i, want := range wantKinds {
		if got[i].Kind != want {
			t.Fatalf("evento[%d].Kind: got %v, want %v; secuencia: %#v", i, got[i].Kind, want, got)
		}
	}

	tc := got[4]
	if tc.CallID != "call_1" {
		t.Fatalf("ToolCall.CallID: got %q, want %q", tc.CallID, "call_1")
	}
	if tc.ToolName != "read" {
		t.Fatalf("ToolCall.ToolName: got %q, want %q", tc.ToolName, "read")
	}
	if string(tc.Input) != `{"path":"foo.go"}` {
		t.Fatalf("ToolCall.Input: got %q, want %q", string(tc.Input), `{"path":"foo.go"}`)
	}

	stepEnded := got[len(got)-1]
	if stepEnded.Usage == nil {
		t.Fatalf("StepEnded.Usage es nil; se esperaba el usage del chunk final")
	}
	if stepEnded.Usage.InputTokens != 10 {
		t.Fatalf("StepEnded.Usage.InputTokens: got %d, want %d", stepEnded.Usage.InputTokens, 10)
	}
	if stepEnded.Usage.OutputTokens != 5 {
		t.Fatalf("StepEnded.Usage.OutputTokens: got %d, want %d", stepEnded.Usage.OutputTokens, 5)
	}
}

// TestOpenAIProvider_StreamEmitsMultipleToolCalls exige que dos tool calls paralelas
// del mismo turno (index 0 e index 1, cada una en su propio chunk) se emitan como
// EXACTAMENTE dos eventos ToolCall, en el orden en que aparecieron sus index. Esto
// tumba una impl que solo maneje un index, que pierda una de las llamadas o que no
// respete el orden de aparicion. Las lineas SSE usan raw string literals para dejar
// literales las comillas escapadas del JSON de arguments.
func TestOpenAIProvider_StreamEmitsMultipleToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.go\"}"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"echo","arguments":"{\"text\":\"hi\"}"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	var calls []Event
	for _, ev := range got {
		if ev.Kind == ToolCall {
			calls = append(calls, ev)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("cantidad de ToolCall: got %d, want 2; eventos: %#v", len(calls), got)
	}

	if calls[0].CallID != "call_a" {
		t.Fatalf("primer ToolCall.CallID: got %q, want %q", calls[0].CallID, "call_a")
	}
	if calls[0].ToolName != "read" {
		t.Fatalf("primer ToolCall.ToolName: got %q, want %q", calls[0].ToolName, "read")
	}
	if string(calls[0].Input) != `{"path":"a.go"}` {
		t.Fatalf("primer ToolCall.Input: got %q, want %q", string(calls[0].Input), `{"path":"a.go"}`)
	}

	if calls[1].CallID != "call_b" {
		t.Fatalf("segundo ToolCall.CallID: got %q, want %q", calls[1].CallID, "call_b")
	}
	if calls[1].ToolName != "echo" {
		t.Fatalf("segundo ToolCall.ToolName: got %q, want %q", calls[1].ToolName, "echo")
	}
	if string(calls[1].Input) != `{"text":"hi"}` {
		t.Fatalf("segundo ToolCall.Input: got %q, want %q", string(calls[1].Input), `{"text":"hi"}`)
	}
}

// TestOpenAIProvider_StreamToolCallAfterText exige que un turno donde el modelo narra
// y LUEGO llama a una tool cierre el texto antes de streamear el input de la tool: la
// secuencia exacta de Kinds [StepStarted, TextStarted, TextDelta, TextEnded,
// ToolInputStarted, ToolInputDelta, ToolInputEnded, ToolCall, StepEnded] prueba que el
// bloque de texto se CIERRA con TextEnded cuando llegan los tool_calls, y que el input
// incremental y el ToolCall van DESPUES de TextEnded y ANTES de StepEnded. Esto tumba
// una impl que emita el ToolCall dentro del bloque de texto, que no cierre el texto, que
// no streamee el input o que invierta el orden ToolCall/StepEnded. Las lineas SSE usan
// raw string literals para dejar literales las comillas escapadas del JSON de arguments.
func TestOpenAIProvider_StreamToolCallAfterText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Voy a leer"},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"x\"}"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	wantKinds := []EventKind{StepStarted, TextStarted, TextDelta, TextEnded, ToolInputStarted, ToolInputDelta, ToolInputEnded, ToolCall, StepEnded}
	if len(got) != len(wantKinds) {
		t.Fatalf("cantidad de eventos: got %d, want %d; eventos: %#v", len(got), len(wantKinds), got)
	}
	for i, want := range wantKinds {
		if got[i].Kind != want {
			t.Fatalf("evento[%d].Kind: got %v, want %v; secuencia: %#v", i, got[i].Kind, want, got)
		}
	}

	if got[2].Text != "Voy a leer" {
		t.Fatalf("TextDelta.Text: got %q, want %q", got[2].Text, "Voy a leer")
	}

	tc := got[7]
	if tc.CallID != "call_1" {
		t.Fatalf("ToolCall.CallID: got %q, want %q", tc.CallID, "call_1")
	}
	if tc.ToolName != "read" {
		t.Fatalf("ToolCall.ToolName: got %q, want %q", tc.ToolName, "read")
	}
	if string(tc.Input) != `{"path":"x"}` {
		t.Fatalf("ToolCall.Input: got %q, want %q", string(tc.Input), `{"path":"x"}`)
	}
}

// TestOpenAIProvider_StreamSendsMappedRequest exige que Stream construya el
// request real OpenAI a partir del Request: resuelve el modelo por defecto cuando
// req.Model esta vacio, mapea los Messages por Role/Text y materializa los Tools
// como function tools. El handler captura el body del POST y el test navega el
// JSON crudo. Esto tumba la impl minima, que ignoraba Messages/Tools y mandaba un
// request casi vacio.
func TestOpenAIProvider_StreamSendsMappedRequest(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{
		Model: "",
		Messages: []Message{
			{Role: "user", Text: "hola"},
			{Role: "assistant", Text: "hey"},
		},
		Tools: []ToolDef{
			{Name: "echo", Description: "echoes", Schema: json.RawMessage(`{"type":"object"}`)},
		},
	})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}
	drain(out)

	if body == nil {
		t.Fatalf("el handler no capturo el body del POST")
	}

	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("no se pudo parsear el body enviado: %v; body: %s", err, body)
	}

	if sent["model"] != "test-model" {
		t.Fatalf("model: got %v, want %q (debio resolver el default)", sent["model"], "test-model")
	}

	messages, ok := sent["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages: got %#v, want 2 entradas", sent["messages"])
	}
	m0 := messages[0].(map[string]any)
	if m0["role"] != "user" || m0["content"] != "hola" {
		t.Fatalf("messages[0]: got role=%v content=%v, want user/hola", m0["role"], m0["content"])
	}
	m1 := messages[1].(map[string]any)
	if m1["role"] != "assistant" || m1["content"] != "hey" {
		t.Fatalf("messages[1]: got role=%v content=%v, want assistant/hey", m1["role"], m1["content"])
	}

	tools, ok := sent["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools: got %#v, want 1 entrada", sent["tools"])
	}
	tool0 := tools[0].(map[string]any)
	fn, ok := tool0["function"].(map[string]any)
	if !ok {
		t.Fatalf("tools[0].function: got %#v, want objeto", tool0["function"])
	}
	if fn["name"] != "echo" {
		t.Fatalf("tools[0].function.name: got %v, want %q", fn["name"], "echo")
	}
}

// TestOpenAIProvider_StreamSerializesToolRoundTrip exige que toOpenAIMessages
// serialice el round-trip de tool calls que la API OpenAI-compatible
// (OpenAI/OpenRouter) requiere para un historial multi-paso: un mensaje
// assistant con tool_calls (cada uno con id, function.name y function.arguments
// como string JSON crudo) seguido de un mensaje role:"tool" con tool_call_id que
// empareje. Hoy el provider mapea el rol "tool" a UserMessage y nunca emite los
// tool_calls del assistant, asi que contra un modelo real el historial queda mal
// formado y la API devuelve 400. El handler captura el body del POST y el test
// navega el JSON crudo, igual que TestOpenAIProvider_StreamSendsMappedRequest.
func TestOpenAIProvider_StreamSerializesToolRoundTrip(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{
		Messages: []Message{
			{Role: "user", Text: "lee el archivo"},
			{Role: "assistant", Text: "", ToolCalls: []ToolCallPart{
				{ID: "call_1", Name: "read", Arguments: json.RawMessage(`{"path":"foo.go"}`)},
			}},
			{Role: "tool", ToolCallID: "call_1", Text: "contenido del archivo"},
		},
	})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}
	drain(out)

	if body == nil {
		t.Fatalf("el handler no capturo el body del POST")
	}

	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("no se pudo parsear el body enviado: %v; body: %s", err, body)
	}

	messages, ok := sent["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("messages: got %#v, want 3 entradas", sent["messages"])
	}

	// messages[1]: assistant con tool_calls[0] que lleva id, function.name y
	// function.arguments como string JSON crudo.
	mAssistant := messages[1].(map[string]any)
	if mAssistant["role"] != "assistant" {
		t.Fatalf("messages[1].role: got %v, want %q", mAssistant["role"], "assistant")
	}
	toolCalls, ok := mAssistant["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("messages[1].tool_calls: got %#v, want 1 entrada", mAssistant["tool_calls"])
	}
	tc0 := toolCalls[0].(map[string]any)
	if tc0["id"] != "call_1" {
		t.Fatalf("messages[1].tool_calls[0].id: got %v, want %q", tc0["id"], "call_1")
	}
	fn, ok := tc0["function"].(map[string]any)
	if !ok {
		t.Fatalf("messages[1].tool_calls[0].function: got %#v, want objeto", tc0["function"])
	}
	if fn["name"] != "read" {
		t.Fatalf("messages[1].tool_calls[0].function.name: got %v, want %q", fn["name"], "read")
	}
	if fn["arguments"] != `{"path":"foo.go"}` {
		t.Fatalf("messages[1].tool_calls[0].function.arguments: got %v, want %q (string JSON crudo)", fn["arguments"], `{"path":"foo.go"}`)
	}

	// messages[2]: role:"tool" con tool_call_id que empareja y el contenido del
	// resultado.
	mTool := messages[2].(map[string]any)
	if mTool["role"] != "tool" {
		t.Fatalf("messages[2].role: got %v, want %q", mTool["role"], "tool")
	}
	if mTool["tool_call_id"] != "call_1" {
		t.Fatalf("messages[2].tool_call_id: got %v, want %q", mTool["tool_call_id"], "call_1")
	}
	if mTool["content"] != "contenido del archivo" {
		t.Fatalf("messages[2].content: got %v, want %q", mTool["content"], "contenido del archivo")
	}

	// El pegamento: el tool_call_id del resultado debe emparejar con el id de la
	// tool call del assistant, o la API lo rechaza.
	if mTool["tool_call_id"] != tc0["id"] {
		t.Fatalf("emparejamiento: messages[2].tool_call_id (%v) != messages[1].tool_calls[0].id (%v)", mTool["tool_call_id"], tc0["id"])
	}
}

// TestOpenAIProvider_StreamInterleavesToolInputByCall exige que dos tool calls
// paralelas cuyos fragmentos de function.arguments llegan INTERCALADOS por index
// se atribuyan correctamente por CallID, sin mezclar el input de una con la otra.
// El stream abre ambos index (call_a en 0, call_b en 1) y luego va alternando
// fragmentos: 0,1,0,1. Esto tumba una impl que acumule en un solo buffer global,
// que pierda la asociacion index->CallID, o que concatene los fragmentos en el
// orden de llegada en vez de por tool call. Se afirma: exactamente un
// ToolInputStarted por CallID; que concatenar los ToolInputDelta.Input de cada
// CallID reconstruye su JSON propio; y que el volcado final, en orden de index, es
// ToolInputEnded(call_a), ToolCall(call_a), ToolInputEnded(call_b), ToolCall(call_b).
// Las lineas SSE usan raw string literals para dejar literales las comillas
// escapadas del JSON de arguments.
func TestOpenAIProvider_StreamInterleavesToolInputByCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"echo","arguments":""}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"text\":"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"hi\"}"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	// Exactamente un ToolInputStarted por CallID, sin duplicados ni mezclas.
	startedByCall := map[string]int{}
	for _, ev := range got {
		if ev.Kind == ToolInputStarted {
			startedByCall[ev.CallID]++
		}
	}
	if startedByCall["call_a"] != 1 {
		t.Fatalf("ToolInputStarted de call_a: got %d, want 1; eventos: %#v", startedByCall["call_a"], got)
	}
	if startedByCall["call_b"] != 1 {
		t.Fatalf("ToolInputStarted de call_b: got %d, want 1; eventos: %#v", startedByCall["call_b"], got)
	}
	if len(startedByCall) != 2 {
		t.Fatalf("CallIDs con ToolInputStarted: got %v, want solo call_a y call_b", startedByCall)
	}

	// Concatenar los ToolInputDelta de cada CallID reconstruye su JSON propio:
	// la atribucion por CallID no debe entremezclar los fragmentos.
	inputByCall := map[string]string{}
	for _, ev := range got {
		if ev.Kind == ToolInputDelta {
			inputByCall[ev.CallID] += string(ev.Input)
		}
	}
	if inputByCall["call_a"] != `{"path":"a.go"}` {
		t.Fatalf("input concatenado de call_a: got %q, want %q", inputByCall["call_a"], `{"path":"a.go"}`)
	}
	if inputByCall["call_b"] != `{"text":"hi"}` {
		t.Fatalf("input concatenado de call_b: got %q, want %q", inputByCall["call_b"], `{"text":"hi"}`)
	}

	// El volcado final, en orden de index, es Ended(call_a), ToolCall(call_a),
	// Ended(call_b), ToolCall(call_b). Se filtra solo Ended/ToolCall y se verifica
	// el orden relativo con CallID y ToolName/Input.
	type tail struct {
		kind   EventKind
		callID string
	}
	var got_tail []tail
	for _, ev := range got {
		if ev.Kind == ToolInputEnded || ev.Kind == ToolCall {
			got_tail = append(got_tail, tail{ev.Kind, ev.CallID})
		}
	}
	wantTail := []tail{
		{ToolInputEnded, "call_a"},
		{ToolCall, "call_a"},
		{ToolInputEnded, "call_b"},
		{ToolCall, "call_b"},
	}
	if !reflect.DeepEqual(got_tail, wantTail) {
		t.Fatalf("orden del volcado final: got %#v, want %#v; eventos: %#v", got_tail, wantTail, got)
	}

	// Las ToolCall finales llevan ToolName e Input completos y correctos.
	var calls []Event
	for _, ev := range got {
		if ev.Kind == ToolCall {
			calls = append(calls, ev)
		}
	}
	if calls[0].ToolName != "read" || string(calls[0].Input) != `{"path":"a.go"}` {
		t.Fatalf("ToolCall call_a: got name=%q input=%q, want read/%q", calls[0].ToolName, string(calls[0].Input), `{"path":"a.go"}`)
	}
	if calls[1].ToolName != "echo" || string(calls[1].Input) != `{"text":"hi"}` {
		t.Fatalf("ToolCall call_b: got name=%q input=%q, want echo/%q", calls[1].ToolName, string(calls[1].Input), `{"text":"hi"}`)
	}
}

// TestOpenAIProvider_StreamToolCallWithoutArgsEmitsNoDelta exige que una tool call
// cuyos arguments vienen vacios (el modelo no manda ningun fragmento de args, p.ej.
// una tool sin parametros) NO emita ningun ToolInputDelta: no se emiten deltas
// vacios. La subsecuencia de eventos de tool debe ser EXACTAMENTE ToolInputStarted,
// ToolInputEnded, ToolCall, con el ToolCall final llevando Input vacio. Esto tumba
// una impl que emita un ToolInputDelta por cada fragmento incluido el vacio, o que
// no maneje el caso de args ausentes. Las lineas SSE usan raw string literals.
func TestOpenAIProvider_StreamToolCallWithoutArgsEmitsNoDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}

	got := drain(out)

	// Filtra solo los eventos relacionados a la tool, conservando su orden.
	var toolEvents []Event
	for _, ev := range got {
		switch ev.Kind {
		case ToolInputStarted, ToolInputDelta, ToolInputEnded, ToolCall:
			toolEvents = append(toolEvents, ev)
		}
	}

	wantKinds := []EventKind{ToolInputStarted, ToolInputEnded, ToolCall}
	if len(toolEvents) != len(wantKinds) {
		t.Fatalf("cantidad de eventos de tool: got %d, want %d (no debe emitirse ToolInputDelta vacio); eventos: %#v", len(toolEvents), len(wantKinds), got)
	}
	for i, want := range wantKinds {
		if toolEvents[i].Kind != want {
			t.Fatalf("evento de tool[%d].Kind: got %v, want %v; subsecuencia: %#v", i, toolEvents[i].Kind, want, toolEvents)
		}
	}

	if toolEvents[0].CallID != "call_1" {
		t.Fatalf("ToolInputStarted.CallID: got %q, want %q", toolEvents[0].CallID, "call_1")
	}
	if toolEvents[1].CallID != "call_1" {
		t.Fatalf("ToolInputEnded.CallID: got %q, want %q", toolEvents[1].CallID, "call_1")
	}

	toolCall := toolEvents[2]
	if toolCall.CallID != "call_1" {
		t.Fatalf("ToolCall.CallID: got %q, want %q", toolCall.CallID, "call_1")
	}
	if toolCall.ToolName != "read" {
		t.Fatalf("ToolCall.ToolName: got %q, want %q", toolCall.ToolName, "read")
	}
	if string(toolCall.Input) != "" {
		t.Fatalf("ToolCall.Input: got %q, want vacio", string(toolCall.Input))
	}
}

// TestOpenAIProvider_StreamSerializesMultipleToolCallsRoundTrip exige que
// toOpenAIMessages serialice el round-trip de MULTIPLES tool calls paralelas en un
// solo turno del assistant: dos tool_calls (call_a/read y call_b/echo) seguidos de
// dos mensajes role:"tool", cada uno con el tool_call_id que empareja. Esto tumba
// una impl que solo serialice una tool call, que pierda el orden o que cruce la
// atribucion entre llamadas. El handler captura el body del POST y el test navega
// el JSON crudo, igual que TestOpenAIProvider_StreamSerializesToolRoundTrip.
func TestOpenAIProvider_StreamSerializesMultipleToolCallsRoundTrip(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{
		Messages: []Message{
			{Role: "user", Text: "lee dos archivos"},
			{Role: "assistant", Text: "", ToolCalls: []ToolCallPart{
				{ID: "call_a", Name: "read", Arguments: json.RawMessage(`{"path":"a.go"}`)},
				{ID: "call_b", Name: "echo", Arguments: json.RawMessage(`{"text":"hi"}`)},
			}},
			{Role: "tool", ToolCallID: "call_a", Text: "contenido a"},
			{Role: "tool", ToolCallID: "call_b", Text: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}
	drain(out)

	if body == nil {
		t.Fatalf("el handler no capturo el body del POST")
	}

	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("no se pudo parsear el body enviado: %v; body: %s", err, body)
	}

	messages, ok := sent["messages"].([]any)
	if !ok || len(messages) != 4 {
		t.Fatalf("messages: got %#v, want 4 entradas", sent["messages"])
	}

	// messages[1]: assistant con dos tool_calls en orden, cada uno con id,
	// function.name y function.arguments como string JSON crudo.
	mAssistant := messages[1].(map[string]any)
	if mAssistant["role"] != "assistant" {
		t.Fatalf("messages[1].role: got %v, want %q", mAssistant["role"], "assistant")
	}
	toolCalls, ok := mAssistant["tool_calls"].([]any)
	if !ok || len(toolCalls) != 2 {
		t.Fatalf("messages[1].tool_calls: got %#v, want 2 entradas", mAssistant["tool_calls"])
	}

	tc0 := toolCalls[0].(map[string]any)
	if tc0["id"] != "call_a" {
		t.Fatalf("messages[1].tool_calls[0].id: got %v, want %q", tc0["id"], "call_a")
	}
	fn0, ok := tc0["function"].(map[string]any)
	if !ok {
		t.Fatalf("messages[1].tool_calls[0].function: got %#v, want objeto", tc0["function"])
	}
	if fn0["name"] != "read" {
		t.Fatalf("messages[1].tool_calls[0].function.name: got %v, want %q", fn0["name"], "read")
	}
	if fn0["arguments"] != `{"path":"a.go"}` {
		t.Fatalf("messages[1].tool_calls[0].function.arguments: got %v, want %q", fn0["arguments"], `{"path":"a.go"}`)
	}

	tc1 := toolCalls[1].(map[string]any)
	if tc1["id"] != "call_b" {
		t.Fatalf("messages[1].tool_calls[1].id: got %v, want %q", tc1["id"], "call_b")
	}
	fn1, ok := tc1["function"].(map[string]any)
	if !ok {
		t.Fatalf("messages[1].tool_calls[1].function: got %#v, want objeto", tc1["function"])
	}
	if fn1["name"] != "echo" {
		t.Fatalf("messages[1].tool_calls[1].function.name: got %v, want %q", fn1["name"], "echo")
	}
	if fn1["arguments"] != `{"text":"hi"}` {
		t.Fatalf("messages[1].tool_calls[1].function.arguments: got %v, want %q", fn1["arguments"], `{"text":"hi"}`)
	}

	// messages[2] y messages[3]: cada tool result empareja con su tool call por
	// tool_call_id, en orden.
	mToolA := messages[2].(map[string]any)
	if mToolA["role"] != "tool" {
		t.Fatalf("messages[2].role: got %v, want %q", mToolA["role"], "tool")
	}
	if mToolA["tool_call_id"] != "call_a" {
		t.Fatalf("messages[2].tool_call_id: got %v, want %q", mToolA["tool_call_id"], "call_a")
	}
	if mToolA["content"] != "contenido a" {
		t.Fatalf("messages[2].content: got %v, want %q", mToolA["content"], "contenido a")
	}

	mToolB := messages[3].(map[string]any)
	if mToolB["role"] != "tool" {
		t.Fatalf("messages[3].role: got %v, want %q", mToolB["role"], "tool")
	}
	if mToolB["tool_call_id"] != "call_b" {
		t.Fatalf("messages[3].tool_call_id: got %v, want %q", mToolB["tool_call_id"], "call_b")
	}
	if mToolB["content"] != "hi" {
		t.Fatalf("messages[3].content: got %v, want %q", mToolB["content"], "hi")
	}
}

// TestOpenAIProvider_StreamSerializesAssistantTextAndToolCall exige que un mensaje
// assistant que narra Y llama a una tool serialice AMBAS cosas: el content con el
// texto ("voy a leer foo") y los tool_calls con la llamada. Esto tumba una impl que
// trate texto y tool_calls como excluyentes (que pierda el content cuando hay tool
// calls, o que ignore los tool calls cuando hay texto). El handler captura el body
// del POST y el test navega el JSON crudo, igual que
// TestOpenAIProvider_StreamSerializesToolRoundTrip.
func TestOpenAIProvider_StreamSerializesAssistantTextAndToolCall(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider("test-key", server.URL, "test-model")

	out, err := p.Stream(context.Background(), Request{
		Messages: []Message{
			{Role: "user", Text: "lee foo"},
			{Role: "assistant", Text: "voy a leer foo", ToolCalls: []ToolCallPart{
				{ID: "call_1", Name: "read", Arguments: json.RawMessage(`{"path":"foo"}`)},
			}},
			{Role: "tool", ToolCallID: "call_1", Text: "contenido"},
		},
	})
	if err != nil {
		t.Fatalf("Stream devolvio error: %v", err)
	}
	drain(out)

	if body == nil {
		t.Fatalf("el handler no capturo el body del POST")
	}

	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("no se pudo parsear el body enviado: %v; body: %s", err, body)
	}

	messages, ok := sent["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("messages: got %#v, want 3 entradas", sent["messages"])
	}

	// messages[1]: assistant que lleva content (el texto SI se serializa) Y
	// tool_calls (la tool call no se pierde por haber texto).
	mAssistant := messages[1].(map[string]any)
	if mAssistant["role"] != "assistant" {
		t.Fatalf("messages[1].role: got %v, want %q", mAssistant["role"], "assistant")
	}
	if mAssistant["content"] != "voy a leer foo" {
		t.Fatalf("messages[1].content: got %v, want %q (el texto SI se serializa)", mAssistant["content"], "voy a leer foo")
	}
	toolCalls, ok := mAssistant["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("messages[1].tool_calls: got %#v, want 1 entrada", mAssistant["tool_calls"])
	}
	tc0 := toolCalls[0].(map[string]any)
	if tc0["id"] != "call_1" {
		t.Fatalf("messages[1].tool_calls[0].id: got %v, want %q", tc0["id"], "call_1")
	}
	fn, ok := tc0["function"].(map[string]any)
	if !ok {
		t.Fatalf("messages[1].tool_calls[0].function: got %#v, want objeto", tc0["function"])
	}
	if fn["name"] != "read" {
		t.Fatalf("messages[1].tool_calls[0].function.name: got %v, want %q", fn["name"], "read")
	}

	// messages[2]: el tool result empareja con la tool call del assistant.
	mTool := messages[2].(map[string]any)
	if mTool["role"] != "tool" {
		t.Fatalf("messages[2].role: got %v, want %q", mTool["role"], "tool")
	}
	if mTool["tool_call_id"] != "call_1" {
		t.Fatalf("messages[2].tool_call_id: got %v, want %q", mTool["tool_call_id"], "call_1")
	}
}
