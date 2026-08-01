// Package tui implements Atenea's Bubble Tea terminal interface.
package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tui/engine"
)

type UndoDoneMsg struct {
	Result UndoResult
	Err    string
}

type ResumeDoneMsg struct {
	Generation uint64
	SessionID  string
	Result     ResumeResult
	Err        string
}

type ResumeSessionsDoneMsg struct {
	Generation uint64
	Sessions   []session.SessionSummary
	Err        string
}

const resumeResultSessionMismatch = "resume result session mismatch"

type cancelConfirmationExpiredMsg struct{ generation uint64 }

type fileListTarget uint8

const (
	fileListMenu fileListTarget = iota
)

type filesListedMsg struct {
	target     fileListTarget
	generation uint64
	files      []string
	err        error
}

type workspaceRefreshedMsg struct {
	generation uint64
	branch     string
	summary    gitSummary
}

type Agent interface {
	SendPrompt(sessionID, text string) (RunHandle, error)
	SendPlanPrompt(sessionID, text string) (RunHandle, error)
	AcceptPlan(sessionID string) (RunHandle, error)
	Undo(sessionID string) (UndoResult, error)
	ListResumeSessions(currentSessionID string) ([]session.SessionSummary, error)
	ResumeSessionByID(currentSessionID, targetSessionID string) (ResumeResult, error)
	// ResolvePermission settles the pending ask-before-run request with the
	// user's verdict: deny, allow once, or allow this shape for the rest of the
	// session (the engine records the grant).
	ResolvePermission(sessionID, callID string, verdict permission.Verdict)
	Stop(sessionID string)
}

type autoAcceptAgent interface {
	SetAutoAccept(sessionID string, enabled bool)
	AutoAcceptEnabled(sessionID string) bool
}

type yoloAgent interface {
	YoloAuthorized() bool
	YoloEnabled() bool
	SetYolo(bool) bool
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
	entryTool // tool call y su desenlace
	entryPermission
	entryPlanApproval
	entryError
	entryRetry
	entryCompaction
	entryNotice // informational line (connected provider, first-run hint)
	entryEvent  // forward-compatible pass-through for an unknown event kind
)

const historyLimit = engine.HistoryLimit

const cancelConfirmationWindow = 2 * time.Second

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
	kind entryKind
	text string
	// eventKind is set only on entryEvent and preserves the durable taxonomy
	// value so a newer producer remains visible to an older UI.
	eventKind string
	live      bool

	revealed int

	expanded bool

	startedAt time.Time
	duration  time.Duration

	callID string
	tool   string
	status toolStatus
	err    string
	input  string
	// spin is the live spinner frame that animates the run marker of a
	// running "task" (subagent) entry; the spinner tick refreshes it while
	// the subagent runs. Empty means the static run marker.
	spin      string
	output    string
	diff      string
	sessionID string
}

type Model struct {
	agent     Agent
	sessionID string
	activeRun uint64
	events    <-chan tea.Msg

	// transcript is the conversation log and the pure state derived from it
	// (entries, token usage, the smooth-reveal cursor). It is embedded so its
	// fields and methods promote onto Model: `m.entries`, `m.usage`,
	// `m.foldEvent(...)`, `m.hasBacklog()` read as the Model's own. It owns what
	// used to be ~7 scattered Model fields; its own test file exercises the fold,
	// reveal, usage and gating logic directly, without going through View().
	Transcript

	// composer is the chat input crossroads: the editable textarea, the
	// in-memory prompt-history navigation, and the autocomplete popup (slash /
	// "@" / inline "/model"). It is embedded so its state fields promote onto
	// Model (m.input, m.history, m.histIdx, m.menuItems, m.menuSelected,
	// m.modelSearch, m.files, m.filesLoaded, …) and the sub-model owns its own
	// editing/history/menu behavior; the root routes input to it when the active
	// target is targetComposer, interprets its outward intents (submit → the
	// local-command/mode routing in submitPrompt), and seeds/appends the history
	// slice. See composer.go.
	composer

	working bool

	cancelPending    bool
	cancelDeadline   time.Time
	cancelGeneration uint64

	followAgent    bool
	hasNewActivity bool
	lastTranscript string

	spinner spinner.Model

	viewport viewport.Model
	ready    bool
	width    int
	height   int

	model string

	branch        string
	workDir       string
	workspaceRoot string
	gitSummary    gitSummary
	workspaceGen  uint64

	planMode bool

	// commands and listFiles are the composer's autocomplete sources (set via
	// WithCompletions): the "/" slash-command menu and the "@" file listing.
	// They are Model configuration injected into the composer's methods per call
	// while the popup state itself lives on the embedded composer (menuItems,
	// files, …).
	commands  []command.Command
	listFiles func() ([]string, error)
	cacheStatsState

	resumePicker resumePicker
	resumeGen    uint64
	modelPicker  modelPicker
	mcpPicker    mcpPicker
	mcpGen       uint64
	connectPanel connectPanel
	connectGen   uint64

	focus           panelFocus
	terminalFocused bool

	permissionChoice permissionChoice
	permissionScroll int
}

