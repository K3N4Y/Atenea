package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/session/subagent"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tui/engine"
)

// renderEntry renders the entry the way the transcript does: through a Model, so
// the block is presented by the tool that would have settled the call instead of
// by a hand-written presentation that could disagree with it.
func renderEntry(e entry, width int) string {
	return e.render(width, NewModel(&fakeAgent{}, "s1", nil).presentationOf(e))
}

// fakeAgent implements Agent and logs calls to assert on them.
type fakeAgent struct {
	sent           []struct{ sessionID, text string }
	planSent       []struct{ sessionID, text string }
	newSessionID   string
	resolved       []resolvedPermission
	stopped        []string
	accepted       []string
	models         []providerconfig.ProviderModels
	active         providerconfig.Active
	selected       []struct{ providerID, model string }
	refreshes      int
	undos          []string
	resumeLists    []string
	resumeSessions []session.SessionSummary
	resumeListErr  error
	resumeLoads    []struct{ currentID, targetID string }
	resume         ResumeResult
	resumeErr      error
	undoResult     UndoResult
	undoErr        error
	sendErr        error
	planErr        error
	acceptErr      error
	nextRunID      uint64
	tools          tool.Catalog
	// capabilities is what the adapter serving the current model declares;
	// declared == false is the agent that says nothing, which the UI must treat
	// as "unknown" rather than as "no window".
	capabilities llm.Capabilities
	declared     bool
}

func (f *fakeAgent) ModelCatalog() []providerconfig.ProviderModels {
	return providerconfig.CloneProviderModels(f.models)
}
func (f *fakeAgent) CurrentModel() providerconfig.Active { return f.active }
func (f *fakeAgent) ModelCapabilities() (llm.Capabilities, bool) {
	return f.capabilities, f.declared
}

// declaringAgent is the agent whose adapter declares one model's context window,
// which is what any context label in the UI now depends on.
func declaringAgent(model string, window int) *fakeAgent {
	return &fakeAgent{declared: true, capabilities: llm.Capabilities{ContextWindows: map[string]int{model: window}}}
}
func (f *fakeAgent) SelectModel(providerID, model string) (providerconfig.Active, error) {
	f.selected = append(f.selected, struct{ providerID, model string }{providerID, model})
	for _, provider := range f.models {
		if provider.ID == providerID {
			f.active = providerconfig.Active{ProviderID: providerID, ProviderName: provider.Name, Model: model}
			return f.active, nil
		}
	}
	return providerconfig.Active{}, fmt.Errorf("unknown provider")
}
func (f *fakeAgent) RefreshModels() { f.refreshes++ }

func (f *fakeAgent) nextRun(sessionID string) RunHandle {
	f.nextRunID++
	return RunHandle{SessionID: sessionID, RunID: f.nextRunID}
}

func (f *fakeAgent) SendPrompt(sessionID, text string) (RunHandle, error) {
	f.sent = append(f.sent, struct{ sessionID, text string }{sessionID, text})
	if f.sendErr != nil {
		return RunHandle{}, f.sendErr
	}
	if text == "/new" && f.newSessionID != "" {
		return RunHandle{SessionID: f.newSessionID}, nil
	}
	if text == "/compact" {
		return RunHandle{SessionID: sessionID}, nil
	}
	return f.nextRun(sessionID), nil
}

func (f *fakeAgent) SendPlanPrompt(sessionID, text string) (RunHandle, error) {
	f.planSent = append(f.planSent, struct{ sessionID, text string }{sessionID, text})
	if f.planErr != nil {
		return RunHandle{}, f.planErr
	}
	return f.nextRun(sessionID), nil
}

func (f *fakeAgent) AcceptPlan(sessionID string) (RunHandle, error) {
	f.accepted = append(f.accepted, sessionID)
	if f.acceptErr != nil {
		return RunHandle{}, f.acceptErr
	}
	return f.nextRun(sessionID), nil
}

// resolvedPermission is one recorded ResolvePermission call. approved() keeps
// the assertions that only care whether the call ran readable.
type resolvedPermission struct {
	sessionID, callID string
	verdict           permission.Verdict
}

func (r resolvedPermission) approved() bool { return r.verdict.Approved() }

func (f *fakeAgent) ResolvePermission(sessionID, callID string, verdict permission.Verdict) {
	f.resolved = append(f.resolved, resolvedPermission{sessionID, callID, verdict})
}

func (f *fakeAgent) Stop(sessionID string) {
	f.stopped = append(f.stopped, sessionID)
}

func (f *fakeAgent) ToolCatalog() tool.Catalog {
	if f.tools == nil {
		f.tools = shippedToolCatalog()
	}
	return f.tools
}

// shippedToolCatalog is the registry the engine hands the Model, built from the
// same constructors internal/wiring registers. The tools are real so the transcript
// and the permission panel are asserted against the labels, subjects and bodies
// that actually ship — a stand-in would agree with the tests by construction and
// prove nothing. Nothing here is executed, so the dependencies the tools would
// need at run time stay nil and the root never has to exist.
func shippedToolCatalog() tool.Catalog {
	const root = "/nonexistent"
	return tool.NewRegistry(tool.NewOutputStore(1024),
		tool.NewReadTool(root, nil), tool.NewWriteTool(root, nil),
		tool.NewEditTool(root, nil, nil), tool.NewGlobTool(root),
		tool.NewGrepTool(root, nil), tool.NewBashTool(root),
		tool.NewPresentPlanTool(root), tool.NewSkillTool(nil),
		subagent.NewTaskTool(nil, nil, nil, nil), tool.NewWebFetchTool(nil),
		tool.TodoWriteTool{})
}

func (f *fakeAgent) Undo(sessionID string) (UndoResult, error) {
	f.undos = append(f.undos, sessionID)
	return f.undoResult, f.undoErr
}

func (f *fakeAgent) ListResumeSessions(currentSessionID string) ([]session.SessionSummary, error) {
	f.resumeLists = append(f.resumeLists, currentSessionID)
	return append([]session.SessionSummary(nil), f.resumeSessions...), f.resumeListErr
}

func (f *fakeAgent) ResumeSessionByID(currentSessionID, targetSessionID string) (ResumeResult, error) {
	f.resumeLoads = append(f.resumeLoads, struct{ currentID, targetID string }{currentSessionID, targetSessionID})
	return f.resume, f.resumeErr
}

func TestModel_ResumePickerListsFiltersSelectsAndLoadsExactSession(t *testing.T) {
	fake := &fakeAgent{
		resumeSessions: []session.SessionSummary{
			{ID: "tui-alpha", Title: "Alpha session"},
			{ID: "tui-beta", Title: "Beta session"},
			{ID: "tui-gamma", Title: "Gamma session"},
		},
		resume: ResumeResult{
			SessionID: "tui-gamma",
			Events:    []session.SessionEvent{{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "restored transcript"}}},
			Mode:      session.ModePlan,
			History:   []string{"first restored prompt", "latest restored prompt"},
		},
	}
	m := NewModel(fake, "tui-current", nil)
	m.entries = []entry{{kind: entryUser, text: "current transcript"}}
	m.history = []string{"current prompt"}
	m.histIdx = len(m.history)
	m = typeRunes(t, m, "/resume")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil || !m.resumePicker.open || !m.resumePicker.loading {
		t.Fatalf("resume submit = cmd:%v picker:%+v, want async open loading picker", cmd != nil, m.resumePicker)
	}
	if m.input.Value() != "" || len(m.menuItems) != 0 {
		t.Fatalf("composer/menu = %q/%+v, want cleared", m.input.Value(), m.menuItems)
	}
	if len(m.entries) != 1 || m.entries[0].text != "current transcript" {
		t.Fatalf("entries changed while listing: %+v", m.entries)
	}

	m = apply(t, m, cmd())
	if len(fake.resumeLists) != 1 || fake.resumeLists[0] != "tui-current" {
		t.Fatalf("ListResumeSessions calls = %q, want [tui-current]", fake.resumeLists)
	}
	if m.resumePicker.loading || len(m.resumePicker.filtered) != 3 {
		t.Fatalf("listed picker = %+v, want three loaded sessions", m.resumePicker)
	}

	m = typeRunes(t, m, "a")
	if got := sessionIDs(m.resumePicker.filtered); !slices.Equal(got, []string{"tui-alpha", "tui-beta", "tui-gamma"}) {
		t.Fatalf("filtered IDs = %q, want all matching a", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil || !m.resumePicker.open || !m.resumePicker.loading {
		t.Fatalf("selection submit = cmd:%v picker:%+v, want async loading", cmd != nil, m.resumePicker)
	}
	m = apply(t, m, cmd())

	if len(fake.resumeLoads) != 1 || fake.resumeLoads[0].currentID != "tui-current" || fake.resumeLoads[0].targetID != "tui-gamma" {
		t.Fatalf("ResumeSessionByID calls = %+v, want current -> gamma", fake.resumeLoads)
	}
	if m.resumePicker.open || m.sessionID != "tui-gamma" || !m.planMode || m.working || m.activeRun != 0 || !m.followAgent {
		t.Fatalf("restored model = picker:%+v session:%q plan:%v working:%v run:%d follow:%v", m.resumePicker, m.sessionID, m.planMode, m.working, m.activeRun, m.followAgent)
	}
	if len(m.entries) != 1 || m.entries[0].text != "restored transcript" {
		t.Fatalf("entries = %+v, want restored transcript", m.entries)
	}
	if !slices.Equal(m.history, []string{"first restored prompt", "latest restored prompt"}) || m.histIdx != 2 {
		t.Fatalf("history = %q idx=%d, want restored history at end", m.history, m.histIdx)
	}
}

func TestModel_ResumePickerCapturesKeysAndEscapePreservesChat(t *testing.T) {
	fake := &fakeAgent{resumeSessions: []session.SessionSummary{{ID: "tui-old", Title: "Old session"}}}
	m := NewModel(fake, "tui-current", nil)
	m.entries = []entry{{kind: entryUser, text: "keep chat"}}
	m.history = []string{"keep history"}
	m.histIdx = 1
	m.ready = true
	m.viewport.Height = 1
	m.viewport.SetContent("one\ntwo\nthree")
	m.viewport.SetYOffset(1)
	m = typeRunes(t, m, "/resume")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, updated.(Model), cmd())
	offset := m.viewport.YOffset

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.viewport.YOffset != offset {
		t.Fatalf("viewport offset = %d, want picker to capture PgDown at %d", m.viewport.YOffset, offset)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.resumePicker.open || m.sessionID != "tui-current" || len(m.entries) != 1 || m.entries[0].text != "keep chat" || !slices.Equal(m.history, []string{"keep history"}) {
		t.Fatalf("escape changed model: picker:%+v session:%q entries:%+v history:%q", m.resumePicker, m.sessionID, m.entries, m.history)
	}
}

func TestModel_ResumePickerExposesNoMatches(t *testing.T) {
	fake := &fakeAgent{resumeSessions: []session.SessionSummary{{ID: "tui-old", Title: "Old session"}}}
	m := NewModel(fake, "tui-current", nil)
	m = typeRunes(t, m, "/resume")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, updated.(Model), cmd())
	m = typeRunes(t, m, "missing")

	if !m.resumePicker.open || len(m.resumePicker.filtered) != 0 {
		t.Fatalf("picker = %+v, want open no-matches state", m.resumePicker)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.resumePicker.query.Value() != "missin" || len(m.resumePicker.filtered) != 0 {
		t.Fatalf("backspace query/filter = %q/%+v, want updated no-matches state", m.resumePicker.query.Value(), m.resumePicker.filtered)
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || !updated.(Model).resumePicker.open || len(fake.resumeLoads) != 0 {
		t.Fatalf("Enter with no matches = cmd:%v loads:%+v", cmd != nil, fake.resumeLoads)
	}
}

func TestModel_ResumePickerErrorsPreserveCurrentSession(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		fake := &fakeAgent{resumeListErr: errors.New("list failed")}
		m := NewModel(fake, "tui-current", nil)
		m.entries = []entry{{kind: entryUser, text: "keep chat"}}
		m.history = []string{"keep history"}
		m = typeRunes(t, m, "/resume")
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = apply(t, updated.(Model), cmd())

		if !m.resumePicker.open || m.resumePicker.loading || m.resumePicker.err == nil || m.resumePicker.err.Error() != "list failed" {
			t.Fatalf("picker = %+v, want closable list failure", m.resumePicker)
		}
		if m.sessionID != "tui-current" || len(m.entries) != 1 || !slices.Equal(m.history, []string{"keep history"}) {
			t.Fatalf("list failure changed current session: session=%q entries=%+v history=%q", m.sessionID, m.entries, m.history)
		}
	})

	t.Run("load", func(t *testing.T) {
		fake := &fakeAgent{
			resumeSessions: []session.SessionSummary{{ID: "tui-target", Title: "Target"}},
			resumeErr:      errors.New("load failed"),
		}
		m := NewModel(fake, "tui-current", nil)
		m.entries = []entry{{kind: entryUser, text: "keep chat"}}
		m.history = []string{"keep history"}
		m.planMode = true
		m.activeRun = 77
		m.followAgent = false
		m = typeRunes(t, m, "/resume")
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = apply(t, updated.(Model), cmd())
		updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = apply(t, updated.(Model), cmd())

		if !m.resumePicker.open || m.resumePicker.loading || m.resumePicker.err == nil || m.resumePicker.err.Error() != "load failed" {
			t.Fatalf("picker = %+v, want load failure", m.resumePicker)
		}
		if m.sessionID != "tui-current" || len(m.entries) != 1 || m.entries[0].text != "keep chat" || !slices.Equal(m.history, []string{"keep history"}) || !m.planMode || m.activeRun != 77 || m.followAgent {
			t.Fatalf("load failure changed session: session=%q entries=%+v history=%q plan=%v run=%d follow=%v", m.sessionID, m.entries, m.history, m.planMode, m.activeRun, m.followAgent)
		}
	})
}

func TestModel_ResumeCommandRejectsArgumentsAndActiveRunsLocally(t *testing.T) {
	t.Run("arguments", func(t *testing.T) {
		fake := &fakeAgent{}
		m := NewModel(fake, "tui-current", nil)
		m = typeRunes(t, m, "/resume extra")
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if len(fake.resumeLists) != 0 || m.resumePicker.open || len(m.entries) != 1 || m.entries[0].text != "usage: /resume" {
			t.Fatalf("argument rejection = lists:%q picker:%+v entries:%+v", fake.resumeLists, m.resumePicker, m.entries)
		}
	})

	t.Run("active run", func(t *testing.T) {
		fake := &fakeAgent{}
		m := NewModel(fake, "tui-current", nil)
		m.working = true
		m.activeRun = 42
		m = typeRunes(t, m, "/resume")
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if len(fake.resumeLists) != 0 || m.resumePicker.open || len(m.entries) != 1 || m.entries[0].text != engine.ErrResumeActiveRun.Error() {
			t.Fatalf("active rejection = lists:%q picker:%+v entries:%+v", fake.resumeLists, m.resumePicker, m.entries)
		}
	})
}

func TestModel_ResumeBuiltinMenuEnterOpensPicker(t *testing.T) {
	fake := &fakeAgent{resumeSessions: []session.SessionSummary{{ID: "tui-old", Title: "Old"}}}
	m := NewModel(fake, "tui-current", nil).WithCompletions([]command.Command{{Name: "resume", Description: "Resume a session", BuiltIn: true}}, nil)
	m = typeRunes(t, m, "/res")
	if len(m.menuItems) != 1 || !m.menuItems[0].builtin || m.menuItems[0].label != "/resume" {
		t.Fatalf("menuItems = %+v, want builtin /resume", m.menuItems)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil || !m.resumePicker.open || m.input.Value() != "" || len(m.menuItems) != 0 {
		t.Fatalf("menu Enter = cmd:%v picker:%+v input:%q menu:%+v", cmd != nil, m.resumePicker, m.input.Value(), m.menuItems)
	}
	m = apply(t, m, cmd())
	if len(fake.resumeLists) != 1 || fake.resumeLists[0] != "tui-current" {
		t.Fatalf("resume list calls = %q", fake.resumeLists)
	}
}

func TestModel_ResumePickerIgnoresListResultFromClosedGeneration(t *testing.T) {
	fake := &fakeAgent{resumeSessions: []session.SessionSummary{{ID: "tui-a", Title: "Picker A"}}}
	m := NewModel(fake, "tui-current", nil)
	m = typeRunes(t, m, "/resume")
	updated, listA := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	resultA := listA()

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	fake.resumeSessions = []session.SessionSummary{{ID: "tui-b", Title: "Picker B"}}
	m = typeRunes(t, m, "/resume")
	updated, listB := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	m = apply(t, m, resultA)
	if !m.resumePicker.open || !m.resumePicker.loading || len(m.resumePicker.sessions) != 0 {
		t.Fatalf("stale A result changed picker B: %+v", m.resumePicker)
	}
	m = apply(t, m, listB())
	if m.resumePicker.loading || !slices.Equal(sessionIDs(m.resumePicker.filtered), []string{"tui-b"}) {
		t.Fatalf("current B result = %+v, want loaded picker B", m.resumePicker)
	}
}

func TestModel_ResumePickerIgnoresLoadResultAfterCloseAndReopen(t *testing.T) {
	fake := &fakeAgent{resumeSessions: []session.SessionSummary{{ID: "tui-old", Title: "Old target"}}}
	m := NewModel(fake, "tui-current", nil)
	m.entries = []entry{{kind: entryUser, text: "keep current chat"}}
	m.history = []string{"keep current history"}
	m = typeRunes(t, m, "/resume")
	updated, listOld := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, updated.(Model), listOld())
	fake.resume = ResumeResult{
		SessionID: "tui-old",
		Events:    []session.SessionEvent{{Message: &session.Message{ID: "old", Role: session.RoleUser, Text: "old chat"}}},
		History:   []string{"old history"},
	}
	updated, loadOld := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	resultOld := loadOld()

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.sessionID != "tui-current" || len(m.entries) != 1 || m.entries[0].text != "keep current chat" {
		t.Fatalf("Esc during load changed current chat: session=%q entries=%+v", m.sessionID, m.entries)
	}
	fake.resumeSessions = []session.SessionSummary{{ID: "tui-new", Title: "New target"}}
	m = typeRunes(t, m, "/resume")
	updated, listNew := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, updated.(Model), listNew())

	m = apply(t, m, resultOld)
	if !m.resumePicker.open || m.sessionID != "tui-current" || len(m.entries) != 1 || m.entries[0].text != "keep current chat" || !slices.Equal(m.history, []string{"keep current history"}) {
		t.Fatalf("stale load replaced current session: picker=%+v session=%q entries=%+v history=%q", m.resumePicker, m.sessionID, m.entries, m.history)
	}
}

func TestModel_ResumePickerAcceptsLoadFromCurrentGenerationAndTarget(t *testing.T) {
	fake := &fakeAgent{
		resumeSessions: []session.SessionSummary{{ID: "tui-target", Title: "Target"}},
		resume: ResumeResult{
			SessionID: "tui-target",
			Events:    []session.SessionEvent{{Message: &session.Message{ID: "target", Role: session.RoleUser, Text: "target chat"}}},
			History:   []string{"target history"},
		},
	}
	m := NewModel(fake, "tui-current", nil)
	m = typeRunes(t, m, "/resume")
	updated, list := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, updated.(Model), list())
	updated, load := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, updated.(Model), load())

	if m.resumePicker.open || m.sessionID != "tui-target" || len(m.entries) != 1 || m.entries[0].text != "target chat" || !slices.Equal(m.history, []string{"target history"}) {
		t.Fatalf("current load = picker:%+v session:%q entries:%+v history:%q", m.resumePicker, m.sessionID, m.entries, m.history)
	}
}

func TestModel_ResumePickerIgnoresLoadForMismatchedTarget(t *testing.T) {
	fake := &fakeAgent{resumeSessions: []session.SessionSummary{{ID: "tui-target", Title: "Target"}}}
	m := NewModel(fake, "tui-current", nil)
	m.entries = []entry{{kind: entryUser, text: "keep chat"}}
	m = typeRunes(t, m, "/resume")
	updated, list := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, updated.(Model), list())
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	m = apply(t, m, ResumeDoneMsg{
		Generation: m.resumeGen,
		SessionID:  "tui-other",
		Result: ResumeResult{
			SessionID: "tui-other",
			Events:    []session.SessionEvent{{Message: &session.Message{ID: "other", Role: session.RoleUser, Text: "other chat"}}},
		},
	})
	if !m.resumePicker.open || !m.resumePicker.loading || m.sessionID != "tui-current" || len(m.entries) != 1 || m.entries[0].text != "keep chat" {
		t.Fatalf("mismatched target changed model: picker=%+v session=%q entries=%+v", m.resumePicker, m.sessionID, m.entries)
	}
}

func TestModel_ResumePickerFailsVisibleWhenResultSessionMismatchesEnvelope(t *testing.T) {
	fake := &fakeAgent{resumeSessions: []session.SessionSummary{{ID: "tui-target", Title: "Target"}}}
	m := NewModel(fake, "tui-current", nil)
	m.entries = []entry{{kind: entryUser, text: "keep chat"}}
	m = typeRunes(t, m, "/resume")
	updated, list := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, updated.(Model), list())
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	m = apply(t, m, ResumeDoneMsg{
		Generation: m.resumeGen,
		SessionID:  "tui-target",
		Result: ResumeResult{
			SessionID: "tui-other",
			Events:    []session.SessionEvent{{Message: &session.Message{ID: "other", Role: session.RoleUser, Text: "other chat"}}},
		},
	})
	if !m.resumePicker.open || m.resumePicker.loading || m.resumePicker.err == nil || m.resumePicker.err.Error() != "resume result session mismatch" {
		t.Fatalf("mismatched result picker = %+v, want visible stable failure", m.resumePicker)
	}
	if m.sessionID != "tui-current" || len(m.entries) != 1 || m.entries[0].text != "keep chat" {
		t.Fatalf("mismatched result changed current chat: session=%q entries=%+v", m.sessionID, m.entries)
	}
}

func TestModel_ResumePickerCapturesMouseAndEscapePreservesChat(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "tui-current", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 10})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "settled reasoning"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "settled reasoning"})
	m = drainReveal(t, m)
	for i := 0; i < 20; i++ {
		m.entries = append(m.entries, entry{kind: entryUser, text: fmt.Sprintf("message %02d", i)})
	}
	m.followAgent = false
	m = m.syncViewport()
	m.viewport.SetYOffset(0)
	summaryRow := -1
	for row, line := range m.entryLines() {
		if strings.Contains(line.line, "◆ Thought") {
			summaryRow = row
			break
		}
	}
	if summaryRow < 0 {
		t.Fatal("reasoning summary not found")
	}
	clickY := topBarHeight + summaryRow
	originalFocus := m.focus
	originalOffset := m.viewport.YOffset
	originalEntries := append([]entry(nil), m.entries...)
	m = typeRunes(t, m, "/resume")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: clickY})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: 2, Y: clickY})
	if m.viewport.YOffset != originalOffset || m.focus != originalFocus || m.entries[0].expanded {
		t.Fatalf("modal mouse changed chat: offset=%d focus=%v expanded=%v", m.viewport.YOffset, m.focus, m.entries[0].expanded)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.resumePicker.open || m.sessionID != "tui-current" || !slices.EqualFunc(m.entries, originalEntries, func(a, b entry) bool {
		return a.kind == b.kind && a.text == b.text && a.expanded == b.expanded
	}) {
		t.Fatalf("Esc after modal mouse changed chat: picker=%+v session=%q entries=%+v", m.resumePicker, m.sessionID, m.entries)
	}
}

func TestModel_ResumePickerOwnsFocusAcrossTerminalFocusChanges(t *testing.T) {
	m := NewModel(&fakeAgent{}, "tui-current", nil)
	m = typeRunes(t, m, "/resume")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.input.Focused() || !m.resumePicker.query.Focused() {
		t.Fatalf("open focus = composer:%v query:%v, want picker-only focus", m.input.Focused(), m.resumePicker.query.Focused())
	}

	m = apply(t, m, tea.BlurMsg{})
	if m.input.Focused() || m.resumePicker.query.Focused() {
		t.Fatalf("terminal blur focus = composer:%v query:%v, want both blurred", m.input.Focused(), m.resumePicker.query.Focused())
	}
	m = apply(t, m, tea.FocusMsg{})
	if m.input.Focused() || !m.resumePicker.query.Focused() {
		t.Fatalf("terminal refocus = composer:%v query:%v, want picker-only focus", m.input.Focused(), m.resumePicker.query.Focused())
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.input.Focused() || m.resumePicker.query.Focused() {
		t.Fatalf("picker close focus = composer:%v query:%v, want composer restored", m.input.Focused(), m.resumePicker.query.Focused())
	}
}

func TestModel_ResumePickerRendersFullScreenRows(t *testing.T) {
	m := NewModel(&fakeAgent{}, "tui-current", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 72, Height: 18})
	m.entries = []entry{{kind: entryUser, text: "hidden chat text"}}
	m = m.syncViewport()
	m.resumePicker = newResumePicker("tui-current")
	m.resumePicker.setSessions([]session.SessionSummary{
		{
			ID:           "tui-current",
			Title:        "Current session",
			LastActivity: time.Date(2026, time.July, 14, 9, 5, 0, 0, time.Local),
		},
		{
			ID:           "tui-other",
			Title:        "Selected session",
			LastActivity: time.Date(2026, time.July, 13, 17, 45, 0, 0, time.Local),
		},
		{ID: "tui-untitled"},
	})
	m.resumePicker.selected = 1

	view := m.View()
	plain := ansi.Strip(view)
	if strings.Contains(plain, "hidden chat text") || strings.Contains(plain, "chat *") {
		t.Fatalf("View() rendered hidden chat chrome: %q", plain)
	}
	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╰") || !strings.Contains(plain, "Search sessions") {
		t.Fatalf("View() missing rounded focused search box: %q", plain)
	}
	currentLine := lineContaining(t, plain, "Current session")
	for _, want := range []string{"current", "Jul 14, 2026 09:05"} {
		if !strings.Contains(currentLine, want) {
			t.Fatalf("current row = %q, want %q", currentLine, want)
		}
	}
	selectedLine := lineContaining(t, plain, "Selected session")
	if !strings.Contains(selectedLine, "❯") || !strings.Contains(selectedLine, "Jul 13, 2026 17:45") {
		t.Fatalf("selected row = %q", selectedLine)
	}
	if !strings.Contains(view, accentStyle.Render("❯")) {
		t.Fatalf("View() does not accent selected indicator: %q", view)
	}
	if !strings.Contains(view, statusStyle.Render("current")) || !strings.Contains(view, statusStyle.Render("Jul 14, 2026 09:05")) {
		t.Fatalf("View() does not mute current marker and unselected timestamp: %q", view)
	}
	if !strings.Contains(plain, "Untitled session") {
		t.Fatalf("View() missing stable empty-title placeholder: %q", plain)
	}
	if currentIndex, selectedIndex := lineIndexWith(t, plain, "Current session"), lineIndexWith(t, plain, "Selected session"); currentIndex >= selectedIndex {
		t.Fatalf("filtered row order changed: current=%d selected=%d", currentIndex, selectedIndex)
	}
	if got := len(strings.Split(view, "\n")); got != 18 {
		t.Fatalf("View() lines = %d, want terminal height 18: %q", got, plain)
	}
	assertNoLineWiderThan(t, view, 72)
}

func TestModel_ResumePickerRendersLoadingErrorAndExactEmptyState(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*resumePicker)
		want  string
	}{
		{name: "loading", want: "Loading sessions…"},
		{name: "error", setup: func(picker *resumePicker) { picker.fail("sessions unavailable") }, want: "sessions unavailable"},
		{name: "empty", setup: func(picker *resumePicker) { picker.setSessions(nil) }, want: "No sessions found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "tui-current", nil)
			m = apply(t, m, tea.WindowSizeMsg{Width: 42, Height: 9})
			m.resumePicker = newResumePicker("tui-current")
			if tt.setup != nil {
				tt.setup(&m.resumePicker)
			}

			view := m.View()
			plain := ansi.Strip(view)
			if !strings.Contains(plain, tt.want) {
				t.Fatalf("View() = %q, want %q", plain, tt.want)
			}
			if tt.name == "loading" && !strings.Contains(view, statusStyle.Render(tt.want)) {
				t.Fatalf("loading state is not muted: %q", view)
			}
			if tt.name == "error" && !strings.Contains(view, errorStyle.Render(tt.want)) {
				t.Fatalf("error state does not use error style: %q", view)
			}
			if tt.name == "empty" && strings.TrimSpace(lineContaining(t, plain, tt.want)) != "No sessions found" {
				t.Fatalf("empty row = %q, want exact text", lineContaining(t, plain, tt.want))
			}
			assertNoLineWiderThan(t, view, 42)
		})
	}
}

func TestModel_ResumePickerRowsStayANSIAndUnicodeWidthSafe(t *testing.T) {
	m := NewModel(&fakeAgent{}, "tui-current", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 24, Height: 8})
	m.resumePicker = newResumePicker("tui-current")
	m.resumePicker.setSessions([]session.SessionSummary{{
		ID:           "tui-current",
		Title:        "\x1b[31m非常に長いセッション名 with more text\x1b[0m",
		LastActivity: time.Date(2026, time.July, 14, 9, 5, 0, 0, time.Local),
	}})

	view := m.View()
	plain := ansi.Strip(view)
	if strings.Contains(plain, "[31m") || !strings.Contains(plain, "非常") {
		t.Fatalf("View() did not safely render ANSI/unicode title: %q", plain)
	}
	assertNoLineWiderThan(t, view, 24)

	for _, size := range []tea.WindowSizeMsg{{Width: 1, Height: 2}, {Width: 2, Height: 1}} {
		tiny := NewModel(&fakeAgent{}, "tui-current", nil)
		tiny = apply(t, tiny, size)
		tiny.resumePicker = m.resumePicker
		tiny.resumePicker.open = true
		tinyView := tiny.View()
		assertNoLineWiderThan(t, tinyView, size.Width)
		if got := len(strings.Split(tinyView, "\n")); got > size.Height {
			t.Fatalf("tiny View() lines = %d, want at most %d: %q", got, size.Height, ansi.Strip(tinyView))
		}
	}

	beforeSize := NewModel(&fakeAgent{}, "tui-current", nil)
	beforeSize.resumePicker = newResumePicker("tui-current")
	beforeSize.resumePicker.setSessions([]session.SessionSummary{{ID: "one", Title: "Before size"}})
	_ = beforeSize.View()
}

func TestModel_ResumePickerKeepsSelectedRowVisible(t *testing.T) {
	m := NewModel(&fakeAgent{}, "tui-current", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	m.resumePicker = newResumePicker("tui-current")
	sessions := make([]session.SessionSummary, 24)
	for i := range sessions {
		sessions[i] = session.SessionSummary{ID: fmt.Sprintf("session-%02d", i), Title: fmt.Sprintf("Session %02d", i)}
	}
	m.resumePicker.setSessions(sessions)
	m.resumePicker.selected = 19

	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "Session 19") || strings.Contains(plain, "Session 00") {
		t.Fatalf("View() = %q, selected row must be visible in derived window", plain)
	}
	if got := strings.Count(plain, "Session "); got > 5 {
		t.Fatalf("View() rendered %d session rows, want at most available height: %q", got, plain)
	}
	assertNoLineWiderThan(t, m.View(), 40)
}

func lineContaining(t *testing.T, view, needle string) string {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("View() = %q, want line containing %q", view, needle)
	return ""
}

func TestModel_WithSessionRestoresTranscriptAndModeWithinBuilderChain(t *testing.T) {
	events := []session.SessionEvent{{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "restored chat"}}}
	m := NewModel(nil, "tui-session", nil).
		WithSession(events, session.ModePlan).
		WithHistory([]string{"restored history"})

	if m.sessionID != "tui-session" || len(m.entries) != 1 || m.entries[0].text != "restored chat" || !m.planMode {
		t.Fatalf("WithSession = session:%q entries:%+v plan:%v", m.sessionID, m.entries, m.planMode)
	}
	if !slices.Equal(m.history, []string{"restored history"}) {
		t.Fatalf("builder history = %q, want supplied by WithHistory", m.history)
	}
}

func TestModel_UndoIsNativeCommandAndRestoresComposer(t *testing.T) {
	fake := &fakeAgent{undoResult: UndoResult{
		Prompt: "original prompt",
		Events: []session.SessionEvent{{Message: &session.Message{ID: "u0", Role: session.RoleUser, Text: "kept"}}},
	}}
	m := NewModel(fake, "s1", nil)
	m.entries = []entry{{kind: entryUser, text: "old"}, {kind: entryAssistant, text: "answer"}}
	m.history = []string{"old prompt"}
	m.histIdx = len(m.history)
	m = typeRunes(t, m, "/undo")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("undo must run asynchronously")
	}
	m = apply(t, m, cmd())
	if len(fake.undos) != 1 || fake.undos[0] != "s1" {
		t.Fatalf("Undo calls = %v", fake.undos)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt calls = %v", fake.sent)
	}
	if len(m.entries) != 1 || m.entries[0].kind != entryUser || m.entries[0].text != "kept" {
		t.Fatalf("entries = %+v", m.entries)
	}
	if m.input.Value() != "original prompt" || m.input.Position() != len([]rune("original prompt")) {
		t.Fatalf("composer = %q cursor=%d", m.input.Value(), m.input.Position())
	}
	if len(m.history) != 1 || m.history[0] != "old prompt" {
		t.Fatalf("history = %v", m.history)
	}
}

func TestModel_UndoFailureKeepsTranscriptAndComposer(t *testing.T) {
	fake := &fakeAgent{undoErr: errors.New("undo failed")}
	m := NewModel(fake, "s1", nil)
	m.entries = []entry{{kind: entryUser, text: "old"}}
	m = typeRunes(t, m, "/undo")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	m = apply(t, m, cmd())
	if m.input.Value() != "/undo" {
		t.Fatalf("composer = %q", m.input.Value())
	}
	if len(m.entries) != 2 || m.entries[0].text != "old" || m.entries[1].kind != entryError || m.entries[1].text != "undo failed" {
		t.Fatalf("entries = %+v", m.entries)
	}
}

func TestModel_UndoAppearsInSlashCompletion(t *testing.T) {
	eng := engine.New(engine.Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})
	commands := eng.Commands()
	found := false
	for _, item := range commands {
		if item.Name == "undo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Commands = %+v", commands)
	}
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(commands, nil)
	m = typeRunes(t, m, "/un")
	if len(m.menuItems) == 0 || m.menuItems[0].label != "/undo" {
		t.Fatalf("menuItems = %+v", m.menuItems)
	}
}

func TestModel_UndoWithArgumentsIsRejectedLocally(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = typeRunes(t, m, "/undo now")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.undos) != 0 || len(fake.sent) != 0 {
		t.Fatalf("undos=%v sent=%v", fake.undos, fake.sent)
	}
	if len(m.entries) != 1 || m.entries[0].kind != entryError || m.entries[0].text != "usage: /undo" {
		t.Fatalf("entries = %+v", m.entries)
	}
}

// apply passes a message through Update and returns the specific Model.
func apply(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	return settleDiskWork(t, next)
}

func settleDiskWork(t *testing.T, m Model) Model {
	t.Helper()
	for {
		switch {
		case m.treeLoading:
			files, err := m.listFiles()
			updated, _ := m.Update(filesListedMsg{target: fileListTree, generation: m.treeGen, files: files, err: err})
			m = updated.(Model)
		case m.filesLoading:
			files, err := m.listFiles()
			updated, _ := m.Update(filesListedMsg{target: fileListMenu, generation: m.filesGen, files: files, err: err})
			m = updated.(Model)
		case m.viewerLoading:
			content, err := m.fileReader(m.viewer.path)
			viewer := openFileViewer(m.viewer.path, content)
			if err != nil {
				viewer = openFileViewerError(m.viewer.path, err)
			}
			updated, _ := m.Update(fileOpenedMsg{generation: m.viewerGen, path: m.viewer.path, viewer: viewer})
			m = updated.(Model)
		default:
			return m
		}
	}
}

func (m Model) toggleTree() Model {
	next, cmd := m.toggleTreeAsync()
	if cmd == nil {
		return next
	}
	updated, _ := next.Update(cmd())
	return updated.(Model)
}

func TestModel_DiskWorkRunsOutsideUpdate(t *testing.T) {
	t.Run("opening explorer defers workspace listing", func(t *testing.T) {
		calls := 0
		m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
			calls++
			return []string{"go.mod"}, nil
		})
		m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
		if _, ok := updated.(Model); !ok {
			t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
		}
		if calls != 0 {
			t.Fatalf("listFiles calls during Update = %d, want 0", calls)
		}
	})

	t.Run("typing mention defers workspace listing", func(t *testing.T) {
		calls := 0
		m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
			calls++
			return []string{"go.mod"}, nil
		})

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})
		if _, ok := updated.(Model); !ok {
			t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
		}
		if calls != 0 {
			t.Fatalf("listFiles calls during Update = %d, want 0", calls)
		}
	})

	t.Run("opening file defers reading and highlighting", func(t *testing.T) {
		calls := 0
		m := NewModel(&fakeAgent{}, "s1", nil).WithFileReader(func(path string) ([]byte, error) {
			calls++
			return []byte("package main\n"), nil
		})
		m.treeOpen = true
		m.focus = explorerFocus
		m.treeLoaded = true
		m.tree = newFileTree([]string{"main.go"})

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if _, ok := updated.(Model); !ok {
			t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
		}
		if calls != 0 {
			t.Fatalf("fileReader calls during Update = %d, want 0", calls)
		}
	})
}

func TestModel_AsyncDiskWorkTracksLoadingErrorsAndLatestResult(t *testing.T) {
	t.Run("explorer shows loading then error", func(t *testing.T) {
		m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
			return nil, errors.New("glob failed")
		})

		m, cmd := m.toggleTreeAsync()
		if cmd == nil || !m.treeLoading || !strings.Contains(m.View(), "cargando workspace") {
			t.Fatalf("tree loading state = loading:%v cmd:%v view:%q", m.treeLoading, cmd != nil, m.View())
		}
		m = apply(t, m, cmd())
		if m.treeLoading || m.treeError != "glob failed" || !strings.Contains(m.View(), "glob failed") {
			t.Fatalf("tree error state = loading:%v error:%q view:%q", m.treeLoading, m.treeError, m.View())
		}
	})

	t.Run("mention shows loading then files", func(t *testing.T) {
		m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
			return []string{"internal/tui/model.go"}, nil
		})
		m.input.SetValue("@")
		m.input.CursorEnd()

		m, cmd := m.refreshMenu()
		if cmd == nil || !m.filesLoading || len(m.menuItems) != 1 || m.menuItems[0].label != "Loading files…" {
			t.Fatalf("mention loading state = loading:%v cmd:%v items:%+v", m.filesLoading, cmd != nil, m.menuItems)
		}
		m = apply(t, m, cmd())
		if m.filesLoading || len(m.menuItems) != 1 || m.menuItems[0].label != "internal/tui/model.go" {
			t.Fatalf("mention result state = loading:%v items:%+v", m.filesLoading, m.menuItems)
		}
	})

	t.Run("viewer ignores stale file result", func(t *testing.T) {
		m := NewModel(&fakeAgent{}, "s1", nil).WithFileReader(func(path string) ([]byte, error) {
			return []byte(path), nil
		})
		m, first := m.startOpenTreeFile("first.txt")
		m, second := m.startOpenTreeFile("second.txt")

		updated, _ := m.Update(first())
		m = updated.(Model)
		if m.viewer.path != "second.txt" || m.viewer.message != "cargando archivo…" {
			t.Fatalf("stale result changed viewer = %+v", m.viewer)
		}
		updated, _ = m.Update(second())
		m = updated.(Model)
		if m.viewer.path != "second.txt" || m.viewer.message != "" || ansi.Strip(strings.Join(m.viewer.lines, "\n")) != "second.txt" {
			t.Fatalf("latest result not applied = %+v", m.viewer)
		}
	})

	t.Run("viewer applies navigation received while loading", func(t *testing.T) {
		var content strings.Builder
		for line := 0; line < 40; line++ {
			content.WriteString("line\n")
		}
		m := NewModel(&fakeAgent{}, "s1", nil).WithFileReader(func(string) ([]byte, error) {
			return []byte(content.String()), nil
		})
		m.ready = true
		m.height = 10
		m, cmd := m.startOpenTreeFile("long.txt")
		m.focus = viewerFocus
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		m = updated.(Model)
		if m.viewerPending == 0 {
			t.Fatal("PgDown while loading must be queued")
		}
		updated, _ = m.Update(cmd())
		m = updated.(Model)
		if m.viewerLoading || m.viewer.offset == 0 {
			t.Fatalf("loaded viewer = loading:%v offset:%d, want queued scroll applied", m.viewerLoading, m.viewer.offset)
		}
	})
}

