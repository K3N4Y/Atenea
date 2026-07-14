# AGENTS.md

How `atenea` is worked on. This is the default way of working for any agent
(human or AI) touching this repo.

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
- Project documentation: `.okf/`. Consult its index first when you need
  product, architecture, specifications, plans, or research context; then read
  the relevant document in its category.
- Update the relevant `.okf/` documentation whenever a change affects the
  documented behavior, architecture, specification, plan, or research.

## Commands

Tests (Go uses `_test.go` next to the code; tests are the source of truth):

```bash
# Whole suite (broad safety net)
go test ./...

# A focused test during development
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
- Documentation lives in `.okf/`, uses Markdown files, and is written in
  English.
