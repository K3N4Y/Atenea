---
updated_at: 2026-07-26
summary: Which providers a fresh install offers and which one a bare environment lands on — the embedded default catalog that replaced a table in an unimportable main package, and the environment fallback derived from it.
---

# Provider catalog

> Status: implemented 2026-07-26 (audit recommendation R3.3).
> See the [agnosticism and extensibility audit](../audits/2026-07-24-agnostic-extensibility-audit.md) §3.2 and §4 R3.
> Builds on the [provider registry](provider-registry.md) (R3.1) and
> [provider capabilities](provider-capabilities.md) (R3.2).

## The problem it solves

R3.1 made the *wiring* extensible: a declared type resolves to an adapter through
a map, so a new wire format is one registry entry. What stayed closed was the
*data* — which providers exist, at which endpoints, with which models:

```go
// cmd/atenea/main.go — deleted by this change
const (
    openRouterBaseURL  = "https://openrouter.ai/api/v1"
    anthropicModel     = "claude-opus-4-8"
    openCodeZenBaseURL = "https://opencode.ai/zen/v1"
    // ...six constants
)

func defaultProviderConfig() providerconfig.Config {
    return providerconfig.Config{Providers: []providerconfig.Provider{ /* 5 literals */ }}
}
```

Three separate costs:

- **Adding a model was a Go change in `package main`.** Not importable, not
  reviewable by anyone who does not read Go, and invisible to the desktop app.
- **The same literals lived twice.** `defaultProviderConfig` held the base URLs
  for the catalog and a second copy of three of them fed
  `environmentFallbackSnapshot`, the hand-written if-chain that picked a provider
  from `OPENROUTER_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY`. Changing an
  endpoint in one place left the other pointing at the old one.
- **The fallback rebuilt what the registry already knew.** It called
  `llm.NewOpenAIProvider(...)` with hand-picked options — a fourth place deciding
  how a request is shaped, after R3.1 had reduced that to one.

## The shape

```go
//go:embed providers.default.json
var defaultCatalogJSON []byte

func DefaultCatalog() Config
func EnvironmentFallback(cfg Config, getenv func(string) string, registry Registry) (llm.ProviderSnapshot, bool)
```

`providers.default.json` lives in `internal/providerconfig/`, next to the code
that reads it, and has **exactly the shape of a user's `providers.json`** — same
fields, same validation. That is the point: the shipped default is not a
privileged format, it is one instance of the published one, so it can be copied
as a starting point and a user's file can be diffed against it.

It goes through the same door, too. `Load` and `DefaultCatalog` both call one
`decodeConfig`, so `DisallowUnknownFields`, the duplicate-id check and the model
dedup apply to the embedded file as well: a typo like `api_key_ev` fails the
package's own test rather than silently shipping a provider with no credential.

`DefaultCatalog()` returns a **fresh `Config` on every call** — the same guarantee
`DefaultRegistry()` gives, for the same reason. Callers normalize it and merge it
into what the user already has, so a shared backing array would let one host's
config rewrite another's.

It **panics** on a malformed file instead of returning an error. The file is
compiled into the binary: a bad one is a build defect, not a runtime condition,
and forcing every caller to handle an impossible state is noise. A test decodes
it, so no binary ships without the check having run.

## Catalog order is precedence order

`EnvironmentFallback` walks the catalog and takes the first provider whose
API-key variable is set. That makes one list answer two questions that used to be
answered separately and inconsistently: the order the model picker lists
providers in, and the order a bare environment resolves them in. The provider
shown first is the provider an unconfigured environment lands on.

The old if-chain preferred OpenRouter, then OpenAI, then Anthropic; the catalog
leads with Anthropic. So with several keys exported the answer changed — to the
one the picker already presented as the default, which is also the recommended
starting point for agentic coding. A key for a provider the chain never mentioned
(`OPENCODE_API_KEY`) now resolves too, instead of dropping the user into the
offline demo while holding a valid credential.

Two behaviours are deliberate:

- **A provider whose type this build cannot construct is passed over, not
  reported.** The key is still a fact; it just names a wire format this binary
  does not speak, and the next key is a better answer than a dead provider. This
  is the same stance config loading takes on an unknown type (R3.1).
- **Only the environment is read, never a stored credential.** A stored
  credential arrives with the `providers.json` that `/connect` wrote, and that
  file carries its own selection. The fallback is the answer for when there is no
  file at all.

`false` means no usable key anywhere. The offline demo provider stays in the
host, not here: it is a fake for driving the TUI, and `providerconfig` has no
business shipping one.

## The model override is declared, not derived

A provider names the variable that overrides which model it starts on:

```json
{ "id": "anthropic", "api_key_env": "ANTHROPIC_API_KEY", "model_env": "ANTHROPIC_MODEL" }
```

`ANTHROPIC_MODEL` could have been computed from `ANTHROPIC_API_KEY` by string
surgery — trim `_API_KEY`, append `_MODEL` — and that holds for all three
providers that shipped with the old if-chain. It is declared instead, because the
rule breaks the moment a provider is named by another convention
(`MY_GATEWAY_TOKEN`), and because a field in the file is discoverable while a
naming rule buried in a loop is not. A third-party provider can offer the
override; a provider that omits the field simply has no override.

## What the JSON cannot hold

Comments. The rationale for each entry — why Anthropic leads, why model discovery
is off everywhere but OpenRouter, why OpenAI and OpenRouter declare different
types for the same protocol, why the first model matters — lives in the doc
comment on `defaultCatalogJSON` in `defaults.go`, next to the `//go:embed` that
pulls the file in. The invariants that comment asserts are pinned by
`defaults_test.go`, so the prose and the data cannot drift apart silently.

## Cost of a change now

| Change | Before | After |
|---|---|---|
| Add a model to a shipped provider | edit `cmd/atenea/main.go` | one line of JSON |
| Add a provider on a known wire format | edit `cmd/atenea/main.go` (+ constants) | one JSON object |
| Change an endpoint | two places, silently divergent | one place |
| Make a provider environment-selectable | new branch in an if-chain | already is, by being in the catalog |

## Still open

- **The fallback reads the shipped catalog, not the user's merged config.** The
  host passes `DefaultCatalog()` because `Open` takes the fallback as a
  constructor argument, before it has loaded anything — so a `model_env` a user
  edited in their own `providers.json` does not reach it. This matches what
  shipped before, and closing it means the fallback being resolved *inside* `Open`
  once the config is known, which is R4's single composition root.
- **The desktop app does not read this catalog.** `internal/wailsprovider` still
  carries its own base-URL and model constants (R3.4), so "the built-in catalog"
  currently means the TUI's.
- **A model discovered remotely has no window.** `ModelLister` returns bare ids,
  so the models this catalog does *not* curate show an em dash where a context
  label belongs. See
  [provider capabilities](provider-capabilities.md#still-open).

## Related

- [Provider registry](provider-registry.md) — how a declared type becomes an
  adapter.
- [Provider capabilities](provider-capabilities.md) — what that adapter declares
  about the models this catalog lists.
- [`/connect` command and credential store](../specs/2026-07-18-connect-command.md)
  — the allowlist that decides which catalog providers `/connect` manages, and
  which stays keyed by id on purpose.
