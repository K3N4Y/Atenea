---
updated_at: 2026-07-23
summary: Primary-source research and current-state audit for maximizing LLM prompt-cache hits across Atenea's providers.
---

# LLM prompt-cache hit: provider requirements and Atenea audit

Research checked against first-party documentation, official SDK source, and
the Atenea codebase on **2026-07-23**.

## Executive verdict

Atenea does **not** currently provide complete prompt-cache support across its
providers:

- **Native Anthropic:** normal requests now set top-level ephemeral
  `cache_control` and the adapter reports Anthropic's cache read/write usage
  counters.
- **OpenAI:** eligible requests can obtain OpenAI's automatic prefix
  cache without code changes. Atenea preserves the useful prefix order
  (system, history, newest turn) and stable tool definitions. Atenea now
  supplies a stable opaque per-session `prompt_cache_key`; it does not add an
  explicit breakpoint. The adapter reports automatic
  cache hits from `usage.prompt_tokens_details.cached_tokens`.
- **OpenRouter:** underlying supported models may cache automatically and
  OpenRouter applies provider-sticky routing. Atenea sends an opaque stable
  `session_id` for normal session turns. Standard OpenAI-compatible cached-token telemetry is
  preserved when the selected upstream returns it.
- **OpenCode Zen/Go:** Atenea's curated models use Zen's
  `/chat/completions` compatibility route. Zen publishes per-model cached-read
  prices, but its public Zen page does not define a single cache-control or
  cache-hit contract for these heterogeneous models. Atenea sends no cache
  extension; standard cached-token telemetry is preserved when returned, but
  cache behavior must not be assumed. The Zen page now maps OpenAI models to
  `/responses` and Anthropic models to `/messages`; those are not the
  routes/models Atenea's current Zen catalog uses. [Zen documentation][zen]

The highest-value implementation is: activate top-level automatic caching in
the native Anthropic adapter, preserve and expose provider cache metrics, then
add OpenAI `prompt_cache_key` using a stable session identifier. Explicit
breakpoints are a later optimization, useful only where the selected API/model
supports them.

## Current Atenea request shape

`internal/session/runner/turn.go` reconstructs every request as a stable system
prompt, the complete projected message history, and materialized tools. New
conversation content is appended. This is fundamentally cache-friendly because
all providers require an exact reusable **prefix**, not merely similar text.
Within the system prompt, Atenea orders the model-family base, repository
instructions, available skills, and mode instructions before the runtime
`<env>` block. This preserves the largest possible static prefix when runtime
values such as the date change; normal, local, and plan prompts share this
ordering.

There are nevertheless avoidable invalidators:

- `renderCompactedSystem` changes the system prompt after compaction, invalidating
  every cache prefix rooted in the prior system prompt.
- A mode change can switch the system prompt and tool set, also creating a new
  prefix. This is correct behavior and should not be hidden by a cache key.
- Tool ordering and JSON serialization must remain deterministic. Atenea's
  registry currently materializes a stable list, but this should be protected
  by an end-to-end request-body regression test.
- Cache telemetry exists in `llm.Usage` and persistence. The OpenAI-compatible
  adapter fills cache reads from `prompt_tokens_details.cached_tokens`; the
  Anthropic adapter fills cache reads and writes from
  `cache_read_input_tokens` and `cache_creation_input_tokens`.

## Anthropic: exact requirements

Anthropic caching is explicit. Current Messages requests may use either:

1. a single top-level `cache_control`, which automatically moves a breakpoint
   to the last cacheable block as a conversation grows; or
2. up to four block-level `cache_control` breakpoints.

The cache key covers the full prefix in protocol order: **tools, system,
messages**. Content through the breakpoint must match exactly. Static tools,
instructions, repository context, and examples therefore belong first; the
variable suffix belongs afterward. Cache writes occur only at breakpoints, and
reads search backward at most 20 blocks per breakpoint. A growing conversation
that adds 20 or more blocks between writes needs another explicit breakpoint.
[Anthropic prompt caching][anthropic-cache]

Only the `ephemeral` cache type is supported. Its default TTL is 5 minutes; a
hit refreshes that cache at no additional write cost. A 1-hour TTL can be
selected and has a higher write price. Anthropic documents 5-minute writes at
1.25× base input price and 1-hour writes at 2×; cache reads are discounted.
Requests below the model's documented minimum cacheable prompt length are
processed normally without an error, so the usage fields are the only reliable
proof of a hit/write. [Anthropic prompt caching][anthropic-cache]

The official Go SDK version already pinned by Atenea supports this directly:
`MessageNewParams.CacheControl` is the top-level automatic marker,
`NewCacheControlEphemeralParam()` creates the default marker, and the generated
type supports `ttl` values `5m` and `1h`. The SDK also exposes block-level
markers. No dependency upgrade or raw JSON escape hatch is needed.
[Anthropic Go SDK request type][anthropic-sdk-message]

### Recommendation for Atenea

Set the top-level automatic 5-minute cache control on every normal native
Anthropic request. It fits Atenea's append-only agent loop and is the simplest
safe default. If measurements later show misses caused by a changing final
block or the 20-block lookback, add an explicit breakpoint to the last stable
system block while retaining automatic caching for the conversation. Do not
put timestamps, request IDs, or other volatile data before that stable
breakpoint.

## OpenAI: exact requirements

Prompt caching is automatic for eligible requests and requires no opt-in.
Prompts must contain at least **1,024 tokens**; shorter requests still return
`cached_tokens: 0`. Hits require an exact prefix match. OpenAI explicitly
recommends placing static instructions/examples first and variable user data
last. The messages and available tool list participate in the cacheable prefix.
[OpenAI prompt caching][openai-cache]

