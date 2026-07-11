package llm

import (
	"context"
	"testing"
)

type recordingProvider struct{ requests chan Request }

func (p *recordingProvider) Stream(_ context.Context, req Request) (<-chan Event, error) {
	p.requests <- req
	out := make(chan Event)
	close(out)
	return out, nil
}

func TestSwitchableProvider_ImmutableSnapshotsAndForcedModel(t *testing.T) {
	a := &recordingProvider{requests: make(chan Request, 1)}
	b := &recordingProvider{requests: make(chan Request, 1)}
	switcher, err := NewSwitchableProvider(ProviderSnapshot{ProviderID: "a", ProviderName: "A", BaseURL: "http://a", Model: "model-a", Provider: a})
	if err != nil {
		t.Fatal(err)
	}
	old := switcher.Acquire()
	switcher.Swap(ProviderSnapshot{ProviderID: "b", ProviderName: "B", BaseURL: "http://b", Model: "model-b", Provider: b})
	if _, err := old.Provider.Stream(context.Background(), Request{Model: old.Model}); err != nil {
		t.Fatal(err)
	}
	if got := (<-a.requests).Model; got != "model-a" {
		t.Fatalf("old model = %q", got)
	}
	if _, err := switcher.Stream(context.Background(), Request{Model: "stale"}); err != nil {
		t.Fatal(err)
	}
	if got := (<-b.requests).Model; got != "model-b" {
		t.Fatalf("new model = %q", got)
	}
}

func TestSwitchableProvider_RejectsInvalidInitialSnapshot(t *testing.T) {
	if _, err := NewSwitchableProvider(ProviderSnapshot{}); err == nil {
		t.Fatal("expected validation error")
	}
}
