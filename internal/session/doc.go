// Package session is the private side of the durable session domain: the Store
// and its two implementations, the projections that fold the event log, and the
// validation that guards what enters it. The event stream itself — SessionEvent,
// EventKind and their payloads — is published in agentcore/session and
// re-exported here by contract.go.
//
// The Store is an append-only event log as the single source of truth, with
// everything else derived by projection:
//
//   - Messages reprojects the conversation, coalescing each assistant turn's
//     text with its tool calls into one message.
//   - PendingToolCalls projects a Tool.Called with no later Tool.Success or
//     Tool.Failed, which is how a resumed session closes tool calls left hanging
//     by a crash before opening the next turn.
//   - Epoch exposes the ContextEpoch of a turn so the runner can detect a
//     context change that happened between preparing a request and sending it.
//   - CommitCompaction is the only way a Context.Compacted event enters the log,
//     so a checkpoint is always validated and always checked against the epoch it
//     was computed for.
//
// MemoryStore and SQLiteStore are interchangeable behind the same interface and
// validated by the same contract test. The Inbox is the session's durable input
// (queue and steer) behind its own interface, drained by the runner and promoted
// into history messages.
package session
