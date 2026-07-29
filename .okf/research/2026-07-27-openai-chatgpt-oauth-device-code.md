---
updated_at: 2026-07-27
summary: Verified facts about OpenAI's undocumented device-code OAuth flow and the codex backend that accepts a ChatGPT subscription token — endpoints, request shapes, response quirks, and what each one gets wrong if you guess.
---

# OpenAI ChatGPT subscription: device-code OAuth and the codex backend

> Verified 2026-07-27 against the source of `openai/codex`, `anomalyco/opencode`
> and `earendil-works/pi`, and against the live `usercode` endpoint. Nothing here
> is documented by OpenAI, and the endpoints are not RFC 8628 despite looking like
> it. Read this before touching `internal/openaiauth` or `internal/llm/codex.go`.
>
> The interactive half of the flow was **not** exercised against the real server:
> approving a code needs a person with a ChatGPT account. Everything downstream of
> the code is covered against stubs that reproduce the shapes below.

## Why this exists at all

A ChatGPT Plus/Pro subscription is not an API key with another name:

- The token it issues is **rejected by `api.openai.com`**.
- It is **rejected by Chat Completions** at any host.
- The one endpoint that accepts it is the codex backend's Responses API.

So supporting a subscription is not a credential change, it is a wire format. That
is the whole reason `openai-codex` is its own registry entry rather than an option
on `openai`.

## The client id is Codex's, and there is no alternative

```
client_id = app_EMoamEEZ73f0CkXaXp7hrann
issuer    = https://auth.openai.com
```

