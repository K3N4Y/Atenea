package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// Provenance: oh-my-pi@5af71dc9 edit-mode and custom-wire lifecycle contracts.
func TestRegistryFreezesEditModeWithinTurnAndChangesNextTurn(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mode := editmode.ApplyPatch
	edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	edit.Config = func() (editmode.Config, error) { return editmode.Config{Setting: string(mode)}, nil }
	edit.Getenv = func(string) string { return "" }
	registry := NewRegistry(NewOutputStore(0), edit)

	first := registry.Materialize(Permissions{"edit": true})
	if got := first.Definitions[0]; got.Name != "edit" || got.WireName != "apply_patch" || got.CustomFormat == nil {
		t.Fatalf("first definition = %+v", got)
	}
	mode = editmode.Replace
	patch, _ := json.Marshal(map[string]string{"input": "*** Begin Patch\n*** Update File: x.txt\n@@\n-old\n+new\n*** End Patch\n"})
	if _, err := first.Settle(context.Background(), Call{Name: "apply_patch", Input: patch}); err != nil {
		t.Fatalf("frozen settle: %v", err)
	}

	second := registry.Materialize(Permissions{"edit": true})
	if got := second.Definitions[0]; got.WireName != "edit" || got.CustomFormat != nil {
		t.Fatalf("second definition = %+v", got)
	}
	if _, err := second.Settle(context.Background(), Call{Name: "apply_patch", Input: patch}); err == nil {
		t.Fatal("stale wire alias remained in next turn")
	}
	replace := json.RawMessage(`{"path":"x.txt","old_string":"new","new_string":"done"}`)
	if _, err := second.Settle(context.Background(), Call{Name: "edit", Input: replace}); err != nil {
		t.Fatalf("next-turn replace: %v", err)
	}
}

type lifecycleTokens struct{}

func (lifecycleTokens) OAuthToken(context.Context) (llm.OAuthToken, error) {
	return llm.OAuthToken{AccessToken: "token", AccountID: "account"}, nil
}

func TestCodexCustomApplyPatchExecutesThroughRegistry(t *testing.T) {
	root := t.TempDir()
	edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	edit.Mode = editmode.ApplyPatch
	registry := NewRegistry(NewOutputStore(0), edit)
	materialized := registry.Materialize(Permissions{"edit": true})
	if materialized.Err != nil {
		t.Fatal(materialized.Err)
	}
	patch := "*** Begin Patch\n*** Add File: landed.txt\n+landed through codex\n*** End Patch\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) != 1 || request.Tools[0]["type"] != "custom" || request.Tools[0]["name"] != "apply_patch" {
			t.Errorf("tools = %#v", request.Tools)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"ct_1\",\"type\":\"custom_tool_call\",\"call_id\":\"call_1\",\"name\":\"apply_patch\",\"input\":\"\"}}\n\n")
		encoded, _ := json.Marshal(patch)
		fmt.Fprintf(w, "data: {\"type\":\"response.custom_tool_call_input.delta\",\"item_id\":\"ct_1\",\"delta\":%s}\n\n", encoded)
		fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ct_1\",\"type\":\"custom_tool_call\",\"call_id\":\"call_1\",\"name\":\"apply_patch\",\"input\":%s,\"status\":\"completed\"}}\n\n", encoded)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()
	provider := llm.NewCodexProvider(lifecycleTokens{}, server.URL, "gpt-5")
	stream, err := provider.Stream(context.Background(), llm.Request{Tools: materialized.Definitions})
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	for event := range stream {
		if event.Kind != llm.ToolCall {
			continue
		}
		result, err = materialized.Settle(context.Background(), Call{ID: event.CallID, Name: event.ToolName, Input: event.Input})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(root, "landed.txt"))
	if err != nil || string(got) != "landed through codex\n" || len(result.Files) != 1 {
		t.Fatalf("bytes=%q result=%+v err=%v", got, result, err)
	}
}

