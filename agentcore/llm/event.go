package llm

import "encoding/json"

// EventKind classifies every event of a provider's turn stream. The set mirrors
// the agent loop's streaming taxonomy minus the events the host produces rather
// than the provider (a settled tool result). An adapter emits only the kinds
// its wire protocol actually reports: a missing kind degrades the UI, it never
// breaks the turn.
type EventKind int

const (
	StepStarted  EventKind = iota // the turn starts
	StepEnded                     // the turn closes, carrying Usage
	StepFailed                    // the stream failed, carrying Err
	StepRetrying                  // transient wait before retrying

	TextStarted // opens a text block
	TextDelta   // text fragment, carrying Text
	TextEnded   // closes the text block

	ReasoningStarted // opens reasoning
	ReasoningDelta   // reasoning fragment, carrying Text
	ReasoningEnded   // closes reasoning

	ToolCall // the model invokes a tool, carrying CallID, ToolName and the complete Input

	ToolInputStarted // opens the tool input, carrying CallID
	ToolInputDelta   // fragment of the input JSON, carrying CallID and Input
	ToolInputEnded   // closes the tool input, carrying CallID
)

// Event is one event of a turn stream. Kind decides which fields are relevant;
// the rest stay zero. Input is the raw JSON of a tool input: a consumer parses
// it with json.Unmarshal, never by string matching, because the same model
// escapes the same JSON differently between turns. Usage comes only in
// StepEnded.
type Event struct {
	Kind     EventKind
	CallID   string          // ToolCall / ToolInput*
	ToolName string          // ToolCall
	Input    json.RawMessage // ToolCall / ToolInputDelta: raw input JSON
	Text     string          // TextDelta / ReasoningDelta
	Usage    *Usage          // StepEnded only
	Err      error
	// ProviderExecuted marks a ToolCall the provider ran itself: the host does
	// NOT settle it locally, it only persists it.
	ProviderExecuted bool
}

// Usage are the tokens reported when the turn closes (StepEnded).
type Usage struct {
	InputTokens      int
	OutputTokens     int
	ReasoningTokens  int
	CacheReadTokens  int
	CacheWriteTokens int
	// CacheableInputTokens is the provider-normalized logical input used as the
	// cache hit-rate denominator. It includes cached input exactly once.
	CacheableInputTokens int
}
