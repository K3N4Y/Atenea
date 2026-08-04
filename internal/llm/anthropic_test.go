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
			TextMessage("user", "open it"),
			{Role: "assistant", Parts: []Part{{Kind: TextPart, Text: "calling"}}, ToolCalls: []ToolCallPart{{ID: "toolu_1", Name: "read", Arguments: json.RawMessage(`{"path":"old.go"}`)}}},
			{Role: "tool", ToolCallID: "toolu_1", Parts: []Part{{Kind: TextPart, Text: "contents"}}, IsError: true},
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

func TestAnthropicProvider_StreamPreservesUsagePresence(t *testing.T) {
	tests := []struct {
		name   string
		events []string
		want   *Usage
	}{
		{
			name: "omitted",
			events: []string{
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null}}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null}}`,
			},
		},
		{
			name: "empty",
			events: []string{
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{}}}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{}}`,
			},
		},
		{
			name: "partial fields merge",
			events: []string{
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"cache_read_input_tokens":3}}}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":9}}`,
			},
			want: &Usage{InputTokens: 12, OutputTokens: 9, CacheReadTokens: 3, CacheableInputTokens: 15},
		},
		{
			name: "explicit zero",
			events: []string{
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"output_tokens":5}}}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":0}}`,
			},
			want: &Usage{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range tt.events {
					io.WriteString(w, "event: message\ndata: "+event+"\n\n")
				}
				io.WriteString(w, "event: message\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer server.Close()

			out, err := NewAnthropicProvider("key", server.URL, "claude-test").Stream(context.Background(), Request{
				Messages: []Message{TextMessage("user", "hello")},
			})
			if err != nil {
				t.Fatal(err)
			}
			events := drain(out)
			got := events[len(events)-1]
			if got.Kind != StepEnded {
				t.Fatalf("last event = %v, want StepEnded", got.Kind)
			}
			if !reflect.DeepEqual(got.Usage, tt.want) {
				t.Fatalf("usage = %#v, want %#v", got.Usage, tt.want)
			}
		})
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
		Messages: []Message{TextMessage("user", "hello")},
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

// A message's content is every text part in order, joined into the one text block
// Anthropic takes — and a message with nothing to say sends no block at all,
// because Anthropic rejects an empty one.
func TestAnthropicProvider_StreamJoinsTextPartsAndOmitsEmptyContent(t *testing.T) {
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
		Messages: []Message{
			{Role: "user", Parts: []Part{{Kind: TextPart, Text: "read "}, {Kind: TextPart, Text: "foo.go"}}},
			{Role: "assistant", ToolCalls: []ToolCallPart{{ID: "toolu_1", Name: "read", Arguments: json.RawMessage(`{"path":"foo.go"}`)}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	drain(out)

	var body struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(<-requestBodies, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("messages = %#v, want 2", body.Messages)
	}
	user := body.Messages[0].Content
	if len(user) != 1 || user[0]["type"] != "text" || user[0]["text"] != "read foo.go" {
		t.Errorf("user content = %#v, want one text block carrying both parts", user)
	}
	assistant := body.Messages[1].Content
	if len(assistant) != 1 || assistant[0]["type"] != "tool_use" {
		t.Errorf("assistant content = %#v, want the tool use alone and no empty text block", assistant)
	}
}

func TestAnthropicProvider_StreamSerializesOrderedImageContent(t *testing.T) {
	requestBodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	out, err := NewAnthropicProvider("key", server.URL, "claude-test").Stream(context.Background(), Request{
		Messages: []Message{{Role: "user", Parts: []Part{
			{Kind: TextPart, Text: "before"},
			{Kind: ImagePart, MediaType: "image/webp", Data: []byte{3, 4, 5}},
			{Kind: TextPart, Text: "after"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	drain(out)

	var body struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(<-requestBodies, &body); err != nil {
		t.Fatal(err)
	}
	content := body.Messages[0].Content
	if len(content) != 3 || content[0]["type"] != "text" || content[0]["text"] != "before" ||
		content[1]["type"] != "image" || content[2]["type"] != "text" || content[2]["text"] != "after" {
		t.Fatalf("content = %#v, want ordered text, image, text", content)
	}
	source := content[1]["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "image/webp" || source["data"] != "AwQF" {
		t.Errorf("image source = %#v, want MIME type and original bytes", source)
	}
}

func TestAnthropicProvider_StreamRefusesImageOnUnsupportedRole(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()

	out, err := NewAnthropicProvider("key", server.URL, "claude-test").Stream(context.Background(), Request{
		Messages: []Message{{Role: "assistant", Parts: []Part{{Kind: ImagePart, MediaType: "image/png", Data: []byte{1}}}}},
	})
	if out != nil {
		t.Error("Stream returned a channel")
	}
	var unsupported *UnsupportedPartError
	if !errors.As(err, &unsupported) || unsupported.Kind != ImagePart {
		t.Fatalf("Stream error = %v, want unsupported image part", err)
	}
	if called {
		t.Error("request reached endpoint")
	}
}

// This adapter cannot put anything but text on the wire, so a part it does not
// understand fails the request outright — before a token is spent, and loudly
// enough for a host to classify. Sending the rest would hand the model a
// conversation with a hole in it.
func TestAnthropicProvider_StreamRefusesContentItCannotExpress(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()

	out, err := NewAnthropicProvider("key", server.URL, "claude-test").Stream(context.Background(), Request{
		Messages: []Message{
			TextMessage("user", "what is in this?"),
			{Role: "user", Parts: []Part{{Kind: PartKind(1 << 30)}}},
		},
	})
	if out != nil {
		t.Errorf("Stream returned a channel for content it cannot express")
	}
	var unsupported *UnsupportedPartError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Stream error = %v, want an *UnsupportedPartError a host can classify", err)
	}
	if called {
		t.Errorf("the request reached the endpoint: the refusal must cost nothing")
	}
}

func TestAnthropicProvider_StreamReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`)
	}))
	defer server.Close()
	out, err := NewAnthropicProvider("bad", server.URL, "model").Stream(context.Background(), Request{Messages: []Message{TextMessage("user", "hi")}})
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
		Messages: []Message{TextMessage("user", "oversized prompt")},
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
		Messages: []Message{TextMessage("user", "use tools")},
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
	out, err := NewAnthropicProvider("key", server.URL, "model").Stream(ctx, Request{Messages: []Message{TextMessage("user", "hi")}})
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
