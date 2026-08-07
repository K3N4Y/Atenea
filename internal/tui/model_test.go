package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/session/subagent"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
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
	sent []struct {
		sessionID, text string
		images          []session.Image
	}
	planSent []struct {
		sessionID, text string
		images          []session.Image
	}
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
	capabilities   llm.Capabilities
	declared       bool
	reasoning      llm.ReasoningEffort
	autoAccept     map[string]bool
	yoloAuthorized bool
	yoloEnabled    bool
}

func (f *fakeAgent) ReasoningEffort() llm.ReasoningEffort { return f.reasoning }
func (f *fakeAgent) SetReasoningEffort(effort llm.ReasoningEffort) error {
	if effort != "" && effort != llm.ReasoningEffortMinimal && effort != llm.ReasoningEffortLow && effort != llm.ReasoningEffortMedium && effort != llm.ReasoningEffortHigh && effort != llm.ReasoningEffortXHigh && effort != llm.ReasoningEffortMax {
		return fmt.Errorf("unsupported reasoning effort %q", effort)
	}
	f.reasoning = effort
	return nil
}

func (f *fakeAgent) SetAutoAccept(sessionID string, enabled bool) {
	if f.autoAccept == nil {
		f.autoAccept = make(map[string]bool)
	}
	f.autoAccept[sessionID] = enabled
}
func (f *fakeAgent) AutoAcceptEnabled(sessionID string) bool { return f.autoAccept[sessionID] }
func (f *fakeAgent) YoloAuthorized() bool                    { return f.yoloAuthorized }
func (f *fakeAgent) YoloEnabled() bool                       { return f.yoloEnabled }
func (f *fakeAgent) SetYolo(enabled bool) bool {
	if enabled && !f.yoloAuthorized {
		return false
	}
	f.yoloEnabled = enabled
	return true
}

func TestModel_YoloCanLeaveAndReenterOnlyWhenLaunchAuthorized(t *testing.T) {
	fake := &fakeAgent{yoloAuthorized: true, yoloEnabled: true}
	m := NewModel(fake, "s1", nil).WithStatus("build", "model")
	if label := m.composerModelLabel(); !strings.Contains(label, "YOLO") {
		t.Fatalf("label = %q", label)
	}
	for _, input := range []string{"/mode:ask", "/mode:yolo", "/mode"} {
		m = typeRunes(t, m, input)
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil {
			t.Fatalf("%s returned a command", input)
		}
		m = updated.(Model)
	}
	if !fake.yoloEnabled || !strings.Contains(m.entries[len(m.entries)-1].text, "permission mode: yolo") {
		t.Fatalf("mode not restored: %+v", m.entries)
	}

	ordinary := &fakeAgent{}
	m = typeRunes(t, NewModel(ordinary, "s1", nil), "/mode:yolo")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if ordinary.yoloEnabled || m.entries[len(m.entries)-1].kind != entryError {
		t.Fatal("ordinary launch slash-activated YOLO")
	}
}

func TestModel_ModeCommandsStayLocal(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	for _, input := range []string{"/mode:auto-accept", "/mode", "/mode:ask"} {
		m = typeRunes(t, m, input)
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil {
			t.Fatalf("%s returned async command", input)
		}
		m = updated.(Model)
	}
	if len(fake.sent) != 0 || fake.AutoAcceptEnabled("s1") {
		t.Fatalf("commands reached provider or left mode enabled: sent=%v mode=%v", fake.sent, fake.AutoAcceptEnabled("s1"))
	}
}

func TestModel_ReasoningCommandExplainsAvailableEfforts(t *testing.T) {
	fake := &fakeAgent{reasoning: llm.ReasoningEffortHigh}
	m := typeRunes(t, NewModel(fake, "s1", nil), "/reasoning")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/reasoning returned an async command")
	}
	m = updated.(Model)
	if got := m.entries[len(m.entries)-1].text; got != llm.ReasoningHelp(llm.ReasoningEffortHigh) {
		t.Fatalf("reasoning help = %q, want %q", got, llm.ReasoningHelp(llm.ReasoningEffortHigh))
	}
}

func TestModel_ReasoningCommandSetsEffort(t *testing.T) {
	fake := &fakeAgent{}
	m := typeRunes(t, NewModel(fake, "s1", nil), "/reasoning:low")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/reasoning:low returned an async command")
	}
	m = updated.(Model)
	if fake.reasoning != llm.ReasoningEffortLow || m.entries[len(m.entries)-1].text != "reasoning effort: low" {
		t.Fatalf("reasoning = %q, notice = %q", fake.reasoning, m.entries[len(m.entries)-1].text)
	}
}

func TestModel_ModeUnavailableIsAnError(t *testing.T) {
	m := NewModel(struct{ Agent }{Agent: &fakeAgent{}}, "s1", nil)
	m = typeRunes(t, m, "/mode:auto-accept")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	if len(m.entries) == 0 || m.entries[len(m.entries)-1].kind != entryError {
		t.Fatalf("entries = %+v, want genuine mode failure rendered as error", m.entries)
	}
}

func TestModeNoticeAlignsWithTwoCellTranscriptMargin(t *testing.T) {
	got := ansi.Strip(renderEntry(entry{kind: entryNotice, text: "permission mode: auto-accept"}, 80))
	wantPrefix := strings.Repeat(" ", composerOuterMargin) + "permission mode: auto-accept"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("notice = %q, want prefix %q", got, wantPrefix)
	}
}

func TestModeNoticeDoesNotOverflowTinyTerminal(t *testing.T) {
	for width := 1; width <= 4; width++ {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil)
			m = apply(t, m, tea.WindowSizeMsg{Width: width, Height: 12})
			m.Transcript = m.Transcript.appendNotice("permission mode: auto-accept")
			m = m.syncViewport()

			assertNoLineWiderThan(t, m.View(), width)
		})
	}
}

func TestModel_ModeAutocompleteExecutesOnFirstEnter(t *testing.T) {
	for _, tt := range []struct {
		input, status string
		enabled       bool
	}{
		{input: "/mode", status: "permission mode: ask"},
		{input: "/mode:auto-accept", status: "permission mode: auto-accept", enabled: true},
		{input: "/mode:ask", status: "permission mode: ask"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			fake := &fakeAgent{}
			commands := []command.Command{{Name: "mode", BuiltIn: true}, {Name: "mode:auto-accept", BuiltIn: true}, {Name: "mode:ask", BuiltIn: true}}
			m := NewModel(fake, "s1", nil).WithCompletions(commands, nil)
			m = typeRunes(t, m, tt.input)
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if cmd != nil {
				t.Fatal("local mode selection returned an async prompt command")
			}
			m = updated.(Model)
			if got := m.composer.input.Value(); got != "" {
				t.Fatalf("composer = %q, want cleared after one Enter", got)
			}
			if fake.AutoAcceptEnabled("s1") != tt.enabled {
				t.Fatalf("enabled = %v, want %v", fake.AutoAcceptEnabled("s1"), tt.enabled)
			}
			if len(fake.sent) != 0 || len(fake.planSent) != 0 || len(m.composer.history) != 0 {
				t.Fatalf("mode leaked: sent=%v plan=%v history=%v", fake.sent, fake.planSent, m.composer.history)
			}
			if len(m.entries) == 0 || !strings.Contains(m.entries[len(m.entries)-1].text, tt.status) {
				t.Fatalf("status not shown: %+v", m.entries)
			}
			if got := m.entries[len(m.entries)-1].kind; got != entryNotice {
				t.Fatalf("status kind = %v, want informational notice (not error)", got)
			}
		})
	}
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

func (f *fakeAgent) SendPrompt(sessionID string, prompt session.Prompt) (RunHandle, error) {
	f.sent = append(f.sent, struct {
		sessionID, text string
		images          []session.Image
	}{sessionID, prompt.Text, prompt.Images})
	if f.sendErr != nil {
		return RunHandle{}, f.sendErr
	}
	if prompt.Text == "/new" && f.newSessionID != "" {
		return RunHandle{SessionID: f.newSessionID}, nil
	}
	if prompt.Text == "/compact" {
		return RunHandle{SessionID: sessionID}, nil
	}
	return f.nextRun(sessionID), nil
}

func (f *fakeAgent) SendPlanPrompt(sessionID string, prompt session.Prompt) (RunHandle, error) {
	f.planSent = append(f.planSent, struct {
		sessionID, text string
		images          []session.Image
	}{sessionID, prompt.Text, prompt.Images})
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
		subagent.NewTaskTool(subagent.Config{}), tool.NewWebFetchTool(nil),
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
	m.composer.history = []string{"current prompt"}
	m.composer.histIdx = len(m.composer.history)
	m = typeRunes(t, m, "/resume")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil || !m.resumePicker.open || !m.resumePicker.loading {
		t.Fatalf("resume submit = cmd:%v picker:%+v, want async open loading picker", cmd != nil, m.resumePicker)
	}
	if m.composer.input.Value() != "" || len(m.composer.menuItems) != 0 {
		t.Fatalf("composer/menu = %q/%+v, want cleared", m.composer.input.Value(), m.composer.menuItems)
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
	if !slices.Equal(m.composer.history, []string{"first restored prompt", "latest restored prompt"}) || m.composer.histIdx != 2 {
		t.Fatalf("history = %q idx=%d, want restored history at end", m.composer.history, m.composer.histIdx)
	}
}

func TestModel_ResumePickerCapturesKeysAndEscapePreservesChat(t *testing.T) {
	fake := &fakeAgent{resumeSessions: []session.SessionSummary{{ID: "tui-old", Title: "Old session"}}}
	m := NewModel(fake, "tui-current", nil)
	m.entries = []entry{{kind: entryUser, text: "keep chat"}}
	m.composer.history = []string{"keep history"}
	m.composer.histIdx = 1
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
	if m.resumePicker.open || m.sessionID != "tui-current" || len(m.entries) != 1 || m.entries[0].text != "keep chat" || !slices.Equal(m.composer.history, []string{"keep history"}) {
		t.Fatalf("escape changed model: picker:%+v session:%q entries:%+v history:%q", m.resumePicker, m.sessionID, m.entries, m.composer.history)
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
		m.composer.history = []string{"keep history"}
		m = typeRunes(t, m, "/resume")
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = apply(t, updated.(Model), cmd())

		if !m.resumePicker.open || m.resumePicker.loading || m.resumePicker.err == nil || m.resumePicker.err.Error() != "list failed" {
			t.Fatalf("picker = %+v, want closable list failure", m.resumePicker)
		}
		if m.sessionID != "tui-current" || len(m.entries) != 1 || !slices.Equal(m.composer.history, []string{"keep history"}) {
			t.Fatalf("list failure changed current session: session=%q entries=%+v history=%q", m.sessionID, m.entries, m.composer.history)
		}
	})

	t.Run("load", func(t *testing.T) {
		fake := &fakeAgent{
			resumeSessions: []session.SessionSummary{{ID: "tui-target", Title: "Target"}},
			resumeErr:      errors.New("load failed"),
		}
		m := NewModel(fake, "tui-current", nil)
		m.entries = []entry{{kind: entryUser, text: "keep chat"}}
		m.composer.history = []string{"keep history"}
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
		if m.sessionID != "tui-current" || len(m.entries) != 1 || m.entries[0].text != "keep chat" || !slices.Equal(m.composer.history, []string{"keep history"}) || !m.planMode || m.activeRun != 77 || m.followAgent {
			t.Fatalf("load failure changed session: session=%q entries=%+v history=%q plan=%v run=%d follow=%v", m.sessionID, m.entries, m.composer.history, m.planMode, m.activeRun, m.followAgent)
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
	if len(m.composer.menuItems) != 1 || !m.composer.menuItems[0].builtin || m.composer.menuItems[0].label != "/resume" {
		t.Fatalf("menuItems = %+v, want builtin /resume", m.composer.menuItems)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil || !m.resumePicker.open || m.composer.input.Value() != "" || len(m.composer.menuItems) != 0 {
		t.Fatalf("menu Enter = cmd:%v picker:%+v input:%q menu:%+v", cmd != nil, m.resumePicker, m.composer.input.Value(), m.composer.menuItems)
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
	m.composer.history = []string{"keep current history"}
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
	if !m.resumePicker.open || m.sessionID != "tui-current" || len(m.entries) != 1 || m.entries[0].text != "keep current chat" || !slices.Equal(m.composer.history, []string{"keep current history"}) {
		t.Fatalf("stale load replaced current session: picker=%+v session=%q entries=%+v history=%q", m.resumePicker, m.sessionID, m.entries, m.composer.history)
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

	if m.resumePicker.open || m.sessionID != "tui-target" || len(m.entries) != 1 || m.entries[0].text != "target chat" || !slices.Equal(m.composer.history, []string{"target history"}) {
		t.Fatalf("current load = picker:%+v session:%q entries:%+v history:%q", m.resumePicker, m.sessionID, m.entries, m.composer.history)
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
		if strings.Contains(line.line, "● Thought") {
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
	if m.composer.input.Focused() || !m.resumePicker.query.Focused() {
		t.Fatalf("open focus = composer:%v query:%v, want picker-only focus", m.composer.input.Focused(), m.resumePicker.query.Focused())
	}

	m = apply(t, m, tea.BlurMsg{})
	if m.composer.input.Focused() || m.resumePicker.query.Focused() {
		t.Fatalf("terminal blur focus = composer:%v query:%v, want both blurred", m.composer.input.Focused(), m.resumePicker.query.Focused())
	}
	m = apply(t, m, tea.FocusMsg{})
	if m.composer.input.Focused() || !m.resumePicker.query.Focused() {
		t.Fatalf("terminal refocus = composer:%v query:%v, want picker-only focus", m.composer.input.Focused(), m.resumePicker.query.Focused())
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.composer.input.Focused() || m.resumePicker.query.Focused() {
		t.Fatalf("picker close focus = composer:%v query:%v, want composer restored", m.composer.input.Focused(), m.resumePicker.query.Focused())
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
	if !strings.Contains(view, focusStyle.Render("❯")) {
		t.Fatalf("View() does not accent selected indicator: %q", view)
	}
	if !strings.Contains(view, metadataStyle.Render("current")) || !strings.Contains(view, metadataStyle.Render("Jul 14, 2026 09:05")) {
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
			if tt.name == "loading" && !strings.Contains(view, secondaryTextStyle.Render(tt.want)) {
				t.Fatalf("loading state does not use secondary text: %q", view)
			}
			if tt.name == "error" && !strings.Contains(view, dangerStyle.Render(tt.want)) {
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
	if !slices.Equal(m.composer.history, []string{"restored history"}) {
		t.Fatalf("builder history = %q, want supplied by WithHistory", m.composer.history)
	}
}

func TestModel_UndoIsNativeCommandAndRestoresComposer(t *testing.T) {
	images := []session.Image{{MediaType: "image/png", Data: []byte("first")}, {MediaType: "image/jpeg", Data: []byte("third")}}
	fake := &fakeAgent{undoResult: UndoResult{
		Prompt: session.Prompt{Text: "original [image#1] prompt [image#3]", Images: images},
		Events: []session.SessionEvent{{Message: &session.Message{ID: "u0", Role: session.RoleUser, Text: "kept"}}},
	}}
	m := NewModel(fake, "s1", nil)
	m.entries = []entry{{kind: entryUser, text: "old"}, {kind: entryAssistant, text: "answer"}}
	m.composer.history = []string{"old prompt"}
	m.composer.histIdx = len(m.composer.history)
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
	if m.composer.input.Value() != "original [image#1] prompt [image#3]" || m.composer.input.Position() != len([]rune("original [image#1] prompt [image#3]")) {
		t.Fatalf("composer = %q cursor=%d", m.composer.input.Value(), m.composer.input.Position())
	}
	if len(m.composer.history) != 1 || m.composer.history[0] != "old prompt" {
		t.Fatalf("history = %v", m.composer.history)
	}
	m.composer = m.composer.attachImage([]byte("next"))
	if got := m.composer.value(); !strings.HasSuffix(got, "[image#4]") {
		t.Fatalf("composer after paste = %q, want next non-conflicting marker", got)
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("restored prompt must resubmit")
	}
	if len(fake.sent) != 1 || fake.sent[0].text != "original [image#1] prompt [image#3][image#4]" || len(fake.sent[0].images) != 3 {
		t.Fatalf("resubmitted prompt = %+v, want restored text and all images", fake.sent)
	}
	for i, want := range [][]byte{[]byte("first"), []byte("third"), []byte("next")} {
		if !slices.Equal(fake.sent[0].images[i].Data, want) {
			t.Fatalf("resubmitted image %d = %q, want %q", i, fake.sent[0].images[i].Data, want)
		}
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
	if m.composer.input.Value() != "/undo" {
		t.Fatalf("composer = %q", m.composer.input.Value())
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
	if len(m.composer.menuItems) == 0 || m.composer.menuItems[0].label != "/undo" {
		t.Fatalf("menuItems = %+v", m.composer.menuItems)
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
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	return settleDiskWork(t, next)
}

func settleDiskWork(t *testing.T, m Model) Model {
	t.Helper()
	for {
		switch {
		case m.composer.filesLoading:
			files, err := m.listFiles()
			updated, _ := m.Update(filesListedMsg{target: fileListMenu, generation: m.composer.filesGen, files: files, err: err})
			m = updated.(Model)
		default:
			return m
		}
	}
}

func TestModel_DiskWorkRunsOutsideUpdate(t *testing.T) {
	t.Run("typing mention defers workspace listing", func(t *testing.T) {
		calls := 0
		m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
			calls++
			return []string{"go.mod"}, nil
		})

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})
		if _, ok := updated.(Model); !ok {
			t.Fatalf("Update returned %T, expected tui.Model", updated)
		}
		if calls != 0 {
			t.Fatalf("listFiles calls during Update = %d, want 0", calls)
		}
	})
}

func TestModel_AsyncDiskWorkTracksLoadingErrorsAndLatestResult(t *testing.T) {
	t.Run("mention shows loading then files", func(t *testing.T) {
		m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
			return []string{"internal/tui/model.go"}, nil
		})
		m.composer.input.SetValue("@")
		m.composer.input.CursorEnd()

		m, cmd := m.refreshMenu()
		if cmd == nil || !m.composer.filesLoading || len(m.composer.menuItems) != 1 || m.composer.menuItems[0].label != "Loading files…" {
			t.Fatalf("mention loading state = loading:%v cmd:%v items:%+v", m.composer.filesLoading, cmd != nil, m.composer.menuItems)
		}
		m = apply(t, m, cmd())
		if m.composer.filesLoading || len(m.composer.menuItems) != 1 || m.composer.menuItems[0].label != "internal/tui/model.go" {
			t.Fatalf("mention result state = loading:%v items:%+v", m.composer.filesLoading, m.composer.menuItems)
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
	t.Fatalf("the reveal backlog did not drain after 1000 ticks")
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
	if got := m.composer.input.Value(); got != "/model openrouter openai/chatgpt5.5 " {
		t.Fatalf("first Enter completed %q", got)
	}
	m = apply(t, m, ModelsRefreshedMsg{Providers: agent.models})
	if len(m.composer.menuItems) != 0 {
		t.Fatalf("refresh reopened popup over canonical command: %#v", m.composer.menuItems)
	}
	if len(agent.sent) != 0 || len(m.composer.history) != 0 {
		t.Fatalf("/model leaked to prompts/history: sent=%v history=%v", agent.sent, m.composer.history)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.model; got != "openai/chatgpt5.5" {
		t.Fatalf("footer model = %q", got)
	}
	if len(agent.selected) != 1 || agent.selected[0].providerID != "openrouter" || agent.selected[0].model != "openai/chatgpt5.5" {
		t.Fatalf("selected = %#v", agent.selected)
	}
	if got := m.composer.input.Value(); got != "" {
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
	if len(agent.sent) != 0 || len(m.composer.history) != 0 {
		t.Fatalf("/model leaked to prompts/history: sent=%v history=%v", agent.sent, m.composer.history)
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

	line := lineWith(t, ansi.Strip(m.View()), "❯ OpenR")
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

func TestModel_ModelPickerSeparatesSelectionAndActiveState(t *testing.T) {
	agent := &fakeAgent{
		models: []providerconfig.ProviderModels{{ID: "openrouter", Name: "OpenRouter", Models: []string{"model-a"}}},
		active: providerconfig.Active{ProviderID: "openrouter", Model: "model-a"},
	}
	m := NewModel(agent, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 16})
	m = typeRunes(t, m, "/model")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := ansi.Strip(m.View())
	providerLine := lineWith(t, view, "❯ OpenR")
	if !strings.Contains(providerLine, "active") {
		t.Fatalf("selected provider row = %q, want separate active state", providerLine)
	}
	if !strings.Contains(view, "● model-a") {
		t.Fatalf("unfocused active model view = %q, want active marker", view)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRight})
	view = ansi.Strip(m.View())
	modelLine := lineWith(t, view, "❯ model-a")
	if !strings.Contains(modelLine, "active") {
		t.Fatalf("selected model row = %q, want separate active state", modelLine)
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
	t.Fatalf("View() = %q, no line contains %q", view, needle)
	return -1
}

// assertNoLineWiderThan fails if any view line exceeds visible cells width (width of the terminal); Measure with lipgloss.Width to ignore ANSI.
func assertNoLineWiderThan(t *testing.T, view string, width int) {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Fatalf("View() = %q, line %q is %d visible cells wide, no line may exceed terminal width (%d)", view, line, w, width)
		}
	}
}

// assertBoxLinesExactWidth fails if any composer box line (those that contain a border character ┌/│/└ after the margin) does not measure exactly the width of visible cells, or if the view does not contain any. Measure with ansi.StringWidth to ignore ANSI codes.
func assertBoxLinesExactWidth(t *testing.T, view string, width int) {
	t.Helper()
	found := false
	for _, line := range strings.Split(view, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		for _, prefix := range []string{"┌", "│", "└"} {
			if strings.HasPrefix(trimmed, prefix) {
				found = true
				if w := ansi.StringWidth(line); w != width {
					t.Fatalf("View() = %q, composer box line %q is %d visible cells wide, every box line must exactly match terminal width (%d)", view, line, w, width)
				}
			}
		}
	}
	if !found {
		t.Fatalf("View() = %q, contains no composer box line (borders ┌/│/└)", view)
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

	view := renderEntry(entry{kind: entryUser, text: "who are you and what are you capable of?"}, 80)
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
	if got, want := lines[1], "  ┃   who are you and what are you capable of?"; !strings.HasPrefix(got, want) {
		t.Fatalf("middle line = %q, want prefix %q", got, want)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "  ┃") {
			t.Fatalf("user message row %d = %q, every block row must carry the left rail", i, line)
		}
	}
	if strings.Contains(plain, "❯") {
		t.Fatalf("user message must use a rail instead of the input arrow:\n%q", plain)
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
		Text: "a sufficiently long message to wrap inside the block",
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

func TestModel_UserMessageWrapKeepsRailAtBlockLeft(t *testing.T) {
	for width := 16; width <= 80; width++ {
		m := NewModel(nil, "s1", nil)
		m = apply(t, m, tea.WindowSizeMsg{Width: width, Height: 100})
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   "u1",
			Role: session.RoleUser,
			Text: "I want you to commit the changes in English using\nconventional commit",
		}})

		plain := ansi.Strip(m.View())
		messageRows := 0
		for _, line := range strings.Split(plain, "\n") {
			if strings.Contains(line, "┃") {
				messageRows++
				if !strings.HasPrefix(line, "  ┃") {
					t.Fatalf("width %d user rail is not at the block's left edge: %q\n%s", width, line, plain)
				}
			}
		}
		if messageRows < 4 {
			t.Fatalf("width %d user rail rows = %d, want rail on padding and wrapped content rows:\n%s", width, messageRows, plain)
		}
	}
}

func TestEntry_UserMessageKeepsRailWithinTinyWidths(t *testing.T) {
	for width := 0; width <= 5; width++ {
		plain := ansi.Strip(renderEntry(entry{kind: entryUser, text: "longword\nnext"}, width))
		lines := strings.Split(plain, "\n")
		if len(lines) < 4 {
			t.Fatalf("width %d user message rows = %d, want padded multiline block:\n%q", width, len(lines), plain)
		}
		for row, line := range lines {
			if !strings.Contains(line, "┃") {
				t.Fatalf("width %d user message row %d lost its rail: %q", width, row, line)
			}
			if width > 0 && ansi.StringWidth(line) > width {
				t.Fatalf("width %d user message row %d has width %d: %q", width, row, ansi.StringWidth(line), line)
			}
		}
	}
}

func TestModel_UserMessagePaintsRailAndBodyBackground(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hello"}})

	backgroundParams := "48;2;36;36;36"
	for _, row := range strings.Split(m.View(), "\n") {
		if !strings.Contains(row, "┃") {
			continue
		}
		beforeRail, afterRail, found := strings.Cut(row, "┃")
		if !found || !strings.Contains(beforeRail, backgroundParams) {
			t.Fatalf("user rail cell must paint #242424: %q", row)
		}
		if !strings.Contains(afterRail, backgroundParams) {
			t.Fatalf("user message body must restore #242424 after the faint rail: %q", row)
		}
		return
	}
	t.Fatal("user message rendered no rail")
}

func TestModel_FoldsStreamingAssistantText(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "Hello "})
	m = drainReveal(t, m)
	if got := ansi.Strip(m.View()); !strings.Contains(got, "Hello") {
		t.Fatalf("View() without ANSI = %q, must contain %q after the first delta", got, "Hello")
	}

	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "world"})
	m = drainReveal(t, m)
	if got := ansi.Strip(m.View()); !strings.Contains(got, "Hello world") {
		t.Fatalf("View() without ANSI = %q, must contain %q after accumulating deltas", got, "Hello world")
	}

	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "Hello world"},
	})
	if got, count := m.View(), strings.Count(m.View(), "Hello world"); count != 1 {
		t.Fatalf("View() = %q, %q must appear exactly once (count=%d): closing the turn must not duplicate the live block with the coalesced Message", got, "Hello world", count)
	}
}

// Assistant render contract: while the block is live (TextStarted/TextDelta only, no StepEnded) the text is displayed flat as it arrives; When the turn closes (StepEnded sets live = false) the text is rendered as markdown: the raw markers (** and "-") disappear and the content is formatted (emphasis applied, lists with bullets).
func TestModel_RendersClosedAssistantAsMarkdown(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	text := "Hello **strong** statement.\n\n- item one\n- item two"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "strong") {
		t.Fatalf("View() = %q, must contain %q: rendering Markdown must not lose the content", view, "strong")
	}
	if strings.Contains(view, "**") {
		t.Fatalf("View() = %q, must NOT contain %q: closed Markdown renders emphasis instead of showing it raw", view, "**")
	}
	if strings.Contains(view, "- item one") {
		t.Fatalf("View() = %q, must NOT contain %q: closed Markdown renders the raw list dash as a bullet", view, "- item one")
	}
	if !strings.Contains(view, "item one") {
		t.Fatalf("View() = %q, must contain %q: rendering the list must not lose its items", view, "item one")
	}
	if !strings.Contains(view, "item two") {
		t.Fatalf("View() = %q, must contain %q: rendering the list must not lose its items", view, "item two")
	}
	if !strings.Contains(view, "•") {
		t.Fatalf("View() = %q, must contain %q: Markdown list items render with bullets", view, "•")
	}
}
func TestModel_LiveAssistantRendersMarkdownBeforeClosed(t *testing.T) {
	// TRIANGULATE: The renderer must apply Markdown to both the prefix revealed during streaming and the entire content when settling the block.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	text := "this is **strong** live"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = drainReveal(t, m)

	view := ansi.Strip(m.View())
	if strings.Contains(view, "**") {
		t.Fatalf("View() without ANSI = %q, must NOT contain raw Markdown markers while the block is live", view)
	}
	if !strings.Contains(view, "strong") {
		t.Fatalf("View() without ANSI = %q, must contain %q while the block is live", view, "strong")
	}

	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})

	view = ansi.Strip(m.View())
	if strings.Contains(view, "**") {
		t.Fatalf("View() = %q, must NOT contain %q after closing the block", view, "**")
	}
	if !strings.Contains(view, "strong") {
		t.Fatalf("View() = %q, must contain %q: rendering Markdown must not lose content", view, "strong")
	}
}

func TestModel_ClosedMarkdownWrapsToTerminalWidth(t *testing.T) {
	// TRIANGULATE: a renderMarkdown that ignores width (WithWordWrap(0) always), or that passes the full width without discounting the margin of the glamor document, produces lines wider than the terminal. The emergency wrapping of the viewport (ansi.Wrap in syncViewport) re-splits them and leaves orphaned words without margin in column 0. The closed markdown must be wrapped to the width of the terminal by the renderer itself: all the text visible and each line wrapped preserving its margin.
	m := NewModel(nil, "s1", nil)
	// Height 24: glamour v1 wraps this paragraph into more lines than v0.8 and
	// the whole block must stay visible for the per-line margin asserts below.
	m = apply(t, m, tea.WindowSizeMsg{Width: 30, Height: 24})

	// Hyphen-free sentinel: glamour v1 breaks lines at hyphens.
	text := "this long paragraph with **emphasis** must wrap to the terminal's narrow width so it can be read in full up to the finmarkdown token"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "finmarkdown") {
		t.Fatalf("View() = %q, the end of text %q must be visible: closed markdown must wrap to terminal width, not truncate", view, "finmarkdown")
	}
	assertNoLineWiderThan(t, view, 30)
	for _, token := range []string{"emphasis", "finmarkdown"} {
		line := ansi.Strip(lineWith(t, view, token))
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("line with %q = %q, must preserve the markdown render margin: a rendered line wider than the terminal is rewrapped by the viewport's emergency wrapping and leaves the remainder orphaned at column 0", token, line)
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
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "- single item"},
	})
	view := m.View()
	if strings.Contains(view, "- single item") {
		t.Fatalf("View() = %q, must NOT contain %q: the raw list dash renders as a bullet even when text arrives through a StepEnded Message without prior deltas", view, "- single item")
	}
	if !strings.Contains(view, "single item") {
		t.Fatalf("View() = %q, must contain %q: rendering the list must not lose the item", view, "single item")
	}
	if !strings.Contains(view, "•") {
		t.Fatalf("View() = %q, must contain %q: rendering the Markdown list must not lose the item", view, "•")
	}
}

