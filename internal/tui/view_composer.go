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
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(theme.Border)).
				Padding(0, composerBoxPadding)
)

func (m Model) menuView() string {
	var b strings.Builder
	m.composer.visitMenuItems(func(label, description string, selected bool) {
		prefix := "  "
		if selected {
			prefix = focusStyle.Render("❯ ")
		}
		line := prefix + sanitizeTerminalText(label)
		if description != "" {
			line += "  " + metadataStyle.Render(sanitizeTerminalText(description))
		}
		if width := m.chatContentWidth(); m.ready && width > 0 {
			line = ansi.Truncate(line, width, "…")
		}
		b.WriteString(line + "\n")
	})
	return b.String()
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
	box = decorateComposerBorder(box, 0, m.tokenUsageLabel(), "╭", "╮", true, false)
	return decorateComposerBorder(box, -1, m.composerModelLabel(), "╰", "╯", false, true)
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
	input := formatTokenCount(m.usage.InputTokens)
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
