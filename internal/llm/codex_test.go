package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// codexTurn is one Responses turn with everything the mapping has to cover:
// reasoning, text, a function call whose arguments arrive in fragments, and the
// final usage. The `event:` lines are there because that is how the backend frames
// the stream.
const codexTurn = `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","sequence_number":2,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"let me look"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"reading it"}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":4,"output_index":2,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":5,"item_id":"fc_1","output_index":2,"delta":"{\"path\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":6,"item_id":"fc_1","output_index":2,"delta":"\"foo.go\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":7,"output_index":2,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\"foo.go\"}","status":"completed"}}

event: response.completed
data: {"type":"response.completed","sequence_number":8,"response":{"id":"resp_1","status":"completed","usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":4},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":3},"total_tokens":15}}}

`

// staticTokens is a credential source that always answers the same thing and
// counts how often it was asked, which is what proves the adapter resolves per
// request instead of holding on to one.
type staticTokens struct {
	mu     sync.Mutex
	token  OAuthToken
	err    error
	asked  int
	rotate func(int) OAuthToken
}

func (s *staticTokens) OAuthToken(context.Context) (OAuthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked++
	if s.err != nil {
		return OAuthToken{}, s.err
	}
	if s.rotate != nil {
		return s.rotate(s.asked), nil
	}
	return s.token, nil
}

func (s *staticTokens) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asked
}

// codexServer serves the canned turn and captures what the adapter sent.
type codexServer struct {
	server *httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	heads  []http.Header
	paths  []string
}

func newCodexServer(t *testing.T, body string) *codexServer {
	t.Helper()
	captured := &codexServer{}
	captured.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		captured.mu.Lock()
		captured.bodies = append(captured.bodies, payload)
		captured.heads = append(captured.heads, r.Header.Clone())
		captured.paths = append(captured.paths, r.URL.Path)
		captured.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}))
	t.Cleanup(captured.server.Close)
	return captured
}

func (c *codexServer) requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func (c *codexServer) request(t *testing.T, index int) (map[string]any, http.Header, string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if index >= len(c.bodies) {
		t.Fatalf("the adapter made %d requests, want at least %d", len(c.bodies), index+1)
	}
	var body map[string]any
	if err := json.Unmarshal(c.bodies[index], &body); err != nil {
		t.Fatalf("request %d body is not JSON: %v", index, err)
	}
	return body, c.heads[index], c.paths[index]
}

