// Package wailsworkspace owns the live workspace-dependent agent wiring used
// by the Wails adapter. It publishes root, file listing, commands, runner and
// MCP tools as one lifecycle-serialized configuration.
package wailsworkspace

import (
	"context"
	"fmt"
	"os"
	"sync"

	"atenea/internal/agent"
	"atenea/internal/command"
	"atenea/internal/event"
	"atenea/internal/llm"
	"atenea/internal/mcpclient"
	"atenea/internal/session"
	"atenea/internal/tool"
	"atenea/internal/wiring"
)

// ProviderState is the provider snapshot consumed by one wiring build.
type ProviderState struct {
	Provider llm.Provider
	Local    bool
}

// Config contains the stable dependencies shared by every workspace build.
type Config struct {
	Root          string
	ProviderState func() ProviderState
	Store         session.Store
	Inbox         session.Inbox
	Gate          session.PermissionGate
	Snapshots     *tool.SessionSnapshots
	Bus           *event.Bus
	Agent         *agent.Service
}

// Manager owns workspace and MCP lifecycle state. Admit serializes prompt
// admission with every rebuild, so a prompt always sees one complete wiring.
type Manager struct {
	lifecycleMu sync.Mutex
	mu          sync.Mutex
	root        string
	glob        *tool.GlobTool
	providers   func() ProviderState
	store       session.Store
	inbox       session.Inbox
	gate        session.PermissionGate
	snaps       *tool.SessionSnapshots
	bus         *event.Bus
	agent       *agent.Service
	mcp         *mcpclient.Manager
}

// New builds and publishes the initial workspace wiring.
func New(cfg Config) *Manager {
	m := &Manager{
		providers: cfg.ProviderState,
		store:     cfg.Store,
		inbox:     cfg.Inbox,
		gate:      cfg.Gate,
		snaps:     cfg.Snapshots,
		bus:       cfg.Bus,
		agent:     cfg.Agent,
		mcp:       mcpclient.NewManager(cfg.Root),
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

// Reconfigure runs change and publishes wiring from its resulting provider
// snapshot as one lifecycle operation. Failed changes leave wiring untouched.
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
	state := m.providers()
	built := wiring.Build(wiring.Config{
		Root: root, Provider: state.Provider, Store: m.store, Inbox: m.inbox,
		Gate: m.gate, Snaps: m.snaps, Bus: m.bus, Local: state.Local,
		NextID: wiring.NewIDGen(), Mode: m.agent.Mode, MCPTools: m.mcp.Tools(),
	})
	m.mu.Lock()
	m.root = root
	m.glob = built.Glob
	m.mu.Unlock()
	m.agent.Configure(built.Runner, built.Commands)
}
