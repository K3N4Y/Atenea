package providerconfig

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"atenea/internal/llm"
)

func TestDefaultProviderFactorySelectsExplicitCompatibilityProfile(t *testing.T) {
	tests := []struct {
		id            string
		wantField     string
		wantReasoning bool
	}{
		{id: "openai", wantField: "prompt_cache_key"},
		{id: "openrouter", wantField: "session_id", wantReasoning: true},
		{id: "custom"},
		{id: "opencode"},
		{id: "opencode-go"},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			var body []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			provider, err := defaultProviderFactory(Provider{ID: test.id, Type: OpenAICompatible, BaseURL: server.URL, OpenRouterReasoning: test.wantReasoning}, "model", "key")
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
			_, reasoning := sent["reasoning"]
			if reasoning != test.wantReasoning {
				t.Fatalf("reasoning presence = %v, want %v; body=%s", reasoning, test.wantReasoning, body)
			}
		})
	}
}

type inertProvider struct{}

func (inertProvider) Stream(context.Context, llm.Request) (<-chan llm.Event, error) {
	ch := make(chan llm.Event)
	close(ch)
	return ch, nil
}

func fallbackSnapshot() llm.ProviderSnapshot {
	return llm.ProviderSnapshot{ProviderID: "demo", ProviderName: "Demo", BaseURL: "demo://local", Model: "demo", Provider: inertProvider{}}
}

func TestService_OpenUsesPersistedSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(`{"providers":[{"id":"p","name":"Provider","type":"openai-compatible","base_url":"http://p","models":["m"]}],"selected":{"provider":"p","model":"m"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Active(); got.ProviderID != "p" || got.Model != "m" {
		t.Fatalf("active = %#v", got)
	}
}

func TestService_OpenUsesDefaultCatalogWhenConfigIsAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	defaults := Config{Providers: []Provider{{ID: "openrouter", Name: "OpenRouter", Type: OpenAICompatible, BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", Models: []string{"tencent/hy3:free", "poolside/laguna-xs-2.1:free", "cohere/north-mini-code:free"}}}}
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }, nil, nil, nil, defaults)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Catalog()
	if len(got) != 1 || len(got[0].Models) != 3 {
		t.Fatalf("catalog = %#v", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("defaults must stay in memory until selection, stat err=%v", err)
	}
}

func TestService_OpenMergesMissingDefaultProvidersIntoPersistedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(`{"providers":[{"id":"openrouter","name":"Custom Router","type":"openai-compatible","base_url":"http://custom","models":["custom-model"]}],"selected":{"provider":"openrouter","model":"custom-model"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	defaults := Config{Providers: []Provider{
		{ID: "openrouter", Name: "OpenRouter", Type: OpenAICompatible, BaseURL: "https://openrouter.ai/api/v1", Models: []string{"default-model"}},
		{ID: "openai", Name: "OpenAI", Type: OpenAICompatible, BaseURL: "https://api.openai.com/v1", Models: []string{"gpt-5.6-terra"}},
	}}
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }, nil, nil, nil, defaults)
	if err != nil {
		t.Fatal(err)
	}

	got := s.Catalog()
	if len(got) != 2 {
		t.Fatalf("catalog providers = %#v, want persisted provider plus missing default", got)
	}
	if got[0].ID != "openrouter" || got[0].Name != "Custom Router" || got[0].Models[0] != "custom-model" {
		t.Fatalf("persisted provider was overwritten: %#v", got[0])
	}
	if got[1].ID != "openai" || got[1].Models[0] != "gpt-5.6-terra" {
		t.Fatalf("missing default provider was not appended: %#v", got[1])
	}
}

func TestService_OpenResolvesKeyFromCredentialStoreWhenEnvIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	config := `{"providers":[{"id":"p","name":"Provider","type":"openai-compatible","base_url":"http://p","api_key_env":"P_KEY","models":["m"]}],"selected":{"provider":"p","model":"m"}}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	credentials := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	if err := credentials.Put("p", Credential{Type: CredentialTypeAPIKey, APIKey: "stored-key"}); err != nil {
		t.Fatal(err)
	}
	gotKey := ""
	factory := func(_ Provider, _ string, apiKey string) (llm.Provider, error) {
		gotKey = apiKey
		return inertProvider{}, nil
	}
	s, err := Open(path, "", fallbackSnapshot(), func(string) string { return "" }, factory, nil, nil, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "stored-key" {
		t.Fatalf("factory key = %q, want the stored credential", gotKey)
	}
	if got := s.Active(); got.ProviderID != "p" {
		t.Fatalf("active = %#v", got)
	}
}

func TestService_EnvironmentKeyWinsOverStoredCredential(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	config := `{"providers":[{"id":"p","name":"Provider","type":"openai-compatible","base_url":"http://p","api_key_env":"P_KEY","models":["m"]}],"selected":{"provider":"p","model":"m"}}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	credentials := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	if err := credentials.Put("p", Credential{Type: CredentialTypeAPIKey, APIKey: "stored-key"}); err != nil {
		t.Fatal(err)
	}
	gotKey := ""
	factory := func(_ Provider, _ string, apiKey string) (llm.Provider, error) {
		gotKey = apiKey
		return inertProvider{}, nil
	}
	getenv := func(name string) string {
		if name == "P_KEY" {
			return "env-key"
		}
		return ""
	}
	if _, err := Open(path, "", fallbackSnapshot(), getenv, factory, nil, nil, credentials); err != nil {
		t.Fatal(err)
	}
	if gotKey != "env-key" {
		t.Fatalf("factory key = %q, want the environment override to win", gotKey)
	}
}