func NewModel(agent Agent, sessionID string, events <-chan tea.Msg) Model {
	input := newComposerInput()
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(statusStyle))
	return Model{agent: agent, sessionID: sessionID, events: events, composer: composer{input: input}, spinner: sp, followAgent: true, terminalFocused: true}
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

func refreshWorkspace(root string, generation uint64) tea.Cmd {
	if root == "" {
		return nil
	}
	return func() tea.Msg {
		branch, _ := gitBranch(root)
		summary, _ := summarizeGitWorkspace(root)
		return workspaceRefreshedMsg{generation: generation, branch: branch, summary: summary}
	}
}

func (m Model) requestWorkspaceRefresh() (Model, tea.Cmd) {
	if m.workspaceRoot == "" {
		return m, nil
	}
	m.workspaceGen++
	return m, refreshWorkspace(m.workspaceRoot, m.workspaceGen)
}

func (m Model) WithCompletions(commands []command.Command, listFiles func() ([]string, error)) Model {
	m.commands = commands
	m.listFiles = listFiles
	return m
}

// modelSource wires the inline "/model" search to the Model's agent: it exposes
// the model catalog and a refresh trigger without the composer importing the
// agent interface. A non-modelAgent agent reports ok=false so the search shows
// "No models available".
func (m Model) modelSource() modelSource {
	return modelSource{
		catalog: func() ([]providerconfig.ProviderModels, bool) {
			controller, ok := m.agent.(modelAgent)
			if !ok {
				return nil, false
			}
			return controller.ModelCatalog(), true
		},
		refresh: func() {
			if controller, ok := m.agent.(modelAgent); ok {
				controller.RefreshModels()
			}
		},
	}
}

// refreshMenu is the Model-level seam onto the embedded composer's popup
// rebuild: it injects the completion sources, stores the updated composer back,
// and recomputes the viewport height (the popup occupies lines below the
// transcript, which reservedLines discounts). The behavior tests call it on the
// Model, and the ModelsRefreshedMsg / filesListedMsg handlers reuse it.
func (m Model) refreshMenu() (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.composer, cmd = m.composer.refreshMenu(m.commands, m.listFiles, m.modelSource())
	return m.resizeViewport(), cmd
}

// closeMenu is the Model-level seam onto composer.closeMenu, adding the viewport
// recompute the popup's line count change requires.
func (m Model) closeMenu() Model {
	m.composer = m.composer.closeMenu()
	return m.resizeViewport()
}

