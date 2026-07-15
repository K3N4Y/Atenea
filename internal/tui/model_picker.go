package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/providerconfig"
)

type modelPicker struct {
	open             bool
	providers        []providerconfig.ProviderModels
	providerSelected int
	modelSelected    int
	modelsFocused    bool
	active           providerconfig.Active
	err              string
}

func newModelPicker(providers []providerconfig.ProviderModels, active providerconfig.Active) modelPicker {
	picker := modelPicker{open: true, providers: cloneProviderModels(providers), active: active}
	for providerIndex, provider := range picker.providers {
		if provider.ID != active.ProviderID {
			continue
		}
		picker.providerSelected = providerIndex
		for modelIndex, model := range provider.Models {
			if model == active.Model {
				picker.modelSelected = modelIndex
				break
			}
		}
		break
	}
	return picker
}

func (p *modelPicker) setProviders(providers []providerconfig.ProviderModels) {
	selectedProvider, _ := p.selectedProvider()
	selectedModel, _ := p.selectedModel()
	p.providers = cloneProviderModels(providers)
	p.providerSelected = 0
	p.modelSelected = 0
	for providerIndex, provider := range p.providers {
		if provider.ID != selectedProvider.ID {
			continue
		}
		p.providerSelected = providerIndex
		for modelIndex, model := range provider.Models {
			if model == selectedModel {
				p.modelSelected = modelIndex
				break
			}
		}
		break
	}
}

func (p *modelPicker) move(delta int) {
	count := len(p.providers)
	if p.modelsFocused {
		provider, ok := p.selectedProvider()
		if !ok {
			return
		}
		count = len(provider.Models)
	}
	if count == 0 {
		return
	}
	if p.modelsFocused {
		p.modelSelected = wrapSelection(p.modelSelected+delta, count)
		return
	}
	p.providerSelected = wrapSelection(p.providerSelected+delta, count)
	p.modelSelected = 0
}

func wrapSelection(value, count int) int {
	value %= count
	if value < 0 {
		value += count
	}
	return value
}

func (m Model) modelPickerView() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	bodyHeight := 0
	if m.ready {
		bodyHeight = max(m.height-4, 0)
	}
	itemRows := 0
	if bodyHeight > 0 {
		itemRows = max(bodyHeight-4, 0)
	}
	contentWidth := max(width-2*composerOuterMargin, 0)
	gap := 1
	leftWidth := max(contentWidth/3, 18)
	leftWidth = min(leftWidth, max(contentWidth-gap, 0))
	rightWidth := max(contentWidth-leftWidth-gap, 0)

	providers := []string{"Providers", ""}
	providerStart, providerEnd := modelPickerWindow(len(m.modelPicker.providers), m.modelPicker.providerSelected, itemRows)
	for index := providerStart; index < providerEnd; index++ {
		provider := m.modelPicker.providers[index]
		prefix := "  "
		if !m.modelPicker.modelsFocused && index == m.modelPicker.providerSelected {
			prefix = "❯ "
		} else if provider.ID == m.modelPicker.active.ProviderID {
			prefix = "● "
		}
		providers = append(providers, modelPickerProviderRow(prefix, provider.Name, len(provider.Models), leftWidth-composerBoxBorderWidth))
	}
	if len(m.modelPicker.providers) == 0 {
		providers = append(providers, "  No providers available")
	}

	models := []string{"Models", ""}
	if m.modelPicker.err != "" {
		models = append(models, errorStyle.Render(sanitizeTerminalText(m.modelPicker.err)), "")
	}
	if provider, ok := m.modelPicker.selectedProvider(); ok {
		modelStart, modelEnd := modelPickerWindow(len(provider.Models), m.modelPicker.modelSelected, itemRows)
		for index := modelStart; index < modelEnd; index++ {
			model := provider.Models[index]
			prefix := "  "
			if m.modelPicker.modelsFocused && index == m.modelPicker.modelSelected {
				prefix = "❯ "
			} else if provider.ID == m.modelPicker.active.ProviderID && model == m.modelPicker.active.Model {
				prefix = "● "
			}
			models = append(models, prefix+sanitizeTerminalText(model))
		}
		if len(provider.Models) == 0 {
			models = append(models, "  No models available")
		}
	} else {
		models = append(models, "  No models available")
	}

	left := modelPickerColumn(strings.Join(providers, "\n"), leftWidth, bodyHeight)
	right := modelPickerColumn(strings.Join(models, "\n"), rightWidth, bodyHeight)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), right)
	title := accentStyle.Render("Select model")
	help := statusStyle.Render("↑↓ move · ←→ switch column · enter select · esc close")
	margin := strings.Repeat(" ", min(composerOuterMargin, width))
	body = lipgloss.NewStyle().MarginLeft(len(margin)).Render(body)
	return m.renderFullCanvas("\n" + ansi.Truncate(margin+title, width, "") + "\n" + ansi.Truncate(margin+help, width, "") + "\n\n" + body)
}

func modelPickerProviderRow(prefix, name string, count, width int) string {
	countText := strconv.Itoa(count)
	nameWidth := max(width-lipgloss.Width(prefix)-lipgloss.Width(countText)-2, 0)
	name = ansi.Truncate(sanitizeTerminalText(name), nameWidth, "…")
	row := prefix + name
	gap := max(width-lipgloss.Width(row)-lipgloss.Width(countText)-1, 0)
	return ansi.Truncate(row+strings.Repeat(" ", gap)+countText+" ", width, "")
}

func modelPickerWindow(total, selected, visible int) (int, int) {
	if visible <= 0 || total <= visible {
		return 0, total
	}
	start := resumePickerWindowStart(total, selected, visible)
	return start, min(start+visible, total)
}

func modelPickerColumn(content string, width, height int) string {
	if width <= composerBoxBorderWidth {
		return ansi.Truncate(content, max(width, 0), "")
	}
	lines := strings.Split(content, "\n")
	if height > composerBoxBorderWidth && len(lines) > height-composerBoxBorderWidth {
		lines = lines[:height-composerBoxBorderWidth]
	}
	for index, line := range lines {
		lines[index] = ansi.Truncate(line, width-composerBoxBorderWidth, "…")
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Width(width - composerBoxBorderWidth)
	if height > composerBoxBorderWidth {
		style = style.Height(height - composerBoxBorderWidth)
	}
	return style.Render(strings.Join(lines, "\n"))
}

func (p modelPicker) selectedProvider() (providerconfig.ProviderModels, bool) {
	if p.providerSelected < 0 || p.providerSelected >= len(p.providers) {
		return providerconfig.ProviderModels{}, false
	}
	return p.providers[p.providerSelected], true
}

func (p modelPicker) selectedModel() (string, bool) {
	provider, ok := p.selectedProvider()
	if !ok || p.modelSelected < 0 || p.modelSelected >= len(provider.Models) {
		return "", false
	}
	return provider.Models[p.modelSelected], true
}
