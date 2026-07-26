---
updated_at: 2026-07-26
summary: How the Wails desktop adapter consumes the shared provider service.
---

# Wails provider surface

The desktop app has no provider model of its own. `main.App` holds a
`*providerconfig.Service` — the same one the TUI holds, opened on the same paths
through `providerconfig.OpenDefault` — and its bindings are a projection of it.

`internal/wailsprovider` used to sit here with a parallel implementation: a
three-value `Kind` enum, its own base-URL and model constants, its own
`OPENROUTER_API_KEY` lookup and its own `llm.NewOpenAIProvider` calls. It is
deleted. What it did that the service did not — declaring an arbitrary local
endpoint — became a capability of `providerconfig` instead of a second system, so
the TUI gained it too. See
[Provider catalog](provider-catalog.md#the-user-can-declare-providers-too).

## What that changes for a user

The selection lives in `~/.config/atenea/providers.json`. Choosing a model in the
desktop app changes it in the terminal app and the other way round, and a key
pasted in either one is stored once. The desktop also reaches the whole shipped
catalog (Anthropic, OpenRouter, OpenAI, OpenCode) instead of the two options its
own enum knew about.

## The bindings

| Binding | Answers with |
|---|---|
| `ProviderCatalog()` | one row per configured provider: models, `builtIn`, key state |
| `ActiveProvider()` | the selection, plus the context window the active adapter declares |
| `SelectModel(providerID, model)` | rebuilds the adapter and persists the selection |
| `ConnectProvider(providerID, apiKey)` | validates and stores a key |
| `DeclareEndpoint(name, baseURL, model)` | adds a local endpoint, returns its id |
| `ForgetProvider(providerID)` | removes a declared endpoint |
| `RefreshModels()` | re-asks every discovering endpoint; the error is a warning |
| `ListModels(baseURL)` | probes an endpoint *before* it is declared |

Four notes on the shape:

- **`ProviderEntry` merges two sources.** The catalog knows about models and the
  credential store knows about keys; a row needs both, and merging them is the
  adapter's job rather than something `providerconfig` should flatten.
- **`ContextWindow` travels with the selection.** It is what the active adapter
  declares for that model, resolved through the switchable handle
  (`llm.ActiveCapabilities`). Zero means no adapter declares one, and the UI shows
  tokens without a percentage rather than scaling against a number nobody vouched
  for. This is what deleted the fourth hand-maintained window table, which lived
  in `frontend/src/features/chat/contextWindow.ts`.
- **`ListModels` is a probe, not a second provider path.** Adding an endpoint you
  have not declared yet is the one moment the UI needs models from a base URL that
  is in no config. It calls the same `llm.ListModels` the catalog's refresh uses:
  no construction, no key resolution, no constants.
- **`DeclareEndpoint` derives the id from the name.** The form asks for one thing
  instead of two, and a name that collides with a shipped provider is refused by
  `Declare` with the reason.

## Nothing is persisted in the browser

The frontend used to keep `providerKind`, `baseURL` and `model` in `localStorage`
and re-apply them at startup via `SetProvider`. That copy is gone: the backend owns
the selection, the panel reads it, and a stale client copy can no longer re-point a
running app at an endpoint that stopped existing. Only the workspace folder is
still persisted client-side.

## Lifecycle

`SelectModel` and `ConnectProvider` run inside
`wailsworkspace.Manager.Reconfigure`, which excludes prompt admission and
republishes the wiring. The republish is what cuts the runs in flight: they were
streaming from the selection that was just replaced.

The provider handle passed to wiring is the `*llm.SwitchableProvider` itself, not
the adapter of the moment, so a model change needs no re-assembly to take effect —
it swaps what the handle delegates to, atomically. The one thing wiring still asks
per turn is `LocalPrompt`, which reads `Active().LocalModels`. Together those
removed `wailsprovider.Snapshot` and `wailsworkspace.ProviderState`: there is no
provider/config pair left to keep consistent, because there is no second copy of
either.

## Related

- [Provider catalog](provider-catalog.md) — the shipped catalog, user-declared
  providers, and how both hosts open the service.
- [Provider registry](provider-registry.md) — how a declared type becomes an
  adapter.
- [Provider capabilities](provider-capabilities.md) — what that adapter declares,
  including the context windows this surface shows.
- [Wails workspace lifecycle](wails-workspace.md) — the serialization the
  selection changes run inside.
