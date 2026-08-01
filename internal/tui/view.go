package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tui/theme"
)

const composerBoxBorderWidth = 2

const composerBoxPadding = 1

const composerOuterMargin = 2

const inputCursorWidth = 1

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

func activityHeader(marker, name, summary string) string {
	name = sanitizeTerminalText(name)
	summary = sanitizeTerminalText(summary)
	return strings.TrimRight(activityInset+marker+" "+fmt.Sprintf("%-*s", activityNameWidth, name)+" "+summary, " ")
}

const toolOutputPreviewLines = 4

const activityRailPrefix = activityInset + "│ "

const toolDiffPreviewLines = 16

// editDiffCardMaxRows caps the body rows of the rich edit diff card (see
// renderEditDiff). More generous than toolDiffPreviewLines because the
// before/after split repeats each context line in both blocks; the rest is
// summarized in the "… +N lines" mark so a large edit never floods the
// transcript.
const editDiffCardMaxRows = 40

const diffRailGlyph = "▌"

// noMarker is the diffRow marker that drops the +/- column entirely (the write
// card, whose rows are all new file content). Any other byte renders as the
// unified-diff marker between the line number and the text.
const noMarker byte = 0

var (
	canvasStyle      = lipgloss.NewStyle().Background(lipgloss.Color(theme.Canvas))
	accentStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Accent))
	userMessageStyle = lipgloss.NewStyle().Background(lipgloss.Color(theme.UserMessage)).Padding(1, 3)
	userMarkerStyle  = lipgloss.NewStyle().Faint(true)
	userTextStyle    = lipgloss.NewStyle().Background(lipgloss.Color(theme.UserMessage))
	toolRunningStyle = lipgloss.NewStyle().Faint(true)
	toolOKStyle      = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(theme.Success))
	toolFailedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Error))
	toolDeniedStyle  = lipgloss.NewStyle().Faint(true)
	toolOutputStyle  = lipgloss.NewStyle().Faint(true)
	diffAddStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Success))
	diffDelStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Error))

	diffPathStyle    = lipgloss.NewStyle().Background(lipgloss.Color(theme.DiffHeaderBg))
	diffHunkStyle    = lipgloss.NewStyle().Background(lipgloss.Color(theme.DiffHeaderBg)).Foreground(lipgloss.Color(theme.Muted))
	diffDelBandStyle = lipgloss.NewStyle().Background(lipgloss.Color(theme.DiffDelBg)).Foreground(lipgloss.Color(theme.Error))
	diffAddBandStyle = lipgloss.NewStyle().Background(lipgloss.Color(theme.DiffAddBg)).Foreground(lipgloss.Color(theme.Success))
	diffDelRailStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Error))
	diffAddRailStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Success))
	diffCtxStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Muted))

	// The write card reuses the diff-card row machinery but in a single neutral
	// gray instead of the red/green add/remove pair: a write always creates a
	// brand-new file, so there is no before/after — just the file's contents on
	// the CodeBlock gray band, a surface future syntax highlighting can paint.
	writeBandStyle = lipgloss.NewStyle().Background(lipgloss.Color(theme.CodeBlockHex)).Foreground(lipgloss.Color(theme.Muted))
	// The rail carries the band background too: the "▌" glyph fills only the left
	// half of its cell, so without a background the right half would show the
	// canvas and leave a dark sliver between the rail and the gray band.
	writeRailStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Muted)).Background(lipgloss.Color(theme.CodeBlockHex))
	// writePathStyle fills the file-path bar with the olive branch accent
	// (theme.WriteCardPath) and prints the name in the near-black canvas on top,
	// so the written file reads as a highlighted, named target.
	writePathStyle      = lipgloss.NewStyle().Background(lipgloss.Color(theme.WriteCardPath)).Foreground(lipgloss.Color(theme.Canvas))
	permissionStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.Warning))
	errorStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Error))
	statusStyle         = lipgloss.NewStyle().Faint(true)
	thinkingLabelStyle  = lipgloss.NewStyle().Bold(true) // "◆ Thought"/"◆ Thinking…" label of the thinking block header
	composerBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Border))

	composerBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(theme.Border)).
				Padding(0, composerBoxPadding)
)

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
		return permissionStyle.Render(activityHeader(activityAskMarker, "Plan", "presentado") + " (y ejecutar / n seguir en plan)")
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

