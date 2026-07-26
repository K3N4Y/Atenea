// Package llmtest is the contract test kit for agentcore/llm: the executable
// half of what the Provider interface promises in prose.
//
// An adapter is the one place where a vendor's wire format meets the agent loop,
// and almost everything that can go wrong there goes wrong in the shape of the
// stream rather than in its content — a channel that is never closed, a turn that
// stops without closing, a tool call with no id, a block that opens and never
// ends. None of those show up as a compile error and all of them break a host.
// Contract is the pass that finds them: hand it a factory that builds your
// provider plus the request to stream, and it drives one turn and reads the shape
// of what came out.
//
// It reads the request in one respect only, and for the same reason: an adapter
// handed content of a kind it cannot express must say so. That failure leaves no
// trace in the stream at all if the adapter simply skips the part, which is
// exactly why a host cannot find it and the kit has to.
//
// It says nothing about whether the model answered well, or about how the adapter
// maps its own SDK — those are the adapter's own tests. It answers the question a
// host has to answer before it can drive an adapter it did not write: does this
// stream behave like a turn.
//
// Run it under -race. The concurrency check only means something there.
package llmtest

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/agentcore/llm"
)

// Subject is one provider under test together with the request to stream.
//
// The request has to be one the provider accepts and answers — a model it serves,
// a message to reply to — because the kit reads the shape of a real turn. What is
// in that turn is up to the implementer; the most revealing subject is one whose
// turn covers text and a tool call, since that is where the bracketing rules have
// something to say.
type Subject struct {
	Provider llm.Provider
	Request  llm.Request
}

// Contract runs every check of the llm.Provider contract against the provider
// newSubject builds, one subtest per check.
//
// newSubject receives the subtest's *testing.T, so it may stand up a stub server
// with t.Cleanup. It is called once per check and each check streams at least one
// turn, so the subject it returns must be good for a turn every time.
func Contract(t *testing.T, newSubject func(t *testing.T) Subject) {
	t.Helper()
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			c.run(t, func() Subject { return newSubject(t) })
		})
	}
}

// reporter is the part of *testing.T a check uses. Checks report through this
// interface instead of taking a *testing.T so the kit's own tests can feed it
// deliberately broken providers and assert that it complains: a contract kit that
// silently passes everything is worse than no kit at all.
//
// It has no Fatalf on purpose. A check that cannot continue returns.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// fresh builds a subject good for one turn.
type fresh func() Subject

type check struct {
	name string
	run  func(r reporter, next fresh)
}

var checks = []check{
	{"StreamBracketsTheTurn", checkBracketing},
	{"StreamEventsAreWellFormed", checkWellFormed},
	{"UnspeakableContentIsRefused", checkContentRefusal},
	{"CancellationClosesTheChannel", checkCancellation},
	{"StreamIsSafeForConcurrentUse", checkConcurrentUse},
	{"DeclaredCapabilitiesAreUsable", checkCapabilities},
}

// turnTimeout bounds how long the kit waits for a turn to close. It is generous
// on purpose: the kit does not measure how fast a provider is, it only refuses to
// wait forever for a channel that will never close. It is a variable so this
// package's own tests can lower it and still cover the hang they have to provoke.
var turnTimeout = 60 * time.Second

// checkCapabilities reads what the adapter declares, if it declares anything.
// Describing is optional, so saying nothing is not a failure here — but a
// declaration a host cannot use is, because it fails silently: a zero window
// disables preventive compaction without a word, and an answer that changes
// between two reads makes a UI flicker between two truths.
func checkCapabilities(r reporter, next fresh) {
	r.Helper()
	provider := next().Provider

	first, declared := llm.CapabilitiesOf(provider)
	if !declared {
		return
	}
	for model, window := range first.ContextWindows {
		if window <= 0 {
			r.Errorf("model %q declares a context window of %d: a host reads that as unknown and silently stops compacting for it — leave the model out instead of declaring a window it cannot divide by", model, window)
		}
	}
	if first.DefaultMaxOutputTokens < 0 {
		r.Errorf("DefaultMaxOutputTokens is %d: a host reserves it in its context estimate, so a negative one under-counts the request. Zero means the adapter imposes no ceiling", first.DefaultMaxOutputTokens)
	}
	second, _ := llm.CapabilitiesOf(provider)
	if !reflect.DeepEqual(first, second) {
		r.Errorf("two reads of Capabilities disagree (%#v then %#v): it is read on every frame, so a host has no way to show a value that moves under it", first, second)
	}
}

