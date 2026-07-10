---
updated_at: 2026-07-09
summary: Specification for m0 scaffolding spec.
---

# Spec M0 — Scaffolding

Milestone **M0** executable specification. It defines the
final state, scope, TDD plan and acceptance criteria to leave
the package and dependency structure ready for M1..M10.

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

Related roadmap: [agent-loop roadmap](../plans/agent-loop-roadmap.md).

## 1. Context

The agent loop (see [agent-loop architecture](../architecture/agent-loop.md)) is built from the inside
out: types -> store -> provider -> publisher -> tools -> turn -> loop.
Before writing any logic, you need the skeleton of packages that the
rest of the milestones will fill, without touching Wails or real providers.

Today the repo does not have `internal/`. M0 creates it empty but compileable and testable.

## 2. Objective

Leave the tree `internal/` with the five packages of the design, each with a
package name set by a test, the dependency `golang.org/x/sync/errgroup`
declared in `go.mod`/`go.sum`, and the three green quality gates.

M0 **does not** add types, interfaces or business logic: that's M1 onwards.

## 3. Scope

### Inside

- Create the directories and packages:
 `internal/session`, `internal/session/runner`, `internal/llm`,
 `internal/tool`, `internal/event`.
- One `doc.go` file per package (package clause +
 responsibility comment) so that each package has a compileable no-test file.
- One trivial test per package that sets the package name.
- Add and pin `golang.org/x/sync/errgroup` as a dependency.

### Out (do not do in M0)

- Domain types (`Session`, `Message`, `Seq`, `SessionEvent`, `Store`, ...) — M1.
- Interface `Provider` and fake — M2.
- Publisher, registry, runTurn, Run, control signals — M3..M8.
- Any touch a `app.go`, `main.go`, Wails or the frontend — M9.
- SQLite or actual vendor adapter — M10.

## 4. Structure to create

| Route | Package | M0 Files | Future responsibility |
| --- | --- | --- | --- |
| `internal/session/` | `session` | `doc.go`, `scaffold_test.go` | Durable aggregate: Session, Message, Seq, inbox, history, epoch, store (M1) |
| `internal/session/runner/` | `runner` | `doc.go`, `scaffold_test.go` | External loop `Run`, `runTurn`, `publish` (M5..M8) |
| `internal/llm/` | `llm` | `doc.go`, `scaffold_test.go` | Interface `Provider`, `Request`, `Event` and adapters (M2, M10) |
| `internal/tool/` | `tool` | `doc.go`, `scaffold_test.go` | `Registry.Materialize`, `settle`, builtins (M4) |
| `internal/event/` | `event` | `doc.go`, `scaffold_test.go` | `EventBus` towards Wails (M9) |

The package name of the `runner` subdirectory is `runner`, not `session`.

### Reference content

`doc.go` (example for `session`; analogue in each package with its responsibility):

```go
// Package session concentra el dominio durable del agente: Session, Message,
// Seq, inbox, historial y epoch. M0 solo fija el paquete; los tipos llegan en M1.
package session
```

`scaffold_test.go` (example for `session`):

```go
package session

import "testing"

// TestSessionPackageCompiles fija el nombre del paquete y prueba que compila y
// es descubierto por `go test ./...`. El cuerpo queda vacio a proposito: M0 no
// agrega comportamiento. Se reemplaza por tests reales en M1.
func TestSessionPackageCompiles(t *testing.T) {}
```

`scaffold_test.go` from the `runner` package (also **pins** the
`errgroup` dependency with an actual import, so that `go mod tidy` doesn't remove it before
M5 uses it):

```go
package runner

import (
	"testing"

	"golang.org/x/sync/errgroup"
)

// TestRunnerPackageWiresErrgroup fija el nombre del paquete y deja cableada la
// dependencia errgroup que el loop de M5 usara para asentar tools concurrentes.
func TestRunnerPackageWiresErrgroup(t *testing.T) {
	var _ errgroup.Group
}
```

## 5. Dependencies

- `golang.org/x/sync/errgroup`: added now (requested by the roadmap) even if the
 first productive use is M5.
- **Anchoring**: a `go get` without a real import is lost with `go mod tidy`. That's why
 the `runner` test imports `errgroup` (`var _ errgroup.Group`). Thus the
 dependency is fixed at `go.mod`/`go.sum` and survives `go mod tidy`.

