// Package wailsworkspace owns the live workspace-dependent agent wiring used
// by the Wails adapter. It publishes root, file listing, commands, runner and
// MCP tools as one lifecycle-serialized configuration.
package wailsworkspace

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/event"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/mcpclient"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/wiring"
)

// Config contains the stable dependencies shared by every workspace build.
type Config struct {
	Root string
	// Provider is the handle every build wires, not the adapter of the moment: it
	// is switchable, so selecting another model swaps what it delegates to without
	// this manager rebuilding anything.
	Provider llm.Provider
	// LocalPrompt is the per-turn question wiring asks about that selection; see
	// wiring.Config.LocalPrompt.
	LocalPrompt func() bool
	Store       session.Store
	Inbox       session.Inbox
	Gate        permission.Gate
	Snapshots   *tool.SessionSnapshots
	Bus         *event.Bus
	Agent       *agent.Service
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
	inbox       session.Inbox
	gate        permission.Gate
	snaps       *tool.SessionSnapshots
	bus         *event.Bus
	agent       *agent.Service
	mcp         *mcpclient.Manager
}

// New builds and publishes the initial workspace wiring.
func New(cfg Config) *Manager {
	m := &Manager{
		provider:    cfg.Provider,
		localPrompt: cfg.LocalPrompt,
		store:       cfg.Store,
		inbox:       cfg.Inbox,
		gate:        cfg.Gate,
		snaps:       cfg.Snapshots,
		bus:         cfg.Bus,
		agent:       cfg.Agent,
		mcp:         mcpclient.NewManager(cfg.Root),
	}
	m.lifecycleMu.Lock()
	m.rebuildLocked(cfg.Root)
	m.lifecycleMu.Unlock()
	return m
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
		return fmt.Errorf("workspace invalido: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace invalido: %s no es una carpeta", path)
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mcp.SetRoot(path)
	m.rebuildLocked(path)
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
func (m *Manager) Commands() []command.Command { return m.agent.Commands() }

// Close stops all connected MCP processes after excluding lifecycle changes.
func (m *Manager) Close() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mcp.Close()
}

func (m *Manager) rebuildLocked(root string) {
	built := wiring.Build(wiring.Config{
		Root: root, Provider: m.provider, Store: m.store, Inbox: m.inbox,
		Gate: m.gate, Snaps: m.snaps, Bus: m.bus, LocalPrompt: m.localPrompt,
		NextID: wiring.NewIDGen(), Mode: m.agent.Mode, MCPTools: m.mcp.Tools(),
	})
	m.mu.Lock()
	m.root = root
	m.glob = built.Glob
	m.mu.Unlock()
	m.agent.Configure(built.Runner, built.Commands)
}
