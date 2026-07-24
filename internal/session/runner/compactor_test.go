package runner

import (
	"context"
	"errors"
	"strings"
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

func (p *compactionProvider) capturedRequest(index int) llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[index]
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
	if got := provider.capturedRequest(0).SessionKey; got != "" {
		t.Fatalf("compaction SessionKey = %q, want empty auxiliary affinity", got)
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

func TestRunner_TurnAfterCompactionIncludesSummaryAndCurrentActivity(t *testing.T) {
	store := session.NewMemoryStore()
	seedManualCompactionHistory(t, store)
	provider := &compactionProvider{events: validCompactionSummaryEvents()}
	r := newCompactionRunner(t, store, provider)
	if err := r.CompactNow(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	provider.events = []llm.Event{{Kind: llm.StepEnded}}
	if _, err := r.runTurn(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}

	provider.mu.Lock()
	request := provider.requests[len(provider.requests)-1]
	provider.mu.Unlock()
	if !strings.Contains(request.System, "COMPACTED_SESSION_CONTEXT") || !strings.Contains(request.System, "continue current work") {
		t.Fatalf("system = %q, want compacted summary", request.System)
	}
	if len(request.Messages) != 1 || request.Messages[0].Text != "current question" {
		t.Fatalf("messages = %+v, want current activity only", request.Messages)
	}
}

func TestRunner_CompactNowPreservesAssistantToolGroupAfterCurrentUser(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	messages := []session.Message{
		{ID: "u1", Role: session.RoleUser, Text: "old"},
		{ID: "a1", Role: session.RoleAssistant, Text: "old answer"},
		{ID: "u2", Role: session.RoleUser, Text: "current"},
		{ID: "a2", Role: session.RoleAssistant, ToolCalls: []session.ToolCall{{ID: "c1", Name: "read", Arguments: `{}`}}},
		{ID: "c1", Role: session.RoleTool, ToolCallID: "c1", Text: "result"},
	}
	for _, message := range messages {
		message := message
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Message: &message}); err != nil {
			t.Fatal(err)
		}
	}
	provider := &compactionProvider{events: validCompactionSummaryEvents()}
	if err := newCompactionRunner(t, store, provider).CompactNow(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	runnerContext, err := store.ContextForRunner(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runnerContext.Messages) != 3 || runnerContext.Messages[1].Role != session.RoleAssistant || runnerContext.Messages[2].Role != session.RoleTool {
		t.Fatalf("preserved messages = %+v, want user + complete assistant/tool group", runnerContext.Messages)
	}
}

type alwaysNeedsCompaction struct{ calls int }

func (c *alwaysNeedsCompaction) NeedsCompaction(llm.Request) bool { return true }
func (c *alwaysNeedsCompaction) Compact(context.Context, string) error {
	c.calls++
	return session.ErrNoCompactableHistory
}

func TestRunner_AutomaticCompactionWithoutCompactablePrefixStillStreams(t *testing.T) {
	store := session.NewMemoryStore()
	if _, err := store.AppendEvent(context.Background(), "s1", session.SessionEvent{
		Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "current"},
	}); err != nil {
		t.Fatal(err)
	}
	provider := &compactionProvider{events: []llm.Event{{Kind: llm.StepEnded}}}
	r := newCompactionRunner(t, store, provider)
	compactor := &alwaysNeedsCompaction{}
	r.SetCompactor(compactor)
	if _, err := r.runTurn(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if compactor.calls != 1 || provider.callCount() != 1 {
		t.Fatalf("compactor calls = %d, provider calls = %d", compactor.calls, provider.callCount())
	}
}
