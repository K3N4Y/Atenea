---
updated_at: 2026-07-27
summary: Design specification for provider connection — the API-key flow, the shared credential store, and the production gating of .env loading.
---

# /connect: provider connection

## Problem

Until now the only way to supply a provider API key was the process
environment (helped by `.env` auto-loading in the working directory). That is
fine for development but wrong for a distributed binary: an end user should
connect a provider from inside the TUI, once, and a release binary should
never silently import secrets from whatever `.env` happens to be in the
directory it runs from.

## Decisions

- **Sources and precedence.** A provider API key resolves as: real
  environment variable first (the explicit, ephemeral override — same
  convention as `GH_TOKEN` or the AWS CLI), then the credential stored by
  `/connect`. Development builds additionally auto-load `.env` from the
  working directory into the environment (never overriding real variables),
  exactly as before.
- **Production gating.** `dotenv.Load` is compiled out by the `production`
  build tag — the same tag `wails build` already sets; the TUI release is
  built with `go build -tags production ./cmd/atenea`. In a release binary the
  `.env` code path does not exist. Runtime toggles were rejected: an end user
  must not be able to re-enable dev behavior on a production binary.
- **Storage.** Credentials live in `credentials.json` next to
  `providers.json` (user config directory), written atomically with 0600
  permissions in a 0700 directory, behind a `CredentialStore` interface so an
  OS-keyring backend can slot in later. Entries are keyed by provider id with
  a `type` discriminator (`api_key` today; an OAuth variant adds fields, not a
  migration). Decoding is lenient so older binaries read files written by
  newer ones. `Put` refuses to overwrite a corrupt file; `Get` degrades to
  "not connected". Secrets never enter `providers.json`.
  `[updated 2026-07-26]` The discriminator became a real tagged variant with a
  second arm: `exec` reads a bearer token from a command's standard output
  (R3.5). Resolution moved out of the store into a `CredentialResolver`, because
  a store persists and must not execute. `/connect` is unchanged and stays
  API-key-only — its masked input and its per-provider network check mean nothing
  for a credential whose answer is ephemeral — and `Connectable()` still reports
  "a credential is stored", whichever arm it declares. See
  [Provider credentials](../architecture/provider-credentials.md).
- **Scope.** Anthropic, OpenAI, OpenRouter, OpenCode Zen, and OpenCode Go are
  connectable by API key. The flow, storage, and resolution are shared by the TUI
  and Wails hosts through `providerconfig`, including the desktop
  `ConnectProvider` binding.
  `[updated 2026-07-27]` OAuth is no longer deferred: `openai-codex` connects a
  ChatGPT Plus/Pro subscription through a device-code login, which uses the
  `oauth` credential variant and leaves API-key files untouched — exactly the
  shape this note anticipated. `/connect` now branches on how a provider is
  connected, which `Connectable()` reports as `Kind` (`api_key` or
  `device_code`), so neither host infers it from a provider id. See
  [Driving atenea with a ChatGPT subscription](2026-07-27-openai-subscription-oauth.md).
- **UX.** `/connect` opens a full-screen panel listing connectable providers
  with their stored-credential state; `/connect openai` and the other provider
  ids jump straight to the key entry. The key is typed or pasted into a masked
  input owned by the panel — it never passes through the composer nor its
  persisted history.
  While validation is in flight the panel shows the state and ignores edits.
  `[updated 2026-07-27]` A provider whose credential is a login takes the other
  branch: selecting it starts the login at once (there is nothing to type) and the
  panel shows the page to open, the code to enter, and that it is waiting for
  approval; `esc` cancels the login and steps back to the list, and pressed while
  the code is still being minted it retires that attempt — by name, so a mint that
  answers late cannot cancel the code the user is looking at by then — instead of
  leaving one polling behind a closed panel.
  `esc` over a key *validation* closes the panel without silencing anything: the
  key is already at the provider and a rejection still reaches the transcript.
  Dropping it would let the user believe they connected until the next turn
  answered "no credential stored for provider".
- **Validation.** The key is checked against the provider before storing
  (OpenRouter: `GET {base_url}/key`; OpenAI: the read-only
  `GET {base_url}/models`; both send the key as Bearer; 401/403 = invalid key,
  other failures surface as-is). Nothing is persisted on failure — a bad
  key stored today is a confusing mid-chat failure tomorrow.
- **Post-connect.** With no active selection, the provider activates
  immediately on its default model — the first curated entry (for example
  `openrouter/free` or OpenAI's first shipped model) — and the selection is
  persisted; connect → chat with no intermediate step. If the connected
  provider is already selected, the live
  delegate is rebuilt so a rotated key applies without restart. A selection on
  another provider is untouched. The model catalog refreshes after a
  successful connect. Re-running `/connect` rotates the key; `/disconnect` is
  deliberately out of scope for v1 (the file is user-editable meanwhile).
- **First run.** A release binary with no key anywhere starts on the demo
  provider, as before, but seeds the transcript with a notice: no provider is
  connected, `/connect` (or `OPENROUTER_API_KEY`) fixes it, and replies are
  canned until then.

## Touch points

- `internal/providerconfig/credentials.go` — `Credential`, `CredentialStore`,
  `FileCredentialStore`, `DefaultCredentialsPath`.
- `internal/providerconfig/credentialresolver.go` — `CredentialResolver`, the
  `exec` arm's runner, its guardrails and its token cache.
- `internal/providerconfig/service.go` — env-then-credential resolution
  (`apiKeyFor`/`resolveAPIKey`) used by selection and the model catalog.
- `internal/providerconfig/connect.go` — `Connect`, `Connectable`, the
  connectable whitelist, and the per-provider validation strategy.
- `internal/llm/validate.go` — `ValidateOpenRouterKey` and `ValidateOpenAIKey`.
- `internal/tui/connect_panel.go` — the panel; `internal/tui/engine.go` — the
  optional `ConnectService` delegation.
- `internal/dotenv/load_dev.go` / `load_production.go` — the build-tag gate.
- `app.go` (Wails) — `ConnectProvider` delegates to the same service as the TUI.
