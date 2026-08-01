---
updated_at: 2026-07-31
summary: How a stored provider secret becomes the credential an adapter authenticates with — the tagged Credential variant, the exec arm that covers Bedrock/Vertex/gateway auth, the oauth arm that carries a subscription login, and the resolver that separates persisting a credential from running or renewing one.
---

# Provider credentials

> Status: implemented 2026-07-26 (audit recommendation R3.5).
> See the [agnosticism and extensibility audit](../audits/2026-07-24-agnostic-extensibility-audit.md) §3.2 and §4 R3.
> Builds on the [provider registry](provider-registry.md) (R3.1) and the
> [`/connect` command](../specs/2026-07-18-connect-command.md), which introduced
> the store this widens.

## The problem it solves

R3.1 made the wire format extensible and R3.3 made the catalog data, so a build
with a Bedrock factory registered can declare the endpoint in JSON and select it.
It still could not *authenticate* it. A credential was:

```go
type Credential struct {
    Type   string `json:"type"`
    APIKey string `json:"api_key,omitempty"`
}
```

One arm, and the comment above it already promised more. Everything whose auth is
not a static string — Bedrock's SigV4, Vertex's application default credentials,
a corporate gateway that mints a 15-minute token — had nowhere to live. The
registry was extensible in theory and unusable in practice for exactly the
providers a third party would register.

## The shape

```go
const (
    CredentialTypeAPIKey = "api_key"
    CredentialTypeExec   = "exec"
    CredentialTypeOAuth  = "oauth"
)

type Credential struct {
    Type   string           `json:"type"`
    APIKey string           `json:"api_key,omitempty"`
    Exec   *ExecCredential  `json:"exec,omitempty"`
    OAuth  *OAuthCredential `json:"oauth,omitempty"`
}

type ExecCredential struct {
    Command        []string `json:"command"`
    TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
    TTLSeconds     int      `json:"ttl_seconds,omitempty"`
}

type OAuthCredential struct {
    AccessToken  string    `json:"access_token"`
    RefreshToken string    `json:"refresh_token"`
    ExpiresAt    time.Time `json:"expires_at,omitzero"`
    AccountID    string    `json:"account_id,omitempty"`
}

func (c Credential) Validate() error
```

```json
{
  "credentials": {
    "vertex": {
      "type": "exec",
      "exec": { "command": ["gcloud", "auth", "print-access-token"], "ttl_seconds": 1800 }
    }
  }
}
```

An exec credential does not implement anyone's auth protocol. It borrows the
implementation the user already has installed — `gcloud`, `aws`, an internal
helper — and reads a bearer token off its standard output. That is what makes it
worth shipping instead of OAuth: it covers the whole class at the cost of one
subprocess, and the vendor's own tool stays responsible for refresh, MFA and
whatever else it does.

### A tagged variant, not a bag of optional fields

Each arm's payload is nested under the name of the arm, and `Validate` enforces
that exactly the arm `Type` names is populated: an `api_key` credential with a
command is refused, and so is an `exec` credential carrying an `api_key`. The
flat alternative — `{Type, APIKey, Command, Timeout, TTL}` — is a shape where
every reader has to decide for itself which fields the type makes meaningful, and
where the first reader that gets it wrong resolves a credential the user did not
write. A third arm is a new pointer field and a new `case`, which is the same
promise the original comment made, now backed by a check.

`[updated 2026-07-27]` The third arm arrived: `oauth` carries a ChatGPT
subscription login (see
[Driving atenea with a ChatGPT subscription](../specs/2026-07-27-openai-subscription-oauth.md)),
and the exercise was as cheap as the shape promised — one field, one `case`, and
no reader of the other two arms changed. `Validate` refuses a login with no
refresh token (it dies within the hour with no way back); a zero `expires_at` is
accepted and reads as "renew on first use", because an unknown lifetime and an
expired one cost the same to get wrong.

`[updated 2026-07-31]` `account_id` stopped being the arm's requirement and became
the ChatGPT flow's. It is a routing header only that endpoint demands, and the
PostHog login (see
[Driving atenea with a PostHog account](../specs/2026-07-31-posthog-oauth-provider.md))
legitimately stores none — the gateway routes on the bearer alone. The protection
moved to where the fact lives: OpenAI's token exchange refuses to issue a
credential with no account, and the codex adapter refuses to send a request
without one.

**Validation runs at both ends, and deliberately not in the middle.** `Put`
refuses a malformed credential so one never reaches disk; resolution refuses one
too, because `credentials.json` is user-editable by design — that is the reason
`/disconnect` was never needed. Decoding stays lenient, with no
`DisallowUnknownFields` and no type check: the file is shared between builds and
between versions, and rejecting the whole file over one entry would take every
*other* provider's credential down with it. That is the same stance
`normalizeAndValidate` takes on an unknown provider type, for the same reason.

**An absent type and an unknown type are different errors.** "Declared nothing"
is not "declared `api_key` and forgot to say so": resolving it as an API key is
precisely how R2's collapsed-default bug gets reintroduced. An unknown type gets
the list of the types this build knows, the way an unknown wire format does.

### argv, never a shell