Identical in all three reference implementations. OpenAI offers **no client
registration** for this flow, so every third-party client authenticates as the
Codex CLI. The product consequence is recorded as a decision in
[the subscription spec](../specs/2026-07-27-openai-subscription-oauth.md#terms-of-service),
not here.

## The device flow, in four steps

It resembles RFC 8628 and shares none of its endpoints, none of its parameter
names and none of its status codes. Guessing from the RFC produces a client that
fails on every step.

### 1. Mint a code

```
POST {issuer}/api/accounts/deviceauth/usercode
Content-Type: application/json

{"client_id": "app_EMoamEEZ73f0CkXaXp7hrann"}
```

Verified live response:

```json
{
  "device_auth_id": "deviceauth_...",
  "user_code": "V3H5-1MW96",
  "interval": "5",
  "expires_at": "2026-07-28T01:58:53.774686+00:00"
}
```

Two traps:

- **`interval` is a JSON *string*.** A client that insists on a number falls back
  to its own default and earns a `slow_down`. `pi` handles both; so do we
  (`flexSeconds`).
- **`expires_at` is real and worth reading.** `pi` ignores it and hardcodes 15
  minutes, which means it can keep polling a code the server already retired. We
  use the field and keep the 15-minute figure only as the cap for when it is
  missing or unparseable.

### 2. Send the user somewhere else

```
https://auth.openai.com/codex/device
```

The user types `user_code` there. This is the only step no program can do, which
is the point of the flow: the machine running the agent needs no browser.

### 3. Poll

```
POST {issuer}/api/accounts/deviceauth/token
Content-Type: application/json

{"device_auth_id": "deviceauth_...", "user_code": "V3H5-1MW96"}
```

| Answer | Means |
|---|---|
| `200` with `authorization_code` and `code_verifier` | approved |
| `403` or `404` | still pending |
| body error `deviceauth_authorization_pending` | still pending |
| body error `slow_down` | pending, and widen the interval |
| anything else | a real failure |

**The pending statuses are the trap.** A client that treats a non-2xx as failure
gives up three seconds into a flow that takes a human thirty. Both reference
implementations also add a ~3s margin on top of `interval`, because the interval
is a floor and polling exactly on it is how a client earns the `slow_down`.

**There is no local PKCE.** The server generates the verifier and hands it back in
step 3 — which is unusual enough that a reader who knows PKCE will assume the code
is wrong. It is not.

### 4. Exchange

```
POST {issuer}/oauth/token
Content-Type: application/x-www-form-urlencoded

grant_type=authorization_code
code=<authorization_code from step 3>
code_verifier=<code_verifier from step 3>
redirect_uri=https://auth.openai.com/deviceauth/callback
client_id=app_EMoamEEZ73f0CkXaXp7hrann
```

Returns `access_token`, `refresh_token`, `id_token` and `expires_in` (~3600).
Nobody ever navigates to `redirect_uri` — the device flow has no browser redirect
— but the token endpoint demands it, because the code was minted for it.

## Refresh, and the rotation that logs users out

```
POST {issuer}/oauth/token
Content-Type: application/x-www-form-urlencoded

grant_type=refresh_token
refresh_token=<stored>
client_id=app_EMoamEEZ73f0CkXaXp7hrann
```

**The refresh token rotates.** The one that comes back replaces the one sent, and
a client that keeps the old one works for exactly one more hour and then logs the
user out with nothing on screen connecting the two events. This is the single most
expensive thing to get wrong in the whole feature, and it is invisible in testing
unless a test refreshes twice.

## The account id is a routing header, not an identity claim

Every request to the codex backend carries `chatgpt-account-id`. It is read out of
the JWT payload of the access token (`pi`) or the id token (`opencode`), base64url
decoded, **without verifying the signature** — we are not the audience of these
tokens and hold none of the keys. That is not a shortcut: a forged claim buys an
attacker a rejected request, because it is a routing hint the issuer told us to
send back.

Three spellings are in use, and a credential may carry any of them:

1. `chatgpt_account_id` at the top level.
2. `chatgpt_account_id` under the claim `https://api.openai.com/auth`.
3. `organizations[0].id` as a last resort (`opencode` reads this).

A credential with no account id is **unusable**: the header is not optional. The
login refuses to store one rather than trading this error for an opaque rejection
on the first turn.

## The runtime: `POST https://chatgpt.com/backend-api/codex/responses`

The Responses API, not Chat Completions.

Headers, all required:

| Header | Value |
|---|---|
| `Authorization` | `Bearer <access_token>` |
| `chatgpt-account-id` | the account from the JWT |
| `originator` | the calling program (`atenea`) |
| `User-Agent` | a real one; the backend rejects requests without |
| `OpenAI-Beta` | `responses=experimental` |
| `accept` | `text/event-stream` |
| `content-type` | `application/json` |
| `session-id` | a UUID, stable per conversation |
| `x-client-request-id` | a UUID, per request |

Body:

```json
{
  "model": "gpt-5.5",
  "store": false,
  "stream": true,
  "instructions": "<the system prompt goes HERE, not as a message>",
  "input": [ /* Responses items */ ],
  "include": ["reasoning.encrypted_content"],
  "prompt_cache_key": "<stable per conversation>",
  "tool_choice": "auto",
  "parallel_tool_calls": true
}
```

plus optional `reasoning: {effort, summary}` and `text: {verbosity}`.

**Never send `max_output_tokens`.** `opencode` explicitly clears it "to match the
codex cli", and no working client sends one. A host that fills
`llm.Request.MaxOutputTokens` — ours does, to reserve output in its context
estimate — must have it dropped by the adapter, which is a documented, tested
behavior rather than a silent omission.

The system prompt in `instructions` is the other silent one: sent as a message it
is not an error, it is deprioritized, so the failure looks like a model ignoring
its instructions.

A 429 here is a **subscription usage window**, not a rate limit on a request rate.
The vendor SDK's own error for it stringifies as the whole raw response body,
which never contains the words "usage limit" — so the adapter names it.

## Models

The codex backend publishes no `/models`. `opencode` whitelists these under OAuth,
and `gpt-5.5` is the Codex CLI's own default:

```
gpt-5.5 (default), gpt-5.4, gpt-5.4-mini, gpt-5.3-codex-spark
```

Treat the list, and the context windows declared for it, as data that is cheap to
change — it is a curated table in one place precisely because the endpoint will
not answer for itself.

## Sources

- `openai/codex` — the CLI whose client id everyone uses; the authority on the
  request shape.
- `anomalyco/opencode` — the model whitelist under OAuth, the `max_output_tokens`
  removal, and the id-token spelling of the account claim.
- `earendil-works/pi` — the lenient `interval` parsing and the access-token
  spelling of the account claim.
- The live `usercode` endpoint, which is harmless to call and needs no account.
