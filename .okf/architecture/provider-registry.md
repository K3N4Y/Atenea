---
updated_at: 2026-07-31
summary: How a provider's declared wire format resolves to the adapter that speaks it — the factory registry that replaced two closed switches, why the dialect became the type, how a format declares that its credential is a login rather than a string, and how one discovers its own models.
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
type BuildParams struct {
    Provider Provider
    Model    string
    APIKey   string
    Tokens   llm.OAuthTokenSource
}

type Factory func(BuildParams) (llm.Provider, error)
type Registry map[string]Format

func DefaultRegistry() Registry
func (r Registry) Build(params BuildParams) (llm.Provider, error)
func (r Registry) Types() []string
func (r Registry) OAuth(providerType string) (*OAuthFlow, bool)
```

> `[updated 2026-07-27]` `Factory` took `(def, model, apiKey)` until the
> `openai-codex` format arrived, whose credential is a login rather than a string:
> its bearer expires within the hour and travels with an account id, so what a
> factory needs is not the credential but the way to ask for one. The parameters
> became a struct so the next kind of auth arrives as a field instead of as a
> signature change in every factory that does not care. See
> [Driving atenea with a ChatGPT subscription](../specs/2026-07-27-openai-subscription-oauth.md).

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

Six wire formats ship:

| Type | Adapter | What it means |
|---|---|---|
| `anthropic` | `llm.NewAnthropicProvider` | the native Messages API |
| `openai` | OpenAI adapter, `WithOpenAICompatibility` | official OpenAI: `prompt_cache_key`, no `reasoning` |
| `openrouter` | OpenAI adapter, `WithOpenRouterCompatibility` | OpenRouter routing: `session_id`, `reasoning` unless opted out |
| `openai-compatible` | OpenAI adapter, `WithoutOpenRouterReasoning` | the neutral dialect: chat completions, no vendor extension |
| `openai-codex` | `llm.NewCodexProvider` | a ChatGPT subscription: the codex backend's Responses API, authenticated by a login |
| `posthog` | `llm.NewAnthropicOAuthProvider` | PostHog's LLM gateway: the anthropic wire format, authenticated by an OAuth login whose bearer travels per request |

`openai-codex` is the same vendor a third time, and the clearest case yet for the
type deciding everything: a subscription token is refused by `api.openai.com` and
by chat completions, and the endpoint that accepts it wants the system prompt in a
field, refuses an output ceiling, and identifies the caller with a header. Nothing
about that could have been an option on `openai`.

`[updated 2026-07-31]` `posthog` is the inverse case: the *same wire format* as
`anthropic` behind a different credential and a different catalog. It is still its
own type — the credential is a login and the model list is the gateway's own — but
the adapter is the Anthropic one built through a second constructor that takes an
`llm.OAuthTokenSource` instead of a key. See
[Driving atenea with a PostHog account](../specs/2026-07-31-posthog-oauth-provider.md).

### A format declares how it is authenticated

`[updated 2026-07-27]` `Format` gained an optional `OAuth *OAuthFlow` carrying the
two halves of a login — renew a stored credential, run the login that creates one.
Everything that has to tell a login-authenticated provider from a key-authenticated
one asks `Registry.OAuth`: which credential seam to wire into `BuildParams`, which
affordance a UI draws, which `/connect` branch to take.

It lives on the format for the same reason the dialect became the type. The
alternative is the credential wiring switching on type names, which is the coupling
this registry exists to remove — and worse, it would hand a third party's OAuth
format OpenAI's refresh protocol.

`[updated 2026-07-31]` The `posthog` format proved the seam: a second `OAuthFlow`
(authorization-code + PKCE over a loopback redirect, where OpenAI's is
device-code) slotted in without the credential wiring, the device-login service or
either host learning a new provider name. The login's `DeviceCode` carries an
empty `UserCode` — there is nothing to type, the browser brings the approval back
— and a host branches on that emptiness, never on the provider.

### A format can discover its own models

`[updated 2026-07-31]` `Format` gained a second optional hook, `Discover`, for a
format whose model list the generic OpenAI-compatible `GET /models` cannot fetch:

```go
Discover func(ctx context.Context, def Provider, bearer string) ([]string, error)
```

Only `posthog` sets it — the gateway's list hangs off `/v1/models`, wants the
OAuth bearer, and gates models by plan (`allowed: false`), so a curated list would
offer models that fail at selection. The catalog resolves the bearer through the
same freshness margin every turn uses and skips **silently** when the provider was
never connected: an unconnected login provider sits in every default catalog, and
a warning on every refresh would tell the user to log in to something they did not
ask for.

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
- ~~**The default catalog still lives in `cmd/atenea/main.go`** (R3.3)~~
  `[done 2026-07-26]` It is an embedded `providers.default.json` owned by
  `providerconfig`, with the same shape and the same validation as a user's file —
  which this change's honest dialects were the precondition for. The environment
  fallback is derived from it instead of rebuilding providers by hand. See
  [Provider catalog](provider-catalog.md).
- ~~**`internal/wailsprovider` is still a parallel provider system** (R3.4)~~
  `[done 2026-07-26]` Deleted. The desktop app holds the same
  `providerconfig.Service` the TUI does, so every provider either build constructs
  now goes through this registry. See
  [Wails provider surface](wails-provider.md).
- ~~**`Credential` is still `{Type, APIKey}`** (R3.5)~~ `[done 2026-07-26]` It is
  a tagged variant with an `exec` arm that reads a bearer token from a command's
  standard output, so a registered Bedrock/Vertex/gateway factory now has
  somewhere to get its credential from. See
  [Provider credentials](provider-credentials.md).
- **`/connect` is still keyed by provider id** (`connectableProviderIDs`,
  `defaultKeyValidator`). Deliberately not folded into the registry: OpenRouter
  and OpenCode share a wire format and validate differently, so key validation is
  per-provider, not per-type.
