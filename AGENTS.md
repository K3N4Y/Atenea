# AGENTS.md

## Scope

- Work only on the Go core and standalone TUI (`cmd/atenea`, `internal/tui`).
- Do not inspect, modify, build, test, or document the desktop or frontend
  unless the user explicitly expands the scope.
- Go version: see `go.mod`. `ripgrep` must be available on `PATH`.

## Architecture

- `agentcore/` is the public contract surface; implementations belong under
  `internal/`.
- `agentcore/` must not import `internal/` or third-party modules. See
  `.okf/architecture/public-contracts.md`.

## Workflow

- Prefer correctness, simplicity, robustness, and maintainability.
- Choose the simplest implementation that fully meets the current requirements.
  Avoid speculative abstractions, configuration, and indirection.
- Grow the system in layers. Start from the smallest version that works end to
  end, and add each new capability on top of a product that already works.
  Never trade a working product for unfinished complexity.
- Make architectural decisions for the long term. Do not accept a stopgap that
  only works for now and is meant to be replaced later.
- Do not preserve backward compatibility. Remove obsolete paths instead of
  adding compatibility layers, fallbacks, or migrations.
- Keep components modular and concerns clearly separated.
- Prefer established, well-maintained libraries when they reduce overall
  complexity or improve reliability. Do not reimplement common functionality
  without a clear reason.
- Lean on the dependencies already in the project before writing your own
  implementation or adding packages. Do not assume a library lacks a capability
  without checking its documentation and types.
- Reproduce bugs end to end before fixing them.
- Keep comments rare and explain why, not what.
- Write new code, comments, errors, tests, and docs in English.
- Put tests next to code, name them by behavior, and use `-race` for concurrent
  code.
- New tools, model providers, and session stores must run their contract kit.
- Never add the agent as a commit co-author.

## Commands

```bash
go test ./agentcore/... ./internal/... ./cmd/atenea/...
go test -race ./agentcore/... ./internal/... ./cmd/atenea/...
go vet ./agentcore/... ./internal/... ./cmd/atenea/...
gofmt -l .
go build -tags production -o ./build/bin/atenea ./cmd/atenea
```

## Documentation

Consult or update `.okf/` only when a change materially affects context needed
for future work. Routine code changes require no `.okf/` update. New documents
must include `updated_at` and `summary` metadata and be added to `.okf/README.md`.

## Project references

- Issues and triage: `docs/agents/issue-tracker.md`,
  `docs/agents/triage-labels.md`
- Domain glossary and ADRs: `docs/agents/domain.md`
