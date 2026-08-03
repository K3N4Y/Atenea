package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tui/theme"
)

const (
	permissionPanelMaxHeight     = 9
	permissionPanelFallbackWidth = 48
	// permissionActionGap are the columns between two actions of the row.
	permissionActionGap = 4
	// permissionButtonPadding are the columns each button adds around its label
	// (permissionButtonStyle's Padding(0, 1)).
	permissionButtonPadding = 2
)

var (
	permissionPanelStyle        = lipgloss.NewStyle().Background(lipgloss.Color(theme.PermissionPanel))
	permissionCommandStyle      = lipgloss.NewStyle().Background(lipgloss.Color(theme.PermissionCommand))
	permissionAccentStyle       = warningStyle.Bold(true)
	permissionTitleStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Canvas)).Background(lipgloss.Color(theme.PermissionActive))
	permissionButtonStyle       = lipgloss.NewStyle().Background(lipgloss.Color(theme.PermissionCommand)).Padding(0, 1)
	permissionActiveStyle       = permissionButtonStyle.Bold(true).Foreground(lipgloss.Color(theme.Canvas)).Background(lipgloss.Color(theme.PermissionActive))
	permissionCompactLabelStyle = metadataStyle.Background(lipgloss.Color(theme.PermissionCommand))
)

// permissionActionRange is the horizontal click target of one action of the
// row, in columns relative to the panel's left edge.
type permissionActionRange struct {
	start int
	end   int
}

type permissionPanelLayout struct {
	x       int
	y       int
	width   int
	height  int
	actionY int
	// actions are the click targets of the row in choice order, so the offered
	// actions and the selectable choices are the same list.
	actions      []permissionActionRange
	commandStart int
	commandEnd   int
}

func (layout permissionPanelLayout) actionPoint(choice permissionChoice) (int, int) {
	if choice < 0 || int(choice) >= len(layout.actions) {
		return layout.x, layout.y + layout.actionY
	}
	action := layout.actions[choice]
	return layout.x + action.start + max((action.end-action.start)/2, 0), layout.y + layout.actionY
}

// permissionRule derives the grant that approving the pending request for the
// whole session would create, and whether the request is grantable at all. The
// answer comes from the tool that would settle the call, so a tool atenea does not
// ship can offer the session grant too.
func (m Model) permissionRule(request entry) (permission.Rule, bool) {
	call := tool.Call{Name: request.tool, Input: []byte(request.input)}
	return permission.RuleFor(m.tools(), call)
}

// permissionActionLabels lists the actions the panel offers for the request, in
// choice order. The session grant comes last and only when the request is
// grantable AND the row fits the panel: a truncated action reads as a bug, so on
// a narrow terminal the option is withheld rather than half-drawn.
func (m Model) permissionActionLabels(request entry, width int) []string {
	labels := []string{"Deny", "Allow once"}
	rule, grantable := m.permissionRule(request)
	if !grantable {
		return labels
	}
	withSession := []string{labels[0], labels[1], "Allow " + rule.Label() + " this session"}
	if permissionActionRowWidth(withSession) > width {
		return labels
	}
	return withSession
}

// permissionActionRowWidth is the columns the row of actions occupies.
func permissionActionRowWidth(labels []string) int {
	total := permissionActionGap * max(len(labels)-1, 0)
	for _, label := range labels {
		total += ansi.StringWidth(label) + permissionButtonPadding
	}
	return total
}

// permissionActionRanges places the actions across the row. It mirrors
// permissionActionRow: every action is a padded button, selected or not.
func permissionActionRanges(labels []string) []permissionActionRange {
	ranges := make([]permissionActionRange, len(labels))
	offset := 0
	for index, label := range labels {
		width := ansi.StringWidth(label) + permissionButtonPadding
		ranges[index] = permissionActionRange{start: offset, end: offset + width}
		offset += width + permissionActionGap
	}
	return ranges
}

