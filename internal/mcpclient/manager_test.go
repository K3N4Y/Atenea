package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/paths"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/tool"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManager_ConnectsToStdioServerAndExecutesDiscoveredTool(t *testing.T) {
	manager := NewManager(t.TempDir())
	t.Cleanup(manager.Close)

	status, err := manager.Connect(context.Background(), ServerConfig{
		Name:    "test-server",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     map[string]string{"ATENEA_MCP_HELPER": "1"},
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !status.Connected || status.Tools != 1 {
		t.Fatalf("status = %+v, want connected server with one tool", status)
	}

	tools := manager.Tools()
	if len(tools) != 1 || tools[0].Name() != "mcp_test-server_echo" {
		t.Fatalf("tools = %#v, want discovered namespaced echo tool", tools)
	}
	result, err := tools[0].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "pong" {
		t.Fatalf("output = %q, want pong", result.Output)
	}

	if err := manager.Disconnect("test-server"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if statuses := manager.Status(); len(statuses) != 0 {
		t.Fatalf("Status after disconnect = %+v, want empty", statuses)
	}
}

func TestManager_PermissionsKeepLegacyAskAndScopePersistentRules(t *testing.T) {
	connect := func(t *testing.T, config ServerConfig) (*Manager, tool.Tool) {
		t.Helper()
		manager := NewManager(t.TempDir())
		t.Cleanup(manager.Close)
		config.Name = "server"
		config.Command = os.Args[0]
		config.Args = []string{"-test.run=TestMCPHelperProcess"}
		config.Env = map[string]string{"ATENEA_MCP_HELPER": "1"}
		if _, err := manager.Connect(context.Background(), config); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		return manager, manager.Tools()[0]
	}

	legacy, legacyTool := connect(t, ServerConfig{})
	if _, declared := tool.EffectsOf(legacyTool); declared {
		t.Fatal("legacy MCP config declared effects; it must remain ask-by-default")
	}
	if rules := legacy.PermissionRules(); len(rules) != 0 {
		t.Fatalf("legacy rules = %+v, want none", rules)
	}

	trusted, classified := connect(t, ServerConfig{Sensitivity: "reaches-network", AllowedTools: []string{"echo"}})
	if effects, declared := tool.EffectsOf(classified); !declared || effects != tool.ReachesNetwork {
		t.Fatalf("EffectsOf = (%v, %v), want reaches-network declaration", effects, declared)
	}
	if rules := trusted.PermissionRules(); len(rules) != 1 || rules[0] != (permission.Rule{Tool: "mcp_server_echo"}) {
		t.Fatalf("PermissionRules = %+v, want the namespaced tool only", rules)
	}
}

func TestManager_ConnectsToRemoteTransportsAndExecutesDiscoveredTool(t *testing.T) {
	tests := []struct {
		name      string
		typeName  string
		newServer func(*mcp.Server) http.Handler
	}{
		{name: "http alias", typeName: "http", newServer: func(server *mcp.Server) http.Handler {
			return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
		}},
		{name: "streamable HTTP", typeName: "streamable-http", newServer: func(server *mcp.Server) http.Handler {
			return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
		}},
		{name: "legacy SSE", typeName: "sse", newServer: func(server *mcp.Server) http.Handler {
			return mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := testMCPServer()
			httpServer := httptest.NewServer(test.newServer(remote))
			t.Cleanup(httpServer.Close)
			manager := NewManager(t.TempDir())
			t.Cleanup(manager.Close)

			status, err := manager.Connect(context.Background(), ServerConfig{
				Name: "remote", Type: test.typeName, URL: httpServer.URL,
			})
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if !status.Connected || status.Tools != 1 || status.Type != test.typeName || status.URL != httpServer.URL {
				t.Fatalf("status = %+v, want connected %s server with one tool", status, test.typeName)
			}
			result, err := manager.Tools()[0].Execute(context.Background(), json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if result.Output != "pong" {
				t.Fatalf("output = %q, want pong", result.Output)
			}
		})
	}
}

func TestManager_RejectsInvalidServerConfig(t *testing.T) {
	manager := NewManager(t.TempDir())
	if _, err := manager.Connect(context.Background(), ServerConfig{Name: "bad name", Command: "echo"}); err == nil {
		t.Fatal("Connect succeeded with invalid server name")
	}
	if _, err := manager.Connect(context.Background(), ServerConfig{Name: "valid"}); err == nil {
		t.Fatal("Connect succeeded without command")
	}
	invalid := []ServerConfig{
		{Name: "unknown", Type: "websocket", URL: "https://example.com/mcp"},
		{Name: "relative", Type: "http", URL: "/mcp"},
		{Name: "missing-url", Type: "sse"},
		{Name: "mixed", Type: "http", URL: "https://example.com/mcp", Command: "npx"},
		{Name: "stdio-url", Type: "stdio", Command: "npx", URL: "https://example.com/mcp"},
	}
	for _, config := range invalid {
		if _, err := manager.Connect(context.Background(), config); err == nil {
			t.Errorf("Connect succeeded with invalid config %+v", config)
		}
	}
}

func TestManager_AdvertisesInjectedIdentity(t *testing.T) {
	manager := NewManagerWithIdentity(t.TempDir(), paths.NewIdentity("v1.2.3"))
	t.Cleanup(manager.Close)

	_, err := manager.Connect(context.Background(), ServerConfig{
		Name: "identity", Command: os.Args[0],
		Args: []string{"-test.run=TestMCPHelperProcess"},
		Env:  map[string]string{"ATENEA_MCP_HELPER": "1", "ATENEA_MCP_HELPER_IDENTITY_PROBE": "1"},
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := manager.Tools()[0].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "atenea v1.2.3" {
		t.Fatalf("client identity = %q, want %q", result.Output, "atenea v1.2.3")
	}
}

func TestManager_DoesNotExposeParentSecretsToServer(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "parent-secret")
	manager := NewManager(t.TempDir())
	t.Cleanup(manager.Close)

	_, err := manager.Connect(context.Background(), ServerConfig{
		Name:    "env-probe",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env: map[string]string{
			"ATENEA_MCP_HELPER":           "1",
			"ATENEA_MCP_HELPER_ENV_PROBE": "1",
			"EXPLICIT_TOKEN":              "configured-secret",
		},
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	result, err := manager.Tools()[0].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "provider= explicit=configured-secret" {
		t.Fatalf("server environment = %q, want parent secret excluded and configured env preserved", result.Output)
	}
}

func TestManager_RemovesServerAfterUnexpectedSessionTermination(t *testing.T) {
	manager := NewManager(t.TempDir())
	t.Cleanup(manager.Close)

	_, err := manager.Connect(context.Background(), ServerConfig{
		Name:    "short-lived",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env: map[string]string{
			"ATENEA_MCP_HELPER":                 "1",
			"ATENEA_MCP_HELPER_EXIT_AFTER_CALL": "1",
		},
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	tools := manager.Tools()
	if len(tools) != 1 {
		t.Fatalf("Tools() = %#v, want one discovered tool", tools)
	}
	if _, err := tools[0].Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Deadline generoso: bajo -race el proceso helper y la deteccion del cierre
	// van mas lentos; el poll corta apenas el manager remueve el server.
	deadline := time.Now().Add(10 * time.Second)
	for len(manager.Status()) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if statuses := manager.Status(); len(statuses) != 0 {
		t.Fatalf("Status after unexpected termination = %+v, want empty", statuses)
	}
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("ATENEA_MCP_HELPER") != "1" {
		return
	}
	var clientIdentity string
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1.0.0"}, &mcp.ServerOptions{
		InitializedHandler: func(_ context.Context, request *mcp.InitializedRequest) {
			info := request.Session.InitializeParams().ClientInfo
			clientIdentity = info.Name + " " + info.Version
		},
	})
	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "Returns pong.",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if os.Getenv("ATENEA_MCP_HELPER_IDENTITY_PROBE") == "1" {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: clientIdentity}}}, nil
		}
		if os.Getenv("ATENEA_MCP_HELPER_ENV_PROBE") == "1" {
			output := fmt.Sprintf("provider=%s explicit=%s", os.Getenv("OPENAI_API_KEY"), os.Getenv("EXPLICIT_TOKEN"))
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: output}}}, nil
		}
		if os.Getenv("ATENEA_MCP_HELPER_EXIT_AFTER_CALL") == "1" {
			go func() {
				time.Sleep(100 * time.Millisecond)
				os.Exit(0)
			}()
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func testMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name: "echo", Description: "Returns pong.", InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
	})
	return server
}

var _ tool.Tool = (*mcpTool)(nil)
