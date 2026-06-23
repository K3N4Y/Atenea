package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

// resolveUntil retries Resolve until it finds the pending request (Ask registers
// on its own goroutine, so the first Resolve may arrive early). Fails the test
// if it never appears. Isolates the only synchronization point of the broker.
func resolveUntil(t *testing.T, gate *MemoryPermissionGate, sessionID, callID string, approved bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if gate.Resolve(sessionID, callID, approved) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("Ask never registered the request %s/%s", sessionID, callID)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// TestMemoryPermissionGate_AskBlocksUntilApprove asserts the happy path: Ask
// blocks until Resolve delivers a decision, and returns (true, nil) on approve.
func TestMemoryPermissionGate_AskBlocksUntilApprove(t *testing.T) {
	gate := NewMemoryPermissionGate()
	req := PermissionRequest{SessionID: "s1", CallID: "c1", ToolName: "bash"}

	type result struct {
		approved bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		approved, err := gate.Ask(context.Background(), req)
		done <- result{approved, err}
	}()

	resolveUntil(t, gate, "s1", "c1", true)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Ask unexpected error: %v", got.err)
		}
		if !got.approved {
			t.Errorf("Ask approved = false, want true (it was approved)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not return after Resolve (left blocked)")
	}
}

// TestMemoryPermissionGate_AskDeny is the denial path: Resolve(false) makes Ask
// return (false, nil).
func TestMemoryPermissionGate_AskDeny(t *testing.T) {
	gate := NewMemoryPermissionGate()

	type result struct {
		approved bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		approved, err := gate.Ask(context.Background(), PermissionRequest{SessionID: "s1", CallID: "c1"})
		done <- result{approved, err}
	}()

	resolveUntil(t, gate, "s1", "c1", false)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Ask unexpected error: %v", got.err)
		}
		if got.approved {
			t.Errorf("Ask approved = true, want false (it was denied)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not return after Resolve")
	}
}

// TestMemoryPermissionGate_ResolveUnknownReturnsFalse: resolving a request that
// does not exist (callID with no pending Ask) returns false without panicking.
func TestMemoryPermissionGate_ResolveUnknownReturnsFalse(t *testing.T) {
	gate := NewMemoryPermissionGate()
	if gate.Resolve("s1", "unknown", true) {
		t.Errorf("Resolve of a non-existent request returned true, want false")
	}
}

// TestMemoryPermissionGate_CtxCancelUnblocksAsk: cancelling the ctx unblocks Ask
// with an error (without Resolve). This is the path of the stop button mid-wait.
func TestMemoryPermissionGate_CtxCancelUnblocksAsk(t *testing.T) {
	gate := NewMemoryPermissionGate()
	ctx, cancel := context.WithCancel(context.Background())

	type result struct {
		approved bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		approved, err := gate.Ask(ctx, PermissionRequest{SessionID: "s1", CallID: "c1"})
		done <- result{approved, err}
	}()

	cancel()

	select {
	case got := <-done:
		if got.err == nil {
			t.Errorf("Ask err = nil after cancelling ctx, want an error")
		}
		if got.approved {
			t.Errorf("Ask approved = true after cancellation, want false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not return after cancelling the ctx (left hanging)")
	}
}

// TestMemoryPermissionGate_SecondResolveReturnsFalse: after resolving a request,
// a second Resolve for the same callID returns false (no longer pending).
func TestMemoryPermissionGate_SecondResolveReturnsFalse(t *testing.T) {
	gate := NewMemoryPermissionGate()
	done := make(chan struct{})
	go func() {
		gate.Ask(context.Background(), PermissionRequest{SessionID: "s1", CallID: "c1"})
		close(done)
	}()

	resolveUntil(t, gate, "s1", "c1", true)
	<-done // Ask already received: the request was withdrawn

	if gate.Resolve("s1", "c1", true) {
		t.Errorf("second Resolve returned true, want false (request already resolved)")
	}
}

// TestMemoryPermissionGate_ConcurrentDistinctCalls runs several Ask/Resolve for
// distinct callIDs in parallel: each one receives its own decision. Run with
// -race to verify the pending map has no races.
func TestMemoryPermissionGate_ConcurrentDistinctCalls(t *testing.T) {
	gate := NewMemoryPermissionGate()
	const n = 20

	var wg sync.WaitGroup
	got := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		callID := callIDFor(i)
		want := i%2 == 0
		go func(idx int, id string, approve bool) {
			defer wg.Done()
			approved, err := gate.Ask(context.Background(), PermissionRequest{SessionID: "s1", CallID: id})
			if err != nil {
				t.Errorf("Ask(%s) error: %v", id, err)
			}
			got[idx] = approved
		}(i, callID, want)
	}

	for i := 0; i < n; i++ {
		resolveUntil(t, gate, "s1", callIDFor(i), i%2 == 0)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if want := i%2 == 0; got[i] != want {
			t.Errorf("call %d: approved = %v, want %v", i, got[i], want)
		}
	}
}

func callIDFor(i int) string {
	return "c" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}
