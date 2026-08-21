package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/cellbuf"

	"github.com/K3N4Y/atenea/internal/tui/theme"
)

const variantsPickerMaxWidth = 34

type variantsPickerLayout struct {
	modalWidth  int
	innerWidth  int
	modalHeight int
	left        int
	top         int
	itemOffset  int
	itemRows    int
	showContext bool
	showFooter  bool
}

func variantsPickerLayoutFor(width, height int, hasAgentContext bool) variantsPickerLayout {
	availableWidth := max(width, 1)
	modalWidth := min(variantsPickerMaxWidth, availableWidth)
	if availableWidth >= 6 {
		modalWidth = min(modalWidth, availableWidth-2)
	}
	modalWidth = max(modalWidth, 2)
	innerWidth := max(modalWidth-2, 0)

	availableHeight := max(height, 1)
	showFooter := availableHeight >= 4
	chromeRows := 2 // top and bottom borders
	if showFooter {
		chromeRows++
	}
	showContext := hasAgentContext && availableHeight >= len(reasoningVariants)+chromeRows+1
	if showContext {
		chromeRows++
	}
	itemRows := min(len(reasoningVariants), max(availableHeight-chromeRows, 0))
	modalHeight := chromeRows + itemRows
	itemOffset := 1
	if showContext {
		itemOffset++
	}

	return variantsPickerLayout{
		modalWidth:  modalWidth,
		innerWidth:  innerWidth,
		modalHeight: modalHeight,
		left:        max((width-modalWidth)/2, 0),
		top:         max((height-modalHeight)/2, 0),
		itemOffset:  itemOffset,
		itemRows:    itemRows,
		showContext: showContext,
		showFooter:  showFooter,
	}
}

func (l variantsPickerLayout) rowAt(x, y int) (int, bool) {
	row := y - l.top - l.itemOffset
	if row < 0 || row >= l.itemRows {
		return 0, false
	}
	x -= l.left + 1
	if x < 0 || x >= l.innerWidth {
		return 0, false
	}
	return row, true
}

func (m Model) variantsPickerView(base string) string {
	layout := variantsPickerLayoutFor(m.width, m.height, m.variantsPicker.agent != nil)
	background := lipgloss.Color(theme.UserMessage)
	modalBackground := lipgloss.NewStyle().Background(background)
	borderStyle := secondaryTextStyle.Background(background)
	titleStyle := primaryTextStyle.Bold(true).Background(background)
	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color(theme.PermissionCommand))
	rows := make([]string, 0, layout.modalHeight)
	rows = append(rows, variantsPickerTopBorder(layout.innerWidth, borderStyle, titleStyle))
	if layout.showContext {
		context := overlayCell(" "+m.variantsPicker.contextLabel(), layout.innerWidth)
		rows = append(rows, borderedVariantRow(metadataStyle.Background(background).Render(context), borderStyle))
	}
	start, end := m.variantsPicker.window(layout.itemRows)
	for i := start; i < end; i++ {
		variant := reasoningVariants[i]
		cursor := " "
		if i == m.variantsPicker.selected {
			cursor = "›"
		}
		current := " "
		if variant == m.variantsPicker.current {
			current = "●"
		}
		label := overlayCell(cursor+" "+current+" "+variantLabel(variant), layout.innerWidth)
		if i == m.variantsPicker.selected {
			label = selectedStyle.Render(label)
		} else {
			label = modalBackground.Render(label)
		}
		rows = append(rows, borderedVariantRow(label, borderStyle))
	}
	if layout.showFooter {
		footer := secondaryTextStyle.Background(background).
			Render(overlayCell("↑↓ move · enter select · esc close", layout.innerWidth))
		rows = append(rows, borderedVariantRow(footer, borderStyle))
	}
	rows = append(rows, borderStyle.Render("└"+strings.Repeat("─", layout.innerWidth)+"┘"))

	return placeModal(base, strings.Join(rows, "\n"), m.width, m.height)
}

func variantsPickerTopBorder(innerWidth int, borderStyle, titleStyle lipgloss.Style) string {
	title := " Reasoning effort "
	if lipgloss.Width(title) > innerWidth {
		return borderStyle.Render("┌" + strings.Repeat("─", innerWidth) + "┐")
	}
	return borderStyle.Render("┌") + titleStyle.Render(title) +
		borderStyle.Render(strings.Repeat("─", innerWidth-lipgloss.Width(title))+"┐")
}

func borderedVariantRow(content string, borderStyle lipgloss.Style) string {
	return borderStyle.Render("│") + content + borderStyle.Render("│")
}

func (p variantsPicker) contextLabel() string {
	if p.agent == nil {
		return ""
	}
	selection := p.agent.selection
	value := p.agent.name + " · " + selection.Provider + "/" + selection.Model
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(sanitizeTerminalText(value))
}

func placeModal(base, modal string, width, height int) string {
	if width <= 0 || height <= 0 {
		return modal
	}
	buffer := cellbuf.NewBuffer(width, height)
	cellbuf.SetContent(buffer, base)
	modalWidth := min(lipgloss.Width(modal), width)
	modalHeight := min(cellbuf.Height(modal), height)
	left := max((width-modalWidth)/2, 0)
	top := max((height-modalHeight)/2, 0)
	cellbuf.SetContentRect(buffer, modal, cellbuf.Rect(left, top, modalWidth, modalHeight))
	rendered := strings.TrimSuffix(strings.ReplaceAll(cellbuf.Render(buffer), "\r\n", "\n"), "\n")
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		if lineWidth := lipgloss.Width(line); lineWidth < width {
			lines[i] += strings.Repeat(" ", width-lineWidth)
		}
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return strings.Join(lines[:height], "\n")
}
