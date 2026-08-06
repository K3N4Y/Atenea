package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

// fakeGate is a test permission.Gate: it records each request and returns a
// fixed decision (approved) and an optional error, without blocking. The real
// blocking is covered by the concrete broker (permission.MemoryGate); here it
// only matters that the runner asks for permission and respects the response.
type fakeGate struct {
	mu       sync.Mutex
	approved bool
	err      error
	asked    []permission.Request
}

func (g *fakeGate) Ask(ctx context.Context, req permission.Request) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, req)
	return g.approved, g.err
}

func (g *fakeGate) calls() []permission.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]permission.Request, len(g.asked))
	copy(out, g.asked)
	return out
}

// policyFunc adapts a function to permission.Policy for tests.
type policyFunc func(sessionID string, call tool.Call) permission.Decision

func (f policyFunc) Decide(sessionID string, call tool.Call) permission.Decision {
	return f(sessionID, call)
}

// askFor builds a test policy that asks for the named tool and allows
// everything else.
func askFor(name string) policyFunc {
	return func(_ string, c tool.Call) permission.Decision {
		if c.Name == name {
			return permission.Ask
		}
		return permission.Allow
	}
}

// TestRunner_GatedToolDeniedPublishesFailedAndDoesNotExecute asserts the
// ask-before-run contract when the user DENIES: the runner asks for permission
// (persists Tool.Permission.Requested before the outcome), does not run the
// tool, publishes Tool.Failed naming the denial, and still continues
// (cont == true) so the model can react to the rejection.
func TestRunner_GatedToolDeniedPublishesFailedAndDoesNotExecute(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	var calls int
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "counter", Input: json.RawMessage(`{}`)},
		llm.Event{Kind: llm.StepEnded},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), countingTool{calls: &calls})
	r := newRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"counter": true}, func() string { return "a1" })

	gate := &fakeGate{approved: false}
	r.setPermissionGate(gate, askFor("counter"))

	cont, err := r.runTurn(ctx, "s1")
	if err != nil {
		t.Fatalf("runTurn unexpected error: %v", err)
	}
	if !cont {
		t.Errorf("cont = false, want true (a denied tool call still continues)")
	}

	// Permission was asked for c1, with its name.
	got := gate.calls()
	if len(got) != 1 || got[0].CallID != "c1" || got[0].ToolName != "counter" {
		t.Fatalf("gate.Ask calls = %+v, want one for c1/counter", got)
	}

	log := store.snapshot()
	reqSeq, okReq := seqOfKind(log, session.KindToolPermissionRequested, "c1")
	if !okReq {
		t.Fatalf("no Tool.Permission.Requested of c1 persisted")
	}
	failSeq, okFail := seqOfKind(log, session.KindToolFailed, "c1")
	if !okFail {
		t.Fatalf("no Tool.Failed of c1 persisted (denied)")
	}
	if !(reqSeq < failSeq) {
		t.Errorf("Permission.Requested (Seq %d) does not appear before Tool.Failed (Seq %d)", reqSeq, failSeq)
	}
	if _, okSucc := seqOfKind(log, session.KindToolSuccess, "c1"); okSucc {
		t.Errorf("Tool.Success of c1 happened despite the denial")
	}
	if calls != 0 {
		t.Errorf("tool ran %d times despite the denial; want 0", calls)
	}
	for _, ev := range log {
		if ev.Kind == session.KindToolFailed && ev.CallID == "c1" {
			if !strings.Contains(strings.ToLower(ev.Error), "deni") {
				t.Errorf("Tool.Failed.Error = %q, want it to mention the denial", ev.Error)
			}
		}
	}

	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages after denied tool: %v", err)
	}
	projected := toLLMMessages(msgs)
	for _, message := range projected {
		if message.Role == string(session.RoleTool) && message.ToolCallID == "c1" {
			if !message.IsError {
				t.Fatal("denied tool result reached provider with IsError=false, want true")
			}
			return
		}
	}
	t.Fatalf("denied tool result c1 not projected to provider: %+v", projected)
}

