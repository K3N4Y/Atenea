# AGENTS.md

How `atenea` is worked on. This is the default way of working for any agent
(human or AI) touching this repo. It is based on the skill
`.claude/skills/tdd-cycle-evidence/SKILL.md`: verifiable TDD with evidence.

## Guidelines
- when making a technical decisions, do not give much weight to development cost.
instead, prefer Quality, simplicity, robutnes, scalability, and long term maintainability. 
- when writing a commit mesagges, NEVER auto-add your agent name as co-author
- when doing a bug fixes, always start with reproducing the bug in an E2E settings as closely aligned it how and end use this
makes sure you find the real problem so your fix will actually solve it.
- when end-to-end testing a product, be picky about the UI you see and be obsessed with the pixel perfection.
if something clearly look off, even if it is not directly related to what you are doing, try to get it fixed along.
- apply that same high standard to engineering excellence: lint, test failure, and test flakiness.
if you see one, even if it is no caused by what you are working on right now, still get it fixed.


## Stack
- Backend: Go 1.23+ (tested with go1.26).
- App: Wails v2.12 (Go + web frontend).
- Frontend: under `frontend/` (npm).
- Architecture docs: `docs/` (Spanish, no accents).

## Core rule: TDD with evidence

To implement features, fix bugs, or change existing code, follow the verifiable
TDD cycle with evidence. Do not skip steps:

`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`

The full cycle, the per-phase gates, and the `TDD Cycle Evidence` table live in
the skill `.claude/skills/tdd-cycle-evidence/SKILL.md`. That skill is the source
of truth: read it and follow it, and include the evidence table in the progress
and the final response.

## Commands

Tests (Go uses `_test.go` next to the code; tests are the source of truth):

```bash
# Whole suite (broad safety net)
go test ./...

# A single test (RED/GREEN, preferred during the cycle)
go test -run TestName ./internal/session/runner

# With detail when the failure needs to be visible
go test -run TestName -v ./...

# Data race checks in concurrent code (runner/tools use goroutines)
go test -race ./...
```

Quality gates before closing a change:

```bash
gofmt -l .          # must be empty
go vet ./...        # must pass clean
```

Wails app:

```bash
wails dev           # development with frontend hot reload
wails build         # production build
```

## Conventions

- Tests next to the code: `foo.go` -> `foo_test.go`, same package or `_test`.
- Name tests by behavior: `TestRunner_StopsAtStepLimit`.
- Concurrent code (goroutines, channels, `errgroup`) is tested with `-race`.
- The Wails boundary (`runtime.EventsEmit`) lives in `internal/event`; test
  the runner against a fake `EventBus`, not against Wails.
- Docs in Spanish without accents, same as `docs/`.
