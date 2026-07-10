---
updated_at: 2026-07-09
summary: Specification for m2 provider fake spec.
---

# Spec M2 — Provider + fake scriptable

`../plans/agent-loop-roadmap.md` Milestone **M2** Executable Spec. Defines the
final state, scope, TDD plan and acceptance criteria to leave the
border with the model: the interface `Provider`, the types `Request`, `Event`,
`EventKind` and `Usage`, and a scriptable `FakeProvider` that emits a deterministic
 sequence of `llm.Event` through a channel and closes it when finished.

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

M1 left the durable domain (`Seq`, `Message`, `SessionEvent`, `Store`,
`MemoryStore`). The next brick outwards (see the order
`tipos -> store -> provider -> publisher` on the roadmap) is the **border with the
model**: the `internal/llm` package.

The central architectural decision (see `../architecture/agent-loop.md`, "Main
Types" and "Event Streaming") is that **a turn is a single call to the
provider that produces an event channel and closes it upon completion**. The runner
(M5) consumes that channel with `for ev := range in`; the publisher (M3) translates each
event into a durable `SessionEvent`. Canceling the `ctx` interrupts the turn, just like a "stop" button in the UI.

To build M3..M8 with deterministic tests and without a network, you need a fake provider: `FakeProvider`. Plays a fixed script of events, allowing
to write reproducible scenarios (one turn with text, another with tool calls, another
that cancels) without touching Anthropic. The actual adapter (Claude/Anthropic, see
`../architecture/llm-claude.md`) goes into **M10**, behind this same interface.

## 2. Objective

Leave at `internal/llm`:

- the interface `Provider` with `Stream(ctx, Request) (<-chan Event, error)`;
- the types of the stream contract: `Request`, `Event`, `EventKind`, `Usage`;
- a `FakeProvider` (+ `NewFakeProvider`) that:
 - emits its event script in order on a new channel,
 - **closes** the channel when finished (no receiver hangs),
 - cuts sending if `ctx` is canceled (turn interruption),
 - plays the same script in each call to `Stream` (deterministic, without
    mutar el guion);
- the compilation assertion `var _ Provider = (*FakeProvider)(nil)`;
 - behavioral tests that replace M0's `scaffold_test.go`, with the concurrent
 case (goroutine + channel) run with `-race`.

M2 **does not** add the publisher, the mapping to `SessionEvent`, the actual adapter, the
tools registry, or the runner.

## 3. Scope

### Inside

- `internal/llm/provider.go`: interface `Provider`, types `Request`, `Event`,
 `EventKind` (+ constants) and `Usage`.
- `internal/llm/fake.go`: `FakeProvider` + `NewFakeProvider` + assertion
 `var _ Provider`.
- Behavior tests in `internal/llm` (replaces `scaffold_test.go`).

### Out (do not do in M2)

- `publish.go`: `llm.Event -> SessionEvent` translation, delta coalescing and
 the `Step.* / Text.* / Reasoning.* / Tool.*` taxonomy — M3.
- Real adapter `AnthropicProvider` (mapping the SDK stream to `llm.Event`,
 caching, adaptive thinking, compaction) — M10.
- Enrich `Request` with `System []Part`, `Messages`, `Tools []ToolDef` and
 `ProviderOpts`, and build it from the projected history — M5.
- `Registry.Materialize`, `Settle`, builtins — M4.
- `runTurn`, `consume`, `errgroup`, `needsContinuation` — M5.
- Handling supplier error, mid-shift interruption and unresolved
 tools — M8.
- Script **several** different shifts per run (script queue) — arrives
 when M5/M6 request it with a test.
- Any touch to `app.go`, `main.go`, Wails or the frontend — M9.

## 4. Provider contract (`provider.go`)

`internal/llm/provider.go`:

```go
package llm

import (
	"context"
	"encoding/json"
)

// Provider es la frontera con el modelo. Stream produce exactamente UN turno:
// emite los eventos del turno por el channel y lo CIERRA al terminar. El runner
// (M5) lo consume con `for ev := range out`. Cancelar ctx interrumpe el turno
// (equivale a una interrupcion de usuario) y tambien cierra el channel: ningun
// receptor queda colgado. M2 implementa un fake en memoria; el adaptador real
// (Claude/Anthropic) entra en M10 detras de esta misma interface.
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

// Request es la entrada de un turno. En M2 lleva solo el modelo; el fake lo
// ignora (el guion es la fuente de verdad del turno). El runner (M5) le agrega
// System, Messages, Tools y ProviderOpts cuando construye el request desde el
// historial proyectado. Crece sin cambiar la interface Provider.
type Request struct {
	Model string
}

// EventKind clasifica cada evento del stream del proveedor. El conjunto refleja
// 1:1 los eventos de sesion del contrato del loop (ver "Eventos publicados" en
// ../atenea-agent-loop.md), menos los que produce el runner y no el proveedor
// (Tool.Success / Tool.Failed). El publisher (M3) mapea estos kinds a eventos
// durables de sesion; M2 solo los define y el fake los reproduce.
type EventKind int

const (
	StepStarted EventKind = iota // arranca el turno            -> Step.Started
	StepEnded                    // cierra el turno con tokens  -> Step.Ended (lleva Usage)
	StepFailed                   // fallo del stream            -> Step.Failed

	TextStarted // abre un bloque de texto      -> Text.Started
	TextDelta   // fragmento de texto           -> Text.Delta   (lleva Text)
	TextEnded   // cierra el bloque de texto    -> Text.Ended

	ReasoningStarted // abre razonamiento        -> Reasoning.Started
	ReasoningDelta   // fragmento de razonamiento -> Reasoning.Delta (lleva Text)
	ReasoningEnded   // cierra razonamiento      -> Reasoning.Ended

	ToolCall // el modelo invoca una tool       -> Tool.Called (lleva CallID, ToolName, Input)

	ToolInputStarted // abre el input de la tool -> Tool.Input.Started (lleva CallID)
	ToolInputDelta   // fragmento del input JSON -> Tool.Input.Delta   (lleva CallID, Input)
	ToolInputEnded   // cierra el input de la tool -> Tool.Input.Ended (lleva CallID)
)

// Event es un evento del stream de un turno. Kind decide que campos son
// relevantes; el resto queda en cero. Input es el JSON crudo del input de una
// tool: se parsea con json.Unmarshal, nunca por match de string (el modelo puede
// escapar el JSON distinto entre turnos). Usage solo viene en StepEnded.
type Event struct {
	Kind     EventKind
	CallID   string          // ToolCall / ToolInput*
	ToolName string          // ToolCall
	Input    json.RawMessage // ToolCall / ToolInputDelta: input JSON (crudo)
	Text     string          // TextDelta / ReasoningDelta
	Usage    *Usage          // solo StepEnded
}

// Usage son los tokens reportados al cerrar el turno (StepEnded). El proveedor
// real (M10) los completa; el fake los guiona; el publisher (M3) los persiste en
// Step.Ended.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	ReasoningTokens  int
	CacheReadTokens  int
	CacheWriteTokens int
}
```

Design Decision: `Event` and `EventKind` are **transcription** of the contract already
fixed by the design (`../architecture/agent-loop.md` "Main Types" and "Events
published", and the mapping table of `../architecture/llm-claude.md`), not invention of
M2. The entire taxonomy is defined once so that the fake scripts realistic
turns and M3 has a stable target on which to write its mapping. On the other hand
`Request` is left minimal (only `Model`) and grows in M5, just as M1 leaves
`Message` with only `Text` and is enriched by M3. The `EventKind -> SessionEvent`
mapping is from M3; M2 does not implement it.

## 5. The fake scriptable (`fake.go`)

`internal/llm/fake.go`:

```go
package llm

import "context"

// FakeProvider es un Provider determinista para tests sin red. Reproduce un
// guion fijo de eventos en cada llamada a Stream y cierra el channel al
// terminar. Ignora Request (como MemoryStore ignora ctx en M1): el guion es la
// fuente de verdad del turno. Vive fuera de un _test.go a proposito, para que
// los tests del publisher (M3) y del runner (M5+) puedan importarlo.
type FakeProvider struct {
	Script []Event
}

// NewFakeProvider crea un fake que reproducira script en cada Stream.
func NewFakeProvider(script ...Event) *FakeProvider {
	return &FakeProvider{Script: script}
}

// var _ Provider = (*FakeProvider)(nil) asegura en compilacion que FakeProvider
// cumple la interface.
var _ Provider = (*FakeProvider)(nil)

// Stream emite el guion por un channel nuevo y lo cierra al terminar (defer
// close). Si ctx ya esta cancelado al inicio de una iteracion, corta el envio y
// cierra igual; si el productor queda bloqueado en un envio, el case ctx.Done lo
// desbloquea. En ningun caso queda una goroutine colgada.
func (p *FakeProvider) Stream(ctx context.Context, _ Request) (<-chan Event, error) {
	out := make(chan Event)
	go func() {
		defer close(out)
		for _, ev := range p.Script {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out, nil
}
```

Notes:

- **Channel without buffer + producer goroutine**: the receiver sets the rhythm
 (back-pressure), the same as the real stream. That's why the case is run with
 `-race`.
- **Guaranteed closure**: `defer close(out)` closes by draining the script or by
 cutting by `ctx`. The receiver terminates its `for range` without blocking.
- **`Request` ignored**: the fake does not read `Request`; the script defines the turn. It is
 the same pattern as M1 (`MemoryStore` ignores `ctx` out of fidelity to the
 interface). The parameter is named `_` to make it explicit.
- **Immutable script**: `Stream` only reads `p.Script`, it does not mutate or consume it, so
 two calls reproduce the same thing (a multi-turn loop in M6 needs it).
- **No setup error in M2**: `Stream` always returns `nil` as error. The
 second value of the contract is for setup faults of the real provider (M10)
 or fault injection (M8); a mid-turn failure is modeled by canceling
 `ctx`, not returning error here.

## 6. Semantics of the stream

The contract that the fake sets for all downstream consumers:

- **Order**: the events come out in the order of the script.
- **Fidelity**: each `Event` arrives with its fields intact (`Text`, `CallID`,
 `ToolName`, `Input`, `Usage`); the fake does not create or discard fields.
- **Termination**: when the script is exhausted, the channel closes; the `for range`
 ends alone.
- **Empty script**: `Stream` opens and closes the channel without emitting anything; the
 `for range` does not deliver events and does not block.
- **Cancellation (deterministic cut)**: with a `ctx` already canceled when calling
 `Stream`, the `ctx.Err()` check at the top of the first short iteration before
 emitting: the receiver receives zero events and the channel is closed. This is how
 proves, without flakiness, that "canceling the ctx cuts the stream".
- **Cancellation mid-turn**: if the `ctx` is canceled when the producer is already
 blocked on a shipment, the `case <-ctx.Done()` unblocks it and closes the
 channel (without hanging goroutine). A shipment already "in flight" to a ready receiver
 can be completed before the cutoff; the guarantee is that the stream **terminates**,
 not an exact count of events delivered. The exact count under stroke is
 M8 (interrupt), not M2.

## 7. TDD Plan

### Safety net

- Green base state before touching anything. M2 replaces
 `internal/llm/scaffold_test.go`, so first the existing
 (packages M0 + `session` of M1) is run.
- Command: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Expected result: they pass clean. If something fails, it is reported as pre-existing and
 is not followed blindly.

### Understand

- Read entry M2 of the roadmap; "Main Types", "Event Streaming", and
 "Published Events" from `../architecture/agent-loop.md`; the mapping table of
 `../architecture/llm-claude.md`.
- Expected behavior: `Stream` emits an event script and closes the channel;
 cancel `ctx` cuts the stream; empty script closes immediately.

### NETWORK

- Write the test that fails first:
 `TestFakeProvider_StreamEmitsScriptedEventsThenClosesChannel`. Reference to
 `NewFakeProvider`, `Stream`, `Event`, `EventKind`, `Usage`, which do not yet exist
 -> does not compile -> fails (Honest RED in Go is a compilation failure of the
 test package).
- The test scripts a realistic turn (Step/Reasoning/Text/Tool/StepEnded), drains the
 channel with `for range`, and compares what was received against the script with
 `reflect.DeepEqual`.
- Command:
 `go test -run TestFakeProvider_StreamEmitsScriptedEventsThenClosesChannel ./internal/llm`
 -> expected failure.

### GREEN

- Write the minimum: `provider.go` (interface + types) and `fake.go`
 (`FakeProvider`, `NewFakeProvider`, `Stream`).
- Run only the red test until green.
- Command:
 `go test -run TestFakeProvider_StreamEmitsScriptedEventsThenClosesChannel ./internal/llm`.

### TRIANGULATE

Add cases to avoid false green:

- `TestFakeProvider_StreamEmptyScriptClosesImmediately`: empty script ->
 `for range` delivers zero events and terminates without blocking.
- `TestFakeProvider_CanceledCtxCutsStream`: `ctx` canceled before `Stream` ->
 zero events (`< len(script)`) and the channel closes. It is run with `-race`.
- `TestFakeProvider_StreamPreservesToolAndUsageFields`: a `ToolCall` with
 `CallID`/`ToolName`/`Input` and a `StepEnded` with `*Usage` -> the fields arrive
 intact (guard against a fake that fabricates or discards fields).
- `TestFakeProvider_StreamIsReplayable`: two calls to `Stream` from the same fake
 return the same script (guard against mutating/consuming `Script`).
- Commands:
 - `go test -run TestFakeProvider ./internal/llm`
 - `go test -race -run TestFakeProvider ./internal/llm`

### REFACTOR

- Cleanup without changing behavior: update the package comment in
 `internal/llm/doc.go` (no longer "the interface and the fake arrive in M2"), delete
 `scaffold_test.go`, leave consistent test names and helpers (e.g. a
 helper `drain(out) []Event`).
- Verify that the suite is still green after formatting.
- Command: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Acceptance criteria (Done when)

1. There is interface `Provider` with `Stream(ctx, Request) (<-chan Event, error)`
 in `internal/llm/provider.go`.
2. There are the types `Request`, `Event`, `EventKind` (with their set of constants) and
 `Usage` in `provider.go`.
3. `FakeProvider` (+ `NewFakeProvider`) implements `Provider` (verified by
 `var _ Provider`).
4. `Stream` outputs the script in order, with the fields of each `Event` intact, and
 **closes** the channel when finished.
5. Empty script -> `for range` does not deliver events and terminates.
6. `ctx` canceled before `Stream` -> zero events and channel closed; no
 goroutine hangs (clean `-race`).
7. Two calls to `Stream` from the same fake reproduce the same script.
8. M0's `scaffold_test.go` was replaced by behavioral tests.
9. `go test ./...` (and `-race` where applicable) passes; `go vet ./...` clean;
 `gofmt -l .` empty.
10. There were no changes to `app.go`, `main.go`, Wails or the frontend; nor in others
    paquetes `internal/` fuera de `llm`.

## 9. Commands

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Ciclo (test especifico primero)
go test -run TestFakeProvider_StreamEmitsScriptedEventsThenClosesChannel ./internal/llm
go test -run TestFakeProvider ./internal/llm

# Concurrencia (goroutine + channel)
go test -race -run TestFakeProvider ./internal/llm

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
go test -race ./internal/llm
```

## 10. Table of expected evidence

When closing M2, the response/PR should include this table with actual results:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0+M1 green before editing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Stream contract read | roadmap M2, `../architecture/agent-loop.md`, `../architecture/llm-claude.md` | identified behavior |
| NETWORK | Script test+written closing first | `fake_test.go` + `go test -run TestFakeProvider_StreamEmitsScriptedEventsThenClosesChannel ./internal/llm` | expected failure (does not compile) |
| GREEN | `Provider`/minimum rates + `FakeProvider` | `provider.go`, `fake.go` | specific test passes |
| TRIANGULATE | Empty script, cancellation, fidelity, replay | `go test -run TestFakeProvider ./internal/llm`, `go test -race ...` | cases pass, `-race` clean |
| REFACTOR | `doc.go`, delete `scaffold_test.go`, helper `drain` | `gofmt -w internal`, `go vet ./...`, `go test ./...` | green suite |

## 11. Risks and decisions

- **Complete taxonomy of `EventKind` in M2**: the complete set is defined for
 purpose, not just the four that the roadmap names (text, reasoning,
 tool-call, step-finish). It is not speculation: it is a transcription of the contract already
 decided in `../architecture/agent-loop.md` ("Published Events") and the table of
 `../architecture/llm-claude.md`. Setting it once gives M3 a stable target and
 prevents enum churn between milestones. The fake scripts a turn that touches almost all
 kinds, so the set is exercised by tests, not dead.
- **`Request` minimum (only `Model`)**: the fake ignores it, so adding
 `System`/`Messages`/`Tools`/`ProviderOpts` now would be speculative. They are added in M5
 when the runner builds the request from the history, without touching the interface
 `Provider`. Same criteria as M1 with `Message`.
- **Deterministic cancellation via `ctx` pre-cancel**: mid-turn cut
 under race is inherently non-deterministic (an in-flight send can
 be completed). For a test without flakiness, the cut is demonstrated with an already canceled `ctx`
 and the `ctx.Err()` check at the top of the iteration. The exact count
 under concurrent interrupt is M8.
- **Fake outside of `_test.go`**: `FakeProvider` is test infrastructure
 reusable by other packages (publisher M3, runner M5+), so it lives in exported
 `fake.go`, not in a test file. Same criteria as `MemoryStore`
 in M1.
- **A single script per fake**: M2 does not script several different turns per run.
 The external loop (M6) calls `Stream` several times; When a test requires it,
 adds a queue of scripts after the same `FakeProvider` without changing the
 interface. It is not advanced without testing.
- **`Stream` does not return an error in M2**: the setup error path is left for the
 real provider (M10) and the fault injection (M8). A turn failure is modeled
 by canceling `ctx`. Keeping `error` in the signature is a contract, not debt.

## 12. Sources

- Roadmap: `../plans/agent-loop-roadmap.md` (milestone M2)
- Architecture: `../architecture/agent-loop.md` (sections "Main types",
 "Event streaming and tool execution", "Published events")
- LLM integration: `../architecture/llm-claude.md` (SDK event mapping table
 to `llm.Event`; M2 = fake, M10 = adapter real)
- Way of working: `AGENTS.md`
- Previous specs: `atenea-m0-scaffolding-spec.md`,
 `atenea-m1-tipos-store-spec.md`
