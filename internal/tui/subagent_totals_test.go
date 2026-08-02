package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

func TestSubagentTotalReplacesLiveAndRehydrates(t *testing.T) {
	called := session.SessionEvent{Kind: session.KindToolCalled, CallID: "task", ToolName: "task", SessionID: "parent"}
	child := session.WithParentTaskCall(session.SessionEvent{Kind: session.KindToolCalled, CallID: "read", ToolName: "read", SessionID: "child"}, "task")
	started := session.WithParentTaskCall(session.SessionEvent{Kind: session.KindStepStarted, SessionID: "child"}, "task")
	settled := session.WithTaskSettlement(session.SessionEvent{Kind: session.KindToolSuccess, CallID: "task", ToolName: "task", SessionID: "parent"}, tool.TaskSettlement{ToolCalls: 1, Requests: 2, Tokens: 30, Duration: 1500 * time.Millisecond})
	tr := Transcript{}
	for _, ev := range []session.SessionEvent{called, started, child} {
		tr = tr.foldEvent(EventMsg(ev), "parent")
	}
	m := Model{Transcript: tr}
	if !strings.Contains(m.renderTranscript(), "read") {
		t.Fatal("live child not rendered")
	}
	tr = tr.foldEvent(EventMsg(settled), "parent")
	m.Transcript = tr
	if got := m.renderTranscript(); !strings.Contains(got, "↳ 1 tool call · 2 req · 30 tok · 1.5s") || strings.Contains(got, "read") {
		t.Fatalf("render = %q", got)
	}
	if lines := m.entryLines(); len(lines) < 2 || !strings.Contains(lines[len(lines)-1].line, "1 tool call") {
		t.Fatalf("entryLines = %#v", lines)
	}
	rehydrated := Transcript{}.replaceEvents([]session.SessionEvent{called, started, child, settled}, "parent")
	if got := (Model{Transcript: rehydrated}).renderTranscript(); !strings.Contains(got, "↳ 1 tool call") {
		t.Fatalf("rehydrated = %q", got)
	}
}

func TestSubagentTotalGrammarIsolationAndInvalidLegacy(t *testing.T) {
	tr := Transcript{}
	for _, id := range []string{"zero", "many", "invalid"} {
		tr = tr.foldEvent(EventMsg(session.SessionEvent{Kind: session.KindToolCalled, CallID: id, ToolName: "task"}), "s")
	}
	tr = tr.foldEvent(EventMsg(session.WithSubagentToolCalls(session.SessionEvent{Kind: session.KindToolFailed, CallID: "zero", ToolName: "task"}, 0)), "s")
	tr = tr.foldEvent(EventMsg(session.WithSubagentToolCalls(session.SessionEvent{Kind: session.KindToolSuccess, CallID: "many", ToolName: "task"}, 3)), "s")
	tr = tr.foldEvent(EventMsg(session.SessionEvent{Kind: session.KindToolSuccess, CallID: "invalid", ToolName: "task", Attrs: map[string]string{"atenea.internal.subagent_tool_calls": "03"}}), "s")
	out := (Model{Transcript: tr}).renderTranscript()
	if !strings.Contains(out, "↳ 0 tool calls") || !strings.Contains(out, "↳ 3 tool calls") {
		t.Fatalf("render = %q", out)
	}
	if _, ok := tr.childTotals["invalid"]; ok {
		t.Fatal("invalid metadata accepted")
	}
}

func TestSubagentTotalPersistsThroughSQLiteReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := session.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	called := session.SessionEvent{Kind: session.KindToolCalled, CallID: "task", ToolName: "task"}
	settled := session.WithTaskSettlement(session.SessionEvent{Kind: session.KindToolSuccess, CallID: "task", ToolName: "task"}, tool.TaskSettlement{ToolCalls: 4, Requests: 3, Tokens: 200, Duration: 2 * time.Second})
	for _, event := range []session.SessionEvent{called, settled} {
		if _, err := store.AppendEvent(context.Background(), "parent", event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := session.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	events, err := reopened.Events(context.Background(), "parent", 0)
	if err != nil {
		t.Fatal(err)
	}
	transcript := Transcript{}.replaceEvents(events, "parent")
	rendered := (Model{Transcript: transcript}).renderTranscript()
	if !strings.Contains(rendered, "↳ 4 tool calls · 3 req · 200 tok · 2s") {
		t.Fatalf("reopened render = %q", rendered)
	}
	if len(transcript.childBatches) != 0 {
		t.Fatalf("reopened transcript restored live child details: %#v", transcript.childBatches)
	}
}

func TestDetachedTaskRendersSupervisionHint(t *testing.T) {
	called := session.SessionEvent{Kind: session.KindToolCalled, CallID: "task", ToolName: "task"}
	settled := session.WithTaskDetached(session.SessionEvent{Kind: session.KindToolSuccess, CallID: "task", ToolName: "task"})
	transcript := Transcript{}.replaceEvents([]session.SessionEvent{called, settled}, "parent")
	got := (Model{Transcript: transcript}).renderTranscript()
	if !strings.Contains(got, "background job; use task_status, task_wait, or task_cancel") || strings.Contains(got, "0 tool calls") {
		t.Fatalf("render = %q", got)
	}
}
