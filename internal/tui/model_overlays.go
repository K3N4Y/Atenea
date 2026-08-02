package tui

import tea "github.com/charmbracelet/bubbletea"

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
