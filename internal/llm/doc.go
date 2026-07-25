// Package llm is the private side of the provider boundary: the adapters that
// speak a real wire format, the scriptable FakeProvider used by the tests, the
// model catalog lookup and the context-window accounting that feeds compaction.
//
// The contract itself — Provider, Request, Message, ToolDef, Event, Usage — is
// published in agentcore/llm and re-exported here by contract.go, so a caller
// spells every type llm.X regardless of which side defines it.
//
// Adapters currently living here: OpenAIProvider, which talks to an
// OpenAI-compatible endpoint over SSE streaming (also used against OpenRouter),
// and AnthropicProvider. Both translate one turn into llm.Event (StepStarted,
// ReasoningStarted/Delta/Ended, TextStarted/Delta/Ended, ToolInputStarted/Delta/
// Ended for the argument fragments plus a ToolCall per accumulated call,
// StepEnded with Usage, StepFailed on stream error). SwitchableProvider wraps
// whichever one is active so a model change does not have to rebuild the loop.
package llm
