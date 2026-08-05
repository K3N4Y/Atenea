package llm

import "encoding/json"

// ToolDef is the announceable contract of a tool. Name is its stable internal
// route; WireName is used only when an adapter selects CustomFormat. Schema is
// always the portable JSON fallback, so an unsupported provider never drops it.
type ToolDef struct {
	Name         string
	Description  string
	Schema       json.RawMessage
	WireName     string
	CustomFormat *ToolCustomFormat
}

type ToolCustomFormat struct {
	Syntax     string
	Definition string
}
