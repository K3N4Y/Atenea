package providerconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"atenea/internal/llm"
)

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
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }, nil, nil)
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
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }, nil, nil, defaults)
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
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }, nil, nil, defaults)
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

func TestService_SelectSaveFailureKeepsPreviousSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(`{"providers":[{"id":"p","name":"Provider","type":"openai-compatible","base_url":"http://p","models":["one","two"]}],"selected":{"provider":"p","model":"one"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil }, func(string, Config) error { return errors.New("disk full") }, nil)
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
