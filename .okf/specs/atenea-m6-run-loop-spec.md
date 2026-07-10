---
updated_at: 2026-07-09
summary: Specification for m6 run loop spec.
---

# Spec M6 — External Loop (`Run`) + MaxSteps

`../plans/agent-loop-roadmap.md` Milestone **M6** Executable Spec. Defines the
final state, scope, TDD plan and acceptance criteria to wrap
M5's turn (`runTurn`) in the agent's **external loop**: the durable `Inbox`
(queue/steer) and the double loop (activity + steps) with `MaxSteps = 25`. `Run` drains
the inbox of a session until it is idle: it promotes the pending input to messages
in the history, executes a shift, decides whether to continue (only by local tool call or
by pending steer, **never** by wizard text) and, when closing a
activity, opens another if a `queue` remains. Exceeding steps returns
`StepLimitExceededError`.

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

The previous milestones left, from the inside out, all the pieces that the external
loop orchestrates (see the order `tipos -> store -> provider -> publisher -> tools
-> turno -> loop` in the roadmap):

- **M1** left the durable domain (`Seq`, `Message`, `Role`, `SessionEvent`,
 `Store`, `MemoryStore`): the event log is the only source of truth and the
 messages are a derived projection (`Store.Messages(sinceSeq)`).
- **M2** left the border with the model (`llm.Provider`, `llm.Request`,
 `llm.Event`, `llm.EventKind`, `llm.Usage`) and a `FakeProvider` that plays a
 deterministic script of events through a channel and closes it when finished. M2 left the
 fake **replayable**: each `Stream` plays the same script (M6's multi-turn
 loop needs it).
- **M3** left the `Publisher` (`internal/session/runner/publish.go`): translates each
 `llm.Event` to a durable `SessionEvent`, buffers the deltas, materializes the
 `Message` coalesces the wizard into `Text.Ended` and maintains the map
 `callID -> toolName` of the turn.
- **M4** leaves the `tool.Registry` (`Materialize(perms) -> {Definitions, Settle}`),
 the contract types (`Tool`, `Call`, `Result`, `SettleFunc`, `Permissions`,
 `Materialized`, `UnknownToolError`), the `OutputStore` and the builtin `Echo`.
- **M5** left the **shift** (`internal/session/runner/{runner,turn}.go`): the
 `Runner` (+ `NewRunner`), `runTurn(ctx, sessionID) (bool, error)` which builds the
 `llm.Request` from the projected history, calls `Provider.Stream` **once**,
 consumes the stream with the `Publisher`, seats the local
 tool calls concurrently with `errgroup` and returns `needsContinuation`. The lock from
 `Publisher` and `ToolSuccess`/`ToolFailed` also landed on M5.

The next brick is the **external loop** (`internal/session/runner/run.go`).
Its responsibility (see `../architecture/agent-loop.md`, "External Loop (`Run`)", and the
pseudocode of `../architecture/opencode-agent-loop.md`) is to wrap M5's happy turn in
the durable machinery that makes it useful:

- **read** the `Inbox`: if there is no steer or pending queue (and it is not `force`), the
 session is idle and `Run` returns without doing anything;
- **promote** the pending entry: convert the next `queue` (or all the
 `steer`) into `Role: user` messages of the `Store`, so that the next `runTurn` can see the
 in the history;
- **execute** a turn (`runTurn` of M5) and decide the continuation: it continues if
 the turn made a local tool call (`needsContinuation == true`) or if there was a
 `steer` admitted during the run; **does not** continue by wizard text;
- **cut** unproductive loops: at most `MaxSteps = 25` steps per activity;
 exceeding them returns `StepLimitExceededError`;
- **open new activities**: when closing one (the model left the session stable),
 checks if there is another `queue` and, if there is, processes it as an activity new.

M6 builds and tests the loop **against fakes**: the new `MemoryInbox`, the
`MemoryStore`, the `Registry` with `Echo` and test providers (M2's replayable `FakeProvider`
 and, for turns that change script turn by turn, a
multi-turn test provider). It does not touch Wails, nor the actual supplier, nor the
M7 internal control signals.

## 2. Objective

Prepare the external loop and the durable input it needs:

In `internal/session` (session durable input):

- the type `Delivery` (`DeliveryNone`, `DeliveryQueue`, `DeliverySteer`): how a prompt enters the runner;
- the type `Prompt` (`Text`): the text that the user admits; `Promote`) and its implementation in
 memory `MemoryInbox` (+ `NewMemoryInbox`): same pattern as `Store`/`MemoryStore`
 (M10 can add a persistent one behind the same interface).

In `internal/session/runner`:

