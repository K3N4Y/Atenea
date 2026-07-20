package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/llm"
	"atenea/internal/providerconfig"
	"atenea/internal/tui/theme"
)

var modelPrices = map[string]string{
	"gpt-5.6":       "$5/$30",
	"gpt-5.6-terra": "$7.5/$45",
	"gpt-5.6-luna":  "$0.25/$2",
	"gpt-5.4-mini":  "$0.75/$4.5",
	"gpt-5.4-nano":  "$0.20/$1.25",
	"gpt-5":         "$1.25/$10",
	"gpt-5-mini":    "$0.25/$2",
	"gpt-4.1":       "$2/$8",
	"gpt-4.1-mini":  "$0.40/$1.60",
	"gpt-4.1-nano":  "$0.10/$0.40",
	"gpt-4o":        "$2.50/$10",
	"gpt-4o-mini":   "$0.15/$0.60",
}

type modelPicker struct {
	open             bool
	providers        []providerconfig.ProviderModels
	providerSelected int
	modelSelected    int
	modelsFocused    bool
	active           providerconfig.Active
	err              string
}

// cloneProviderModels deep-copies the catalog before the picker keeps it, so
// selection bookkeeping never mutates the slice the model service still owns.
func cloneProviderModels(in []providerconfig.ProviderModels) []providerconfig.ProviderModels {
	out := make([]providerconfig.ProviderModels, len(in))
	for i, provider := range in {
		out[i] = provider
		out[i].Models = append([]string(nil), provider.Models...)
	}
	return out
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

// modelPickerLayout congela las medidas del panel para que la vista y el
// hit-testing del raton compartan la misma geometria.
type modelPickerLayout struct {
	marginLeft  int
	innerWidth  int
	innerHeight int
	leftWidth   int
	rightWidth  int
	itemRows    int
}

func (m Model) modelPickerLayout() modelPickerLayout {
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 16
	}
	innerWidth := max(width-2*composerOuterMargin-composerBoxBorderWidth, 0)
	innerHeight := max(max(height-2, 0)-composerBoxBorderWidth, 0)
	leftWidth := max(innerWidth/4, 18)
	leftWidth = min(leftWidth, max(innerWidth-1, 0))
	return modelPickerLayout{
		marginLeft:  min(composerOuterMargin, width),
		innerWidth:  innerWidth,
		innerHeight: innerHeight,
		leftWidth:   leftWidth,
		rightWidth:  max(innerWidth-leftWidth-1, 0),
		itemRows:    max(innerHeight-4, 0),
	}
}

// rowAt traduce coordenadas de pantalla a una fila visible del panel. La
// pantalla del picker es: fila 0 en blanco, 1 borde superior, 2 cabecera,
// 3 separador y de ahi las filas de items; en X, el contenido empieza tras
// el margen y el borde izquierdo, con la columna divisoria entre listas.
func (l modelPickerLayout) rowAt(x, y int) (overModels bool, row int, ok bool) {
	row = y - 4
	if row < 0 || row >= l.itemRows {
		return false, 0, false
	}
	x -= l.marginLeft + 1
	if x >= 0 && x < l.leftWidth {
		return false, row, true
	}
	if x > l.leftWidth && x <= l.leftWidth+l.rightWidth {
		return true, row, true
	}
	return false, 0, false
}

