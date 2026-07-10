---
updated_at: 2026-07-09
summary: Design for integrating Claude as an LLM provider in Go.
---

# LLM Integration: Claude (Go)

Designed on 2026-06-19. Defines how Athena talks to the model. It is the
`internal/llm` layer that the loop (`agent-loop.md`) consumes via
`Provider.Stream`. Fills the gap that the loop took for granted: how
`Provider`/`Request`/`Event` maps to the real Anthropic API.

## Decisions

- **Default model**: `claude-opus-4-8` (Opus 4.8). Configurable.
- **SDK**: Go official, `github.com/anthropics/anthropic-sdk-go`. Not raw HTTP.
- **Manual loop, not the SDK tool runner**: Atenea's durable runner runs
 the tools and persists state. We use direct `Messages.NewStreaming`, one
 call per turn, and set the tools ourselves. The `BetaToolRunner` of the SDK
 hides exactly the loop that we want to control.
- **Streaming always**: the turn can be long; streaming avoids HTTP timeouts and
 feeds `Text.*`/`Tool.*` events to the UI.

> Why SDK and not HTTP: the SDK brings retries with backoff (429/5xx), typed
> error types, stream accumulation and message types. Reimplementing that
> by hand is wasted work.

## Client and configuration

```go
// internal/llm/anthropic.go
import (
    "github.com/anthropics/anthropic-sdk-go"
    "github.com/anthropics/anthropic-sdk-go/option"
)

// Por defecto lee ANTHROPIC_API_KEY del entorno.
client := anthropic.NewClient()

// O explicita:
client := anthropic.NewClient(option.WithAPIKey(key))
```

Atenea minimum config (see future config/secrets doc):

| Key | Default | Notes |
| --- | --- | --- |
| `model` | `claude-opus-4-8` | constant `anthropic.ModelClaudeOpus4_8` |
| `max_tokens` | `64000` | streaming; Opus 4.8 supports up to 128K |
| `effort` | `high` | `high`/`xhigh` for agentic work (see Thinking) |
| APIkey | env `ANTHROPIC_API_KEY` | never on disk/prompt |

## One turn = one streaming call

The loop contract is: `Provider.Stream(ctx, req)` produces **one** turn and closes
the channel. With the SDK that is exactly a call to `Messages.NewStreaming`.
The adapter translates the neutral `llm.Request` to `MessageNewParams`, consumes the
stream from the SDK and broadcasts our `llm.Event` on a channel.

```go
func (p *AnthropicProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
    out := make(chan llm.Event)

    params := anthropic.MessageNewParams{
        Model:     anthropic.Model(req.Model),
        MaxTokens: req.MaxTokens,
        System:    toSystemBlocks(req.System), // []TextBlockParam, ver caching
        Messages:  toMessages(req.Messages),   // historial proyectado
        Tools:     toTools(req.Tools),          // schemas materializados
        Thinking:  anthropic.ThinkingConfigParamUnion{
            OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{},
        },
    }

    stream := p.client.Messages.NewStreaming(ctx, params)
    go func() {
        defer close(out)
        msg := anthropic.Message{} // acumulador para el mensaje final
        for stream.Next() {
            ev := stream.Current()
            msg.Accumulate(ev)           // reconstruye el assistant message
            emit(out, mapEvent(ev))      // traduce a llm.Event (abajo)
        }
        if err := stream.Err(); err != nil {
            emit(out, llm.ErrorEvent(err))
            return
        }
        // msg ya tiene Content + StopReason + Usage para persistir e historiar.
        emit(out, llm.StepFinished(msg))
    }()
    return out, nil
}
```

`msg.Accumulate(ev)` is key: it rebuilds the entire `assistant message`
(text + blocks `tool_use` + `thinking`) that the runner persists and sends
 again as history on the next turn. We do not re-parse the stream by hand.

### Mapping SDK events to `llm.Event`

The SDK emits block (start/delta/stop) and message (delta/stop) events. It is
type-switched with `.AsAny()`. The only thing that is 100% documented is the text delta;
the others follow the same pattern and it is advisable to confirm them against the SDK types.