// applySelection is the Model-level seam onto composer.applySelection, adding
// the viewport recompute (applying a selection may close the popup).
func (m Model) applySelection() (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.composer, cmd = m.composer.applySelection(m.commands, m.listFiles, m.modelSource())
	return m.resizeViewport(), cmd
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

// The methods below are thin Model-level seams onto the embedded Transcript.
// The transcript module returns a Transcript (value-in/value-out, so it stays
// unit-testable in isolation); these wrappers thread that back into the Model
// so the Bubble Tea update loop keeps its `m = m.foldEvent(...)` idiom and the
// two mutators that need the Model's session id can pass it in. Query methods
// (hasBacklog, hasPendingPlan, pendingPermission, ...) are promoted directly
// from the embedded Transcript and need no wrapper.

// foldEvent folds a durable event into the transcript, scoping the compaction
// upsert to the Model's current session.
func (m Model) foldEvent(ev EventMsg) Model {
	m.Transcript = m.Transcript.foldEvent(ev, m.sessionID)
	return m
}

// replaceEvents rebuilds the transcript from a full durable log.
func (m Model) replaceEvents(events []session.SessionEvent) Model {
	m.Transcript = m.Transcript.replaceEvents(events, m.sessionID)
	return m
}

// foldCompactionStatus folds a manual-compaction status message into the
// transcript, scoped to the Model's session.
func (m Model) foldCompactionStatus(status CompactionStatusMsg) Model {
	m.Transcript = m.Transcript.foldCompactionStatus(status, m.sessionID)
	return m
}

// updateLiveUsage refreshes the estimated live token usage from the streamed
// byte counts.
func (m Model) updateLiveUsage() Model {
	m.Transcript = m.Transcript.updateLiveUsage()
	return m
}

// advanceReveal advances one reveal tick over the transcript.
func (m Model) advanceReveal() Model {
	m.Transcript = m.Transcript.advanceReveal()
	return m
}

func (m Model) appendError(text string) Model {
	m.Transcript = m.Transcript.appendError(text)
	return m
}

// appendNotice appends a dim informational line to the transcript.
func (m Model) appendNotice(text string) Model {
	m.Transcript = m.Transcript.appendNotice(text)
	return m
}

// removePendingPlan drops the plan approval offer from the transcript.
func (m Model) removePendingPlan() Model {
	m.Transcript = m.Transcript.removePendingPlan()
	return m
}

// applyPermissionDecision settles a permission entry (approved or denied).
func (m Model) applyPermissionDecision(permission entry, approved bool) Model {
	m.Transcript = m.Transcript.applyPermissionDecision(permission, approved)
	return m
}

// toggleThinking flips the expanded state of every settled thought block.
func (m Model) toggleThinking() Model {
	m.Transcript = m.Transcript.toggleThinking()
	return m
}

// toggleExpandableAt flips the settled thought or Bash block under the given
// viewport line and reports whether one was toggled (so the caller re-syncs
// the viewport). It hands the module the wrapped viewport lines, which depend
// on the render width the Model owns.
func (m Model) toggleExpandableAt(viewportLine int) (Model, bool) {
	next, ok := m.Transcript.toggleExpandableAt(m.entryLines(), viewportLine)
	m.Transcript = next
	return m, ok
}

func (m Model) Working() bool {
	return m.working
}

func waitForEvent(ch <-chan tea.Msg) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(waitForEvent(m.events), refreshWorkspace(m.workspaceRoot, m.workspaceGen), cursor.Blink)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.update(msg)
	next, ok := updated.(Model)
	if !ok {
		return updated, cmd
	}
	return next, tea.Batch(cmd, next.syncComposerFocus())
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch ev := msg.(type) {
	case EventMsg:
		permissionHeight := m.permissionPanelHeight()
		m = m.foldEvent(ev)
		permissionLayoutChanged := permissionHeight != m.permissionPanelHeight()
		var workspaceCmd tea.Cmd
		if ev.Kind == session.KindToolSuccess && tool.MayChangeFiles(m.tools(), ev.ToolName) {
			m, workspaceCmd = m.requestWorkspaceRefresh()
		}
		pump := waitForEvent(m.events)
		if permissionLayoutChanged {
			m = m.resizeViewport()
		}
		if !m.revealing && m.hasBacklog() {
			m.revealing = true
			return m.syncViewportActivity(), tea.Batch(pump, workspaceCmd, revealTick())
		}
		return m.syncViewportActivity(), tea.Batch(pump, workspaceCmd)
	case workspaceRefreshedMsg:
		if ev.generation < m.workspaceGen {
			return m, nil
		}
		m.workspaceGen = ev.generation
		m.branch = ev.branch
		m.gitSummary = ev.summary
		return m, nil
	case CompactionStatusMsg:
		if ev.SessionID == m.sessionID {
			m = m.foldCompactionStatus(ev)
		}
		return m.syncViewportActivity(), waitForEvent(m.events)
	case RunDoneMsg:
		if ev.SessionID != m.sessionID || ev.RunID != m.activeRun {
			return m, waitForEvent(m.events)
		}
		m.working = false
		m.activeRun = 0
		m.cancelPending = false
		if ev.Err != "" {
			m = m.appendError(ev.Err)
		}
		return m.resizeViewport(), waitForEvent(m.events)
	case UndoDoneMsg:
		if ev.Err != "" {
			return m.appendError(ev.Err).syncViewport(), nil
		}
		m = m.replaceEvents(ev.Result.Events)
		m.input.SetValue(ev.Result.Prompt)
		m.input.CursorEnd()
		m.menuItems = nil
		m.working = false
		m = m.resizeViewport()
		return m.requestWorkspaceRefresh()
	case ResumeDoneMsg:
		if !m.resumePicker.open || ev.Generation != m.resumeGen || ev.SessionID == "" || ev.SessionID != m.resumePicker.targetID {
			return m, nil
		}
		if ev.Err != "" {
			m.resumePicker.fail(ev.Err)
			return m, nil
		}
		if ev.Result.SessionID != ev.SessionID {
			m.resumePicker.fail(resumeResultSessionMismatch)
			return m, nil
		}
		m.resumePicker.close()
		m.sessionID = ev.Result.SessionID
		m = m.replaceEvents(ev.Result.Events)
		m.planMode = ev.Result.Mode == session.ModePlan
		m.activeRun = 0
		m.working = false
		m.followAgent = true
		m.input.SetValue("")
		m.menuItems = nil
		m.history = append([]string(nil), ev.Result.History...)
		if len(m.history) > historyLimit {
			m.history = m.history[len(m.history)-historyLimit:]
		}
		m.histIdx = len(m.history)
		return m.resizeViewport(), nil
	case ResumeSessionsDoneMsg:
		if !m.resumePicker.open || ev.Generation != m.resumeGen {
			return m, nil
		}
		if ev.Err != "" {
			m.resumePicker.fail(ev.Err)
			return m, nil
		}
		m.resumePicker.setSessions(ev.Sessions)
		return m, nil
	case ModelsRefreshedMsg:
		if m.modelPicker.open {
			m.modelPicker.setProviders(ev.Providers)
			return m, waitForEvent(m.events)
		}
		next, cmd := m.refreshMenu()
		return next, tea.Batch(cmd, waitForEvent(m.events))
	case filesListedMsg:
		if ev.target != fileListMenu {
			return m, nil
		}
		var (
			cmd     tea.Cmd
			applied bool
		)
		m.composer, cmd, applied = m.composer.applyListedFiles(ev, m.commands, m.listFiles, m.modelSource())
		if !applied {
			return m, nil
		}
		return m.resizeViewport(), cmd
	case revealTickMsg:
		m = m.advanceReveal()
		if !m.hasBacklog() {
			m.revealing = false
			return m.syncViewportActivity(), nil
		}
		m.revealing = true
		return m.syncViewportActivity(), revealTick()
	case deviceLoginStartedMsg:
		return m.startedDeviceLogin(ev)
	case browserOpenFailedMsg:
		// The login is still perfectly waitable — only the shortcut failed — so
		// the panel stays put and names why the click did nothing. The URL on
		// screen remains the manual path.
		if m.connectPanel.open && m.connectPanel.awaiting {
			m.connectPanel.err = "could not open the browser: " + ev.err.Error()
		}
		return m, nil
	case connectDoneMsg:
		if ev.generation != m.connectGen {
			// A stale success still stored the credential and must land.
			if ev.err == "" {
				return m.applyStaleConnectSuccess(ev)
			}
			// A stale failure has nowhere on the current panel to go, so it either
			// reaches the transcript or is dropped, and the two kinds of attempt want
			// opposite answers. A login the user dismissed reports the death of a code
			// they never saw, minutes later, over whatever they are doing by then. A
			// key they typed and the provider rejected is the opposite: drop it and
			// they walk away believing they connected, and find out at the next turn
			// through "no credential stored for provider".
			if ev.login {
				return m, nil
			}
			return m.appendError(ev.err).syncViewport(), nil
		}
		return m.finishConnect(ev)
	case mcpToggleDoneMsg:
		if ev.generation != m.mcpGen {
			return m, nil
		}
		delete(m.mcpPicker.busy, ev.name)
		if ev.err != "" {
			m.mcpPicker.err = ev.err
		}
		if m.mcpPicker.open {
			m.mcpPicker.refreshFromAgent(m.agent)
		}
		return m, nil
	case cancelConfirmationExpiredMsg:
		if ev.generation == m.cancelGeneration {
			m.cancelPending = false
		}
		return m, nil
	case spinner.TickMsg:
		if !m.working {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(ev)
		// Running subagents animate their transcript marker with the live
		// spinner frame. ponytail: this re-renders the whole transcript per
		// tick while a task runs; cache per-entry renders if CPU ever matters.
		frame := ansi.Strip(m.spinner.View())
		dirty := false
		for i := range m.entries {
			if m.entries[i].kind == entryTool && m.entries[i].tool == "task" && m.entries[i].status == toolRunning && m.entries[i].spin != frame {
				m.entries[i].spin = frame
				dirty = true
			}
		}
		if dirty {
			m = m.syncViewport()
		}
		return m, cmd
	case tea.BlurMsg:
		m.terminalFocused = false
		return m, nil
	case tea.FocusMsg:
		m.terminalFocused = true
		return m, nil
	case tea.WindowSizeMsg:
		m.ready = true
		m.width = ev.Width
		m.height = ev.Height
		m = m.resizeViewport()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(ev)
	case tea.MouseMsg:
		// Modal short-circuits share the precedence resolver (see input_router.go)
		// with the keyboard, but the pointer LEAF behavior differs and is spelled
		// out explicitly here: the resume picker swallows mouse events without
		// dispatching, the other pickers route to their own mouse handlers, and the
		// permission gate is checked below AFTER the top-bar Y adjustment (unlike the
		// pickers, whose overlays cover the whole screen). The plan gate has no
		// pointer short-circuit at all — plan approval is keyboard-only.
		switch m.activeInputTarget() {
		case targetResumePicker:
			return m, nil
		case targetModelPicker:
			return m.handleModelPickerMouse(ev)
		case targetMCPPicker:
			return m.handleMCPPickerMouse(ev)
		case targetConnectPanel:
			return m.handleConnectPanelMouse(ev)
		}
		ev.Y -= m.layout().mouseBodyYOffset
		if m.activeInputTarget() == targetPermissionGate {
			perm, _ := m.pendingPermission()
			if next, handled := m.handlePermissionMouse(ev, perm); handled {
				return next, nil
			}
		}
		if m.newActivityIndicatorHit(ev) {
			return m, nil
		}
		if ev.Action == tea.MouseActionPress && (ev.Button == tea.MouseButtonWheelUp || ev.Button == tea.MouseButtonWheelDown) {
			return m.scrollViewport(ev)
		}
		if ev.Action == tea.MouseActionPress && ev.Button == tea.MouseButtonLeft {
			if viewportLine, ok := m.transcriptLineAtMouse(ev); ok {
				if next, ok := m.toggleExpandableAt(viewportLine); ok {
					return next.syncViewport(), nil
				}
			}
		}
		return m.scrollViewport(ev)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) newActivityIndicatorHit(msg tea.MouseMsg) bool {
	if !m.hasNewActivity || !m.ready || msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return false
	}
	return msg.X == m.viewport.Width-1 && msg.Y == m.viewport.Height-1
}

func (m Model) scrollViewport(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	m.followAgent = m.viewport.AtBottom()
	if m.followAgent {
		m.hasNewActivity = false
	}
	return m, cmd
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		m.cancelPending = false
		m.stopRun()
		return m, tea.Quit
	}
	confirmCancel := m.cancelPending && time.Now().Before(m.cancelDeadline)
	m.cancelPending = false
	// The precedence ORDER of overlays and gates lives once in activeInputTarget
	// (see input_router.go). handleKey only dispatches to the leaf handler for the
	// active target and keeps each target's key-specific exceptions here (e.g.
	// PgUp/PgDn still scroll during the permission gate).
	switch m.activeInputTarget() {
	case targetResumePicker:
		return m.handleResumePickerKey(msg)
	case targetModelPicker:
		return m.handleModelPickerKey(msg)
	case targetMCPPicker:
		return m.handleMCPPickerKey(msg)
	case targetConnectPanel:
		return m.handleConnectPanelKey(msg)
	case targetPermissionGate:
		if msg.Type == tea.KeyPgUp || msg.Type == tea.KeyPgDown {
			return m.scrollViewport(msg)
		}
		perm, _ := m.pendingPermission()
		return m.handlePermissionKey(msg, perm), nil
	case targetPlanGate:
		return m.resolvePlanKey(msg)
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		return m.handleKeyRuneBatch(msg)
	}
	if msg.Type == tea.KeyShiftTab {
		m = m.toggleThinking()
		return m.syncViewport(), nil
	}
	if !m.working && m.input.Value() == "" && m.lastErrorIsProvider() && keyRune(msg) == "d" {
		m.Transcript = m.Transcript.toggleLastErrorDetails()
		return m.syncViewport(), nil
	}
	if !m.working && m.input.Value() == "" && m.lastErrorIsProvider() && keyRune(msg) == "r" {
		retrier, ok := m.agent.(retryAgent)
		if !ok {
			return m, nil
		}
		handle, err := retrier.RetryPrompt(m.sessionID)
		if err != nil {
			return m.appendError(err.Error()).syncViewport(), nil
		}
		m.Transcript = m.Transcript.removeLastError()
		m.working = true
		m.activeRun = handle.RunID
		return m.resizeViewport(), m.spinner.Tick
	}
	if msg.Type == tea.KeyPgUp || msg.Type == tea.KeyPgDown {
		return m.scrollViewport(msg)
	}
	return m.composerKey(msg, confirmCancel)
}

