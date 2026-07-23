---
updated_at: 2026-07-22
summary: Primary-source research for integrating OpenCode Zen and OpenCode Go as LLM providers in Atenea.
---

# OpenCode Zen and Go provider integration

## Executive summary

OpenCode Zen and OpenCode Go should be represented as **two providers**, even
though both use an OpenCode account API key and OpenCode's own registry names
the environment variable `OPENCODE_API_KEY` for both. Their base URLs,
entitlements, model catalogs, billing, and OpenCode model prefixes differ:

| Provider | Base URL | OpenCode prefix | Commercial model |
| --- | --- | --- | --- |
| OpenCode Zen | `https://opencode.ai/zen/v1` | `opencode/<model-id>` | Pay as you go; some temporary free models |
| OpenCode Go | `https://opencode.ai/zen/go/v1` | `opencode-go/<model-id>` | Subscription with rolling and monthly usage limits |

Sources: the official [Zen documentation][zen-docs], [Go
documentation][go-docs], and OpenCode's official [models.dev provider
registry][models-registry].

The safe first integration for Atenea's existing OpenAI-compatible chat client
is the intersection advertised at each provider's `/chat/completions`
endpoint. Do not assume that every model returned by `/models` accepts Chat
Completions: OpenCode explicitly routes some Zen models through OpenAI
Responses, Anthropic Messages, or Google Generative Language protocols, and
some Go models through Anthropic Messages.

## Authentication

1. The user signs in at OpenCode, obtains an API key, and pastes it into the
   client. Zen requires a funded/billed account; Go requires a Go subscription.
   The Go setup flow links to the same OpenCode Zen console/account system.
   [Zen setup][zen-docs] [Go setup][go-docs]
2. OpenCode's provider registry declares `OPENCODE_API_KEY` for both provider
   IDs (`opencode` and `opencode-go`). [Official registry][models-registry]
3. For direct OpenAI-compatible HTTP calls, send the key as:

   ```http
   Authorization: Bearer <api-key>
   ```

   This is the standard authentication produced by the registry's declared
   `@ai-sdk/openai-compatible` package. It was also verified on 2026-07-22
   against both official Chat Completions endpoints: no header returned
   `Missing API key`, a bogus bearer token returned `Invalid API key`, and a
   bogus `x-api-key` was treated as missing. The checks made no billed model
   request.
4. A key is a credential, not an entitlement. A valid OpenCode key does not by
   itself mean that its workspace has a Go subscription or Zen balance. Keep
   provider selection separate so an entitlement error is attributable to the
   correct service.

## Protocols and endpoints

### OpenCode Zen

Base URL: `https://opencode.ai/zen/v1`.

| Protocol | Endpoint | Models currently documented in this family |
| --- | --- | --- |
| OpenAI Chat Completions | `/chat/completions` | Grok, DeepSeek, MiniMax, GLM, Kimi, and the temporary free OpenAI-compatible models shown by the docs |
| OpenAI Responses | `/responses` | GPT models |
| Anthropic Messages | `/messages` | Claude and Qwen models |
| Google Generative Language | `/models/<model-id>` | Gemini models |
| OpenAI-style discovery | `/models` | Public model list |

The exact model-to-protocol mapping and IDs are in the official [Zen endpoint
table][zen-docs]. The live [Zen models endpoint][zen-models] is public and
returns an OpenAI-shaped `{ "object": "list", "data": [...] }` response, but it
does not state which inference protocol each model needs. Therefore discovery
alone is insufficient for protocol selection.

### OpenCode Go

Base URL: `https://opencode.ai/zen/go/v1`.

| Protocol | Endpoint | Models currently documented in this family |
| --- | --- | --- |
| OpenAI Chat Completions | `/chat/completions` | Grok, GLM, Kimi, DeepSeek, MiMo, and Hy3 models listed in the docs |
| Anthropic Messages | `/messages` | MiniMax and Qwen models listed in the docs |
| OpenAI-style discovery | `/models` | Public Go model list |

The exact mapping is in the official [Go endpoint table][go-docs]. The live [Go
models endpoint][go-models] has the same OpenAI-shaped discovery response as
Zen. As with Zen, its result does not encode the required inference protocol.

## Model IDs observed on 2026-07-22

Model catalogs change; OpenCode says the Go list may change and Zen publishes
deprecation dates. Atenea should avoid hard-coding the entire catalog as a
long-lived truth. If a curated static list is needed, it should be treated as a
tested snapshot and refreshed deliberately from official sources.

