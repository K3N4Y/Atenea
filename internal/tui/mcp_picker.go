package tui

// The /mcp picker: a full-screen modal (mirroring the /model picker) that
// lists the MCP servers declared in .mcp.json and toggles each one on or off.
// Connecting spawns a subprocess and can take seconds, so toggles run as
// asynchronous commands: the row shows starting…/stopping… until the
// mcpToggleDoneMsg lands and the list refreshes from the agent.
//
// The list UX (cursor, wrap, scroll window) and the bordered panel chrome live
// in the overlay module; this file owns only the MCP domain and its async
// toggles (the busy map and the mcpGen-guarded mcpToggleDoneMsg).

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"atenea/internal/mcpclient"
)

// mcpAgent is the engine surface the picker needs. Connect/Disconnect block
// while the server process starts or stops, so the Model always calls them
// from a tea.Cmd, never from Update.
type mcpAgent interface {
	MCPServers() ([]mcpclient.ServerStatus, error)
	ConnectMCPServer(name string) error
	DisconnectMCPServer(name string) error
}

// mcpToggleDoneMsg reports the outcome of one connect/disconnect. generation
// invalidates results from a picker that was closed and reopened meanwhile.
type mcpToggleDoneMsg struct {
	generation uint64
	name       string
	err        string
}

type mcpPicker struct {
	open        bool
	servers     []mcpclient.ServerStatus
	overlayList // navigation: selected + move + window over servers
	// busy marks servers with a toggle in flight; each entry clears when its
	// mcpToggleDoneMsg arrives, so several servers can toggle concurrently.
	busy map[string]bool
	err  string
}

func newMCPPicker() mcpPicker {
	return mcpPicker{open: true, busy: make(map[string]bool)}
}

// refreshFromAgent reloads the merged server list, keeping the selection on
// the same server by name. A listing error (e.g. a broken .mcp.json) lands in
// err and shows inside the panel.
func (p *mcpPicker) refreshFromAgent(agent Agent) {
	controller, ok := agent.(mcpAgent)
	if !ok {
		return
	}
	selected, hadSelection := p.selectedServer()
	servers, err := controller.MCPServers()
	if err != nil {
		p.servers = nil
		p.setCount(0)
		p.err = err.Error()
		return
	}
	p.servers = servers
	p.overlayList.selected = 0
	if hadSelection {
		for index, server := range servers {
			if server.Name == selected.Name {
				p.overlayList.selected = index
				break
			}
		}
	}
	p.setCount(len(servers))
}

func (p mcpPicker) selectedServer() (mcpclient.ServerStatus, bool) {
	index, ok := p.hasSelection()
	if !ok || index >= len(p.servers) {
		return mcpclient.ServerStatus{}, false
	}
	return p.servers[index], true
}

// handleMCPPickerKey routes the keyboard while the picker is open: arrows
// move, enter or space toggles the selected server, r reloads the list (picks
// up .mcp.json edits), esc closes. Everything else is inert.
func (m Model) handleMCPPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mcpPicker.open = false
		return m.resizeViewport(), nil
	case tea.KeyUp:
		m.mcpPicker.move(-1)
		return m, nil
	case tea.KeyDown:
		m.mcpPicker.move(1)
		return m, nil
	case tea.KeyEnter, tea.KeySpace:
		return m.toggleMCPSelection()
	}
	switch keyRune(msg) {
	case " ":
		return m.toggleMCPSelection()
	case "r":
		m.mcpPicker.refreshFromAgent(m.agent)
		return m, nil
	}
	return m, nil
}

// handleMCPPickerMouse mirrors the keyboard: the wheel moves the selection
// and a left click on a row selects and toggles it (same path as enter).
func (m Model) handleMCPPickerMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.mcpPicker.move(-1)
	case tea.MouseButtonWheelDown:
		m.mcpPicker.move(1)
	case tea.MouseButtonLeft:
		layout := overlayLayoutFor(m.width, m.height)
		row, ok := layout.rowAt(msg.X, msg.Y)
		if !ok {
			return m, nil
		}
		errRows := m.mcpPicker.errRows()
		start, end := m.mcpPicker.window(layout.itemRows - errRows)
		index := start + row - errRows
		if row < errRows || index >= end {
			return m, nil
		}
		m.mcpPicker.overlayList.selected = index
		return m.toggleMCPSelection()
	}
	return m, nil
}

