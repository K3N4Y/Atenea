package llmtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/agentcore/llm"
)

// A kit is only worth what it catches. These tests run every check twice: once
// against a provider whose turn honors the contract, where nothing may be
// reported, and once against a provider that breaks exactly one clause, where the
// check that owns that clause must be the one to complain. A check that stops
// catching its own violation fails here instead of silently blessing a broken
// adapter.

// recorder stands in for *testing.T so a check's complaints can be read back
// instead of failing this test. It is written from the goroutines of the
// concurrency check, so it locks.
type recorder struct {
	mu       sync.Mutex
	failures []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *recorder) reported() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.failures...)
}

// scripted replays a fixed turn and closes the channel when it ends, honoring
// cancellation on every send. It is the smallest thing that behaves like an
// adapter, so the shape of its script is the only thing under test.
type scripted struct{ turn []llm.Event }

func (s scripted) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		for _, ev := range s.turn {
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out, nil
}

// compliantTurn covers everything the checks have an opinion about: a bracketed
// text block, a tool input streamed in fragments, the complete tool call, and the
// usage that closes the turn.
func compliantTurn() []llm.Event {
	return []llm.Event{
		{Kind: llm.StepStarted},
		{Kind: llm.ReasoningStarted},
		{Kind: llm.ReasoningDelta, Text: "let me look"},
		{Kind: llm.ReasoningEnded},
		{Kind: llm.TextStarted},
		{Kind: llm.TextDelta, Text: "reading it"},
		{Kind: llm.TextEnded},
		{Kind: llm.ToolInputStarted, CallID: "c1"},
		{Kind: llm.ToolInputDelta, CallID: "c1", Input: json.RawMessage(`{"path":`)},
		{Kind: llm.ToolInputDelta, CallID: "c1", Input: json.RawMessage(`"foo.go"}`)},
		{Kind: llm.ToolInputEnded, CallID: "c1"},
		{Kind: llm.ToolCall, CallID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"foo.go"}`)},
		{Kind: llm.StepEnded, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 4}},
	}
}

func compliantSubject() Subject {
	return Subject{Provider: scripted{turn: compliantTurn()}}
}

func TestContract_PassesForACompliantProvider(t *testing.T) {
	Contract(t, func(*testing.T) Subject { return compliantSubject() })
}

func TestChecks_ReportNothingForACompliantProvider(t *testing.T) {
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			r := &recorder{}
			c.run(r, compliantSubject)
			if reported := r.reported(); len(reported) > 0 {
				t.Errorf("%s reported a compliant provider:\n%s", c.name, strings.Join(reported, "\n"))
			}
		})
	}
}

// The violating providers. A turn that breaks one rule is a script; the two that
// break the channel itself need their own Stream.

// neverCloses hands back a channel it forgets, which is what a leaked adapter
// goroutine looks like from the consumer's side.
type neverCloses struct{}

func (neverCloses) Stream(context.Context, llm.Request) (<-chan llm.Event, error) {
	out := make(chan llm.Event)
	go func() {
		time.Sleep(5 * time.Second)
		close(out)
	}()
	return out, nil
}

// nilChannel returns neither a channel nor an error, so a consumer ranging over
// the result blocks forever.
type nilChannel struct{}

func (nilChannel) Stream(context.Context, llm.Request) (<-chan llm.Event, error) {
	return nil, nil
}

// refusesTheRequest rejects the request the subject declares as valid.
type refusesTheRequest struct{}

func (refusesTheRequest) Stream(context.Context, llm.Request) (<-chan llm.Event, error) {
	return nil, errors.New("nope")
}

func TestChecks_CatchAViolatingProvider(t *testing.T) {
	// The two hang cases have to outlast the kit's own patience, so the whole
	// table runs with a timeout short enough to keep this test fast.
	restore := turnTimeout
	turnTimeout = 50 * time.Millisecond
	t.Cleanup(func() { turnTimeout = restore })

	turn := func(events ...llm.Event) func() Subject {
		return func() Subject { return Subject{Provider: scripted{turn: events}} }
	}
	provider := func(p llm.Provider) func() Subject {
		return func() Subject { return Subject{Provider: p} }
	}

	cases := []struct {
		violation string
		check     string
		subject   func() Subject
	}{
		{"a channel that is never closed", "StreamBracketsTheTurn", provider(neverCloses{})},
		{"a nil channel and no error", "StreamBracketsTheTurn", provider(nilChannel{})},
		{"a Stream that refuses a valid request", "StreamBracketsTheTurn", provider(refusesTheRequest{})},
		{"a turn with no events at all", "StreamBracketsTheTurn", turn()},
		{"a turn that does not open with StepStarted", "StreamBracketsTheTurn", turn(
			llm.Event{Kind: llm.TextStarted},
			llm.Event{Kind: llm.TextEnded},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"a turn that ends without a terminal event", "StreamBracketsTheTurn", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.TextStarted},
			llm.Event{Kind: llm.TextDelta, Text: "cut off"},
			llm.Event{Kind: llm.TextEnded},
		)},
		{"a turn with two terminal events", "StreamBracketsTheTurn", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.StepFailed, Err: errors.New("boom")},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"events after the terminal one", "StreamBracketsTheTurn", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.StepEnded},
			llm.Event{Kind: llm.TextDelta, Text: "too late"},
		)},
		{"a StepFailed with no Err", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.StepFailed, Text: "provider said no"},
		)},
		{"usage on an event that is not StepEnded", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted, Usage: &llm.Usage{InputTokens: 10}},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"a text delta with no block open", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.TextDelta, Text: "orphan"},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"a text block left open", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.TextStarted},
			llm.Event{Kind: llm.TextDelta, Text: "half"},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"nested text blocks", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.TextStarted},
			llm.Event{Kind: llm.TextStarted},
			llm.Event{Kind: llm.TextEnded},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"a reasoning delta with no block open", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.ReasoningDelta, Text: "orphan"},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"a reasoning block left open", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.ReasoningStarted},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"a tool call with no id", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.ToolCall, ToolName: "read", Input: json.RawMessage(`{}`)},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"a tool call with no name", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.ToolCall, CallID: "c1", Input: json.RawMessage(`{}`)},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"a tool call carrying a fragment instead of the complete input", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":`)},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"a tool input fragment before its block opened", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.ToolInputDelta, CallID: "c1", Input: json.RawMessage(`{"path":`)},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"a tool input block left open", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.ToolInputStarted, CallID: "c1"},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"a streamed tool input with no call id", "StreamEventsAreWellFormed", turn(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.ToolInputStarted},
			llm.Event{Kind: llm.StepEnded},
		)},
		{"a channel that outlives its cancelled context", "CancellationClosesTheChannel", provider(neverCloses{})},
		{"a nil channel and no error", "CancellationClosesTheChannel", provider(nilChannel{})},
		{"a channel that is never closed", "StreamIsSafeForConcurrentUse", provider(neverCloses{})},
	}

	for _, c := range cases {
		t.Run(c.violation+"/"+c.check, func(t *testing.T) {
			run := checkByName(t, c.check)
			r := &recorder{}
			run(r, c.subject)
			if len(r.reported()) == 0 {
				t.Errorf("%s accepted a provider with %s", c.check, c.violation)
			}
		})
	}
}

func checkByName(t *testing.T, name string) func(reporter, fresh) {
	t.Helper()
	for _, c := range checks {
		if c.name == name {
			return c.run
		}
	}
	t.Fatalf("no check named %q", name)
	return nil
}

// TestName_RendersEveryKind keeps the failure messages readable: EventKind is an
// int, so a kind the table forgot would be reported as a bare number in exactly
// the failure someone is trying to understand.
func TestName_RendersEveryKind(t *testing.T) {
	for kind := llm.StepStarted; kind <= llm.ToolInputEnded; kind++ {
		if got := name(kind); strings.HasPrefix(got, "EventKind(") {
			t.Errorf("name(%d) = %s: add the kind to the table", int(kind), got)
		}
	}
}
