package mcpclient

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/K3N4Y/atenea/agentcore/tool/tooltest"
)

// TestMCPTool_Contract is the kit's most honest run: an MCP tool is written by
// somebody else, discovered at runtime and dropped into the same registry as the
// builtins, which is exactly the position a third-party tool will be in. If the
// contract holds here, over a real stdio session with a real server process, it
// holds for the tools atenea has never seen.
//
// One server for the whole run: the checks are read-only against a stateless
// echo, and reconnecting per check would only measure process spawning.
func TestMCPTool_Contract(t *testing.T) {
	manager := NewManager(t.TempDir())
	t.Cleanup(manager.Close)

	if _, err := manager.Connect(context.Background(), ServerConfig{
		Name:    "test-server",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     map[string]string{"ATENEA_MCP_HELPER": "1"},
	}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	tools := manager.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %#v, want the one discovered tool", tools)
	}

	tooltest.Contract(t, func(*testing.T) tooltest.Subject {
		return tooltest.Subject{Tool: tools[0], Input: json.RawMessage(`{}`)}
	})
}
