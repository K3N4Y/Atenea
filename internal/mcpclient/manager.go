// Package mcpclient connects Atenea to MCP servers over local and remote transports.
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

	"github.com/K3N4Y/atenea/agentcore/permission"
	"github.com/K3N4Y/atenea/internal/paths"
	"github.com/K3N4Y/atenea/internal/tool"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const connectTimeout = 30 * time.Second

var serverName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,48}$`)

// ServerConfig describes one MCP transport. An empty Type is legacy stdio;
// remote transports use URL, while stdio uses Command, Args, Env, and Cwd.
type ServerConfig struct {
	Name    string            `json:"name,omitempty"`
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	URL     string            `json:"url,omitempty"`
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
	mu       sync.RWMutex
	root     string
	identity paths.Identity
	servers  map[string]*server
}

func NewManager(root string) *Manager {
	return NewManagerWithIdentity(root, paths.Identity{})
}

// NewManagerWithIdentity constructs a manager that advertises identity during
// MCP initialization. The identity is copied, so it cannot change underneath
// concurrent connections.
func NewManagerWithIdentity(root string, identity paths.Identity) *Manager {
	return &Manager{root: root, identity: identity.OrDevelopment(), servers: make(map[string]*server)}
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
		return ServerStatus{}, fmt.Errorf("MCP %q is already connected", config.Name)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: m.identity.Product, Version: m.identity.Version}, nil)
	client.AddRoots(&mcp.Root{URI: rootURI(root), Name: filepath.Base(root)})
	transport, err := transportFor(config, root)
	if err != nil {
		return ServerStatus{}, err
	}
	connectCtx := ctx
	cancel := func() {}
	if transportType(config) == "stdio" {
		connectCtx, cancel = context.WithTimeout(ctx, connectTimeout)
	}
	defer cancel()
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return ServerStatus{}, fmt.Errorf("connect MCP %q: %w", config.Name, err)
	}
	srv := &server{config: config, client: client, session: session}
	definitions, err := listTools(connectCtx, session)
	if err != nil {
		_ = session.Close()
		return ServerStatus{}, fmt.Errorf("discover the tools of MCP %q: %w", config.Name, err)
	}
	names := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		adapter, err := newTool(config.Name, session, definition)
		if err != nil {
			_ = session.Close()
			return ServerStatus{}, fmt.Errorf("tool %q of MCP %q: %w", definition.Name, config.Name, err)
		}
		if _, duplicate := names[adapter.Name()]; duplicate {
			_ = session.Close()
			return ServerStatus{}, fmt.Errorf("two tools of MCP %q both become %q", config.Name, adapter.Name())
		}
		names[adapter.Name()] = struct{}{}
		srv.tools = append(srv.tools, adapter)
	}
	m.mu.Lock()
	if _, exists := m.servers[config.Name]; exists {
		m.mu.Unlock()
		_ = session.Close()
		return ServerStatus{}, fmt.Errorf("MCP %q is already connected", config.Name)
	}
	m.servers[config.Name] = srv
	m.mu.Unlock()
	// The legacy SDK SSE connection reports EOF from Wait while its split GET/POST
	// session is still usable. Monitoring it would tear down a healthy session.
	// Streamable HTTP and stdio have a single connection whose Wait is reliable.
	if transportType(config) != "sse" {
		go m.removeWhenSessionEnds(config.Name, srv)
	}
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
		return fmt.Errorf("disconnect MCP %q: %w", name, err)
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
		return fmt.Errorf("invalid MCP name %q: use up to 48 letters, digits, _ or -", config.Name)
	}
	switch transportType(config) {
	case "stdio":
		if strings.TrimSpace(config.Command) == "" {
			return fmt.Errorf("a stdio MCP server needs a command to run")
		}
		if strings.TrimSpace(config.URL) != "" {
			return fmt.Errorf("a stdio MCP server cannot declare a URL")
		}
	case "http", "streamable-http", "sse":
		if strings.TrimSpace(config.URL) == "" {
			return fmt.Errorf("an %s MCP server needs a URL", transportType(config))
		}
		parsed, err := url.ParseRequestURI(config.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("invalid MCP URL %q: use an absolute http or https URL", config.URL)
		}
		if strings.TrimSpace(config.Command) != "" || len(config.Args) != 0 || len(config.Env) != 0 || strings.TrimSpace(config.Cwd) != "" {
			return fmt.Errorf("an %s MCP server cannot declare stdio process settings", transportType(config))
		}
	default:
		return fmt.Errorf("unsupported MCP transport type %q: use stdio, http, streamable-http, or sse", config.Type)
	}
	return nil
}

func transportType(config ServerConfig) string {
	if config.Type == "" {
		return "stdio"
	}
	return strings.ToLower(strings.TrimSpace(config.Type))
}

func transportFor(config ServerConfig, root string) (mcp.Transport, error) {
	switch transportType(config) {
	case "stdio":
		command := exec.Command(config.Command, config.Args...)
		command.Dir = config.Cwd
		if command.Dir == "" {
			command.Dir = root
		}
		command.Env = mergeEnv(baseEnv(), config.Env)
		return &mcp.CommandTransport{Command: command}, nil
	case "http", "streamable-http":
		return &mcp.StreamableClientTransport{Endpoint: config.URL}, nil
	case "sse":
		return &mcp.SSEClientTransport{Endpoint: config.URL}, nil
	default:
		return nil, fmt.Errorf("unsupported MCP transport type %q", config.Type)
	}
}

func rootURI(root string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(root)}).String()
}

func baseEnv() []string {
	keys := []string{"PATH", "HOME", "USERPROFILE", "TMPDIR", "TMP", "TEMP", "LANG", "LANGUAGE", "TZ", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"}
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "LC_") {
			env = append(env, entry)
		}
	}
	return env
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
		return nil, fmt.Errorf("serialize the input schema: %w", err)
	}
	var object map[string]any
	if err := json.Unmarshal(schema, &object); err != nil {
		return nil, fmt.Errorf("the input schema must be a JSON object: %w", err)
	}
	if object == nil {
		return nil, fmt.Errorf("the input schema must be a JSON object")
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
			return nil, fmt.Errorf("repeated cursor %q", result.NextCursor)
		}
		seen[result.NextCursor] = struct{}{}
		cursor = result.NextCursor
	}
}

func (t *mcpTool) Name() string            { return t.name }
func (t *mcpTool) Description() string     { return t.description }
func (t *mcpTool) Schema() json.RawMessage { return t.schema }

// mcpTool deliberately does NOT implement tool.Declaring. MCP carries no
// statement of what a tool affects, and this side of the wire cannot find out:
// the same protocol serves a docs lookup and a production deploy. Silence is the
// honest answer, and it is also the safe one — an undeclared tool is asked about
// (see permission.EffectsPolicy), so a server the user just connected does not
// get unattended execution. If MCP or .mcp.json grows a way to declare effects,
// this is the method that reads it.

// GrantRule grants the tool as a whole, which is exactly what the panel showed:
// this tool, from this server. Narrowing it would mean interpreting the
// arguments, and their meaning lives in the server rather than here — one
// server's "path" is the next one's database key. Without a grant an MCP tool
// would have to be approved call by call forever, with no way to say "this one is
// fine".
func (t *mcpTool) GrantRule(tool.Call) (permission.Rule, bool) {
	return permission.Rule{Tool: t.name}, true
}

func (t *mcpTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	var arguments map[string]any
	if err := json.Unmarshal(input, &arguments); err != nil {
		return tool.Result{}, fmt.Errorf("invalid MCP input: %w", err)
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