### OpenAI Chat Completions-compatible Zen models

The current Zen documentation assigns these IDs to `/chat/completions`:

```text
grok-4.5
grok-build-0.1
deepseek-v4-pro
deepseek-v4-flash
minimax-m3
minimax-m2.7
minimax-m2.5
glm-5.2
glm-5.1
glm-5
kimi-k2.5
kimi-k2.6
kimi-k2.7-code
big-pickle
mimo-v2.5-free
laguna-s-2.1-free
north-mini-code-free
nemotron-3-ultra-free
deepseek-v4-flash-free
```

Source: [Zen endpoint table][zen-docs]. Free-model availability and data
handling are temporary/model-specific, so they should not be the sole default.

### OpenAI Chat Completions-compatible Go models

The current Go documentation assigns these IDs to `/chat/completions`:

```text
grok-4.5
glm-5.2
glm-5.1
kimi-k3
kimi-k2.7-code
kimi-k2.6
deepseek-v4-pro
deepseek-v4-flash
mimo-v2.5
mimo-v2.5-pro
hy3
```

Source: [Go endpoint table][go-docs]. Go also exposes MiniMax and Qwen models,
but its documentation assigns those to `/messages`, not Chat Completions.

## Zen versus Go

- **Zen is a multi-protocol AI gateway** with proprietary and open models,
  pay-as-you-go credits, and temporary free offerings. The docs say OpenCode
  curates and benchmarks model/provider combinations. [Zen docs][zen-docs]
- **Go is a low-cost subscription focused on open coding models**. At the time
  of research it costs $5 for the first month and then $10/month, with a
  five-hour, weekly, and monthly usage allowance expressed in dollar value.
  After limits, it can optionally fall back to the workspace's Zen balance.
  [Go docs][go-docs]
- Go is designed for international access and says its models are hosted in the
  US, EU, and Singapore. Zen says all its models are hosted in the US.
  [Go privacy][go-docs] [Zen privacy][zen-docs]
- The services have different privacy details. Zen lists exceptions and
  retention terms for specific free models and proprietary APIs; Go states its
  providers follow zero-retention and do not train on user data. Consult the
  live privacy sections before presenting a guarantee in product UI.

## Implementation guidance for Atenea

1. Add provider identities such as `opencode-zen` and `opencode-go`; do not
   model Go as a Zen model or merge their catalogs.
2. Both can use the existing API-key credential shape and bearer auth. They may
   share a user-entered key, but should have separate connection records and
   base URLs.
3. If Atenea currently implements only OpenAI Chat Completions, expose only the
   IDs in the compatible lists above. This produces honest capability rather
   than a model selector that fails after selection.
4. Use `GET <base-url>/models` for availability validation or refresh, but
   retain a trusted model-to-protocol map because discovery does not provide
   protocol metadata.
5. To expose all Zen/Go models later, add protocol adapters for OpenAI
   Responses, Anthropic Messages, and (for Zen Gemini) Google Generative
   Language. Select the adapter per model as OpenCode's endpoint table does.
6. Use a stable coding-oriented default only after an end-to-end streaming and
   tool-call test. Model presence in `/models` proves availability, not Atenea
   compatibility.
7. Surface 401 errors as credential failures and subscription/balance/limit
   failures as provider entitlement or quota failures; do not collapse them
   into “model unavailable.”

## Sources

- [OpenCode Zen documentation][zen-docs] — setup, endpoint/protocol mapping,
  IDs, billing, privacy, and model deprecations.
- [OpenCode Go documentation][go-docs] — setup, endpoint/protocol mapping, IDs,
  subscription limits, fallback behavior, and privacy.
- [OpenCode models.dev registry][models-registry] — canonical provider IDs,
  base URLs, environment variable, SDK family, and registry model metadata.
- [Live Zen model discovery][zen-models] and [live Go model
  discovery][go-models] — current server-advertised IDs and response shape.
- [OpenCode provider source][provider-source] — official client behavior for
  the `opencode` provider, including authenticated versus public/free model
  loading.

[zen-docs]: https://opencode.ai/docs/zen/
[go-docs]: https://opencode.ai/docs/go/
[models-registry]: https://models.dev/api.json
[zen-models]: https://opencode.ai/zen/v1/models
[go-models]: https://opencode.ai/zen/go/v1/models
[provider-source]: https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/provider/provider.ts
