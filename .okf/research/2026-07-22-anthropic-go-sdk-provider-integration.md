---
updated_at: 2026-07-22
summary: Primary-source research for integrating Anthropic's official Go SDK as a native Atenea LLM provider.
---

# Anthropic Go SDK provider integration

Research checked against first-party sources on **2026-07-22**. Local files
reviewed: `go.mod`, `internal/llm/anthropic.go`, `internal/llm/provider.go`, and
the built-in provider catalog in `cmd/atenea/main.go`.

## Executive verdict

Atenea already implements Anthropic as a native `anthropic` provider backed by
`github.com/anthropics/anthropic-sdk-go` and the Messages API. This is the right
architecture; an OpenAI-compatible translation is neither necessary nor a
better fit for Anthropic's block protocol.

The SDK dependency is current: `go.mod` pins **v1.59.0**, which GitHub marks as
the latest stable (non-prerelease) release, published **2026-07-22 at
16:42:55Z**. Its own `go.mod` requires Go 1.24 and Atenea declares Go 1.25, so
the toolchain requirement is satisfied. [GitHub latest-release API]
[v1.59.0 release][sdk-release] [SDK go.mod][sdk-go-mod]

The model catalog is not current. Atenea defaults to
`claude-sonnet-4-5-20250929` and also advertises
`claude-opus-4-1-20250805`; the latter is officially deprecated and retires on
2026-08-05. Anthropic's current guidance says to start with **Claude Opus 4.8**
for complex agentic coding and enterprise work, and to use **Claude Fable 5**
when the highest available capability is required. **Claude Sonnet 5** is also
in the current model table and is the natural balanced tier. The built-in
catalog should therefore move to current native IDs and drop the deprecated
Opus 4.1 entry. [Model overview][models-overview] [SDK model constants]

The core request/stream adapter is broadly correct: explicit API-key auth,
top-level system blocks, required positive `max_tokens`, native tool schemas,
block-indexed streaming, fragmented JSON accumulation, and separate cache
usage fields all match the SDK/API. It nevertheless has material gaps before
it can be called complete: error classification is absent, tool-result errors
cannot be represented, ordered/interleaved assistant blocks cannot be round
tripped, thinking signatures are discarded, stream retry/partial-output
semantics are not made explicit, and model discovery is implemented for key
validation but disabled for the user-facing catalog.

The existing `llm.Provider` boundary is sufficient: `Stream(context.Context,
llm.Request)` maps naturally to `client.Messages.NewStreaming(ctx,
anthropic.MessageNewParams{...})`. A dedicated adapter is still necessary
because Anthropic messages use content blocks, a top-level system prompt, and
`tool_use`/`tool_result` blocks rather than OpenAI chat-completion shapes.
[Messages API reference][messages-api] [official streaming example][sdk-stream]
[official tool streaming example][sdk-tool-stream]

## Client, authentication, and endpoint

Construct the client with an explicit key from Atenea's credential store:

```go
client := anthropic.NewClient(option.WithAPIKey(apiKey))
```

`option.WithAPIKey` sends `X-Api-Key`; when no explicit option is supplied, the
SDK's default credential chain first checks `ANTHROPIC_API_KEY`. Explicitly
passing the credential is preferable in Atenea because provider changes happen
at runtime and the application already owns credential lookup. [SDK getting
started][sdk-readme] [SDK authentication implementation][sdk-client]
[WithAPIKey implementation][sdk-options]

The normal API base is managed by the SDK. `ANTHROPIC_BASE_URL` or
`option.WithBaseURL` can override it, but a first-party Anthropic provider
should not expose an arbitrary base URL unless proxy support is an intentional
product requirement. [SDK default client options][sdk-client]

## Request conversion

Build `anthropic.MessageNewParams` as follows:

| Atenea | Anthropic SDK |
| --- | --- |
| `Request.Model` | `anthropic.Model(req.Model)` |
| `Request.MaxOutputTokens` | `MaxTokens` (required; apply a positive provider default when Atenea sends zero) |
| `Request.System` | `System: []anthropic.TextBlockParam{{Text: req.System}}` |
| user text | `anthropic.NewUserMessage(anthropic.NewTextBlock(text))` |
| assistant text/tool calls | one assistant message containing text and `ToolUseBlockParam` blocks |
| tool result | a user message containing `anthropic.NewToolResultBlock(toolCallID, text, isError)` |
| `Request.Tools` | `[]anthropic.ToolUnionParam`, each wrapping `ToolParam{Name, Description, InputSchema}` |

