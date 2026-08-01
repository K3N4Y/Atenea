---
updated_at: 2026-07-31
summary: Design specification for driving atenea with a PostHog account — the PKCE loopback login in internal/posthogauth, the posthog wire format over the OAuth-authenticated Anthropic adapter, the account-id relaxation of the oauth credential arm, and the plan-gated model discovery through the registry's new Discover hook.
---

# Driving atenea with a PostHog account

> Status: implemented 2026-07-31 (TUI only; the desktop Vue frontend is a
> recorded follow-up).
> Builds on [Driving atenea with a ChatGPT subscription](2026-07-27-openai-subscription-oauth.md),
> whose oauth arm, token seam and device-login service this reuses, and on the
> [provider registry](../architecture/provider-registry.md).
> The protocol facts are ported from PostHog's own TypeScript client
> (`posthog-code`'s `posthog-provider` extension), which is the reference
> implementation this one mirrors.

## Problem

PostHog ships an LLM gateway (`gateway.us.posthog.com/posthog_code`) that serves
Claude-family models to PostHog accounts, authenticated by an OAuth login rather
than an API key. atenea had the whole OAuth substrate — the credential arm, the
per-request token seam, the device-login orchestration in the service both hosts
share — but every piece of it assumed OpenAI's shape: a device code the user
types, a credential that carries an account id, a curated model list.

PostHog differs in all three places:

- **The login is authorization-code + PKCE with a loopback redirect.** There is
  no device-code flow. The user's browser opens
  `{issuer}/oauth/authorize` and the approval comes back as a redirect to
  `http://localhost:8237/callback` — a fixed port, because it is part of the
  registered redirect URI.
- **The credential has no account id.** The gateway routes on the bearer alone.
- **The model list is plan-gated per account.** `GET {gateway}/v1/models` marks
  models outside the caller's plan `allowed: false`, so a static list would
  offer models that fail at selection.

## Decisions

- **A second `OAuthFlow`, not a second credential kind.** The PKCE flow maps
  onto the existing `DeviceCode` shape: the verification URI is the consent
  page, `Await` is the callback wait plus the token exchange, and `UserCode` is
  empty because there is nothing to type. `StartDeviceLogin`'s guard changed
  from "a code and a wait" to "a page and a wait"; everything else — pending
  logins, cancellation, retirement, `storeLogin` activation — is reused as-is.
  Hosts branch on the empty code, never on the provider: the TUI's awaiting
  stage drops the numbered "enter the code" step and says "open this in your
  browser".
- **`internal/posthogauth` mirrors `internal/openaiauth`.** Same shape (a
  `Client` with option seams, a `Tokens` result, redaction discipline: no code,
  verifier or token ever appears in an error), different protocol. Two quirks
  worth naming: the token endpoint takes a **JSON body**, not RFC 6749's form
  encoding, and the only scope the production OAuth apps accept is the wildcard
  `*` (explicit scopes are refused with `invalid_scope`; the wildcard is
  grandfathered).
- **The listener binds before the URL is shown.** A consent granted with
  nothing listening lands in the void, and a busy port (another login, another
  tool) fails legibly before anything was painted. The callback handler decides
  success or failure — OAuth `error` param, missing code, state mismatch —
  *before* writing the success/error page, so the browser never says
  "authentication complete" on a path the client rejects.
- **The adapter is the Anthropic one with a second constructor.** The gateway
  speaks anthropic-messages, so `NewAnthropicOAuthProvider(tokens, baseURL,
  model)` reuses the whole translation and differs only in auth: no static key
  (and the SDK's env-derived `ANTHROPIC_API_KEY` header explicitly deleted — a
  stray key in the environment must never authenticate a gateway request), a
  bearer resolved per request through `llm.OAuthTokenSource`, sent as
  `Authorization: Bearer` — the header every direct caller in the reference
  implementation uses. Capabilities are per-instance, because the gateway
  declares 1M windows for the opus/sonnet family where Anthropic's public API
  declares 200K.
- **`account_id` moved from the arm to the flow.** `Credential.Validate` no
  longer requires it for the oauth arm; OpenAI's token exchange refuses to
  issue a credential without one and the codex adapter refuses to send a
  request without one, which is where that fact actually lives. See
  [provider credentials](../architecture/provider-credentials.md).
- **US by default, EU by config.** The shipped entry points at the US cloud and
  gateway. An EU account edits `base_url` and `oauth_issuer` in its own
  `providers.json`; the OAuth client id follows the issuer host, so the region
  is one fact, not two the user must keep consistent. No region-selection UI.
- **Claude models only.** The gateway also serves GPT models over
  openai-responses (off `/v1`) and Cloudflare-hosted models; both are out of
  scope, and discovery filters them out along with `allowed: false` entries.
- **Model discovery through `Format.Discover`.** The registry gained an
  optional per-format hook (see
  [the registry doc](../architecture/provider-registry.md#a-format-can-discover-its-own-models)),
  because the gateway's list needs the OAuth bearer, hangs off `/v1/models`,
  and must be filtered. An unconnected provider is a silent skip, not a
  refresh warning.
- **OAuth only, like `openai-codex`.** No pasted-key path: nothing static could
  hold a login, and the user-editable `credentials.json` remains the escape
  hatch for headless setups.

## Known limitations

- **The loopback flow needs a browser on the same machine** (or port 8237
  forwarded over SSH). PostHog offers no device-code alternative, so this is
  the flow's constraint, not a choice. The device-code providers keep working
  everywhere they did.
- **Port 8237 is fixed.** It is the registered redirect URI. Two concurrent
  logins collide with a legible, retryable error.
- **The desktop Vue frontend is a follow-up.** The shared catalog lists PostHog
  in the desktop settings too, but `ProviderSettings.vue` still renders its
  code block unconditionally and hardcodes the ChatGPT button label; until the
  follow-up lands, the desktop login stage for PostHog shows an empty code.
- **The 1M context windows are trusted from the gateway's declarations.** If
  real requests reject long prompts, `posthogWindows` in
  `internal/llm/capabilities.go` is the one place to correct.

## What this does not close

- GPT models through the gateway's openai-responses surface.
- A region picker at login (the reference implementation has one; here region
  is config).
- Auto-opening the browser from the TUI *when the login starts*. What the TUI
  does have (added the same day): the URL renders as an OSC 8 hyperlink whose
  target is the full URL even when the display is clipped to the panel width,
  and ctrl+click on the awaiting stage opens the page from the app — necessary
  because mouse tracking makes the terminal report the click instead of acting
  on it. The desktop's `OpenLoginPage` already opens it natively.
