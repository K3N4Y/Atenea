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
)

const inputPrompt = "❯ "

const userMessageRail = "┃"

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

var toolOutputStyle = metadataStyle.Faint(true)

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

func renderTaskReportPreview(output string) string {
	output = sanitizeTerminalText(output)
	if strings.TrimSpace(output) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) > 3 {
		lines = lines[:3]
	}
	for i := range lines {
		lines[i] = toolOutputStyle.Render(activityRailPrefix + lines[i])
	}
	return strings.Join(lines, "\n")
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
	userMessageStyle   = surfaceStyle
	userRailStyle      = surfaceStyle.Inherit(metadataStyle).Faint(true)
	userTextStyle      = surfaceStyle.Inherit(primaryTextStyle)
	toolRunningStyle   = secondaryTextStyle
	toolFailedStyle    = dangerStyle
	toolDeniedStyle    = secondaryTextStyle
	permissionStyle    = warningStyle.Bold(true)
	thinkingLabelStyle = primaryTextStyle.Bold(true)
)

func activityHeader(marker, name, summary string) string {
	name = sanitizeTerminalText(name)
	summary = sanitizeTerminalText(summary)
	return strings.TrimRight(activityInset+marker+" "+fmt.Sprintf("%-*s", activityNameWidth, name)+" "+summary, " ")
}

func (e entry) render(width int, p tool.Presentation) string {
	switch e.kind {
	case entryUser:
		text := sanitizeTerminalText(e.text)
		margin := composerOuterMargin
		horizontalPadding := 3
		bodyWidth := 0
		if width > 0 {
			margin = min(margin, max((width-4)/2, 0))
			blockWidth := width - 2*margin
			bodyWidth = max(blockWidth-ansi.StringWidth(userMessageRail), 0)
			horizontalPadding = min(horizontalPadding, max((bodyWidth-1)/2, 0))
			contentWidth := max(bodyWidth-2*horizontalPadding, 1)
			text = hardWrapOverflow(ansi.Wrap(text, contentWidth, ""), contentWidth)
		} else {
			for _, line := range strings.Split(text, "\n") {
				bodyWidth = max(bodyWidth, ansi.StringWidth(line)+2*horizontalPadding)
			}
		}
		lines := strings.Split(text, "\n")
		rows := make([]string, 0, len(lines)+2)
		rows = append(rows, "")
		rows = append(rows, lines...)
		rows = append(rows, "")
		rail := userRailStyle.Render(userMessageRail)
		for index, line := range rows {
			body := strings.Repeat(" ", bodyWidth)
			if index > 0 && index < len(rows)-1 {
				line = ansi.Truncate(line, max(bodyWidth-2*horizontalPadding, 0), "")
				body = strings.Repeat(" ", horizontalPadding) + line + strings.Repeat(" ", max(bodyWidth-horizontalPadding-ansi.StringWidth(line), 0))
			}
			rows[index] = rail + userTextStyle.Render(body)
		}
		return lipgloss.NewStyle().Margin(0, margin).Render(userMessageStyle.Render(strings.Join(rows, "\n")))
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
			return dangerStyle.Render(activityHeader(activityFailMarker, "error", e.text))
		}
		summary := friendlyProviderError(e.text)
		line := activityHeader(activityFailMarker, "error", summary) + "  [r retry] [d details]"
		if e.expanded {
			line += "\n" + metadataStyle.Render("  │ "+sanitizeProviderDetails(e.text))
		}
		return dangerStyle.Render(line)
	case entryRetry:
		return permissionStyle.Render(activityHeader("↻", "retry", e.text))
	case entryCompaction:
		if e.err != "" {
			return dangerStyle.Render("[error] " + sanitizeTerminalText(e.err))
		}
		return metadataStyle.Render("[context] " + sanitizeTerminalText(e.text))
	case entryNotice:
		margin := min(composerOuterMargin, width/2)
		return lipgloss.NewStyle().Margin(0, margin).Render(metadataStyle.Render(sanitizeTerminalText(e.text)))
	case entryEvent:
		return metadataStyle.Render("[" + sanitizeTerminalText(e.eventKind) + "] " + sanitizeTerminalText(e.text))
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
			lines = append(lines, secondaryTextStyle.Render(line))
		}
		return insetThinking(strings.Join(lines, "\n"))
	}
	summary := thinkingLabelStyle.Render("◆ Thought") + metadataStyle.Render(" for "+formatThinkingDuration(e.duration))
	if !e.expanded {
		return insetThinking(summary + metadataStyle.Render(" ⇧Tab"))
	}
	body := sanitizeTerminalText(e.revealedText())
	if width > len(thinkingInset) {
		body = ansi.Wrap(body, width-len(thinkingInset), "")
	}
	return insetThinking(strings.Join([]string{summary, metadataStyle.Render(body)}, "\n"))
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