`Command` is an argv list executed directly. There is no shell, so there are no
quoting rules to learn, no word splitting to be surprised by and no injection
semantics to reason about in a file that already grants code execution. A user
who genuinely wants a pipeline writes `["sh", "-lc", "..."]`, which puts the
shell in the file where a reader can see it rather than in the reader's
assumptions.

## Resolving is not persisting

```go
type CredentialStore interface {
    Get(providerID string) (Credential, bool)
    Put(providerID string, credential Credential) error
}

type CredentialResolver struct{ /* store, runner, clock, token cache, refresh gates */ }

func (r *CredentialResolver) Token(ctx context.Context, providerID string) (string, error)
func (r *CredentialResolver) CachedToken(ctx context.Context, providerID string) (string, error)
func (r *CredentialResolver) OAuthTokenSource(providerID string, refresh OAuthRefresher) llm.OAuthTokenSource
```

The store was the obvious place to put resolution and it is the wrong one. A
store persists; making it execute would mean an OS-keyring backend — the reason
the interface exists — has to know what a subprocess is. So the store keeps
answering *what is stored*, including a credential this build cannot honor, and
`CredentialResolver` answers *what token to send*, dispatching on `Type`:
`api_key` resolves to itself, `exec` runs the command, and `oauth` resolves to
**nothing static** — see below.

`Service` holds both, as two fields, because they are two jobs: `/connect` reads
and writes the store, and everything that needs an actual bearer string goes
through the resolver. `Catalog` holds only the resolver — listing models never
writes a credential.

### Two entry points, because a listing and a conversation are not the same

`apiKeyFor` (listing) calls `CachedToken`; `resolveAPIKey` (selection) calls
`Token`. The asymmetry is the caching story, and it is deliberate rather than a
TTL picked to split the difference:

- **A catalog refresh walks every configured provider.** Resolving fresh there
  means one subprocess per exec-credentialed provider *every time the model
  picker opens*. A token read minutes ago is good enough to ask an endpoint which
  models it serves; if it has expired the listing gets a 401 and the picker still
  shows the curated models.
- **A selection bakes the token into the adapter.** `registry.Build(provider,
  model, apiKey)` produces the handle a whole conversation runs on, so the string
  it gets must be as fresh as this process can make it — and re-selecting the
  model has to be a way to recover from an expired token, which it would not be
  if it could be served from a cache.

The cache is keyed by provider id and fingerprinted by the command, so editing
`credentials.json` invalidates the entry instead of serving a token the user just
replaced. `ttl_seconds` declares the lifetime, defaulting to five minutes. There
is no way to switch the reuse off: what it protects against is not optional.

An `api_key` credential is never cached. Reading it is a file read that
`FileCredentialStore` performs on every call on purpose, so that the TUI and the
desktop app observe each other's writes.

### The resolution runs outside `s.mu`

`Service.Select` used to take the write lock and resolve inside it. With a
command in the path that would freeze every reader — the model picker, the
composer footer, a running turn asking what it is talking to — for up to the
resolution timeout. `applySelection` now resolves first and takes the lock only
to persist and swap, which is what `Connect` already did with its network
validation. `Connect` was restructured the same way: it stores the key under the
lock, releases it, and goes through `applySelection`.

`Open` grew a `context.Context` for the same reason it needed one at all:
activating the persisted selection can now run a command, and the caller has to
be able to bound it. Both hosts pass `context.Background()` from their
composition root, which is where a root context belongs.

## Guardrails

An exec credential turns `credentials.json` into code this user runs. What holds
that down:

- **A timeout.** 30 seconds by default — the commands this exists for talk to the
  network on a cold token cache — and `timeout_seconds` shortens it. The deadline
  is applied by the resolver rather than the runner, so it holds for an injected
  runner too, and `WaitDelay` bounds a surviving grandchild holding the output
  pipes open.
- **Standard output is trimmed, and must be one word.** Empty is refused.
  Anything with whitespace left in it after trimming is refused too: a bearer
  token is one opaque word, and splicing a command's warning banner into an
  `Authorization` header produces an unreadable transport error instead of this
  legible one.
- **The token never appears in an error.** A failure quotes the command's name
  and its standard error, flattened to one truncated line, and never its standard
  output. Errors from here reach a TUI panel and a log file.
- **The environment is inherited; standard input is not.** `gcloud` needs `HOME`
  and `PATH`, `aws` needs its profile variables — a command run in a scrubbed
  environment would be useless. Standard input is left closed so a command that
  decides to prompt fails on EOF instead of hanging until the deadline.
- **A group- or world-writable credentials file is refused.** Anyone who can
  write that file, or the directory holding it, can choose what this user
  executes. The check is on the exec arm only: a widened file is a
  confidentiality problem for an API key and a code-execution one for a command,
  and locking someone out of their own key would be the worse trade. Windows is
  exempt, because Go reports a synthetic mode there and the check would refuse
  everything.

The path is discovered through `CredentialFile`, an optional capability
(`CredentialPath() string`) resolved by type assertion — the idiom the tool and
provider capabilities already use. A keyring-backed store does not implement it
and has nothing to check.