func TestEntryAssistant_RenderRendersRevealedMarkdownWhileLive(t *testing.T) {
	entry := entry{
		kind:     entryAssistant,
		text:     "**Hello** world",
		live:     true,
		revealed: len([]rune("**Hello**")),
	}

	rendered := ansi.Strip(renderEntry(entry, 80))
	if strings.Contains(rendered, "**Hello**") {
		t.Fatalf("render(80) = %q, must not contain raw Markdown markers while the assistant is live", rendered)
	}
	if !strings.Contains(rendered, "Hello") {
		t.Fatalf("render(80) = %q, must contain the revealed Markdown text", rendered)
	}
	if strings.Contains(rendered, "world") {
		t.Fatalf("render(80) = %q, must not reveal pending backlog %q", rendered, "world")
	}
}

func TestEntryAssistant_RenderRendersRevealedListWhileLiveAndCompleteListWhenSettled(t *testing.T) {
	entry := entry{kind: entryAssistant, text: "- visible item\n- pending item", live: true, revealed: len([]rune("- visible item\n"))}
	live := ansi.Strip(renderEntry(entry, 80))
	if !strings.Contains(live, "•") || !strings.Contains(live, "visible item") {
		t.Fatalf("render(80) live = %q, must render the revealed item as a Markdown list", live)
	}
	if strings.Contains(live, "pending item") {
		t.Fatalf("render(80) live = %q, must not reveal the pending item", live)
	}
	entry.live = false
	entry.revealed = len([]rune(entry.text))
	settled := ansi.Strip(renderEntry(entry, 80))
	for _, want := range []string{"•", "visible item", "pending item"} {
		if !strings.Contains(settled, want) {
			t.Fatalf("render(80) settled = %q, must contain %q", settled, want)
		}
	}
}

// Assistant settled text color contract: closed Markdown uses the terminal default color, not glamour's gray 252.
func TestModel_AssistantMarkdownUsesDefaultForeground(t *testing.T) {
	forceANSI256Profile(t)
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	text := "settled-text with **emphasis** from the assistant"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text}})
	m = drainReveal(t, m)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "settled-text") {
		t.Fatalf("View() without ANSI = %q, must contain settled text", view)
	}
	if strings.Contains(view, "38;5;252") {
		t.Fatalf("View() = %q, must NOT contain SGR sequence %q", view, "38;5;252")
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

func TestModel_AssistantMarkdownKeepsEditorialContentNeutral(t *testing.T) {
	// Headings use weight and links use underline; neither borrows the focus
	// accent because no Markdown content is actively selected here.
	forceANSI256Profile(t)

	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	text := "# Titulo\n\n[docs](https://example.com)"
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
	markdown := renderMarkdown(text, 80)
	if strings.Contains(markdown, "36") {
		t.Fatalf("renderMarkdown() = %q, static Markdown content must not use the interactive accent", markdown)
	}
	if !strings.Contains(markdown, "\x1b[4m") {
		t.Fatalf("renderMarkdown() = %q, links must remain discoverable through underline", markdown)
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
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hello atenea"}})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "hello human"})
	m = drainReveal(t, m)

	view := m.View()
	userLine := lineWith(t, ansi.Strip(view), "hello atenea")
	if !strings.HasPrefix(userLine, "  ┃   ") {
		t.Fatalf("user line = %q, must carry rail %q at the block's left edge", userLine, "  ┃")
	}
	assistantLine := lineWith(t, ansi.Strip(view), "hello human")
	if strings.Contains(assistantLine, "┃") {
		t.Fatalf("assistant line = %q, must NOT carry the user rail %q", assistantLine, "┃")
	}
}

func TestModel_RendersToolCallLifecycle(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	if got := m.View(); !strings.Contains(got, "● Bash     ls") {
		t.Fatalf("View() = %q, Tool.Called must show the ToolName with the Input summary and the running marker %q", got, "● Bash     ls")
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "file.txt",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "file.txt", ToolCallID: "c1"},
	})
	if got := m.View(); !strings.Contains(got, "✓ Bash     ls") {
		t.Fatalf("View() = %q, Tool.Success must settle the tool as %q", got, "✓ Bash     ls")
	}
	if got := m.View(); strings.Contains(got, "●") {
		t.Fatalf("View() = %q, the settled tool must not still appear as running", got)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "edit", Input: json.RawMessage(`{"input":"[a.go#ab12]\n"}`)})
	if got := m.View(); !strings.Contains(got, "● Edit     a.go") {
		t.Fatalf("View() = %q, the second tool call must appear as running with the patch file %q", got, "● Edit     a.go")
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c2", ToolName: "edit", Error: "permission denied"})
	got := m.View()
	for _, want := range []string{"× Edit     a.go", "│ error: permission denied"} {
		if !strings.Contains(got, want) {
			t.Fatalf("View() = %q, Tool.Failed must show %q: the header with the Input summary and the Error as a rail line", got, want)
		}
	}
	if !strings.Contains(got, "✓ Bash     ls") {
		t.Fatalf("View() = %q, c2's failure must not affect c1's successful state", got)
	}
	if strings.Contains(got, "●") {
		t.Fatalf("View() = %q, no tool should remain running", got)
	}
}

// Contract for the "task" tool render: the header reads `SubAgent <type>`
// (the subagent_type field of the Input, never the raw JSON), the running
// marker animates, and a completed report shows at most its first three lines.
func TestModel_RendersTaskToolAsSubAgentWithSpinner(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "task", Input: json.RawMessage(`{"subagent_type":"explorer","prompt":"find the config loader"}`)})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "● SubAgent explorer") {
		t.Fatalf("View() = %q, a running task must render as %q (SubAgent + subagent_type)", view, "● SubAgent explorer")
	}
	if strings.Contains(view, `{"subagent_type"`) {
		t.Fatalf("View() = %q, the raw Input JSON must not leak into the header", view)
	}

	m.working = true
	m = apply(t, m, spinner.TickMsg{})
	frame := ansi.Strip(m.spinner.View())
	view = ansi.Strip(m.View())
	if !strings.Contains(view, frame+" SubAgent explorer") {
		t.Fatalf("View() = %q, a running task must animate its marker with the spinner frame %q", view, frame)
	}
	if strings.Contains(view, "● SubAgent") {
		t.Fatalf("View() = %q, the spinner frame must replace the static run marker", view)
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "task", Text: "scope: project\nagent: explorer\nsummary: found loader\nfull subagent report",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "scope: project\nagent: explorer\nsummary: found loader\nfull subagent report", ToolCallID: "c1"},
	})
	got := ansi.Strip(m.View())
	for _, want := range []string{"✓ SubAgent explorer", "scope: project", "agent: explorer", "summary: found loader"} {
		if !strings.Contains(got, want) {
			t.Fatalf("View() = %q, a finished task must show %q", got, want)
		}
	}
	if strings.Contains(got, "full subagent report") {
		t.Fatalf("View() = %q, task output must stop after its first three lines", got)
	}
}

// Tool "skill" render contract: uses the activity grammar with the name of the skill as a summary (`● Skill <name>`), where the name is the "name" field of the Input JSON, without filtering the raw Input to the header. On success, the header goes WITHOUT a preview of the output: the body of the SKILL.md that travels in ev.Text is for the model, not for the transcript.
func TestModel_RendersSkillToolAsSkillLine(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "skill", Input: json.RawMessage(`{"name":"code-review"}`)})
	view := m.View()
	if !strings.Contains(view, "● Skill    code-review") {
		t.Fatalf("View() = %q, the running skill tool must render as a dedicated line %q (name = name field of Input)", view, "● Skill    code-review")
	}
	if strings.Contains(view, `{"name"`) {
		t.Fatalf("View() = %q, the raw Input must not leak into the header: the dedicated line uses the bare name as its summary", view)
	}

	body := "<skill_content name=\"code-review\">\nskill body for the model\n</skill_content>"
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "skill", Text: body,
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: body, ToolCallID: "c1"},
	})
	view = m.View()
	if !strings.Contains(view, "✓ Skill    code-review") {
		t.Fatalf("View() = %q, the successful skill tool must settle as %q", view, "✓ Skill    code-review")
	}
	if strings.Contains(view, "skill_content") {
		t.Fatalf("View() = %q, must not contain %q: the successful skill line has no output preview, and the SKILL.md body is for the model, not the transcript", view, "skill_content")
	}
}

func TestModel_SkillToolFailureShowsError(t *testing.T) {
	// TRIANGULATE: a poor implementation of renderSkill only covers the running/ok states and leaves the ● marker forever before Tool.Failed. A skill failure (for example, a missing name) uses the same dedicated line with the × marker and the error as a rail line, just like the other tools.
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "skill", Input: json.RawMessage(`{"name":"missing"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c1", ToolName: "skill", Error: `skill "missing" not found`})

	view := m.View()
	for _, want := range []string{"× Skill    missing", `│ error: skill "missing" not found`} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, the failed skill must settle as %q: the dedicated line also covers the error state, not only running/ok", view, want)
		}
	}
	if strings.Contains(view, "●") {
		t.Fatalf("View() = %q, the skill settled with an error must not still appear as running", view)
	}
}

func TestModel_SkillToolWithoutNameRendersBareHeader(t *testing.T) {
	// TRIANGULATE: a poor implementation assumes that the Input of the skill is valid JSON (panic or garbage in the header when parsing it) when it cannot extract the name. With non-parseable Input the header is "● Skill" stripped: without summary, without dangling spaces in the alignment and without filtering the raw input to the transcript.
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "skill", Input: json.RawMessage(`not-json`)})

	view := m.View()
	if !strings.Contains(view, "● Skill") {
		t.Fatalf("View() = %q, with unparseable Input the skill must render with the bare header %q", view, "● Skill")
	}
	skillLine := lineWith(t, view, "● Skill")
	if got := strings.TrimRight(skillLine, " "); got != "  ● Skill" {
		t.Fatalf("skill line = %q, the bare header has no summary: it remains %q without inheriting anything from Input", skillLine, "  ● Skill")
	}
	if strings.Contains(view, "no-es-json") {
		t.Fatalf("View() = %q, the raw Input %q must not leak into the transcript", view, "not-json")
	}
}

func TestModel_ReadToolShowsOnlyStatusAndFileName(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"internal/tui/view.go:20-40"}`)})

	view := ansi.Strip(m.View())
	if want := "  ● Reading  view.go"; !strings.Contains(view, want) {
		t.Fatalf("View() without ANSI = %q, read while running must show only %q", view, want)
	}
	if strings.Contains(view, "internal/tui") || strings.Contains(view, ":20-40") {
		t.Fatalf("View() without ANSI = %q, read must not show the path or selector", view)
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "read", Text: "[internal/tui/view.go#ABCD]\n20:package tui",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "[internal/tui/view.go#ABCD]\n20:package tui", ToolCallID: "c1"},
	})

	view = ansi.Strip(m.View())
	if want := "  ✓ Read     view.go"; !strings.Contains(view, want) {
		t.Fatalf("View() without ANSI = %q, successful read must show only %q", view, want)
	}
	for _, hidden := range []string{"Reading", "internal/tui", "20:package tui", "ABCD"} {
		if strings.Contains(view, hidden) {
			t.Fatalf("View() without ANSI = %q, successful read must not show %q", view, hidden)
		}
	}
}

// Tool call detail contract: the header carries the summary of the Input (`✓ <name> <summary>`; with a single string field the summary is its value) and Tool.Success brings the output in ev.Text, which is displayed under the header with each line of rail `│ ` up to 4 lines; with more lines a final mark appears `│ … +N lines`. With 3 output lines they all fit: no truncation mark should appear.
func TestModel_ToolSuccessShowsOutputPreview(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls -la"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "one\ntwo\nthree",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "one\ntwo\nthree", ToolCallID: "c1"},
	})
	m.entries[len(m.entries)-1].expanded = true
	m = m.syncViewport()

	view := m.View()
	if !strings.Contains(view, "✓ Bash     ls -la") {
		t.Fatalf("View() = %q, the header must include the Input summary %q: with one string field the summary is its value", view, "✓ Bash     ls -la")
	}
	for _, want := range []string{"│ one", "│ two", "│ three"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, must contain %q: each Tool.Success output line appears below the header with the rail prefix", view, want)
		}
	}
	if strings.Contains(view, "lines") {
		t.Fatalf("View() = %q, must not contain the truncation marker %q: 3 output lines fit within the limit of 4 and are shown in full", view, "lines")
	}
}

