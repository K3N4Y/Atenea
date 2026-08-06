package tui

import tea "github.com/charmbracelet/bubbletea"

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Pickers cover the whole screen and receive unshifted coordinates. The
	// permission gate belongs to the body, so it is routed only after removing
	// the top-bar offset. Plan approval intentionally has no pointer shortcut.
	switch m.activeInputTarget() {
	case targetResumePicker:
		return m, nil
	case targetModelPicker:
		return m.handleModelPickerMouse(msg)
	case targetMCPPicker:
		return m.handleMCPPickerMouse(msg)
	case targetSkillsPicker:
		return m.handleSkillsPickerMouse(msg)
	case targetConnectPanel:
		return m.handleConnectPanelMouse(msg)
	}
	msg.Y -= m.layout().mouseBodyYOffset
	if m.selection != nil {
		switch {
		case msg.Action == tea.MouseActionMotion && msg.Button == tea.MouseButtonLeft:
			return m.dragSelection(msg), nil
		case msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft:
			if m.snackbarHit(msg) {
				return m.cancelSelection(), nil
			}
			m = m.dragSelection(msg)
			return m.finishSelection()
		case msg.Action == tea.MouseActionRelease:
			return m.cancelSelection(), nil
		}
	}
	if m.snackbarHit(msg) && msg.Button != tea.MouseButtonWheelUp && msg.Button != tea.MouseButtonWheelDown {
		return m, nil
	}
	if m.activeInputTarget() == targetPermissionGate {
		perm, _ := m.pendingPermission()
		if next, handled := m.handlePermissionMouse(msg, perm); handled {
			return next, nil
		}
	}
	if m.newActivityIndicatorHit(msg) {
		return m, nil
	}
	if msg.Action == tea.MouseActionPress && (msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown) {
		m = m.cancelSelection()
		return m.scrollViewport(msg)
	}
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		if next, started := m.startSelection(msg); started {
			return next, nil
		}
		if viewportLine, ok := m.transcriptLineAtMouse(msg); ok {
			if next, ok := m.toggleExpandableAt(viewportLine); ok {
				return next.syncViewport(), nil
			}
		}
	}
	return m.scrollViewport(msg)
}

func (m Model) newActivityIndicatorHit(msg tea.MouseMsg) bool {
	if !m.hasNewActivity || !m.ready || msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return false
	}
	return msg.X == m.viewport.Width-1 && msg.Y == m.viewport.Height-1
}

func (m Model) scrollViewport(msg tea.Msg) (Model, tea.Cmd) {
	m = m.cancelSelection()
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	m.followAgent = m.viewport.AtBottom()
	if m.followAgent {
		m.hasNewActivity = false
	}
	return m, cmd
}
