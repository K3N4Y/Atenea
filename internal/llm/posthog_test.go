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

	"github.com/K3N4Y/atenea/agentcore/llm/llmtest"
)

// The PostHog gateway speaks anthropic-messages, so the adapter under test is
// AnthropicProvider built the OAuth way: no static key, a bearer resolved per
// request. The stream translation is covered by the Anthropic tests; what these
// cover is the credential path — which header goes out, and what happens when
// no credential can be resolved.

// posthogGateway is a stub gateway that serves the canned anthropic turn and
// records the auth headers every request carried.
type posthogGateway struct {
	server *httptest.Server
	mu     sync.Mutex
	auths  []string
	apiKey []string
}

func newPosthogGateway(t *testing.T) *posthogGateway {
	t.Helper()
	gateway := &posthogGateway{}
	gateway.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateway.mu.Lock()
		gateway.auths = append(gateway.auths, r.Header.Get("Authorization"))
		gateway.apiKey = append(gateway.apiKey, r.Header.Get("x-api-key"))
		gateway.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range anthropicTurn {
			io.WriteString(w, "event: message\ndata: "+event+"\n\n")
		}
	}))
	t.Cleanup(gateway.server.Close)
	return gateway
}

func TestPosthogProvider_Contract(t *testing.T) {
	llmtest.Contract(t, func(t *testing.T) llmtest.Subject {
		gateway := newPosthogGateway(t)
		return llmtest.Subject{
			Provider: NewAnthropicOAuthProvider(&staticTokens{token: OAuthToken{AccessToken: "access"}}, gateway.server.URL, "claude-opus-4-8"),
			Request:  contractRequest("claude-opus-4-8"),
		}
	})
}

func TestPosthogProvider_FailedTurnContract(t *testing.T) {
	llmtest.Contract(t, func(t *testing.T) llmtest.Subject {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`)
		}))
		t.Cleanup(server.Close)
		return llmtest.Subject{
			Provider: NewAnthropicOAuthProvider(&staticTokens{token: OAuthToken{AccessToken: "access"}}, server.URL, "claude-opus-4-8"),
			Request:  contractRequest("claude-opus-4-8"),
		}
	})
}

func TestPosthogResponsesProvider_Contract(t *testing.T) {
	llmtest.Contract(t, func(t *testing.T) llmtest.Subject {
		server := sseServer(t, func(w io.Writer) { io.WriteString(w, codexTurn) })
		return llmtest.Subject{
			Provider: NewOAuthResponsesProvider(&staticTokens{token: OAuthToken{AccessToken: "access"}}, server.URL, "gpt-5.5"),
			Request:  contractRequest("gpt-5.5"),
		}
	})
}

// TestPosthogProvider_SendsTheBearerAndNoAPIKey pins the auth decision: the
// gateway reads the OAuth access token as a bearer, and the SDK's own x-api-key
// header must not appear — an ANTHROPIC_API_KEY from the environment has no
// business authenticating a gateway request.
func TestPosthogProvider_SendsTheBearerAndNoAPIKey(t *testing.T) {
	// The SDK reads this on its own at construction; the environment here is
	// exactly the machine the leak would happen on.
	t.Setenv("ANTHROPIC_API_KEY", "sk-stray-environment-key")
	gateway := newPosthogGateway(t)
	provider := NewAnthropicOAuthProvider(&staticTokens{token: OAuthToken{AccessToken: "gateway-token"}}, gateway.server.URL, "claude-opus-4-8")

	events, err := provider.Stream(context.Background(), contractRequest("claude-opus-4-8"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range events {
	}

	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.auths) == 0 {
		t.Fatal("no request reached the gateway")
	}
	if got := gateway.auths[0]; got != "Bearer gateway-token" {
		t.Fatalf("Authorization = %q, want the resolved bearer", got)
	}
	if got := gateway.apiKey[0]; got != "" {
		t.Fatalf("x-api-key = %q, want none on the OAuth path", got)
	}
}

// TestPosthogProvider_RefusesTheTurnWhenTheCredentialCannotBeResolved: a dead
// login has to reach the user as the sentence that tells them what to do,
// before a request that could only have been rejected.
func TestPosthogProvider_RefusesTheTurnWhenTheCredentialCannotBeResolved(t *testing.T) {
	gateway := newPosthogGateway(t)
	tokens := &staticTokens{err: errors.New("the credential for provider \"posthog\" expired; log in again")}
	provider := NewAnthropicOAuthProvider(tokens, gateway.server.URL, "claude-opus-4-8")

	_, err := provider.Stream(context.Background(), contractRequest("claude-opus-4-8"))
	if err == nil || !strings.Contains(err.Error(), "log in again") {
		t.Fatalf("Stream() error = %v, want the sentence that says what to do", err)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.auths) != 0 {
		t.Fatalf("the adapter sent %d requests with no credential", len(gateway.auths))
	}
}

func TestDescribePosthog_DeclaresTheGatewayCatalog(t *testing.T) {
	capabilities := DescribePosthog()
	if !capabilities.Streaming || !capabilities.Tools {
		t.Fatal("the gateway adapter streams and calls tools")
	}
	if got := capabilities.ContextWindows["claude-opus-4-8"]; got != 1_000_000 {
		t.Fatalf("claude-opus-4-8 window = %d, want the gateway's declared 1M", got)
	}
	if got := capabilities.ContextWindows["claude-haiku-4-5"]; got != 200_000 {
		t.Fatalf("claude-haiku-4-5 window = %d, want 200K", got)
	}
	// The instance must declare the same thing the description does, or the
	// picker labels models one way and the turn behaves another.
	provider := NewAnthropicOAuthProvider(&staticTokens{}, "https://gateway.test", "claude-opus-4-8")
	built := provider.Capabilities()
	described := DescribePosthog("claude-opus-4-8")
	if built.ContextWindows["claude-opus-4-8"] != described.ContextWindows["claude-opus-4-8"] || built.Vision != described.Vision {
		t.Fatalf("DescribePosthog drifted from built Claude adapter: described=%+v built=%+v", described, built)
	}
}

func TestListPosthogModels_FiltersByPlanAndFamily(t *testing.T) {
	var sawAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sawAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"data":[
			{"id":"claude-opus-4-8","owned_by":"anthropic"},
			{"id":"claude-haiku-4-5","owned_by":"anthropic","allowed":true},
			{"id":"claude-opus-5","owned_by":"anthropic","allowed":false},
			{"id":"gpt-5.5","owned_by":"openai"},
			{"id":"text-embedding-3-small","owned_by":"openai"},
			{"id":"gpt-cloudflare","owned_by":"cloudflare"},
			{"id":"@cf/zai-org/glm-5.2","owned_by":"cloudflare"},
			{"id":"","owned_by":"anthropic"}
		]}`)
	}))
	defer server.Close()

	models, err := ListPosthogModels(context.Background(), server.URL+"/", "bearer-token")
	if err != nil {
		t.Fatalf("ListPosthogModels: %v", err)
	}
	want := []string{"claude-opus-4-8", "claude-haiku-4-5", "gpt-5.5"}
	if len(models) != len(want) {
		t.Fatalf("models = %v, want %v", models, want)
	}
	for i, model := range want {
		if models[i] != model {
			t.Fatalf("models = %v, want %v", models, want)
		}
	}
	if sawAuth != "Bearer bearer-token" {
		t.Fatalf("Authorization = %q, want the bearer", sawAuth)
	}
}

