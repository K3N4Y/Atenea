package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tui/theme"
)

const inputPrompt = "❯ "

const toolInputSummaryWidth = 48

const (
	activityRunMarker  = "●"
	activityOKMarker   = "✓"
	activityFailMarker = "✗"
	activityAskMarker  = "?"
)

const activityNameWidth = 8

const activityInset = "  "

const activityRailPrefix = activityInset + "│ "

const toolOutputPreviewLines = 4

var toolOutputStyle = lipgloss.NewStyle().Faint(true)

func renderCappedLines(text string, maxLines int, renderLine func(line string) string) string {
	text = sanitizeTerminalText(text)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	shown := lines
	if len(shown) > maxLines {
		shown = shown[:maxLines]
	}
	rendered := make([]string, 0, len(shown)+1)
	for _, line := range shown {
		rendered = append(rendered, renderLine(line))
	}
	if hidden := len(lines) - len(shown); hidden > 0 {
		rendered = append(rendered, toolOutputStyle.Render(activityRailPrefix+"… +"+strconv.Itoa(hidden)+" lines"))
	}
	return strings.Join(rendered, "\n")
}

func renderOutputPreview(output string) string {
	return renderCappedLines(output, toolOutputPreviewLines, func(line string) string {
		return toolOutputStyle.Render(activityRailPrefix + line)
	})
}

func summarizeToolInput(raw string) string {
	if raw == "" {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || len(fields) == 0 {
		return ""
	}
	if len(fields) == 1 {
		for _, value := range fields {
			var text string
			if err := json.Unmarshal(value, &text); err == nil && text != "" {
				return text
			}
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(raw)); err != nil {
		return ""
	}
	return compact.String()
}

var (
	userMessageStyle   = lipgloss.NewStyle().Background(lipgloss.Color(theme.UserMessage)).Padding(1, 3)
	userMarkerStyle    = lipgloss.NewStyle().Faint(true)
	userTextStyle      = lipgloss.NewStyle().Background(lipgloss.Color(theme.UserMessage))
	toolRunningStyle   = lipgloss.NewStyle().Faint(true)
	toolOKStyle        = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(theme.Success))
	toolFailedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Error))
	toolDeniedStyle    = lipgloss.NewStyle().Faint(true)
	permissionStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.Warning))
	thinkingLabelStyle = lipgloss.NewStyle().Bold(true)
)

func activityHeader(marker, name, summary string) string {
	name = sanitizeTerminalText(name)
	summary = sanitizeTerminalText(summary)
	return strings.TrimRight(activityInset+marker+" "+fmt.Sprintf("%-*s", activityNameWidth, name)+" "+summary, " ")
}

func (e entry) render(width int, p tool.Presentation) string {
	switch e.kind {
	case entryUser:
		style := userMessageStyle
		if width > 2*composerOuterMargin {
			style = style.Width(width - 2*composerOuterMargin)
		}
		text := sanitizeTerminalText(e.text)
		if width > 2*composerOuterMargin {
			contentWidth := max(width-2*composerOuterMargin-userMessageStyle.GetHorizontalFrameSize(), 1)
			text = ansi.Wrap(text, max(contentWidth-ansi.StringWidth(inputPrompt), 1), "")
		}
		lines := strings.Split(text, "\n")
		for index, line := range lines {
			prompt := strings.Repeat(" ", ansi.StringWidth(inputPrompt))
			if index == 0 {
				prompt = userMarkerStyle.Render(inputPrompt)
			}
			lines[index] = prompt + userTextStyle.Render(line)
		}
		return lipgloss.NewStyle().Margin(0, composerOuterMargin).Render(style.Render(strings.Join(lines, "\n")))
	case entryReasoning:
		return e.renderThinking(width)
	case entryTool:
		return e.renderTool(p, width)
	case entryPermission:
		return permissionStyle.Render(activityHeader(activityAskMarker, activityLabel(p, e), displaySubject(p.Subject)))
	case entryPlanApproval:
		return permissionStyle.Render(activityHeader(activityAskMarker, "Plan", "presented") + " (y run / n stay in plan)")
	case entryError:
		if !isProviderError(e.text) {
			return errorStyle.Render(activityHeader(activityFailMarker, "error", e.text))
		}
		summary := friendlyProviderError(e.text)
		line := activityHeader(activityFailMarker, "error", summary) + "  [r retry] [d details]"
		if e.expanded {
			line += "\n" + statusStyle.Render("  │ "+sanitizeProviderDetails(e.text))
		}
		return errorStyle.Render(line)
	case entryRetry:
		return permissionStyle.Render(activityHeader("↻", "retry", e.text))
	case entryCompaction:
		if e.err != "" {
			return errorStyle.Render("[error] " + sanitizeTerminalText(e.err))
		}
		return statusStyle.Render("[context] " + sanitizeTerminalText(e.text))
	case entryNotice:
		margin := min(composerOuterMargin, width/2)
		return lipgloss.NewStyle().Margin(0, margin).Render(statusStyle.Render(sanitizeTerminalText(e.text)))
	case entryEvent:
		return statusStyle.Render("[" + sanitizeTerminalText(e.eventKind) + "] " + sanitizeTerminalText(e.text))
	default:
		if e.settled() {
			return renderMarkdown(e.text, width)
		}
		return renderMarkdown(e.revealedText(), width)
	}
}

