---
updated_at: 2026-07-09
summary: Specification for m8 interrupcion fallos spec.
---

# Spec M8 — Interrupt + fault handling

`../plans/agent-loop-roadmap.md` Milestone **M8** Executable Spec. Defines the
final state, scope, TDD plan and acceptance criteria to harden
the M5/M6/M7 shift against **interruptions and failures**: mid-shift cancellation of `ctx`
, provider stream errors, unresolved tools, and
crash cleanup. The central rule: **after any failure, the
history is not left with hanging tools** and the turn marks its failure with
`Step.Failed`. A tool is "hanging" when it has a durable `Tool.Called` without a
`Tool.Success` or a `Tool.Failed` to close it; M8 closes that ambiguous state on
each failure path and on resume.

M7's turn (`runTurnAttempt` + `consume`) already seats local tools with
`errgroup` and handles the control signals. M8 adds explicit
fail paths: `consume` closes unresolved tools and issues `Step.Failed` when the
turn is interrupted or the provider fails, using a context **decoupled** from the
cancel so that those close writes survive the canceled `ctx`; and
`Run` runs `failInterruptedTools` at startup to close tools that a crash left
 halfway.

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

The previous milestones left, from the inside out, all the pieces that the turn
assembles, that the external loop orchestrates and that the control signals protect:

- **M1** left the durable domain (`Seq`, `Message`, `Role`, `SessionEvent`,
 `Store`, `MemoryStore`): the event log is the only source of truth and the
 messages are a derived projection (`Store.Messages(sinceSeq)`).
- **M2** left the border with the model (`llm.Provider`, `llm.Request`,
 `llm.Event`, `llm.EventKind`, `llm.Usage`) and a replayable `FakeProvider` that
 plays a deterministic script through a channel and **closes** it when finished.
 Cancel `ctx` cuts the stream (deterministic cut) and closes the channel same:
 no receiver is left hanging. The kind `llm.StepFailed` ("stream failure") already exists in M2's contract, anticipating that M8 would consume it. `callID -> toolName` of the shift. `SessionEvent` already carries the `Error` field (the
 message of a `Tool.Failed`); M1's comment anticipated that "M8 reuses
 for `Step.Failed`". The constants `KindStepFailed` ("it is emitted by M8") and
 `KindToolFailed` ("it is emitted by the runner in M5/M8") are already in `event.go`.
- **M4** left the `tool.Registry` (`Materialize(perms) -> {Definitions, Settle}`),
 the `ToolOutputStore` and the builtin `Echo`.
 `errgroup`; each goroutine publishes `Tool.Success` or, upon an error of
 `Settle`, `Tool.Failed` (in-band failure: does not cut the turn). A tool
 provider-executed is only persisted.
- **M6** left the **external loop** (`internal/session/runner/run.go`): the durable `Inbox`
 and the double loop (activity + steps, `MaxSteps = 25`). `Run` left an explicit
 gap: `// failInterruptedTools (limpieza de tools colgadas tras
  crash) entra en M8` (run.go), just after the idle check and before
 promote.
- **M7** left the **control signals** (`errRebuildTurn`,
 `errContinueAfterCompaction`) and the `ContextEpoch`: retry loop
 on `runTurnAttempt`, which snapshots the epoch when preparing, re-checks it before
 `Stream` and rebuilds or compacts without streaming old state.

The next brick is **explicit fault handling**. Your responsibility
(see `../architecture/agent-loop.md`, "Interrupts and Fault Handling", and
`../architecture/opencode-agent-loop.md`) is not to leave the history in an ambiguous state when
something goes wrong:

- **interruption**: the user cancels the `ctx` of the turn (a "stop" button on M9);
 the in-flight tools are interrupted and closed with `Tool.Failed`, the turn issues
 `Step.Failed` and returns the cancellation error;
- **provider error**: the stream reports a failure (a `llm.StepFailed`) or
 `Stream` fails; the turn emits `Step.Failed` and closes the unresolved tools;
- **unresolved tool**: a tool that the framework provider executed but that the
 failed turn never resolved is closed with `Tool.Failed`;
- **resume after crash**: `failInterruptedTools`, at the start of `Run`, closes
 the tools left `Tool.Called` without result in the durable log of a previous
 run.

M8 builds and tests this **against fakes**: a test tool that blocks until
`ctx` is cancelled, a `FakeProvider` that emits a `llm.StepFailed`, and
hand-seeded `Tool.Called` events in the store to simulate the crash. It doesn't touch
Wails, nor the actual provider, nor SQLite persistence.

## 2. Objective

Leave the turn ready with failure paths and resumption after crash:

In `internal/session` (the context photo):

- the type `PendingTool` (`CallID`, `ToolName string`): a tool call of the durable log
 that was left unclosed (`internal/session/store.go`, next to the interface); `Tool.Called` without a later `Tool.Success`/`Tool.Failed`).

In `internal/session/runner`:

- the type of error `ProviderError` (`Message string`): the failure of the stream of the
 provider, distinguishable with `errors.As` by the UI (M9), the same as
 `StepLimitExceededError` of M6;
- the `Publisher` wins, additively, `StepFailed(ctx, cause)`,
 `FailUnresolvedTools(ctx, cause)` and a set `settled` (the `callID` already closed
 with `Tool.Success`/`Tool.Failed`), to close the ambiguous state of a failed
 turn; the
 cancellation (`context.WithoutCancel`) for closing writes; returns the
 failure error (the cancellation as `context.Canceled`, the stream failure as
 `*ProviderError`); unresolved
 tool provider-executed and crash cleanup.

M8 **does not** change the control signals of M7, nor the `Inbox`, nor the double loop of
`Run` (it only uncomments `failInterruptedTools`), nor does it touch `internal/llm` (the kind
`StepFailed` already exists), nor does it add real Wails or provider/store.

## 3. Scope

### Inside

- `internal/session/store.go`: the type `PendingTool` and the method `PendingToolCalls`
 in the interface `Store`.
- `internal/session/memstore.go`: `MemoryStore.PendingToolCalls` (projection of
 tools hanging on the log; `ErrSessionNotFound` if the session does not exist).
- `internal/session/runner/turn.go`: the type `ProviderError`;
 failure error and the attempt returns it as is (it is not a
 control signal, so `runTurn` does not retry).
- `internal/session/runner/publish.go`: `StepFailed`, `FailUnresolvedTools`, the set
 `settled` and the private helper `failTool` (additives; `ToolSuccess`/`ToolFailed`
 mark `settled`).
- `internal/session/runner/run.go`: `failInterruptedTools` and its call to the start
 of `Run` (uncomments the M6 gap). The body of the double loop does not change.
- Behavior tests in `internal/session/runner/turn_failure_test.go`
 (new): interruption by `ctx`, provider error, tool provider-executed without
 resolving and `failInterruptedTools`. A test of the
 `MemoryStore.PendingToolCalls` contract on `internal/session/memstore_test.go`.
- Update `internal/session/runner/doc.go` (interrupt and handling of
 faults landed) and `internal/session/doc.go` (`PendingToolCalls` landed).

### Out (do not do on M8)

- The **result** of a tool provider-executed (the event with which the provider
 returns the output of a tool that I executed). M8 does **not** model it: in the happy
 path a tool provider-executed is persisted as `Tool.Called` and nothing else (same
 as M5). M8 closes a provider-executed only when the **turn failed** (leaving it
 unresolved) or when a crash left it hanging (`failInterruptedTools`). Closing
 a provider-executed on every clean turn close would require first modeling its
 result event, and that comes with the actual adapter — **M10**. See "Risks and
 decisions".
- **Retries/backoff** of the provider in the event of a temporary failure. M8 reports the
 fault (`*ProviderError`, `Step.Failed`) and cuts the turn; the retry policy
 (how many times, with what wait) comes with the real adapter — **M10**.
- **Question rejected by the user** (a tool that asks for permission and the user
 denies). The architecture lists it as a failure path, but it requires a UI of
 permissions that does not yet exist; M4 permissions are static (`tool.Permissions`).
 It comes with the app — **M9**.
- `EventBus`, `runtime.EventsEmit`, `app.go`, `main.go`, Wails, frontend, and the
 wiring of the "stop" button that cancels the `ctx` — **M9**. M8 tests the interrupt
 by canceling the `ctx` directly in the test, not from the UI.
- `Store` SQLite, persistent `Inbox` and the real `Provider` adapter — **M10**. The
 tests of M1..M8 should remain green with the real store.

## 4. Added to the contract (`session`)

### `internal/session/store.go` (new type + method in the interface)

`failInterruptedTools` needs to know which tools were left half-finished in the durable log
 from a previous run. That response is a **projection** on the log (same as
`Messages`): the `Tool.Called` that do not have a `Tool.Success` nor a `Tool.Failed`
with the same `callID`. It is exposed in the `Store`, it is not rebuilt in the runner,
because the durable log is from the store and `PendingToolCalls` is of the same type as
`Messages` (a view derived from the log).

```go
// PendingTool es una tool call que quedo registrada (Tool.Called) sin un resultado
// (ni Tool.Success ni Tool.Failed) en el log durable: tipicamente porque un crash o
// una interrupcion cortaron la corrida antes de cerrarla. Run la cierra al arrancar
// con failInterruptedTools.
type PendingTool struct {
	CallID   string
	ToolName string
}

type Store interface {
	AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error)
	LoadSession(ctx context.Context, sessionID string) (Session, error)
	Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error)
	Epoch(ctx context.Context, sessionID string) (ContextEpoch, error)

	// PendingToolCalls proyecta sobre el log durable las tool calls colgadas: las
	// que tienen un Tool.Called sin un Tool.Success ni un Tool.Failed posterior, en
	// orden de Called. Run las usa al arrancar para cerrar restos de una corrida
	// previa. ErrSessionNotFound si la sesion no existe.
	PendingToolCalls(ctx context.Context, sessionID string) ([]PendingTool, error)
}
```

