---
updated_at: 2026-07-27
summary: Design specification for driving atenea with a ChatGPT Plus/Pro subscription — the oauth credential arm, the per-request token seam, the openai-codex wire format, the device-code login in both hosts, and the terms-of-service consideration of reusing the Codex client id.
---

# Driving atenea with a ChatGPT subscription

> Status: implemented 2026-07-27.
> The endpoint facts this rests on are in
> [OpenAI ChatGPT subscription: device-code OAuth and the codex backend](../research/2026-07-27-openai-chatgpt-oauth-device-code.md).
> Supersedes the "OAuth for OpenAI is explicitly deferred" scope note in
> [`/connect` command and credential store](2026-07-18-connect-command.md).

## Problem

Every way of paying atenea for tokens was pay-per-token. A user with a ChatGPT
Plus or Pro subscription already pays OpenAI a flat monthly fee for the same
models, and had no way to spend it here: the only credential the product
understood was a string you paste.

The obstacle was never the storage. It was that a subscription differs from an API
key in four places at once — how you obtain it, how long it lasts, what a request
carries, and which endpoint accepts it — and each of those touched a different
part of the provider stack.

## Decisions

- **A subscription is a wire format, not a credential.** Its token is rejected by
  `api.openai.com` and by Chat Completions; the endpoint that accepts it takes a
  different request. So `openai-codex` is its own registry entry with its own
  adapter and its own declared capabilities, next to `openai` and `openrouter`.
  The vendor being the same is not a reason to share a dialect — the registry's
  whole premise is that the declared type, never the id, decides how a request is
  shaped.

- **Login is device-code only.** The alternative — a loopback redirect on a fixed
  local port — needs a browser on the same machine as the process, which the
  terminal host cannot promise: atenea runs over SSH, in containers, on machines
  with no desktop session. A code typed somewhere else works everywhere the other
  one does, and in the places it does not.

- **The poll loop survives the blink, not the outage.** This is the one flow
  designed for the user to walk away from the machine, so a poll that says nothing
  about the login does not end it: neither one that never arrived — DNS, a reset,
  a wifi switch — nor one the server answered with a 5xx from its edge or a bare
  429, because the code stays approvable through all of them and the next poll is
  three seconds away. They spend one shared budget: ten in a row is an outage
  rather than a blink, and the wait ends with a sentence naming which of the two it
  was, since "check the connection" is the wrong thing to tell someone whose
  connection is fine. Every other answer is one the next poll would repeat — a bad
  request, a revoked client, an unknown device id — and ends the login on the first
  occurrence.

- **A new credential arm, `oauth`.** `Credential` gains a fourth field and a
  fourth `Validate` case, exactly as the type's original comment promised:

  ```go
  type OAuthCredential struct {
      AccessToken  string    `json:"access_token"`
      RefreshToken string    `json:"refresh_token"`
      ExpiresAt    time.Time `json:"expires_at,omitzero"`
      AccountID    string    `json:"account_id"`
  }
  ```

  Every field answers a question an `api_key` never raises — when this stops
  working, what replaces it, whose account it is. `Validate` refuses a login with
  no refresh token (it would die within the hour with no way back) and one with no
  account id (no request could be routed). A zero `ExpiresAt` is accepted and
  reads as "renew on first use": an unknown lifetime and an expired one cost the
  same to get wrong, and one refresh settles it.

- **The token is resolved per request, not baked at selection time.** This is the
  decision the rest of the design follows from. An access token lives about an
  hour and a conversation outlives that, so a credential resolved when the model
  was selected turns the second hour of a session into the endpoint's own 401 —
  with `/model` as the only recovery. And the account id is a second value, so a
  `string` could not have carried the credential anyway.

  The seam is declared where the adapter lives and implemented where the storage
  lives:

  ```go
  // internal/llm
  type OAuthToken struct{ AccessToken, AccountID string }
  type OAuthTokenSource interface {
      OAuthToken(ctx context.Context) (OAuthToken, error)
  }

  // internal/providerconfig
  func (r *CredentialResolver) OAuthTokenSource(providerID string, refresh OAuthRefresher) llm.OAuthTokenSource
  ```

  `internal/llm` therefore never imports `internal/providerconfig`, and the
  adapter knows nothing about files, refresh protocols or credential arms.

