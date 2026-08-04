// Package tui implements Atenea's Bubble Tea terminal interface.
package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
)

type Agent interface {
	SendPrompt(sessionID string, prompt session.Prompt) (RunHandle, error)
	SendPlanPrompt(sessionID string, prompt session.Prompt) (RunHandle, error)
	AcceptPlan(sessionID string) (RunHandle, error)
	Undo(sessionID string) (UndoResult, error)
	ListResumeSessions(currentSessionID string) ([]session.SessionSummary, error)
	ResumeSessionByID(currentSessionID, targetSessionID string) (ResumeResult, error)
	ResolvePermission(sessionID, callID string, verdict permission.Verdict)
	Stop(sessionID string)
}

type Model struct {
	agent     Agent
	sessionID string
	activeRun uint64
	events    <-chan tea.Msg

	Transcript
	composer composer

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

	selection       *transcriptSelection
	copyToClipboard func(string) error
	copyGeneration  uint64
	snackbar        copySnackbar

	permissionChoice permissionChoice
	permissionScroll int
	imageClipboard   func() ([]byte, error)
}

func NewModel(agent Agent, sessionID string, events <-chan tea.Msg) Model {
	input := newComposerInput()
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(secondaryTextStyle))
	return Model{agent: agent, sessionID: sessionID, events: events, composer: composer{input: input, nextImage: 1}, spinner: sp, followAgent: true, terminalFocused: true}
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

// WithImageClipboard injects the image reader used by Ctrl+V.
func (m Model) WithImageClipboard(read func() ([]byte, error)) Model {
	m.imageClipboard = read
	return m
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
