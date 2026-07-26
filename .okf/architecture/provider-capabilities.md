---
updated_at: 2026-07-25
summary: What an adapter declares about itself beyond streaming a turn — the optional Capabilities interface that replaced the global model-to-context-window table, and why the registry describes a format without building it.
---

# Provider capabilities

> Status: implemented 2026-07-25 (audit recommendation R3.2).
> See the [agnosticism and extensibility audit](../audits/2026-07-24-agnostic-extensibility-audit.md) §3.2 and §4 R3.
> Builds on the [provider registry](provider-registry.md) (R3.1).

## The problem it solves

`Provider` had one method, so everything a host needed to know about an adapter
beyond "stream this turn" had to be known some other way. In practice that meant
a table in core:

```go
// internal/llm/context.go — deleted by this change
var contextWindows = map[string]int{
    "claude-opus-4-8":           200_000,  // Anthropic's native id
    "anthropic/claude-opus-4.8": 200_000,  // OpenRouter's id for the same family
    "gpt-4o":                    128_000,  // OpenAI's id
    "openai/gpt-4o":             128_000,  // OpenRouter's id for it
    ...
}
```

Three things were wrong with it, and they compound:

- **It answered for adapters the caller was not talking to.** A single map keyed
  by model id had to hold both spellings of the same model and could not say
  which adapter either belonged to. A local endpoint serving something it called
  `gpt-4o` inherited OpenAI's 128K window.
- **It could not grow from outside.** R3.1 made adding a wire format one registry
  entry, but a `bedrock` factory registered by a third party had nowhere to put
  the windows of the models it serves. Its models were permanently unknown, which
  means no preventive compaction and an em dash in the model picker.