func activeRunDone(m Model, err string) RunDoneMsg {
	return RunDoneMsg{SessionID: m.sessionID, RunID: m.activeRun, Err: err}
}

func TestModel_ProviderRateLimitIsCompactAndNotDuplicated(t *testing.T) {
	raw := `provider stream failed: POST "https://openrouter.ai/api/v1/chat/completions?api_key=super-secret": 429 Too Many Requests {"error":{"message":"temporarily rate-limited upstream"}}`
	root := t.TempDir()
	eng := engine.New(engine.Config{
		Root:     root,
		Provider: llm.NewFakeProvider(llm.Event{Kind: llm.StepStarted}, llm.Event{Kind: llm.StepFailed, Text: strings.TrimPrefix(raw, "provider stream failed: ")}),
		Store:    session.NewMemoryStore(),
	})
	m := NewModel(eng, "s1", eng.Events())
	m = typeRunes(t, m, "hello")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	for m.working {
		m = apply(t, m, nextMsg(t, eng.Events(), 5*time.Second))
	}

	errorCount := 0
	for _, entry := range m.entries {
		if entry.kind == entryError {
			errorCount++
		}
	}
	if errorCount != 1 {
		t.Fatalf("error entries = %d, want one compact entry: %+v", errorCount, m.entries)
	}
	view := m.View()
	if !strings.Contains(view, "Rate limit reached") || !strings.Contains(view, "[r retry] [d details]") {
		t.Fatalf("View() = %q, want compact actionable rate-limit message", view)
	}
	if strings.Contains(view, "temporarily rate-limited upstream") || strings.Contains(view, "https://openrouter.ai") {
		t.Fatalf("View() leaked raw provider details while collapsed: %q", view)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if view = m.View(); !strings.Contains(view, "temporarily rate-limited upstream") {
		t.Fatalf("View() = %q, d must reveal sanitized technical details", view)
	}
	if strings.Contains(view, "super-secret") || !strings.Contains(view, "[redacted]") {
		t.Fatalf("View() = %q, details must redact provider credentials", view)
	}
}

func TestModel_ProviderRetryStatusIsTransient(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, EventMsg(session.SessionEvent{Kind: session.KindStepRetrying, Text: "Rate limit reached. Retrying in 2s…"}))
	if view := m.View(); !strings.Contains(view, "Retrying in 2s") {
		t.Fatalf("View() = %q, want visible retry wait", view)
	}
	m = apply(t, m, EventMsg(session.SessionEvent{Kind: session.KindTextStarted}))
	for _, entry := range m.entries {
		if entry.kind == entryRetry {
			t.Fatalf("retry status survived provider recovery: %+v", m.entries)
		}
	}
}

type failOnceProvider struct{ calls int }

func (p *failOnceProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.calls++
	if p.calls == 1 {
		return llm.NewFakeProvider(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.StepFailed, Text: "429 Too Many Requests"},
		).Stream(ctx, req)
	}
	return llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "recovered"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	).Stream(ctx, req)
}

func TestModel_RetryReusesFailedTurnWithoutDuplicatingPrompt(t *testing.T) {
	store := session.NewMemoryStore()
	provider := &failOnceProvider{}
	eng := engine.New(engine.Config{Root: t.TempDir(), Provider: provider, Store: store})
	m := NewModel(eng, "s1", eng.Events())
	m = typeRunes(t, m, "hello")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	for m.working {
		m = apply(t, m, nextMsg(t, eng.Events(), 5*time.Second))
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	for m.working {
		m = apply(t, m, nextMsg(t, eng.Events(), 5*time.Second))
	}
	messages, err := store.Messages(context.Background(), "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Text != "hello" || messages[1].Text != "recovered" {
		t.Fatalf("messages = %+v, retry must reuse one user turn and append one answer", messages)
	}
}

// drainReveal applies reveal ticks until the smooth streaming backlog is exhausted: tests whose assertion presupposes the text already revealed use it to not depend on the rhythm of the animation. The iteration limit prevents the test from crashing if the reveal stops progressing.
func drainReveal(t *testing.T, m Model) Model {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if !m.hasBacklog() {
			return m
		}
		m = apply(t, m, revealTickMsg{})
	}
	t.Fatalf("el backlog del reveal no se agoto tras 1000 ticks")
	return m
}

func TestModel_ModelCommandCompletesInlineThenSelects(t *testing.T) {
	agent := &fakeAgent{
		models: []providerconfig.ProviderModels{
			{ID: "openrouter", Name: "OpenRouter", Models: []string{"old", "openai/chatgpt5.5"}},
			{ID: "openai", Name: "OpenAI", Models: []string{"chatgpt5.5"}},
		},
		active: providerconfig.Active{ProviderID: "openrouter", ProviderName: "OpenRouter", Model: "old"},
	}
	m := NewModel(agent, "s1", nil).WithStatus("build", "old")
	m = typeRunes(t, m, "/model chatgpt5.5")
	view := m.View()
	lineWith(t, view, "openai/chatgpt5.5")
	lineWith(t, view, "OpenRouter")
	lineWith(t, view, "OpenAI")
	lineWith(t, view, "chatgpt5.5")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "/model openrouter openai/chatgpt5.5 " {
		t.Fatalf("first Enter completed %q", got)
	}
	m = apply(t, m, ModelsRefreshedMsg{Providers: agent.models})
	if len(m.menuItems) != 0 {
		t.Fatalf("refresh reopened popup over canonical command: %#v", m.menuItems)
	}
	if len(agent.sent) != 0 || len(m.history) != 0 {
		t.Fatalf("/model leaked to prompts/history: sent=%v history=%v", agent.sent, m.history)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.model; got != "openai/chatgpt5.5" {
		t.Fatalf("footer model = %q", got)
	}
	if len(agent.selected) != 1 || agent.selected[0].providerID != "openrouter" || agent.selected[0].model != "openai/chatgpt5.5" {
		t.Fatalf("selected = %#v", agent.selected)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input after selection = %q", got)
	}
}

func TestModel_ModelCommandOpensTwoColumnPicker(t *testing.T) {
	agent := &fakeAgent{
		models: []providerconfig.ProviderModels{
			{ID: "openrouter", Name: "OpenRouter", Models: []string{"old", "openai/chatgpt5.5"}},
			{ID: "openai", Name: "OpenAI", Models: []string{"chatgpt5.5"}},
		},
		active: providerconfig.Active{ProviderID: "openrouter", ProviderName: "OpenRouter", Model: "old"},
	}
	m := NewModel(agent, "s1", nil).WithStatus("build", "old")
	m = typeRunes(t, m, "/model")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := ansi.Strip(m.View())
	for _, want := range []string{"Models", "Providers", "Available models", "OpenRouter", "OpenAI", "openai/chatgpt5.5"} {
		if !strings.Contains(view, want) {
			t.Fatalf("model picker missing %q:\n%s", want, view)
		}
	}
	if len(agent.sent) != 0 || len(m.history) != 0 {
		t.Fatalf("/model leaked to prompts/history: sent=%v history=%v", agent.sent, m.history)
	}
}

func TestModel_ModelPickerSelectsModelFromAnotherProvider(t *testing.T) {
	agent := &fakeAgent{
		models: []providerconfig.ProviderModels{
			{ID: "openrouter", Name: "OpenRouter", Models: []string{"old", "openai/chatgpt5.5"}},
			{ID: "openai", Name: "OpenAI", Models: []string{"chatgpt5.5"}},
		},
		active: providerconfig.Active{ProviderID: "openrouter", ProviderName: "OpenRouter", Model: "old"},
	}
	m := NewModel(agent, "s1", nil).WithStatus("build", "old")
	m = typeRunes(t, m, "/model")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "chatgpt5.5") || strings.Contains(view, "openai/chatgpt5.5") {
		t.Fatalf("right column did not switch to OpenAI models:\n%s", view)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(agent.selected) != 1 || agent.selected[0].providerID != "openai" || agent.selected[0].model != "chatgpt5.5" {
		t.Fatalf("selected = %#v", agent.selected)
	}
	if got := m.model; got != "chatgpt5.5" {
		t.Fatalf("footer model = %q", got)
	}
	if strings.Contains(ansi.Strip(m.View()), "Select model") {
		t.Fatalf("model picker remained open after selection:\n%s", m.View())
	}
}

func TestModel_ModelPickerClickSelectsModel(t *testing.T) {
	agent := &fakeAgent{
		models: []providerconfig.ProviderModels{
			{ID: "openrouter", Name: "OpenRouter", Models: []string{"old", "openai/chatgpt5.5"}},
			{ID: "openai", Name: "OpenAI", Models: []string{"chatgpt5.5"}},
		},
		active: providerconfig.Active{ProviderID: "openrouter", ProviderName: "OpenRouter", Model: "old"},
	}
	m := NewModel(agent, "s1", nil).WithStatus("build", "old")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeRunes(t, m, "/model")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// With 80x24: margin 2 + border 1, providers column of 18 cells and the first row of items at Y=4. Click on the second provider (OpenAI).
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 4, Y: 5})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "chatgpt5.5") || strings.Contains(view, "openai/chatgpt5.5") {
		t.Fatalf("click on provider did not switch the models column:\n%s", view)
	}
	if len(agent.selected) != 0 {
		t.Fatalf("provider click must not select a model, selected = %#v", agent.selected)
	}

	// Click on divider: inert.
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 21, Y: 4})
	if len(agent.selected) != 0 {
		t.Fatalf("divider click must be inert, selected = %#v", agent.selected)
	}

	// Click on the first row of models: confirm as enter.
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 25, Y: 4})
	if len(agent.selected) != 1 || agent.selected[0].providerID != "openai" || agent.selected[0].model != "chatgpt5.5" {
		t.Fatalf("selected = %#v", agent.selected)
	}
	if got := m.model; got != "chatgpt5.5" {
		t.Fatalf("footer model = %q", got)
	}
	if strings.Contains(ansi.Strip(m.View()), "Available models") {
		t.Fatalf("model picker remained open after click selection:\n%s", m.View())
	}
}

func TestModel_ModelPickerFitsTerminal(t *testing.T) {
	models := make([]string, 20)
	for index := range models {
		models[index] = fmt.Sprintf("provider/model-%02d-with-a-long-name", index)
	}
	agent := &fakeAgent{
		models: []providerconfig.ProviderModels{
			{ID: "openrouter", Name: "OpenRouter", Models: models},
			{ID: "openai", Name: "OpenAI", Models: []string{"chatgpt5.5"}},
		},
		active: providerconfig.Active{ProviderID: "openrouter", ProviderName: "OpenRouter", Model: models[0]},
	}
	m := NewModel(agent, "s1", nil).WithStatus("build", models[0])
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 10})
	m = typeRunes(t, m, "/model")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("model picker height = %d, want <= 10:\n%s", lines, view)
	}
	assertNoLineWiderThan(t, view, 60)
}

func TestModel_ModelPickerIndentsEveryColumnLine(t *testing.T) {
	agent := &fakeAgent{
		models: []providerconfig.ProviderModels{
			{ID: "openrouter", Name: "OpenRouter", Models: []string{"model-a"}},
			{ID: "openai", Name: "OpenAI", Models: []string{"gpt-5.6"}},
		},
		active: providerconfig.Active{ProviderID: "openrouter", Model: "model-a"},
	}
	m := NewModel(agent, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 16})
	m = typeRunes(t, m, "/model")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	for _, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if strings.Contains(line, "Providers") && !strings.HasPrefix(line, "  │") {
			t.Fatalf("column body is not aligned with its top border: %q", line)
		}
	}
}

func TestModel_ModelPickerLeavesTopMargin(t *testing.T) {
	agent := &fakeAgent{
		models: []providerconfig.ProviderModels{{ID: "openrouter", Name: "OpenRouter", Models: []string{"model-a"}}},
		active: providerconfig.Active{ProviderID: "openrouter", Model: "model-a"},
	}
	m := NewModel(agent, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 16})
	m = typeRunes(t, m, "/model")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "" || !strings.Contains(lines[1], "Models") {
		t.Fatalf("model picker top lines = %q, want blank line then title", lines[:min(len(lines), 2)])
	}
}

func TestModel_ModelPickerShowsModelCountRightAligned(t *testing.T) {
	agent := &fakeAgent{
		models: []providerconfig.ProviderModels{
			{ID: "openrouter", Name: "OpenRouter", Models: []string{"model-a", "model-b"}},
			{ID: "openai", Name: "OpenAI", Models: []string{"gpt-5.6"}},
		},
		active: providerconfig.Active{ProviderID: "openrouter", Model: "model-a"},
	}
	m := NewModel(agent, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 16})
	m = typeRunes(t, m, "/model")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	line := lineWith(t, ansi.Strip(m.View()), "> OpenRouter")
	firstBorder := strings.Index(line, "│")
	providerBorder := -1
	if firstBorder >= 0 {
		if offset := strings.Index(line[firstBorder+1:], "│"); offset >= 0 {
			providerBorder = firstBorder + 1 + offset
		}
	}
	if firstBorder < 0 || providerBorder <= firstBorder || !strings.HasSuffix(line[firstBorder+1:providerBorder], "2 ") {
		t.Fatalf("provider row does not end with model count: %q", line)
	}
}

func TestModel_ModelPickerUsesSingleBorderedPanel(t *testing.T) {
	agent := &fakeAgent{
		models: []providerconfig.ProviderModels{
			{ID: "openrouter", Name: "OpenRouter", Models: []string{"model-a"}},
			{ID: "openai", Name: "OpenAI", Models: []string{"gpt-5.6"}},
		},
		active: providerconfig.Active{ProviderID: "openrouter", Model: "model-a"},
	}
	m := NewModel(agent, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 16})
	m = typeRunes(t, m, "/model")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if len(lines) < 3 || !strings.Contains(lines[1], "Models") || strings.Count(lines[1], "┌") != 1 {
		t.Fatalf("model picker does not have one titled outer border: %q", lines[:min(len(lines), 3)])
	}
	header := lineWith(t, strings.Join(lines, "\n"), "Available models")
	if !strings.Contains(header, "Providers") || strings.Count(header, "│") < 3 {
		t.Fatalf("model picker header lacks internal columns: %q", header)
	}
}

func TestModel_ModelPickerShowsContextBeforePrice(t *testing.T) {
	agent := &fakeAgent{
		models: []providerconfig.ProviderModels{{
			ID: "openai", Name: "OpenAI", Models: []string{"gpt-4.1"},
			Capabilities: llm.Capabilities{ContextWindows: map[string]int{"gpt-4.1": 1_047_576}},
		}},
		active: providerconfig.Active{ProviderID: "openai", Model: "gpt-4.1"},
	}
	m := NewModel(agent, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 16})
	m = typeRunes(t, m, "/model")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := ansi.Strip(m.View())
	header := lineWith(t, view, "Available models")
	if contextIndex, priceIndex := strings.Index(header, "Context"), strings.Index(header, "Price"); contextIndex < 0 || priceIndex <= contextIndex {
		t.Fatalf("metadata headers are missing or out of order: %q", header)
	}
	line := lineWith(t, view, "gpt-4.1")
	if contextIndex, priceIndex := strings.Index(line, "1.05M"), strings.Index(line, "$2/$8"); contextIndex < 0 || priceIndex <= contextIndex {
		t.Fatalf("model metadata is missing or out of order: %q", line)
	}
}

func TestModel_ModelPopupKeepsDistinctProviderNameAndID(t *testing.T) {
	agent := &fakeAgent{models: []providerconfig.ProviderModels{{ID: "ollama", Name: "Local", Models: []string{"qwen"}}}}
	m := NewModel(agent, "s1", nil)
	m = typeRunes(t, m, "/model qwen")
	if view := m.View(); !strings.Contains(view, "Local · ollama") {
		t.Fatalf("distinct provider identity missing:\n%s", view)
	}
}

func TestModel_OpenRouterCuratedModelsShowContext(t *testing.T) {
	agent := &fakeAgent{models: []providerconfig.ProviderModels{{
		ID: "openrouter", Name: "OpenRouter",
		Models: []string{"tencent/hy3:free", "poolside/laguna-xs-2.1:free", "cohere/north-mini-code:free"},
		Capabilities: llm.Capabilities{ContextWindows: map[string]int{
			"tencent/hy3:free":            262_144,
			"poolside/laguna-xs-2.1:free": 262_144,
			"cohere/north-mini-code:free": 256_000,
		}},
	}}}
	m := NewModel(agent, "s1", nil)
	m = typeRunes(t, m, "/model ")
	view := m.View()
	for _, want := range []string{"tencent/hy3:free", "262K context", "poolside/laguna-xs-2.1:free", "cohere/north-mini-code:free", "256K context"} {
		if !strings.Contains(view, want) {
			t.Fatalf("model popup missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(strings.ToLower(view), "openrouter · openrouter") {
		t.Fatalf("provider should be shown once:\n%s", view)
	}
}

// lineWith returns the first line of view that contains needle, or fails.
func lineWith(t *testing.T, view, needle string) string {
	t.Helper()
	return strings.Split(view, "\n")[lineIndexWith(t, view, needle)]
}

// lineIndexWith returns the index (base 0) of the first line of view that contains needle; The test fails if none contain it.
func lineIndexWith(t *testing.T, view, needle string) int {
	t.Helper()
	for i, line := range strings.Split(view, "\n") {
		if strings.Contains(line, needle) {
			return i
		}
	}
	t.Fatalf("View() = %q, no contiene ninguna linea con %q", view, needle)
	return -1
}

// assertNoLineWiderThan fails if any view line exceeds visible cells width (width of the terminal); Measure with lipgloss.Width to ignore ANSI.
func assertNoLineWiderThan(t *testing.T, view string, width int) {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Fatalf("View() = %q, la linea %q mide %d celdas visibles, ninguna linea debe exceder el ancho de la terminal (%d)", view, line, w, width)
		}
	}
}

// assertBoxLinesExactWidth fails if any composer box line (those that contain a border character ╭/│/╰ after the margin) does not measure exactly the width of visible cells, or if the view does not contain any. Measure with ansi.StringWidth to ignore ANSI codes.
func assertBoxLinesExactWidth(t *testing.T, view string, width int) {
	t.Helper()
	found := false
	for _, line := range strings.Split(view, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		for _, prefix := range []string{"╭", "│", "╰"} {
			if strings.HasPrefix(trimmed, prefix) {
				found = true
				if w := ansi.StringWidth(line); w != width {
					t.Fatalf("View() = %q, la linea de la caja %q mide %d celdas visibles, cada linea de la caja debe medir exactamente el ancho de la terminal (%d)", view, line, w, width)
				}
			}
		}
	}
	if !found {
		t.Fatalf("View() = %q, no contiene ninguna linea de la caja del composer (bordes ╭/│/╰)", view)
	}
}

// forceANSI256Profile sets the ANSI256 color profile during the test: without TTY the profile is Ascii and no color is output, so tests that assert on SGR sequences need it for the colors to be observable. The glamor renderer is memoized in markdownRendererCache keyed only by wrap and remains pinned to the profile with which it was built: it must be invalidated when changing the profile (otherwise the test reuses an Ascii renderer built by another test and passes false) and also when cleaning (otherwise an ANSI256 renderer poisons the following tests).
func forceANSI256Profile(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	markdownRendererCache.renderer = nil
	t.Cleanup(func() {
		lipgloss.SetColorProfile(prev)
		markdownRendererCache.renderer = nil
	})
}

func TestEntry_UserMessageMatchesReferenceWithoutTimestamp(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	view := renderEntry(entry{kind: entryUser, text: "quien eres y que eres capaz de hacer?"}, 80)
	plain := ansi.Strip(view)
	lines := strings.Split(plain, "\n")

	if len(lines) != 3 {
		t.Fatalf("user message lines = %d, want 3:\n%q", len(lines), plain)
	}
	for _, line := range strings.Split(view, "\n") {
		if got := ansi.StringWidth(line); got != 80 {
			t.Fatalf("user message line width = %d, want 80:\n%q", got, view)
		}
	}
	if got, want := lines[1], "     ❯ quien eres y que eres capaz de hacer?"; !strings.HasPrefix(got, want) {
		t.Fatalf("middle line = %q, want prefix %q", got, want)
	}
	if !strings.Contains(view, "\x1b[48;2;36;36;36m") {
		t.Fatalf("user message must use reference background #242424:\n%q", view)
	}
	if strings.Contains(view, "\x1b[1m") {
		t.Fatalf("user message text must not be bold:\n%q", view)
	}
	if strings.Contains(plain, "12:50 AM") {
		t.Fatalf("user message must not render a timestamp:\n%q", plain)
	}
}

func TestModel_UserMessageWrapsInsideReferenceBlock(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 32, Height: 12})
	m = apply(t, m, EventMsg{Message: &session.Message{
		ID:   "u1",
		Role: session.RoleUser,
		Text: "un mensaje suficientemente largo para envolver dentro del bloque",
	}})

	view := m.View()
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 32 {
			t.Fatalf("user message line width = %d, want <= 32:\n%q", width, view)
		}
	}
	if got := strings.Count(view, "\x1b[48;2;36;36;36m"); got < 3 {
		t.Fatalf("reference background rows = %d, want at least 3:\n%q", got, view)
	}
}

func TestModel_UserMessageWrapKeepsMarkerBesideFirstLine(t *testing.T) {
	for width := 16; width <= 80; width++ {
		m := NewModel(nil, "s1", nil)
		m = apply(t, m, tea.WindowSizeMsg{Width: width, Height: 100})
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   "u1",
			Role: session.RoleUser,
			Text: "quiero que hagas commit de los cambios en ingles con\nconventional commit",
		}})

		plain := ansi.Strip(m.View())
		markerHasText := false
		for _, line := range strings.Split(plain, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "❯ ") && trimmed != "❯" {
				markerHasText = true
			}
			if trimmed == "❯" {
				t.Fatalf("width %d user marker rendered alone on its own row:\n%s", width, plain)
			}
		}
		if !markerHasText {
			t.Fatalf("width %d user marker must stay beside the first text row:\n%s", width, plain)
		}
	}
}

func TestModel_UserMessageKeepsGrayBackgroundAfterFaintMarker(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}})

	view := m.View()
	grayText := "\x1b[48;2;36;36;36mhola"
	if !strings.Contains(view, grayText) {
		t.Fatalf("user text must restore #242424 after the faint marker; want %q in:\n%q", grayText, view)
	}
}

func TestModel_FoldsStreamingAssistantText(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "Hola "})
	m = drainReveal(t, m)
	if got := ansi.Strip(m.View()); !strings.Contains(got, "Hola") {
		t.Fatalf("View() sin ANSI = %q, debe contener %q tras el primer delta", got, "Hola")
	}

	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "mundo"})
	m = drainReveal(t, m)
	if got := ansi.Strip(m.View()); !strings.Contains(got, "Hola mundo") {
		t.Fatalf("View() sin ANSI = %q, debe contener %q tras acumular deltas", got, "Hola mundo")
	}

	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "Hola mundo"},
	})
	if got, count := m.View(), strings.Count(m.View(), "Hola mundo"); count != 1 {
		t.Fatalf("View() = %q, %q debe aparecer exactamente una vez (count=%d): cerrar el turno no debe duplicar el bloque en vivo con el Message coalescido", got, "Hola mundo", count)
	}
}

// Assistant render contract: while the block is live (TextStarted/TextDelta only, no StepEnded) the text is displayed flat as it arrives; When the turn closes (StepEnded sets live = false) the text is rendered as markdown: the raw markers (** and "-") disappear and the content is formatted (emphasis applied, lists with bullets).
func TestModel_RendersClosedAssistantAsMarkdown(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	text := "Hola **fuerte** dicho.\n\n- item uno\n- item dos"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "fuerte") {
		t.Fatalf("View() = %q, debe contener %q: rendir markdown no debe perder el contenido", view, "fuerte")
	}
	if strings.Contains(view, "**") {
		t.Fatalf("View() = %q, NO debe contener %q: con el bloque cerrado el enfasis markdown se rinde, no se muestra crudo", view, "**")
	}
	if strings.Contains(view, "- item uno") {
		t.Fatalf("View() = %q, NO debe contener %q: con el bloque cerrado el guion crudo de lista se rinde como bullet", view, "- item uno")
	}
	if !strings.Contains(view, "item uno") {
		t.Fatalf("View() = %q, debe contener %q: rendir la lista no debe perder sus items", view, "item uno")
	}
	if !strings.Contains(view, "item dos") {
		t.Fatalf("View() = %q, debe contener %q: rendir la lista no debe perder sus items", view, "item dos")
	}
	if !strings.Contains(view, "•") {
		t.Fatalf("View() = %q, debe contener %q: los items de lista markdown se rinden con bullet", view, "•")
	}
}

func TestModel_LiveAssistantRendersMarkdownBeforeClosed(t *testing.T) {
	// TRIANGULATE: The renderer must apply Markdown to both the prefix revealed during streaming and the entire content when settling the block.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	text := "esto es **fuerte** en vivo"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = drainReveal(t, m)

	view := ansi.Strip(m.View())
	if strings.Contains(view, "**") {
		t.Fatalf("View() sin ANSI = %q, NO debe contener marcadores Markdown crudos mientras el bloque esta vivo", view)
	}
	if !strings.Contains(view, "fuerte") {
		t.Fatalf("View() sin ANSI = %q, debe contener %q mientras el bloque esta vivo", view, "fuerte")
	}

	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})

	view = ansi.Strip(m.View())
	if strings.Contains(view, "**") {
		t.Fatalf("View() = %q, NO debe contener %q tras cerrar el bloque: al cerrarse el turno el enfasis markdown se rinde, no se muestra crudo", view, "**")
	}
	if !strings.Contains(view, "fuerte") {
		t.Fatalf("View() = %q, debe contener %q: rendir el markdown no debe perder el contenido", view, "fuerte")
	}
}

func TestModel_ClosedMarkdownWrapsToTerminalWidth(t *testing.T) {
	// TRIANGULATE: a renderMarkdown that ignores width (WithWordWrap(0) always), or that passes the full width without discounting the margin of the glamor document, produces lines wider than the terminal. The emergency wrapping of the viewport (ansi.Wrap in syncViewport) re-splits them and leaves orphaned words without margin in column 0. The closed markdown must be wrapped to the width of the terminal by the renderer itself: all the text visible and each line wrapped preserving its margin.
	m := NewModel(nil, "s1", nil)
	// Height 24: glamour v1 wraps this paragraph into more lines than v0.8 and
	// the whole block must stay visible for the per-line margin asserts below.
	m = apply(t, m, tea.WindowSizeMsg{Width: 30, Height: 24})

	// Hyphen-free sentinel: glamour v1 breaks lines at hyphens.
	text := "este parrafo largo con **enfasis** debe envolverse al ancho angosto de la terminal para poder leerse entero hasta el token finmarkdown"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "finmarkdown") {
		t.Fatalf("View() = %q, el final del texto %q debe estar visible: el markdown cerrado debe envolverse al ancho de la terminal, no truncarse", view, "finmarkdown")
	}
	assertNoLineWiderThan(t, view, 30)
	for _, token := range []string{"enfasis", "finmarkdown"} {
		line := ansi.Strip(lineWith(t, view, token))
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("linea con %q = %q, debe conservar el margen del render markdown: una linea rendida mas ancha que la terminal la re-parte el envolvimiento de emergencia del viewport y deja el resto huerfano en la columna 0", token, line)
		}
	}
}

func TestModel_StepEndedMessageRendersAsMarkdown(t *testing.T) {
	// TRIANGULATE: a poor implementation would only yield markdown if there were streaming deltas. When the live block is empty and it is the StepEnded coalesced Message that fills in the text (see foldEvent, KindStepEnded), that text must also be rendered as markdown upon closing.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "- solo item"},
	})

	view := m.View()
	if strings.Contains(view, "- solo item") {
		t.Fatalf("View() = %q, NO debe contener %q: el guion crudo de lista se rinde como bullet aunque el texto llegue por el Message del StepEnded sin deltas previos", view, "- solo item")
	}
	if !strings.Contains(view, "solo item") {
		t.Fatalf("View() = %q, debe contener %q: rendir la lista no debe perder el item", view, "solo item")
	}
	if !strings.Contains(view, "•") {
		t.Fatalf("View() = %q, debe contener %q: el item de lista markdown se rinde con bullet", view, "•")
	}
}

func TestEntryAssistant_RenderRendersRevealedMarkdownWhileLive(t *testing.T) {
	entry := entry{
		kind:     entryAssistant,
		text:     "**Hola** mundo",
		live:     true,
		revealed: len([]rune("**Hola**")),
	}

	rendered := ansi.Strip(renderEntry(entry, 80))
	if strings.Contains(rendered, "**Hola**") {
		t.Fatalf("render(80) = %q, no debe contener el marcador markdown crudo mientras el assistant sigue en vivo", rendered)
	}
	if !strings.Contains(rendered, "Hola") {
		t.Fatalf("render(80) = %q, debe contener el texto markdown ya revelado", rendered)
	}
	if strings.Contains(rendered, "mundo") {
		t.Fatalf("render(80) = %q, no debe revelar el backlog pendiente %q", rendered, "mundo")
	}
}

func TestEntryAssistant_RenderRendersRevealedListWhileLiveAndCompleteListWhenSettled(t *testing.T) {
	entry := entry{
		kind:     entryAssistant,
		text:     "- item visible\n- item pendiente",
		live:     true,
		revealed: len([]rune("- item visible\n")),
	}

	live := ansi.Strip(renderEntry(entry, 80))
	if !strings.Contains(live, "•") || !strings.Contains(live, "item visible") {
		t.Fatalf("render(80) vivo = %q, debe rendir el item revelado como lista Markdown", live)
	}
	if strings.Contains(live, "item pendiente") {
		t.Fatalf("render(80) vivo = %q, no debe filtrar el item pendiente", live)
	}

	entry.live = false
	entry.revealed = len([]rune(entry.text))
	settled := ansi.Strip(renderEntry(entry, 80))
	for _, want := range []string{"•", "item visible", "item pendiente"} {
		if !strings.Contains(settled, want) {
			t.Fatalf("render(80) asentado = %q, debe contener %q", settled, want)
		}
	}
}

// Assistant seated text color contract: Closed markdown is rendered with the terminal's default color, NOT the gray "252" that Document.Color sets to the "dark" glamor style (the text looks dull against the rest of the view). The rest of the theme (colored headings, etc.) is preserved; Only the gray of the document is prohibited here. ANSI256 (forceANSI256Profile) is forced so that gray is observable in the output.
func TestModel_AssistantMarkdownUsesDefaultForeground(t *testing.T) {
	forceANSI256Profile(t)

	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	text := "texto-asentado con **enfasis** del assistant"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})
	m = drainReveal(t, m)

	view := ansi.Strip(m.View())
	if plain := ansi.Strip(view); !strings.Contains(plain, "texto-asentado") {
		t.Fatalf("View() sin ANSI = %q, debe contener %q: quitar el gris del documento no debe perder el contenido del texto", plain, "texto-asentado")
	}
	if strings.Contains(view, "38;5;252") {
		t.Fatalf("View() = %q, NO debe contener la secuencia SGR %q: el texto asentado del assistant se rinde con el color por defecto de la terminal, no con el gris 252 del estilo dark de glamour", view, "38;5;252")
	}
}

func TestModel_ViewPaintsCompleteDarkCanvas(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(previousProfile)
	})

	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 32, Height: 10})

	view := m.View()
	if !strings.Contains(view, "\x1b[48;2;20;20;20m") {
		t.Fatalf("View() = %q, want #141414 true-color background", view)
	}

	lines := strings.Split(ansi.Strip(view), "\n")
	if len(lines) != 10 {
		t.Fatalf("View() has %d lines, want 10", len(lines))
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 32 {
			t.Fatalf("line %d width = %d, want 32", i, got)
		}
	}
}

func TestModel_ViewRestoresDarkCanvasAfterChildStyleResets(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(previousProfile)
	})

	background := "\x1b[48;2;20;20;20m"
	leadingANSI := func(value string) string {
		var prefix strings.Builder
		for strings.HasPrefix(value, "\x1b[") {
			end := -1
			for i := 2; i < len(value); i++ {
				if value[i] >= 0x40 && value[i] <= 0x7e {
					end = i + 1
					break
				}
			}
			if end < 0 {
				break
			}
			prefix.WriteString(value[:end])
			value = value[end:]
		}
		return prefix.String()
	}
	tests := []struct {
		name  string
		model func() Model
	}{
		{name: "chat", model: func() Model { return NewModel(nil, "s1", nil) }},
		{name: "explorer", model: func() Model {
			m := NewModel(nil, "s1", nil)
			m.treeOpen = true
			return m
		}},
		{name: "file viewer", model: func() Model {
			m := NewModel(nil, "s1", nil)
			m.viewer = openFileViewer("example.go", []byte("package example\n"))
			return m
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := apply(t, tt.model(), tea.WindowSizeMsg{Width: 40, Height: 12})
			for lineNumber, line := range strings.Split(m.View(), "\n") {
				for _, reset := range []string{"\x1b[0m", "\x1b[m"} {
					remaining := line
					for {
						_, after, found := strings.Cut(remaining, reset)
						if !found {
							break
						}
						if ansi.Strip(after) != "" && !strings.Contains(leadingANSI(after), background) {
							t.Fatalf("line %d = %q, reset %q must restore the canvas background before rendering more cells", lineNumber+1, line, reset)
						}
						remaining = after
					}
				}
			}
		})
	}
}

func TestModel_ViewPaintsDarkCanvasAcrossLayouts(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(previousProfile)
	})

	tests := []struct {
		name  string
		model func() Model
	}{
		{
			name: "chat",
			model: func() Model {
				return NewModel(nil, "s1", nil)
			},
		},
		{
			name: "explorer",
			model: func() Model {
				m := NewModel(nil, "s1", nil)
				m.treeOpen = true
				return m
			},
		},
		{
			name: "file viewer",
			model: func() Model {
				m := NewModel(nil, "s1", nil)
				m.viewer = openFileViewer("example.go", []byte("package example\n"))
				return m
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := apply(t, tt.model(), tea.WindowSizeMsg{Width: 40, Height: 12})
			view := m.View()
			if !strings.Contains(view, "\x1b[48;2;20;20;20m") {
				t.Fatalf("View() = %q, want #141414 true-color background", view)
			}

			lines := strings.Split(ansi.Strip(view), "\n")
			if len(lines) != 12 {
				t.Fatalf("View() has %d lines, want 12", len(lines))
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got != 40 {
					t.Fatalf("line %d width = %d, want 40", i, got)
				}
			}
		})
	}
}

func TestModel_ViewDarkCanvasWithoutWindowSizeDoesNotPad(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(previousProfile)
	})

	view := NewModel(nil, "s1", nil).View()
	if view == "" {
		t.Fatal("View() must remain non-empty before the first WindowSizeMsg")
	}
	if !strings.Contains(view, "\x1b[48;2;20;20;20m") {
		t.Fatalf("View() = %q, want #141414 true-color background", view)
	}
	for i, line := range strings.Split(ansi.Strip(view), "\n") {
		if got := lipgloss.Width(line); got >= 80 {
			t.Fatalf("line %d width = %d, unknown-size view must not assume an 80-column terminal", i, got)
		}
	}
}

func TestModel_AssistantMarkdownKeepsOwnThemeAccents(t *testing.T) {
	// TRIANGULATE: a poor implementation "themes" the markdown by falling back
	// to the notty/ascii style or stripping ALL colors: the noise goes away
	// but so do the accents. A settled markdown H1 must still render the TUI
	// accent (color "6" + bold -> SGR 36;1 in ANSI256) while the stock
	// dark-theme colors (document gray 252, heading blue 39) stay gone.
	forceANSI256Profile(t)

	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	text := "# Titulo\n\ntexto"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})
	m = drainReveal(t, m)

	view := m.View()
	if plain := ansi.Strip(view); !strings.Contains(plain, "Titulo") {
		t.Fatalf("View() without ANSI = %q, must contain %q: theming must not lose the heading content", plain, "Titulo")
	}
	if !strings.Contains(view, "36;1") {
		t.Fatalf("View() = %q, must contain the SGR sequence %q: a settled markdown H1 renders the TUI accent (bold color 6); stripping all colors is not a theme", view, "36;1")
	}
	for _, stock := range []string{"38;5;252", "38;5;39"} {
		if strings.Contains(view, stock) {
			t.Fatalf("View() = %q, must NOT contain the stock dark-theme SGR sequence %q: the TUI ships its own markdown theme", view, stock)
		}
	}
}

func TestModel_RendersUserMessages(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// The user's message arrives WITHOUT Kind: the runner promotes the prompt as SessionEvent{Message: {Role: user}}.
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola atenea"}})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "hola humano"})
	m = drainReveal(t, m)

	view := m.View()
	userLine := lineWith(t, ansi.Strip(view), "hola atenea")
	if !strings.HasPrefix(userLine, "     ❯ ") {
		t.Fatalf("linea del usuario = %q, debe llevar el marcador %q y la sangria visual de referencia", userLine, "     ❯ ")
	}
	assistantLine := lineWith(t, ansi.Strip(view), "hola humano")
	if strings.Contains(assistantLine, "❯ ") {
		t.Fatalf("linea del assistant = %q, NO debe llevar el marcador de usuario %q", assistantLine, "❯ ")
	}
}

