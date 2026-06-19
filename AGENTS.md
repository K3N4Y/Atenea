# AGENTS.md

How `atenea` is worked on. This is the default way of working for any agent
(human or AI) touching this repo. It is based on the skill
`.claude/skills/tdd-cycle-evidence/SKILL.md`: verifiable TDD with evidence.

## Stack

- Backend: Go 1.23+ (tested with go1.26).
- App: Wails v2.12 (Go + web frontend).
- Frontend: under `frontend/` (npm).
- Architecture docs: `docs/` (Spanish, no accents).

## Core rule: TDD with evidence

To implement features, fix bugs, or change existing code, follow the cycle in
order and **do not skip steps**:

`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`

### 1. Safety net

- If you modify existing files, run the relevant tests first.
- If they fail, report the failure as preexisting and do not keep editing blind.
- Record the command and the result before editing.

### 2. Understand

- Read the task, spec, acceptance scenarios, design, and existing patterns.
- Identify the expected behavior before writing tests.
- Follow the repo conventions for names, structure, helpers, and style.

### 3. RED

- Write a failing test first.
- The test describes the expected behavior, not the implementation.
- Do not write production code before the test.
- Run the specific test and capture the expected failure.

### 4. GREEN

- Write the minimum code to pass the test.
- Run the specific test, not the whole suite, unless it cannot be isolated.
- Keep the change small and focused on the red case.

### 5. TRIANGULATE

- Add additional cases: happy path and edge case.
- Use them to avoid false green from hardcoded code or weak tests.
- Run the specific tests after each new case.

### 6. REFACTOR

- Clean up without changing behavior.
- After each refactor, verify the tests still pass.
- Separate refactors from functional changes when possible.

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

## Evidence

In the progress and the final response, include the `TDD Cycle Evidence` table.
It must show at least RED, GREEN, TRIANGULATE, and REFACTOR; add Safety net and
Understand when they apply.

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Existing tests checked | `go test ./...` | pass/fail/preexisting |
| Understand | Relevant files and scenarios read | `<files>` | behavior identified |
| RED | Failing test written first | `<test file>` and `go test -run ...` | expected failure |
| GREEN | Minimal production code added | `<files>` and `go test -run ...` | specific test passed |
| TRIANGULATE | Additional cases added | `<test file>` and `go test -run ...` | cases passed |
| REFACTOR | Cleanup without behavior change | `<files>` and `go test ./...` | tests still passed |

If a step does not apply, mark it `N/A` and explain why. If tests cannot be
run, say so explicitly and show the blocker.
