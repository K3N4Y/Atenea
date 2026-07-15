---
updated_at: 2026-07-14
summary: Design specification for selecting and persisting OpenAI-compatible providers, endpoints, and models from the TUI.
---

# Design: provider and model selector in the TUI

Date: 2026-07-10
Status: implemented

## Objective

Add a local `/model` command to `atenea-tui` that opens a full-screen model
selector. Providers occupy the narrower left column and the selected provider's
models occupy the wider right column. The selector changes the active provider,
endpoint, and model as one atomic selection, persists that selection globally,
and applies it to every session from its next LLM call.

Providers are declared manually in a global user configuration file. Atenea
does not create, edit, or delete provider definitions in this version. It may
discover additional models from each provider's OpenAI-compatible `/models`
endpoint and combine them with the models declared by the user.

## Motivation

The TUI currently chooses a provider and model only at process startup. A user
who wants to move between OpenRouter and a local OpenAI-compatible server must
restart Atenea with different environment variables. This interrupts the flow,
does not expose models available from the endpoint, and leaves model selection
outside the TUI.

The Wails boundary already exposes model discovery through
`internal/llm.ListModels`. The TUI should reuse that provider-neutral contract
while adding a safe runtime boundary for switching the provider shared by the
runner, subagents, and provider-backed tools.

## Prior-art conclusions

The design follows the common parts of established agent TUIs without copying
their provider-specific catalogs:

- Claude Code and Codex expose model selection as local UI behavior rather than
  sending `/model` to the LLM.
- Codex combines a catalog with remote information, caches it, and persists the
  selected model.
- OpenCode enriches providers with an external catalog, while Aider accepts a
  broad provider/model namespace and supplements known metadata.
- Atenea will start with a smaller hybrid catalog: user-declared models plus
  the provider's own `/models` response and a local cache. It will not depend
  on an external metadata service in v1.

## Scope v1

- Read providers from one global user configuration file.
- Support OpenAI-compatible providers with a base URL and optional API-key
  environment-variable reference.
- Open a two-column selector with `/model`.
- Keep `/model <query>` as the compact inline autocomplete path for users who
  already know part of the provider or model name.
- Display providers in the left column and only the selected provider's models
  in the wider right column.
- Move within a column with Up/Down, switch columns with Left/Right or Tab,
  select with Enter, and close with Escape.
- Mark the active provider/model pair with `●`.
- Combine configured, discovered, and cached model identifiers without
  duplicates.
- Switch provider, endpoint, and model atomically.
- Apply a successful switch to all sessions from their next LLM call.
- Allow an already active stream to finish with the provider snapshot it
  started with.
- Persist the successful selection globally for the next Atenea process.
- Keep the previous selection intact if validation, provider construction,
  switching, or persistence fails.
- Preserve the existing environment-based startup behavior when no global
  provider configuration exists.

## Out of scope

- Creating, editing, reordering, or deleting providers from the TUI.
- Storing API-key values in Atenea configuration.
- Providers that are not OpenAI-compatible.
- Per-project provider configuration.
- Per-session provider or model selection.
- Different models for normal mode, plan mode, subagents, or individual tools.
- External model metadata from `models.dev` or another catalog service.
- Pricing, context-window, capability, popularity, or benchmark metadata in the
  selector.
- Switching the provider underneath an LLM call already in progress.
- Automatically testing every model before listing it.

## Global configuration

### Location

The provider configuration lives at `providers.json` inside Atenea's directory
under `os.UserConfigDir()`. On a typical Linux installation this resolves to
`~/.config/atenea/providers.json`; macOS and Windows use their operating-system
equivalents.

The discovered-model cache is a separate file in the same directory. Discovery
must never rewrite provider definitions supplied by the user.

### Schema

```json
{
  "providers": [
    {
      "id": "openrouter",
      "name": "OpenRouter",
      "type": "openai-compatible",
      "base_url": "https://openrouter.ai/api/v1",
      "api_key_env": "OPENROUTER_API_KEY",
      "openrouter_reasoning": true,
      "models": ["anthropic/claude-sonnet-4"]
    },
    {
      "id": "ollama",
      "name": "Ollama local",
      "type": "openai-compatible",
      "base_url": "http://localhost:11434/v1",
      "models": ["qwen3:14b"]
    }
  ],
  "selected": {
    "provider": "openrouter",
    "model": "anthropic/claude-sonnet-4"
  }
}
```

### Provider fields

- `id`: required stable identifier, unique within the file.
- `name`: required display name used as the selector heading.
- `type`: required and equal to `openai-compatible` in v1. Unknown types make
  that provider invalid; they do not silently change semantics.