// Edit success renders the rich diff card instead of the generic activity line: a file-path bar, the "@@ … @@" hunk header, then the removed side ("before", red) above the added side ("after", green), each line numbered with its real file line. The output preview ("ok") is dropped: the diff IS the result worth reviewing.
func TestModel_ToolSuccessShowsEditDiff(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "edit", Input: json.RawMessage(`{"path":"a.go"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "edit", Text: "ok",
		Diff:    "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	for _, want := range []string{"a.go", "@@ -1 +1 @@", "1 - old", "1 + new"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("View() without ANSI = %q, must contain %q: successful edit renders as a diff card with path, hunk, and before/after blocks", plain, want)
		}
	}
	// The red block ("before", removed) goes above the green ("after", added).
	if i, j := strings.Index(plain, "1 - old"), strings.Index(plain, "1 + new"); i < 0 || j < 0 || i > j {
		t.Fatalf("View() without ANSI = %q, removed block must precede the added block", plain)
	}
	// The card is inserted like the rest of the content: the row opens with the margin (activityInset) and the rail ▌ in the same column as "✓ Read".
	if row := lineWith(t, plain, "1 - old"); !strings.HasPrefix(row, activityInset+"▌") {
		t.Fatalf("diff row = %q, must begin with the margin %q and rail ▌", row, activityInset)
	}
	// Neither the old unified preview rail nor the output preview survive.
	for _, banned := range []string{"│ -old", "│ +new", "│ ok"} {
		if strings.Contains(plain, banned) {
			t.Fatalf("View() without ANSI = %q, must not contain %q: the card replaces the unified and output previews", plain, banned)
		}
	}
}

func TestModel_EditFourModesPreviewSettlementResizeAndNoDuplicates(t *testing.T) {
	tests := []struct{ name, input, operation string }{
		{"hashline", `{"input":"[a.go#ABCD]\\nPUT 1.=1:\\n+new"}`, "update"},
		{"apply_patch", `{"input":"*** Begin Patch\\n*** Update File: a.go\\n@@\\n-old\\n+new\\n*** End Patch"}`, "update"},
		{"patch", `{"path":"a.go","edits":[{"op":"update","diff":"@@\\n-old\\n+new"}]}`, "update"},
		{"replace", `{"path":"a.go","old_string":"old","new_string":"new"}`, "edit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil)
			m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
			m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "edit", Input: json.RawMessage(tc.input)})
			previewFiles := []tool.FileResult{{Path: "a.go", Operation: contract.FileOperation(tc.operation), OldText: "old\n", NewText: "preview\n", Diff: "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+preview"}}
			m = apply(t, m, PreviewMsg(tool.PreviewEvent{SessionID: "s1", CallID: "c1", Preview: tool.Preview{Digest: "p1", Pending: true, Files: previewFiles}}))
			finalFiles := []tool.FileResult{{Path: "a.go", Operation: contract.FileOperation(tc.operation), OldText: "old\n", NewText: "new\n", Diff: "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new", Warnings: []string{"settled warning"}, Committed: true}}
			encodedFiles, _ := json.Marshal(finalFiles)
			m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "c1", ToolName: "edit", Text: "Updated a.go", Diff: finalFiles[0].Diff, Attrs: map[string]string{"tool.files": string(encodedFiles)}})
			// A stale asynchronous projection must not revive or overwrite settlement.
			m = apply(t, m, PreviewMsg(tool.PreviewEvent{SessionID: "s1", CallID: "c1", Preview: tool.Preview{Digest: "late", Pending: true, Files: previewFiles}}))
			m = apply(t, m, tea.WindowSizeMsg{Width: 62, Height: 20})
			plain := ansi.Strip(m.View())
			for _, want := range []string{"a.go", "1 - old", "1 + new"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("view lacks %q: %s", want, plain)
				}
			}
			if strings.Contains(plain, "preview") || strings.Count(plain, "1 + new") != 1 {
				t.Fatalf("stale/duplicate preview survived final settlement: %s", plain)
			}
		})
	}
}
func TestModel_EditPreviewDoesNotReflowBeforeToolCall(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	diffs := []string{
		"--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new",
		"--- a/a.go\n+++ b/a.go\n@@ -1,4 +1,4 @@\n-old1\n-old2\n-old3\n-old4\n+new1\n+new2\n+new3\n+new4",
	}
	var rows []int
	for i, diff := range diffs {
		m = apply(t, m, PreviewMsg(tool.PreviewEvent{
			SessionID: "s1",
			CallID:    "c1",
			Preview: tool.Preview{
				Digest:  "preview-" + strconv.Itoa(i),
				Pending: true,
				Files: []tool.FileResult{{
					Path:      "a.go",
					Operation: contract.FileUpdated,
					Diff:      diff,
				}},
			},
		}))
		rows = append(rows, strings.Count(m.renderTranscript(), "\n")+1)
	}
	if rows[0] != rows[1] {
		t.Fatalf("partial edit previews changed transcript height from %d to %d rows; the live tool must not reflow on every input fragment", rows[0], rows[1])
	}
	if transcript := m.renderTranscript(); transcript != "" {
		t.Fatalf("partial edit previews rendered before Tool.Called: %q", transcript)
	}

	m = apply(t, m, EventMsg{
		Kind:     session.KindToolCalled,
		CallID:   "c1",
		ToolName: "edit",
		Input:    json.RawMessage(`{"input":"[a.go#ABCD]\nPUT 1.=1:\n+new"}`),
	})
	stable := m.renderTranscript()
	if view := ansi.Strip(m.View()); !strings.Contains(view, "a.go") || !strings.Contains(view, "1 + new") {
		t.Fatalf("View() = %q, the final preview must render once Tool.Called supplies the stable tool identity", view)
	}
	m = apply(t, m, PreviewMsg(tool.PreviewEvent{
		SessionID: "s1",
		CallID:    "c1",
		Preview: tool.Preview{
			Digest:  "late",
			Pending: true,
			Files: []tool.FileResult{{
				Path:      "a.go",
				Operation: contract.FileUpdated,
				Diff:      "--- a/a.go\n+++ b/a.go\n@@ -1,20 +1,20 @@\n-old\n+late",
			}},
		},
	}))
	if got := m.renderTranscript(); got != stable {
		t.Fatalf("later edit preview reflowed the stable running card:\n%s\n!=\n%s", got, stable)
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
			t.Fatalf("View() without ANSI = %q, must contain %q with the real line number", plain, want)
		}
	}
	// Each line of context appears in the red block AND in the green block.
	for _, ctx := range []string{"ctxA", "ctxB"} {
		if got := strings.Count(plain, ctx); got < 2 {
			t.Fatalf("View() without ANSI = %q, context line %q must appear in both blocks (before/after), got %d", plain, ctx, got)
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
	if strings.Contains(view, "│ l1") {
		t.Fatalf("View() = %q, Bash output must be collapsed by default", view)
	}
	lines := m.entryLines()
	bashRow := -1
	for i, line := range lines {
		if strings.Contains(line.line, "cat f") {
			bashRow = i
			break
		}
	}
	if bashRow < 0 {
		t.Fatalf("entryLines() = %v, want Bash command header", lines)
	}
	clickY := topBarHeight + bashRow - m.viewport.YOffset
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: clickY})
	view = m.View()
	for _, want := range []string{"│ l1", "│ l2", "│ l3", "│ l4"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, must contain %q: the first 4 output lines appear below the header", view, want)
		}
	}
	if !strings.Contains(view, "+2 lines") {
		t.Fatalf("View() = %q, must contain marker %q: the 2 lines beyond the limit are summarized", view, "+2 lines")
	}
	for _, banned := range []string{"│ l5", "│ l6"} {
		if strings.Contains(view, banned) {
			t.Fatalf("View() = %q, must not contain %q: the preview is cut at the 4-line limit", view, banned)
		}
	}
}

// The generic summary of the Input is the fallback for a tool that does NOT say how to present itself (tool.Presenter): those of an MCP server, and any that this build does not know. That is why the subject is tested with a tool outside the registry.  TRIANGULATE: returns a summary of the Input that returns the entire input without truncating, or that with several fields chooses a single field instead of the complete compact JSON.
func TestModel_ToolInputSummaryCompactsMultiField(t *testing.T) {
	const remote = "mcp_editor_patch"
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 200, Height: 24})

	// Two fields: The summary is the compact JSON, not the value of a single field.
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: remote, Input: json.RawMessage(`{"path":"a.go","text":"x"}`)})
	view := m.View()
	if want := `● ` + remote + ` {"path":"a.go","text":"x"}`; !strings.Contains(view, want) {
		t.Fatalf("View() = %q, the header must contain %q: with several fields the summary is compact JSON", view, want)
	}

	// A single string field longer than the maximum of 48 cells: the summary is truncated with the ellipsis and the tail of the input does not appear.
	long := strings.Repeat("x", 60) + "-final-tail"
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "bash", Input: json.RawMessage(`{"command":"` + long + `"}`)})
	view = m.View()
	if !strings.Contains(view, "…") {
		t.Fatalf("View() = %q, must contain the ellipsis %q: an input longer than the limit is truncated in the header", view, "…")
	}
	if strings.Contains(view, "final-tail") {
		t.Fatalf("View() = %q, must not contain %q: the tail of a long input is outside the truncated summary", view, "final-tail")
	}
}

func TestModel_ShowsPendingPermissionAndClearsOnOutcome(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	// Runner's actual order: Tool.Called and then Tool.Permission.Requested while blocking in the gate (ask-before-run).
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"rm -rf /tmp/x"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"rm -rf /tmp/x"}`)})

	view := m.View()
	permLine := lineWith(t, view, "! Bash")
	for _, want := range []string{"! Bash", "rm -rf /tmp/x"} {
		if !strings.Contains(permLine, want) {
			t.Fatalf("pending request = %q, must contain %q (attention marker, ToolName, and Input summary)", permLine, want)
		}
	}
	if view := ansi.Strip(view); !strings.Contains(view, "Permission required") || !strings.Contains(view, "Deny") || !strings.Contains(view, "Allow") {
		t.Fatalf("View() = %q, the inline panel must contain a title and actions", view)
	}
	if callID, ok := m.PendingPermission(); !ok || callID != "c1" {
		t.Fatalf("PendingPermission() = (%q, %v), must expose pending request c1", callID, ok)
	}

	// The outcome arrives as Tool.Success of the SAME CallID: the request disappears.
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "hecho",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "hecho", ToolCallID: "c1"},
	})
	if got := m.View(); strings.Contains(got, "Permission required") {
		t.Fatalf("View() = %q, Tool.Success for c1 must remove the pending request", got)
	}
	if callID, ok := m.PendingPermission(); ok {
		t.Fatalf("PendingPermission() = (%q, %v), no request should remain after the outcome", callID, ok)
	}

	// Tool.Failed also resolves the request (e.g. denied by the user).
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"b.go"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"b.go"}`)})
	if callID, ok := m.PendingPermission(); !ok || callID != "c2" {
		t.Fatalf("PendingPermission() = (%q, %v), must expose pending request c2", callID, ok)
	}
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c2", ToolName: "write", Error: "denied by the user"})
	if got := m.View(); strings.Contains(got, "(allow/deny)") {
		t.Fatalf("View() = %q, Tool.Failed for c2 must remove the pending request", got)
	}
	if callID, ok := m.PendingPermission(); ok {
		t.Fatalf("PendingPermission() = (%q, %v), no request should remain after Tool.Failed", callID, ok)
	}
}

// A tool that blocks on the ask-before-run gate emits Tool.Called (running "●")
// immediately followed by Tool.Permission.Requested ("!") for the same call.
// While the gate is open the transcript must show only the orange "! <tool>"
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
	if !strings.Contains(transcript, "! Bash") {
		t.Fatalf("renderTranscript() = %q, the pending permission ask must stay visible", transcript)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	transcript = ansi.Strip(m.renderTranscript())
	if !strings.Contains(transcript, "● Bash") || strings.Contains(transcript, "! Bash") {
		t.Fatalf("renderTranscript() = %q, approving must reveal the running header and drop the ask", transcript)
	}
	if len(fake.resolved) != 1 || fake.resolved[0].callID != "c1" || !fake.resolved[0].approved() {
		t.Fatalf("resolved = %+v, want c1 approved", fake.resolved)
	}
}

// A pending permission blocks on the user, so the agent status line must
// disappear while the ask is open and return once it is resolved (the run keeps
// going).
func TestModel_WorkingLineHiddenWhilePermissionPending(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithWorkspace("main", "~/dev/atenea")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.working = true
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	if got := m.View(); strings.Contains(got, "Checking context") {
		t.Fatalf("View() = %q, the working line must be hidden while a permission is pending", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if got := m.View(); !strings.Contains(got, "Checking context") {
		t.Fatalf("View() = %q, resolving restores the working line while the run continues", got)
	}
}

func TestModel_ShowsStepFailedError(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindStepFailed, Error: "context exhausted: token limit"})

	view := m.View()
	errLine := lineWith(t, view, "context exhausted: token limit")
	if !strings.Contains(errLine, "× error") {
		t.Fatalf("failure line = %q, must carry marker %q to distinguish it from normal text", errLine, "× error")
	}
}

// Contract of the visual hierarchy of activity: the header of each tool carries a status marker with two columns of margin (`●` running, `✓` success, `×` failure), the name of the tool aligned to 8 columns (`%-8s`) and the summary of the Input (` ● Bash ls`); The detail goes below as rail lines with the same margin (` │ 18 matches`, ` │ error: exit 1`). The old `[tool] ...` format disappears from the transcript.
func TestModel_RendersActivityMarkersThroughToolLifecycle(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	plain := ansi.Strip(m.View())
	if want := "  ● Bash     ls"; !strings.Contains(plain, want) {
		t.Fatalf("View() without ANSI = %q, the running tool must render as %q: two margin columns, marker ●, name aligned to 8 columns, and Input summary", plain, want)
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "18 matches",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "18 matches", ToolCallID: "c1"},
	})
	m.entries[len(m.entries)-1].expanded = true
	m = m.syncViewport()
	plain = ansi.Strip(m.View())
	if want := "  ✓ Bash     ls"; !strings.Contains(plain, want) {
		t.Fatalf("View() without ANSI = %q, the successful tool must settle as %q: marker ✓ replaces ● in the same column", plain, want)
	}
	railLine := lineWith(t, plain, "18 matches")
	if want := "  │ 18 matches"; !strings.HasPrefix(railLine, want) {
		t.Fatalf("output line = %q, must carry the rail with the same margin as %q", railLine, want)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "bash", Input: json.RawMessage(`{"command":"false"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c2", ToolName: "bash", Error: "exit 1"})
	m.entries[len(m.entries)-1].expanded = true
	m = m.syncViewport()
	plain = ansi.Strip(m.View())
	if want := "  × Bash     false"; !strings.Contains(plain, want) {
		t.Fatalf("View() without ANSI = %q, the failed tool must settle as %q: marker × with the same name column", plain, want)
	}
	failLine := lineWith(t, plain, "error: exit 1")
	if want := "  │ error: exit 1"; !strings.HasPrefix(failLine, want) {
		t.Fatalf("failure line = %q, the tool error goes below the header as rail line %q, not attached to the header", failLine, want)
	}
	if strings.Contains(plain, "[tool]") {
		t.Fatalf("View() without ANSI = %q, must not contain old format %q: status markers replace it", plain, "[tool]")
	}
}

func TestModel_SearchToolsSummarizePatternAndResultCount(t *testing.T) {
	tests := []struct {
		name, tool, output, want string
	}{
		{name: "grep results", tool: "grep", output: "Found 3 matches\nresult", want: "  ✓ Grep     */*.md (3)"},
		{name: "grep no results", tool: "grep", output: "No files found", want: "  ✓ Grep     */*.md (no results)"},
		{name: "grep truncated", tool: "grep", output: "Found 100 matches (more matches available)\nresult", want: "  ✓ Grep     */*.md (100+)"},
		{name: "glob results", tool: "glob", output: "a.md\nb.md", want: "  ✓ Glob     */*.md (2)"},
		{name: "glob no results", tool: "glob", output: "No files found", want: "  ✓ Glob     */*.md (no results)"},
		{name: "glob over 100 results", tool: "glob", output: strings.Repeat("file.md\n", 101), want: "  ✓ Glob     */*.md (100+)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil)
			m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: tt.tool, Input: json.RawMessage(`{"pattern":"*/*.md"}`)})
			m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "c1", ToolName: tt.tool, Text: tt.output})
			plain := ansi.Strip(m.View())
			if !strings.Contains(plain, tt.want) {
				t.Fatalf("View() = %q, want %q", plain, tt.want)
			}
		})
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
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "analysis complete"})
	m = drainReveal(t, m)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c3", ToolName: "bash", Input: json.RawMessage(`{"command":"pwd"}`)})

	plain := ansi.Strip(m.View())
	if want := "  ✓ Bash     ls\n  ● Grep     foo"; !strings.Contains(plain, want) {
		t.Fatalf("View() without ANSI = %q, must contain %q: adjacent activity entries group into physically contiguous lines without a blank line", plain, want)
	}

	lines := strings.Split(plain, "\n")
	narrIdx := lineIndexWith(t, plain, "analysis complete")
	if narrIdx == 0 || strings.TrimSpace(lines[narrIdx-1]) != "" {
		t.Fatalf("line before narrative = %q, assistant narrative breaks the activity group with a blank line", lines[narrIdx-1])
	}
	toolIdx := lineIndexWith(t, plain, "pwd")
	if toolIdx == 0 || strings.TrimSpace(lines[toolIdx-1]) != "" {
		t.Fatalf("line before tool after narrative = %q, activity after narrative starts a new group separated by a blank line", lines[toolIdx-1])
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
		Diff:    "--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,3 @@\n-old\n+new\n+extra",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	hunk := lineWith(t, plain, "@@ -1,2 +1,3 @@")
	for _, want := range []string{"@@ -1,2 +1,3 @@", "+2 -1"} {
		if !strings.Contains(hunk, want) {
			t.Fatalf("hunk line = %q, must contain %q: the +N -M stat is in the hunk bar", hunk, want)
		}
	}
	if strings.Contains(plain, "✓ Edit") {
		t.Fatalf("View() without ANSI = %q, must not contain %q: the card replaces the activity line", plain, "✓ Edit")
	}
	// The changed lines go as numbered rows in the blocks, without the old rail.
	for _, want := range []string{"1 - old", "1 + new", "2 + extra"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("View() without ANSI = %q, must contain row %q", plain, want)
		}
	}
	if strings.Contains(plain, "│ +new") {
		t.Fatalf("View() without ANSI = %q, must not contain old rail %q", plain, "│ +new")
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
			t.Fatalf("View() without ANSI = %q, must contain added row %q", plain, want)
		}
	}
	if strings.Contains(plain, " - ") {
		t.Fatalf("View() without ANSI = %q, a pure insertion must not emit a removed row", plain)
	}
}

// A successful write renders as a diff card sibling to the edit card, but in a
// single neutral gray: the file-path bar and every written line on the gray
// band, numbered, with NO hunk bar, NO "+N -M" stat, and NO +/- marker. A write
// always creates a brand-new file, so there is never a removed side to show.
func TestModel_ToolSuccessShowsWriteCard(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "write", Input: json.RawMessage(`{"path":"new.go","content":"package main"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "write", Text: "ok",
		Diff:    "--- a/new.go\n+++ b/new.go\n@@ -0,0 +1,2 @@\n+package main\n+// hello",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	// The route and each line are numbered but WITHOUT a + marker.
	for _, want := range []string{"new.go", "1  package main", "2  // hello"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("View() without ANSI = %q, must contain %q: successful write renders as a gray card with path and numbered lines without a marker", plain, want)
		}
	}
	// The card replaces the line of activity.
	if strings.Contains(plain, "✓ Write") {
		t.Fatalf("View() without ANSI = %q, must not contain %q: the card replaces the activity line", plain, "✓ Write")
	}
	// No hunk bar, no stat +N -M and no +/- marker on rows.
	for _, banned := range []string{"@@", "+2 -0", "1 + package main", " - "} {
		if strings.Contains(plain, banned) {
			t.Fatalf("View() without ANSI = %q, must not contain %q: write shows no hunk, stat, or diff marker", plain, banned)
		}
	}
	// The row opens with the margin and the rail ▌ in the same column as the rest.
	if row := lineWith(t, plain, "1  package main"); !strings.HasPrefix(row, activityInset+"▌") {
		t.Fatalf("write row = %q, must begin with margin %q and rail ▌", row, activityInset)
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
		fmt.Fprintf(&b, "+line-%02d\n", i)
	}
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "write", Input: json.RawMessage(`{"path":"big.go"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "write", Text: "ok",
		Diff:    b.String(),
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	// path (1) + 39 rows = 40 (the top); Line-39 is the last one that enters and 60-39 = 21 lines are hidden in the summary mark.
	if !strings.Contains(plain, "line-39") {
		t.Fatalf("View() without ANSI = %q, the last line within the limit must be shown", plain)
	}
	if !strings.Contains(plain, "… +21 lines") {
		t.Fatalf("View() without ANSI = %q, must summarize the excess as %q", plain, "… +21 lines")
	}
	// The first line beyond the top (and the following ones) do not appear.
	for _, banned := range []string{"line-40", "line-60"} {
		if strings.Contains(plain, banned) {
			t.Fatalf("View() without ANSI = %q, must not contain %q: it is cut at the limit", plain, banned)
		}
	}
}

// An empty-file write yields an empty diff, so it keeps the generic activity
// line instead of an empty card: there is nothing to show on the band.
func TestModel_WriteWithoutDiffShowsActivityLine(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "write", Input: json.RawMessage(`{"path":"empty.go","content":""}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "write", Text: "ok",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "✓ Write") {
		t.Fatalf("View() without ANSI = %q, an empty write without a diff preserves the activity line", plain)
	}
}

// TRIANGULATE: destroy a header that truncates the name of the tool to the width of the alignment column (8) or that is too long: with a name longer than the column, the name remains intact and the summary is ONE space in the name.
func TestModel_ActivityHeaderKeepsLongToolNameReadable(t *testing.T) {
	// A name longer than the column only appears in a tool that does not say how to present itself: the tools themselves have a short label (Bash, Edit, SubAgent).
	const remote = "mcp_planner_present_plan"
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: remote, Input: json.RawMessage(`{"plan":"migrate the runner"}`)})

	plain := ansi.Strip(m.View())
	line := lineWith(t, plain, remote)
	if want := "  ● " + remote + " migrate the runner"; line != want {
		t.Fatalf("header = %q, want exactly %q: a name longer than the 8-column width is not truncated and the summary stays ONE space from the name", line, want)
	}
}

// TRIANGULATE: knock down a header that leaves the tail of the alignment invisible when there is no summary (without Input or with Input `{}`): the line is exactly the marker and the name, without dangling spaces.
func TestModel_ActivityHeaderWithoutSummaryHasNoTrailingSpaces(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	// Without Input and with Input `{}`: in both cases the summary is empty and the header must trim the spaces from the name alignment.
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash"})
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "grep", Input: json.RawMessage(`{}`)})

	plain := ansi.Strip(m.View())
	if line := lineWith(t, plain, "● Bash"); line != "  ● Bash" {
		t.Fatalf("header without Input = %q, want exactly %q: no trailing alignment spaces remain without a summary", line, "  ● Bash")
	}
	if line := lineWith(t, plain, "● Grep"); line != "  ● Grep" {
		t.Fatalf("header with Input {} = %q, want exactly %q: the empty object produces no summary or trailing alignment spaces", line, "  ● Grep")
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
			name:    "headers, hunk, and content",
			diff:    "--- a/x\n+++ b/x\n@@ -1,2 +1,3 @@\n context\n-old\n+new\n+extra",
			added:   2,
			removed: 1,
		},
		{
			name:    "headers and context only",
			diff:    "--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n context",
			added:   0,
			removed: 0,
		},
		{name: "empty diff", diff: "", added: 0, removed: 0},
		{name: "bare + and - lines count", diff: "+\n-", added: 1, removed: 1},
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
	m.entries[len(m.entries)-1].expanded = true
	m = m.syncViewport()

	plain := ansi.Strip(m.View())
	header := lineWith(t, plain, "✓ Bash")
	if want := "  ✓ Bash     ls"; header != want {
		t.Fatalf("header = %q, want exactly %q: success without diff adds nothing after the summary", header, want)
	}
	for _, banned := range []string{"+0 -0", " +"} {
		if strings.Contains(header, banned) {
			t.Fatalf("header = %q, MUST NOT contain %q: the +N -M stat applies only when there is a diff", header, banned)
		}
	}
	for _, needle := range []string{"main.go", "view.go"} {
		if line := lineWith(t, plain, needle); !strings.HasPrefix(line, "  │ ") {
			t.Fatalf("output line = %q, successful tool output uses the rail %q after the margin", line, "  │ ")
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
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "I continue with the rest"})
	m = drainReveal(t, m)

	plain := ansi.Strip(m.View())
	// The running "● Write" header is hidden while its permission is still pending: only the orange "! Write" line of the permission represents the gated call, without duplicating it in two contiguous rows.
	want := "  ✓ Bash     ls\n  ! Write    b.go\n  × error    boom"
	if !strings.Contains(plain, want) {
		t.Fatalf("View() without ANSI = %q, must contain %q: the successful tool, pending permission, and step error remain physically contiguous, without blank lines", plain, want)
	}
	if strings.Contains(plain, "● Write") {
		t.Fatalf("View() without ANSI = %q, the running header must not duplicate the call while its permission remains pending", plain)
	}

	lines := strings.Split(plain, "\n")
	narrIdx := lineIndexWith(t, plain, "I continue with the rest")
	if narrIdx == 0 || strings.TrimSpace(lines[narrIdx-1]) != "" {
		t.Fatalf("line before narrative = %q, the assistant narrative after the activity group is separated by a blank line", lines[narrIdx-1])
	}
}

func TestModel_ToolInputDeltasAreNotTranscript(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// Reasoning: the thought block is shown while it flows, but when closed it collapses to the summary "● Thought for...": once the reveal is drained, its text is NOT left as plain text of the transcript.
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pienso en secreto"})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pienso en secreto"})

	// Tool input: raw fragments travel in Text and the complete JSON in Input; none of them are conversational texts, EVER.
	m = apply(t, m, EventMsg{Kind: session.KindToolInputStarted, CallID: "c1"})
	m = apply(t, m, EventMsg{Kind: session.KindToolInputDelta, CallID: "c1", Text: `{"cmd":"ls`})
	m = apply(t, m, EventMsg{Kind: session.KindToolInputEnded, CallID: "c1", Input: json.RawMessage(`{"cmd":"ls"}`)})

	// The normal text of the assistant is transcribed: contrasts with the above.
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "visible response"})
	m = drainReveal(t, m)

	view := m.View()
	for _, leak := range []string{"pienso en secreto", `{"cmd":"ls`} {
		if strings.Contains(view, leak) {
			t.Fatalf("View() = %q, must not filter %q as conversation text", view, leak)
		}
	}
	if !strings.Contains(view, "visible response") {
		t.Fatalf("View() = %q, assistant text must be visible", view)
	}
}

// Parity with the desktop ThinkingBlock: reasoning is displayed as a collapsible block of the transcript. While flowing, the view carries the header "● Thinking..." and below ONLY the last 4 non-empty lines of the revealed text (sliding window); Reasoning.Ended, with the backlog already drained, collapses the block to a single summary line prefixed with "● Thought" (readable duration), and the header and preview disappear.
func TestModel_ShowsReasoningAsCollapsibleThinkingBlock(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	text := "reason-1\nreason-2\nreason-3\nreason-4\nreason-5\nreason-6"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "● Thinking…") {
		t.Fatalf("View() = %q, active reasoning must show the header %q", view, "● Thinking…")
	}
	for _, want := range []string{"reason-3", "reason-4", "reason-5", "reason-6"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, preview must show %q (the last 4 non-empty lines of revealed text)", view, want)
		}
	}
	for _, gone := range []string{"reason-1", "reason-2"} {
		if strings.Contains(view, gone) {
			t.Fatalf("View() = %q, %q has left the sliding window: only the last 4 non-empty lines are shown", view, gone)
		}
	}

	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)

	view = m.View()
	if !strings.Contains(view, "● Thought") {
		t.Fatalf("View() = %q, completed reasoning must collapse to a summary line with prefix %q", view, "● Thought")
	}
	if strings.Contains(view, "● Thinking…") {
		t.Fatalf("View() = %q, header %q must disappear when the block collapses", view, "● Thinking…")
	}
	if strings.Contains(view, "reason-6") {
		t.Fatalf("View() = %q, preview lines must disappear when the block collapses", view)
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
		t.Fatalf("View() = %q, %q MUST NOT be visible without reveal ticks: thought delta is revealed progressively, not all at once", view, "token-final")
	}
	if strings.Contains(view, "inicio-marca") {
		t.Fatalf("View() = %q, %q MUST NOT be visible without reveal ticks: the prefix also waits for its tick", view, "inicio-marca")
	}

	m = apply(t, m, revealTickMsg{})
	view = m.View()
	if !strings.Contains(view, "inicio-marca") {
		t.Fatalf("View() = %q, after ONE tick the prefix %q must be visible", view, "inicio-marca")
	}
	if strings.Contains(view, "token-final") {
		t.Fatalf("View() = %q, after ONE tick the final %q must NOT yet be visible: one tick reveals one step, not the whole backlog", view, "token-final")
	}

	m = drainReveal(t, m)
	if view := m.View(); !strings.Contains(view, "token-final") {
		t.Fatalf("View() = %q, after the backlog drains the final %q must be visible in the preview window", view, "token-final")
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
	if !strings.Contains(view, "  ● Thinking…\n  r2\n  r3\n  r4\n  r5") {
		t.Fatalf("View() = %q, the preview must contain the last 4 NON-EMPTY lines with uniform inset (%q): no interspersed blanks or lost content lines", view, "  ● Thinking…\n  r2\n  r3\n  r4\n  r5")
	}
	if strings.Contains(view, "r1") {
		t.Fatalf("View() = %q, %q has left the 4-line window", view, "r1")
	}
}