- **The same table existed three times.** `internal/llm/context.go`, the TUI's
  `curatedModelContext` (the OpenRouter free models the first one lacked) and
  `frontend/src/features/chat/contextWindow.ts`. The first two are now one
  declaration; the third is still there — see [Still open](#still-open).

Everything else the audit's §3.2 listed as "resolved at construction time and
then forgotten" had the same shape: whether the adapter asks for reasoning,
whether it reports its retries, which cache field it speaks, what output ceiling
it applies on its own. All decided by which options a constructor got, none of it
answerable afterwards.

## The shape

```go
// agentcore/llm
type Capabilities struct {
    Streaming              bool
    Tools                  bool
    Reasoning              bool
    Vision                 bool
    PromptCaching          PromptCaching  // none | implicit | keyed
    RetryTelemetry         bool
    DefaultMaxOutputTokens int
    ContextWindows         map[string]int
}

func (c Capabilities) ContextWindow(model string) (int, bool)

type Describing interface {
    Provider
    Capabilities() Capabilities
}

func CapabilitiesOf(p Provider) (Capabilities, bool)
```

The idiom is R2's, unchanged: an **optional interface discovered by type
assertion**, resolved through one function that returns `(value, answered)`.

- **Optional, so `Provider` stays a one-method contract.** The minimum to be an
  adapter is still to stream a turn. An adapter written against an endpoint whose
  catalog nobody has enumerated should not have to invent answers to be usable.
- **`CapabilitiesOf` is the only place the assertion happens**, because "said
  nothing" and "declared the zero value" must never flatten into each other. A
  provider that does not implement `Describing` has not said it cannot stream and
  serves no known model; it has said nothing, and the host keeps its own
  defaults. Every consumer here checks the bool before reading the struct.

## What a host does with each answer

The audit's field list is implemented in full, but only some of it is read today,
and the doc comments say which is which rather than leaving a reader to guess:

| Field | Read by | Effect |
|---|---|---|
| `ContextWindows` | `runner.contextCompactor`, TUI top bar, usage line, model picker, `/model` menu | preventive compaction threshold; every context label |
| `DefaultMaxOutputTokens` | `runner.contextCompactor` | reserved in the estimate when the request leaves `MaxOutputTokens` at zero |
| `Streaming`, `Tools`, `Reasoning`, `Vision`, `PromptCaching`, `RetryTelemetry` | nothing yet | declared facts; see below |

The six unread fields are deliberate and were the one real design argument here.
The rule R2 recorded — *a flag exists only where the host has a distinct reaction
to it* — would have cut the struct to two fields. It was overruled on purpose,
because these six are exactly the asymmetries §3.2 catalogued as invisible, and
a declaration is how they stop being invisible:

- `Reasoning` is false for the Anthropic adapter. That is not an oversight in the
  declaration, it is a fact about the adapter: it never sends the `thinking`
  parameter, so it never asks for reasoning, and the `ThinkingBlock` branch in
  its stream loop only fires if an endpoint volunteers one. Nobody could have
  read that off the code without going looking.
- `RetryTelemetry` is false for Anthropic and true for the OpenAI adapter. §3.2
  called this out as an observable asymmetry; now it is an observable *statement*.
- `Vision` is false everywhere, because `Message` has only `Text`. It is the flag
  R3.6's content-part seam flips, and declaring it false now is cheaper than
  adding a field to a published contract later.
- `PromptCaching` is the `compatibilityProfile` made explicit, which was R3.2's
  second stated goal. It answers the question a host actually has — *is
  `Request.SessionKey` worth anything here?* — rather than "does caching happen".
  `keyed` for OpenAI (`prompt_cache_key`) and OpenRouter (`session_id`),
  `implicit` for Anthropic (`cache_control` on every request, no key, no opt-out),
  `none` for the neutral dialect.

The cost of the six is that every implementer answers questions nobody reads yet.
The `llmtest` check keeps that cost from turning into a silent lie: it verifies
what it can (positive windows, non-negative ceiling, stable answers) rather than
pretending an unread field is verified.

## Windows are declared per dialect, not per model id

`internal/llm/capabilities.go` holds three tables — `anthropicWindows`,
`openAIWindows`, `openRouterWindows` — and the neutral dialect declares none.
That split is the point:

```go
openAI     := llm.DescribeOpenAI(llm.WithOpenAICompatibility())
openRouter := llm.DescribeOpenAI(llm.WithOpenRouterCompatibility())

openAI.ContextWindow("gpt-4o")            // 128_000, true
openAI.ContextWindow("openai/gpt-4o")     // 0, false — that is OpenRouter's spelling
openRouter.ContextWindow("openai/gpt-4o") // 128_000, true
openRouter.ContextWindow("claude-opus-4-8") // 0, false — that is Anthropic's
```

The neutral dialect declaring nothing is a real answer, not a gap: an
`openai-compatible` endpoint is LM Studio, Ollama, vLLM or an unrecognized
gateway, and there is no catalog to have an opinion about.

## The registry describes a format without building it

The [provider registry](provider-registry.md) doc predicted this: capabilities
are "what would let the registry describe a format rather than just build it".
`Registry` went from `map[string]Factory` to `map[string]Format`:

```go
type Format struct {
    Build    Factory   // constructs the live provider
    Describe Describer // what that provider would declare, without building it
}
```

Description has to be separate from construction because **the model picker
labels every model of every configured provider, and all but the selected one are
never built**. An answer that costs an SDK client, a credential lookup or a socket
is not one a render path can ask for.

Keeping the two halves honest is a property of how they are written, not a
convention: both close over the same `llm.Option` values.

```go
func openAIDialect(opts ...llm.Option) Format {
    return Format{
        Build:    func(def Provider, model, apiKey string) (llm.Provider, error) {
            return llm.NewOpenAIProvider(apiKey, def.BaseURL, model, opts...), nil
        },
        Describe: func(Provider) llm.Capabilities { return llm.DescribeOpenAI(opts...) },
    }
}
```

`llm.DescribeOpenAI(opts...)` applies those options to a zero `OpenAIProvider` and
returns its `Capabilities()` — the same code path the constructor runs, so a
format's description cannot drift from the provider that format builds. A test
asserts that equality for all three dialects.

`Describe` is `nil`-able, and a format registered with only a `Build` declares
nothing. That is the same silence as a provider without `Describing`, read the
same way: unknown, not "no".

`Open`'s fifth parameter changed from `Factory` to `Registry` as a consequence. An
embedder that had a bare factory now names the format it speaks, which it had to
know anyway:

```go
registry := providerconfig.DefaultRegistry()
registry["bedrock"] = providerconfig.Format{Build: newBedrockProvider, Describe: describeBedrock}
providerconfig.Open(..., registry, ...)
```

## How each consumer reaches an answer

Two paths, because there are two different questions:

**"What is the window of the model I am running?"** → ask the running adapter.
`llm.ActiveCapabilities` unwraps `SwitchableProvider` through the existing
`Acquire` seam. `SwitchableProvider` deliberately does **not** implement
`Describing`: answering on behalf of a delegate that declared nothing would turn
"said nothing" into "declared the zero value", the exact flattening the contract
warns about. The compactor calls it directly; the TUI reaches it through
`Engine.ModelCapabilities`, resolved on every use rather than cached, for the same
reason `tools()` is — `/model` swaps the adapter and a copy would answer for the
one that is gone.

**"What is the window of every model I could pick?"** → ask the catalog.
`ProviderModels` carries a `Capabilities` filled by `Registry.Describe` at
snapshot time, so the picker and the `/model` menu label providers that were never
constructed.

Both paths resolve to the same declaration, so they cannot disagree.

## What this cost, and what it bought

Deleted: the global `contextWindows` table (28 entries, 4 unrelated dialects in
one map), `llm.ContextWindow`, and the TUI's `curatedModelContext` fallback with
the two-source lookup at each of its three call sites.

Gained: an adapter registered from outside answers for its own models, with the
same reach into compaction and the UI as the ones that ship here. Preventive
compaction also got a correctness fix on the way — a request that leaves
`MaxOutputTokens` at zero now reserves the ceiling the adapter applies on its
own, which for Anthropic is 8192 tokens the estimate used to ignore.

## Still open

- **The frontend keeps its own table.**
  `frontend/src/features/chat/contextWindow.ts` has a fourth copy of the windows,
  with a 200K default for anything it does not know. Closing it means the desktop
  app consuming `providerconfig` instead of `internal/wailsprovider`, which is
  R3.4; until then the Go side is one declaration and the Wails frontend is
  another.
- **Windows are declared, never discovered.** OpenRouter's `/models` returns
  `context_length` per model and `llm.ListModels` throws it away. Reading it would
  give hundreds of models a real window instead of an em dash, and it belongs with
  R3.3's data-driven catalog, where `ModelLister` stops returning bare ids.
- **Six declared fields have no reader.** `Streaming`, `Tools`, `Reasoning`,
  `Vision`, `PromptCaching` and `RetryTelemetry` are facts nothing acts on yet.
  The obvious first consumers are the model picker (a reasoning badge) and the
  turn UI (saying "no retry telemetry" rather than showing an unexplained pause).
- **No capability is negotiated, only declared.** A host cannot ask Anthropic to
  stop caching, or ask an OpenAI-dialect provider for reasoning at request time.
  That would be a `Request` field, not a `Capabilities` one.