- **Renewal happens before expiry, once, and is persisted.** Three properties,
  each protecting against a different failure:

  - A **five-minute margin** (`DefaultOAuthRefreshMargin`): a token that expires
    mid-request fails a turn that is minutes long, and renewing early costs one
    request against a subscription that is not billed per token.
  - **Single flight per provider, inside one process.** A main turn and its
    subagents share one adapter and notice the same expiry at the same moment.
    Without serialization each would refresh, each would rotate the refresh token,
    and every rotation but the last would already be retired — leaving the stored
    credential naming a token the server has dropped. Waiters re-read the store
    after the gate, which is also what makes them use the *current* refresh token
    rather than the copy they read before waiting.
  - **Across processes, the collision is absorbed rather than prevented.** The
    gate is a mutex and `credentials.json` is coordination-free, so the TUI and
    the desktop app can both renew in the same second and the loser reads
    `invalid_grant` about a credential that is now good. A failed renewal
    therefore re-reads the store once and serves what it finds if another process
    already rotated it. Preventing the second request means a lock file shared by
    two binaries, which is a file to clean up after a crash in exchange for one
    wasted round trip.
  - **The rotation is only real once written.** A renewal the store could not
    persist is reported rather than served: the credential is already broken at
    that point, and serving the turn anyway buys an hour and then a logout with no
    cause the user could see.
  - **Stop cancels a turn, not a rotation.** The context threaded into the token
    source is the turn's, and the runner cancels it the moment the user presses
    Stop. The renewal runs on `context.WithoutCancel` of it, under a timeout of its
    own, because the dangerous window is between OpenAI committing the rotation and
    this process reading the body: aborting there drops a refresh token the server
    has already retired, and the next prompt sends the user back through the login.
    It is the same reason the device-login polling is detached from the click that
    started it.

  A renewal that cannot be completed produces an error that says *log in again*,
  because that is the one provider failure a user can act on.

- **`Factory` widened to a params struct.** A format now needs either a resolved
  string or a way to ask for a credential, so passing `(def, model, apiKey)` no
  longer describes the job:

  ```go
  type BuildParams struct {
      Provider Provider
      Model    string
      APIKey   string
      Tokens   llm.OAuthTokenSource
  }
  type Factory func(BuildParams) (llm.Provider, error)
  ```

  A struct rather than a fifth positional argument, so the next kind of auth
  arrives as a field instead of as a signature change in every factory that does
  not care. The codex factory refuses a `nil` `Tokens`: an adapter with no way to
  resolve a credential could only ever produce 401s, and naming the wiring beats
  letting the endpoint name something else.

- **Which providers authenticate by login is registry data, not a name check.**
  `Format` gains an optional `OAuth *OAuthFlow` carrying the two halves — renew a
  stored credential, run a login. Everything that has to tell the two kinds apart
  (which seam to wire, which affordance to draw, which `/connect` branch to take)
  asks the registry. A third party registering an OAuth format supplies its own
  flow instead of silently inheriting OpenAI's.

- **The issuer is configuration.** `Provider.OAuthIssuer` sits beside `BaseURL`
  and for the same reason: which host issues a credential is a fact about the
  endpoint, and one compiled into an adapter cannot be pointed at a stub or at an
  enterprise tenant. Empty means the format's default, so the shipped catalog does
  not declare it.

- **The login orchestration lives in the shared service, not in either host.**
  `StartDeviceLogin` / `AwaitDeviceLogin` / `CancelDeviceLogin` /
  `CancelDeviceLoginAttempt` on `providerconfig.Service`. Starting and awaiting are
  separate calls because a host must paint the code before anything blocks on a
  human, and because the two have different lifetimes. Awaiting is cancellable
  *without* abandoning the login: a redraw or a page reload must not throw away a
  code the user is in the middle of typing, so only the cancel calls end one.
  Starting a second login retires the first, whose code the server has stopped
  honoring anyway — and because that retirement can leave two waits pointing at one
  attempt, a login releases *every* waiter with the same outcome instead of handing
  the result to whoever arrived first and parking the rest until the process exits.

  Retirement is ordered by when an attempt *started*, which `DeviceLogin.Attempt`
  carries. Two mints in flight can answer inverted, and the two cancel calls answer
  different questions: `CancelDeviceLogin` retires whatever is pending, which is
  what a UI showing one code and one Cancel button means, while
  `CancelDeviceLoginAttempt` retires one named attempt and does nothing once the
  provider has moved on — what a host abandoning its own attempt means.

  On approval the service does exactly what `Connect` does for a key: store the
  credential, and activate the provider on its first curated model when nothing
  else is selected.

- **`Connectable()` reports the kind.** `ConnectableProvider.Kind` is `api_key` or
  `device_code`. A UI that inferred it from the provider id would be one catalog
  release away from drawing a password field for something that has no password.

## Catalog entry