OpenAI routes by a hash of the initial prompt prefix (typically the first 256
tokens, model-dependent). `prompt_cache_key` is combined with that hash and
should be reused for requests sharing a long common prefix. OpenAI recommends
approximately 15 requests/minute across all prefixes per key; higher-volume
workloads should partition keys with a stable mapping. For GPT-5.6 and later,
the key is required for the newer, more reliable implicit/explicit matching.
[OpenAI prompt caching][openai-cache]

GPT-5.6 and later also support explicit breakpoints in Responses and Chat
Completions: a supported content block receives
`cache_control: {"mode":"explicit"}`. The rendered prefix through the block
must still be at least 1,024 tokens, and the longest matching breakpoint wins.
Their `prompt_cache_options.ttl` currently accepts `30m`, which is also the
default minimum lifetime; OpenAI may retain the prefix longer. Older models use
the separate `prompt_cache_retention` policy, with in-memory entries generally
active for 5–10 minutes of inactivity (maximum one hour) or supported extended
retention up to 24 hours. [OpenAI prompt caching][openai-cache]

Chat Completions reports reads in
`usage.prompt_tokens_details.cached_tokens` and newer cache writes in
`cache_write_tokens`. Atenea pins an official SDK whose generated usage type
already exposes `CachedTokens`, which Atenea maps into
`Usage.CacheReadTokens`. [OpenAI Go SDK usage type][openai-sdk-usage]

### Recommendation for Atenea

Keep system instructions, tool schemas, and history byte-stable; add a stable,
non-secret per-session `prompt_cache_key`; and map cache writes when supported
by the pinned SDK/API response. Do not
use a single global cache key. Add explicit breakpoints only for model/API
combinations verified to accept them, because Atenea also uses the same adapter
for generic OpenAI-compatible servers.

## OpenRouter

OpenRouter says most supported providers enable caching automatically, while
some use explicit `cache_control`. After a cached request it uses provider
sticky routing so later requests for the same model/conversation reach the
same upstream endpoint. By default it derives the conversation key from the
first system/developer message and first non-system message; callers can send
`session_id` for explicit control. Manual `provider.order` disables sticky
routing. [OpenRouter prompt caching][openrouter-cache]

Atenea's append-only opening messages are compatible with the derived key, and
normal runner turns now provide a stable opaque identity as `session_id`,
avoiding accidental cross-session grouping when two chats begin identically.
The extension is enabled only for OpenRouter, not all OpenAI-compatible endpoints. Cache support,
threshold, TTL, and pricing still belong to the chosen upstream model/provider;
OpenRouter is a router, not one uniform cache implementation.

## OpenCode Zen and Go

Zen's official table is the authoritative route map. It currently routes its
OpenAI family through `/responses`, Anthropic through `/messages`, and Atenea's
curated Kimi/DeepSeek/GLM/Grok/MiniMax models through `/chat/completions`.
The same table publishes separate cached-read prices where available. It does
not document a Zen-wide request parameter, stable-session routing contract,
minimum prefix, TTL, or cache usage response shape for the Chat Completions
catalog. [Zen documentation][zen]

Consequently, a generic `cache_control` field should not be added to Atenea's
Zen/Go requests without a first-party contract for each route/model. Preserve
exact prefixes and telemetry fields that the compatibility response provides,
but treat observed hits as model-specific. If reliable caching on Zen's OpenAI
or Anthropic offerings becomes a goal, Atenea should use their documented
native routes rather than forcing them through its currently curated
Chat-Completions-only catalog.

## Implementation and validation order

1. Add an end-to-end provider-body test proving two consecutive agent steps
   preserve an identical system/tools/history prefix and append only the new
   suffix.
2. Enable Anthropic top-level automatic 5-minute caching and assert the emitted
   JSON contains `cache_control: {"type":"ephemeral"}`. Keep the already working
   read/write usage mapping.
3. Map OpenAI-compatible `cached_tokens` into `Usage.CacheReadTokens`; safely
   map writes only when the response field exists.
4. **Implemented:** thread a stable session cache/routing identity through `llm.Request`, using
   it as OpenAI `prompt_cache_key` and OpenRouter `session_id`. Keep provider
   extensions scoped by provider capability rather than base adapter defaults.
5. Add observability: cache read tokens, write tokens, eligible input tokens,
   and hit ratio (`cache_read / input`) per provider/model. Validate with two
   real requests made within the provider TTL; a successful response alone is
   not proof of caching.
6. Revisit explicit breakpoints only from measured misses. Optimize stable
   prefix size and breakpoint placement before increasing TTL, because TTL
   cannot rescue a prefix that changes byte-for-byte.

## Sources

- [Anthropic prompt caching][anthropic-cache]
- [Anthropic Go SDK generated Messages request type][anthropic-sdk-message]
- [OpenAI prompt caching][openai-cache]
- [OpenAI Go SDK generated usage type][openai-sdk-usage]
- [OpenRouter prompt caching and sticky routing][openrouter-cache]
- [OpenCode Zen model, route, pricing, and privacy documentation][zen]

[anthropic-cache]: https://platform.claude.com/docs/en/build-with-claude/prompt-caching
[anthropic-sdk-message]: https://github.com/anthropics/anthropic-sdk-go/blob/v1.59.0/message.go#L9608-L9780
[openai-cache]: https://developers.openai.com/api/docs/guides/prompt-caching/
[openai-sdk-usage]: https://github.com/openai/openai-go/blob/v2.7.1/completion.go#L222-L240
[openrouter-cache]: https://openrouter.ai/docs/guides/best-practices/prompt-caching
[zen]: https://opencode.ai/docs/zen/
