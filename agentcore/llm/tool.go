package llm

import "encoding/json"

// ToolDef is the announceable schema of a tool: what the Request carries to the
// provider so the model knows which tools it may call and with what input
// shape. Schema is the raw JSON Schema of the input, emitted by the tool
// itself; the adapter translates it to the format of its SDK and never rewrites
// it.
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage
}