func TestModel_ThinkingKeepsChatInsetWhileStreamingAndExpanded(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 44, Height: 18})

	text := "streaming-inset-a\nstreaming-inset-b"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)

	assertThinkingInset(t, m.View(), "● Thinking…", "streaming-inset-a", "streaming-inset-b")

	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})

	assertThinkingInset(t, m.View(), "● Thought", "streaming-inset-a", "streaming-inset-b")
}

func TestEntry_RenderThinkingInsetsEveryWrappedLine(t *testing.T) {
	e := entry{
		kind:     entryReasoning,
		text:     strings.Repeat("long-thought ", 8),
		revealed: len(strings.Repeat("long-thought ", 8)),
		expanded: true,
	}

	lines := strings.Split(ansi.Strip(e.renderThinking(24)), "\n")
	if len(lines) < 3 {
		t.Fatalf("renderThinking() produced %d lines, want a header and multiple wrapped lines: %q", len(lines), lines)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, thinkingInset) {
			t.Fatalf("wrapped line = %q, want inset %q", line, thinkingInset)
		}
		if got, want := ansi.StringWidth(line), 24; got > want {
			t.Fatalf("wrapped line width = %d, want <= %d: %q", got, want, line)
		}
	}
}

func assertThinkingInset(t *testing.T, view string, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		line := strings.TrimRight(lineWith(t, ansi.Strip(view), needle), " ")
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("thought line %q = %q, want two-cell inset", needle, line)
		}
	}
}

func TestModel_TextStartedClosesLiveThinking(t *testing.T) {
	// TRIANGULATE: if the runner never issues Reasoning.Ended, a naive fold leaves the "● Thinking..." header alive forever while the response streams below it. Starting the text implies that the thought is finished: Text.Started closes it defensively.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "I weigh options"})
	m = drainReveal(t, m)

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "response"})
	m = drainReveal(t, m)

	view := m.View()
	if strings.Contains(view, "● Thinking…") {
		t.Fatalf("View() = %q, Text.Started must close active thought: header %q cannot survive response start", view, "● Thinking…")
	}
	if !strings.Contains(view, "● Thought") {
		t.Fatalf("View() = %q, defensively closed thought must collapse to summary %q", view, "● Thought")
	}
	if !strings.Contains(view, "response") {
		t.Fatalf("View() = %q, response %q must appear after collapsed thought", view, "response")
	}
}

func TestModel_StepEndedClosesLiveThinking(t *testing.T) {
	// TRIANGULATE: a step can die thinking (cancellation, provider error) without Reasoning.Ended or Text.Started involved. Step.Ended closes the thought defensively just like Text.Started; Without that closure the header "● Thinking..." would remain alive forever.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "I think and the step dies"})
	m = drainReveal(t, m)

	m = apply(t, m, EventMsg{Kind: session.KindStepEnded})

	view := m.View()
	if strings.Contains(view, "● Thinking…") {
		t.Fatalf("View() = %q, Step.Ended must close active thought: header %q cannot survive step end", view, "● Thinking…")
	}
	if !strings.Contains(view, "● Thought") {
		t.Fatalf("View() = %q, thought closed by step end must collapse to summary %q", view, "● Thought")
	}
}

func TestModel_ReasoningEndedTextCollapsesWithoutAnimation(t *testing.T) {
	// TRIANGULATE: when Reasoning.Ended brings the complete text without previous deltas (provider that does not stream the thought), the fill is NOT animated: it is revealed complete and collapsed in the same fold, without ticks in between. A fold that only assigns the text without marking it revealed would leave the block "writing" after it is closed.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "final-filler-without-stream"})

	view := m.View()
	if !strings.Contains(view, "● Thought") {
		t.Fatalf("View() = %q, Ended text with no prior deltas must immediately collapse to summary %q, without ticks", view, "● Thought")
	}
	if strings.Contains(view, "● Thinking…") {
		t.Fatalf("View() = %q, header %q must not appear after Ended: filler text is not animated", view, "● Thinking…")
	}
	if strings.Contains(view, "final-filler-without-stream") {
		t.Fatalf("View() = %q, Ended filler text must never appear flat, even before draining", view)
	}

	m = drainReveal(t, m)
	if view := m.View(); strings.Contains(view, "final-filler-without-stream") {
		t.Fatalf("View() = %q, filler text must not appear after draining either: no backlog remains to animate", view)
	}
}

func TestModel_TwoThinkingBlocksInSameRunStaySeparate(t *testing.T) {
	// TRIANGULATE: a fold that reuses the previous thought block instead of opening a new one would mix the lines of the first in the preview of the second and collapse both into ONE summary line. Each Reasoning.Started opens its own block.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "first-a\nfirst-b"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "first-a\nfirst-b"})
	m = drainReveal(t, m)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "second-a\nsecond-b"})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "second-a") || !strings.Contains(view, "second-b") {
		t.Fatalf("View() = %q, the second thought preview must show its lines", view)
	}
	if strings.Contains(view, "first-a") || strings.Contains(view, "first-b") {
		t.Fatalf("View() = %q, the second thought preview MUST NOT mix lines from the first (already collapsed)", view)
	}

	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "second-a\nsecond-b"})
	m = drainReveal(t, m)

	view = m.View()
	if count := strings.Count(view, "● Thought"); count < 2 {
		t.Fatalf("View() = %q, two thoughts in one run must collapse to TWO summaries %q (count=%d)", view, "● Thought", count)
	}
}

func TestModel_ThinkingCollapseWaitsForRevealDrain(t *testing.T) {
	// TRIANGULATE: An instant crash upon receiving Ended with pending backlog would cut the animation mid-sentence. Parity with the desktop gift: the block continues "writing" until the reveal is drained and only then collapses to the summary.
	m := NewModel(nil, "s1", nil)

	text := "flowing-start " + strings.Repeat("c", 150) + "\n" + strings.Repeat("d", 150) + " late-final"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	// A tick so that there is a visible prefix to assert before the Ended.
	m = apply(t, m, revealTickMsg{})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})

	view := m.View()
	if !strings.Contains(view, "● Thinking…") {
		t.Fatalf("View() = %q, Ended does NOT collapse while backlog remains: header %q must continue during draining", view, "● Thinking…")
	}
	if !strings.Contains(view, "flowing-start") {
		t.Fatalf("View() = %q, revealed prefix %q must remain visible while the thought finishes writing", view, "flowing-start")
	}
	if strings.Contains(view, "● Thought") {
		t.Fatalf("View() = %q, summary %q must not appear until reveal backlog drains", view, "● Thought")
	}

	m = drainReveal(t, m)
	view = m.View()
	if !strings.Contains(view, "● Thought") {
		t.Fatalf("View() = %q, after backlog drains the closed thought must collapse to summary %q", view, "● Thought")
	}
	if strings.Contains(view, "● Thinking…") {
		t.Fatalf("View() = %q, header %q must disappear when collapsing", view, "● Thinking…")
	}
}

func TestModel_SecondTurnOpensNewBlock(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// First complete turn: streaming, closing the block and closing the step.
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "First response"})
	m = apply(t, m, EventMsg{Kind: session.KindTextEnded, Text: "First response"})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "First response"},
	})

	// Second turn: the new streaming opens a NEW block.
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "Second response"})
	m = drainReveal(t, m)

	view := m.View()
	if strings.Contains(view, "First responseSecond response") {
		t.Fatalf("View() = %q, the second turn MUST NOT concatenate with the previous block", view)
	}
	first := strings.Index(view, "First response")
	second := strings.Index(view, "Second response")
	if first < 0 || second < 0 || first >= second {
		t.Fatalf("View() without ANSI = %q, both texts must appear as separate, ordered blocks", view)
	}
	if count := strings.Count(view, "First response"); count != 1 {
		t.Fatalf("View() = %q, %q must appear exactly once (count=%d)", view, "First response", count)
	}
}

func TestModel_EnterWithEmptyInputDoesNotSend(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt was called %d times, Enter with empty input must send nothing", len(fake.sent))
	}
	if m.Working() {
		t.Fatalf("Working() = true, Enter with empty input must not mark the model working")
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
		t.Fatalf("ResolvePermission was called %d times, 'y' must resolve exactly once", len(fake.resolved))
	}
	if got := fake.resolved[0]; got.sessionID != "s1" || got.callID != "c1" || !got.approved() {
		t.Fatalf("ResolvePermission(%q, %q, %v), expected ResolvePermission(%q, %q, true)", got.sessionID, got.callID, got.approved(), "s1", "c1")
	}
	if got := m.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, rune 'y' MUST NOT enter input while permission is pending", got)
	}
	if callID, ok := m.PendingPermission(); ok {
		t.Fatalf("PendingPermission() = (%q, %v), resolving must immediately hide the panel and prevent duplicate decisions", callID, ok)
	}
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "ok",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})
	if callID, ok := m.PendingPermission(); ok {
		t.Fatalf("PendingPermission() = (%q, %v), Tool.Success must remove the request", callID, ok)
	}

	// Scenario 2: 'n' denies pending request c2; Additionally, the runes do not enter the input and Enter does not send a prompt while permission is pending.
	fake2 := &fakeAgent{}
	m2 := NewModel(fake2, "s1", nil)
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"a.go"}`)})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"a.go"}`)})

	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	if got := m2.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, runes MUST NOT enter input while permission is pending", got)
	}
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake2.sent) != 0 {
		t.Fatalf("SendPrompt was called %d times, Enter MUST NOT send a prompt while permission is pending", len(fake2.sent))
	}
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if len(fake2.resolved) != 1 {
		t.Fatalf("ResolvePermission was called %d times, 'n' must resolve exactly once", len(fake2.resolved))
	}
	if got := fake2.resolved[0]; got.sessionID != "s1" || got.callID != "c2" || got.approved() {
		t.Fatalf("ResolvePermission(%q, %q, %v), expected ResolvePermission(%q, %q, false)", got.sessionID, got.callID, got.approved(), "s1", "c2")
	}
	if got := ansi.Strip(m2.View()); !strings.Contains(got, "Denied by user") || strings.Contains(got, "Permission required") {
		t.Fatalf("View() = %q, deny must close the panel and leave a neutral transcript state", got)
	}
}

func TestModel_PermissionPanelRendersInlineAboveComposer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithWorkspace("main", "~/dev/atenea")
	m.composer.input.SetValue("draft stays here")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash",
		Input: json.RawMessage(`{"command":"printf 'one\\ntwo\\nthree\\nfour\\nfive'"}`),
	})

	view := ansi.Strip(m.View())
	for _, want := range []string{
		"Permission required", "Bash printf 'one\\ntwo\\nthree\\nfour\\nfive'",
		"Deny", "Allow once", "draft stays here",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, want %q", view, want)
		}
	}
	for _, unwanted := range []string{
		"Bash command", "Requested by", "Working directory",
		"❯",
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
	if m.composer.input.Focused() {
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

func TestModel_PermissionPanelsShareActionButtonGrammar(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	activeBackground := "48;2;177;184;107m"
	for _, tc := range []struct {
		name string
		msg  EventMsg
	}{
		{
			name: "compact",
			msg:  EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
		},
		{
			name: "detailed",
			msg:  EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "mcp_deploy", Input: json.RawMessage(`{"target":"prod"}`)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil)
			m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
			m = apply(t, m, tc.msg)

			panel := m.permissionPanelView()
			plain := ansi.Strip(panel)
			if !strings.Contains(plain, "Allow once") || strings.Contains(plain, "›") {
				t.Fatalf("permissionPanelView() = %q, actions must use the shared button labels without cursor markers", plain)
			}
			actionLine := lineWith(t, panel, "Deny")
			if backgroundIndex, denyIndex := strings.Index(actionLine, activeBackground), strings.Index(actionLine, "Deny"); backgroundIndex < 0 || backgroundIndex > denyIndex {
				t.Fatalf("action line = %q, Deny must render as the active button", actionLine)
			}
		})
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
	m.composer.input.SetValue("preserved draft")
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
	if got := m.composer.input.Value(); got != "preserved draft" {
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
	if !strings.Contains(plain, "! Bash") || !strings.Contains(plain, "Denied by user") || strings.Contains(plain, "error: tool denied") {
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
		t.Fatalf("ResolvePermission was called %d times, 'y' must resolve exactly once", len(fake.resolved))
	}
	if got := fake.resolved[0]; got.sessionID != "child-1" || got.callID != "c9" || !got.approved() {
		t.Fatalf("ResolvePermission(%q, %q, %v), expected ResolvePermission(%q, %q, true): the subagent permission uses the event SessionID", got.sessionID, got.callID, got.approved(), "child-1", "c9")
	}
}

func TestModel_CtrlCStopsAndQuits(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	if len(fake.stopped) != 1 || fake.stopped[0] != "s1" {
		t.Fatalf("Stop = %v, Ctrl+C must call Stop(%q) exactly once", fake.stopped, "s1")
	}
	if cmd == nil {
		t.Fatalf("cmd = nil, Ctrl+C must return a tea.Cmd producing tea.QuitMsg")
	}
	if msg := cmd(); msg == nil {
		t.Fatalf("cmd() = nil, expected tea.QuitMsg")
	} else if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, expected tea.QuitMsg", msg)
	}

	// With permission pending Ctrl+C still works the same.
	fake2 := &fakeAgent{}
	m2 := NewModel(fake2, "s1", nil)
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})

	_, cmd2 := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if len(fake2.stopped) != 1 || fake2.stopped[0] != "s1" {
		t.Fatalf("Stop = %v, Ctrl+C with pending permission must call Stop(%q)", fake2.stopped, "s1")
	}
	if cmd2 == nil {
		t.Fatalf("cmd = nil, Ctrl+C with pending permission must return a tea.Cmd producing tea.QuitMsg")
	}
	if msg := cmd2(); msg == nil {
		t.Fatalf("cmd() = nil, expected tea.QuitMsg with pending permission")
	} else if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, expected tea.QuitMsg with pending permission", msg)
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
		t.Fatalf("Stop = %v, the first Esc should only request confirmation", fake.stopped)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, the first Esc should schedule confirmation expiration")
	}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "Esc again to cancel") {
		t.Fatalf("View() = %q, confirmation is missing below the composer", plain)
	}
	line := ansi.Strip(lineWith(t, m.View(), "Esc again to cancel"))
	if !strings.HasPrefix(line, "  Esc again to cancel") || !strings.Contains(line, "1 file changed  +2  −1") {
		t.Fatalf("line below composer = %q, the notice should remain on the left and Git on the right", line)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if len(fake.stopped) != 1 || fake.stopped[0] != "s1" {
		t.Fatalf("Stop = %v, the second Esc must call Stop(%q) exactly once", fake.stopped, "s1")
	}
	if cmd != nil {
		if _, quits := cmd().(tea.QuitMsg); quits {
			t.Fatal("the second Esc should cancel the run without quitting the TUI")
		}
	}
	if plain := ansi.Strip(m.View()); strings.Contains(plain, "Esc again to cancel") {
		t.Fatalf("View() = %q, confirmation must disappear after cancellation", plain)
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
		t.Fatal("a key other than Esc must disarm the confirmation")
	}
	if got := m.composer.input.Value(); got != "x" {
		t.Fatalf("input = %q, the disarming key must be processed normally", got)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	generation := m.cancelGeneration
	m = apply(t, m, cancelConfirmationExpiredMsg{generation: generation})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if len(fake.stopped) != 0 || !m.cancelPending {
		t.Fatalf("Stop = %v pending = %v, after Esc expires it should start a new confirmation", fake.stopped, m.cancelPending)
	}

	idle := NewModel(fake, "s1", nil)
	idle = apply(t, idle, tea.KeyMsg{Type: tea.KeyEsc})
	if idle.cancelPending {
		t.Fatal("Esc without an active run must not show confirmation")
	}
}

func TestModel_RunDoneStopsWorkingAndShowsError(t *testing.T) {
	// Clean run: RunDoneMsg{Err: ""} just turns off Working.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.Working() {
		t.Fatalf("Working() = false, the model must remain working after sending the prompt")
	}

	m = apply(t, m, activeRunDone(m, ""))
	if m.Working() {
		t.Fatalf("Working() = true, RunDoneMsg must turn off the working state")
	}
	if got := m.View(); strings.Contains(got, "× error") {
		t.Fatalf("View() = %q, a clean run must not show an error", got)
	}

	// Failed run: RunDoneMsg{Err: "boom"} also shows the error.
	m2 := NewModel(fake, "s1", nil)
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyEnter})

	m2 = apply(t, m2, activeRunDone(m2, "boom"))
	if m2.Working() {
		t.Fatalf("Working() = true, RunDoneMsg with an error must also turn off the working state")
	}
	errLine := lineWith(t, m2.View(), "boom")
	if !strings.Contains(errLine, "× error") {
		t.Fatalf("failure line = %q, it must carry marker %q", errLine, "× error")
	}
}

func TestModel_StaleRunDoneDoesNotStopReplacementRun(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m.composer.input.SetValue("first")
	m, _ = m.submitPrompt()
	firstRunID := m.activeRun

	m.composer.input.SetValue("second")
	m, _ = m.submitPrompt()
	secondRunID := m.activeRun
	if firstRunID == secondRunID {
		t.Fatalf("run IDs = %d and %d, each run must have its own identity", firstRunID, secondRunID)
	}

	m = apply(t, m, RunDoneMsg{SessionID: "s1", RunID: firstRunID})
	if !m.Working() {
		t.Fatal("the late completion of the previous run turned off the new run's indicator")
	}
	if m.activeRun != secondRunID {
		t.Fatalf("activeRun = %d, expected the new run %d to remain active", m.activeRun, secondRunID)
	}

	m = apply(t, m, RunDoneMsg{SessionID: "s1", RunID: secondRunID})
	if m.Working() {
		t.Fatal("completion of the active run must turn off the indicator")
	}
}

func TestModel_EventPumpDeliversFromChannel(t *testing.T) {
	ch := make(chan tea.Msg, 2)
	first := EventMsg{Kind: session.KindTextStarted}
	second := EventMsg{Kind: session.KindTextDelta, Text: "hello"}
	ch <- first
	ch <- second

	m := NewModel(nil, "s1", ch)

	// Init sets off the bomb: the cmd does receive and delivers the first msg.
	cmd := m.Init()
	if cmd == nil {
		t.Fatalf("Init() = nil, with an event channel it must return the event-pump command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("Init command = %#v, want event pump and composer cursor commands", batch)
	}
	msg := batch[0]()
	if got, ok := msg.(EventMsg); !ok || got.Kind != first.Kind {
		t.Fatalf("cmd() = %#v, expected the first EventMsg %#v", msg, first)
	}

	// Consuming an event resets the bomb: the new cmd delivers the second msg.
	updated, cmd2 := m.Update(msg)
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	if cmd2 == nil {
		t.Fatalf("Update(EventMsg) returned a nil command, the event pump must restart after each event")
	}
	msg2 := cmd2()
	if got, ok := msg2.(EventMsg); !ok || got.Kind != second.Kind || got.Text != second.Text {
		t.Fatalf("cmd() = %#v, expected the second EventMsg %#v", msg2, second)
	}

	// RunDoneMsg also resets the bomb.
	_, cmd3 := m.Update(activeRunDone(m, ""))
	if cmd3 == nil {
		t.Fatalf("Update(RunDoneMsg) returned a nil command, the event pump must restart after run completion")
	}

	// Closed channel: cmd returns nil instead of hanging or delivering garbage.
	close(ch)
	if got := cmd3(); got != nil {
		t.Fatalf("cmd() = %#v with a closed channel, expected nil", got)
	}

	// Channel nil (fold tests): only the cursor command remains.
	if cmd := NewModel(nil, "s1", nil).Init(); cmd == nil {
		t.Fatal("Init() = nil with a nil channel, expected the cursor command")
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
			Text: fmt.Sprintf("message-%02d", i),
		}})
	}

	view := m.View()
	if !strings.Contains(view, "message-29") {
		t.Fatalf("View() = %q, the last entry %q must be visible: the view follows the tail", view, "message-29")
	}
	if strings.Contains(view, "message-00") {
		t.Fatalf("View() = %q, the first entry %q must NOT be visible: height is bounded by the viewport", view, "message-00")
	}
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("View() has %d lines, it must respect terminal height (<= 10)", lines)
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
			Text: fmt.Sprintf("message-%02d", i),
		}})
	}

	// PgUp goes back one page: the queue is no longer visible and previous history appears.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	view := m.View()
	if strings.Contains(view, "message-29") {
		t.Fatalf("View() = %q, after PgUp the tail %q must NOT remain visible", view, "message-29")
	}
	if !strings.Contains(view, "message-") {
		t.Fatalf("View() = %q, an earlier history message should be visible after PgUp", view)
	}
	if got := m.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, PgUp must NOT write to the text input", got)
	}

	// Several consecutive PgDns return the view to the queue.
	for i := 0; i < 5; i++ {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if got := m.View(); !strings.Contains(got, "message-29") {
		t.Fatalf("View() = %q, after several PgDn the tail %q must become visible again", got, "message-29")
	}
	if got := m.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, PgDn must NOT write to the text input", got)
	}

	// With pending permission PgUp is still scrolling: it does not trigger the gate.
	fake := &fakeAgent{}
	m2 := NewModel(fake, "s1", nil)
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m2 = apply(t, m2, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("permission-history-%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("permission-message-%02d", i),
		}})
	}
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	beforeOffset := m2.viewport.YOffset
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyPgUp})
	if got := m2.viewport.YOffset; got >= beforeOffset {
		t.Fatalf("viewport.YOffset = %d, want less than %d: PgUp with pending permission must scroll the transcript", got, beforeOffset)
	}
	if len(fake.resolved) != 0 {
		t.Fatalf("ResolvePermission was called %d times; PgUp must NOT trigger the permission gate", len(fake.resolved))
	}
	if got := m2.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, PgUp with pending permission must NOT write to the text input", got)
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
			Text: fmt.Sprintf("message-%02d", i),
		}})
	}

	// Two wheels up go back in history: the tail is no longer visible.
	m = apply(t, m, wheelUp)
	m = apply(t, m, wheelUp)
	view := m.View()
	if strings.Contains(view, "message-29") {
		t.Fatalf("View() = %q, after scrolling up the tail %q must NOT remain visible", view, "message-29")
	}
	if !strings.Contains(view, "message-") {
		t.Fatalf("View() = %q, an earlier history message should be visible after scrolling up", view)
	}
	if got := m.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, the wheel must NOT write to the text input", got)
	}

	// Several wheels below return the view to the tail.
	for i := 0; i < 5; i++ {
		m = apply(t, m, wheelDown)
	}
	if got := m.View(); !strings.Contains(got, "message-29") {
		t.Fatalf("View() = %q, after scrolling down the tail %q must become visible again", got, "message-29")
	}

	// With permission pending, the wheel continues to scroll: it does not trigger the gate.
	fake := &fakeAgent{}
	m2 := NewModel(fake, "s1", nil)
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 40, Height: 10})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, wheelUp)
	if len(fake.resolved) != 0 {
		t.Fatalf("ResolvePermission was called %d times; the wheel must NOT trigger the permission gate", len(fake.resolved))
	}
	if got := m2.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, the wheel with pending permission must NOT write to the text input", got)
	}
}

