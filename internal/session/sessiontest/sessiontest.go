// Package sessiontest is the contract test kit for the durable session store.
//
// StoreContract is the behavior every session.Store has to have, written once and
// run against each implementation: the in-memory store, the SQLite store on
// :memory: and on a file, and the decorators that wrap them. Two stores that
// disagree about what "not found" means, or about the order of a projection, are
// two different products behind one interface — this is what keeps that from
// happening quietly.
//
// It stays under internal/ on purpose. The shape of the durable event is
// published (agentcore/session), how it is persisted is not: there is no way to
// hand a host a Store from outside, so publishing a kit for one would advertise a
// seam that does not exist. See .okf/architecture/public-contracts.md.
package sessiontest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/session"
)

// summaryProjection is a SessionSummary without its timestamp, so a comparison
// can be made against a literal: LastActivity is asserted on its own terms
// (non-zero, UTC, moving forward) rather than pinned to a value.
type summaryProjection struct {
	ID    string
	Title string
	Cwd   string
}

func projectSummaries(summaries []session.SessionSummary) []summaryProjection {
	projected := make([]summaryProjection, 0, len(summaries))
	for _, summary := range summaries {
		projected = append(projected, summaryProjection{ID: summary.ID, Title: summary.Title, Cwd: summary.Cwd})
	}
	return projected
}

