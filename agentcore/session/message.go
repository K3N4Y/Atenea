package session

// Role is the author of a projected message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Message is the projection of the conversation for one turn: its ordered rich
// content and the Seq of the event that materialized it. Text precedes Images
// when projected to a provider.
type Message struct {
	ID         string
	Role       Role
	Text       string
	Images     []Image
	ToolCalls  []ToolCall // role=assistant: tool calls the model asked for
	ToolCallID string     // role=tool: matches the assistant tool call this result answers
	IsError    bool       // role=tool: the result represents an execution failure
	Seq        Seq
}

// Image is raw image-file content and its MIME media type.
type Image struct {
	MediaType string
	Data      []byte
}

// ToolCall is an assistant tool call in the projection: the id the result pairs
// with, the tool name and the raw JSON arguments. Arguments is a string rather
// than json.RawMessage so it survives a JSON boundary even when empty.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}