// clampActionRanges keeps the click targets inside the panel: a row truncated
// by a narrow terminal must not answer clicks on columns it does not draw.
func clampActionRanges(ranges []permissionActionRange, width int) []permissionActionRange {
	clamped := make([]permissionActionRange, len(ranges))
	for index, action := range ranges {
		clamped[index] = permissionActionRange{start: min(action.start, width), end: min(action.end, width)}
	}
	return clamped
}

// permissionPanelBox is the panel's horizontal geometry: the chat column it
// lives in, its outer margin and the inner width the renderers lay out against.
// Before the first WindowSizeMsg there is no known size and it falls back to a
// fixed panel width, so the panel still renders — and the action row is
// measured against the very width it will be drawn at.
func (m Model) permissionPanelBox() (contentWidth, margin, panelWidth int) {
	l := m.baseLayout()
	if !m.ready || l.chatContentWidth <= 0 {
		contentWidth = permissionPanelFallbackWidth
		margin = min(composerOuterMargin, contentWidth/2)
		return contentWidth, margin, max(contentWidth-2*margin, 0)
	}
	return l.chatContentWidth, l.chatMargin, l.chatInnerWidth
}

// permissionActions are the actions offered for the request at the panel's
// current width. The keyboard and the mouse select over this same list.
func (m Model) permissionActions(request entry) []string {
	_, _, panelWidth := m.permissionPanelBox()
	return m.permissionActionLabels(request, panelWidth)
}

// permissionChoiceCount is how many actions the panel offers for the request.
func (m Model) permissionChoiceCount(request entry) permissionChoice {
	return permissionChoice(len(m.permissionActions(request)))
}

// permissionSelection is the selected action clamped to the offered ones.
func (m Model) permissionSelection(request entry) permissionChoice {
	return clampPermissionChoice(m.permissionChoice, m.permissionChoiceCount(request))
}

// clampPermissionChoice keeps a choice inside the offered actions: a selection
// carried over from a wider panel or a grantable request must never point past
// the row.
func clampPermissionChoice(choice, count permissionChoice) permissionChoice {
	return min(max(choice, permissionDeny), count-1)
}

func (m Model) permissionPanelHeight() int {
	if _, ok := m.pendingPermission(); !ok {
		return 0
	}
	if !m.ready {
		return permissionPanelMaxHeight
	}
	contentHeight := m.bodyHeight()
	// No working-line reservation: a pending permission suppresses it (see
	// showsWorking), so the panel takes that row too.
	baseReserved := m.composerReservedLines() + m.composer.menuHeight()
	available := max(contentHeight-baseReserved, 0)
	if len(m.entries) > 0 && available > 0 {
		available--
	}
	return min(available, permissionPanelMaxHeight)
}

func (m Model) permissionPanelLayout() (permissionPanelLayout, bool) {
	permission, ok := m.pendingPermission()
	if !ok {
		return permissionPanelLayout{}, false
	}
	_, margin, panelWidth := m.permissionPanelBox()
	height := m.permissionPanelHeight()
	lines, metadata := m.permissionPanelLines(permission, panelWidth, height)
	if len(lines) == 0 {
		return permissionPanelLayout{}, false
	}
	x := margin
	y := m.viewport.Height + m.composer.menuHeight()
	return permissionPanelLayout{
		x: x, y: y, width: panelWidth, height: len(lines),
		actionY: metadata.actionY, actions: metadata.actions,
		commandStart: metadata.commandStart, commandEnd: metadata.commandEnd,
	}, true
}

func (m Model) permissionPanelView() string {
	permission, ok := m.pendingPermission()
	if !ok {
		return ""
	}
	width, margin, panelWidth := m.permissionPanelBox()
	lines, _ := m.permissionPanelLines(permission, panelWidth, m.permissionPanelHeight())
	if len(lines) == 0 {
		return ""
	}
	left := strings.Repeat(" ", margin)
	right := strings.Repeat(" ", max(width-margin-panelWidth, 0))
	for index, line := range lines {
		lines[index] = left + line + right
	}
	return strings.Join(lines, "\n") + "\n"
}

