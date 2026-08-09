package tui

import (
	"context"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	tea "github.com/charmbracelet/bubbletea"
)

type variantsPicker struct {
	open          bool
	selected      int
	agentName     string
	agentProvider string
	agentModel    string
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
	p.selected = 0
	for i, variant := range reasoningVariants {
		if variant == current {
			p.selected = i
			return
		}
	}
}

func (p *variantsPicker) openAgent(agentName, provider, model string, effort llm.ReasoningEffort) {
	p.openAt(effort)
	p.agentName = agentName
	p.agentProvider = provider
	p.agentModel = model
}

func (p *variantsPicker) close() {
	p.open = false
	p.agentName = ""
	p.agentProvider = ""
	p.agentModel = ""
}

func (p *variantsPicker) move(delta int) {
	p.selected = (p.selected + delta) % len(reasoningVariants)
	if p.selected < 0 {
		p.selected += len(reasoningVariants)
	}
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
		effort := reasoningVariants[m.variantsPicker.selected]
		if m.variantsPicker.agentName != "" {
			controller, ok := m.agent.(agentModelAgent)
			if !ok {
				m.variantsPicker.close()
				return m.appendError("agent model selection is unavailable"), nil
			}
			name := m.variantsPicker.agentName
			selection := providerconfig.AgentModelSelection{
				Provider: m.variantsPicker.agentProvider, Model: m.variantsPicker.agentModel, ReasoningEffort: effort,
			}
			m.variantsPicker.close()
			return m, setAgentModelCmd(controller, name, selection)
		}
		agent, ok := m.agent.(reasoningAgent)
		if !ok {
			m.variantsPicker.close()
			return m.appendError("reasoning selection is unavailable"), nil
		}
		if err := agent.SetReasoningEffort(effort); err != nil {
			m.variantsPicker.close()
			return m.appendError(err.Error()), nil
		}
		label := string(effort)
		if label == "" {
			label = "default"
		}
		m.variantsPicker.close()
		m.Transcript = m.Transcript.appendNotice("reasoning effort: " + label)
	}
	return m, nil
}

func variantLabel(variant llm.ReasoningEffort) string {
	if variant == "" {
		return "default"
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
