package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/mcpclient"
)

// fakeMCPAgent extends fakeAgent with the mcpAgent surface: it flips the
// in-memory server list on connect/disconnect and records the calls.
type fakeMCPAgent struct {
	*fakeAgent
	servers       []mcpclient.ServerStatus
	listErr       error
	connects      []string
	disconnects   []string
	connectErr    error
	disconnectErr error
}

func (f *fakeMCPAgent) MCPServers() ([]mcpclient.ServerStatus, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]mcpclient.ServerStatus(nil), f.servers...), nil
}

func (f *fakeMCPAgent) ConnectMCPServer(name string) error {
	f.connects = append(f.connects, name)
	if f.connectErr != nil {
		return f.connectErr
	}
	for i := range f.servers {
		if f.servers[i].Name == name {
			f.servers[i].Connected = true
			f.servers[i].Tools = 3
		}
	}
	return nil
}

func (f *fakeMCPAgent) DisconnectMCPServer(name string) error {
	f.disconnects = append(f.disconnects, name)
	if f.disconnectErr != nil {
		return f.disconnectErr
	}
	for i := range f.servers {
		if f.servers[i].Name == name {
			f.servers[i].Connected = false
			f.servers[i].Tools = 0
		}
	}
	return nil
}

func newMCPTestAgent(servers ...mcpclient.ServerStatus) *fakeMCPAgent {
	return &fakeMCPAgent{fakeAgent: &fakeAgent{}, servers: servers}
}

func mcpServer(name, command string, connected bool, tools int) mcpclient.ServerStatus {
	return mcpclient.ServerStatus{
		ServerConfig: mcpclient.ServerConfig{Name: name, Command: command},
		Connected:    connected,
		Tools:        tools,
	}
}

// applyCmd pasa un mensaje por Update y devuelve tambien el cmd, para poder
// ejecutar el toggle asincrono del picker dentro del test.
func applyCmd(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	return next, cmd
}

func openMCPPicker(t *testing.T, agent Agent) Model {
	t.Helper()
	m := NewModel(agent, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeRunes(t, m, "/mcp")
	return apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
}

func TestModel_MCPCommandOpensPickerAndListsServers(t *testing.T) {
	agent := newMCPTestAgent(
		mcpServer("github", "npx github-mcp", false, 0),
		mcpServer("playwright", "npx @playwright/mcp", true, 12),
	)
	m := openMCPPicker(t, agent)

	if !m.mcpPicker.open {
		t.Fatal("/mcp must open the picker")
	}
	if m.input.Value() != "" {
		t.Fatalf("composer input = %q, want empty after /mcp", m.input.Value())
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"MCP Servers", "github", "playwright", "12 tools", "on", "off", "npx @playwright/mcp"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view is missing %q:\n%s", want, view)
		}
	}
}

func TestModel_MCPPickerShowsEmptyStateWithConfigHint(t *testing.T) {
	m := openMCPPicker(t, newMCPTestAgent())
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "No MCP servers configured") || !strings.Contains(view, ".mcp.json") {
		t.Fatalf("empty state must point at .mcp.json:\n%s", view)
	}
}

func TestModel_MCPPickerTogglesServerOnEnter(t *testing.T) {
	agent := newMCPTestAgent(mcpServer("github", "npx github-mcp", false, 0))
	m := openMCPPicker(t, agent)

	m, cmd := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a server must issue the async toggle cmd")
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "starting…") {
		t.Fatalf("the row must show the toggle in flight:\n%s", view)
	}
	// Enter de nuevo mientras esta busy: inerte, no duplica la conexion.
	m, dup := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if dup != nil {
		t.Fatal("toggling a busy server must be a no-op")
	}

	m = apply(t, m, cmd())
	if len(agent.connects) != 1 || agent.connects[0] != "github" {
		t.Fatalf("connects = %v, want [github]", agent.connects)
	}
	if len(m.mcpPicker.busy) != 0 {
		t.Fatalf("busy = %v, want empty after the toggle lands", m.mcpPicker.busy)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "3 tools") || !strings.Contains(view, "● github") {
		t.Fatalf("the row must show the server on with its tools:\n%s", view)
	}

	// Enter otra vez: ahora desconecta.
	m, cmd = applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, cmd())
	if len(agent.disconnects) != 1 || agent.disconnects[0] != "github" {
		t.Fatalf("disconnects = %v, want [github]", agent.disconnects)
	}
	if strings.Contains(ansi.Strip(m.View()), "3 tools") {
		t.Fatalf("the row must show the server off again:\n%s", ansi.Strip(m.View()))
	}
}