func (m Model) modelPickerView() string {
	layout := m.modelPickerLayout()
	innerWidth := layout.innerWidth
	leftWidth := layout.leftWidth
	rightWidth := layout.rightWidth
	itemRows := layout.itemRows

	providers := make([]string, 0, itemRows)
	providerStart, providerEnd := modelPickerWindow(len(m.modelPicker.providers), m.modelPicker.providerSelected, itemRows)
	for index := providerStart; index < providerEnd; index++ {
		provider := m.modelPicker.providers[index]
		prefix := "  "
		if !m.modelPicker.modelsFocused && index == m.modelPicker.providerSelected {
			prefix = "> "
		} else if provider.ID == m.modelPicker.active.ProviderID {
			prefix = "● "
		}
		row := modelPickerProviderRow(prefix, provider.Name, len(provider.Models), leftWidth)
		if !m.modelPicker.modelsFocused && index == m.modelPicker.providerSelected {
			row = accentStyle.Render(row)
		}
		providers = append(providers, row)
	}
	if len(m.modelPicker.providers) == 0 {
		providers = append(providers, modelPickerCell("  No providers", leftWidth))
	}

	models := make([]string, 0, itemRows)
	if m.modelPicker.err != "" {
		models = append(models, errorStyle.Render(modelPickerCell(m.modelPicker.err, rightWidth)))
	}
	selectedProvider, hasProvider := m.modelPicker.selectedProvider()
	if hasProvider {
		modelStart, modelEnd := modelPickerWindow(len(selectedProvider.Models), m.modelPicker.modelSelected, itemRows-len(models))
		for index := modelStart; index < modelEnd; index++ {
			model := selectedProvider.Models[index]
			prefix := "  "
			if m.modelPicker.modelsFocused && index == m.modelPicker.modelSelected {
				prefix = "> "
			} else if selectedProvider.ID == m.modelPicker.active.ProviderID && model == m.modelPicker.active.Model {
				prefix = "● "
			}
			row := modelPickerModelRow(prefix, model, rightWidth)
			if m.modelPicker.modelsFocused && index == m.modelPicker.modelSelected {
				row = accentStyle.Render(row)
			}
			models = append(models, row)
		}
		if len(selectedProvider.Models) == 0 {
			models = append(models, modelPickerCell("  No models available", rightWidth))
		}
	} else {
		models = append(models, modelPickerCell("  No models available", rightWidth))
	}

	for len(providers) < itemRows {
		providers = append(providers, strings.Repeat(" ", leftWidth))
	}
	for len(models) < itemRows {
		models = append(models, strings.Repeat(" ", rightWidth))
	}

	providerName := ""
	if hasProvider {
		providerName = " · " + sanitizeTerminalText(selectedProvider.Name)
	}
	lines := []string{
		modelPickerCell(" Providers", leftWidth) + "│" + modelPickerModelHeader(" Available models"+providerName, rightWidth),
		strings.Repeat("─", leftWidth) + "┼" + strings.Repeat("─", rightWidth),
	}
	for index := 0; index < itemRows; index++ {
		lines = append(lines, modelPickerCell(providers[index], leftWidth)+"│"+modelPickerCell(models[index], rightWidth))
	}
	lines = append(lines,
		strings.Repeat("─", leftWidth)+"┴"+strings.Repeat("─", rightWidth),
		modelPickerCell(" ↑↓ move · ←→ column · enter select · esc close", innerWidth),
	)

	panelStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(theme.Border)).
		Width(innerWidth)
	if layout.innerHeight > 0 {
		panelStyle = panelStyle.Height(layout.innerHeight)
	}
	panel := pickerPanelTitle(panelStyle.Render(strings.Join(lines, "\n")), "Models")
	panel = lipgloss.NewStyle().MarginLeft(layout.marginLeft).Render(panel)
	return m.renderFullCanvas("\n" + panel)
}

// handleModelPickerMouse traduce el raton a las mismas acciones del teclado:
// la rueda mueve la seleccion de la columna con foco, un clic en la columna
// de providers la selecciona y un clic sobre un modelo lo confirma (el mismo
// camino que enter). El picker se pinta a pantalla completa sin top bar, asi
// que las coordenadas llegan sin trasladar.
func (m Model) handleModelPickerMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.modelPicker.move(-1)
	case tea.MouseButtonWheelDown:
		m.modelPicker.move(1)
	case tea.MouseButtonLeft:
		layout := m.modelPickerLayout()
		overModels, row, ok := layout.rowAt(msg.X, msg.Y)
		if !ok {
			return m, nil
		}
		if !overModels {
			start, end := modelPickerWindow(len(m.modelPicker.providers), m.modelPicker.providerSelected, layout.itemRows)
			index := start + row
			if index >= end {
				return m, nil
			}
			if m.modelPicker.providerSelected != index {
				m.modelPicker.modelSelected = 0
			}
			m.modelPicker.providerSelected = index
			m.modelPicker.modelsFocused = false
			return m, nil
		}
		provider, hasProvider := m.modelPicker.selectedProvider()
		if !hasProvider {
			return m, nil
		}
		errRows := 0
		if m.modelPicker.err != "" {
			errRows = 1
		}
		start, end := modelPickerWindow(len(provider.Models), m.modelPicker.modelSelected, layout.itemRows-errRows)
		index := start + row - errRows
		if row < errRows || index >= end {
			return m, nil
		}
		m.modelPicker.modelsFocused = true
		m.modelPicker.modelSelected = index
		return m.confirmModelSelection()
	}
	return m, nil
}

