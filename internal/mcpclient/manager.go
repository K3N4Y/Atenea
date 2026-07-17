// Package mcpclient connects Atenea to local MCP servers over stdio.
package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"atenea/internal/tool"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const connectTimeout = 30 * time.Second

var serverName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,48}$`)

// ServerConfig describes a local MCP server process. Command and Args map
// directly to the stdio process; Env augments the inherited environment.
type ServerConfig struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
}

// ServerStatus is the safe, serializable connection state exposed to the UI.
type ServerStatus struct {
	ServerConfig
	Connected bool `json:"connected"`
	Tools     int  `json:"tools"`
}

type server struct {
	config  ServerConfig
	client  *mcp.Client
	session *mcp.ClientSession
	tools   []tool.Tool
}

// Manager owns the subprocesses and MCP sessions for one application instance.
// It is safe for the runner and the settings UI to access concurrently.
type Manager struct {
	mu      sync.RWMutex
	root    string
	servers map[string]*server
}

func NewManager(root string) *Manager {
	return &Manager{root: root, servers: make(map[string]*server)}
}

// SetRoot updates the root advertised to connected MCP servers.
func (m *Manager) SetRoot(root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if root == m.root {
		return
	}
	old := rootURI(m.root)
	m.root = root
	newRoot := &mcp.Root{URI: rootURI(root), Name: filepath.Base(root)}
	for _, srv := range m.servers {
		srv.client.RemoveRoots(old)
		srv.client.AddRoots(newRoot)
	}
}

// Connect starts, initializes, and discovers the tools of a local MCP server.
func (m *Manager) Connect(ctx context.Context, config ServerConfig) (ServerStatus, error) {
	if err := validate(config); err != nil {
		return ServerStatus{}, err
	}
	m.mu.RLock()
	_, exists := m.servers[config.Name]
	root := m.root
	m.mu.RUnlock()
	if exists {
		return ServerStatus{}, fmt.Errorf("MCP %q ya esta conectado", config.Name)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "atenea", Version: "dev"}, nil)
	client.AddRoots(&mcp.Root{URI: rootURI(root), Name: filepath.Base(root)})
	command := exec.Command(config.Command, config.Args...)
	command.Dir = config.Cwd
	if command.Dir == "" {
		command.Dir = root
	}
	if len(config.Env) > 0 {
		command.Env = mergeEnv(os.Environ(), config.Env)
	}
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	session, err := client.Connect(connectCtx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return ServerStatus{}, fmt.Errorf("conectar MCP %q: %w", config.Name, err)
	}
	srv := &server{config: config, client: client, session: session}
	definitions, err := listTools(connectCtx, session)
	if err != nil {
		_ = session.Close()
		return ServerStatus{}, fmt.Errorf("descubrir tools de MCP %q: %w", config.Name, err)
	}
	names := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		adapter, err := newTool(config.Name, session, definition)
		if err != nil {
			_ = session.Close()
			return ServerStatus{}, fmt.Errorf("tool %q de MCP %q: %w", definition.Name, config.Name, err)
		}
		if _, duplicate := names[adapter.Name()]; duplicate {
			_ = session.Close()
			return ServerStatus{}, fmt.Errorf("dos tools de MCP %q se convierten en %q", config.Name, adapter.Name())
		}
		names[adapter.Name()] = struct{}{}
		srv.tools = append(srv.tools, adapter)
	}
	m.mu.Lock()
	if _, exists := m.servers[config.Name]; exists {
		m.mu.Unlock()
		_ = session.Close()
		return ServerStatus{}, fmt.Errorf("MCP %q ya esta conectado", config.Name)
	}
	m.servers[config.Name] = srv
	m.mu.Unlock()
	go m.removeWhenSessionEnds(config.Name, srv)
	return ServerStatus{ServerConfig: config, Connected: true, Tools: len(srv.tools)}, nil
}

func (m *Manager) removeWhenSessionEnds(name string, ended *server) {
	_ = ended.session.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.servers[name] == ended {
		delete(m.servers, name)
	}
}

// Disconnect closes the MCP session and its subprocess. It is idempotent.
func (m *Manager) Disconnect(name string) error {
	m.mu.Lock()
	srv, ok := m.servers[name]
	if ok {
		delete(m.servers, name)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	if err := srv.session.Close(); err != nil {
		return fmt.Errorf("desconectar MCP %q: %w", name, err)
	}
	return nil
}

// Close disconnects every server. It is used during application shutdown.
func (m *Manager) Close() {
	m.mu.Lock()
	servers := m.servers
	m.servers = make(map[string]*server)
	m.mu.Unlock()
	for _, srv := range servers {
		_ = srv.session.Close()
	}
}

func (m *Manager) Status() []ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	statuses := make([]ServerStatus, 0, len(m.servers))
	for _, srv := range m.servers {
		statuses = append(statuses, ServerStatus{ServerConfig: srv.config, Connected: true, Tools: len(srv.tools)})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses
}

// Tools returns the tools exposed by every connected MCP server.
func (m *Manager) Tools() []tool.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var tools []tool.Tool
	for _, srv := range m.servers {
		tools = append(tools, srv.tools...)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name() < tools[j].Name() })
	return tools
}

func validate(config ServerConfig) error {
	if !serverName.MatchString(config.Name) {
		return fmt.Errorf("nombre MCP invalido %q: usa hasta 48 letras, numeros, _ o -", config.Name)
	}
	if strings.TrimSpace(config.Command) == "" {
		return fmt.Errorf("el comando MCP no puede estar vacio")
	}
	return nil
}

func rootURI(root string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(root)}).String()
}

func mergeEnv(base []string, override map[string]string) []string {
	values := make(map[string]string, len(base)+len(override))
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range override {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	merged := make([]string, 0, len(keys))
	for _, key := range keys {
		merged = append(merged, key+"="+values[key])
	}
	return merged
}

type mcpTool struct {
	name        string
	description string
	schema      json.RawMessage
	remoteName  string
	session     *mcp.ClientSession
}

func newTool(serverName string, session *mcp.ClientSession, definition *mcp.Tool) (*mcpTool, error) {
	schema, err := json.Marshal(definition.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("serializar input schema: %w", err)
	}
	var object map[string]any
	if err := json.Unmarshal(schema, &object); err != nil {
		return nil, fmt.Errorf("input schema debe ser un objeto JSON: %w", err)
	}
	if object == nil {
		return nil, fmt.Errorf("input schema debe ser un objeto JSON")
	}
	return &mcpTool{
		name:        toolName(serverName, definition.Name),
		description: definition.Description,
		schema:      schema,
		remoteName:  definition.Name,
		session:     session,
	}, nil
}

func listTools(ctx context.Context, session *mcp.ClientSession) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	seen := make(map[string]struct{})
	for cursor := ""; ; {
		result, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		tools = append(tools, result.Tools...)
		if result.NextCursor == "" {
			return tools, nil
		}
		if _, repeated := seen[result.NextCursor]; repeated {
			return nil, fmt.Errorf("cursor repetido %q", result.NextCursor)
		}
		seen[result.NextCursor] = struct{}{}
		cursor = result.NextCursor
	}
}

func (t *mcpTool) Name() string            { return t.name }
func (t *mcpTool) Description() string     { return t.description }
func (t *mcpTool) Schema() json.RawMessage { return t.schema }

func (t *mcpTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	var arguments map[string]any
	if err := json.Unmarshal(input, &arguments); err != nil {
		return tool.Result{}, fmt.Errorf("input MCP invalido: %w", err)
	}
	result, err := t.session.CallTool(ctx, &mcp.CallToolParams{Name: t.remoteName, Arguments: arguments})
	if err != nil {
		return tool.Result{}, fmt.Errorf("MCP %s: %w", t.remoteName, err)
	}
	return tool.Result{Output: formatResult(result)}, nil
}

func formatResult(result *mcp.CallToolResult) string {
	parts := make([]string, 0, len(result.Content)+1)
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
			continue
		}
		if raw, err := json.Marshal(content); err == nil {
			parts = append(parts, string(raw))
		}
	}
	if len(parts) == 0 && result.StructuredContent != nil {
		if raw, err := json.Marshal(result.StructuredContent); err == nil {
			parts = append(parts, string(raw))
		}
	}
	output := strings.Join(parts, "\n")
	if result.IsError {
		return "MCP tool error: " + output
	}
	return output
}

func toolName(server, remote string) string {
	name := "mcp_" + normalize(server) + "_" + normalize(remote)
	if len(name) <= 128 {
		return name
	}
	sum := sha256.Sum256([]byte(server + "\x00" + remote))
	return fmt.Sprintf("mcp_%s_%x", normalize(server), sum[:8])
}

func normalize(value string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			return r
		}
		return '_'
	}, value)
}
