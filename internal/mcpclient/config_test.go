package mcpclient

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// isolateGlobalConfig apunta el config global a un directorio vacio del test,
// para que un ~/.config/atenea/mcp.json de la maquina no contamine el resultado.
func isolateGlobalConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestLoadConfig_ParsesServersSortedByName(t *testing.T) {
	isolateGlobalConfig(t)
	root := t.TempDir()
	writeConfig(t, root, `{
		"mcpServers": {
			"zeta":  {"command": "npx", "args": ["zeta-mcp"], "env": {"TOKEN": "x"}},
			"alpha": {"command": "uvx", "args": ["alpha-mcp"], "cwd": "/tmp"}
		}
	}`)

	configs, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(configs))
	}
	if configs[0].Name != "alpha" || configs[1].Name != "zeta" {
		t.Fatalf("expected servers sorted by name, got %q, %q", configs[0].Name, configs[1].Name)
	}
	if configs[0].Command != "uvx" || configs[0].Cwd != "/tmp" {
		t.Fatalf("alpha config not preserved: %+v", configs[0])
	}
	if configs[1].Env["TOKEN"] != "x" {
		t.Fatalf("zeta env not preserved: %+v", configs[1])
	}
}

func TestLoadConfig_MissingFileReturnsNoServers(t *testing.T) {
	isolateGlobalConfig(t)
	configs, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatalf("missing file must not be an error, got %v", err)
	}
	if len(configs) != 0 {
		t.Fatalf("expected no servers, got %+v", configs)
	}
}

func TestLoadConfig_MergesGlobalWithWorkspacePrecedence(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skipf("XDG_CONFIG_HOME is not the UserConfigDir override on %s", runtime.GOOS)
	}
	configHome := isolateGlobalConfig(t)
	globalDir := filepath.Join(configHome, "atenea")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	globalConfig := `{"mcpServers": {
		"global-only": {"command": "uvx", "args": ["global-mcp"]},
		"shared":      {"command": "global-command"}
	}}`
	if err := os.WriteFile(filepath.Join(globalDir, "mcp.json"), []byte(globalConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeConfig(t, root, `{"mcpServers": {
		"local-only": {"command": "npx", "args": ["local-mcp"]},
		"shared":     {"command": "workspace-command"}
	}}`)

	configs, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(configs) != 3 {
		t.Fatalf("expected 3 merged servers, got %d: %+v", len(configs), configs)
	}
	if configs[0].Name != "global-only" || configs[1].Name != "local-only" || configs[2].Name != "shared" {
		t.Fatalf("expected merged servers sorted by name, got %+v", configs)
	}
	if configs[2].Command != "workspace-command" {
		t.Fatalf("workspace must override the global server with the same name, got %+v", configs[2])
	}
}

func TestLoadConfig_RejectsInvalidJSON(t *testing.T) {
	isolateGlobalConfig(t)
	root := t.TempDir()
	writeConfig(t, root, `{not json`)
	if _, err := LoadConfig(root); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestLoadConfig_RejectsInvalidServerName(t *testing.T) {
	isolateGlobalConfig(t)
	root := t.TempDir()
	writeConfig(t, root, `{"mcpServers": {"bad name!": {"command": "npx"}}}`)
	if _, err := LoadConfig(root); err == nil {
		t.Fatal("expected a validation error for the server name")
	}
}

func TestLoadConfig_RejectsEmptyCommand(t *testing.T) {
	isolateGlobalConfig(t)
	root := t.TempDir()
	writeConfig(t, root, `{"mcpServers": {"ok": {"command": "  "}}}`)
	if _, err := LoadConfig(root); err == nil {
		t.Fatal("expected a validation error for the empty command")
	}
}

func TestMerge_OverlaysConnectedStatusAndKeepsUnconfiguredServers(t *testing.T) {
	configs := []ServerConfig{
		{Name: "alpha", Command: "npx"},
		{Name: "beta", Command: "uvx"},
	}
	connected := []ServerStatus{
		{ServerConfig: ServerConfig{Name: "beta", Command: "uvx"}, Connected: true, Tools: 4},
		{ServerConfig: ServerConfig{Name: "ghost", Command: "old"}, Connected: true, Tools: 1},
	}

	merged := Merge(configs, connected)
	if len(merged) != 3 {
		t.Fatalf("expected 3 servers, got %d: %+v", len(merged), merged)
	}
	if merged[0].Name != "alpha" || merged[0].Connected {
		t.Fatalf("alpha must be listed disconnected: %+v", merged[0])
	}
	if merged[1].Name != "beta" || !merged[1].Connected || merged[1].Tools != 4 {
		t.Fatalf("beta must carry its live status: %+v", merged[1])
	}
	if merged[2].Name != "ghost" || !merged[2].Connected {
		t.Fatalf("a connected server missing from the config must remain toggleable: %+v", merged[2])
	}
}

func skipWithoutXDGOverride(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skipf("XDG_CONFIG_HOME is not the UserConfigDir override on %s", runtime.GOOS)
	}
}

func TestUpsertGlobalConfig_CreatesUpdatesAndRoundTrips(t *testing.T) {
	skipWithoutXDGOverride(t)
	isolateGlobalConfig(t)

	if err := UpsertGlobalConfig(ServerConfig{Name: "github", Command: "npx", Args: []string{"github-mcp"}}); err != nil {
		t.Fatalf("UpsertGlobalConfig: %v", err)
	}
	if err := UpsertGlobalConfig(ServerConfig{Name: "github", Command: "uvx", Env: map[string]string{"TOKEN": "x"}}); err != nil {
		t.Fatalf("UpsertGlobalConfig (replace): %v", err)
	}

	configs, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(configs) != 1 || configs[0].Name != "github" || configs[0].Command != "uvx" || configs[0].Env["TOKEN"] != "x" {
		t.Fatalf("upsert must replace the existing entry, got %+v", configs)
	}

	// The map key is the name: the entry must not duplicate it inside.
	raw, err := os.ReadFile(GlobalConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"name"`) {
		t.Fatalf("the stored entry must not duplicate the name field:\n%s", raw)
	}
	info, err := os.Stat(GlobalConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config file permissions = %v, want 0600 (env can carry tokens)", info.Mode().Perm())
	}
}

func TestUpsertGlobalConfig_RejectsInvalidServer(t *testing.T) {
	skipWithoutXDGOverride(t)
	isolateGlobalConfig(t)
	if err := UpsertGlobalConfig(ServerConfig{Name: "bad name!", Command: "npx"}); err == nil {
		t.Fatal("expected a validation error")
	}
	if _, err := os.Stat(GlobalConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("an invalid upsert must not create the file, stat err = %v", err)
	}
}

func TestRemoveGlobalConfig_RemovesAndReportsMissing(t *testing.T) {
	skipWithoutXDGOverride(t)
	isolateGlobalConfig(t)
	if err := UpsertGlobalConfig(ServerConfig{Name: "github", Command: "npx"}); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveGlobalConfig("github")
	if err != nil || !removed {
		t.Fatalf("RemoveGlobalConfig = (%v, %v), want (true, nil)", removed, err)
	}
	configs, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 0 {
		t.Fatalf("expected no servers after removal, got %+v", configs)
	}

	removed, err = RemoveGlobalConfig("github")
	if err != nil || removed {
		t.Fatalf("removing an absent server = (%v, %v), want (false, nil)", removed, err)
	}
}