// composerKey routes a key to the embedded composer while it holds focus and
// interprets its outward intent. The open menu wins, Enter submits through the
// root's submitPrompt, and Esc/Tab with the menu closed drive the root's
// run-control.
func (m Model) composerKey(msg tea.KeyMsg, confirmCancel bool) (tea.Model, tea.Cmd) {
	var (
		intent composerIntent
		cmd    tea.Cmd
	)
	menuWasOpen := m.menuOpen()
	m.composer, intent, cmd = m.composer.handleKey(msg, m.commands, m.listFiles, m.modelSource())
	switch {
	case intent.submit:
		// The composer already completed a builtin selection onto the input (and
		// closed the menu) before surfacing submit; submitPrompt is the single
		// dispatch point for local commands, slash expansion, and mode routing.
		// A menu-close changed the reserved line count, so recompute the viewport
		// first (matching the original closeMenu().submitPrompt()); a plain Enter
		// left the popup untouched and submitPrompt recomputes at its end anyway.
		if menuWasOpen {
			m = m.resizeViewport()
		}
		return m.submitPrompt()
	case intent.handled:
		// A menu rebuild, apply, or close may have changed the reserved line count;
		// recompute the viewport so the popup lines above the composer box stay
		// accounted for. Menu nav (Up/Down) leaves the count unchanged, so the
		// recompute is an idempotent no-op there.
		return m.resizeViewport(), cmd
	}
	// Not handled: a run-control key the root owns with the menu closed.
	switch msg.Type {
	case tea.KeyEsc:
		if !m.working {
			return m, nil
		}
		if confirmCancel {
			m.stopRun()
			return m, nil
		}
		m.cancelPending = true
		m.cancelDeadline = time.Now().Add(cancelConfirmationWindow)
		m.cancelGeneration++
		generation := m.cancelGeneration
		return m, tea.Tick(cancelConfirmationWindow, func(time.Time) tea.Msg {
			return cancelConfirmationExpiredMsg{generation: generation}
		})
	case tea.KeyTab:
		m.planMode = !m.planMode
		return m, nil
	}
	return m, nil
}

