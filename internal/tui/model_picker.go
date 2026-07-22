package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/llm"
	"atenea/internal/providerconfig"
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

// modelPicker composes TWO overlay lists — one over the providers, one over
// the selected provider's models — with a modelsFocused flag choosing which
// column the arrows move. The flag lives here, not in the lists: the overlay
// module owns single-list navigation, and a two-column widget is exactly the
// place that composes two of them.
type modelPicker struct {
	open          bool
	providers     []providerconfig.ProviderModels
	providerList  overlayList
	modelList     overlayList
	modelsFocused bool
	active        providerconfig.Active
	err           string
}

func newModelPicker(providers []providerconfig.ProviderModels, active providerconfig.Active) modelPicker {
	picker := modelPicker{open: true, providers: providerconfig.CloneProviderModels(providers), active: active}
	picker.providerList.setCount(len(picker.providers))
	for providerIndex, provider := range picker.providers {
		if provider.ID != active.ProviderID {
			continue
		}
		picker.providerList.selected = providerIndex
		for modelIndex, model := range provider.Models {
			if model == active.Model {
				picker.modelList.selected = modelIndex
				break
			}
		}
		break
	}
	picker.syncModelList()
	return picker
}

func (p *modelPicker) setProviders(providers []providerconfig.ProviderModels) {
	selectedProvider, _ := p.selectedProvider()
	selectedModel, _ := p.selectedModel()
	p.providers = providerconfig.CloneProviderModels(providers)
	p.providerList.selected = 0
	p.modelList.selected = 0
	p.providerList.setCount(len(p.providers))
	for providerIndex, provider := range p.providers {
		if provider.ID != selectedProvider.ID {
			continue
		}
		p.providerList.selected = providerIndex
		for modelIndex, model := range provider.Models {
			if model == selectedModel {
				p.modelList.selected = modelIndex
				break
			}
		}
		break
	}
	p.syncModelList()
}

// syncModelList points the model list at the currently selected provider's
// models, clamping its cursor into the new range. It runs whenever the
// selected provider changes so the model column and its cursor stay coherent.
func (p *modelPicker) syncModelList() {
	provider, ok := p.selectedProvider()
	if !ok {
		p.modelList.setCount(0)
		return
	}
	p.modelList.setCount(len(provider.Models))
}

func (p *modelPicker) move(delta int) {
	if p.modelsFocused {
		p.modelList.move(delta)
		return
	}
	p.providerList.move(delta)
	p.modelList.selected = 0
	p.syncModelList()
}

// selectProvider points the picker at a provider by index (keyboard focus
// change or a mouse click), resetting the model cursor when the provider
// actually changes so the model column reflects the new provider.
func (p *modelPicker) selectProvider(index int) {
	if p.providerList.selected != index {
		p.modelList.selected = 0
	}
	p.providerList.selected = index
	p.syncModelList()
}

// modelPickerLayout freezes the two-column panel measurements so the view and
// the mouse hit-testing share one geometry. It extends the shared overlay
// layout with the provider/model column split down the middle.
type modelPickerLayout struct {
	overlayLayout
	leftWidth  int
	rightWidth int
}

func (m Model) modelPickerLayout() modelPickerLayout {
	base := overlayLayoutFor(m.width, m.height)
	leftWidth := max(base.innerWidth/4, 18)
	leftWidth = min(leftWidth, max(base.innerWidth-1, 0))
	return modelPickerLayout{
		overlayLayout: base,
		leftWidth:     leftWidth,
		rightWidth:    max(base.innerWidth-leftWidth-1, 0),
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

// handleModelPickerKey routes the keyboard while the two-column picker is
// open: left/right (or tab) switch the focused column, up/down move within it,
// enter jumps focus into the models column and then confirms, esc closes.
func (m Model) handleModelPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.modelPicker.open = false
	case tea.KeyLeft:
		m.modelPicker.modelsFocused = false
	case tea.KeyRight:
		m.modelPicker.modelsFocused = true
	case tea.KeyTab:
		m.modelPicker.modelsFocused = !m.modelPicker.modelsFocused
	case tea.KeyUp:
		m.modelPicker.move(-1)
	case tea.KeyDown:
		m.modelPicker.move(1)
	case tea.KeyEnter:
		if !m.modelPicker.modelsFocused {
			m.modelPicker.modelsFocused = true
			return m, nil
		}
		return m.confirmModelSelection()
	}
	return m, nil
}

