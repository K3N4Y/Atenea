package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/K3N4Y/atenea/internal/tui/theme"
)

var canvasStyle = lipgloss.NewStyle().Background(lipgloss.Color(theme.Canvas))

func (m Model) renderCanvas(content string) string {
	content = restoreCanvasBackground(content)
	if !m.ready {
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			lines[i] = canvasStyle.Render(line)
		}
		return strings.Join(lines, "\n")
	}
	l := m.baseLayout()
	return canvasStyle.Width(l.width).Height(max(l.bodyHeight, 0)).Render(content)
}

func (m Model) renderFullCanvas(content string) string {
	content = restoreCanvasBackground(content)
	if !m.ready {
		return m.renderCanvas(content)
	}
	l := m.baseLayout()
	return canvasStyle.Width(l.width).Height(l.height).Render(content)
}

func restoreCanvasBackground(content string) string {
	styledMarker := canvasStyle.Render("x")
	background, _, found := strings.Cut(styledMarker, "x")
	if !found || background == "" {
		return content
	}
	content = strings.ReplaceAll(content, "\x1b[0m", "\x1b[0m"+background)
	return strings.ReplaceAll(content, "\x1b[m", "\x1b[m"+background)
}