```json
{
  "id": "openai-codex",
  "name": "OpenAI (ChatGPT subscription)",
  "type": "openai-codex",
  "base_url": "https://chatgpt.com/backend-api/codex",
  "disable_model_discovery": true,
  "models": ["gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark"]
}
```

It declares **no `api_key_env`** — there is no variable that could hold a login,
and declaring one would make an unconnected subscription look configured, and
would let the environment fallback select a provider it cannot authenticate.
Discovery is off because the endpoint publishes no `/models`; the model list and
the context windows that go with it are curated data in one place, cheap to
correct.

## The dialect

Four things differ from `openai`, and each one fails silently if guessed:

1. **The system prompt is `instructions`, a field.** Sent as a message it is not
   rejected, it is deprioritized — the failure looks like a model ignoring its
   instructions.
2. **`max_output_tokens` is never sent.** The host fills
   `llm.Request.MaxOutputTokens` to reserve output in its context estimate; the
   adapter drops it. Asserted by a test, because an omission nobody tests is an
   omission somebody restores.
3. **The account travels as a header**, alongside the bearer.
4. **`store: false` and `include: ["reasoning.encrypted_content"]`**, with
   `prompt_cache_key` carrying the host's conversation key and `session-id`
   derived from it so a conversation stays one conversation to the backend.

A 429 is a spent subscription window, so it surfaces as a sentence naming that
rather than as the SDK's own error, which stringifies as the whole response body
and never says "usage limit".

The adapter is built on `openai-go/v2`'s Responses API with the base URL pointed
at the codex backend; no SSE parsing is hand-rolled.

## UX

**Terminal.** `/connect` lists both kinds of provider and branches on `Kind`.
Selecting a subscription starts the login immediately — there is nothing to type
here — and the panel then shows the page to open, the code to enter, and that it
is waiting for approval, with the code's deadline when the server gave one. The
deadline is a clock time, not a countdown: nothing redraws this panel while it
waits for a human — the spinner only ticks during a turn and the awaiting stage
swallows every key — so "expires in 9 minutes" would still read nine minutes nine
minutes later, and would keep reading it after the code was dead. `esc` cancels
the login and steps back to the list. `/connect openai-codex` skips the list,
mirroring how naming a key provider jumps to its key entry.

`esc` pressed *during* the round trip that mints the code closes the panel and
retires that attempt too. The request is already out and has no handle to cancel —
the code does not exist yet — so the panel's generation moves instead, and the code
that eventually lands is cancelled rather than awaited: nobody approves one they
were never shown, and polling it anyway would report its expiry ten minutes later
over whatever the user is doing by then. For the same reason a retired attempt's
answer never writes the panel's `busy` or its error — both belong to whatever the
user started next.

Moving the generation is not the same as silencing the attempt, and `esc` over an
API-key validation is the case that separates them: the key is already at the
provider and can still come back rejected, which is the only chance the user has to
learn nothing was stored. So a stale outcome carries what kind of attempt produced
it (`connectDoneMsg.login`) — a dismissed login reports nothing, a rejected key
reaches the transcript. Silence there is worse than a late error: the user believes
they connected and finds out at the next turn, through "no credential stored for
provider".

The attempt a dismissed mint retires is named, not implied. Two mints can be in
flight for one provider and the network decides which answers first, so
`providerconfig` numbers them by *start* order, `DeviceLogin.Attempt` carries that
number to the host, and both `Service.replacePendingLogin` and
`Service.CancelDeviceLoginAttempt` refuse to touch a newer login than the one they
were handed. Without it the older mint installs itself over the retry and cancels
it on the way in, and the user watches a code they never cancelled fail with "the
login was cancelled".

**Desktop.** The provider row shows *Sign in with ChatGPT* instead of a password
field, then the code, the verification URL, and when the code expires, with a
Cancel button. The deadline is the same clock time the terminal panel writes,
rendered from `DeviceLogin.ExpiresAt`: a user with both hosts open must not be told
two different things about one code, and an absolute instant stays true whether or
not anything redraws it. A server that named no expiry gets no deadline invented
for it in either host. A button opens the page in the user's browser; the flow does
**not** depend on it, and the binding takes a provider id rather than a URL so the
frontend cannot ask the process to open somewhere the authorization server did not
name.

Two things the panel does not do. The sign-in button dies while its code is being
minted — that round trip is the only part of the flow with nothing on screen, and
a second click through it would mint a second code, retire the first server-side,
and then blank the live one on its way out. And cancelling is not an error:
`AwaitProviderLogin` answers a login the user abandoned with nothing to report,
because the panel paints whatever a provider action rejects with in red next to
the row. `providerconfig.ErrLoginCancelled` is what makes that condition
recognizable to both hosts instead of a message either one has to match on.