func TestModel_MouseWheelSurvivesTinyOrUnsizedTerminal(t *testing.T) {
	// TRIANGULATE: a poor fix could assume an already sized viewport when resending the wheel. Without prior WindowSizeMsg (ready == false) or with pty 0x0, a wheel event should not panic and View() should continue to return a string even if it is demoted.
	t.Run("without prior WindowSizeMsg", func(t *testing.T) {
		m := NewModel(nil, "s1", nil)

		m = apply(t, m, wheelUp)
		if got := m.View(); got == "" {
			t.Fatalf("View() = %q, without a known terminal size it must return a string even in degraded mode", got)
		}
	})

	t.Run("pty 0x0 with folded message", func(t *testing.T) {
		m := NewModel(nil, "s1", nil)

		m = apply(t, m, tea.WindowSizeMsg{Width: 0, Height: 0})
		m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hello"}})

		m = apply(t, m, wheelUp)
		if got := m.View(); got == "" {
			t.Fatalf("View() = %q, with a 0x0 terminal the wheel must not crash the TUI and View must return a string even in degraded mode", got)
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
			Text: fmt.Sprintf("message-%02d", i),
		}})
	}

	// Two wheels up: the tail is no longer visible (precondition of the case).
	m = apply(t, m, wheelUp)
	m = apply(t, m, wheelUp)
	offset := m.viewport.YOffset
	if got := m.View(); strings.Contains(got, "message-29") {
		t.Fatalf("View() = %q, after scrolling up the tail %q must NOT remain visible", got, "message-29")
	}

	// New activity arrives: preserves the reading position and shows a passive arrow instead of dragging the user to the queue.
	m = apply(t, m, EventMsg{Message: &session.Message{
		ID:   "u30",
		Role: session.RoleUser,
		Text: "message-30",
	}})
	if got := m.viewport.YOffset; got != offset {
		t.Fatalf("viewport.YOffset = %d, want %d: new activity must not move the reading position", got, offset)
	}
	view := ansi.Strip(m.View())
	if strings.Contains(view, "message-30") {
		t.Fatalf("View() = %q, new activity must not show the tail again", view)
	}
	if !strings.Contains(view, "↓") {
		t.Fatalf("View() = %q, it must show a passive arrow when new activity is outside the view", view)
	}
}

func TestModel_StreamingRevealPreservesReadingPositionAndMarksActivity(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID: fmt.Sprintf("u%02d", i), Role: session.RoleUser, Text: fmt.Sprintf("message-%02d", i),
		}})
	}
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: strings.Repeat("stream ", 20)})
	m = apply(t, m, wheelUp)
	m = apply(t, m, wheelUp)
	offset := m.viewport.YOffset

	m = apply(t, m, revealTickMsg{})

	if got := m.viewport.YOffset; got != offset {
		t.Fatalf("viewport.YOffset = %d, want %d: reveal must not drag the reading position", got, offset)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↓") {
		t.Fatalf("View() = %q, reveal outside the view must mark new activity", view)
	}
}

func TestModel_ReturningToBottomClearsNewActivityIndicatorAndResumesFollowing(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID: fmt.Sprintf("u%02d", i), Role: session.RoleUser, Text: fmt.Sprintf("message-%02d", i),
		}})
	}
	m = apply(t, m, wheelUp)
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u30", Role: session.RoleUser, Text: "message-30"}})

	for !m.viewport.AtBottom() {
		m = apply(t, m, wheelDown)
	}
	if view := ansi.Strip(m.View()); strings.Contains(view, "↓") {
		t.Fatalf("View() = %q, returning to the bottom must hide the indicator", view)
	}

	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u31", Role: session.RoleUser, Text: "message-31"}})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "message-31") {
		t.Fatalf("View() = %q, returning to the bottom must resume following", view)
	}
}

func TestModel_NewActivityIndicatorIsPassiveAndAgentOnly(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID: fmt.Sprintf("u%02d", i), Role: session.RoleUser, Text: fmt.Sprintf("message-%02d", i),
		}})
	}
	m = apply(t, m, wheelUp)

	// A local change of presentation is not new activity of the agent.
	m = m.syncViewport()
	if view := ansi.Strip(m.View()); strings.Contains(view, "↓") {
		t.Fatalf("View() = %q, a local change must not show the indicator", view)
	}

	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u30", Role: session.RoleUser, Text: "message-30"}})
	beforeOffset := m.viewport.YOffset
	beforeView := m.View()
	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.viewport.Width - 1,
		Y:      m.viewport.Height - 1,
	})
	if got := m.viewport.YOffset; got != beforeOffset {
		t.Fatalf("viewport.YOffset = %d, want %d: the arrow must be passive", got, beforeOffset)
	}
	if got := m.View(); got != beforeView {
		t.Fatalf("View() changed after clicking the passive arrow:\nbefore=%q\nafter=%q", beforeView, got)
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
			Text: fmt.Sprintf("message-%02d", i),
		}})
	}
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})

	before := m.View()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion})

	if len(fake.resolved) != 0 {
		t.Fatalf("ResolvePermission was called %d times; a click must NOT trigger the permission gate", len(fake.resolved))
	}
	if got := m.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, the click and movement must NOT write to the text input", got)
	}
	if got := m.View(); got != before {
		t.Fatalf("View() changed after the click/movement:\nbefore = %q\nafter = %q; non-wheel mouse events must be inert", before, got)
	}
}

func TestModel_WorkingIndicatorVisibleWhileRunning(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	// Without a run in progress there is no indicator.
	if got := m.View(); strings.Contains(got, "working") {
		t.Fatalf("View() = %q, no run in progress should show the working indicator", got)
	}

	// The user sends a prompt: the stable indicator appears.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.View(); !strings.Contains(got, "Checking context") {
		t.Fatalf("View() = %q, it must show indicator %q while the run continues", got, "Checking context")
	}

	// With ready (known terminal size) the indicator is also visible.
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	if got := m.View(); !strings.Contains(got, "Checking context") {
		t.Fatalf("View() = %q, with ready set the indicator %q must also appear", got, "Checking context")
	}

	// Clean end of run: the indicator disappears.
	m = apply(t, m, activeRunDone(m, ""))
	if got := m.View(); strings.Contains(got, "Checking context") {
		t.Fatalf("View() = %q, RunDoneMsg must remove the working indicator", got)
	}
}

func TestModel_WorkingIndicatorUsesConcreteMicrocopy(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.working = true

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "read-1", ToolName: "read", Input: json.RawMessage(`{"path":"internal/tui/view.go"}`)})
	if got := m.View(); !strings.Contains(got, "Checking context") || strings.Contains(got, " working") {
		t.Fatalf("View() = %q, reading context must render concrete UX microcopy and not generic working", got)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "read-1", ToolName: "read"})
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "edit-1", ToolName: "edit", Input: json.RawMessage(`{"input":"[internal/tui/view.go#ABCD]\n- old\n+ new"}`)})
	if got := m.View(); !strings.Contains(got, "Reviewing changes") || strings.Contains(got, " working") {
		t.Fatalf("View() = %q, changing files must render concrete UX microcopy and not generic working", got)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "edit-1", ToolName: "edit"})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "Done"})
	if got := m.View(); !strings.Contains(got, "Preparing response") || strings.Contains(got, " working") {
		t.Fatalf("View() = %q, response streaming must render concrete UX microcopy and not generic working", got)
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
			Text: fmt.Sprintf("message-%02d", i),
		}})
	}

	// Sending a prompt turns on working: the status line appears.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	if !strings.Contains(view, "Checking context") {
		t.Fatalf("View() = %q, a run in progress must show indicator %q", view, "Checking context")
	}
	if lines := strings.Count(view, "\n") + 1; lines > 12 {
		t.Fatalf("View() has %d lines; the status line must NOT exceed the bounded height (<= 12)", lines)
	}
	if !strings.Contains(view, "message-29") {
		t.Fatalf("View() = %q, the view must follow the tail (%q visible) even with the status line", view, "message-29")
	}
}

// TestModel_WorkingIndicatorAlignsWithComposerLeftMargin covers the left margin of the "working" status line: the rest of the view (the composer box and the top bar) starts composerOuterMargin columns from the left edge of the terminal, but the spinner line starts in column 0 (attached to the edge). The spinner glyph should align with the "┌" edge of the composer box, both to composerOuterMargin columns.
func TestModel_WorkingIndicatorAlignsWithComposerLeftMargin(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "hello")
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
		t.Fatalf("View() = %q, no line with spinner %q was found", view, m.spinner.View())
	}

	composerCol := -1
	for _, line := range lines {
		plain := ansi.Strip(line)
		trimmed := strings.TrimLeft(plain, " ")
		if strings.HasPrefix(trimmed, "┌") {
			composerCol = len(plain) - len(trimmed)
			break
		}
	}
	if composerCol == -1 {
		t.Fatalf("View() = %q, no line with the composer's top border (┌) was found", view)
	}

	if spinnerCol != composerCol {
		t.Fatalf("spinner column = %d, composer border column = %d; both must match (same left margin)", spinnerCol, composerCol)
	}
	if spinnerCol != composerOuterMargin {
		t.Fatalf("spinner column+%q = %d, expected composerOuterMargin (%d)", "working", spinnerCol, composerOuterMargin)
	}
}

// TestModel_WorkingIndicatorAlignsWithComposerLeftMargin_WiderTerminal repeats the column alignment assertion with a different terminal width (100, not 40/80) to rule out the observed margin being a hardcoded value that only happens to match a particular run by chance: if the implementation calculated the spinner column from the terminal width (e.g. relative or proportional) instead of a fixed prefix of composerOuterMargin spaces, this case would detect it because it would still expect exactly composerOuterMargin regardless of the width. It also confirms that no line in the view exceeds the width of the terminal.
func TestModel_WorkingIndicatorAlignsWithComposerLeftMargin_WiderTerminal(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

	m = typeRunes(t, m, "hello")
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
		t.Fatalf("View() = %q, no line with spinner %q was found", view, m.spinner.View())
	}

	composerCol := -1
	for _, line := range lines {
		plain := ansi.Strip(line)
		trimmed := strings.TrimLeft(plain, " ")
		if strings.HasPrefix(trimmed, "┌") {
			composerCol = len(plain) - len(trimmed)
			break
		}
	}
	if composerCol == -1 {
		t.Fatalf("View() = %q, no line with the composer's top border (┌) was found", view)
	}

	if spinnerCol != composerCol {
		t.Fatalf("spinner column = %d, composer border column = %d; both must match at width 100", spinnerCol, composerCol)
	}
	if spinnerCol != composerOuterMargin {
		t.Fatalf("spinner column+%q at width 100 = %d, expected composerOuterMargin (%d), not a width-dependent value", "working", spinnerCol, composerOuterMargin)
	}
}

// TestModel_WorkingIndicatorDoesNotOverflowTinyTerminal covers a very small terminal (Width 10): chatContent() bounds the margin of the status line with `min(composerOuterMargin, m.chatContentWidth()/2)`, the same pattern that topBarLine uses for its margin, so no line in View() should exceed the width of the terminal (10 cells) or produce a negative indentation. If a future implementation reverted to an unbounded fixed prefix, this test would detect it.
func TestModel_WorkingIndicatorDoesNotOverflowTinyTerminal(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 10, Height: 24})

	m = typeRunes(t, m, "hello")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	assertNoLineWiderThan(t, view, 10)

	lines := strings.Split(view, "\n")
	for _, line := range lines {
		plain := ansi.Strip(line)
		if strings.Contains(plain, m.spinner.View()) {
			indent := len(plain) - len(strings.TrimLeft(plain, " "))
			if indent < 0 {
				t.Fatalf("View() = %q, the status line has negative/corrupt indentation (%d) on a narrow terminal", view, indent)
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
		m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hello"}})
		if got := m.View(); got == "" {
			t.Fatalf("View() = %q, with a 0x0 terminal it must return a string even in degraded mode", got)
		}
	})

	t.Run("one-line terminal with a run in progress", func(t *testing.T) {
		fake := &fakeAgent{}
		m := NewModel(fake, "s1", nil)

		// With 1 line high, turning on working (input + status line) leaves the reserved lines above the height: negative viewport.
		m = apply(t, m, tea.WindowSizeMsg{Width: 20, Height: 1})
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

		if len(fake.sent) != 1 {
			t.Fatalf("SendPrompt was called %d times; Enter must send the prompt exactly once", len(fake.sent))
		}
		m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hello"}})
		if got := m.View(); got == "" {
			t.Fatalf("View() = %q, with a one-line terminal it must return a string even in degraded mode", got)
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
			Text: fmt.Sprintf("message-%02d", i),
		}})
	}

	// The terminal grows to a usable size.
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	view := m.View()
	if !strings.Contains(view, "message-29") {
		t.Fatalf("View() = %q, after the terminal grows the transcript tail must become visible again (message-29)", view)
	}
	if strings.Contains(view, "message-00") {
		t.Fatalf("View() = %q, height must remain bounded: message-00 does not fit in 10 lines", view)
	}
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("View() has %d lines; it must not exceed terminal height (10)", lines)
	}
}

func TestModel_WrapsLongAssistantTextToTerminalWidth(t *testing.T) {
	// Real bug (reproduced E2E): on a narrow terminal the assistant's response looks like ONE truncated line. The transcript is dumped raw into the bubbles viewport, which horizontally cuts each line to the width of the terminal (ansi.Cut) instead of doing word-wrap: the end of the text disappears from view.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	// The sentinel token has no hyphens: glamour v1 breaks lines at hyphens,
	// which would split it across the wrap and defeat the Contains assert.
	long := "this is a long assistant response that on a narrow terminal must wrap to several lines so it can be read in full finrespuesta"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: long})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "finrespuesta") {
		t.Fatalf("View() = %q, the end of text %q must be visible: text wider than the terminal must wrap to several lines, not be truncated", view, "finrespuesta")
	}
	assertNoLineWiderThan(t, view, 40)
}

func TestModel_RewrapsOnResize(t *testing.T) {
	// TRIANGULATE: a poor fix could wrap the transcript ONE time to the first advertised width. When the terminal narrows, the text should be re-wrapped to the new width, not cut to the old width.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	// Hyphen-free sentinel: glamour v1 breaks lines at hyphens.
	long := "this long assistant response must be rewrapped when the terminal width changes finrespuesta"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: long})
	m = drainReveal(t, m)

	// The terminal narrows: the transcript must be re-wrapped to 24 cells.
	m = apply(t, m, tea.WindowSizeMsg{Width: 24, Height: 10})

	view := m.View()
	if !strings.Contains(view, "finrespuesta") {
		t.Fatalf("View() = %q, the end of text %q must remain visible after resize: the transcript must rewrap to the new width", view, "finrespuesta")
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
		t.Fatalf("View() without ANSI = %q, the final suffix must remain visible even when Markdown rendering wraps it", view)
	}
	assertNoLineWiderThan(t, view, 40)
}

func TestModel_FollowsTailOfWrappedResponse(t *testing.T) {
	// TRIANGULATE: GotoBottom counts lines on the content already loaded in the viewport. If the transcript were wrapped AFTER SetContent, the line count would be short and the view would not follow the queue of a wrapped response that occupies more lines than the height of the viewport.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	// ~500 word cells: wrapped at 40 it occupies ~14 lines, more than the height of the viewport (9). Distinctive token at the beginning and another at the end.
	long := "response-start " + strings.Repeat("word ", 60) + "response-end"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: long})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "response-end") {
		t.Fatalf("View() = %q, the view must follow the tail: the end %q of the wrapped response must be visible", view, "response-end")
	}
	if strings.Contains(view, "response-start") {
		t.Fatalf("View() = %q, the beginning %q must NOT be visible: the wrapped response exceeds the viewport and the view must follow the tail", view, "response-start")
	}
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("View() has %d lines, must not exceed terminal height (10)", lines)
	}
}

func TestModel_ComposerBottomBorderShowsModel(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "openrouter/free")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})

	plain := ansi.Strip(m.View())
	lines := strings.Split(plain, "\n")
	var bottomBorder string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "└") {
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

func TestModel_ComposerBottomBorderShowsNonDefaultReasoningEffort(t *testing.T) {
	fake := &fakeAgent{reasoning: llm.ReasoningEffortHigh}
	m := NewModel(fake, "s1", nil).WithStatus("build", "gpt-5.6-sol")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})

	bottomBorder := strings.Split(ansi.Strip(m.composerBox()), "\n")[2]
	if !strings.Contains(bottomBorder, "gpt-5.6-sol(high)") {
		t.Fatalf("composer bottom border = %q, want model and reasoning effort", bottomBorder)
	}
}

func TestModel_ComposerBottomBorderOmitsDefaultReasoningEffort(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "fable-5")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})

	bottomBorder := strings.Split(ansi.Strip(m.composerBox()), "\n")[2]
	if !strings.Contains(bottomBorder, "fable-5") || strings.Contains(bottomBorder, "default") || strings.Contains(bottomBorder, "()") {
		t.Fatalf("composer bottom border = %q, default effort must show only the model", bottomBorder)
	}
}

func TestModel_ComposerBottomBorderUpdatesAfterReasoningCommand(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "gpt-5.6-sol")
	m = typeRunes(t, m, "/reasoning:high")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/reasoning:high returned an async command")
	}
	m = updated.(Model)

	if label := m.composerModelLabel(); label != "gpt-5.6-sol(high)" {
		t.Fatalf("composer model label = %q, want updated reasoning effort", label)
	}
}

func TestModel_ComposerBottomBorderKeepsYoloVisibleWithReasoningEffort(t *testing.T) {
	fake := &fakeAgent{reasoning: llm.ReasoningEffortHigh, yoloEnabled: true}
	m := NewModel(fake, "s1", nil).WithStatus("build", "openrouter/free")
	m = apply(t, m, tea.WindowSizeMsg{Width: 28, Height: 12})

	bottomBorder := strings.Split(ansi.Strip(m.composerBox()), "\n")[2]
	if !strings.Contains(bottomBorder, "… · YOLO") {
		t.Fatalf("composer bottom border = %q, truncated model must keep YOLO visible", bottomBorder)
	}
	assertBoxLinesExactWidth(t, m.composerBox(), 28)
}

func TestModel_ComposerBottomBorderKeepsModeAtExactTruncationBoundary(t *testing.T) {
	tests := []struct {
		name string
		make func() Model
		want string
	}{
		{
			name: "plan",
			make: func() Model {
				m := NewModel(nil, "s1", nil).WithStatus("build", "long-model")
				return apply(t, m, tea.KeyMsg{Type: tea.KeyTab})
			},
			want: "… · plan",
		},
		{
			name: "YOLO",
			make: func() Model {
				return NewModel(&fakeAgent{reasoning: llm.ReasoningEffortHigh, yoloEnabled: true}, "s1", nil).WithStatus("build", "long-model")
			},
			want: "… · YOLO",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.make()
			m = apply(t, m, tea.WindowSizeMsg{Width: 14, Height: 12})

			bottomBorder := strings.Split(ansi.Strip(m.composerBox()), "\n")[2]
			if !strings.Contains(bottomBorder, tt.want) {
				t.Fatalf("composer bottom border = %q, want exact-fit suffix %q", bottomBorder, tt.want)
			}
			assertBoxLinesExactWidth(t, m.composerBox(), 14)
		})
	}
}

func TestModel_ComposerHasTwoCellOuterMargin(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "model")
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 8})

	lines := strings.Split(ansi.Strip(m.View()), "\n")
	bottom := -1
	for index, line := range lines {
		if strings.Contains(line, "└") {
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
	if !strings.HasPrefix(bottomBorder, "└") || !strings.HasSuffix(bottomBorder, "┘") {
		t.Fatalf("composer bottom border = %q, square corners must remain intact", bottomBorder)
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
	m = typeRunes(t, m, "first line")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = typeRunes(t, m, "second line")

	if got, want := m.composer.input.Value(), "first line\nsecond line"; got != want {
		t.Fatalf("input.Value() = %q, Ctrl+J must insert a newline and preserve draft %q", got, want)
	}
	if got := strings.Count(ansi.Strip(m.composerBox()), "\n"); got != 3 {
		t.Fatalf("composerBox() has %d newlines; with two lines it must grow to four lines including borders", got)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.sent) != 1 {
		t.Fatalf("SendPrompt was called %d times; Enter must send the multiline prompt exactly once", len(fake.sent))
	}
	if got, want := fake.sent[0].text, "first line\nsecond line"; got != want {
		t.Fatalf("SendPrompt text = %q, want %q", got, want)
	}
}

func TestModel_ComposerMultilineRendersPromptOnlyOnFirstRow(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 32, Height: 12})
	m = typeRunes(t, m, "first line that wraps")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = typeRunes(t, m, "second line")

	plain := ansi.Strip(m.composerBox())
	if got := strings.Count(plain, "›"); got != 1 {
		t.Fatalf("composer prompt count = %d, want 1 across wrapped and explicit multiline rows:\n%s", got, plain)
	}
	if line := lineWith(t, plain, "first line"); !strings.Contains(line, "› first line") {
		t.Fatalf("first composer row = %q, the prompt must stay beside the first text row", line)
	}
}

