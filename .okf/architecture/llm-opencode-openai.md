---
updated_at: 2026-07-09
summary: Design for testing LLM integration through OpenCode and the OpenAI SDK.
---

# LLM integration for testing: OpenCode Go via OpenAI SDK (Go)

Designed on 2026-06-19. For the **first tests** Atenea uses the
**OpenCode Go** subscription (OpenCode Zen gateway, OpenAI-compatible) instead of paying Anthropic
directly. It is another implementation of the same `Provider` from `agent-loop.md`,
so the loop does not change: just another adapter is plugged in.

Claude's adapter (`llm-claude.md`) remains the final destination
(M10). This is the cheap way to start (M2/M5).

## What is OpenCode Zen / OpenCode Go

- **OpenCode Zen**: gateway of curated models from the OpenCode team, with an API
 **OpenAI-compatible**.
- **OpenCode Go**: the low-cost subscription plan on that gateway (access to
 tested coding models). This is what is used here.
- Base endpoint: `https://opencode.ai/zen/v1`
 (chat completions in `https://opencode.ai/zen/v1/chat/completions`).
- As it is OpenAI-compatible, the **OpenAI SDK** pointed to that base URL is used.

## Configuration

| Key | Value | Notes |
| --- | --- | --- |
| URL base | `https://opencode.ai/zen/v1` | Zen gateway |
| APIkey | from `opencode.ai/auth` | header `Authorization: Bearer` |
| env var | `OPENCODE_API_KEY` | our name; confirm the one you prefer |
| model id | **to be confirmed** | see "Values ​​to be confirmed" |

Atenea chooses the provider by config: `provider = opencode` for testing,
`provider = anthropic` for production. Same interface, different adapter.

## Client (OpenAI SDK for Go)

```go
// internal/llm/opencode.go
import (
    "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
)

client := openai.NewClient(
    option.WithAPIKey(os.Getenv("OPENCODE_API_KEY")),
    option.WithBaseURL("https://opencode.ai/zen/v1"),
)
```

`WithBaseURL` is the only thing that changes compared to using direct OpenAI: the SDK speaks
the same protocol against the OpenCode gateway.

> Confirm the import path / major version of the SDK (`github.com/openai/openai-go`
> publishes majors as `/v2`) before setting `go.mod`. Do not invent the version.

## One turn = one streaming call

Same as the loop contract: `Provider.Stream(ctx, req)` produces a turn and
closes the channel. With the OpenAI SDK that is `Chat.Completions.NewStreaming`,
consumed with `ChatCompletionAccumulator` (rebuilds the final message and the
tool calls from the stream).

```go
func (p *OpenCodeProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
    out := make(chan llm.Event)

    params := openai.ChatCompletionNewParams{
        Model:    req.Model,                 // model id del gateway (a confirmar)
        Messages: toOpenAIMessages(req),     // system + historial + tool results
        Tools:    toOpenAITools(req.Tools),  // schemas materializados
        // MaxTokens / MaxCompletionTokens segun el SDK
    }

    stream := p.client.Chat.Completions.NewStreaming(ctx, params)
    go func() {
        defer close(out)
        acc := openai.ChatCompletionAccumulator{}
        for stream.Next() {
            chunk := stream.Current()
            acc.AddChunk(chunk)

            // Texto incremental:
            if len(chunk.Choices) > 0 {
                if d := chunk.Choices[0].Delta.Content; d != "" {
                    emit(out, llm.TextDelta(d))
                }
            }
            // Tool call completa (el accumulator la junta a partir de los deltas):
            if tc, ok := acc.JustFinishedToolCall(); ok {
                emit(out, llm.ToolCall(tc.Index, tc.Name, tc.Arguments))
            }
            if _, ok := acc.JustFinishedContent(); ok {
                emit(out, llm.TextEnded())
            }
        }
        if err := stream.Err(); err != nil {
            emit(out, llm.ErrorEvent(err))
            return
        }
        emit(out, llm.StepFinished(acc.ChatCompletion)) // mensaje final + finish_reason
    }()
    return out, nil
}
```

`acc` fulfills the role that `msg.Accumulate` fulfills in Claude's adapter:
rebuilds the complete assistant message (text + `tool_calls`) that the runner
persists and history for the next turn.

