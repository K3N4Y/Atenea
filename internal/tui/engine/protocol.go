package engine

import "atenea/internal/session"

// This file defines the protocol between the Engine (producer) and the Bubble
// Tea Model (consumer): the durable events and lifecycle messages that flow
// over the Engine's channel, plus the values its synchronous operations return.
// The presentation layer imports these types; the Engine never imports the
// presentation layer, so the dependency only ever points one way (tui -> engine).

// HistoryLimit caps how many durable prompts the composer history keeps and how
// many the Engine reconstructs. Both layers share this policy from here.
const HistoryLimit = 100

// EventMsg is the durable session event that travels from the Engine to the
// Model. It is a distinct tea.Msg type so the Model's Update loop can switch on
// it without confusing it with the lifecycle messages below.
type EventMsg session.SessionEvent

// RunHandle identifies a concrete run within a session. RunID == 0 means the
// operation did not start a run (/new, /compact).
type RunHandle struct {
	SessionID string
	RunID     uint64
}

// RunDoneMsg marks the end of a run; Err == "" means it finished cleanly.
type RunDoneMsg struct {
	SessionID string
	RunID     uint64
	Err       string
}

// CompactionState is the observable state of a manual compaction.
type CompactionState int

const (
	CompactionQueued CompactionState = iota
	CompactionRunning
	CompactionNotNeeded
	CompactionFailed
)

// CompactionStatusMsg reports a transition in a session's manual compaction.
type CompactionStatusMsg struct {
	SessionID string
	State     CompactionState
	Err       string
}
