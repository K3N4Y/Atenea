package session

import (
	"encoding/json"
	"strings"
)

//go:generate go run ../../internal/cmd/eventkindgen -source event.go -output ../../frontend/src/features/chat/eventKinds.generated.ts

// Seq is the monotonic sequence a store assigns to every event of a session. It
// starts at 1 and grows by 1 per session, defining the total durable order of
// the history. Every "since" filter in the projections is expressed against it.
type Seq int64

// EventKind names each durable session event within the streaming taxonomy. It
// is a stable string rather than an int so it reads the same in the log, in the
// store and in a UI that maps it one to one.
//
// The set is open: a consumer switching on EventKind needs a default branch,
// because an unknown kind means the producer is newer than the consumer. Events
// with no taxonomy carry "".
type EventKind string

const (
	// ExtensionEventPrefix is the preferred namespace for stable extension
	// events. Extension authors should use ext.<vendor>.<event>.
	ExtensionEventPrefix = "ext."
	// ExperimentalEventPrefix is reserved for private or experimental events.
	ExperimentalEventPrefix = "x-"
)

// IsExtensionEventKind reports whether kind uses one of the namespaces reserved
// for extension-emitted events. A bare namespace is not an event kind.
func IsExtensionEventKind(kind EventKind) bool {
	value := string(kind)
	return strings.HasPrefix(value, ExtensionEventPrefix) && len(value) > len(ExtensionEventPrefix) ||
		strings.HasPrefix(value, ExperimentalEventPrefix) && len(value) > len(ExperimentalEventPrefix)
}

const (
	KindStepStarted  EventKind = "Step.Started"
	KindStepEnded    EventKind = "Step.Ended"
	KindStepFailed   EventKind = "Step.Failed"
	KindStepRetrying EventKind = "Step.Retrying"

	KindTextStarted EventKind = "Text.Started"
	KindTextDelta   EventKind = "Text.Delta"
	KindTextEnded   EventKind = "Text.Ended"

	KindReasoningStarted EventKind = "Reasoning.Started"
	KindReasoningDelta   EventKind = "Reasoning.Delta"
	KindReasoningEnded   EventKind = "Reasoning.Ended"

	KindToolInputStarted EventKind = "Tool.Input.Started"
	KindToolInputDelta   EventKind = "Tool.Input.Delta"
	KindToolInputEnded   EventKind = "Tool.Input.Ended"

	KindToolCalled  EventKind = "Tool.Called"
	KindToolSuccess EventKind = "Tool.Success"
	KindToolFailed  EventKind = "Tool.Failed"

	// KindToolPermissionRequested asks the user for approval before settling a
	// gated tool call (ask-before-run). The host emits it before blocking on the
	// permission gate; a UI shows it as an Approve/Deny prompt. It carries no
	// Message: the projection ignores it. The outcome is expressed by the
	// subsequent Tool.Success or Tool.Failed, not by a separate resolution event.
	KindToolPermissionRequested EventKind = "Tool.Permission.Requested"

	// KindSessionTitle carries the generated session title in Text (a short
	// summary of the first message). A session list prefers it over the first
	// user message; the last Session.Title wins. It materializes no Message: it
	// adds nothing to the conversation, only to the listing.
	KindSessionTitle EventKind = "Session.Title"

	// KindSessionCwd carries in Text the working directory the session was
	// created in, so a session list can group conversations by folder. The last
	// Session.Cwd wins. It materializes no Message.
	KindSessionCwd EventKind = "Session.Cwd"

	// KindComposerPrompt carries in Text the literal prompt sent from a
	// composer. It materializes no Message and never enters the model's context:
	// it only lets a UI rehydrate its prompt history across processes.
	KindComposerPrompt EventKind = "Composer.Prompt"

	// KindSessionMode carries in Text the last mode a UI used. It does not enter
	// the model's context; it lets a session reopen in the mode it was left in.
	KindSessionMode EventKind = "Session.Mode"

	KindPromptCheckpointStarted  EventKind = "Prompt.Checkpoint.Started"
	KindPromptCheckpointFinished EventKind = "Prompt.Checkpoint.Finished"
	KindPromptCheckpointReverted EventKind = "Prompt.Checkpoint.Reverted"

	KindContextCompacted EventKind = "Context.Compacted"
)

// SessionEvent is the durable event: the single source of truth of a session. A
// store assigns SessionID and Seq when appending it, so a producer leaves both
// zero.
//
// Kind names the event; the payload fields carry the data that Kind implies and
// the rest stay zero. Delta events (Text.Delta, Reasoning.Delta, Tool.Input.*)
// leave Message nil so they add nothing to the projection: the assistant's
// Message is materialized when the turn closes (Step.Ended), coalescing the
// accumulated text with its tool calls into a single Message. The struct grows
// additively, so a new field never invalidates an existing consumer.
type SessionEvent struct {
	SessionID string
	Seq       Seq
	Kind      EventKind // taxonomy of the contract; "" for events with no taxonomy

	Message *Message // projection: set when the assistant's turn closes (text + tool calls) or on a tool result

	Text     string // Text/Reasoning deltas and ends, and the Tool.Input.Delta fragment
	CallID   string // Tool.*
	ToolName string // Tool.Called, Tool.Success, Tool.Failed
	// Input carries the complete, valid JSON of Tool.Called / Tool.Input.Ended.
	// The raw fragment of Tool.Input.Delta does NOT go here: json.RawMessage
	// requires valid JSON and the event crosses a JSON boundary, so that
	// fragment travels in Text.
	Input json.RawMessage
	Usage *Usage // estimated input on Step.Started; exact provider usage on Step.Ended
	Error string // failure message of a tool (Tool.Failed) or of a turn (Step.Failed)
	// Diff is a unified diff for the UI ONLY (Tool.Success of a file-mutating
	// tool). It does not enter Message, so the model neither sees it nor pays
	// tokens for it; it is persisted and replayed when the session is rehydrated.
	Diff string
	// Attrs carries extension-specific metadata without growing the published
	// event or SQLite schema for every new value. Keys should be namespaced
	// (for example, "ext.example.trace_id"); consumers preserve unknown keys.
	Attrs map[string]string

	Compaction *CompactionCheckpoint
	Checkpoint *PromptCheckpoint
}

// Usage are the turn's tokens persisted on Step.Ended. It mirrors llm.Usage on
// purpose: the durable contract does not depend on the provider contract, so the
// producer copies the fields when crossing the boundary.
type Usage struct {
	InputTokens          int
	OutputTokens         int
	ReasoningTokens      int
	CacheReadTokens      int
	CacheWriteTokens     int
	CacheableInputTokens int
}