// StoreContract runs the durable contract of session.Store against any
// implementation. newStore returns an empty, ready store — a fresh MemoryStore, a
// SQLiteStore over ":memory:" or over a temporary file, a decorator around any of
// them — and is called once per subtest, so each one gets its own world.
func StoreContract(t *testing.T, newStore func(t *testing.T) session.Store) {
	t.Helper()

	// appendMessage appends an event that materializes a message and returns the
	// Seq the store assigned.
	appendMessage := func(t *testing.T, store session.Store, sessionID string, m session.Message) session.Seq {
		t.Helper()
		seq, err := store.AppendEvent(context.Background(), sessionID, session.SessionEvent{Message: &m})
		if err != nil {
			t.Fatalf("AppendEvent(%q): unexpected error: %v", sessionID, err)
		}
		return seq
	}

	t.Run("AppendEventAssignsMonotonicSeq", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		for want := session.Seq(1); want <= 3; want++ {
			got, err := store.AppendEvent(ctx, "s1", session.SessionEvent{})
			if err != nil {
				t.Fatalf("AppendEvent #%d: %v", want, err)
			}
			if got != want {
				t.Fatalf("AppendEvent #%d: got Seq %d, want %d", want, got, want)
			}
		}
	})

	t.Run("EventsRoundTripExtensionAttrsDefensively", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		attrs := map[string]string{
			"ext.example.trace_id": "trace-123",
			"ext.example.detail":   "preserve me",
		}
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{
			Kind:  session.EventKind("ext.example.Observed"),
			Attrs: attrs,
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}

		attrs["ext.example.trace_id"] = "mutated after append"
		got, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		want := map[string]string{
			"ext.example.trace_id": "trace-123",
			"ext.example.detail":   "preserve me",
		}
		if len(got) != 1 || !reflect.DeepEqual(got[0].Attrs, want) {
			t.Fatalf("Events Attrs = %#v, want %#v", got, want)
		}

		got[0].Attrs["ext.example.trace_id"] = "mutated after read"
		again, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatalf("Events after mutation: %v", err)
		}
		if !reflect.DeepEqual(again[0].Attrs, want) {
			t.Fatalf("Events Attrs after caller mutation = %#v, want %#v", again[0].Attrs, want)
		}
	})

	t.Run("MessagesReturnInSeqOrder", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		appendMessage(t, store, "s1", session.Message{ID: "m1", Role: session.RoleUser, Text: "hola"})
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{}); err != nil { // event with no message
			t.Fatalf("AppendEvent (no message): unexpected error: %v", err)
		}
		appendMessage(t, store, "s1", session.Message{ID: "m2", Role: session.RoleAssistant, Text: "que tal"})

		got, err := store.Messages(ctx, "s1", 0)
		if err != nil {
			t.Fatalf("Messages: unexpected error: %v", err)
		}

		want := []session.Message{
			{ID: "m1", Role: session.RoleUser, Text: "hola", Seq: 1},
			{ID: "m2", Role: session.RoleAssistant, Text: "que tal", Seq: 3},
		}
		if len(got) != len(want) {
			t.Fatalf("Messages: got %d messages, want %d (%+v)", len(got), len(want), got)
		}
		for i := range want {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Fatalf("Messages[%d]: got %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	t.Run("MessagesSinceSeqFiltersOlder", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		appendMessage(t, store, "s1", session.Message{ID: "m1", Role: session.RoleUser, Text: "uno"})
		seq2 := appendMessage(t, store, "s1", session.Message{ID: "m2", Role: session.RoleAssistant, Text: "dos"})
		appendMessage(t, store, "s1", session.Message{ID: "m3", Role: session.RoleUser, Text: "tres"})

		got, err := store.Messages(ctx, "s1", seq2)
		if err != nil {
			t.Fatalf("Messages: unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].ID != "m3" || got[0].Seq != 3 {
			t.Fatalf("Messages(sinceSeq=%d): got %+v, want only m3 with Seq 3", seq2, got)
		}
	})

	t.Run("MessagesSinceSeqBeyondLastReturnsEmpty", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		last := appendMessage(t, store, "s1", session.Message{ID: "m1", Role: session.RoleUser, Text: "uno"})

		got, err := store.Messages(ctx, "s1", last+5)
		if err != nil {
			t.Fatalf("Messages: unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("Messages(sinceSeq beyond last): got %+v, want empty", got)
		}
	})

	t.Run("UnknownSessionReturnsNotFound", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		if _, err := store.LoadSession(ctx, "ghost"); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("LoadSession(ghost): got %v, want ErrSessionNotFound", err)
		}
		if _, err := store.Messages(ctx, "ghost", 0); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("Messages(ghost): got %v, want ErrSessionNotFound", err)
		}
		if _, err := store.Epoch(ctx, "ghost"); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("Epoch(ghost): got %v, want ErrSessionNotFound", err)
		}
		if _, err := store.PendingToolCalls(ctx, "ghost"); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("PendingToolCalls(ghost): got %v, want ErrSessionNotFound", err)
		}
	})

	t.Run("LoadSessionReturnsSessionAfterAppend", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{}); err != nil {
			t.Fatalf("AppendEvent: unexpected error: %v", err)
		}
		got, err := store.LoadSession(ctx, "s1")
		if err != nil {
			t.Fatalf("LoadSession(s1): unexpected error: %v", err)
		}
		if got != (session.Session{ID: "s1"}) {
			t.Fatalf("LoadSession(s1): got %+v, want {ID:s1}", got)
		}
	})

	t.Run("EpochStableAndNotFound", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// Unknown session: the same not-found contract as LoadSession/Messages.
		if _, err := store.Epoch(ctx, "ghost"); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("Epoch(ghost): got %v, want ErrSessionNotFound", err)
		}

		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{}); err != nil {
			t.Fatalf("AppendEvent: unexpected error: %v", err)
		}

		// Two consecutive reads return the same epoch: stable, no spurious rebuild.
		first, err := store.Epoch(ctx, "s1")
		if err != nil {
			t.Fatalf("Epoch (first): unexpected error: %v", err)
		}
		second, err := store.Epoch(ctx, "s1")
		if err != nil {
			t.Fatalf("Epoch (second): unexpected error: %v", err)
		}
		if first != second {
			t.Fatalf("Epoch is not stable: first %+v, second %+v (spurious rebuild)", first, second)
		}
	})

	t.Run("PendingToolCallsFoldsCalledVsResolved", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Kind: session.KindToolCalled, CallID: "c1", ToolName: "echo"}); err != nil {
			t.Fatalf("AppendEvent (c1 called): unexpected error: %v", err)
		}
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Kind: session.KindToolCalled, CallID: "c2", ToolName: "read"}); err != nil {
			t.Fatalf("AppendEvent (c2 called): unexpected error: %v", err)
		}
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Kind: session.KindToolSuccess, CallID: "c2", ToolName: "read"}); err != nil {
			t.Fatalf("AppendEvent (c2 success): unexpected error: %v", err)
		}

		pending, err := store.PendingToolCalls(ctx, "s1")
		if err != nil {
			t.Fatalf("PendingToolCalls(s1): unexpected error: %v", err)
		}
		if len(pending) != 1 {
			t.Fatalf("PendingToolCalls(s1) = %+v, want only c1", pending)
		}
		if pending[0] != (session.PendingTool{CallID: "c1", ToolName: "echo"}) {
			t.Fatalf("PendingToolCalls(s1)[0] = %+v, want c1 echo", pending[0])
		}

		// A session with only a Message (no tool calls): empty projection.
		if _, err := store.AppendEvent(ctx, "empty", session.SessionEvent{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}}); err != nil {
			t.Fatalf("AppendEvent (empty session): unexpected error: %v", err)
		}
		empty, err := store.PendingToolCalls(ctx, "empty")
		if err != nil {
			t.Fatalf("PendingToolCalls(empty): unexpected error: %v", err)
		}
		if len(empty) != 0 {
			t.Fatalf("PendingToolCalls(empty) = %+v, want empty", empty)
		}
	})

	t.Run("SessionsEmptyWhenNoSessions", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("Sessions on empty store: got %+v, want empty", got)
		}
	})

	t.Run("SessionsOrdersByRecencyWithFirstUserMessageTitle", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// s1 gets its first message first; its title is that first prompt even
		// after more user messages arrive.
		appendMessage(t, store, "s1", session.Message{ID: "m1", Role: session.RoleUser, Text: "primera pregunta"})
		appendMessage(t, store, "s1", session.Message{ID: "m2", Role: session.RoleAssistant, Text: "respuesta"})
		// s2 arrives later: it must sort before s1 (more recent).
		appendMessage(t, store, "s2", session.Message{ID: "m3", Role: session.RoleUser, Text: "otra cosa"})

		before, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions before new s1 activity: unexpected error: %v", err)
		}
		beforeWant := []summaryProjection{
			{ID: "s2", Title: "otra cosa"},
			{ID: "s1", Title: "primera pregunta"},
		}
		if projected := projectSummaries(before); !reflect.DeepEqual(projected, beforeWant) {
			t.Fatalf("Sessions before new s1 activity: got %+v, want %+v", projected, beforeWant)
		}
		for _, summary := range before {
			if summary.LastActivity.IsZero() {
				t.Fatalf("Sessions before new s1 activity: summary %+v has zero LastActivity", summary)
			}
			if summary.LastActivity.Location() != time.UTC {
				t.Fatalf("Sessions before new s1 activity: summary %+v LastActivity is not UTC", summary)
			}
		}
		s1LastActivity := before[1].LastActivity

		// s1 sees activity again: it becomes the most recent.
		lowerBound := time.UnixMilli(time.Now().UTC().UnixMilli()).UTC()
		appendMessage(t, store, "s1", session.Message{ID: "m4", Role: session.RoleUser, Text: "segunda pregunta"})

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		want := []summaryProjection{
			{ID: "s1", Title: "primera pregunta"},
			{ID: "s2", Title: "otra cosa"},
		}
		if projected := projectSummaries(got); !reflect.DeepEqual(projected, want) {
			t.Fatalf("Sessions: got %+v, want %+v", projected, want)
		}
		for _, summary := range got {
			if summary.LastActivity.IsZero() {
				t.Fatalf("Sessions: summary %+v has zero LastActivity", summary)
			}
			if summary.LastActivity.Location() != time.UTC {
				t.Fatalf("Sessions: summary %+v LastActivity is not UTC", summary)
			}
		}
		if got[0].LastActivity.Before(s1LastActivity) {
			t.Fatalf("Sessions: updated s1 LastActivity %v is before previous value %v", got[0].LastActivity, s1LastActivity)
		}
		if got[0].LastActivity.Before(lowerBound) {
			t.Fatalf("Sessions: updated s1 LastActivity %v is before append lower bound %v", got[0].LastActivity, lowerBound)
		}
	})

	t.Run("SessionsTitleEmptyWhenNoUserMessage", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// A session with events but no user message: empty Title.
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Kind: session.KindStepStarted}); err != nil {
			t.Fatalf("AppendEvent: unexpected error: %v", err)
		}

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		want := []summaryProjection{{ID: "s1", Title: ""}}
		if projected := projectSummaries(got); !reflect.DeepEqual(projected, want) {
			t.Fatalf("Sessions: got %+v, want %+v", projected, want)
		}
	})

	t.Run("SessionsPrefersTitleEventOverFirstUserMessage", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// The first user message (the fallback) and then a Session.Title event: the
		// generated title wins over the first prompt, and the last Session.Title is
		// the one in force.
		appendMessage(t, store, "s1", session.Message{ID: "m1", Role: session.RoleUser, Text: "como configuro el proxy"})
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Kind: session.KindSessionTitle, Text: "Configuracion del proxy"}); err != nil {
			t.Fatalf("AppendEvent (Session.Title): unexpected error: %v", err)
		}

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		want := []summaryProjection{{ID: "s1", Title: "Configuracion del proxy"}}
		if projected := projectSummaries(got); !reflect.DeepEqual(projected, want) {
			t.Fatalf("Sessions: got %+v, want %+v", projected, want)
		}

		// The generated title is truncated to 80 runes too, like the fallback.
		long := strings.Repeat("ñ", 200)
		if _, err := store.AppendEvent(ctx, "s2", session.SessionEvent{Kind: session.KindSessionTitle, Text: long}); err != nil {
			t.Fatalf("AppendEvent (long Session.Title): unexpected error: %v", err)
		}
		got, err = store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		var s2 *session.SessionSummary
		for i := range got {
			if got[i].ID == "s2" {
				s2 = &got[i]
			}
		}
		if s2 == nil {
			t.Fatalf("Sessions: s2 is missing from %+v", got)
		}
		if wantTitle := strings.Repeat("ñ", 80); s2.Title != wantTitle {
			t.Fatalf("Session.Title not truncated to 80 runes: got %d runes", len([]rune(s2.Title)))
		}
	})

	t.Run("SessionsCarryCwdFromCwdEvent", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// Session.Cwd carries the session's folder; the projection exposes it in
		// SessionSummary.Cwd so a session list can group by folder. The last
		// Session.Cwd wins over an earlier one.
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Kind: session.KindSessionCwd, Text: "/home/u/viejo"}); err != nil {
			t.Fatalf("AppendEvent (earlier Session.Cwd): unexpected error: %v", err)
		}
		appendMessage(t, store, "s1", session.Message{ID: "m1", Role: session.RoleUser, Text: "hola"})
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Kind: session.KindSessionCwd, Text: "/home/u/proj"}); err != nil {
			t.Fatalf("AppendEvent (Session.Cwd): unexpected error: %v", err)
		}

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		want := []summaryProjection{{ID: "s1", Title: "hola", Cwd: "/home/u/proj"}}
		if projected := projectSummaries(got); !reflect.DeepEqual(projected, want) {
			t.Fatalf("Sessions: got %+v, want %+v", projected, want)
		}
	})

	t.Run("SessionsCwdEmptyWithoutCwdEvent", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// No Session.Cwd (an old session): Cwd stays "" and the list groups it apart.
		appendMessage(t, store, "s1", session.Message{ID: "m1", Role: session.RoleUser, Text: "hola"})

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		want := []summaryProjection{{ID: "s1", Title: "hola", Cwd: ""}}
		if projected := projectSummaries(got); !reflect.DeepEqual(projected, want) {
			t.Fatalf("Sessions: got %+v, want %+v", projected, want)
		}
	})

	t.Run("SessionsTruncatesLongTitleTo80Runes", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// A title with multibyte runes, to verify the cut is by rune, not by byte.
		long := strings.Repeat("ñ", 200)
		appendMessage(t, store, "s1", session.Message{ID: "m1", Role: session.RoleUser, Text: long})

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("Sessions: got %+v, want one session", got)
		}
		if want := strings.Repeat("ñ", 80); got[0].Title != want {
			t.Fatalf("Sessions title not truncated to 80 runes: got %d runes", len([]rune(got[0].Title)))
		}
	})

	t.Run("EventsRoundTripsLogInSeqOrder", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// A representative sequence: user prompt, assistant with a tool call
		// (coalesced into Step.Ended with Usage), tool result, and streaming events
		// with no message. Events must return it identical (except the SessionID
		// and Seq the store assigns).
		in := []session.SessionEvent{
			{Kind: session.KindStepStarted},
			{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}},
			{Kind: session.KindToolCalled, CallID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"foo.go"}`)},
			{Kind: session.KindToolSuccess, CallID: "c1", ToolName: "read", Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "contenido", ToolCallID: "c1"}},
			{Kind: session.KindToolFailed, CallID: "c2", ToolName: "bash", Error: "boom"},
			{Kind: session.KindStepEnded, Message: &session.Message{
				ID: "a1", Role: session.RoleAssistant, Text: "listo",
				ToolCalls: []session.ToolCall{{ID: "c1", Name: "read", Arguments: `{"path":"foo.go"}`}},
			}, Usage: &session.Usage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 2, CacheReadTokens: 1, CacheWriteTokens: 3}},
		}
		for i, ev := range in {
			if _, err := store.AppendEvent(ctx, "s1", ev); err != nil {
				t.Fatalf("AppendEvent #%d: unexpected error: %v", i, err)
			}
		}

		got, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatalf("Events: unexpected error: %v", err)
		}
		if len(got) != len(in) {
			t.Fatalf("Events: got %d events, want %d (%+v)", len(got), len(in), got)
		}
		for i := range in {
			want := in[i]
			want.SessionID = "s1"
			want.Seq = session.Seq(i + 1)
			if !reflect.DeepEqual(got[i], want) {
				t.Fatalf("Events[%d]: got %+v, want %+v", i, got[i], want)
			}
		}
	})

	t.Run("EventsRoundTripsTopLevelText", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// The assistant's content and its reasoning travel in the SessionEvent's
		// top-level Text (not in a Message): Reasoning.Ended and Text.Ended carry it
		// complete. Events must return it intact, or rehydration loses "what it
		// thought" and "what it answered". A tool result that also carries top-level
		// Text (as the real publisher does) must round-trip ev.Text AND Message.Text
		// at once.
		in := []session.SessionEvent{
			{Kind: session.KindReasoningStarted},
			{Kind: session.KindReasoningDelta, Text: "pien"},
			{Kind: session.KindReasoningEnded, Text: "pienso, luego existo"},
			{Kind: session.KindTextStarted},
			{Kind: session.KindTextDelta, Text: "ho"},
			{Kind: session.KindTextEnded, Text: "hola mundo"},
			{Kind: session.KindToolSuccess, CallID: "c1", ToolName: "read", Text: "contenido",
				Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "contenido", ToolCallID: "c1"}},
		}
		for i, ev := range in {
			if _, err := store.AppendEvent(ctx, "s1", ev); err != nil {
				t.Fatalf("AppendEvent #%d: unexpected error: %v", i, err)
			}
		}

		got, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatalf("Events: unexpected error: %v", err)
		}
		if len(got) != len(in) {
			t.Fatalf("Events: got %d events, want %d (%+v)", len(got), len(in), got)
		}
		for i := range in {
			want := in[i]
			want.SessionID = "s1"
			want.Seq = session.Seq(i + 1)
			if !reflect.DeepEqual(got[i], want) {
				t.Fatalf("Events[%d]: got %+v, want %+v", i, got[i], want)
			}
		}
	})

	t.Run("EventsRoundTripsDiff", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// The UI-only diff of edit/write travels in SessionEvent.Diff. Events must
		// return it intact or rehydration loses the colored diff when an old session
		// is reopened.
		in := session.SessionEvent{
			Kind: session.KindToolSuccess, CallID: "c1", ToolName: "edit", Text: "[foo.go#ab12]",
			Diff:    "--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-a\n+b\n",
			Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "[foo.go#ab12]", ToolCallID: "c1"},
		}
		if _, err := store.AppendEvent(ctx, "s1", in); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}

		got, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("Events: got %d, want 1", len(got))
		}
		if got[0].Diff != in.Diff {
			t.Fatalf("Events[0].Diff round-trip:\n got %q\nwant %q", got[0].Diff, in.Diff)
		}
	})

	t.Run("EventsSinceSeqFiltersOlder", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Kind: session.KindStepStarted}); err != nil {
			t.Fatalf("AppendEvent: unexpected error: %v", err)
		}
		seq2, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}})
		if err != nil {
			t.Fatalf("AppendEvent: unexpected error: %v", err)
		}
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Kind: session.KindStepEnded}); err != nil {
			t.Fatalf("AppendEvent: unexpected error: %v", err)
		}

		got, err := store.Events(ctx, "s1", seq2)
		if err != nil {
			t.Fatalf("Events: unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].Kind != session.KindStepEnded || got[0].Seq != 3 {
			t.Fatalf("Events(sinceSeq=%d): got %+v, want only the Step.Ended with Seq 3", seq2, got)
		}
	})

	t.Run("EventsUnknownSessionReturnsNotFound", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		if _, err := store.Events(ctx, "ghost", 0); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("Events(ghost): got %v, want ErrSessionNotFound", err)
		}
	})

	t.Run("DeleteSessionRemovesSessionLeavingOthers", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		appendMessage(t, store, "s1", session.Message{ID: "m1", Role: session.RoleUser, Text: "hola"})
		appendMessage(t, store, "s2", session.Message{ID: "m2", Role: session.RoleUser, Text: "otra"})

		if err := store.DeleteSession(ctx, "s1"); err != nil {
			t.Fatalf("DeleteSession(s1): unexpected error: %v", err)
		}

		if _, err := store.LoadSession(ctx, "s1"); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("LoadSession(s1) after delete: got %v, want ErrSessionNotFound", err)
		}
		if _, err := store.Events(ctx, "s1", 0); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("Events(s1) after delete: got %v, want ErrSessionNotFound", err)
		}

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].ID != "s2" {
			t.Fatalf("Sessions after deleting s1: got %+v, want only s2", got)
		}
	})

	t.Run("DeleteUnknownSessionReturnsNotFound", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// Deleting a session that does not exist (no events) touches nothing: the
		// same not-found contract as LoadSession/Messages/Events.
		if err := store.DeleteSession(ctx, "ghost"); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("DeleteSession(ghost): got %v, want ErrSessionNotFound", err)
		}
	})

	t.Run("ConcurrentAppendsAssignUniqueSeqs", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		const n = 100
		seqs := make([]session.Seq, n)
		var wg sync.WaitGroup
		wg.Add(n)
		for i := 0; i < n; i++ {
			go func(i int) {
				defer wg.Done()
				seq, err := store.AppendEvent(ctx, "s1", session.SessionEvent{})
				if err != nil {
					t.Errorf("AppendEvent: unexpected error: %v", err)
					return
				}
				seqs[i] = seq
			}(i)
		}
		wg.Wait()

		sort.Slice(seqs, func(a, b int) bool { return seqs[a] < seqs[b] })
		for i := 0; i < n; i++ {
			if seqs[i] != session.Seq(i+1) {
				t.Fatalf("sorted seqs[%d] = %d, want %d (gaps or duplicates: %v)", i, seqs[i], i+1, seqs)
			}
		}
	})
}
