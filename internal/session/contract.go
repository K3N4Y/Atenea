package session

import contract "github.com/K3N4Y/atenea/agentcore/session"

// The durable event stream is published in agentcore/session: SessionEvent, its
// taxonomy and its payloads are the contract an integration reads and an
// extension emits into. This package is its private side: the Store and its two
// implementations, the projections that fold the log, and the validation that
// guards what enters it. The aliases below re-export the contract so
// implementation code keeps one spelling (session.SessionEvent, session.Seq)
// whichever side of the boundary a type is defined on.
//
// A new contract type belongs in agentcore/session and gets an alias here; a new
// implementation detail belongs here and nowhere else.

type (
	Seq          = contract.Seq
	EventKind    = contract.EventKind
	SessionEvent = contract.SessionEvent
	Usage        = contract.Usage

	Role     = contract.Role
	Message  = contract.Message
	Image    = contract.Image
	ToolCall = contract.ToolCall

	ContextEpoch         = contract.ContextEpoch
	CompactionReason     = contract.CompactionReason
	StructuredSummary    = contract.StructuredSummary
	CompactionCheckpoint = contract.CompactionCheckpoint
	PromptCheckpoint     = contract.PromptCheckpoint
)

const (
	KindStepStarted  = contract.KindStepStarted
	KindStepEnded    = contract.KindStepEnded
	KindStepFailed   = contract.KindStepFailed
	KindStepRetrying = contract.KindStepRetrying

	KindTextStarted = contract.KindTextStarted
	KindTextDelta   = contract.KindTextDelta
	KindTextEnded   = contract.KindTextEnded

	KindReasoningStarted = contract.KindReasoningStarted
	KindReasoningDelta   = contract.KindReasoningDelta
	KindReasoningEnded   = contract.KindReasoningEnded

	KindToolInputStarted = contract.KindToolInputStarted
	KindToolInputDelta   = contract.KindToolInputDelta
	KindToolInputEnded   = contract.KindToolInputEnded

	KindToolCalled              = contract.KindToolCalled
	KindToolSuccess             = contract.KindToolSuccess
	KindToolFailed              = contract.KindToolFailed
	KindToolPermissionRequested = contract.KindToolPermissionRequested

	KindSessionTitle   = contract.KindSessionTitle
	KindSessionCwd     = contract.KindSessionCwd
	KindComposerPrompt = contract.KindComposerPrompt
	KindSessionMode    = contract.KindSessionMode

	KindPromptCheckpointStarted  = contract.KindPromptCheckpointStarted
	KindPromptCheckpointFinished = contract.KindPromptCheckpointFinished
	KindPromptCheckpointReverted = contract.KindPromptCheckpointReverted

	KindContextCompacted = contract.KindContextCompacted

	RoleUser      = contract.RoleUser
	RoleAssistant = contract.RoleAssistant
	RoleSystem    = contract.RoleSystem
	RoleTool      = contract.RoleTool

	CompactionPreventive = contract.CompactionPreventive
	CompactionOverflow   = contract.CompactionOverflow
)
