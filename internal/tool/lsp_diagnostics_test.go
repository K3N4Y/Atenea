package tool

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

func TestLSPDiagnosticsMiddleware_AppendsDiagnosticsAfterWrite(t *testing.T) {
	root := t.TempDir()
	lsp := NewLSPTool(root)
	lsp.commandFor = func(string) ([]string, error) {
		return []string{os.Args[0], "-test.run=TestLSPServerProcess", "--", "--lsp-test-server", "--with-diagnostic"}, nil
	}
	t.Cleanup(func() {
		lsp.mu.Lock()
		defer lsp.mu.Unlock()
		for _, client := range lsp.servers {
			client.close()
		}
	})

	registry := NewRegistry(NewOutputStore(0), NewWriteTool(root, hashline.NewMemSnapshotStore()))
	registry.Use(LSPDiagnosticsMiddleware(lsp))
	result, err := registry.Materialize(Permissions{"write": true}).Settle(context.Background(), Call{
		ID: "c1", Name: "write", Input: json.RawMessage(`{"path":"broken.go","content":"package broken"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "LSP diagnostics:\nbroken.go:1:1: error: broken declaration") {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestLSPDiagnosticsMiddleware_IgnoresUnavailableServer(t *testing.T) {
	root := t.TempDir()
	lsp := NewLSPTool(root)
	lsp.commandFor = func(string) ([]string, error) { return []string{"definitely-not-an-lsp-binary"}, nil }
	registry := NewRegistry(NewOutputStore(0), NewWriteTool(root, hashline.NewMemSnapshotStore()))
	registry.Use(LSPDiagnosticsMiddleware(lsp))

	result, err := registry.Materialize(Permissions{"write": true}).Settle(context.Background(), Call{
		ID: "c1", Name: "write", Input: json.RawMessage(`{"path":"ok.go","content":"package ok"}`),
	})
	if err != nil {
		t.Fatalf("committed write became a failure: %v", err)
	}
	if strings.Contains(result.Output, "LSP diagnostics") {
		t.Fatalf("unexpected diagnostics: %q", result.Output)
	}
}
