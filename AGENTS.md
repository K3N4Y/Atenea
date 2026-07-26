# AGENTS.md

How `atenea` is worked on. This is the default way of working for any agent
(human or AI) touching this repo.

## Guidelines

- When making a technical decision, give little weight to development cost.
  Prefer quality, simplicity, robustness, scalability, and long-term
  maintainability.
- When fixing a bug, start by reproducing it end to end, as close as possible to
  how a real user hits it. That is what makes sure you found the real problem, so
  the fix actually solves it.
- When testing the product end to end, be picky about the UI and obsessive about
  pixel perfection. If something clearly looks off, get it fixed even when it is
  not what you were working on.
- Hold engineering excellence to that same standard: lint, test failures, and
  test flakiness. If you see one, fix it even when you did not cause it.
- Avoid excessive or redundant comments. Code should explain itself through
  naming and structure; comment only the non-obvious logic (why, not what).

## Stack

Two hosts share one agent core:

- **Core**: Go 1.25+. `go.mod` is the source of truth for the version, and CI
  reads it rather than pinning its own.
- **Desktop app**: Wails v2.12 — root package (`main.go`, `app.go`) plus
  `internal/wailssession` and `internal/wailsworkspace`.
- **TUI**: `cmd/atenea` over `internal/tui`, shipped as a standalone binary
  (`.goreleaser.yml`, `install.sh`).
- **Frontend**: `frontend/` — Vue 3, Vite, TypeScript, Pinia, Vitest.

A change to shared behavior has to hold for both hosts, not only the one in
front of you.

## Architecture

`agentcore/` is the published contract surface: contracts public, loop private.
A tool, a model provider, the durable event stream and the ask-before-run gate
live there because a third party implements or reads them. Everything that runs
the agent — turn loop, stores, wiring, UIs, and the tools and adapters atenea
ships — stays under `internal/`, free to move.

Two invariants, both enforced by `agentcore/boundary_test.go`:

- No package under `agentcore/` imports `internal/`.
- No package under `agentcore/` imports a third-party module.

A type that would break either one belongs on the private side. See
[Published contracts](.okf/architecture/public-contracts.md).

## Commands

Two prerequisites, or the root package will not build:

- `frontend/dist` must exist. It is gitignored and `main.go` embeds it; an empty
  directory is enough (`mkdir -p frontend/dist`).
- `ripgrep` must be on `PATH`. The grep and glob tools shell out to `rg`.

Go (`_test.go` next to the code; tests are the source of truth):

```bash
go test ./...                                    # whole suite
go test -race ./...                              # concurrent code (runner, tools)
go test -run TestName ./internal/session/runner  # focused, add -v for detail
```

Frontend, from `frontend/`:

```bash
npm test              # vitest
npm run lint          # eslint
npm run format:check  # prettier
```

Running each host:

```bash
wails dev    # desktop, with frontend hot reload
wails build  # desktop production build

go build -tags production -o ./build/bin/atenea ./cmd/atenea  # TUI
```

Quality gates before closing a change. This is exactly what CI enforces, so a
change that skips one of them is a change that breaks the build:

```bash
gofmt -l .    # must be empty
go vet ./...
go test ./... && go test -race ./...
cd frontend && npm run lint && npm run format:check && npm test
```

## Conventions

- Tests next to the code: `foo.go` -> `foo_test.go`, same package or `_test`.
- Name tests by behavior: `TestRunner_CancelDuringTurnFailsInFlightTool`.
- Concurrent code (goroutines, channels, `errgroup`) is tested with `-race`.
- A new tool, model provider or session store runs its contract kit
  (`tooltest.Contract`, `llmtest.Contract`, `sessiontest.StoreContract`) on top
  of its own tests: the kit covers what every implementation owes the host, the
  tests cover what this one does. See
  [Contract test kits](.okf/architecture/public-contracts.md#contract-test-kits).
- The Wails boundary (`runtime.EventsEmit`) lives in `internal/event`; test the
  runner against a fake `EventBus`, not against Wails.
- Everything new is written in English: code, comments, errors, tests, docs and
  commit messages. Spanish comments that predate this rule are migrated as their
  files get touched, not in a sweep of their own.
- Commit subjects follow conventional commits, scoped by package:
  `feat(providerconfig): ship the default provider catalog as embedded data`.
- Never add your agent name as a commit co-author.

## Documentation

`.okf/` is the source of truth for product, architecture, specification, plan
and research context. Consult [its index](.okf/README.md) first, then read the
document in the relevant category. Every document opens with `updated_at` and
`summary` YAML metadata.

Update the affected `.okf/` document in the same change that alters the
behavior, architecture, specification, plan or research it describes. A new
document is also added to the index.

## Agent skills

- **Issues**: GitHub Issues (`K3N4Y/Atenea`, via the `gh` CLI). See
  `docs/agents/issue-tracker.md`.
- **Triage labels**: `needs-triage`, `needs-info`, `ready-for-agent`,
  `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.
- **Domain docs**: a `CONTEXT.md` glossary at the repo root and ADRs under
  `.okf/architecture/adr/`, when they exist. `/domain-modeling` creates them
  lazily, as terms and decisions actually get resolved, so their absence is not
  something to flag. See `docs/agents/domain.md`.