```go
func mapEvent(ev anthropic.MessageStreamEventUnion) llm.Event {
    switch e := ev.AsAny().(type) {
    case anthropic.ContentBlockDeltaEvent:
        switch d := e.Delta.AsAny().(type) {
        case anthropic.TextDelta:
            return llm.TextDelta(d.Text)
        // ThinkingDelta    -> llm.ReasoningDelta(...)   (verificar nombre en SDK)
        // InputJSONDelta   -> llm.ToolInputDelta(...)   (verificar nombre en SDK)
        }
    // ContentBlockStartEvent -> Text/Reasoning/Tool.Input Started o ToolCall
    // ContentBlockStopEvent  -> Text/Reasoning/Tool.Input Ended
    // MessageDeltaEvent      -> stop_reason + usage parcial
    // MessageStopEvent       -> Step.Ended
    }
    return llm.Ignore
}
```

Correspondence to loop session events:

| Anthropic block/event | `llm.Event` | Session event |
| --- | --- | --- |
| content_block_start (text) | TextStarted | `Text.Started` |
| content_block_delta (TextDelta) | TextDelta | `Text.Delta` |
| content_block_stop (text) | TextEnded | `Text.Ended` |
| content_block_start (thinking) | ReasoningStarted | `Reasoning.Started` |
| content_block_delta (ThinkingDelta) | ReasoningDelta | `Reasoning.Delta` |
| content_block_start (tool_use) | ToolCall | `Tool.Called` |
| content_block_delta (InputJSONDelta) | ToolInputDelta | `Tool.Input.Delta` |
| message_delta (stop_reason, usage) | — | `Step` tokens |
| message_stop | StepFinished | `Step.Ended` |

## Tool use: how it fits with the durable loop

Athena does **not** let the SDK run tools. Flow per shift:

1. The stream brings `tool_use` blocks (name + JSON input for `callID`).
2. When closing the stream, `msg` has `StopReason == StopReasonToolUse` and the blocks
 `tool_use`.
3. The runner runs the tools locally (`settle`, with `errgroup`) and publishes
 `Tool.Success`/`Tool.Failed`.
4. On the **next** turn, the history includes:
 - the `assistant message` with the `tool_use` (`msg.ToParam()`), and
 - a `user message` with a `tool_result` for each `callID`.

```go
// Construir el assistant turn previo para el historial:
messages = append(messages, msg.ToParam())

// Por cada tool ejecutada, un tool_result (el id es el del bloque tool_use):
results := []anthropic.ContentBlockParamUnion{
    anthropic.NewToolResultBlock(callID, resultText, isError),
}
messages = append(messages, anthropic.NewUserMessage(results...))
```

This is the SDK's "manual agentic loop", but governed by our `Run`/`runTurn`
instead of a helper. `StopReasonToolUse` is the continuation signal from the
provider side; the loop really decides (see `needsContinuation`).

Definition of tools (from `ToolRegistry.Materialize`):

```go
addTool := anthropic.ToolParam{
    Name:        "read",
    Description: anthropic.String("Lee un archivo..."),
    InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{ /* ... */ }},
}
tools := []anthropic.ToolUnionParam{{OfTool: &addTool}}
```

The input of each `tool_use` is parsed with `json.Unmarshal` over the raw JSON
(`variant.JSON.Input.Raw()`) — never string match over the serialized JSON:
Opus 4.8 can escape Unicode/slashes differently.

## Thinking and effort

- **Adaptive only** in Opus 4.8: `Thinking: {OfAdaptive: &ThinkingConfigAdaptiveParam{}}`.
- `thinking: {type:"enabled", budget_tokens:N}` gives **400**. Neither does `temperature`,
 `top_p`, `top_k` (400). It is steered by prompt/effort, not by sampling.
- **Thinking content omitted by default** in Opus 4.8: the block arrives empty
 unless `display: "summarized"` is requested. If the UI shows reasoning, you should
 opt for summarized (map to `Reasoning.Delta`); if not, leave it omitted.
- **Effort**: `high`/`xhigh` for agentic/coding work. It goes in the output config
 of the request; **check the exact binding in `anthropic-sdk-go`** before setting
 the field (do not invent it). Default `high`.