- the constant `MaxSteps = 25`;
 - the type `StepLimitExceededError` (`Max int`); `Run(ctx, sessionID string, force bool) error`: the external loop (activity) +
 the step loop (`MaxSteps`), with the pseudocode semantics;
- the helper `promote(ctx, sessionID, d)`: removes the delivery
 `d` prompts from the inbox and materializes them as `Message{Role: user}` in the `Store`;
- behavioral tests that exercise `Run` against the inbox and the providers of
 test.

M6 **does not** change the body of `runTurn` (it is still the isolated happy turn of
M5), nor does it add control signals, nor does it interrupt by `ctx`, nor does `failInterruptedTools`,
nor `Step.Failed`, nor does it play Wails.

## 3. Scope

### Inside

- `internal/session/inbox.go` (new): `Delivery` (+ constants), `Prompt`, the
 interface `Inbox` and `MemoryInbox` (+ `NewMemoryInbox`).
- `internal/session/runner/run.go` (new): `MaxSteps`, `StepLimitExceededError`,
 `Run` and the helper `promote`.
- `internal/session/runner/runner.go`: field `inbox` in `Runner` and parameter in
 `NewRunner` (additive).
- Behavior tests in `internal/session/inbox_test.go` (new, the contract
 of the inbox) and `internal/session/runner/run_test.go` (new, the loop).
- Update call to `NewRunner` on `internal/session/runner/turn_test.go`
 (M5): Pass an extra `session.NewMemoryInbox()`. **mechanical** change; the
 M5 tests still call `runTurn` directly (which ignores the inbox) and its
 behavior does not change.
- Update `internal/session/runner/doc.go` (the external `Run` landed on M6) and
 `internal/session/doc.go` (the `Inbox` landed: queue/steer durable).

### Out (do not do on M6)

- The control signals (`errRebuildTurn`, `errContinueAfterCompaction`), the
 `runTurn` with its retry loop over `runTurnAttempt`, the `ContextEpoch`, the
 agent/model reselection, the system context/baseline and the compaction by
 overflow — **M7**. In M6 `runTurn` remains the only happy attempt at M5; the
 loop just calls it in a loop. The promotion of M6 is a **fine pre-step** in `Run`
 (materialize the prompt and that's it); folding it into `runTurnAttempt` (along with
 epoch/agent/compaction) is M7.
- Interruption by `ctx` canceled mid-run, errors from the
 provider stream, `failInterruptedTools` at the beginning of `Run` (cleanup after crash) and the
 closing of hanging tools — **M8**. In M6 the loop propagates the hard error that
 returns `runTurn`/`HasPending`/`Promote`/`AppendEvent`, but does not open the
 out-of-band fault paths or the restart cleanup.
- The refinement "promoting `queue` also drains `steer` to a cutoff"
 (step 5 of `runTurnAttempt`): in M6 `Promote(queue)` outputs **only** the following queued
 prompt and `Promote(steer)` drains the steers; priority
 steer-over-queue at the start of `Run` and `promotion = steer` after the first
 turn cover the milestone scenarios. Fine cutoff (admitted/promoted seq) and rich
 type `Promotion` arrive when M7 needs it.
- `Request.System`/`ProviderOpts`, advanced projected history
 (`EntriesForRunner` with baseline/compaction) — **M7**.
- `EventBus`, `runtime.EventsEmit`, `app.go`, `main.go`, Wails, frontend — **M9**.
 M9 cables `SendPrompt -> inbox.Admit(queue) + go Run(...)` without changing the loop logic
- `Store` SQLite and a persistent `Inbox` and the real `Provider` adapter — **M10**.

## 4. Added to the contract (`session`)

### `internal/session/inbox.go` (new)

The durable input of the session. The prompts do not enter directly into the model: they are first
admitted with a `Delivery` and the runner promotes them to history messages
when he sets up the turn. It is modeled as an interface (same as `Store`) with an in-memory implementation; M10 can add a persistent without touching the runner.

