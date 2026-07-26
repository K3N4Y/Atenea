package host

import (
	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

// Sitting is the agent state that belongs to one run of the process instead of
// to one assembly of it. Every member is created exactly once and rewired into
// each runner the process builds, because a rewire — an MCP server connecting, a
// workspace change, a model change — must not drop the user's permission
// answers, forget which files they have read, or restart the turn lifecycle
// underneath them.
//
// It is a type of its own, and not just five fields on [Host], because nothing
// in it touches disk. That is what lets a caller who only needs the agent state
// — an engine driven directly, with no database and no credentials file — get it
// from [NewSitting] without opening either, while still going through the one
// constructor that decides what a sitting is made of.
type Sitting struct {
	// Gate is the ask-before-run broker: the runner blocks on it and the UI
	// answers through it. It does not depend on the workspace, so it is created
	// once and every build receives the same one.
	Gate *permission.MemoryGate
	// Grants records the user's "allow this for the rest of the session" answers.
	// The store is separate from the policy that reads it because their lifetimes
	// differ: grants belong to the sitting, while deciding whether one covers a
	// call means asking the tool that would settle it, which comes from the
	// registry of the moment. See permission.SessionGrants.
	Grants *permission.SessionGrants
	// Inbox is the prompt queue the runner drains per session.
	Inbox session.Inbox
	// Agent is the UI-independent turn lifecycle both hosts drive. It is
	// assembled unconfigured on purpose: whichever manager owns the wiring calls
	// Configure with the runner and commands of the moment, and calls it again on
	// every rewire.
	Agent *agent.Service
	// Snapshots is the read state read, write and edit share: read records the
	// hash and the lines it showed, edit anchors an edit against them, write
	// registers a file it created. It is keyed by session rather than by folder,
	// so it outlives a workspace change.
	Snapshots *tool.SessionSnapshots
}

// NewSitting assembles the per-process agent state. It is the only place that
// decides what a sitting is made of, so the engine that assembles one for itself
// and the host that hands one to both UIs cannot end up with different ideas of
// it.
func NewSitting() *Sitting {
	inbox := session.NewMemoryInbox()
	return &Sitting{
		Gate:      permission.NewMemoryGate(),
		Grants:    permission.NewSessionGrants(),
		Inbox:     inbox,
		Agent:     agent.NewService(inbox),
		Snapshots: tool.NewSessionSnapshots(),
	}
}