- `base_url`: required absolute HTTP or HTTPS URL. Atenea normalizes trailing
  slashes before constructing `/models` and provider requests.
- `api_key_env`: optional environment-variable name. The secret value is read
  from the process environment only when needed and is never persisted.
- `openrouter_reasoning`: optional boolean, false by default. When true, Atenea
  enables the OpenRouter-specific top-level `reasoning` request extension. It
  is explicit rather than inferred from the provider ID or endpoint.
- `models`: optional list of model identifiers. Blank identifiers are invalid;
  duplicate identifiers within one provider are collapsed while preserving the
  first occurrence.

### Selected fields

- `provider`: the selected provider ID.
- `model`: the selected model identifier for that provider.

The selected model may be user-configured, discovered, or cache-only. Atenea
must preserve it in the merged catalog even when a later discovery response no
longer contains it, so the current selection never disappears silently.

### Validation and writes

- Invalid JSON or an invalid top-level schema fails global configuration
  loading with an actionable message that includes the file path.
- Duplicate provider IDs, empty required fields, invalid URLs, unknown provider
  types, and a selection referencing a missing provider are invalid.
- A selected model not present in the configured list is valid because it may
  have originated from discovery or a previous manual configuration.
- Configuration updates use an atomic same-directory temporary-file write,
  flush, close, and rename. A failed write leaves the previous file intact.
- File permissions should follow the operating-system defaults for user config.
  The file contains no secret values.

## Startup and compatibility

When `providers.json` exists and is valid, Atenea resolves the persisted
selection, validates the required API-key environment variable, constructs the
provider, and starts the TUI with that active provider/model pair.

When `providers.json` does not exist, Atenea retains the current startup
behavior based on the environment, in order of precedence: `OPENROUTER_API_KEY`
(model from `OPENROUTER_MODEL`), then `OPENAI_API_KEY` (model from `OPENAI_MODEL`,
defaulting to `gpt-5.6-terra`), and finally the demo fallback when neither key is set.
The OpenAI fallback constructs its provider with the OpenRouter `reasoning`
extension disabled, since the official OpenAI API rejects that field. Atenea does
not create `providers.json` implicitly in this path. This avoids surprising
configuration writes and keeps existing installations working. Both OpenRouter
and OpenAI ship as seeded default provider definitions. Missing default providers
are merged into existing configurations by provider ID without overwriting user
definitions. OpenAI uses a curated list of agent-compatible models and does not
consume `GET /models`, because that endpoint also returns image, audio, embedding,
moderation, and realtime models that cannot satisfy Atenea's streaming tool-calling
contract.

When `providers.json` exists but is invalid, Atenea reports the configuration
error clearly and uses the existing environment-based startup behavior as a
temporary fallback. The invalid file is not overwritten or repaired
automatically.

When the persisted selection cannot be constructed because its required API
key is absent, startup uses the same environment fallback and reports the exact
missing variable. Other configured providers remain available in `/model` if
the TUI can start through the fallback.

## Hybrid model catalog

For each configured provider, the selector merges these sources in order:

1. The persisted selected model, when this is the selected provider.
2. Models declared in the provider's `models` field.
3. Models returned by the latest successful `GET <base_url>/models` request.
4. Models from the last valid cache entry for that provider.

Deduplication is exact and case-sensitive because model identifiers are opaque
provider values. The first occurrence determines ordering. Remote-only models
are sorted lexicographically before being appended, preventing endpoint order
changes from making keyboard navigation unstable.

### Refresh behavior

- Opening `/model` renders configured models and valid cached models without
  waiting for the network.
- Atenea starts one background refresh per provider that does not already have
  a refresh in flight.
- Successful results update the in-memory list and the cache, while preserving
  the current filter and the selected row when that row still exists.
- A failed refresh does not remove configured or cached models.
- The current `internal/llm.ListModels` timeout remains the upper bound for an
  individual provider request unless implementation evidence justifies moving
  the timeout into the catalog component.
- The cache records the provider ID, normalized base URL, model IDs, and fetch
  time. A cache entry is ignored when its base URL no longer matches the
  provider definition.
- Cache corruption is isolated: Atenea ignores the corrupt cache, reports a
  non-fatal warning, and continues with configured models.

The v1 cache has no freshness cutoff for display: stale data is preferable to
an empty offline selector and is visually indistinguishable in the intentionally
minimal UI. Every selector opening still attempts a background refresh.

## `/model` command semantics

