package runner

import (
	"context"
	"errors"
	"sync"
	"testing"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

type compactionProvider struct {
	mu       sync.Mutex
	requests []llm.Request
	events   []llm.Event
}

func (p *compactionProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return llm.NewFakeProvider(p.events...).Stream(ctx, req)
}

func (p *compactionProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func validCompactionSummaryEvents() []llm.Event {
	return []llm.Event{
		{Kind: llm.TextDelta, Text: `{"current_goal":"continue current work","constraints_and_instructions":[],"decisions":[],"completed_work":[],"files_and_changes":[],"relevant_tool_results":[],"failures_and_attempts":[],"pending_and_next_step":[],"facts_not_to_reinterpret":[]}`},
		{Kind: llm.StepEnded},
	}
}

func newCompactionRunner(t *testing.T, store session.Store, provider llm.Provider) *Runner {
	t.Helper()
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"echo": true}, idCounter())
	r.SetCompactor(NewContextCompactor(store, provider))
	return r
}

func seedManualCompactionHistory(t *testing.T, store session.Store) {
	t.Helper()
	ctx := context.Background()
	for _, message := range []session.Message{
		{ID: "u1", Role: session.RoleUser, Text: "old question"},
		{ID: "a1", Role: session.RoleAssistant, Text: "old answer"},
		{ID: "u2", Role: session.RoleUser, Text: "current question"},
	} {
		message := message
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Message: &message}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunner_CompactNowCommitsCheckpointAndPreservesCurrentActivity(t *testing.T) {
	store := session.NewMemoryStore()
	seedManualCompactionHistory(t, store)
	provider := &compactionProvider{events: validCompactionSummaryEvents()}
	r := newCompactionRunner(t, store, provider)

	if err := r.CompactNow(context.Background(), "s1"); err != nil {
		t.Fatalf("CompactNow() error = %v", err)
	}

	if got := provider.callCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
	events, err := store.Events(context.Background(), "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != session.KindContextCompacted || last.Compaction == nil {
		t.Fatalf("last event = %+v, want durable Context.Compacted", last)
	}
	runnerContext, err := store.ContextForRunner(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runnerContext.Messages) != 1 || runnerContext.Messages[0].Text != "current question" {
		t.Fatalf("runner context messages = %+v, want current question preserved literally", runnerContext.Messages)
	}
}

func TestRunner_CompactNowReturnsNoCompactableHistoryWithoutProviderCall(t *testing.T) {
	store := session.NewMemoryStore()
	if _, err := store.AppendEvent(context.Background(), "s1", session.SessionEvent{
		Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "current question"},
	}); err != nil {
		t.Fatal(err)
	}
	provider := &compactionProvider{events: validCompactionSummaryEvents()}
	r := newCompactionRunner(t, store, provider)

	err := r.CompactNow(context.Background(), "s1")
	if !errors.Is(err, session.ErrNoCompactableHistory) {
		t.Fatalf("CompactNow() error = %v, want ErrNoCompactableHistory", err)
	}
	if got := provider.callCount(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

func TestCompactor_InvalidSummaryDoesNotCommitCheckpoint(t *testing.T) {
	store := session.NewMemoryStore()
	seedManualCompactionHistory(t, store)
	provider := &compactionProvider{events: []llm.Event{{Kind: llm.TextDelta, Text: `{}`}, {Kind: llm.StepEnded}}}
	r := newCompactionRunner(t, store, provider)

	err := r.CompactNow(context.Background(), "s1")
	if !errors.Is(err, session.ErrInvalidSummary) {
		t.Fatalf("CompactNow() error = %v, want ErrInvalidSummary", err)
	}
	events, loadErr := store.Events(context.Background(), "s1", 0)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	for _, event := range events {
		if event.Kind == session.KindContextCompacted {
			t.Fatalf("invalid summary committed checkpoint: %+v", event)
		}
	}
}