// renderThinking renders the thinking block (parity with the desktop
// ThinkingBlock). While live or with backlog left to reveal: the header
// "◆ Thinking…" and below it only the last thinkingPreviewLines non-empty
// lines of the revealed text, each line ONE segment (plain assertable
// content); never markdown, it is a glimpse of the thought, not an answer.
// Settled (closed and drained) it collapses to a single summary line
// "◆ Thought for <duration>" — the "◆ Thought" label as one bold segment and
// the duration as a faint one; with expanded set the view renders instead the
// full thinking text under that same header (faint, wrapped to width; see
// toggleThinking). width is the usable viewport width (0 = no wrapping); only
// the expanded body uses it, the other shapes ignore it.
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
		// Collapsed summary: one line with the hint of the key that expands
		// it. The "◆ Thought" label is stable for the tests; the " ⇧Tab" hint
		// goes at the end so substring asserts keep working.
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

// formatThinkingDuration renders the thinking duration short and readable:
// seconds with one decimal under a minute ("0.0s", "3.4s"), otherwise the
// duration rounded to seconds ("1m5s").
func formatThinkingDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

func (e entry) renderTool(p tool.Presentation, width int) string {
	// A settled call that changed a file renders as the rich card instead of the
	// generic activity line: per-hunk before/after for a change, a single neutral
	// gray for a brand-new file. Every other state (running, failed, denied) keeps
	// the minimal line — there is no diff to show yet — and an unparseable diff
	// falls back to it too.
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
	return e.renderActivity(activityLabel(p, e), displaySubject(p.Subject), !p.HidesOutput)
}

// activityLabel is the name to draw for the call in this state: the progressive
// form while it is in flight ("Reading"), the plain one once it settled ("Read"),
// and the raw tool name when the presentation offered neither.
func activityLabel(p tool.Presentation, e entry) string {
	if e.status == toolRunning && p.Running != "" {
		return p.Running
	}
	if p.Label != "" {
		return p.Label
	}
	return e.tool
}

// displaySubject prepares a subject for a header line. Whatever the tool returned
// is raw text the model wrote, so it is sanitized, flattened to a single line and
// truncated to the width the header reserves — one place, so every tool's subject
// is bounded the same way.
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
		return toolFailedStyle.Render(activityHeader(activityFailMarker, name, summary)) +
			"\n" + toolFailedStyle.Render(activityRailPrefix+"error: "+sanitizeTerminalText(e.err))
	case toolDenied:
		return toolDeniedStyle.Render(activityHeader("–", name, "Denied by user"))
	default:
		// A running entry with a live spinner frame (subagents) animates its
		// marker; the rest keep the static run marker.
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

func (m Model) reservedLines() int {
	reserved := m.composerReservedLines() + len(m.menuItems)
	if m.showsWorking() {
		reserved++
	}
	reserved += m.permissionPanelHeight()
	return reserved
}

// showsWorking reports whether the "working" status line is rendered: a run is
// in flight AND no permission is pending. A pending permission blocks on the
// user, so the agent is not working — its panel replaces the line.
func (m Model) showsWorking() bool {
	if _, pending := m.pendingPermission(); pending {
		return false
	}
	return m.working
}

func (m Model) composerReservedLines() int {
	reserved := m.input.Height() + 2
	if _, permissionPending := m.pendingPermission(); !permissionPending {
		reserved += composerOuterMargin
	}
	return reserved
}

func (m Model) resizeViewport() Model {
	if !m.ready {
		return m
	}
	// One geometry pass owns every dimension applied here: the textarea width and
	// height and the viewport width and height. resizeViewport only APPLIES them
	// (it legitimately mutates m.input/m.viewport from Update); the arithmetic —
	// stripping the box border/padding/prompt/cursor from the width and bounding
	// the input against the reserved-line budget — lives in layout.go.
	l := m.layout()
	m.input.SetWidth(l.inputWidth)
	m.viewport.Width = l.viewportWidth
	m.input.SetHeight(l.inputHeight)
	m.viewport.Height = l.viewportHeight
	return m.syncViewport()
}

