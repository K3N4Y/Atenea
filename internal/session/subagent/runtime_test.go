package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

func runtimeIDs() func() string {
	var n atomic.Int64
	return func() string { return "runtime-" + string(rune('a'+n.Add(1))) }
}
func runtimeTool(provider llm.Provider) *TaskTool {
	return NewTaskTool([]agent.Def{{Name: "worker", Tools: []string{"echo"}}}, provider, tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{}), runtimeIDs())
}

func TestTaskOutputSchema(t *testing.T) {
	for _, tc := range []struct {
		report  string
		wantErr bool
	}{{`{"answer":"ok"}`, false}, {`{"answer":3}`, true}, {`not-json`, true}} {
		provider := llm.NewFakeProvider(llm.Event{Kind: llm.TextStarted}, llm.Event{Kind: llm.TextDelta, Text: tc.report}, llm.Event{Kind: llm.TextEnded}, llm.Event{Kind: llm.StepEnded})
		_, err := runtimeTool(provider).Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x","output_schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}}`))
		if tc.wantErr != (err != nil) {
			t.Fatalf("report %q err=%v", tc.report, err)
		}
		if err != nil && !strings.Contains(err.Error(), "schema violation") {
			t.Fatalf("non-actionable error: %v", err)
		}
	}
}

func TestTaskOutputSchemaIsIncludedInChildPrompt(t *testing.T) {
	provider := &spyProvider{}
	schema := `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`
	_, _ = runtimeTool(provider).Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x","output_schema":`+schema+`}`))
	if !strings.Contains(provider.system, schema) {
		t.Fatalf("system prompt does not contain schema: %q", provider.system)
	}
}

func TestTaskAllowsMultipleRequestsWithoutRequestBudget(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.Event{{{Kind: llm.ToolCall, CallID: "c", ToolName: "echo", Input: json.RawMessage(`{"text":"x"}`)}, {Kind: llm.StepEnded}}, {{Kind: llm.TextStarted}, {Kind: llm.TextDelta, Text: "done"}, {Kind: llm.TextEnded}, {Kind: llm.StepEnded}}}}
	result, err := runtimeTool(provider).Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x"}`))
	if err != nil || result.Output != "done" {
		t.Fatalf("result=%q err=%v, want a completed multi-request task", result.Output, err)
	}
}

func TestTaskCountsTokensWithoutEnforcingABudget(t *testing.T) {
	usage := &llm.Usage{InputTokens: 2, OutputTokens: 3, ReasoningTokens: 4}
	recorder := tool.NewSettlementRecorder()
	ctx := tool.WithSettlementRecorder(context.Background(), recorder)
	if _, err := runtimeTool(llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded, Usage: usage})).Execute(ctx, json.RawMessage(`{"subagent_type":"worker","prompt":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if got := recorder.TaskSettlement().Tokens; got != 9 {
		t.Fatalf("recorded tokens = %d, want 9", got)
	}
}

func TestTaskRejectsRemovedTokenBudget(t *testing.T) {
	_, err := runtimeTool(llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded})).Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x","token_budget":4000}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field \"token_budget\"") {
		t.Fatalf("removed token_budget err=%v", err)
	}
}

func TestTaskSchemaDoesNotOfferTokenBudget(t *testing.T) {
	schema := string(runtimeTool(llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded})).Schema())
	if strings.Contains(schema, "token_budget") {
		t.Fatalf("task schema still offers token_budget: %s", schema)
	}
	if strings.Contains(schema, "request_budget") {
		t.Fatalf("task schema still offers request_budget: %s", schema)
	}
}

type blockingProvider struct{}

func (blockingProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	ch := make(chan llm.Event)
	go func() { defer close(ch); <-ctx.Done() }()
	return ch, nil
}

func TestTaskTimeoutAndDetachedSupervision(t *testing.T) {
	tt := runtimeTool(blockingProvider{})
	_, err := tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x","timeout_ms":5}`))
	var budget *BudgetError
	if !errors.As(err, &budget) || budget.Kind != "timeout_ms" {
		t.Fatalf("timeout err=%v", err)
	}

	result, err := tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x","detached":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var start map[string]any
	if err := json.Unmarshal([]byte(result.Output), &start); err != nil {
		t.Fatal(err)
	}
	id := start["job_id"].(string)
	tools := tt.SupervisionTools()
	byName := map[string]tool.Tool{}
	for _, candidate := range tools {
		byName[candidate.Name()] = candidate
	}
	var status tool.Result
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		status, err = byName["task_status"].Execute(context.Background(), json.RawMessage(`{"job_id":"`+id+`"}`))
		if err == nil && strings.Contains(status.Output, `"status":"running"`) && strings.Contains(status.Output, `"requests":1`) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil || !strings.Contains(status.Output, `"requests":1`) {
		t.Fatalf("status=%s err=%v", status.Output, err)
	}
	if _, err = byName["task_cancel"].Execute(context.Background(), json.RawMessage(`{"job_id":"`+id+`"}`)); err != nil {
		t.Fatal(err)
	}
	wait, err := byName["task_wait"].Execute(context.Background(), json.RawMessage(`{"job_id":"`+id+`"}`))
	if err != nil || !strings.Contains(wait.Output, `"status":"cancelled"`) {
		t.Fatalf("wait=%s err=%v", wait.Output, err)
	}
	if _, err = byName["task_cancel"].Execute(context.Background(), json.RawMessage(`{"job_id":"`+id+`"}`)); err != nil {
		t.Fatalf("idempotent cancel: %v", err)
	}
	if _, err = byName["task_status"].Execute(context.Background(), json.RawMessage(`{"job_id":"missing"}`)); err == nil {
		t.Fatal("unknown job accepted")
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = byName["task_wait"].Execute(cancelCtx, json.RawMessage(`{"job_id":"`+id+`"}`)); err != nil { /* completed wins either branch */
	}
}

