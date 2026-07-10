---
updated_at: 2026-07-09
summary: Specification for m1 tipos store spec.
---

# Spec M1 — Types + Store in memory

`../plans/agent-loop-roadmap.md` Milestone **M1** Executable Spec. Defines the
final state, scope, TDD plan and acceptance criteria to leave the domain's
durable backbone: `Seq`, `Session`, `Message`, `SessionEvent` and an in-memory
`Store` that aggregates events and reprojects messages.

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

M0 left the `internal/` tree with five empty but compileable packages. The loop
(see `../architecture/agent-loop.md`) is built from the inside out: the first
brick is the durable domain of the session.

The central architectural decision ("Durable Persistence" section) is that **the
source state is events, not living memory between turns**. The runner reconstructs
the supplier's request each turn by reading from `Store`. That's why M1 sets two
things: the domain types and a `Store` that behaves as a log append-only
with a projection of messages derived from that log.

`Store` boots as an in-memory implementation. SQLite is M10 and comes in behind
of the same interface without touching the rest.

## 2. Objective

Leave at `internal/session`:

- the types `Seq`, `Role`, `Message`, `SessionEvent`, `Session`;
 - the interface `Store` with `AppendEvent`, `LoadSession` and `Messages`; `Seq` monotonic per session when adding an event,
 - reprojects messages from the event log in order of `Seq`,
 - filters by `sinceSeq` and reports non-existent sessions with a sentinel error;
 - behavioral tests that replace M0's `scaffold_test.go`, including
 a concurrent case run with `-race`.

M1 **does not** add provider, publisher, tools, inbox, epoch, runner or Wails.

## 3. Scope

### Inside

- `internal/session/session.go`: domain types (`Seq`, `Role`, `Message`,
 `SessionEvent`, `Session`).
- `internal/session/store.go`: interface `Store` + `ErrSessionNotFound`.
- `internal/session/memstore.go`: `MemoryStore` + `NewMemoryStore`.
- Behavior tests on `internal/session` (replaces `scaffold_test.go`).
- Compilation assertion `var _ Store = (*MemoryStore)(nil)`.

### Out (do not do in M1)

- Interface `Provider`, `Request`, `Event` and fake — M2.
- Publisher and taxonomy of streaming events
 (`Step.* / Text.* / Reasoning.* / Tool.*`) and delta coalescing — M3.
- `Registry.Materialize`, `settle`, builtins — M4.
- `runTurn`, `Run`, `MaxSteps`, continued — M5/M6.
- `Inbox` (`Admit`/`HasPending`/`Promote`) — M6.
- `ContextEpoch` and control signals (`errRebuildTurn`,
 `errContinueAfterCompaction`) — M7.
- `History.EntriesForRunner` (baseline/compaction projection) — M5.
- `Store` SQLite and real provider adapter — M10.
- Any touch to `app.go`, `main.go`, Wails or the frontend — M9.

## 4. Domain types

`internal/session/session.go`:

```go
package session

// Seq es la secuencia monotonica que el Store asigna a cada evento de una
// sesion. Empieza en 1 y crece de a 1 por sesion. Define el orden total durable
// del historial; el filtro sinceSeq de Messages se expresa contra este valor.
type Seq int64

// Role es el autor de un mensaje proyectado.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Message es la proyeccion de la conversacion para un turno. En M1 lleva texto
// plano y el Seq del evento que lo materializo (ordena y filtra por sinceSeq).
// Las partes ricas (tool calls, reasoning) y el coalescing de deltas de
// streaming llegan con el publisher en M3, sin cambiar esta forma minima.
type Message struct {
	ID   string
	Role Role
	Text string
	Seq  Seq
}

// SessionEvent es el evento durable: la unica fuente de verdad de la sesion. El
// Store asigna SessionID y Seq al agregarlo; el llamador los deja en cero. En M1
// un evento puede materializar un mensaje (Message != nil) o no aportar a la
// proyeccion (Message == nil). La taxonomia de streaming
// (Step.* / Text.* / Reasoning.* / Tool.*) la agrega el publisher en M3 sobre
// esta misma estructura.
type SessionEvent struct {
	SessionID string
	Seq       Seq
	Message   *Message
}

// Session es el agregado durable de una conversacion. En M1 es minimo: solo el
// ID. Agente, modelo y workspace se agregan cuando el runner los necesita
// (M5/M7), no antes.
type Session struct {
	ID string
}
```

Design decision: in M1 a `SessionEvent` that materializes a message loads the
`Message` already formed and the projection is **an event -> a message**. It's not a
casual simplification: it leaves the contract "Messages is derived from the log, not
stored separately." M3 enriches the fold to coalesce multiple deltas
(`Text.Started/Delta/Ended`) into a single message **without** changing the
`Store` interface or the shape of `SessionEvent`.

## 5. Interface Store and in-memory implementation

`internal/session/store.go`:

