package llm

import (
	"context"
	"encoding/json"
)

// Provider is the boundary with a model. Stream produces exactly ONE turn: it
// emits the turn's events on the returned channel and CLOSES it when the turn
// ends, so a consumer drains it with `for ev := range out`. Cancelling ctx
// interrupts the turn (the shape a user interruption takes) and must close the
// channel too: no receiver is ever left hanging.
//
// The turn is bracketed: it opens with StepStarted and closes with exactly one
// StepEnded or StepFailed, the last event of the stream. That is not decoration —
// a host materializes the assistant's message when the turn closes, so a stream
// that simply stops loses the turn from the history. An interrupted turn is the
// one exception: cancelling means closing the channel, whatever was in flight.
//
// A host may call Stream concurrently for different sessions — a main turn and
// the subagents it spawned — so an implementation must be safe for concurrent
// use.
//
// The llmtest kit checks all of this, plus the bracketing of the blocks inside
// the turn, against an implementation.
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

// Request is the input of one turn. It grows additively, so a new field never
// changes the Provider signature.
type Request struct {
	Model           string
	SessionKey      string    // opaque conversation affinity; adapters may map it to the routing/cache field they support
	System          string    // turn baseline prompt (environment, identity, repo instructions); the host builds it
	Messages        []Message // projected history in the provider's format
	Tools           []ToolDef // tool schemas the model may call in this turn
	MaxOutputTokens int
}

// Message is one history message projected into the provider's format. The
// host builds it from its durable history; the adapter translates it to the
// blocks of its SDK.
//
// Its content is Parts, and only Parts. A Text field beside them would be a
// second way to say the same thing, which leaves every adapter deciding which of
// the two wins and lets the two disagree — so there is exactly one answer to what
// a message says. TextMessage builds the text-only case, TextOnly reads it back.
type Message struct {
	Role       string
	Parts      []Part         // the message's content, in order
	ToolCalls  []ToolCallPart // role=assistant: tool calls the model asked for
	ToolCallID string         // role=tool: matches the assistant tool call this result answers
	IsError    bool           // role=tool: the result represents an execution failure
}

// ToolCallPart is an assistant tool call projected into the history: the id the
// result pairs with, the tool name, and the raw JSON arguments exactly as the
// model emitted them (never re-serialized).
type ToolCallPart struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}