`max_tokens` is required and is an absolute output ceiling. The Messages API
accepts only `user` and `assistant` message roles; system instructions belong
in the top-level `system` field. Consecutive messages of the same role are
combined by the API, but the adapter should preserve Atenea's block ordering,
especially assistant `tool_use` blocks and the following user `tool_result`
blocks. [Message request reference][messages-api] [generated request types][sdk-message]

Anthropic tool schemas are JSON Schema objects. Atenea stores each schema as
raw JSON, so the adapter must decode it into the SDK's `ToolInputSchemaParam`
(or its object fields) and fail before making the request if it is invalid; it
must not turn the schema JSON into a quoted string. Descriptions are optional
but strongly recommended by Anthropic. [Tool use overview][tool-use]
[generated tool parameter documentation][sdk-message]

For history, preserve every assistant response as a single ordered block list.
An assistant turn may contain both text and multiple parallel `tool_use`
blocks. Return all corresponding `tool_result` blocks in the immediately
following user message; each result's `tool_use_id` must match its call. The
official SDK example follows exactly this loop by appending
`message.ToParam()`, then a user message containing results. [Official tool
example][sdk-tools] [tool-result protocol][tool-use]

## Streaming conversion

Call `NewStreaming` with the caller's context, iterate with `stream.Next()`, and
always inspect `stream.Err()` after iteration. Anthropic streams SSE events in
the order `message_start`, content-block start/deltas/stop,
`message_delta`, and `message_stop`; the API may also send ping and error
events. The SDK exposes typed message/content events and reports terminal
stream failures through `stream.Err()`. [Streaming Messages guide][streaming]
[official SDK streaming example][sdk-stream] [SDK message service][sdk-message]

Recommended Atenea mapping:

| Anthropic event/block | Atenea event |
| --- | --- |
| first accepted stream / `MessageStartEvent` | `StepStarted` |
| text `ContentBlockStartEvent` | `TextStarted` |
| `TextDelta.Text` | `TextDelta` |
| text `ContentBlockStopEvent` | `TextEnded` |
| tool-use `ContentBlockStartEvent` | `ToolInputStarted` with block ID; retain ID/name by block index |
| `InputJSONDelta.PartialJSON` | `ToolInputDelta` with the raw fragment |
| tool-use `ContentBlockStopEvent` | `ToolInputEnded`, then `ToolCall` with the complete accumulated raw JSON |
| `MessageDeltaEvent.Usage` + `MessageStopEvent` | `StepEnded` with usage |
| `stream.Err()` | `StepFailed` unless cancellation is handled by the caller's interruption path |

Tool input JSON is deliberately streamed as partial string fragments and may
split anywhere; individual deltas are not valid JSON. Use `Message.Accumulate`
for the authoritative final message (the official example does this on every
event), while independently forwarding fragments for Atenea's live tool-input
UI. Emit `ToolCall` only after the block closes and the accumulated input is
valid raw JSON. [Streaming input JSON deltas][streaming] [official accumulated
tool-stream example][sdk-tool-stream]

Track content blocks by the event's `Index`, not merely “current block”:
Anthropic assigns an index to each start/delta/stop event, and a response can
contain several text and tool blocks. This also makes parallel tool calls safe.
[Streaming event schema][streaming] [generated stream event types][sdk-message]

The final usage is carried by `message_delta`; Anthropic defines total input as
`input_tokens + cache_creation_input_tokens + cache_read_input_tokens`.
For Atenea, map the base `input_tokens`, `output_tokens`, cache-read, and
cache-creation values into their separate `llm.Usage` fields rather than
double-counting them in `InputTokens`. [Generated `MessageDeltaEvent`
documentation][sdk-message]

## Errors, retries, and cancellation

API failures can be inspected with `errors.As(err, *anthropic.Error)`. The
typed error exposes `StatusCode`, `RequestID`, and `Type()` (for example
`rate_limit_error` or `overloaded_error`); retain the request ID in logs and
user-facing diagnostics because Anthropic documents it as the support
correlation identifier. Network/context errors are not wrapped as
`anthropic.Error`, so preserve `errors.Is(err, context.Canceled)` and
`context.DeadlineExceeded`. [SDK API error type][sdk-error] [Anthropic error
guide][errors]

Anthropic documents the principal HTTP errors as 400 invalid request, 401
authentication, 403 permission, 404 not found, 413 request too large, 429 rate
limit, 500 API error, and 529 overloaded. Do not classify every 400 as context
overflow: inspect the structured error type/message and only translate a real
context-window failure to Atenea's `ContextOverflowError`. [Anthropic error
guide][errors]

The SDK defaults to two retries and provides `option.WithMaxRetries`. Its retry
implementation is useful for failures before a response is consumed, but an
agent stream cannot safely be replayed after Atenea has published partial text
without deduplication. Start with the SDK default, expose retry status only when
the SDK/provider supplies an observable retry, and never add a second blind
retry loop around the entire streamed turn. [SDK retry option][sdk-options]
[SDK retry default][sdk-request-config]

