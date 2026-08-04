package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/providerconfig"
)

const cancelConfirmationWindow = 2 * time.Second

type cancelConfirmationExpiredMsg struct{ generation uint64 }

type imageClipboardMsg struct {
	data       []byte
	err        error
	generation uint64
}

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

func (m Model) refreshMenu() (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.composer, cmd = m.composer.refreshMenu(m.commands, m.listFiles, m.modelSource())
	return m.resizeViewport(), cmd
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		m.cancelPending = false
		m.stopRun()
		return m, tea.Quit
	}
	confirmCancel := m.cancelPending && time.Now().Before(m.cancelDeadline)
	m.cancelPending = false
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
	if !m.working && m.composer.value() == "" && m.lastErrorIsProvider() && keyRune(msg) == "d" {
		m.Transcript = m.Transcript.toggleLastErrorDetails()
		return m.syncViewport(), nil
	}
	if !m.working && m.composer.value() == "" && m.lastErrorIsProvider() && keyRune(msg) == "r" {
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

func (m Model) pasteImage() (tea.Model, tea.Cmd) {
	if m.imageClipboard == nil {
		return m, nil
	}
	read := m.imageClipboard
	generation := m.composer.generation
	return m, func() tea.Msg {
		data, err := read()
		return imageClipboardMsg{data: data, err: err, generation: generation}
	}
}

func (m Model) composerKey(msg tea.KeyMsg, confirmCancel bool) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlV {
		return m.pasteImage()
	}
	var (
		intent composerIntent
		cmd    tea.Cmd
	)
	menuWasOpen := m.composer.menuOpen()
	m.composer, intent, cmd = m.composer.handleKey(msg, m.commands, m.listFiles, m.modelSource())
	switch {
	case intent.submit:
		if menuWasOpen {
			m = m.resizeViewport()
		}
		return m.submitPrompt()
	case intent.handled:
		return m.resizeViewport(), cmd
	}
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

func (m Model) handleKeyRuneBatch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	for _, key := range msg.Runes {
		next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}, Alt: msg.Alt})
		nextModel, ok := next.(Model)
		if !ok {
			return next, nil
		}
		m = nextModel
	}
	return m, nil
}