### `internal/session/memstore.go` (log projection)

```go
// PendingToolCalls recorre el log de la sesion y devuelve las tool calls que tienen
// un Tool.Called sin un Tool.Success ni Tool.Failed con el mismo CallID: las tools
// que quedaron a medias (crash/interrupcion). Mantiene el orden de Called.
// ErrSessionNotFound si la sesion no existe.
func (s *MemoryStore) PendingToolCalls(ctx context.Context, sessionID string) ([]PendingTool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log, ok := s.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	resolved := make(map[string]bool)
	for _, ev := range log {
		if ev.Kind == KindToolSuccess || ev.Kind == KindToolFailed {
			resolved[ev.CallID] = true
		}
	}
	var pending []PendingTool
	for _, ev := range log {
		if ev.Kind == KindToolCalled && !resolved[ev.CallID] {
			pending = append(pending, PendingTool{CallID: ev.CallID, ToolName: ev.ToolName})
		}
	}
	return pending, nil
}
```

Note: a `Tool.Called` whose result arrived (`Tool.Success`/`Tool.Failed`) **not**
is pending; one without result yes. The order is that of appearance of `Tool.Called`.

## 5. The shift with fault handling (`runner`)

### `internal/session/runner/turn.go` (ProviderError + closure of ambiguous state)

```go
// ProviderError lo devuelve runTurn cuando el stream del proveedor reporta un fallo
// (un llm.StepFailed en el stream). Tipo (no sentinel) para que la UI (M9) lo
// distinga de una interrupcion (context.Canceled) o de un StepLimitExceededError con
// errors.As y muestre el mensaje del proveedor. No es una senal de control: escapa de
// runTurn (el retry loop no lo traga).
type ProviderError struct{ Message string }

func (e *ProviderError) Error() string {
	if e.Message == "" {
		return "provider stream failed"
	}
	return "provider stream failed: " + e.Message
}
```

`consume` wins the closure of the ambiguous state. The skeleton of M5/M7 (
stream range, settle goroutines with `errgroup`, `g.Wait`) is preserved; what's new is:
detect the fault (a `llm.StepFailed` in the stream, a `g.Wait` error, or the
`ctx` cancelled), close unresolved tools and issue `Step.Failed`, all with a
decoupled** context of the cancellation so that closing writes are not
lost when the `ctx` of the shift is already cancelled.

```go
func (r *Runner) consume(ctx context.Context, in <-chan llm.Event,
	pub *Publisher, settle tool.SettleFunc) (bool, error) {

	// Las escrituras de CIERRE (Tool.Failed de una tool interrumpida, Step.Failed)
	// deben sobrevivir a la cancelacion del turno: si usaran el ctx cancelado, un
	// store real las rechazaria y el historial quedaria ambiguo. Se desacoplan de la
	// cancelacion (conservan valores/deadline, pierden el Done).
	cleanupCtx := context.WithoutCancel(ctx)

	g, gctx := errgroup.WithContext(ctx)
	needsContinuation := false
	var streamErr error
	for ev := range in {
		if ev.Kind == llm.StepFailed {
			streamErr = &ProviderError{Message: ev.Text} // fallo del proveedor: lo cierra al final
			continue
		}
		if err := pub.Publish(ctx, ev); err != nil {
			return false, err
		}
		if ev.Kind == llm.ToolCall && !ev.ProviderExecuted {
			ev := ev
			needsContinuation = true
			g.Go(func() error {
				res, err := settle(gctx, tool.Call{ID: ev.CallID, Name: ev.ToolName, Input: ev.Input})
				if err != nil {
					// El cierre de la tool usa cleanupCtx: si la cancelacion fue lo que
					// la corto, el Tool.Failed igual se persiste.
					return pub.ToolFailed(cleanupCtx, ev.CallID, err)
				}
				return pub.ToolSuccess(cleanupCtx, ev.CallID, res.Output)
			})
		}
	}
	waitErr := g.Wait()
	if waitErr != nil {
		return false, waitErr // fallo del store al publicar un resultado: error duro
	}

	// Causa del fallo del turno, si la hubo: error de stream o interrupcion. (waitErr
	// ya se devolvio arriba: solo es un fallo de store, no un cierre de tool.)
	cause := streamErr
	if cause == nil && ctx.Err() != nil {
		cause = ctx.Err()
	}
	if cause != nil {
		if err := pub.FailUnresolvedTools(cleanupCtx, cause); err != nil {
			return false, err
		}
		if err := pub.StepFailed(cleanupCtx, cause); err != nil {
			return false, err
		}
		return false, cause
	}
	return needsContinuation, nil
}
```

Notes:

- **`runTurn`/`runTurnAttempt` do not change.** The fault that `consume` returns
 (`context.Canceled` or `*ProviderError`) **is not** a control signal: the
 `switch` of `runTurn` falls into `default` and returns it without retrying. This is
 important: an interrupt **does not** retry (the user shorted on purpose) and neither does
 a provider error (M8 does not retry; that's M10). The attempt continues
 returning the `consume` error as is.
- **Tool closure uses `cleanupCtx`.** The settle goroutines publish their
 result with `cleanupCtx` (not `ctx`): if the cancellation shorts the tool, their
 `Tool.Failed` is still written. Those of the happy path (without cancellation) do not change:
 `WithoutCancel` of a current ctx behaves the same. `Publish` of the stream continues with
 `ctx`: if `ctx` cancels, `FakeProvider` has already cut the stream, so there are no
 more events to publish; The only thing that matters is the closure. It is the counterpoint of `Step.Ended`.
- **Tools not resolved.** `FailUnresolvedTools` closes any `Tool.Called` of the
 turn that has not received `Tool.Success`/`Tool.Failed`: in practice, a tool
 provider-executed that the failed turn never resolved (the local ones close themselves
 in their goroutine). The `settled` set of `Publisher` prevents double closing.
- **Store failure = hard error.** If `g.Wait` returns an error (only happens if
 `AppendEvent` failed to publish a tool result), `consume` propagates it without
 attempting more writes: the store is broken, there is no way to close nothing.

### `internal/session/runner/publish.go` (StepFailed + FailUnresolvedTools + settled)

```go
type Publisher struct {
	// ...campos de M3/M5...
	settled map[string]bool // callID -> ya cerrado (Tool.Success o Tool.Failed)
}

// NewPublisher inicializa tambien settled (ademas de input/tools).

// StepFailed persiste un Step.Failed durable con el mensaje de la causa: marca que
// el turno fracaso (interrupcion o error de proveedor). No materializa Message: es un
// marcador de turno, no parte de la conversacion que ve el modelo.
func (p *Publisher) StepFailed(ctx context.Context, cause error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.emit(ctx, session.SessionEvent{Kind: session.KindStepFailed, Error: cause.Error()})
}

// FailUnresolvedTools cierra con Tool.Failed cada tool call del turno (mapa tools)
// que aun no se haya cerrado (no esta en settled): tipicamente una provider-executed
// que el turno fallido nunca resolvio. Usa el mensaje de la causa. El orden de cierre
// no esta garantizado (iteracion de mapa); cada cierre es independiente.
func (p *Publisher) FailUnresolvedTools(ctx context.Context, cause error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for callID := range p.tools {
		if p.settled[callID] {
			continue
		}
		if err := p.failTool(ctx, callID, cause.Error()); err != nil {
			return err
		}
	}
	return nil
}

// failTool persiste un Tool.Failed (con su Message{Role: tool}) y marca el callID
// como cerrado. Asume el candado tomado: lo comparten ToolFailed (publico) y
// FailUnresolvedTools.
func (p *Publisher) failTool(ctx context.Context, callID, msg string) error {
	p.settled[callID] = true
	return p.emit(ctx, session.SessionEvent{
		Kind:     session.KindToolFailed,
		CallID:   callID,
		ToolName: p.tools[callID],
		Error:    msg,
		Message:  &session.Message{ID: callID, Role: session.RoleTool, Text: msg},
	})
}
```

M5's `ToolSuccess` and `ToolFailed` flag `settled[callID] = true` (ToolFailed via
`failTool`; ToolSuccess adds the flag before issuing). So `FailUnresolvedTools`
does not re-close a tool that has already been settled.

### `internal/session/runner/run.go` (failInterruptedTools)

```go
// failInterruptedTools cierra, al arrancar Run, las tools que un crash o una
// interrupcion dejaron colgadas en una corrida anterior: un Tool.Called durable sin
// resultado. Por cada una apendea un Tool.Failed (con su Message{Role: tool}) para
// que el historial no quede ambiguo y el modelo vea, en el proximo turno, que esa
// tool no completo. Una sesion sin eventos previos (ErrSessionNotFound) no tiene nada
// que limpiar.
func (r *Runner) failInterruptedTools(ctx context.Context, sessionID string) error {
	pending, err := r.store.PendingToolCalls(ctx, sessionID)
	if errors.Is(err, session.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, pt := range pending {
		if _, err := r.store.AppendEvent(ctx, sessionID, session.SessionEvent{
			Kind:     session.KindToolFailed,
			CallID:   pt.CallID,
			ToolName: pt.ToolName,
			Error:    interruptedToolMsg,
			Message:  &session.Message{ID: pt.CallID, Role: session.RoleTool, Text: interruptedToolMsg},
		}); err != nil {
			return err
		}
	}
	return nil
}

const interruptedToolMsg = "tool interrumpida antes de completar"
```

And in `Run`, the M6 ​​gap is uncommented (after the idle check, before
promote):

```go
	if !force && !hasSteer && !hasQueue {
		return nil // sesion idle, nada que hacer
	}

	if err := r.failInterruptedTools(ctx, sessionID); err != nil {
		return err
	}
```

Notes:

- **`failInterruptedTools` runs once for `Run`, before the first turn.** Clears
 the inherited durable state; the tools of the current run are closed within their
 turn (`consume`), not here.
- **Writes with the `ctx` of `Run`** (not canceled on normal startup): no need
 `cleanupCtx`, unlike `consume`.
- **`ErrSessionNotFound` is not a bug.** A session new (its first prompt still in
 the inbox, without events in the store) does not have tools hanging; is treated as "nothing to
 clean up". M6 `Run` tests (sessions that start with a queue) remain
 green: `PendingToolCalls` returns empty or `ErrSessionNotFound`.

## 6. Shift semantics with fault handling

The contract that M8 establishes (and that M9/M10 retain):

- **Happy path intact.** Without interruption or `llm.StepFailed`, `consume` does not
 detect cause of failure, does not issue `Step.Failed` or close tools, and returns
 `needsContinuation` same as M5/M7. The entire previous suite remains green, including M5's
 tool provider-executed test (clean shutdown: the provider-executed remains
 as `Tool.Called` and M8 does not close it, because the turn did not fail).
- **Interruption (`ctx` canceled mid-turn).** Local tools in flight
 receive `gctx` canceled: their `Settle` returns `ctx.Err()` and the goroutine closes them
 with `Tool.Failed` (written with `cleanupCtx`, durable despite cancellation). The
 turn outputs `Step.Failed` and `consume` returns `context.Canceled`; `runTurn` is propagated by
 (it is not a control signal). After the interruption, there are no hanging tools.
- **Provider error (`llm.StepFailed` in the stream).** `consume` registers the cause
 (`*ProviderError` with the event message), closes the unresolved tools, issues
 `Step.Failed` and returns the `*ProviderError`. If `Stream` input fails (before
 any event), the attempt returns that error as is, without `Step.Failed` (the
 turn did not start).
- **Tool provider-executed unresolved.** On a **failed** turn (interrupt or
 stream error), a tool provider-executed that the framework provider executed but
 never resolved is closed with `Tool.Failed` via `FailUnresolvedTools`. No
 is left hanging.
- **Resumption after crash.** When starting, `Run` closes with `Tool.Failed` every
 `Tool.Called` without result that a crash left in the durable
 log (`failInterruptedTools`). The next turn sees those closures in the history.
- **Closing writes survive cancellation.** Closing tools and
 `Step.Failed` use `context.WithoutCancel(ctx)`: although the turn was interrupted,
 the history remains closed, unambiguous. That is the "Done when" criterion of the milestone.
- **Hard store error cuts the turn.** If `AppendEvent` fails to publish a
 tool result, `g.Wait` propagates it and `consume` returns it without further writes.

## 7. TDD Plan

### Safety net

- Green base state before touching anything. M8 adds an interface type and method
 in `session` (`PendingTool`, `Store.PendingToolCalls`), grows `consume`/`Publisher`
 and uncomments `failInterruptedTools`; first the existing one is run (M0..M7).
- Command: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Expected result: they pass clean. When adding `PendingToolCalls` to the interface
 `Store`, the only real implementer is `MemoryStore` (the test
 decorators `recordingStore`, `epochFlipStore` **embed** it and inherit the method); the fakes
 of `Publisher` (`recordingAppender`/`failingAppender`) implement only
 `eventAppender` (one method), not `Store`, so they don't break. After adding
 `MemoryStore.PendingToolCalls`, run through `go build ./...` and
 `go test ./internal/session/...` to confirm that M1..M7 are still green.

### Understand

- Read entry M8 of the roadmap; "Interrupts and fault handling" and the numbered list
 from `runTurnAttempt` (step 17: "Handle faults, interrupts, context
 overflow and unresolved tools") from `../architecture/agent-loop.md`; `../architecture/opencode-agent-loop.md` fault handling
 M5's `consume` contract (
 settle goroutines, `Tool.Failed` in-band) and the gap that M6 left in `Run`.
- Expected behavior: cancel `ctx` closes tools in flight and issues
 `Step.Failed`; a `llm.StepFailed` cuts the turn with `*ProviderError` closing
 unresolved tools; `failInterruptedTools` cleans debris when starting.

### NETWORK

- Write the test that fails first:
 `TestRunner_CancelDuringTurnFailsInFlightTool`. Reference symbols that do NOT
 yet exist (`pub.StepFailed`/`FailUnresolvedTools`, the closure in `consume`,
 `context.WithoutCancel` in the code) and a new behavior: in Go the change of
 behavior is what fails (not necessarily compilation), so it is run and
 the failure is captured.
- The test uses a `blockingTool` test: its `Execute` closes a channel `started`
 (notices that I start) and then blocks on `<-ctx.Done()`, returning `ctx.Err()`. The
 `FakeProvider` hyphens `StepStarted, ToolCall c1 (blocking), StepEnded`. The test:
 starts `r.runTurn` in a goroutine with a cancelable `ctx`, waits for `<-started`,
 cancels the `ctx`, and picks up `(cont, err)`. It states:
 - `errors.Is(err, context.Canceled)`: the interrupt is propagated;
 - the durable log has a `Tool.Failed` of `c1` and **no** `Tool.Success` of `c1`
    (la tool en vuelo se cerro pese a la cancelacion);