func checkBracketing(r reporter, next fresh) {
	r.Helper()
	subject := next()

	events, closed := drain(r, context.Background(), subject)
	if !closed {
		return
	}
	if len(events) == 0 {
		r.Errorf("the turn produced no events at all: a host cannot tell an empty answer from a provider that silently did nothing")
		return
	}
	if first := events[0].Kind; first != llm.StepStarted {
		r.Errorf("the first event of the turn is %s, want StepStarted: a host opens the turn on it — it is what lets a UI show the turn as running before the first token arrives", name(first))
	}

	terminals := 0
	for i, ev := range events {
		if !terminal(ev.Kind) {
			continue
		}
		terminals++
		if i != len(events)-1 {
			r.Errorf("%s is event %d of %d: the terminal event closes the turn, so nothing may follow it — the events after it are %s",
				name(ev.Kind), i+1, len(events), summarize(events[i+1:]))
		}
	}
	switch terminals {
	case 1: // the contract
	case 0:
		r.Errorf("the turn ended without StepEnded or StepFailed (%s): a host materializes the assistant's message when the turn closes, so a stream that just stops loses the turn from the history",
			summarize(events))
	default:
		r.Errorf("the turn carries %d terminal events (%s): exactly one StepEnded or StepFailed closes a turn", terminals, summarize(events))
	}
}

func checkWellFormed(r reporter, next fresh) {
	r.Helper()
	subject := next()

	events, _ := drain(r, context.Background(), subject)

	// Open text and reasoning blocks, and open tool inputs by call id: every
	// delta belongs to a block that was opened, and every block that opened has
	// to close before the turn does.
	textOpen, reasoningOpen := false, false
	inputsOpen := map[string]bool{}

	for i, ev := range events {
		where := fmt.Sprintf("event %d (%s)", i+1, name(ev.Kind))

		if ev.Usage != nil && ev.Kind != llm.StepEnded {
			r.Errorf("%s carries Usage: a host accounts the turn's tokens once, on StepEnded", where)
		}

		switch ev.Kind {
		case llm.StepFailed:
			if ev.Err == nil {
				r.Errorf("%s carries no Err: the error IS the payload of StepFailed — a host classifies the failure through it (errors.As), and text it can only show", where)
			}

		case llm.TextStarted:
			if textOpen {
				r.Errorf("%s opens a text block while one is already open: blocks do not nest", where)
			}
			textOpen = true
		case llm.TextDelta:
			if !textOpen {
				r.Errorf("%s arrives with no text block open: a consumer appends a delta to the block TextStarted opened", where)
			}
		case llm.TextEnded:
			if !textOpen {
				r.Errorf("%s closes a text block that was never opened", where)
			}
			textOpen = false

		case llm.ReasoningStarted:
			if reasoningOpen {
				r.Errorf("%s opens a reasoning block while one is already open: blocks do not nest", where)
			}
			reasoningOpen = true
		case llm.ReasoningDelta:
			if !reasoningOpen {
				r.Errorf("%s arrives with no reasoning block open: a consumer appends a delta to the block ReasoningStarted opened", where)
			}
		case llm.ReasoningEnded:
			if !reasoningOpen {
				r.Errorf("%s closes a reasoning block that was never opened", where)
			}
			reasoningOpen = false

		case llm.ToolInputStarted:
			if ev.CallID == "" {
				r.Errorf("%s carries no CallID: the id is what pairs the streamed input with the tool call it belongs to", where)
				break
			}
			if inputsOpen[ev.CallID] {
				r.Errorf("%s opens the input of call %q twice", where, ev.CallID)
			}
			inputsOpen[ev.CallID] = true
		case llm.ToolInputDelta:
			if ev.CallID == "" {
				r.Errorf("%s carries no CallID: a consumer cannot tell which call's input this fragment extends", where)
				break
			}
			if !inputsOpen[ev.CallID] {
				r.Errorf("%s extends the input of call %q before ToolInputStarted opened it", where, ev.CallID)
			}
		case llm.ToolInputEnded:
			if ev.CallID == "" {
				r.Errorf("%s carries no CallID", where)
				break
			}
			if !inputsOpen[ev.CallID] {
				r.Errorf("%s closes the input of call %q, which was never opened", where, ev.CallID)
			}
			delete(inputsOpen, ev.CallID)

		case llm.ToolCall:
			if ev.CallID == "" {
				r.Errorf("%s carries no CallID: the id is what the tool's result is paired back with, and a provider that omits it makes the call unanswerable", where)
			}
			if ev.ToolName == "" {
				r.Errorf("%s carries no ToolName: a host resolves the tool to settle by name", where)
			}
			// Empty is how a tool with no arguments comes back. Anything else has
			// to be the complete, valid JSON of the input: a host hands it to the
			// tool as-is and persists it as the model's own arguments.
			if len(ev.Input) > 0 && !json.Valid(ev.Input) {
				r.Errorf("%s carries an Input that is not valid JSON (%s): ToolCall carries the COMPLETE input — the fragments belong to ToolInputDelta", where, ev.Input)
			}
			if inputsOpen[ev.CallID] {
				r.Errorf("%s arrives while the streamed input of call %q is still open: close it with ToolInputEnded first", where, ev.CallID)
			}
		}
	}

	if textOpen {
		r.Errorf("the turn ended with a text block still open: a consumer is left waiting for the TextEnded that never came")
	}
	if reasoningOpen {
		r.Errorf("the turn ended with a reasoning block still open: a consumer is left waiting for the ReasoningEnded that never came")
	}
	for callID := range inputsOpen {
		r.Errorf("the turn ended with the streamed input of call %q still open", callID)
	}
}

