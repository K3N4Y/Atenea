package providerconfig

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
)

// TestRegistry_BuildsWireFormatDeclaredByType pins the dialect to the declared
// type and to nothing else: the same id built under two types must produce two
// different request shapes.
func TestRegistry_BuildsWireFormatDeclaredByType(t *testing.T) {
	tests := []struct {
		name          string
		providerType  string
		reasoning     bool
		wantField     string
		wantReasoning bool
	}{
		{name: "openai", providerType: OpenAI, wantField: "prompt_cache_key"},
		{name: "openrouter", providerType: OpenRouter, reasoning: true, wantField: "session_id", wantReasoning: true},
		{name: "openrouter without reasoning", providerType: OpenRouter, wantField: "session_id"},
		{name: "neutral", providerType: OpenAICompatible},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			def := Provider{ID: "same-id-every-time", Type: test.providerType, BaseURL: server.URL, OpenRouterReasoning: test.reasoning}
			provider, err := DefaultRegistry().Build(def, "model", "key")
			if err != nil {
				t.Fatal(err)
			}
			stream, err := provider.Stream(context.Background(), llm.Request{SessionKey: "opaque-key"})
			if err != nil {
				t.Fatal(err)
			}
			for range stream {
			}
			var sent map[string]any
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"prompt_cache_key", "session_id"} {
				_, exists := sent[field]
				if (field == test.wantField) != exists {
					t.Fatalf("field %q presence = %v, want %v; body=%s", field, exists, field == test.wantField, body)
				}
			}
			if _, reasoning := sent["reasoning"]; reasoning != test.wantReasoning {
				t.Fatalf("reasoning presence = %v, want %v; body=%s", reasoning, test.wantReasoning, body)
			}
		})
	}
}

func TestRegistry_BuildsNativeAnthropicProvider(t *testing.T) {
	provider, err := DefaultRegistry().Build(Provider{ID: "anthropic", Type: Anthropic, BaseURL: "https://api.anthropic.com"}, "claude-opus-4-8", "key")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(*llm.AnthropicProvider); !ok {
		t.Fatalf("provider = %T, want the native Anthropic adapter", provider)
	}
}

// TestRegistry_BuildUnknownTypeNamesRegisteredTypes: the registry is the only
// place that knows what this build speaks, so its error has to say it.
func TestRegistry_BuildUnknownTypeNamesRegisteredTypes(t *testing.T) {
	_, err := DefaultRegistry().Build(Provider{ID: "x", Type: "bedrock", BaseURL: "http://x"}, "model", "key")
	if err == nil {
		t.Fatal("expected an unsupported type error")
	}
	for _, want := range []string{`"bedrock"`, Anthropic, OpenAI, OpenAICompatible, OpenRouter} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestRegistry_DefaultRegistryCopiesAreIndependent(t *testing.T) {
	extended := DefaultRegistry()
	extended["bedrock"] = Format{Build: func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }}
	if _, ok := DefaultRegistry()["bedrock"]; ok {
		t.Fatal("extending one registry reached the defaults")
	}
}

// TestRegistry_ExtraTypeIsUsableEndToEnd is the point of R3.1: a wire format
// this package never heard of becomes usable by registering a factory, with no
// edit to config validation, the service, or the catalog.
func TestRegistry_ExtraTypeIsUsableEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	config := `{"providers":[{"id":"b","name":"Bedrock","type":"bedrock","base_url":"http://b","models":["nova"]}],"selected":{"provider":"b","model":"nova"}}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := DefaultRegistry()
	built := ""
	registry["bedrock"] = Format{Build: func(def Provider, model, _ string) (llm.Provider, error) {
		built = def.ID + ":" + model
		return inertProvider{}, nil
	}}

	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, registry, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if built != "b:nova" {
		t.Fatalf("built = %q, want the registered factory to have run", built)
	}
	if got := s.Active(); got.ProviderID != "b" || got.Model != "nova" {
		t.Fatalf("active = %#v", got)
	}
}

// TestRegistry_UnknownTypeSurfacesOnlyOnTheProviderThatDeclaresIt: a build
// without the factory must still read the shared config — the other providers
// stay usable and only selecting the unknown one fails.
func TestRegistry_UnknownTypeSurfacesOnlyOnTheProviderThatDeclaresIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	config := `{"providers":[{"id":"b","name":"Bedrock","type":"bedrock","base_url":"http://b","models":["nova"]},{"id":"local","name":"Local","type":"openai-compatible","base_url":"http://local","models":["qwen"]}],"selected":{"provider":"local","model":"qwen"}}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Catalog(); len(got) != 2 {
		t.Fatalf("catalog = %#v, want both providers kept", got)
	}
	if got := s.Active(); got.ProviderID != "local" {
		t.Fatalf("active = %#v, want the speakable provider selected", got)
	}
	if _, err := s.Select(context.Background(), "b", "nova"); err == nil || !strings.Contains(err.Error(), "bedrock") {
		t.Fatalf("Select err = %v, want it to name the unsupported type", err)
	}
}

// TestRegistry_DescribesWithoutBuilding is what the model picker depends on: it
// labels every model of every configured provider, and all but the selected one
// are never constructed.
func TestRegistry_DescribesWithoutBuilding(t *testing.T) {
	registry := DefaultRegistry()

	anthropic, ok := registry.Describe(Provider{ID: "anthropic", Type: Anthropic})
	if !ok {
		t.Fatal("the anthropic format must describe itself")
	}
	if window, known := anthropic.ContextWindow("claude-opus-4-8"); !known || window != 200_000 {
		t.Fatalf("anthropic window = (%d, %v), want (200000, true)", window, known)
	}

	openAI, _ := registry.Describe(Provider{ID: "openai", Type: OpenAI})
	if _, known := openAI.ContextWindow("claude-opus-4-8"); known {
		t.Error("a format must only answer for the model ids its own dialect names")
	}
}

// TestRegistry_DescribeReadsTheProviderDefinition: OpenRouter's reasoning is a
// per-provider opt-out, so the description has to be of this provider, not of the
// format in the abstract.
func TestRegistry_DescribeReadsTheProviderDefinition(t *testing.T) {
	registry := DefaultRegistry()
	with, _ := registry.Describe(Provider{ID: "openrouter", Type: OpenRouter, OpenRouterReasoning: true})
	without, _ := registry.Describe(Provider{ID: "openrouter", Type: OpenRouter})
	if !with.Reasoning || without.Reasoning {
		t.Fatalf("Reasoning with=%v without=%v, want true then false", with.Reasoning, without.Reasoning)
	}
}

// TestRegistry_DescribeIsSilentForWhatItCannotSpeak: an unknown type, or a format
// registered with a factory and no description, says nothing — which a host reads
// as "unknown", never as "declares nothing".
func TestRegistry_DescribeIsSilentForWhatItCannotSpeak(t *testing.T) {
	registry := DefaultRegistry()
	registry["bedrock"] = Format{Build: func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }}

	if _, ok := registry.Describe(Provider{ID: "x", Type: "vertex"}); ok {
		t.Error("a type this build does not know cannot be described")
	}
	if _, ok := registry.Describe(Provider{ID: "b", Type: "bedrock"}); ok {
		t.Error("a format registered without a Describe declared nothing")
	}
}
