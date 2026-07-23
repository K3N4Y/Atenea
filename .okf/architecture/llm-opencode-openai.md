---
updated_at: 2026-07-22
summary: OpenCode Zen and Go integration through Atenea's OpenAI-compatible provider.
---

# OpenCode Zen and Go via the OpenAI-compatible provider

OpenCode Zen and OpenCode Go reuse Atenea's existing OpenAI Chat Completions
adapter, so the agent loop does not change. They are separate provider entries
because they have different endpoints, catalogs, billing, and entitlements.

Claude's adapter (`llm-claude.md`) remains the final destination
(M10). This is the cheap way to start (M2/M5).

## What is OpenCode Zen / OpenCode Go

- **OpenCode Zen** is the pay-as-you-go gateway at
  `https://opencode.ai/zen/v1`.
- **OpenCode Go** is the subscription service at
  `https://opencode.ai/zen/go/v1`.
- Both use bearer authentication from `OPENCODE_API_KEY`.
- Atenea exposes only models assigned to `/chat/completions` by OpenCode. The
  full discovery response also contains models requiring Responses, Anthropic
  Messages, or Google protocols, which this adapter cannot serve.

## Configuration

| Key | Value | Notes |
| --- | --- | --- |
| Zen URL base | `https://opencode.ai/zen/v1` | pay as you go |
| Go URL base | `https://opencode.ai/zen/go/v1` | subscription |
| APIkey | from `opencode.ai/auth` | header `Authorization: Bearer` |
| env var | `OPENCODE_API_KEY` | official OpenCode provider registry name |
| model id | raw model ID | for example `kimi-k2.7-code` |

The default catalog uses raw IDs because Atenea sends them directly to the
gateway. Prefixes such as `opencode/` and `opencode-go/` belong to OpenCode's
own configuration format, not the raw HTTP request.

## Runtime behavior

- `/connect` stores a credential separately for each provider. The same key can
  be pasted into both when the OpenCode workspace has both entitlements.
- OpenCode has no documented non-billable credential-validation endpoint;
  `/models` is public. Atenea therefore checks that the entered key is non-empty
  and surfaces authentication or entitlement errors on the first inference.
- Model discovery is disabled for these entries. Their curated lists contain
  only models documented for Chat Completions, preventing selection of a model
  that requires another protocol.
- The existing OpenAI-compatible stream mapping, tool-call handling, and
  provider snapshot behavior apply unchanged.

## Runtime provider snapshots

`internal/llm.SwitchableProvider` keeps one stable provider reference for
wiring while publishing immutable provider/model snapshots. The runner acquires
one snapshot per logical LLM call and uses its model for system-prompt
selection, compaction, request construction, and streaming. A swap affects only
future calls; an active stream finishes on its acquired provider. Direct calls
through the switcher force the snapshot model over stale request values.

OpenAI-compatible discovery remains provider-neutral through
`llm.ListModels(ctx, baseURL, apiKey)`. Authenticated providers receive a Bearer
header; keyless local endpoints omit it.

## Sources

- OpenCode Zen: https://opencode.ai/docs/zen/
- OpenCode Go: https://opencode.ai/docs/go/
- Detailed primary-source research: `../research/2026-07-22-opencode-zen-go-provider-integration.md`
- Loop that consumes this layer: `agent-loop.md`
- Production adapter (Claude): `llm-claude.md`
- Way of working: `AGENTS.md`