// unspeakableKind is a content part of a kind no build of this contract defines,
// which is how the kit gets hold of content that every adapter must refuse —
// including one that has grown to speak images, since there is always a kind
// newer than the adapter reading it.
const unspeakableKind = llm.PartKind(1 << 30)

// checkContentRefusal streams a turn whose last message carries content the
// adapter cannot express, and insists that the adapter says so.
//
// The failure this exists for is silent by construction: an adapter that skips
// the part it does not understand sends the model a conversation with a hole in
// it, and the answer that comes back is wrong for a reason nothing recorded — not
// the stream, not the history, not the user's screen. Refusing costs a turn;
// dropping costs the trust in every answer after it.
//
// Either shape of refusal is accepted, because they cost a host the same: Stream
// declining the request outright (the earliest and cheapest, since no turn opens
// and no channel exists), or a turn that opens and fails with the cause in Err.
func checkContentRefusal(r reporter, next fresh) {
	r.Helper()
	subject := next()

	unspeakable := llm.Message{Role: "user", Parts: []llm.Part{{Kind: unspeakableKind, Text: "look at this"}}}
	subject.Request.Messages = append(append([]llm.Message(nil), subject.Request.Messages...), unspeakable)

	out, err := subject.Provider.Stream(context.Background(), subject.Request)
	if err != nil {
		return
	}
	if out == nil {
		r.Errorf("Stream returned a nil channel and no error: a consumer ranges over the channel, so nil is a deadlock")
		return
	}
	events, closed := collect(r, out)
	if !closed {
		return
	}
	for _, ev := range events {
		if ev.Kind != llm.StepFailed {
			continue
		}
		if ev.Err == nil {
			r.Errorf("the turn refused a %s part but StepFailed carries no Err: a host classifies with errors.As, and unexpressible content is the one failure it can actually act on by offering another model", unspeakableKind)
		}
		return
	}
	r.Errorf("a message carrying a %s part produced a turn that did not fail (%s): an adapter that cannot express a part must refuse the turn — skipping it sends the model a conversation with a hole in it, and nothing downstream can tell that is what happened",
		unspeakableKind, summarize(events))
}