func TestTaskInjectedProviderEnvironmentAndSummary(t *testing.T) {
	fallback := llm.NewFakeProvider(llm.Event{Kind: llm.TextDelta, Text: "wrong"}, llm.Event{Kind: llm.StepEnded})
	tt := runtimeTool(fallback)
	var gotDef string
	tt.SetProviderResolver(func(_ context.Context, def agent.Def) (llm.Provider, error) {
		gotDef = def.Name
		return llm.NewFakeProvider(llm.Event{Kind: llm.TextStarted}, llm.Event{Kind: llm.TextDelta, Text: "isolated"}, llm.Event{Kind: llm.TextEnded}, llm.Event{Kind: llm.StepEnded, Usage: &llm.Usage{InputTokens: 1, OutputTokens: 2, ReasoningTokens: 3}}), nil
	})
	var cleaned atomic.Bool
	tt.SetEnvironmentResolver(func(context.Context, agent.Def) (ChildEnvironment, error) {
		return ChildEnvironment{Store: session.NewMemoryStore(), Inbox: session.NewMemoryInbox(), Registry: tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{}), Cleanup: func() error { cleaned.Store(true); return nil }}, nil
	})
	recorder := tool.NewSettlementRecorder()
	ctx := tool.WithSettlementRecorder(context.Background(), recorder)
	result, err := tt.Execute(ctx, json.RawMessage(`{"subagent_type":"worker","prompt":"x","worktree":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "isolated" || gotDef != "worker" || !cleaned.Load() {
		t.Fatalf("result=%q def=%q cleanup=%v", result.Output, gotDef, cleaned.Load())
	}
	summary := recorder.TaskSettlement()
	if summary.Requests != 1 || summary.Tokens != 6 || summary.Duration <= 0 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestTaskWaitHonorsCancellation(t *testing.T) {
	tt := runtimeTool(blockingProvider{})
	start, _ := tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x","detached":true}`))
	var value map[string]any
	json.Unmarshal([]byte(start.Output), &value)
	wait := tt.SupervisionTools()[1]
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := wait.Execute(ctx, json.RawMessage(`{"job_id":"`+value["job_id"].(string)+`"}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait err=%v", err)
	}
	tt.SupervisionTools()[2].Execute(context.Background(), json.RawMessage(`{"job_id":"`+value["job_id"].(string)+`"}`))
}

func TestSupervisorRejectsStartsAfterClose(t *testing.T) {
	tt := runtimeTool(llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded}))
	tt.supervisor.Close()
	_, err := tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x","detached":true}`))
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("start after close err=%v", err)
	}
}

