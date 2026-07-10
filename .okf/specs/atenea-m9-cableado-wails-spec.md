---
updated_at: 2026-07-09
summary: Specification for m9 cableado wails spec.
---

# Spec M9 — Wails Wiring (EventBus + SendPrompt)

`../plans/agent-loop-roadmap.md` Milestone **M9** Executable Spec. Defines the
final state, scope, TDD plan and acceptance criteria to connect
the loop (M1..M8, today 100% green against fakes) to the real Wails app **without changing the
loop logic**. The central rule: the UI observes the session as a **stream of the
durable log** and the only point that Wails knows is `internal/event`.

M9 adds three pieces and a minimal frontend:

- **`event.Bus`**: Forwards a `session.SessionEvent` (or `Run` hard error) to the
 frontend channel `session:<id>`. It doesn't matter Wails: it receives an injected `EmitFunc`
 (in production `runtime.EventsEmit` linked to the `ctx` of the app; in tests
 a fake that registers).
- **`event.EmittingStore`**: decorates a `session.Store`; After each successful `AppendEvent`
 it forwards the event (already with its `Seq` and `SessionID`) to `Bus`. It is the only
 bridge Store -> UI: since **all** the runner appends (`Publisher`, `promote`,
 `failInterruptedTools`) go through the `Store`, the UI sees each event without touching the
 loop.
- **`app.go`**: `SendPrompt(sessionID, text)` does `Admit(queue)` and start `Run`
 in a goroutine with a cancelable `ctx`; `Stop(sessionID)` cancels that `ctx`
 (stop button). The provider is still `FakeProvider` (the real one is M10).

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

Previous milestones left the loop complete inside out, green against
fakes and without a single Wails dependency:

- **M1** left the durable domain (`Seq`, `Message`, `SessionEvent`, `Store`,
 `MemoryStore`): the event log is the only source of truth; the messages are
 a projection (`Store.Messages(sinceSeq)`). `AppendEvent` assigns `SessionID` and
 `Seq` and **ignores** those brought by the event.
- **M2** I leave the border with the model (`llm.Provider`, `Request`, `Event`) and the
 `FakeProvider` replayable. M9 continues using the fake; the real adapter is M10.
- **M3** left the `Publisher`: translate the stream to durable `SessionEvent` via
 `eventAppender.AppendEvent`. The runner passes him the `Store` of the session.
- **M4** left the `tool.Registry` (`Materialize -> {Definitions, Settle}`), the
 `OutputStore` and the builtin `Echo`.
- **M5** left the turn (`runTurnAttempt` + `consume`): enter local tools with
 `errgroup` and publishes its results.
- **M6** left the external loop (`Run` + `Inbox` + `MaxSteps`). The comment of
 `Runner` already anticipated "a real generator in M9" for `nextID`, and
 `StepLimitExceededError` "so that the UI (M9) distinguishes it with errors.As".
- **M7** left the control signals (`errRebuildTurn`, `errContinueAfterCompaction`)
 and the
 closure survives; `Run` runs `failInterruptedTools` on
 boot. `Step.Failed` does not materialize `Message`: it is a marker that **the UI (M9)
 observes for the durable event**.

What is missing is the wiring: make the log visible through the UI and start/cut the
`Run` from the frontend. The architecture already describes it (`../architecture/agent-loop.md`,
section "Integration with Wails"): the `EventBus` is the only point that Wails knows,
`app.go` starts the runner in a goroutine and the stop button cancels the `ctx`.

## 2. Objective

A prompt from the Wails UI produces **visible streaming** of the turn and a stop
 button interrupts it, **without changing a line of the loop logic** (`Run`, `runTurn`,
`consume`, `Publisher`). The runner is tested against a fake `Bus` (a `EmitFunc`
 that registers), never against Wails. The border with Wails (`runtime.EventsEmit`,
`wails.Run`, the Vue) is isolated and is the only section not covered by `go test`,
verifiable by hand with `wails dev`.

