package permission

import (
	"context"
	"sync"
)

// MemoryGate is the in-memory ask-before-run broker: Ask registers a pending
// request per (SessionID, CallID) and blocks; Resolve (invoked by an App
// binding or the TUI engine from the UI) delivers the decision to the waiting
// Ask. It is safe for concurrent use. It does not persist anything: if the
// app restarts with a pending request, the tool call is left unsettled and
// failInterruptedTools (in the runner) closes it as interrupted on the next
// Run.
type MemoryGate struct {
	mu      sync.Mutex
	pending map[string]pendingRequest // key(SessionID,CallID) -> waiting request
}

// pendingRequest is a request blocked in Ask: the request itself, so the
// resolver can derive a session grant from what the runner actually submitted,
// and the cap-1 channel Resolve delivers the decision on.
type pendingRequest struct {
	request  Request
	decision chan bool
}

// NewMemoryGate creates an empty broker.
func NewMemoryGate() *MemoryGate {
	return &MemoryGate{pending: make(map[string]pendingRequest)}
}

// permKey combines sessionID and callID into a collision-free key (the NUL
// separator does not appear in IDs).
func permKey(sessionID, callID string) string {
	return sessionID + "\x00" + callID
}

// Ask registers the request and blocks until Resolve delivers a decision or the
// ctx is cancelled. The channel is buffered (cap 1) so Resolve never blocks on
// delivery. On the cancellation path it drains a decision that may have arrived
// in a race with Resolve before returning the error.
func (g *MemoryGate) Ask(ctx context.Context, req Request) (bool, error) {
	key := permKey(req.SessionID, req.CallID)
	ch := make(chan bool, 1)

	g.mu.Lock()
	g.pending[key] = pendingRequest{request: req, decision: ch}
	g.mu.Unlock()

	select {
	case approved := <-ch:
		return approved, nil
	case <-ctx.Done():
		g.mu.Lock()
		delete(g.pending, key)
		g.mu.Unlock()
		// Race: Resolve may have delivered just before cancellation. If a
		// decision is on the channel, respect it instead of returning the ctx
		// error.
		select {
		case approved := <-ch:
			return approved, nil
		default:
			return false, ctx.Err()
		}
	}
}

// Resolve delivers the decision to the pending Ask for (sessionID, callID) and
// returns true if one was waiting. It removes the request under the lock so a
// second call (or one for an unknown callID) returns false without double
// delivery. Invoked by the App's ResolveToolPermission binding and the TUI
// engine's ResolvePermission.
func (g *MemoryGate) Resolve(sessionID, callID string, approved bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := permKey(sessionID, callID)
	waiting, ok := g.pending[key]
	if !ok {
		return false
	}
	delete(g.pending, key)
	waiting.decision <- approved // cap 1 and single sender: never blocks
	return true
}

// Pending returns the request blocked on a decision for (sessionID, callID).
// The resolver derives a session grant from it instead of from what the UI
// happens to hold, so the grant is always the shape of the call the gate is
// actually blocking.
func (g *MemoryGate) Pending(sessionID, callID string) (Request, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	waiting, ok := g.pending[permKey(sessionID, callID)]
	if !ok {
		return Request{}, false
	}
	return waiting.request, true
}
