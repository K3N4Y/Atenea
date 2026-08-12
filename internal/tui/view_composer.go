package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/tui/theme"
)

var (
	composerBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Border))
	composerBoxStyle    = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color(theme.Border)).
				Padding(0, composerBoxPadding)
)

const (
	menuBoxBorderWidth  = 2
	menuBoxBorderHeight = 2
	menuBoxPadding      = 1
)

func (m Model) menuView() string {
	var rows []string
	m.composer.visitMenuItems(func(label, description string, selected bool) {
		prefix := "  "
		if selected {
			prefix = focusStyle.Render("❯ ")
		}
		line := prefix + sanitizeTerminalText(label)
		if description != "" {
			line += "  " + metadataStyle.Render(sanitizeTerminalText(description))
		}
		rows = append(rows, line)
	})
	if len(rows) == 0 {
		return ""
	}

	width := m.chatContentWidth()
	margin := 0
	if m.ready {
		l := m.baseLayout()
		width = l.chatInnerWidth
		margin = l.chatMargin
	} else {
		for _, row := range rows {
			width = max(width, ansi.StringWidth(row)+menuBoxBorderWidth+2*menuBoxPadding)
		}
	}
	if width < menuBoxBorderWidth {
		for i, row := range rows {
			rows[i] = ansi.Truncate(row, width, "…")
		}
		return strings.Repeat(" ", margin) + strings.Join(rows, "\n") + "\n"
	}

	contentWidth := max(width-menuBoxBorderWidth-2*menuBoxPadding, 0)
	for i, row := range rows {
		row = ansi.Truncate(row, contentWidth, "…")
		rows[i] = composerBorderStyle.Render("│") +
			strings.Repeat(" ", menuBoxPadding) + row +
			strings.Repeat(" ", max(contentWidth-ansi.StringWidth(row), 0)+menuBoxPadding) +
			composerBorderStyle.Render("│")
	}

	box := renderMenuBox(rows, width)
	if !m.ready {
		return box + "\n"
	}
	right := max(m.chatContentWidth()-margin-width, 0)
	leftPadding := strings.Repeat(" ", margin)
	rightPadding := strings.Repeat(" ", right)
	boxLines := strings.Split(box, "\n")
	for i, line := range boxLines {
		boxLines[i] = leftPadding + line + rightPadding
	}
	return strings.Join(boxLines, "\n") + "\n"
}

func renderMenuBox(rows []string, width int) string {
	border := func(left, right string) string {
		return composerBorderStyle.Render(left + strings.Repeat("─", max(width-menuBoxBorderWidth, 0)) + right)
	}
	lines := make([]string, 0, len(rows)+menuBoxBorderHeight)
	lines = append(lines, border("┌", "┐"))
	lines = append(lines, rows...)
	lines = append(lines, border("└", "┘"))
	return strings.Join(lines, "\n")
}

func (m Model) composerBox() string {
	return m.composerBoxWithWidth(m.chatContentWidth())
}

func (m Model) composerView() string {
	if !m.ready {
		return m.composerBox()
	}
	l := m.baseLayout()
	width := l.chatContentWidth
	margin := l.chatMargin
	box := m.composerBoxWithWidth(l.chatInnerWidth)
	box = lipgloss.NewStyle().Margin(0, margin).Render(box)
	if _, permissionPending := m.pendingPermission(); permissionPending {
		return box
	}
	return strings.Join([]string{
		box,
		m.gitSummaryLine(width, margin),
		strings.Repeat(" ", width),
	}, "\n")
}

func (m Model) composerBoxWithWidth(width int) string {
	style := composerBoxStyle
	if m.ready {
		style = style.Width(max(width-composerBoxBorderWidth, 0))
	}
	box := style.Render(m.composer.inputView())
	box = decorateComposerBorder(box, 0, m.tokenUsageLabel(), "┌", "┐", true, false)
	return decorateComposerBorder(box, -1, m.composerModelLabel(), "└", "┘", false, true)
}

func (m Model) composerModelLabel() string {
	label := m.model
	if agent, ok := m.agent.(reasoningAgent); ok && label != "" {
		if effort := agent.ReasoningEffort(); effort != "" {
			label += "(" + string(effort) + ")"
		}
	}
	if yolo, ok := m.agent.(yoloAgent); ok && yolo.YoloEnabled() {
		if label == "" {
			return "YOLO"
		}
		return label + " · YOLO"
	}
	if m.planMode && label != "" {
		return label + " · plan"
	}
	return label
}

func decorateComposerBorder(box string, lineIndex int, label, leftCorner, rightCorner string, alignLeft, truncate bool) string {
	if label == "" {
		return box
	}
	lines := strings.Split(box, "\n")
	if lineIndex < 0 {
		lineIndex = len(lines) + lineIndex
	}
	width := ansi.StringWidth(lines[lineIndex])
	const fixedBorderWidth = 5
	labelWidth := width - fixedBorderWidth - 1
	if labelWidth < 2 {
		return box
	}
	if ansi.StringWidth(label) > labelWidth {
		if !truncate {
			return box
		}
		for _, suffix := range []string{" · plan", " · YOLO"} {
			if strings.HasSuffix(label, suffix) && labelWidth >= ansi.StringWidth(suffix)+1 {
				model := strings.TrimSuffix(label, suffix)
				label = ansi.Truncate(model, labelWidth-ansi.StringWidth(suffix), "…") + suffix
				break
			}
		}
		if ansi.StringWidth(label) > labelWidth {
			label = ansi.Truncate(label, labelWidth, "…")
		}
	}
	styledLabel := metadataStyle.Render(label)
	remaining := width - ansi.StringWidth(styledLabel) - fixedBorderWidth
	if remaining < 1 {
		return box
	}
	if alignLeft {
		lines[lineIndex] = composerBorderStyle.Render(leftCorner+"─ ") + styledLabel + composerBorderStyle.Render(" "+strings.Repeat("─", remaining)+rightCorner)
	} else {
		lines[lineIndex] = composerBorderStyle.Render(leftCorner+strings.Repeat("─", remaining)+" ") + styledLabel + composerBorderStyle.Render(" ─"+rightCorner)
	}
	return strings.Join(lines, "\n")
}

func (m Model) tokenUsageLabel() string {
	if m.usage == nil {
		return ""
	}
	input := formatTokenCount(m.usage.TotalInputTokens())
	output := formatTokenCount(m.usage.OutputTokens)
	if m.liveUsage {
		input = "~" + input
		if m.usage.OutputTokens > 0 {
			output = "~" + output
		}
	}
	label := "↑ " + input + " ↓ " + output
	label += m.cacheStatsUsageLabel()
	return label
}

func formatTokenCount(tokens int) string {
	if tokens < 1_000 {
		return strconv.Itoa(tokens)
	}
	if tokens >= 1_000_000 {
		if tokens%1_000_000 == 0 || tokens >= 10_000_000 {
			return strconv.Itoa(tokens/1_000_000) + "m"
		}
		return strings.TrimSuffix(strconv.FormatFloat(float64(tokens)/1_000_000, 'f', 1, 64), ".0") + "m"
	}
	if tokens%1_000 == 0 || tokens >= 10_000 {
		return strconv.Itoa(tokens/1_000) + "k"
	}
	return strings.TrimSuffix(strconv.FormatFloat(float64(tokens)/1_000, 'f', 1, 64), ".0") + "k"
}