func openRouterDefaults() Config {
	return Config{Providers: []Provider{
		{ID: "openrouter", Name: "OpenRouter", Type: OpenAICompatible, BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", Models: []string{"openrouter/free", "tencent/hy3:free"}},
		{ID: "openai", Name: "OpenAI", Type: OpenAICompatible, BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", Models: []string{"gpt-5.6"}},
	}}
}

func TestService_ConnectStoresKeyAndActivatesDefaultModelWhenNothingSelected(t *testing.T) {
	dir := t.TempDir()
	credentials := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	factory := func(_ Provider, _ string, apiKey string) (llm.Provider, error) { return inertProvider{}, nil }
	s, err := Open(filepath.Join(dir, "providers.json"), "", fallbackSnapshot(), func(string) string { return "" }, factory, nil, nil, credentials, openRouterDefaults())
	if err != nil {
		t.Fatal(err)
	}
	validated := ""
	s.validateKey = func(_ context.Context, provider Provider, apiKey string) error {
		validated = provider.ID + ":" + apiKey
		return nil
	}

	active, err := s.Connect(context.Background(), "openrouter", "sk-or-new")
	if err != nil {
		t.Fatal(err)
	}
	if validated != "openrouter:sk-or-new" {
		t.Fatalf("validated = %q, want the key checked before storing", validated)
	}
	credential, ok := credentials.Get("openrouter")
	if !ok || credential.APIKey != "sk-or-new" || credential.Type != CredentialTypeAPIKey {
		t.Fatalf("stored credential = %#v, ok = %v", credential, ok)
	}
	if active.ProviderID != "openrouter" || active.Model != "openrouter/free" {
		t.Fatalf("active = %#v, want OpenRouter on its default model", active)
	}
	reopened, err := Load(filepath.Join(dir, "providers.json"))
	if err != nil || reopened.Selected.Provider != "openrouter" || reopened.Selected.Model != "openrouter/free" {
		t.Fatalf("persisted selection = %#v err=%v", reopened.Selected, err)
	}
}

func TestService_ConnectAnthropicStoresKeyAndActivatesNativeProvider(t *testing.T) {
	dir := t.TempDir()
	credentials := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	defaults := Config{Providers: []Provider{{
		ID: "anthropic", Name: "Anthropic", Type: Anthropic,
		BaseURL: "https://api.anthropic.com", APIKeyEnv: "ANTHROPIC_API_KEY",
		DisableModelDiscovery: true, Models: []string{"claude-sonnet-4-5-20250929"},
	}}}
	var built Provider
	factory := func(provider Provider, _ string, _ string) (llm.Provider, error) {
		built = provider
		return inertProvider{}, nil
	}
	s, err := Open(filepath.Join(dir, "providers.json"), "", fallbackSnapshot(), func(string) string { return "" }, factory, nil, nil, credentials, defaults)
	if err != nil {
		t.Fatal(err)
	}
	s.validateKey = func(_ context.Context, provider Provider, apiKey string) error {
		if provider.Type != Anthropic || apiKey != "sk-ant-test" {
			t.Fatalf("validator got provider=%#v key=%q", provider, apiKey)
		}
		return nil
	}

	active, err := s.Connect(context.Background(), "anthropic", "sk-ant-test")
	if err != nil {
		t.Fatal(err)
	}
	if active.ProviderID != "anthropic" || active.Model != "claude-sonnet-4-5-20250929" || built.Type != Anthropic {
		t.Fatalf("active=%#v built=%#v, want native Anthropic default", active, built)
	}
}

func TestService_ConnectRejectsInvalidKeyWithoutPersisting(t *testing.T) {
	dir := t.TempDir()
	credentials := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	s, err := Open(filepath.Join(dir, "providers.json"), "", fallbackSnapshot(), func(string) string { return "" }, func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }, nil, nil, credentials, openRouterDefaults())
	if err != nil {
		t.Fatal(err)
	}
	s.validateKey = func(context.Context, Provider, string) error { return errors.New("invalid API key") }

	if _, err := s.Connect(context.Background(), "openrouter", "sk-or-bad"); err == nil {
		t.Fatal("expected the validation error")
	}
	if _, ok := credentials.Get("openrouter"); ok {
		t.Fatal("a rejected key must not be stored")
	}
	if got := s.Active().ProviderID; got != "demo" {
		t.Fatalf("active provider = %q, want the fallback untouched", got)
	}
}

func TestService_ConnectRotatesKeyOfSelectedProviderLive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	config := `{"providers":[{"id":"openrouter","name":"OpenRouter","type":"openai-compatible","base_url":"https://openrouter.ai/api/v1","api_key_env":"OPENROUTER_API_KEY","models":["m"]}],"selected":{"provider":"openrouter","model":"m"}}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	credentials := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	if err := credentials.Put("openrouter", Credential{Type: CredentialTypeAPIKey, APIKey: "sk-or-old"}); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	factory := func(_ Provider, _ string, apiKey string) (llm.Provider, error) {
		keys = append(keys, apiKey)
		return inertProvider{}, nil
	}
	s, err := Open(path, "", fallbackSnapshot(), func(string) string { return "" }, factory, nil, nil, credentials)
	if err != nil {
		t.Fatal(err)
	}
	s.validateKey = func(context.Context, Provider, string) error { return nil }

	active, err := s.Connect(context.Background(), "openrouter", "sk-or-rotated")
	if err != nil {
		t.Fatal(err)
	}
	if active.Model != "m" {
		t.Fatalf("active model = %q, want the existing selection kept", active.Model)
	}
	if len(keys) != 2 || keys[1] != "sk-or-rotated" {
		t.Fatalf("factory keys = %#v, want the live provider rebuilt with the rotated key", keys)
	}
}

func TestService_ConnectLeavesOtherSelectedProviderAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	config := `{"providers":[{"id":"openrouter","name":"OpenRouter","type":"openai-compatible","base_url":"https://openrouter.ai/api/v1","api_key_env":"OPENROUTER_API_KEY","models":["m"]},{"id":"local","name":"Local","type":"openai-compatible","base_url":"http://localhost:1234/v1","models":["llama"]}],"selected":{"provider":"local","model":"llama"}}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	credentials := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	s, err := Open(path, "", fallbackSnapshot(), func(string) string { return "" }, func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }, nil, nil, credentials)
	if err != nil {
		t.Fatal(err)
	}
	s.validateKey = func(context.Context, Provider, string) error { return nil }

	active, err := s.Connect(context.Background(), "openrouter", "sk-or-new")
	if err != nil {
		t.Fatal(err)
	}
	if active.ProviderID != "local" || active.Model != "llama" {
		t.Fatalf("active = %#v, want the local selection untouched", active)
	}
	if credential, ok := credentials.Get("openrouter"); !ok || credential.APIKey != "sk-or-new" {
		t.Fatalf("credential = %#v, ok = %v", credential, ok)
	}
}

