package session

// Role is the author of a projected message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Message is the projection of the conversation for one turn: the text plus the
// Seq of the event that materialized it, which orders and filters it. For the
// tool-call round trip it also carries ToolCalls (the calls the assistant asked
// for, in order) and ToolCallID (on a tool result, the call it answers).
type Message struct {
	ID         string
	Role       Role
	Text       string
	ToolCalls  []ToolCall // role=assistant: tool calls the model asked for
	ToolCallID string     // role=tool: matches the assistant tool call this result answers
	IsError    bool       // role=tool: the result represents an execution failure
	Seq        Seq
}

// ToolCall is an assistant tool call in the projection: the id the result pairs
// with, the tool name and the raw JSON arguments. Arguments is a string rather
// than json.RawMessage so it survives a JSON boundary even when empty.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}
