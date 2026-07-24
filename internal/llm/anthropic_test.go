package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAnthropicProvider_StreamMapsNativeMessagesRequestAndEvents(t *testing.T) {
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("X-Api-Key"); got != "key" {
			t.Errorf("X-Api-Key = %q", got)
		}
		requestBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":1,"cache_creation_input_tokens":4,"cache_read_input_tokens":3}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hola"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"read","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":12,"output_tokens":9,"cache_creation_input_tokens":4,"cache_read_input_tokens":3,"output_tokens_details":{"thinking_tokens":2}}}`,
			`{"type":"message_stop"}`,
		} {
			io.WriteString(w, "event: message\ndata: "+event+"\n\n")
		}
	}))
	defer server.Close()

	p := NewAnthropicProvider("key", server.URL, "fallback")
	out, err := p.Stream(context.Background(), Request{
		Model: "claude-test", System: "Be exact", MaxOutputTokens: 321,
		Messages: []Message{
			{Role: "user", Text: "open it"},
			{Role: "assistant", Text: "calling", ToolCalls: []ToolCallPart{{ID: "toolu_1", Name: "read", Arguments: json.RawMessage(`{"path":"old.go"}`)}}},
			{Role: "tool", ToolCallID: "toolu_1", Text: "contents", IsError: true},
		},
		Tools: []ToolDef{{Name: "read", Description: "Read a file", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(out)
	wantKinds := []EventKind{StepStarted, TextStarted, TextDelta, TextEnded, ToolInputStarted, ToolInputDelta, ToolInputDelta, ToolInputEnded, ToolCall, StepEnded}
	kinds := make([]EventKind, len(got))
	for i := range got {
		kinds[i] = got[i].Kind
	}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("kinds = %v, want %v; events=%#v", kinds, wantKinds, got)
	}
	if got[2].Text != "Hola" || got[8].CallID != "toolu_2" || got[8].ToolName != "read" || string(got[8].Input) != `{"path":"main.go"}` {
		t.Fatalf("mapped events = %#v", got)
	}
	if want := (&Usage{InputTokens: 12, OutputTokens: 9, ReasoningTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4, CacheableInputTokens: 19}); !reflect.DeepEqual(got[9].Usage, want) {
		t.Fatalf("usage = %#v, want %#v", got[9].Usage, want)
	}

	var body map[string]any
	if err := json.Unmarshal(requestBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "claude-test" || body["max_tokens"] != float64(321) || body["stream"] != true {
		t.Fatalf("request = %s", requestBody)
	}
	if _, ok := body["thinking"]; ok {
		t.Fatalf("thinking must be disabled: %s", requestBody)
	}
	system := body["system"].([]any)[0].(map[string]any)
	if system["text"] != "Be exact" {
		t.Fatalf("system = %#v", system)
	}
	messages := body["messages"].([]any)
	assistant := messages[1].(map[string]any)["content"].([]any)
	if assistant[1].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("assistant = %#v", assistant)
	}
	result := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "toolu_1" {
		t.Fatalf("tool result = %#v", result)
	}
	if result["is_error"] != true {
		t.Fatalf("tool result must preserve failure state: %#v", result)
	}
	tool := body["tools"].([]any)[0].(map[string]any)
	schema := tool["input_schema"].(map[string]any)
	if schema["additionalProperties"] != false {
		t.Fatalf("schema lost extra fields: %#v", schema)
	}
}

func TestAnthropicProvider_StreamEnablesFiveMinutePromptCaching(t *testing.T) {
	requestBodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		requestBodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	out, err := NewAnthropicProvider("key", server.URL, "claude-test").Stream(context.Background(), Request{
		Messages: []Message{{Role: "user", Text: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	drain(out)

	var body struct {
		CacheControl map[string]any `json:"cache_control"`
	}
	if err := json.Unmarshal(<-requestBodies, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"type": "ephemeral"}
	if !reflect.DeepEqual(body.CacheControl, want) {
		t.Fatalf("cache_control = %#v, want %#v", body.CacheControl, want)
	}
}

func TestAnthropicProvider_StreamReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`)
	}))
	defer server.Close()
	out, err := NewAnthropicProvider("bad", server.URL, "model").Stream(context.Background(), Request{Messages: []Message{{Role: "user", Text: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(out)
	if len(got) != 2 || got[1].Kind != StepFailed || got[1].Err == nil || !strings.Contains(got[1].Text, "bad key") {
		t.Fatalf("events = %#v", got)
	}
}

func TestAnthropicProvider_StreamClassifiesContextOverflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Request-Id", "req_overflow_123")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`)
	}))
	defer server.Close()

	out, err := NewAnthropicProvider("key", server.URL, "claude-test").Stream(context.Background(), Request{
		Messages: []Message{{Role: "user", Text: "oversized prompt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drain(out)
	if len(events) != 2 || events[1].Kind != StepFailed {
		t.Fatalf("events = %#v", events)
	}
	var overflow *ContextOverflowError
	if !errors.As(events[1].Err, &overflow) {
		t.Fatalf("error type = %T, want *ContextOverflowError: %v", events[1].Err, events[1].Err)
	}
	if !strings.Contains(overflow.Message, "prompt is too long") || !strings.Contains(overflow.Message, "req_overflow_123") {
		t.Fatalf("overflow diagnostic = %q", overflow.Message)
	}
}

func TestAnthropicProvider_StreamRejectsInvalidParallelToolInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"valid","name":"read","input":{}}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"invalid","name":"write","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"main.go\"}"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_stop"}`,
		} {
			io.WriteString(w, "event: message\ndata: "+event+"\n\n")
		}
	}))
	defer server.Close()

	out, err := NewAnthropicProvider("key", server.URL, "claude-test").Stream(context.Background(), Request{
		Messages: []Message{{Role: "user", Text: "use tools"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drain(out)
	var validCall bool
	var failure error
	for _, event := range events {
		if event.Kind == ToolCall && event.CallID == "valid" {
			validCall = true
		}
		if event.Kind == ToolCall && event.CallID == "invalid" {
			t.Fatalf("invalid tool input was emitted for execution: %#v", events)
		}
		if event.Kind == StepFailed {
			failure = event.Err
		}
	}
	if !validCall {
		t.Fatalf("valid parallel tool call was lost: %#v", events)
	}
	if failure == nil || !strings.Contains(failure.Error(), `tool call "invalid" input`) {
		t.Fatalf("failure = %v; events=%#v", failure, events)
	}
}

func TestAnthropicProvider_StreamCancellationClosesChannel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { close(started); <-release }))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	out, err := NewAnthropicProvider("key", server.URL, "model").Stream(ctx, Request{Messages: []Message{{Role: "user", Text: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var events []Event
	go func() {
		events = drain(out)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not close after cancellation")
	}
	for _, event := range events {
		if event.Kind == StepFailed {
			t.Fatalf("cancellation emitted StepFailed: %#v", events)
		}
	}
}

func TestValidateAnthropicKeyUsesModelsAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("X-Api-Key") != "valid" {
			t.Fatalf("request = %s key=%q", r.URL.Path, r.Header.Get("X-Api-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[],"has_more":false,"first_id":null,"last_id":null}`)
	}))
	defer server.Close()
	if err := ValidateAnthropicKey(context.Background(), server.URL, "valid"); err != nil {
		t.Fatal(err)
	}
}