## 3. Scope

### Inside

- `internal/event/bus.go`: `EmitFunc`, `Bus`, `Bus.Publish(ev)`,
 `Bus.PublishError(sessionID, err)`.
- `internal/event/store.go`: `EmittingStore` which decorates `session.Store` and forwards
 each added event to the `Bus`; read-only methods delegate unchanged.
- `app.go`: `App` with `inbox`, `runner`, `bus` and a map of `cancel` per session;
 injectable constructor `newApp(provider, emit)`; `NewApp()` production;
 `SendPrompt`, `Stop`, `startup`; a real ID generator for `nextID`.
- `main.go`: bind `App` (with `SendPrompt`/`Stop`) in `wails.Run`.
- Minimal frontend (`frontend/src/App.vue` + generated bindings): prompt input,
 send button, stop button, mapping list `Step.* / Text.* / Tool.*` streaming
 listening to `session:<id>`.

### Out (do not do in M9)

- **Real Provider / Anthropic**: M9 starts with the dashed `FakeProvider`. The
 actual adapter is **M10**.
- **Store SQLite**: M9 follows with `MemoryStore` after `EmittingStore`. The persistent store
 is **M10**.
- **Reprojection of history in the UI when opening a session** (read `Messages` and
 paint the old): M9 only streams the new. Rich UI (markdown, tools diffs,
 reprojection) arrives when needed; M9 tests the channel.
- **Steering from the UI** (`Admit(steer)` live) and **multiple sessions** in the
 UI: the `session:<id>` channel already supports it, but M9 wires a session and the
 `SendPrompt`/`Stop` stream. `Steer` is added when the UI requests it.
- **fine UX of stop vs error**: `Run` interrupted returns `context.Canceled` and is
 forwarded by `PublishError`; Distinguishing "deliberate stop" from "glitch" in the UI is
 differs.

## 4. The event bus (`internal/event/bus.go`)

`Bus` is the only point that knows the frontend event channel. It doesn't matter
Wails: it receives a `EmitFunc` whose shape copies that of `runtime.EventsEmit`
(`eventName` + variadic payload) without dragging the dependency.

```go
package event

import "atenea/internal/session"

// EmitFunc es la frontera con Wails. En produccion (main.go/app.go) envuelve
// runtime.EventsEmit ligado al ctx de la app; en tests es un fake que registra lo
// emitido. Su forma copia la de runtime.EventsEmit (eventName + payload variadico)
// para que envolverla sea trivial y para no acoplar event/ ni los tests a Wails.
type EmitFunc func(eventName string, optionalData ...interface{})

// Bus reenvia eventos de sesion al frontend. Es la unica pieza que conoce el
// nombre del canal (session:<id>); el runner publica eventos neutrales (via el
// EmittingStore) y el Bus decide a donde van.
type Bus struct {
	emit EmitFunc
}

// NewBus crea el bus sobre una EmitFunc. emit nil deja un bus inerte (util en
// produccion antes de startup, cuando aun no hay ctx de Wails).
func NewBus(emit EmitFunc) *Bus { return &Bus{emit: emit} }

// Publish reenvia un evento durable de sesion al canal session:<id>. El evento ya
// trae su Seq y SessionID (los asigno el Store): la UI ordena por Seq, no por
// orden de llegada.
func (b *Bus) Publish(ev session.SessionEvent) {
	if b.emit == nil {
		return
	}
	b.emit("session:"+ev.SessionID, ev)
}

// PublishError reenvia al canal session:<id>:error un error duro que corto una
// corrida de Run (fallo de proveedor, limite de pasos, interrupcion). No es un
// SessionEvent durable: es el cierre observable de una actividad que fallo.
func (b *Bus) PublishError(sessionID string, err error) {
	if b.emit == nil {
		return
	}
	b.emit("session:"+sessionID+":error", err.Error())
}
```

