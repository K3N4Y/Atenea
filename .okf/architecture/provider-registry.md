---
updated_at: 2026-07-25
summary: How a provider's declared wire format resolves to the adapter that speaks it — the factory registry that replaced two closed switches, and why the dialect became the type.
---

# Provider registry

> Status: implemented 2026-07-25 (audit recommendation R3.1).
> See the [agnosticism and extensibility audit](../audits/2026-07-24-agnostic-extensibility-audit.md) §3.2 and §4 R3.

## The problem it solves

The provider *domain model* was already neutral — `Request`, `Message`, `Event`,
`Usage`, with each SDK type confined to its adapter. The wiring around it was
closed twice over:

```go
func defaultProviderFactory(def Provider, model, apiKey string) (llm.Provider, error) {
    if def.Type == Anthropic { ... }          // closed 2-value enum, enforced at config load
    opts := []llm.Option{llm.WithoutOpenRouterReasoning()}
    switch def.ID {                            // and the dialect keyed by *identity*
    case "openai":     opts = ...
    case "openrouter": opts = ...
    }
    return llm.NewOpenAIProvider(...)
}
```

Two different questions were being answered by two different switches, and
neither could be extended from outside:

- **Which adapter speaks this endpoint?** Decided by `Type`, whose only two legal
  values were enforced in `normalizeAndValidate`. A third wire format could not
  even be *declared* in `providers.json` — the file failed to load.
- **Which dialect of the OpenAI protocol?** Decided by the provider's `ID`. This
  is the same disease [tool capabilities](tool-capabilities.md) cured for tools:
  core deciding what an extension is by looking up its name. A new OpenAI-ish
  gateway (Azure, Groq, an enterprise proxy) could not get `prompt_cache_key`
  without an edit to this function.

## The shape

```go
type Factory func(def Provider, model, apiKey string) (llm.Provider, error)
type Registry map[string]Format

func DefaultRegistry() Registry
func (r Registry) Build(def Provider, model, apiKey string) (llm.Provider, error)
func (r Registry) Types() []string
```

> R3.2 widened the map's value from a bare `Factory` to a `Format` that also
> describes the wire format without building it. See
> [Provider capabilities](provider-capabilities.md#the-registry-describes-a-format-without-building-it).

A plain map, not a package-level table mutated from `init()`. Registration by
`init()` buys nothing here — there is one composition root and it can name what
it wants — while costing determinism, testability and import-order sanity.
Extending is an assignment:

```go
registry := providerconfig.DefaultRegistry()
registry["bedrock"] = newBedrockProvider
providerconfig.Open(..., registry.Build, ...)
```

`Open` already took an injectable factory, so nothing in its signature moved:
`registry.Build` *is* a `Factory`. What changed is that extending went from
replacing the whole factory to adding one entry, and `nil` now means the default
registry. (R3.2 later changed the parameter itself from `Factory` to `Registry`,
so that a format could be described as well as built.)

`DefaultRegistry()` returns a **fresh map on every call**, so extending one copy
can never reach another — the failure mode of a shared package-level registry.

## The type is the dialect

Four wire formats ship:

| Type | Adapter | What it means |
|---|---|---|
| `anthropic` | `llm.NewAnthropicProvider` | the native Messages API |
| `openai` | OpenAI adapter, `WithOpenAICompatibility` | official OpenAI: `prompt_cache_key`, no `reasoning` |
| `openrouter` | OpenAI adapter, `WithOpenRouterCompatibility` | OpenRouter routing: `session_id`, `reasoning` unless opted out |
| `openai-compatible` | OpenAI adapter, `WithoutOpenRouterReasoning` | the neutral dialect: chat completions, no vendor extension |

Keying the registry on `Type` alone would have left the `ID` switch alive inside
the `openai-compatible` factory — a registry with a switch in it. Promoting the
dialect to a type is what actually removes it: **the provider's identity now
decides nothing about how a request is shaped.** Two providers with the same id
and different types produce different request bodies, and that is what the
registry test asserts.

`openai` as a type and `openai` as a provider id are the same word for two
different things, which is fine and already true of `anthropic`. The id names
*who you are talking to*; the type names *what language you are speaking*. A
local LM Studio endpoint and an unrecognized gateway are both
`openai-compatible` under different ids; a corporate OpenAI proxy is `openai`
under its own id.

### Configs written before this

A `providers.json` written by an older build says `openai-compatible` for the
`openai` and `openrouter` entries, because back then every OpenAI-ish endpoint
declared that type. Reading it literally would silently drop OpenAI's
`prompt_cache_key` and OpenRouter's routing and reasoning fields — a downgrade
with no error, the worst kind.

`migrateLegacyDialect` reproduces the old id switch **exactly once, at the config
boundary**, so nothing downstream ever looks at an id again. It is bounded and
dated: the map knows only the two ids the default catalog ever shipped, it never
grows, and the first `/model` selection rewrites the file with the resolved type.
It can be deleted once configs have rotated.

The one case it cannot express is a provider deliberately named `openai` that
wants the neutral dialect. That was equally impossible before — the old switch
matched the same id — so nothing regressed, and renaming the entry resolves it.

## An unknown type is not a config error

`normalizeAndValidate` no longer checks the type against a list. It requires the
field to be *present*; the registry answers whether this build can speak it, and
only when the provider is actually built:

```
provider "x" has unsupported type "bedrock" (known types: anthropic, openai, openai-compatible, openrouter)
```

This is the precondition for the registry to be worth anything. `providers.json`
is shared between builds — one with a Bedrock factory registered, one without —
and rejecting the whole file over one entry would take every *other* provider
down with it, dropping the user to the environment fallback with none of their
configuration. Now the file loads, the speakable providers work, and only
selecting the unspeakable one fails, with an error that names what this build
does have.

The error naming its alternatives is not decoration. The point of a registry is
that the answer is data rather than code; an error that does not say which data
this build was given sends the reader to source that no longer holds the answer.

## What this cost, and what it bought

Adding a wire format was **7 files**, two of them `main` packages. It is now one
factory plus one registry entry — and for anything OpenAI-shaped, not even an
adapter: a new dialect is a `Factory` closure over existing `llm.Option`s.

## Still open

The rest of R3 is untouched and each part is independent of this one:

- ~~**No capability negotiation** (R3.2)~~ `[done 2026-07-25]` The registry now
  describes a format as well as building it: `Registry` maps a type to a
  `Format{Build, Describe}`, and an adapter declares its context windows,
  its cache shape, its default output ceiling and the rest through the optional
  `llm.Describing` interface. `internal/llm/context.go`'s hardcoded model map is
  gone. See [Provider capabilities](provider-capabilities.md).
- **The default catalog still lives in `cmd/atenea/main.go`** (R3.3) — an
  unimportable `main` package. It now declares its dialects honestly, which is a
  precondition for moving it to an embedded `providers.default.json`.
- **`internal/wailsprovider` is still a parallel provider system** (R3.4) with its
  own 3-value `Kind` enum. It does not go through this registry.
- **`Credential` is still `{Type, APIKey}`** (R3.5) — no `exec` credential, so
  Bedrock/Vertex/gateway auth has nowhere to live even with a factory registered.
- **`/connect` is still keyed by provider id** (`connectableProviderIDs`,
  `defaultKeyValidator`). Deliberately not folded into the registry: OpenRouter
  and OpenCode share a wire format and validate differently, so key validation is
  per-provider, not per-type.
