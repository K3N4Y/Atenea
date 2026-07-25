package tool

import (
	"context"
	"encoding/json"
)

// Tool is a registered tool: the schema it announces and its execution. A host
// materializes Name/Description/Schema into what the model may call and settles
// Execute when the model calls it.
//
// Execute receives the raw JSON input the model produced and must parse it with
// json.Unmarshal, never by string matching: the same model escapes the same
// JSON differently between turns. It returns the complete Result — capping large
// output is the host's job, not the tool's.
//
// A tool may be settled concurrently with other tools of the same turn, so
// Execute must be safe for concurrent use. Returning an error means the call
// failed; returning a Result with output describing the problem means the model
// gets a chance to correct itself. Prefer the second for anything the model can
// fix.
//
// Whatever the input, Execute has to come back: a host settles it in a goroutine
// of the turn, so a panic takes the whole agent down and a call that never
// returns hangs the session. Honor the context — a cancelled one is how a user
// interruption arrives — and turn a malformed input into an error, never a panic.
// The tooltest kit checks all of this against an implementation.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(ctx context.Context, input json.RawMessage) (Result, error)
}

// Call is a tool call waiting to be settled: the id that pairs the result with
// the model's request, the tool name, and the raw JSON input. A named struct
// grows (metadata, provenance) without changing every signature it travels
// through.
type Call struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// Result is the settled result of a tool call. Output is what the model sees in
// the next turn; Truncated marks it as a capped version of a larger output the
// host kept, addressable by the Call's ID. Diff is a unified diff for the UI
// ONLY: the model never sees it and it is never capped, so a tool that mutates
// files can show its change without spending context on it.
type Result struct {
	Output    string
	Truncated bool
	Diff      string
}
