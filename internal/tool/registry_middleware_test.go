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

func TestRegistry_PermissionStopsBeforeRepairWhenRequesterIsMissing(t *testing.T) {
	var input json.RawMessage
	registry := NewRegistry(NewOutputStore(0), recorderTool{
		name: "lister", schema: listerSchema, got: &input, out: "tool",
	})
	registry.SetPermissionGate(unusedGate{}, askingPolicy{})

	_, err := registry.Materialize(Permissions{"lister": true}).Settle(
		context.Background(),
		Call{ID: "c1", Name: "lister", Input: json.RawMessage(`{"items":"[\"a\"]"}`)},
	)
	if !errors.Is(err, ErrPermissionUnresolved) {
		t.Fatalf("Settle error = %v, want ErrPermissionUnresolved", err)
	}
	if input != nil {
		t.Fatalf("tool executed with %s before permission was resolved", input)
	}
}