func TestListPosthogModels_ReportsFailures(t *testing.T) {
	t.Run("server failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid token"}`)
		}))
		defer server.Close()
		_, err := ListPosthogModels(context.Background(), server.URL, "stale")
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("ListPosthogModels = %v, want the status", err)
		}
	})
	t.Run("unreadable body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `not json`)
		}))
		defer server.Close()
		_, err := ListPosthogModels(context.Background(), server.URL, "token")
		if err == nil || !strings.Contains(err.Error(), "unreadable") {
			t.Fatalf("ListPosthogModels = %v, want an unreadable-response error", err)
		}
	})
}
func TestPosthogClaude_MapsReasoningEffortBeforeCredentialAndHTTP(t *testing.T) {
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	tokens := &staticTokens{token: OAuthToken{AccessToken: "access"}}
	provider := NewAnthropicOAuthProvider(tokens, server.URL, "claude-opus-4-8")
	req := contractRequest("claude-opus-4-8")
	req.Reasoning = &ReasoningPreference{Effort: ReasoningEffortHigh}

	out, err := provider.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	drain(out)

	var body struct {
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
	}
	if err := json.Unmarshal(requestBody, &body); err != nil {
		t.Fatal(err)
	}
	if body.OutputConfig.Effort != "high" {
		t.Fatalf("output_config.effort = %q, want high; request=%s", body.OutputConfig.Effort, requestBody)
	}
}

func TestPosthogCapabilitiesMatchModelAwareDescription(t *testing.T) {
	claude := NewAnthropicOAuthProvider(&staticTokens{}, "https://gateway.test", "claude-opus-4-8").Capabilities()
	gpt := NewPosthogResponsesProvider(&staticTokens{}, "https://gateway.test", "gpt-5.5").(Describing).Capabilities()
	described := DescribePosthog("claude-opus-4-8", "gpt-5.5")

	for name, item := range map[string]struct {
		caps  Capabilities
		model string
	}{
		"Claude": {caps: claude, model: "claude-opus-4-8"},
		"GPT":    {caps: gpt, model: "gpt-5.5"},
	} {
		built, model := item.caps, item.model
		if built.Streaming != described.Streaming || built.Tools != described.Tools {
			t.Errorf("%s capabilities disagree on shared behavior: built=%#v described=%#v", name, built, described)
		}
		if got, ok := built.ContextWindow(model); !ok || got != described.ContextWindows[model] {
			t.Errorf("%s ContextWindow(%q) = %d, %v; described %d", name, model, got, ok, described.ContextWindows[model])
			if built.Vision != described.Vision {
				t.Errorf("%s Vision = %v, want description %v", name, built.Vision, described.Vision)
			}
		}
		if built.Reasoning != described.ReasoningModels[model] {
			t.Errorf("%s Reasoning = %v, want model-aware description %v", name, built.Reasoning, described.ReasoningModels[model])
		}
		if built.PromptCaching != described.PromptCachingModels[model] {
			t.Errorf("%s PromptCaching = %v, want model-aware description %v", name, built.PromptCaching, described.PromptCachingModels[model])
		}
	}
}