`EmitFunc` nil-safe on purpose: `NewApp` arms `Bus` before Wails calls
`startup` (where `ctx` arrives); issuing before that is a no-op, not a panic.

## 5. The Store -> UI (`internal/event/store.go`)

 bridgeThe runner never learns from `Bus`: it is injected with a `Store` that, in addition to persisting, forwards. `EmittingStore` decorates any `session.Store`.

```go
package event

import (
	"context"
	"sync"

	"atenea/internal/session"
)

// EmittingStore decora un session.Store: tras cada AppendEvent exitoso reenvia el
// evento (ya con su Seq y SessionID asignados) al Bus, asi la UI observa el log
// durable como un stream. Es el unico puente Store -> UI: como TODOS los appends
// del runner (Publisher de M3, promote y failInterruptedTools de M6/M8) pasan por
// el Store, la UI ve cada evento sin tocar la logica del loop. Los metodos de solo
// lectura delegan sin cambios.
type EmittingStore struct {
	inner session.Store
	bus   *Bus
	mu    sync.Mutex // serializa append+emit: la UI ve los eventos en orden de Seq
}

// NewEmittingStore envuelve inner para reenviar al bus cada evento agregado.
func NewEmittingStore(inner session.Store, bus *Bus) *EmittingStore {
	return &EmittingStore{inner: inner, bus: bus}
}

// var _ session.Store = (*EmittingStore)(nil) asegura que cumple la interface y es
// inyectable en el Runner sin cambios.
var _ session.Store = (*EmittingStore)(nil)

func (s *EmittingStore) AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, err := s.inner.AppendEvent(ctx, sessionID, ev)
	if err != nil {
		return seq, err // fallo del store: no se emite nada a la UI
	}
	ev.SessionID = sessionID
	ev.Seq = seq
	s.bus.Publish(ev)
	return seq, nil
}

// LoadSession / Messages / Epoch / PendingToolCalls delegan sin cambios.
```

Decisions:

- **`Store` decorator, not hook in `Publisher`.** `Publisher` only sees the
 events from the provider stream; **does not** see the `Message{Role:user}` that `promote`
 adds nor the `Tool.Failed` that `failInterruptedTools` adds after a crash.
 Decorating the `Store` captures all three append sites evenly and leaves
 `Run`/`runTurn`/`consume`/`Publisher` intact (M9 hard requirement: loop logic
 does not change).
- **`mu` serializes append+emit to deliver in order of `Seq`.** The `MemoryStore`
 already allocates `Seq` under its own lock, but two settle goroutines could
 forward the `Bus` outside order after returning from the append. The decorator lock
 causes the UI to receive events in the same order as their `Seq`. `EventsEmit` is
 non-blocking, so the cost is minimal. As each event still has its `Seq`,
 the UI could order alone; The lock only makes the contract simpler and the tests
 deterministic. (Lock order: `Publisher.mu` -> `EmittingStore.mu` ->
 `MemoryStore.mu`, always in that sense; no cycle.)
- **Emitted only after a successful append.** If the store fails, a
 event that is not in the log is not resent: the UI never sees something that is not durable.

## 6. The wiring of the app (`app.go`)

`app.go` assembles the loop with real-but-fake dependencies (memory store, fake
provider) and exposes the control to Wails. The construction is factored so that the
tests inject a registering `EmitFunc` and a scripted `FakeProvider`.