func TestModel_RendersToolCallLifecycle(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	if got := m.View(); !strings.Contains(got, "● Bash     ls") {
		t.Fatalf("View() = %q, Tool.Called debe mostrar el ToolName con el resumen del Input y el marcador de ejecucion %q", got, "● Bash     ls")
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "archivo.txt",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "archivo.txt", ToolCallID: "c1"},
	})
	if got := m.View(); !strings.Contains(got, "✓ Bash     ls") {
		t.Fatalf("View() = %q, Tool.Success debe asentar la tool como %q", got, "✓ Bash     ls")
	}
	if got := m.View(); strings.Contains(got, "●") {
		t.Fatalf("View() = %q, la tool asentada no debe seguir mostrandose como en ejecucion", got)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "edit", Input: json.RawMessage(`{"patch":"[a.go#ab12]\n"}`)})
	if got := m.View(); !strings.Contains(got, "● Edit     a.go") {
		t.Fatalf("View() = %q, el segundo tool call debe mostrarse en ejecucion con el archivo del patch %q", got, "● Edit     a.go")
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c2", ToolName: "edit", Error: "permiso denegado"})
	got := m.View()
	for _, want := range []string{"✗ Edit     a.go", "│ error: permiso denegado"} {
		if !strings.Contains(got, want) {
			t.Fatalf("View() = %q, Tool.Failed debe mostrar %q: el header con el resumen del Input y el Error como linea de rail", got, want)
		}
	}
	if !strings.Contains(got, "✓ Bash     ls") {
		t.Fatalf("View() = %q, el fallo de c2 no debe tocar el estado ok de c1", got)
	}
	if strings.Contains(got, "●") {
		t.Fatalf("View() = %q, no debe quedar ninguna tool en ejecucion", got)
	}
}

// Contract for the "task" tool render: the header reads `SubAgent <type>`
// (the subagent_type field of the Input, never the raw JSON) and, while the
// subagent runs, the spinner tick animates the run marker with the live
// spinner frame instead of the static one. Success settles it as `✓`.
func TestModel_RendersTaskToolAsSubAgentWithSpinner(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "task", Input: json.RawMessage(`{"subagent_type":"explore","prompt":"find the config loader"}`)})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "● SubAgent explore") {
		t.Fatalf("View() = %q, a running task must render as %q (SubAgent + subagent_type)", view, "● SubAgent explore")
	}
	if strings.Contains(view, `{"subagent_type"`) {
		t.Fatalf("View() = %q, the raw Input JSON must not leak into the header", view)
	}

	m.working = true
	m = apply(t, m, spinner.TickMsg{})
	frame := ansi.Strip(m.spinner.View())
	view = ansi.Strip(m.View())
	if !strings.Contains(view, frame+" SubAgent explore") {
		t.Fatalf("View() = %q, a running task must animate its marker with the spinner frame %q", view, frame)
	}
	if strings.Contains(view, "● SubAgent") {
		t.Fatalf("View() = %q, the spinner frame must replace the static run marker", view)
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "task", Text: "subagent report",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "subagent report", ToolCallID: "c1"},
	})
	if got := ansi.Strip(m.View()); !strings.Contains(got, "✓ SubAgent explore") {
		t.Fatalf("View() = %q, a finished task must settle as %q", got, "✓ SubAgent explore")
	}
}

// Tool "skill" render contract: uses the activity grammar with the name of the skill as a summary (`● Skill <name>`), where the name is the "name" field of the Input JSON, without filtering the raw Input to the header. On success, the header goes WITHOUT a preview of the output: the body of the SKILL.md that travels in ev.Text is for the model, not for the transcript.
func TestModel_RendersSkillToolAsSkillLine(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "skill", Input: json.RawMessage(`{"name":"code-review"}`)})
	view := m.View()
	if !strings.Contains(view, "● Skill    code-review") {
		t.Fatalf("View() = %q, la tool skill en ejecucion debe rendirse como linea dedicada %q (nombre = campo name del Input)", view, "● Skill    code-review")
	}
	if strings.Contains(view, `{"name"`) {
		t.Fatalf("View() = %q, NO debe filtrar el Input crudo al header: la linea dedicada lleva el nombre pelado como resumen", view)
	}

	body := "<skill_content name=\"code-review\">\ncuerpo del skill para el modelo\n</skill_content>"
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "skill", Text: body,
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: body, ToolCallID: "c1"},
	})
	view = m.View()
	if !strings.Contains(view, "✓ Skill    code-review") {
		t.Fatalf("View() = %q, la tool skill exitosa debe asentarse como %q", view, "✓ Skill    code-review")
	}
	if strings.Contains(view, "skill_content") {
		t.Fatalf("View() = %q, NO debe contener %q: en exito la linea de skill va sin preview del output, el cuerpo del SKILL.md es para el modelo y no para el transcript", view, "skill_content")
	}
}

func TestModel_SkillToolFailureShowsError(t *testing.T) {
	// TRIANGULATE: a poor implementation of renderSkill only covers the running/ok states and before Tool.Failed it leaves the ● marker forever. The skill failure (e.g. nonexistent name) sits on the same dedicated line with the ✗ marker and the error as a rail line, just like the rest of the tools.
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "skill", Input: json.RawMessage(`{"name":"inexistente"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c1", ToolName: "skill", Error: `skill "inexistente" no encontrada`})

	view := m.View()
	for _, want := range []string{"✗ Skill    inexistente", `│ error: skill "inexistente" no encontrada`} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, la skill fallida debe asentarse como %q: la linea dedicada tambien cubre el estado de error, no solo running/ok", view, want)
		}
	}
	if strings.Contains(view, "●") {
		t.Fatalf("View() = %q, la skill asentada con error no debe seguir mostrandose como en ejecucion", view)
	}
}

func TestModel_SkillToolWithoutNameRendersBareHeader(t *testing.T) {
	// TRIANGULATE: a poor implementation assumes that the Input of the skill is valid JSON (panic or garbage in the header when parsing it) when it cannot extract the name. With non-parseable Input the header is "● Skill" stripped: without summary, without dangling spaces in the alignment and without filtering the raw input to the transcript.
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "skill", Input: json.RawMessage(`no-es-json`)})

	view := m.View()
	if !strings.Contains(view, "● Skill") {
		t.Fatalf("View() = %q, con Input no parseable la skill debe rendirse con el header pelado %q", view, "● Skill")
	}
	skillLine := lineWith(t, view, "● Skill")
	if got := strings.TrimRight(skillLine, " "); got != "  ● Skill" {
		t.Fatalf("linea de la skill = %q, el header pelado no lleva resumen: queda %q sin heredar nada del Input", skillLine, "  ● Skill")
	}
	if strings.Contains(view, "no-es-json") {
		t.Fatalf("View() = %q, NO debe filtrar el Input crudo %q al transcript", view, "no-es-json")
	}
}

func TestModel_ReadToolShowsOnlyStatusAndFileName(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"internal/tui/view.go:20-40"}`)})

	view := ansi.Strip(m.View())
	if want := "  ● Reading  view.go"; !strings.Contains(view, want) {
		t.Fatalf("View() sin ANSI = %q, read en ejecucion debe mostrar solo %q", view, want)
	}
	if strings.Contains(view, "internal/tui") || strings.Contains(view, ":20-40") {
		t.Fatalf("View() sin ANSI = %q, read no debe mostrar la ruta ni el selector", view)
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "read", Text: "[internal/tui/view.go#ABCD]\n20:package tui",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "[internal/tui/view.go#ABCD]\n20:package tui", ToolCallID: "c1"},
	})

	view = ansi.Strip(m.View())
	if want := "  ✓ Read     view.go"; !strings.Contains(view, want) {
		t.Fatalf("View() sin ANSI = %q, read exitoso debe mostrar solo %q", view, want)
	}
	for _, hidden := range []string{"Reading", "internal/tui", "20:package tui", "ABCD"} {
		if strings.Contains(view, hidden) {
			t.Fatalf("View() sin ANSI = %q, read exitoso no debe mostrar %q", view, hidden)
		}
	}
}

// Tool call detail contract: the header carries the summary of the Input (`✓ <name> <summary>`; with a single string field the summary is its value) and Tool.Success brings the output in ev.Text, which is displayed under the header with each line of rail `│ ` up to 4 lines; with more lines a final mark appears `│ … +N lines`. With 3 output lines they all fit: no truncation mark should appear.
func TestModel_ToolSuccessShowsOutputPreview(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls -la"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "uno\ndos\ntres",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "uno\ndos\ntres", ToolCallID: "c1"},
	})

	view := m.View()
	if !strings.Contains(view, "✓ Bash     ls -la") {
		t.Fatalf("View() = %q, el header debe llevar el resumen del Input %q: con un solo campo string el resumen es su valor", view, "✓ Bash     ls -la")
	}
	for _, want := range []string{"│ uno", "│ dos", "│ tres"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, debe contener %q: cada linea del output de Tool.Success se muestra bajo el header prefijada con la barra", view, want)
		}
	}
	if strings.Contains(view, "lines") {
		t.Fatalf("View() = %q, NO debe contener la marca de truncado %q: 3 lineas de output caben en el tope de 4 y se muestran completas", view, "lines")
	}
}

// Edit success renders the rich diff card instead of the generic activity line: a file-path bar, the "@@ … @@" hunk header, then the removed side ("before", red) above the added side ("after", green), each line numbered with its real file line. The output preview ("ok") is dropped: the diff IS the result worth reviewing.
func TestModel_ToolSuccessShowsEditDiff(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "edit", Input: json.RawMessage(`{"path":"a.go"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "edit", Text: "ok",
		Diff:    "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-viejo\n+nuevo",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	for _, want := range []string{"a.go", "@@ -1 +1 @@", "1 - viejo", "1 + nuevo"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("View() sin ANSI = %q, debe contener %q: la edit exitosa se rinde como la tarjeta de diff con ruta, hunk y bloques antes/después", plain, want)
		}
	}
	// The red block ("before", removed) goes above the green ("after", added).
	if i, j := strings.Index(plain, "1 - viejo"), strings.Index(plain, "1 + nuevo"); i < 0 || j < 0 || i > j {
		t.Fatalf("View() sin ANSI = %q, el bloque de quitadas debe ir antes que el de agregadas", plain)
	}
	// The card is inserted like the rest of the content: the row opens with the margin (activityInset) and the rail ▌ in the same column as "✓ Read".
	if row := lineWith(t, plain, "1 - viejo"); !strings.HasPrefix(row, activityInset+"▌") {
		t.Fatalf("fila del diff = %q, debe abrir con el margen %q y el rail ▌", row, activityInset)
	}
	// Neither the old unified preview rail nor the output preview survive.
	for _, banned := range []string{"│ -viejo", "│ +nuevo", "│ ok"} {
		if strings.Contains(plain, banned) {
			t.Fatalf("View() sin ANSI = %q, NO debe contener %q: la tarjeta reemplaza al preview unificado y al del output", plain, banned)
		}
	}
}

// The before/after split repeats every context line in BOTH blocks, in gray:
// the red block is the whole old slice of the hunk (context + removed) and the
// green block the whole new slice (context + added), each numbered with its
// side's real file line.
func TestModel_EditDiffShowsContextInBothBlocks(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "edit", Input: json.RawMessage(`{"path":"main.go"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "edit", Text: "ok",
		Diff:    "--- a/main.go\n+++ b/main.go\n@@ -10,3 +10,3 @@\n ctxA\n-old\n+new\n ctxB",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	// The removed one has the number of the old file and the added one has the number of the new one.
	for _, want := range []string{"11 - old", "11 + new"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("View() sin ANSI = %q, debe contener %q con el numero de linea real", plain, want)
		}
	}
	// Each line of context appears in the red block AND in the green block.
	for _, ctx := range []string{"ctxA", "ctxB"} {
		if got := strings.Count(plain, ctx); got < 2 {
			t.Fatalf("View() sin ANSI = %q, la linea de contexto %q debe aparecer en ambos bloques (antes/después), got %d", plain, ctx, got)
		}
	}
}

// TRIANGULATE: drops a preview of the output without a cap, which would dump the entire 6 lines into the transcript instead of cutting into 4 and summarizing the rest at the mark.
func TestModel_ToolOutputPreviewTruncatesLongOutput(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"cat f"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "l1\nl2\nl3\nl4\nl5\nl6",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "l1\nl2\nl3\nl4\nl5\nl6", ToolCallID: "c1"},
	})

	view := m.View()
	for _, want := range []string{"│ l1", "│ l2", "│ l3", "│ l4"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, debe contener %q: las primeras 4 lineas del output se muestran bajo el header", view, want)
		}
	}
	if !strings.Contains(view, "+2 lines") {
		t.Fatalf("View() = %q, debe contener la marca %q: las 2 lineas que exceden el tope se resumen", view, "+2 lines")
	}
	for _, banned := range []string{"│ l5", "│ l6"} {
		if strings.Contains(view, banned) {
			t.Fatalf("View() = %q, NO debe contener %q: el preview corta en el tope de 4 lineas", view, banned)
		}
	}
}

// The generic summary of the Input is the fallback for a tool that does NOT say how to present itself (tool.Presenter): those of an MCP server, and any that this build does not know. That is why the subject is tested with a tool outside the registry.  TRIANGULATE: returns a summary of the Input that returns the entire input without truncating, or that with several fields chooses a single field instead of the complete compact JSON.
func TestModel_ToolInputSummaryCompactsMultiField(t *testing.T) {
	const remote = "mcp_editor_patch"
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 200, Height: 24})

	// Two fields: The summary is the compact JSON, not the value of a single field.
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: remote, Input: json.RawMessage(`{"path":"a.go","texto":"x"}`)})
	view := m.View()
	if want := `● ` + remote + ` {"path":"a.go","texto":"x"}`; !strings.Contains(view, want) {
		t.Fatalf("View() = %q, el header debe contener %q: con varios campos el resumen es el JSON compacto", view, want)
	}

	// A single string field longer than the maximum of 48 cells: the summary is truncated with the ellipsis and the tail of the input does not appear.
	long := strings.Repeat("x", 60) + "-cola-final"
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "bash", Input: json.RawMessage(`{"command":"` + long + `"}`)})
	view = m.View()
	if !strings.Contains(view, "…") {
		t.Fatalf("View() = %q, debe contener la elipsis %q: un input mas largo que el tope se trunca en el header", view, "…")
	}
	if strings.Contains(view, "cola-final") {
		t.Fatalf("View() = %q, NO debe contener %q: la cola de un input largo queda fuera del resumen truncado", view, "cola-final")
	}
}

func TestModel_ShowsPendingPermissionAndClearsOnOutcome(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	// Runner's actual order: Tool.Called and then Tool.Permission.Requested while blocking in the gate (ask-before-run).
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"rm -rf /tmp/x"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"rm -rf /tmp/x"}`)})

	view := m.View()
	permLine := lineWith(t, view, "? Bash")
	for _, want := range []string{"? Bash", "rm -rf /tmp/x"} {
		if !strings.Contains(permLine, want) {
			t.Fatalf("solicitud pendiente = %q, debe contener %q (marcador ?, ToolName y resumen del Input)", permLine, want)
		}
	}
	if view := ansi.Strip(view); !strings.Contains(view, "Permission required") || !strings.Contains(view, "Deny") || !strings.Contains(view, "Allow") {
		t.Fatalf("View() = %q, el panel inline debe contener titulo y acciones", view)
	}
	if callID, ok := m.PendingPermission(); !ok || callID != "c1" {
		t.Fatalf("PendingPermission() = (%q, %v), debe exponer la solicitud pendiente c1", callID, ok)
	}

	// The outcome arrives as Tool.Success of the SAME CallID: the request disappears.
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "hecho",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "hecho", ToolCallID: "c1"},
	})
	if got := m.View(); strings.Contains(got, "Permission required") {
		t.Fatalf("View() = %q, Tool.Success de c1 debe retirar la solicitud pendiente", got)
	}
	if callID, ok := m.PendingPermission(); ok {
		t.Fatalf("PendingPermission() = (%q, %v), no debe quedar solicitud tras el desenlace", callID, ok)
	}

	// Tool.Failed also resolves the request (e.g. denied by the user).
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"b.go"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"b.go"}`)})
	if callID, ok := m.PendingPermission(); !ok || callID != "c2" {
		t.Fatalf("PendingPermission() = (%q, %v), debe exponer la solicitud pendiente c2", callID, ok)
	}
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c2", ToolName: "write", Error: "denegada por el usuario"})
	if got := m.View(); strings.Contains(got, "(aprobar/denegar)") {
		t.Fatalf("View() = %q, Tool.Failed de c2 debe retirar la solicitud pendiente", got)
	}
	if callID, ok := m.PendingPermission(); ok {
		t.Fatalf("PendingPermission() = (%q, %v), no debe quedar solicitud tras Tool.Failed", callID, ok)
	}
}

// A tool that blocks on the ask-before-run gate emits Tool.Called (running "●")
// immediately followed by Tool.Permission.Requested ("?") for the same call.
// While the gate is open the transcript must show only the naranja "? <tool>"
// ask, never a duplicate running header for that call. Approving keeps the tool
// running, so its "●" header returns once the ask is gone.
func TestModel_RunningToolHiddenWhilePermissionPending(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})

	transcript := ansi.Strip(m.renderTranscript())
	if strings.Contains(transcript, "● Bash") {
		t.Fatalf("renderTranscript() = %q, the running header must be hidden while its permission is pending", transcript)
	}
	if !strings.Contains(transcript, "? Bash") {
		t.Fatalf("renderTranscript() = %q, the pending permission ask must stay visible", transcript)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	transcript = ansi.Strip(m.renderTranscript())
	if !strings.Contains(transcript, "● Bash") || strings.Contains(transcript, "? Bash") {
		t.Fatalf("renderTranscript() = %q, approving must reveal the running header and drop the ask", transcript)
	}
	if len(fake.resolved) != 1 || fake.resolved[0].callID != "c1" || !fake.resolved[0].approved() {
		t.Fatalf("resolved = %+v, want c1 approved", fake.resolved)
	}
}

// A pending permission blocks on the user, so the agent is not working: the
// "working" status line must disappear while the ask is open and return once it
// is resolved (the run keeps going). "Working directory" in the panel is capital
// W, so the lowercase " working" check does not collide with it.
func TestModel_WorkingLineHiddenWhilePermissionPending(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithWorkspace("main", "~/dev/atenea")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.working = true
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	if got := m.View(); strings.Contains(got, " working") {
		t.Fatalf("View() = %q, the working line must be hidden while a permission is pending", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if got := m.View(); !strings.Contains(got, " working") {
		t.Fatalf("View() = %q, resolving restores the working line while the run continues", got)
	}
}

func TestModel_ShowsStepFailedError(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindStepFailed, Error: "contexto agotado: limite de tokens"})

	view := m.View()
	errLine := lineWith(t, view, "contexto agotado: limite de tokens")
	if !strings.Contains(errLine, "✗ error") {
		t.Fatalf("linea del fallo = %q, debe llevar el marcador %q para distinguirse del texto normal", errLine, "✗ error")
	}
}

// Contract of the visual hierarchy of activity: the header of each tool carries a status marker with two columns of margin (`●` running, `✓` success, `✗` failure), the name of the tool aligned to 8 columns (`%-8s`) and the summary of the Input (` ● Bash ls`); The detail goes below as rail lines with the same margin (` │ 18 matches`, ` │ error: exit 1`). The old `[tool] ...` format disappears from the transcript.
func TestModel_RendersActivityMarkersThroughToolLifecycle(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	plain := ansi.Strip(m.View())
	if want := "  ● Bash     ls"; !strings.Contains(plain, want) {
		t.Fatalf("View() sin ANSI = %q, la tool en ejecucion debe rendirse como %q: dos columnas de margen, marcador ●, nombre alineado a 8 columnas y resumen del Input", plain, want)
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "18 matches",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "18 matches", ToolCallID: "c1"},
	})
	plain = ansi.Strip(m.View())
	if want := "  ✓ Bash     ls"; !strings.Contains(plain, want) {
		t.Fatalf("View() sin ANSI = %q, la tool exitosa debe asentarse como %q: el marcador ✓ reemplaza al ● en la misma columna", plain, want)
	}
	railLine := lineWith(t, plain, "18 matches")
	if want := "  │ 18 matches"; !strings.HasPrefix(railLine, want) {
		t.Fatalf("linea del output = %q, debe llevar el rail con el mismo margen como %q", railLine, want)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "bash", Input: json.RawMessage(`{"command":"false"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c2", ToolName: "bash", Error: "exit 1"})
	plain = ansi.Strip(m.View())
	if want := "  ✗ Bash     false"; !strings.Contains(plain, want) {
		t.Fatalf("View() sin ANSI = %q, la tool fallida debe asentarse como %q: marcador ✗ con la misma columna de nombre", plain, want)
	}
	failLine := lineWith(t, plain, "error: exit 1")
	if want := "  │ error: exit 1"; !strings.HasPrefix(failLine, want) {
		t.Fatalf("linea del fallo = %q, el error de la tool va debajo del header como linea de rail %q, no pegado al header", failLine, want)
	}
	if strings.Contains(plain, "[tool]") {
		t.Fatalf("View() sin ANSI = %q, NO debe contener el formato viejo %q: los marcadores de estado lo reemplazan", plain, "[tool]")
	}
}

// Compact grouping contract: adjacent activity entries (tools, permissions, step errors) are joined WITHOUT a blank line between them (separator "\n"), while the assistant narrative retains its own paragraph ("\n\n") and breaks the group: two consecutive tools remain on physically contiguous lines and the narrative is surrounded by blank lines.
func TestModel_GroupsAdjacentActivityEntriesWithoutBlankLine(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, ToolCallID: "c1"},
	})
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "grep", Input: json.RawMessage(`{"pattern":"foo"}`)})

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "listo el analisis"})
	m = drainReveal(t, m)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c3", ToolName: "bash", Input: json.RawMessage(`{"command":"pwd"}`)})

	plain := ansi.Strip(m.View())
	if want := "  ✓ Bash     ls\n  ● Grep     foo"; !strings.Contains(plain, want) {
		t.Fatalf("View() sin ANSI = %q, debe contener %q: dos entradas de actividad adyacentes se agrupan en lineas fisicamente contiguas, sin linea en blanco entre si", plain, want)
	}

	lines := strings.Split(plain, "\n")
	narrIdx := lineIndexWith(t, plain, "listo el analisis")
	if narrIdx == 0 || strings.TrimSpace(lines[narrIdx-1]) != "" {
		t.Fatalf("linea previa a la narrativa = %q, la narrativa del assistant rompe el grupo de actividad: se separa con linea en blanco", lines[narrIdx-1])
	}
	toolIdx := lineIndexWith(t, plain, "pwd")
	if toolIdx == 0 || strings.TrimSpace(lines[toolIdx-1]) != "" {
		t.Fatalf("linea previa a la tool posterior a la narrativa = %q, la actividad tras la narrativa abre grupo nuevo separado por linea en blanco", lines[toolIdx-1])
	}
}

// The "+N -M" stat rides on the hunk header bar (not on a "✓ Edit" line): it
// counts the +/- content lines of that hunk, excluding the +++/--- file
// headers. The changed lines render as numbered rows in the before/after
// blocks, no longer with the "│ " rail of the old unified preview.
func TestModel_EditSuccessShowsDiffStatInHeader(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "edit", Input: json.RawMessage(`{"path":"main.go"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "edit", Text: "ok",
		Diff:    "--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,3 @@\n-vieja\n+nueva\n+extra",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	hunk := lineWith(t, plain, "@@ -1,2 +1,3 @@")
	for _, want := range []string{"@@ -1,2 +1,3 @@", "+2 -1"} {
		if !strings.Contains(hunk, want) {
			t.Fatalf("linea del hunk = %q, debe contener %q: el stat +N -M va en la barra del hunk", hunk, want)
		}
	}
	if strings.Contains(plain, "✓ Edit") {
		t.Fatalf("View() sin ANSI = %q, NO debe contener %q: la tarjeta reemplaza la linea de actividad", plain, "✓ Edit")
	}
	// The changed lines go as numbered rows in the blocks, without the old rail.
	for _, want := range []string{"1 - vieja", "1 + nueva", "2 + extra"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("View() sin ANSI = %q, debe contener la fila %q", plain, want)
		}
	}
	if strings.Contains(plain, "│ +nueva") {
		t.Fatalf("View() sin ANSI = %q, NO debe contener el rail viejo %q", plain, "│ +nueva")
	}
}

// A pure insertion (no removed lines) omits the red "before" block entirely: there is no old slice to show, so only the green "after" block renders.
func TestModel_EditDiffOmitsEmptyRemovedBlock(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "edit", Input: json.RawMessage(`{"path":"main.go"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "edit", Text: "ok",
		Diff:    "--- a/main.go\n+++ b/main.go\n@@ -0,0 +1,2 @@\n+line1\n+line2",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	for _, want := range []string{"1 + line1", "2 + line2"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("View() sin ANSI = %q, debe contener la fila agregada %q", plain, want)
		}
	}
	if strings.Contains(plain, " - ") {
		t.Fatalf("View() sin ANSI = %q, una insercion pura no debe emitir ninguna fila quitada", plain)
	}
}

// A successful write renders as a diff card sibling to the edit card, but in a
// single neutral gray: the file-path bar and every written line on the gray
// band, numbered, with NO hunk bar, NO "+N -M" stat, and NO +/- marker. A write
// always creates a brand-new file, so there is never a removed side to show.
func TestModel_ToolSuccessShowsWriteCard(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "write", Input: json.RawMessage(`{"path":"nuevo.go","content":"package main"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "write", Text: "ok",
		Diff:    "--- a/nuevo.go\n+++ b/nuevo.go\n@@ -0,0 +1,2 @@\n+package main\n+// hola",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	// The route and each line are numbered but WITHOUT a + marker.
	for _, want := range []string{"nuevo.go", "1  package main", "2  // hola"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("View() sin ANSI = %q, debe contener %q: el write exitoso se rinde como tarjeta gris con ruta y lineas numeradas sin marcador", plain, want)
		}
	}
	// The card replaces the line of activity.
	if strings.Contains(plain, "✓ Write") {
		t.Fatalf("View() sin ANSI = %q, NO debe contener %q: la tarjeta reemplaza la linea de actividad", plain, "✓ Write")
	}
	// No hunk bar, no stat +N -M and no +/- marker on rows.
	for _, banned := range []string{"@@", "+2 -0", "1 + package main", " - "} {
		if strings.Contains(plain, banned) {
			t.Fatalf("View() sin ANSI = %q, NO debe contener %q: el write no muestra hunk, stat ni marcador de diff", plain, banned)
		}
	}
	// The row opens with the margin and the rail ▌ in the same column as the rest.
	if row := lineWith(t, plain, "1  package main"); !strings.HasPrefix(row, activityInset+"▌") {
		t.Fatalf("fila del write = %q, debe abrir con el margen %q y el rail ▌", row, activityInset)
	}
}

// A write longer than editDiffCardMaxRows collapses the overflow into a
// "… +N lines" tail, matching the edit card, so a big new file never floods
// the transcript.
func TestModel_WriteCardTruncatesLongFile(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// 60 lines: with the route bar there are 61 rows, more than the limit of 40.
	var b strings.Builder
	b.WriteString("--- a/big.go\n+++ b/big.go\n@@ -0,0 +1,60 @@\n")
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&b, "+linea-%02d\n", i)
	}
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "write", Input: json.RawMessage(`{"path":"big.go"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "write", Text: "ok",
		Diff:    b.String(),
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	// path (1) + 39 rows = 40 (the top); Line-39 is the last one that enters and 60-39 = 21 lines are hidden in the summary mark.
	if !strings.Contains(plain, "linea-39") {
		t.Fatalf("View() sin ANSI = %q, la ultima linea dentro del tope debe mostrarse", plain)
	}
	if !strings.Contains(plain, "… +21 lines") {
		t.Fatalf("View() sin ANSI = %q, debe resumir el exceso como %q", plain, "… +21 lines")
	}
	// The first line beyond the top (and the following ones) do not appear.
	for _, banned := range []string{"linea-40", "linea-60"} {
		if strings.Contains(plain, banned) {
			t.Fatalf("View() sin ANSI = %q, NO debe contener %q: se corta en el tope", plain, banned)
		}
	}
}

// An empty-file write yields an empty diff, so it keeps the generic activity
// line instead of an empty card: there is nothing to show on the band.
func TestModel_WriteWithoutDiffShowsActivityLine(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "write", Input: json.RawMessage(`{"path":"vacio.go","content":""}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "write", Text: "ok",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "✓ Write") {
		t.Fatalf("View() sin ANSI = %q, un write sin diff conserva la linea de actividad", plain)
	}
}

// TRIANGULATE: destroy a header that truncates the name of the tool to the width of the alignment column (8) or that is too long: with a name longer than the column, the name remains integer and the summary is ONE space in the name.
func TestModel_ActivityHeaderKeepsLongToolNameReadable(t *testing.T) {
	// A name longer than the column only appears in a tool that does not say how to present itself: the tools themselves have a short label (Bash, Edit, SubAgent).
	const remote = "mcp_planner_present_plan"
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: remote, Input: json.RawMessage(`{"plan":"migrar el runner"}`)})

	plain := ansi.Strip(m.View())
	line := lineWith(t, plain, remote)
	if want := "  ● " + remote + " migrar el runner"; line != want {
		t.Fatalf("header = %q, want exactamente %q: un nombre mas largo que la columna de 8 no se trunca y el resumen queda a UN espacio del nombre", line, want)
	}
}

// TRIANGULATE: knock down a header that leaves the tail of the alignment invisible when there is no summary (without Input or with Input `{}`): the line is exactly the marker and the name, without dangling spaces.
func TestModel_ActivityHeaderWithoutSummaryHasNoTrailingSpaces(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	// Without Input and with Input `{}`: in both the summary is empty and the header must trim the spaces from the name alignment.
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash"})
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "grep", Input: json.RawMessage(`{}`)})

	plain := ansi.Strip(m.View())
	if line := lineWith(t, plain, "● Bash"); line != "  ● Bash" {
		t.Fatalf("header sin Input = %q, want exactamente %q: sin resumen no quedan espacios colgantes de la alineacion", line, "  ● Bash")
	}
	if line := lineWith(t, plain, "● Grep"); line != "  ● Grep" {
		t.Fatalf("header con Input {} = %q, want exactamente %q: el objeto vacio no produce resumen ni espacios colgantes", line, "  ● Grep")
	}
}

// TRIANGULATE: drop a hardcoded diffStat to the diff of the test above or count file headers +++/--- as content; The table also covers the empty diff and the stripped +/- lines (without text behind them).
func TestEntry_DiffStatIgnoresFileHeadersAndEmptyDiff(t *testing.T) {
	tests := []struct {
		name    string
		diff    string
		added   int
		removed int
	}{
		{
			name:    "cabeceras, hunk y contenido",
			diff:    "--- a/x\n+++ b/x\n@@ -1,2 +1,3 @@\n contexto\n-vieja\n+nueva\n+extra",
			added:   2,
			removed: 1,
		},
		{
			name:    "solo cabeceras y contexto",
			diff:    "--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n contexto",
			added:   0,
			removed: 0,
		},
		{name: "diff vacio", diff: "", added: 0, removed: 0},
		{name: "lineas + y - peladas cuentan", diff: "+\n-", added: 1, removed: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, removed := diffStat(tt.diff)
			if added != tt.added || removed != tt.removed {
				t.Fatalf("diffStat(%q) = (+%d, -%d), want (+%d, -%d)", tt.diff, added, removed, tt.added, tt.removed)
			}
		})
	}
}

// TRIANGULATE: knock down a header that always pastes the stat (`✓ Bash ls +0 -0`) instead of only when there is diff: a successful tool WITHOUT diff takes the stripped header and its output as rail lines.
func TestModel_SuccessWithoutDiffShowsNoStat(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "main.go\nview.go",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "main.go\nview.go", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	header := lineWith(t, plain, "✓ Bash")
	if want := "  ✓ Bash     ls"; header != want {
		t.Fatalf("header = %q, want exactamente %q: el exito sin diff no agrega nada tras el resumen", header, want)
	}
	for _, banned := range []string{"+0 -0", " +"} {
		if strings.Contains(header, banned) {
			t.Fatalf("header = %q, NO debe contener %q: el stat +N -M solo aplica cuando hay diff", header, banned)
		}
	}
	for _, needle := range []string{"main.go", "view.go"} {
		if line := lineWith(t, plain, needle); !strings.HasPrefix(line, "  │ ") {
			t.Fatalf("linea del output = %q, el output de la tool exitosa va con el rail %q tras el margen", line, "  │ ")
		}
	}
}

// TRIANGULATE: destroys a compact group that only includes tools: the pending permission (entryPermission) and the hard failure of the step (entryError) are also activity and join the group without blank lines; The assistant's narrative that follows retains its own paragraph.
func TestModel_PermissionAndErrorJoinActivityGroup(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, ToolCallID: "c1"},
	})
	// Runner's actual order: Tool.Called and then Tool.Permission.Requested while blocking in the gate (ask-before-run).
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"b.go"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"b.go"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindStepFailed, Error: "boom"})

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "sigo con el resto"})
	m = drainReveal(t, m)

	plain := ansi.Strip(m.View())
	// The running "● Write" header is hidden while its permission is still pending: only the orange "? Write" line of the permission represents the gated call, without duplicating it in two contiguous rows.
	want := "  ✓ Bash     ls\n  ? Write    b.go\n  ✗ error    boom"
	if !strings.Contains(plain, want) {
		t.Fatalf("View() sin ANSI = %q, debe contener %q: la tool exitosa, el permiso pendiente y el error de step quedan fisicamente contiguos, sin lineas en blanco entre si", plain, want)
	}
	if strings.Contains(plain, "● Write") {
		t.Fatalf("View() sin ANSI = %q, el header en ejecucion no debe duplicar la llamada mientras su permiso sigue pendiente", plain)
	}

	lines := strings.Split(plain, "\n")
	narrIdx := lineIndexWith(t, plain, "sigo con el resto")
	if narrIdx == 0 || strings.TrimSpace(lines[narrIdx-1]) != "" {
		t.Fatalf("linea previa a la narrativa = %q, la narrativa del assistant tras el grupo de actividad se separa con linea en blanco", lines[narrIdx-1])
	}
}

func TestModel_ToolInputDeltasAreNotTranscript(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// Reasoning: the thought block is shown while it flows, but when closed it collapses to the summary "◆ Thought for...": once the reveal is drained, its text is NOT left as plain text of the transcript.
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pienso en secreto"})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pienso en secreto"})

	// Tool input: raw fragments travel in Text and the complete JSON in Input; none of them are conversational texts, EVER.
	m = apply(t, m, EventMsg{Kind: session.KindToolInputStarted, CallID: "c1"})
	m = apply(t, m, EventMsg{Kind: session.KindToolInputDelta, CallID: "c1", Text: `{"cmd":"ls`})
	m = apply(t, m, EventMsg{Kind: session.KindToolInputEnded, CallID: "c1", Input: json.RawMessage(`{"cmd":"ls"}`)})

	// The normal text of the assistant is transcribed: contrasts with the above.
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "respuesta visible"})
	m = drainReveal(t, m)

	view := m.View()
	for _, leak := range []string{"pienso en secreto", `{"cmd":"ls`} {
		if strings.Contains(view, leak) {
			t.Fatalf("View() = %q, no debe filtrar %q como texto de la conversacion", view, leak)
		}
	}
	if !strings.Contains(view, "respuesta visible") {
		t.Fatalf("View() = %q, el texto del assistant si debe verse", view)
	}
}

// Parity with the desktop ThinkingBlock: reasoning is displayed as a collapsible block of the transcript. While flowing, the view carries the header "◆ Thinking..." and below ONLY the last 4 non-empty lines of the revealed text (sliding window); Reasoning.Ended, with the backlog already drained, collapses the block to a single summary line prefixed with "◆Thought" (readable duration), and the header and preview disappear.
func TestModel_ShowsReasoningAsCollapsibleThinkingBlock(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	text := "razon-1\nrazon-2\nrazon-3\nrazon-4\nrazon-5\nrazon-6"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "◆ Thinking…") {
		t.Fatalf("View() = %q, el reasoning en curso debe mostrar la cabecera %q", view, "◆ Thinking…")
	}
	for _, want := range []string{"razon-3", "razon-4", "razon-5", "razon-6"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, el preview debe mostrar %q (las ultimas 4 lineas no vacias del texto revelado)", view, want)
		}
	}
	for _, gone := range []string{"razon-1", "razon-2"} {
		if strings.Contains(view, gone) {
			t.Fatalf("View() = %q, %q ya salio de la ventana deslizante: solo se muestran las ultimas 4 lineas no vacias", view, gone)
		}
	}

	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)

	view = m.View()
	if !strings.Contains(view, "◆ Thought") {
		t.Fatalf("View() = %q, el reasoning terminado debe colapsar a una linea de resumen con el prefijo %q", view, "◆ Thought")
	}
	if strings.Contains(view, "◆ Thinking…") {
		t.Fatalf("View() = %q, la cabecera %q debe desaparecer al colapsar el bloque", view, "◆ Thinking…")
	}
	if strings.Contains(view, "razon-6") {
		t.Fatalf("View() = %q, las lineas del preview deben desaparecer al colapsar el bloque", view)
	}
}

func TestModel_ThinkingRevealsProgressivelyLikeAssistant(t *testing.T) {
	// TRIANGULATE: a fold that appends the delta of thought and reveals it at once (revealed = total) passes the main test, which drains before asserting. The thought participates in the same soft reveal as the assistant text: without ticks nothing is seen, each tick advances a prefix and only when draining is the end seen.
	m := NewModel(nil, "s1", nil)

	// Two long lines (300+ runes) so that the final token is within the 4-line preview window once drained.
	text := "inicio-marca " + strings.Repeat("a", 150) + "\n" + strings.Repeat("b", 150) + " token-final"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})

	view := m.View()
	if strings.Contains(view, "token-final") {
		t.Fatalf("View() = %q, %q NO debe verse sin ticks de reveal: el delta del pensamiento se revela progresivamente, no de golpe", view, "token-final")
	}
	if strings.Contains(view, "inicio-marca") {
		t.Fatalf("View() = %q, %q NO debe verse sin ticks de reveal: tambien el prefijo espera su tick", view, "inicio-marca")
	}

	m = apply(t, m, revealTickMsg{})
	view = m.View()
	if !strings.Contains(view, "inicio-marca") {
		t.Fatalf("View() = %q, tras UN tick debe verse el prefijo %q del pensamiento", view, "inicio-marca")
	}
	if strings.Contains(view, "token-final") {
		t.Fatalf("View() = %q, tras UN tick el final %q aun NO debe verse: un tick revela un paso, no todo el backlog", view, "token-final")
	}

	m = drainReveal(t, m)
	if view := m.View(); !strings.Contains(view, "token-final") {
		t.Fatalf("View() = %q, con el backlog drenado el final %q debe verse en la ventana del preview", view, "token-final")
	}
}

func TestModel_ThinkingPreviewSkipsBlankLines(t *testing.T) {
	// TRIANGULATE: a window that cuts the last 4 RAW lines (without empty filtering) shows blanks and loses content: from "r1\n\nr2\n\nr3\n\nr4\n\nr5" it would show ["", "r4", "", "r5"]. The window first filters the empty ones and then cuts: r2..r5 close to the header, without interspersed blanks.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "r1\n\nr2\n\nr3\n\nr4\n\nr5"})
	m = drainReveal(t, m)

	lines := strings.Split(ansi.Strip(m.View()), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	view := strings.Join(lines, "\n")
	if !strings.Contains(view, "  ◆ Thinking…\n  r2\n  r3\n  r4\n  r5") {
		t.Fatalf("View() = %q, el preview debe contener las ultimas 4 lineas NO vacias con el inset uniforme (%q): ni blancos intercalados ni lineas de contenido perdidas", view, "  ◆ Thinking…\n  r2\n  r3\n  r4\n  r5")
	}
	if strings.Contains(view, "r1") {
		t.Fatalf("View() = %q, %q ya salio de la ventana de 4 lineas no vacias", view, "r1")
	}
}

func TestModel_ThinkingKeepsChatInsetWhileStreamingAndExpanded(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 44, Height: 18})

	text := "streaming-inset-a\nstreaming-inset-b"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)

	assertThinkingInset(t, m.View(), "◆ Thinking…", "streaming-inset-a", "streaming-inset-b")

	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})

	assertThinkingInset(t, m.View(), "◆ Thought", "streaming-inset-a", "streaming-inset-b")
}