```go
package session

import (
	"context"
	"sync"
)

// Delivery clasifica como entra un prompt durable al runner. queue es el prompt
// principal (uno por actividad abierta); steer es direccionamiento aceptado
// mientras la sesion ya esta corriendo (entra en la siguiente continuacion). El
// valor cero DeliveryNone significa "nada que promover": Run lo usa cuando corre
// con force sin input pendiente, y promote lo trata como no-op.
type Delivery int

const (
	DeliveryNone  Delivery = iota // sin entrega: no hay nada que promover
	DeliveryQueue                 // prompt principal en cola (uno por actividad)
	DeliverySteer                 // direccionamiento aceptado durante la actividad
)

// Prompt es el texto que el usuario admite en el inbox. M6 lleva solo Text; las
// partes ricas (adjuntos, referencias) llegan cuando la UI las necesite.
type Prompt struct {
	Text string
}

// Inbox es el input durable de la sesion. Admit no bloquea y es durable; el loop
// (Run) drena el inbox cuando corre. M6 implementa MemoryInbox; M10 puede agregar
// una version persistente detras de esta misma interface, igual que Store.
type Inbox interface {
	// Admit agrega p al input pendiente de sessionID con la entrega d.
	Admit(ctx context.Context, sessionID string, p Prompt, d Delivery) error

	// HasPending informa si hay algun prompt pendiente de la entrega d.
	HasPending(ctx context.Context, sessionID string, d Delivery) (bool, error)

	// Promote saca de pendientes los prompts de la entrega d que entran al proximo
	// turno y los devuelve en orden de admision: queue saca el SIGUIENTE prompt
	// encolado (FIFO, uno solo); steer saca TODOS los steers pendientes. El runner
	// materializa cada prompt devuelto como un Message{Role: user} en el Store
	// antes de leer el historial. DeliveryNone (o sin pendientes) devuelve nil.
	Promote(ctx context.Context, sessionID string, d Delivery) ([]Prompt, error)
}

// MemoryInbox es la implementacion en memoria del Inbox para M6..M9. Guarda dos
// colas FIFO por sesion (queue y steer) bajo un mutex.
type MemoryInbox struct {
	mu    sync.Mutex
	queue map[string][]Prompt
	steer map[string][]Prompt
}

// NewMemoryInbox crea un inbox vacio listo para usar.
func NewMemoryInbox() *MemoryInbox {
	return &MemoryInbox{queue: map[string][]Prompt{}, steer: map[string][]Prompt{}}
}

// var _ Inbox = (*MemoryInbox)(nil) asegura en compilacion que cumple la interface.
var _ Inbox = (*MemoryInbox)(nil)

func (in *MemoryInbox) Admit(ctx context.Context, sessionID string, p Prompt, d Delivery) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	switch d {
	case DeliveryQueue:
		in.queue[sessionID] = append(in.queue[sessionID], p)
	case DeliverySteer:
		in.steer[sessionID] = append(in.steer[sessionID], p)
	}
	return nil
}

func (in *MemoryInbox) HasPending(ctx context.Context, sessionID string, d Delivery) (bool, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	switch d {
	case DeliveryQueue:
		return len(in.queue[sessionID]) > 0, nil
	case DeliverySteer:
		return len(in.steer[sessionID]) > 0, nil
	}
	return false, nil
}

func (in *MemoryInbox) Promote(ctx context.Context, sessionID string, d Delivery) ([]Prompt, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	switch d {
	case DeliveryQueue:
		q := in.queue[sessionID]
		if len(q) == 0 {
			return nil, nil
		}
		next := q[0]
		in.queue[sessionID] = q[1:]
		return []Prompt{next}, nil
	case DeliverySteer:
		s := in.steer[sessionID]
		if len(s) == 0 {
			return nil, nil
		}
		in.steer[sessionID] = nil
		return s, nil
	}
	return nil, nil
}
```

## 5. The external loop (`runner`)

### `internal/session/runner/runner.go` (field `inbox`, additive)

The `Runner` gains the `Inbox` as a dependency: `Run` reads and promotes the input
durable. `runTurn`/`consume` do not change. `NewRunner` adds the parameter.

```go
type Runner struct {
	store    session.Store
	inbox    session.Inbox
	provider llm.Provider
	registry *tool.Registry
	perms    tool.Permissions
	nextID   func() string
}

func NewRunner(store session.Store, inbox session.Inbox, provider llm.Provider,
	registry *tool.Registry, perms tool.Permissions, nextID func() string) *Runner {
	return &Runner{
		store: store, inbox: inbox, provider: provider,
		registry: registry, perms: perms, nextID: nextID,
	}
}
```

### `internal/session/runner/run.go` (new)

