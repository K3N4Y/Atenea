package runner

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

type taskProbe struct{ calls *int }

func (t taskProbe) Name() string          { return "task" }
func (taskProbe) Description() string     { return "test" }
func (taskProbe) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t taskProbe) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	(*t.calls)++
	return tool.Result{}, nil
}

func TestRunnerTaskDeniedAndFinalRejectedPersistZero(t *testing.T) {
	for _, tc := range []struct {
		name  string
		final bool
	}{{"denied", false}, {"final-rejected", true}} {
		t.Run(tc.name, func(t *testing.T) {
			store := newRecordingStore()
			seedUser(t, store, "s")
			provider := llm.NewFakeProvider(llm.Event{Kind: llm.StepStarted}, llm.Event{Kind: llm.ToolCall, CallID: "task-call", ToolName: "task", Input: json.RawMessage(`{}`)}, llm.Event{Kind: llm.StepEnded})
			calls := 0
			registry := tool.NewRegistry(tool.NewOutputStore(0), taskProbe{calls: &calls})
			r := newRunner(store, session.NewMemoryInbox(), provider, registry, tool.Permissions{"task": true}, func() string { return "a" })
			if !tc.final {
				r.setPermissionGate(&fakeGate{}, policyFunc(func(string, tool.Call) permission.Decision { return permission.Deny }))
			}
			_, err := r.runTurnWithFinal(context.Background(), "s", tc.final)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatalf("task executed %d times", calls)
			}
			for _, ev := range store.snapshot() {
				if ev.Kind == session.KindToolFailed && ev.CallID == "task-call" {
					if n, ok := session.SubagentToolCalls(ev); !ok || n != 0 {
						t.Fatalf("total = %d, %v", n, ok)
					}
					return
				}
			}
			t.Fatal("task failure not persisted")
		})
	}
}
