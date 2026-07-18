---
updated_at: 2026-07-18
summary: Design specification for the /connect command, the shared credential store, and the production gating of .env loading.
---

# /connect: provider connection by API key

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
- **Scope (v1).** Only OpenRouter is connectable, only by API key. The flow,
  storage, and resolution are generic: adding a provider means whitelisting
  its id and giving it a validation strategy. The `/connect` UX exists only in
  the TUI; the Wails app resolves stored credentials through the same
  `providerconfig` code, so a key connected in the TUI works there too.
- **UX.** `/connect` opens a full-screen panel listing connectable providers
  with their stored-credential state; `/connect openrouter` jumps straight to
  the key entry. The key is typed or pasted into a masked input owned by the
  panel — it never passes through the composer nor its persisted history.
  While validation is in flight the panel shows the state and ignores edits.
- **Validation.** The key is checked against the provider before storing
  (OpenRouter: `GET {base_url}/key` with the key as Bearer; 401/403 = invalid
  key, other failures surface as-is). Nothing is persisted on failure — a bad
  key stored today is a confusing mid-chat failure tomorrow.
- **Post-connect.** With no active selection, the provider activates
  immediately on its default model — the first curated entry
  (`openrouter/free`) — and the selection is persisted; connect → chat with no
  intermediate step. If the connected provider is already selected, the live
  delegate is rebuilt so a rotated key applies without restart. A selection on
  another provider is untouched. The model catalog refreshes after a
  successful connect. Re-running `/connect` rotates the key; `/disconnect` is
  deliberately out of scope for v1 (the file is user-editable meanwhile).
- **First run.** A release binary with no key anywhere starts on the demo
  provider, as before, but seeds the transcript with a notice: no provider is
  connected, `/connect` (or `OPENROUTER_API_KEY`) fixes it, and replies are
  canned until then.

## Touch points

- `internal/providerconfig/credentials.go` — `CredentialStore`,
  `FileCredentialStore`, `DefaultCredentialsPath`.
- `internal/providerconfig/service.go` — env-then-credential resolution
  (`apiKeyFor`/`resolveAPIKey`) used by selection and the model catalog.
- `internal/providerconfig/connect.go` — `Connect`, `Connectable`, the
  connectable whitelist, and the per-provider validation strategy.
- `internal/llm/validate.go` — `ValidateOpenRouterKey`.
- `internal/tui/connect_panel.go` — the panel; `internal/tui/engine.go` — the
  optional `ConnectService` delegation.
- `internal/dotenv/load_dev.go` / `load_production.go` — the build-tag gate.
- `app.go` (Wails) — `openRouterAPIKey` resolution shared-by-convention with
  the TUI.