const thinkingPreviewLines = 4

const thinkingInset = "  "

func (e entry) renderThinking(width int) string {
	if !e.settled() {
		lines := []string{thinkingLabelStyle.Render("◆ Thinking…")}
		for _, line := range lastNonEmptyLines(sanitizeTerminalText(e.revealedText()), thinkingPreviewLines) {
			lines = append(lines, statusStyle.Render(line))
		}
		return insetThinking(strings.Join(lines, "\n"))
	}
	summary := thinkingLabelStyle.Render("◆ Thought") + statusStyle.Render(" for "+formatThinkingDuration(e.duration))
	if !e.expanded {
		return insetThinking(summary + statusStyle.Render(" ⇧Tab"))
	}
	body := sanitizeTerminalText(e.revealedText())
	if width > len(thinkingInset) {
		body = ansi.Wrap(body, width-len(thinkingInset), "")
	}
	return insetThinking(strings.Join([]string{summary, statusStyle.Render(body)}, "\n"))
}

func insetThinking(text string) string {
	return thinkingInset + strings.ReplaceAll(text, "\n", "\n"+thinkingInset)
}

func lastNonEmptyLines(text string, limit int) []string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) > limit {
		kept = kept[len(kept)-limit:]
	}
	return kept
}

func formatThinkingDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

func (e entry) renderTool(p tool.Presentation, width int) string {
	if e.status == toolOK && e.diff != "" {
		card := ""
		switch p.Kind {
		case tool.FileChange:
			card = renderEditDiff(e.diff, width)
		case tool.FileCreation:
			card = renderWriteCard(e.diff, width)
		}
		if card != "" {
			return card
		}
	}
	showDetail := !p.HidesOutput
	if e.tool == "bash" {
		showDetail = e.expanded
	} else if e.status == toolFailed {
		showDetail = true
	}
	return e.renderActivity(activityLabel(p, e), displaySubject(p.Subject), showDetail)
}

func activityLabel(p tool.Presentation, e entry) string {
	if e.status == toolRunning && p.Running != "" {
		return p.Running
	}
	if p.Label != "" {
		return p.Label
	}
	return e.tool
}

func displaySubject(subject string) string {
	if subject == "" {
		return ""
	}
	subject = sanitizeTerminalText(subject)
	subject = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(subject)
	return ansi.Truncate(subject, toolInputSummaryWidth, "…")
}

func (e entry) renderActivity(name, summary string, showDetail bool) string {
	switch e.status {
	case toolOK:
		detail := ""
		if showDetail {
			detail = renderDiffPreview(e.diff)
			if detail != "" {
				added, removed := diffStat(e.diff)
				summary += "  +" + strconv.Itoa(added) + " -" + strconv.Itoa(removed)
			} else {
				detail = renderOutputPreview(e.output)
			}
		}
		out := toolOKStyle.Render(activityHeader(activityOKMarker, name, summary))
		if detail != "" {
			out += "\n" + detail
		}
		return out
	case toolFailed:
		out := toolFailedStyle.Render(activityHeader(activityFailMarker, name, summary))
		if showDetail {
			out += "\n" + toolFailedStyle.Render(activityRailPrefix+"error: "+sanitizeTerminalText(e.err))
		}
		return out
	case toolDenied:
		return toolDeniedStyle.Render(activityHeader("–", name, "Denied by user"))
	default:
		marker := activityRunMarker
		if e.spin != "" {
			marker = e.spin
		}
		return toolRunningStyle.Render(activityHeader(marker, name, summary))
	}
}

func (m Model) renderTranscript() string {
	width := 0
	if m.ready {
		width = m.viewport.Width
	}
	var b strings.Builder
	for i, ve := range m.visibleEntries() {
		if i > 0 {
			if ve.joinCompact {
				b.WriteString("\n")
			} else {
				b.WriteString("\n\n")
			}
		}
		b.WriteString(ve.entry.render(width, m.presentationOf(ve.entry)))
	}
	return b.String()
}

type entryLine struct {
	idx  int
	line string
}

func (m Model) entryLines() []entryLine {
	width := 0
	if m.ready {
		width = m.viewport.Width
	}
	var out []entryLine
	for i, ve := range m.visibleEntries() {
		if i > 0 && !ve.joinCompact {
			out = append(out, entryLine{idx: -1, line: ""})
		}
		block := hardWrapOverflow(ve.entry.render(width, m.presentationOf(ve.entry)), width)
		for _, line := range strings.Split(block, "\n") {
			out = append(out, entryLine{idx: ve.idx, line: line})
		}
	}
	return out
}

func (m Model) transcriptView() string {
	if m.ready {
		if m.viewport.Height <= 0 {
			return ""
		}
		view := m.viewport.View()
		if m.hasNewActivity {
			view = renderNewActivityIndicator(view, m.viewport.Width)
		}
		return view + "\n"
	}
	if transcript := m.renderTranscript(); transcript != "" {
		return transcript + "\n\n"
	}
	return ""
}

func renderNewActivityIndicator(view string, width int) string {
	if view == "" || width <= 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	last := len(lines) - 1
	line := ansi.Truncate(lines[last], max(width-1, 0), "")
	line += strings.Repeat(" ", max(width-1-lipgloss.Width(line), 0)) + "↓"
	lines[last] = line
	return strings.Join(lines, "\n")
}
