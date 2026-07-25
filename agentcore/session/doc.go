// Package session is the published contract of a session's durable event
// stream.
//
// SessionEvent is the single source of truth of a conversation: an append-only
// log where every event carries a Kind from the streaming taxonomy and the
// payload that Kind implies. Everything a host shows, replays or exports is a
// projection of that log, so this is the contract an integration reads —
// a UI, a headless consumer, an exporter — and the one an extension emits into.
//
// Only the data shape is published. The Store that persists it, the projections
// that fold it and the validation that guards it stay private under
// internal/session. Two consequences follow, and they are deliberate:
//
//   - EventKind is a string, so the set is open. A consumer that switches on it
//     must have a default branch: an unknown kind is something newer than the
//     consumer, not an error.
//   - The payload fields are additive. A field that is zero means "not carried
//     by this Kind", never "false".
package session