```go
type App struct {
	ctx    context.Context // ctx de Wails; lo fija startup. Solo lo usa la EmitFunc real.
	inbox  session.Inbox
	runner *runner.Runner
	bus    *event.Bus

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // sessionID -> cancel de la corrida en vuelo
	wg      sync.WaitGroup                // tests esperan a las corridas (wait); la UI no
}

// newApp arma la app con la frontera (emit) y el provider inyectados, para que los
// tests usen un emit fake y un FakeProvider guionado. NewApp (produccion) la llama
// con runtime.EventsEmit y el provider del momento (M9: el fake; M10: el real).
func newApp(provider llm.Provider, emit event.EmitFunc) *App {
	a := &App{cancels: map[string]context.CancelFunc{}}
	a.bus = event.NewBus(emit)
	store := event.NewEmittingStore(session.NewMemoryStore(), a.bus)
	a.inbox = session.NewMemoryInbox()
	registry := tool.NewRegistry(tool.NewOutputStore(outputLimit), tool.Echo{})
	a.runner = runner.NewRunner(store, a.inbox, provider, registry,
		tool.Permissions{"echo": true}, newIDGen())
	return a
}

// NewApp arma la app de produccion. La EmitFunc cierra sobre a para leer a.ctx
// (que startup fija despues): emitir antes de startup es un no-op (bus nil-safe).
func NewApp() *App {
	var a *App
	emit := func(name string, data ...interface{}) {
		runtime.EventsEmit(a.ctx, name, data...)
	}
	a = newApp(demoProvider(), emit)
	return a
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// SendPrompt admite el texto como prompt en cola y arranca Run en una goroutine.
// Es el binding que el frontend llama al enviar. Admit es durable y no bloquea; el
// loop drena el inbox.
func (a *App) SendPrompt(sessionID, text string) error {
	if err := a.inbox.Admit(context.Background(), sessionID,
		session.Prompt{Text: text}, session.DeliveryQueue); err != nil {
		return err
	}
	a.start(sessionID)
	return nil
}

// Stop cancela la corrida en vuelo de sessionID (boton stop). El ctx cancelado
// interrumpe el turno: M8 cierra las tools en vuelo con Tool.Failed y emite
// Step.Failed. No-op si la sesion no esta corriendo.
func (a *App) Stop(sessionID string) {
	a.mu.Lock()
	cancel := a.cancels[sessionID]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
```

`start` launches `Run` with a session-registered cancelable `ctx` and clears it upon completion; sends through `PublishError` the hard error with which `Run` cuts off an
activity. `wait` (not exported) blocks until flight runs finish:
only used by tests to be deterministic without `sleep`; the UI is fire-and-forget.

```go
func (a *App) start(sessionID string) {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	if old := a.cancels[sessionID]; old != nil {
		old() // una corrida previa de la misma sesion no debe quedar viva
	}
	a.cancels[sessionID] = cancel
	a.mu.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer a.clearCancel(sessionID, cancel)
		if err := a.runner.Run(ctx, sessionID, false); err != nil {
			a.bus.PublishError(sessionID, err)
		}
	}()
}
```

`newIDGen` returns a real `assistantMessageID` generator (an atomic counter
with prefix): unique per process, sufficient for M9 (the `MemoryStore` is reset
with the app). `demoProvider` builds a `FakeProvider` with a short dash (text +
`Step.Ended`) so that `wails dev` shows streaming without a network. `outputLimit` is the
bound of `OutputStore` (a reasonable fixed value).

`main.go` binds `App` and exposes `SendPrompt`/`Stop` (drops `Greet`).

## 7. The minimum frontend (border, manual verification)

`frontend/src/App.vue` is replaced by a minimal UI: an input, a send button
(`SendPrompt`), a stop button (`Stop`) and a list that maps each event of the channel
`session:<id>` to a line. Use `EventsOn` from the Wails runtime and the generated
bindings (`window.go.main.App.SendPrompt/Stop`). It is the **untested border for
`go test`** (same as `runtime.EventsEmit`): it is verified by hand with `wails dev`.
The generated bindings (`frontend/wailsjs/go/main/App.*`) are updated to
reflect `SendPrompt`/`Stop`.

The mapping is direct, 1:1 with the taxonomy: `Text.Delta` adds text to the current
wizard block; `Tool.Called`/`Tool.Success`/`Tool.Failed` show the tool and
its status; `Step.Failed` marks the turn as interrupted/failed.

## 8. TDD Plan

### Safety net

Before touching anything, confirm that M1..M8 are green:

```bash
go test ./...        # toda la suite verde
go vet ./...         # limpio
gofmt -l .           # vacio
```

### Understand

Read the "Integration with Wails" section of `../architecture/agent-loop.md`, the contract
of `session.Store` (`AppendEvent` assigns `Seq`/`SessionID`), the `Publisher` (sole
producer of the durable stream) and the three sites of `AppendEvent` of the runner
(`Publisher.emit`, `promote`, `failInterruptedTools`). Identify that decorating the
`Store` captures all three without touching the loop.

### NETWORK

First test (Store -> UI bridge), in `internal/event/store_test.go`:
`TestEmittingStore_ForwardsAppendedEventToBusWithSeq` — a `AppendEvent` on the
decorator forwards to `Bus` an event **with the `Seq` and `SessionID` assigned** by the internal
store. Fault: `EmittingStore` does not exist.

### GREEN

Implement `event.Bus` (Publish/PublishError on `EmitFunc`) and `EmittingStore`
minimums to pass the specific test.

### TRIANGULATE

- `TestBus_PublishEmitsOnSessionChannel` / `TestBus_PublishErrorEmitsOnErrorChannel`:
 the `Bus` sets the correct channel name (`session:<id>`, `session:<id>:error`) and
 passes the payload; `EmitFunc` nil is no-op (no panic).
- `TestEmittingStore_ReadMethodsDelegate`: `Messages`/`Epoch`/`PendingToolCalls`
 delegate to the inner without forwarding (only `AppendEvent` emits).
- `TestEmittingStore_AppendErrorDoesNotEmit`: if the inner fails, nothing is forwarded.
- `TestEmittingStore_TurnStreamsEventsInSeqOrder` (`-race`): a real `Runner` with
 `FakeProvider` (text + a tool call) on `EmittingStore(MemoryStore, busFake)`
 forwards to the bus the same durable sequence as the log, in order of `Seq`, including
 the `Message{Role:user}` of `promote`. Runs with `-race` (concurrent settlement).
- `TestApp_SendPromptStreamsTurnToBus`: `SendPrompt` admits, starts `Run`, and after
 `wait()` the emit fake registers the events of the turn in the `session:s1` channel.
- `TestApp_StopInterruptsInflightTurn`: with a provider scripted to a tool
 blocking, `SendPrompt` starts the shift, `Stop` cancels, `wait()` returns and the
 register bus `Tool.Failed` + `Step.Failed` (the M8 interrupt travels through the
 wiring). `-race`.

### REFACTOR

Shared test helpers (build an end-to-end runner, a fake concurrent bus),
`doc.go` from the `event` package, comments. Verify that the entire suite is still green
and `-race` clean.

## 9. Acceptance criteria (Done when)

- `event.Bus` forwards `Publish`/`PublishError` to the correct channel via `EmitFunc`;
 nil is no-op.
- `event.EmittingStore` complies with `session.Store`, forwards each aggregated event with its
 `Seq`/`SessionID`, delegates the readings and does not emit on error of the inner.
- `Runner` runs **unchanged** behind `EmittingStore`: M1..M8
 suite remains green.
- `app.go`: `SendPrompt` supports + boots `Run`; `Stop` cancels the run; both
 tested against an emit fake and a `FakeProvider`, including the
 interrupt path (`Tool.Failed` + `Step.Failed` on the bus).
- `go test ./...` green, `go test -race` clean in concurrent tests,
 `gofmt -l .` empty, `go vet ./...` clean.
- Minimal wired frontend (verifiable with `wails dev`): a prompt produces
 visible streaming and stop cuts it. (Border not covered by `go test`.)

## 10. Commands

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Ciclo (test especifico primero)
go test -run TestEmittingStore_ForwardsAppendedEventToBusWithSeq ./internal/event

# Triangulacion (bus, delegacion, error, turno end-to-end, app)
go test -run 'TestBus_|TestEmittingStore_' ./internal/event
go test -run 'TestApp_' .