```bash
go get golang.org/x/sync/errgroup
go mod tidy   # debe dejar errgroup presente, no removerlo
```

## 6. TDD Plan

M0 is structural: several phases are mapped honestly and others are marked N/A.

### Safety net

- Green base state before touching anything.
- Command: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Expected result: they pass clean (only the current `main` packages exist).
 If something fails, it is reported as pre-existing and is not followed blindly.

### Understand

- Read `../architecture/agent-loop.md` ("Package Layout" section) and entry
 M0 of the roadmap.
- Expected behavior: five packages with their name fixed, without logic.

### NETWORK

- Before creating the packages, `go test ./internal/...` fails because the
 directory/package does not exist (there is nothing to compile).
- For `runner`, the `scaffold_test.go` that imports `errgroup` does not compile until
 the dependency is in the module.
- Command: `go test ./internal/...` -> expected error
 (`no such file or directory` / `cannot find package`).

### GREEN

- Create, package by package, its minimum `doc.go` + `scaffold_test.go`.
- `go get golang.org/x/sync/errgroup` for the `runner` test to compile.
- Command per package (preferred during the cycle):
 `go test ./internal/session`, then `./internal/session/runner`, etc.
- Result: each `go test ./internal/<pkg>` pass.

### TRIANGULATE

- Each additional package is a new case that proves that the
 structure generalizes (session -> runner -> llm -> tool -> event).
- Dependency edge case: `go mod tidy` **does not** have to remove `errgroup`
 (verified by the `runner` test import).
- Command: `go test ./...` (the entire suite) + `go mod tidy` and review `go.mod`.

### REFACTOR

- Cleanup without changing behavior: package comments consisting of
 each `doc.go`, `gofmt`, `go mod tidy` to sort `go.mod`/`go.sum`.
- Verify that the suite is still green after formatting.
- Command: `gofmt -w internal && go test ./...`.

## 7. Acceptance criteria (Done when)

1. There are the five packages with their `doc.go` and their `scaffold_test.go`.
2. `go test ./...` passes clean (includes the new packages).
3. `go vet ./...` passes without findings.
4. `gofmt -l .` does not print anything (everything formatted).
5. `go.mod` declares `golang.org/x/sync` and `go mod tidy` maintains it.
6. There were no changes to `app.go`, `main.go`, Wails or the frontend.

## 8. Commands

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Dependencia
go get golang.org/x/sync/errgroup

# Ciclo (por paquete y luego completo)
go test ./internal/session
go test ./internal/session/runner
go test ./internal/llm
go test ./internal/tool
go test ./internal/event
go test ./...

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go mod tidy         # errgroup permanece
```

## 9. Table of expected evidence

When closing M0, the response/PR should include this table with actual results:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Green preview suite | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Layout and input M0 read | `../architecture/agent-loop.md`, roadmap | identified structure |
| NETWORK | Non-existent packages do not compile | `go test ./internal/...` | expected failure |
| GREEN | `doc.go` + `scaffold_test.go` per package; `go get errgroup` | `go test ./internal/<pkg>` | each packet passes |
| TRIANGULATE | Five packages + dependency anchoring | `go test ./...`, `go mod tidy` | everyone passes, errgroup remains |
| REFACTOR | Formatting and doc comments without changing behavior | `gofmt -w internal`, `go test ./...` | green suite |

## 10. Risks and decisions

- **`go mod tidy` eliminates errgroup**: mitigated by the real import in the test of
 `runner` (explicit decision of M0, not M5). subpackage**: `internal/session/runner` is package `runner`; no
 reuse `session` for the subdirectory.
- **Ephemeral scaffold tests**: `scaffold_test.go` is replaced by
 behavior tests when each packet receives logic (M1+). Empty bodies for
 purpose meanwhile.

## 11. Sources

- Roadmap: `../plans/agent-loop-roadmap.md` (milestone M0)
- Architecture: `../architecture/agent-loop.md` (section "Package Layout")
- Way of working: `AGENTS.md`
- `golang.org/x/sync/errgroup`: https://pkg.go.dev/golang.org/x/sync/errgroup
