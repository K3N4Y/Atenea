package tui

import tea "github.com/charmbracelet/bubbletea"

func keyRune(msg tea.KeyMsg) string {
	if msg.Type != tea.KeyRunes {
		return ""
	}
	return string(msg.Runes)
}

// syncComposerFocus derives whether the textarea should hold terminal focus
// from the shared precedence resolver (see input_router.go).
func (m *Model) syncComposerFocus() tea.Cmd {
	target := m.activeInputTarget()
	if target == targetComposer && m.terminalFocused {
		if !m.composer.focused() {
			return m.composer.focus()
		}
		return nil
	}
	m.composer.blur()
	if target == targetResumePicker {
		if m.terminalFocused {
			if !m.resumePicker.query.Focused() {
				return m.resumePicker.query.Focus()
			}
			return nil
		}
		m.resumePicker.query.Blur()
	}
	return nil
}

func (m Model) transcriptLineAtMouse(msg tea.MouseMsg) (int, bool) {
	if !m.ready || msg.Y < 0 || msg.Y >= m.viewport.Height {
		return 0, false
	}
	return m.viewport.YOffset + msg.Y, true
}

func (m Model) layoutSize() layoutSize {
	return layoutSize{width: m.width, height: m.height, ready: m.ready}
}

func (m Model) layout() Layout {
	return computeLayout(m.layoutSize(), layoutState{
		reservedLines: m.reservedLines(),
		inputHeight:   m.composer.inputHeight(),
	})
}

func (m Model) baseLayout() Layout {
	return computeLayout(m.layoutSize(), layoutState{})
}

func listFilesCmd(listFiles func() ([]string, error), target fileListTarget, generation uint64) tea.Cmd {
	return func() tea.Msg {
		files, err := listFiles()
		return filesListedMsg{target: target, generation: generation, files: files, err: err}
	}
}
