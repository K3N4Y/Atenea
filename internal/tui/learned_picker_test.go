package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/learning"
)

type fakeLearningAgent struct {
	*fakeAgent
	runs       []learning.Run
	lessons    []learning.Lesson
	learnErr   error
	auditErr   error
	learned    []string
	approved   []string
	rejected   []string
	cancelled  []string
	retried    []string
	toggled    []string
	deleted    []string
	auditCalls int
}

func (f *fakeLearningAgent) Learn(sessionID string) (learning.Run, error) {
	f.learned = append(f.learned, sessionID)
	if f.learnErr != nil {
		return learning.Run{}, f.learnErr
	}
	run := learning.Run{ID: "new-run", SessionID: sessionID, Status: learning.Queued}
	f.runs = append([]learning.Run{run}, f.runs...)
	return run, nil
}

func (f *fakeLearningAgent) LearningAudit() ([]learning.Run, []learning.Lesson, error) {
	f.auditCalls++
	if f.auditErr != nil {
		return nil, nil, f.auditErr
	}
	return append([]learning.Run(nil), f.runs...), append([]learning.Lesson(nil), f.lessons...), nil
}

func (f *fakeLearningAgent) ApproveLearning(id string, candidate learning.Candidate) (learning.Lesson, error) {
	f.approved = append(f.approved, id)
	lesson := learning.Lesson{ID: "lesson-" + id, RunID: id, Candidate: candidate, Enabled: true}
	f.lessons = append(f.lessons, lesson)
	return lesson, nil
}

func (f *fakeLearningAgent) RejectLearning(id string) error {
	f.rejected = append(f.rejected, id)
	return nil
}

func (f *fakeLearningAgent) CancelLearning(id string) error {
	f.cancelled = append(f.cancelled, id)
	return nil
}

func (f *fakeLearningAgent) RetryLearning(id string) (learning.Run, error) {
	f.retried = append(f.retried, id)
	return learning.Run{ID: "retry-" + id, Status: learning.Queued}, nil
}

func (f *fakeLearningAgent) SetLessonEnabled(id string, enabled bool) error {
	f.toggled = append(f.toggled, id)
	for i := range f.lessons {
		if f.lessons[i].ID == id {
			f.lessons[i].Enabled = enabled
		}
	}
	return nil
}

func (f *fakeLearningAgent) DeleteLesson(id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func learningCandidate(statement string) learning.Candidate {
	return learning.Candidate{
		Statement: statement,
		Scope:     "this workspace",
		Evidence:  []learning.Evidence{{Seq: 1, Summary: "observed in the session"}},
	}
}

func openLearnedPicker(t *testing.T, agent Agent) Model {
	t.Helper()
	m := NewModel(agent, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = typeRunes(t, m, "/learned")
	m, cmd := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/learned must load the audit asynchronously")
	}
	return apply(t, m, cmd())
}

func TestModel_LearnQueuesCurrentSessionAsynchronouslyWithoutOpeningModelPicker(t *testing.T) {
	agent := &fakeLearningAgent{fakeAgent: &fakeAgent{}}
	m := NewModel(agent, "s1", nil)
	m = typeRunes(t, m, "/learn")
	m, cmd := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/learn must enqueue through a tea.Cmd")
	}
	if m.modelPicker.open {
		t.Fatal("/learn must use the /agents configuration instead of opening a model picker")
	}
	if m.composer.value() != "" {
		t.Fatalf("composer = %q, want cleared", m.composer.value())
	}
	m = apply(t, m, cmd())
	if len(agent.learned) != 1 || agent.learned[0] != "s1" {
		t.Fatalf("Learn calls = %v, want [s1]", agent.learned)
	}
	if len(m.entries) == 0 || !strings.Contains(m.entries[len(m.entries)-1].text, "learning queued") {
		t.Fatalf("entries = %+v, want queued notice", m.entries)
	}
}

func TestModel_LearnSurfacesErrorsAndRejectsArguments(t *testing.T) {
	agent := &fakeLearningAgent{fakeAgent: &fakeAgent{}, learnErr: errors.New("session has no durable evidence")}
	m := NewModel(agent, "s1", nil)
	m = typeRunes(t, m, "/learn")
	m, cmd := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, cmd())
	if len(m.entries) == 0 || !strings.Contains(m.entries[len(m.entries)-1].text, "no durable evidence") {
		t.Fatalf("entries = %+v, want learning error", m.entries)
	}

	m = NewModel(agent, "s1", nil)
	m = typeRunes(t, m, "/learn extra")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.entries[len(m.entries)-1].text; got != "usage: /learn" {
		t.Fatalf("error = %q, want usage", got)
	}
}

func TestModel_LearnedShowsRunsAndLessons(t *testing.T) {
	candidate := learningCandidate("Run gofmt after editing Go files")
	agent := &fakeLearningAgent{
		fakeAgent: &fakeAgent{},
		runs: []learning.Run{
			{ID: "ready", Status: learning.Ready, Candidate: &candidate, CreatedAt: time.Now()},
			{ID: "failed", Status: learning.Failed, Error: "provider unavailable", CreatedAt: time.Now().Add(-time.Minute)},
		},
		lessons: []learning.Lesson{{ID: "lesson-1", Candidate: learningCandidate("Prefer focused tests"), Enabled: true}},
	}
	m := openLearnedPicker(t, agent)
	if !m.learnedPicker.open || m.activeInputTarget() != targetLearnedPicker {
		t.Fatalf("picker open = %v target = %v", m.learnedPicker.open, m.activeInputTarget())
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"Learned Guidance", "Run gofmt", "provider unavailable", "Prefer focused tests", "ready", "enabled"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view is missing %q:\n%s", want, view)
		}
	}
}