func searchToolSummary(e entry, pattern string) string {
	if e.status != toolOK {
		return pattern
	}
	if e.tool == "grep" {
		var count int
		if _, err := fmt.Sscanf(e.output, "Found %d matches", &count); err == nil && count > 0 {
			if strings.Contains(e.output, "(more matches available)") {
				return pattern + " (100+)"
			}
			return pattern + " (" + strconv.Itoa(count) + ")"
		}
		return pattern + " (no results)"
	}
	if strings.HasPrefix(strings.TrimSpace(e.output), "No files found") {
		return pattern + " (no results)"
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(e.output), "\n") {
		if line != "" && !strings.HasPrefix(line, "[") {
			count++
		}
	}
	if count == 0 {
		return pattern + " (no results)"
	}
	if count > 100 {
		return pattern + " (100+)"
	}
	return pattern + " (" + strconv.Itoa(count) + ")"
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
	if e.tool == "grep" || e.tool == "glob" {
		return e.renderActivity(activityLabel(p, e), searchToolSummary(e, p.Subject), false)
	}
	if e.tool == "task" {
		block := e.renderActivity(activityLabel(p, e), displaySubject(p.Subject), false)
		if e.status == toolOK {
			if report := renderTaskReportPreview(e.output); report != "" {
				block += "\n" + report
			}
		}
		return block
	} else if e.tool == "bash" {
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
		out := secondaryTextStyle.Render(activityInset) + successStyle.Render(activityOKMarker) + secondaryTextStyle.Render(" "+activityHeader("", name, summary)[len(activityInset)+1:])
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

func (m Model) renderVisibleBlock(ve visibleEntry, width int) string {
	block := ve.entry.render(width, m.presentationOf(ve.entry))
	if ve.entry.kind != entryTool || ve.entry.tool != "task" {
		return block
	}
	if m.childDetached[ve.entry.callID] {
		return block + "\n  ↳ background job; use task_status, task_wait, or task_cancel"
	}
	if total, ok := m.childTotals[ve.entry.callID]; ok {
		label := "tool calls"
		if total == 1 {
			label = "tool call"
		}
		summary := strconv.Itoa(total) + " " + label
		if usage, ok := m.childSummaries[ve.entry.callID]; ok {
			summary += " · " + strconv.Itoa(usage.Requests) + " req · " + strconv.Itoa(usage.Tokens) + " tok · " + usage.Duration.Round(time.Millisecond).String()
			if usage.Workspace != "" {
				summary += " · " + usage.Workspace
			}
		}
		return block + "\n  ↳ " + summary
	}
	for _, child := range m.childBatches[ve.entry.callID] {
		p := m.presentationOf(child)
		childBlock := child.renderActivity(activityLabel(p, child), displaySubject(p.Subject), false)
		childBlock = "  ↳ " + strings.ReplaceAll(childBlock, "\n", "\n    ")
		block += "\n" + childBlock
	}
	return block
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
		b.WriteString(m.renderVisibleBlock(ve, width))
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
		block := hardWrapOverflow(m.renderVisibleBlock(ve, width), width)
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
		view := m.renderSelection(m.viewport.View(), m.viewport.YOffset)
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
