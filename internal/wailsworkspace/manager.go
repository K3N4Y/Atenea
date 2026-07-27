// Package wailsworkspace owns the live workspace-dependent agent wiring used
// by the Wails adapter. It publishes root, file listing, commands, runner and
// MCP tools as one lifecycle-serialized configuration.
package wailsworkspace

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/event"
	"github.com/K3N4Y/atenea/internal/host"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/mcpclient"
	"github.com/K3N4Y/atenea/internal/paths"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/wiring"
)

// Config contains the stable dependencies shared by every workspace build.
type Config struct {
	Identity paths.Identity
	Root     string
	// Provider is the handle every build wires, not the adapter of the moment: it
	// is switchable, so selecting another model swaps what it delegates to without
	// this manager rebuilding anything.
	Provider llm.Provider
	// LocalPrompt is the per-turn question wiring asks about that selection; see
	// wiring.Config.LocalPrompt.
	LocalPrompt func() bool
	Store       session.Store
	Bus         *event.Bus
	// Sitting is the per-process agent state this manager rewires into every build
	// rather than rebuilding: the permission gate, the prompt inbox, the read
	// snapshots and the turn lifecycle it reconfigures. See host.Sitting.
	Sitting *host.Sitting
}

// Manager owns workspace and MCP lifecycle state. Admit serializes prompt
// admission with every rebuild, so a prompt always sees one complete wiring.
type Manager struct {
	lifecycleMu sync.Mutex
	mu          sync.Mutex
	root        string
	glob        *tool.GlobTool
	provider    llm.Provider
	localPrompt func() bool
	store       session.Store
	bus         *event.Bus
	sitting     *host.Sitting
	mcp         *mcpclient.Manager
}

// New builds and publishes the initial workspace wiring.
func New(cfg Config) *Manager {
	m := &Manager{
		provider:    cfg.Provider,
		localPrompt: cfg.LocalPrompt,
		store:       cfg.Store,
		bus:         cfg.Bus,
		sitting:     cfg.Sitting,
		mcp:         mcpclient.NewManagerWithRuntime(cfg.Root, cfg.Identity, cfg.Provider, nil),
	}
	m.lifecycleMu.Lock()
	m.rebuildLocked(cfg.Root)
	m.lifecycleMu.Unlock()
	if configs, err := mcpclient.LoadConfig(cfg.Root); err == nil {
		m.mcp.Start(configs, m.refreshMCP)
	}
	return m
}

func (m *Manager) refreshMCP() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.rebuildLocked(m.root)
}

// Admit runs fn while workspace reconfiguration is excluded.
func (m *Manager) Admit(fn func() error) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	return fn()
}

// Root returns the currently published workspace root.
func (m *Manager) Root() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.root
}

// SetRoot validates and atomically publishes wiring for path.
func (m *Manager) SetRoot(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("invalid workspace: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("invalid workspace: %s is not a folder", path)
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mcp.SetRoot(path)
	m.rebuildLocked(path)
	if configs, err := mcpclient.LoadConfig(path); err == nil {
		m.mcp.Start(configs, m.refreshMCP)
	}
	return nil
}

// Reconfigure runs change and republishes wiring as one lifecycle operation, so
// no prompt is admitted in between. A failed change leaves wiring untouched.
//
// The republish is what cuts the runs in flight: they were streaming from the
// selection the change replaced, and a turn that finishes under a model the user
// has already left is not one they asked for.
func (m *Manager) Reconfigure(change func() error) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if err := change(); err != nil {
		return err
	}
	m.rebuildLocked(m.root)
	return nil
}

// Files lists files using the glob from the currently published wiring.
func (m *Manager) Files(ctx context.Context) ([]string, error) {
	m.mu.Lock()
	g := m.glob
	m.mu.Unlock()
	files, _, err := g.Files(ctx, "", ".", g.MaxLimit)
	files = append(files, m.mcp.ResourceNames()...)
	return files, err
}

// ConnectMCP connects a server and publishes its tools before admitting a new turn.
func (m *Manager) ConnectMCP(ctx context.Context, cfg mcpclient.ServerConfig) (mcpclient.ServerStatus, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	status, err := m.mcp.Connect(ctx, cfg)
	if err != nil {
		return mcpclient.ServerStatus{}, err
	}
	m.rebuildLocked(m.root)
	return status, nil
}

// DisconnectMCP disconnects a server and removes its tools from future turns.
func (m *Manager) DisconnectMCP(name string) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if err := m.mcp.Disconnect(name); err != nil {
		return err
	}
	m.rebuildLocked(m.root)
	return nil
}

// MCPStatus returns a snapshot of live MCP connections.
func (m *Manager) MCPStatus() []mcpclient.ServerStatus {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	return m.mcp.Status()
}

// Commands returns the commands from the currently configured agent.
func (m *Manager) Commands() []command.Command { return m.sitting.Agent.Commands() }

// Close stops all connected MCP processes after excluding lifecycle changes.
func (m *Manager) Close() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mcp.Close()
}

// rebuildLocked re-anchors the whole wiring at root and publishes it.
//
// The sitting's Grants deliberately stays out of the build. The desktop's
// ResolveToolPermission binding carries an approve/deny boolean and cannot
// express "allow for the rest of the session", so handing wiring a store nothing
// ever writes to would advertise an affordance this UI does not have. The day the
// frontend grows the third button, this is the line that changes.
func (m *Manager) rebuildLocked(root string) {
	built := wiring.Build(wiring.Config{
		Root: root, Provider: m.provider, Store: m.store, Inbox: m.sitting.Inbox,
		Gate: m.sitting.Gate, Snaps: m.sitting.Snapshots, Bus: m.bus, LocalPrompt: m.localPrompt,
		NextID: wiring.NewIDGen(), Mode: m.sitting.Agent.Mode, MCPTools: m.mcp.Tools(),
		PersistentGrants: m.mcp.PermissionRules(),
	})
	m.mu.Lock()
	m.root = root
	m.glob = built.Glob
	m.mu.Unlock()
	commands := append(built.Commands.List(), m.mcp.Commands()...)
	m.sitting.Agent.Configure(built.Runner, command.New(commands, m.mcp.Mentions()...))
}
