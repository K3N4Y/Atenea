package session

import (
	"context"
	"sync"
)

// PermissionRequest describes the tool call waiting for user approval
// (ask-before-run). The runner builds it and hands it to the PermissionGate;
// the implementation correlates it by (SessionID, CallID) with the response
// arriving from the UI.
type PermissionRequest struct {
	SessionID string
	CallID    string
	ToolName  string
	Input     []byte // raw JSON input of the tool call (informational for the UI)
}

// PermissionGate is the ask-before-run boundary: Ask blocks until the user
// approves or denies the tool call (or the ctx is cancelled). The runner
// consumes it as an optional dependency (nil = never asks); the concrete
// implementation (MemoryPermissionGate, which the UI resolves via an App
// binding) lives here, sibling of Inbox.
type PermissionGate interface {
	// Ask blocks until the user's decision on req and returns approved=true when
	// approved. A cancelled ctx (stop) must unblock Ask with an error.
	Ask(ctx context.Context, req PermissionRequest) (approved bool, err error)
}

// MemoryPermissionGate is the in-memory ask-before-run broker: Ask registers a
// pending request per (SessionID, CallID) and blocks; Resolve (invoked by an
// App binding from the UI) delivers the decision to the waiting Ask. It is
// safe for concurrent use. It does not persist anything: if the app restarts
// with a pending request, the tool call is left unsettled and
// failInterruptedTools (in run.go) closes it as interrupted on the next Run.
type MemoryPermissionGate struct {
	mu      sync.Mutex
	pending map[string]chan bool // key(SessionID,CallID) -> decision channel (cap 1)
}

// NewMemoryPermissionGate creates an empty broker.
func NewMemoryPermissionGate() *MemoryPermissionGate {
	return &MemoryPermissionGate{pending: make(map[string]chan bool)}
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
func (g *MemoryPermissionGate) Ask(ctx context.Context, req PermissionRequest) (bool, error) {
	key := permKey(req.SessionID, req.CallID)
	ch := make(chan bool, 1)

	g.mu.Lock()
	g.pending[key] = ch
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
// delivery. Invoked by the App's ResolveToolPermission binding.
func (g *MemoryPermissionGate) Resolve(sessionID, callID string, approved bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := permKey(sessionID, callID)
	ch, ok := g.pending[key]
	if !ok {
		return false
	}
	delete(g.pending, key)
	ch <- approved // cap 1 and single sender: never blocks
	return true
}