func TestModel_ComposerGrowthStopsAtFiveLines(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	for line := 0; line < composerMaxLines+2; line++ {
		m = typeRunes(t, m, fmt.Sprintf("line %d", line))
		if line < composerMaxLines+1 {
			m = apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
		}
	}

	if got := m.composer.input.Height(); got != composerMaxLines {
		t.Fatalf("input.Height() = %d, the composer must stop growing at %d lines", got, composerMaxLines)
	}
	if got := strings.Count(ansi.Strip(m.composerBox()), "\n"); got != composerMaxLines+1 {
		t.Fatalf("composerBox() has %d newlines; at the limit it must render %d lines including borders", got, composerMaxLines+1)
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
		if strings.HasPrefix(strings.TrimLeft(line, " "), "┌") {
			topBorder = line
			break
		}
	}
	if topBorder == "" {
		t.Fatalf("View() = %q, want a composer top border", plain)
	}
	for _, want := range []string{"↑ 1.2k", "↓ 345"} {
		if !strings.Contains(topBorder, want) {
			t.Fatalf("composer top border = %q, want it to contain %q", topBorder, want)
		}
	}
	if strings.Contains(topBorder, "ctx") {
		t.Fatalf("composer top border = %q, context usage must not be shown alongside token counts", topBorder)
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
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~1.2k ↓ 0") || strings.Contains(view, "ctx") {
		t.Fatalf("View() = %q, want live input usage without context usage", view)
	}

	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: strings.Repeat("a", 3_000)})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~1.2k ↓ ~1k") || strings.Contains(view, "ctx") {
		t.Fatalf("View() = %q, want live output usage without context usage", view)
	}

	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: strings.Repeat("b", 1_500)})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~1.2k ↓ ~1.5k") || strings.Contains(view, "ctx") {
		t.Fatalf("View() = %q, want live reasoning usage without context usage", view)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolInputDelta, Text: strings.Repeat("c", 1_500)})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~1.2k ↓ ~2k") || strings.Contains(view, "ctx") {
		t.Fatalf("View() = %q, want live tool usage without context usage", view)
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindStepEnded,
		Usage: &session.Usage{
			InputTokens:     1_300,
			OutputTokens:    900,
			ReasoningTokens: 100,
		},
	})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ 1.3k ↓ 900") || strings.Contains(view, "ctx") {
		t.Fatalf("View() = %q, want exact provider usage without context usage", view)
	}
}

func TestModel_LiveUsageTransitions(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m.outputBytes = 9
	m.reasoningBytes = 12
	m.toolInputBytes = 15
	m = m.foldEvent(EventMsg{Kind: session.KindStepStarted, Usage: &session.Usage{InputTokens: 20}})
	if !m.liveUsage || m.outputBytes != 0 || m.reasoningBytes != 0 || m.toolInputBytes != 0 {
		t.Fatalf("StepStarted = live:%v bytes:%d/%d/%d, want live usage with reset counters", m.liveUsage, m.outputBytes, m.reasoningBytes, m.toolInputBytes)
	}

	m = m.foldEvent(EventMsg{Kind: session.KindTextDelta, Text: "abcdef"})
	estimated := *m.usage
	m = m.foldEvent(EventMsg{Kind: session.KindStepEnded})
	if m.liveUsage || *m.usage != estimated {
		t.Fatalf("StepEnded without Usage = live:%v usage:%+v, want to preserve estimate %+v and close live usage", m.liveUsage, *m.usage, estimated)
	}

	m.liveUsage = true
	m = m.foldEvent(EventMsg{Kind: session.KindStepFailed, Error: "boom"})
	if m.liveUsage {
		t.Fatal("StepFailed must close live usage")
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
			t.Fatalf("updateLiveUsage() modified a model without active usage: %+v", m)
		}
	}
}

func TestEstimatedTokens(t *testing.T) {
	for _, tc := range []struct{ bytes, want int }{{0, 0}, {1, 1}, {2, 1}, {3, 1}, {30_000, 10_000}} {
		if got := estimatedTokens(tc.bytes); got != tc.want {
			t.Errorf("estimatedTokens(%d) = %d, want %d", tc.bytes, got, tc.want)
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

	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~10k") || strings.Contains(view, "ctx") {
		t.Fatalf("live View() = %q, want the conservative 10k estimate marked as approximate without context usage", view)
	}

	m = apply(t, m, EventMsg{
		Kind:  session.KindStepEnded,
		Usage: &session.Usage{InputTokens: 9_100, OutputTokens: 250},
	})

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "↑ 9.1k") || strings.Contains(view, "ctx") {
		t.Fatalf("completed View() = %q, want exact provider usage without context usage", view)
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
		t.Fatalf("composerBox() = %q, a box that is too narrow must omit the label", plain)
	}
}

func TestModel_ComposerBoxWithoutUsageHasNoTokenLabel(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	if plain := ansi.Strip(m.composerBox()); strings.Contains(plain, "↑") || strings.Contains(plain, "↓") {
		t.Fatalf("composerBox() = %q, without usage it must not show tokens", plain)
	}
}

func TestFormatTokenCount(t *testing.T) {
	for _, tc := range []struct {
		tokens int
		want   string
	}{
		{0, "0"}, {999, "999"}, {1_000, "1k"}, {1_500, "1.5k"},
		{9_999, "10k"}, {10_000, "10k"}, {128_000, "128k"}, {1_000_000, "1m"},
	} {
		if got := formatTokenCount(tc.tokens); got != tc.want {
			t.Errorf("formatTokenCount(%d) = %q, want %q", tc.tokens, got, tc.want)
		}
	}
}

func TestModel_ComposerBoxWrapsInput(t *testing.T) {
	// TRIANGULATE: the input ALWAYS lives inside a square-corner box that spans the width of the terminal (Claude Code style), whether or not the composer's status is set.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	view := m.View()
	for _, want := range []string{"┌", "└"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, the input must render inside a square-border box: missing %q", view, want)
		}
	}
	assertBoxLinesExactWidth(t, view, 40)

	// The box has horizontal padding (Claude Code style): the inner line starts with "│ ›" (border, space, prompt), not with the prompt attached to the edge. It is measured without ANSI because the prompt is stylized.
	if plain := ansi.Strip(view); !strings.Contains(plain, "│ ›") {
		t.Fatalf("View() without ANSI = %q, the box's inner line must have horizontal padding: it must contain %q (border, space, prompt), not a prompt attached to the edge", plain, "│ ›")
	}

	topAt, inputAt, bottomAt := -1, -1, -1
	for i, line := range strings.Split(view, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		switch {
		case strings.HasPrefix(trimmed, "┌"):
			topAt = i
		case strings.HasPrefix(trimmed, "└"):
			bottomAt = i
		case strings.Contains(line, inputPrompt):
			inputAt = i
		}
	}
	if topAt == -1 || inputAt == -1 || bottomAt == -1 || topAt >= inputAt || inputAt >= bottomAt {
		t.Fatalf("View() = %q, the input line (%q at %d) must be BETWEEN the top border (┌ at %d) and bottom border (└ at %d)", view, inputPrompt, inputAt, topAt, bottomAt)
	}

	// With set status the foot is BELOW the bottom edge of the box.
	m2 := NewModel(nil, "s1", nil).WithStatus("build", "openrouter/free")
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 40, Height: 12})
	view2 := m2.View()
	bottomAt2 := strings.Index(view2, "└")
	footerAt := strings.Index(view2, "openrouter/free")
	if bottomAt2 == -1 || footerAt == -1 || footerAt < bottomAt2 {
		t.Fatalf("View() = %q, the status footer (openrouter/free at %d) must appear AFTER the box's bottom border (└ at %d)", view2, footerAt, bottomAt2)
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
			Text: fmt.Sprintf("message-%02d", i),
		}})
	}

	// Sending a prompt turns on working: the indicator appears on the box.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	if lines := strings.Count(view, "\n") + 1; lines > 12 {
		t.Fatalf("View() has %d lines; box + footer + indicator must not exceed bounded height (<= 12)", lines)
	}
	for _, want := range []string{"message-29", "Checking context", "openrouter/free"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, must contain %q (transcript tail, work indicator, and status footer)", view, want)
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
		t.Fatalf("View() has %d lines; a long prompt must not exceed bounded height (<= 10)", lines)
	}
	if got := strings.Count(view, "›"); got != 1 {
		t.Fatalf("View() = %q, prompt %q must appear exactly once even when text wraps (count=%d)", view, "›", got)
	}
	interior := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "│") {
			interior++
		}
	}
	if interior != 3 {
		t.Fatalf("View() = %q, the box must grow to the 3 visual rows that fit in this terminal (│ lines = %d)", view, interior)
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
		t.Fatalf("View() = %q, after Tab the composer footer must show %q", view, "openrouter/free · plan")
	}
	if strings.Contains(view, "build ·") {
		t.Fatalf("View() = %q, after Tab the footer must NOT continue showing %q", view, "build ·")
	}

	// In plan mode, Enter sends the prompt via SendPlanPrompt, not via SendPrompt.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("research x")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.planSent) != 1 {
		t.Fatalf("SendPlanPrompt was called %d times; Enter in plan mode must send the prompt exactly once through the plan path", len(fake.planSent))
	}
	if got := fake.planSent[0]; got.sessionID != "s1" || got.text != "research x" {
		t.Fatalf("SendPlanPrompt(%q, %q), expected SendPlanPrompt(%q, %q)", got.sessionID, got.text, "s1", "research x")
	}
	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt was called %d times; in plan mode the prompt must NOT use the build path", len(fake.sent))
	}
}

func TestModel_TabTogglesBackToBuild(t *testing.T) {
	// TRIANGULATE: Tab TOggles the mode, not just turns it on. Two Tab returns the composer footer to build and Enter sends again via SendPrompt (the normal path), not SendPlanPrompt.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	view := m.View()
	if !strings.Contains(view, " m ─┘") {
		t.Fatalf("View() = %q, after Tab Tab the composer border must show only model %q again", view, "m")
	}
	if strings.Contains(view, "· plan") {
		t.Fatalf("View() = %q, after Tab Tab the footer must NOT continue showing %q", view, "· plan")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hazlo")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 1 {
		t.Fatalf("SendPrompt was called %d times; back in build, Enter must send exactly once through the normal path", len(fake.sent))
	}
	if got := fake.sent[0]; got.sessionID != "s1" || got.text != "hazlo" {
		t.Fatalf("SendPrompt(%q, %q), expected SendPrompt(%q, %q)", got.sessionID, got.text, "s1", "hazlo")
	}
	if len(fake.planSent) != 0 {
		t.Fatalf("SendPlanPrompt was called %d times; after returning to build, the prompt must not use the plan path", len(fake.planSent))
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
	if !strings.Contains(view, " m ─┘") {
		t.Fatalf("View() = %q, with pending permission Tab must not change the border: it must keep showing model %q", view, "m")
	}
	if strings.Contains(view, "· plan") {
		t.Fatalf("View() = %q, with pending permission Tab must not activate plan mode", view)
	}
}

func TestModel_PresentPlanOffersAcceptAndYExecutes(t *testing.T) {
	// When the agent presents a plan (tool present_plan successfully posted), the conversation displays a pending approval offer; the 'y' key accepts the plan via Agent.AcceptPlan and withdraws the offer.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "p1"})

	view := m.View()
	planLine := lineWith(t, view, "! Plan")
	if !strings.Contains(planLine, "(y run / n stay in plan)") {
		t.Fatalf("approval offer = %q, must contain %q", planLine, "(y run / n stay in plan)")
	}

	// 'and' accept the plan: ONE call to AcceptPlan with the TUI session.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	if len(fake.accepted) != 1 {
		t.Fatalf("AcceptPlan was called %d times; 'y' must accept the plan exactly once", len(fake.accepted))
	}
	if got := fake.accepted[0]; got != "s1" {
		t.Fatalf("AcceptPlan(%q), expected AcceptPlan(%q)", got, "s1")
	}
	if got := m.View(); strings.Contains(got, "! Plan") {
		t.Fatalf("View() = %q, accepting the plan must withdraw the approval offer", got)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt was called %d times; accepting the plan must not send a prompt through the build path", len(fake.sent))
	}
	if len(fake.planSent) != 0 {
		t.Fatalf("SendPlanPrompt was called %d times; accepting the plan must not send a prompt through the plan path", len(fake.planSent))
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

	if got := m.View(); strings.Contains(got, "! Plan") {
		t.Fatalf("View() = %q, 'n' must withdraw the plan approval offer", got)
	}
	if len(fake.accepted) != 0 {
		t.Fatalf("AcceptPlan was called %d times; 'n' must NOT accept the plan", len(fake.accepted))
	}
	if got := m.View(); !strings.Contains(got, "m · plan") {
		t.Fatalf("View() = %q, after 'n' the footer must continue showing %q: rejecting the offer does not change mode", got, "m · plan")
	}

	// The next shipment continues to follow the plan path: the mode did not change.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("adjust the plan")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.planSent) != 1 {
		t.Fatalf("SendPlanPrompt was called %d times; after 'n' Enter must continue sending through the plan path exactly once", len(fake.planSent))
	}
	if got := fake.planSent[0]; got.sessionID != "s1" || got.text != "adjust the plan" {
		t.Fatalf("SendPlanPrompt(%q, %q), expected SendPlanPrompt(%q, %q)", got.sessionID, got.text, "s1", "adjust the plan")
	}
	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt was called %d times; after 'n' the prompt must NOT use the build path", len(fake.sent))
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
	if got := m.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, normal runes must NOT enter input while a plan is pending", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.sent) != 0 || len(fake.planSent) != 0 || len(fake.accepted) != 0 {
		t.Fatalf("sent=%d planSent=%d accepted=%d; neither Enter nor normal runes must send or accept anything with a pending plan", len(fake.sent), len(fake.planSent), len(fake.accepted))
	}

	// 'y' accepts: build again and the run is in progress.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(fake.accepted) != 1 || fake.accepted[0] != "s1" {
		t.Fatalf("accepted = %v, 'y' must call AcceptPlan(%q) exactly once", fake.accepted, "s1")
	}
	view := m.View()
	if !strings.Contains(view, " m ─┘") {
		t.Fatalf("View() = %q, after accepting the plan the border must show only model %q again", view, "m")
	}
	if strings.Contains(view, "· plan") {
		t.Fatalf("View() = %q, after accepting the plan the footer must NOT continue showing %q", view, "· plan")
	}
	if !strings.Contains(view, "Checking context") {
		t.Fatalf("View() = %q, after accepting the plan the run remains active: it must show indicator %q", view, "Checking context")
	}
}

func TestModel_PresentPlanFailedDoesNotOfferApproval(t *testing.T) {
	// Fine point: a present_plan set with Tool.Failed does NOT offer approval and the keyboard remains normal (the rune goes to the input and 'y' does not accept anything).
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "p1", Error: "invalid plan"})

	if got := m.View(); strings.Contains(got, "! Plan") {
		t.Fatalf("View() = %q, a failed present_plan must NOT offer plan approval", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(fake.accepted) != 0 {
		t.Fatalf("AcceptPlan was called %d times; without a pending offer 'y' must NOT accept anything", len(fake.accepted))
	}
	if got := m.composer.input.Value(); got != "y" {
		t.Fatalf("input.Value() = %q, without a pending offer the rune 'y' must go to normal input", got)
	}
}

func TestModel_EnterSendsTypedPromptViaAgent(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	// The user types "hello" and presses Enter.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 1 {
		t.Fatalf("SendPrompt was called %d times; Enter must send the prompt exactly once", len(fake.sent))
	}
	if got := fake.sent[0]; got.sessionID != "s1" || got.text != "hello" {
		t.Fatalf("SendPrompt(%q, %q), expected SendPrompt(%q, %q)", got.sessionID, got.text, "s1", "hello")
	}
	if !m.Working() {
		t.Fatalf("Working() = false; the model must remain working after sending the prompt until RunDoneMsg")
	}
}

func TestModel_CtrlVPastesAndSubmitsImageInBuildAndPlanModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan bool
	}{
		{name: "build"},
		{name: "plan", plan: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeAgent{}
			reads := 0
			m := NewModel(fake, "s1", nil).WithImageClipboard(func() ([]byte, error) {
				reads++
				return []byte("png"), nil
			})
			m.planMode = tc.plan

			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
			m = updated.(Model)
			if reads != 0 || cmd == nil {
				t.Fatalf("reads=%d cmd=%v, clipboard read must be asynchronous", reads, cmd != nil)
			}
			m = apply(t, m, cmd())
			if got := m.composer.value(); got != "[image#1]" {
				t.Fatalf("composer = %q, want image marker", got)
			}

			m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			sent := fake.sent
			if tc.plan {
				sent = fake.planSent
			}
			if len(sent) != 1 || sent[0].text != "[image#1]" || len(sent[0].images) != 1 {
				t.Fatalf("sent = %+v, want one marker and one image", sent)
			}
			if got := sent[0].images[0]; got.MediaType != "image/png" || !slices.Equal(got.Data, []byte("png")) {
				t.Fatalf("image = %+v, want pasted PNG", got)
			}
		})
	}
}

func TestModel_DelayedImagePasteDoesNotCrossSuccessfulClear(t *testing.T) {
	fake := &fakeAgent{}
	m := typeRunes(t, NewModel(fake, "s1", nil).WithImageClipboard(func() ([]byte, error) {
		return []byte("png"), nil
	}), "send now")

	updated, paste := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = updated.(Model)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, paste())

	if got := m.composer.value(); got != "" {
		t.Fatalf("new draft = %q, delayed image from the submitted draft must be discarded", got)
	}
	if len(m.composer.images) != 0 {
		t.Fatalf("new draft images = %+v, want none", m.composer.images)
	}
}

func TestModel_ImageSendFailureRetainsAttachmentAndTextPasteRemainsText(t *testing.T) {
	fake := &fakeAgent{sendErr: errors.New("send failed")}
	m := NewModel(fake, "s1", nil)
	m.composer = m.composer.attachImage([]byte("png"))

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.composer.prompt(); got.Text != "[image#1]" || len(got.Images) != 1 {
		t.Fatalf("prompt after failure = %+v, want marker and attachment retained", got)
	}

	m = NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted text"), Paste: true})
	if got := m.composer.value(); got != "pasted text" {
		t.Fatalf("text paste = %q, want unchanged text", got)
	}
}

func TestModel_SendFailuresKeepPendingUserAction(t *testing.T) {
	t.Run("build prompt", func(t *testing.T) {
		fake := &fakeAgent{sendErr: errors.New("send failed")}
		m := typeRunes(t, NewModel(fake, "s1", nil), "hello")

		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)

		if cmd != nil || m.Working() || m.composer.input.Value() != "hello" {
			t.Fatalf("cmd=%v working=%v composer=%q", cmd != nil, m.Working(), m.composer.input.Value())
		}
		if got := m.entries[len(m.entries)-1]; got.kind != entryError || got.text != "send failed" {
			t.Fatalf("last entry = %+v", got)
		}
	})

	t.Run("plan prompt", func(t *testing.T) {
		fake := &fakeAgent{planErr: errors.New("plan send failed")}
		m := NewModel(fake, "s1", nil)
		m.planMode = true
		m = typeRunes(t, m, "research")

		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)

		if cmd != nil || m.Working() || !m.planMode || m.composer.input.Value() != "research" {
			t.Fatalf("cmd=%v working=%v planMode=%v composer=%q", cmd != nil, m.Working(), m.planMode, m.composer.input.Value())
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
	{Name: "commit", Description: "generate a commit"},
	{Name: "model", Description: "Select provider and model", BuiltIn: true},
	{Name: "compact", Description: "Compact conversation context", BuiltIn: true},
	{Name: "mcp", Description: "Toggle MCP servers on or off", BuiltIn: true},
	{Name: "connect", Description: "Connect a provider with an API key", BuiltIn: true},
	{Name: "review", Description: "review the diff"},
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
			msg.Type = tea.KeySpace // bubbletea reports the space as KeySpace
		}
		m = apply(t, m, msg)
	}
	return m
}

// menuSelectedLine returns the menu row marked with "❯ " (without ANSI). The
// selector sits inside the menu's left border, so the row starts with "│ ❯ ".
func menuSelectedLine(view string) string {
	for _, line := range strings.Split(view, "\n") {
		if plain := ansi.Strip(line); strings.Contains(plain, "│ ❯ ") {
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
	if !strings.Contains(commitLine, "generate a commit") {
		t.Fatalf("/commit line = %q, the filtered item must preserve its description", commitLine)
	}
	if strings.Contains(view, "/review") {
		t.Fatalf("View() = %q, after typing %q the menu must NOT keep showing %q", view, "/co", "/review")
	}
	if got := menuSelectedLine(view); !strings.Contains(got, "/commit") {
		t.Fatalf("selected menu line = %q, the only /commit candidate must remain selected", got)
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
	if len(m.composer.history) != 0 {
		t.Fatalf("history = %v, /compact must not enter prompt history", m.composer.history)
	}
	if m.Working() {
		t.Fatal("Working() = true, compact status must own progress")
	}
	if got := m.composer.input.Value(); got != "" {
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
		t.Fatalf("selected menu line = %q, with %q typed the menu must be open on /commit", got, "/commit")
	}

	m = typeRunes(t, m, " ")
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("selected menu line = %q, the space must close the menu (what follows are args)", got)
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
			Text: fmt.Sprintf("message-%02d", i),
		}})
	}

	m = typeRunes(t, m, "/")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/new") {
		t.Fatalf("selected menu line = %q, the built-in /new command must start selected", got)
	}

	// Local commands lead the initial menu, so move past all five to the first skill.
	for range 5 {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/commit") {
		t.Fatalf("selected menu line = %q, Down must move the marker to the /commit skill", got)
	}

	// Entering a skill preserves the fill-with-space flow.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.composer.input.Value(); got != "/commit " {
		t.Fatalf("input.Value() = %q, Enter on a skill must complete it with a space for arguments", got)
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("selected menu line = %q, completing a skill must close the menu", got)
	}
	if got := len(m.agent.(*fakeAgent).sent); got != 0 {
		t.Fatalf("SendPrompt was called %d times; Enter on a skill must only complete it", got)
	}

	// In a fresh menu, Up from /new cycles to the last item.
	mCycle := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(menuCommands, nil)
	mCycle = apply(t, mCycle, tea.WindowSizeMsg{Width: 80, Height: 24})
	mCycle = typeRunes(t, mCycle, "/")
	mCycle = apply(t, mCycle, tea.KeyMsg{Type: tea.KeyUp})
	if got := menuSelectedLine(mCycle.View()); !strings.Contains(got, "/review") && !strings.Contains(got, "/cache-stats") {
		t.Fatalf("selected menu line = %q, Up on /new must cycle to the last item", got)
	}

	// Down in the last one returns to the integrated command.
	mCycle = apply(t, mCycle, tea.KeyMsg{Type: tea.KeyDown})
	if got := menuSelectedLine(mCycle.View()); !strings.Contains(got, "/new") {
		t.Fatalf("selected menu line = %q, Down on the last item must cycle to the first (/new)", got)
	}

	// The arrows remained in the second popup: they do not write in the input.
	view := m.View()
	if !strings.Contains(view, "message-29") {
		t.Fatalf("View() = %q, with the menu open Up/Down must NOT scroll the viewport: the tail (message-29) must remain visible", view)
	}
	if got := mCycle.composer.input.Value(); got != "/" {
		t.Fatalf("input.Value() = %q, with the menu open Up/Down must NOT write to the input", got)
	}
}