- the log has a `Step.Failed` (the turn marked its failure);### GREEN

- Write the minimum: `PendingTool` + `Store.PendingToolCalls` (interface y
 `MemoryStore`); `ProviderError`, the closure of the ambiguous state in `consume`
 (`cleanupCtx`, cause detection, `FailUnresolvedTools`, `StepFailed`); the set
 `settled` and the methods `StepFailed`/`FailUnresolvedTools`/`failTool` in the
 `Publisher`; `failInterruptedTools` and its call in `Run`.
- Run only the red test to green.
- Command:
 `go test -run TestRunner_CancelDuringTurnFailsInFlightTool ./internal/session/runner`.

### TRIANGULATE

Add cases to avoid false green (those in the roadmap):

- `TestRunner_ProviderStreamErrorEmitsStepFailed`: `FakeProvider` with dash
 `StepStarted`, `StepFailed{Text: "boom"}` (without tools). It states that `runTurn` returns
 an error that `errors.As(err, *ProviderError)` recognizes with `Message` containing
 `"boom"`, that the log has a `Step.Failed` with that message in `Error`, and that there are no
 tools hanging. Isolates the provider error path.
- `TestRunner_ProviderExecutedToolNeverResolvesIsClosed`: script `StepStarted`,
 `ToolCall c1 {ProviderExecuted: true}`, `StepFailed{Text: "boom"}`. The
 provider-executed is only persisted (not settled) and the stream fails before
 resolves it. Asserts: `runTurn` returns `*ProviderError`; the log has the
 `Tool.Called` of `c1` **and** a `Tool.Failed` of `c1` (the provider-executed without
 resolve closed); `PendingToolCalls` empty. Checks "marked tool executed by
 the provider that never resolves".
- `TestRunner_RunFailsInterruptedToolsBeforeTurn`: manually seeding a
 session in the store with a `Message{Role: user}` and a `Tool.Called` of `c1` (without result:
 simulates the crash). `Admit` a prompt on `queue`, build a `Runner` with a text-only
 `recordingProvider` and run `r.Run(ctx, "s1", false)`. Asserts:
 - after `Run`, `store.PendingToolCalls("s1")` returns **empty** (the hang is
    cerro);
- the projection contains a `Message{ID: c1, Role: tool}` with the text of
    interrupcion (el modelo vera que `c1` no completo);
- the new shift has run (there is a message from the assistant). Check
    "`failInterruptedTools` al inicio limpia restos de una corrida previa".
- (Store contract) in `internal/session/memstore_test.go`,
 `TestMemoryStore_PendingToolCallsFindsUnresolved`: in a session with `Tool.Called
  c1`, `Tool.Called c2`, `Tool.Success c2`, `PendingToolCalls` returns only `c1`;
 a non-existent session returns `ErrSessionNotFound`; a session without tools returns
 empty. Sets the contract that `failInterruptedTools` assumes.
- Commands:
 - `go test -run 'TestRunner_Cancel|TestRunner_ProviderStream|TestRunner_ProviderExecuted|TestRunner_RunFailsInterrupted|TestMemoryStore_PendingToolCalls' ./internal/session/...`
 - `go test -race -run 'TestRunner_Cancel|TestRunner_ProviderExecuted' ./internal/session/runner`
    (la interrupcion y el cierre concurrente ejercen `errgroup`, el candado del
    `Publisher` y `cleanupCtx`: el caso critico de `-race`).

### REFACTOR

- Cleanup without changing behavior: factor the `blockingTool` and the
 seeding helpers (`seedToolCalled`) if they reduce duplication; reuse
 `seedUser`/`recordingProvider`/`idCounter`/`newRecordingStore`/`seqOfKind` from the
 M5/M6/M7 tests where applicable. Update `internal/session/runner/doc.go` (the
 interruption by `ctx`, the closing of unresolved tools, `Step.Failed` and
 `failInterruptedTools` landed on M8; M9/M10 connect Wails and the real) and
 `internal/session/doc.go` (`PendingToolCalls` landed: the projection of tools
 hanging for resumption after crash).
- Verify that the suite is still green after formatting.
- Command: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Acceptance criteria (Done when)

1. There are `session.PendingTool` (`CallID`, `ToolName`) and
 `Store.PendingToolCalls(ctx, sessionID) ([]PendingTool, error)`; `MemoryStore` implements it
 by projecting the `Tool.Called` without result (`ErrSessionNotFound` if the
 session does not exist). M1..M7 remain green.
2. There is `ProviderError` (`Message string`) in `runner`, distinguishable with
 `errors.As`; It is not a control signal (it escapes `runTurn`).
3. The `Publisher` has `StepFailed`, `FailUnresolvedTools` and the `settled` set;
 `ToolSuccess`/`ToolFailed` mark `settled`; `FailUnresolvedTools` does not re-close an already seated
 tool.