// TestRunner_GatedToolApprovedSettlesAfterAsking asserts the APPROVAL path: the
// runner asks for permission (Tool.Permission.Requested before Tool.Success),
// and on approval settles the tool normally, persisting Tool.Success with its
// output.
func TestRunner_GatedToolApprovedSettlesAfterAsking(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{"text":"pong"}`)},
		llm.Event{Kind: llm.StepEnded},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := newRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"echo": true}, func() string { return "a1" })

	gate := &fakeGate{approved: true}
	r.setPermissionGate(gate, askFor("echo"))

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn unexpected error: %v", err)
	}

	if got := gate.calls(); len(got) != 1 || got[0].CallID != "c1" {
		t.Fatalf("gate.Ask calls = %+v, want one for c1", got)
	}

	log := store.snapshot()
	reqSeq, okReq := seqOfKind(log, session.KindToolPermissionRequested, "c1")
	if !okReq {
		t.Fatalf("no Tool.Permission.Requested of c1 persisted")
	}
	succSeq, okSucc := seqOfKind(log, session.KindToolSuccess, "c1")
	if !okSucc {
		t.Fatalf("no Tool.Success of c1 persisted (approved)")
	}
	if !(reqSeq < succSeq) {
		t.Errorf("Permission.Requested (Seq %d) does not appear before Tool.Success (Seq %d)", reqSeq, succSeq)
	}
	for _, ev := range log {
		if ev.Kind == session.KindToolSuccess && ev.CallID == "c1" && ev.Text != "pong" {
			t.Errorf("Tool.Success.Text = %q, want %q (the tool ran after approval)", ev.Text, "pong")
		}
	}
}

// TestRunner_GateSkippedWhenPolicyAllows is the triangulation: with a gate
// present but a policy that allows everything, the runner does NOT ask and
// settles directly (M5 path intact), even though the gate would have denied.
func TestRunner_GateSkippedWhenPolicyAllows(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{"text":"pong"}`)},
		llm.Event{Kind: llm.StepEnded},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := newRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"echo": true}, func() string { return "a1" })

	gate := &fakeGate{approved: false}                                                                                 // would deny if asked
	r.setPermissionGate(gate, policyFunc(func(_ string, c tool.Call) permission.Decision { return permission.Allow })) // nothing gated

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn unexpected error: %v", err)
	}

	if got := gate.calls(); len(got) != 0 {
		t.Errorf("gate.Ask was called %d times; want 0 (the tool was not gated)", len(got))
	}
	log := store.snapshot()
	if _, ok := seqOfKind(log, session.KindToolPermissionRequested, "c1"); ok {
		t.Errorf("Tool.Permission.Requested persisted for a non-gated tool")
	}
	if _, ok := seqOfKind(log, session.KindToolSuccess, "c1"); !ok {
		t.Errorf("non-gated tool was not settled (missing Tool.Success)")
	}
}

func TestRunner_AutoAcceptSafeBashSkipsPermissionButExpansionAsks(t *testing.T) {
	for _, tt := range []struct {
		name, command string
		wantAsk       bool
	}{
		{name: "safe", command: "touch safe.txt"},
		{name: "expansion", command: "touch ~/escaped", wantAsk: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := newRecordingStore()
			seedUser(t, store, "s1")
			input, err := json.Marshal(map[string]string{"command": tt.command})
			if err != nil {
				t.Fatal(err)
			}
			provider := llm.NewFakeProvider(llm.Event{Kind: llm.StepStarted}, llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: input}, llm.Event{Kind: llm.StepEnded})
			root := t.TempDir()
			bash := tool.NewBashTool(root)
			reg := tool.NewRegistry(tool.NewOutputStore(0), bash)
			r := newRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"bash": true}, func() string { return "a1" })
			modes := permission.NewAutoAcceptModes()
			modes.Set("s1", true)
			r.setPermissionGate(&fakeGate{approved: false}, permission.NewAutoAcceptPolicy(permission.NewEffectsPolicy(reg), modes, reg))
			if _, err := r.runTurn(ctx, "s1"); err != nil {
				t.Fatal(err)
			}
			_, asked := seqOfKind(store.snapshot(), session.KindToolPermissionRequested, "c1")
			if asked != tt.wantAsk {
				t.Fatalf("permission event = %v, want %v", asked, tt.wantAsk)
			}
			_, statErr := os.Stat(filepath.Join(root, "safe.txt"))
			if !tt.wantAsk && statErr != nil {
				t.Fatalf("safe command did not execute: %v", statErr)
			}
		})
	}
}

func TestRunner_AutoAcceptStillAsksForUndeclaredMCPShape(t *testing.T) {
	store := newRecordingStore()
	seedUser(t, store, "s1")
	provider := llm.NewFakeProvider(llm.Event{Kind: llm.StepStarted}, llm.Event{Kind: llm.ToolCall, CallID: "m1", ToolName: "counter", Input: json.RawMessage(`{}`)}, llm.Event{Kind: llm.StepEnded})
	calls := 0
	reg := tool.NewRegistry(tool.NewOutputStore(0), countingTool{calls: &calls})
	r := newRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"counter": true}, func() string { return "a1" })
	modes := permission.NewAutoAcceptModes()
	modes.Set("s1", true)
	r.setPermissionGate(&fakeGate{approved: false}, permission.NewAutoAcceptPolicy(permission.NewEffectsPolicy(reg), modes, reg))
	if _, err := r.runTurn(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if _, asked := seqOfKind(store.snapshot(), session.KindToolPermissionRequested, "m1"); !asked {
		t.Fatal("undeclared MCP-shaped tool did not request permission")
	}
	if calls != 0 {
		t.Fatalf("undeclared tool executed %d times after denial", calls)
	}
}