// codexRequest is a turn with everything a request has to carry: a system prompt,
// history with a tool round trip, a tool schema, a session key — and a
// MaxOutputTokens the adapter has to throw away.
func codexRequest() Request {
	return Request{
		Model:           "gpt-5.5",
		System:          "Be exact.",
		SessionKey:      "session-42",
		MaxOutputTokens: 4096,
		Messages: []Message{
			TextMessage("user", "read foo.go"),
			{Role: "assistant", Parts: []Part{{Kind: TextPart, Text: "on it"}}, ToolCalls: []ToolCallPart{{ID: "call_0", Name: "read", Arguments: json.RawMessage(`{"path":"foo.go"}`)}}},
			{Role: "tool", ToolCallID: "call_0", Parts: []Part{{Kind: TextPart, Text: "package main"}}},
		},
		Tools: []ToolDef{{
			Name:        "read",
			Description: "Reads a file.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		}},
	}
}

func drainCodex(t *testing.T, provider Provider, req Request) []Event {
	t.Helper()
	out, err := provider.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var events []Event
	for event := range out {
		events = append(events, event)
	}
	return events
}

func TestOAuthResponsesProvider_SendsOnlyTheStandardOAuthPolicy(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	provider := NewOAuthResponsesProvider(&staticTokens{token: OAuthToken{AccessToken: "posthog-token"}}, server.server.URL, "gpt-5.5")
	drainCodex(t, provider, codexRequest())

	body, headers, path := server.request(t, 0)
	if path != "/responses" {
		t.Fatalf("path = %q, want the standard Responses endpoint", path)
	}
	if got := headers.Get("Authorization"); got != "Bearer posthog-token" {
		t.Fatalf("Authorization = %q, want the per-request bearer", got)
	}
	if got := headers.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key = %q, want none on the PostHog OAuth path", got)
	}
	if got := body["max_output_tokens"]; got != float64(codexRequest().MaxOutputTokens) {
		t.Fatalf("max_output_tokens = %#v, want %d", got, codexRequest().MaxOutputTokens)
	}
	for _, name := range []string{"chatgpt-account-id", "originator", "OpenAI-Beta", "session-id", "x-client-request-id"} {
		if got := headers.Get(name); got != "" {
			t.Errorf("%s = %q, want no ChatGPT-only header", name, got)
		}
	}
	if got := headers.Get("User-Agent"); got == codexUserAgent {
		t.Fatalf("User-Agent = %q, want no ChatGPT-only identity", got)
	}
}

// TestCodexProvider_SendsTheSubscriptionHeadersTheBackendDemands: the codex
// backend identifies a caller by header, not only by bearer, and a request missing
// any of them is rejected with a message that names none of this.
func TestCodexProvider_SendsTheSubscriptionHeadersTheBackendDemands(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	tokens := &staticTokens{token: OAuthToken{AccessToken: "access-1", AccountID: "acct_42"}}

	drainCodex(t, NewCodexProvider(tokens, server.server.URL, "gpt-5.5"), codexRequest())

	_, headers, path := server.request(t, 0)
	if path != "/responses" {
		t.Fatalf("the adapter posted to %q, want the Responses endpoint under the codex base URL", path)
	}
	want := map[string]string{
		"Authorization":      "Bearer access-1",
		"chatgpt-account-id": "acct_42",
		"originator":         codexOriginator,
		"User-Agent":         codexUserAgent,
		"OpenAI-Beta":        codexBetaHeader,
		"accept":             "text/event-stream",
		"Content-Type":       "application/json",
	}
	for header, value := range want {
		if got := headers.Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
	for _, header := range []string{"session-id", "x-client-request-id"} {
		if headers.Get(header) == "" {
			t.Errorf("%s is missing: the backend correlates a conversation and a request by these", header)
		}
	}
}

// TestCodexProvider_KeepsTheSessionHeaderStableAcrossTheConversation: the header
// is what the backend groups a conversation by, so two turns of one session must
// carry the same value and two sessions must not.
func TestCodexProvider_KeepsTheSessionHeaderStableAcrossTheConversation(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	provider := NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "a", AccountID: "b"}}, server.server.URL, "gpt-5.5")

	first := codexRequest()
	drainCodex(t, provider, first)
	drainCodex(t, provider, first)
	other := codexRequest()
	other.SessionKey = "session-99"
	drainCodex(t, provider, other)

	_, one, _ := server.request(t, 0)
	_, two, _ := server.request(t, 1)
	_, three, _ := server.request(t, 2)
	if one.Get("session-id") != two.Get("session-id") {
		t.Errorf("session-id moved between two turns of one session: %q then %q", one.Get("session-id"), two.Get("session-id"))
	}
	if one.Get("session-id") == three.Get("session-id") {
		t.Errorf("two sessions share the session-id %q", one.Get("session-id"))
	}
	if one.Get("x-client-request-id") == two.Get("x-client-request-id") {
		t.Error("x-client-request-id repeated across two requests: it identifies the request, not the session")
	}
}

// TestCodexProvider_NeverSendsAMaxOutputTokensCeiling: the backend refuses a
// subscription request that names one, and Request.MaxOutputTokens is set by every
// host that reserves output in its context estimate. Dropping it is the behavior,
// not an omission.
func TestCodexProvider_NeverSendsAMaxOutputTokensCeiling(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	req := codexRequest()
	req.MaxOutputTokens = 32_000

	drainCodex(t, NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "a", AccountID: "b"}}, server.server.URL, "gpt-5.5"), req)

	body, _, _ := server.request(t, 0)
	if _, present := body["max_output_tokens"]; present {
		t.Fatalf("the request carries max_output_tokens = %v: this dialect rejects it", body["max_output_tokens"])
	}
}

