package tui

import (
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

type UndoDoneMsg struct {
	Result UndoResult
	Err    string
}

type CheckpointDoneMsg struct {
	Result CheckpointResult
	Err    string
}

type RewindDoneMsg struct {
	Result RewindResult
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

type fileListTarget uint8

const fileListMenu fileListTarget = 0

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

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch ev := msg.(type) {
	case EventMsg:
		m = m.cancelSelection()
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
			m = m.cancelSelection()
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
	case CheckpointDoneMsg:
		if ev.Err != "" {
			return m.appendError(ev.Err).syncViewport(), nil
		}
		return m, nil
	case RewindDoneMsg:
		if ev.Err != "" {
			return m.appendError(ev.Err).syncViewport(), nil
		}
		m = m.resetRunState()
		m = m.replaceEvents(ev.Result.Events)
		m.Transcript = m.Transcript.appendNotice("rewound to checkpoint: " + ev.Result.CheckpointID)
		m = m.resizeViewport()
		return m.requestWorkspaceRefresh()
	case UndoDoneMsg:
		if ev.Err != "" {
			return m.appendError(ev.Err).syncViewport(), nil
		}
		m = m.applyUndo(ev.Result)
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
		m = m.restoreSession(ev.Result)
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
		m = m.cancelSelection()
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
		if m.connectPanel.open && m.connectPanel.awaiting {
			m.connectPanel.err = "could not open the browser: " + ev.err.Error()
		}
		return m, nil
	case connectDoneMsg:
		if ev.generation != m.connectGen {
			// A stale success still stored a credential and must land. A dismissed
			// device login failure refers to a code the user no longer sees, while a
			// rejected typed key must remain visible or connection appears successful.
			if ev.err == "" {
				return m.applyStaleConnectSuccess(ev)
			}
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
		return m.updateSpinner(ev)
	case snackbarExpiredMsg:
		if ev.generation == m.snackbar.generation {
			m.snackbar = copySnackbar{}
		}
		return m, nil
	case tea.BlurMsg:
		m = m.cancelSelection()
		m.terminalFocused = false
		return m, nil
	case tea.FocusMsg:
		m.terminalFocused = true
		return m, nil
	case tea.WindowSizeMsg:
		m = m.cancelSelection()
		m.ready = true
		m.width = ev.Width
		m.height = ev.Height
		m = m.resizeViewport()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(ev)
	case tea.MouseMsg:
		return m.handleMouse(ev)
	}
	var cmd tea.Cmd
	m.composer, cmd = m.composer.update(msg)
	return m, cmd
}

func (m Model) updateSpinner(msg spinner.TickMsg) (Model, tea.Cmd) {
	if !m.working {
		return m, nil
	}
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	frame := ansi.Strip(m.spinner.View())
	dirty := false
	for i := range m.entries {
		if m.entries[i].kind == entryTool && m.entries[i].tool == "task" && m.entries[i].status == toolRunning && m.entries[i].spin != frame {
			m.entries[i].spin = frame
			dirty = true
		}
	}
	for parentCallID, batch := range m.childBatches {
		for i := range batch {
			if batch[i].status == toolRunning && batch[i].spin != frame {
				batch[i].spin = frame
				dirty = true
			}
		}
		m.childBatches[parentCallID] = batch
	}
	if dirty {
		m = m.syncViewport()
	}
	return m, cmd
}
