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
// A host may call Stream concurrently for different sessions — a main turn and
// the subagents it spawned — so an implementation must be safe for concurrent
// use.
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
type Message struct {
	Role       string
	Text       string
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