func (m Model) handleResumePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.resumePicker.close()
		return m, nil
	case tea.KeyUp:
		m.resumePicker.move(-1)
		return m, nil
	case tea.KeyDown:
		m.resumePicker.move(1)
		return m, nil
	case tea.KeyEnter:
		if m.resumePicker.loading {
			return m, nil
		}
		selected, ok := m.resumePicker.selectedSession()
		if !ok {
			return m, nil
		}
		m.resumePicker.beginLoad(selected.ID)
		currentSessionID := m.sessionID
		targetSessionID := selected.ID
		generation := m.resumeGen
		agent := m.agent
		return m, func() tea.Msg {
			result, err := agent.ResumeSessionByID(currentSessionID, targetSessionID)
			if err != nil {
				return ResumeDoneMsg{Generation: generation, SessionID: targetSessionID, Err: err.Error()}
			}
			return ResumeDoneMsg{Generation: generation, SessionID: targetSessionID, Result: result}
		}
	case tea.KeyPgUp, tea.KeyPgDown:
		return m, nil
	}

	var cmd tea.Cmd
	m.resumePicker.query, cmd = m.resumePicker.query.Update(msg)
	m.resumePicker.filter()
	return m, cmd
}

func (m Model) handleKeyRuneBatch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	for _, key := range msg.Runes {
		next, _ := m.handleKey(tea.KeyMsg{
			Type:  tea.KeyRunes,
			Runes: []rune{key},
			Alt:   msg.Alt,
		})
		nextModel, ok := next.(Model)
		if !ok {
			return next, nil
		}
		m = nextModel
	}
	return m, nil
}
