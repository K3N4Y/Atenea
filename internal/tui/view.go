package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/K3N4Y/atenea/internal/tui/theme"
)

var (
	primaryTextStyle   = lipgloss.NewStyle()
	secondaryTextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Border))
	metadataStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Muted))
	focusStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Accent))
	successStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Success))
	warningStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Warning))
	dangerStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Error))
	surfaceStyle       = lipgloss.NewStyle().Background(lipgloss.Color(theme.UserMessage))
	selectedRowStyle   = focusStyle
)

func (m Model) View() string {
	if m.modelPicker.open {
		return m.modelPickerView()
	}
	if m.mcpPicker.open {
		return m.mcpPickerView()
	}
	if m.learnedPicker.open {
		return m.learnedPickerView()
	}
	if m.skillsPicker.open {
		return m.skillsPickerView()
	}
	if m.variantsPicker.open {
		return m.variantsPickerView(m.chatView())
	}
	if m.connectPanel.open {
		return m.connectPanelView()
	}
	if m.resumePicker.open {
		return m.resumePickerView()
	}

	return m.chatView()
}

func (m Model) chatView() string {
	content := m.chatContent()
	canvas := m.renderCanvas(content)
	if !m.ready {
		return canvas
	}
	canvas = m.renderSnackbar(canvas)
	return m.topBar() + "\n" + canvas
}

func (m Model) chatContent() string {
	return m.transcriptView() + m.menuView() + m.workingStatusView() + m.permissionPanelView() + m.composerView()
}

func (m Model) reservedLines() int {
	reserved := m.composerReservedLines() + m.composerMenuReservedLines()
	if m.showsWorking() {
		reserved++
	}
	reserved += m.permissionPanelHeight()
	return reserved
}

func (m Model) composerMenuReservedLines() int {
	if m.composer.menuHeight() == 0 {
		return 0
	}
	return m.composer.menuHeight() + menuBoxBorderHeight
}

func (m Model) composerReservedLines() int {
	reserved := m.composer.inputHeight() + 2
	if _, permissionPending := m.pendingPermission(); !permissionPending {
		reserved += composerOuterMargin
	}
	return reserved
}

func (m Model) resizeViewport() Model {
	if !m.ready {
		return m
	}
	// One geometry pass owns every dimension applied here: the textarea width and
	// height and the viewport width and height. resizeViewport only applies them;
	// the arithmetic stripping box chrome and bounding the input against the
	// reserved-line budget lives in layout.go.
	l := m.layout()
	m.composer = m.composer.setWidth(l.inputWidth)
	m.viewport.Width = l.viewportWidth
	m.composer = m.composer.setHeight(l.inputHeight)
	m.viewport.Height = l.viewportHeight
	return m.syncViewport()
}

func (m Model) syncViewport() Model {
	return m.syncViewportContent(false)
}

func (m Model) syncViewportActivity() Model {
	return m.syncViewportContent(true)
}

func (m Model) syncViewportContent(agentActivity bool) Model {
	if !m.ready {
		return m
	}
	rawTranscript := m.renderTranscript()
	contentChanged := rawTranscript != m.lastTranscript
	if contentChanged && m.selection != nil {
		m.selection = nil
	}
	offset := m.viewport.YOffset
	transcript := hardWrapOverflow(rawTranscript, m.viewport.Width)
	m.viewport.SetContent(transcript)
	if m.followAgent {
		m.viewport.GotoBottom()
		m.hasNewActivity = false
	} else {
		m.viewport.SetYOffset(offset)
		if agentActivity && contentChanged {
			m.hasNewActivity = true
		}
	}
	m.lastTranscript = rawTranscript
	return m
}
