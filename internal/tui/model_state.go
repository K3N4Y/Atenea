package tui

import (
	"time"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tui/engine"
)

type autoAcceptAgent interface {
	SetAutoAccept(sessionID string, enabled bool)
	AutoAcceptEnabled(sessionID string) bool
}

type yoloAgent interface {
	YoloAuthorized() bool
	YoloEnabled() bool
	SetYolo(bool) bool
}

type reasoningAgent interface {
	ReasoningEffort() llm.ReasoningEffort
	SetReasoningEffort(llm.ReasoningEffort) error
}

type modelAgent interface {
	ModelCatalog() []providerconfig.ProviderModels
	CurrentModel() providerconfig.Active
	SelectModel(providerID, model string) (providerconfig.Active, error)
	RefreshModels()
}

type retryAgent interface {
	RetryPrompt(sessionID string) (RunHandle, error)
}

// catalogAgent is the optional surface an Agent implements to expose the tool
// catalog. It is what lets the presentation layer ask a tool about itself from a
// durable event, which carries a name and nothing else: what the call may have
// changed, what granting it would authorize, how it should be drawn.
type catalogAgent interface {
	ToolCatalog() tool.Catalog
}

// tools is the catalog to ask about a tool the UI only knows by name, or nil when
// the agent does not expose one (the fakes in the tests). It is resolved on every
// use rather than kept in the Model: a rewire replaces the registry, and a copy
// held across one would answer for tools that are no longer there.
//
// Every caller has to handle nil, which is the same discipline the catalog itself
// demands for a name it does not know — see tool.Catalog.
func (m Model) tools() tool.Catalog {
	agent, ok := m.agent.(catalogAgent)
	if !ok {
		return nil
	}
	return agent.ToolCatalog()
}

// capabilityAgent is the optional surface an Agent implements to expose what the
// adapter serving the current model declares about itself. It is the same shape
// as catalogAgent and for the same reason: /model swaps the adapter, so the
// answer is asked for on every use instead of being copied into the Model.
type capabilityAgent interface {
	ModelCapabilities() (llm.Capabilities, bool)
}

// contextWindow is the active model's total token window, and whether anything
// knows it. Unknown is shown as absent, never as a guess: the window is what
// every usage figure is read against, so a wrong one is worse than none.
func (m Model) contextWindow(model string) (int, bool) {
	agent, ok := m.agent.(capabilityAgent)
	if !ok {
		return 0, false
	}
	capabilities, ok := agent.ModelCapabilities()
	if !ok {
		return 0, false
	}
	return capabilities.ContextWindow(model)
}

// presentationOf is how the entry's tool call should read: what the tool says
// about it, or — for a tool that says nothing, or one that is no longer registered
// — its own name plus a generic summary of the raw input, which is what the
// transcript showed for every tool before any of them could speak.
//
// Entries that are not about a tool call get the zero value; render ignores it.
func (m Model) presentationOf(e entry) tool.Presentation {
	if e.kind != entryTool && e.kind != entryPermission {
		return tool.Presentation{}
	}
	call := tool.Call{ID: e.callID, Name: e.tool, Input: []byte(e.input)}
	if p, ok := tool.PresentationFor(m.tools(), call, tool.Result{Diff: e.diff}); ok {
		return p
	}
	return tool.Presentation{Label: e.tool, Subject: summarizeToolInput(e.input)}
}

type entryKind int

const (
	entryAssistant entryKind = iota
	entryReasoning
	entryUser
	entryTool
	entryPermission
	entryPlanApproval
	entryError
	entryRetry
	entryCompaction
	entryNotice
	entryEvent
)

const historyLimit = engine.HistoryLimit

type panelFocus int

const (
	chatFocus panelFocus = iota
)

type toolStatus int

const (
	toolRunning toolStatus = iota
	toolOK
	toolFailed
	toolDenied
)

// permissionChoice is the action selected in the permission panel. The third
// one exists only when the request is grantable (see permissionRule): web_fetch
// and compound shell commands cannot be summarized by a rule, so there the
// panel offers two.
type permissionChoice int

const (
	permissionDeny permissionChoice = iota
	permissionAllowOnce
	permissionAllowSession
)

