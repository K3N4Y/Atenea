---
updated_at: 2026-07-09
summary: Specification for m7 control signals spec.
---

# Spec M7 — Control signals (rebuild / compaction)

`../plans/agent-loop-roadmap.md` Milestone **M7** Executable Spec. Defines the
final state, scope, TDD plan and acceptance criteria to harden
the M5/M6 shift with the **internal control signals**: `errRebuildTurn` and
`errContinueAfterCompaction` (sentinels checked with `errors.Is`) and the
`ContextEpoch`. The happy turn of M5 (`runTurn`) becomes an **attempt**
(`runTurnAttempt`) wrapped in a retry loop: the attempt snapshots the epoch when
starts preparing the request, re-reads it just before calling the provider and, if
changes (agent, model or revision), discards the request and rebuilds **without** having called
 `Stream`. If the request exceeds the context before starting the
wizard message, compact the history and retry once via the post-compaction route. The
central rule: **no request is executed representing old session state**.

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

The previous milestones left, from the inside out, all the pieces that the turn
assembles and that the external loop orchestrates:

- **M1** left the durable domain (`Seq`, `Message`, `Role`, `SessionEvent`,
 `Store`, `MemoryStore`): the event log is the only source of truth and the
 messages are a derived projection (`Store.Messages(sinceSeq)`).
- **M2** left the border with the model (`llm.Provider`, `llm.Request`,
 `llm.Event`, `llm.EventKind`, `llm.Usage`) and a replayable `FakeProvider` that
 reproduces a deterministic script of events through a channel and closes it when finished.
- **M3** left the `Publisher` (`internal/session/runner/publish.go`): translate each
 `llm.Event` to a durable `SessionEvent`, buffers the deltas and maintains the map
 `callID -> toolName` of the turn.
- **M4** I leave the `tool.Registry` (`Materialize(perms) -> {Definitions, Settle}`),
 the contract types and the builtin `Echo`.
- **M5** I leave the **turn** (`internal/session/runner/turn.go`): `runTurn(ctx,
  sessionID) (bool, error)` assembles `llm.Request` from the projected history,
 calls `Provider.Stream` **once**, consumes the stream with `Publisher`, seats
 local tool calls concurrently with `errgroup`, and returns
 `needsContinuation`. M5 left `runTurn` as **a single happy attempt**, anticipating
 that M7 would wrap it with the control signal retry.
- **M6** left the **external loop** (`internal/session/runner/run.go`): the durable `Inbox`
 (queue/steer, `internal/session/inbox.go`) and the double loop (activity +
 steps, `MaxSteps = 25`). `Run` promotes the pending input to `Message{Role: user}`
 with the `promote` helper (a **pre-step** before `runTurn`), executes the turn and
 decides the continuation (tool local or steer, never text). M6 made it explicit that
 folding the promotion into the attempt and adding the control signals was M7's.

