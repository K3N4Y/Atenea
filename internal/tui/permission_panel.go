package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/tui/theme"
)

const (
	permissionPanelMaxHeight     = 9
	permissionPanelFallbackWidth = 48
)

var (
	permissionPanelStyle     = lipgloss.NewStyle().Background(lipgloss.Color(theme.PermissionPanel))
	permissionCommandStyle   = lipgloss.NewStyle().Background(lipgloss.Color(theme.PermissionCommand))
	permissionAccentStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.Success))
	permissionSelectionStyle = lipgloss.NewStyle().Bold(true)
	permissionTitleStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Canvas)).Background(lipgloss.Color(theme.PermissionActive))
	permissionButtonStyle    = lipgloss.NewStyle().Background(lipgloss.Color(theme.PermissionCommand)).Padding(0, 1)
	permissionActiveStyle    = permissionButtonStyle.Bold(true).Foreground(lipgloss.Color(theme.Canvas)).Background(lipgloss.Color(theme.PermissionActive))
	permissionBashLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Muted)).Background(lipgloss.Color(theme.PermissionCommand))
)

type permissionPanelLayout struct {
	x            int
	y            int
	width        int
	height       int
	actionY      int
	denyStart    int
	denyEnd      int
	allowStart   int
	allowEnd     int
	commandStart int
	commandEnd   int
}

func (layout permissionPanelLayout) actionPoint(choice permissionChoice) (int, int) {
	start, end := layout.denyStart, layout.denyEnd
	if choice == permissionAllowOnce {
		start, end = layout.allowStart, layout.allowEnd
	}
	return layout.x + start + max((end-start)/2, 0), layout.y + layout.actionY
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
	baseReserved := m.composerReservedLines() + len(m.menuItems)
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
	width := m.chatContentWidth()
	margin := min(composerOuterMargin, width/2)
	panelWidth := max(width-2*margin, 0)
	height := m.permissionPanelHeight()
	lines, metadata := m.permissionPanelLines(permission, panelWidth, height)
	if len(lines) == 0 {
		return permissionPanelLayout{}, false
	}
	x := margin
	y := m.viewport.Height + len(m.menuItems)
	if m.chatPanelVisible() {
		// El chat es la columna derecha: se corre x tras el arbol y su gutter de
		// una columna. Sin caja no hay borde ni titulo, asi que y no se desplaza.
		x += m.treePanelWidth() + 1
	}
	return permissionPanelLayout{
		x: x, y: y, width: panelWidth, height: len(lines),
		actionY: metadata.actionY, denyStart: metadata.denyStart, denyEnd: metadata.denyEnd,
		allowStart: metadata.allowStart, allowEnd: metadata.allowEnd,
		commandStart: metadata.commandStart, commandEnd: metadata.commandEnd,
	}, true
}

func (m Model) permissionPanelView() string {
	permission, ok := m.pendingPermission()
	if !ok {
		return ""
	}
	width := m.chatContentWidth()
	if !m.ready || width <= 0 {
		width = permissionPanelFallbackWidth
	}
	margin := min(composerOuterMargin, width/2)
	panelWidth := max(width-2*margin, 0)
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
	denyStart    int
	denyEnd      int
	allowStart   int
	allowEnd     int
	commandStart int
	commandEnd   int
}

func (m Model) permissionPanelLines(permission entry, width, height int) ([]string, permissionPanelMetadata) {
	if width <= 0 || height <= 0 {
		return nil, permissionPanelMetadata{}
	}
	if strings.EqualFold(permission.tool, "bash") {
		return m.bashPermissionPanelLines(permission, width, height)
	}
	if height == 1 {
		line := "› Deny    Allow once"
		if m.permissionChoice == permissionAllowOnce {
			line = "Deny    › Allow once"
		}
		line = ansi.Truncate(line, width, "")
		return []string{permissionPanelStyle.Width(width).Render(permissionSelectionStyle.Render(line))}, permissionPanelMetadata{
			actionY: 0, denyStart: 0, denyEnd: min(len("› Deny"), width),
			allowStart: min(len("› Deny    "), width), allowEnd: min(len("› Deny    Allow once"), width),
			commandStart: -1, commandEnd: -1,
		}
	}
	count := m.pendingPermissionCount()
	title := "Permission required"
	if count > 1 {
		title += fmt.Sprintf(" · 1 of %d", count)
	}
	toolLabel := permissionToolLabel(permission.tool)
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

	deny := "› Deny"
	allow := "Allow once"
	if m.permissionChoice == permissionAllowOnce {
		deny = "Deny"
		allow = "› Allow once"
	}
	actions := deny + "    " + allow
	metadata.actionY = len(plainLines)
	metadata.denyStart = 0
	metadata.denyEnd = len(deny)
	metadata.allowStart = len(deny) + 4
	metadata.allowEnd = metadata.allowStart + len(allow)
	plainLines = append(plainLines, actions)
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
			styled := line
			if m.permissionChoice == permissionDeny {
				styled = permissionSelectionStyle.Render(deny) + "    " + allow
			} else {
				styled = deny + "    " + permissionSelectionStyle.Render(allow)
			}
			lines[index] = permissionPanelStyle.Width(width).Render(styled)
		default:
			if index == 0 {
				line = permissionAccentStyle.Render(line)
			} else {
				line = statusStyle.Render(line)
			}
			lines[index] = permissionPanelStyle.Width(width).Render(line)
		}
	}
	return lines, metadata
}