func (m Model) modelPickerView() string {
	layout := m.modelPickerLayout()
	innerWidth := layout.innerWidth
	leftWidth := layout.leftWidth
	rightWidth := layout.rightWidth
	itemRows := layout.itemRows

	providers := make([]string, 0, itemRows)
	providerStart, providerEnd := m.modelPicker.providerList.window(itemRows)
	for index := providerStart; index < providerEnd; index++ {
		provider := m.modelPicker.providers[index]
		prefix := "  "
		if !m.modelPicker.modelsFocused && index == m.modelPicker.providerList.selected {
			prefix = "> "
		} else if provider.ID == m.modelPicker.active.ProviderID {
			prefix = "● "
		}
		row := modelPickerProviderRow(prefix, provider.Name, len(provider.Models), leftWidth)
		if !m.modelPicker.modelsFocused && index == m.modelPicker.providerList.selected {
			row = accentStyle.Render(row)
		}
		providers = append(providers, row)
	}
	if len(m.modelPicker.providers) == 0 {
		providers = append(providers, overlayCell("  No providers", leftWidth))
	}

	models := make([]string, 0, itemRows)
	if m.modelPicker.err != "" {
		models = append(models, errorStyle.Render(overlayCell(m.modelPicker.err, rightWidth)))
	}
	selectedProvider, hasProvider := m.modelPicker.selectedProvider()
	if hasProvider {
		modelStart, modelEnd := m.modelPicker.modelList.window(itemRows - len(models))
		for index := modelStart; index < modelEnd; index++ {
			model := selectedProvider.Models[index]
			prefix := "  "
			if m.modelPicker.modelsFocused && index == m.modelPicker.modelList.selected {
				prefix = "> "
			} else if selectedProvider.ID == m.modelPicker.active.ProviderID && model == m.modelPicker.active.Model {
				prefix = "● "
			}
			row := modelPickerModelRow(prefix, model, rightWidth)
			if m.modelPicker.modelsFocused && index == m.modelPicker.modelList.selected {
				row = accentStyle.Render(row)
			}
			models = append(models, row)
		}
		if len(selectedProvider.Models) == 0 {
			models = append(models, overlayCell("  No models available", rightWidth))
		}
	} else {
		models = append(models, overlayCell("  No models available", rightWidth))
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
		overlayCell(" Providers", leftWidth) + "│" + modelPickerModelHeader(" Available models"+providerName, rightWidth),
		strings.Repeat("─", leftWidth) + "┼" + strings.Repeat("─", rightWidth),
	}
	for index := 0; index < itemRows; index++ {
		lines = append(lines, overlayCell(providers[index], leftWidth)+"│"+overlayCell(models[index], rightWidth))
	}
	lines = append(lines,
		strings.Repeat("─", leftWidth)+"┴"+strings.Repeat("─", rightWidth),
		overlayCell(" ↑↓ move · ←→ column · enter select · esc close", innerWidth),
	)

	return m.renderOverlayPanel(layout.overlayLayout, "Models", lines)
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
			start, end := m.modelPicker.providerList.window(layout.itemRows)
			index := start + row
			if index >= end {
				return m, nil
			}
			m.modelPicker.selectProvider(index)
			m.modelPicker.modelsFocused = false
			return m, nil
		}
		if _, hasProvider := m.modelPicker.selectedProvider(); !hasProvider {
			return m, nil
		}
		errRows := 0
		if m.modelPicker.err != "" {
			errRows = 1
		}
		start, end := m.modelPicker.modelList.window(layout.itemRows - errRows)
		index := start + row - errRows
		if row < errRows || index >= end {
			return m, nil
		}
		m.modelPicker.modelsFocused = true
		m.modelPicker.modelList.selected = index
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

func modelPickerModelHeader(title string, width int) string {
	nameWidth, contextWidth, priceWidth := modelPickerMetadataWidths(width)
	return overlayCell(title, nameWidth) + overlayCell("Context", contextWidth) + overlayRightCell("Price $/1M", priceWidth)
}

func modelPickerModelRow(prefix, model string, width int) string {
	nameWidth, contextWidth, priceWidth := modelPickerMetadataWidths(width)
	return overlayCell(prefix+sanitizeTerminalText(model), nameWidth) +
		overlayCell(modelContextLabel(model), contextWidth) +
		overlayRightCell(modelPriceLabel(model), priceWidth)
}

func modelPickerMetadataWidths(width int) (int, int, int) {
	priceWidth := min(12, max(width/4, 0))
	contextWidth := min(9, max((width-priceWidth)/4, 0))
	return max(width-contextWidth-priceWidth, 0), contextWidth, priceWidth
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

func (p modelPicker) selectedProvider() (providerconfig.ProviderModels, bool) {
	index, ok := p.providerList.hasSelection()
	if !ok || index >= len(p.providers) {
		return providerconfig.ProviderModels{}, false
	}
	return p.providers[index], true
}

func (p modelPicker) selectedModel() (string, bool) {
	provider, ok := p.selectedProvider()
	if !ok {
		return "", false
	}
	index, ok := p.modelList.hasSelection()
	if !ok || index >= len(provider.Models) {
		return "", false
	}
	return provider.Models[index], true
}