# Concurrencia (settle concurrente + stop interrumpe)
go test -race -run 'TestEmittingStore_TurnStreams|TestApp_Stop' ./...

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde

# Frontera Wails (verificacion manual, no go test)
wails dev           # enviar un prompt, ver streaming, probar stop
```

## 11. Table of expected evidence

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M1..M8 green before editing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Border Wails and append sites read | `../architecture/agent-loop.md` (Integration with Wails), `store.go`, `publish.go`, `run.go` | identified behavior |
| NETWORK | Store Bridge -> UI written first | `internal/event/store_test.go` + `go test -run TestEmittingStore_ForwardsAppendedEventToBusWithSeq ./internal/event` | expected failure |
| GREEN | `Bus` + `EmittingStore` minimums | `internal/event/{bus,store}.go` | specific test passes |
| TRIANGULATE | Bus, delegation, error, end-to-end shift, app + stop | `go test -run 'TestBus_|TestEmittingStore_|TestApp_' ./...`, `go test -race ...` | cases pass, `-race` clean |
| REFACTOR | Helpers, `doc.go`, minimal frontend | `gofmt -w .`, `go vet ./...`, `go test ./...` | green suite, M1..M8 intact |

## 12. Risks and decisions

- **The logic of the loop does not change.** It is the invariant of M9. All
 visibility to the UI is achieved by decorating the `Store` (injection), not editing `Run`,
 `runTurn`, `consume` or `Publisher`. If any test of M1..M8 changes, M9 is
 done wrong.
- **`Store` decorator vs hook in `Publisher`.** See section 5: the
 decorator captures all three append sites (including the user prompt and cleanup
 after crash); a hook on the `Publisher` would only see the provider's stream.
- **Injected `EmitFunc`, not direct `runtime.EventsEmit`.** Keeps `event/` and the
 tests without dependency on Wails and allows an emit fake. The only place that names
 Wails is the closure of `NewApp` (and `main.go`).
- **`Bus` nil-safe.** `NewApp` arms the `Bus` before `startup` (where the
 `ctx` arrives). Issuing before that is a no-op, not a panic; Also avoid passing a nil `ctx`
 to `runtime.EventsEmit`.
- **Provider fake in M9.** The wiring is tested with the `FakeProvider`; change it to
 the real one is just the argument of `newApp` (M10). Thus M9 verifies the channel without network.
- **`wait()` only for tests.** Starting `Run` in goroutine is fire-and-forget in
 production; Tests need a deterministic synchronization point. A
 `WaitGroup` with a non-exported `wait()` gives it no `sleep` or flakiness, and does not
 change the production behavior.
- **Stop forwards `context.Canceled` for `PublishError`.** Faithful to the
 architecture (`a.bus.PublishError(sessionID, err)`). Distinguishing in the UI a deliberate stop from
 a real failure is UX that is deferred; the error channel already carries the cause.
- **real `nextID` (atomic counter).** Unique per process, sufficient with
 `MemoryStore` (it is restarted with the app). A stable ID between reboots (uuid or
 ULID) arrives with the M10 persistent store.
- **The frontend is border, not TDD-ea.** `go test` covers the `Bus`, the
 `EmittingStore` and `app.go`; the Vue and `runtime.EventsEmit` are checked with
 `wails dev`. Consistent with `AGENTS.md`: "test the runner against a fake EventBus,
 not against Wails".

## 13. Sources

- Roadmap: `../plans/agent-loop-roadmap.md` (milestone M9)
- Architecture: `../architecture/agent-loop.md` (section "Integration with Wails",
 "Streaming of events and execution of tools", "Key differences Go vs TS")
- Way of working: `AGENTS.md`
- Wails runtime (events): https://wails.io/../reference/runtime/events
- Previous specs: `atenea-m1-tipos-store-spec.md` ..
 `atenea-m8-interrupcion-fallos-spec.md`
