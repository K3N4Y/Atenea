package permission

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/tool"
)

type fixedPolicy Decision

func (p fixedPolicy) Decide(string, tool.Call) Decision { return Decision(p) }

func TestAutoAcceptModes_IsolatedAndConcurrent(t *testing.T) {
	m := NewAutoAcceptModes()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.Set("one", true); _ = m.Enabled("one") }()
	}
	wg.Wait()
	if !m.Enabled("one") || m.Enabled("two") {
		t.Fatal("mode leaked across sessions")
	}
}

func TestAutoAcceptModeChangeDoesNotResolvePendingGate(t *testing.T) {
	gate := NewMemoryGate()
	modes := NewAutoAcceptModes()
	done := make(chan bool, 1)
	go func() {
		approved, _ := gate.Ask(context.Background(), Request{SessionID: "s", CallID: "c"})
		done <- approved
	}()
	deadline := time.After(time.Second)
	for {
		if _, ok := gate.Pending("s", "c"); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("request never became pending")
		default:
		}
	}
	modes.Set("s", true)
	select {
	case <-done:
		t.Fatal("mode change resolved an already-pending request")
	default:
	}
	gate.Resolve("s", "c", false)
	if <-done {
		t.Fatal("pending request approval changed")
	}
}

func TestAutoAcceptPolicy_OnlyUpgradesAskForSafeCalls(t *testing.T) {
	root := t.TempDir()
	catalog := tool.NewRegistry(nil, tool.NewWriteTool(root, nil), tool.NewBashTool(root))
	modes := NewAutoAcceptModes()
	modes.Set("s", true)
	p := NewAutoAcceptPolicy(fixedPolicy(Ask), modes, catalog)
	write := tool.Call{Name: "write", Input: json.RawMessage(`{"path":"new.txt","content":"ok"}`)}
	if got := p.Decide("s", write); got != Allow {
		t.Fatalf("write = %v", got)
	}
	unsafe := tool.Call{Name: "bash", Input: json.RawMessage(`{"command":"rm -rf ."}`)}
	if got := p.Decide("s", unsafe); got != Ask {
		t.Fatalf("unsafe bash = %v", got)
	}
	deny := NewAutoAcceptPolicy(fixedPolicy(Deny), modes, catalog)
	if got := deny.Decide("s", write); got != Deny {
		t.Fatalf("deny = %v", got)
	}
}
