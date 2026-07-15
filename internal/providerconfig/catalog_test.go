package providerconfig

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalog_SnapshotMergesConfiguredCachedAndSelected(t *testing.T) {
	c := NewCatalog(Config{Providers: []Provider{{ID: "p", Name: "Provider", Type: OpenAICompatible, BaseURL: "http://p", Models: []string{"configured"}}}, Selected: Selection{Provider: "p", Model: "selected"}}, "", nil, nil)
	c.cached = map[string][]string{"p": {"cached", "configured"}}
	got := c.Snapshot()
	want := []string{"selected", "configured", "cached"}
	if !reflect.DeepEqual(got[0].Models, want) {
		t.Fatalf("models = %#v, want %#v", got[0].Models, want)
	}
}

func TestCatalog_RefreshRetainsUsableModelsOnFailure(t *testing.T) {
	c := NewCatalog(Config{Providers: []Provider{{ID: "p", Name: "Provider", Type: OpenAICompatible, BaseURL: "http://p", Models: []string{"configured"}}}}, "", nil, func(context.Context, string, string) ([]string, error) { return nil, errors.New("offline") })
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
	})

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
	})
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