`/model` is a local TUI command, not a prompt-template command from
`internal/command`. It must be recognized before normal slash-command expansion
or prompt submission.

- Exact `/model` opens the selector with an empty filter.
- `/model <query>` opens it with the trimmed query as the initial filter.
- Additional whitespace between the command and query is ignored.
- A slash command whose name merely begins with `model` is not intercepted.
- Opening or canceling the selector does not add a user message to the session,
  prompt history, or durable event log.
- Selecting a model adds only a transient TUI confirmation; it is not an LLM
  conversation message.

## Selector experience

The selector is a modal overlay on the chat. Its normal content is deliberately
minimal:

```text
OpenRouter
  ● anthropic/claude-sonnet-4
    openai/gpt-5
    google/gemini-2.5-pro

Ollama local
    qwen3:14b
    deepseek-r1:8b
```

- Each provider appears once as a non-selectable heading.
- Only model identifiers appear under the heading.
- `●` marks the active provider/model pair.
- The modal does not normally show endpoints, cache source, descriptions,
  capabilities, or other metadata.
- Up/Down moves among selectable model rows and skips headings.
- `Enter` selects the highlighted row.
- `Esc` closes the selector without changing anything.
- Typing edits the filter; Backspace removes one rune.
- Filtering is case-insensitive and matches the provider display name,
  provider ID, or model identifier. A provider heading remains visible when
  either the provider or one of its models matches.
- A provider match shows all of that provider's models. A model-only match
  shows only matching models under its provider heading.
- Providers with no models after configured, cached, and discovered sources
  show one non-selectable dim row, `No models available`.
- A provider refresh failure does not add permanent explanatory rows. If the
  provider has no usable models, the dim empty row remains and a transient
  status may report the refresh error.
- If filtering yields no selectable model, the modal shows `No matches` and
  `Enter` does nothing.
- Opening the modal initially highlights the active pair when visible;
  otherwise it highlights the first selectable row.
- Resizing preserves the filter and highlighted logical provider/model pair.

The modal must remain usable in narrow and short terminals by clipping long
model identifiers and using a vertically scrollable list. Clipping is visual
only; selection always uses the complete identifier.

## Switching architecture

### Stable provider boundary

Introduce a concurrency-safe switchable implementation of `llm.Provider`. The
runner, subagents, and provider-backed tools receive this one stable object from
`wiring.Build`, preserving the existing shared-provider topology.

The switchable provider owns one immutable active snapshot containing:

- provider ID and display name;
- normalized base URL;
- model identifier;
- constructed `llm.Provider` implementation.

`Stream` reads the active snapshot exactly once before delegating. Replacing the
active snapshot therefore affects future calls but cannot move an existing
stream to another endpoint or model.

### Switch transaction

Selecting a row performs these operations in order:

1. Resolve the provider definition and complete model identifier.
2. Validate the required API-key environment variable, if any.
3. Construct the candidate OpenAI-compatible provider.
4. Prepare the updated persisted selection.
5. Persist the selection atomically.
6. Swap the active provider snapshot.
7. Update the TUI footer and show the transient confirmation
   `Model changed to <provider name> · <model>`.

If steps 1-5 fail, no runtime state changes. Once the atomic file rename in step
5 succeeds, step 6 is an in-memory non-failing swap. This order guarantees that
the running selection and persisted selection do not diverge after a reported
failure.

The selector may be used while an agent run is active. The active run and any
provider calls it has already started retain their captured snapshot. Any later
provider call, including a later step of the same multi-step agent run, uses the
new global selection. This precisely defines "from the next LLM call" and
avoids rebuilding the entire engine.

## State ownership

- A provider-config repository owns loading, validation, and atomic persistence.
- A model-catalog service owns configured/discovered/cache merging and refresh
  coordination.
- The switchable provider owns the active runtime snapshot.
- `internal/tui.Engine` exposes list, refresh, current-selection, and switch
  operations through the existing `Agent` boundary or a focused adjacent
  interface.
- `internal/tui.Model` owns modal presentation, filter, cursor, scroll, and
  transient status.
- The footer reads the active snapshot rather than a startup-only model string.

These boundaries keep JSON, HTTP discovery, concurrency, and Bubble Tea input
handling independently testable.

## Errors and recovery

- Missing API key: keep the modal open, keep the old selection, and show
  `Missing environment variable <NAME>`.
- Provider construction failure: keep the old selection and show a concise
  provider-specific error.
- Persistence failure: keep the old selection and include the configuration
  path in the error.
- Discovery failure: continue with configured and cached models; do not block
  selection of those models.