func TestModel_TabAppliesSelectedCommand(t *testing.T) {
	// With the menu open, Tab applies the selection (mirror of applyCommand in command.ts): replaces the "/co" token with "/commit " with the caret after the space, ready for the args. The recompute sees the space and closes the menu. Tab with menu open DOES NOT toggle plan-mode.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m").WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/co")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/commit") {
		t.Fatalf("selected menu line = %q, /commit must be selected after typing %q", got, "/co")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	if got := m.composer.input.Value(); got != "/commit " {
		t.Fatalf("input.Value() = %q, Tab must replace the token with %q (command + space for args)", got, "/commit ")
	}
	if got := m.composer.input.Position(); got != len("/commit ") {
		t.Fatalf("input.Position() = %d, the caret must remain after the space (%d)", m.composer.input.Position(), len("/commit "))
	}
	view := m.View()
	if got := menuSelectedLine(view); got != "" {
		t.Fatalf("selected menu line = %q; applying the command must close the menu (recomputation sees the space)", got)
	}
	if !strings.Contains(view, " m ─┘") || strings.Contains(view, "· plan") {
		t.Fatalf("View() = %q; Tab with the menu open must NOT toggle plan mode: the border must continue showing model %q", view, "m")
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
		t.Fatalf("SendPrompt was called %d times; Enter with the menu open must apply the selection, NOT send", len(fake.sent))
	}
	if got := m.composer.input.Value(); got != "/commit " {
		t.Fatalf("input.Value() = %q; Enter with the menu open must apply the selection (%q)", got, "/commit ")
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("selected menu line = %q; applying the selection must close the menu", got)
	}

	// Closed menu: the second Enter sends the text as is via SendPrompt.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.sent) != 1 {
		t.Fatalf("SendPrompt was called %d times; Enter with the menu closed must send exactly once", len(fake.sent))
	}
	if got := fake.sent[0]; got.sessionID != "s1" || got.text != "/commit " {
		t.Fatalf("SendPrompt(%q, %q), expected SendPrompt(%q, %q): text is sent as-is", got.sessionID, got.text, "s1", "/commit ")
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
		t.Fatalf("View() = %q; with %q typed the menu must be open", m.View(), "/c")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("selected menu line = %q; Esc must close the popup", got)
	}
	if len(fake.stopped) != 0 {
		t.Fatalf("Stop was called %d times; Esc with the menu open must NOT stop the run", len(fake.stopped))
	}
	if got := m.composer.input.Value(); got != "/c" {
		t.Fatalf("input.Value() = %q; Esc only closes the popup: text %q must remain intact", got, "/c")
	}

	// Another rune recomputes the menu from the still valid token: it is reopened.
	m = typeRunes(t, m, "o")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/commit") {
		t.Fatalf("selected menu line = %q; typing another rune must reopen the menu over /commit", got)
	}

	// With the menu closed, the first Esc arms and the second stops the run.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc}) // closes the reopened popup
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc}) // menu cerrado: arma
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc}) // confirma
	if len(fake.stopped) != 1 || fake.stopped[0] != "s1" {
		t.Fatalf("Stop = %v; with the menu closed, two Esc presses must stop the run (Stop(%q) once)", fake.stopped, "s1")
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

	m = typeRunes(t, m, "hello @")
	view := m.View()
	for _, want := range []string{"internal/tui/model.go", "app.go", "README.md"} {
		lineWith(t, view, want)
	}
	if got := menuSelectedLine(view); !strings.Contains(got, "internal/tui/model.go") {
		t.Fatalf("selected menu line = %q; the first listed file must start selected", got)
	}

	// "mo" filters by basename: only model.go starts with "mo".
	m = typeRunes(t, m, "mo")
	view = m.View()
	lineWith(t, view, "internal/tui/model.go")
	for _, drop := range []string{"app.go", "README.md"} {
		if strings.Contains(view, drop) {
			t.Fatalf("View() = %q, after filtering by %q the menu must NOT keep showing %q", view, "mo", drop)
		}
	}
	if calls != 1 {
		t.Fatalf("listFiles was called %d times; it must be called ONCE when the token activates and cached while it remains active", calls)
	}

	// With listFiles nil the menu simply does not open.
	m2 := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, nil)
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 = typeRunes(t, m2, "hello @")
	if got := menuSelectedLine(m2.View()); got != "" {
		t.Fatalf("selected menu line = %q; without listFiles the @ menu must not open", got)
	}

	// With listFiles failing the menu shows the error without blocking the input.
	m3 := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return nil, fmt.Errorf("rg unavailable")
	})
	m3 = apply(t, m3, tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 = typeRunes(t, m3, "hello @")
	if got := menuSelectedLine(m3.View()); !strings.Contains(got, "Could not list files: rg unavailable") {
		t.Fatalf("selected menu line = %q; with listFiles failing the @ menu must show the error", got)
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
		t.Fatalf("selected menu line = %q; an @ inside a word (email) must NOT open the @ menu", got)
	}
}

func TestModel_TabAppliesSelectedMention(t *testing.T) {
	// With the @-menu open, Tab replaces the token with "@<path> " while preserving the text around it (mirror of applyMention: text[:start] + "@<path> " + text[end:]) and leaves the caret after the space. Recomputation closes the menu.
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go", "app.go", "README.md"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "hello @mo")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "internal/tui/model.go") {
		t.Fatalf("selected menu line = %q; with %q typed, model.go must be selected", got, "@mo")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	want := "hello @internal/tui/model.go "
	if got := m.composer.input.Value(); got != want {
		t.Fatalf("input.Value() = %q; Tab must replace the token with the mention while preserving surrounding text (%q)", got, want)
	}
	if got := m.composer.input.Position(); got != len([]rune(want)) {
		t.Fatalf("input.Position() = %d; the caret must remain after the space (%d)", m.composer.input.Position(), len([]rune(want)))
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("selected menu line = %q; applying the mention must close the menu", got)
	}
}

func TestModel_SlashOpensCommandMenu(t *testing.T) {
	// With commands configured via WithCompletions, typing "/" as the first character in the composer opens a menu popup above the box: one line per command with "/<name>" and its description. The first item starts selected and is marked with the prefix "❯" (those not selected have two prefix spaces).
	cmds := withMenuBuiltins(
		command.Command{Name: "commit", Description: "generate a commit"},
		command.Command{Name: "review", Description: "review the diff"},
	)
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(cmds, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})

	view := m.View()
	commitLine := lineWith(t, view, "/commit")
	if !strings.Contains(commitLine, "generate a commit") {
		t.Fatalf("/commit line = %q; the menu must show description %q beside the command", commitLine, "generate a commit")
	}
	lineWith(t, view, "/review")
	newLine := lineWith(t, view, "/new")
	if plain := ansi.Strip(newLine); !strings.Contains(plain, "│ ❯ ") {
		t.Fatalf("/new line without ANSI = %q; the built-in command must have its selector inside the rectangle as %q", plain, "│ ❯ ")
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
		command.Command{Name: "renew", Description: "skill with fuzzy match"},
	), nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/new") {
		t.Fatalf("selected menu line = %q; /new must be the built-in command selected above skills", got)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.sessionID; got != "s2" {
		t.Fatalf("sessionID = %q; Enter on /new must activate new session %q", got, "s2")
	}
	if got := m.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q; executing /new from the menu must clear the composer without leaving a space", got)
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("selected menu line = %q; executing /new must close the command menu", got)
	}
	if got := fake.sent; len(got) != 1 || got[0].text != "/new" {
		t.Fatalf("SendPrompt calls = %#v; Enter on /new must execute the reserved command exactly once", got)
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
		t.Fatalf("View() before /new = %q; it must show usage from the previous session", view)
	}

	m = typeRunes(t, m, "/new")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if view := ansi.Strip(m.View()); strings.Contains(view, "↑") || strings.Contains(view, "↓") {
		t.Fatalf("View() after /new = %q; the new session must not inherit input or output tokens", view)
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
		t.Fatalf("usage state after /new = usage:%+v live:%v bytes:%d/%d/%d; it must start clean", m.usage, m.liveUsage, m.outputBytes, m.reasoningBytes, m.toolInputBytes)
	}
}

func TestModel_ExactNewEnterBeatsFuzzySkillSelection(t *testing.T) {
	// Even if a fuzzy skill is selected, typing /new exactly and pressing Enter should execute the reservation, not complete the skill.
	fake := &fakeAgent{newSessionID: "s2"}
	m := NewModel(fake, "s1", nil).WithCompletions(withMenuBuiltins(
		command.Command{Name: "renew", Description: "skill with fuzzy match"},
	), nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/new")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got := m.sessionID; got != "s2" {
		t.Fatalf("sessionID = %q, Enter with /new typed must activate the new session %q even with a fuzzy skill", got, "s2")
	}
	if got := m.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, Enter with /new typed must execute it, not complete a skill", got)
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("selected menu line = %q, Enter with /new typed must close the command menu", got)
	}
}

func TestModel_NewWithTrailingSpaceKeepsComposerForArguments(t *testing.T) {
	// The space closes the menu and disables only the reserved command: the text is left intact so that the user can continue typing arguments.
	fake := &fakeAgent{newSessionID: "s2"}
	m := NewModel(fake, "s1", nil).WithCompletions([]command.Command{
		{Name: "renew", Description: "skill with fuzzy match"},
	}, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/new ")

	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("selected menu line = %q, /new with trailing space must close the menu", got)
	}
	if got := m.composer.input.Value(); got != "/new " {
		t.Fatalf("input.Value() = %q, /new with trailing space must be preserved for arguments", got)
	}
	if got := m.sessionID; got != "s1" {
		t.Fatalf("sessionID = %q, typing /new with trailing space must not execute the reserved command", got)
	}
	if got := len(fake.sent); got != 0 {
		t.Fatalf("SendPrompt was called %d times; typing /new with trailing space must not execute the reserved command", got)
	}
}

func TestModel_MenuLinesTruncateToTerminalWidth(t *testing.T) {
	// A menu line wider than the terminal would be wrapped by the terminal with two real lines, but reservedLines only discounts ONE per item: the layout is broken. The menu should truncate each line to the width of the terminal, as the rest of the view already does (the transcript wraps with ansi.Wrap, the textinput scrolls horizontally).
	longPath := strings.Repeat("sub/", 30) + "file-with-long-name.go"
	listFiles := func() ([]string, error) {
		return []string{longPath}, nil
	}
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, listFiles)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})

	view := m.View()
	lineWith(t, view, "sub/") // the menu line remains present, truncated
	assertNoLineWiderThan(t, view, 40)
}

// Animated indicator contract: while a run is in progress the status line shows a spinner glyph followed by "working"; the static prefix "..." disappears. Starting the run (Enter with text) returns a non-nil tea.Cmd that pumps the animation: executing it produces a message that, applied to Update, advances the spinner glyph (the status line changes) and returns in turn the next cmd of the loop.
func TestModel_WorkingIndicatorAnimatesOnTicks(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// The user types "hello" and presses Enter; The Enter cmd is preserved (the apply helper discards it and here is the heart of the contract).
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}

	// a) Starting the run must return the cmd that pumps the animation: without cmd no one produces ticks and the spinner remains frozen.
	if cmd == nil {
		t.Fatalf("Update(Enter) returned nil cmd; starting the run must return the animation-pumping cmd: without it the spinner remains frozen")
	}

	// b) The status line carries concrete microcopy without the old static
	// marker "...working": now the prefix is the animated glyph.
	view := m.View()
	if !strings.Contains(view, "Checking context") {
		t.Fatalf("View() = %q, the status line with %q must be visible while the run is in progress", view, "Checking context")
	}
	if strings.Contains(view, "... working") {
		t.Fatalf("View() = %q, MUST NOT contain the static marker %q: the fixed prefix is replaced by the spinner glyph", view, "... working")
	}

	// c) Running the cmd produces the tick message; applying it to Update should advance the spinner glyph: the status line changes.
	before := lineWith(t, view, "Checking context")
	msg := cmd()
	if msg == nil {
		t.Fatalf("cmd() = nil, the animation cmd must produce a message applicable to Update")
	}
	updated, tickCmd := m.Update(msg)
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	after := lineWith(t, m.View(), "Checking context")
	if after == before {
		t.Fatalf("status line after tick = %q, identical to the previous one: the tick must advance the spinner frame, an identical line means frozen animation", after)
	}

	// d) The loop continues: the Update of the tick must schedule the next tick.
	if tickCmd == nil {
		t.Fatalf("Update(tick) returned nil cmd; the animation loop must schedule the next tick")
	}
}

// TRIANGULATE: the tick loop must die when the run ends. A tick case that always reschedules without looking at working leaves the TUI waking up forever: an old tick that arrives AFTER RunDoneMsg should not reschedule the loop (cmd nil) or revive the status line.
func TestModel_SpinnerTickDiesAfterRunDone(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// The run starts and there is one tick left in flight (the cmd has already produced its msg).
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(Enter) returned nil cmd; starting the run must return the animation-pumping cmd")
	}
	msg := cmd()

	// The bullfight ends; Only then does the old tick arrive.
	m = apply(t, m, activeRunDone(m, ""))
	updated, tickCmd := m.Update(msg)
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}

	if tickCmd != nil {
		t.Fatalf("Update(tick) after RunDoneMsg returned a non-nil cmd; the animation loop must NOT reschedule after the run ends")
	}
	if got := m.View(); strings.Contains(got, "working") {
		t.Fatalf("View() = %q, after RunDoneMsg the old tick must NOT revive the status line %q", got, "working")
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
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update('y') returned nil cmd; accepting the plan starts the run and must return the animation-pumping cmd")
	}

	before := lineWith(t, m.View(), "Checking context")
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
		t.Fatalf("cmd() = nil, the animation cmd must produce a message applicable to Update")
	}
	m = apply(t, m, msg)
	after := lineWith(t, m.View(), "Checking context")
	if after == before {
		t.Fatalf("status line after tick = %q, identical to the previous one: the plan-path tick must advance the spinner frame", after)
	}
}

// TRIANGULATE: the animation is not single use. A poor implementation with a loop state that does not restart (starts only on the first run) leaves the spinner dead on the second: after RunDoneMsg, a new Enter must return the cmd of the animation and its tick must advance the glyph.
func TestModel_SecondRunRestartsSpinner(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// First run: Enter starts the loop and RunDoneMsg turns it off.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	updated, cmd1 := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	if cmd1 == nil {
		t.Fatalf("Update(Enter) returned nil cmd; starting the first run must return the animation-pumping cmd")
	}
	m = apply(t, m, activeRunDone(m, ""))

	// Second run: the loop must be reborn with the new Enter.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("otra vez")})
	updated, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	if cmd2 == nil {
		t.Fatalf("Update(Enter) for the second run returned nil cmd; every run must restart the animation")
	}

	before := lineWith(t, m.View(), "Checking context")
	msg := cmd2()
	if msg == nil {
		t.Fatalf("cmd() = nil, the second-run animation cmd must produce a message applicable to Update")
	}
	m = apply(t, m, msg)
	after := lineWith(t, m.View(), "Checking context")
	if after == before {
		t.Fatalf("status line after tick = %q, identical to the previous one: the second-run tick must advance the spinner frame", after)
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
	if got := m.composer.input.Value(); got != "segundo" {
		t.Fatalf("input.Value() = %q, the up arrow must recall the last sent prompt (%q)", got, "segundo")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.composer.input.Value(); got != "primero" {
		t.Fatalf("input.Value() = %q, the second up arrow must go back to the previous prompt (%q)", got, "primero")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.composer.input.Value(); got != "primero" {
		t.Fatalf("input.Value() = %q, at the top of history another up arrow stays at %q: it does not cycle or empty", got, "primero")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.composer.input.Value(); got != "segundo" {
		t.Fatalf("input.Value() = %q, the down arrow must undo forward and return to the most recent prompt (%q)", got, "segundo")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, past the most recent prompt the down arrow must restore the input from before navigation (empty after Enter)", got)
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
	if got := m.composer.input.Value(); got != "borrador" {
		t.Fatalf("input.Value() = %q, with typed text the down arrow must not open or replace from history", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.composer.input.Value(); got != "borrador" {
		t.Fatalf("input.Value() = %q, with typed text the up arrow must not open or replace from history", got)
	}

	// Emptying the composer enables navigation.
	m.composer.input.SetValue("")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.composer.input.Value(); got != "primero" {
		t.Fatalf("input.Value() = %q, with an empty composer the up arrow must recall %q", got, "primero")
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.composer.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, advancing past the most recent prompt must leave the composer empty", got)
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
	if got := m.composer.input.Value(); got != "prompt-003" {
		t.Fatalf("input.Value() = %q, after 102 sends history must retain only the 100 most recent prompts and stop at %q", got, "prompt-003")
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
		t.Fatalf("selected menu line = %q, with %q typed the menu must be open on /new", got, "/")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.composer.input.Value(); got != "/" {
		t.Fatalf("input.Value() = %q, with the menu open the up arrow must NOT touch the input: menu selection handles navigation", got)
	}
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/review") && !strings.Contains(got, "/cache-stats") {
		t.Fatalf("selected menu line = %q, with the menu open the up arrow must move the selection cyclically", got)
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
		t.Fatalf("SendPlanPrompt was called %d times; Enter in plan mode must send the prompt exactly once through the plan path", len(fake.planSent))
	}
	m = apply(t, m, activeRunDone(m, ""))

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.composer.input.Value(); got != "plan-uno" {
		t.Fatalf("input.Value() = %q, the up arrow must recall the sent plan prompt (%q): plan prompts are also stored in history", got, "plan-uno")
	}
}

// TRIANGULATE: Enter with empty input does not send (covered separately) and should not stack anything either. Take down an implementation that stacks all submits and leaves a "" sneaking into the history.
func TestModel_EmptySubmitDoesNotPolluteHistory(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("only-one")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// Enter with empty input: does not send and should not touch the history.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.composer.input.Value(); got != "only-one" {
		t.Fatalf("input.Value() = %q, the first up arrow must recall the only sent prompt (%q), without an empty submit in history", got, "only-one")
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.composer.input.Value(); got != "only-one" {
		t.Fatalf("input.Value() = %q, at the top of history the up arrow stays at %q: the empty submit must not have been stored", got, "only-one")
	}
}

// Smooth streaming contract (parity with desktop frontend, frontend/src/lib/reveal.ts): assistant deltas ACCUMULATE in the input but the view does NOT show them in full immediately. A reveal tick loop (revealTickMsg, analogous to spinner.TickMsg) advances the revealed text: each tick reveals ~max(base, ceil(backlog/8)) runes, with a base of ~6-7 runes per tick (desktop pace: 1 char every 5ms at ~33ms ticks). With enough ticks the full text becomes visible.
func TestModel_SmoothRevealsAssistantTextOnTicks(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	text := strings.Repeat("word ", 40) + "final-del-texto"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	// (a) The delta does NOT appear complete at once: the tail of the text is not yet revealed right after accumulating the delta.
	if got := m.View(); strings.Contains(got, "final-del-texto") {
		t.Fatalf("View() = %q, MUST NOT contain %q immediately after the delta: text is revealed progressively by reveal ticks", got, "final-del-texto")
	}

	// (b) A reveal tick advances the visible text: a prefix is ​​already visible, but the tail is not yet (progressive reveal, not all at once).
	m = apply(t, m, revealTickMsg{})
	view := m.View()
	if !strings.Contains(view, "word") {
		t.Fatalf("View() = %q, must contain %q after a reveal tick: each tick reveals a portion of accumulated text", view, "word")
	}
	if strings.Contains(view, "final-del-texto") {
		t.Fatalf("View() = %q, MUST NOT contain %q after a single tick: one tick reveals a portion, not the entire text", view, "final-del-texto")
	}

	// (c) With enough ticks the full text becomes visible.
	for i := 0; i < 200; i++ {
		m = apply(t, m, revealTickMsg{})
		if strings.Contains(m.View(), "final-del-texto") {
			break
		}
	}
	if got := m.View(); !strings.Contains(got, "final-del-texto") {
		t.Fatalf("View() = %q, must contain %q after enough reveal ticks: the loop eventually shows the full text", got, "final-del-texto")
	}
}

// TRIANGULATE: the catch-up limits the latency. With pure constant pace (~7 runes per tick) a delta of ~4000 runes would take ~570 ticks (~19 seconds at 33ms) to drain: the visible text would be eternally behind a fast model. The step proportional to the backlog (ceil(backlog/8)) must leave the full text visible in a limited number of ticks.
func TestModel_RevealCatchUpDrainsHugeDeltaInBoundedTicks(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// ~4011 runes in a single delta (fast model dumping text at once).
	text := strings.Repeat("word ", 500) + "fin-catchup"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	// The first tick doesn't reveal everything: the catch-up speeds up the pace, it doesn't turn it into an instant reveal (that would kill the animation).
	m = apply(t, m, revealTickMsg{})
	if got := m.View(); strings.Contains(got, "fin-catchup") {
		t.Fatalf("View() = %q, MUST NOT contain %q after a single tick of a ~4000-rune delta: catch-up limits latency without becoming instant", got, "fin-catchup")
	}

	// At most 64 ticks in total (~2 seconds at 33ms) leave the full text visible: the proportional step geometrically drains the backlog.
	for i := 0; i < 63 && m.hasBacklog(); i++ {
		m = apply(t, m, revealTickMsg{})
	}
	if got := m.View(); !strings.Contains(got, "fin-catchup") {
		t.Fatalf("View() = %q, must contain %q after 64 ticks: proportional catch-up must drain a huge delta in bounded ticks", got, "fin-catchup")
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
		t.Fatalf("View() without ANSI = %q, must render the revealed Markdown during streaming", got)
	}

	// The shift is closed with a pending backlog.
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})

	view := ansi.Strip(m.View())
	if strings.Contains(view, "fin-drenado") {
		t.Fatalf("View() = %q, MUST NOT contain %q immediately after StepEnded: closing the turn must not reveal the pending queue at once", view, "fin-drenado")
	}
	if strings.Contains(view, "**") || !strings.Contains(view, "fuerte") {
		t.Fatalf("View() without ANSI = %q, must preserve Markdown for the revealed prefix after StepEnded", view)
	}

	// Once the backlog is drained, the closed block is rendered as markdown.
	m = drainReveal(t, m)
	view = ansi.Strip(m.View())
	if strings.Contains(view, "**") {
		t.Fatalf("View() = %q, MUST NOT contain %q after draining: closed and drained Markdown must render emphasis", view, "**")
	}
	if !strings.Contains(view, "fuerte") {
		t.Fatalf("View() = %q, must contain %q: rendering Markdown must not lose content", view, "fuerte")
	}
	if !strings.Contains(view, "fin-drenado") {
		t.Fatalf("View() = %q, must contain %q: draining the backlog must show the complete rendered text", view, "fin-drenado")
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
			t.Fatalf("reveal backlog was not drained after 1000 ticks")
		}
		m = apply(t, m, revealTickMsg{})
		view := m.View()
		if !utf8.ValidString(view) {
			t.Fatalf("View() = %q after tick %d is not valid UTF-8: reveal cuts must use runes, not split multibyte characters", view, ticks)
		}
		if strings.ContainsRune(view, '�') {
			t.Fatalf("View() = %q after tick %d contains replacement character U+FFFD: a multibyte character was split by a byte cut", view, ticks)
		}
	}
	// The drain must have gone through intermediate cuts: an instant reveal would pass the assertions above without exercising anything.
	if ticks < 2 {
		t.Fatalf("backlog (%d runes) drained in %d tick(s), it must drain over multiple ticks to exercise intermediate cuts", utf8.RuneCountInString(text), ticks)
	}
	plain := ansi.Strip(m.View())
	if got, want := strings.Count(plain, "日本語テキスト"), 8; got != want {
		t.Fatalf("View() without ANSI = %q, contains %d occurrences of Japanese text, expected %d after draining", plain, got, want)
	}
	if got, want := strings.Count(plain, "🚀🚀🚀"), 8; got != want {
		t.Fatalf("View() without ANSI = %q, contains %d emoji groups; expected %d after draining", plain, got, want)
	}
}

// TRIANGULATE (mirror of the spinner life cycle): the reveal tick loop is born with the first delta that leaves the backlog, it is not duplicated with subsequent deltas, it is rearmed while there is backlog, it dies when drained and is reborn with a new delta. With nil event channel the bomb is nil and the cmd returned by Update is ONLY the reveal tick: each transition is direct assertable.
func TestModel_RevealTickLoopLifecycle(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// 200 runes per delta: no single tick drains the entire backlog.
	delta := strings.Repeat("word ", 25)
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})

	// a) The first delta with backlog starts the loop: the cmd produces the tick.
	updated, cmd := m.Update(EventMsg{Kind: session.KindTextDelta, Text: delta})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(delta) returned nil cmd; the first delta with backlog must start the reveal loop")
	}
	msg := cmd()
	if _, ok := msg.(revealTickMsg); !ok {
		t.Fatalf("cmd() = %T, the loop-start command must produce a revealTickMsg", msg)
	}

	// b) A second delta with the loop already running DOES NOT double the chain of ticks: two chains would double the rhythm of the reveal.
	updated, cmd = m.Update(EventMsg{Kind: session.KindTextDelta, Text: delta})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	if cmd != nil {
		t.Fatalf("Update(delta) with the reveal loop running returned a non-nil cmd; a second delta must not start another tick chain")
	}

	// c) A tick with backlog remaining is reset: the cmd produces the next tick.
	updated, cmd = m.Update(revealTickMsg{})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(tick) with remaining backlog returned nil cmd; the loop must schedule the next tick while text remains unrevealed")
	}
	msg = cmd()
	if _, ok := msg.(revealTickMsg); !ok {
		t.Fatalf("cmd() = %T, the loop-rearm command must produce the next revealTickMsg", msg)
	}

	// d) With the backlog drained the next tick is not rescheduled: the loop dies.
	m = drainReveal(t, m)
	updated, cmd = m.Update(revealTickMsg{})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	if cmd != nil {
		t.Fatalf("Update(tick) without backlog returned a non-nil cmd; the reveal loop must end when drained")
	}

	// e) A new delta after the drain restarts the loop.
	updated, cmd = m.Update(EventMsg{Kind: session.KindTextDelta, Text: delta})
	if _, ok = updated.(Model); !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(delta) after draining returned nil cmd; a new delta must restart the reveal loop")
	}
	msg = cmd()
	if _, ok := msg.(revealTickMsg); !ok {
		t.Fatalf("cmd() = %T, the loop-restart command must produce a revealTickMsg", msg)
	}
}

