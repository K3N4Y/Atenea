---
updated_at: 2026-08-01
summary: Research and implementation proposal for model-specific reasoning levels through the PostHog provider.
---

# PostHog reasoning levels: implementation research

## Executive recommendation

Implement reasoning as a **request preference**, not as a provider capability or a provider-specific configuration flag.

The shared contract should carry an optional reasoning policy on `agentcore/llm.Request`. The PostHog adapter should translate that policy according to the selected model family:

- `gpt-*`: map the policy to the Responses API `reasoning.effort` field, and continue requesting a reasoning summary so the existing `Reasoning*` events remain useful.
- `claude-*`: map the policy to Anthropic Messages extended thinking only when the PostHog gateway demonstrably accepts it for that model. The request must set `thinking.type` and a valid `budget_tokens`, and must account for thinking tokens inside `max_tokens`.
- Unsupported or unknown combinations must fail before a request is sent, or fall back only when the policy explicitly permits automatic selection. Never silently send a vendor field to the wrong wire format.

The first deliverable should support GPT effort levels end to end and introduce the neutral contract. Claude should be enabled through a separately verified compatibility matrix, because the current PostHog integration explicitly does not send Anthropic `thinking` and the public PostHog documentation does not publish the gateway's model-by-model thinking policy.

## Current state in Atenea

PostHog is one registry format with two wire surfaces:

- Claude models use the Anthropic Messages adapter at the gateway root (`internal/providerconfig/registry.go:149-169`).
- GPT models use the standard OAuth Responses adapter under `/v1` (`internal/providerconfig/registry.go:156-162`).
- Model discovery is authenticated and plan-gated (`internal/llm/posthogmodels.go:12-23`, `internal/llm/posthogmodels.go:47-71`).

Reasoning events already exist in the public LLM contract (`agentcore/llm/event.go:20-24`), and `Capabilities.Reasoning` describes whether an adapter asks for reasoning (`agentcore/llm/capabilities.go:28-32`). That flag is intentionally not a request preference.

The current behavior is split:

- The PostHog Anthropic adapter never sends the `thinking` parameter (`internal/llm/anthropic.go:104-114`; asserted by `internal/llm/anthropic_test.go:73-81`). It can forward a thinking block if the endpoint volunteers one, but it does not request one. `posthogCapabilities.Reasoning` is therefore false (`internal/llm/capabilities.go:116-127`).
- The shared Responses adapter already supports `reasoning.effort` and `reasoning.summary` (`internal/llm/codex.go:116-124`, `internal/llm/codex.go:251-253`). PostHog currently builds it with `codexOptions()`, which fixes effort to `medium` and summary to `auto` (`internal/providerconfig/registry.go:185-191`).
- Responses reasoning events are already translated into the shared event stream (`internal/llm/codex.go:364-381`).
- The current `Request` has no reasoning preference (`agentcore/llm/provider.go:30-39`).

Therefore the smallest real gap is not event rendering. It is request-time configuration and model-specific translation.

## Confirmed external protocol facts

### OpenAI Responses API