func TestEntry_RenderThinkingInsetsEveryWrappedLine(t *testing.T) {
	e := entry{
		kind:     entryReasoning,
		text:     strings.Repeat("pensamiento-largo ", 8),
		revealed: len(strings.Repeat("pensamiento-largo ", 8)),
		expanded: true,
	}

	lines := strings.Split(ansi.Strip(e.renderThinking(24)), "\n")
	if len(lines) < 3 {
		t.Fatalf("renderThinking() produjo %d lineas, want cabecera y multiples lineas envueltas: %q", len(lines), lines)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, thinkingInset) {
			t.Fatalf("linea envuelta = %q, want inset %q", line, thinkingInset)
		}
		if got, want := ansi.StringWidth(line), 24; got > want {
			t.Fatalf("ancho de linea envuelta = %d, want <= %d: %q", got, want, line)
		}
	}
}

func assertThinkingInset(t *testing.T, view string, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		line := strings.TrimRight(lineWith(t, ansi.Strip(view), needle), " ")
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("linea de pensamiento %q = %q, want inset de dos celdas", needle, line)
		}
	}
}

func TestModel_TextStartedClosesLiveThinking(t *testing.T) {
	// TRIANGULATE: if the runner never issues Reasoning.Ended, a naive fold leaves the "◆ Thinking..." header alive forever while the response streams below it. Starting the text implies that the thought is finished: Text.Started closes it defensively.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "sopeso opciones"})
	m = drainReveal(t, m)

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "respuesta"})
	m = drainReveal(t, m)

	view := m.View()
	if strings.Contains(view, "◆ Thinking…") {
		t.Fatalf("View() = %q, Text.Started debe cerrar el pensamiento en vivo: la cabecera %q no puede sobrevivir al arranque de la respuesta", view, "◆ Thinking…")
	}
	if !strings.Contains(view, "◆ Thought") {
		t.Fatalf("View() = %q, el pensamiento cerrado defensivamente debe colapsar al resumen %q", view, "◆ Thought")
	}
	if !strings.Contains(view, "respuesta") {
		t.Fatalf("View() = %q, la respuesta %q debe verse tras el pensamiento colapsado", view, "respuesta")
	}
}

func TestModel_StepEndedClosesLiveThinking(t *testing.T) {
	// TRIANGULATE: a step can die thinking (cancellation, provider error) without Reasoning.Ended or Text.Started involved. Step.Ended closes the thought defensively just like Text.Started; Without that closure the header "◆ Thinking..." would remain alive forever.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pienso y el step muere"})
	m = drainReveal(t, m)

	m = apply(t, m, EventMsg{Kind: session.KindStepEnded})

	view := m.View()
	if strings.Contains(view, "◆ Thinking…") {
		t.Fatalf("View() = %q, Step.Ended debe cerrar el pensamiento en vivo: la cabecera %q no puede sobrevivir al fin del step", view, "◆ Thinking…")
	}
	if !strings.Contains(view, "◆ Thought") {
		t.Fatalf("View() = %q, el pensamiento cerrado por el fin del step debe colapsar al resumen %q", view, "◆ Thought")
	}
}

func TestModel_ReasoningEndedTextCollapsesWithoutAnimation(t *testing.T) {
	// TRIANGULATE: when Reasoning.Ended brings the complete text without previous deltas (provider that does not stream the thought), the fill is NOT animated: it is revealed complete and collapsed in the same fold, without ticks in between. A fold that only assigns the text without marking it revealed would leave the block "writing" after it is closed.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "relleno-final-sin-stream"})

	view := m.View()
	if !strings.Contains(view, "◆ Thought") {
		t.Fatalf("View() = %q, sin deltas previos el Ended con texto debe colapsar de inmediato al resumen %q, sin ticks de por medio", view, "◆ Thought")
	}
	if strings.Contains(view, "◆ Thinking…") {
		t.Fatalf("View() = %q, la cabecera %q no debe verse tras el Ended: el texto de relleno no se anima", view, "◆ Thinking…")
	}
	if strings.Contains(view, "relleno-final-sin-stream") {
		t.Fatalf("View() = %q, el texto de relleno del Ended jamas debe verse plano, ni siquiera antes de drenar", view)
	}

	m = drainReveal(t, m)
	if view := m.View(); strings.Contains(view, "relleno-final-sin-stream") {
		t.Fatalf("View() = %q, el texto de relleno tampoco debe aparecer tras drenar: no quedo backlog que animar", view)
	}
}

func TestModel_TwoThinkingBlocksInSameRunStaySeparate(t *testing.T) {
	// TRIANGULATE: a fold that reuses the previous thought block instead of opening a new one would mix the lines of the first in the preview of the second and collapse both into ONE summary line. Each Reasoning.Started opens its own block.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "primero-a\nprimero-b"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "primero-a\nprimero-b"})
	m = drainReveal(t, m)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "segundo-a\nsegundo-b"})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "segundo-a") || !strings.Contains(view, "segundo-b") {
		t.Fatalf("View() = %q, el preview del segundo pensamiento debe mostrar sus lineas", view)
	}
	if strings.Contains(view, "primero-a") || strings.Contains(view, "primero-b") {
		t.Fatalf("View() = %q, el preview del segundo pensamiento NO debe mezclar lineas del primero (ya colapsado)", view)
	}

	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "segundo-a\nsegundo-b"})
	m = drainReveal(t, m)

	view = m.View()
	if count := strings.Count(view, "◆ Thought"); count < 2 {
		t.Fatalf("View() = %q, dos pensamientos en la misma corrida deben colapsar a DOS resumenes %q (count=%d)", view, "◆ Thought", count)
	}
}

func TestModel_ThinkingCollapseWaitsForRevealDrain(t *testing.T) {
	// TRIANGULATE: An instant crash upon receiving Ended with pending backlog would cut the animation mid-sentence. Parity with the desktop gift: the block continues "writing" until the reveal is drained and only then collapses to the summary.
	m := NewModel(nil, "s1", nil)

	text := "inicio-fluye " + strings.Repeat("c", 150) + "\n" + strings.Repeat("d", 150) + " final-tardio"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	// A tick so that there is a visible prefix to assert before the Ended.
	m = apply(t, m, revealTickMsg{})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})

	view := m.View()
	if !strings.Contains(view, "◆ Thinking…") {
		t.Fatalf("View() = %q, con backlog pendiente el Ended NO colapsa todavia: la cabecera %q debe seguir mientras se drena", view, "◆ Thinking…")
	}
	if !strings.Contains(view, "inicio-fluye") {
		t.Fatalf("View() = %q, el prefijo ya revelado %q debe seguir visible mientras el pensamiento termina de escribirse", view, "inicio-fluye")
	}
	if strings.Contains(view, "◆ Thought") {
		t.Fatalf("View() = %q, el resumen %q no debe aparecer hasta drenar el backlog del reveal", view, "◆ Thought")
	}

	m = drainReveal(t, m)
	view = m.View()
	if !strings.Contains(view, "◆ Thought") {
		t.Fatalf("View() = %q, con el backlog drenado el pensamiento cerrado debe colapsar al resumen %q", view, "◆ Thought")
	}
	if strings.Contains(view, "◆ Thinking…") {
		t.Fatalf("View() = %q, la cabecera %q debe desaparecer al colapsar", view, "◆ Thinking…")
	}
}

func TestModel_SecondTurnOpensNewBlock(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// First complete turn: streaming, closing the block and closing the step.
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "Primera respuesta"})
	m = apply(t, m, EventMsg{Kind: session.KindTextEnded, Text: "Primera respuesta"})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "Primera respuesta"},
	})

	// Second turn: the new streaming opens a NEW block.
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "Segunda respuesta"})
	m = drainReveal(t, m)

	view := m.View()
	if strings.Contains(view, "Primera respuestaSegunda respuesta") {
		t.Fatalf("View() = %q, el segundo turno NO debe concatenar al bloque anterior", view)
	}
	first := strings.Index(view, "Primera respuesta")
	second := strings.Index(view, "Segunda respuesta")
	if first < 0 || second < 0 || first >= second {
		t.Fatalf("View() sin ANSI = %q, ambos textos deben verse como bloques separados y ordenados", view)
	}
	if count := strings.Count(view, "Primera respuesta"); count != 1 {
		t.Fatalf("View() = %q, %q debe aparecer exactamente una vez (count=%d)", view, "Primera respuesta", count)
	}
}

func TestModel_EnterWithEmptyInputDoesNotSend(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, Enter con input vacio no debe enviar nada", len(fake.sent))
	}
	if m.Working() {
		t.Fatalf("Working() = true, Enter con input vacio no debe marcar el modelo como trabajando")
	}
}

func TestModel_PermissionKeysResolveViaAgent(t *testing.T) {
	// Scenario 1: 'y' approves pending request c1.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(fake.resolved) != 1 {
		t.Fatalf("ResolvePermission fue llamado %d veces, 'y' debe resolver exactamente una vez", len(fake.resolved))
	}
	if got := fake.resolved[0]; got.sessionID != "s1" || got.callID != "c1" || !got.approved() {
		t.Fatalf("ResolvePermission(%q, %q, %v), se esperaba ResolvePermission(%q, %q, true)", got.sessionID, got.callID, got.approved(), "s1", "c1")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, la runa 'y' NO debe entrar al input mientras hay permiso pendiente", got)
	}
	if callID, ok := m.PendingPermission(); ok {
		t.Fatalf("PendingPermission() = (%q, %v), resolver debe ocultar inmediatamente el panel y evitar dobles decisiones", callID, ok)
	}
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "ok",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})
	if callID, ok := m.PendingPermission(); ok {
		t.Fatalf("PendingPermission() = (%q, %v), Tool.Success debe retirar la solicitud", callID, ok)
	}

	// Scenario 2: 'n' denies pending request c2; Additionally, the runes do not enter the input and Enter does not send a prompt while permission is pending.
	fake2 := &fakeAgent{}
	m2 := NewModel(fake2, "s1", nil)
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"a.go"}`)})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"a.go"}`)})

	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	if got := m2.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, las runas NO deben entrar al input mientras hay permiso pendiente", got)
	}
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake2.sent) != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, Enter NO debe enviar prompt mientras hay permiso pendiente", len(fake2.sent))
	}
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if len(fake2.resolved) != 1 {
		t.Fatalf("ResolvePermission fue llamado %d veces, 'n' debe resolver exactamente una vez", len(fake2.resolved))
	}
	if got := fake2.resolved[0]; got.sessionID != "s1" || got.callID != "c2" || got.approved() {
		t.Fatalf("ResolvePermission(%q, %q, %v), se esperaba ResolvePermission(%q, %q, false)", got.sessionID, got.callID, got.approved(), "s1", "c2")
	}
	if got := ansi.Strip(m2.View()); !strings.Contains(got, "Denied by user") || strings.Contains(got, "Permission required") {
		t.Fatalf("View() = %q, denegar debe cerrar el panel y dejar un estado neutral en el transcript", got)
	}
}

func TestModel_PermissionPanelRendersInlineAboveComposer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithWorkspace("main", "~/dev/atenea")
	m.input.SetValue("draft stays here")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash",
		Input: json.RawMessage(`{"command":"printf 'one\\ntwo\\nthree\\nfour\\nfive'"}`),
	})

	view := ansi.Strip(m.View())
	for _, want := range []string{
		"Permission required", "Bash printf 'one\\ntwo\\nthree\\nfour\\nfive'",
		"Deny", "Allow", "draft stays here",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, want %q", view, want)
		}
	}
	for _, unwanted := range []string{
		"Bash command", "Requested by", "Working directory", "Allow once",
		"←/→ select", "›",
	} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("View() = %q, Bash permission panel must hide %q", view, unwanted)
		}
	}
	if strings.Contains(view, "1 of 1") {
		t.Fatalf("View() = %q, a single permission must not show a queue counter", view)
	}
	if strings.Index(view, "Permission required") > strings.Index(view, "draft stays here") {
		t.Fatalf("View() = %q, permission panel must render above composer", view)
	}
	lines := strings.Split(view, "\n")
	titleIndex := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, "Permission required") })
	if titleIndex < 0 || titleIndex+4 >= len(lines) ||
		strings.TrimSpace(lines[titleIndex+1]) != "" ||
		!strings.Contains(lines[titleIndex+2], "Bash printf") ||
		strings.TrimSpace(lines[titleIndex+3]) != "" ||
		!strings.Contains(lines[titleIndex+4], "Deny") {
		t.Fatalf("View() = %q, Bash panel must space title, command, and actions like the reference", view)
	}
	if m.input.Focused() {
		t.Fatal("composer must be blurred while permission is pending")
	}
}

func TestModel_PermissionPanelReusesToolCallInputWhenRequestOmitsIt(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithWorkspace("main", "~/dev/atenea")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	// This is the exact durable event emitted by Publisher.ToolPermissionRequested:
	// it carries CallID and ToolName, but no Input payload.
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash"})

	panel := ansi.Strip(m.permissionPanelView())
	if !strings.Contains(panel, " ls") || strings.Contains(panel, "No input provided") {
		t.Fatalf("permission panel = %q, must reuse the preceding Tool.Called input", panel)
	}
}

func TestModel_BashPermissionPanelUsesGreenBackgroundForActiveAction(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	activeBackground := "48;2;177;184;107m"
	titleLine := lineWith(t, m.permissionPanelView(), "Permission required")
	if !strings.Contains(titleLine, activeBackground) {
		t.Fatalf("title line = %q, Bash permission title must use the green bar", titleLine)
	}
	actionLine := lineWith(t, m.permissionPanelView(), "Deny")
	if backgroundIndex, denyIndex := strings.Index(actionLine, activeBackground), strings.Index(actionLine, "Deny"); backgroundIndex < 0 || backgroundIndex > denyIndex {
		t.Fatalf("action line = %q, Deny must start active with the green background", actionLine)
	}
	denyIndex := strings.Index(actionLine, "Deny")
	allowIndex := strings.Index(actionLine, "Allow")
	if denyIndex < 0 || allowIndex < 0 || !strings.Contains(actionLine[denyIndex+len("Deny"):allowIndex], "48;2;48;48;48m") {
		t.Fatalf("action line = %q, the gap between actions must keep the panel background", actionLine)
	}
	commandLine := lineWith(t, m.permissionPanelView(), "Bash")
	if labelColorIndex, bashIndex := strings.Index(commandLine, "38;2;153;153;153;"), strings.Index(commandLine, "Bash"); labelColorIndex < 0 || labelColorIndex > bashIndex {
		t.Fatalf("command line = %q, Bash label must use #999999", commandLine)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	actionLine = lineWith(t, m.permissionPanelView(), "Allow")
	if backgroundIndex, denyIndex := strings.Index(actionLine, activeBackground), strings.Index(actionLine, "Deny"); backgroundIndex < 0 || backgroundIndex < denyIndex {
		t.Fatalf("action line = %q, Right must move the green active background to Allow", actionLine)
	}

	// `ls` is grantable, so a third action follows Allow and takes the highlight
	// in its turn.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	actionLine = lineWith(t, m.permissionPanelView(), "this session")
	if backgroundIndex, sessionIndex := strings.Index(actionLine, activeBackground), strings.Index(actionLine, "Allow ls this session"); backgroundIndex < 0 || sessionIndex < 0 || backgroundIndex > sessionIndex {
		t.Fatalf("action line = %q, Right must move the green active background to the session grant", actionLine)
	}
}

func TestModel_PermissionPanelQueuesFIFOAndShowsOrigin(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "parent", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 72, Height: 20})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "first", ToolName: "bash", Input: json.RawMessage(`{"command":"echo first"}`)})
	m = apply(t, m, EventMsg{SessionID: "child-1", Kind: session.KindToolPermissionRequested, CallID: "second", ToolName: "mcp_deploy", Input: json.RawMessage(`{"target":"prod"}`)})

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Bash echo first") || strings.Contains(view, "1 of 2") || strings.Contains(view, "Requested by") {
		t.Fatalf("View() = %q, first queued permission must be shown first", view)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	view = ansi.Strip(m.View())
	if strings.Contains(view, "1 of 1") || !strings.Contains(view, "Requested by subagent") || !strings.Contains(view, "prod") {
		t.Fatalf("View() = %q, resolving first permission must reveal child permission", view)
	}
	if got := fake.resolved[0]; got.callID != "first" || !got.approved() {
		t.Fatalf("first resolution = %+v, want first approved", got)
	}
}

func TestModel_PermissionPanelKeyboardNavigationAndEscape(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m.input.SetValue("preserved draft")
	m = apply(t, m, tea.WindowSizeMsg{Width: 64, Height: 18})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"echo ok"}`)})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.resolved) != 1 || fake.resolved[0].approved() {
		t.Fatalf("resolved = %+v, Enter must deny the default action", fake.resolved)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c2", ToolName: "bash", Input: json.RawMessage(`{"command":"echo ok"}`)})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.resolved) != 2 || !fake.resolved[1].approved() {
		t.Fatalf("resolved = %+v, Right then Enter must approve", fake.resolved)
	}
	if got := m.input.Value(); got != "preserved draft" {
		t.Fatalf("input.Value() = %q, want preserved draft", got)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c3", ToolName: "bash", Input: json.RawMessage(`{"command":"echo no"}`)})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if len(fake.resolved) != 3 || fake.resolved[2].approved() {
		t.Fatalf("resolved = %+v, Esc must deny immediately", fake.resolved)
	}
}

func TestModel_PermissionPanelScrollsLongCommand(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 52, Height: 20})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash",
		Input: json.RawMessage(`{"command":"line-1\nline-2\nline-3\nline-4\nline-5\nline-6"}`),
	})

	before := ansi.Strip(m.View())
	if !strings.Contains(before, "line-1") || !strings.Contains(before, "↓ more") {
		t.Fatalf("View() = %q, long command must start at first line and advertise overflow", before)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	after := ansi.Strip(m.permissionPanelView())
	if m.permissionScroll != 1 || strings.Contains(after, "line-1") || !strings.Contains(after, "line-5") {
		t.Fatalf("View() = %q, Down must scroll the command window", after)
	}
}

func TestModel_DeniedPermissionStaysNeutralAfterToolFailed(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 16})
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"touch forbidden"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"touch forbidden"}`)})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c1", ToolName: "bash", Error: "tool denied by the user"})

	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "– Bash") || !strings.Contains(plain, "Denied by user") || strings.Contains(plain, "error: tool denied") {
		t.Fatalf("View() = %q, denied tool must remain neutral after durable Tool.Failed", plain)
	}
}

func TestModel_PermissionPanelMouseActions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		choice  permissionChoice
		verdict permission.Verdict
	}{
		{name: "deny", choice: permissionDeny, verdict: permission.Denied},
		{name: "allow once", choice: permissionAllowOnce, verdict: permission.AllowedOnce},
		{name: "allow session", choice: permissionAllowSession, verdict: permission.AllowedSession},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeAgent{}
			m := NewModel(fake, "s1", nil)
			m = apply(t, m, tea.WindowSizeMsg{Width: 70, Height: 20})
			m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"echo click"}`)})
			layout, ok := m.permissionPanelLayout()
			if !ok {
				t.Fatal("permissionPanelLayout() = false")
			}
			x, y := layout.actionPoint(tc.choice)
			m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y + topBarHeight})
			if len(fake.resolved) != 1 || fake.resolved[0].verdict != tc.verdict {
				t.Fatalf("resolved = %+v, click verdict want %v", fake.resolved, tc.verdict)
			}
		})
	}
}

// TestModel_PermissionPanelOffersSessionGrantForBash: the panel names the exact
// prefix it grants, so the user sees the scope before accepting it.
func TestModel_PermissionPanelOffersSessionGrantForBash(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"go test ./..."}`)})

	panel := ansi.Strip(m.permissionPanelView())
	if !strings.Contains(panel, "Allow go test this session") {
		t.Fatalf("permissionPanelView() = %q, want the session grant naming the granted prefix", panel)
	}
}

// TestModel_PermissionPanelOffersSessionGrantForMCPTools: a tool atenea does not
// ship asks like any other and can be granted for the session as a whole, which is
// exactly what the panel showed. Before tools could describe themselves, only
// bash, write and edit were grantable, so an MCP tool re-asked forever with no way
// to say "this one is fine".
func TestModel_PermissionPanelOffersSessionGrantForMCPTools(t *testing.T) {
	const remote = "mcp_github_create_issue"
	fake := &fakeAgent{tools: catalogWithRemoteTool(remote)}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: remote, Input: json.RawMessage(`{"title":"bug"}`)})

	panel := ansi.Strip(m.permissionPanelView())
	if want := "Allow " + remote + " this session"; !strings.Contains(panel, want) {
		t.Fatalf("permissionPanelView() = %q, want %q", panel, want)
	}
	// The remote tool cannot state its call as text, so it gets the detailed panel
	// with the raw input rather than a compact surface implying otherwise.
	if !strings.Contains(panel, remote+" request") || !strings.Contains(panel, `"title"`) {
		t.Fatalf("permissionPanelView() = %q, want the detailed panel titled by the tool and showing its raw input", panel)
	}
}

// catalogWithRemoteTool is a registry holding one tool from an MCP server: silent
// about its effects — so it asks — and grantable as a whole.
func catalogWithRemoteTool(name string) tool.Catalog {
	return tool.NewRegistry(tool.NewOutputStore(1024),
		tool.NewBashTool("/nonexistent"), remoteTool{name: name})
}

type remoteTool struct{ name string }

func (r remoteTool) Name() string            { return r.name }
func (r remoteTool) Description() string     { return r.name + " (remote)" }
func (r remoteTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (r remoteTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, nil
}
func (r remoteTool) GrantRule(tool.Call) (permission.Rule, bool) {
	return permission.Rule{Tool: r.name}, true
}

// TestModel_PermissionPanelSessionGrantKeys: 'a' grants the rule for the
// session, and ←/→ reach the third action so Enter confirms it.
func TestModel_PermissionPanelSessionGrantKeys(t *testing.T) {
	request := EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "write", Input: json.RawMessage(`{"path":"a.go","content":"x"}`)}

	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, request)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if len(fake.resolved) != 1 || fake.resolved[0].verdict != permission.AllowedSession {
		t.Fatalf("resolved = %+v, 'a' must grant the rule for the session", fake.resolved)
	}

	arrows := &fakeAgent{}
	m2 := NewModel(arrows, "s1", nil)
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 = apply(t, m2, request)
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyRight})
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyRight})
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyRight}) // stops at the last action
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyEnter})
	if len(arrows.resolved) != 1 || arrows.resolved[0].verdict != permission.AllowedSession {
		t.Fatalf("resolved = %+v, ←/→ must reach the session grant and Enter must confirm it", arrows.resolved)
	}
}

// TestModel_PermissionPanelWithholdsSessionGrantWhenNotExpressible: web_fetch
// has no rule (a blanket pass on outbound network cannot be summarized), so the
// panel offers two actions and 'a' is a no-op.
func TestModel_PermissionPanelWithholdsSessionGrantWhenNotExpressible(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "web_fetch", Input: json.RawMessage(`{"url":"https://example.com","prompt":"lee"}`)})

	if panel := ansi.Strip(m.permissionPanelView()); strings.Contains(panel, "this session") {
		t.Fatalf("permissionPanelView() = %q, web_fetch must not offer a session grant", panel)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if len(fake.resolved) != 0 {
		t.Fatalf("resolved = %+v, 'a' must be a no-op with no grant available", fake.resolved)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.resolved) != 1 || fake.resolved[0].verdict != permission.AllowedOnce {
		t.Fatalf("resolved = %+v, the selection must not move past the last offered action", fake.resolved)
	}
}

// TestModel_PermissionPanelWithholdsSessionGrantOnNarrowTerminal: a truncated
// action reads as a bug, so a narrow panel withholds it instead of half-drawing
// it — and the selection cannot point at what is not drawn.
func TestModel_PermissionPanelWithholdsSessionGrantOnNarrowTerminal(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 34, Height: 20})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"go test ./..."}`)})

	if panel := ansi.Strip(m.permissionPanelView()); strings.Contains(panel, "this session") {
		t.Fatalf("permissionPanelView() = %q, a narrow panel must not offer the grant half-drawn", panel)
	}
	layout, ok := m.permissionPanelLayout()
	if !ok {
		t.Fatal("permissionPanelLayout() = false")
	}
	if len(layout.actions) != 2 {
		t.Fatalf("layout.actions = %+v, want 2 actions", layout.actions)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.resolved) != 1 || fake.resolved[0].verdict != permission.AllowedOnce {
		t.Fatalf("resolved = %+v, want AllowedOnce", fake.resolved)
	}
}

func TestModel_PermissionPanelFitsTinyTerminal(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithWorkspace("main", "~/dev/atenea")
	m = apply(t, m, tea.WindowSizeMsg{Width: 28, Height: 10})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"one two three four five six seven eight nine ten"}`)})

	view := m.View()
	if got := strings.Count(view, "\n") + 1; got > 10 {
		t.Fatalf("View() lines = %d, want <= 10: %q", got, ansi.Strip(view))
	}
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 28 {
			t.Fatalf("line width = %d, want <= 28: %q", width, ansi.Strip(line))
		}
	}
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "Permission required") || !strings.Contains(plain, "Deny") || !strings.Contains(plain, "Allow") {
		t.Fatalf("View() = %q, tiny terminal must preserve title and actions", plain)
	}
}