// TestCodexProvider_ShapesTheResponsesRequest: the four ways this dialect differs
// from chat completions, asserted where they are observable — the system prompt in
// instructions, nothing stored, the encrypted reasoning asked for, and the history
// projected onto Responses items rather than messages with roles.
func TestCodexProvider_ShapesTheResponsesRequest(t *testing.T) {
	server := newCodexServer(t, codexTurn)

	drainCodex(t, NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "a", AccountID: "b"}}, server.server.URL, "gpt-5.5"), codexRequest())

	body, _, _ := server.request(t, 0)
	if body["instructions"] != "Be exact." {
		t.Errorf("instructions = %v, want the system prompt: sent as a message it is silently deprioritized", body["instructions"])
	}
	if store, ok := body["store"].(bool); !ok || store {
		t.Errorf("store = %v, want false explicitly", body["store"])
	}
	if include, _ := json.Marshal(body["include"]); string(include) != `["reasoning.encrypted_content"]` {
		t.Errorf("include = %s, want the encrypted reasoning", include)
	}
	if body["prompt_cache_key"] != "session-42" {
		t.Errorf("prompt_cache_key = %v, want the request's session key", body["prompt_cache_key"])
	}
	if body["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", body["tool_choice"])
	}
	if parallel, ok := body["parallel_tool_calls"].(bool); !ok || !parallel {
		t.Errorf("parallel_tool_calls = %v, want true", body["parallel_tool_calls"])
	}
	if body["model"] != "gpt-5.5" {
		t.Errorf("model = %v, want the request's model", body["model"])
	}
	items, _ := json.Marshal(body["input"])
	for _, want := range []string{
		`{"content":"read foo.go","role":"user","type":"message"}`,
		`{"content":"on it","role":"assistant","type":"message"}`,
		`{"arguments":"{\"path\":\"foo.go\"}","call_id":"call_0","name":"read","type":"function_call"}`,
		`{"call_id":"call_0","output":"package main","type":"function_call_output"}`,
	} {
		if !strings.Contains(string(items), want) {
			t.Errorf("input = %s\nwant it to contain %s", items, want)
		}
	}
	tools, _ := json.Marshal(body["tools"])
	if !strings.Contains(string(tools), `"strict":false`) || !strings.Contains(string(tools), `"type":"function"`) {
		t.Errorf("tools = %s, want a non-strict function tool", tools)
	}
}

// TestCodexProvider_TranslatesTheResponsesStreamIntoOneTurn: the Responses stream
// names items where chat completions named choices, and the turn a host consumes
// has to come out identical either way.
func TestCodexProvider_TranslatesTheResponsesStreamIntoOneTurn(t *testing.T) {
	server := newCodexServer(t, codexTurn)

	events := drainCodex(t, NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "a", AccountID: "b"}}, server.server.URL, "gpt-5.5"), codexRequest())

	want := []EventKind{
		StepStarted,
		ReasoningStarted, ReasoningDelta, ReasoningEnded,
		TextStarted, TextDelta, TextEnded,
		ToolInputStarted, ToolInputDelta, ToolInputDelta, ToolInputEnded, ToolCall,
		StepEnded,
	}
	if len(events) != len(want) {
		t.Fatalf("the turn produced %d events, want %d:\n%#v", len(events), len(want), events)
	}
	for i, kind := range want {
		if events[i].Kind != kind {
			t.Fatalf("event %d is kind %d, want %d:\n%#v", i+1, events[i].Kind, kind, events)
		}
	}
	call := events[11]
	if call.CallID != "call_1" || call.ToolName != "read" || string(call.Input) != `{"path":"foo.go"}` {
		t.Errorf("ToolCall = %#v, want the call id the result pairs with plus the complete input", call)
	}
	usage := events[len(events)-1].Usage
	if usage == nil {
		t.Fatal("StepEnded carries no usage: the completed response is the only place it arrives")
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 5 || usage.ReasoningTokens != 3 || usage.CacheReadTokens != 4 {
		t.Errorf("usage = %#v, want the completed response's own accounting", usage)
	}
}