## Prompt caching

The render order is `tools -> system -> messages`. Keep the stable first
(agent prompt + context baseline), the volatile last. A breakpoint in
the last block of system caches tools + system together.

```go
System: []anthropic.TextBlockParam{{
    Text:         systemPromptEstable,
    CacheControl: anthropic.NewCacheControlEphemeralParam(),
}},
```

- The loop already helps: the request is rebuilt from a durable state each turn, so
 with a frozen system prompt the prefix remains byte-identical.
- Fits with the `prompt cache key` that the loop mentions for the turn.
- Check hits: `resp.Usage.CacheReadInputTokens` / `CacheCreationInputTokens`.
 If `CacheReadInputTokens` is 0 between turns identical, there is a silent invalidator
 (date/uuid in system, reordered tools, non-deterministic JSON).

Invariant: do not put `time.Now()`, ids per request, or tools that change order in
the prefix. Sort tools deterministically (by name).

## Live steering (mid-session system)

The `DeliverySteer` of the loop matches the **mid-session system messages** (beta
`mid-conversation-system-2026-04-07`): instead of editing the system top-level (which
invalidates the cache), a `role: "system"` message is added to the end of `messages`.
It is the non-forgeable operator channel. Check the binding in the Go SDK before
using it; if the model does not support it, fallback to a `<system-reminder>` in the user turn
.

## Compaction

The loop has `ContinueAfterOverflowCompaction`. On Claude's side, the compaction
server-side (beta `compact-2026-01-12`) lives on `client.Beta.Messages.New` with
`ContextManagement`:

```go
params := anthropic.BetaMessageNewParams{
    Model:     anthropic.ModelClaudeOpus4_8,
    MaxTokens: 64000,
    Betas:     []anthropic.AnthropicBeta{"compact-2026-01-12"},
    ContextManagement: anthropic.BetaContextManagementConfigParam{
        Edits: []anthropic.BetaContextManagementConfigEditUnionParam{
            {OfCompact20260112: &anthropic.BetaCompact20260112EditParam{}},
        },
    },
    Messages: /* ... */,
}
```

Critical: persist **all** `resp.Content` (not just the text). The
compaction blocks are reused in the next request; If only the text is saved, the compaction state is lost. This reinforces why the runner history the entire
 message (`msg`), not a string.

## Errors, retries and stop reasons

- The SDK retries 429/5xx with backoff (default `max_retries=2`). To classify,
 use the SDK error type (`.Type()`), no match of string.
- `StopReason` to handle in `runTurn`:
 - `StopReasonToolUse` -> there are local tool calls -> continuation.
 - `StopReasonEndTurn` -> closed turn (except pending steer).
 - `StopReasonRefusal` -> there `StopDetails` (category/explanation); do not retry
    con el mismo prompt.
- `StopReasonMaxTokens` -> upload `max_tokens` or resume.
 - `model_context_window_exceeded` -> loop compaction/overflow path.

## What NOT to use

- `BetaToolRunner` from the SDK: hides the loop that the durable runner needs to control.
- `budget_tokens`, `temperature`, `top_p`, `top_k`: 400 in Opus 4.8.
- Prefills from the last turn assistant: 400. To force formatting, use structured
 outputs (`output_config.format`), no prefill.
- `max_tokens` low for no reason: truncates halfway. Default 64000 in streaming.

## Mapping to the roadmap milestones

- **M2** (Provider + fake): the fake imitates the `Stream` channel. The actual adapter
 of this section goes into **M10**, behind the same interface.
- **M3** (Publisher): consumes the `llm.Event` mapping from here.
- **M5** (one turn): `Messages.NewStreaming` once + accumulate + seat tools.
- **M7** (control): `model_context_window_exceeded` + compaction.

## Sources

- Go SDK: https://github.com/anthropics/anthropic-sdk-go
- API reference used for this doc: skill `claude-api` (cache 2026-05-26):
 models, streaming, tool use, thinking adaptive, prompt caching, compaction,
 error codes.
- Loop that consumes this layer: `agent-loop.md`
- Roadmap: `../plans/agent-loop-roadmap.md`
- Way of working: `AGENTS.md`
