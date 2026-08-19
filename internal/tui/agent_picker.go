package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/agent"
)

type agentPicker struct {
	open   bool
	agents []agent.Def
	overlayList
}

func newAgentPicker(agents []agent.Def) agentPicker {
	picker := agentPicker{open: true, agents: cloneAgentDefs(agents)}
	picker.setCount(len(picker.agents))
	return picker
}

func cloneAgentDefs(defs []agent.Def) []agent.Def {
	cloned := append([]agent.Def(nil), defs...)
	for i := range cloned {
		cloned[i].Tools = append([]string(nil), cloned[i].Tools...)
	}
	return cloned
}

func (m Model) handleAgentPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.agentPicker.open = false
	case tea.KeyUp:
		m.agentPicker.move(-1)
	case tea.KeyDown:
		m.agentPicker.move(1)
	case tea.KeyEnter:
		if m.agentPicker.selected < 0 || m.agentPicker.selected >= len(m.agentPicker.agents) {
			return m, nil
		}
		def := m.agentPicker.agents[m.agentPicker.selected]
		controller, ok := m.agent.(modelAgent)
		if !ok {
			return m.appendError("model selection is unavailable"), nil
		}
		active := controller.CurrentModel()
		if selection, ok := m.agent.(agentModelAgent); ok {
			if effective, configured := selection.EffectiveAgentModel(def.Name, def.Model); configured {
				active.ProviderID, active.Model = effective.Provider, effective.Model
			}
		}
		m.agentPicker.open = false
		m.modelPicker = newAgentModelPicker(controller.ModelCatalog(), active, def.Name)
		controller.RefreshModels()
	}
	return m, nil
}

func (m Model) agentPickerView() string {
	layout := overlayLayoutFor(m.width, m.height)
	lines := []string{" Agent / role                     Resolution", strings.Repeat("─", layout.innerWidth)}
	controller, _ := m.agent.(agentModelAgent)
	start, end := m.agentPicker.window(layout.itemRows)
	for i := start; i < end; i++ {
		def := m.agentPicker.agents[i]
		resolution := "inherits"
		if controller != nil {
			if configured, overridden := controller.AgentModel(def.Name); overridden {
				effective, ok := controller.EffectiveAgentModel(def.Name, def.Model)
				if ok {
					resolution = effective.Provider + "/" + effective.Model
				} else {
					resolution = configured.Model
				}
				if configured.ReasoningEffort != "" {
					resolution += " (" + string(configured.ReasoningEffort) + ")"
				}
				resolution += " (override)"
			} else if def.Model != "" {
				if effective, ok := controller.EffectiveAgentModel(def.Name, def.Model); ok {
					resolution = effective.Provider + "/" + effective.Model + " (manifest)"
				}
			}
		} else if def.Model != "" {
			resolution = def.Model + " (manifest)"
		}
		prefix := "  "
		if i == m.agentPicker.selected {
			prefix = "❯ "
		}
		row := fmt.Sprintf("%s%s — %s", prefix, sanitizeAgentPickerText(def.Name), sanitizeAgentPickerText(resolution))
		if def.Description != "" {
			row += " · " + sanitizeAgentPickerText(def.Description)
		}
		lines = append(lines, overlayCell(row, layout.innerWidth))
	}
	if len(m.agentPicker.agents) == 0 && layout.itemRows > 0 {
		lines = append(lines, overlayCell("  No agent roles available", layout.innerWidth))
	}
	for len(lines) < layout.itemRows+2 {
		lines = append(lines, strings.Repeat(" ", layout.innerWidth))
	}
	lines = append(lines, strings.Repeat("─", layout.innerWidth))
	lines = append(lines, overlayCell(" ↑↓ move · enter configure · esc close", layout.innerWidth))
	return m.renderOverlayPanel(layout, "Agents", lines)
}

func sanitizeAgentPickerText(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(sanitizeTerminalText(value))
}

func (m Model) handleAgentPickerMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.agentPicker.move(-1)
	case tea.MouseButtonWheelDown:
		m.agentPicker.move(1)
	case tea.MouseButtonLeft:
		layout := overlayLayoutFor(m.width, m.height)
		row, ok := layout.rowAt(msg.X, msg.Y)
		if !ok {
			return m, nil
		}
		start, end := m.agentPicker.window(layout.itemRows)
		index := start + row
		if index >= end {
			return m, nil
		}
		m.agentPicker.selected = index
		next, cmd := m.handleAgentPickerKey(tea.KeyMsg{Type: tea.KeyEnter})
		return next.(Model), cmd
	}
	return m, nil
}