// toggleMCPSelection flips the selected server on or off asynchronously. The
// row is marked busy until its mcpToggleDoneMsg lands; toggling an
// already-busy server is a no-op.
func (m Model) toggleMCPSelection() (Model, tea.Cmd) {
	server, ok := m.mcpPicker.selectedServer()
	if !ok || m.mcpPicker.busy[server.Name] {
		return m, nil
	}
	controller, ok := m.agent.(mcpAgent)
	if !ok {
		m.mcpPicker.err = "MCP management is unavailable"
		return m, nil
	}
	m.mcpPicker.busy[server.Name] = true
	m.mcpPicker.err = ""
	generation := m.mcpGen
	name := server.Name
	connected := server.Connected
	return m, func() tea.Msg {
		var err error
		if connected {
			err = controller.DisconnectMCPServer(name)
		} else {
			err = controller.ConnectMCPServer(name)
		}
		done := mcpToggleDoneMsg{generation: generation, name: name}
		if err != nil {
			done.err = err.Error()
		}
		return done
	}
}

// errRows is the number of header rows the inline error banner consumes; the
// window and hit-testing offset item rows past it.
func (p mcpPicker) errRows() int {
	if p.err != "" {
		return 1
	}
	return 0
}

func (m Model) mcpPickerView() string {
	layout := overlayLayoutFor(m.width, m.height)
	innerWidth := layout.innerWidth
	itemRows := layout.itemRows
	nameWidth, statusWidth, toolsWidth, commandWidth := mcpPickerWidths(innerWidth)

	rows := make([]string, 0, itemRows)
	if m.mcpPicker.err != "" {
		rows = append(rows, errorStyle.Render(overlayCell(" "+sanitizeTerminalText(m.mcpPicker.err), innerWidth)))
	}
	start, end := m.mcpPicker.window(itemRows - len(rows))
	for index := start; index < end; index++ {
		rows = append(rows, m.mcpPickerRow(m.mcpPicker.servers[index], index == m.mcpPicker.selected,
			nameWidth, statusWidth, toolsWidth, commandWidth))
	}
	if len(m.mcpPicker.servers) == 0 && m.mcpPicker.err == "" {
		hint := "  Add them to " + mcpclient.ConfigFile + " at the workspace root"
		if global := mcpclient.GlobalConfigPath(); global != "" {
			hint += " or " + global
		}
		rows = append(rows,
			overlayCell("  No MCP servers configured", innerWidth),
			statusStyle.Render(overlayCell(hint, innerWidth)),
		)
	}
	for len(rows) < itemRows {
		rows = append(rows, strings.Repeat(" ", innerWidth))
	}

	lines := []string{
		overlayCell(" Server", nameWidth) + overlayCell("Status", statusWidth) +
			overlayCell("Tools", toolsWidth) + overlayCell("Command", commandWidth),
		strings.Repeat("─", max(innerWidth, 0)),
	}
	for index := 0; index < itemRows; index++ {
		lines = append(lines, overlayCell(rows[index], innerWidth))
	}
	lines = append(lines,
		strings.Repeat("─", max(innerWidth, 0)),
		overlayCell(" ↑↓ move · enter toggle · r reload · esc close", innerWidth),
	)

	return m.renderOverlayPanel(layout, "MCP Servers", lines)
}

// mcpPickerWidths splits the panel into name/status/tools/command columns;
// the command absorbs the remaining width and shows dimmed.
func mcpPickerWidths(innerWidth int) (int, int, int, int) {
	statusWidth := min(15, max(innerWidth/4, 0))
	toolsWidth := min(9, max(innerWidth/6, 0))
	nameWidth := min(max(innerWidth/4, 18), max(innerWidth-statusWidth-toolsWidth, 0))
	commandWidth := max(innerWidth-nameWidth-statusWidth-toolsWidth, 0)
	return nameWidth, statusWidth, toolsWidth, commandWidth
}

func (m Model) mcpPickerRow(server mcpclient.ServerStatus, selected bool, nameWidth, statusWidth, toolsWidth, commandWidth int) string {
	prefix := "  "
	if selected {
		prefix = "> "
	}
	glyph := "○ "
	if m.mcpPicker.busy[server.Name] {
		glyph = "◌ "
	} else if server.Connected {
		glyph = "● "
	}
	status := "off"
	switch {
	case m.mcpPicker.busy[server.Name] && server.Connected:
		status = "stopping…"
	case m.mcpPicker.busy[server.Name]:
		status = "starting…"
	case server.Connected:
		status = "on"
	}
	tools := "—"
	if server.Connected {
		tools = strconv.Itoa(server.Tools) + " tools"
	}
	command := sanitizeTerminalText(strings.TrimSpace(server.Command + " " + strings.Join(server.Args, " ")))

	row := overlayCell(prefix+glyph+sanitizeTerminalText(server.Name), nameWidth) +
		overlayCell(status, statusWidth) + overlayCell(tools, toolsWidth)
	commandCell := overlayCell(command, commandWidth)
	if selected {
		return accentStyle.Render(row + commandCell)
	}
	return row + statusStyle.Render(commandCell)
}