func TestModel_PermissionPanelDoesNotOverflowExtremelyShortTerminal(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 24, Height: 8})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"echo tiny"}`)})

	view := m.View()
	if got := strings.Count(view, "\n") + 1; got > 8 {
		t.Fatalf("View() lines = %d, want <= 8: %q", got, ansi.Strip(view))
	}
	if plain := ansi.Strip(view); !strings.Contains(plain, "Deny") {
		t.Fatalf("View() = %q, shortest usable panel must preserve a safe action", plain)
	}
}

func TestModel_PermissionResolvesWithEventSessionID(t *testing.T) {
	// The allow event can come from a DAUGHTER session (subagent): the parent bus surfaces the child event keeping SessionID = childID. The 'y' key should resolve to THAT SessionID, not the TUI.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, EventMsg{SessionID: "child-1", Kind: session.KindToolCalled, CallID: "c9", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	m = apply(t, m, EventMsg{SessionID: "child-1", Kind: session.KindToolPermissionRequested, CallID: "c9", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	if len(fake.resolved) != 1 {
		t.Fatalf("ResolvePermission fue llamado %d veces, 'y' debe resolver exactamente una vez", len(fake.resolved))
	}
	if got := fake.resolved[0]; got.sessionID != "child-1" || got.callID != "c9" || !got.approved() {
		t.Fatalf("ResolvePermission(%q, %q, %v), se esperaba ResolvePermission(%q, %q, true): el permiso del subagente se resuelve con el SessionID del evento", got.sessionID, got.callID, got.approved(), "child-1", "c9")
	}
}

func TestModel_CtrlCStopsAndQuits(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if len(fake.stopped) != 1 || fake.stopped[0] != "s1" {
		t.Fatalf("Stop = %v, Ctrl+C debe llamar Stop(%q) exactamente una vez", fake.stopped, "s1")
	}
	if cmd == nil {
		t.Fatalf("cmd = nil, Ctrl+C debe devolver un tea.Cmd que produzca tea.QuitMsg")
	}
	if msg := cmd(); msg == nil {
		t.Fatalf("cmd() = nil, se esperaba tea.QuitMsg")
	} else if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, se esperaba tea.QuitMsg", msg)
	}

	// With permission pending Ctrl+C still works the same.
	fake2 := &fakeAgent{}
	m2 := NewModel(fake2, "s1", nil)
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})

	_, cmd2 := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if len(fake2.stopped) != 1 || fake2.stopped[0] != "s1" {
		t.Fatalf("Stop = %v, Ctrl+C con permiso pendiente debe llamar Stop(%q)", fake2.stopped, "s1")
	}
	if cmd2 == nil {
		t.Fatalf("cmd = nil, Ctrl+C con permiso pendiente debe devolver un tea.Cmd que produzca tea.QuitMsg")
	}
	if msg := cmd2(); msg == nil {
		t.Fatalf("cmd() = nil, se esperaba tea.QuitMsg con permiso pendiente")
	} else if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, se esperaba tea.QuitMsg con permiso pendiente", msg)
	}
}

func TestModel_EscRequiresConfirmationBeforeStopping(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m.working = true
	m.activeRun = 1
	m.gitSummary = gitSummary{Files: 1, Additions: 2, Deletions: 1}
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if len(fake.stopped) != 0 {
		t.Fatalf("Stop = %v, el primer Esc solo debe pedir confirmacion", fake.stopped)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, el primer Esc debe programar la expiracion de la confirmacion")
	}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "Esc again to cancel") {
		t.Fatalf("View() = %q, falta la confirmacion bajo el composer", plain)
	}
	line := ansi.Strip(lineWith(t, m.View(), "Esc again to cancel"))
	if !strings.HasPrefix(line, "  Esc again to cancel") || !strings.Contains(line, "1 file changed  +2  −1") {
		t.Fatalf("linea bajo composer = %q, el aviso debe quedar a la izquierda y Git a la derecha", line)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if len(fake.stopped) != 1 || fake.stopped[0] != "s1" {
		t.Fatalf("Stop = %v, el segundo Esc debe llamar Stop(%q) exactamente una vez", fake.stopped, "s1")
	}
	if cmd != nil {
		if _, quits := cmd().(tea.QuitMsg); quits {
			t.Fatal("el segundo Esc debe cancelar la corrida sin salir de la TUI")
		}
	}
	if plain := ansi.Strip(m.View()); strings.Contains(plain, "Esc again to cancel") {
		t.Fatalf("View() = %q, la confirmacion debe desaparecer al cancelar", plain)
	}
}

func TestModel_EscConfirmationDisarms(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m.working = true
	m.activeRun = 1
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.cancelPending || strings.Contains(ansi.Strip(m.View()), "Esc again to cancel") {
		t.Fatal("una tecla distinta de Esc debe desarmar la confirmacion")
	}
	if got := m.input.Value(); got != "x" {
		t.Fatalf("input = %q, la tecla que desarma debe procesarse normalmente", got)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	generation := m.cancelGeneration
	m = apply(t, m, cancelConfirmationExpiredMsg{generation: generation})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if len(fake.stopped) != 0 || !m.cancelPending {
		t.Fatalf("Stop = %v pending = %v, tras expirar Esc debe iniciar una confirmacion nueva", fake.stopped, m.cancelPending)
	}

	idle := NewModel(fake, "s1", nil)
	idle = apply(t, idle, tea.KeyMsg{Type: tea.KeyEsc})
	if idle.cancelPending {
		t.Fatal("Esc sin corrida activa no debe mostrar confirmacion")
	}
}

func TestModel_RunDoneStopsWorkingAndShowsError(t *testing.T) {
	// Clean run: RunDoneMsg{Err: ""} just turns off Working.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.Working() {
		t.Fatalf("Working() = false, el modelo debe quedar trabajando tras enviar el prompt")
	}

	m = apply(t, m, activeRunDone(m, ""))
	if m.Working() {
		t.Fatalf("Working() = true, RunDoneMsg debe apagar el estado de trabajo")
	}
	if got := m.View(); strings.Contains(got, "✗ error") {
		t.Fatalf("View() = %q, una corrida limpia no debe mostrar error", got)
	}

	// Failed run: RunDoneMsg{Err: "boom"} also shows the error.
	m2 := NewModel(fake, "s1", nil)
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyEnter})

	m2 = apply(t, m2, activeRunDone(m2, "boom"))
	if m2.Working() {
		t.Fatalf("Working() = true, RunDoneMsg con error tambien debe apagar el estado de trabajo")
	}
	errLine := lineWith(t, m2.View(), "boom")
	if !strings.Contains(errLine, "✗ error") {
		t.Fatalf("linea del fallo = %q, debe llevar el marcador %q", errLine, "✗ error")
	}
}

func TestModel_StaleRunDoneDoesNotStopReplacementRun(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m.input.SetValue("primera")
	m, _ = m.submitPrompt()
	firstRunID := m.activeRun

	m.input.SetValue("segunda")
	m, _ = m.submitPrompt()
	secondRunID := m.activeRun
	if firstRunID == secondRunID {
		t.Fatalf("run IDs = %d y %d, cada corrida debe tener identidad propia", firstRunID, secondRunID)
	}

	m = apply(t, m, RunDoneMsg{SessionID: "s1", RunID: firstRunID})
	if !m.Working() {
		t.Fatal("el cierre tardio de la corrida anterior apago el indicador de la corrida nueva")
	}
	if m.activeRun != secondRunID {
		t.Fatalf("activeRun = %d, se esperaba conservar la corrida nueva %d", m.activeRun, secondRunID)
	}

	m = apply(t, m, RunDoneMsg{SessionID: "s1", RunID: secondRunID})
	if m.Working() {
		t.Fatal("el cierre de la corrida activa debe apagar el indicador")
	}
}

func TestModel_EventPumpDeliversFromChannel(t *testing.T) {
	ch := make(chan tea.Msg, 2)
	first := EventMsg{Kind: session.KindTextStarted}
	second := EventMsg{Kind: session.KindTextDelta, Text: "hola"}
	ch <- first
	ch <- second

	m := NewModel(nil, "s1", ch)

	// Init sets off the bomb: the cmd does receive and delivers the first msg.
	cmd := m.Init()
	if cmd == nil {
		t.Fatalf("Init() = nil, con canal de eventos debe devolver el cmd de la bomba")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("Init command = %#v, want event pump and composer cursor commands", batch)
	}
	msg := batch[0]()
	if got, ok := msg.(EventMsg); !ok || got.Kind != first.Kind {
		t.Fatalf("cmd() = %#v, se esperaba el primer EventMsg %#v", msg, first)
	}

	// Consuming an event resets the bomb: the new cmd delivers the second msg.
	updated, cmd2 := m.Update(msg)
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd2 == nil {
		t.Fatalf("Update(EventMsg) devolvio cmd nil, la bomba debe rearmarse tras cada evento")
	}
	msg2 := cmd2()
	if got, ok := msg2.(EventMsg); !ok || got.Kind != second.Kind || got.Text != second.Text {
		t.Fatalf("cmd() = %#v, se esperaba el segundo EventMsg %#v", msg2, second)
	}

	// RunDoneMsg also resets the bomb.
	_, cmd3 := m.Update(activeRunDone(m, ""))
	if cmd3 == nil {
		t.Fatalf("Update(RunDoneMsg) devolvio cmd nil, la bomba debe rearmarse tras el fin de corrida")
	}

	// Closed channel: cmd returns nil instead of hanging or delivering garbage.
	close(ch)
	if got := cmd3(); got != nil {
		t.Fatalf("cmd() = %#v con canal cerrado, se esperaba nil", got)
	}

	// Channel nil (fold tests): only the cursor command remains.
	if cmd := NewModel(nil, "s1", nil).Init(); cmd == nil {
		t.Fatal("Init() = nil con canal nil, se esperaba el comando del cursor")
	}
}

func TestModel_ViewportFollowsTailOnNewEvents(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// The terminal announces its size: the conversation must live in a high-bounded viewport that follows the queue (auto-scroll at the bottom).
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	// Many more entries than fit in 10 lines.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	view := m.View()
	if !strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, la ultima entrada %q debe estar visible: la vista sigue la cola", view, "mensaje-29")
	}
	if strings.Contains(view, "mensaje-00") {
		t.Fatalf("View() = %q, la primera entrada %q NO debe estar visible: el alto esta acotado por el viewport", view, "mensaje-00")
	}
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("View() tiene %d lineas, debe respetar el alto de la terminal (<= 10)", lines)
	}
}

func TestModel_PgUpScrollsHistoryBack(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	// Many more entries than fit: the view starts following the queue.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	// PgUp goes back one page: the queue is no longer visible and previous history appears.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	view := m.View()
	if strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, tras PgUp la cola %q NO debe seguir visible", view, "mensaje-29")
	}
	if !strings.Contains(view, "mensaje-") {
		t.Fatalf("View() = %q, tras PgUp debe verse algun mensaje anterior del historial", view)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, PgUp NO debe escribir en el textinput", got)
	}

	// Several consecutive PgDns return the view to the queue.
	for i := 0; i < 5; i++ {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if got := m.View(); !strings.Contains(got, "mensaje-29") {
		t.Fatalf("View() = %q, tras varios PgDn la cola %q debe volver a verse", got, "mensaje-29")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, PgDn NO debe escribir en el textinput", got)
	}

	// With pending permission PgUp is still scrolling: it does not trigger the gate.
	fake := &fakeAgent{}
	m2 := NewModel(fake, "s1", nil)
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m2 = apply(t, m2, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("permission-history-%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-permiso-%02d", i),
		}})
	}
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	beforeOffset := m2.viewport.YOffset
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyPgUp})
	if got := m2.viewport.YOffset; got >= beforeOffset {
		t.Fatalf("viewport.YOffset = %d, want less than %d: PgUp con permiso pendiente debe desplazar el transcript", got, beforeOffset)
	}
	if len(fake.resolved) != 0 {
		t.Fatalf("ResolvePermission fue llamado %d veces, PgUp NO debe disparar el gate de permisos", len(fake.resolved))
	}
	if got := m2.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, PgUp con permiso pendiente NO debe escribir en el textinput", got)
	}
}

// Mouse wheel events shared by scroll tests.
var (
	wheelUp   = tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}
	wheelDown = tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}
)

func TestModel_MouseWheelScrollsHistory(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	// Many more entries than fit: the view starts following the queue.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	// Two wheels up go back in history: the tail is no longer visible.
	m = apply(t, m, wheelUp)
	m = apply(t, m, wheelUp)
	view := m.View()
	if strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, tras rueda arriba la cola %q NO debe seguir visible", view, "mensaje-29")
	}
	if !strings.Contains(view, "mensaje-") {
		t.Fatalf("View() = %q, tras rueda arriba debe verse algun mensaje anterior del historial", view)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, la rueda NO debe escribir en el textinput", got)
	}

	// Several wheels below return the view to the tail.
	for i := 0; i < 5; i++ {
		m = apply(t, m, wheelDown)
	}
	if got := m.View(); !strings.Contains(got, "mensaje-29") {
		t.Fatalf("View() = %q, tras varias ruedas abajo la cola %q debe volver a verse", got, "mensaje-29")
	}

	// With permission pending, the wheel continues to scroll: it does not trigger the gate.
	fake := &fakeAgent{}
	m2 := NewModel(fake, "s1", nil)
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 40, Height: 10})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, wheelUp)
	if len(fake.resolved) != 0 {
		t.Fatalf("ResolvePermission fue llamado %d veces, la rueda NO debe disparar el gate de permisos", len(fake.resolved))
	}
	if got := m2.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, la rueda con permiso pendiente NO debe escribir en el textinput", got)
	}
}

func TestModel_MouseWheelSurvivesTinyOrUnsizedTerminal(t *testing.T) {
	// TRIANGULATE: a poor fix could assume an already sized viewport when resending the wheel. Without prior WindowSizeMsg (ready == false) or with pty 0x0, a wheel event should not panic and View() should continue to return a string even if it is demoted.
	t.Run("sin WindowSizeMsg previo", func(t *testing.T) {
		m := NewModel(nil, "s1", nil)

		m = apply(t, m, wheelUp)
		if got := m.View(); got == "" {
			t.Fatalf("View() = %q, sin tamano de terminal conocido debe devolver un string aunque sea degradado", got)
		}
	})

	t.Run("pty 0x0 con mensaje foldeado", func(t *testing.T) {
		m := NewModel(nil, "s1", nil)

		m = apply(t, m, tea.WindowSizeMsg{Width: 0, Height: 0})
		m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}})

		m = apply(t, m, wheelUp)
		if got := m.View(); got == "" {
			t.Fatalf("View() = %q, con terminal 0x0 la rueda no debe tumbar la TUI y View debe devolver un string aunque sea degradado", got)
		}
	})
}

func TestModel_NewEventPreservesReadingPositionWhileScrolledUp(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	// Two wheels up: the tail is no longer visible (precondition of the case).
	m = apply(t, m, wheelUp)
	m = apply(t, m, wheelUp)
	offset := m.viewport.YOffset
	if got := m.View(); strings.Contains(got, "mensaje-29") {
		t.Fatalf("View() = %q, tras rueda arriba la cola %q NO debe seguir visible", got, "mensaje-29")
	}

	// New activity arrives: preserves the reading position and shows a passive arrow instead of dragging the user to the queue.
	m = apply(t, m, EventMsg{Message: &session.Message{
		ID:   "u30",
		Role: session.RoleUser,
		Text: "mensaje-30",
	}})
	if got := m.viewport.YOffset; got != offset {
		t.Fatalf("viewport.YOffset = %d, want %d: la actividad nueva no debe mover la posicion de lectura", got, offset)
	}
	view := ansi.Strip(m.View())
	if strings.Contains(view, "mensaje-30") {
		t.Fatalf("View() = %q, la actividad nueva no debe volver a mostrar la cola", view)
	}
	if !strings.Contains(view, "↓") {
		t.Fatalf("View() = %q, debe mostrar una flecha pasiva cuando hay actividad nueva fuera de vista", view)
	}
}

func TestModel_StreamingRevealPreservesReadingPositionAndMarksActivity(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID: fmt.Sprintf("u%02d", i), Role: session.RoleUser, Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: strings.Repeat("stream ", 20)})
	m = apply(t, m, wheelUp)
	m = apply(t, m, wheelUp)
	offset := m.viewport.YOffset

	m = apply(t, m, revealTickMsg{})

	if got := m.viewport.YOffset; got != offset {
		t.Fatalf("viewport.YOffset = %d, want %d: el reveal no debe arrastrar la lectura", got, offset)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↓") {
		t.Fatalf("View() = %q, el reveal fuera de vista debe marcar actividad nueva", view)
	}
}

func TestModel_ReturningToBottomClearsNewActivityIndicatorAndResumesFollowing(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID: fmt.Sprintf("u%02d", i), Role: session.RoleUser, Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}
	m = apply(t, m, wheelUp)
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u30", Role: session.RoleUser, Text: "mensaje-30"}})

	for !m.viewport.AtBottom() {
		m = apply(t, m, wheelDown)
	}
	if view := ansi.Strip(m.View()); strings.Contains(view, "↓") {
		t.Fatalf("View() = %q, al volver al fondo debe ocultar el indicador", view)
	}

	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u31", Role: session.RoleUser, Text: "mensaje-31"}})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "mensaje-31") {
		t.Fatalf("View() = %q, al volver al fondo debe reanudar el seguimiento", view)
	}
}

func TestModel_NewActivityIndicatorIsPassiveAndAgentOnly(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID: fmt.Sprintf("u%02d", i), Role: session.RoleUser, Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}
	m = apply(t, m, wheelUp)

	// A local change of presentation is not new activity of the agent.
	m = m.syncViewport()
	if view := ansi.Strip(m.View()); strings.Contains(view, "↓") {
		t.Fatalf("View() = %q, un cambio local no debe mostrar el indicador", view)
	}

	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u30", Role: session.RoleUser, Text: "mensaje-30"}})
	beforeOffset := m.viewport.YOffset
	beforeView := m.View()
	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.viewport.Width - 1,
		Y:      m.viewport.Height - 1,
	})
	if got := m.viewport.YOffset; got != beforeOffset {
		t.Fatalf("viewport.YOffset = %d, want %d: la flecha debe ser pasiva", got, beforeOffset)
	}
	if got := m.View(); got != beforeView {
		t.Fatalf("View() cambio tras clicar la flecha pasiva:\nantes=%q\ndespues=%q", beforeView, got)
	}
}

func TestModel_MouseClickIsInert(t *testing.T) {
	// TRIANGULATE: with tea.WithMouseCellMotion the terminal ALSO sends clicks and drags, not just rolls. A left click or a movement must be inert: they do not resolve the pending permission, they do not write to the input and they do not change the view.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	for i := 0; i < 5; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})

	before := m.View()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion})

	if len(fake.resolved) != 0 {
		t.Fatalf("ResolvePermission fue llamado %d veces, un click NO debe disparar el gate de permisos", len(fake.resolved))
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, el click y el movimiento NO deben escribir en el textinput", got)
	}
	if got := m.View(); got != before {
		t.Fatalf("View() cambio tras el click/movimiento:\nantes = %q\ndespues = %q, los eventos de mouse que no son rueda deben ser inertes", before, got)
	}
}

func TestModel_WorkingIndicatorVisibleWhileRunning(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	// Without a run in progress there is no indicator.
	if got := m.View(); strings.Contains(got, "working") {
		t.Fatalf("View() = %q, sin corrida en curso NO debe verse el indicador de trabajo", got)
	}

	// The user sends a prompt: the stable indicator appears.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.View(); !strings.Contains(got, "working") {
		t.Fatalf("View() = %q, debe mostrar el indicador %q mientras la corrida sigue", got, "working")
	}

	// With ready (known terminal size) the indicator is also visible.
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	if got := m.View(); !strings.Contains(got, "working") {
		t.Fatalf("View() = %q, con ready el indicador %q tambien debe verse", got, "working")
	}

	// Clean end of run: the indicator disappears.
	m = apply(t, m, activeRunDone(m, ""))
	if got := m.View(); strings.Contains(got, "working") {
		t.Fatalf("View() = %q, RunDoneMsg debe retirar el indicador de trabajo", got)
	}
}

func TestModel_ViewFitsHeightWithIndicator(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	// Many more entries than fit in 12 lines.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	// Sending a prompt turns on working: the status line appears.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	if !strings.Contains(view, "working") {
		t.Fatalf("View() = %q, con corrida en curso debe verse el indicador %q", view, "working")
	}
	if lines := strings.Count(view, "\n") + 1; lines > 12 {
		t.Fatalf("View() tiene %d lineas, la linea de estado NO debe romper el alto acotado (<= 12)", lines)
	}
	if !strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, la vista debe seguir la cola (%q visible) aun con la linea de estado", view, "mensaje-29")
	}
}

// TestModel_WorkingIndicatorAlignsWithComposerLeftMargin covers the left margin of the "working" status line: the rest of the view (the composer box and the top bar) starts composerOuterMargin columns from the left edge of the terminal, but the spinner line starts in column 0 (attached to the edge). The spinner glyph should align with the "╭" edge of the composer box, both to composerOuterMargin columns.
func TestModel_WorkingIndicatorAlignsWithComposerLeftMargin(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "hola")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	// The status line is located by the spinner glyph, not by the word "working": this test is about column alignment, not the exact text of the line (that is an unrelated pre-existing bug, it is fixed separately in the GREEN phase).
	lines := strings.Split(view, "\n")
	spinnerCol := -1
	for _, line := range lines {
		plain := ansi.Strip(line)
		if strings.Contains(plain, m.spinner.View()) {
			spinnerCol = len(plain) - len(strings.TrimLeft(plain, " "))
			break
		}
	}
	if spinnerCol == -1 {
		t.Fatalf("View() = %q, no se encontro ninguna linea con el spinner %q", view, m.spinner.View())
	}

	composerCol := -1
	for _, line := range lines {
		plain := ansi.Strip(line)
		trimmed := strings.TrimLeft(plain, " ")
		if strings.HasPrefix(trimmed, "╭") {
			composerCol = len(plain) - len(trimmed)
			break
		}
	}
	if composerCol == -1 {
		t.Fatalf("View() = %q, no se encontro la linea del borde superior (╭) de la caja del composer", view)
	}

	if spinnerCol != composerCol {
		t.Fatalf("columna del spinner = %d, columna del borde ╭ del composer = %d; ambas deben coincidir (mismo margen izquierdo)", spinnerCol, composerCol)
	}
	if spinnerCol != composerOuterMargin {
		t.Fatalf("columna del spinner+%q = %d, se esperaba composerOuterMargin (%d)", "working", spinnerCol, composerOuterMargin)
	}
}

// TestModel_WorkingIndicatorAlignsWithComposerLeftMargin_WiderTerminal repeats the column alignment assertion with a different terminal width (100, not 40/80) to rule out the observed margin being a hardcoded value that only happens to match a particular run by chance: if the implementation calculated the spinner column from the terminal width (e.g. relative or proportional) instead of a fixed prefix of composerOuterMargin spaces, this case would detect it because it would still expect exactly composerOuterMargin regardless of the width. It also confirms that no line in the view exceeds the width of the terminal.
func TestModel_WorkingIndicatorAlignsWithComposerLeftMargin_WiderTerminal(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

	m = typeRunes(t, m, "hola")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	assertNoLineWiderThan(t, view, 100)

	lines := strings.Split(view, "\n")
	spinnerCol := -1
	for _, line := range lines {
		plain := ansi.Strip(line)
		if strings.Contains(plain, m.spinner.View()) {
			spinnerCol = len(plain) - len(strings.TrimLeft(plain, " "))
			break
		}
	}
	if spinnerCol == -1 {
		t.Fatalf("View() = %q, no se encontro ninguna linea con el spinner %q", view, m.spinner.View())
	}

	composerCol := -1
	for _, line := range lines {
		plain := ansi.Strip(line)
		trimmed := strings.TrimLeft(plain, " ")
		if strings.HasPrefix(trimmed, "╭") {
			composerCol = len(plain) - len(trimmed)
			break
		}
	}
	if composerCol == -1 {
		t.Fatalf("View() = %q, no se encontro la linea del borde superior (╭) de la caja del composer", view)
	}

	if spinnerCol != composerCol {
		t.Fatalf("columna del spinner = %d, columna del borde ╭ del composer = %d; ambas deben coincidir con ancho 100", spinnerCol, composerCol)
	}
	if spinnerCol != composerOuterMargin {
		t.Fatalf("columna del spinner+%q con ancho 100 = %d, se esperaba composerOuterMargin (%d), no un valor dependiente del ancho", "working", spinnerCol, composerOuterMargin)
	}
}

// TestModel_WorkingIndicatorDoesNotOverflowTinyTerminal covers a very small terminal (Width 10): chatContent() bounds the margin of the status line with `min(composerOuterMargin, m.chatContentWidth()/2)`, the same pattern that topBarLine uses for its margin, so no line in View() should exceed the width of the terminal (10 cells) or produce a negative indentation. If a future implementation reverted to an unbounded fixed prefix, this test would detect it.
func TestModel_WorkingIndicatorDoesNotOverflowTinyTerminal(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 10, Height: 24})

	m = typeRunes(t, m, "hola")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	assertNoLineWiderThan(t, view, 10)

	lines := strings.Split(view, "\n")
	for _, line := range lines {
		plain := ansi.Strip(line)
		if strings.Contains(plain, m.spinner.View()) {
			indent := len(plain) - len(strings.TrimLeft(plain, " "))
			if indent < 0 {
				t.Fatalf("View() = %q, la linea de estado tiene sangria negativa/corrupta (%d) en terminal minuscula", view, indent)
			}
		}
	}
}

func TestModel_SurvivesTinyTerminal(t *testing.T) {
	// Real bug (E2E under pty): a tiny terminal (0x0 when creating the pty, or 1 line) leaves the viewport height NEGATIVE in resizeViewport (m.height - m.reservedLines()) and bubbles/viewport panics (slice out of range in visibleLines) when doing SetContent/GotoBottom in syncViewport. Expected behavior (GREEN): without panic, View() returns a string (can be downgraded) and the program lives on.

	t.Run("pty 0x0", func(t *testing.T) {
		m := NewModel(nil, "s1", nil)

		// The newly created pty announces size 0x0.
		m = apply(t, m, tea.WindowSizeMsg{Width: 0, Height: 0})

		// An event that touches the viewport and render should not knock down the TUI.
		m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}})
		if got := m.View(); got == "" {
			t.Fatalf("View() = %q, con terminal 0x0 debe devolver un string aunque sea degradado", got)
		}
	})

	t.Run("terminal de 1 linea con corrida en curso", func(t *testing.T) {
		fake := &fakeAgent{}
		m := NewModel(fake, "s1", nil)

		// With 1 line high, turning on working (input + status line) leaves the reserved lines above the height: negative viewport.
		m = apply(t, m, tea.WindowSizeMsg{Width: 20, Height: 1})
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

		if len(fake.sent) != 1 {
			t.Fatalf("SendPrompt fue llamado %d veces, Enter debe enviar el prompt exactamente una vez", len(fake.sent))
		}
		m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}})
		if got := m.View(); got == "" {
			t.Fatalf("View() = %q, con terminal de 1 linea debe devolver un string aunque sea degradado", got)
		}
	})
}

func TestModel_RecoversAfterResizeFromTiny(t *testing.T) {
	// TRIANGULATE: a poor fix could "survive" the tiny terminal by freezing the viewport or leaving ready = false forever. This case requires that, after the terminal grows, the viewport shows the tail of the transcript again and continues limiting the height to that of the terminal.
	m := NewModel(nil, "s1", nil)

	// The newly created pty announces 0x0.
	m = apply(t, m, tea.WindowSizeMsg{Width: 0, Height: 0})

	// Folding 30 user messages with the terminal even 0x0 should not panic.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	// The terminal grows to a usable size.
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	view := m.View()
	if !strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, tras crecer la terminal debe volver a verse la cola del transcript (mensaje-29)", view)
	}
	if strings.Contains(view, "mensaje-00") {
		t.Fatalf("View() = %q, el alto debe seguir acotado: mensaje-00 no cabe en 10 lineas", view)
	}
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("View() tiene %d lineas, no debe exceder el alto de la terminal (10)", lines)
	}
}

func TestModel_WrapsLongAssistantTextToTerminalWidth(t *testing.T) {
	// Real bug (reproduced E2E): on a narrow terminal the assistant's response looks like ONE truncated line. The transcript is dumped raw into the bubbles viewport, which horizontally cuts each line to the width of the terminal (ansi.Cut) instead of doing word-wrap: the end of the text disappears from view.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	// The sentinel token has no hyphens: glamour v1 breaks lines at hyphens,
	// which would split it across the wrap and defeat the Contains assert.
	long := "esta es una respuesta larga del assistant que en una terminal angosta debe hacer wrap a varias lineas para leerse entera finrespuesta"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: long})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "finrespuesta") {
		t.Fatalf("View() = %q, el final del texto %q debe estar visible: el texto mas ancho que la terminal debe hacer wrap a varias lineas, no truncarse", view, "finrespuesta")
	}
	assertNoLineWiderThan(t, view, 40)
}

func TestModel_RewrapsOnResize(t *testing.T) {
	// TRIANGULATE: a poor fix could wrap the transcript ONE time to the first advertised width. When the terminal narrows, the text should be re-wrapped to the new width, not cut to the old width.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	// Hyphen-free sentinel: glamour v1 breaks lines at hyphens.
	long := "esta respuesta larga del assistant debe re-envolverse cuando la terminal cambia de ancho finrespuesta"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: long})
	m = drainReveal(t, m)

	// The terminal narrows: the transcript must be re-wrapped to 24 cells.
	m = apply(t, m, tea.WindowSizeMsg{Width: 24, Height: 10})

	view := m.View()
	if !strings.Contains(view, "finrespuesta") {
		t.Fatalf("View() = %q, el final del texto %q debe seguir visible tras el resize: el transcript debe re-envolverse al ancho nuevo", view, "finrespuesta")
	}
	assertNoLineWiderThan(t, view, 24)
}

func TestModel_WrapsUnbreakableLongToken(t *testing.T) {
	// TRIANGULATE: a word-wrap-only implementation does not split tokens without spaces longer than the width: a long URL would remain on a single line that the viewport truncates. The token must be divided into several lines to be read in its entirety.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	// A single 92-cell token with no spaces: hard cut to 40 gives lines of 40 + 40 + 12, and the distinctive suffix falls integer on the last line.
	url := "https://example.com/" + strings.Repeat("x", 60) + "sufijo-final"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: url})
	m = drainReveal(t, m)

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "sufijo-") || !strings.Contains(view, "final") {
		t.Fatalf("View() sin ANSI = %q, el sufijo final debe seguir visible aunque el renderer Markdown lo envuelva", view)
	}
	assertNoLineWiderThan(t, view, 40)
}

func TestModel_FollowsTailOfWrappedResponse(t *testing.T) {
	// TRIANGULATE: GotoBottom counts lines on the content already loaded in the viewport. If the transcript were wrapped AFTER SetContent, the line count would be short and the view would not follow the queue of a wrapped response that occupies more lines than the height of the viewport.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	// ~500 word cells: wrapped at 40 it occupies ~14 lines, more than the height of the viewport (9). Distinctive token at the beginning and another at the end.
	long := "inicio-de-respuesta " + strings.Repeat("palabra ", 60) + "fin-de-respuesta"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: long})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "fin-de-respuesta") {
		t.Fatalf("View() = %q, la vista debe seguir la cola: el final %q de la respuesta envuelta debe estar visible", view, "fin-de-respuesta")
	}
	if strings.Contains(view, "inicio-de-respuesta") {
		t.Fatalf("View() = %q, el inicio %q NO debe verse: la respuesta envuelta ocupa mas lineas que el viewport y la vista debe seguir la cola", view, "inicio-de-respuesta")
	}
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("View() tiene %d lineas, no debe exceder el alto de la terminal (10)", lines)
	}
}

func TestModel_ComposerBottomBorderShowsModel(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "openrouter/free")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})

	plain := ansi.Strip(m.View())
	lines := strings.Split(plain, "\n")
	var bottomBorder string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "╰") {
			bottomBorder = line
		}
	}
	if bottomBorder == "" {
		t.Fatalf("View() = %q, want a composer bottom border", plain)
	}
	if !strings.Contains(bottomBorder, "openrouter/free") {
		t.Fatalf("composer bottom border = %q, want model label %q", bottomBorder, "openrouter/free")
	}
	if strings.Contains(plain, "\nbuild · openrouter/free") {
		t.Fatalf("View() = %q, agent/model status must not render as a standalone footer", plain)
	}
	assertBoxLinesExactWidth(t, m.View(), 60)
}

func TestModel_ComposerHasTwoCellOuterMargin(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "model")
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 8})

	lines := strings.Split(ansi.Strip(m.View()), "\n")
	bottom := -1
	for index, line := range lines {
		if strings.Contains(line, "╰") {
			bottom = index
			if !strings.HasPrefix(line, "  ") || !strings.HasSuffix(line, "  ") {
				t.Fatalf("composer border = %q, want two-cell left and right margins", line)
			}
		}
	}
	if bottom == -1 {
		t.Fatalf("View() = %q, want composer bottom border", ansi.Strip(m.View()))
	}
	if bottom+2 >= len(lines) || strings.TrimSpace(lines[bottom+1]) != "" || strings.TrimSpace(lines[bottom+2]) != "" {
		t.Fatalf("lines after composer = %q, want two empty bottom rows", lines[bottom+1:])
	}
}

func TestModel_ComposerBottomBorderTruncatesLongModel(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5-very-long-model-name")
	m = apply(t, m, tea.WindowSizeMsg{Width: 32, Height: 12})

	plain := ansi.Strip(m.composerBox())
	lines := strings.Split(plain, "\n")
	bottomBorder := lines[len(lines)-1]
	if !strings.Contains(bottomBorder, "…") {
		t.Fatalf("composer bottom border = %q, want a truncated model label", bottomBorder)
	}
	if strings.Contains(bottomBorder, "very-long-model-name") {
		t.Fatalf("composer bottom border = %q, long model label must be truncated", bottomBorder)
	}
	assertBoxLinesExactWidth(t, m.composerBox(), 32)
}

func TestModel_ComposerBottomBorderKeepsPlanVisibleWhenModelIsTruncated(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5-very-long-model-name")
	m = apply(t, m, tea.WindowSizeMsg{Width: 32, Height: 12})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	plain := ansi.Strip(m.composerBox())
	bottomBorder := strings.Split(plain, "\n")[2]
	if !strings.Contains(bottomBorder, "… · plan") {
		t.Fatalf("composer bottom border = %q, truncated model must keep the active plan mode visible", bottomBorder)
	}
	assertBoxLinesExactWidth(t, m.composerBox(), 32)
}

func TestModel_ComposerBottomBorderOmitsModelWhenTerminalIsTooNarrow(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "model")
	m = apply(t, m, tea.WindowSizeMsg{Width: 6, Height: 8})

	plain := ansi.Strip(m.composerBox())
	lines := strings.Split(plain, "\n")
	bottomBorder := lines[len(lines)-1]
	if strings.Contains(bottomBorder, "model") || strings.Contains(bottomBorder, "…") {
		t.Fatalf("composer bottom border = %q, terminal too narrow must omit the model label", bottomBorder)
	}
	if !strings.HasPrefix(bottomBorder, "╰") || !strings.HasSuffix(bottomBorder, "╯") {
		t.Fatalf("composer bottom border = %q, rounded corners must remain intact", bottomBorder)
	}
	assertBoxLinesExactWidth(t, m.composerBox(), 6)
}

func TestModel_ComposerBordersKeepTokensAndModelWithoutShortcuts(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "openrouter/free")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	m = apply(t, m, EventMsg{
		Kind: session.KindStepEnded,
		Usage: &session.Usage{
			InputTokens:  1_234,
			OutputTokens: 345,
		},
	})

	plain := ansi.Strip(m.composerBox())
	lines := strings.Split(plain, "\n")
	if !strings.Contains(lines[0], "↑ 1.2k ↓ 345") {
		t.Fatalf("composer top border = %q, want token usage", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "openrouter/free") {
		t.Fatalf("composer bottom border = %q, want model label", lines[len(lines)-1])
	}
	for _, shortcut := range []string{"Shift+Tab", "Ctrl+.", "shortcuts"} {
		if strings.Contains(plain, shortcut) {
			t.Fatalf("composerBox() = %q, must not add shortcut hint %q", plain, shortcut)
		}
	}
	assertBoxLinesExactWidth(t, m.composerBox(), 60)
}

func TestModel_ComposerCtrlJInsertsNewlineAndEnterSubmitsMultilinePrompt(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m = typeRunes(t, m, "primera linea")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = typeRunes(t, m, "segunda linea")

	if got, want := m.input.Value(), "primera linea\nsegunda linea"; got != want {
		t.Fatalf("input.Value() = %q, Ctrl+J debe insertar un salto de linea y conservar el borrador %q", got, want)
	}
	if got := strings.Count(ansi.Strip(m.composerBox()), "\n"); got != 3 {
		t.Fatalf("composerBox() tiene %d saltos, con dos lineas debe crecer a cuatro lineas incluyendo bordes", got)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.sent) != 1 {
		t.Fatalf("SendPrompt fue llamado %d veces, Enter debe enviar el prompt multilinea exactamente una vez", len(fake.sent))
	}
	if got, want := fake.sent[0].text, "primera linea\nsegunda linea"; got != want {
		t.Fatalf("SendPrompt text = %q, want %q", got, want)
	}
}

func TestModel_ComposerMultilineRendersPromptOnlyOnFirstRow(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 32, Height: 12})
	m = typeRunes(t, m, "primera linea que se envuelve")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = typeRunes(t, m, "segunda linea")

	plain := ansi.Strip(m.composerBox())
	if got := strings.Count(plain, "❯"); got != 1 {
		t.Fatalf("composer prompt count = %d, want 1 across wrapped and explicit multiline rows:\n%s", got, plain)
	}
	if line := lineWith(t, plain, "primera linea"); !strings.Contains(line, "❯ primera linea") {
		t.Fatalf("first composer row = %q, the prompt must stay beside the first text row", line)
	}
}

func TestModel_ComposerGrowthStopsAtFiveLines(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	for line := 0; line < composerMaxLines+2; line++ {
		m = typeRunes(t, m, fmt.Sprintf("linea %d", line))
		if line < composerMaxLines+1 {
			m = apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
		}
	}

	if got := m.input.Height(); got != composerMaxLines {
		t.Fatalf("input.Height() = %d, el composer debe dejar de crecer en %d lineas", got, composerMaxLines)
	}
	if got := strings.Count(ansi.Strip(m.composerBox()), "\n"); got != composerMaxLines+1 {
		t.Fatalf("composerBox() tiene %d saltos, con el limite debe renderizar %d lineas incluyendo bordes", got, composerMaxLines+1)
	}
}

func TestModel_ComposerTopBorderShowsTokenUsage(t *testing.T) {
	m := NewModel(declaringAgent("anthropic/claude-sonnet-4.5", 200_000), "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	m = apply(t, m, EventMsg{
		Kind: session.KindStepEnded,
		Usage: &session.Usage{
			InputTokens:  1_234,
			OutputTokens: 345,
		},
	})

	plain := ansi.Strip(m.View())
	var topBorder string
	for _, line := range strings.Split(plain, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "╭") {
			topBorder = line
			break
		}
	}
	if topBorder == "" {
		t.Fatalf("View() = %q, want a composer top border", plain)
	}
	for _, want := range []string{"↑ 1.2k", "↓ 345", "ctx 1.2k/200k"} {
		if !strings.Contains(topBorder, want) {
			t.Fatalf("composer top border = %q, want it to contain %q", topBorder, want)
		}
	}
	assertBoxLinesExactWidth(t, m.View(), 60)
}

func TestModel_ComposerTokenUsageUpdatesDuringStreaming(t *testing.T) {
	m := NewModel(declaringAgent("anthropic/claude-sonnet-4.5", 200_000), "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	m = apply(t, m, EventMsg{
		Kind:  session.KindStepStarted,
		Usage: &session.Usage{InputTokens: 1_200},
	})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~1.2k ↓ 0 ctx ~1.2k/200k") {
		t.Fatalf("View() = %q, want live input usage at step start", view)
	}

	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: strings.Repeat("a", 3_000)})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~1.2k ↓ ~1k ctx ~1.2k/200k") {
		t.Fatalf("View() = %q, want live output usage after text delta", view)
	}

	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: strings.Repeat("b", 1_500)})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~1.2k ↓ ~1.5k ctx ~1.2k/200k") {
		t.Fatalf("View() = %q, want live output usage after reasoning delta", view)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolInputDelta, Text: strings.Repeat("c", 1_500)})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~1.2k ↓ ~2k ctx ~1.2k/200k") {
		t.Fatalf("View() = %q, want live output usage after tool input delta", view)
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindStepEnded,
		Usage: &session.Usage{
			InputTokens:     1_300,
			OutputTokens:    900,
			ReasoningTokens: 100,
		},
	})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ 1.3k ↓ 900 ctx 1.3k/200k") {
		t.Fatalf("View() = %q, want exact provider usage after step end", view)
	}
}

func TestModel_LiveUsageTransitions(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m.outputBytes = 9
	m.reasoningBytes = 12
	m.toolInputBytes = 15
	m = m.foldEvent(EventMsg{Kind: session.KindStepStarted, Usage: &session.Usage{InputTokens: 20}})
	if !m.liveUsage || m.outputBytes != 0 || m.reasoningBytes != 0 || m.toolInputBytes != 0 {
		t.Fatalf("StepStarted = live:%v bytes:%d/%d/%d, quiero uso vivo con contadores reiniciados", m.liveUsage, m.outputBytes, m.reasoningBytes, m.toolInputBytes)
	}

	m = m.foldEvent(EventMsg{Kind: session.KindTextDelta, Text: "abcdef"})
	estimated := *m.usage
	m = m.foldEvent(EventMsg{Kind: session.KindStepEnded})
	if m.liveUsage || *m.usage != estimated {
		t.Fatalf("StepEnded sin Usage = live:%v usage:%+v, quiero conservar estimacion %+v y cerrar uso vivo", m.liveUsage, *m.usage, estimated)
	}

	m.liveUsage = true
	m = m.foldEvent(EventMsg{Kind: session.KindStepFailed, Error: "boom"})
	if m.liveUsage {
		t.Fatal("StepFailed debe cerrar el uso vivo")
	}
}

func TestModel_UpdateLiveUsageRequiresActiveUsage(t *testing.T) {
	for _, m := range []Model{
		{Transcript: Transcript{liveUsage: false, usage: &session.Usage{OutputTokens: 7}, outputBytes: 30}},
		{Transcript: Transcript{liveUsage: true, usage: nil, outputBytes: 30}},
	} {
		beforeUsage := m.usage
		m = m.updateLiveUsage()
		if m.usage != beforeUsage || m.outputBytes != 30 {
			t.Fatalf("updateLiveUsage() modifico un modelo sin uso activo: %+v", m)
		}
	}
}

func TestEstimatedTokens(t *testing.T) {
	for _, tc := range []struct{ bytes, want int }{{0, 0}, {1, 1}, {2, 1}, {3, 1}, {30_000, 10_000}} {
		if got := estimatedTokens(tc.bytes); got != tc.want {
			t.Errorf("estimatedTokens(%d) = %d, quiero %d", tc.bytes, got, tc.want)
		}
	}
}

func TestModel_ComposerDistinguishesEstimatedAndExactInputUsage(t *testing.T) {
	m := NewModel(declaringAgent("anthropic/claude-sonnet-4.5", 200_000), "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	m = apply(t, m, EventMsg{
		Kind:  session.KindStepStarted,
		Usage: &session.Usage{InputTokens: 10_000},
	})

	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~10k") || !strings.Contains(view, "ctx ~10k/200k") {
		t.Fatalf("live View() = %q, want the conservative 10k estimate marked as approximate", view)
	}

	m = apply(t, m, EventMsg{
		Kind:  session.KindStepEnded,
		Usage: &session.Usage{InputTokens: 9_100, OutputTokens: 250},
	})

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "↑ 9.1k") || !strings.Contains(view, "ctx 9.1k/200k") {
		t.Fatalf("completed View() = %q, want exact provider usage 9.1k", view)
	}
	if strings.Contains(view, "~9.1k") {
		t.Fatalf("completed View() = %q, exact provider usage must not be marked approximate", view)
	}
}

func TestModel_ComposerTokenUsageHandlesUnknownModelAndNarrowWidth(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "custom/model")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	m = apply(t, m, EventMsg{
		Kind:  session.KindStepEnded,
		Usage: &session.Usage{InputTokens: 10_000, OutputTokens: 2_500},
	})

	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "↑ 10k ↓ 2.5k") {
		t.Fatalf("View() = %q, want compact input and output token counts", plain)
	}
	if strings.Contains(plain, "ctx") {
		t.Fatalf("View() = %q, unknown models must not show a made-up context window", plain)
	}

	m = apply(t, m, EventMsg{
		Kind:  session.KindStepEnded,
		Usage: &session.Usage{InputTokens: 20_000, OutputTokens: 3_000},
	})
	if plain = ansi.Strip(m.View()); strings.Contains(plain, "↑ 10k") || !strings.Contains(plain, "↑ 20k ↓ 3k") {
		t.Fatalf("View() = %q, want the latest completed step usage", plain)
	}

	m = apply(t, m, tea.WindowSizeMsg{Width: 10, Height: 8})
	assertBoxLinesExactWidth(t, m.View(), 10)
	if plain := ansi.Strip(m.composerBox()); strings.Contains(plain, "↑") {
		t.Fatalf("composerBox() = %q, una caja demasiado estrecha debe omitir la etiqueta", plain)
	}
}

func TestModel_ComposerBoxWithoutUsageHasNoTokenLabel(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	if plain := ansi.Strip(m.composerBox()); strings.Contains(plain, "↑") || strings.Contains(plain, "↓") {
		t.Fatalf("composerBox() = %q, sin usage no debe mostrar tokens", plain)
	}
}

func TestFormatTokenCount(t *testing.T) {
	for _, tc := range []struct {
		tokens int
		want   string
	}{
		{0, "0"}, {999, "999"}, {1_000, "1k"}, {1_500, "1.5k"},
		{9_999, "10k"}, {10_000, "10k"}, {128_000, "128k"},
	} {
		if got := formatTokenCount(tc.tokens); got != tc.want {
			t.Errorf("formatTokenCount(%d) = %q, quiero %q", tc.tokens, got, tc.want)
		}
	}
}

func TestModel_ComposerBoxWrapsInput(t *testing.T) {
	// TRIANGULATE: the input ALWAYS lives inside a rounded-edge box that spans the width of the terminal (Claude Code style), whether or not the composer's status is set.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	view := m.View()
	for _, want := range []string{"╭", "╰"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, el input debe renderizarse dentro de una caja de borde redondeado: falta %q", view, want)
		}
	}
	assertBoxLinesExactWidth(t, view, 40)

	// The box has horizontal padding (Claude Code style): the inner line starts with "│ ❯" (border, space, prompt), not with the prompt attached to the edge. It is measured without ANSI because the prompt is stylized.
	if plain := ansi.Strip(view); !strings.Contains(plain, "│ ❯") {
		t.Fatalf("View() sin ANSI = %q, la linea interior de la caja debe tener padding horizontal: debe contener %q (borde, espacio, prompt), no el prompt pegado al borde", plain, "│ ❯")
	}

	topAt, inputAt, bottomAt := -1, -1, -1
	for i, line := range strings.Split(view, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		switch {
		case strings.HasPrefix(trimmed, "╭"):
			topAt = i
		case strings.HasPrefix(trimmed, "╰"):
			bottomAt = i
		case strings.Contains(line, inputPrompt):
			inputAt = i
		}
	}
	if topAt == -1 || inputAt == -1 || bottomAt == -1 || topAt >= inputAt || inputAt >= bottomAt {
		t.Fatalf("View() = %q, la linea del input (%q en %d) debe quedar ENTRE el borde superior (╭ en %d) y el inferior (╰ en %d)", view, inputPrompt, inputAt, topAt, bottomAt)
	}

	// With set status the foot is BELOW the bottom edge of the box.
	m2 := NewModel(nil, "s1", nil).WithStatus("build", "openrouter/free")
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 40, Height: 12})
	view2 := m2.View()
	bottomAt2 := strings.Index(view2, "╰")
	footerAt := strings.Index(view2, "openrouter/free")
	if bottomAt2 == -1 || footerAt == -1 || footerAt < bottomAt2 {
		t.Fatalf("View() = %q, el pie de status (openrouter/free en %d) debe aparecer DESPUES del borde inferior de la caja (╰ en %d)", view2, footerAt, bottomAt2)
	}
}

func TestModel_ComposerBoxFollowsResize(t *testing.T) {
	// TRIANGULATE: a box hardcoded to the first advertised width is useless; After resizing the terminal, each line of the box must measure the new width.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
	assertBoxLinesExactWidth(t, m.View(), 40)

	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	assertBoxLinesExactWidth(t, m.View(), 60)
}

func TestModel_ViewFitsHeightWithBoxModelAndIndicator(t *testing.T) {
	// TRIANGULATE: with the box (3 lines), the model on the edge and the work indicator on at the same time, the height is still limited to that of the terminal and the view follows the tail of the transcript.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "openrouter/free")
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	// Many more entries than fit in 12 lines.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	// Sending a prompt turns on working: the indicator appears on the box.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	if lines := strings.Count(view, "\n") + 1; lines > 12 {
		t.Fatalf("View() tiene %d lineas, caja + pie + indicador no deben romper el alto acotado (<= 12)", lines)
	}
	for _, want := range []string{"mensaje-29", "working", "openrouter/free"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, debe contener %q (cola del transcript, indicador de trabajo y pie de status)", view, want)
		}
	}
	assertBoxLinesExactWidth(t, view, 40)
}

func TestModel_LongTypedPromptGrowsWithoutOverflowingTerminal(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 24, Height: 10})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("a", 80))})

	view := m.View()
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("View() tiene %d lineas, un prompt largo no debe romper el alto acotado (<= 10)", lines)
	}
	if got := strings.Count(view, "❯"); got != 1 {
		t.Fatalf("View() = %q, el prompt %q debe aparecer exactamente una vez aunque el texto se envuelva (count=%d)", view, "❯", got)
	}
	interior := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "│") {
			interior++
		}
	}
	if interior != 3 {
		t.Fatalf("View() = %q, la caja debe crecer a las 3 filas visuales que caben en esta terminal (lineas │ = %d)", view, interior)
	}
	assertNoLineWiderThan(t, view, 24)
	assertBoxLinesExactWidth(t, view, 24)
}

func TestModel_TabTogglesAgentModeToPlan(t *testing.T) {
	// Tab toggles the agent mode between "build" and "plan" (Claude Code style): the composer footer reflects the live mode and Enter sends the prompt via the active mode path (SendPlanPrompt in plan).
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "openrouter/free")

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	view := m.View()
	if !strings.Contains(view, "openrouter/free · plan") {
		t.Fatalf("View() = %q, tras Tab el pie del composer debe mostrar %q", view, "openrouter/free · plan")
	}
	if strings.Contains(view, "build ·") {
		t.Fatalf("View() = %q, tras Tab el pie NO debe seguir mostrando %q", view, "build ·")
	}

	// In plan mode, Enter sends the prompt via SendPlanPrompt, not via SendPrompt.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("investiga x")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.planSent) != 1 {
		t.Fatalf("SendPlanPrompt fue llamado %d veces, Enter en modo plan debe enviar el prompt exactamente una vez por el camino de plan", len(fake.planSent))
	}
	if got := fake.planSent[0]; got.sessionID != "s1" || got.text != "investiga x" {
		t.Fatalf("SendPlanPrompt(%q, %q), se esperaba SendPlanPrompt(%q, %q)", got.sessionID, got.text, "s1", "investiga x")
	}
	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, en modo plan el prompt NO debe ir por el camino de build", len(fake.sent))
	}
}

func TestModel_TabTogglesBackToBuild(t *testing.T) {
	// TRIANGULATE: Tab TOggles the mode, not just turns it on. Two Tab returns the composer footer to build and Enter sends again via SendPrompt (the normal path), not SendPlanPrompt.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	view := m.View()
	if !strings.Contains(view, " m ─╯") {
		t.Fatalf("View() = %q, tras Tab Tab el borde del composer debe volver a mostrar solo el modelo %q", view, "m")
	}
	if strings.Contains(view, "· plan") {
		t.Fatalf("View() = %q, tras Tab Tab el pie NO debe seguir mostrando %q", view, "· plan")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hazlo")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 1 {
		t.Fatalf("SendPrompt fue llamado %d veces, de vuelta en build Enter debe enviar exactamente una vez por el camino normal", len(fake.sent))
	}
	if got := fake.sent[0]; got.sessionID != "s1" || got.text != "hazlo" {
		t.Fatalf("SendPrompt(%q, %q), se esperaba SendPrompt(%q, %q)", got.sessionID, got.text, "s1", "hazlo")
	}
	if len(fake.planSent) != 0 {
		t.Fatalf("SendPlanPrompt fue llamado %d veces, tras volver a build el prompt NO debe ir por el camino de plan", len(fake.planSent))
	}
}

func TestModel_TabIsInertWhilePermissionPending(t *testing.T) {
	// TRIANGULATE: with a pending permission the keyboard is in approval mode (only y/n do anything): Tab should NOT toggle the agent mode or change the composer footer.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	view := m.View()
	if !strings.Contains(view, " m ─╯") {
		t.Fatalf("View() = %q, con permiso pendiente Tab NO debe cambiar el borde: debe seguir mostrando el modelo %q", view, "m")
	}
	if strings.Contains(view, "· plan") {
		t.Fatalf("View() = %q, con permiso pendiente Tab NO debe activar el modo plan", view)
	}
}

func TestModel_PresentPlanOffersAcceptAndYExecutes(t *testing.T) {
	// When the agent presents a plan (tool present_plan successfully posted), the conversation displays a pending approval offer; the 'y' key accepts the plan via Agent.AcceptPlan and withdraws the offer.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "p1"})

	view := m.View()
	planLine := lineWith(t, view, "? Plan")
	if !strings.Contains(planLine, "(y ejecutar / n seguir en plan)") {
		t.Fatalf("oferta de aprobacion = %q, debe contener %q", planLine, "(y ejecutar / n seguir en plan)")
	}

	// 'and' accept the plan: ONE call to AcceptPlan with the TUI session.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	if len(fake.accepted) != 1 {
		t.Fatalf("AcceptPlan fue llamado %d veces, 'y' debe aceptar el plan exactamente una vez", len(fake.accepted))
	}
	if got := fake.accepted[0]; got != "s1" {
		t.Fatalf("AcceptPlan(%q), se esperaba AcceptPlan(%q)", got, "s1")
	}
	if got := m.View(); strings.Contains(got, "? Plan") {
		t.Fatalf("View() = %q, aceptar el plan debe retirar la oferta de aprobacion", got)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, aceptar el plan NO debe enviar un prompt por el camino de build", len(fake.sent))
	}
	if len(fake.planSent) != 0 {
		t.Fatalf("SendPlanPrompt fue llamado %d veces, aceptar el plan NO debe enviar un prompt por el camino de plan", len(fake.planSent))
	}
}

func TestModel_PlanApprovalNRejectsAndStaysInPlanMode(t *testing.T) {
	// TRIANGULATE: 'n' discards the approval offer WITHOUT touching the mode or accepting anything: the footer remains in plan and the next Enter continues going through SendPlanPrompt. A broken implementation that turns off planMode (or calls AcceptPlan) on rejection should fall here.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab}) // a plan-mode
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "p1"})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})

	if got := m.View(); strings.Contains(got, "? Plan") {
		t.Fatalf("View() = %q, 'n' debe retirar la oferta de aprobacion del plan", got)
	}
	if len(fake.accepted) != 0 {
		t.Fatalf("AcceptPlan fue llamado %d veces, 'n' NO debe aceptar el plan", len(fake.accepted))
	}
	if got := m.View(); !strings.Contains(got, "m · plan") {
		t.Fatalf("View() = %q, tras 'n' el pie debe seguir mostrando %q: rechazar la oferta no cambia el modo", got, "m · plan")
	}

	// The next shipment continues to follow the plan path: the mode did not change.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ajusta el plan")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.planSent) != 1 {
		t.Fatalf("SendPlanPrompt fue llamado %d veces, tras 'n' Enter debe seguir enviando por el camino de plan exactamente una vez", len(fake.planSent))
	}
	if got := fake.planSent[0]; got.sessionID != "s1" || got.text != "ajusta el plan" {
		t.Fatalf("SendPlanPrompt(%q, %q), se esperaba SendPlanPrompt(%q, %q)", got.sessionID, got.text, "s1", "ajusta el plan")
	}
	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, tras 'n' el prompt NO debe ir por el camino de build", len(fake.sent))
	}
}

func TestModel_PlanApprovalCapturesKeyboard(t *testing.T) {
	// TRIANGULATE: with the plan offer pending the keyboard is in approval mode: the normal runes DO NOT feed the input and Enter does NOT send anything. 'and' then accepts: the foot builds again and the run continues working until RunDoneMsg.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab}) // a plan-mode
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "p1"})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, las runas normales NO deben entrar al input mientras hay plan pendiente", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.sent) != 0 || len(fake.planSent) != 0 || len(fake.accepted) != 0 {
		t.Fatalf("sent=%d planSent=%d accepted=%d, ni Enter ni las runas normales deben enviar o aceptar nada con plan pendiente", len(fake.sent), len(fake.planSent), len(fake.accepted))
	}

	// 'y' accepts: build again and the run is in progress.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(fake.accepted) != 1 || fake.accepted[0] != "s1" {
		t.Fatalf("accepted = %v, 'y' debe llamar AcceptPlan(%q) exactamente una vez", fake.accepted, "s1")
	}
	view := m.View()
	if !strings.Contains(view, " m ─╯") {
		t.Fatalf("View() = %q, tras aceptar el plan el borde debe volver a mostrar solo el modelo %q", view, "m")
	}
	if strings.Contains(view, "· plan") {
		t.Fatalf("View() = %q, tras aceptar el plan el pie NO debe seguir mostrando %q", view, "· plan")
	}
	if !strings.Contains(view, "working") {
		t.Fatalf("View() = %q, tras aceptar el plan la corrida queda en curso: debe verse el indicador %q", view, "working")
	}
}

func TestModel_PresentPlanFailedDoesNotOfferApproval(t *testing.T) {
	// Fine point: a present_plan set with Tool.Failed does NOT offer approval and the keyboard remains normal (the rune goes to the input and 'y' does not accept anything).
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "p1", Error: "plan invalido"})

	if got := m.View(); strings.Contains(got, "? Plan") {
		t.Fatalf("View() = %q, un present_plan fallido NO debe ofrecer la aprobacion del plan", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(fake.accepted) != 0 {
		t.Fatalf("AcceptPlan fue llamado %d veces, sin oferta pendiente 'y' NO debe aceptar nada", len(fake.accepted))
	}
	if got := m.input.Value(); got != "y" {
		t.Fatalf("input.Value() = %q, sin oferta pendiente la runa 'y' debe ir al input normal", got)
	}
}

func TestModel_EnterSendsTypedPromptViaAgent(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	// The user types "hello" and presses Enter.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 1 {
		t.Fatalf("SendPrompt fue llamado %d veces, Enter debe enviar el prompt exactamente una vez", len(fake.sent))
	}
	if got := fake.sent[0]; got.sessionID != "s1" || got.text != "hola" {
		t.Fatalf("SendPrompt(%q, %q), se esperaba SendPrompt(%q, %q)", got.sessionID, got.text, "s1", "hola")
	}
	if !m.Working() {
		t.Fatalf("Working() = false, el modelo debe quedar trabajando tras enviar el prompt hasta RunDoneMsg")
	}
}

func TestModel_SendFailuresKeepPendingUserAction(t *testing.T) {
	t.Run("build prompt", func(t *testing.T) {
		fake := &fakeAgent{sendErr: errors.New("send failed")}
		m := typeRunes(t, NewModel(fake, "s1", nil), "hola")

		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)

		if cmd != nil || m.Working() || m.input.Value() != "hola" {
			t.Fatalf("cmd=%v working=%v composer=%q", cmd != nil, m.Working(), m.input.Value())
		}
		if got := m.entries[len(m.entries)-1]; got.kind != entryError || got.text != "send failed" {
			t.Fatalf("last entry = %+v", got)
		}
	})

	t.Run("plan prompt", func(t *testing.T) {
		fake := &fakeAgent{planErr: errors.New("plan send failed")}
		m := NewModel(fake, "s1", nil)
		m.planMode = true
		m = typeRunes(t, m, "investiga")

		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)

		if cmd != nil || m.Working() || !m.planMode || m.input.Value() != "investiga" {
			t.Fatalf("cmd=%v working=%v planMode=%v composer=%q", cmd != nil, m.Working(), m.planMode, m.input.Value())
		}
		if got := m.entries[len(m.entries)-1]; got.kind != entryError || got.text != "plan send failed" {
			t.Fatalf("last entry = %+v", got)
		}
	})

	t.Run("plan approval", func(t *testing.T) {
		fake := &fakeAgent{acceptErr: errors.New("accept failed")}
		m := NewModel(fake, "s1", nil)
		m.planMode = true
		m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
		m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "p1"})

		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
		m = updated.(Model)

		if cmd != nil || m.Working() || !m.planMode || !m.hasPendingPlan() {
			t.Fatalf("cmd=%v working=%v planMode=%v pendingPlan=%v", cmd != nil, m.Working(), m.planMode, m.hasPendingPlan())
		}
		if got := m.entries[len(m.entries)-1]; got.kind != entryError || got.text != "accept failed" {
			t.Fatalf("last entry = %+v", got)
		}
	})
}

// menuCommands are the commands shared by the menu "/" tests.
var menuCommands = []command.Command{
	{Name: "new", Description: "Start a new session", BuiltIn: true},
	{Name: "commit", Description: "genera un commit"},
	{Name: "model", Description: "Select provider and model", BuiltIn: true},
	{Name: "compact", Description: "Compact conversation context", BuiltIn: true},
	{Name: "mcp", Description: "Toggle MCP servers on or off", BuiltIn: true},
	{Name: "connect", Description: "Connect a provider with an API key", BuiltIn: true},
	{Name: "review", Description: "revisa el diff"},
}

func withMenuBuiltins(commands ...command.Command) []command.Command {
	return append([]command.Command{
		{Name: "new", Description: "Start a new session", BuiltIn: true},
		{Name: "model", Description: "Select provider and model", BuiltIn: true},
	}, commands...)
}

// typeRunes feeds input rune by rune, like real keystrokes.
func typeRunes(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			msg.Type = tea.KeySpace // bubbletea reporta el espacio como KeySpace
		}
		m = apply(t, m, msg)
	}
	return m
}

// menuSelectedLine returns the menu line marked with "❯ " (prefix at the beginning of the line, without ANSI), or "" if there is none. The composer's line is not confusing: it starts with the border "│", not with the marker.
func menuSelectedLine(view string) string {
	for _, line := range strings.Split(view, "\n") {
		if plain := ansi.Strip(line); strings.HasPrefix(plain, "❯ ") {
			return plain
		}
	}
	return ""
}

func TestModel_CommandMenuFiltersAsYouType(t *testing.T) {
	// The menu is recomputed with each key: typing "/", "c", "o" filters the candidates with the ranking of filterCommands (name prefix first): only /commit remains and /review disappears from the popup.
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/")
	view := m.View()
	lineWith(t, view, "/commit")
	lineWith(t, view, "/review")

	m = typeRunes(t, m, "co")
	view = m.View()
	commitLine := lineWith(t, view, "/commit")
	if !strings.Contains(commitLine, "genera un commit") {
		t.Fatalf("linea de /commit = %q, el item filtrado debe conservar su descripcion", commitLine)
	}
	if strings.Contains(view, "/review") {
		t.Fatalf("View() = %q, tras teclear %q el menu NO debe seguir mostrando %q", view, "/co", "/review")
	}
	if got := menuSelectedLine(view); !strings.Contains(got, "/commit") {
		t.Fatalf("linea seleccionada del menu = %q, el unico candidato /commit debe quedar seleccionado", got)
	}
}

func TestModel_SlashMenuIncludesCompactBuiltin(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeRunes(t, m, "/comp")
	line := lineWith(t, m.View(), "/compact")
	if !strings.Contains(line, "Compact conversation context") {
		t.Fatalf("compact line = %q", line)
	}
}

func TestModel_CompactSubmitsWithoutPromptHistoryOrWorkingSpinner(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeRunes(t, m, "/compact")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 1 || fake.sent[0].text != "/compact" {
		t.Fatalf("sent = %+v", fake.sent)
	}
	if len(m.history) != 0 {
		t.Fatalf("history = %v, /compact must not enter prompt history", m.history)
	}
	if m.Working() {
		t.Fatal("Working() = true, compact status must own progress")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input = %q, want cleared", got)
	}
}

func TestModel_CompactStatusDeduplicatesAndResolves(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, CompactionStatusMsg{SessionID: "s1", State: CompactionQueued})
	m = apply(t, m, CompactionStatusMsg{SessionID: "s1", State: CompactionQueued})
	if got := strings.Count(ansi.Strip(m.View()), "Compaction queued"); got != 1 {
		t.Fatalf("queued count = %d, view = %q", got, m.View())
	}
	m = apply(t, m, CompactionStatusMsg{SessionID: "s1", State: CompactionRunning})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Compacting context") || strings.Contains(view, "Compaction queued") {
		t.Fatalf("running view = %q", view)
	}
	m = apply(t, m, EventMsg{SessionID: "s1", Kind: session.KindContextCompacted, Compaction: &session.CompactionCheckpoint{
		Summary: session.StructuredSummary{CurrentGoal: "continue"},
	}})
	view = ansi.Strip(m.View())
	if !strings.Contains(view, "Context compacted") || strings.Contains(view, "Compacting context") {
		t.Fatalf("completed view = %q", view)
	}
}

func TestModel_SeparateDurableCompactionsRemainSeparateEntries(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, EventMsg{SessionID: "s1", Kind: session.KindContextCompacted})
	m = apply(t, m, EventMsg{SessionID: "s1", Message: &session.Message{Role: session.RoleUser, Text: "later work"}})
	m = apply(t, m, EventMsg{SessionID: "s1", Kind: session.KindContextCompacted})

	count := 0
	for _, entry := range m.entries {
		if entry.kind == entryCompaction {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("compaction entries = %d, want 2", count)
	}
}

func TestModel_NewCompactionAfterResolvedNoopCreatesNewEntry(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, CompactionStatusMsg{SessionID: "s1", State: CompactionQueued})
	m = apply(t, m, CompactionStatusMsg{SessionID: "s1", State: CompactionNotNeeded})
	m = apply(t, m, CompactionStatusMsg{SessionID: "s1", State: CompactionQueued})

	var states []string
	for _, entry := range m.entries {
		if entry.kind == entryCompaction {
			states = append(states, entry.text)
		}
	}
	want := []string{"Not enough context to compact", "Compaction queued"}
	if !slices.Equal(states, want) {
		t.Fatalf("compaction states = %v, want %v", states, want)
	}
}

func TestModel_CompactStatusNotNeededAndFailure(t *testing.T) {
	notNeeded := NewModel(nil, "s1", nil)
	notNeeded = apply(t, notNeeded, CompactionStatusMsg{SessionID: "s1", State: CompactionQueued})
	notNeeded = apply(t, notNeeded, CompactionStatusMsg{SessionID: "s1", State: CompactionNotNeeded})
	if view := ansi.Strip(notNeeded.View()); !strings.Contains(view, "Not enough context to compact") {
		t.Fatalf("not-needed view = %q", view)
	}

	failed := NewModel(nil, "s1", nil)
	failed = apply(t, failed, CompactionStatusMsg{SessionID: "s1", State: CompactionRunning})
	failed = apply(t, failed, CompactionStatusMsg{SessionID: "s1", State: CompactionFailed, Err: "provider unavailable"})
	if view := ansi.Strip(failed.View()); !strings.Contains(view, "provider unavailable") || !strings.Contains(view, "[error]") {
		t.Fatalf("failed view = %q", view)
	}
}

func TestModel_CompactStatusForOtherSessionIsIgnored(t *testing.T) {
	m := NewModel(nil, "visible", nil)
	m = apply(t, m, CompactionStatusMsg{SessionID: "other", State: CompactionQueued})
	if view := ansi.Strip(m.View()); strings.Contains(view, "Compaction queued") {
		t.Fatalf("other session status leaked into view: %q", view)
	}
}

func TestModel_CommandMenuClosesOnSpace(t *testing.T) {
	// The first space closes the menu: what follows the name are the command args and the popup should no longer cover the conversation.
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/commit")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/commit") {
		t.Fatalf("linea seleccionada del menu = %q, con %q tecleado el menu debe estar abierto sobre /commit", got, "/commit")
	}

	m = typeRunes(t, m, " ")
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, el espacio debe cerrar el menu (lo que sigue son los args)", got)
	}
}

func TestModel_MenuKeysNavigateSelection(t *testing.T) {
	// With the menu open, Up/Down move the "❯" marker cyclically and are captured by the popup: they do not scroll the viewport or write to the input.
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Transcript longer than viewport: view follows queue (message-29).
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	m = typeRunes(t, m, "/")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/new") {
		t.Fatalf("linea seleccionada del menu = %q, el comando integrado /new debe arrancar seleccionado", got)
	}

	// Local commands lead the initial menu, so move past all five to the first skill.
	for range 5 {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/commit") {
		t.Fatalf("linea seleccionada del menu = %q, Down debe mover el marcador a la skill /commit", got)
	}

	// Entering a skill preserves the fill-with-space flow.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "/commit " {
		t.Fatalf("input.Value() = %q, Enter sobre una skill debe completarla con espacio para argumentos", got)
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, completar una skill debe cerrar el menu", got)
	}
	if got := len(m.agent.(*fakeAgent).sent); got != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, Enter sobre una skill solo debe completarla", got)
	}

	// In a fresh menu, Up from /new cycles to the last item.
	mCycle := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(menuCommands, nil)
	mCycle = apply(t, mCycle, tea.WindowSizeMsg{Width: 80, Height: 24})
	mCycle = typeRunes(t, mCycle, "/")
	mCycle = apply(t, mCycle, tea.KeyMsg{Type: tea.KeyUp})
	if got := menuSelectedLine(mCycle.View()); !strings.Contains(got, "/review") && !strings.Contains(got, "/cache-stats") {
		t.Fatalf("linea seleccionada del menu = %q, Up en /new debe ciclar al ultimo item", got)
	}

	// Down in the last one returns to the integrated command.
	mCycle = apply(t, mCycle, tea.KeyMsg{Type: tea.KeyDown})
	if got := menuSelectedLine(mCycle.View()); !strings.Contains(got, "/new") {
		t.Fatalf("linea seleccionada del menu = %q, Down en el ultimo item debe ciclar al primero (/new)", got)
	}

	// The arrows remained in the second popup: they do not write in the input.
	view := m.View()
	if !strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, con menu abierto Up/Down NO deben scrollear el viewport: la cola (mensaje-29) debe seguir visible", view)
	}
	if got := mCycle.input.Value(); got != "/" {
		t.Fatalf("input.Value() = %q, Up/Down con menu abierto NO deben escribir en el input", got)
	}
}

func TestModel_TabAppliesSelectedCommand(t *testing.T) {
	// With the menu open, Tab applies the selection (mirror of applyCommand in command.ts): replaces the "/co" token with "/commit " with the caret after the space, ready for the args. The recompute sees the space and closes the menu. Tab with menu open DOES NOT toggle plan-mode.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m").WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/co")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/commit") {
		t.Fatalf("linea seleccionada del menu = %q, con %q tecleado /commit debe estar seleccionado", got, "/co")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	if got := m.input.Value(); got != "/commit " {
		t.Fatalf("input.Value() = %q, Tab debe reemplazar el token por %q (comando + espacio para los args)", got, "/commit ")
	}
	if got := m.input.Position(); got != len("/commit ") {
		t.Fatalf("input.Position() = %d, el caret debe quedar tras el espacio (%d)", got, len("/commit "))
	}
	view := m.View()
	if got := menuSelectedLine(view); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, aplicar el comando debe cerrar el menu (el recomputo ve el espacio)", got)
	}
	if !strings.Contains(view, " m ─╯") || strings.Contains(view, "· plan") {
		t.Fatalf("View() = %q, Tab con menu abierto NO debe alternar el plan-mode: el borde debe seguir mostrando el modelo %q", view, "m")
	}
}

func TestModel_EnterAppliesSelectionInsteadOfSending(t *testing.T) {
	// With the menu open, Enter applies the selection the same as Tab and does NOT send anything; the second Enter (menu already closed) if you send the text as is.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/co")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, Enter con menu abierto debe aplicar la seleccion, NO enviar", len(fake.sent))
	}
	if got := m.input.Value(); got != "/commit " {
		t.Fatalf("input.Value() = %q, Enter con menu abierto debe aplicar la seleccion (%q)", got, "/commit ")
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, aplicar la seleccion debe cerrar el menu", got)
	}

	// Closed menu: the second Enter sends the text as is via SendPrompt.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.sent) != 1 {
		t.Fatalf("SendPrompt fue llamado %d veces, con el menu cerrado Enter debe enviar exactamente una vez", len(fake.sent))
	}
	if got := fake.sent[0]; got.sessionID != "s1" || got.text != "/commit " {
		t.Fatalf("SendPrompt(%q, %q), se esperaba SendPrompt(%q, %q): el texto se envia tal cual", got.sessionID, got.text, "s1", "/commit ")
	}
}

func TestModel_EscClosesMenuWithoutStopping(t *testing.T) {
	// With the menu open, Esc closes the popup WITHOUT stopping the run and without touching the input text; Typing another rune recomputes and reopens the menu. With the menu closed, two Esc confirm the cancellation.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithCompletions(menuCommands, nil)
	m.working = true
	m.activeRun = 1
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/c")
	if got := menuSelectedLine(m.View()); got == "" {
		t.Fatalf("View() = %q, con %q tecleado el menu debe estar abierto", m.View(), "/c")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, Esc debe cerrar el popup", got)
	}
	if len(fake.stopped) != 0 {
		t.Fatalf("Stop fue llamado %d veces, Esc con menu abierto NO debe detener la corrida", len(fake.stopped))
	}
	if got := m.input.Value(); got != "/c" {
		t.Fatalf("input.Value() = %q, Esc solo cierra el popup: el texto %q debe quedar intacto", got, "/c")
	}

	// Another rune recomputes the menu from the still valid token: it is reopened.
	m = typeRunes(t, m, "o")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/commit") {
		t.Fatalf("linea seleccionada del menu = %q, teclear otra runa debe reabrir el menu sobre /commit", got)
	}

	// With the menu closed, the first Esc arms and the second stops the run.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc}) // cierra el popup reabierto
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc}) // menu cerrado: arma
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc}) // confirma
	if len(fake.stopped) != 1 || fake.stopped[0] != "s1" {
		t.Fatalf("Stop = %v, con menu cerrado dos Esc deben detener la corrida (Stop(%q) una vez)", fake.stopped, "s1")
	}
}

func TestModel_AtOpensFileMenu(t *testing.T) {
	// A word-starting "@" opens the @-menu of files (mirror of detectMention/filterFiles in mention.ts): the label is the path, without description; the filter ranks the basename (prefix before substring) before the match in the route. listFiles is called ONE time when the token is activated and is cached as long as it is active.
	calls := 0
	listFiles := func() ([]string, error) {
		calls++
		return []string{"internal/tui/model.go", "app.go", "README.md"}, nil
	}
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, listFiles)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "hola @")
	view := m.View()
	for _, want := range []string{"internal/tui/model.go", "app.go", "README.md"} {
		lineWith(t, view, want)
	}
	if got := menuSelectedLine(view); !strings.Contains(got, "internal/tui/model.go") {
		t.Fatalf("linea seleccionada del menu = %q, el primer archivo del listado debe arrancar seleccionado", got)
	}

	// "mo" filters by basename: only model.go starts with "mo".
	m = typeRunes(t, m, "mo")
	view = m.View()
	lineWith(t, view, "internal/tui/model.go")
	for _, drop := range []string{"app.go", "README.md"} {
		if strings.Contains(view, drop) {
			t.Fatalf("View() = %q, tras filtrar por %q el menu NO debe seguir mostrando %q", view, "mo", drop)
		}
	}
	if calls != 1 {
		t.Fatalf("listFiles fue llamado %d veces, debe llamarse UNA vez al activarse el token y cachearse mientras siga activo", calls)
	}

	// With listFiles nil the menu simply does not open.
	m2 := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, nil)
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 = typeRunes(t, m2, "hola @")
	if got := menuSelectedLine(m2.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, sin listFiles el @-menu no debe abrir", got)
	}

	// With listFiles failing the menu shows the error without blocking the input.
	m3 := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return nil, fmt.Errorf("rg no disponible")
	})
	m3 = apply(t, m3, tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 = typeRunes(t, m3, "hola @")
	if got := menuSelectedLine(m3.View()); !strings.Contains(got, "Could not list files: rg no disponible") {
		t.Fatalf("linea seleccionada del menu = %q, con listFiles fallando el @-menu debe mostrar el error", got)
	}
}

func TestModel_AtInsideWordDoesNotOpenMenu(t *testing.T) {
	// The "@" must begin the word (beginning of the text or preceded by a space): an email like a@b DOES NOT trigger the @-menu (detectMention mirror).
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"app.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "a@b")
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, un @ dentro de palabra (email) NO debe abrir el @-menu", got)
	}
}

func TestModel_TabAppliesSelectedMention(t *testing.T) {
	// With the @-menu open, Tab replaces the token with "@<path> " while preserving the text around it (mirror of applyMention: text[:start] + "@<path> " + text[end:]) and leaves the caret after the space. Recomputation closes the menu.
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go", "app.go", "README.md"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "hola @mo")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "internal/tui/model.go") {
		t.Fatalf("linea seleccionada del menu = %q, con %q tecleado model.go debe estar seleccionado", got, "@mo")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	want := "hola @internal/tui/model.go "
	if got := m.input.Value(); got != want {
		t.Fatalf("input.Value() = %q, Tab debe reemplazar el token por la mencion conservando el texto alrededor (%q)", got, want)
	}
	if got := m.input.Position(); got != len([]rune(want)) {
		t.Fatalf("input.Position() = %d, el caret debe quedar tras el espacio (%d)", got, len([]rune(want)))
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, aplicar la mencion debe cerrar el menu", got)
	}
}

func TestModel_SlashOpensCommandMenu(t *testing.T) {
	// With commands configured via WithCompletions, typing "/" as the first character in the composer opens a menu popup above the box: one line per command with "/<name>" and its description. The first item starts selected and is marked with the prefix "❯" (those not selected have two prefix spaces).
	cmds := withMenuBuiltins(
		command.Command{Name: "commit", Description: "genera un commit"},
		command.Command{Name: "review", Description: "revisa el diff"},
	)
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(cmds, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})

	view := m.View()
	commitLine := lineWith(t, view, "/commit")
	if !strings.Contains(commitLine, "genera un commit") {
		t.Fatalf("linea de /commit = %q, el menu debe mostrar la descripcion %q junto al comando", commitLine, "genera un commit")
	}
	lineWith(t, view, "/review")
	newLine := lineWith(t, view, "/new")
	if plain := ansi.Strip(newLine); !strings.HasPrefix(plain, "❯ ") {
		t.Fatalf("linea de /new sin ANSI = %q, el comando integrado debe arrancar seleccionado con el prefijo %q", plain, "❯ ")
	}
}

func TestModel_CommandMenuAlwaysIncludesModelBuiltin(t *testing.T) {
	commands := make([]command.Command, 10)
	for i := range commands {
		commands[i] = command.Command{Name: fmt.Sprintf("skill-%02d", i), Description: "skill"}
	}
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(withMenuBuiltins(commands...), nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeRunes(t, m, "/")
	if view := m.View(); !strings.Contains(view, "/model") {
		t.Fatalf("slash menu must reserve a row for /model even when skills fill the limit:\n%s", view)
	}
}

func TestModel_CommandMenuPrioritizesNewAndEnterCreatesSession(t *testing.T) {
	// /new is a built-in command, not a fuzzy skill: it must appear first and Enter on its selection creates and activates the session without inserting a space.
	fake := &fakeAgent{newSessionID: "s2"}
	m := NewModel(fake, "s1", nil).WithCompletions(withMenuBuiltins(
		command.Command{Name: "renew", Description: "skill con coincidencia fuzzy"},
	), nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/new") {
		t.Fatalf("linea seleccionada del menu = %q, /new debe ser el comando integrado seleccionado por encima de skills", got)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.sessionID; got != "s2" {
		t.Fatalf("sessionID = %q, Enter sobre /new debe activar la sesion nueva %q", got, "s2")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, ejecutar /new desde el menu debe limpiar el composer sin dejar un espacio", got)
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, ejecutar /new debe cerrar el menu de comandos", got)
	}
	if got := fake.sent; len(got) != 1 || got[0].text != "/new" {
		t.Fatalf("SendPrompt llamadas = %#v, Enter sobre /new debe ejecutar el comando reservado exactamente una vez", got)
	}
}

func TestModel_NewSessionClearsTokenUsage(t *testing.T) {
	fake := &fakeAgent{newSessionID: "s2"}
	m := NewModel(fake, "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{
		Kind: session.KindStepEnded,
		Usage: &session.Usage{
			InputTokens:  1_234,
			OutputTokens: 345,
		},
	})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ 1.2k ↓ 345") {
		t.Fatalf("View() antes de /new = %q, debe mostrar el uso de la sesion anterior", view)
	}

	m = typeRunes(t, m, "/new")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if view := ansi.Strip(m.View()); strings.Contains(view, "↑") || strings.Contains(view, "↓") {
		t.Fatalf("View() despues de /new = %q, la sesion nueva no debe heredar tokens de subida ni bajada", view)
	}
}

func TestModel_NewSessionClearsLiveTokenEstimates(t *testing.T) {
	fake := &fakeAgent{newSessionID: "s2"}
	m := NewModel(fake, "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5")
	m = apply(t, m, EventMsg{
		Kind:  session.KindStepStarted,
		Usage: &session.Usage{InputTokens: 1_200},
	})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: strings.Repeat("a", 900)})

	m = typeRunes(t, m, "/new")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.usage != nil || m.liveUsage || m.outputBytes != 0 || m.reasoningBytes != 0 || m.toolInputBytes != 0 {
		t.Fatalf("estado de uso despues de /new = usage:%+v live:%v bytes:%d/%d/%d, debe arrancar limpio", m.usage, m.liveUsage, m.outputBytes, m.reasoningBytes, m.toolInputBytes)
	}
}

func TestModel_ExactNewEnterBeatsFuzzySkillSelection(t *testing.T) {
	// Even if a fuzzy skill is selected, typing /new exactly and pressing Enter should execute the reservation, not complete the skill.
	fake := &fakeAgent{newSessionID: "s2"}
	m := NewModel(fake, "s1", nil).WithCompletions(withMenuBuiltins(
		command.Command{Name: "renew", Description: "skill con coincidencia fuzzy"},
	), nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/new")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got := m.sessionID; got != "s2" {
		t.Fatalf("sessionID = %q, Enter con /new escrito debe activar la sesion nueva %q aunque haya una skill fuzzy", got, "s2")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, Enter con /new escrito debe ejecutarlo, no completar una skill", got)
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, Enter con /new escrito debe cerrar el menu de comandos", got)
	}
}

func TestModel_NewWithTrailingSpaceKeepsComposerForArguments(t *testing.T) {
	// The space closes the menu and disables only the reserved command: the text is left intact so that the user can continue typing arguments.
	fake := &fakeAgent{newSessionID: "s2"}
	m := NewModel(fake, "s1", nil).WithCompletions([]command.Command{
		{Name: "renew", Description: "skill con coincidencia fuzzy"},
	}, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/new ")

	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, /new con espacio final debe cerrar el menu", got)
	}
	if got := m.input.Value(); got != "/new " {
		t.Fatalf("input.Value() = %q, /new con espacio final debe conservarse para argumentos", got)
	}
	if got := m.sessionID; got != "s1" {
		t.Fatalf("sessionID = %q, escribir /new con espacio final no debe ejecutar el reservado", got)
	}
	if got := len(fake.sent); got != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, escribir /new con espacio final no debe ejecutar el reservado", got)
	}
}

func TestModel_MenuLinesTruncateToTerminalWidth(t *testing.T) {
	// A menu line wider than the terminal would be wrapped by the terminal with two real lines, but reservedLines only discounts ONE per item: the layout is broken. The menu should truncate each line to the width of the terminal, as the rest of the view already does (the transcript wraps with ansi.Wrap, the textinput scrolls horizontally).
	longPath := strings.Repeat("sub/", 30) + "archivo-de-nombre-largo.go"
	listFiles := func() ([]string, error) {
		return []string{longPath}, nil
	}
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, listFiles)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})

	view := m.View()
	lineWith(t, view, "sub/") // la linea del menu sigue presente, truncada
	assertNoLineWiderThan(t, view, 40)
}

// Animated indicator contract: while a run is in progress the status line shows a spinner glyph followed by "working"; the static prefix "..." disappears. Starting the run (Enter with text) returns a non-nil tea.Cmd that pumps the animation: executing it produces a message that, applied to Update, advances the spinner glyph (the status line changes) and returns in turn the next cmd of the loop.
func TestModel_WorkingIndicatorAnimatesOnTicks(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// The user types "hello" and presses Enter; The Enter cmd is preserved (the apply helper discards it and here is the heart of the contract).
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}

	// a) Starting the run must return the cmd that pumps the animation: without cmd no one produces ticks and the spinner remains frozen.
	if cmd == nil {
		t.Fatalf("Update(Enter) devolvio cmd nil, arrancar la corrida debe devolver el cmd que bombea la animacion: sin cmd el spinner queda congelado")
	}

	// b) The status line retains "working" but without the old static marker "...working": now the prefix is ​​the animated glyph.
	view := m.View()
	if !strings.Contains(view, "working") {
		t.Fatalf("View() = %q, con corrida en curso debe verse la linea de estado con %q", view, "working")
	}
	if strings.Contains(view, "... working") {
		t.Fatalf("View() = %q, NO debe contener el marcador estatico %q: el prefijo fijo se reemplaza por el glifo del spinner", view, "... working")
	}

	// c) Running the cmd produces the tick message; applying it to Update should advance the spinner glyph: the status line changes.
	before := lineWith(t, view, "working")
	msg := cmd()
	if msg == nil {
		t.Fatalf("cmd() = nil, el cmd de la animacion debe producir un mensaje aplicable a Update")
	}
	updated, tickCmd := m.Update(msg)
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	after := lineWith(t, m.View(), "working")
	if after == before {
		t.Fatalf("linea de estado tras el tick = %q, identica a la previa: el tick debe avanzar el frame del spinner, una linea identica significa animacion congelada", after)
	}

	// d) The loop continues: the Update of the tick must schedule the next tick.
	if tickCmd == nil {
		t.Fatalf("Update(tick) devolvio cmd nil, el loop de animacion debe agendar el proximo tick")
	}
}

// TRIANGULATE: the tick loop must die when the run ends. A tick case that always reschedules without looking at working leaves the TUI waking up forever: an old tick that arrives AFTER RunDoneMsg should not reschedule the loop (cmd nil) or revive the status line.
func TestModel_SpinnerTickDiesAfterRunDone(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// The run starts and there is one tick left in flight (the cmd has already produced its msg).
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(Enter) devolvio cmd nil, arrancar la corrida debe devolver el cmd que bombea la animacion")
	}
	msg := cmd()

	// The bullfight ends; Only then does the old tick arrive.
	m = apply(t, m, activeRunDone(m, ""))
	updated, tickCmd := m.Update(msg)
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}

	if tickCmd != nil {
		t.Fatalf("Update(tick) tras RunDoneMsg devolvio cmd no nil, el loop de animacion NO debe re-agendarse cuando la corrida termino: sin este corte la TUI queda despertando para siempre")
	}
	if got := m.View(); strings.Contains(got, "working") {
		t.Fatalf("View() = %q, tras RunDoneMsg el tick viejo NO debe revivir la linea de estado %q", got, "working")
	}
}

// TRIANGULATE: the path of the plan also encourages. A poor implementation that wires the tick only in the Enter path leaves the spinner frozen when the run starts accepting a plan with 'y': accepting the plan should return the cmd that pumps the animation and its tick should advance the glyph.
func TestModel_AcceptPlanStartsSpinner(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// The agent presents a settled plan (present_plan called and successful).
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "p1"})

	// 'and' accepts the plan: he starts the run and must pump the spinner.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update('y') devolvio cmd nil, aceptar el plan arranca la corrida y debe devolver el cmd que bombea la animacion: sin cmd el spinner queda congelado en el camino del plan")
	}

	before := lineWith(t, m.View(), "working")
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, batched := range batch {
			if candidate := batched(); candidate != nil {
				if _, ok := candidate.(spinner.TickMsg); ok {
					msg = candidate
					break
				}
			}
		}
	}
	if msg == nil {
		t.Fatalf("cmd() = nil, el cmd de la animacion debe producir un mensaje aplicable a Update")
	}
	m = apply(t, m, msg)
	after := lineWith(t, m.View(), "working")
	if after == before {
		t.Fatalf("linea de estado tras el tick = %q, identica a la previa: el tick del camino del plan debe avanzar el frame del spinner", after)
	}
}

// TRIANGULATE: the animation is not single use. A poor implementation with a loop state that does not restart (starts only on the first run) leaves the spinner dead on the second: after RunDoneMsg, a new Enter must return the cmd of the animation and its tick must advance the glyph.
func TestModel_SecondRunRestartsSpinner(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// First run: Enter starts the loop and RunDoneMsg turns it off.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	updated, cmd1 := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd1 == nil {
		t.Fatalf("Update(Enter) devolvio cmd nil, arrancar la primera corrida debe devolver el cmd que bombea la animacion")
	}
	m = apply(t, m, activeRunDone(m, ""))

	// Second run: the loop must be reborn with the new Enter.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("otra vez")})
	updated, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd2 == nil {
		t.Fatalf("Update(Enter) de la segunda corrida devolvio cmd nil, cada corrida debe reencender la animacion: un loop de un solo uso deja el spinner muerto en la segunda corrida")
	}

	before := lineWith(t, m.View(), "working")
	msg := cmd2()
	if msg == nil {
		t.Fatalf("cmd() = nil, el cmd de la animacion de la segunda corrida debe producir un mensaje aplicable a Update")
	}
	m = apply(t, m, msg)
	after := lineWith(t, m.View(), "working")
	if after == before {
		t.Fatalf("linea de estado tras el tick = %q, identica a la previa: el tick de la segunda corrida debe avanzar el frame del spinner", after)
	}
}

// Prompt history contract: each SENT prompt (Enter with text, build path or plan) is saved in a history in memory of the TUI session, in order of sending. With the autocomplete menu CLOSED and no permission/plan pending, the UP arrow scrolls through history backwards (most recent first) putting each prompt in the input; At the top, another up arrow stays there (it does not cycle or empty). The DOWN arrow undoes forward and, after the most recent, leaves the entry as it was before starting to navigate. Without history, the up arrow does nothing.
func TestModel_UpArrowRecallsPromptHistory(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Two sent prompts: they remain in the history in order of sending.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("primero")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("segundo")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "segundo" {
		t.Fatalf("input.Value() = %q, la flecha arriba debe recuperar el ultimo prompt enviado (%q)", got, "segundo")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "primero" {
		t.Fatalf("input.Value() = %q, la segunda flecha arriba debe retroceder al prompt anterior (%q)", got, "primero")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "primero" {
		t.Fatalf("input.Value() = %q, en el tope del historial otra flecha arriba se queda en %q: no cicla ni se vacia", got, "primero")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.input.Value(); got != "segundo" {
		t.Fatalf("input.Value() = %q, la flecha abajo debe deshacer hacia adelante y volver al prompt mas reciente (%q)", got, "segundo")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, pasado el mas reciente la flecha abajo debe dejar el input como estaba antes de empezar a navegar (vacio tras el Enter)", got)
	}
}

// With text already typed, Up/Down should not open the history: the user must clear the composer before exploring previous prompts. Once inside the history, Down continues advancing and when passing the most recent one, it leaves the input clean.
func TestModel_NonEmptyInputBlocksHistoryExploration(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("primero")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("borrador")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.input.Value(); got != "borrador" {
		t.Fatalf("input.Value() = %q, con texto escrito la flecha abajo no debe abrir ni reemplazar con el historial", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "borrador" {
		t.Fatalf("input.Value() = %q, con texto escrito la flecha arriba no debe abrir ni reemplazar con el historial", got)
	}

	// Emptying the composer enables navigation.
	m.input.SetValue("")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "primero" {
		t.Fatalf("input.Value() = %q, con el composer vacio la flecha arriba debe recuperar %q", got, "primero")
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, al avanzar despues del prompt mas reciente el composer debe quedar limpio", got)
	}
}

func TestModel_HistoryKeepsOnlyLatestHundredPrompts(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	for i := 1; i <= 102; i++ {
		m = typeRunes(t, m, fmt.Sprintf("prompt-%03d", i))
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	}

	for range 101 {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	}
	if got := m.input.Value(); got != "prompt-003" {
		t.Fatalf("input.Value() = %q, tras 102 envios el historial debe conservar solo los 100 mas recientes y detenerse en %q", got, "prompt-003")
	}
}

// TRIANGULATE: with the autocomplete menu open, Up/Down belong to the popup selection, not to the prompt history. Drops a history handler placed BEFORE the menu gate in handleKey.
func TestModel_MenuOpenKeepsUpDownForSelection(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// There is history: without the menu gate, the up arrow would recover it.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("primero")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	m = typeRunes(t, m, "/")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/new") {
		t.Fatalf("linea seleccionada del menu = %q, con %q tecleado el menu debe estar abierto sobre /new", got, "/")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "/" {
		t.Fatalf("input.Value() = %q, con menu abierto la flecha arriba NO debe tocar el input: la seleccion del menu es quien navega", got)
	}
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/review") && !strings.Contains(got, "/cache-stats") {
		t.Fatalf("linea seleccionada del menu = %q, con menu abierto la flecha arriba debe mover la seleccion ciclicamente", got)
	}
}

// TRIANGULATE: Prompts sent in plan-mode (SendPlanPrompt path) are also stacked in history. Drop an implementation that stacks only in the SendPrompt path.
func TestModel_HistoryRecordsPlanPrompts(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Tab goes to plan-mode: Enter sends by SendPlanPrompt.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("plan-uno")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.planSent) != 1 {
		t.Fatalf("SendPlanPrompt fue llamado %d veces, Enter en plan-mode debe enviar el prompt exactamente una vez por el camino de plan", len(fake.planSent))
	}
	m = apply(t, m, activeRunDone(m, ""))

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "plan-uno" {
		t.Fatalf("input.Value() = %q, la flecha arriba debe recuperar el prompt de plan enviado (%q): los prompts de plan tambien se apilan en el historial", got, "plan-uno")
	}
}

// TRIANGULATE: Enter with empty input does not send (covered separately) and should not stack anything either. Take down an implementation that stacks all submits and leaves a "" sneaking into the history.
func TestModel_EmptySubmitDoesNotPolluteHistory(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("unico")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// Enter with empty input: does not send and should not touch the history.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "unico" {
		t.Fatalf("input.Value() = %q, la primera flecha arriba debe recuperar el unico prompt enviado (%q), sin un submit vacio colado en el historial", got, "unico")
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "unico" {
		t.Fatalf("input.Value() = %q, en el tope del historial la flecha arriba se queda en %q: el submit vacio no debe haberse apilado", got, "unico")
	}
}

// Smooth streaming contract (parity with desktop frontend, frontend/src/lib/reveal.ts): assistant deltas ACCUMULATE in the input but the view does NOT show them in full immediately. A reveal tick loop (revealTickMsg, analogous to spinner.TickMsg) advances the revealed text: each tick reveals ~max(base, ceil(backlog/8)) runes, with a base of ~6-7 runes per tick (desktop pace: 1 char every 5ms at ~33ms ticks). With enough ticks the full text becomes visible.
func TestModel_SmoothRevealsAssistantTextOnTicks(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	text := strings.Repeat("palabra ", 40) + "final-del-texto"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	// (a) The delta does NOT appear complete at once: the tail of the text is not yet revealed right after accumulating the delta.
	if got := m.View(); strings.Contains(got, "final-del-texto") {
		t.Fatalf("View() = %q, NO debe contener %q inmediatamente tras el delta: el texto se revela progresivamente con los ticks de reveal, no aparece completo de golpe", got, "final-del-texto")
	}

	// (b) A reveal tick advances the visible text: a prefix is ​​already visible, but the tail is not yet (progressive reveal, not all at once).
	m = apply(t, m, revealTickMsg{})
	view := m.View()
	if !strings.Contains(view, "palabra") {
		t.Fatalf("View() = %q, debe contener %q tras un tick de reveal: cada tick revela un tramo del texto acumulado", view, "palabra")
	}
	if strings.Contains(view, "final-del-texto") {
		t.Fatalf("View() = %q, NO debe contener %q tras UN solo tick: un tick revela ~max(base, ceil(backlog/8)) runas, no el texto entero", view, "final-del-texto")
	}

	// (c) With enough ticks the full text becomes visible.
	for i := 0; i < 200; i++ {
		m = apply(t, m, revealTickMsg{})
		if strings.Contains(m.View(), "final-del-texto") {
			break
		}
	}
	if got := m.View(); !strings.Contains(got, "final-del-texto") {
		t.Fatalf("View() = %q, debe contener %q tras suficientes ticks de reveal: el loop de reveal termina mostrando el texto completo", got, "final-del-texto")
	}
}

// TRIANGULATE: the catch-up limits the latency. With pure constant pace (~7 runes per tick) a delta of ~4000 runes would take ~570 ticks (~19 seconds at 33ms) to drain: the visible text would be eternally behind a fast model. The step proportional to the backlog (ceil(backlog/8)) must leave the full text visible in a limited number of ticks.
func TestModel_RevealCatchUpDrainsHugeDeltaInBoundedTicks(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// ~4011 runes in a single delta (fast model dumping text at once).
	text := strings.Repeat("palabra ", 500) + "fin-catchup"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	// The first tick doesn't reveal everything: the catch-up speeds up the pace, it doesn't turn it into an instant reveal (that would kill the animation).
	m = apply(t, m, revealTickMsg{})
	if got := m.View(); strings.Contains(got, "fin-catchup") {
		t.Fatalf("View() = %q, NO debe contener %q tras UN solo tick de un delta de ~4000 runas: el catch-up acota la latencia sin volverse un reveal instantaneo", got, "fin-catchup")
	}

	// At most 64 ticks in total (~2 seconds at 33ms) leave the full text visible: the proportional step geometrically drains the backlog.
	for i := 0; i < 63 && m.hasBacklog(); i++ {
		m = apply(t, m, revealTickMsg{})
	}
	if got := m.View(); !strings.Contains(got, "fin-catchup") {
		t.Fatalf("View() = %q, debe contener %q tras 64 ticks: el catch-up proporcional al backlog debe drenar un delta enorme en una cantidad acotada de ticks (un paso constante puro tardaria ~570)", got, "fin-catchup")
	}
}

// TRIANGULATE: the swap to markdown waits for the reveal to drain. A poor implementation would render markdown as soon as the block is closed (StepEnded) even if there is backlog: the entire text would flash suddenly in the middle of the animation. When the turn is closed with a pending backlog, the view should continue to show only the revealed prefix, already rendered as Markdown; Just when draining, the full content is shown.
func TestModel_RevealMarkdownSwapWaitsForDrain(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// ~502 runes: The first tick reveals ~63 (initial ** included) and leaves a lot of tail unrevealed.
	text := "**fuerte** " + strings.Repeat("relleno ", 60) + "fin-drenado"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	// One tick before closing: The revealed prefix is ​​now rendered as Markdown.
	m = apply(t, m, revealTickMsg{})
	if got := ansi.Strip(m.View()); strings.Contains(got, "**") || !strings.Contains(got, "fuerte") {
		t.Fatalf("View() sin ANSI = %q, debe rendir el Markdown revelado durante streaming", got)
	}

	// The shift is closed with a pending backlog.
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})

	view := ansi.Strip(m.View())
	if strings.Contains(view, "fin-drenado") {
		t.Fatalf("View() = %q, NO debe contener %q inmediatamente tras StepEnded: cerrar el turno no debe revelar de golpe la cola pendiente, el reveal sigue su ritmo de ticks", view, "fin-drenado")
	}
	if strings.Contains(view, "**") || !strings.Contains(view, "fuerte") {
		t.Fatalf("View() sin ANSI = %q, debe conservar el Markdown del prefijo revelado tras StepEnded", view)
	}

	// Once the backlog is drained, the closed block is rendered as markdown.
	m = drainReveal(t, m)
	view = ansi.Strip(m.View())
	if strings.Contains(view, "**") {
		t.Fatalf("View() = %q, NO debe contener %q tras drenar: con el bloque cerrado y drenado el enfasis markdown se rinde, no se muestra crudo", view, "**")
	}
	if !strings.Contains(view, "fuerte") {
		t.Fatalf("View() = %q, debe contener %q: rendir el markdown no debe perder el contenido", view, "fuerte")
	}
	if !strings.Contains(view, "fin-drenado") {
		t.Fatalf("View() = %q, debe contener %q: drenar el backlog debe terminar mostrando el texto completo rendido", view, "fin-drenado")
	}
}

// TRIANGULATE: the reveal cut is by runes, never by bytes. An implementation that cuts the prefix with e.text[:n] splits multibyte characters in half: the intermediate view is left with invalid UTF-8 or the replacement character U+FFFD. After each intermediate tick the view must be valid UTF-8 and without U+FFFD; when draining, entire multibyte text intact.
func TestModel_RevealCutsByRunesNotBytes(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// ~256 runes with absent accents but kanji and emoji of 3-4 bytes: almost any byte break falls in the middle of a character.
	text := strings.Repeat("cancion nunca japon 日本語テキスト 🚀🚀🚀 ", 8)
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	ticks := 0
	for m.hasBacklog() {
		ticks++
		if ticks > 1000 {
			t.Fatalf("el backlog del reveal no se agoto tras 1000 ticks")
		}
		m = apply(t, m, revealTickMsg{})
		view := m.View()
		if !utf8.ValidString(view) {
			t.Fatalf("View() = %q tras el tick %d, no es UTF-8 valido: el corte del reveal debe ser por runas, un corte por bytes parte los caracteres multibyte", view, ticks)
		}
		if strings.ContainsRune(view, '�') {
			t.Fatalf("View() = %q tras el tick %d, contiene el caracter de reemplazo U+FFFD: un caracter multibyte quedo partido por un corte por bytes", view, ticks)
		}
	}
	// The drain must have gone through intermediate cuts: an instant reveal would pass the assertions above without exercising anything.
	if ticks < 2 {
		t.Fatalf("el backlog (%d runas) se dreno en %d tick(s), debe drenar en varios ticks para ejercitar los cortes intermedios", utf8.RuneCountInString(text), ticks)
	}
	plain := ansi.Strip(m.View())
	if got, want := strings.Count(plain, "日本語テキスト"), 8; got != want {
		t.Fatalf("View() sin ANSI = %q, contiene %d ocurrencias de texto japonés, se esperaban %d tras drenar", plain, got, want)
	}
	if got, want := strings.Count(plain, "🚀🚀🚀"), 8; got != want {
		t.Fatalf("View() sin ANSI = %q, contiene %d grupos de emoji, se esperaban %d tras drenar", plain, got, want)
	}
}

// TRIANGULATE (mirror of the spinner life cycle): the reveal tick loop is born with the first delta that leaves the backlog, it is not duplicated with subsequent deltas, it is rearmed while there is backlog, it dies when drained and is reborn with a new delta. With nil event channel the bomb is nil and the cmd returned by Update is ONLY the reveal tick: each transition is direct assertable.
func TestModel_RevealTickLoopLifecycle(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// 200 runes per delta: no single tick drains the entire backlog.
	delta := strings.Repeat("palabra ", 25)
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})

	// a) The first delta with backlog starts the loop: the cmd produces the tick.
	updated, cmd := m.Update(EventMsg{Kind: session.KindTextDelta, Text: delta})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(delta) devolvio cmd nil, el primer delta con backlog debe devolver el cmd que arranca el loop de reveal: sin cmd nadie produce ticks y el texto queda congelado")
	}
	msg := cmd()
	if _, ok := msg.(revealTickMsg); !ok {
		t.Fatalf("cmd() = %T, el cmd del arranque del loop debe producir un revealTickMsg", msg)
	}

	// b) A second delta with the loop already running DOES NOT double the chain of ticks: two chains would double the rhythm of the reveal.
	updated, cmd = m.Update(EventMsg{Kind: session.KindTextDelta, Text: delta})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd != nil {
		t.Fatalf("Update(delta) con el loop de reveal corriendo devolvio cmd no nil, un segundo delta NO debe arrancar otra cadena de ticks: cadenas duplicadas aceleran el reveal con cada delta")
	}

	// c) A tick with backlog remaining is reset: the cmd produces the next tick.
	updated, cmd = m.Update(revealTickMsg{})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(tick) con backlog restante devolvio cmd nil, el loop debe reagendar el proximo tick mientras quede texto sin revelar")
	}
	msg = cmd()
	if _, ok := msg.(revealTickMsg); !ok {
		t.Fatalf("cmd() = %T, el cmd del rearme del loop debe producir el proximo revealTickMsg", msg)
	}

	// d) With the backlog drained the next tick is not rescheduled: the loop dies.
	m = drainReveal(t, m)
	updated, cmd = m.Update(revealTickMsg{})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd != nil {
		t.Fatalf("Update(tick) sin backlog devolvio cmd no nil, el loop de reveal debe morir al drenarse: sin este corte la TUI queda despertando cada 33ms para siempre")
	}

	// e) A new delta after the drain restarts the loop.
	updated, cmd = m.Update(EventMsg{Kind: session.KindTextDelta, Text: delta})
	if _, ok = updated.(Model); !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(delta) tras drenar devolvio cmd nil, un delta nuevo debe reencender el loop de reveal: un loop de un solo uso deja el texto congelado en el segundo turno de streaming")
	}
	msg = cmd()
	if _, ok := msg.(revealTickMsg); !ok {
		t.Fatalf("cmd() = %T, el cmd del reencendido del loop debe producir un revealTickMsg", msg)
	}
}

// TRIANGULATE: the reveal loop is NOT tied to working like the spinner loop. An implementation that copies the spinner.TickMsg case cut (!working => cmd nil) leaves the text frozen half-revealed when the run ends before draining the backlog: ticks after RunDoneMsg should continue revealing until drained.
func TestModel_RevealSurvivesRunDone(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	// Actual run in progress: working on via Enter with text.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	text := strings.Repeat("palabra ", 40) + "fin-tras-run-done"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	// The run ends with a pending backlog: working is turned off but the text queue remains unrevealed.
	m = apply(t, m, activeRunDone(m, ""))
	if m.Working() {
		t.Fatalf("Working() = true, RunDoneMsg debe apagar el estado de trabajo")
	}
	if got := m.View(); strings.Contains(got, "fin-tras-run-done") {
		t.Fatalf("View() = %q, RunDoneMsg NO debe revelar la cola de golpe: el reveal sigue su ritmo de ticks tambien al terminar la corrida", got)
	}

	// The tick after the end of the run continues advancing and rescheduling.
	updated, cmd := m.Update(revealTickMsg{})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(tick) tras RunDoneMsg devolvio cmd nil con backlog pendiente, el loop de reveal no debe morir con working: debe seguir drenando el texto restante")
	}

	m = drainReveal(t, m)
	if got := m.View(); !strings.Contains(got, "fin-tras-run-done") {
		t.Fatalf("View() = %q, debe contener %q tras drenar: los ticks posteriores a RunDoneMsg deben terminar mostrando el texto completo", got, "fin-tras-run-done")
	}
}

// Thinking toggle contract (Shift+Tab key, see handleKey and toggleThinking): a settled thought (closed and with reveal drained) collapses to the summary line "◆ Thought for <dur>"; Shift+Tab expands it to the full text and a second Shift+Tab collapses it again. The hint " ⇧Tab" accompanies the collapsed summary to reveal the key.
func TestModel_ShiftTabExpandsAndCollapsesSettledThinking(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	text := "razon-1\nrazon-2\nrazon-3"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)

	// Seated: Collapsed by default.
	view := m.View()
	if !strings.Contains(view, "◆ Thought") {
		t.Fatalf("View() = %q, el pensamiento asentado debe colapsar a %q", view, "◆ Thought")
	}
	if !strings.Contains(view, " ⇧Tab") {
		t.Fatalf("View() = %q, el resumen colapsado debe llevar el hint %q para descubrir el toggle", view, " ⇧Tab")
	}
	if strings.Contains(view, "razon-2") {
		t.Fatalf("View() = %q, el pensamiento colapsado NO debe mostrar el texto completo", view)
	}

	// Shift+Tab expande.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	for _, want := range []string{"◆ Thought", "razon-1", "razon-2", "razon-3"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, tras Shift+Tab el pensamiento expandido debe mostrar %q", view, want)
		}
	}

	// Shift+Tab colapsa de nuevo.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	if !strings.Contains(view, "◆ Thought") {
		t.Fatalf("View() = %q, el segundo Shift+Tab debe volver al resumen colapsado %q", view, "◆ Thought")
	}
	if strings.Contains(view, "razon-2") {
		t.Fatalf("View() = %q, el segundo Shift+Tab debe colapsar el texto otra vez", view)
	}
}

func TestModel_SettledThinkingSummaryAlignsWithAssistantContent(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "respuesta-asistente"})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "respuesta-asistente"},
	})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pensamiento-asentado"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pensamiento-asentado"})
	m = drainReveal(t, m)

	assistantLine := ansi.Strip(lineWith(t, m.View(), "respuesta-asistente"))
	thinkingLine := ansi.Strip(lineWith(t, m.View(), "◆ Thought"))
	assistantIndent := assistantLine[:len(assistantLine)-len(strings.TrimLeft(assistantLine, " "))]

	if got, want := assistantIndent, "  "; got != want {
		t.Fatalf("prefijo del contenido assistant = %q, want %q", got, want)
	}
	if !strings.HasPrefix(thinkingLine, assistantIndent) {
		t.Fatalf("linea del resumen de pensamiento = %q, debe alinearse con el contenido assistant %q", thinkingLine, assistantLine)
	}
}

func TestModel_LiveThinkingHeaderAlignsWithAssistantContent(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "respuesta-asistente"})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "respuesta-asistente"},
	})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pensamiento-vivo"})
	m = drainReveal(t, m)

	assistantLine := ansi.Strip(lineWith(t, m.View(), "respuesta-asistente"))
	thinkingLine := ansi.Strip(lineWith(t, m.View(), "◆ Thinking…"))
	assistantIndent := assistantLine[:len(assistantLine)-len(strings.TrimLeft(assistantLine, " "))]

	if got, want := assistantIndent, "  "; got != want {
		t.Fatalf("prefijo del contenido assistant = %q, want %q", got, want)
	}
	if !strings.HasPrefix(thinkingLine, assistantIndent) {
		t.Fatalf("linea del encabezado de pensamiento vivo = %q, debe alinearse con el contenido assistant %q", thinkingLine, assistantLine)
	}
}

func TestModel_LiveThinkingHeaderKeepsChatIndentWhenSettledWithExplorer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "respuesta-visible"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "respuesta-visible"},
	})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "preview-sin-indentacion-adicional"})
	m = drainReveal(t, m)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after opening explorer = %v, want %v", got, want)
	}

	liveView := ansi.Strip(m.View())
	assistantLine := lineWith(t, liveView, "respuesta-visible")
	liveHeaderLine := lineWith(t, liveView, "◆ Thinking…")
	// Column measured in display cells, not byte offset: the tree row in the wizard line has a multibyte folder icon, so strings.Index (bytes) would not match the header row (ASCII prefix) even if they both fall in the same visible column.
	column := func(line, sub string) int { return ansi.StringWidth(line[:strings.Index(line, sub)]) }
	chatContentColumn := column(assistantLine, "respuesta-visible")
	liveHeaderColumn := column(liveHeaderLine, "◆ Thinking…")
	if got, want := liveHeaderColumn, chatContentColumn; got != want {
		t.Fatalf("columna de ◆ Thinking… = %d, want %d: debe alinearse con el contenido visible del chat", got, want)
	}
	// Without a box, the chat no longer provides a border: the header starts after the tree, its gutter from a column and the indentation from the thought block itself.
	if got, want := liveHeaderColumn, m.treePanelWidth()+1+len(thinkingInset); got != want {
		t.Fatalf("columna de ◆ Thinking… = %d, want %d: arbol + gutter + sangria del pensamiento", got, want)
	}

	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "preview-sin-indentacion-adicional"})
	m = drainReveal(t, m)

	settledView := ansi.Strip(m.View())
	settledHeaderLine := lineWith(t, settledView, "◆ Thought")
	settledHeaderColumn := column(settledHeaderLine, "◆ Thought")
	if got, want := settledHeaderColumn, liveHeaderColumn; got != want {
		t.Fatalf("columna del resumen asentado = %d, want %d: ReasoningEnded no debe desplazar horizontalmente el encabezado", got, want)
	}
}

func TestModel_ShiftTabExpandsSettledThinkingWithExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pensamiento-completo"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pensamiento-completo"})
	m = drainReveal(t, m)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after opening explorer = %v, want %v", got, want)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := ansi.Strip(m.View()); !strings.Contains(got, "pensamiento-completo") {
		t.Fatalf("View() sin ANSI = %q, Shift+Tab con foco del explorador debe expandir el pensamiento asentado", got)
	}
}

func TestModel_ShiftTabExpandsSettledThinkingWithViewerFocusWithoutScrollingFile(t *testing.T) {
	file := strings.Repeat("archivo\n", 80)
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"archivo.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"archivo.txt": []byte(file)}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pensamiento-del-visor"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pensamiento-del-visor"})
	m = drainReveal(t, m)

	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus after opening file = %v, want %v", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	viewerPath, viewerOffset := m.viewer.path, m.viewer.offset
	if viewerOffset == 0 {
		t.Fatal("viewer precondition: PgDown must move the long file")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if !m.entries[0].expanded {
		t.Fatal("Shift+Tab con foco del visor debe expandir el pensamiento asentado")
	}
	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus after Shift+Tab = %v, want %v", got, want)
	}
	if got, want := m.viewer.path, viewerPath; got != want {
		t.Fatalf("viewer.path after Shift+Tab = %q, want %q", got, want)
	}
	if got, want := m.viewer.offset, viewerOffset; got != want {
		t.Fatalf("viewer.offset after Shift+Tab = %d, want %d: global thinking toggle must not scroll the file", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if got := ansi.Strip(m.View()); !strings.Contains(got, "pensamiento-del-visor") {
		t.Fatalf("View() sin ANSI = %q, el pensamiento expandido debe verse al cerrar el visor", got)
	}
}

// Contract: the toggle is inert while the thought is still live (preview of the last lines, not the full text). A Shift+Tab during the stream should not set expanded or reveal the entire text prematurely.
func TestModel_ShiftTabIsInertWhileThinkingLive(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "vivo-1\nvivo-2\nvivo-3\nvivo-4\nvivo-5"})
	m = drainReveal(t, m)

	// The live preview shows the header and last lines, not the summary or the full expanded text.
	view := m.View()
	if !strings.Contains(view, "◆ Thinking…") {
		t.Fatalf("View() = %q, en vivo debe mostrar %q", view, "◆ Thinking…")
	}
	if strings.Contains(view, "◆ Thought") {
		t.Fatalf("View() = %q, en vivo NO debe mostrar el resumen colapsado %q", view, "◆ Thought")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	if strings.Contains(view, "◆ Thought") {
		t.Fatalf("View() = %q, Shift+Tab durante el stream vivo no debe colapsar todavia", view)
	}
	if strings.Contains(view, "vivo-1") {
		t.Fatalf("View() = %q, Shift+Tab durante el stream vivo no debe expandir el texto entero", view)
	}
}

func TestModel_ShiftTabIsInertForLiveThinkingWithExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"archivo.txt", "otro.txt"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "vivo-uno\nvivo-dos\nvivo-tres\nvivo-cuatro\nvivo-cinco"})
	m = drainReveal(t, m)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after opening explorer = %v, want %v", got, want)
	}

	beforeView, beforeCursor := ansi.Strip(m.View()), m.treeCursor
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	afterView := ansi.Strip(m.View())
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after Shift+Tab = %v, want %v", got, want)
	}
	if got, want := m.treeCursor, beforeCursor; got != want {
		t.Fatalf("treeCursor after Shift+Tab = %d, want %d", got, want)
	}
	if got, want := afterView, beforeView; got != want {
		t.Fatalf("View() after Shift+Tab = %q, want unchanged live-thinking preview %q", got, want)
	}
	if strings.Contains(afterView, "vivo-uno") || strings.Contains(afterView, "◆ Thought") {
		t.Fatalf("View() = %q, Shift+Tab con pensamiento vivo no debe revelar todo ni mostrar el resumen asentado", afterView)
	}
}

// Contract: Shift+Tab toggles ALL seated thought blocks at once. With two thoughts finished, a single blow expands them both and a second collapses them both.
func TestModel_ShiftTabTogglesAllSettledThinkingBlocks(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	for _, tag := range []string{"primero", "segundo"} {
		m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
		m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: tag + "-a\n" + tag + "-b"})
		m = drainReveal(t, m)
		m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: tag + "-a\n" + tag + "-b"})
		m = drainReveal(t, m)
	}

	// Both collapsed by default: two summaries, no text.
	view := m.View()
	if n := strings.Count(view, "◆ Thought"); n != 2 {
		t.Fatalf("View() = %q, dos pensamientos asentados deben colapsar a dos resumenes %q (n=%d)", view, "◆ Thought", n)
	}
	if strings.Contains(view, "primero-a") || strings.Contains(view, "segundo-a") {
		t.Fatalf("View() = %q, ambos colapsados no deben mostrar texto", view)
	}

	// A Shift+Tab expands both.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	if !strings.Contains(view, "primero-a") || !strings.Contains(view, "segundo-a") {
		t.Fatalf("View() = %q, un solo Shift+Tab debe expandir AMBOS pensamientos", view)
	}
	if n := strings.Count(view, "◆ Thought"); n != 2 {
		t.Fatalf("View() = %q, tras expandir siguen habiendo dos resumenes de cabecera (n=%d)", view, n)
	}

	// A second Shift+Tab collapses both.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	if strings.Contains(view, "primero-a") || strings.Contains(view, "segundo-a") {
		t.Fatalf("View() = %q, el segundo Shift+Tab debe colapsar AMBOS", view)
	}
}

// Click toggle contract (see toggleThinkingAt and the tea.MouseMsg case of Update): a left click on the summary line of a settled thought expands it to the full text, just like Shift+Tab but on the specific block under the cursor. The click maps to the entry via entryLines, so the clicked row should fall on the summary line.
func TestModel_ClickExpandsSettledThinking(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	text := "razon-1\nrazon-2\nrazon-3"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)

	// Locate the "◆Thought" summary row in the viewport content.
	lines := m.entryLines()
	summaryRow := -1
	for i, l := range lines {
		if strings.Contains(l.line, "◆ Thought") {
			summaryRow = i
			break
		}
	}
	if summaryRow < 0 {
		t.Fatalf("entryLines() no contiene el resumen %q: %v", "◆ Thought", lines)
	}
	// The row on the screen is the one with the content minus the visible scrolling, plus the row of the top bar that moves the body one row down.
	clickY := topBarHeight + summaryRow - m.viewport.YOffset
	if clickY < topBarHeight {
		t.Fatalf("summaryRow=%d YOffset=%d, el resumen no esta visible para clicar", summaryRow, m.viewport.YOffset)
	}

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: clickY})
	view := m.View()
	for _, want := range []string{"◆ Thought", "razon-1", "razon-2", "razon-3"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, el clic sobre el resumen debe expandir el pensamiento mostrando %q", view, want)
		}
	}
}

// TRIANGULATE the shared condition compactActivityJoin (its reason for being): with a compact group of DOS tools before a collapsed thought, the summary row is located in what the user SEES (View), not in entryLines, and the click on that row should expand the thought. If entryLines continued to output the separator between activities, its numbering would diverge from that of the viewport and the click would land on the wrong line.
func TestModel_ClickTargetingStaysAlignedWithCompactGroups(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})

	// Compact group: two tools sitting adjacent to each other (no blank line).
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, ToolCallID: "c1"},
	})
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "grep", Input: json.RawMessage(`{"pattern":"foo"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c2", ToolName: "grep",
		Message: &session.Message{ID: "c2", Role: session.RoleTool, ToolCallID: "c2"},
	})

	text := "razon-1\nrazon-2"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)
	target := len(m.entries) - 1

	// The row is searched on the real screen: the short transcript is shown from the top (without scrolling) and the viewport opens the view, so the row Y of the screen is the absolute line of the content.
	if m.viewport.YOffset != 0 {
		t.Fatalf("viewport.YOffset = %d, want 0: el transcript corto se muestra desde arriba", m.viewport.YOffset)
	}
	summaryY := lineIndexWith(t, ansi.Strip(m.View()), "◆ Thought")

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: summaryY})
	if !m.entries[target].expanded {
		t.Fatal("el clic sobre la fila visible del resumen debe expandir el pensamiento pese al grupo compacto de tools encima")
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"razon-1", "razon-2"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() sin ANSI = %q, el pensamiento expandido debe mostrar %q", view, want)
		}
	}
}