### The oauth arm resolves per request instead

`[updated 2026-07-27]`

`Token` and `CachedToken` both answer `("", nil)` for an `oauth` credential, and
that is not a gap — it is the honest answer. A login has no static bearer: the one
it holds expires within the hour, and it travels with an account id that no string
could have carried. Answering "nothing static" is also what makes a provider
authenticated this way build on the keyless placeholder rather than on a secret its
adapter would ignore.

What an adapter gets instead is a seam:

```go
// internal/llm declares what an adapter may ask for
type OAuthToken struct{ AccessToken, AccountID string }
type OAuthTokenSource interface {
    OAuthToken(ctx context.Context) (OAuthToken, error)
}
```

`CredentialResolver.OAuthTokenSource` binds this resolver to one provider and
implements it. The direction matters: `internal/llm` declares the interface and
`providerconfig` implements it, so the adapter package never imports the storage
package and knows nothing about files, arms or refresh protocols.

Resolution renews the credential a configurable margin **before** it expires
(`DefaultOAuthRefreshMargin`, five minutes), because a token that expires
mid-request fails a turn that is minutes long. Four properties hold it together:

- **Single flight per provider, inside one process.** A main turn and its subagents
  share one adapter and notice the same expiry at the same instant. Without
  serialization each would refresh and each would rotate the refresh token, and
  every rotation but the last would already be retired — leaving the stored
  credential naming a token the server has dropped. Waiters re-read the store after
  the gate, which is also what makes them refresh from the *current* token rather
  than from the copy they read before waiting.
- **Across processes the race is absorbed, not prevented.** `oauthGate` is a
  `sync.Mutex` and `FileCredentialStore` is coordination-free by design, so the TUI
  and the desktop app can both notice the same expiry in the same second and both
  renew. OpenAI honors whichever lands first and answers the other `invalid_grant`
  about a credential that is, by then, perfectly good. After a failed renewal the
  resolver re-reads the store once and serves what it finds if another process
  already rotated it, which turns the collision into a no-op. A lock file would
  prevent the second request instead of absorbing it; it is a file to reason about,
  to clean up after a crash and to share between two binaries, for a collision that
  costs one wasted request.
- **The rotation is persisted, or the turn fails.** OpenAI rotates the refresh
  token on every renewal. A renewal the store could not write is reported rather
  than served: the credential is already broken at that point, and serving the turn
  anyway buys an hour and then a logout with no cause the user could see.
- **The renewal does not run on the caller's context.** `[updated 2026-07-27]` The
  ctx that reaches the resolver is the turn's, and the runner cancels it when the
  user presses Stop. The refresh runs on `context.WithoutCancel` of it under
  `oauthRefreshTimeout`, because a cancellation landing between the server
  committing the rotation and this process reading the body loses a token the
  server has already retired — the same silent logout the point above protects
  against, arriving through a different door.
- **A failed renewal says what to do.** "log in again" is the one thing a user can
  act on, so it is in the error rather than in a log line.

## What this does not close

**A resolved *string* still lives in the adapter until the next selection.**
`gcloud auth print-access-token` returns an hour; a gateway may return fifteen
minutes. A conversation that outlives an `api_key` or `exec` token fails at the
wire with the provider's own 401, and the fix a user has is `/model` — selecting
the same model again re-resolves and rebuilds the adapter, and so does `/connect`.
`[updated 2026-07-27]` The `oauth` arm closes this for itself, through the seam
above; doing the same for the exec arm means the same seam with a different
resolver behind it, and is a smaller job now that the shape exists.

**`/connect` is no longer only a paste-an-API-key flow.** `[updated 2026-07-27]`
It branches on how a provider is connected, which `Connectable()` now reports as
`Kind`, and the `oauth` arm gets a device-code login instead of a masked input. The
paragraph below still holds for `exec`, which has no login and no key to validate.

**`/connect` stays a paste-an-API-key flow for the exec arm.** It is a masked input plus a
per-provider network check, and neither half means anything for an exec
credential: "the command produced a token" is not a validation of the command,
because the answer is ephemeral and the next run is what matters. An exec
credential is declared by editing `credentials.json`, the same file `/connect`
writes and the same file the spec already treats as user-editable.
`Connectable()`'s notion of connected is unchanged and stays honest — a
credential is stored — whichever arm it declares.

**A keyless provider with a stored credential now uses it.** `resolveAPIKey`
consults the store whether or not the provider declares `api_key_env`, because a
provider authenticated by a command has no variable to name, and handing it the
keyless placeholder while a credential sat in the file would be a silently wrong
answer. The environment override still wins, and still wins *before* anything is
executed.

## Related

- [Provider registry](provider-registry.md) — how a declared wire format resolves
  to the adapter this token authenticates.
- [Provider catalog](provider-catalog.md) — which providers exist to hold a
  credential in the first place.
- [`/connect` command and credential store](../specs/2026-07-18-connect-command.md)
  — the flow that writes the `api_key` arm.
- [Driving atenea with a ChatGPT subscription](../specs/2026-07-27-openai-subscription-oauth.md)
  — the flow that writes the `oauth` arm, and the per-request seam it resolves
  through.