- Empty provider catalog: keep the provider heading visible with
  `No models available`.
- Cache corruption: ignore the cache and continue; never overwrite provider
  configuration.
- A selected model later rejected by the remote server produces the normal LLM
  request error. Atenea does not silently fall back to another model.

## Documentation changes during implementation

- Update `.okf/architecture/tui.md` with local-command handling, modal state,
  and the dynamic footer.
- Update `.okf/architecture/llm-opencode-openai.md` with the switchable-provider
  boundary and catalog discovery responsibilities.
- Document the global `providers.json` schema and examples in the user-facing
  project documentation.

## Verification strategy

Implementation must follow the repository's verifiable TDD cycle.

### Safety net

- Run the current focused TUI, provider, wiring, and model-list tests before
  production changes.
- Run `go test ./...` to establish the broad baseline.

### RED and GREEN slices

1. Configuration parsing, validation, fallback, and atomic persistence.
2. Catalog merge ordering, exact deduplication, cache isolation, and refresh.
3. Switchable-provider snapshot semantics under concurrent streams.
4. Engine operations for listing and selecting provider/model pairs.
5. Modal opening, grouping, filtering, navigation, resize, cancel, and errors.
6. Dynamic footer and transient success confirmation.

Each slice starts with the closest behavior test and reaches GREEN with the
smallest production change.

### Triangulation

- Providers with and without API-key variables.
- Configured-only, discovered-only, cached-only, and selected-only models.
- Duplicate models across all catalog sources.
- Endpoint unavailable, malformed response, timeout, and corrupt cache.
- Failed persistence leaves both disk and runtime selection unchanged.
- An old stream continues on provider A while a later provider call uses B.
- `/model`, `/model query`, unknown slash commands, and ordinary prompt input.
- Empty, filtered, narrow, short, and resized modal states.

### E2E evidence

Run `atenea-tui` in a PTY against deterministic fake OpenAI-compatible servers:

1. Start with provider A selected and verify its model in the footer.
2. Enter `/model`, navigate to provider B, select its model, and verify the
   confirmation and footer.
3. Send a prompt and assert that only provider B receives the next request with
   the selected model ID.
4. Restart Atenea with the same config directory and verify provider B remains
   selected.
5. Repeat while provider A has an active streaming response: that response
   finishes through A and the following LLM call reaches B.

The E2E harness must inspect the rendered modal for grouping, active marker,
focus, clipping, and layout regressions rather than asserting only backend
state.

### Closing gates

```bash
gofmt -l .
go vet ./...
go test -race ./...
go test ./...
```

`gofmt -l .` must be empty and all other commands must pass cleanly before the
implementation is considered complete.

## TDD Cycle Evidence

This document defines behavior but does not modify production code. RED through
REFACTOR are intentionally deferred to the implementation plan and execution.

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Existing architecture and nearby tests identified | `.okf/architecture/tui.md`, `internal/tui/*_test.go`, `internal/llm/models.go`, `app_provider_test.go` | baseline commands specified for implementation |
| Understand | Current startup, shared provider wiring, slash-command expansion, and model discovery inspected | `cmd/atenea-tui/main.go`, `internal/tui/engine.go`, `internal/tui/model.go`, `internal/command/command.go`, `internal/wiring/wiring.go` | behavior and extension boundaries identified |
| RED | No production behavior changed in design phase | implementation plan pending | N/A |
| GREEN | No production behavior changed in design phase | implementation plan pending | N/A |
| TRIANGULATE | Required variants and E2E scenarios specified | Verification strategy in this document | N/A until implementation |
| REFACTOR | Architecture boundaries reviewed for focused ownership | State ownership and switching architecture in this document | design-only pass |

## Acceptance criteria

- `/model` opens a grouped modal and never reaches the LLM as prompt text.
- The modal shows provider headings and only model identifiers beneath them.
- The active pair is marked with `●`; filtering and keyboard navigation work.
- Provider definitions come only from global `providers.json` in v1.
- API-key values never appear in persisted Atenea files.
- Configured, discovered, and cached models merge deterministically.
- Selecting a pair atomically persists and activates provider, endpoint, and
  model, or changes nothing on failure.
- Every session uses the global selection from its next provider call.
- Calls already started complete with the snapshot they captured.
- The footer updates immediately after a successful switch.
- The selected pair survives process restart.
- Existing environment-only startup continues when the configuration file is
  absent and remains an explicit fallback when it is invalid.
- Unit, race, integration, PTY E2E, formatting, vet, and whole-suite gates pass
  with recorded TDD evidence.
