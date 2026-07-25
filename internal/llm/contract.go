package llm

import contract "github.com/K3N4Y/atenea/agentcore/llm"

// The provider contract lives in agentcore/llm, the published surface a third
// party implements. This package is its private side: the adapters, the model
// catalog and the context accounting. The aliases below re-export the contract
// so implementation code keeps one spelling (llm.Request, llm.Event) whichever
// side of the boundary a type is defined on.
//
// A new contract type belongs in agentcore/llm and gets an alias here; a new
// implementation detail belongs here and nowhere else.

type (
	Provider     = contract.Provider
	Request      = contract.Request
	Message      = contract.Message
	ToolCallPart = contract.ToolCallPart
	ToolDef      = contract.ToolDef
	Event        = contract.Event
	EventKind    = contract.EventKind
	Usage        = contract.Usage
)

const (
	StepStarted  = contract.StepStarted
	StepEnded    = contract.StepEnded
	StepFailed   = contract.StepFailed
	StepRetrying = contract.StepRetrying

	TextStarted = contract.TextStarted
	TextDelta   = contract.TextDelta
	TextEnded   = contract.TextEnded

	ReasoningStarted = contract.ReasoningStarted
	ReasoningDelta   = contract.ReasoningDelta
	ReasoningEnded   = contract.ReasoningEnded

	ToolCall = contract.ToolCall

	ToolInputStarted = contract.ToolInputStarted
	ToolInputDelta   = contract.ToolInputDelta
	ToolInputEnded   = contract.ToolInputEnded
)