// TestCodexProvider_ResolvesTheCredentialOnEveryRequest: an access token lives
// about an hour and a session outlives that, so the adapter must not keep the one
// it was handed. Two turns, two resolutions, and the second bearer is the second
// token.
func TestCodexProvider_ResolvesTheCredentialOnEveryRequest(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	tokens := &staticTokens{rotate: func(n int) OAuthToken {
		return OAuthToken{AccessToken: "access-" + strings.Repeat("x", n), AccountID: "acct"}
	}}
	provider := NewCodexProvider(tokens, server.server.URL, "gpt-5.5")

	drainCodex(t, provider, codexRequest())
	drainCodex(t, provider, codexRequest())

	if tokens.count() != 2 {
		t.Fatalf("the credential was resolved %d times for two turns, want once per request", tokens.count())
	}
	_, first, _ := server.request(t, 0)
	_, second, _ := server.request(t, 1)
	if first.Get("Authorization") == second.Get("Authorization") {
		t.Fatalf("both turns carried %q: the adapter cached the credential instead of asking again", first.Get("Authorization"))
	}
}

// TestCodexProvider_RefusesTheTurnWhenTheCredentialCannotBeResolved: a refresh
// that cannot be completed has to reach the user as the sentence that tells them
// what to do — before a request goes out, since one without a credential could
// only have been rejected.
func TestCodexProvider_RefusesTheTurnWhenTheCredentialCannotBeResolved(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	tokens := &staticTokens{err: errors.New("the ChatGPT credential expired; log in again")}

	_, err := NewCodexProvider(tokens, server.server.URL, "gpt-5.5").Stream(context.Background(), codexRequest())
	if err == nil {
		t.Fatal("Stream accepted a turn whose credential could not be resolved")
	}
	if !strings.Contains(err.Error(), "log in again") {
		t.Fatalf("Stream() error = %v, want the sentence that says what to do", err)
	}
	if server.requests() != 0 {
		t.Fatalf("the adapter sent %d requests with no credential", server.requests())
	}
}

// TestCodexProvider_RefusesACredentialWithNoAccount: the stored oauth arm no
// longer insists on an account id (other flows have none), so the adapter that
// sends the header is where its absence has to be refused — before a request
// the endpoint could only reject as unroutable.
func TestCodexProvider_RefusesACredentialWithNoAccount(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	tokens := &staticTokens{token: OAuthToken{AccessToken: "access"}}

	_, err := NewCodexProvider(tokens, server.server.URL, "gpt-5.5").Stream(context.Background(), codexRequest())
	if err == nil || !strings.Contains(err.Error(), "log in again") {
		t.Fatalf("Stream() error = %v, want a refusal telling the user to log in again", err)
	}
	if server.requests() != 0 {
		t.Fatalf("the adapter sent %d requests with an unroutable credential", server.requests())
	}
}

// TestCodexProvider_RefusesToStreamWithNoCredentialSource: an adapter wired
// without one could only ever produce 401s, and saying so beats letting the
// endpoint say something else.
func TestCodexProvider_RefusesToStreamWithNoCredentialSource(t *testing.T) {
	server := newCodexServer(t, codexTurn)

	_, err := NewCodexProvider(nil, server.server.URL, "gpt-5.5").Stream(context.Background(), codexRequest())
	if err == nil || !strings.Contains(err.Error(), "credential source") {
		t.Fatalf("Stream() error = %v, want one naming the missing credential source", err)
	}
}

