package providerconfig

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
	"time"
)

func TestCatalog_SnapshotMergesConfiguredCachedAndSelected(t *testing.T) {
	c := NewCatalog(Config{Providers: []Provider{{ID: "p", Name: "Provider", Type: OpenAICompatible, BaseURL: "http://p", Models: []string{"configured"}}}, Selected: Selection{Provider: "p", Model: "selected"}}, "", nil, nil, nil, nil)
	c.cached = map[string][]string{"p": {"cached", "configured"}}
	got := c.Snapshot()
	want := []string{"selected", "configured", "cached"}
	if !reflect.DeepEqual(got[0].Models, want) {
		t.Fatalf("models = %#v, want %#v", got[0].Models, want)
	}
}

func TestCatalog_RefreshRetainsUsableModelsOnFailure(t *testing.T) {
	c := NewCatalog(Config{Providers: []Provider{{ID: "p", Name: "Provider", Type: OpenAICompatible, BaseURL: "http://p", Models: []string{"configured"}}}}, "", nil, func(context.Context, string, string) ([]string, error) { return nil, errors.New("offline") }, nil, nil)
	got, err := c.Refresh(context.Background())
	if err == nil {
		t.Fatal("expected warning")
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Models, []string{"configured"}) {
		t.Fatalf("catalog = %#v", got)
	}
}

func TestCatalog_RefreshSkipsProvidersWithDiscoveryDisabled(t *testing.T) {
	var calls atomic.Int32
	c := NewCatalog(Config{Providers: []Provider{{
		ID: "openai", Name: "OpenAI", Type: OpenAICompatible, BaseURL: "https://api.openai.com/v1",
		DisableModelDiscovery: true, Models: []string{"gpt-5.6-terra"},
	}}}, "", nil, func(context.Context, string, string) ([]string, error) {
		calls.Add(1)
		return []string{"gpt-image-2"}, nil
	}, nil, nil)

	got, err := c.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("model lister calls = %d, want 0", calls.Load())
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Models, []string{"gpt-5.6-terra"}) {
		t.Fatalf("catalog = %#v, want curated models only", got)
	}
}

func TestCatalog_RefreshUsesStoredCredentialWhenEnvIsEmpty(t *testing.T) {
	credentials := NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err := credentials.Put("p", Credential{Type: CredentialTypeAPIKey, APIKey: "stored-key"}); err != nil {
		t.Fatal(err)
	}
	gotKey := ""
	c := NewCatalog(Config{Providers: []Provider{{ID: "p", Name: "Provider", Type: OpenAICompatible, BaseURL: "http://p", APIKeyEnv: "P_KEY"}}}, "",
		func(string) string { return "" },
		func(_ context.Context, _ string, apiKey string) ([]string, error) {
			gotKey = apiKey
			return []string{"remote"}, nil
		}, NewCredentialResolver(credentials), nil)
	if _, err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotKey != "stored-key" {
		t.Fatalf("lister key = %q, want the stored credential", gotKey)
	}
}

func TestCatalog_ConcurrentRefreshesShareInflightResult(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c := NewCatalog(Config{Providers: []Provider{{ID: "p", Name: "Provider", Type: OpenAICompatible, BaseURL: "http://p"}}}, "", nil, func(context.Context, string, string) ([]string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []string{"remote"}, nil
	}, nil, nil)
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			if _, err := c.Refresh(context.Background()); err != nil {
				t.Errorf("Refresh: %v", err)
			}
		}()
	}
	<-started
	time.Sleep(25 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("list calls = %d, want 1", got)
	}
}

// TestCatalog_SnapshotCarriesWhatEachFormatDeclares: the picker labels models of
// providers that were never built, so the description has to travel with the
// catalog entry rather than be asked of a live provider.
func TestCatalog_SnapshotCarriesWhatEachFormatDeclares(t *testing.T) {
	c := NewCatalog(Config{Providers: []Provider{
		{ID: "anthropic", Name: "Anthropic", Type: Anthropic, BaseURL: "https://api.anthropic.com", Models: []string{"claude-opus-4-8"}},
		{ID: "local", Name: "Local", Type: "vertex", BaseURL: "http://local", Models: []string{"gemini"}},
	}}, "", nil, nil, nil, nil)

	got := c.Snapshot()
	if window, ok := got[0].Capabilities.ContextWindow("claude-opus-4-8"); !ok || window != 200_000 {
		t.Fatalf("anthropic entry window = (%d, %v), want (200000, true)", window, ok)
	}
	if len(got[1].Capabilities.ContextWindows) != 0 {
		t.Fatalf("a type this build cannot speak must add nothing: %#v", got[1].Capabilities)
	}
}

// TestCloneProviderModels_DeepCopiesDeclaredWindows: a clone the caller can
// mutate must not reach the table the adapter still owns.
func TestCloneProviderModels_DeepCopiesDeclaredWindows(t *testing.T) {
	original := []ProviderModels{{
		ID: "p", Models: []string{"m"},
		Capabilities: llm.Capabilities{ContextWindows: map[string]int{"m": 100}},
	}}
	clone := CloneProviderModels(original)
	clone[0].Capabilities.ContextWindows["m"] = 1
	if original[0].Capabilities.ContextWindows["m"] != 100 {
		t.Fatal("mutating the clone reached the original")
	}
}