Official source: [OpenAI reasoning guide](https://platform.openai.com/docs/guides/reasoning), sections “Reasoning effort”, “Reasoning mode”, “Reasoning summaries”, “Managing the context window”, and “Allocating space for reasoning”.

- `reasoning.effort` is model-dependent. Documented values include `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, and `max`; a model may support only a subset.
- Lower effort generally trades quality and reasoning-token usage for lower latency and cost; higher effort does the opposite.
- Raw reasoning tokens are not exposed. A reasoning summary must be requested explicitly through `reasoning.summary`.
- `summary: auto` selects the most detailed available summarizer for the model.
- Reasoning tokens consume output budget and context-window space. `max_output_tokens` covers reasoning plus visible output.
- The guide states that defaults and supported values depend on the model; the adapter must not assume one universal level.

Atenea already has the correct Responses request seam: `responsesProvider.Stream` conditionally sets `params.Reasoning` (`internal/llm/codex.go:251-253`). The PostHog adapter should stop treating `medium` as an immutable provider-wide fact.

### Anthropic Messages API

Official source: [Anthropic extended thinking](https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking), sections “How to use extended thinking”, “Budget rules and tuning”, “Supported models”, “Streaming thinking”, and “Prompt caching in manual mode”.

- Manual extended thinking is requested with:

  ```json
  {
    "thinking": {
      "type": "enabled",
      "budget_tokens": 10000
    }
  }
  ```

- `budget_tokens` has a documented minimum of 1,024 and normally must be lower than `max_tokens`.
- `max_tokens` includes thinking and visible output, so enabling thinking without reserving enough output budget can truncate the answer.
- The documented model behavior is version-sensitive. The page states that Claude 4.5 and earlier support manual enabled thinking, while newer families use adaptive thinking or reject the old `type: enabled` shape; this must be checked against the model IDs and gateway behavior rather than inferred from the `claude-` prefix.
- Streaming usage can report thinking-token details in the final `message_delta` usage object.
- Changing thinking configuration invalidates prompt-cache breakpoints.

Atenea's Anthropic SDK request construction currently has no thinking field (`internal/llm/anthropic.go:108-114`). The stream translator already understands `ThinkingBlock` and `ThinkingDelta` (`internal/llm/anthropic.go:175-196`), so the output side is substantially ready.

### PostHog-specific evidence and limits

The repository's PostHog specification identifies the local reference implementation as PostHog Code's TypeScript client (`.okf/specs/2026-07-31-posthog-oauth-provider.md:13-15`), but the referenced GitHub repository URL was unavailable during this research (404), and GitHub code search requires authentication. PostHog's public AI engineering page does not document gateway reasoning controls or a model-by-model effort matrix.

What is directly established by this codebase:

- PostHog exposes Claude and GPT models through one plan-gated catalog (`internal/llm/posthogmodels.go:61-71`).
- GPT requests use standard Responses reasoning fields, not ChatGPT-subscription-only headers (`internal/llm/codex.go:149-152`).
- Claude requests use Anthropic Messages, but currently do not request thinking (`internal/llm/anthropic.go:46-69`, `internal/llm/anthropic.go:104-114`).

What is **not** established and must be verified against the live gateway or a reachable first-party PostHog client:

- Which PostHog Claude IDs accept manual or adaptive thinking.
- Whether PostHog forwards `budget_tokens`, `thinking.type`, and any adaptive-thinking fields unchanged.
- Whether PostHog normalizes Claude and GPT effort names behind another field.
- Whether plan gating changes the supported effort levels.

These are compatibility facts, not safe assumptions.

## Proposed domain contract

Add an optional, provider-neutral preference to `agentcore/llm.Request`, for example:

```go
type ReasoningEffort string

const (
    ReasoningEffortDefault ReasoningEffort = ""
    ReasoningEffortNone    ReasoningEffort = "none"
    ReasoningEffortMinimal ReasoningEffort = "minimal"
    ReasoningEffortLow     ReasoningEffort = "low"
    ReasoningEffortMedium  ReasoningEffort = "medium"
    ReasoningEffortHigh    ReasoningEffort = "high"
    ReasoningEffortXHigh   ReasoningEffort = "xhigh"
    ReasoningEffortMax     ReasoningEffort = "max"
)

type Reasoning struct {
    Effort ReasoningEffort
}

type Request struct {
    // existing fields...
    Reasoning *Reasoning
}
```

The exact type names are illustrative; the important properties are:

1. Empty/nil means provider default. Existing callers preserve behavior.
2. The contract contains a user preference, not `budget_tokens`, `thinking.type`, `summary`, or an SDK enum.
3. Vendor-specific parameters remain inside adapters.
4. Validation is model-aware and happens before network I/O.
5. A future UI can offer “default”, “low”, “medium”, and “high” without knowing whether the selected endpoint speaks Responses or Messages.

Do not put this field in `Capabilities`: capabilities answer what an adapter can do; they do not ask it to do something (`agentcore/llm/capabilities.go:7-12`).

## Model-family translation matrix

| PostHog model family | Wire format | Preference translation | Current state | Required validation |
|---|---|---|---|---|
| `gpt-*` | Responses `/v1` | `reasoning.effort`; keep `summary: auto` when summaries are desired | Effort fixed to `medium` by `codexOptions()` | Per-model accepted effort values and summary availability |
| `claude-*` legacy/4.x/4.5 | Anthropic Messages | `thinking.type=enabled`, derived `budget_tokens`; reserve `max_tokens` | Thinking not requested | Gateway acceptance, minimum/maximum budget, streaming usage |
| `claude-*` newer adaptive families | Anthropic Messages | Adaptive-thinking request shape if gateway supports it; do not send legacy enabled shape blindly | Unsupported by current adapter | Exact gateway/API version and field translation |
| unknown family | Unknown | Refuse construction or return unsupported preference error | Construction already refuses unknown family (`internal/providerconfig/registry.go:163`) | Keep refusal explicit |

The UI should not expose every backend value immediately. Start with a stable user vocabulary (`default`, `low`, `medium`, `high`) and map only values confirmed for the selected model. Preserve advanced values internally for future models, but do not advertise them unless discovery supplies support metadata.

## Capabilities and discovery changes

`Capabilities.Reasoning bool` is too coarse for a selector. Keep it for backward-compatible “does this instance request/report reasoning?” behavior, and add a separate optional model-level description only if the product needs to show support before construction.

Recommended future shape:

```go
type ReasoningSupport struct {
    Efforts []ReasoningEffort
    Default ReasoningEffort
}
```

Possible placement:

- Extend `Capabilities` with a `ReasoningSupport` map keyed by model ID for curated/static knowledge.
- Prefer extending the model discovery result so PostHog can report account- and plan-specific support if the gateway eventually exposes it.

Do not fabricate support from model-name prefixes. If support is unknown, the picker should show “default only” or leave the advanced control unavailable rather than claim that `high` works.

## Adapter implementation plan

### Phase 1: shared request preference and GPT PostHog support

1. Add the neutral effort type and optional request field in `agentcore/llm`.
2. Add a request-level override to the Responses provider. Constructor options remain useful for the provider default; request preference wins when present.
3. Validate the effort against a model/profile matrix. Do not validate only against the global OpenAI list.
4. Make the PostHog registry build the Responses provider with a PostHog-specific default profile, not the ChatGPT `codexOptions()` policy. The current `codexOptions()` function should remain for the ChatGPT subscription format.
5. Preserve `reasoning.summary=auto` for GPT models so existing reasoning events continue to appear.
6. Add tests that inspect the outgoing JSON for default, low, high, and unsupported values, including the precedence of request override over constructor default.

### Phase 2: Claude gateway compatibility probe and support

1. Build an integration probe against a controlled PostHog gateway or an official reachable PostHog client/reference. Test each configured Claude ID with the smallest valid budget and streaming enabled.
2. Record accepted request shapes and failures in a versioned compatibility table, not in ad hoc model-prefix logic.
3. Add a PostHog-specific Anthropic profile carrying thinking policy and model budget bounds.
4. Translate the neutral effort to a budget using an explicit table. Example policy (to be calibrated, not shipped as fact): low = 4k, medium = 10k, high = 20k.
5. Increase `max_tokens` or compute the effective ceiling so thinking and visible output both fit. The request must not silently reduce the user's visible output allowance.
6. Set `Capabilities.Reasoning` based on the active request/profile behavior, and preserve the existing block/event translation and usage accounting.
7. Add negative tests for unsupported model/effort combinations and for budgets below 1,024.

### Phase 3: UI and host behavior

The shared preference is now process-local sitting state. The runner reads it
when constructing each turn, while the TUI exposes `/reasoning` and
`/reasoning:<level>` and the desktop binding exposes `ReasoningEffort` and
`SetReasoningEffort`. Rewiring a workspace or switching models preserves the
selection. Model-specific support matrices remain a future requirement; the
current GPT profile intentionally exposes only the four validated contract
levels.

## Testing requirements

The permanent contract needs behavior tests, not source assertions:

- Default request omits vendor reasoning fields or uses the provider's documented default.
- PostHog GPT request contains `reasoning.effort` for each supported level and still contains the requested summary behavior.
- Request-level effort overrides the constructor/provider default without changing other sessions.
- Unsupported GPT effort is rejected before an HTTP request.
- Claude request, when enabled, contains valid `thinking` and a budget of at least 1,024; `max_tokens` accounts for the budget.
- Claude unsupported combinations fail before a request and explain the model/level mismatch.
- Streaming reasoning events remain properly bracketed for both adapters.
- Usage preserves reasoning-token counts when the gateway reports them.
- Context compaction reserves the effective output ceiling, including reasoning where the adapter can determine it.
- Both TUI and desktop construct requests with the same preference.

Run the repository's required gates after implementation: `gofmt -l .`, `go vet ./...`, `go test ./...`, `go test -race ./...`, and the frontend lint/format/test commands.

## Risks and mitigations

- **Gateway drift:** PostHog may change the model catalog or proxy behavior. Mitigate with a compatibility table, focused contract tests, and a conservative default.
- **Silent quality/cost changes:** A fixed `medium` default hides a product decision. Keep default behavior stable until the UI and telemetry make the choice visible.
- **Budget accounting:** Claude thinking consumes `max_tokens`; treating it as an independent allowance can truncate visible output. Centralize effective-budget calculation in the adapter.
- **False capability claims:** `claude-*` and `gpt-*` are routing families, not proof of reasoning support. Require verified metadata or conservative fallback.
- **Cross-host divergence:** The TUI and desktop share the agent core but have separate selection surfaces. Put the preference in `Request` and test both wiring paths.
- **Raw chain-of-thought exposure:** Continue exposing only provider-approved summaries/events. Do not add a field that asks for or stores raw hidden reasoning.

## Decision

Proceed with the neutral request-level preference and implement GPT/PostHog first, because the Responses translation already exists and its field is confirmed. Treat Claude levels as a gated second phase pending a reachable PostHog-owned compatibility source. The current fixed PostHog GPT setting (`medium`) should become the default value, not a hard-coded immutable policy.
