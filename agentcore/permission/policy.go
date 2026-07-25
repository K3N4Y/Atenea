package permission

import "github.com/K3N4Y/atenea/agentcore/tool"

// Decision is the policy verdict for a tool call.
type Decision int

const (
	// Ask blocks the call until the user approves or denies it. It is the zero
	// value on purpose: an unclassified decision fails safe by asking.
	Ask Decision = iota
	// Allow settles the call without asking.
	Allow
	// Deny fails the call without asking.
	Deny
)

// Policy decides what to do with a tool call before it settles. It receives the
// session the call belongs to and the full call — name and raw input — so an
// implementation can match on command prefixes or paths and scope its rules per
// session.
//
// Decide is consulted from several goroutines of the same turn, so an
// implementation must be safe for concurrent use. It must not execute anything
// or have side effects: it answers a question, it does not settle a call.
type Policy interface {
	Decide(sessionID string, call tool.Call) Decision
}
