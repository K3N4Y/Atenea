package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tool/tooltest"
)

const transientStreamFailure = "PostHog (gpt-5.6-luna): stream error: stream ID 61; INTERNAL_ERROR; received from peer"

func transientFailureTurn() []llm.Event {
	return []llm.Event{
		{Kind: llm.StepStarted},
		{Kind: llm.StepFailed, Text: transientStreamFailure},
	}
}

func countKind(log []session.SessionEvent, kind session.EventKind) int {
	count := 0
	for _, ev := range log {
		if ev.Kind == kind {
			count++
		}
	}
	return count
}

func TestRunner_TransientStreamErrorRetriesAndSucceeds(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	provider := &scriptedProvider{turns: [][]llm.Event{
		transientFailureTurn(),
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "ok"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tooltest.Echo{})
	r := newRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"echo": true}, idCounter())
	r.transientRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}

	cont, err := r.runTurn(ctx, "s1")
	if err != nil {
		t.Fatalf("runTurn err = %v, want nil (the retry should absorb the transient failure)", err)
	}
	if cont {
		t.Errorf("runTurn cont = true, want false (text alone does not continue)")
	}
	if calls := len(provider.capturedRequests()); calls != 2 {
		t.Errorf("provider Stream calls = %d, want 2", calls)
	}

	log := store.snapshot()
	if got := countKind(log, session.KindStepRetrying); got != 1 {
		t.Errorf("Step.Retrying count = %d, want 1", got)
	}
	if got := countKind(log, session.KindStepFailed); got != 0 {
		t.Errorf("Step.Failed count = %d, want 0 (the turn recovered)", got)
	}
	if got := countKind(log, session.KindStepEnded); got != 1 {
		t.Errorf("Step.Ended count = %d, want 1", got)
	}
	for _, ev := range log {
		if ev.Kind == session.KindStepRetrying && !strings.Contains(ev.Text, "retrying in") {
			t.Errorf("Step.Retrying.Text = %q, want the wait announced", ev.Text)
		}
	}
}

func TestRunner_TransientStreamFailureAfterToolCallPreservesMatchingDeclaration(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	provider := &scriptedProvider{turns: [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "toolu_1", ToolName: "echo", Input: []byte(`{"text":"once"}`)},
			{Kind: llm.StepFailed, Text: transientStreamFailure},
		},
		{{Kind: llm.StepStarted}, {Kind: llm.StepEnded}},
	}}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tooltest.Echo{})
	r := newRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"echo": true}, idCounter())
	r.transientRetryDelays = []time.Duration{time.Millisecond}

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn: %v", err)
	}

	requests := provider.capturedRequests()
	if len(requests) != 2 {
		t.Fatalf("provider Stream calls = %d, want 2", len(requests))
	}
	declared := false
	for _, message := range requests[1].Messages {
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				declared = declared || call.ID == "toolu_1"
			}
		}
		if message.Role == "tool" && message.ToolCallID == "toolu_1" && !declared {
			t.Fatalf("retry history contains tool_result %q without an earlier matching assistant tool_use: %+v", message.ToolCallID, requests[1].Messages)
		}
	}
	if !declared {
		t.Fatalf("retry history lost assistant tool_use %q: %+v", "toolu_1", requests[1].Messages)
	}
}

func TestRunner_TransientStreamErrorExhaustsRetriesAndFails(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	provider := &scriptedProvider{turns: [][]llm.Event{
		transientFailureTurn(),
		transientFailureTurn(),
		transientFailureTurn(),
	}}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tooltest.Echo{})
	r := newRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"echo": true}, idCounter())
	r.transientRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}

	_, err := r.runTurn(ctx, "s1")
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("runTurn err = %T %v, want *ProviderError after exhausting retries", err, err)
	}
	if providerErr.Message != transientStreamFailure {
		t.Errorf("ProviderError.Message = %q, want %q", providerErr.Message, transientStreamFailure)
	}
	if calls := len(provider.capturedRequests()); calls != 3 {
		t.Errorf("provider Stream calls = %d, want 3 (one attempt plus two retries)", calls)
	}

	log := store.snapshot()
	if got := countKind(log, session.KindStepRetrying); got != 2 {
		t.Errorf("Step.Retrying count = %d, want 2", got)
	}
	if got := countKind(log, session.KindStepFailed); got != 1 {
		t.Errorf("Step.Failed count = %d, want 1", got)
	}
}

func TestRunner_NonTransientStreamErrorDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	provider := &scriptedProvider{turns: [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.StepFailed, Text: "Anthropic (claude-x): invalid_request_error"},
		},
	}}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tooltest.Echo{})
	r := newRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"echo": true}, idCounter())

	_, err := r.runTurn(ctx, "s1")
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("runTurn err = %T %v, want *ProviderError", err, err)
	}
	if calls := len(provider.capturedRequests()); calls != 1 {
		t.Errorf("provider Stream calls = %d, want 1 (an API rejection reproduces on retry)", calls)
	}
	if got := countKind(store.snapshot(), session.KindStepRetrying); got != 0 {
		t.Errorf("Step.Retrying count = %d, want 0", got)
	}
}

func TestRunner_CancelDuringRetryBackoffClosesStep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	provider := &scriptedProvider{turns: [][]llm.Event{transientFailureTurn()}}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tooltest.Echo{})
	r := newRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"echo": true}, idCounter())
	r.transientRetryDelays = []time.Duration{time.Hour}

	done := make(chan error, 1)
	go func() {
		_, err := r.runTurn(ctx, "s1")
		done <- err
	}()

	// Cancel once the retry wait is the only thing left running.
	deadline := time.After(2 * time.Second)
	for countKind(store.snapshot(), session.KindStepRetrying) == 0 {
		select {
		case <-deadline:
			t.Fatal("Step.Retrying was never published")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runTurn did not return after cancelling the backoff wait")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("runTurn err = %T %v, want the original *ProviderError", err, err)
	}
	if got := countKind(store.snapshot(), session.KindStepFailed); got != 1 {
		t.Errorf("Step.Failed count = %d, want 1 (the step must close durably)", got)
	}
}
