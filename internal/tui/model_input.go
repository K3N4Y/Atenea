package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/providerconfig"
)

// confirmWindow is how long a first destructive keypress stays armed waiting
// for the second one that confirms it.
const confirmWindow = 2 * time.Second

// confirmKind is the destructive action armed by a first keypress. Esc
// (cancel the run) and Ctrl+C (quit) share this one-shot confirmation instead
// of acting on a single press: an in-flight turn and the composer draft are
// too expensive to lose to a mistyped key, and Ctrl+C is the reflex of a user
// who just selected text with the mouse.
type confirmKind uint8

const (
	confirmNone confirmKind = iota
	confirmCancelRun
	confirmQuit
)

type confirmExpiredMsg struct{ generation uint64 }

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
	armed := m.armedConfirm()
	m.confirm = confirmNone
	if msg.Type == tea.KeyCtrlC {
		if armed != confirmQuit {
			return m.armConfirm(confirmQuit)
		}
		m.stopRun()
		return m, tea.Quit
	}
	confirmCancel := armed == confirmCancelRun
	switch m.activeInputTarget() {
	case targetAgentPicker:
		return m.handleAgentPickerKey(msg)
	case targetResumePicker:
		return m.handleResumePickerKey(msg)
	case targetVariantsPicker:
		return m.handleVariantsPickerKey(msg)
	case targetModelPicker:
		return m.handleModelPickerKey(msg)
	case targetMCPPicker:
		return m.handleMCPPickerKey(msg)
	case targetLearnedPicker:
		return m.handleLearnedPickerKey(msg)
	case targetSkillsPicker:
		return m.handleSkillsPickerKey(msg)
	case targetConnectPanel:
		return m.handleConnectPanelKey(msg)
	case targetPermissionGate:
		if isPageScroll(msg) {
			return m.scrollViewport(msg)
		}
		perm, _ := m.pendingPermission()
		return m.handlePermissionKey(msg, perm), nil
	case targetPlanGate:
		if isPageScroll(msg) {
			return m.scrollViewport(msg)
		}
		return m.resolvePlanKey(msg)
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		return m.composerKey(msg, confirmCancel)
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
	if isPageScroll(msg) {
		return m.scrollViewport(msg)
	}
	return m.composerKey(msg, confirmCancel)
}

// isPageScroll reports the keys that must always reach the transcript viewport,
// even while a modal gate owns the keyboard: an approval prompt is useless if
// the content being approved cannot be scrolled into view.
func isPageScroll(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyPgUp || msg.Type == tea.KeyPgDown
}

// armedConfirm reports the confirmation still inside its window; an expired one
// counts as disarmed even if its expiry tick has not arrived yet.
func (m Model) armedConfirm() confirmKind {
	if m.confirm == confirmNone || !time.Now().Before(m.confirmDeadline) {
		return confirmNone
	}
	return m.confirm
}

// armConfirm stages a destructive action so that repeating its key inside
// confirmWindow performs it, and schedules the expiry that disarms it.
func (m Model) armConfirm(kind confirmKind) (tea.Model, tea.Cmd) {
	m.confirm = kind
	m.confirmDeadline = time.Now().Add(confirmWindow)
	m.confirmGeneration++
	generation := m.confirmGeneration
	return m, tea.Tick(confirmWindow, func(time.Time) tea.Msg {
		return confirmExpiredMsg{generation: generation}
	})
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
		return m.armConfirm(confirmCancelRun)
	case tea.KeyTab:
		m.planMode = !m.planMode
		return m, nil
	}
	return m, nil
}