func TestTaskDiscardsFailedIsolatedEnvironment(t *testing.T) {
	tt := runtimeTool(llm.NewFakeProvider(llm.Event{Kind: llm.TextStarted}, llm.Event{Kind: llm.TextDelta, Text: `{"answer":3}`}, llm.Event{Kind: llm.TextEnded}, llm.Event{Kind: llm.StepEnded}))
	var discarded atomic.Bool
	tt.SetEnvironmentResolver(func(context.Context, agent.Def) (ChildEnvironment, error) {
		return ChildEnvironment{Store: session.NewMemoryStore(), Inbox: session.NewMemoryInbox(), Registry: tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{}), Workspace: "/tmp/isolation", Discard: func() error { discarded.Store(true); return nil }}, nil
	})
	_, err := tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x","worktree":true,"output_schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}}`))
	if err == nil || !discarded.Load() {
		t.Fatalf("err=%v discarded=%v", err, discarded.Load())
	}
}

func TestTypedWorktreeResultCarriesArtifactEnvelope(t *testing.T) {
	tt := runtimeTool(llm.NewFakeProvider(llm.Event{Kind: llm.TextStarted}, llm.Event{Kind: llm.TextDelta, Text: `{"answer":"ok"}`}, llm.Event{Kind: llm.TextEnded}, llm.Event{Kind: llm.StepEnded}))
	tt.SetEnvironmentResolver(func(context.Context, agent.Def) (ChildEnvironment, error) {
		return ChildEnvironment{Store: session.NewMemoryStore(), Inbox: session.NewMemoryInbox(), Registry: tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{}), Workspace: "/tmp/isolation"}, nil
	})
	result, err := tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x","worktree":true,"output_schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}}`))
	if err != nil || !strings.Contains(result.Output, `"worktree":"/tmp/isolation"`) || !strings.Contains(result.Output, `"result":{"answer":"ok"}`) {
		t.Fatalf("result=%s err=%v", result.Output, err)
	}
}

func TestTaskReportsDiscardFailure(t *testing.T) {
	tt := runtimeTool(llm.NewFakeProvider(llm.Event{Kind: llm.TextStarted}, llm.Event{Kind: llm.TextDelta, Text: `not-json`}, llm.Event{Kind: llm.TextEnded}, llm.Event{Kind: llm.StepEnded}))
	tt.SetEnvironmentResolver(func(context.Context, agent.Def) (ChildEnvironment, error) {
		return ChildEnvironment{Store: session.NewMemoryStore(), Inbox: session.NewMemoryInbox(), Registry: tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{}), Discard: func() error { return errors.New("discard failed") }}, nil
	})
	_, err := tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x","worktree":true,"output_schema":{"type":"object"}}`))
	if err == nil || !strings.Contains(err.Error(), "schema violation") || !strings.Contains(err.Error(), "discard failed") {
		t.Fatalf("err=%v", err)
	}
}

func TestTaskTimeoutIncludesConcurrencyQueue(t *testing.T) {
	provider := blockingProvider{}
	tt := runtimeTool(provider)
	tt.SetMaxConcurrency(1)
	first, err := tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"hold","detached":true}`))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for tt.sem != nil && len(tt.sem) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	_, err = tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"queued","timeout_ms":10}`))
	var budget *BudgetError
	if !errors.As(err, &budget) || budget.Kind != "timeout_ms" || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("queued timeout err=%v elapsed=%s", err, time.Since(started))
	}
	var job map[string]any
	_ = json.Unmarshal([]byte(first.Output), &job)
	for _, candidate := range tt.SupervisionTools() {
		if candidate.Name() == "task_cancel" {
			_, _ = candidate.Execute(context.Background(), json.RawMessage(`{"job_id":"`+job["job_id"].(string)+`"}`))
		}
	}
	tt.Close()
}