func TestModel_ClickExpandsSettledThinkingInChatWithExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 120, Height: 32})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pensamiento-del-chat"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pensamiento-del-chat"})
	m = drainReveal(t, m)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after opening explorer = %v, want %v", got, want)
	}

	view := ansi.Strip(m.View())
	summaryX, summaryY := -1, -1
	for y, line := range strings.Split(view, "\n") {
		if x := strings.Index(line, "◆ Thought"); x >= 0 {
			summaryX, summaryY = x, y
			break
		}
	}
	if summaryX < m.treePanelWidth() || summaryY < 0 {
		t.Fatalf("View() = %q, no contiene el resumen de pensamiento en el panel chat", view)
	}

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      summaryX,
		Y:      summaryY,
	})
	if got := ansi.Strip(m.View()); !strings.Contains(got, "pensamiento-del-chat") {
		t.Fatalf("View() sin ANSI = %q, el clic sobre el resumen del chat debe expandir el pensamiento", got)
	}
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after chat thinking click = %v, want %v: el clic no debe cambiar el foco del explorador", got, want)
	}
}

func TestModel_ClickExpandsScrolledThinkingInChatWithExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 120, Height: 18})
	for i := 0; i < 8; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("relleno-%d", i),
		}})
	}
	for _, text := range []string{"pensamiento-primero", "pensamiento-objetivo"} {
		m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
		m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
		m = drainReveal(t, m)
		m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
		m = drainReveal(t, m)
	}
	target := len(m.entries) - 1

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after opening explorer = %v, want %v", got, want)
	}
	summaryRow := -1
	for row, line := range m.entryLines() {
		if line.idx == target && strings.Contains(line.line, "◆ Thought") {
			summaryRow = row
			break
		}
	}
	if summaryRow < 0 {
		t.Fatal("entryLines() no contiene el resumen del pensamiento objetivo")
	}
	m.viewport.SetYOffset(max(summaryRow-m.viewport.Height+1, 1))
	if m.viewport.YOffset <= 0 {
		t.Fatalf("viewport.YOffset = %d, want > 0 for a scrolled transcript", m.viewport.YOffset)
	}
	clickY := topBarHeight + summaryRow - m.viewport.YOffset
	if clickY < topBarHeight || clickY >= m.viewport.Height+topBarHeight {
		t.Fatalf("target summary row=%d, offset=%d, clickY=%d, viewport height=%d: el resumen objetivo debe estar visible dentro del transcript derecho", summaryRow, m.viewport.YOffset, clickY, m.viewport.Height)
	}

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() + 2,
		Y:      clickY,
	})
	if !m.entries[target].expanded {
		t.Fatal("el clic sobre el resumen desplazado debe expandir el bloque de pensamiento objetivo")
	}
	if m.entries[target-1].expanded {
		t.Fatal("el clic sobre el resumen desplazado no debe expandir otro bloque de pensamiento")
	}
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after scrolled chat thinking click = %v, want %v", got, want)
	}
}