Pass Atenea's `ctx` directly to `NewStreaming`; cancellation then aborts the
HTTP request/stream through Go's request context. The adapter goroutine must
close its output channel on every path, check `ctx.Err()` when the stream ends,
and avoid converting intentional cancellation into a provider failure event.
[SDK streaming signature][sdk-message] [Go request-context contract][go-request]

## Models

Do not derive a permanent catalog solely from SDK constants. Anthropic exposes
a Models API and the SDK provides a `Models` service; use it for discovery (with
pagination), while keeping a small tested default. Model aliases can move to a
new snapshot, whereas dated model IDs give reproducible behavior. [Models API
reference][models-api] [model lifecycle and aliases][models-overview]
[SDK client services][sdk-client]

At implementation time, choose the default from Anthropic's live model
overview and verify it supports the required tool-use and streaming behavior.
Avoid copying Atenea's existing OpenRouter-prefixed IDs such as
`anthropic/...`: the native API consumes Anthropic model IDs such as the aliases
and snapshots published in the official model table. [Model overview][models-overview]

### Current first-party model guidance (2026-07-22)

| Tier | Native API ID | Official positioning / Atenea implication |
| --- | --- | --- |
| Highest capability | `claude-fable-5` | Anthropic calls it its most capable widely released model and recommends it when the highest available capability is needed. Make available, but do not silently choose the most expensive tier for every task. |
| Complex agentic/coding default | `claude-opus-4-8` | Anthropic explicitly says to start here for complex agentic coding and enterprise work. This is the strongest evidence-backed default for Atenea's coding-agent use case. |
| Balanced current tier | `claude-sonnet-5` | Listed among the latest models; suitable as the lower-cost/current balanced option if product policy favors efficiency over the official coding recommendation. |
| Fastest current tier | `claude-haiku-4-5` / `claude-haiku-4-5-20251001` | Current Haiku tier. A dated ID is appropriate when reproducibility matters. |

Aliases such as `claude-opus-4-8` can move, while dated IDs are stable
snapshots. Atenea should make that product tradeoff explicit: an alias tracks
Anthropic improvements; a snapshot maximizes reproducibility. The SDK's model
type is a string alias, so generated constants are helpful but do not restrict
the API to only the constants shipped in a particular SDK version. [Model
overview][models-overview] [SDK model constants]

The local catalog observed in `cmd/atenea/main.go` is:

```text
default: claude-sonnet-4-5-20250929
catalog: claude-sonnet-4-5-20250929
         claude-opus-4-1-20250805   (deprecated; retirement 2026-08-05)
         claude-haiku-4-5-20251001
```

This explains the reported stale-model behavior independently of the SDK
version: upgrading a client library does not update an application-owned,
hard-coded model catalog.

## Assessment of the current Atenea adapter

### Correct or well aligned

- `option.WithAPIKey` and optional `WithBaseURL` are idiomatic SDK use.
- `ValidateAnthropicKey` uses the native Models API rather than an
  OpenAI-compatible endpoint.
- `MessageNewParams` correctly maps model, required `MaxTokens`, messages,
  tools, and the top-level system instruction.
- user/assistant/tool history maps to Anthropic's user/assistant roles and
  `tool_use`/`tool_result` content blocks.
- tool schemas are decoded as JSON objects rather than serialized as strings.
- stream blocks are tracked by `Index`, allowing multiple tool blocks, and raw
  `input_json_delta` fragments are accumulated before `ToolCall` emission.
- base input/output and cache read/write tokens are kept separate rather than
  double-counted.
- `stream.Err()` is checked and the caller's context is passed to
  `NewStreaming`.

### Gaps and risks

1. **Current models are not exposed.** `DisableModelDiscovery: true` makes the
   stale hard-coded list authoritative even though the provider already knows
   how to call `Models.List`.
2. **No structured error translation.** `runStream` emits the raw SDK error;
   it does not retain/classify `anthropic.Error` status, type, and request ID,
   nor map a verified context-window error to Atenea's context-overflow type.
3. **Tool failures are always reported as success.** The conversion always
   calls `NewToolResultBlock(..., false)`; `llm.Message` has no tool-result
   error bit.
4. **Content ordering is lossy.** `llm.Message` stores one text field followed
   by a separate tool-call slice. It cannot reproduce an assistant response
   whose text, thinking, and tool blocks are interleaved in another order.
