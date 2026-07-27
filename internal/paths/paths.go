// Package paths owns the product's user filesystem roots.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

const productDirectory = "atenea"

// compatibilityDirectories is ordered by ownership: Atenea's native layout,
// the agent-agnostic convention, then Claude Code compatibility. Discovery is
// first-wins, so this order is part of the interface and must remain identical
// for skills and subagents.
var compatibilityDirectories = [...]string{".atenea", ".agents", ".claude"}

const (
	databaseFile    = "atenea.db"
	checkpointsDir  = "checkpoints"
	credentialsFile = "credentials.json"
	providersFile   = "providers.json"
	modelsCacheFile = "models-cache.json"
	mcpConfigFile   = "mcp.json"
)

// ConfigDir returns the directory for user-specific configuration.
func ConfigDir() (string, error) {
	if root := os.Getenv("XDG_CONFIG_HOME"); root != "" {
		return filepath.Join(root, productDirectory), nil
	}

	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, productDirectory), nil
}

// DataDir returns the directory for durable user-specific data.
func DataDir() (string, error) {
	if root := os.Getenv("XDG_DATA_HOME"); root != "" {
		return filepath.Join(root, productDirectory), nil
	}

	root, err := userDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, productDirectory), nil
}

// CacheDir returns the directory for disposable user-specific data.
func CacheDir() (string, error) {
	if root := os.Getenv("XDG_CACHE_HOME"); root != "" {
		return filepath.Join(root, productDirectory), nil
	}

	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, productDirectory), nil
}

// DB returns the durable session database path.
func DB() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, databaseFile), nil
}

// Checkpoints returns the directory for durable workspace checkpoints.
func Checkpoints() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, checkpointsDir), nil
}

// Credentials returns the user credential store path.
func Credentials() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, credentialsFile), nil
}

// Providers returns the user provider configuration path.
func Providers() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, providersFile), nil
}

// ModelsCache returns the disposable model discovery cache path.
func ModelsCache() (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, modelsCacheFile), nil
}

// MCPConfig returns the global MCP server configuration path.
func MCPConfig() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, mcpConfigFile), nil
}

// SkillDirs returns the ordered directories used for skill discovery. Project
// definitions precede global definitions, and each base follows the shared
// .atenea, .agents, .claude compatibility order. Duplicate paths are removed.
func SkillDirs(root string) []string {
	return discoveryDirs(root, "skills")
}

// AgentDirs returns the ordered directories used for subagent discovery. It has
// exactly the same project, home, compatibility, and deduplication semantics as
// SkillDirs.
func AgentDirs(root string) []string {
	return discoveryDirs(root, "agents")
}

// BuiltinSkillDir returns the global Atenea-owned skill directory. Built-in
// skills are materialized there and then found through normal discovery.
func BuiltinSkillDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, compatibilityDirectories[0], "skills"), nil
}

func discoveryDirs(root, kind string) []string {
	bases := []string{root}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		bases = append(bases, home)
	}

	dirs := make([]string, 0, len(bases)*len(compatibilityDirectories))
	seen := make(map[string]struct{}, cap(dirs))
	for _, base := range bases {
		for _, compatibilityDir := range compatibilityDirectories {
			dir := filepath.Join(base, compatibilityDir, kind)
			if _, exists := seen[dir]; exists {
				continue
			}
			seen[dir] = struct{}{}
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func userDataDir() (string, error) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share"), nil
	}
	return os.UserConfigDir()
}