### Mapping to `llm.Event`

| Stream OpenAI | `llm.Event` | Session event |
| --- | --- | --- |
| `Delta.Content` (text) | TextDelta | `Text.Delta` |
| `JustFinishedContent()` | TextEnded | `Text.Ended` |
| deltas of `tool_calls` → `JustFinishedToolCall()` | ToolCall | `Tool.Called` |
| `finish_reason` in the last chunk | StepFinished | `Step.Ended` |

`finish_reason`:

- `tool_calls` -> there were local tool calls -> continuation.
- `stop` -> closed turn (except pending steer).
- `length` -> equivalent to max_tokens (raise the limit or resume).

## Tool use OpenAI style (differs from Claude)

The flow in the loop is the same (the runner executes and settles), but the **form of
the messages** changes with respect to Anthropic:

- The assistant turn brings an array `tool_calls` (each with `id`, `function.name`,
 `function.arguments` as **string JSON**).
- The result of each tool does NOT go as `tool_result` within a user message.
 It goes as a separate message with `role: "tool"` and `tool_call_id` = the `id` of the call.

```go
// Siguiente turno: assistant con tool_calls + un mensaje role:"tool" por resultado.
messages = append(messages, assistantTurnConToolCalls) // del acc
messages = append(messages, openai.ToolMessage(resultText, toolCallID))
```

- The `arguments` are JSON generated by the model: **validate before executing**
 (invalid JSON or invented params may come). Parse with `json.Unmarshal`.
- Confirm the exact SDK constructors (`openai.ToolMessage`,
 `openai.UserMessage`, `openai.ChatCompletionToolParam`, etc.) before coding.

## Key differences vs the Claude adapter

| Theme | Claude (Anthropic) | OpenCode Go (OpenAI-compatible) |
| --- | --- | --- |
| tool result | `tool_result` in user message | message `role:"tool"` + `tool_call_id` |
| Tool call args | parsed JSON object | **string** JSON (validate) |
| Reasoning/thinking | `thinking` blocks (adaptive) | usually absent; depends on the model |
| Prompt caching | `cache_control` explicit | without breakpoint control (gateway cache) |
| Continuation signal | `StopReasonToolUse` | `finish_reason == "tool_calls"` |
| Steering | mid-session system (beta) | extra message `role:"system"`/`developer` |

Implication for Athena: the adapter is thin and insulated; the rest of the loop
(history, events, tools settle, continuation) does not notice the difference. That's why
starting with OpenCode Go and migrating to Claude later does not touch the runner.

## What NOT to expect in tests

- **No `cache_control`**: cache breakpoints are not controlled as in Claude.
- **No guaranteed thinking blocks**: many OpenAI-compatible models do not expose them
; `Reasoning.*` events may be empty for v1.
- **Model discovery**: OpenCode Zen may not expose `/v1/models`, so
 the model id is confirmed by hand (dashboard), not by API.

## Fit with the roadmap

- **M2** (Provider + fake): the fake imitates the channel; This OpenCode adapter is the
 first **real** and cheap provider to iterate with.
- **M5** (one turn): `NewStreaming` + `ChatCompletionAccumulator` + seat tools.
- **M10**: switch to Claude (`llm-claude.md`) behind the **same**
 interface, without touching the loop.

## Values ​​to confirm (do not invent)

1. **Model exact id** that the gateway accepts (e.g. `gpt-5.5`, `big-pickle`, ...).
 In the OpenCode config the format is `opencode/<id>`; in the raw API confirm
 if it is sent with or without `opencode/` prefix. Verify in the dashboard/account.
2. **Name of the env var** of the API key (here `OPENCODE_API_KEY`).
3. **Version/import path** of the `github.com/openai/openai-go` SDK.
4. **Exact constructors** of the SDK for messages/tools/params.
5. If the chosen model exposes reasoning, and `MaxTokens` vs `MaxCompletionTokens`.

## Sources

- OpenCode Zen: https://opencode.ai/../zen/
- OpenCode Providers: https://opencode.ai/../providers/
- OpenAI Go SDK: https://github.com/openai/openai-go
- Loop that consumes this layer: `agent-loop.md`
- Production adapter (Claude): `llm-claude.md`
- Way of working: `AGENTS.md`