type permissionPanelMetadata struct {
	actionY      int
	actions      []permissionActionRange
	commandStart int
	commandEnd   int
}

func (m Model) permissionPanelLines(permission entry, width, height int) ([]string, permissionPanelMetadata) {
	if width <= 0 || height <= 0 {
		return nil, permissionPanelMetadata{}
	}
	// A tool that can state its call as text gets the compact panel, whose body IS
	// that text — the exact thing being authorized. One that cannot falls through to
	// the detailed panel, which shows the raw input instead. The distinction is the
	// tool's answer, not a list of names kept here, so an MCP tool that cannot
	// summarize itself degrades to the honest panel rather than to a wrong one.
	p := m.presentationOf(permission)
	if p.Body != "" {
		return m.compactPermissionPanelLines(permission, activityLabel(p, permission), p.Body, width, height)
	}
	labels := m.permissionActionLabels(permission, width)
	selected := clampPermissionChoice(m.permissionChoice, permissionChoice(len(labels)))
	if height == 1 {
		return []string{permissionPanelStyle.Width(width).Render(permissionActionRow(labels, selected, width))}, permissionPanelMetadata{
			actionY:      0,
			actions:      clampActionRanges(permissionActionRanges(labels), width),
			commandStart: -1, commandEnd: -1,
		}
	}
	count := m.pendingPermissionCount()
	title := "Permission required"
	if count > 1 {
		title += fmt.Sprintf(" · 1 of %d", count)
	}
	toolLabel := permissionToolLabel(activityLabel(p, permission))
	origin := "Requested by main agent"
	if permission.sessionID != "" && permission.sessionID != m.sessionID {
		origin = "Requested by subagent"
	}
	workingDirectory := m.workDir
	if workingDirectory == "" {
		workingDirectory = m.workspaceRoot
	}
	if workingDirectory == "" {
		workingDirectory = "."
	}

	plainLines := []string{title}
	lineKinds := []int{0}
	if height >= 3 {
		plainLines = append(plainLines, toolLabel+" · "+origin)
		lineKinds = append(lineKinds, 0)
	}
	if height >= 4 {
		plainLines = append(plainLines, "Working directory  "+workingDirectory)
		lineKinds = append(lineKinds, 0)
	}

	showHelp := height >= 6
	fixedAfterCommand := 1
	if showHelp {
		fixedAfterCommand++
	}
	commandSlots := height - len(plainLines) - fixedAfterCommand
	metadata := permissionPanelMetadata{commandStart: -1, commandEnd: -1}
	if commandSlots > 0 {
		commandLines := permissionInputLines(permission, max(width-2, 1))
		visible := min(commandSlots, 4, len(commandLines))
		maxScroll := max(len(commandLines)-visible, 0)
		scroll := min(max(m.permissionScroll, 0), maxScroll)
		metadata.commandStart = len(plainLines)
		for _, line := range commandLines[scroll : scroll+visible] {
			plainLines = append(plainLines, " "+line)
			lineKinds = append(lineKinds, 1)
		}
		metadata.commandEnd = len(plainLines)
		if scroll+visible < len(commandLines) && visible > 0 {
			last := len(plainLines) - 1
			plainLines[last] = ansi.Truncate(plainLines[last], max(width-len(" ↓ more"), 0), "") + " ↓ more"
		}
	}

	metadata.actionY = len(plainLines)
	metadata.actions = clampActionRanges(permissionActionRanges(labels), width)
	plainLines = append(plainLines, permissionActionRowText(labels))
	lineKinds = append(lineKinds, 2)
	if showHelp && len(plainLines) < height {
		plainLines = append(plainLines, "←/→ select · ↑/↓ scroll · enter confirm · esc deny")
		lineKinds = append(lineKinds, 0)
	}

	lines := make([]string, len(plainLines))
	for index, line := range plainLines {
		line = sanitizeTerminalText(line)
		line = ansi.Truncate(line, width, "")
		switch lineKinds[index] {
		case 1:
			lines[index] = permissionCommandStyle.Width(width).Render(line)
		case 2:
			lines[index] = permissionPanelStyle.Width(width).Render(permissionActionRow(labels, selected, width))
		default:
			if index == 0 {
				line = permissionAccentStyle.Render(line)
			} else {
				line = metadataStyle.Render(line)
			}
			lines[index] = permissionPanelStyle.Width(width).Render(line)
		}
	}
	return lines, metadata
}