func TestModel_ClickChatPanelTitleIsInertWithExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pensamiento-del-titulo"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pensamiento-del-titulo"})
	m = drainReveal(t, m)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after opening explorer = %v, want %v", got, want)
	}

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() + 2,
		Y:      0,
	})
	if m.entries[0].expanded {
		t.Fatal("el clic sobre el titulo del panel derecho no debe alternar pensamientos")
	}
	if got, want := m.focus, chatFocus; got != want {
		t.Fatalf("focus after chat panel title click = %v, want %v: el clic derecho fuera del transcript debe enfocar el chat", got, want)
	}
}

// Contract: a click on the text of an ALREADY expanded thought collapses it again (toggle back and forth on the same block).
func TestModel_ClickCollapsesExpandedThinking(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	text := "razon-1\nrazon-2"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)

	// Expand first with Shift+Tab.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := m.View(); !strings.Contains(got, "razon-1") {
		t.Fatalf("View() = %q, precondicion: Shift+Tab debe expandir", got)
	}

	// Click on the first line of the expanded text (the "◆ Thought" header).
	lines := m.entryLines()
	headerRow := -1
	for i, l := range lines {
		if strings.Contains(l.line, "◆ Thought") {
			headerRow = i
			break
		}
	}
	clickY := topBarHeight + headerRow - m.viewport.YOffset
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: clickY})
	view := m.View()
	if strings.Contains(view, "razon-1") {
		t.Fatalf("View() = %q, el clic sobre el bloque expandido debe colapsarlo", view)
	}
	if !strings.Contains(view, "◆ Thought") {
		t.Fatalf("View() = %q, tras colapsar debe volver el resumen %q", view, "◆ Thought")
	}
}

// Contract: a left click on a line that is NOT a settled thought (an empty line of separation or the text of a user message) is inert: it does not expand anything or change the view.
func TestModel_ClickOutsideThinkingIsInert(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "penso-a\npenso-b"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "penso-a\npenso-b"})
	m = drainReveal(t, m)

	before := m.View()
	// Click on the user message line (first entry, row 0).
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: 0})
	if got := m.View(); got != before {
		t.Fatalf("View() cambio tras clic fuera del pensamiento:\nantes = %q\ndespues = %q, el clic solo alterna bloques de pensamiento asentados", before, got)
	}
	if strings.Contains(m.View(), "penso-a") {
		t.Fatalf("View() = %q, el clic fuera del pensamiento no debe expandirlo", m.View())
	}
}

func TestModel_LeaderSpaceE_OpensTree(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go", "go.mod"}, nil
	})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q after leader Space, want empty", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	if !m.treeOpen {
		t.Fatal("Space then e must open the file tree")
	}
	if got := m.View(); !strings.Contains(got, "go.mod") {
		t.Fatalf("View() = %q, open tree must render the file tree", got)
	}
}

func TestModel_ToolDiffRefreshesOpenTreeAndPreservesState(t *testing.T) {
	files := []string{"internal/tui/model.go", "go.mod"}
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return files, nil
	})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	selected := m.selectedTreePath()

	files = append(files, "internal/tui/new.go")
	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "c1", Diff: "diff --git a/internal/tui/new.go b/internal/tui/new.go"})

	if !m.tree.expanded["internal"] {
		t.Fatal("tool diff refresh must preserve expanded directories")
	}
	if got := m.selectedTreePath(); got != selected {
		t.Fatalf("selected path after refresh = %q, want %q", got, selected)
	}
	if !slices.Contains(m.tree.paths(), "internal/tui/new.go") {
		t.Fatalf("tree paths = %v, tool diff refresh must include new files", m.tree.paths())
	}
}

func TestModel_TreeKeyRRefreshesClosedOverSnapshot(t *testing.T) {
	files := []string{"old.go"}
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return files, nil
	})
	m = m.toggleTree()
	files = []string{"new.go"}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	if got, want := m.selectedTreePath(), "new.go"; got != want {
		t.Fatalf("selected path after manual refresh = %q, want %q", got, want)
	}
}

func TestModel_LeaderSpaceE_TogglesClosed(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"go.mod"}, nil
	})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	if m.treeOpen {
		t.Fatal("second Space+e must close the file tree")
	}
}

func TestModel_KeyRunesBatch_LeaderSpaceEOpensTree(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"go.mod", "internal/tui/model.go"}, nil
	})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" e")})

	if !m.treeOpen {
		t.Fatal(`single KeyRunes batch " e" must open the file tree`)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, leader batch must not insert into composer", got)
	}
	if got := m.View(); !strings.Contains(got, "go.mod") {
		t.Fatalf("View() = %q, open tree must render the file tree", got)
	}
}

func TestModel_KeyRunesBatch_LeaderSpaceEParity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		batch    string
		wantOpen bool
	}{
		{name: "two pairs close", batch: " e e", wantOpen: false},
		{name: "three pairs open", batch: " e e e", wantOpen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
				return []string{"go.mod", "internal/tui/model.go"}, nil
			})

			m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.batch)})

			if got := m.treeOpen; got != tc.wantOpen {
				t.Fatalf("treeOpen = %v, want %v after batch %q", got, tc.wantOpen, tc.batch)
			}
			if got := m.input.Value(); got != "" {
				t.Fatalf("input.Value() = %q, repeated leader pairs must not insert into composer", got)
			}
		})
	}
}

func TestModel_KeyRunesBatch_NormalTextInsertsIntoComposer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola mundo")})

	if got, want := m.input.Value(), "hola mundo"; got != want {
		t.Fatalf("input.Value() = %q, want %q: normal text batch must preserve every rune in order", got, want)
	}
	if m.treeOpen || m.leaderPending {
		t.Fatalf("treeOpen=%v leaderPending=%v, normal text batch must not trigger leader state", m.treeOpen, m.leaderPending)
	}
}

func TestModel_TreeKeys_NavigateAndOpenFileViewer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) {
			return []string{"internal/tui/model.go", "go.mod"}, nil
		}).
		WithFileReader(viewerReader(map[string][]byte{"internal/tui/model.go": []byte("package tui\n")}))
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	// Directories sort before files. Expand internal, move to internal/tui,
	// expand it, move to model.go and select the file.
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune{'j'}},
		{Type: tea.KeyRunes, Runes: []rune{'l'}},
		{Type: tea.KeyDown},
		{Type: tea.KeyEnter},
	} {
		m = apply(t, m, msg)
	}

	if !m.treeOpen || !m.viewer.active() {
		t.Fatal("selecting a file must open the viewer and keep the tree open")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, opening a file must not insert a mention", got)
	}
}

func TestModel_TreeOpen_CapturesKeyboard(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"go.mod"}, nil
	})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, tree keyboard must not feed textinput", got)
	}
}

func TestModel_TreeNavigationScrollsSelectedRowIntoView(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{
			"file-00.go",
			"file-01.go",
			"file-02.go",
			"file-03.go",
			"file-04.go",
		}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 3 + topBarHeight})
	m = m.toggleTree()

	for range 3 {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}

	if got := m.View(); strings.Contains(got, "file-00.go") {
		t.Fatalf("View() = %q, rows above the viewport must scroll away with the selection", got)
	}
}

