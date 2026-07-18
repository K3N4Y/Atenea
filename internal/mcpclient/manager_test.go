package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"atenea/internal/tool"

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

func TestManager_RejectsInvalidServerConfig(t *testing.T) {
	manager := NewManager(t.TempDir())
	if _, err := manager.Connect(context.Background(), ServerConfig{Name: "bad name", Command: "echo"}); err == nil {
		t.Fatal("Connect succeeded with invalid server name")
	}
	if _, err := manager.Connect(context.Background(), ServerConfig{Name: "valid"}); err == nil {
		t.Fatal("Connect succeeded without command")
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
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "Returns pong.",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

var _ tool.Tool = (*mcpTool)(nil)