// permissionActionRowText renders the action labels without terminal styling,
// matching the visual spacing of the styled button row.
func permissionActionRowText(labels []string) string {
	parts := make([]string, len(labels))
	for index, label := range labels {
		parts[index] = " " + label + " "
	}
	return strings.Join(parts, strings.Repeat(" ", permissionActionGap))
}

// permissionActionRow renders the panel action row: one button per action, the
// selected one on the active background.
func permissionActionRow(labels []string, selected permissionChoice, width int) string {
	parts := make([]string, len(labels))
	for index, label := range labels {
		style := permissionButtonStyle
		if permissionChoice(index) == selected {
			style = permissionActiveStyle
		}
		parts[index] = style.Render(label)
	}
	gap := permissionPanelStyle.Render(strings.Repeat(" ", permissionActionGap))
	return ansi.Truncate(strings.Join(parts, gap), width, "")
}

func (m Model) compactPermissionPanelLines(permission entry, label, body string, width, height int) ([]string, permissionPanelMetadata) {
	metadata := permissionPanelMetadata{commandStart: -1, commandEnd: -1}
	labels := m.permissionActionLabels(permission, width)
	selected := clampPermissionChoice(m.permissionChoice, permissionChoice(len(labels)))
	metadata.actions = clampActionRanges(permissionActionRanges(labels), width)
	if height == 1 {
		metadata.actionY = 0
		return []string{permissionPanelStyle.Width(width).Render(permissionActionRow(labels, selected, width))}, metadata
	}

	plainLines := []string{"Permission required"}
	lineKinds := []int{0}
	showSpacing := height >= 5
	if showSpacing {
		plainLines = append(plainLines, "")
		lineKinds = append(lineKinds, 3)
	}
	fixedAfterCommand := 1
	if showSpacing {
		fixedAfterCommand++
	}
	commandSlots := height - len(plainLines) - fixedAfterCommand
	if commandSlots > 0 {
		commandLines := compactPermissionInputLines(permission, label, body, width)
		visible := min(commandSlots, 4, len(commandLines))
		maxScroll := max(len(commandLines)-visible, 0)
		scroll := min(max(m.permissionScroll, 0), maxScroll)
		metadata.commandStart = len(plainLines)
		for _, line := range commandLines[scroll : scroll+visible] {
			plainLines = append(plainLines, line)
			lineKinds = append(lineKinds, 1)
		}
		metadata.commandEnd = len(plainLines)
		if scroll+visible < len(commandLines) && visible > 0 {
			last := len(plainLines) - 1
			plainLines[last] = ansi.Truncate(plainLines[last], max(width-len(" ↓ more"), 0), "") + " ↓ more"
		}
	}
	if showSpacing {
		plainLines = append(plainLines, "")
		lineKinds = append(lineKinds, 3)
	}

	metadata.actionY = len(plainLines)
	plainLines = append(plainLines, permissionActionRowText(labels))
	lineKinds = append(lineKinds, 2)

	lines := make([]string, len(plainLines))
	for index, line := range plainLines {
		line = ansi.Truncate(sanitizeTerminalText(line), width, "")
		switch lineKinds[index] {
		case 1:
			lines[index] = renderCompactPermissionCommandLine(line, label, width)
		case 2:
			lines[index] = permissionPanelStyle.Width(width).Render(permissionActionRow(labels, selected, width))
		case 3:
			lines[index] = permissionPanelStyle.Width(width).Render("")
		default:
			lines[index] = permissionTitleStyle.Width(width).Render(line)
		}
	}
	return lines, metadata
}