func checkCancellation(r reporter, next fresh) {
	r.Helper()
	subject := next()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out, err := subject.Provider.Stream(ctx, subject.Request)
	// Refusing outright is a legitimate answer: there is no channel, so there is
	// nothing that can be left open.
	if err != nil {
		return
	}
	if out == nil {
		r.Errorf("Stream returned a nil channel and no error: a consumer ranges over the channel, so nil is a deadlock")
		return
	}
	cancel()

	// Interrupting a turn is the shape a user interruption takes, and the only
	// thing a host needs from it is the same thing it needs from a turn that
	// ended: the channel closes. Whatever events were already in flight are fine.
	deadline := time.After(turnTimeout)
	for {
		select {
		case _, ok := <-out:
			if !ok {
				return
			}
		case <-deadline:
			r.Errorf("the channel was still open %s after the context was cancelled: cancelling interrupts the turn and must close the channel too, or the consumer draining it is left hanging and the goroutine behind it leaks", turnTimeout)
			return
		}
	}
}

func checkConcurrentUse(r reporter, next fresh) {
	r.Helper()

	// A host streams several turns at once — a main turn and the subagents it
	// spawned share one provider — so two turns in flight is the normal case, not
	// an edge one. Both have to close.
	const concurrent = 2
	subjects := make([]Subject, concurrent)
	for i := range subjects {
		subjects[i] = next()
	}

	var wg sync.WaitGroup
	wg.Add(concurrent)
	for _, subject := range subjects {
		go func() {
			defer wg.Done()
			drain(r, context.Background(), subject)
		}()
	}
	wg.Wait()
}

// drain streams one turn and returns its events, reporting a channel that never
// closes: a consumer drains a turn with `for ev := range out`, so a channel left
// open is a hung session and a leaked goroutine, not a slow answer.
func drain(r reporter, ctx context.Context, subject Subject) (events []llm.Event, closed bool) {
	r.Helper()

	out, err := subject.Provider.Stream(ctx, subject.Request)
	if err != nil {
		r.Errorf("Stream refused the request the subject declares as valid: %v", err)
		return nil, false
	}
	if out == nil {
		r.Errorf("Stream returned a nil channel and no error: a consumer ranges over the channel, so nil is a deadlock")
		return nil, false
	}
	return collect(r, out)
}

// collect reads a turn's channel to the end, reporting one that never closes.
func collect(r reporter, out <-chan llm.Event) (events []llm.Event, closed bool) {
	r.Helper()

	deadline := time.After(turnTimeout)
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				return events, true
			}
			events = append(events, ev)
		case <-deadline:
			r.Errorf("the turn's channel was not closed within %s, after %d events (%s): Stream produces exactly one turn and closes the channel when it ends",
				turnTimeout, len(events), summarize(events))
			return events, false
		}
	}
}

// terminal reports whether the kind closes a turn.
func terminal(kind llm.EventKind) bool {
	return kind == llm.StepEnded || kind == llm.StepFailed
}

// name renders an EventKind for a failure message. EventKind is an int, so
// without this a failure would read "the first event is 4".
func name(kind llm.EventKind) string {
	names := [...]string{
		llm.StepStarted:      "StepStarted",
		llm.StepEnded:        "StepEnded",
		llm.StepFailed:       "StepFailed",
		llm.StepRetrying:     "StepRetrying",
		llm.TextStarted:      "TextStarted",
		llm.TextDelta:        "TextDelta",
		llm.TextEnded:        "TextEnded",
		llm.ReasoningStarted: "ReasoningStarted",
		llm.ReasoningDelta:   "ReasoningDelta",
		llm.ReasoningEnded:   "ReasoningEnded",
		llm.ToolCall:         "ToolCall",
		llm.ToolInputStarted: "ToolInputStarted",
		llm.ToolInputDelta:   "ToolInputDelta",
		llm.ToolInputEnded:   "ToolInputEnded",
	}
	if int(kind) < 0 || int(kind) >= len(names) || names[kind] == "" {
		return fmt.Sprintf("EventKind(%d)", int(kind))
	}
	return names[kind]
}

// summarize renders a stream as its sequence of kinds, which is what a failure
// about the shape of a turn needs to show.
func summarize(events []llm.Event) string {
	kinds := make([]string, len(events))
	for i, ev := range events {
		kinds[i] = name(ev.Kind)
	}
	return strings.Join(kinds, " -> ")
}
