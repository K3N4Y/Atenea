package mcpclient

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ConfigFile is the workspace file that declares MCP servers for the TUI,
// using the de-facto standard format shared by other agent CLIs:
//
//	{"mcpServers": {"<name>": {"command": "npx", "args": ["..."], "env": {...}, "cwd": "..."}}}
const ConfigFile = ".mcp.json"

type configFile struct {
	Servers map[string]ServerConfig `json:"mcpServers"`
}

// GlobalConfigPath is the user-level MCP config, next to atenea's other
// settings (~/.config/atenea/mcp.json on Linux). Empty when the user config
// directory cannot be resolved: only workspace servers remain.
func GlobalConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "atenea", "mcp.json")
}

// Scope is the file a declaration came from.
type Scope string

const (
	// ScopeGlobal is the user-level config, available in every workspace.
	ScopeGlobal Scope = "global"
	// ScopeWorkspace is the project's own .mcp.json.
	ScopeWorkspace Scope = "workspace"
)

// Declaration is one declared server and where it was declared.
//
// A shadowed declaration is one another declaration of the same name beat. Only
// a global one can be shadowed, since the workspace wins, and it stays in the
// list rather than being dropped because "why is this server running a different
// command than the one I configured" is answered by seeing both.
type Declaration struct {
	ServerConfig
	Scope Scope
	// Path is the file that declares it, which is the file to edit.
	Path     string
	Shadowed bool
}

// Declarations returns every declared server, sorted by name and, within a name,
// the one in force first: the global config merged with <root>/.mcp.json, where a
// workspace server overrides a global one with the same name (the same
// project-over-global precedence as skills). Missing files are not an error.
// Declared names must satisfy the same validation Connect enforces, so a bad
// entry surfaces here (naming the file and server) instead of failing later on
// connect.
//
// This is the one reader of the two files. LoadConfig is this list with the
// shadowed entries dropped, so what a host connects and what `atenea mcp list`
// prints cannot disagree.
func Declarations(root string) ([]Declaration, error) {
	globalPath := GlobalConfigPath()
	global, err := loadConfigFile(globalPath)
	if err != nil {
		return nil, err
	}
	workspacePath := filepath.Join(root, ConfigFile)
	workspace, err := loadConfigFile(workspacePath)
	if err != nil {
		return nil, err
	}
	declarations := make([]Declaration, 0, len(global)+len(workspace))
	overridden := make(map[string]bool, len(workspace))
	for _, config := range workspace {
		overridden[config.Name] = true
		declarations = append(declarations, Declaration{ServerConfig: config, Scope: ScopeWorkspace, Path: workspacePath})
	}
	for _, config := range global {
		declarations = append(declarations, Declaration{
			ServerConfig: config,
			Scope:        ScopeGlobal,
			Path:         globalPath,
			Shadowed:     overridden[config.Name],
		})
	}
	sort.Slice(declarations, func(i, j int) bool {
		if declarations[i].Name != declarations[j].Name {
			return declarations[i].Name < declarations[j].Name
		}
		return !declarations[i].Shadowed && declarations[j].Shadowed
	})
	return declarations, nil
}

// LoadConfig returns the servers in force, sorted by name: every declaration
// except the ones a same-named declaration overrides.
func LoadConfig(root string) ([]ServerConfig, error) {
	declarations, err := Declarations(root)
	if err != nil {
		return nil, err
	}
	configs := make([]ServerConfig, 0, len(declarations))
	for _, declaration := range declarations {
		if declaration.Shadowed {
			continue
		}
		configs = append(configs, declaration.ServerConfig)
	}
	return configs, nil
}

func loadConfigFile(path string) ([]ServerConfig, error) {
	servers, err := readConfigMap(path)
	if err != nil {
		return nil, err
	}
	configs := make([]ServerConfig, 0, len(servers))
	for name, config := range servers {
		config.Name = name
		if err := validate(config); err != nil {
			return nil, fmt.Errorf("%s: server %q: %w", path, name, err)
		}
		configs = append(configs, config)
	}
	return configs, nil
}

// UpsertGlobalConfig adds or replaces a server in the global MCP config file,
// creating the file (and its directory) on first use. It is how UIs persist a
// server so every atenea surface (desktop app and TUI) shares it.
func UpsertGlobalConfig(config ServerConfig) error {
	if err := validate(config); err != nil {
		return err
	}
	path := GlobalConfigPath()
	if path == "" {
		return fmt.Errorf("cannot resolve the user config directory")
	}
	servers, err := readConfigMap(path)
	if err != nil {
		return err
	}
	name := config.Name
	// The map key is the name; keeping it inside the entry would duplicate it.
	config.Name = ""
	servers[name] = config
	return writeConfigMap(path, servers)
}

// RemoveGlobalConfig deletes a server from the global MCP config file, reading
// root's .mcp.json only to explain a removal it cannot perform: a server the
// workspace declares is not the global config's to delete, and the error names
// the file to edit instead.
//
// It reports whether the server was removed. false with no error means nothing
// anywhere declares that name, which each host reads its own way — the desktop
// panel is refreshing a list that may be stale, while a person typing
// `atenea mcp remove` mistyped a name.
func RemoveGlobalConfig(root, name string) (bool, error) {
	path := GlobalConfigPath()
	if path == "" {
		return false, fmt.Errorf("cannot resolve the user config directory")
	}
	servers, err := readConfigMap(path)
	if err != nil {
		return false, err
	}
	if _, ok := servers[name]; !ok {
		return false, workspaceDeclaration(root, name)
	}
	delete(servers, name)
	return true, writeConfigMap(path, servers)
}

// workspaceDeclaration is the error for a name the global config does not
// declare and the workspace does, and nil when the workspace does not declare it
// either.
func workspaceDeclaration(root, name string) error {
	declarations, err := Declarations(root)
	if err != nil {
		return err
	}
	for _, declaration := range declarations {
		if declaration.Name == name && declaration.Scope == ScopeWorkspace {
			return fmt.Errorf("MCP %q is declared in %s; edit that file to remove it", name, declaration.Path)
		}
	}
	return nil
}

func readConfigMap(path string) (map[string]ServerConfig, error) {
	if path == "" {
		return map[string]ServerConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]ServerConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var file configFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if file.Servers == nil {
		return map[string]ServerConfig{}, nil
	}
	return file.Servers, nil
}

// writeConfigMap persists the servers atomically (temp file + rename, same
// move as providerconfig.Save) with 0600 permissions: env entries can carry
// tokens, so the file stays private to the user.
func writeConfigMap(path string, servers map[string]ServerConfig) error {
	data, err := json.MarshalIndent(configFile{Servers: servers}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcp-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// Merge joins the declared configs with the manager's connected statuses into
// one list for the UI: every declared server appears (disconnected by default,
// overlaid with its live status when connected), plus any connected server no
// longer present in the config, so it can still be toggled off. Sorted by name.
func Merge(configs []ServerConfig, connected []ServerStatus) []ServerStatus {
	byName := make(map[string]ServerStatus, len(connected))
	for _, status := range connected {
		byName[status.Name] = status
	}
	merged := make([]ServerStatus, 0, len(configs)+len(connected))
	for _, config := range configs {
		if status, ok := byName[config.Name]; ok {
			delete(byName, config.Name)
			merged = append(merged, status)
			continue
		}
		merged = append(merged, ServerStatus{ServerConfig: config})
	}
	for _, status := range byName {
		merged = append(merged, status)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name })
	return merged
}