// confirmModelSelection aplica el modelo seleccionado (enter o clic) y cierra
// el picker; los fallos quedan visibles en el propio panel via err.
func (m Model) confirmModelSelection() (Model, tea.Cmd) {
	provider, providerOK := m.modelPicker.selectedProvider()
	model, modelOK := m.modelPicker.selectedModel()
	if !providerOK || !modelOK {
		return m, nil
	}
	controller, ok := m.agent.(modelAgent)
	if !ok {
		m.modelPicker.err = "model selection is unavailable"
		return m, nil
	}
	active, err := controller.SelectModel(provider.ID, model)
	if err != nil {
		m.modelPicker.err = err.Error()
		return m, nil
	}
	m.model = active.Model
	m.modelPicker.open = false
	return m, nil
}

func modelPickerProviderRow(prefix, name string, count, width int) string {
	countText := strconv.Itoa(count)
	nameWidth := max(width-lipgloss.Width(prefix)-lipgloss.Width(countText)-2, 0)
	name = ansi.Truncate(sanitizeTerminalText(name), nameWidth, "…")
	row := prefix + name
	gap := max(width-lipgloss.Width(row)-lipgloss.Width(countText)-1, 0)
	return ansi.Truncate(row+strings.Repeat(" ", gap)+countText+" ", width, "")
}

func modelPickerCell(value string, width int) string {
	value = ansi.Truncate(value, max(width, 0), "…")
	return value + strings.Repeat(" ", max(width-lipgloss.Width(value), 0))
}

func modelPickerModelHeader(title string, width int) string {
	nameWidth, contextWidth, priceWidth := modelPickerMetadataWidths(width)
	return modelPickerCell(title, nameWidth) + modelPickerCell("Context", contextWidth) + modelPickerRightCell("Price $/1M", priceWidth)
}

func modelPickerModelRow(prefix, model string, width int) string {
	nameWidth, contextWidth, priceWidth := modelPickerMetadataWidths(width)
	return modelPickerCell(prefix+sanitizeTerminalText(model), nameWidth) +
		modelPickerCell(modelContextLabel(model), contextWidth) +
		modelPickerRightCell(modelPriceLabel(model), priceWidth)
}

func modelPickerMetadataWidths(width int) (int, int, int) {
	priceWidth := min(12, max(width/4, 0))
	contextWidth := min(9, max((width-priceWidth)/4, 0))
	return max(width-contextWidth-priceWidth, 0), contextWidth, priceWidth
}

func modelPickerRightCell(value string, width int) string {
	value = ansi.Truncate(value, max(width, 0), "…")
	return strings.Repeat(" ", max(width-lipgloss.Width(value), 0)) + value
}

func modelContextLabel(model string) string {
	if window, ok := llm.ContextWindow(model); ok {
		if window >= 1_000_000 {
			value := strconv.FormatFloat(float64(window)/1_000_000, 'f', 2, 64)
			return strings.TrimRight(strings.TrimRight(value, "0"), ".") + "M"
		}
		if window >= 1_000 {
			return strconv.Itoa((window+500)/1_000) + "K"
		}
		return strconv.Itoa(window)
	}
	if context := curatedModelContext[model]; context != "" {
		return context
	}
	return "—"
}

func modelPriceLabel(model string) string {
	if price := modelPrices[model]; price != "" {
		return price
	}
	if strings.HasSuffix(model, ":free") {
		return "free"
	}
	return "—"
}

// pickerPanelTitle incrusta el titulo en el borde superior del panel; lo
// comparten el picker de modelos y el de MCPs.
func pickerPanelTitle(panel, title string) string {
	lines := strings.Split(panel, "\n")
	if len(lines) == 0 {
		return panel
	}
	width := ansi.StringWidth(lines[0])
	remaining := max(width-ansi.StringWidth(title)-5, 0)
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Border))
	lines[0] = border.Render("┌─ ") + accentStyle.Render(title) + border.Render(" "+strings.Repeat("─", remaining)+"┐")
	return strings.Join(lines, "\n")
}

func modelPickerWindow(total, selected, visible int) (int, int) {
	if visible <= 0 || total <= visible {
		return 0, total
	}
	start := resumePickerWindowStart(total, selected, visible)
	return start, min(start+visible, total)
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
