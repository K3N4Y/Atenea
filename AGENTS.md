# AGENTS.md

Repo map, scope, and local conventions. Engineering principles live in the
system prompt (`internal/session/prompt/`), not here.

## Scope

- Active work happens in the Go core and the standalone TUI: `agentcore/`,
  `internal/`, and `cmd/atenea`.
- The Wails desktop app (root `main.go`, `app.go`, `frontend/`) is out of
  scope: do not inspect, modify, build, or test it unless the user explicitly
  expands the scope.
- Go version: see `go.mod`. `ripgrep` must be available on `PATH` (the grep
  and glob tools shell out to it).

## Map

Go straight to the right package instead of exploring:

- `agentcore/` — public contract surface (`llm`, `memory`, `permission`,
  `session`, `tool`). It must not import `internal/` or third-party modules;
  `agentcore/boundary_test.go` enforces this (ADR 0001).
- `internal/session/` — agent loop and session state: `prompt/` (system-prompt
  assembly; the embedded `.txt` files are the runtime prompts), `subagent/`
  (task tool and supervisor), `runner/`, compaction, memory, stores.
- `internal/tool/` — built-in tools; each tool's model-facing description is
  the `.txt` file next to its implementation.
- `internal/llm/` — model providers; provider auth lives in
  `internal/openaiauth/` and `internal/posthogauth/`, config in
  `internal/providerconfig/`.
- `internal/tui/` — terminal UI. `internal/wiring/` — composition root.
- `internal/agent/` + `agents/` — subagent catalog and the embedded manifests
  (explorer, coder, tester, reviewer, ...).
- `cmd/atenea` — standalone TUI entry point.
- Docs: `.okf/` is the documentation source of truth, indexed by
  `.okf/README.md`. `CONTEXT.md` is the domain glossary. `research/` holds
  informal notes and reference material. `docs/agents/` has the issue tracker
  and triage labels.

## Conventions

- Write new code, comments, errors, tests, and docs in English. The repo is
  migrating from Spanish; migrate old text only when you already touch it.
- Tests live next to the code, are named by behavior, and concurrent code runs
  with `-race`.
- New tools, model providers, session stores, and permission backends must
  pass the contract kit in their package (`contract.go` / `contract_test.go`).
- The prompt variants in `internal/session/prompt/` (`default.txt`,
  `anthropic.txt`, `local.txt`) must stay equivalent on exploration and
  verification rules; when you change one, mirror the others.
- Commit messages in English, with no AI attribution or co-author trailers.

## Commands

```bash
go test ./agentcore/... ./internal/... ./cmd/atenea/...
go test -race ./agentcore/... ./internal/... ./cmd/atenea/...
go vet ./agentcore/... ./internal/... ./cmd/atenea/...
gofmt -l agentcore internal cmd *.go
go build -tags production -o ./build/bin/atenea ./cmd/atenea
```

The `production` build tag selects the production dotenv and TUI cache-stats
variants; development is the default. The user's installed binary comes from
`go install -tags production ./cmd/atenea` and lands in `~/go/bin/atenea`.

## Documentation

Update `.okf/` only when a change materially affects context needed for future
work; routine code changes need no doc update. New `.okf/` documents need
`updated_at` and `summary` metadata and an entry in `.okf/README.md`.