```go
package session

import (
	"context"
	"errors"
)

// ErrSessionNotFound se devuelve al leer una sesion que nunca recibio un evento.
var ErrSessionNotFound = errors.New("session not found")

// Store es la persistencia durable de la sesion. El log de eventos es la fuente
// de verdad; los mensajes son una proyeccion derivada. M1 implementa una version
// en memoria; M10 agrega SQLite detras de esta misma interface.
type Store interface {
	// AppendEvent agrega ev al log durable de sessionID, le asigna el siguiente
	// Seq monotonico y lo devuelve. Crea la sesion si es su primer evento. El
	// SessionID y Seq que traiga ev se ignoran: los fija el Store.
	AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error)

	// LoadSession devuelve el agregado de la sesion. ErrSessionNotFound si la
	// sesion nunca recibio un evento.
	LoadSession(ctx context.Context, sessionID string) (Session, error)

	// Messages reproyecta los mensajes de la sesion en orden de Seq y devuelve
	// solo los materializados por eventos con Seq > sinceSeq. sinceSeq = 0
	// reconstruye el historial completo desde cero. ErrSessionNotFound si la
	// sesion no existe.
	Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error)
}
```

`internal/session/memstore.go`:

```go
package session

import (
	"context"
	"sync"
)

// MemoryStore es la implementacion en memoria del Store para M1..M9. Guarda el
// log de eventos por sesion bajo un mutex y deriva los mensajes al vuelo.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string][]SessionEvent
}

// NewMemoryStore crea un store vacio listo para usar.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string][]SessionEvent)}
}

// var _ Store = (*MemoryStore)(nil) asegura en tiempo de compilacion que
// MemoryStore cumple la interface.
var _ Store = (*MemoryStore)(nil)

func (s *MemoryStore) AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log := s.sessions[sessionID]
	seq := Seq(len(log) + 1)
	ev.SessionID = sessionID
	ev.Seq = seq
	s.sessions[sessionID] = append(log, ev)
	return seq, nil
}

func (s *MemoryStore) LoadSession(ctx context.Context, sessionID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[sessionID]; !ok {
		return Session{}, ErrSessionNotFound
	}
	return Session{ID: sessionID}, nil
}

func (s *MemoryStore) Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log, ok := s.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	var out []Message
	for _, ev := range log {
		if ev.Message == nil || ev.Seq <= sinceSeq {
			continue
		}
		m := *ev.Message
		m.Seq = ev.Seq
		out = append(out, m)
	}
	return out, nil
}
```

Notes:

- `ctx` is not used in memory, but is preserved in the signature for fidelity to the
 interface (SQLite on M10 if you need it). Does not fire `go vet`.
- A single mutex serializes append and read: guarantees `Seq` without gaps or duplicate
 and leaves the path for the `-race` check.
- The session is created lazily in the first `AppendEvent`. Reading a session
 that never received an event is the "nonexistent session" case.

## 6. Reprojection model

`Messages` does not save messages: it **derives** them from the log each time. Rules:

- iterates through the events in insertion order (which coincides with the order of `Seq`); consistent.

Key property (Roadmap Done when): With `sinceSeq = 0`, `Messages`
rebuilds the entire history from scratch from the log. There is no status of
live messages outside the log.

## 7. TDD Plan

### Safety net

- Green base state before touching anything. M1 replaces
 `internal/session/scaffold_test.go`, so the existing one is run first.
- Command: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Expected result: they pass clean (M0 packets). If something fails, it is reported as
 pre-existing and is not followed blindly.

### Understand

- Read roadmap entry M1, "Main Types" and "Durable Persistence"
 from `../architecture/agent-loop.md`, and "Session / Durable Input / Projected History
" from `../architecture/opencode-agent-loop.md`.
- Expected behavior: log append-only with monotonic `Seq` and derived
 messages; nonexistent session reads fail with a sentinel.

### NETWORK

- Write the test that fails first:
 `TestMemoryStore_AppendEventAssignsMonotonicSeq`. Reference to
 `NewMemoryStore`, `AppendEvent` and `SessionEvent`, which do not yet exist -> does not
 compile -> fails (Honest RED in Go is a compilation failure of the
 test package).
- Command: `go test -run TestMemoryStore_AppendEventAssignsMonotonicSeq ./internal/session`
 -> expected failure.

### GREEN

- Write the minimum: `session.go` (types), `store.go` (interface +
 `ErrSessionNotFound`), `memstore.go` (`MemoryStore` with `AppendEvent` that
 assigns `Seq` and `Messages` that reprojects).
- Run only the red test until green.
- Command: `go test -run TestMemoryStore_AppendEventAssignsMonotonicSeq ./internal/session`.

### TRIANGULATE

Add cases to avoid false green:

- `TestMemoryStore_MessagesReturnsInSeqOrder`: several events with and without message;
 `Messages(0)` returns the messages in order of `Seq`.
- `TestMemoryStore_MessagesSinceSeqFiltersOlder`: `Messages(k)` returns only the
 of `Seq > k`.
- `TestMemoryStore_MessagesSinceSeqBeyondLastReturnsEmpty`: `sinceSeq` greater than
 the last `Seq` -> empty slice, no error.
