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

// Definer publishes optional wire and custom-format metadata while retaining a
// mandatory JSON schema fallback.
type Definer interface {
	Tool
	Definition() ToolDefinition
}

type ToolDefinition struct {
	Name         string
	Description  string
	Schema       json.RawMessage
	WireName     string
	CustomFormat *CustomFormat
}

type CustomFormat struct {
	Syntax     string
	Definition string
}

// Freezer materializes a turn-owned tool whose advertised definition and
// execution strategy cannot change underneath that turn.
type Freezer interface {
	Tool
	Freeze() Tool
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
// the next turn; structured metadata survives output capping and persistence.
type Result struct {
	Output    string
	Truncated bool
	Files     []FileResult
	Diff      string
	Metadata  map[string]any
}

// Preview is ephemeral presentation metadata projected from partial tool input.
// It is never authoritative over the final Result.
type Preview struct {
	Files   []FileResult
	Pending bool
	Error   string
	Digest  string
}

// PreviewEvent is an ephemeral host event. It has no durable session sequence
// and must never be reconstructed into model context.
type PreviewEvent struct {
	SessionID string
	CallID    string
	Preview   Preview
}

// MatcherEntry identifies introduced content associated with one target path.
type MatcherEntry struct {
	Path   string
	Digest string
}

// Previewer is an optional capability implemented by a frozen tool strategy.
// Both methods must be pure, tolerate partial input, and never mutate disk or
// session-owned registers.
type Previewer interface {
	Tool
	Preview(ctx context.Context, partial json.RawMessage) Preview
	MatcherEntries(partial json.RawMessage) []MatcherEntry
}

type FileOperation string

const (
	FileUpdated FileOperation = "update"
	FileCreated FileOperation = "create"
	FileDeleted FileOperation = "delete"
	FileMoved   FileOperation = "move"
	FileNoop    FileOperation = "noop"
	FileError   FileOperation = "error"
	FileEdited                = FileUpdated
)

type Diagnostic struct {
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
}

type FileResult struct {
	Path, SourcePath, Destination string
	Operation                     FileOperation
	Preview, OldText, NewText     string
	Diff                          string
	Warnings                      []string
	Diagnostics                   []Diagnostic
	Header, SnapshotTag           string
	FirstChangedLine              int
	Committed                     bool
	Error, DisplayError           string
	SnapshotsPruned               bool
}

const SnapshotTextBudget = 32_768

// PruneSnapshotText applies the exact combined old/new character budget. It
// never prunes output or diffs, which remain model/UI-visible evidence.
func (r *Result) PruneSnapshotText() {
	pruned := false
	for i := range r.Files {
		file := &r.Files[i]
		if len([]rune(file.OldText))+len([]rune(file.NewText)) <= SnapshotTextBudget {
			continue
		}
		file.OldText, file.NewText, file.SnapshotsPruned = "", "", true
		pruned = true
	}
	if pruned {
		if r.Metadata == nil {
			r.Metadata = make(map[string]any)
		}
		r.Metadata["snapshot_text_pruned"] = true
	}
}
