package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/cellbuf"

	"github.com/K3N4Y/atenea/internal/tui/theme"
)

func (m Model) variantsPickerView(base string) string {
	const modalWidth = 34

	width := min(modalWidth, max(m.width-2, 1))
	innerWidth := max(width-2, 0)
	modalBackground := lipgloss.NewStyle().Background(lipgloss.Color(theme.UserMessage))
	borderStyle := secondaryTextStyle.Background(lipgloss.Color(theme.UserMessage))
	rows := make([]string, 0, len(reasoningVariants)+4)
	rows = append(rows, borderStyle.Render("┌"+strings.Repeat("─", innerWidth)+"┐"))
	for i, variant := range reasoningVariants {
		label := overlayCell("  "+variantLabel(variant), innerWidth)
		if i == m.variantsPicker.selected {
			label = lipgloss.NewStyle().
				Background(lipgloss.Color(theme.PermissionCommand)).
				Render(overlayCell("› "+variantLabel(variant), innerWidth))
		} else {
			label = modalBackground.Render(label)
		}
		rows = append(rows, borderStyle.Render("│")+label+borderStyle.Render("│"))
	}
	rows = append(rows,
		borderStyle.Render("│")+modalBackground.Render(strings.Repeat(" ", innerWidth))+borderStyle.Render("│"),
		borderStyle.Render("│")+
			secondaryTextStyle.Background(lipgloss.Color(theme.UserMessage)).
				Render(overlayCell("↑↓ move · enter select · esc close", innerWidth))+
			borderStyle.Render("│"),
		borderStyle.Render("└"+strings.Repeat("─", innerWidth)+"┘"),
	)
	modal := strings.Join(rows, "\n")

	return placeModal(base, modal, m.width, m.height)
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