5. **Thinking continuity is lossy.** The stream forwards `ThinkingDelta` to
   the UI but ignores `SignatureDelta` and history has no thinking/signature
   blocks. Anthropic requires thinking blocks to be passed back unchanged in
   multi-turn tool-use conversations; modified blocks can cause a 400. This
   matters if adaptive/extended thinking is enabled now or later. [Extended
   thinking guide][extended-thinking]
6. **Tool JSON is not validated at block close.** Concatenation is correct,
   but the adapter emits a `ToolCall` without a final `json.Valid`/decode
   check. A malformed or incomplete stream becomes a downstream tool error
   instead of a provider/protocol error.
7. **Cancellation is not distinguished from failure.** A context error from
   `stream.Err()` is emitted as `StepFailed`; intentional interruption should
   follow Atenea's cancellation contract.
8. **Retries during streaming need an explicit policy.** The SDK has retry
   behavior, while Atenea publishes deltas immediately. Retrying an entire
   response after visible partial output would require deduplication; do not
   add a second blind retry loop.
9. **A hard 60-second request timeout may truncate valid long agent turns.**
   Atenea passes `defaultRequestTimeout` through `WithRequestTimeout`; this
   option covers the request. Its value should be evaluated against long
   thinking/tool responses and the caller's own cancellation/deadline policy.

## Atenea-specific implementation checklist

1. Refresh the built-in model default/catalog from the current official table;
   remove deprecated Opus 4.1 and decide explicitly between moving aliases and
   reproducible snapshots.
2. Decide whether to enable native Models API discovery (with pagination) or
   maintain a regularly reviewed curated catalog.
3. Add structured conversion helpers for Anthropic errors and cancellation.
4. Preserve mixed assistant content. Atenea's current `llm.Message` has separate
   `Text` and `ToolCalls`, which is adequate only if it preserves the protocol's
   required order; add ordered content parts if E2E fixtures reveal interleaved
   blocks.
5. Accumulate SDK messages and simultaneously emit live deltas. Index tool
   blocks and support multiple calls in one assistant response.
6. Use the Models API for the selector or a documented curated snapshot; do not
   call the existing OpenAI-compatible `/models` helper for Anthropic.
7. E2E-test: plain streaming text, system instruction, one tool, parallel
   tools, failed tool result (`is_error: true`), fragmented JSON, cancellation,
   invalid key, rate limit/overload, context overflow, and usage mapping.

## Sources

- Anthropic's API/platform documentation: Messages, streaming, tools, errors,
  models, and model lifecycle.
- Anthropic's official Go SDK repository at release v1.59.0: README, generated
  request/event types, examples, authentication, error, and retry behavior.
- Go's standard-library request-context documentation for cancellation.

[sdk-readme]: https://github.com/anthropics/anthropic-sdk-go/blob/v1.59.0/README.md
[sdk-release]: https://github.com/anthropics/anthropic-sdk-go/releases/tag/v1.59.0
[github-latest-release-api]: https://api.github.com/repos/anthropics/anthropic-sdk-go/releases/latest
[sdk-go-mod]: https://github.com/anthropics/anthropic-sdk-go/blob/v1.59.0/go.mod
[sdk-model-constants]: https://github.com/anthropics/anthropic-sdk-go/blob/v1.59.0/message.go#L4282-L4317
[sdk-client]: https://github.com/anthropics/anthropic-sdk-go/blob/v1.59.0/client.go
[sdk-message]: https://github.com/anthropics/anthropic-sdk-go/blob/v1.59.0/message.go
[sdk-options]: https://github.com/anthropics/anthropic-sdk-go/blob/v1.59.0/option/requestoption.go
[sdk-request-config]: https://github.com/anthropics/anthropic-sdk-go/blob/v1.59.0/internal/requestconfig/requestconfig.go
[sdk-error]: https://github.com/anthropics/anthropic-sdk-go/blob/v1.59.0/internal/apierror/apierror.go
[sdk-stream]: https://github.com/anthropics/anthropic-sdk-go/blob/v1.59.0/examples/message-streaming/main.go
[sdk-tools]: https://github.com/anthropics/anthropic-sdk-go/blob/v1.59.0/examples/tools/main.go
[sdk-tool-stream]: https://github.com/anthropics/anthropic-sdk-go/blob/v1.59.0/examples/tools-streaming/main.go
[messages-api]: https://platform.claude.com/docs/en/api/messages
[streaming]: https://platform.claude.com/docs/en/build-with-claude/streaming
[tool-use]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview
[errors]: https://platform.claude.com/docs/en/api/errors
[models-api]: https://platform.claude.com/docs/en/api/models
[models-overview]: https://platform.claude.com/docs/en/about-claude/models/overview
[extended-thinking]: https://platform.claude.com/docs/en/build-with-claude/extended-thinking
[go-request]: https://pkg.go.dev/net/http#NewRequestWithContext