func TestModel_TreeNavigationScrollsBackAtTopAndAfterResize(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{
			"file-00.go",
			"file-01.go",
			"file-02.go",
			"file-03.go",
			"file-04.go",
		}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 4 + topBarHeight})
	m = m.toggleTree()

	for range 4 {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}
	if got, want := m.treeOffset, 1; got != want {
		t.Fatalf("treeOffset at bottom = %d, want %d", got, want)
	}

	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 2 + topBarHeight})
	if got, want := m.treeOffset, 3; got != want {
		t.Fatalf("treeOffset after shrinking = %d, want %d", got, want)
	}

	for range 4 {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	}
	if got, want := m.treeOffset, 0; got != want {
		t.Fatalf("treeOffset after returning to top = %d, want %d", got, want)
	}
	if got := m.View(); !strings.Contains(got, "file-00.go") {
		t.Fatalf("View() = %q, first row must be visible after moving to top", got)
	}
}

func TestModel_TreeMouseWheelScrollsSelectionWithoutMovingTranscript(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{
			"file-00.go",
			"file-01.go",
			"file-02.go",
			"file-03.go",
			"file-04.go",
		}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 2 + topBarHeight})
	m = m.toggleTree()
	for i := 0; i < 20; i++ {
		m.entries = append(m.entries, entry{kind: entryUser, text: fmt.Sprintf("message-%d", i)})
	}
	m = m.syncViewport()
	m.viewport.SetYOffset(2)

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelDown,
		X:      0,
		Y:      3,
	})

	if got, want := m.treeCursor, 3; got != want {
		t.Fatalf("treeCursor = %d, want %d", got, want)
	}
	if got, want := m.treeOffset, 2; got != want {
		t.Fatalf("treeOffset = %d, want %d", got, want)
	}
	if got, want := m.viewport.YOffset, 2; got != want {
		t.Fatalf("viewport.YOffset = %d, want %d", got, want)
	}
}

func TestModel_ViewerFocusWheelAtPointerOriginScrollsSplitExplorer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) {
			return []string{"file-00.go", "file-01.go", "file-02.go", "file-03.go", "file-04.go"}, nil
		}).
		WithFileReader(viewerReader(map[string][]byte{
			"file-00.go": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n"),
		}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus before wheel = %v, want %v", got, want)
	}
	treeCursor, viewerOffset := m.treeCursor, m.viewer.offset

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelDown,
		X:      0,
		Y:      0,
	})

	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus after wheel = %v, want %v: wheel must not change keyboard focus", got, want)
	}
	if got := m.treeCursor; got <= treeCursor {
		t.Fatalf("treeCursor = %d, want greater than %d: wheel at explorer origin must scroll the tree", got, treeCursor)
	}
	if got, want := m.viewer.offset, viewerOffset; got != want {
		t.Fatalf("viewer offset = %d, want %d: wheel at explorer origin must not scroll the viewer", got, want)
	}
}

func TestModel_NarrowFullWidthViewerWheelNeverScrollsHiddenTree(t *testing.T) {
	const width, height = 20, 8

	for _, x := range []int{0, width - 1} {
		t.Run(fmt.Sprintf("x=%d", x), func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil).
				WithCompletions(nil, func() ([]string, error) {
					return []string{"file-00.go", "file-01.go", "file-02.go", "file-03.go", "file-04.go"}, nil
				}).
				WithFileReader(viewerReader(map[string][]byte{
					"file-00.go": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n"),
				}))
			m = apply(t, m, tea.WindowSizeMsg{Width: width, Height: height})
			m = m.toggleTree()
			m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

			if got, want := m.treePanelWidth(), width; got != want {
				t.Fatalf("treePanelWidth() = %d, want %d: narrow terminal must hide the tree", got, want)
			}
			if view := ansi.Strip(m.View()); !strings.Contains(view, "file-00.go") {
				t.Fatalf("View() = %q, want the active viewer visibly rendered full-width", view)
			}
			if got, want := m.focus, viewerFocus; got != want {
				t.Fatalf("focus before wheel = %v, want %v", got, want)
			}
			treeCursor, viewerOffset := m.treeCursor, m.viewer.offset

			m = apply(t, m, tea.MouseMsg{
				Action: tea.MouseActionPress,
				Button: tea.MouseButtonWheelDown,
				X:      x,
				Y:      topBarHeight,
			})

			if got, want := m.focus, viewerFocus; got != want {
				t.Fatalf("focus after wheel = %v, want %v: wheel must not change keyboard focus", got, want)
			}
			if got := m.viewer.offset; got <= viewerOffset {
				t.Fatalf("viewer offset = %d, want greater than %d: wheel over full-width viewer must scroll the viewer", got, viewerOffset)
			}
			if got, want := m.treeCursor, treeCursor; got != want {
				t.Fatalf("treeCursor = %d, want %d: hidden tree must not receive wheel events", got, want)
			}
		})
	}
}

func TestModel_TreeMouseClickOpensFileFromAnyColumnInRow(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"hello.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"hello.go": []byte("package main\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() - 1,
		Y:      topBarHeight,
	})

	if !m.viewer.active() {
		t.Fatal("clicking anywhere on a file row must open the file viewer")
	}
	if got, want := m.viewer.path, "hello.go"; got != want {
		t.Fatalf("viewer.path = %q, want %q", got, want)
	}
}

func TestModel_TreeMouseClickReplacesOpenFileViewer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"first.go", "second.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{
			"first.go":  []byte("package first\n"),
			"second.go": []byte("package second\n"),
		}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() - 1, Y: topBarHeight})

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() - 1, Y: 1 + topBarHeight})

	if got, want := m.viewer.path, "second.go"; got != want {
		t.Fatalf("viewer.path = %q, want %q", got, want)
	}
}

func TestModel_TreeMouseClickFolderRowTogglesExpansion(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() - 1,
		Y:      topBarHeight,
	})

	if !m.tree.expanded["internal"] {
		t.Fatal("clicking anywhere on a folder row must toggle its expansion")
	}
	if got := len(m.tree.visibleRows()); got != 2 {
		t.Fatalf("visible rows = %d, want 2", got)
	}
}

func TestModel_LeaderTimeoutCancelsWithoutInput(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, leaderTimeoutMsg{})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	if m.treeOpen {
		t.Fatal("leader timeout must not open tree")
	}
	if got := m.input.Value(); got != "x" {
		t.Fatalf("input.Value() = %q, after timeout the next key must reach input", got)
	}
}

func TestModel_TreeListErrorRendersWithoutPanic(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return nil, fmt.Errorf("workspace unavailable")
	})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	if got := m.View(); !strings.Contains(got, "workspace unavailable") {
		t.Fatalf("View() = %q, tree error must be visible", got)
	}
}

func TestModel_TreeKeys_HCollapsesThenMovesToParent(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if got := m.tree.visibleRows()[m.treeCursor].node.path; got != "internal/tui" {
		t.Fatalf("selected path after h from file = %q, want parent internal/tui", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if m.tree.expanded["internal/tui"] {
		t.Fatal("h on expanded directory must collapse it")
	}
}

func TestModel_TreeKeys_EscapeAndQCloseWithoutInsert(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
	} {
		t.Run(key.String(), func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
				return []string{"go.mod"}, nil
			})
			m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
			m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
			m = apply(t, m, key)

			if m.treeOpen {
				t.Fatal("close key must close tree")
			}
			if got := m.input.Value(); got != "" {
				t.Fatalf("input.Value() = %q, closing tree must not insert", got)
			}
		})
	}
}

func TestModel_TreeLeaderDoesNotInterceptPendingGates(t *testing.T) {
	permission := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"go.mod"}, nil
	})
	permission = apply(t, permission, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	permission = apply(t, permission, tea.KeyMsg{Type: tea.KeySpace})
	permission = apply(t, permission, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if permission.treeOpen || permission.leaderPending {
		t.Fatal("pending permission must keep leader and tree inactive")
	}

	plan := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"go.mod"}, nil
	})
	plan.entries = append(plan.entries, entry{kind: entryPlanApproval})
	plan = apply(t, plan, tea.KeyMsg{Type: tea.KeySpace})
	plan = apply(t, plan, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if plan.treeOpen || plan.leaderPending {
		t.Fatal("pending plan must keep leader and tree inactive")
	}
}

func TestModel_TreeViewHandlesNarrowTerminal(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 12, Height: 4})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	if got := m.View(); !strings.Contains(got, "internal") {
		t.Fatalf("View() = %q, narrow terminal must still render the file tree without panic", got)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if width := ansi.StringWidth(line); width > 12 {
			t.Fatalf("narrow View line width = %d, want <= 12: %q", width, line)
		}
	}
}

func TestModel_TreeSelectionPreservesExistingComposer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) {
			return []string{"go.mod"}, nil
		}).
		WithFileReader(viewerReader(map[string][]byte{"go.mod": []byte("module atenea\n")}))
	m.input.SetValue("revisa")
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got, want := m.input.Value(), "revisa"; got != want {
		t.Fatalf("input.Value() = %q, want %q", got, want)
	}
	if !m.viewer.active() || !m.treeOpen {
		t.Fatal("selection must open viewer while preserving tree")
	}
}

func viewerReader(files map[string][]byte) FileReader {
	return func(path string) ([]byte, error) {
		content, ok := files[path]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return content, nil
	}
}

func TestModel_TreeEnterFileOpensViewerWithoutMention(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"go.mod"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"go.mod": []byte("module atenea\n")}))
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.viewer.active() || !m.treeOpen {
		t.Fatalf("viewer active=%v treeOpen=%v", m.viewer.active(), m.treeOpen)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input = %q, must not contain a mention", got)
	}
}

func TestModel_FileViewerEscapePreservesExplorerCursor(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"a.go", "b.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"b.go": []byte("package b\n")}))
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	cursor, offset := m.treeCursor, m.treeOffset
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewer.active() || !m.treeOpen || m.treeCursor != cursor || m.treeOffset != offset {
		t.Fatal("Esc must preserve explorer state")
	}
}

func TestModel_FileViewerScrollCapturesKeysButPermissionWins(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 5 + topBarHeight})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.viewer.offset == 0 {
		t.Fatal("Down must scroll viewer")
	}
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash"})
	before := m.viewer.offset
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.viewer.offset != before {
		t.Fatal("permission must capture key")
	}
}

func TestModel_FileViewerMouseWheelScrollsFileWithoutMovingHiddenTranscript(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 5 + topBarHeight})
	for i := 0; i < 20; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{ID: fmt.Sprintf("u%d", i), Role: session.RoleUser, Text: fmt.Sprintf("message-%d", i)}})
	}
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	beforeOffset, beforeTranscriptOffset := m.viewer.offset, m.viewport.YOffset
	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelUp,
		X:      m.treePanelWidth() + 2,
		Y:      topBarHeight,
	})
	if m.viewer.offset >= beforeOffset {
		t.Fatalf("wheel up viewer offset = %d, want less than %d", m.viewer.offset, beforeOffset)
	}
	if m.viewport.YOffset != beforeTranscriptOffset {
		t.Fatalf("hidden transcript offset = %d, want %d", m.viewport.YOffset, beforeTranscriptOffset)
	}
}

func TestModel_FileViewerTrackpadScrollKeepsTabbedRowsWithinLayout(t *testing.T) {
	content := strings.Repeat("\tfield string // comment that must not wrap the terminal row\n", 12)
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"tabs.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"tabs.go": []byte(content)}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 6 + topBarHeight})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	for range 12 {
		m = apply(t, m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
			X:      m.treePanelWidth() + 2,
			Y:      topBarHeight,
		})
	}
	if got, want := m.viewer.offset, 7; got != want {
		t.Fatalf("viewer offset after continuous wheel events = %d, want %d", got, want)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "\t") {
			t.Fatalf("View() retains terminal tab: %q", line)
		}
		if width := ansi.StringWidth(line); width > 80 {
			t.Fatalf("View() overflows terminal width %d: %q", width, line)
		}
	}
}

func TestModel_FileViewerHeightMatchesRenderedLayout(t *testing.T) {
	for _, test := range []struct {
		name     string
		width    int
		height   int
		openTree bool
		wantRows int
	}{
		{name: "full screen", width: 12, height: 24 + topBarHeight, openTree: true, wantRows: 23},
		{name: "split panels", width: 80, height: 24 + topBarHeight, openTree: true, wantRows: 23},
		{name: "without explorer", width: 80, height: 24 + topBarHeight, wantRows: 23},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil)
			m = apply(t, m, tea.WindowSizeMsg{Width: test.width, Height: test.height})
			if test.openTree {
				m = m.toggleTree()
			}
			if got := m.fileViewerHeight(); got != test.wantRows {
				t.Fatalf("fileViewerHeight() = %d, want %d", got, test.wantRows)
			}
		})
	}
}

func TestModel_FileViewerMouseClickDoesNotToggleHiddenThinking(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"one.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"one.txt": []byte("one\n")}))
	m.entries = []entry{{kind: entryReasoning, text: "hidden thought", revealed: len("hidden thought"), duration: time.Second}}
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: 0})
	if m.entries[0].expanded {
		t.Fatal("clicking the viewer must not toggle hidden transcript thinking")
	}
}

func TestModel_FileViewerPreservesTranscriptPositionAcrossIncomingEvents(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"one.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"one.txt": []byte("one\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
	for i := 0; i < 20; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{ID: fmt.Sprintf("u%d", i), Role: session.RoleUser, Text: fmt.Sprintf("message-%d", i)}})
	}
	m = apply(t, m, wheelUp)
	beforeOffset := m.viewport.YOffset
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "later", Role: session.RoleUser, Text: "later message"}})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewport.YOffset != beforeOffset {
		t.Fatalf("transcript offset after viewer = %d, want %d", m.viewport.YOffset, beforeOffset)
	}
}

func TestModel_TreeEnterFileShowsReadFailure(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"gone.go"}, nil }).
		WithFileReader(viewerReader(nil))
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.View(); !strings.Contains(got, "no se puede abrir gone.go") {
		t.Fatalf("View() = %q", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewer.active() || !m.treeOpen {
		t.Fatal("Esc must return to chat while preserving explorer")
	}
}

func TestModel_FileViewerReplacesChatWithHeaderAndGutter(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"main.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"main.go": []byte("package main\nfunc main() {}\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 10})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	view := ansi.Strip(m.View())
	for _, want := range []string{"main.go · 1-2/2", "1", "package main", "2", "func main() {}"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q: %q", want, view)
		}
	}
	if strings.Contains(view, "build ·") {
		t.Fatalf("composer status rendered: %q", view)
	}
}

func TestModel_FileViewerContentKeepsTwoCellMargin(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"main.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"main.go": []byte("package main\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 10})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	for _, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if strings.Contains(line, "package main") {
			wantPrefix := strings.Repeat(" ", m.treePanelWidth()+splitGutter+composerOuterMargin)
			if !strings.HasPrefix(line, wantPrefix) {
				t.Fatalf("viewer content starts at column %d, want the two-cell margin after the explorer: %q", len(line)-len(strings.TrimLeft(line, " ")), line)
			}
			return
		}
	}
	t.Fatal("viewer content line not rendered")
}

func TestModel_FileViewerNarrowTerminalNeverOverflows(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"long.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"long.go": []byte("package extremelylongpackagename\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 12, Height: 6})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "long.go") || !strings.Contains(view, "pack") {
		t.Fatalf("narrow terminal must show the active file viewer: %q", view)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if ansi.StringWidth(line) > 12 {
			t.Fatalf("overflow: %q", line)
		}
	}
}

func TestModel_FileViewerResizeBetweenSplitAndFullScreenKeepsScroll(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 10})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	beforeOffset := m.viewer.offset
	if beforeOffset == 0 {
		t.Fatal("precondition: PgDown must scroll the split viewer so the test exercises offset preservation")
	}
	m = apply(t, m, tea.WindowSizeMsg{Width: 12, Height: 10})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "many.tx") {
		t.Fatalf("narrow viewer must fill the screen: %q", view)
	}
	if m.treeVisible() {
		t.Fatalf("narrow viewer must hide the tree: treePanelWidth=%d width=%d", m.treePanelWidth(), m.width)
	}
	if m.viewer.offset != beforeOffset {
		t.Fatalf("viewer offset after narrow resize = %d, want %d", m.viewer.offset, beforeOffset)
	}
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 10})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "many.txt ·") {
		t.Fatalf("wide viewer must restore split layout: %q", view)
	}
	if !m.treeVisible() {
		t.Fatalf("wide viewer must restore the split layout with the tree visible: treePanelWidth=%d width=%d", m.treePanelWidth(), m.width)
	}
}

func TestModel_ResizeToFullWidthTreeNormalizesFocusAndKeepsItAfterSplitReturns(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: topBarHeight})
	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus before narrow resize = %v, want %v", got, want)
	}

	m = apply(t, m, tea.WindowSizeMsg{Width: 12, Height: 4})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus in full-width tree = %v, want %v: the tree is the only visible focus target", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got, want := m.treeCursor, 0; got != want {
		t.Fatalf("treeCursor after Down in full-width tree = %d, want %d: narrow-layout keys must not keep scrolling the hidden viewer", got, want)
	}

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after split layout returns = %v, want %v: resize normalization must retain the only visible panel's focus", got, want)
	}
}

func TestModel_ClickChatFocusesChatWithoutOverflow(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"main.go"}, nil })
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() + 2,
		Y:      10,
	})

	if got, want := m.focus, chatFocus; got != want {
		t.Fatalf("focus after chat click = %v, want %v", got, want)
	}
	assertNoLineWiderThan(t, m.View(), 80)
}

func TestModel_NarrowViewerFocusChromePreservesHeaderAndGutterWithoutOverflow(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"long-file-name.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"long-file-name.go": []byte("package main\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 12, Height: 6})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := ansi.Strip(m.View())
	for _, want := range []string{"long-fi", "1", "pack"} {
		if !strings.Contains(view, want) {
			t.Fatalf("narrow viewer view must retain %q: %q", want, view)
		}
	}
	assertNoLineWiderThan(t, m.View(), 12)
}

func TestModel_ClickTreeFocusesExplorerAndCapturesKeys(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"first.go", "second.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{
			"first.go":  []byte("package first\n"),
			"second.go": []byte("package second\n"),
		}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 3})

	beforeCursor := m.treeCursor
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 0})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after explorer click = %v, want %v", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})

	if got, want := m.treeCursor, beforeCursor+1; got != want {
		t.Fatalf("treeCursor = %d, want %d: clicking explorer must route tree keys to it", got, want)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, explorer keys must not reach the composer", got)
	}
}

func TestModel_ComposerCursorStartsBlinking(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m.input.Cursor.BlinkSpeed = time.Millisecond

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() = nil, want composer cursor blink command")
	}
	if m.input.Cursor.Blink {
		t.Fatal("composer cursor starts hidden, want it visible while chat is focused")
	}
	blinkMsg := cmd()
	updated, next := m.Update(blinkMsg)
	m = updated.(Model)
	if next == nil {
		t.Fatal("initial cursor blink message did not schedule the next blink")
	}
	m = apply(t, m, next())
	if !m.input.Cursor.Blink {
		t.Fatal("composer cursor did not toggle after its blink interval")
	}
}

func TestModel_ComposerCursorFollowsPanelFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"one.go"}, nil })
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if m.input.Focused() {
		t.Fatal("composer remains focused while explorer owns keyboard input")
	}

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() + 2,
		Y:      10,
	})
	if !m.input.Focused() {
		t.Fatal("composer remains blurred after chat regains keyboard focus")
	}
}

func TestModel_ComposerCursorHidesWhileInputGateIsPending(t *testing.T) {
	tests := []struct {
		name    string
		entry   entry
		resolve tea.Msg
	}{
		{
			name:    "permission",
			entry:   entry{kind: entryPermission, sessionID: "s1", callID: "c1"},
			resolve: EventMsg{Kind: session.KindToolFailed, CallID: "c1"},
		},
		{
			name:    "plan approval",
			entry:   entry{kind: entryPlanApproval},
			resolve: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil)
			m.entries = append(m.entries, test.entry)
			m = apply(t, m, struct{}{})
			if m.input.Focused() {
				t.Fatal("composer remains focused while another input gate owns the keyboard")
			}

			m = apply(t, m, test.resolve)
			if !m.input.Focused() {
				t.Fatal("composer remains blurred after the input gate is resolved")
			}
		})
	}
}

func TestModel_ComposerCursorFollowsTerminalFocus(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, tea.BlurMsg{})
	if m.input.Focused() {
		t.Fatal("composer remains focused after the terminal window loses focus")
	}

	m = apply(t, m, tea.FocusMsg{})
	if !m.input.Focused() {
		t.Fatal("composer remains blurred after the terminal window regains focus")
	}
}

func TestModel_TerminalRefocusPreservesExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"one.go"}, nil })
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = apply(t, m, tea.BlurMsg{})
	m = apply(t, m, tea.FocusMsg{})

	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("panel focus after terminal refocus = %v, want %v", got, want)
	}
	if m.input.Focused() {
		t.Fatal("terminal refocus steals keyboard focus from explorer")
	}

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() + 2,
		Y:      10,
	})
	if !m.input.Focused() {
		t.Fatal("composer remains blurred after terminal and chat both regain focus")
	}
}

func TestModel_ClickChatFocusesComposer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"one.go"}, nil })
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() + 2,
		Y:      10,
	})
	if got, want := m.focus, chatFocus; got != want {
		t.Fatalf("focus after chat click = %v, want %v", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})

	if got, want := m.input.Value(), "hola"; got != want {
		t.Fatalf("input.Value() = %q, want %q: clicking chat must route typed runes to the composer", got, want)
	}
}

func TestModel_ClickViewerFocusesFileScroll(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 5 + topBarHeight})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: topBarHeight})

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: 4 + topBarHeight})
	if got, want := m.focus, chatFocus; got != want {
		t.Fatalf("focus after chat click = %v, want %v", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if got := m.viewer.offset; got != 0 {
		t.Fatalf("viewer offset = %d after chat focus, want 0: chat keys must not scroll the file", got)
	}

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: topBarHeight})
	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus after viewer click = %v, want %v", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if got := m.viewer.offset; got == 0 {
		t.Fatal("clicking the viewer must route scroll keys to the file")
	}
}

func TestModel_ClickChatAfterOpeningViewerRoutesMouseWheelToTranscript(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 5 + topBarHeight})
	m = m.toggleTree()
	for i := 0; i < 20; i++ {
		m.entries = append(m.entries, entry{kind: entryUser, text: fmt.Sprintf("message-%d", i)})
	}
	m = m.syncViewport()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp, X: m.treePanelWidth() + 2, Y: 4 + topBarHeight})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: topBarHeight})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: topBarHeight})
	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus after viewer click = %v, want %v", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: 4 + topBarHeight})
	if got, want := m.focus, chatFocus; got != want {
		t.Fatalf("focus after chat click = %v, want %v", got, want)
	}
	viewerOffset := m.viewer.offset
	transcriptOffset := m.viewport.YOffset

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: m.treePanelWidth() + 2, Y: 4 + topBarHeight})

	if got, want := m.viewer.offset, viewerOffset; got != want {
		t.Fatalf("viewer offset = %d, want %d: wheel after chat click must not scroll the file", got, want)
	}
	if got := m.viewport.YOffset; got <= transcriptOffset {
		t.Fatalf("transcript offset = %d, want greater than %d: wheel after chat click must scroll chat transcript", got, transcriptOffset)
	}
}

func TestModel_MouseWheelRoutesByPointerWithoutChangingKeyboardFocus(t *testing.T) {
	t.Run("narrow split chat scrolls from its leftmost visible column while explorer keeps keyboard focus", func(t *testing.T) {
		const width, height = 22, 4
		m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
			return []string{"file.go"}, nil
		})
		m = apply(t, m, tea.WindowSizeMsg{Width: width, Height: height})
		m = m.toggleTree()
		for i := 0; i < 20; i++ {
			m.entries = append(m.entries, entry{kind: entryUser, text: fmt.Sprintf("message-%d", i)})
		}
		m = m.syncViewport()
		m.viewport.SetYOffset(0)

		if !m.chatPanelVisible() {
			t.Fatal("narrow split layout must keep a visible chat panel")
		}
		if got, want := m.chatContentWidth(), 1; got != want {
			t.Fatalf("chatContentWidth() = %d, want %d: test must exercise the one-cell-wide chat panel", got, want)
		}
		if got, want := m.focus, explorerFocus; got != want {
			t.Fatalf("focus before wheel = %v, want %v", got, want)
		}
		transcriptOffset := m.viewport.YOffset

		m = apply(t, m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
			X:      m.treePanelWidth() + 1,
			Y:      1,
		})

		if got, want := m.focus, explorerFocus; got != want {
			t.Fatalf("focus after narrow chat wheel = %v, want %v: wheel must not change keyboard focus", got, want)
		}
		if got := m.viewport.YOffset; got <= transcriptOffset {
			t.Fatalf("transcript offset = %d, want greater than %d: wheel over the visible narrow chat panel must scroll chat", got, transcriptOffset)
		}
	})

	t.Run("right transcript scrolls while viewer keeps keyboard focus", func(t *testing.T) {
		m := NewModel(&fakeAgent{}, "s1", nil).
			WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
			WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n")}))
		m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
		m = m.toggleTree()
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		for i := 0; i < 20; i++ {
			m.entries = append(m.entries, entry{kind: entryUser, text: fmt.Sprintf("message-%d", i)})
		}
		m = m.syncViewport()
		m.viewport.SetYOffset(0)

		if got, want := m.focus, viewerFocus; got != want {
			t.Fatalf("focus before wheel = %v, want %v", got, want)
		}
		viewerOffset := m.viewer.offset
		transcriptOffset := m.viewport.YOffset

		m = apply(t, m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
			X:      m.treePanelWidth() + 2,
			Y:      m.fileViewerHeight() + topBarHeight,
		})

		if got, want := m.focus, viewerFocus; got != want {
			t.Fatalf("focus after right transcript wheel = %v, want %v: wheel must not change keyboard focus", got, want)
		}
		if got, want := m.viewer.offset, viewerOffset; got != want {
			t.Fatalf("viewer offset = %d, want %d: wheel over the right transcript must not scroll the focused viewer", got, want)
		}
		if got := m.viewport.YOffset; got <= transcriptOffset {
			t.Fatalf("transcript offset = %d, want greater than %d: wheel over the right transcript must scroll chat", got, transcriptOffset)
		}
	})

	t.Run("right chat scrolls while explorer keeps keyboard focus", func(t *testing.T) {
		const width, height = 80, 8 + topBarHeight
		m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
			return []string{"file-00.go", "file-01.go", "file-02.go", "file-03.go"}, nil
		})
		m = apply(t, m, tea.WindowSizeMsg{Width: width, Height: height})
		m = m.toggleTree()
		for i := 0; i < 20; i++ {
			m.entries = append(m.entries, entry{kind: entryUser, text: fmt.Sprintf("message-%d", i)})
		}
		m = m.syncViewport()
		m.viewport.SetYOffset(0)

		if got, want := m.focus, explorerFocus; got != want {
			t.Fatalf("focus before wheel = %v, want %v", got, want)
		}
		transcriptOffset := m.viewport.YOffset

		m = apply(t, m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
			X:      m.treePanelWidth() + 2,
			Y:      1,
		})

		if got, want := m.focus, explorerFocus; got != want {
			t.Fatalf("focus after right chat wheel = %v, want %v: wheel must not change keyboard focus", got, want)
		}
		if got := m.viewport.YOffset; got <= transcriptOffset {
			t.Fatalf("transcript offset = %d, want greater than %d: wheel over right chat must scroll chat", got, transcriptOffset)
		}
		view := m.View()
		if !strings.Contains(ansi.Strip(view), "message-") {
			t.Fatalf("View() = %q, split layout must keep the chat transcript rendered", view)
		}
		assertNoLineWiderThan(t, view, width)
		if lines := len(strings.Split(view, "\n")); lines > height {
			t.Fatalf("View() has %d lines, want at most terminal height %d: %q", lines, height, view)
		}
	})

	t.Run("right viewer scrolls even while explorer owns keyboard focus", func(t *testing.T) {
		m := NewModel(&fakeAgent{}, "s1", nil).
			WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
			WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n")}))
		m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 10})
		m = m.toggleTree()
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		m.focus = explorerFocus

		viewerOffset := m.viewer.offset
		m = apply(t, m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
			X:      m.treePanelWidth() + 2,
			Y:      topBarHeight,
		})

		if got, want := m.focus, explorerFocus; got != want {
			t.Fatalf("focus after viewer wheel = %v, want %v: wheel must not change keyboard focus", got, want)
		}
		if got := m.viewer.offset; got <= viewerOffset {
			t.Fatalf("viewer offset = %d, want greater than %d: wheel over viewer must scroll the file even without viewer focus", got, viewerOffset)
		}
	})

	t.Run("explorer scrolls even while viewer owns keyboard focus", func(t *testing.T) {
		m := NewModel(&fakeAgent{}, "s1", nil).
			WithCompletions(nil, func() ([]string, error) {
				return []string{"file-00.go", "file-01.go", "file-02.go", "file-03.go", "file-04.go"}, nil
			}).
			WithFileReader(viewerReader(map[string][]byte{"file-00.go": []byte("package main\n")}))
		m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
		m = m.toggleTree()
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

		if got, want := m.focus, viewerFocus; got != want {
			t.Fatalf("focus before wheel = %v, want %v", got, want)
		}
		treeCursor := m.treeCursor
		m = apply(t, m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
			X:      0,
			Y:      3,
		})

		if got, want := m.focus, viewerFocus; got != want {
			t.Fatalf("focus after explorer wheel = %v, want %v: wheel must not change keyboard focus", got, want)
		}
		if got := m.treeCursor; got <= treeCursor {
			t.Fatalf("treeCursor = %d, want greater than %d: wheel over explorer must move the tree even without explorer focus", got, treeCursor)
		}
	})
}

func TestModel_ExplorerSelectionAfterOpeningViewerRoutesTreeKeysAndEsc(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"first.txt", "second.txt", "third.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{
			"first.txt":  []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n"),
			"second.txt": []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n"),
			"third.txt":  []byte("alpha\nbeta\ngamma\ndelta\nepsilon\nzeta\neta\ntheta\niota\n"),
		}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 10})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: topBarHeight})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: topBarHeight})
	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus after viewer click = %v, want %v", got, want)
	}

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 1 + topBarHeight})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after explorer file click = %v, want %v", got, want)
	}
	if got, want := m.viewer.path, "second.txt"; got != want {
		t.Fatalf("viewer.path = %q, want %q", got, want)
	}
	viewerOffset := m.viewer.offset

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got, want := m.treeCursor, 2; got != want {
		t.Fatalf("treeCursor after j = %d, want %d: explorer keys must navigate the tree", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if got, want := m.treeCursor, 1; got != want {
		t.Fatalf("treeCursor after k = %d, want %d: explorer keys must navigate the tree", got, want)
	}
	if got, want := m.viewer.offset, viewerOffset; got != want {
		t.Fatalf("viewer offset = %d, want %d: explorer j/k must not scroll the file", got, want)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.treeOpen {
		t.Fatal("Esc from explorer focus must close the tree")
	}
	if !m.viewer.active() {
		t.Fatal("Esc from explorer focus must not close the viewer")
	}
}

func TestModel_TreeFileClickKeepsExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"first.go", "second.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{
			"first.go":  []byte("package first\n"),
			"second.go": []byte("package second\n"),
		}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: topBarHeight})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 1 + topBarHeight})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after explorer file click = %v, want %v", got, want)
	}

	if got, want := m.viewer.path, "second.go"; got != want {
		t.Fatalf("viewer.path = %q, want %q", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if got, want := m.treeCursor, 0; got != want {
		t.Fatalf("treeCursor = %d, want %d: opening a file from explorer must keep explorer keyboard focus", got, want)
	}
}

func TestModel_ClosingFocusedViewerReturnsChatFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"one.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"one.go": []byte("package one\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: topBarHeight})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: topBarHeight})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("chat")})

	if m.viewer.active() {
		t.Fatal("Esc must close the focused viewer")
	}
	if got, want := m.input.Value(), "chat"; got != want {
		t.Fatalf("input.Value() = %q, want %q: Esc from the focused viewer must return keyboard focus to chat", got, want)
	}
}

// Reproduce: with the tree open, open a file into the viewer (a mouse click
// keeps explorer focus), close the tree with `q`, then close the viewer. The
// chat must expand back to full width — closing the tree via the explorer key
// path used to skip resizeViewport, leaving the chat frozen at the split width.
func TestModel_ClosingTreeWithViewerOpenExpandsChat(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"main.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"main.go": []byte("package main\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	full := m.viewport.Width

	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: topBarHeight})
	if !m.viewer.active() {
		t.Fatal("precondition: clicking a tree file row must open the viewer")
	}
	if m.viewport.Width >= full {
		t.Fatalf("precondition: split viewport width = %d, want narrower than %d", m.viewport.Width, full)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if m.treeOpen {
		t.Fatal("precondition: q must close the tree")
	}
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: topBarHeight})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewer.active() {
		t.Fatal("precondition: Esc must close the focused viewer")
	}

	if got := m.viewport.Width; got != full {
		t.Fatalf("chat viewport width = %d, want %d: closing the tree and viewer must expand the chat to full width", got, full)
	}
}

// TestModel_SubmittingNewActivatesFreshSessionForFuturePrompts drives /new
// through the Model against a real Engine: it is an integration test of the
// presentation layer, so it lives here rather than in the engine package.
func TestModel_SubmittingNewActivatesFreshSessionForFuturePrompts(t *testing.T) {
	// TRIANGULATE: creating a durable row is not enough. The composer must change to the new ID so that the next prompt does not return to the previous session.
	root := t.TempDir()
	store := session.NewMemoryStore()
	if _, err := store.AppendEvent(context.Background(), "s1", session.SessionEvent{
		Kind: session.KindSessionCwd,
		Text: root,
	}); err != nil {
		t.Fatalf("store.AppendEvent(s1, Session.Cwd) = %v, se esperaba nil", err)
	}
	eng := engine.New(engine.Config{
		Root:     root,
		Provider: llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded}),
		Store:    store,
	})
	m := NewModel(eng, "s1", eng.Events())

	m = typeRunes(t, m, "/new")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	sessions, err := store.Sessions(context.Background())
	if err != nil {
		t.Fatalf("store.Sessions() = %v, se esperaba nil", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("store.Sessions() contiene %d sesiones, se esperaban 2", len(sessions))
	}
	newSessionID := ""
	for _, s := range sessions {
		if s.ID != "s1" {
			newSessionID = s.ID
			break
		}
	}
	if newSessionID == "" {
		t.Fatal("no se encontro la sesion creada por /new")
	}
	m = typeRunes(t, m, "continua aqui")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	_, done := collectUntilRunDone(t, eng.Events(), 10*time.Second, nil)
	if done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia", done.Err)
	}
	messages, err := store.Messages(context.Background(), newSessionID, 0)
	if err != nil {
		t.Fatalf("store.Messages(%s, 0) = %v, se esperaba nil", newSessionID, err)
	}
	if len(messages) != 1 || messages[0].Text != "continua aqui" {
		t.Fatalf("mensajes de %s = %+v, se esperaba que el siguiente prompt se enviara a la sesion nueva", newSessionID, messages)
	}
}

// nextMsg and collectUntilRunDone drain the engine channel for Model+Engine integration tests (the engine has its own copies for its tests).
func nextMsg(t *testing.T, ch <-chan tea.Msg, timeout time.Duration) tea.Msg {
	t.Helper()
	select {
	case <-time.After(timeout):
		t.Fatalf("timeout de %v esperando el siguiente mensaje del engine", timeout)
		return nil
	case msg, ok := <-ch:
		if !ok {
			t.Fatal("canal del engine cerrado antes de tiempo")
		}
		return msg
	}
}

func collectUntilRunDone(t *testing.T, ch <-chan tea.Msg, timeout time.Duration, onEvent func(session.SessionEvent)) ([]session.SessionEvent, RunDoneMsg) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var events []session.SessionEvent
	for {
		switch m := nextMsg(t, ch, time.Until(deadline)).(type) {
		case EventMsg:
			ev := session.SessionEvent(m)
			events = append(events, ev)
			if onEvent != nil {
				onEvent(ev)
			}
		case RunDoneMsg:
			return events, m
		default:
			t.Fatalf("mensaje inesperado en el canal del engine: %T", m)
		}
	}
}

// TestModel_PermissionPanelWriteShowsPathAndContent: the write permission
// reuses the compact bash-style panel — muted "Write" label, the target path
// on the first body line, and the content to write below it. The user sees
// what they authorize; none of the generic-panel metadata renders.
func TestModel_PermissionPanelWriteShowsPathAndContent(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithWorkspace("main", "~/dev/atenea")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "write",
		Input: json.RawMessage(`{"path":"notes/plan.txt","content":"line one\nline two\n"}`),
	})

	view := ansi.Strip(m.View())
	for _, want := range []string{
		"Permission required", "Write notes/plan.txt", "line one", "line two", "Deny", "Allow",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, want %q", view, want)
		}
	}
	panel := ansi.Strip(m.permissionPanelView())
	for _, unwanted := range []string{
		"write request", "Requested by", "Working directory", "Allow once", `"content"`,
	} {
		if strings.Contains(panel, unwanted) {
			t.Fatalf("permissionPanelView() = %q, write permission panel must hide %q", panel, unwanted)
		}
	}
}

// TestModel_PermissionPanelEditShowsPatch: the edit permission shows the
// hashline patch verbatim on the compact panel — its [path#HASH] header names
// the file and the hunks carry the change being authorized.
func TestModel_PermissionPanelEditShowsPatch(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithWorkspace("main", "~/dev/atenea")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "edit",
		Input: json.RawMessage(`{"patch":"[tracked.txt#abc123]\nSWAP 1.=1:\n+new line"}`),
	})

	view := ansi.Strip(m.View())
	for _, want := range []string{
		"Permission required", "Edit [tracked.txt#abc123]", "+new line", "Deny", "Allow",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, want %q", view, want)
		}
	}
	if panel := ansi.Strip(m.permissionPanelView()); strings.Contains(panel, `"patch"`) {
		t.Fatalf("permissionPanelView() = %q, edit permission panel must not dump raw JSON", panel)
	}
}

// TestModel_PermissionPanelWebFetchShowsURL: the web_fetch permission shows
// the URL being fetched on the compact panel.
func TestModel_PermissionPanelWebFetchShowsURL(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithWorkspace("main", "~/dev/atenea")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "web_fetch",
		Input: json.RawMessage(`{"url":"https://example.com/docs","prompt":"summarize"}`),
	})

	view := ansi.Strip(m.View())
	for _, want := range []string{"Permission required", "WebFetch https://example.com/docs", "Deny", "Allow"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, want %q", view, want)
		}
	}
}

// TestModel_PermissionPanelGenericFallbackForUnknownTool: a gated tool
// without a dedicated compact renderer (e.g. a future MCP tool) keeps the
// detailed generic panel: tool label, origin, working directory, pretty-JSON
// input and Deny / Allow once.
func TestModel_PermissionPanelGenericFallbackForUnknownTool(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithWorkspace("main", "~/dev/atenea")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "mcp_deploy",
		Input: json.RawMessage(`{"target":"prod"}`),
	})

	view := ansi.Strip(m.View())
	for _, want := range []string{
		"Permission required", "mcp_deploy request", "Requested by main agent", "Allow once",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, want %q", view, want)
		}
	}
}