func TestModel_MCPPickerSurfacesConnectError(t *testing.T) {
	agent := newMCPTestAgent(mcpServer("github", "npx github-mcp", false, 0))
	agent.connectErr = errors.New("spawn npx: executable not found")
	m := openMCPPicker(t, agent)

	m, cmd := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, cmd())
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "executable not found") {
		t.Fatalf("connect error must show inside the panel:\n%s", view)
	}
	if strings.Contains(view, "● github") {
		t.Fatalf("a failed connect must leave the server off:\n%s", view)
	}
}

func TestModel_MCPPickerDropsStaleToggleResults(t *testing.T) {
	agent := newMCPTestAgent(mcpServer("github", "npx github-mcp", false, 0))
	m := openMCPPicker(t, agent)
	m, cmd := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// Cerrar y reabrir invalida la generacion: el resultado viejo no aplica.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = typeRunes(t, m, "/mcp")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	stale := cmd().(mcpToggleDoneMsg)
	stale.err = "stale failure"
	m = apply(t, m, stale)
	if m.mcpPicker.err != "" {
		t.Fatalf("stale toggle result must be dropped, err = %q", m.mcpPicker.err)
	}
}

func TestModel_MCPPickerEscClosesAndArgsShowUsage(t *testing.T) {
	m := openMCPPicker(t, newMCPTestAgent(mcpServer("github", "npx", false, 0)))
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mcpPicker.open {
		t.Fatal("esc must close the picker")
	}

	m = typeRunes(t, m, "/mcp extra")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mcpPicker.open {
		t.Fatal("/mcp with args must not open the picker")
	}
	if len(m.entries) == 0 || m.entries[len(m.entries)-1].text != "usage: /mcp" {
		t.Fatalf("entries = %+v, want a usage error", m.entries)
	}
}

func TestModel_MCPPickerUnavailableWithoutMCPAgent(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = typeRunes(t, m, "/mcp")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mcpPicker.open {
		t.Fatal("an agent without MCP support must not open the picker")
	}
	if len(m.entries) == 0 || !strings.Contains(m.entries[len(m.entries)-1].text, "unavailable") {
		t.Fatalf("entries = %+v, want an unavailable error", m.entries)
	}
}

func TestModel_MCPPickerClickTogglesRow(t *testing.T) {
	agent := newMCPTestAgent(
		mcpServer("github", "npx github-mcp", false, 0),
		mcpServer("playwright", "npx @playwright/mcp", false, 0),
	)
	m := openMCPPicker(t, agent)

	// Con 80x24 la primera fila de items queda en Y=4 (fila en blanco, borde,
	// cabecera, separador); la segunda fila (playwright) en Y=5.
	m, cmd := applyCmd(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 6, Y: 5})
	if cmd == nil {
		t.Fatal("click on a row must toggle it")
	}
	m = apply(t, m, cmd())
	if len(agent.connects) != 1 || agent.connects[0] != "playwright" {
		t.Fatalf("connects = %v, want [playwright]", agent.connects)
	}
	if m.mcpPicker.selected != 1 {
		t.Fatalf("selected = %d, want the clicked row", m.mcpPicker.selected)
	}
}

func TestModel_CommandMenuOffersMCPBuiltin(t *testing.T) {
	m := NewModel(newMCPTestAgent(), "s1", nil)
	m = typeRunes(t, m, "/mc")
	found := false
	for _, item := range m.menuItems {
		if item.label == "/mcp" && item.builtin {
			found = true
		}
	}
	if !found {
		t.Fatalf("menu items = %+v, want the /mcp builtin", m.menuItems)
	}
}