4. Canceling `ctx` mid-turn closes the tools in flight with `Tool.Failed`
 (written with `context.WithoutCancel`, durable despite cancellation), issues
 `Step.Failed`, and causes `runTurn` to return `context.Canceled` (checked with
 `errors.Is`). After the interruption `PendingToolCalls` is empty.
5. A `llm.StepFailed` in the stream causes `runTurn` to return a `*ProviderError`
 with the event message, emit `Step.Failed`, and close unresolved tools.
6. An unresolved tool provider-executed on a failed turn is closed with
 `Tool.Failed` (does not hang).
7. `Run` runs `failInterruptedTools` at startup (the M6 ​​gap): closes with
 `Tool.Failed` each `Tool.Called` without durable log result and materializes its
 `Message{Role: tool}`; a session without events does not fail.
8. The happy path (without interruption or `StepFailed`) does not emit `Step.Failed` nor
 closes tools: the turn behaves like M5/M7 (its green tests unmodified,
 including that of tool provider-executed, which in a clean close is not closed).
9. `go test ./...` (and `-race` where applicable) passes; `go vet ./...` clean;
 `gofmt -l .` empty.
10. There were no changes to `app.go`, `main.go`, Wails, the frontend or `internal/event`.
    En `internal/llm` no se toco nada (el kind `StepFailed` ya existia). En
    `internal/session` solo se agrego `PendingTool` y `PendingToolCalls` en
    `store.go`/`memstore.go` y se actualizo `doc.go`; `session.go`, `event.go`,
    `epoch.go`, `inbox.go` intactos. En `runner` crecieron `turn.go` (`ProviderError`
    + cierre en `consume`), `publish.go` (`StepFailed`/`FailUnresolvedTools`/`settled`)
    y `run.go` (`failInterruptedTools` + su llamada) y `doc.go`; `runner.go` intacto.
    Las senales de control de M7 (`runTurn`/`runTurnAttempt`) no cambiaron.

## 9. Commands

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Tras sumar PendingToolCalls a la interface Store, confirmar que compila y que M1..M7 siguen verdes
go build ./...
go test ./internal/session/...

# Ciclo (test especifico primero)
go test -run TestRunner_CancelDuringTurnFailsInFlightTool ./internal/session/runner

# Triangulacion (error de proveedor, provider-executed sin resolver, failInterruptedTools, contrato del store)
go test -run 'TestRunner_Cancel|TestRunner_ProviderStream|TestRunner_ProviderExecuted|TestRunner_RunFailsInterrupted|TestMemoryStore_PendingToolCalls' ./internal/session/...

