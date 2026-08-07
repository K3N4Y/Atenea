package tui

import (
	"github.com/K3N4Y/atenea/internal/llm"
	tea "github.com/charmbracelet/bubbletea"
)

type variantsPicker struct {
	open     bool
	selected int
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

func (p *variantsPicker) close() { p.open = false }

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
		agent, ok := m.agent.(reasoningAgent)
		if !ok {
			m.variantsPicker.close()
			return m.appendError("reasoning selection is unavailable"), nil
		}
		if err := agent.SetReasoningEffort(reasoningVariants[m.variantsPicker.selected]); err != nil {
			m.variantsPicker.close()
			return m.appendError(err.Error()), nil
		}
		label := string(reasoningVariants[m.variantsPicker.selected])
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