```go
package runner

import (
	"context"
	"fmt"

	"atenea/internal/session"
)

// MaxSteps corta loops improductivos de modelo/tool/continuacion: 25 pasos por
// actividad, igual que el loop de referencia (OpenCode).
const MaxSteps = 25

// StepLimitExceededError lo devuelve Run cuando una actividad agota MaxSteps sin
// dejar la sesion estable (el modelo siguio pidiendo continuacion). Tipo (no
// sentinel) para que la UI (M9) lo distinga de un fallo de proveedor con
// errors.As y muestre el limite.
type StepLimitExceededError struct {
	Max int
}

func (e *StepLimitExceededError) Error() string {
	return fmt.Sprintf("step limit exceeded: %d steps", e.Max)
}

// Run es el loop externo del agente: drena el Inbox de la sesion hasta dejarla
// idle. Si no hay steer ni queue pendiente (y no es force), retorna sin hacer
// nada. Mientras haya una actividad abierta corre el loop de pasos (hasta
// MaxSteps): promueve el input pendiente, ejecuta un turno (runTurn de M5) y
// decide si continuar. El loop NO continua por texto del asistente; solo por una
// tool call local (needsContinuation del turno) o por un steer admitido durante
// la corrida. Al cerrar una actividad revisa si hay otro queue y, si lo hay, abre
// una nueva. El request se reconstruye del Store en cada turno: Run no guarda
// estado vivo entre turnos.
func (r *Runner) Run(ctx context.Context, sessionID string, force bool) error {
	hasSteer, err := r.inbox.HasPending(ctx, sessionID, session.DeliverySteer)
	if err != nil {
		return err
	}
	hasQueue := false
	if !hasSteer {
		if hasQueue, err = r.inbox.HasPending(ctx, sessionID, session.DeliveryQueue); err != nil {
			return err
		}
	}
	if !force && !hasSteer && !hasQueue {
		return nil // sesion idle, nada que hacer
	}

	// failInterruptedTools (limpieza de tools colgadas tras crash) entra en M8.

	promotion := session.DeliveryNone
	switch {
	case hasSteer:
		promotion = session.DeliverySteer
	case hasQueue:
		promotion = session.DeliveryQueue
	}
	openActivity := force || hasSteer || hasQueue

	for openActivity {
		needsContinuation := true

		for step := 0; step < MaxSteps; step++ {
			if err := r.promote(ctx, sessionID, promotion); err != nil {
				return err
			}
			needsContinuation, err = r.runTurn(ctx, sessionID)
			if err != nil {
				return err
			}
			promotion = session.DeliverySteer // tras el primer turno solo se promueve steer

			if !needsContinuation {
				if needsContinuation, err = r.inbox.HasPending(ctx, sessionID, session.DeliverySteer); err != nil {
					return err
				}
			}
			if !needsContinuation {
				break
			}
		}
		if needsContinuation {
			return &StepLimitExceededError{Max: MaxSteps}
		}

		if openActivity, err = r.inbox.HasPending(ctx, sessionID, session.DeliveryQueue); err != nil {
			return err
		}
		if openActivity {
			promotion = session.DeliveryQueue
		}
	}
	return nil
}

// promote saca del inbox los prompts de la entrega d y los materializa como
// mensajes Role:user en el Store, en orden de admision, para que el proximo
// runTurn los vea en el historial. DeliveryNone (o sin pendientes) no agrega
// nada: el turno corre con el historial existente (p.ej. una continuacion tras
// asentar tools). Usa el generador de IDs del runner para el ID del mensaje.
func (r *Runner) promote(ctx context.Context, sessionID string, d session.Delivery) error {
	prompts, err := r.inbox.Promote(ctx, sessionID, d)
	if err != nil {
		return err
	}
	for _, p := range prompts {
		if _, err := r.store.AppendEvent(ctx, sessionID, session.SessionEvent{
			Message: &session.Message{ID: r.nextID(), Role: session.RoleUser, Text: p.Text},
		}); err != nil {
			return err
		}
	}
	return nil
}
```

Notes:

- **A single `runTurn` per step.** The loop calls `runTurn` (M5's happy turn)
 once per step. `runTurn` inside calls `Provider.Stream` once. The
 provider produces a shift, not the entire session; the loop decides whether to invoke another.
- **Promote before reading the history.** `promote` runs **before** `runTurn`
 at each step: it materializes the promoted prompt as `Message{Role: user}` in the
 `Store`, so `runTurn` (which reads `Store.Messages(sessionID, 0)`) sees it. In a
 continuation (promotion `steer` without pending steers), `promote` is no-op and the
 turn runs with the history left by the tools.
- **Continuation: tool local or steer, not text.** `needsContinuation` comes from the
 turn (true if there was a local tool call). If false, the loop checks
 `HasPending(steer)`: a steer admitted during the turn forces another turn. A
 text-only turn with no steer pending closes the activity. It is the central rule
 of the loop (do not continue by text from the wizard).
- **`promotion = steer` after the first turn.** Within an activity, after
 the first step the promotion drops to `steer`: the `queue` has already entered and only the
 live steering affects the following continuations. When a new
 activity is opened (there is another `queue`), it returns to `queue`.
- **`MaxSteps` cuts the loop.** If after 25 steps the turn continues asking for
 continuation (tool after tool not closed), the loop exits with
 `StepLimitExceededError`. Protects against unproductive model loops.
- **New activities.** When closing an activity (the session is stable), the
 loop checks `HasPending(queue)`: if there is another prompt, it opens a new activity with
 `promotion = queue`. Thus `Run` drains several queued prompts in a single
 run.
- **Hard errors are propagated.** Any error from `HasPending`/`Promote`/
 `AppendEvent`/`runTurn` cuts `Run` and is returned. The
 out-of-band failure paths (cancellation, stream dropped, tools hung) are M8.

## 6. Semantics of the loop

The contract that M6 sets (and that M9 wires to the UI without changing it):

- **Idle.** `Run(ctx, sessionID, false)` without steer or pending queue returns
 `nil` without executing any turn (does not call the provider).
- **One queue, one turn, idle.** After `Admit(queue, p)`, `Run` promotes `p` to a
 `Message{Role: user}`, executes one turn; if the turn is text-only
 (`needsContinuation == false`) and there is no steer pending, close the activity; without
 another queue, `Run` returns `nil`. The projection is left with the user and the
 assistant.
- **Continues as long as there are tool calls.** If the turn makes a local tool call,
 `needsContinuation == true` and the loop executes another turn (with the history that
 includes the result of the tool). Chains turns until one leaves the session
 stable.
- **Wizard text does not continue alone.** A text-only turn, with no steer
 pending, **closes** the activity: the loop does not chain turns by text.
- **Steer admitted during the run.** A `steer` admitted while the turn
 runs is detected at the end (`HasPending(steer)`), forces a continuation and in
 the next step `promote(steer)` materializes it as `Message{Role: user}`. The
 projection wins that message and the turn sees it.
- **Second queue opens new activity.** With two prompts in `queue`, `Run` processes
 the first one (an activity), closes it, detects the second pending one and opens a new
 activity that processes it. The projection is left with the two pairs
 user/assistant, in order.
- **Exceed steps.** If an activity exhausts `MaxSteps` with continuation always
 pending, `Run` returns `*StepLimitExceededError{Max: 25}`.
- **Reconstruction from the store.** On each turn `runTurn` rereads the history of the
 `Store`; `Run` does not drag live messages between turns.

## 7. TDD Plan

### Safety net

- Green base state before touching anything. M6 adds new types in `session`
 (`Inbox`/`MemoryInbox`/`Delivery`/`Prompt`), a new file in `runner`
 (`run.go`) and a parameter to `NewRunner`; first the existing one is run (M0..M5).
- Command: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Expected result: they pass clean. The only point that **stops compiling** by
 design is `turn_test.go` (M5): its calls to `NewRunner` are left with one argument
 less when adding `inbox`. They are updated mechanically (pass
 `session.NewMemoryInbox()`); its behavior (call `runTurn` directly) does not
 change. After the change, `go test ./internal/session/runner` is re-run to
 confirm that M5 is still green.

### Understand

- Read entry M6 of the roadmap; "External loop (`Run`)" and "Who makes a turn" from
 `../architecture/agent-loop.md`; the pseudocode and the "Input durable" section,
 "Conditions for continuation" of `../architecture/opencode-agent-loop.md`; and the contract of
 M5 (`runTurn` returning `needsContinuation`).
- Expected behavior: drain the inbox; promote input before turn;
 continue only by tool local or steer; cut to `MaxSteps`; open new
 activities by `queue`.

### NETWORK

- Write the test that fails first:
 `TestRunner_RunProcessesQueuedPromptThenIdle`. Reference `NewRunner` (with the new
 `inbox`), `Run`, `session.NewMemoryInbox`, `session.Prompt`,
 `session.DeliveryQueue`, which do not yet exist -> does not compile -> fails (RED honest
 in Go is a compilation failure of the test package).
- The test builds `store := session.NewMemoryStore()`, `inbox := session.NewMemoryInbox()`,
 `inbox.Admit(ctx, "s1", session.Prompt{Text: "hola"}, session.DeliveryQueue)`, a text-only
 `FakeProvider` (`StepStarted, TextStarted, TextDelta("ok"),
  TextEnded, StepEnded`), a `Registry` with `Echo`, and
 `r := NewRunner(store, inbox, fake, reg, tool.Permissions{"echo": true}, ids())`
 with `ids()` a counter (`"m1","m2",...`). Asserts:
 - `r.Run(ctx, "s1", false)` returns `err == nil`;
 - `Store.Messages(ctx, "s1", 0)` has 2 messages: user `"hola"` and a
    asistente con `Text == "ok"`, en ese orden (el queue se promovio y corrio un
    solo turno que cerro la actividad).
- Command:
 `go test -run TestRunner_RunProcessesQueuedPromptThenIdle ./internal/session/runner`
 -> expected failure.

### GREEN

- Write the minimum: `internal/session/inbox.go` (`Delivery`, `Prompt`, `Inbox`,
 `MemoryInbox`); the `inbox` field and the `NewRunner` parameter; `run.go`
 (`MaxSteps`, `StepLimitExceededError`, `Run`, `promote`). Update calls
 to `NewRunner` in `turn_test.go`.
- Run only the red test to green.
- Command:
 `go test -run TestRunner_RunProcessesQueuedPromptThenIdle ./internal/session/runner`.

### TRIANGULATE

Add cases to avoid false green (those in the roadmap). Two new
test providers (in `run_test.go`), because M2's `FakeProvider` replays the same
script in each `Stream` and the loop calls `Stream` once per turn:

- `scriptedProvider`: returns one **different** script per turn (a list of
 scripts; the ith `Stream` plays the ith; exhausted, empty stream). Each
 `Stream` delegates to a fresh `FakeProvider` with the turn script; counts
 calls (`calls`).
- `steeringProvider`: wraps a provider and, in its **first** `Stream` (with
 `sync.Once`), supports a `steer` in the inbox before delegating. Simulates "steer
 admitted during the run".

Tests:

- `TestRunner_RunContinuesWhileToolCalls`: `scriptedProvider` with two turns
 -> turn 1 `ToolCall{echo}`, turn 2 text only. `Admit(queue, "hola")`. Asserts
 `Run` returns `nil`; that **two** shifts were executed (`prov.calls == 2`); that
 the projection includes the `Message{Role: tool}` of the result of `echo` and the text
 of the turn 2 wizard (continuous through the tool and stopped when stable).
- `TestRunner_RunAssistantTextDoesNotContinueAlone`: `scriptedProvider` (or the replayable
 `FakeProvider`) of text only. `Admit(queue, "hola")`. It states that
 **a single** turn was executed (`prov.calls == 1`) and `Run == nil` (the text does not
 chain another turn). It is the complement of the previous case: it isolates the rule "do not
 continue by text".
- `TestRunner_RunSteerAdmittedDuringRunEntersNextContinuation`: `steeringProvider`
 which in the first `Stream` supports `steer "sigue"`, wrapping a
 `scriptedProvider` of two text-only turns. `Admit(queue, "hola")`. Affirms
 `Run == nil`; that they ran **two** shifts; and that the projection contains a
 `Message{Role: user, Text: "sigue"}` (the steer entered the next
 continuation, although turn 1 was text only). Proof at the same time that the steer
 continues and that the promotion materialized.
- `TestRunner_RunSecondQueueOpensNewActivity`: `FakeProvider` text-only
 (replayable). `Admit(queue, "p1")` and `Admit(queue, "p2")`. `Run == nil`;
 states that the projection remains with the sequence `user "p1"`, assistant, `user "p2"`,
 assistant (two activities, in order of FIFO admission).
- `TestRunner_RunExceedingStepsReturnsStepLimitExceeded`: `FakeProvider`
 **replayable** with a script that **always** makes a `ToolCall{echo}`
 (`StepStarted, ToolCall{c1, echo, {"text":"x"}}, StepEnded`). Each turn continues
 -> after 25 steps the loop exits. `Admit(queue, "loop")`. It states that `Run` returns
 an error and that `errors.As` recognizes it as `*StepLimitExceededError` with
 `Max == 25`.
- (Inbox isolated) `TestMemoryInbox_AdmitHasPendingPromote` in
 `internal/session/inbox_test.go`: `Admit(queue)` -> `HasPending(queue) == true`;
 `Promote(queue)` returns the prompt and exits it (`HasPending(queue) == false`);
 `Promote(steer)` drains all supported steers; `Promote` no pending
 returns `nil`. Sets the inbox contract that the loop assumes.
- Commands:
 - `go test -run 'TestRunner_Run|TestMemoryInbox' ./internal/session/...`
 - `go test -race -run 'TestRunner_Run' ./internal/session/runner` (the loop follows
    asentando tools con `errgroup`/candado del `Publisher`; el caso de tool calls
    corre con `-race`)

### REFACTOR

- Cleanup without changing behavior: factor the `run_test.go`
 test helpers (a `idCounter()` for `nextID`, a `runFixture` that creates store+inbox+provider+
 registry+runner, a projection reader) if it reduces duplication; reuse
 `seedUser`/`hasToolMessage` from `turn_test.go` where applicable. Update
 `internal/session/runner/doc.go` (external `Run` landed on M6; control
 signals are still on M7) and `internal/session/doc.go` (`Inbox` landed:
 queue/steer durable; epoch/projected history still pending).
- Verify that the suite is still green after formatting.
- Command: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Acceptance criteria (Done when)

1. There is `session.Inbox` (`Admit`, `HasPending`, `Promote`) with `MemoryInbox`
 (+ `NewMemoryInbox`), and types `Delivery` (`DeliveryNone`/`DeliveryQueue`/
 `DeliverySteer`) and `Prompt` (`Text`).
2. The `Runner` has the field `inbox session.Inbox` and `NewRunner` receives it
 (additive: `runTurn`/`consume` did not change; M5 is still green after updating the
 construct in `turn_test.go`).
3. There are `MaxSteps = 25` and `StepLimitExceededError` (`Max int`), inspectable
 with `errors.As`.
4. `Run(ctx, sessionID, force)` with the idle session (without steer or queue and without force)
 returns `nil` without executing any shift.
5. After `Admit(queue, p)`, `Run` promotes `p` to `Message{Role: user}`, executes a
 text-only shift, and leaves the session idle (`nil`); the projection remains with the
 user and the assistant.
6. A turn with a local tool call causes the loop to execute another turn; chains until
 a turn leaves the session stable. Verified by counting the turns and the
 projection (message `Role: tool` + final assistant).
7. A text-only turn, with no pending steer, does **not** chain another turn (closes
 the activity). Verified with shift count `== 1`.
8. A `steer` admitted during the run forces a continuation and materializes
 as `Message{Role: user}` in the next step; the projection includes it.
9. With two prompts in `queue`, `Run` processes both in successive activities; the
 projection is left with the two user/assistant pairs in FIFO order.
10. An activity that exhausts `MaxSteps` with continuation always pending returns
    `*StepLimitExceededError{Max: 25}`.
11. `go test ./...` (and `-race` where applicable) passes; `go vet ./...` clean;
    `gofmt -l .` vacio.
12. There were no changes to `app.go`, `main.go`, Wails, the frontend or `internal/event`.
    En `internal/llm` no se toco nada (el fake replayable de M2 alcanza; los
    proveedores multi-turno y de steering son helpers de **test** en `run_test.go`).
    En `internal/session` solo se agrego `inbox.go` (y `doc.go`); `session.go`,
    `event.go`, `store.go`, `memstore.go` intactos. En `runner` solo se agrego
    `run.go`, crecio `runner.go` (campo/param) y `doc.go`; `turn.go`/`publish.go`
    intactos salvo el `turn_test.go` actualizado.

## 9. Commands

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Tras sumar el inbox a NewRunner, confirmar que M5 sigue verde
go test ./internal/session/runner

# Ciclo (test especifico primero)
go test -run TestRunner_RunProcessesQueuedPromptThenIdle ./internal/session/runner

# Triangulacion (loop + contrato del inbox)
go test -run 'TestRunner_Run|TestMemoryInbox' ./internal/session/...

# Concurrencia real (continuacion con tools asentadas en errgroup + candado)
go test -race -run TestRunner_Run ./internal/session/runner

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
```

## 10. Table of expected evidence

When closing M6, the response/PR should include this table with actual results:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0..M5 green before editing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | External loop contract read | roadmap M6, `../architecture/agent-loop.md` (Run), `../architecture/opencode-agent-loop.md` (pseudocode) | identified behavior |
| NETWORK | Test queue->idle written first | `run_test.go` + `go test -run TestRunner_RunProcessesQueuedPromptThenIdle ./internal/session/runner` | expected failure (does not compile) |
| GREEN | `inbox.go` + `run.go` + `inbox` in `NewRunner` minimums; `turn_test.go` updated | `internal/session/inbox.go`, `internal/session/runner/{run,runner}.go` | specific test passes |
| TRIANGULATE | Continue through tools, text does not continue, steer in continuation, second queue, step limit, inbox contract | `go test -run 'TestRunner_Run|TestMemoryInbox' ./internal/session/...`, `go test -race ...` | cases pass, `-race` clean |
| REFACTOR | test helpers, `doc.go` updated | `gofmt -w internal`, `go vet ./...`, `go test ./...` | green suite, M1..M5 intact |

## 11. Risks and decisions

- **Promotion on `Run`, not within `runTurn`.** The architecture shows
 `runTurn(ctx, sessionID, promotion)` promoting within (step 5 of
 `runTurnAttempt`). M6 leaves it as a **pre-fine step** on `Run` (`promote` before
 `runTurn`) and keeps `runTurn(ctx, sessionID)` exactly like M5: the isolated
 happy turn. Thus the M5 shift tests remain unitary and valid, and
 the change is additive. Folding the promotion within `runTurnAttempt` (next to
 epoch/agent/compaction) is M7, when the turn preparation grows; doing it
 now would be moving logic without a test that asks for it at that level.
- **`NewRunner` grows with `inbox`; M5 `turn_test.go` is updated.** The `Runner`
 needs the real inbox (it reads it and promotes it in `Run`), so the dependency
 goes into the constructor. Calls to `NewRunner` on `turn_test.go` add up to a
 `session.NewMemoryInbox()`; They keep calling `runTurn` directly (which ignores the
 inbox), their behavior doesn't change. The honest parameter was preferred to an optional
 setter or parallel constructor: it is the idiomatic form in Go and leaves the
 dependency explicit.
- **`Inbox` as an interface to `MemoryInbox`, just like `Store`.** The durable
 input is modeled behind an interface so that M10 can add a persistent
 version without touching the runner. The in-memory implementation with two FIFO
 queues per session is enough for M6..M9. It's the same criteria "deterministic fakes, the real
 at the end" of the plan.
- **`Promote` returns `[]Prompt`; the runner materializes the `Message`.** The inbox
 only retains and releases prompts; who converts them into `Message{Role: user}` del
 `Store` is the runner (`promote`). Thus the inbox does not depend on the store and the source of truth of the history is still the event log. The type
 `Promotion` rich (with `admitted_seq`/`promoted_seq`/cutoff) of the architecture was deferred:
 M6 does not exercise it and adding it would be populating fields that no one reads until M7.
- **`Promote(queue)` takes one; `Promote(steer)` drains all.** It is the minimum
 semantic that covers the scenarios: `queue` is "one per activity" and `steer`
 is "all pending addressing". The refinement "when promoting queue
 also drain steer until cutoff" is not necessary because the priority
 steer-over-queue at the start of `Run` and the `promotion = steer` after the first
 turn already route the steering. That fine cutoff comes with the epoch in M7.
- **The loop continues through the local tool or steer, never through text.** `needsContinuation`
 of the shift (M5) marks the local tool; the later `HasPending(steer)` marks the
 live steering. A text-only turn without steer closes the activity. It is the
 rule that distinguishes a durable agent from a chat that responds and stops;
 is tested in isolation (`...DoesNotContinueAlone`) and in contrast (`...ContinuesWhileToolCalls`,
 `...SteerAdmittedDuringRun`).
- **`DeliveryNone` as a safe zero value.** `DeliveryNone` is added (not in the
 pseudocode) so that `promote(none)` is a clean no-op when `Run` runs with
 `force` and no pending input, and so that the zero value of `Delivery` does not
 mean "queue" by accident. It is a minimal and defensive deviation from the
 pseudocode.
- **`force` is already included, although M6 exercises it little.** The signature
 `Run(ctx, sessionID, force bool)` is the one that M9 (`app.go`) is going to call
 (`Run(..., false)`); including `force` now avoids changing the signature later and
 satisfies "M9 does not change the loop logic". The M6 ​​tests focus on the
 queue/steer paths; `force` remains wired (it runs even if there is no input) without a
 dedicated test, or with a minimum one if you want to fix the behavior.
- **Multi-shift providers as test helpers.** The `FakeProvider` of M2
 replays the same script in each `Stream`, which serves for the cases of
 repeated-text and step-limit (tool at each turn), but not for a turn-1
 other than turn-2. For this, M6 adds `scriptedProvider` (one script per turn) and
 `steeringProvider` (admits a steer in the first `Stream`) **in `run_test.go`**:
 the production `FakeProvider` and the `Provider` interface are not touched. EachThe loop
 continues always -> after 25 steps it exits with `StepLimitExceededError`. It is the simplest
 way to cause the unproductive loop without a special provider.
- **`promote` uses `nextID` for the ID of the user message.** The promoted
 message needs an ID; is taken from the same injected generator as the
 `assistantMessageID`. In tests, `nextID` is a counter (`"m1","m2",...`) so that
 user and assistant do not collide and the projection is readable. It was avoided to invent a
 inbox ID scheme: the runner generator already fulfills that role and
 M9 wires a real one.

## 12. Sources

- Roadmap: `../plans/agent-loop-roadmap.md` (milestone M6)
- Architecture: `../architecture/agent-loop.md` (sections "External loop (`Run`)",
 "Making a turn (`runTurn`)" and "Durable persistence")
- Reference loop: `../architecture/opencode-agent-loop.md` (loop pseudocode, "Input
 durable", "Conditions of continued")
- Way of working: `AGENTS.md`
- Previous specs: `atenea-m1-tipos-store-spec.md`,
 `atenea-m2-provider-fake-spec.md`, `atenea-m3-publisher-spec.md`,
 `atenea-m4-tool-registry-spec.md`, `atenea-m5-run-turn-spec.md`
</content>
</invoke>