# Concurrencia real (interrupcion + cierre concurrente: errgroup, candado del Publisher, cleanupCtx)
go test -race -run 'TestRunner_Cancel|TestRunner_ProviderExecuted' ./internal/session/runner

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
```

## 10. Table of expected evidence

When closing M8, the response/PR should include this table with actual results:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0..M7 green before editing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Interruption and failure contract read | roadmap M8, `../architecture/agent-loop.md` (interrupts/fault handling), `../architecture/opencode-agent-loop.md`, `consume` of M5, gap of M6 | identified behavior |
| NETWORK | Interruption test by ctx written first | `turn_failure_test.go` + `go test -run TestRunner_CancelDuringTurnFailsInFlightTool ./internal/session/runner` | expected failure |
| GREEN | `PendingToolCalls` + close at `consume` + `StepFailed`/`FailUnresolvedTools` + `failInterruptedTools` minimums | `internal/session/{store,memstore}.go`, `internal/session/runner/{turn,publish,run}.go` | specific test passes |
| TRIANGULATE | Provider error, unresolved provider-executed, failInterruptedTools, store contract | `go test -run 'TestRunner_Cancel|TestRunner_ProviderStream|TestRunner_ProviderExecuted|TestRunner_RunFailsInterrupted|TestMemoryStore_PendingToolCalls' ./internal/session/...`, `go test -race ...` | cases pass, `-race` clean |
| REFACTOR | test helpers, `doc.go` updated | `gofmt -w internal`, `go vet ./...`, `go test ./...` | green suite, M1..M7 intact |

## 11. Risks and decisions

- **`consume` closes ambiguous state; `runTurn`/`runTurnAttempt` do not change.** M8
 concentrates the fault handling in `consume` (the only one that has the `Publisher`, the
 tools goroutines and the stream in view) and leaves the retry loop and the attempt of M7
 intact. The failure error (`context.Canceled`, `*ProviderError`) is not a control signal: it falls into the `default` of `runTurn` and is returned. So M7 (rebuild/
 compaction) and M6 (`Run`) are not touched, and the fault raises as a hard error that cuts
 the activity.
- **Decoupled context for closing writes (`context.WithoutCancel`).**
 The fine point of the milestone: if the closing of an interrupted tool or the `Step.Failed`
 will use the already canceled `ctx`, a real store would reject them and the history would remain
 with the tool hanging — just what M8 avoids. `context.WithoutCancel` (Go 1.21+,
 available in go 1.23) preserves values ​​and deadline but discards `Done`, so the
 closing write survives the cancellation. With `MemoryStore` (which ignores the
 `ctx`) the effect is not observed, but the code is correct for the real store of
 M10; It is documented so that the actual adapter doesn't break it. The `Settle` of the tool if
 receives `gctx` (cancellable): we want the tool to **cut**, but its closure
 **to be written**.
- **`Step.Failed` only after starting the shift.** If `provider.Stream` fails from
 input (before any event), the attempt returns that error without issuing
 `Step.Failed`: there was no `Step.Started`, so there is no turn to mark as failed,
 and the `Publisher` was not even created (it is created after `Stream`). The `Step.Failed` is the
 symmetrical closure of a turn that **did** start (a `StepStarted` already started) and then
 was interrupted or failed. It is a simple and faithful rule: each `Step.Failed` has its
 `Step.Started`.
- **Provider-executed unresolved: it only closes on failure, not on clean closure.**
 M8 closes an unresolved provider-executed tool when the **turn fails**
 (`FailUnresolvedTools` on the failure path) or when a crash leaves it hanging
 (`failInterruptedTools`). **Does not** close it in a clean turn closure, because the
 result of a provider-executed (the event with which the provider returns its
 output) is not yet modeled: doing so would close all legitimate provider-executed
 as "failed". That keeps the `TestRunner_ProviderExecutedToolIsOnlyPersisted` of
 M5 green (clean closure: the provider-executed is only persisted) without changing its
 semantics. The result of provider-executed and its clean closure arrive with the actual
 adapter — **M10**. It is an explicit scope decision, not an oversight.
- **`PendingToolCalls` in the `Store`, not in the runner.** The question "what tools
 were left hanging in the log" is a projection of the durable log, just like
 `Messages`. Putting it in the `Store` (which owns the log) instead of exposing the raw
 log and rebuilding it in the runner keeps the small interface and the
 projection logic together with its data. `EventKind` and `SessionEvent` already live in `session`, so
 the scan does not couple the store to anything new. The real driver (SQLite) will implement the
 same projection with a query — **M10**.
- **`failInterruptedTools` with direct `AppendEvent`, not with a `Publisher`.** Runs
 when starting `Run`, outside of any turn (there is no stream, nor `assistantMessageID`,
 nor settle concurrency): a `Publisher` (which is a shift) would be noise. Append
 the `Tool.Failed` directly to the store with its `Message{Role: tool}`, just as you would
 `failTool` but without the on-call status. Maintains `failInterruptedTools` as a
 fine, self-contained clean.
- **`ErrSessionNotFound` as "nothing to clean".** A new session starts `Run`
 with its prompt still in the inbox (no events in the store): `PendingToolCalls` gives
 `ErrSessionNotFound`. `failInterruptedTools` swallows it and continues. Requiring that
 the caller seed the session before `Run` is avoided, and M6's `Run` tests (which start
 from scratch) remain green unchanged.
- **Interrupt is not retried.** Unlike M7's
 control signals (which `runTurn` swallows and retries), `context.Canceled` and `*ProviderError` are
 return: an interruption is intentional (the user shorts) and a
 provider error in M8 cuts the turn (the retry with backoff is M10). The retry loop of
 M7 distinguishes exactly that with its `switch`: only the two signals retry.
- **`Step.Failed` without `Message` (turn marker).** Unlike `Tool.Failed`
 (which materializes a `Message{Role: tool}` so that the model sees the failure of the
 tool), `Step.Failed` does not materialize `Message`: is a marker that the turn
 failed, not part of the conversation. The projection (`Messages`) does not include it; the
 UI (M9) observes it for the durable event. Thus an interrupted turn does not inject a
 spurious message to the history that the model would see.
- **`FailUnresolvedTools` closing order not guaranteed.** Iterates the map `tools`
 (non-deterministic order). For a single dangling it is irrelevant; with several, tests
 assert membership in the set, not order. A stable order (by `Seq` del
 `Tool.Called`) is deferred when a test requests it; adding it now would be precision
 without a consumer.

## 12. Sources

- Roadmap: `../plans/agent-loop-roadmap.md` (milestone M8)
- Architecture: `../architecture/agent-loop.md` (sections "Interrupts and
 fault handling", the numbered list of `runTurnAttempt` —step 17— and "Event streaming and
 tool execution")
- Reference loop: `../architecture/opencode-agent-loop.md` (fault handling, interrupts
 and `failInterruptedTools`)
- Way of working: `AGENTS.md`
- Previous specs: `atenea-m1-tipos-store-spec.md`,
 `atenea-m2-provider-fake-spec.md`, `atenea-m3-publisher-spec.md`,
 `atenea-m4-tool-registry-spec.md`, `atenea-m5-run-turn-spec.md`,
 `atenea-m6-run-loop-spec.md`, `atenea-m7-control-signals-spec.md`