type entry struct {
	kind      entryKind
	text      string
	eventKind string
	live      bool
	revealed  int
	expanded  bool
	startedAt time.Time
	duration  time.Duration
	callID    string
	tool      string
	status    toolStatus
	err       string
	input     string
	spin      string
	output    string
	diff      string
	sessionID string
}

func (m Model) WithStatus(_ string, model string) Model {
	m.model = model
	return m
}

// WithNotice seeds the transcript with a dim informational line shown before
// any conversation. The launcher uses it to point at /connect when the TUI
// starts on the demo provider (no key anywhere).
func (m Model) WithNotice(text string) Model {
	return m.appendNotice(text)
}

func (m Model) WithWorkspace(branch, dir string) Model {
	m.branch = branch
	m.workDir = dir
	return m
}

func (m Model) WithWorkspaceRoot(branch, dir, root string) Model {
	m = m.WithWorkspace(branch, dir)
	m.workspaceRoot = root
	return m
}

func (m Model) WithHistory(history []string) Model {
	m.composer = m.composer.seedHistory(history)
	return m
}

func (m Model) WithSession(events []session.SessionEvent, mode session.Mode) Model {
	m = m.replaceEvents(events)
	m.planMode = mode == session.ModePlan
	return m
}

func (m Model) PendingPermission() (string, bool) {
	if e, ok := m.pendingPermission(); ok {
		return e.callID, true
	}
	return "", false
}

func (m Model) foldEvent(ev EventMsg) Model {
	m.Transcript = m.Transcript.foldEvent(ev, m.sessionID)
	return m
}

func (m Model) replaceEvents(events []session.SessionEvent) Model {
	m.Transcript = m.Transcript.replaceEvents(events, m.sessionID)
	return m
}

func (m Model) foldCompactionStatus(status CompactionStatusMsg) Model {
	m.Transcript = m.Transcript.foldCompactionStatus(status, m.sessionID)
	return m
}

func (m Model) updateLiveUsage() Model {
	m.Transcript = m.Transcript.updateLiveUsage()
	return m
}

func (m Model) advanceReveal() Model {
	m.Transcript = m.Transcript.advanceReveal()
	return m
}

func (m Model) appendError(text string) Model {
	m.Transcript = m.Transcript.appendError(text)
	return m
}

func (m Model) appendNotice(text string) Model {
	m.Transcript = m.Transcript.appendNotice(text)
	return m
}

func (m Model) removePendingPlan() Model {
	m.Transcript = m.Transcript.removePendingPlan()
	return m
}

func (m Model) applyPermissionDecision(permission entry, approved bool) Model {
	m.Transcript = m.Transcript.applyPermissionDecision(permission, approved)
	return m
}

func (m Model) toggleThinking() Model {
	m.Transcript = m.Transcript.toggleThinking()
	return m
}

func (m Model) toggleExpandableAt(viewportLine int) (Model, bool) {
	next, ok := m.Transcript.toggleExpandableAt(m.entryLines(), viewportLine)
	m.Transcript = next
	return m, ok
}

func (m Model) Working() bool {
	return m.working
}

// resetRunState clears execution state that cannot survive a session transition
// or a replacement of the durable transcript. It deliberately leaves the
// session's mode, history, and follow preference to each transition.
func (m Model) resetRunState() Model {
	m.activeRun = 0
	m.working = false
	m.cancelPending = false
	m.cancelDeadline = time.Time{}
	return m
}

func (m Model) clearTranscript() Model {
	return m.replaceEvents(nil)
}

// freshSession applies the local side of /new after the engine has created the
// new durable session.
func (m Model) freshSession(sessionID string) Model {
	m.sessionID = sessionID
	m = m.resetRunState()
	m = m.clearTranscript()
	m.planMode = false
	m.composer = m.composer.seedHistory(nil).clear()
	return m
}

func (m Model) restoreSession(result engine.ResumeResult) Model {
	m.sessionID = result.SessionID
	m = m.resetRunState()
	m = m.replaceEvents(result.Events)
	m.planMode = result.Mode == session.ModePlan
	m.followAgent = true
	m.composer = m.composer.clear().seedHistory(result.History)
	return m
}

func (m Model) applyUndo(result engine.UndoResult) Model {
	m = m.resetRunState()
	m = m.replaceEvents(result.Events)
	m.composer = m.composer.restoreDraft(result.Prompt)
	return m
}