## Secret hygiene

Nothing secret is rendered, logged or emitted: not an access or refresh token, not
an authorization code, not a code verifier. Errors from `internal/openaiauth` quote
a status and a flattened, truncated response body, and only for statuses that could
not have carried a grant — a `200` the build cannot parse names the status and
nothing else, because that is the shape the authorization code and its verifier
arrive in. `credentials.json` keeps its atomic 0600 write. The device code itself
*is* shown, which is the point of the flow — it is single-use and worthless without
the account that approves it.

The transcript's Details pane is the last stop, and it does not trust the layers
above it: `providerSecretPatterns` redacts every field name a credential arrives
under — `api_key`, `authorization`, `access`/`refresh`/`id_token`,
`authorization_code`, `code_verifier` — plus a bare `sk-` key and a bare JWT, which
is what catches a token with no field name around it.

The terminal end-to-end test asserts the absence: it fails if the refresh token,
the authorization code, the code verifier or the account id ever appear on screen.

## Terms of service

**We authenticate as the Codex CLI, with our own `originator`.** OpenAI offers no
client registration for this flow, so `app_EMoamEEZ73f0CkXaXp7hrann` is the only
client id that exists for it; `opencode` and `pi` do the same. `originator` is set
to `atenea` rather than Codex's value: the client id is unavoidable, but claiming
to *be* their program when we are not is avoidable, and a truthful originator is
what lets OpenAI attribute the traffic.

This is a real consideration and it is the user's account that carries the risk.
Using a ChatGPT subscription through a third-party client may not be what OpenAI
intends, and the remedy available to them is against the account, not against us.
It is recorded here, in the spec, rather than in a code comment, because it is a
product decision and not a fact about the protocol.

## What this does not close

- **Reasoning items are not replayed.** `include` asks for
  `reasoning.encrypted_content` and the adapter drops what comes back, because
  `llm.Message` has nowhere to carry an opaque per-turn blob. The model loses its
  chain of thought between turns of one conversation, which costs quality on long
  multi-step work and nothing in correctness. Closing it means a content part kind
  for opaque provider state in `agentcore/llm`, which is a contract change and its
  own decision.
- **The interactive login was never run against the real server.** Approving a
  code needs a person with a ChatGPT account, so the flow is covered against stubs
  that reproduce the verified shapes, end to end through the real binary. The
  `usercode` endpoint is the one part that was exercised live.
- **Two processes can still renew the same credential twice.** The gate is
  in-process, so a TUI and a desktop app that notice the same expiry in the same
  second both call `/oauth/token`; OpenAI rotates on the first and refuses the
  second. The loser re-reads the store and serves the rotation that landed, so the
  user sees nothing — but the wasted request is real, and a renewal that fails for
  a reason the store cannot answer (the other process rotated *and* then failed to
  write) still ends in "log in again". Closing it means a lock file shared by both
  binaries; the cost of the residue is one request.
- **No `/disconnect`.** Unchanged from the original `/connect` spec: the file is
  user-editable, and re-running the login rotates the credential.
- **Context windows are declared, not discovered.** The endpoint publishes
  neither, so the four models share the gpt-5.4 family's published window. It is
  the conservative reading — under-declaring costs an early compaction,
  over-declaring costs a failed turn — and it is one table to correct.

## Touch points

- `internal/openaiauth/` — the device-code client: mint, poll, exchange, refresh,
  and the account claim.
- `internal/llm/tokensource.go` — `OAuthToken`, `OAuthTokenSource`.
- `internal/llm/codex.go`, `internal/llm/capabilities.go` — the adapter and what
  it declares.
- `internal/providerconfig/credentials.go` — the `oauth` arm.
- `internal/providerconfig/oauth.go` — resolution, the margin, single flight,
  persistence of the rotation, and OpenAI's flow.
- `internal/providerconfig/devicelogin.go` — the login the hosts drive.
- `internal/providerconfig/registry.go` — `BuildParams`, `OAuthFlow`,
  `openai-codex`.
- `internal/providerconfig/connect.go` — `Kind`, and the two kinds refusing each
  other.
- `internal/tui/connect_panel.go`, `internal/tui/engine/engine.go` — the terminal
  branch.
- `app.go`, `frontend/src/features/settings/` — the desktop branch.

## Related

- [Provider credentials](../architecture/provider-credentials.md) — the arm and
  the resolver this widened.
- [Provider registry](../architecture/provider-registry.md) — how a declared wire
  format resolves to an adapter.
- [`/connect` command and credential store](2026-07-18-connect-command.md) — the
  flow this grew a second branch on.
