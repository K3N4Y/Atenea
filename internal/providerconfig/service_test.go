package providerconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
)

type inertProvider struct{}

func (inertProvider) Stream(context.Context, llm.Request) (<-chan llm.Event, error) {
	ch := make(chan llm.Event)
	close(ch)
	return ch, nil
}

// everyType is a Registry that answers with build for every wire format these
// tests declare, so a test that only cares about "some provider got built" does
// not have to enumerate formats it is not testing.
func everyType(build Factory) Registry {
	registry := Registry{}
	for _, format := range append(DefaultRegistry().Types(), "bedrock") {
		registry[format] = Format{Build: build}
	}
	return registry
}

// inertRegistry builds a provider that streams nothing, for the tests whose
// subject is the config round trip rather than the adapter.
func inertRegistry() Registry {
	return everyType(func(Provider, string, string) (llm.Provider, error) { return inertProvider{}, nil })
}

func fallbackSnapshot() llm.ProviderSnapshot {
	return llm.ProviderSnapshot{ProviderID: "demo", ProviderName: "Demo", BaseURL: "demo://local", Model: "demo", Provider: inertProvider{}}
}

func TestService_OpenUsesPersistedSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(`{"providers":[{"id":"p","name":"Provider","type":"openai-compatible","base_url":"http://p","models":["m"]}],"selected":{"provider":"p","model":"m"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, inertRegistry(), nil, nil, nil)
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
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, inertRegistry(), nil, nil, nil, defaults)
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
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, inertRegistry(), nil, nil, nil, defaults)
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

// TestService_LegacyDialectSurvivesTheRoundTripToDisk is the upgrade path a real
// user walks: a config written before the dialect became the type must still
// build the OpenAI dialect, and the next selection must persist the resolved
// type so the migration shim stops being load-bearing.
func TestService_LegacyDialectSurvivesTheRoundTripToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	legacy := `{"providers":[{"id":"openai","name":"OpenAI","type":"openai-compatible","base_url":"https://api.openai.com/v1","models":["gpt-5.6","gpt-4.1"]}],"selected":{"provider":"openai","model":"gpt-5.6"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	var built []string
	factory := func(def Provider, _ string, _ string) (llm.Provider, error) {
		built = append(built, def.Type)
		return inertProvider{}, nil
	}
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, everyType(factory), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(built) != 1 || built[0] != OpenAI {
		t.Fatalf("built types = %#v, want the OpenAI dialect resolved from the id", built)
	}

	if _, err := s.Select(context.Background(), "openai", "gpt-4.1"); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), `"type": "openai"`) || strings.Contains(string(persisted), "openai-compatible") {
		t.Fatalf("selection did not rewrite the legacy type: %s", persisted)
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
	s, err := Open(path, "", fallbackSnapshot(), func(string) string { return "" }, everyType(factory), nil, nil, credentials)
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
	if _, err := Open(path, "", fallbackSnapshot(), getenv, everyType(factory), nil, nil, credentials); err != nil {
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
	s, err := Open(filepath.Join(dir, "providers.json"), "", fallbackSnapshot(), func(string) string { return "" }, everyType(factory), nil, nil, credentials, openRouterDefaults())
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
	s, err := Open(filepath.Join(dir, "providers.json"), "", fallbackSnapshot(), func(string) string { return "" }, everyType(factory), nil, nil, credentials, defaults)
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
	s, err := Open(filepath.Join(dir, "providers.json"), "", fallbackSnapshot(), func(string) string { return "" }, inertRegistry(), nil, nil, credentials, openRouterDefaults())
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
	s, err := Open(path, "", fallbackSnapshot(), func(string) string { return "" }, everyType(factory), nil, nil, credentials)
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
	s, err := Open(path, "", fallbackSnapshot(), func(string) string { return "" }, inertRegistry(), nil, nil, credentials)
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
	s, err := Open(filepath.Join(dir, "providers.json"), "", fallbackSnapshot(), func(string) string { return "" }, inertRegistry(), nil, nil, credentials, openRouterDefaults())
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
	s, err := Open(filepath.Join(dir, "providers.json"), "", fallbackSnapshot(), func(string) string { return "" }, inertRegistry(), nil, nil, credentials, openRouterDefaults())
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
	s, err := Open(path, "", fallbackSnapshot(), os.Getenv, inertRegistry(), func(string, Config) error { return errors.New("disk full") }, nil, nil)
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

// offlineLister is the model lister for tests whose subject is the config rather
// than discovery: every endpoint refuses, so nothing reaches the network and a
// refresh still produces a full snapshot from the curated models.
func offlineLister() ModelLister {
	return func(context.Context, string, string) ([]string, error) { return nil, errors.New("offline") }
}

// shippedService opens a service over a temp config with the built-in catalog
// merged in, which is the shape both hosts actually run with — and the only shape
// in which "ships with atenea" means anything.
func shippedService(t *testing.T) (*Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "providers.json")
	s, err := Open(path, "", fallbackSnapshot(), envFrom(nil), inertRegistry(), nil, offlineLister(), nil, DefaultCatalog())
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func catalogEntry(providers []ProviderModels, id string) (ProviderModels, bool) {
	for _, provider := range providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return ProviderModels{}, false
}

// A declared endpoint is a provider like any other: it reaches the picker, it can
// be selected, and it lands in the providers.json the other host reads — which is
// the whole point of declaring it here instead of in one host's own config.
func TestService_DeclareMakesAUserEndpointSelectableAndPersistsIt(t *testing.T) {
	s, path := shippedService(t)
	endpoint := Provider{ID: "lmstudio", Name: "LM Studio", Type: OpenAICompatible, BaseURL: "http://localhost:1234/v1", LocalModels: true, Models: []string{"qwen2.5-coder"}}

	if err := s.Declare(endpoint); err != nil {
		t.Fatalf("Declare: %v", err)
	}
	entry, ok := catalogEntry(s.Catalog(), "lmstudio")
	if !ok || entry.Name != "LM Studio" || entry.BuiltIn {
		t.Fatalf("catalog entry = %#v (ok=%v), want the declared endpoint, not built in", entry, ok)
	}
	// Declaring is not selecting: the endpoint exists, the conversation has not moved.
	if got := s.Active().ProviderID; got == "lmstudio" {
		t.Fatal("Declare activated the endpoint; declaring one and chatting with it are two decisions")
	}

	active, err := s.Select(context.Background(), "lmstudio", "qwen2.5-coder")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if !active.LocalModels {
		t.Fatalf("active = %#v, want LocalModels: a host shaping a turn around it has nowhere else to read it", active)
	}

	reopened, err := Open(path, "", fallbackSnapshot(), envFrom(nil), inertRegistry(), nil, offlineLister(), nil, DefaultCatalog())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.Active(); got.ProviderID != "lmstudio" || !got.LocalModels {
		t.Fatalf("reopened active = %#v, want the declared endpoint back from disk", got)
	}
}

// Declare answers now, to someone who can fix it. That is the difference from
// loading a shared file, where an entry this build cannot use is kept for the
// build that can.
func TestService_DeclareRefusesWhatCouldNotBeUsed(t *testing.T) {
	tests := []struct {
		name string
		def  Provider
		want string
	}{
		{
			name: "an id that ships with atenea",
			def:  Provider{ID: "anthropic", Name: "Not Anthropic", Type: OpenAICompatible, BaseURL: "http://localhost:1234/v1"},
			want: "ships with atenea",
		},
		{
			name: "a wire format this build cannot speak",
			def:  Provider{ID: "gateway", Name: "Gateway", Type: "vertex", BaseURL: "https://gateway.test"},
			want: "not one this build speaks",
		},
		{
			name: "a base URL no adapter could reach",
			def:  Provider{ID: "lmstudio", Name: "LM Studio", Type: OpenAICompatible, BaseURL: "localhost:1234"},
			want: "invalid base URL",
		},
		{
			name: "no display name for the picker to show",
			def:  Provider{ID: "lmstudio", Type: OpenAICompatible, BaseURL: "http://localhost:1234/v1"},
			want: "requires id, name",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, path := shippedService(t)
			err := s.Declare(test.def)
			if err == nil {
				t.Fatalf("Declare(%#v) = nil, want an error mentioning %q", test.def, test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Declare error = %q, want it to mention %q", err, test.want)
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a rejected declaration wrote the config file (stat err=%v)", statErr)
			}
		})
	}
}

// Re-declaring an id is how a user fixes a typo in the endpoint they added, so it
// replaces rather than duplicating or refusing.
func TestService_DeclareReplacesAnEndpointWithTheSameID(t *testing.T) {
	s, _ := shippedService(t)
	if err := s.Declare(Provider{ID: "lmstudio", Name: "LM Studio", Type: OpenAICompatible, BaseURL: "http://localhost:1234/v1", Models: []string{"qwen"}}); err != nil {
		t.Fatal(err)
	}
	before := len(s.Catalog())

	if err := s.Declare(Provider{ID: "lmstudio", Name: "LM Studio", Type: OpenAICompatible, BaseURL: "http://localhost:4321/v1", Models: []string{"llama"}}); err != nil {
		t.Fatal(err)
	}
	if got := len(s.Catalog()); got != before {
		t.Fatalf("catalog grew to %d entries, want %d: re-declaring an id replaces it", got, before)
	}
	entry, _ := catalogEntry(s.Catalog(), "lmstudio")
	if len(entry.Models) != 1 || entry.Models[0] != "llama" {
		t.Fatalf("entry = %#v, want the re-declared models", entry)
	}
}

func TestService_ForgetRemovesADeclaredEndpoint(t *testing.T) {
	s, path := shippedService(t)
	if err := s.Declare(Provider{ID: "lmstudio", Name: "LM Studio", Type: OpenAICompatible, BaseURL: "http://localhost:1234/v1", Models: []string{"qwen"}}); err != nil {
		t.Fatal(err)
	}
	// A second endpoint takes the selection: the active provider cannot be
	// forgotten, and a config with no selection is not one a host reopens cleanly.
	if err := s.Declare(Provider{ID: "ollama", Name: "Ollama", Type: OpenAICompatible, BaseURL: "http://localhost:11434/v1", Models: []string{"llama"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Select(context.Background(), "ollama", "llama"); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget("lmstudio"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if entry, ok := catalogEntry(s.Catalog(), "lmstudio"); ok {
		t.Fatalf("catalog still offers %#v after forgetting it", entry)
	}
	reopened, err := Open(path, "", fallbackSnapshot(), envFrom(nil), inertRegistry(), nil, offlineLister(), nil, DefaultCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if entry, ok := catalogEntry(reopened.Catalog(), "lmstudio"); ok {
		t.Fatalf("the forgotten endpoint came back from disk: %#v", entry)
	}
}

// The two removals that would leave the host in a state it cannot explain: one
// that silently undoes itself at the next launch, and one that pulls the provider
// out from under a live conversation.
func TestService_ForgetRefusesBuiltInAndActiveProviders(t *testing.T) {
	s, _ := shippedService(t)
	if err := s.Forget("anthropic"); err == nil || !strings.Contains(err.Error(), "ships with atenea") {
		t.Fatalf("Forget(anthropic) = %v, want a refusal: the built-in catalog is merged back at every launch", err)
	}
	if err := s.Forget("nope"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Forget(nope) = %v, want it to say the provider is not configured", err)
	}

	if err := s.Declare(Provider{ID: "lmstudio", Name: "LM Studio", Type: OpenAICompatible, BaseURL: "http://localhost:1234/v1", Models: []string{"qwen"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Select(context.Background(), "lmstudio", "qwen"); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget("lmstudio"); err == nil || !strings.Contains(err.Error(), "select another provider first") {
		t.Fatalf("Forget on the active provider = %v, want it to ask for another selection first", err)
	}
}

// A host offers removal per row, so the flag has to survive every path that
// produces a row — including a refresh, which is where the desktop's picker gets
// its entries after the user asks for models.
func TestService_CatalogMarksWhatShipsWithAtenea(t *testing.T) {
	s, _ := shippedService(t)
	if err := s.Declare(Provider{ID: "lmstudio", Name: "LM Studio", Type: OpenAICompatible, BaseURL: "http://localhost:1234/v1", Models: []string{"qwen"}}); err != nil {
		t.Fatal(err)
	}
	// The offline lister makes every discovery attempt fail; the warning it joins
	// is not the subject here, the flags on the snapshot are.
	refreshed, _ := s.Refresh(context.Background())
	for _, providers := range [][]ProviderModels{s.Catalog(), refreshed} {
		for _, provider := range providers {
			if want := provider.ID != "lmstudio"; provider.BuiltIn != want {
				t.Errorf("%s BuiltIn = %v, want %v", provider.ID, provider.BuiltIn, want)
			}
		}
	}
}

// With no catalog behind it nothing ships with atenea, so a host that composes
// the service itself can forget any provider in its own config.
func TestService_WithoutADefaultCatalogNothingIsBuiltIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	s, err := Open(path, "", fallbackSnapshot(), envFrom(nil), inertRegistry(), nil, offlineLister(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Declare(Provider{ID: "anthropic", Name: "Anthropic", Type: Anthropic, BaseURL: "https://api.anthropic.com", Models: []string{"claude-opus-4-8"}}); err != nil {
		t.Fatalf("Declare: %v", err)
	}
	// The lister the host injected has to survive that first Declare: with no config
	// file there was no catalog to inherit it from, and reaching for the default
	// would have this test talking to api.anthropic.com.
	if _, err := s.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("Refresh error = %v, want the injected lister's warning", err)
	}
	if err := s.Forget("anthropic"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
}