// TRIANGULATE: the reveal loop is NOT tied to working like the spinner loop. An implementation that copies the spinner.TickMsg case cut (!working => cmd nil) leaves the text frozen half-revealed when the run ends before draining the backlog: ticks after RunDoneMsg should continue revealing until drained.
func TestModel_RevealSurvivesRunDone(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	// Actual run in progress: working on via Enter with text.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	text := strings.Repeat("word ", 40) + "after-run-done"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	// The run ends with a pending backlog: working is turned off but the text queue remains unrevealed.
	m = apply(t, m, activeRunDone(m, ""))
	if m.Working() {
		t.Fatalf("Working() = true, RunDoneMsg must turn off working state")
	}
	if got := m.View(); strings.Contains(got, "after-run-done") {
		t.Fatalf("View() = %q, RunDoneMsg MUST NOT reveal the queue at once; reveal pacing continues after the run", got)
	}

	// The tick after the end of the run continues advancing and rescheduling.
	updated, cmd := m.Update(revealTickMsg{})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, expected tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(tick) after RunDoneMsg returned nil cmd with pending backlog; the reveal loop must continue draining remaining text")
	}

	m = drainReveal(t, m)
	if got := m.View(); !strings.Contains(got, "after-run-done") {
		t.Fatalf("View() = %q, must contain %q after draining: post-RunDoneMsg ticks must show the complete text", got, "after-run-done")
	}
}

// Thinking toggle contract (Shift+Tab key, see handleKey and toggleThinking): a settled thought (closed and with reveal drained) collapses to the summary line "● Thought for <dur>"; Shift+Tab expands it to the full text and a second Shift+Tab collapses it again. The hint " ⇧Tab" accompanies the collapsed summary to reveal the key.
func TestModel_ShiftTabExpandsAndCollapsesSettledThinking(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	text := "reason-1\nreason-2\nreason-3"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)

	// Seated: Collapsed by default.
	view := m.View()
	if !strings.Contains(view, "● Thought") {
		t.Fatalf("View() = %q, the settled thought must collapse to %q", view, "● Thought")
	}
	if !strings.Contains(view, " ⇧Tab") {
		t.Fatalf("View() = %q, the collapsed summary must include hint %q to discover the toggle", view, " ⇧Tab")
	}
	if strings.Contains(view, "reason-2") {
		t.Fatalf("View() = %q, the collapsed thought must NOT show the full text", view)
	}

	// Shift+Tab expande.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	for _, want := range []string{"● Thought", "reason-1", "reason-2", "reason-3"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, after Shift+Tab the expanded thought must show %q", view, want)
		}
	}

	// Shift+Tab colapsa de nuevo.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	if !strings.Contains(view, "● Thought") {
		t.Fatalf("View() = %q, the second Shift+Tab must return to collapsed summary %q", view, "● Thought")
	}
	if strings.Contains(view, "reason-2") {
		t.Fatalf("View() = %q, the second Shift+Tab must collapse the text again", view)
	}
}

func TestModel_SettledThinkingSummaryAlignsWithAssistantContent(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "assistant-response"})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "assistant-response"},
	})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "settled-thought"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "settled-thought"})
	m = drainReveal(t, m)

	assistantLine := ansi.Strip(lineWith(t, m.View(), "assistant-response"))
	thinkingLine := ansi.Strip(lineWith(t, m.View(), "● Thought"))
	assistantIndent := assistantLine[:len(assistantLine)-len(strings.TrimLeft(assistantLine, " "))]

	if got, want := assistantIndent, "  "; got != want {
		t.Fatalf("assistant content prefix = %q, want %q", got, want)
	}
	if !strings.HasPrefix(thinkingLine, assistantIndent) {
		t.Fatalf("thinking summary line = %q, must align with assistant content %q", thinkingLine, assistantLine)
	}
}

func TestModel_LiveThinkingHeaderAlignsWithAssistantContent(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "assistant-response"})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "assistant-response"},
	})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "live-thought"})
	m = drainReveal(t, m)

	assistantLine := ansi.Strip(lineWith(t, m.View(), "assistant-response"))
	thinkingLine := ansi.Strip(lineWith(t, m.View(), "● Thinking…"))
	assistantIndent := assistantLine[:len(assistantLine)-len(strings.TrimLeft(assistantLine, " "))]

	if got, want := assistantIndent, "  "; got != want {
		t.Fatalf("assistant content prefix = %q, want %q", got, want)
	}
	if !strings.HasPrefix(thinkingLine, assistantIndent) {
		t.Fatalf("live thinking header line = %q, must align with assistant content %q", thinkingLine, assistantLine)
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
	if !strings.Contains(view, "● Thinking…") {
		t.Fatalf("View() = %q, live thinking must show %q", view, "● Thinking…")
	}
	if strings.Contains(view, "● Thought") {
		t.Fatalf("View() = %q, live thinking must NOT show collapsed summary %q", view, "● Thought")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	if strings.Contains(view, "● Thought") {
		t.Fatalf("View() = %q, Shift+Tab during live streaming must not collapse yet", view)
	}
	if strings.Contains(view, "vivo-1") {
		t.Fatalf("View() = %q, Shift+Tab during live streaming must not expand the entire text", view)
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
	if n := strings.Count(view, "● Thought"); n != 2 {
		t.Fatalf("View() = %q, two settled thoughts must collapse to two summaries %q (n=%d)", view, "● Thought", n)
	}
	if strings.Contains(view, "primero-a") || strings.Contains(view, "segundo-a") {
		t.Fatalf("View() = %q, neither collapsed thought must show text", view)
	}

	// A Shift+Tab expands both.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	if !strings.Contains(view, "primero-a") || !strings.Contains(view, "segundo-a") {
		t.Fatalf("View() = %q, one Shift+Tab must expand BOTH thoughts", view)
	}
	if n := strings.Count(view, "● Thought"); n != 2 {
		t.Fatalf("View() = %q, after expanding there are still two header summaries (n=%d)", view, n)
	}

	// A second Shift+Tab collapses both.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	if strings.Contains(view, "primero-a") || strings.Contains(view, "segundo-a") {
		t.Fatalf("View() = %q, the second Shift+Tab must collapse BOTH", view)
	}
}

// Click toggle contract (see toggleThinkingAt and the tea.MouseMsg case of Update): a left click on the summary line of a settled thought expands it to the full text, just like Shift+Tab but on the specific block under the cursor. The click maps to the entry via entryLines, so the clicked row should fall on the summary line.
func TestModel_ClickExpandsSettledThinking(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	text := "reason-1\nreason-2\nreason-3"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)

	// Locate the "● Thought" summary row in the viewport content.
	lines := m.entryLines()
	summaryRow := -1
	for i, l := range lines {
		if strings.Contains(l.line, "● Thought") {
			summaryRow = i
			break
		}
	}
	if summaryRow < 0 {
		t.Fatalf("entryLines() does not contain summary %q: %v", "● Thought", lines)
	}
	// The row on the screen is the one with the content minus the visible scrolling, plus the row of the top bar that moves the body one row down.
	clickY := topBarHeight + summaryRow - m.viewport.YOffset
	if clickY < topBarHeight {
		t.Fatalf("summaryRow=%d YOffset=%d, the summary is not visible to click", summaryRow, m.viewport.YOffset)
	}

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: clickY})
	view := m.View()
	for _, want := range []string{"● Thought", "reason-1", "reason-2", "reason-3"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, clicking the summary must expand the thought and show %q", view, want)
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

	text := "reason-1\nreason-2"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)
	target := len(m.entries) - 1

	// The row is searched on the real screen: the short transcript is shown from the top (without scrolling) and the viewport opens the view, so the row Y of the screen is the absolute line of the content.
	if m.viewport.YOffset != 0 {
		t.Fatalf("viewport.YOffset = %d, want 0: the short transcript is shown from the top", m.viewport.YOffset)
	}
	summaryY := lineIndexWith(t, ansi.Strip(m.View()), "● Thought")

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: summaryY})
	if !m.entries[target].expanded {
		t.Fatal("clicking the visible summary row must expand the thought despite the compact tool group above")
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"reason-1", "reason-2"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() without ANSI = %q, the expanded thought must show %q", view, want)
		}
	}
}

// Contract: a click on the text of an ALREADY expanded thought collapses it again (toggle back and forth on the same block).
func TestModel_ClickCollapsesExpandedThinking(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	text := "reason-1\nreason-2"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)

	// Expand first with Shift+Tab.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := m.View(); !strings.Contains(got, "reason-1") {
		t.Fatalf("View() = %q, precondition: Shift+Tab must expand", got)
	}

	// Click on the first line of the expanded text (the "● Thought" header).
	lines := m.entryLines()
	headerRow := -1
	for i, l := range lines {
		if strings.Contains(l.line, "● Thought") {
			headerRow = i
			break
		}
	}
	clickY := topBarHeight + headerRow - m.viewport.YOffset
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: clickY})
	view := m.View()
	if strings.Contains(view, "reason-1") {
		t.Fatalf("View() = %q, clicking the expanded block must collapse it", view)
	}
	if !strings.Contains(view, "● Thought") {
		t.Fatalf("View() = %q, after collapsing the summary %q must return", view, "● Thought")
	}
}

// Contract: a left click on a line that is NOT a settled thought (an empty line of separation or the text of a user message) is inert: it does not expand anything or change the view.
func TestModel_ClickOutsideThinkingIsInert(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hello"}})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "thought-a\nthought-b"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "thought-a\nthought-b"})
	m = drainReveal(t, m)

	before := m.View()
	// Click on the user message line (first entry, row 0).
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: 0})
	if got := m.View(); got != before {
		t.Fatalf("View() changed after clicking outside the thought:\nbefore = %q\nafter = %q; clicks only toggle settled thought blocks", before, got)
	}
	if strings.Contains(m.View(), "thought-a") {
		t.Fatalf("View() = %q, clicking outside the thought must not expand it", m.View())
	}
}

func TestModel_KeyRunesBatch_NormalTextInsertsIntoComposer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello world")})

	if got, want := m.composer.input.Value(), "hello world"; got != want {
		t.Fatalf("input.Value() = %q, want %q: normal text batch must preserve every rune in order", got, want)
	}
}

func TestModel_ComposerCursorStartsBlinking(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m.composer.input.Cursor.BlinkSpeed = time.Millisecond

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() = nil, want composer cursor blink command")
	}
	if m.composer.input.Cursor.Blink {
		t.Fatal("composer cursor starts hidden, want it visible while chat is focused")
	}
	blinkMsg := cmd()
	updated, next := m.Update(blinkMsg)
	m = updated.(Model)
	if next == nil {
		t.Fatal("initial cursor blink message did not schedule the next blink")
	}
	m = apply(t, m, next())
	if !m.composer.input.Cursor.Blink {
		t.Fatal("composer cursor did not toggle after its blink interval")
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
			if m.composer.input.Focused() {
				t.Fatal("composer remains focused while another input gate owns the keyboard")
			}

			m = apply(t, m, test.resolve)
			if !m.composer.input.Focused() {
				t.Fatal("composer remains blurred after the input gate is resolved")
			}
		})
	}
}

func TestModel_ComposerCursorFollowsTerminalFocus(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, tea.BlurMsg{})
	if m.composer.input.Focused() {
		t.Fatal("composer remains focused after the terminal window loses focus")
	}

	m = apply(t, m, tea.FocusMsg{})
	if !m.composer.input.Focused() {
		t.Fatal("composer remains blurred after the terminal window regains focus")
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
		t.Fatalf("store.AppendEvent(s1, Session.Cwd) = %v, expected nil", err)
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
		t.Fatalf("store.Sessions() = %v, expected nil", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("store.Sessions() contains %d sessions, expected 2", len(sessions))
	}
	newSessionID := ""
	for _, s := range sessions {
		if s.ID != "s1" {
			newSessionID = s.ID
			break
		}
	}
	if newSessionID == "" {
		t.Fatal("the session created by /new was not found")
	}
	m = typeRunes(t, m, "continue here")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	_, done := collectUntilRunDone(t, eng.Events(), 10*time.Second, nil)
	if done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, expected a clean run", done.Err)
	}
	messages, err := store.Messages(context.Background(), newSessionID, 0)
	if err != nil {
		t.Fatalf("store.Messages(%s, 0) = %v, expected nil", newSessionID, err)
	}
	if len(messages) != 1 || messages[0].Text != "continue here" {
		t.Fatalf("messages for %s = %+v, expected the next prompt to be sent to the new session", newSessionID, messages)
	}
}

// nextMsg and collectUntilRunDone drain the engine channel for Model+Engine integration tests (the engine has its own copies for its tests).
func nextMsg(t *testing.T, ch <-chan tea.Msg, timeout time.Duration) tea.Msg {
	t.Helper()
	select {
	case <-time.After(timeout):
		t.Fatalf("timeout of %v waiting for the engine's next message", timeout)
		return nil
	case msg, ok := <-ch:
		if !ok {
			t.Fatal("engine channel closed unexpectedly")
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
			t.Fatalf("unexpected message on engine channel: %T", m)
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
		"Permission required", "Write notes/plan.txt", "line one", "line two", "Deny", "Allow once",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, want %q", view, want)
		}
	}
	panel := ansi.Strip(m.permissionPanelView())
	for _, unwanted := range []string{
		"write request", "Requested by", "Working directory", `"content"`,
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
		Input: json.RawMessage(`{"input":"[tracked.txt#abc123]\nPUT 1.=1:\n+new line"}`),
	})

	view := ansi.Strip(m.View())
	for _, want := range []string{
		"Permission required", "Edit [tracked.txt#abc123]", "+new line", "Deny", "Allow once",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, want %q", view, want)
		}
	}
	if panel := ansi.Strip(m.permissionPanelView()); strings.Contains(panel, `"input"`) {
		t.Fatalf("permissionPanelView() = %q, edit permission panel must not dump raw JSON", panel)
	}
}

func TestModelApplyPatchAliasUsesStableEditPresentationAcrossMaterializations(t *testing.T) {
	edit := tool.NewEditTool(t.TempDir(), hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	edit.Mode = editmode.Hashline
	registry := tool.NewRegistry(tool.NewOutputStore(1024), edit)
	agent := &fakeAgent{tools: registry}
	m := NewModel(agent, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 90, Height: 40})
	input := json.RawMessage(`{"input":"*** Begin Patch\n*** Update File: src/a.go\n@@\n-old\n+new\n*** End Patch"}`)
	// A historical event must render before any current turn is materialized.
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "perm", ToolName: "apply_patch", Input: input})
	// Alternating current turns must not change presentation of that durable name.
	for _, mode := range []editmode.Mode{editmode.ApplyPatch, editmode.Hashline, editmode.Replace} {
		edit.Mode = mode
		if materialized := registry.Materialize(tool.Permissions{"edit": true}); materialized.Err != nil {
			t.Fatal(materialized.Err)
		}
	}
	permissionView := ansi.Strip(m.View())
	for _, want := range []string{"Permission required", "Edit", "a.go", "*** Update File: src/a.go", "-old"} {
		if !strings.Contains(permissionView, want) {
			t.Fatalf("permission view lacks %q: %s", want, permissionView)
		}
	}
	if strings.Contains(permissionView, `{"input"`) {
		t.Fatalf("permission used raw generic input: %s", permissionView)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "run", ToolName: "apply_patch", Input: input})
	previewFiles := []tool.FileResult{{Path: "src/a.go", Operation: contract.FileUpdated, OldText: "old\n", NewText: "new\n", Diff: "--- a/src/a.go\n+++ b/src/a.go\n@@ -1 +1 @@\n-old\n+new"}}
	m = apply(t, m, PreviewMsg(tool.PreviewEvent{SessionID: "s1", CallID: "run", Preview: tool.Preview{Digest: "p", Pending: true, Files: previewFiles}}))
	runningView := ansi.Strip(m.View())
	if !strings.Contains(runningView, "src/a.go") || !strings.Contains(runningView, "1 + new") {
		t.Fatalf("running alias did not render edit card: %s", runningView)
	}
	encoded, _ := json.Marshal(previewFiles)
	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "run", ToolName: "apply_patch", Diff: previewFiles[0].Diff, Attrs: map[string]string{"tool.files": string(encoded)}})
	finalView := ansi.Strip(m.View())
	if !strings.Contains(finalView, "src/a.go") || !strings.Contains(finalView, "1 + new") {
		t.Fatalf("settled alias did not render per-file edit card: %s", finalView)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "fail", ToolName: "apply_patch", Input: input})
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "fail", ToolName: "apply_patch", Error: "hunk failed"})
	if failureView := ansi.Strip(m.View()); !strings.Contains(failureView, "Edit") || !strings.Contains(failureView, "a.go") || !strings.Contains(failureView, "hunk failed") {
		t.Fatalf("failed alias lost edit presentation/error: %s", failureView)
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
	for _, want := range []string{"Permission required", "WebFetch https://example.com/docs", "Deny", "Allow once"} {
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
func TestModel_ReasoningCommandRestoresProviderDefault(t *testing.T) {
	fake := &fakeAgent{reasoning: llm.ReasoningEffortHigh}
	m := typeRunes(t, NewModel(fake, "s1", nil), "/reasoning:default")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/reasoning:default returned an async command")
	}
	if updated.(Model).agent.(*fakeAgent).reasoning != "" {
		t.Fatalf("reasoning = %q, want provider default", fake.reasoning)
	}
}

func TestModelSessionTransitionsResetDerivedState(t *testing.T) {
	seed := func() Model {
		m := NewModel(nil, "old", nil)
		m.entries = []entry{{kind: entryUser, text: "stale"}}
		m.usage = &session.Usage{InputTokens: 9}
		m.liveUsage = true
		m.outputBytes, m.reasoningBytes, m.toolInputBytes = 1, 2, 3
		m.revealing = true
		m.working, m.activeRun, m.cancelPending = true, 42, true
		m.composer = m.composer.seedHistory([]string{"stale history"})
		return m
	}
	assertClean := func(t *testing.T, m Model) {
		t.Helper()
		if m.usage != nil || m.liveUsage || m.outputBytes != 0 || m.reasoningBytes != 0 || m.toolInputBytes != 0 || m.revealing || m.working || m.activeRun != 0 || m.cancelPending {
			t.Fatalf("derived state was not reset")
		}
	}
	t.Run("fresh", func(t *testing.T) {
		m := seed().freshSession("new")
		assertClean(t, m)
		if m.sessionID != "new" || m.planMode || len(m.composer.history) != 0 {
			t.Fatalf("fresh transition incorrect")
		}
	})
	t.Run("restore", func(t *testing.T) {
		m := seed().restoreSession(engine.ResumeResult{SessionID: "restored", Mode: session.ModePlan, History: []string{"past"}, Events: []session.SessionEvent{{Message: &session.Message{Role: session.RoleUser, Text: "restored"}}}})
		assertClean(t, m)
		if m.sessionID != "restored" || !m.planMode || len(m.entries) != 1 || m.composer.history[0] != "past" {
			t.Fatalf("restore transition incorrect")
		}
	})
	t.Run("undo", func(t *testing.T) {
		m := seed().applyUndo(engine.UndoResult{Prompt: session.Prompt{Text: "retry"}, Events: []session.SessionEvent{{Message: &session.Message{Role: session.RoleUser, Text: "kept"}}}})
		assertClean(t, m)
		if m.sessionID != "old" || len(m.entries) != 1 || m.composer.input.Value() != "retry" {
			t.Fatalf("undo transition incorrect")
		}
	})
}
func TestModel_HidesCompletedCheckpointNotice(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	updated, cmd := m.update(CheckpointDoneMsg{Result: CheckpointResult{ID: "checkpoint-1"}})
	if cmd != nil {
		t.Fatal("checkpoint completion returned a command")
	}
	m = updated.(Model)
	for _, entry := range m.entries {
		if strings.Contains(entry.text, "checkpoint") {
			t.Fatalf("entries = %+v, checkpoint completion should be silent", m.entries)
		}
	}
}

func TestTranscript_HidesCheckpointToolActivity(t *testing.T) {
	var transcript Transcript
	transcript = transcript.foldEvent(EventMsg{Kind: session.KindToolCalled, CallID: "checkpoint-1", ToolName: "checkpoint"}, "s1")
	transcript = transcript.foldEvent(EventMsg{Kind: session.KindToolSuccess, CallID: "checkpoint-1", ToolName: "checkpoint"}, "s1")
	if len(transcript.entries) != 0 {
		t.Fatalf("entries = %+v, checkpoint tool activity should not be rendered", transcript.entries)
	}
}