// renderCompactPermissionCommandLine styles a command-surface row: the first
// row carries the muted tool label so the body remains the primary focus;
// continuation rows (indented by the wrap) render plain.
func renderCompactPermissionCommandLine(line, label string, width int) string {
	prefix := " " + label + " "
	if !strings.HasPrefix(line, prefix) {
		return permissionCommandStyle.Width(width).Render(line)
	}
	rest := strings.TrimPrefix(line, prefix)
	styled := permissionCommandStyle.Render(" ") +
		permissionCompactLabelStyle.Render(label) +
		permissionCommandStyle.Render(" "+rest)
	remaining := max(width-ansi.StringWidth(styled), 0)
	return styled + permissionCommandStyle.Render(strings.Repeat(" ", remaining))
}

// compactPermissionInputLines lays out the compact panel body: the tool label
// prefixes the first line and continuation lines align under it, wrapped to
// the surface width (the caller scrolls them).
func compactPermissionInputLines(permission entry, label, body string, width int) []string {
	prefix := " " + label + " "
	text := sanitizeTerminalText(body)
	if text == "" {
		text = "No input provided"
	}
	if width > 0 {
		text = ansi.Wrap(text, max(width-len(prefix), 1), "")
	}
	lines := strings.Split(text, "\n")
	for index := range lines {
		if index == 0 {
			lines[index] = prefix + lines[index]
		} else {
			lines[index] = strings.Repeat(" ", len(prefix)) + lines[index]
		}
	}
	return lines
}

// permissionToolLabel titles the detailed panel's request line. It is only reached
// by a tool that could not state its call as text, so the title says which tool is
// asking and the body below shows its raw input.
func permissionToolLabel(label string) string {
	if label == "" {
		return "Tool request"
	}
	return sanitizeTerminalText(label) + " request"
}

func permissionInputLines(permission entry, width int) []string {
	text := permissionInputText(permission)
	text = sanitizeTerminalText(text)
	if width > 0 {
		text = ansi.Wrap(text, width, "")
	}
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return []string{"No input provided"}
	}
	return lines
}

// permissionInputText renders the raw tool input as pretty JSON: the generic
// panel body and the compact fallback when a dedicated field is missing.
func permissionInputText(permission entry) string {
	var value any
	if json.Unmarshal([]byte(permission.input), &value) == nil {
		if formatted, err := json.MarshalIndent(value, "", "  "); err == nil {
			return string(formatted)
		}
	}
	return permission.input
}

func (m Model) handlePermissionMouse(msg tea.MouseMsg, permission entry) (Model, bool) {
	layout, ok := m.permissionPanelLayout()
	if !ok {
		return m, false
	}
	inside := msg.X >= layout.x && msg.X < layout.x+layout.width && msg.Y >= layout.y && msg.Y < layout.y+layout.height
	if !inside {
		if msg.Action == tea.MouseActionPress && (msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown) {
			return m, false
		}
		return m, true
	}
	if msg.Action != tea.MouseActionPress {
		return m, true
	}
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		row := msg.Y - layout.y
		if row >= layout.commandStart && row < layout.commandEnd {
			if msg.Button == tea.MouseButtonWheelUp {
				m.permissionScroll = max(m.permissionScroll-1, 0)
			} else {
				m.permissionScroll++
			}
			return m, true
		}
		return m, false
	}
	if msg.Button != tea.MouseButtonLeft || msg.Y-layout.y != layout.actionY {
		return m, true
	}
	x := msg.X - layout.x
	for index, action := range layout.actions {
		if action.start < action.end && x >= action.start && x < action.end {
			return m.resolvePermission(permission, permissionVerdict(permissionChoice(index))), true
		}
	}
	return m, true
}