The next brick is the **control signals** and the `ContextEpoch`. Its
responsibility (see `../architecture/agent-loop.md`, "What makes a turn (`runTurn`)" and
"Control signals via errors", and `../architecture/opencode-agent-loop.md`, "Internal
transitions") is to protect the loop from executing a request that no longer represents the actual
state of the session:

- **snapshot** the context (`ContextEpoch`) when starting to prepare the shift;
- **re-check** the epoch just before calling the provider: if the agent, the
 model or the revision changed between preparing and calling, the request became old;
 is discarded and rebuilt from the store (`errRebuildTurn`), **without** having
 called `Stream`;
- **compact** before overflow: if the request exceeds the context before starting the
 wizard message, the history is compacted and retried once through the path
 post-compaction (`errContinueAfterCompaction`).

M7 builds and tests this **against fakes**: `ContextEpoch` is exposed by `Store`
(`MemoryStore` returns a stable epoch, so the happy path of M5/M6 does not
rebuild); concurrent switching and overflow are simulated with
test decorators (a `Store` that returns different epochs between reads and a `Compactor`
fake). It does not touch Wails, nor the real provider, nor the interruption/failures of M8.

## 2. Objective

Prepare the shift with control signals and the epoch you need:

In `internal/session` (the context photo):

- the type `ContextEpoch` (`Agent`, `Model string`; `BaselineSeq Seq`; `Revision
  int`): the photo that the runner compares to detect concurrent changes
 (`internal/session/epoch.go`, new);
- the `Epoch(ctx, sessionID) (ContextEpoch, error)` method in the `Store` interface and
 its implementation in `MemoryStore` (returns a epoch stable, all at zero).

In `internal/session/runner`:

- sentinels `errRebuildTurn` and `errContinueAfterCompaction` (not exported:
 internal packet flow control, checked with `errors.Is`);
- `runTurn(ctx, sessionID) (bool, error)` rewritten as **retry loop** over
 `runTurnAttempt`: swallow signals and retry; any other error (or success)
 is returned; `compactor Compactor` field **optional** (nil) in the `Runner`: nil
 means "never compact" (M5/M6 happy path); the tests inject it
 white-box (same package);
- behavioral tests that perform rebuild (agent/model change and revision mismatch
) and compaction.

M7 **does not** change `consume`, nor `Publisher`, nor `Inbox`, nor the body of `Run`
(the promotion is still the pre-step of M6), nor does it add interruption by `ctx`,
`Step.Failed`, `failInterruptedTools` nor does it touch Wails.

## 3. Scope

### Inside

- `internal/session/epoch.go` (new): the type `ContextEpoch`.
- `internal/session/store.go`: the method `Epoch` in the interface `Store`.
- `internal/session/memstore.go`: `MemoryStore.Epoch` (epoch stable at zero).
- `internal/session/runner/turn.go`: the sentinels `errRebuildTurn`/
 `errContinueAfterCompaction`, the `runTurn` retry loop and the new `runTurnAttempt`
 (which replaces the M5's `runTurn` body). `consume`/`toLLMMessages` intact.
- `internal/session/runner/runner.go`: the interface `Compactor` and the optional field
 `compactor` (additive; `NewRunner` **does not** change signature: the field remains nil).
- Behavior tests on `internal/session/runner/turn_signals_test.go` (new):
 rebuild by change of epoch and compaction for overflow, with its
 test decorators. If appropriate, a minimum test of the `MemoryStore.Epoch` contract in
 `internal/session/memstore_test.go`.
- Update `internal/session/runner/doc.go` (the control signals landed) and
 `internal/session/doc.go` (the `ContextEpoch` landed).

### Out (do not do in M7)

- The **real source** of the epoch (file/instruction reconciliation, watch of the
 workspace, real agent selection, real advance of `BaselineSeq`): in M7
 `MemoryStore.Epoch` returns a stable epoch of zero; what drives the epoch in the
 tests are **decorators**. The real epoch driver (with agent/config model and
 revision that changes by context) arrives with the real store and the app — **M10/M9**.
- `Request.System []Part` (agent prompt + context baseline) and
 `Request.ProviderOpts` (prompt cache key): M6 refers to them as "M7", but
 populating fields that no one reads violates the criteria of the plan (M5/M6 did not add
 fields without a consumer). The turn with control signals does **not** need them: epoch and
 `BaselineSeq` already cover "do not run with old state". `System`/`ProviderOpts`
 arrive when there is a **real system context source**, along with the real adapter
 — **M10**.
- Fold the **promotion** into `runTurnAttempt` (step 5 of `runTurnAttempt` in
 the architecture). M6 left `promote` as a pre-step of `Run` and M7 preserves it: the
 retry of `errContinueAfterCompaction` retries the attempt **without** re-promoting (the
 promotion already occurred on `Run`, once), so it does not need the
 `promotion` parameter or re-setting it. See "Risks and decisions".
- Interruption due to `ctx` canceled mid-shift, errors in the stream of the
 provider, `Step.Failed`, tools that the framework provider executed but never
 resolved, and `failInterruptedTools` (cleanup after crash) — **M8**.
- `EventBus`, `runtime.EventsEmit`, `app.go`, `main.go`, Wails, frontend — **M9**.
- `Store` SQLite, persistent `Inbox` and the actual `Provider` adapter — **M10**. The
 tests of M1..M7 should remain green with the real store.

## 4. Added to the contract (`session`)

### `internal/session/epoch.go` (new)

The context photo that the runner compares to detect concurrent changes between
preparing a turn and calling it. It is modeled as a comparable value (`==`/`!=`): the
attempt snapshots it at start and re-reads it before `Stream`; any difference
forces reconstruction.

```go
package session

// ContextEpoch es una foto del contexto de la sesion que el runner usa para
// detectar cambios concurrentes entre preparar un turno y llamar al proveedor.
// Agent y Model identifican la configuracion activa del turno (el modelo del epoch
// es el que el runner pone en el Request). Revision se incrementa cuando el contexto
// cambia de una forma que invalida un request ya preparado (cambio de agente/modelo,
// reconciliacion de archivos/instrucciones). BaselineSeq marca desde donde cuenta el
// historial proyectado del turno (Store.Messages lee con sinceSeq = BaselineSeq):
// una compaction futura lo avanza para dejar fuera lo ya resumido.
//
// Es comparable a proposito (solo campos comparables): el runner decide el rebuild
// con un simple after != before. M7 lo usa minimo: MemoryStore.Epoch devuelve un
// epoch estable en cero, asi el camino feliz no reconstruye nunca; el driver real
// (que mueve Agent/Model/Revision/BaselineSeq por cambios de contexto) llega con el
// store real (M10).
type ContextEpoch struct {
	Agent       string
	Model       string
	BaselineSeq Seq
	Revision    int
}
```

### `internal/session/store.go` (new method in the interface)

The epoch is read from `Store`, just like the history: it is the durable state of the session.
`Run` rebuilds the store request each turn, so the epoch also leaves
from there (never in a live state between turns).

```go
type Store interface {
	AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error)
	LoadSession(ctx context.Context, sessionID string) (Session, error)
	Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error)

	// Epoch devuelve la foto del contexto vigente de la sesion. El runner la
	// snapshotea al preparar un turno y la re-lee antes de llamar al proveedor: si
	// cambio, descarta el request y reconstruye. ErrSessionNotFound si la sesion no
	// existe.
	Epoch(ctx context.Context, sessionID string) (ContextEpoch, error)
}
```

### `internal/session/memstore.go` (stable implementation)

`MemoryStore` returns a stable epoch of zero: the M5/M6 happy path snapshots
and re-reads the same value, so it never rebuilds and the previous behavior does not
change. The epoch that really evolves (agent/model/revision) is provided by the real
store or a test decorator.

```go
// Epoch devuelve la foto del contexto de la sesion. M7 no tiene aun una fuente real
// de contexto (agente/modelo de config, reconciliacion de archivos), asi que
// MemoryStore devuelve el epoch cero estable: snapshot y recheck coinciden y el
// runner no reconstruye. ErrSessionNotFound si la sesion no existe. El driver real
// del epoch llega en M10.
func (s *MemoryStore) Epoch(ctx context.Context, sessionID string) (ContextEpoch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[sessionID]; !ok {
		return ContextEpoch{}, ErrSessionNotFound
	}
	return ContextEpoch{}, nil
}
```

## 5. The turn with control signals (`runner`)

### `internal/session/runner/runner.go` (Optional Compactor, Additive)

The `Runner` gains an **optional** collaborator: the `Compactor`. nil means "never
compact" (M5/M6 run with nil and do not change). `NewRunner` **does not** change signature: the
field becomes nil; M7 tests inject it with white-box (same `runner` package). A real
`Compactor` and its wiring arrive with the app (M9/M10), when there is a real
notion of "the request exceeds the context".

```go
type Runner struct {
	store     session.Store
	inbox     session.Inbox
	provider  llm.Provider
	registry  *tool.Registry
	perms     tool.Permissions
	nextID    func() string
	compactor Compactor // opcional; nil = nunca compacta (camino feliz de M5/M6)
}

// Compactor decide si un Request excede el contexto del modelo y, si pasa, compacta
// el historial durable de la sesion para que el siguiente intento entre. nil en el
// Runner significa "nunca compacta". M7 lo inyecta con un fake en tests; el real
// (que mide tokens contra el limite del modelo y resume el historial) llega en M10.
type Compactor interface {
	// NeedsCompaction informa si req excede el contexto y hay que compactar antes de
	// llamar al proveedor.
	NeedsCompaction(req llm.Request) bool
	// Compact reduce el historial durable de la sesion (resumen/baseline) para que el
	// siguiente intento arme un request que entre. Debe hacer progreso: tras Compact,
	// NeedsCompaction del nuevo request debe terminar siendo false.
	Compact(ctx context.Context, sessionID string) error
}
```

`NewRunner` remains the same as M6 (six parameters); The `compactor` is not wired there.

### `internal/session/runner/turn.go` (sentinels + retry loop + attempt)

```go
package runner

import (
	"context"
	"errors"

	"golang.org/x/sync/errgroup"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// errRebuildTurn y errContinueAfterCompaction son senales de control internas del
// turno: nunca escapan de runTurn (el retry loop las traga). En vez de excepciones,
// Go usa sentinels envueltos y errors.Is.
//
//   - errRebuildTurn: el contexto cambio mientras se preparaba el turno (cambio de
//     agente/modelo o mismatch de la revision del epoch). El request quedo viejo: se
//     descarta y se reconstruye desde el store, SIN haber llamado al proveedor.
//   - errContinueAfterCompaction: hubo overflow de contexto antes de empezar el
//     mensaje del asistente. Se compacto el historial y se reintenta una vez por la
//     ruta post-compaction.
var (
	errRebuildTurn             = errors.New("rebuild prepared turn")
	errContinueAfterCompaction = errors.New("continue after overflow compaction")
)

// runTurn ejecuta un turno reintentando ante las senales de control internas. El
// cuerpo del turno vive en runTurnAttempt; runTurn solo decide que hacer con su
// resultado: ante errRebuildTurn o errContinueAfterCompaction reintenta (el attempt
// se reconstruye desde el store, ya con el epoch reconciliado o el historial
// compactado); cualquier otro error, o el exito, se devuelve. La terminacion la
// garantiza el contrato de cada senal: el rebuild solo se dispara mientras el epoch
// siga cambiando (se estabiliza) y la compaction debe hacer progreso (el request
// deja de exceder el contexto).
func (r *Runner) runTurn(ctx context.Context, sessionID string) (bool, error) {
	for {
		cont, err := r.runTurnAttempt(ctx, sessionID)
		switch {
		case errors.Is(err, errRebuildTurn):
			continue // algo cambio mientras se preparaba: reconstruir desde el store
		case errors.Is(err, errContinueAfterCompaction):
			continue // hubo overflow: se compacto, reintentar por la ruta post-compaction
		default:
			return cont, err
		}
	}
}

// runTurnAttempt es UN intento del turno. Snapshotea el epoch al empezar a preparar,
// arma el Request desde el historial proyectado (a partir del BaselineSeq del epoch)
// y las tools materializadas, resolviendo el modelo del epoch. Si el request excede
// el contexto, compacta y devuelve errContinueAfterCompaction. Re-lee el epoch antes
// de llamar al proveedor: si cambio (agente/modelo/revision), devuelve errRebuildTurn
// SIN llamar a Stream. Si el epoch sigue vigente, llama Stream UNA vez y consume el
// stream igual que M5. Devuelve needsContinuation.
func (r *Runner) runTurnAttempt(ctx context.Context, sessionID string) (bool, error) {
	// Snapshot del contexto al empezar la preparacion.
	before, err := r.store.Epoch(ctx, sessionID)
	if err != nil {
		return false, err
	}

	// Historial proyectado desde el baseline del epoch y tools materializadas.
	msgs, err := r.store.Messages(ctx, sessionID, before.BaselineSeq)
	if err != nil {
		return false, err
	}
	mat := r.registry.Materialize(r.perms)
	req := llm.Request{Model: before.Model, Messages: toLLMMessages(msgs), Tools: mat.Definitions}

	// Overflow antes del mensaje del asistente: compactar y reintentar una vez.
	if r.compactor != nil && r.compactor.NeedsCompaction(req) {
		if err := r.compactor.Compact(ctx, sessionID); err != nil {
			return false, err
		}
		return false, errContinueAfterCompaction
	}

	// Re-leer el epoch antes de llamar al proveedor: si cambio, el request quedo
	// viejo. Se descarta y se reconstruye SIN haber llamado a Stream.
	after, err := r.store.Epoch(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if after != before {
		return false, errRebuildTurn
	}

	// Epoch vigente: una sola llamada al proveedor y consumo del stream (M5).
	in, err := r.provider.Stream(ctx, req)
	if err != nil {
		return false, err
	}
	pub := NewPublisher(r.store, sessionID, r.nextID())
	return r.consume(ctx, in, pub, mat.Settle)
}
```

`consume` and `toLLMMessages` do not change compared to M5.

Notes:

- **Snapshot/recheck of the epoch around the preparation.** The attempt reads the
 epoch at the beginning (`before`) and re-reads it just before `Stream` (`after`). All
 preparation (read history, materialize tools, assemble request, decide
 compaction) occurs between the two reads: it is the window where a concurrent
 context change can invalidate the request. `after != before` detects it and
 cuts off before the effect (the call to the provider).
- **The provider never sees an old request.** The `errRebuildTurn` is returned
 **before** `provider.Stream`. A request that represents old state is discarded;
 the retry rebuilds it with the already stable epoch. That is the "Done when" criteria of the
 milestone.
- **The model exits the epoch.** `req.Model = before.Model`. Thus the rebuild is observed
: after a model change, the rebuilt request carries the new model
 (the old one is never streamed). `MemoryStore` returns `Model: ""`, the same as the
 request from M5/M6 (which did not set `Model`): the happy path does not change. `Compact` modifies durable history; The retry re-reads the compacted
 history and creates an incoming request. The epoch `BaselineSeq` is where a real
 compactor would make the cut; in M7 the fake does not advance it (re-reads the entire
 history, including a compaction marker) and `NeedsCompaction` stops requesting
 compaction after the first `Compact`.
- **`compactor` nil = happy path.** Without a compactor, the overflow block is skipped:
 `runTurnAttempt` remains the same as the `runTurn` from M5 plus the snapshot/recheck of the
 epoch (which always coincides with `MemoryStore`). M5/M6 green without touching their tests.
- **The signals do not escape.** `runTurn` swallows `errRebuildTurn`/
 `errContinueAfterCompaction` and retries. `Run` (M6) still seeing only
 `needsContinuation` or a hard error; his body does not change.

## 6. Semantics of the turn with control signals

The contract that M7 sets (and that M8/M9/M10 keep):

- **Happy path intact.** With `MemoryStore` (stable epoch) and `compactor` nil,
 `runTurn` snapshots and re-reads the same epoch, does not rebuild, calls `Stream` once
 and behaves exactly like M5/M6. The entire previous suite remains green.
- **Rebuild due to agent/model change.** If between the snapshot and the recheck the epoch
 changes agent or model, the attempt returns `errRebuildTurn` **before**
 `Stream`; `runTurn` retry. The request that arrives at the provider reflects the
 **new** state (e.g. the new model), and the old one is never streamed. It is executed once
 only once (a single turn in the projection).
- **Rebuild by revision mismatch.** Same as the previous one but the field that
 changes is `Revision`: any difference in the epoch (not just agent/model) forces
 the rebuild. This is the architecture's "context epoch is still in effect" check. `runTurn` retries once for path
 post-compaction. The second attempt creates a request that comes in and streams it. The
 compaction runs **only once** and the shift produces its wizard message.
- **Reconstruction from the store.** Each attempt rereads epoch and history of the
 `Store`: the rebuild and post-compaction do not carry over live state from the previous
 attempt.
- **Hard error cuts the shift.** Yes `Epoch`/`Messages`/`Compact`/`Stream` or any
 `AppendEvent` returns an error that is **not** a control signal, `runTurnAttempt`
 propagates it, and `runTurn` returns it without retrying.

## 7. TDD Plan

### Safety net

- Green base state before touching anything. M7 adds an interface type and method
 in `session` (`ContextEpoch`, `Store.Epoch`), rewrites `turn.go` (retry loop +
 attempt) and adds an optional field to `Runner`; first the existing
 (M0..M6) is run.
- Command: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Expected result: they pass clean. When adding `Epoch` to the `Store` interface, the only
 real implementer is `MemoryStore` (and `recordingStore` from `turn_test.go`, which
 **embeds** and inherits the method); the `Publisher`
 fakes (`recordingAppender`/`failingAppender`) implement only `eventAppender` (a
 one-method interface), not `Store`, so they **do not** break. After adding
 `MemoryStore.Epoch`, run through `go build ./...` and `go test ./internal/session/...`
 to confirm that M1..M6 are still green.

### Understand

- Read entry M7 of the roadmap; "What is a shift (`runTurn`)", "
 control signals via errors" and the numbered list of `runTurnAttempt` from
 `../architecture/agent-loop.md`; "Internal transitions" and "Context epoch" by
 `../architecture/opencode-agent-loop.md`; and the contract of M5 (`runTurn` returning
 `needsContinuation`) and M6 (`Run` with `promote` as a pre-step).
- Expected behavior: snapshot the epoch, re-check it before `Stream`,
 rebuild on change (without streaming old), compact on overflow and retry
 a once.

### NETWORK

- Write the test that fails first:
 `TestRunner_RebuildsTurnWhenModelChangesBeforeStream`. Reference symbols that still
 DO NOT exist: `session.ContextEpoch` and the epoch check in the runner. In Go that is
 a compilation failure of the test package -> RED honest (same as M5/M6).
- The test uses a `epochFlipStore` decorator (it embeds `*session.MemoryStore` and
 overwrites `Epoch`): it returns `before = ContextEpoch{Model: "viejo"}` in its
 **first** call and `after = ContextEpoch{Model: "nuevo"}` in the following ones. Thus,
 in the first attempt the snapshot sees `"viejo"` and the recheck sees `"nuevo"` (mismatch
 -> rebuild); on the second attempt both reads see `"nuevo"` (match ->
 stream). Seeds a user message, builds a `recordingProvider` (captures the
 `Request`) with a text-only script (`StepStarted, TextStarted, TextDelta("ok"),
  TextEnded, StepEnded`), a `Registry` with `Echo`, and the `Runner` with `idCounter()`.
 Asserts:
 - `cont, err := r.runTurn(ctx, "s1")` returns `err == nil`, `cont == false`;
 - `prov.captured().Model == "nuevo"`: the streamed request carries the new model
    (el viejo se descarto antes de `Stream`);
- the projection has **only one** wizard message (`Text == "ok"`): the rebuild
    descarto el primer attempt antes de streamear, no encadeno dos turnos.
- Command:
 `go test -run TestRunner_RebuildsTurnWhenModelChangesBeforeStream ./internal/session/runner`
 -> expected failure.

### GREEN

- Write the minimum: `internal/session/epoch.go` (`ContextEpoch`); `Store.Epoch` in
 the interface and `MemoryStore.Epoch` (epoch zero stable); sentinels and retry
 loop `runTurn` + `runTurnAttempt` in `turn.go` (epoch snapshot/recheck, epoch
 `req.Model`); the field `compactor` and the interface `Compactor` in
 `runner.go`.
- Run only the red test until green.
- Command:
 `go test -run TestRunner_RebuildsTurnWhenModelChangesBeforeStream ./internal/session/runner`.

### TRIANGULATE

Add cases to avoid false green (those in the roadmap):

- `TestRunner_RebuildsTurnWhenEpochRevisionChanges`: same `epochFlipStore` but
 `before = {Revision: 1}` and `after = {Revision: 2}` (same model). It states that
 `runTurn` runs without error, that the provider was called **once** (a single message from
 assistant in the projection) and, if you want to fix the signal, that a direct
 `runTurnAttempt` returns an error that `errors.Is(err, errRebuildTurn)`
 recognizes (white-box test of the isolated attempt). Checks for "epoch force
 rebuild mismatch" for a different agent/model field.
- `TestRunner_CompactsAndRetriesOnceWhenRequestOverflows`: flat `MemoryStore` (stable epoch
 without rebuild) and a fake `Compactor` injected white-box (`r.compactor =
  &fakeCompactor{store: store}`). The fake: `NeedsCompaction` returns `true` until
 `Compact` runs once (then `false`); `Compact` marks `compacted = true`,
 increments a counter and appends a `Message{Role: system, Text: "[compactado]"}`
 to the store. Seed a user, text-only script. It states:
 - `runTurn` returns `err == nil`, `cont == false`;
 - the `Compact` counter was set to **1** (compacted only once);
 - the provider was called **once** (a wizard message);
 - the projection contains the `Message{Role: system, Text: "[compactado]"}` (the
    compaction corrio antes del turno que streameo).
- `TestRunner_HappyPathDoesNotRebuildOrCompact` (explicit regression): `MemoryStore`
 + `compactor` nil + a `recordingProvider`. It states that `runTurn` streams once,
 than `captured().Model == ""` (epoch zero) and that the projection is as expected
 (user + assistant). Anchor that the happy path does not trigger any signal.
- (epoch contract) in `internal/session/memstore_test.go`,
 `TestMemoryStore_EpochIsStableAndNotFound`: `Epoch` of a non-existent session
 returns `ErrSessionNotFound`; after a `AppendEvent`, two consecutive `Epoch`
 return the **same** value (stable, without spurious rebuild). Sets the contract that
 the runner assumes from the store.
- Commands:
 - `go test -run 'TestRunner_Rebuilds|TestRunner_Compacts|TestRunner_HappyPath|TestMemoryStore_Epoch' ./internal/session/...`
 - `go test -race -run TestRunner ./internal/session/runner` (the turn continues
    asentando tools con `errgroup`/candado del `Publisher`; los decoradores de epoch
    y el compactor usan mutex)

### REFACTOR

- Cleanup without changing behavior: factor test
 decorators (`epochFlipStore`, `fakeCompactor`) if it reduces duplication; reuse
 `seedUser`/`recordingProvider`/`idCounter` from M5/M6 tests where applicable.
 Update `internal/session/runner/doc.go` (control signals
 `errRebuildTurn`/`errContinueAfterCompaction` and epoch snapshot/recheck
 landed on M7; interruption and failures are still in M8) and
 `internal/session/doc.go` (the `ContextEpoch` landed: the context photo that the
 runner compares; the real epoch driver is still pending).
- Verify that the suite is still green after formatting.
- Command: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Acceptance criteria (Done when)

1. There is `session.ContextEpoch` (`Agent`, `Model`, `BaselineSeq`, `Revision`),
 comparable with `==`/`!=`.
2. Interface `Store` has `Epoch(ctx, sessionID) (ContextEpoch, error)` and
 `MemoryStore` implements it by returning a stable epoch of zero
 (`ErrSessionNotFound` if the session does not exist); M1..M6 remain green.
3. There are sentinels `errRebuildTurn` and `errContinueAfterCompaction` (not
 exported), checked with `errors.Is` inside the package.
4. `runTurn` is a retry loop over `runTurnAttempt`: it swallows the two signals and
 retries; any other errors or success are returned. `consume`/`toLLMMessages`
 did not change.
5. `runTurnAttempt` snapshots the epoch at startup, arms `Request` (model from
 epoch, history from `BaselineSeq`) and re-reads the epoch before `Stream`; if
 `after != before` returns `errRebuildTurn` **without** calling `Stream`.
6. An agent/model change between snapshot and recheck rebuilds the turn: the streamed
 request reflects the new state and the old state never reaches the provider
 (verified by capturing the `Request` and counting a single turn).
7. A `Revision` mismatch from the epoch also forces the rebuild (same
 `errRebuildTurn`).
8. There is the interface `Compactor` and the optional field `compactor` (nil = never
 compact); `NewRunner` did not change signature. A request that exceeds the context causes
 `runTurnAttempt` to compact (once) and return `errContinueAfterCompaction`;
 `runTurn` retries and streams the compacted request.
9. The happy path (`MemoryStore` + `compactor` nil) does not trigger any signal: the
 turn behaves like M5/M6 (M5 `turn_test.go` and M6 `run_test.go` green without
 modified).
10. `go test ./...` (and `-race` where applicable) passes; `go vet ./...` clean;
    `gofmt -l .` vacio.
11. There were no changes to `app.go`, `main.go`, Wails, the frontend or `internal/event`.
    En `internal/llm` no se toco nada (`Request.System`/`ProviderOpts` siguen
    diferidos). En `internal/session` solo se agrego `epoch.go`, el metodo `Epoch`
    en `store.go`/`memstore.go` y `doc.go`; `session.go`, `event.go`, `inbox.go`
    intactos. En `runner` solo crecieron `turn.go` (retry loop + attempt + sentinels)
    y `runner.go` (interface `Compactor` + campo) y `doc.go`; `publish.go`/`run.go`
    intactos.

## 9. Commands

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Tras sumar Epoch a la interface Store, confirmar que compila y que M1..M6 siguen verdes
go build ./...
go test ./internal/session/...

# Ciclo (test especifico primero)
go test -run TestRunner_RebuildsTurnWhenModelChangesBeforeStream ./internal/session/runner

# Triangulacion (rebuild por revision, compaction, camino feliz, contrato del epoch)
go test -run 'TestRunner_Rebuilds|TestRunner_Compacts|TestRunner_HappyPath|TestMemoryStore_Epoch' ./internal/session/...

# Concurrencia real (tools asentadas en errgroup + candado; decoradores con mutex)
go test -race -run TestRunner ./internal/session/runner

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
```

## 10. Table of expected evidence

When closing M7, the response/PR should include this table with actual results:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0..M6 green before editing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Control signals contract read | roadmap M7, `../architecture/agent-loop.md` (runTurn/signals), `../architecture/opencode-agent-loop.md` (internal transitions, epoch) | identified behavior |
| NETWORK | Rebuild test due to model change written first | `turn_signals_test.go` + `go test -run TestRunner_RebuildsTurnWhenModelChangesBeforeStream ./internal/session/runner` | expected failure (does not compile) |
| GREEN | `epoch.go` + `Store.Epoch` + retry loop/attempt + `Compactor` minimums | `internal/session/epoch.go`, `internal/session/{store,memstore}.go`, `internal/session/runner/{turn,runner}.go` | specific test passes |
| TRIANGULATE | Rebuild by revision, compaction once, happy path, epoch contract | `go test -run 'TestRunner_Rebuilds|TestRunner_Compacts|TestRunner_HappyPath|TestMemoryStore_Epoch' ./internal/session/...`, `go test -race ...` | cases pass, `-race` clean |
| REFACTOR | test decorators, `doc.go` updated | `gofmt -w internal`, `go vet ./...`, `go test ./...` | green suite, M1..M6 intact |

## 11. Risks and decisions

- **`runTurn` as retry loop, `runTurnAttempt` as body.** M5 left `runTurn` as
 "one happy try" anticipating exactly this wrapper. M7 moves the body from
 M5 to `runTurnAttempt` (plus the epoch snapshot/recheck) and leaves `runTurn` as the
 loop that swallows the signals. The `runTurn(ctx, sessionID) (bool, error)` signature does not
 change, so M6 (`Run` calls it) and M5's tests (they call it direct) remain
 green without touching.
- **The epoch is compared integer (`after != before`), unifying the two checks of the
 architecture.** The architecture separates the agent/model check (step 6,
 re-read the session) of the epoch review check (step 13). M7 unifies them: the
 `ContextEpoch` carries `Agent`, `Model` and `Revision`, and any difference between
 snapshot and recheck triggers the same `errRebuildTurn`. It's simpler and just as faithful: the observable effect (not streaming an old request) is identical, and a single comparison point avoids duplicating logic. That's why `Session` **does not** grow with
 `Agent`/`Model` in M7: the epoch is the bearer of the turn context.
- **Promotion is still the pre-step of `Run` (M6 decision preserved).** The
 architecture promotes within `runTurnAttempt` (step 5) and the retry de
 `errContinueAfterCompaction` resets `promotion = steer`. M6 chose to leave
 `promote` as a fine pre-step into `Run` (a single promotion per step, before
 `runTurn`). M7 preserves it: since the promotion already occurred in `Run`, the retry of the
 attempt **does not** re-promote (it does not duplicate the user's prompt), so it does not need
 the `promotion` parameter or to reset it. Rewriting `Run` is avoided (M6 green without
 changes) and the same effect as the post-compaction route of the
 architecture is maintained (do not re-consume the queue after compacting).
- **`MemoryStore.Epoch` stable; the change is simulated with decorators.** There is no
 real source of context (config agent/model, file watch) yet, so
 `MemoryStore` returns the stable zero epoch: the happy path snapshots and re-reads the
 itself and never rebuilds. The concurrent shift is tested with a `epochFlipStore`
 test (returns `before` the first time and `after` later), just as M6 simulated the
 concurrent steering with a `steeringProvider`. This is not how an epoch
 driver is invented without a real consumer; that driver arrives in M10 with the real store.
- **optional `Compactor` (nil) instead of parameter of `NewRunner`.** Unlike the
 `inbox` of M6 (which `Run` always needs and that's why I enter the constructor), the
 compaction is an **optional** collaborator: most turns do not compact and
 there is still no real notion of "request exceeds context". Modeling it as a
 nil-able field (not a required parameter) avoids touching the ~8 constructs of
 `NewRunner` in the M5/M6 tests with a noisy `nil`, and lets the M7
 tests inject it white-box (same `runner` package). A real `Compactor` and its wiring in
 `NewRunner`/`app.go` arrive when there is the real measurement of tokens (M10). It is the
 same criterion "deterministic fakes, the real at the end" of the plan.
- **`req.Model` since the epoch.** Resolving the epoch model (instead of leaving it at
 zero like M5/M6) is what makes the rebuild **observable**: after a model change,
 the rebuilt request carries the new model and the test captures it. `MemoryStore`
 returns `Model: ""`, identical to the M5/M6 request, so the happy path does not
 change. It is step 7 of `runTurnAttempt` ("solve the model") in its minimal form.
- **`BaselineSeq` is used, not speculated.** `runTurnAttempt` reads the history with
 `Messages(ctx, sessionID, before.BaselineSeq)` instead of `0`. `MemoryStore` returns
 `BaselineSeq: 0`, so it reads the entire history just like M5/M6; but the field remains
 **wired** to its only real consumer (the projected history slice), not as
 a dead field. A real compaction would advance it; the M7 fake doesn't touch it (re-read
 everything, including the marker) and that's enough to try "compact and retry once".
- **`Request.System`/`ProviderOpts` are still deferred (refinement on M6).** M6
 ready as "M7", but the turn with control signals does not exercise them: the epoch and the
 `BaselineSeq` cover "not running with old state". Populating `System`/`ProviderOpts` without
 a real source of system context would be adding fields that no one reads, just what
 M5/M6 avoided. They differ to M10 (real adapter + real system context). It is an
 explicit scope decision, not an oversight.
- **Termination of the retry loop by contract, not by hard bound.** `runTurn` uses an unlimited
 `for` (like the architecture). Termination is guaranteed by the contract of
 each signal: the rebuild only fires as long as the epoch continues to change (a concurrent
 change stabilizes in one or two turns) and `Compact` **must** make progress
 (after compacting, the request stops exceeding the context). A decorator/compactor that
 never converges would hang the loop; the `MaxSteps` of `Run` (M6) does not cover this internal
 loop. The contract is documented and a hard limit (a
 retry counter with its error) is deferred to when a real driver justifies it; adding it now would be
 protection without a test that asks for it. The M7 fakes converge on purpose (the epoch is
 stabilized after the first round; the compactor sets `NeedsCompaction` to false).
- **Sentinels not exported.** `errRebuildTurn`/`errContinueAfterCompaction` are
 internal flow control of the `runner` packet: they never escape `runTurn`. They are not
 exported, the same as in the architecture. The white-box tests (same package) can
 assert them with `errors.Is` over `runTurnAttempt` if you want to set the signal, but
 the bulk of the tests assert the **observable behavior** (the request reflects the
 new state, the provider runs once, the compaction marker appears), not the
 internal error.

## 12. Sources

- Roadmap: `../plans/agent-loop-roadmap.md` (milestone M7)
- Architecture: `../architecture/agent-loop.md` (sections "What makes a turn
 (`runTurn`)", the numbered list of `runTurnAttempt`, "Control signals via
 errors" and "Durable persistence" — `Store.Epoch`)
- Reference loop: `../architecture/opencode-agent-loop.md` (sections "Context epoch",
 "Internal transitions" and the steps of `runTurnAttempt`)
- Way of working: `AGENTS.md`
- Previous specs: `atenea-m1-tipos-store-spec.md`,
 `atenea-m2-provider-fake-spec.md`, `atenea-m3-publisher-spec.md`,
 `atenea-m4-tool-registry-spec.md`, `atenea-m5-run-turn-spec.md`,
 `atenea-m6-run-loop-spec.md`