func TestEditMaterializationReturnsConfigurationErrors(t *testing.T) {
	edit := NewEditTool(t.TempDir(), hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	edit.Config = func() (editmode.Config, error) { return editmode.Config{Setting: "not-a-mode"}, nil }
	edit.Getenv = func(string) string { return "" }
	materialized := NewRegistry(NewOutputStore(0), edit).Materialize(Permissions{"edit": true})
	if materialized.Err == nil {
		t.Fatal("invalid edit.mode was silently replaced with a default")
	}
}

type aliasedTestTool struct{ name, alias string }

func (t aliasedTestTool) Name() string          { return t.name }
func (aliasedTestTool) Description() string     { return "test" }
func (aliasedTestTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (aliasedTestTool) Execute(context.Context, json.RawMessage) (Result, error) {
	return Result{}, nil
}
func (t aliasedTestTool) Definition() ToolDefinition {
	return ToolDefinition{Name: t.name, WireName: t.alias, Description: "test", Schema: t.Schema()}
}

func TestRegistryCatalogResolvesPermanentPresentationAlias(t *testing.T) {
	edit := NewEditTool(t.TempDir(), hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	edit.Mode = editmode.ApplyPatch
	registry := NewRegistry(NewOutputStore(0), edit)
	materialized := registry.Materialize(Permissions{"edit": true})
	if materialized.Err != nil {
		t.Fatal(materialized.Err)
	}
	aliased, ok := registry.Lookup("apply_patch")
	if !ok || aliased.Name() != "edit" {
		t.Fatalf("alias lookup=(%T, %v)", aliased, ok)
	}
	call := Call{Name: "apply_patch", Input: json.RawMessage(`{"input":"*** Begin Patch\n*** Update File: src/a.go\n@@\n-old\n+new\n*** End Patch"}`)}
	presentation, ok := PresentationFor(registry, call, Result{Diff: "diff"})
	if !ok || presentation.Label != "Edit" || presentation.Subject != "a.go" || !strings.Contains(presentation.Body, "*** Update File: src/a.go") || presentation.Kind != FileChange {
		t.Fatalf("presentation=%+v ok=%v", presentation, ok)
	}
}

func TestRegistryCatalogAliasIsStableAcrossModesAndHistoricalEvents(t *testing.T) {
	edit := NewEditTool(t.TempDir(), hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	edit.Mode = editmode.Hashline
	registry := NewRegistry(NewOutputStore(0), edit)
	call := Call{Name: "apply_patch", Input: json.RawMessage(`{"input":"*** Begin Patch\n*** Update File: src/old.go\n@@\n-old\n+new\n*** End Patch"}`)}
	if p, ok := PresentationFor(registry, call, Result{}); !ok || p.Label != "Edit" || p.Subject != "old.go" || strings.Contains(p.Body, `{"input"`) {
		t.Fatalf("historical presentation before materialization = %+v, %v", p, ok)
	}
	for _, mode := range []editmode.Mode{editmode.ApplyPatch, editmode.Hashline, editmode.Replace, editmode.ApplyPatch} {
		edit.Mode = mode
		if materialized := registry.Materialize(Permissions{"edit": true}); materialized.Err != nil {
			t.Fatal(materialized.Err)
		}
		if aliased, ok := registry.Lookup("apply_patch"); !ok || aliased.Name() != "edit" {
			t.Fatalf("stable alias after %s = (%T, %v)", mode, aliased, ok)
		}
		if p, ok := PresentationFor(registry, call, Result{}); !ok || p.Subject != "old.go" {
			t.Fatalf("presentation after %s = %+v, %v", mode, p, ok)
		}
	}
}

func TestRegistryConcurrentTurnsKeepExecutionLocalAndPresentationStable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	edit.TurnConfig = func(_, session string) (editmode.Config, error) {
		if session == "A" {
			return editmode.Config{Setting: string(editmode.ApplyPatch)}, nil
		}
		return editmode.Config{Setting: string(editmode.Replace)}, nil
	}
	registry := NewRegistry(NewOutputStore(0), edit)
	patch := json.RawMessage(`{"input":"*** Begin Patch\n*** Update File: x.txt\n@@\n-old\n+new\n*** End Patch\n"}`)
	const count = 20
	var wg sync.WaitGroup
	errCh := make(chan error, count*2)
	for i := 0; i < count; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			a := registry.MaterializeFor(Permissions{"edit": true}, "model", "A")
			if a.Err != nil {
				errCh <- a.Err
				return
			}
			if _, err := a.Settle(context.Background(), Call{Name: "apply_patch", Input: patch}); err != nil {
				var unknown *UnknownToolError
				if errors.As(err, &unknown) {
					errCh <- fmt.Errorf("A lost frozen alias: %w", err)
				}
			}
		}()
		go func() {
			defer wg.Done()
			b := registry.MaterializeFor(Permissions{"edit": true}, "model", "B")
			if b.Err != nil {
				errCh <- b.Err
				return
			}
			if _, err := b.Settle(context.Background(), Call{Name: "apply_patch", Input: patch}); err == nil {
				errCh <- errors.New("B incorrectly routed A's execution alias")
			}
			if p, ok := PresentationFor(registry, Call{Name: "apply_patch", Input: patch}, Result{}); !ok || p.Label != "Edit" || p.Subject != "x.txt" || strings.Contains(p.Body, `{"input"`) {
				errCh <- fmt.Errorf("unstable presentation: %+v, %v", p, ok)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestRegistryRejectsWireAliasCollisionDeterministically(t *testing.T) {
	registry := NewRegistry(NewOutputStore(0), aliasedTestTool{name: "a", alias: "z"}, aliasedTestTool{name: "z"})
	materialized := registry.Materialize(Permissions{"a": true, "z": true})
	if materialized.Err == nil || materialized.Err.Error() != `tool route "z" collides between "a" and "z"` {
		t.Fatalf("error = %v", materialized.Err)
	}
}