// TestCodexProvider_NamesTheSubscriptionLimitInsteadOfDumpingTheBody: the failure
// this dialect actually produces is a plan whose usage window is spent, and the
// SDK's own error for it stringifies as a wall of JSON that never says so.
func TestCodexProvider_NamesTheSubscriptionLimitInsteadOfDumpingTheBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-should-retry", "false")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"You've hit your usage limit. Try again after 3pm.","plan_type":"plus"}}`)
	}))
	t.Cleanup(server.Close)

	events := drainCodex(t, NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "a", AccountID: "b"}}, server.URL, "gpt-5.5"), codexRequest())

	last := events[len(events)-1]
	if last.Kind != StepFailed || last.Err == nil {
		t.Fatalf("the turn ended with %#v, want StepFailed", last)
	}
	message := last.Err.Error()
	if !strings.Contains(message, "ChatGPT subscription limit reached") {
		t.Errorf("StepFailed error = %q, want it to name the subscription limit", message)
	}
	if !strings.Contains(message, "Try again after 3pm.") {
		t.Errorf("StepFailed error = %q, want the reason the backend gave", message)
	}
	if strings.Contains(message, "plan_type") {
		t.Errorf("StepFailed error = %q, want a sentence rather than the raw body", message)
	}
}

// TestCodexProvider_ClassifiesAContextOverflowSoAHostCanCompact: a host acts on
// this one by compacting and retrying, and it classifies with errors.As — so the
// overflow has to arrive as the type and not as a string.
func TestCodexProvider_ClassifiesAContextOverflowSoAHostCanCompact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}`)
	}))
	t.Cleanup(server.Close)

	events := drainCodex(t, NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "a", AccountID: "b"}}, server.URL, "gpt-5.5"), codexRequest())

	last := events[len(events)-1]
	var overflow *ContextOverflowError
	if last.Kind != StepFailed || !errors.As(last.Err, &overflow) {
		t.Fatalf("the turn ended with %#v, want StepFailed carrying a ContextOverflowError", last)
	}
}

// TestCodexProvider_FailsTheTurnTheBackendFailedMidStream: a failure that arrives
// inside the stream has no status code to read, only a code and a message, and it
// still has to close the turn with the cause.
func TestCodexProvider_FailsTheTurnTheBackendFailedMidStream(t *testing.T) {
	const failed = `event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"m","output_index":0,"content_index":0,"delta":"thinking"}

event: response.failed
data: {"type":"response.failed","sequence_number":2,"response":{"id":"r","status":"failed","error":{"code":"rate_limit_exceeded","message":"Rate limit reached for the plan."}}}

`
	server := newCodexServer(t, failed)

	events := drainCodex(t, NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "a", AccountID: "b"}}, server.server.URL, "gpt-5.5"), codexRequest())

	kinds := []EventKind{StepStarted, TextStarted, TextDelta, TextEnded, StepFailed}
	if len(events) != len(kinds) {
		t.Fatalf("the turn produced %d events, want %d:\n%#v", len(events), len(kinds), events)
	}
	for i, kind := range kinds {
		if events[i].Kind != kind {
			t.Fatalf("event %d is kind %d, want %d: the open text block has to close before the turn fails", i+1, events[i].Kind, kind)
		}
	}
	if err := events[len(events)-1].Err; err == nil || !strings.Contains(err.Error(), "ChatGPT subscription limit reached") {
		t.Fatalf("StepFailed error = %v, want the limit named", err)
	}
}

// TestCodexProvider_FailsATurnWhoseToolCallNeverCompleted: a call the backend
// opened and abandoned leaves a host waiting for input that will not arrive, so
// the turn fails instead of ending as though it had run.
func TestCodexProvider_FailsATurnWhoseToolCallNeverCompleted(t *testing.T) {
	const abandoned = `event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"{\"path\":"}