- `TestMemoryStore_UnknownSessionReturnsNotFound`: `LoadSession` and `Messages`
 on a session without events -> `ErrSessionNotFound` (`errors.Is`).
- `TestMemoryStore_ConcurrentAppendsAssignUniqueSeqs`: N goroutines do
 `AppendEvent` on the same session; the returned `Seq` form exactly
 `{1..N}` with no gaps or duplicates. It is run with `-race`.
- Commands:
 - `go test -run TestMemoryStore ./internal/session`
 - `go test -race -run TestMemoryStore_ConcurrentAppendsAssignUniqueSeqs ./internal/session`

### REFACTOR

- Cleanup without changing behavior: package comment in `doc.go`
 updated (no longer "types arrive in M1"), assertion `var _ Store`,
 names and consistent test helpers.
- Verify that the suite is still green after formatting.
- Command: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Acceptance criteria (Done when)

1. There are `Seq`, `Role`, `Message`, `SessionEvent`, `Session` in
 `internal/session/session.go`.
2. There is the `Store` interface with `AppendEvent`, `LoadSession`, `Messages` and the
 sentinel `ErrSessionNotFound` on `store.go`.
3. `MemoryStore` implements `Store` (verified by `var _ Store`).
4. `AppendEvent` allocates `Seq` monotonic per session starting at 1, without gaps
 or duplicates, even under concurrent appends (clean `-race`).
5. `Messages(0)` rebuilds the entire history in order of `Seq`;
 `Messages(k)` filters by `Seq > k`; `sinceSeq` greater than last returns
 empty without error.
6. `LoadSession` and `Messages` on non-existent session return
 `ErrSessionNotFound`.
7. M0's `scaffold_test.go` was replaced by behavioral tests.
8. `go test ./...` (and `-race` where applicable) passes; `go vet ./...` clean;
 `gofmt -l .` empty.
9. There were no changes to `app.go`, `main.go`, Wails or the frontend; nor in other
 `internal/` packages outside of `session`.

## 9. Commands

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Ciclo (test especifico primero)
go test -run TestMemoryStore_AppendEventAssignsMonotonicSeq ./internal/session
go test -run TestMemoryStore ./internal/session

# Concurrencia
go test -race -run TestMemoryStore_ConcurrentAppendsAssignUniqueSeqs ./internal/session

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
go test -race ./internal/session
```

## 10. Table of expected evidence

When closing M1, the response/PR should include this table with actual results:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Green M0 suite before editing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Types and persistence read | roadmap M1, `../architecture/agent-loop.md`, `../architecture/opencode-agent-loop.md` | identified behavior |
| NETWORK | Monotonic Seq test written first | `session_test.go` + `go test -run TestMemoryStore_AppendEventAssignsMonotonicSeq ./internal/session` | expected failure (does not compile) |
| GREEN | Minimum rates + `Store` + `MemoryStore` | `session.go`, `store.go`, `memstore.go` | specific test passes |
| TRIANGULATE | Order, `sinceSeq`, not-found and concurrency | `go test -run TestMemoryStore ./internal/session`, `go test -race ...` | cases pass, `-race` clean |
| REFACTOR | `doc.go`, assertion `var _ Store`, format | `gofmt -w internal`, `go vet ./...`, `go test ./...` | green suite |

## 11. Risks and decisions

- **One-to-one projection in M1**: it is chosen on purpose so that the `Store` remains
 as log append-only with derived messages, without pre-building the
 deltas machinery. M3 enriches the fold (coalescer `Text.*`) without changing the interface.
- **Lazy session creation**: the first `AppendEvent` creates the session. Avoid
 a `CreateSession` that M1 does not need and leaves "nonexistent session" as a
 read over empty log. If M5/M9 ask for explicit lifecycle, it is added there.
- **`Session` minimum (ID only)**: agent/model/workspace are from the runner (M5) and
 from the epoch (M7). Adding them now would be speculating; they are added when a test requires them
.
- **`Seq` is not trusted from the caller**: `AppendEvent` ignores incoming `SessionID`/`Seq`
 and sets them by the store. Thus the total order is the sole responsibility of the
 store and cannot be corrupted from the outside.
- **Concurrency with mutexes, not channels**: for an in-memory store a `sync.Mutex`
 is the simplest thing that guarantees monotonic `Seq`. It is validated with `-race`. SQLite
 (M10) will move serialization to transactions behind the same interface.
- **`ctx` no memory usage**: kept in the signature by the interface; M10 honors it
 (SQLite cancellation/timeout). It is not a debt, it is a contract.

## 12. Sources

- Roadmap: `../plans/agent-loop-roadmap.md` (milestone M1)
- Architecture: `../architecture/agent-loop.md` (sections "Main types" and
 "Durable persistence")
- Reference loop: `../architecture/opencode-agent-loop.md` (sections "Session", "Input
 durable", "Projected history")
- Way of working: `AGENTS.md`
- Spec previous: `atenea-m0-scaffolding-spec.md`