func (m Model) bashPermissionPanelLines(permission entry, width, height int) ([]string, permissionPanelMetadata) {
	metadata := permissionPanelMetadata{commandStart: -1, commandEnd: -1}
	if height == 1 {
		denyStyle, allowStyle := permissionButtonStyle, permissionButtonStyle
		if m.permissionChoice == permissionDeny {
			denyStyle = permissionActiveStyle
		} else {
			allowStyle = permissionActiveStyle
		}
		line := denyStyle.Render("Deny") + permissionPanelStyle.Render("    ") + allowStyle.Render("Allow")
		metadata.actionY = 0
		metadata.denyEnd = min(len(" Deny "), width)
		metadata.allowStart = min(len(" Deny     "), width)
		metadata.allowEnd = min(len(" Deny     Allow "), width)
		return []string{permissionPanelStyle.Width(width).Render(line)}, metadata
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
		commandLines := bashPermissionInputLines(permission, width)
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

	deny := "Deny"
	allow := "Allow"
	metadata.actionY = len(plainLines)
	metadata.denyEnd = len(" Deny ")
	metadata.allowStart = len(" Deny ") + 4
	metadata.allowEnd = metadata.allowStart + len(" Allow ")
	plainLines = append(plainLines, deny+"    "+allow)
	lineKinds = append(lineKinds, 2)

	lines := make([]string, len(plainLines))
	for index, line := range plainLines {
		line = ansi.Truncate(sanitizeTerminalText(line), width, "")
		switch lineKinds[index] {
		case 1:
			lines[index] = renderBashPermissionCommandLine(line, width)
		case 2:
			denyStyle, allowStyle := permissionButtonStyle, permissionButtonStyle
			if m.permissionChoice == permissionDeny {
				denyStyle = permissionActiveStyle
			} else {
				allowStyle = permissionActiveStyle
			}
			styled := denyStyle.Render(deny) + permissionPanelStyle.Render("    ") + allowStyle.Render(allow)
			lines[index] = permissionPanelStyle.Width(width).Render(styled)
		case 3:
			lines[index] = permissionPanelStyle.Width(width).Render("")
		default:
			lines[index] = permissionTitleStyle.Width(width).Render(line)
		}
	}
	return lines, metadata
}

func renderBashPermissionCommandLine(line string, width int) string {
	const prefix = " Bash "
	if !strings.HasPrefix(line, prefix) {
		return permissionCommandStyle.Width(width).Render(line)
	}
	rest := strings.TrimPrefix(line, prefix)
	styled := permissionCommandStyle.Render(" ") +
		permissionBashLabelStyle.Render("Bash") +
		permissionCommandStyle.Render(" "+rest)
	remaining := max(width-ansi.StringWidth(styled), 0)
	return styled + permissionCommandStyle.Render(strings.Repeat(" ", remaining))
}

func bashPermissionInputLines(permission entry, width int) []string {
	const prefix = " Bash "
	text := sanitizeTerminalText(permissionInputText(permission))
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

func permissionToolLabel(tool string) string {
	if strings.EqualFold(tool, "bash") {
		return "Bash command"
	}
	if tool == "" {
		return "Tool request"
	}
	return sanitizeTerminalText(tool) + " request"
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

func permissionInputText(permission entry) string {
	if strings.EqualFold(permission.tool, "bash") {
		var input struct {
			Command string `json:"command"`
			Cmd     string `json:"cmd"`
		}
		if json.Unmarshal([]byte(permission.input), &input) == nil {
			if input.Command != "" {
				return input.Command
			}
			if input.Cmd != "" {
				return input.Cmd
			}
		}
	}
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
	switch {
	case x >= layout.denyStart && x < layout.denyEnd:
		return m.resolvePermission(permission, false), true
	case x >= layout.allowStart && x < layout.allowEnd:
		return m.resolvePermission(permission, true), true
	default:
		return m, true
	}
}
