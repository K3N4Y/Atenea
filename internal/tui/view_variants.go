package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/K3N4Y/atenea/internal/tui/theme"
)

func (m Model) variantsPickerView() string {
	rows := make([]string, len(reasoningVariants))
	for i, variant := range reasoningVariants {
		label := variantLabel(variant)
		if i == m.variantsPicker.selected {
			rows[i] = focusStyle.Render("› " + label)
		} else {
			rows[i] = "  " + label
		}
	}
	content := lipgloss.NewStyle().
		Padding(1, 2).
		Background(lipgloss.Color(theme.Canvas)).
		Render("variants\n" + strings.Join(rows, "\n") + "\n\n↑↓ move · enter select · esc close")
	return m.renderFullCanvas(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content))
}