func TestRunner_AutoAcceptHardLinkAsksAndPreservesOutsideAlias(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.Link(victim, outside); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	store := newRecordingStore()
	seedUser(t, store, "s1")
	provider := llm.NewFakeProvider(llm.Event{Kind: llm.StepStarted}, llm.Event{Kind: llm.ToolCall, CallID: "h1", ToolName: "bash", Input: json.RawMessage(`{"command":"touch victim.txt"}`)}, llm.Event{Kind: llm.StepEnded})
	bash := tool.NewBashTool(root)
	reg := tool.NewRegistry(tool.NewOutputStore(0), bash)
	r := newRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"bash": true}, func() string { return "a1" })
	modes := permission.NewAutoAcceptModes()
	modes.Set("s1", true)
	r.setPermissionGate(&fakeGate{approved: false}, permission.NewAutoAcceptPolicy(permission.NewEffectsPolicy(reg), modes, reg))
	if _, err := r.runTurn(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if _, asked := seqOfKind(store.snapshot(), session.KindToolPermissionRequested, "h1"); !asked {
		t.Fatal("hard-linked mutation did not ask")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatalf("outside alias changed to %q", got)
	}
}

// TestRunner_GatedAndUngatedToolsConcurrent runs two tool calls in one turn
// (one gated and approved, one not gated): both settle. Run with -race to
// verify the gate introduces no races between the settle goroutines.
func TestRunner_GatedAndUngatedToolsConcurrent(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	var calls int
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{"text":"pong"}`)},
		llm.Event{Kind: llm.ToolCall, CallID: "c2", ToolName: "counter", Input: json.RawMessage(`{}`)},
		llm.Event{Kind: llm.StepEnded},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{}, countingTool{calls: &calls})
	r := newRunner(store, session.NewMemoryInbox(), fake, reg,
		tool.Permissions{"echo": true, "counter": true}, func() string { return "a1" })

	gate := &fakeGate{approved: true}
	r.setPermissionGate(gate, askFor("echo")) // only echo is gated

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn unexpected error: %v", err)
	}

	log := store.snapshot()
	if _, ok := seqOfKind(log, session.KindToolSuccess, "c1"); !ok {
		t.Errorf("missing Tool.Success of c1 (gated echo, approved)")
	}
	if _, ok := seqOfKind(log, session.KindToolSuccess, "c2"); !ok {
		t.Errorf("missing Tool.Success of c2 (non-gated counter)")
	}
	if calls != 1 {
		t.Errorf("counter ran %d times; want 1", calls)
	}
	if got := gate.calls(); len(got) != 1 || got[0].CallID != "c1" {
		t.Errorf("gate.Ask = %+v; want only c1", got)
	}
}

// TestRunner_PolicyDeniedFailsWithoutAsking asserts the Deny verdict: the
// runner publishes Tool.Failed with the permission-denied cause WITHOUT
// consulting the gate, does not run the tool, and does not persist
// Tool.Permission.Requested (there is nothing for the user to answer). No
// shipped policy returns Deny yet; this pins the runner path a future
// rule-based policy relies on.
func TestRunner_PolicyDeniedFailsWithoutAsking(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	var calls int
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "counter", Input: json.RawMessage(`{}`)},
		llm.Event{Kind: llm.StepEnded},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), countingTool{calls: &calls})
	r := newRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"counter": true}, func() string { return "a1" })

	gate := &fakeGate{approved: true} // would approve if asked: Deny must not ask
	r.setPermissionGate(gate, policyFunc(func(_ string, c tool.Call) permission.Decision { return permission.Deny }))

	cont, err := r.runTurn(ctx, "s1")
	if err != nil {
		t.Fatalf("runTurn unexpected error: %v", err)
	}
	if !cont {
		t.Errorf("cont = false, want true (a denied tool call still continues)")
	}

	if got := gate.calls(); len(got) != 0 {
		t.Errorf("gate.Ask was called %d times; want 0 (Deny never asks)", len(got))
	}
	if calls != 0 {
		t.Errorf("tool ran %d times despite the policy denial; want 0", calls)
	}
	log := store.snapshot()
	if _, ok := seqOfKind(log, session.KindToolPermissionRequested, "c1"); ok {
		t.Errorf("Tool.Permission.Requested persisted for a policy-denied call")
	}
	if _, ok := seqOfKind(log, session.KindToolFailed, "c1"); !ok {
		t.Fatalf("no Tool.Failed of c1 persisted (policy denied)")
	}
	for _, ev := range log {
		if ev.Kind == session.KindToolFailed && ev.CallID == "c1" {
			if !strings.Contains(strings.ToLower(ev.Error), "deni") {
				t.Errorf("Tool.Failed.Error = %q, want it to mention the denial", ev.Error)
			}
		}
	}
}
