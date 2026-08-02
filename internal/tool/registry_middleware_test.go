package tool

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/K3N4Y/atenea/agentcore/permission"
	contracttool "github.com/K3N4Y/atenea/agentcore/tool"
)

type askingPolicy struct{}

func (askingPolicy) Decide(string, contracttool.Call) permission.Decision { return permission.Ask }

type unusedGate struct{}

func (unusedGate) Ask(context.Context, permission.Request) (bool, error) {
	panic("missing requester must stop before consulting the gate")
}

func TestRegistry_MiddlewareRunsAfterRepairAndBeforeOutputCapping(t *testing.T) {
	var input json.RawMessage
	registry := NewRegistry(NewOutputStore(4), recorderTool{
		name: "lister", schema: listerSchema, got: &input, out: "tool",
	})
	var calls []string
	registry.Use(func(next SettleFunc) SettleFunc {
		return func(ctx context.Context, call Call) (Result, error) {
			calls = append(calls, string(call.Input))
			result, err := next(ctx, call)
			result.Output += " extension"
			return result, err
		}
	})

	result, err := registry.Materialize(Permissions{"lister": true}).Settle(
		context.Background(),
		Call{ID: "c1", Name: "lister", Input: json.RawMessage(`{"items":"[\"a\"]"}`)},
	)
	if err != nil {
		t.Fatalf("Settle returned an unexpected error: %v", err)
	}
	if want := []string{`{"items":["a"]}`}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("middleware inputs = %q, want repaired input %q", calls, want)
	}
	if !result.Truncated || len(result.Output) != 4 {
		t.Fatalf("result = %+v, want middleware output capped to four bytes", result)
	}
}

func TestRegistry_MaterializedMiddlewareSnapshotIsStable(t *testing.T) {
	var calls int
	registry := NewRegistry(NewOutputStore(0), spyTool{name: "echo", calls: &calls, out: "tool"})
	before := registry.Materialize(Permissions{"echo": true})
	registry.Use(func(next SettleFunc) SettleFunc {
		return func(ctx context.Context, call Call) (Result, error) {
			result, err := next(ctx, call)
			result.Output = "decorated"
			return result, err
		}
	})
	after := registry.Materialize(Permissions{"echo": true})

	first, err := before.Settle(context.Background(), Call{ID: "c1", Name: "echo"})
	if err != nil {
		t.Fatalf("settling old snapshot: %v", err)
	}
	second, err := after.Settle(context.Background(), Call{ID: "c2", Name: "echo"})
	if err != nil {
		t.Fatalf("settling new snapshot: %v", err)
	}
	if first.Output != "tool" || second.Output != "decorated" {
		t.Fatalf("snapshot outputs = %q, %q; want tool, decorated", first.Output, second.Output)
	}
}

func TestRegistry_RepairsBeforePermissionClassification(t *testing.T) {
	var classified json.RawMessage
	policy := policyFunc(func(_ string, call contracttool.Call) permission.Decision {
		classified = append(classified[:0], call.Input...)
		return permission.Ask
	})
	registry := NewRegistry(NewOutputStore(0), recorderTool{name: "lister", schema: listerSchema})
	registry.SetPermissionGate(unusedGate{}, policy)

	_, err := registry.Materialize(Permissions{"lister": true}).Settle(
		context.Background(),
		Call{ID: "c1", Name: "lister", Input: json.RawMessage(`{"items":"[\"a\"]"}`)},
	)
	if !errors.Is(err, ErrPermissionUnresolved) {
		t.Fatalf("Settle error = %v, want ErrPermissionUnresolved", err)
	}
	if string(classified) != `{"items":["a"]}` {
		t.Fatalf("classified input = %s, want repaired input", classified)
	}
}

func TestRegistry_RepairableLSPRenameCannotBypassPermission(t *testing.T) {
	lsp := NewLSPTool(t.TempDir())
	started := false
	lsp.commandFor = func(string) ([]string, error) {
		started = true
		return nil, errors.New("must not start")
	}
	registry := NewRegistry(NewOutputStore(0), lsp)
	registry.SetPermissionGate(unusedGate{}, policyFunc(func(_ string, call contracttool.Call) permission.Decision {
		registered, _ := registry.Lookup(call.Name)
		effects, _ := EffectsForCall(registered, call)
		if effects == WritesFiles {
			return permission.Ask
		}
		return permission.Allow
	}))

	_, err := registry.Materialize(Permissions{"lsp": true}).Settle(context.Background(), Call{
		ID: "rename", Name: "lsp",
		Input: json.RawMessage(`{"operation":"rename","path":"x.go","line":"1","column":"1","new_name":"renamed"}`),
	})
	if !errors.Is(err, ErrPermissionUnresolved) {
		t.Fatalf("Settle error = %v, want permission before execution", err)
	}
	if started {
		t.Fatal("language server started before rename permission")
	}
}

type policyFunc func(string, contracttool.Call) permission.Decision

func (f policyFunc) Decide(sessionID string, call contracttool.Call) permission.Decision {
	return f(sessionID, call)
}
