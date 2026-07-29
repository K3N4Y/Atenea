package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/K3N4Y/atenea/agentcore/llm/llmtest"
)

// Every provider atenea ships goes through the published contract kit. The
// adapters' own tests cover the mapping — which SSE field becomes which event,
// which request field the vendor wants — and this file covers the shape of the
// turn: that it opens, closes, brackets its blocks, names its tool calls and lets
// go of the channel when the context is cancelled.
//
// It is also the kit's own proof of usefulness. A kit that only ever runs against
// the fakes written to satisfy it is a tautology; these are the real adapters,
// talking to a stub server over the vendor's own wire format.

// openAITurn is one SSE turn with everything the checks look at: reasoning, text,
// a tool call whose input arrives in fragments, and the final usage chunk.
const openAITurn = `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning":"let me look"},"finish_reason":null}]}

data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"reading it"},"finish_reason":null}]}

data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]},"finish_reason":null}]}

data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"foo.go\"}"}}]},"finish_reason":null}]}

data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}

data: [DONE]

`

// anthropicTurn is the same turn in Anthropic's own event stream.
var anthropicTurn = []string{
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"reading it"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"foo.go\"}"}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":12,"output_tokens":9}}`,
	`{"type":"message_stop"}`,
}

// contractRequest is a request every adapter can answer: a model, a system
// prompt, one user message and one tool it may call.
func contractRequest(model string) Request {
	return Request{
		Model:    model,
		System:   "Be exact.",
		Messages: []Message{TextMessage("user", "read foo.go")},
		Tools: []ToolDef{{
			Name:        "read",
			Description: "Reads a file.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		}},
	}
}

// sseServer serves the same canned event stream to every request, so a check may
// stream the turn as many times as it needs.
func sseServer(t *testing.T, body func(w io.Writer)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		body(w)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestFakeProvider_Contract(t *testing.T) {
	llmtest.Contract(t, func(*testing.T) llmtest.Subject {
		return llmtest.Subject{
			Provider: NewFakeProvider(
				Event{Kind: StepStarted},
				Event{Kind: TextStarted},
				Event{Kind: TextDelta, Text: "reading it"},
				Event{Kind: TextEnded},
				Event{Kind: ToolInputStarted, CallID: "c1"},
				Event{Kind: ToolInputDelta, CallID: "c1", Input: json.RawMessage(`{"path":`)},
				Event{Kind: ToolInputDelta, CallID: "c1", Input: json.RawMessage(`"foo.go"}`)},
				Event{Kind: ToolInputEnded, CallID: "c1"},
				Event{Kind: ToolCall, CallID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"foo.go"}`)},
				Event{Kind: StepEnded, Usage: &Usage{InputTokens: 10, OutputTokens: 5}},
			),
			Request: contractRequest("fake-model"),
		}
	})
}

// TestSwitchableProvider_Contract covers the decorator every session actually
// talks to: the runner streams through the switch, so a delegate that honors the
// contract must still honor it once wrapped.
func TestSwitchableProvider_Contract(t *testing.T) {
	llmtest.Contract(t, func(t *testing.T) llmtest.Subject {
		delegate := NewFakeProvider(
			Event{Kind: StepStarted},
			Event{Kind: TextStarted},
			Event{Kind: TextDelta, Text: "hola"},
			Event{Kind: TextEnded},
			Event{Kind: StepEnded, Usage: &Usage{InputTokens: 3, OutputTokens: 1}},
		)
		provider, err := NewSwitchableProvider(ProviderSnapshot{
			ProviderID: "fake", ProviderName: "Fake", BaseURL: "https://fake.test", Model: "fake-model", Provider: delegate,
		})
		if err != nil {
			t.Fatalf("NewSwitchableProvider: %v", err)
		}
		return llmtest.Subject{Provider: provider, Request: contractRequest("fake-model")}
	})
}

func TestOpenAIProvider_Contract(t *testing.T) {
	llmtest.Contract(t, func(t *testing.T) llmtest.Subject {
		server := sseServer(t, func(w io.Writer) { io.WriteString(w, openAITurn) })
		return llmtest.Subject{
			Provider: NewOpenAIProvider("test-key", server.URL, "test-model"),
			Request:  contractRequest("test-model"),
		}
	})
}

// TestOpenAIProvider_FailedTurnContract runs the contract over the other way a
// turn can end. A failure is a turn too: it opens, it closes on StepFailed and it
// carries the cause, because that is what the host classifies (a context overflow
// is compacted and retried, anything else surfaces). The status is one the SDK
// does not retry, so the check reads the failure and not the waiting.
func TestOpenAIProvider_FailedTurnContract(t *testing.T) {
	llmtest.Contract(t, func(t *testing.T) llmtest.Subject {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"context_length_exceeded","type":"invalid_request_error"}}`)
		}))
		t.Cleanup(server.Close)
		return llmtest.Subject{
			Provider: NewOpenAIProvider("test-key", server.URL, "test-model"),
			Request:  contractRequest("test-model"),
		}
	})
}

// TestCodexProvider_Contract covers the ChatGPT-subscription dialect: a different
// wire vocabulary (Responses items instead of chat choices) and a credential
// resolved per request rather than baked in, over the same turn shape.
func TestCodexProvider_Contract(t *testing.T) {
	llmtest.Contract(t, func(t *testing.T) llmtest.Subject {
		server := sseServer(t, func(w io.Writer) { io.WriteString(w, codexTurn) })
		return llmtest.Subject{
			Provider: NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "access", AccountID: "acct"}}, server.URL, "gpt-5.5"),
			Request:  contractRequest("gpt-5.5"),
		}
	})
}

// TestCodexProvider_FailedTurnContract runs the contract over the other way this
// dialect's turn ends. The status is the one a spent subscription answers with, so
// the check reads the failure rather than the waiting.
func TestCodexProvider_FailedTurnContract(t *testing.T) {
	llmtest.Contract(t, func(t *testing.T) llmtest.Subject {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-should-retry", "false")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"You've hit your usage limit."}}`)
		}))
		t.Cleanup(server.Close)
		return llmtest.Subject{
			Provider: NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "access", AccountID: "acct"}}, server.URL, "gpt-5.5"),
			Request:  contractRequest("gpt-5.5"),
		}
	})
}

func TestAnthropicProvider_Contract(t *testing.T) {
	llmtest.Contract(t, func(t *testing.T) llmtest.Subject {
		server := sseServer(t, func(w io.Writer) {
			for _, event := range anthropicTurn {
				io.WriteString(w, "event: message\ndata: "+event+"\n\n")
			}
		})
		return llmtest.Subject{
			Provider: NewAnthropicProvider("key", server.URL, "claude-test"),
			Request:  contractRequest("claude-test"),
		}
	})
}

func TestAnthropicProvider_FailedTurnContract(t *testing.T) {
	llmtest.Contract(t, func(t *testing.T) llmtest.Subject {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`)
		}))
		t.Cleanup(server.Close)
		return llmtest.Subject{
			Provider: NewAnthropicProvider("key", server.URL, "claude-test"),
			Request:  contractRequest("claude-test"),
		}
	})
}