func TestModel_LearnedAppliesContextualPrimaryAndDestructiveActions(t *testing.T) {
	candidate := learningCandidate("Run gofmt")
	agent := &fakeLearningAgent{
		fakeAgent: &fakeAgent{},
		runs:      []learning.Run{{ID: "ready", Status: learning.Ready, Candidate: &candidate}},
		lessons:   []learning.Lesson{{ID: "lesson-1", Candidate: learningCandidate("Use focused tests"), Enabled: true}},
	}
	m := openLearnedPicker(t, agent)

	m, cmd := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a ready run must approve asynchronously")
	}
	m = apply(t, m, cmd())
	if len(agent.approved) != 1 || agent.approved[0] != "ready" {
		t.Fatalf("approved = %v, want [ready]", agent.approved)
	}

	m.learnedPicker.set(agent.runs, agent.lessons)
	m.learnedPicker.selected = len(agent.runs)
	m, cmd = applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, cmd())
	if len(agent.toggled) != 1 || agent.toggled[0] != "lesson-1" {
		t.Fatalf("toggled = %v, want [lesson-1]", agent.toggled)
	}

	m, cmd = applyCmd(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = apply(t, m, cmd())
	if len(agent.deleted) != 1 || agent.deleted[0] != "lesson-1" {
		t.Fatalf("deleted = %v, want [lesson-1]", agent.deleted)
	}
}

func TestModel_LearnedRunActionsFollowStatus(t *testing.T) {
	tests := []struct {
		name   string
		status learning.Status
		key    tea.KeyMsg
		called func(*fakeLearningAgent) []string
	}{
		{
			name:   "ready run rejects with destructive action",
			status: learning.Ready,
			key:    tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}},
			called: func(agent *fakeLearningAgent) []string { return agent.rejected },
		},
		{
			name:   "failed run retries with primary action",
			status: learning.Failed,
			key:    tea.KeyMsg{Type: tea.KeyEnter},
			called: func(agent *fakeLearningAgent) []string { return agent.retried },
		},
		{
			name:   "queued run cancels with primary action",
			status: learning.Queued,
			key:    tea.KeyMsg{Type: tea.KeyEnter},
			called: func(agent *fakeLearningAgent) []string { return agent.cancelled },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := learningCandidate("Keep the action matrix explicit")
			agent := &fakeLearningAgent{
				fakeAgent: &fakeAgent{},
				runs:      []learning.Run{{ID: "run", Status: tc.status, Candidate: &candidate}},
			}
			m := openLearnedPicker(t, agent)
			m, cmd := applyCmd(t, m, tc.key)
			if cmd == nil {
				t.Fatal("learning run action must execute asynchronously")
			}
			_ = apply(t, m, cmd())
			if calls := tc.called(agent); len(calls) != 1 || calls[0] != "run" {
				t.Fatalf("calls = %v, want [run]", calls)
			}
		})
	}
}

func TestModel_LearningChangeRefreshesOpenAudit(t *testing.T) {
	agent := &fakeLearningAgent{fakeAgent: &fakeAgent{}}
	m := openLearnedPicker(t, agent)
	calls := agent.auditCalls
	m, cmd := applyCmd(t, m, LearningChangedMsg{Workspace: "workspace"})
	if cmd == nil {
		t.Fatal("learning change must schedule an audit refresh")
	}
	m = apply(t, m, cmd())
	if agent.auditCalls != calls+1 {
		t.Fatalf("audit calls = %d, want %d", agent.auditCalls, calls+1)
	}
}

func TestModel_LearnedDropsSupersededAuditResult(t *testing.T) {
	agent := &fakeLearningAgent{fakeAgent: &fakeAgent{}}
	m := openLearnedPicker(t, agent)

	m, first := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m, second := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if first == nil || second == nil {
		t.Fatal("each reload must issue an audit command")
	}
	newer := second().(learnedAuditDoneMsg)
	newer.runs = []learning.Run{{ID: "newer", Status: learning.Ready}}
	m = apply(t, m, newer)

	older := first().(learnedAuditDoneMsg)
	older.runs = []learning.Run{{ID: "older", Status: learning.Failed}}
	m = apply(t, m, older)
	if len(m.learnedPicker.runs) != 1 || m.learnedPicker.runs[0].ID != "newer" {
		t.Fatalf("runs = %+v, superseded audit overwrote the latest result", m.learnedPicker.runs)
	}
}

func TestModel_LearnedEscClosesAndUnavailableAgentErrors(t *testing.T) {
	m := openLearnedPicker(t, &fakeLearningAgent{fakeAgent: &fakeAgent{}})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.learnedPicker.open {
		t.Fatal("esc must close /learned")
	}

	m = NewModel(&fakeAgent{}, "s1", nil)
	m = typeRunes(t, m, "/learned")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.entries) == 0 || !strings.Contains(m.entries[len(m.entries)-1].text, "unavailable") {
		t.Fatalf("entries = %+v, want unavailable error", m.entries)
	}
}
