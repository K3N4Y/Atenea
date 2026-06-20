package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