`
	server := newCodexServer(t, abandoned)

	events := drainCodex(t, NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "a", AccountID: "b"}}, server.server.URL, "gpt-5.5"), codexRequest())

	last := events[len(events)-1]
	if last.Kind != StepFailed || last.Err == nil || !strings.Contains(last.Err.Error(), "never completed") {
		t.Fatalf("the turn ended with %#v, want StepFailed naming the abandoned call", last)
	}
	if events[len(events)-2].Kind != ToolInputEnded {
		t.Fatalf("event before the failure is kind %d, want the streamed input closed first", events[len(events)-2].Kind)
	}
}

// TestCodexProvider_RefusesContentItCannotExpress: this dialect carries text only,
// and skipping a part it cannot express would put a conversation with a hole in it
// in front of the model.
func TestCodexProvider_RefusesContentItCannotExpress(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	req := codexRequest()
	req.Messages = append(req.Messages, Message{Role: "user", Parts: []Part{{Kind: PartKind(1 << 20), Text: "look"}}})

	if _, err := NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "a", AccountID: "b"}}, server.server.URL, "gpt-5.5").Stream(context.Background(), req); err == nil {
		t.Fatal("Stream accepted a part this dialect cannot express")
	}
}

// TestCodexProvider_DeclaresWhatTheDialectDoes: the description a picker reads is
// resolved without building the adapter, so the two must not be able to disagree.
func TestCodexProvider_DeclaresWhatTheDialectDoes(t *testing.T) {
	declared := DescribeCodex(WithCodexReasoning(CodexEffortMedium, CodexSummaryAuto))
	built := NewCodexProvider(&staticTokens{}, "https://chatgpt.com/backend-api/codex", "gpt-5.5",
		WithCodexReasoning(CodexEffortMedium, CodexSummaryAuto)).Capabilities()
	if declared.Reasoning != built.Reasoning || declared.PromptCaching != built.PromptCaching {
		t.Fatalf("DescribeCodex() = %#v but the built adapter declares %#v", declared, built)
	}
	if !declared.Reasoning {
		t.Error("an adapter that asks for a reasoning summary declares Reasoning false")
	}
	if DescribeCodex().Reasoning {
		t.Error("an adapter that asks for no summary declares Reasoning true: a host would wait for events that cannot arrive")
	}
	if declared.DefaultMaxOutputTokens != 0 {
		t.Errorf("DefaultMaxOutputTokens = %d, want zero: this dialect never sends a ceiling", declared.DefaultMaxOutputTokens)
	}
	if window, ok := declared.ContextWindow("gpt-5.5"); !ok || window <= 0 {
		t.Errorf("ContextWindow(gpt-5.5) = %d, %v; want the curated window", window, ok)
	}
}

// TestCodexProvider_AsksForReasoningAndVerbosityWhenTold: both are optional
// fields, and sending them unset is how an endpoint gets a request it rejects.
func TestCodexProvider_AsksForReasoningAndVerbosityWhenTold(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	provider := NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "a", AccountID: "b"}}, server.server.URL, "gpt-5.5",
		WithCodexReasoning(CodexEffortHigh, CodexSummaryAuto),
		WithCodexVerbosity(CodexVerbosityLow))

	drainCodex(t, provider, codexRequest())

	body, _, _ := server.request(t, 0)
	reasoning, _ := json.Marshal(body["reasoning"])
	if string(reasoning) != `{"effort":"high","summary":"auto"}` {
		t.Errorf("reasoning = %s, want the effort and summary asked for", reasoning)
	}
	text, _ := json.Marshal(body["text"])
	if string(text) != `{"verbosity":"low"}` {
		t.Errorf("text = %s, want the verbosity asked for", text)
	}

	plain := newCodexServer(t, codexTurn)
	drainCodex(t, NewCodexProvider(&staticTokens{token: OAuthToken{AccessToken: "a", AccountID: "b"}}, plain.server.URL, "gpt-5.5"), codexRequest())
	bare, _, _ := plain.request(t, 0)
	if _, present := bare["reasoning"]; present {
		t.Errorf("reasoning = %v with no option set, want the field omitted", bare["reasoning"])
	}
	if _, present := bare["text"]; present {
		t.Errorf("text = %v with no option set, want the field omitted", bare["text"])
	}
}
func TestPosthogResponsesProvider_RequestReasoningOverride(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	req := codexRequest()
	req.Reasoning = &ReasoningPreference{Effort: ReasoningEffortHigh}
	provider := NewPosthogResponsesProvider(&staticTokens{token: OAuthToken{AccessToken: "a"}}, server.server.URL, req.Model)
	drainCodex(t, provider, req)
	body, _, _ := server.request(t, 0)
	got, _ := json.Marshal(body["reasoning"])
	if string(got) != `{"effort":"high","summary":"auto"}` {
		t.Fatalf("reasoning = %s, want per-call high effort with the provider summary", got)
	}
}

func TestPosthogResponsesProvider_RejectsUnknownReasoningBeforeHTTP(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	req := codexRequest()
	req.Reasoning = &ReasoningPreference{Effort: ReasoningEffort("turbo")}
	provider := NewPosthogResponsesProvider(&staticTokens{token: OAuthToken{AccessToken: "a"}}, server.server.URL, req.Model)
	if _, err := provider.Stream(context.Background(), req); err == nil || !strings.Contains(err.Error(), "unsupported PostHog reasoning effort") {
		t.Fatalf("Stream error = %v, want conservative validation error", err)
	}
	if server.requests() != 0 {
		t.Fatalf("validation sent %d requests", server.requests())
	}
}
func TestCodexProvider_RejectsExplicitReasoningBeforeCredentialResolution(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	tokens := &staticTokens{err: errors.New("credential resolution must not run")}
	req := codexRequest()
	req.Reasoning = &ReasoningPreference{Effort: ReasoningEffortHigh}

	_, err := NewCodexProvider(tokens, server.server.URL, req.Model).Stream(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "does not support per-request reasoning") {
		t.Fatalf("Stream error = %v, want unsupported explicit reasoning error", err)
	}
	if tokens.count() != 0 || server.requests() != 0 {
		t.Fatalf("unsupported preference touched credential or HTTP: credential resolutions=%d, requests=%d", tokens.count(), server.requests())
	}
}

func TestPosthogResponsesProvider_RejectsIncompatibleModelBeforeCredentialResolution(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	tokens := &staticTokens{err: errors.New("credential resolution must not run")}
	provider := NewPosthogResponsesProvider(tokens, server.server.URL, "gpt-5.5")
	req := codexRequest()
	req.Model = "gemini-x"

	_, err := provider.Stream(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "PostHog") || !strings.Contains(err.Error(), "model family") {
		t.Fatalf("Stream error = %v, want PostHog model-family validation error", err)
	}
	if tokens.count() != 0 || server.requests() != 0 {
		t.Fatalf("incompatible model touched credential or HTTP: credential resolutions=%d, requests=%d", tokens.count(), server.requests())
	}
}

func TestPosthogResponsesProvider_RejectsClaudeModelBeforeCredentialResolution(t *testing.T) {
	server := newCodexServer(t, codexTurn)
	tokens := &staticTokens{err: errors.New("credential resolution must not run")}
	provider := NewPosthogResponsesProvider(tokens, server.server.URL, "gpt-5.5")
	req := codexRequest()
	req.Model = "claude-opus-4-8"

	_, err := provider.Stream(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "GPT model family") {
		t.Fatalf("Stream error = %v, want GPT-family validation error", err)
	}
	if tokens.count() != 0 || server.requests() != 0 {
		t.Fatalf("incompatible Claude model touched credential or HTTP: credential resolutions=%d, requests=%d", tokens.count(), server.requests())
	}
}