func TestService_ConnectableListsOnlyOpenRouterWithConnectionState(t *testing.T) {
	dir := t.TempDir()
	credentials := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	s, err := Open(filepath.Join(dir, "providers.json"), "", fallbackSnapshot(), func(string) string { return "" }, func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }, nil, nil, credentials, openRouterDefaults())
	if err != nil {
		t.Fatal(err)
	}
	got := s.Connectable()
	if len(got) != 1 || got[0].ID != "openrouter" || got[0].Connected {
		t.Fatalf("connectable = %#v, want only OpenRouter, not connected", got)
	}
	if err := credentials.Put("openrouter", Credential{Type: CredentialTypeAPIKey, APIKey: "sk"}); err != nil {
		t.Fatal(err)
	}
	got = s.Connectable()
	if len(got) != 1 || !got[0].Connected {
		t.Fatalf("connectable = %#v, want OpenRouter connected after storing a key", got)
	}
}

func TestService_ConnectRejectsUnsupportedProviderAndEmptyKey(t *testing.T) {
	dir := t.TempDir()
	credentials := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	s, err := Open(filepath.Join(dir, "providers.json"), "", fallbackSnapshot(), func(string) string { return "" }, func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }, nil, nil, credentials, openRouterDefaults())
	if err != nil {
		t.Fatal(err)
	}
	s.validateKey = func(context.Context, Provider, string) error { return nil }
	if _, err := s.Connect(context.Background(), "openai", "sk-oai"); err == nil {
		t.Fatal("expected /connect to reject a provider outside the supported set")
	}
	if _, err := s.Connect(context.Background(), "openrouter", "   "); err == nil {
		t.Fatal("expected /connect to reject an empty key")
	}
}

func TestService_SelectSaveFailureKeepsPreviousSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(`{"providers":[{"id":"p","name":"Provider","type":"openai-compatible","base_url":"http://p","models":["one","two"]}],"selected":{"provider":"p","model":"one"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }, func(string, Config) error { return errors.New("disk full") }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Select(context.Background(), "p", "two"); err == nil {
		t.Fatal("expected save error")
	}
	if got := s.Active().Model; got != "one" {
		t.Fatalf("active model = %q", got)
	}
	if got := s.Provider().Acquire().Model; got != "one" {
		t.Fatalf("snapshot model = %q", got)
	}
}
