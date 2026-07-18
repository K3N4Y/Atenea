package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"atenea/internal/llm"
	"atenea/internal/mcpclient"
)

// TestApp_MCPConfigLifecycle covers the Wails surface over the shared MCP
// config: saving from the UI lands in the global file, ListMCPs merges it
// with the workspace .mcp.json, and removal distinguishes global entries
// from workspace-declared ones.
func TestApp_MCPConfigLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skipf("XDG_CONFIG_HOME is not the UserConfigDir override on %s", runtime.GOOS)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	workspaceConfig := `{"mcpServers": {"local": {"command": "npx", "args": ["local-mcp"]}}}`
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(workspaceConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	app := newApp(llm.NewFakeProvider(), func(string, ...interface{}) {})
	if err := app.SetWorkspace(root); err != nil {
		t.Fatal(err)
	}

	if err := app.SaveMCPConfig(mcpclient.ServerConfig{Name: "github", Command: "npx", Args: []string{"github-mcp"}}); err != nil {
		t.Fatalf("SaveMCPConfig: %v", err)
	}
	servers, err := app.ListMCPs()
	if err != nil {
		t.Fatalf("ListMCPs: %v", err)
	}
	if len(servers) != 2 || servers[0].Name != "github" || servers[1].Name != "local" {
		t.Fatalf("servers = %+v, want github (global) and local (workspace)", servers)
	}
	if servers[0].Connected || servers[1].Connected {
		t.Fatalf("declared servers must list disconnected until connected explicitly: %+v", servers)
	}

	if err := app.RemoveMCPConfig("github"); err != nil {
		t.Fatalf("RemoveMCPConfig: %v", err)
	}
	servers, err = app.ListMCPs()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Name != "local" {
		t.Fatalf("servers after removal = %+v, want only the workspace one", servers)
	}

	err = app.RemoveMCPConfig("local")
	if err == nil || !strings.Contains(err.Error(), mcpclient.ConfigFile) {
		t.Fatalf("removing a workspace-declared server must point at %s, got %v", mcpclient.ConfigFile, err)
	}
}