func (m Model) syncViewport() Model {
	return m.syncViewportContent(false)
}

func (m Model) syncViewportActivity() Model {
	return m.syncViewportContent(true)
}

func (m Model) syncViewportContent(agentActivity bool) Model {
	if !m.ready {
		return m
	}
	rawTranscript := m.renderTranscript()
	contentChanged := rawTranscript != m.lastTranscript
	offset := m.viewport.YOffset
	transcript := hardWrapOverflow(rawTranscript, m.viewport.Width)
	m.viewport.SetContent(transcript)
	if m.followAgent {
		m.viewport.GotoBottom()
		m.hasNewActivity = false
	} else {
		m.viewport.SetYOffset(offset)
		if agentActivity && contentChanged {
			m.hasNewActivity = true
		}
	}
	m.lastTranscript = rawTranscript
	return m
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
		// hardWrapOverflow (same as syncViewport) may split a long line into
		// several physical ones; each is its own entryLine so row N of this list
		// is the absolute row N of the viewport and click mapping does not shift.
		block := hardWrapOverflow(ve.entry.render(width, m.presentationOf(ve.entry)), width)
		for _, l := range strings.Split(block, "\n") {
			out = append(out, entryLine{idx: ve.idx, line: l})
		}
	}
	return out
}

func (m Model) View() string {
	if m.modelPicker.open {
		return m.modelPickerView()
	}
	if m.mcpPicker.open {
		return m.mcpPickerView()
	}
	if m.connectPanel.open {
		return m.connectPanelView()
	}
	if m.resumePicker.open {
		return m.resumePickerView()
	}

	content := m.chatContent()
	canvas := m.renderCanvas(content)
	if !m.ready {
		return canvas
	}
	return m.topBar() + "\n" + canvas
}

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

func (m Model) chatContent() string {
	status := ""
	if m.showsWorking() {
		margin := composerOuterMargin
		if m.ready {
			// Same chat-column outer margin the composer box and permission panel
			// inset by (layout.chatMargin), so the spinner glyph starts in the same
			// column as the box's "╭" corner.
			margin = m.baseLayout().chatMargin
		}
		status = strings.Repeat(" ", margin) + m.spinner.View() + statusStyle.Render(" "+m.workingStatusLabel()) + "\n"
	}
	return m.transcriptView() + m.menuView() + status + m.permissionPanelView() + m.composerView()
}

func (m Model) workingStatusLabel() string {
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := m.entries[i]
		switch e.kind {
		case entryAssistant:
			if !e.settled() {
				return "Preparing response"
			}
		case entryReasoning:
			if !e.settled() {
				return "Checking context"
			}
		case entryTool:
			p := m.presentationOf(e)
			if e.status == toolRunning {
				return workingToolStatusLabel(e, p)
			}
			if e.status == toolOK && toolReviewsChanges(e, p) {
				return "Reviewing changes"
			}
		case entryRetry:
			return "Still working"
		case entryCompaction:
			if e.live {
				return "Checking context"
			}
		case entryUser:
			return "Checking context"
		}
	}
	return "Checking context"
}

func workingToolStatusLabel(e entry, p tool.Presentation) string {
	if toolReviewsChanges(e, p) {
		return "Reviewing changes"
	}
	switch e.tool {
	case "read", "grep", "glob", "web_fetch", "skill":
		return "Checking context"
	case "present_plan":
		return "Preparing response"
	default:
		return "Still working"
	}
}

func toolReviewsChanges(e entry, p tool.Presentation) bool {
	if p.Kind == tool.FileChange || p.Kind == tool.FileCreation {
		return true
	}
	return e.tool == "edit" || e.tool == "write"
}

func (m Model) chatView(content string) string {
	if m.ready {
		return lipgloss.NewStyle().Width(max(m.contentWidth(), 0)).Height(max(m.bodyHeight(), 0)).Render(content)
	}
	return content
}

