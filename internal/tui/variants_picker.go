package tui

import (
	"context"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	tea "github.com/charmbracelet/bubbletea"
)

type variantsPicker struct {
	open    bool
	current llm.ReasoningEffort
	agent   *agentReasoningTarget
	overlayList
}

type agentReasoningTarget struct {
	name      string
	selection providerconfig.AgentModelSelection
}

var reasoningVariants = []llm.ReasoningEffort{
	"",
	llm.ReasoningEffortMinimal,
	llm.ReasoningEffortLow,
	llm.ReasoningEffortMedium,
	llm.ReasoningEffortHigh,
	llm.ReasoningEffortXHigh,
	llm.ReasoningEffortMax,
}

func (p *variantsPicker) openAt(current llm.ReasoningEffort) {
	p.open = true
	p.current = current
	p.agent = nil
	p.selected = 0
	p.setCount(len(reasoningVariants))
	for i, variant := range reasoningVariants {
		if variant == current {
			p.selected = i
			return
		}
	}
}

func (p *variantsPicker) openAgent(agentName, provider, model string, effort llm.ReasoningEffort) {
	p.openAt(effort)
	p.agent = &agentReasoningTarget{
		name: agentName,
		selection: providerconfig.AgentModelSelection{
			Provider: provider,
			Model:    model,
		},
	}
}

func (p *variantsPicker) close() {
	*p = variantsPicker{}
}

func (p variantsPicker) selectedEffort() (llm.ReasoningEffort, bool) {
	selected, ok := p.hasSelection()
	if !ok || selected >= len(reasoningVariants) {
		return "", false
	}
	return reasoningVariants[selected], true
}

func (m Model) handleVariantsPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.variantsPicker.close()
	case tea.KeyUp:
		m.variantsPicker.move(-1)
	case tea.KeyDown:
		m.variantsPicker.move(1)
	case tea.KeyEnter:
		return m.confirmVariantsPickerSelection()
	}
	return m, nil
}

func (m Model) handleVariantsPickerMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.variantsPicker.move(-1)
	case tea.MouseButtonWheelDown:
		m.variantsPicker.move(1)
	case tea.MouseButtonLeft:
		layout := variantsPickerLayoutFor(m.width, m.height, m.variantsPicker.agent != nil)
		row, ok := layout.rowAt(msg.X, msg.Y)
		if !ok {
			return m, nil
		}
		start, end := m.variantsPicker.window(layout.itemRows)
		selected := start + row
		if selected >= end {
			return m, nil
		}
		m.variantsPicker.selected = selected
		return m.confirmVariantsPickerSelection()
	}
	return m, nil
}

func (m Model) confirmVariantsPickerSelection() (Model, tea.Cmd) {
	effort, ok := m.variantsPicker.selectedEffort()
	if !ok {
		return m, nil
	}
	if target := m.variantsPicker.agent; target != nil {
		controller, ok := m.agent.(agentModelAgent)
		if !ok {
			m.variantsPicker.close()
			return m.appendError("agent model selection is unavailable").syncViewport(), nil
		}
		name := target.name
		selection := target.selection
		selection.ReasoningEffort = effort
		m.variantsPicker.close()
		return m, setAgentModelCmd(controller, name, selection)
	}
	controller, ok := m.agent.(reasoningAgent)
	if !ok {
		m.variantsPicker.close()
		return m.appendError("reasoning selection is unavailable").syncViewport(), nil
	}
	if err := controller.SetReasoningEffort(effort); err != nil {
		m.variantsPicker.close()
		return m.appendError(err.Error()).syncViewport(), nil
	}
	m.variantsPicker.close()
	m.Transcript = m.Transcript.appendNotice("reasoning effort: " + reasoningEffortLabel(effort))
	return m.syncViewport(), nil
}

func reasoningEffortLabel(effort llm.ReasoningEffort) string {
	if effort == "" {
		return "default"
	}
	return string(effort)
}

func variantLabel(variant llm.ReasoningEffort) string {
	if variant == "" {
		return "provider default"
	}
	return string(variant)
}

type agentModelSetMsg struct {
	name string
	err  error
}

func setAgentModelCmd(controller agentModelAgent, name string, selection providerconfig.AgentModelSelection) tea.Cmd {
	return func() tea.Msg {
		return agentModelSetMsg{name: name, err: controller.SetAgentModel(context.Background(), name, selection)}
	}
}
