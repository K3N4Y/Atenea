package tui

import (
	"testing"

	"github.com/K3N4Y/atenea/internal/session"
)

func TestQueuedOldSessionEventsAreIgnoredAfterSwitch(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "old", nil)
	m.working = true
	m = m.freshSession("new")
	beforeWorkspaceGen := m.workspaceGen

	old := EventMsg{SessionID: "old", Seq: 7, Kind: session.KindToolSuccess, ToolName: "write", Text: "stale"}
	updated, _ := m.update(old)
	m = updated.(Model)
	if len(m.Transcript.entries) != 0 || m.working || m.workspaceGen != beforeWorkspaceGen {
		t.Fatalf("old event mutated switched model: entries=%+v working=%v workspaceGen=%d", m.Transcript.entries, m.working, m.workspaceGen)
	}
	m = m.foldEvent(EventMsg{SessionID: "new", Seq: 1, Kind: session.KindTextDelta, Text: "current"})
	if len(m.Transcript.entries) == 0 {
		t.Fatal("current session event did not route")
	}
	m = m.foldEvent(EventMsg{SessionID: "new", Seq: 2, Kind: session.KindToolCalled, CallID: "task", ToolName: "task"})
	started := session.WithParentTaskCall(session.SessionEvent{SessionID: "child", Seq: 1, Kind: session.KindStepStarted}, "task")
	updated, _ = m.update(EventMsg(started))
	m = updated.(Model)
	child := session.WithParentTaskCall(session.SessionEvent{SessionID: "child", Seq: 2, Kind: session.KindToolCalled, CallID: "child-call", ToolName: "bash"}, "task")
	updated, _ = m.update(EventMsg(child))
	m = updated.(Model)
	if len(m.Transcript.childBatches) == 0 {
		t.Fatal("decorated child event did not route")
	}
}

func TestInitialEmptySessionAcceptsFirstDurableEvent(t *testing.T) {
	m := NewModel(&fakeAgent{}, "", nil)
	updated, _ := m.update(EventMsg{SessionID: "created", Seq: 1, Kind: session.KindTextDelta, Text: "hello"})
	if got := updated.(Model); len(got.Transcript.entries) == 0 {
		t.Fatal("initial empty session rejected event")
	}
}