func (m Model) menuView() string {
	var b strings.Builder
	for i, item := range m.menuItems {
		prefix := "  "
		if i == m.menuSelected {
			prefix = accentStyle.Render("❯ ")
		}
		line := prefix + sanitizeTerminalText(item.label)
		if item.description != "" {
			line += "  " + statusStyle.Render(sanitizeTerminalText(item.description))
		}
		if width := m.chatContentWidth(); m.ready && width > 0 {
			line = ansi.Truncate(line, width, "…")
		}
		b.WriteString(line + "\n")
	}
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

func (m Model) gitSummaryLine(width, margin int) string {
	innerWidth := max(width-2*margin, 0)
	left := ""
	if m.cancelPending {
		left = ansi.Truncate(statusStyle.Render("Esc again to cancel"), innerWidth, "…")
	}
	leftWidth := ansi.StringWidth(left)
	separatorWidth := 0
	if left != "" {
		separatorWidth = 1
	}
	rightWidth := max(innerWidth-leftWidth-separatorWidth, 0)
	right := m.gitSummaryLabel(rightWidth)
	gap := max(innerWidth-leftWidth-ansi.StringWidth(right), 0)
	return strings.Repeat(" ", margin) + left + strings.Repeat(" ", gap) + right + strings.Repeat(" ", margin)
}

func (m Model) gitSummaryLabel(width int) string {
	if m.gitSummary.Files == 0 || width <= 0 {
		return ""
	}
	fileWord := "files"
	if m.gitSummary.Files == 1 {
		fileWord = "file"
	}
	stats := fmt.Sprintf("+%d  −%d", m.gitSummary.Additions, m.gitSummary.Deletions)
	variants := []string{
		fmt.Sprintf("%d %s changed  %s", m.gitSummary.Files, fileWord, stats),
		fmt.Sprintf("%d %s  %s", m.gitSummary.Files, fileWord, stats),
		stats,
	}
	for index, variant := range variants {
		if ansi.StringWidth(variant) > width {
			continue
		}
		prefix := strings.TrimSuffix(variant, stats)
		styledStats := diffAddStyle.Render(fmt.Sprintf("+%d", m.gitSummary.Additions)) + "  " + diffDelStyle.Render(fmt.Sprintf("−%d", m.gitSummary.Deletions))
		if index == len(variants)-1 {
			return styledStats
		}
		return statusStyle.Render(prefix) + styledStats
	}
	return ""
}

func (m Model) composerBoxWithWidth(width int) string {
	style := composerBoxStyle
	if m.ready {
		style = style.Width(max(width-composerBoxBorderWidth, 0))
	}
	box := style.Render(m.input.View())
	box = decorateComposerBorder(box, 0, m.tokenUsageLabel(), "╭", "╮", true, false)
	return decorateComposerBorder(box, -1, m.composerModelLabel(), "╰", "╯", false, true)
}

func (m Model) composerModelLabel() string {
	if m.planMode && m.model != "" {
		return m.model + " · plan"
	}
	return m.model
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
		const planSuffix = " · plan"
		if strings.HasSuffix(label, planSuffix) && labelWidth > ansi.StringWidth(planSuffix)+1 {
			model := strings.TrimSuffix(label, planSuffix)
			label = ansi.Truncate(model, labelWidth-ansi.StringWidth(planSuffix), "…") + planSuffix
		} else {
			label = ansi.Truncate(label, labelWidth, "…")
		}
	}
	styledLabel := statusStyle.Render(label)
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
	if window, ok := m.contextWindow(m.model); ok {
		label += " ctx " + input + "/" + formatTokenCount(window)
	}
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

// chatContentWidth and contentWidth are thin seams onto the layout module
// (computeLayout via baseLayout). baseLayout suffices because these widths do
// not depend on the reserved-line count.
func (m Model) chatContentWidth() int {
	return m.baseLayout().chatContentWidth
}

func (m Model) chatPanelVisible() bool {
	return false
}

func (m Model) contentWidth() int {
	return m.baseLayout().contentWidth
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
