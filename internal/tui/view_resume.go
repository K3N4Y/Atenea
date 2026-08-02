package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tui/theme"
)

func (m Model) resumePickerView() string {
	width := max(m.width, 0)
	height := max(m.height, 0)
	if !m.ready {
		return m.renderCanvas(m.resumePickerSearch(width))
	}

	lines := make([]string, 0, height)
	if height >= 4 {
		lines = append(lines, "")
	}
	for _, line := range strings.Split(m.resumePickerSearch(width), "\n") {
		if len(lines) >= height {
			break
		}
		lines = append(lines, ansi.Truncate(line, width, ""))
	}
	if len(lines) < height {
		lines = append(lines, "")
	}

	visibleRows := max(height-len(lines), 0)
	for _, line := range m.resumePickerBody(visibleRows, max(width-2*composerOuterMargin, 0)) {
		if len(lines) >= height {
			break
		}
		lines = append(lines, strings.Repeat(" ", min(composerOuterMargin, width))+line)
	}

	return m.renderFullCanvas(strings.Join(lines, "\n"))
}

func (m Model) resumePickerSearch(width int) string {
	if width <= 0 {
		return ""
	}

	boxWidth := max(width-2*composerOuterMargin, 0)
	query := m.resumePicker.query
	if boxWidth < 4 {
		query.Width = width
		return ansi.Truncate(query.View(), width, "")
	}

	query.Width = max(boxWidth-4, 0)
	search := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(theme.Border)).
		Padding(0, 1).
		Width(boxWidth - composerBoxBorderWidth).
		Render(query.View())
	margin := strings.Repeat(" ", composerOuterMargin)
	lines := strings.Split(search, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(margin+line, width, "")
	}
	return strings.Join(lines, "\n")
}

func (m Model) resumePickerBody(visibleRows, width int) []string {
	if visibleRows <= 0 || width <= 0 {
		return nil
	}
	if m.resumePicker.loading {
		return []string{ansi.Truncate(statusStyle.Render("Loading sessions…"), width, "")}
	}
	if m.resumePicker.err != nil {
		message := sanitizeResumePickerLine(m.resumePicker.err.Error())
		return []string{ansi.Truncate(errorStyle.Render(message), width, "")}
	}
	if len(m.resumePicker.filtered) == 0 {
		return []string{ansi.Truncate(statusStyle.Render("No sessions found"), width, "")}
	}

	start, end := m.resumePicker.window(visibleRows)
	rows := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		rows = append(rows, m.resumePickerRow(m.resumePicker.filtered[index], index == m.resumePicker.selected, width))
	}
	return rows
}

func (m Model) resumePickerRow(summary session.SessionSummary, selected bool, width int) string {
	if width <= 0 {
		return ""
	}

	prefix := "  "
	styledPrefix := prefix
	if selected {
		prefix = "❯ "
		styledPrefix = accentStyle.Render("❯") + " "
	}
	prefixWidth := lipgloss.Width(prefix)
	available := max(width-prefixWidth, 0)

	title := sanitizeResumePickerLine(summary.Title)
	if strings.TrimSpace(title) == "" {
		title = "Untitled session"
	}

	date := ""
	if !summary.LastActivity.IsZero() {
		date = formatResumeActivity(summary.LastActivity)
	}
	current := summary.ID == m.resumePicker.currentID
	metadata := date
	if current {
		metadata = "current"
		if date != "" {
			metadata += "  " + date
		}
	}
	if metadata != "" && lipgloss.Width(metadata)+1+min(8, available) > available {
		metadata = ""
	}

	metadataWidth := lipgloss.Width(metadata)
	titleWidth := available
	if metadataWidth > 0 {
		titleWidth = max(available-metadataWidth-1, 0)
	}
	title = ansi.Truncate(title, titleWidth, "…")
	styledTitle := title
	if selected {
		styledTitle = accentStyle.Render(title)
	}

	row := styledPrefix + styledTitle
	if metadataWidth == 0 {
		return ansi.Truncate(row, width, "")
	}
	row += strings.Repeat(" ", max(width-prefixWidth-lipgloss.Width(title)-metadataWidth, 1))
	if current {
		row += statusStyle.Render("current")
		if date != "" {
			row += "  "
		}
	}
	if date != "" {
		if selected {
			row += accentStyle.Render(date)
		} else {
			row += statusStyle.Render(date)
		}
	}
	return ansi.Truncate(row, width, "")
}

func sanitizeResumePickerLine(value string) string {
	return strings.ReplaceAll(sanitizeTerminalText(value), "\n", " ")
}
