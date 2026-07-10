---
updated_at: 2026-07-09
summary: Specification for m5 run turn spec.
---

# Spec M5 — A happy turn (`runTurn`)

`../plans/agent-loop-roadmap.md` Milestone **M5** Executable Spec. Defines the
final state, scope, TDD plan and acceptance criteria for
assembling M1..M4 in **one turn**: the part that builds the `llm.Request` from the
projected history, calls `Provider.Stream` **once**, consumes the stream with
the `Publisher`, seats local tool calls **concurrently** with `errgroup`
and returns `needsContinuation`. A text-only turn persists its events and
returns `false`; a local tool call registers `Tool.Called` before executing,
publishes `Tool.Success` (or `Tool.Failed` if it fails) and returns `true`; several tool
calls run at the same time and the turn **waits** for all of them before deciding.

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

The previous milestones left, from the inside out, all the pieces that the shift
assembles (see the order `tipos -> store -> provider -> publisher -> tools -> turno`
on the roadmap):

- **M1** left the durable domain (`Seq`, `Message`, `Role`, `SessionEvent`,
 `Store`, `MemoryStore`): the event log is the only source of truth and the
 messages are a derived projection (`Store.Messages(sinceSeq)`).
- **M2** left the border with the model (`llm.Provider`, `llm.Request`,
 `llm.Event`, `llm.EventKind`, `llm.Usage`) and a `FakeProvider` that reproduces a
 deterministic script of events through a channel and closes it when finished.
- **M3** left the `Publisher` (`internal/session/runner/publish.go`): translate each
 `llm.Event` to a durable `SessionEvent` with the contract taxonomy, buffers
 the deltas, materializes the wizard's coalesced `Message` into `Text.Ended` and
 maintains the `callID -> toolName` map of the turn.
- **M4** left the `tool.Registry` (`Materialize(perms) -> {Definitions, Settle}`),
 the contract types (`Tool`, `Call`, `Result`, `SettleFunc`, `Permissions`,
 `Materialized`, `UnknownToolError`), the `OutputStore` that limits the large output
 by `callID` and the first builtin executable, `Echo`.

The next brick is the **turn** (`internal/session/runner/turn.go`). Your
responsibility (see `../architecture/agent-loop.md`, "What makes a turn (`runTurn`)"
and "Streaming events and executing tools") is to assemble the above into a
single call to the provider:

- **build** the `llm.Request` from the projected history (`Store.Messages`)
 and the materialized tools with the agent permissions
 (`Registry.Materialize`);
- **call once** `Provider.Stream(ctx, req)` and obtain the channel of the turn;
- **consume** the stream with a `Publisher` of the turn: each event is persisted; and
 each **local** tool call launches a goroutine that nods it (`Settle`),
 publishing `Tool.Success` or `Tool.Failed`;
- **wait** with `errgroup` for **all** tools to nod before deciding;
- **return** `needsContinuation`: `true` if there were at least a local tool call
 (the external loop of M6 will make another turn), `false` if the turn was text only.

M5 builds and tests the **isolated** turn: with the scripted `FakeProvider`, the
`MemoryStore` and the `Registry` with `Echo`, calling `runTurn` directly (without the
external loop `Run`, which is M6). Does not touch Wails or the actual provider.

## 2. Objective

Leave the assembled shift ready and the aggregates it needs:

In `internal/llm` (the request grows to carry history and tools):

- type `Message` (`Role`, `Text`): the projected history converted to
 provider format. Added **additive**;
- the fields `Request.Messages []Message` and `Request.Tools []ToolDef`: M5 populates them
 when constructing the turn (compliance with the note of M2 "Request grows without changing the
 interface" and that of M4 "Request.Tools deferred to M5"). `Provider`, `Event`,
 `Usage` and `FakeProvider` do not change their contract;
- the `Event.ProviderExecuted bool` field: marks a tool call that the provider
 executed itself (it is not settled locally). Additive.

In `internal/session` (durable event grows for tool failure):

- the `SessionEvent.Error string` field: the failure message of a tool
 (`Tool.Failed`). Additive; M8 reuses it for `Step.Failed`.

In `internal/session/runner`:

- the `Publisher.ToolSuccess(ctx, callID, output)` and
 `Publisher.ToolFailed(ctx, callID, cause)` methods: publish the synthetic result of
 a tool (consulting the `callID -> toolName` map of M3) and materialize a
 `Message{Role: tool}` for the model to see in the next turn;
- a **lock** (`sync.Mutex`) in the `Publisher`: in M5 the consumer loop
 publishes from the main goroutine while the `settle`
 goroutines publish `Tool.Success`/`Tool.Failed`. M3 set the lock here;
- the `Runner` (+ `NewRunner`) with its dependencies (`Store`, `Provider`,
 `Registry`, `Permissions`, ID generator);
- `runTurn(ctx, sessionID) (bool, error)`: build the request, call `Stream` once
 once, create the `Publisher` of the shift and delegate to `consume`;
- `consume(ctx, in, pub, settle) (bool, error)`: the architecture loop
 (`for ev := range in`) with `errgroup` to establish concurrent tools;
- behavioral tests that exercise `runTurn` against the fake scripted.

M5 **does not** build the external loop `Run`, nor the `Inbox`/steer, nor the
control signals, nor the interruption by `ctx`, nor `Step.Failed`, nor does it play Wails.

## 3. Scope

### Inside

- `internal/llm/provider.go`: type `Message` (`Role`, `Text`); fields
 `Request.Messages` and `Request.Tools`; field `Event.ProviderExecuted` (all
 additive; M2 and M4 remain green).
- `internal/session/session.go`: field `SessionEvent.Error` (additive; M1/M3
 remain green).
- `internal/session/runner/publish.go`: lock `sync.Mutex` and methods
 `ToolSuccess`/`ToolFailed`.
- `internal/session/runner/runner.go` (new): `Runner`, `NewRunner` and their fields.
- `internal/session/runner/turn.go` (new): `runTurn`, `consume` and the helper
 `toLLMMessages`.
- Behavioral tests in `internal/session/runner/turn_test.go` (new).
- Remove `internal/session/runner/scaffold_test.go`: M3 left it said that the scaffold
 is removed in M5, when `consume` imports `errgroup` in production code
 (`turn.go`); the actual import anchors `errgroup` in `go.mod` without the scaffold.
- Update `internal/session/runner/doc.go` (the loop `runTurn`/`consume`
 landed) and `internal/llm/doc.go` (the `Request` grew with `Messages`/`Tools` and
 the `Event` with `ProviderExecuted`); if applicable, `internal/session/doc.go`.

### Out (do not do on M5)

- The external loop `Run`, the `Inbox` (queue/steer), `Promote`/`HasPending`,
 `MaxSteps`, `StepLimitExceededError`, open new activities — **M6**. M5 leaves
 `runTurn` returning `needsContinuation`; The one who calls it in the loop is M6.
- The control signals (`errRebuildTurn`, `errContinueAfterCompaction`), the
 `runTurn` with its retry loop on `runTurnAttempt`, the `ContextEpoch`, the
 agent/model reselection, the system context/baseline and the compaction by
 overflow — **M7**. On M5 `runTurn` is **one try** happy: read history,
 materialize, set request, call `Stream` once and consume. The wrapper for
 retry by internal signals is added by M7.
- Interrupt by `ctx` canceled mid-shift, errors in the stream from the
 provider, tools that the provider framework executed but never resolved, and
 `failInterruptedTools` (cleanup after crash) — **M8**. In M5 the only fault that
 is handled is the **in-band** one: `Settle` returns error -> `Tool.Failed` (includes the denied/unknown
 tool, which `Settle` rejects with `UnknownToolError`). The
 out-of-band faults (cancellation, stream error, ambiguous state) are from M8.
- The translation `llm.StepFailed -> Step.Failed` with its `Error`: M5 introduces the
 field `SessionEvent.Error` for `Tool.Failed`, but `Step.Failed` (path of
 shift faults) is **M8**. The `Publisher` continues to ignore `llm.StepFailed`.
- `Request.System []Part` and `Request.ProviderOpts` (system context, prompt cache
 key): the happy turn does not need them; they arrive with the system context/baseline in
 **M7**.
- The rich mapping of parts in `llm.Message` (tool_use/tool_result as blocks with
 su `tool_use_id`): the fake ignores the request, so M5 reaches with
 `Role`/`Text`. The actual adapter translates `Role: "tool"` (ID == callID) to a
 `tool_result` block in **M10**.
- `read`/`edit`/`bash`/`write`/`grep`/`glob`: M5 uses `Echo` (M4) as tool local
 executable. The real buildins arrive with their tests.
- `EventBus`, `runtime.EventsEmit`, `app.go`, `main.go`, Wails, frontend — **M9**.
- `Store` SQLite and real `Provider` adapter — **M10**.

## 4. Added to the contract (`llm`, `session`)

### `internal/llm/provider.go` (additive)

The shift builds the request from history. To carry the history and the
tools, the `Request` grows with two fields and the type `Message` appears (the
history projected in the provider format). The `Event` adds `ProviderExecuted` to
distinguish a tool that the provider already ran. None of this changes the
`Provider` interface or the `FakeProvider`.

```go
// Message es un mensaje del historial proyectado en el formato del proveedor. M5
// lo construye desde session.Message (Role/Text) al armar el Request; el adaptador
// real (M10) lo traduce a los bloques de su SDK. Para un mensaje de resultado de
// tool (Role "tool"), el ID del session.Message es el callID, que M10 usa como
// tool_use_id; en M5 alcanza Role/Text porque el fake ignora el Request.
type Message struct {
	Role string
	Text string
}

// Request es la entrada de un turno. M2 lo dejo con solo Model; M5 lo puebla con
// el historial (Messages) y las tools materializadas (Tools) al construir el
// turno. System (system context/baseline) y ProviderOpts (prompt cache key) llegan
// en M7; el campo se agrega cuando esa pieza lo use. El FakeProvider sigue
// ignorando el Request: el guion es la fuente de verdad del turno.
type Request struct {
	Model    string
	Messages []Message // historial proyectado convertido al formato del proveedor
	Tools    []ToolDef // schemas materializados por el registry (M4)
}

// Event ... (M2). ProviderExecuted marca una ToolCall que el proveedor ejecuto el
// mismo: el runner NO la asienta localmente (no lanza Settle), solo la persiste.
// Las tool calls locales (ProviderExecuted == false) las ejecuta Atenea via Settle.
type Event struct {
	Kind             EventKind
	CallID           string
	ToolName         string
	Input            json.RawMessage
	Text             string
	Usage            *Usage
	ProviderExecuted bool // ToolCall ejecutada por el proveedor: solo se persiste
}
```

### `internal/session/session.go` (additive)

`SessionEvent` sum `Error`: the failure message of a tool. M3 deferred to invent
this field "until a test exercises it"; M5's `Tool.Failed` is that test.
M8 reuses it for `Step.Failed`.

```go
type SessionEvent struct {
	SessionID string
	Seq       Seq
	Kind      EventKind

	Message *Message

	Text     string
	CallID   string
	ToolName string
	Input    json.RawMessage
	Usage    *Usage
	Error    string // Tool.Failed (y Step.Failed en M8): mensaje del fallo
}
```

## 5. The turn (`runner`)

### `internal/session/runner/publish.go` (lock + tools result)

The `Publisher` gains a lock and the two methods that publish the synthetic result of a tool. M3 left the map `callID -> toolName` precisely so that
these methods name the tool when settling.

```go
// Publisher ... (M3). En M5 el loop de consumo publica desde la goroutine
// principal (Publish) mientras las goroutines de settle publican el resultado
// (ToolSuccess/ToolFailed); el candado serializa los appends en un orden total y
// protege los buffers y mapas. M3 anticipo "el candado llega en M5 con su test
// -race".
type Publisher struct {
	store     eventAppender
	sessionID string
	asstMsgID string

	mu     sync.Mutex
	text   strings.Builder
	reason strings.Builder
	input  map[string][]byte
	tools  map[string]string
}

// Publish toma el candado y traduce el evento como en M3 (cuerpo identico).
func (p *Publisher) Publish(ctx context.Context, ev llm.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	// ... mismo switch de M3 ...
}

// ToolSuccess publica el resultado de una tool local asentada: persiste un
// Tool.Success con el output acotado (lo que vera el modelo) y materializa un
// Message{Role: tool, ID: callID} para que el resultado entre en la proyeccion y
// el modelo lo vea en el siguiente turno. ToolName sale del mapa del turno.
func (p *Publisher) ToolSuccess(ctx context.Context, callID, output string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.emit(ctx, session.SessionEvent{
		Kind:     session.KindToolSuccess,
		CallID:   callID,
		ToolName: p.tools[callID],
		Text:     output,
		Message:  &session.Message{ID: callID, Role: session.RoleTool, Text: output},
	})
}

// ToolFailed publica el fallo de una tool: persiste un Tool.Failed con el mensaje
// del error en Error y materializa un Message{Role: tool} con ese texto, para que
// el modelo vea que la tool fallo y pueda reaccionar. Cubre el fallo de Execute y
// la tool denegada/desconocida (UnknownToolError de M4).
func (p *Publisher) ToolFailed(ctx context.Context, callID string, cause error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	msg := cause.Error()
	return p.emit(ctx, session.SessionEvent{
		Kind:     session.KindToolFailed,
		CallID:   callID,
		ToolName: p.tools[callID],
		Error:    msg,
		Message:  &session.Message{ID: callID, Role: session.RoleTool, Text: msg},
	})
}
```

`emit` does not change (called with the lock taken). `publish.go` adds
`import "sync"`.

### `internal/session/runner/runner.go` (new)

```go
package runner

import (
	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// Runner ensambla el turno: lee el historial del Store, materializa tools del
// Registry con los permisos del agente, llama al Provider y publica los eventos.
// En M5 expone runTurn (un turno aislado); el loop externo Run (drenar el Inbox,
// MaxSteps) lo agrega M6 sobre esta misma estructura. nextID genera el
// assistantMessageID de cada turno (determinista en tests; un generador real en
// M9).
type Runner struct {
	store    session.Store
	provider llm.Provider
	registry *tool.Registry
	perms    tool.Permissions
	nextID   func() string
}

func NewRunner(store session.Store, provider llm.Provider, registry *tool.Registry,
	perms tool.Permissions, nextID func() string) *Runner {
	return &Runner{store: store, provider: provider, registry: registry, perms: perms, nextID: nextID}
}
```

### `internal/session/runner/turn.go` (new)

```go
package runner

import (
	"context"

	"golang.org/x/sync/errgroup"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// runTurn ejecuta UN turno feliz: arma el Request desde el historial proyectado y
// las tools materializadas, llama Provider.Stream una sola vez, crea el Publisher
// del turno y consume el stream. Devuelve needsContinuation: true si el turno hizo
// al menos una tool call local (el loop de M6 hara otro turno), false si fue solo
// texto. M7 envuelve esto con el retry de senales de control; M5 es el intento.
func (r *Runner) runTurn(ctx context.Context, sessionID string) (bool, error) {
	msgs, err := r.store.Messages(ctx, sessionID, 0)
	if err != nil {
		return false, err
	}
	mat := r.registry.Materialize(r.perms)
	req := llm.Request{Messages: toLLMMessages(msgs), Tools: mat.Definitions}

	in, err := r.provider.Stream(ctx, req)
	if err != nil {
		return false, err
	}
	pub := NewPublisher(r.store, sessionID, r.nextID())
	return r.consume(ctx, in, pub, mat.Settle)
}

// consume drena el stream del turno. Publica cada evento como SessionEvent durable
// (orden total del log) y, por cada tool call LOCAL, lanza una goroutine que la
// asienta y publica su resultado. El turno ESPERA a que todas asienten (g.Wait)
// antes de decidir la continuacion. Una tool provider-executed solo se persiste
// (Publish), no se asienta. El error de una tool se registra como Tool.Failed y NO
// corta el turno; solo un fallo del store (de Publish/ToolSuccess/ToolFailed) lo
// hace.
func (r *Runner) consume(ctx context.Context, in <-chan llm.Event,
	pub *Publisher, settle tool.SettleFunc) (bool, error) {

	g, gctx := errgroup.WithContext(ctx)
	needsContinuation := false
	for ev := range in {
		if err := pub.Publish(ctx, ev); err != nil {
			return false, err
		}
		if ev.Kind == llm.ToolCall && !ev.ProviderExecuted {
			ev := ev // captura para la goroutine
			needsContinuation = true
			g.Go(func() error {
				res, err := settle(gctx, tool.Call{ID: ev.CallID, Name: ev.ToolName, Input: ev.Input})
				if err != nil {
					return pub.ToolFailed(ctx, ev.CallID, err)
				}
				return pub.ToolSuccess(ctx, ev.CallID, res.Output)
			})
		}
	}
	if err := g.Wait(); err != nil {
		return false, err
	}
	return needsContinuation, nil
}

// toLLMMessages convierte el historial proyectado al formato del proveedor.
func toLLMMessages(msgs []session.Message) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = llm.Message{Role: string(m.Role), Text: m.Text}
	}
	return out
}
```

Notes:

- **One `Stream` per turn.** `runTurn` calls `Provider.Stream` exactly one
 time. The provider produces a shift, not the entire session; the M6 ​​loop decides whether
 to invoke another turn after setting tools. This is the heart of the durable design
 (see `../architecture/agent-loop.md`, "What makes a turn").
- **`Tool.Called` before the effect.** `Publish(ev)`'s `ToolCall` persists
 `Tool.Called` **before** launching `settle`'s goroutine. Thus the side effect
 is always registered first (a rule preserved from OpenCode).
- **Concurrency with wait.** Each local tool runs in its goroutine of
 `errgroup`; `g.Wait()` makes the turn wait for **all** before deciding
 `needsContinuation`. The `-race` test is performed with two tools in rendezvous.
- **`gctx` to settle, `ctx` to publish.** `settle` uses `gctx` (if a tool
 returns a hard store error, it cancels the sisters); posts from the
 result use `ctx`. A tool execution failure is not a hard error: it becomes
 becomes `Tool.Failed` and the goroutine returns the store error (nil if the
 append passed), so it does not cancel the others.
- **Provider-executed is only persisted.** A `ToolCall` with
 `ProviderExecuted == true` is published as `Tool.Called` but it does not launch `settle`
 or mark `needsContinuation`: the provider executed it within the same stream.
- **`needsContinuation` only by local tool.** The text-only shift returns
 `false`: the loop does not continue due to wizard text (external loop rule).

## 6. Semantics of the shift

The contract that M5 sets for the external loop (M6) and the projection:

- **Build from history.** `runTurn` reads `Store.Messages(sessionID, 0)`,
 converts it to `[]llm.Message` and materializes the tools with the permissions of the
 agent; the `Request` carries `Messages` (history) and `Tools` (definitions
 sorted by name). The request is rebuilt from the store, never from a live state.
- **Text-only shift.** A `StepStarted, Text.Started/Delta*/Ended,
  StepEnded` script persists its events via the `Publisher` (including the wizard's coalesced `Message`
 and `runTurn` returns `needsContinuation == false`.
- **Local tool call.** A `ToolCall` (local) persist `Tool.Called` **before**
 execute; when settling publishes `Tool.Success` with the output (bounded by the
 `OutputStore`) and materializes a `Message{Role: tool, ID: callID}`; `runTurn`
 returns `true`.
- **Several concurrent tools.** Two `ToolCall` launch two goroutines that run at
 at the same time; the turn waits for both (`g.Wait`) and both `Tool.Success` are
 persisted before `runTurn` returns.
- **Tool failure (in-band).** If `Settle` returns an error (`Execute` failure or
 `UnknownToolError` due to denied/unknown tool), it is published `Tool.Failed` with
 the message in `Error` and a `Message{Role: tool}` with that text; `runTurn` continues
 returning `true` (there was local tool call) and **no** turn error.
- **Provider-executed.** A `ToolCall` with `ProviderExecuted == true` is only
 persisted (`Tool.Called`); does not settle locally (your `Execute` does not run) and does not
 check continuation.
- **Hard error cuts the turn.** If `Provider.Stream` or any `AppendEvent`
 (via `Publish`/`ToolSuccess`/`ToolFailed`) returns error, `runTurn` will propagates.
- **Projection ready for the next turn.** After the turn,
 `Store.Messages(sessionID, 0)` includes the wizard message and the
 `Role: tool` results messages, in order of `Seq`: the next turn (M6) will see them
 in its `Request`.

## 7. TDD Plan

### Safety net

- Green base state before touching anything. M5 adds additive types/fields in `llm`
 and `session`, lock and methods in `Publisher`, and `Runner`; First, the existing one is run
 (M0..M4).
- Command: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Expected result: they pass clean. If something fails, it is reported as pre-existing and
 is not followed blindly. After the additive additions,
 `go test ./internal/llm ./internal/session` is re-run to confirm that M2/M4 (llm) and
 M1/M3 (session) are still green.

### Understand

- Read entry M5 of the roadmap; "What does a turn (`runTurn`)", "Streaming of
 events and execution of tools" and "Provider-executed vs local" of
 `../architecture/agent-loop.md`; and the contracts already set by M3 (`Publisher`,
 map `callID -> toolName`) and M4 (`Materialize`, `Settle`, `Call`).
- Expected behavior: build the request from the history; consume the
 stream publishing each event; seat local concurrent tools and wait for
 all; return `needsContinuation`; provider-executed only persists.

### NETWORK

- Write the test that fails first:
 `TestRunner_TextOnlyTurnPersistsEventsAndStops`. Reference to `NewRunner`,
 `runTurn` and the new fields, which do not yet exist -> does not compile -> fails (Honest RED
 in Go is a compilation failure of the test package).
- The test seeds the `MemoryStore` with a message from user
 (`AppendEvent(SessionEvent{Message: &Message{ID:"u1", Role: RoleUser,
  Text:"hola"}})`), creates a `FakeProvider` with the script `StepStarted,
  Text.Started, Text.Delta("hola "), Text.Delta("mundo"), Text.Ended, StepEnded`,
 a `Registry` with `Echo` and `NewRunner(store, fake, reg, Permissions{"echo":true},
  func() string {return "a1"})`, and asserts:
 - `cont, err := r.runTurn(ctx, "s1")` returns `err == nil` and `cont == false`;
 - `Store.Messages(ctx, "s1", 0)` returns the user and a wizard message
    `{ID:"a1", Role:assistant, Text:"hola mundo"}`.
- Command:
 `go test -run TestRunner_TextOnlyTurnPersistsEventsAndStops ./internal/session/runner`
 -> expected failure.

### GREEN

- Write the minimum: the additive fields in `llm` (`Message`, `Request.Messages`,
 `Request.Tools`, `Event.ProviderExecuted`) and `session` (`SessionEvent.Error`);
 the lock and the methods `ToolSuccess`/`ToolFailed` in `publish.go`; the `Runner`
 in `runner.go`; `runTurn`/`consume`/`toLLMMessages` in `turn.go`.
- Run only the red test to green.
- Command:
 `go test -run TestRunner_TextOnlyTurnPersistsEventsAndStops ./internal/session/runner`.

### TRIANGULATE

Add cases to avoid false green (those in the roadmap):

- `TestRunner_LocalToolCallRegistersCalledThenSettlesSuccess`: hyphen with a
 `ToolCall{CallID:"c1", ToolName:"echo", Input:{"text":"pong"}}`; `Echo`
 allowed. `cont == true` states; that in the persisted log `Tool.Called` (from
 `c1`) appears **before** `Tool.Success` (from `c1`); which `Tool.Success` carries
 `Text == "pong"`; and that the projection includes a `Message{ID:"c1", Role:tool,
  Text:"pong"}`.
- `TestRunner_TwoToolCallsSettleConcurrentlyAndTurnWaits` (`-race`): a
 `barrierTool` whose `Execute` makes `wg.Done(); wg.Wait()` on a
 `sync.WaitGroup` with `Add(2)`; script with two `ToolCall` (`c1`, `c2`) to that tool.
 If the tools ran in series, the first one would block forever in `Wait`
 (deadlock -> timeout): that the test passes proves concurrency. Asserts `cont ==
  true` and that **both** `Tool.Success` were persisted when returning `runTurn`.
- `TestRunner_LocalToolFailureRecordsToolFailed`: a tool whose `Execute` returns
 error (e.g. `Echo` with invalid input `{`, or a spy with `execErr`). It states
 `cont == true`, `err == nil` (the fault is in-band), that
 `Tool.Failed` was persisted with the message in `Error` (and there is **no** `Tool.Success`), and that the
 projection has a `Message{Role:tool}` with the error text.
- `TestRunner_BuildsRequestFromHistoryAndMaterializedTools`: Use a
 `recordingProvider` (captures the `Request` and delegates to a `FakeProvider`). Seed
 a user message; allow `echo`. After `runTurn`, assert that the captured `Request`
 has `Messages` reflecting the history (the user message) and
 `Tools` with the definition of `echo`. Check "build request from
 history".
- `TestRunner_ProviderExecutedToolIsOnlyPersisted`: script with a `ToolCall`
 marked `ProviderExecuted: true` about an allowed spy. It states that the spy's `Execute` counter
 was left at **0** (it was not set locally), that `Tool.Called`
 was persisted, and that `cont == false` (there was no local tool to continue).
- Commands:
 - `go test -run TestRunner ./internal/session/runner`
 - `go test -race -run TestRunner ./internal/session/runner` (the concurrency
    real del turno y del candado del `Publisher`)

### REFACTOR

- Cleanup without changing behavior: factor the test helpers (a
 `seedUser(store, ...)`, a `drain`/read the log of `MemoryStore`, a
 `turnFixture` that creates store+fake+registry+runner) if it reduces duplication; remove
 `scaffold_test.go` (errgroup already comes from `turn.go`); update
 `internal/session/runner/doc.go` (the `runTurn`/`consume` loop landed on M5; the external
 `Run` is still on M6) and `internal/llm/doc.go` (the `Request` grew and the
 `Event` added `ProviderExecuted`); if applicable, `internal/session/doc.go` (the
 `Error` field).
- Verify that the suite is still green after formatting.
- Command: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Acceptance criteria (Done when)

1. There is `llm.Message` (`Role`, `Text`) and `Request` has `Messages []Message` and
 `Tools []ToolDef`; `Event` has `ProviderExecuted bool`. The aggregates are
 additive: `Provider`, `Usage` and the `FakeProvider` did not change (M2/M4 green).
2. `SessionEvent` has `Error string` (additive); M1/M3 remain green.
3. The `Publisher` has a lock and the methods `ToolSuccess(ctx, callID, output)` and
 `ToolFailed(ctx, callID, cause)`, which consult the `callID -> toolName` map of
 M3 and materialize a `Message{Role: tool, ID: callID}`.
4. There is `Runner` (+ `NewRunner`) with `Store`, `Provider`, `Registry`,
 `Permissions` and the ID generator.
5. `runTurn` constructs the `Request` from `Store.Messages` and `Registry.Materialize`,
 calls `Provider.Stream` **once**, consumes the stream and returns
 `needsContinuation`.
6. A text-only turn persists its events (with the coalesced `Message` from the
 wizard) and returns `false`.
7. A local tool call persists `Tool.Called` **before** executing, publishes
 `Tool.Success` with the output (and a `Message{Role: tool}`) and returns `true`.
8. Two tool calls run concurrently and the turn waits for both before
 returns; verified with `-race` and a rendezvous that deadlocks if it were
 sequential.
9. A tool failure (`Execute` with error or tool denied/unknown) publishes
 `Tool.Failed` with the message in `Error`, does not produce `Tool.Success` and does not cause
 the turn to fail (`runTurn` returns `true`, `err == nil`).
10. A `ProviderExecuted` tool call is only persisted (`Tool.Called`); I don't know
    asienta localmente (0 `Execute`) y no marca continuacion.
11. `go test ./...` (and `-race` where applicable) passes; `go vet ./...` clean;
    `gofmt -l .` vacio.
12. There were no changes to `app.go`, `main.go`, Wails, the frontend or
    `internal/event`. En `internal/llm` solo crecieron `provider.go`/`doc.go`; en
    `internal/session` solo `session.go` (campo `Error`) y, si aplica, `doc.go`;
    `memstore.go`/`store.go` intactos. El `scaffold_test.go` de `runner` se
    elimino (errgroup queda anclado por `turn.go`).

## 9. Commands

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Confirmar que M2/M4 (llm) y M1/M3 (session) siguen verdes tras los agregados
go test ./internal/llm ./internal/session

# Ciclo (test especifico primero)
go test -run TestRunner_TextOnlyTurnPersistsEventsAndStops ./internal/session/runner
go test -run TestRunner ./internal/session/runner

# Concurrencia real del turno (dos tools en rendezvous) y candado del Publisher
go test -race -run TestRunner ./internal/session/runner

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
```

## 10. Table of expected evidence

When closing M5, the answer/PR should include this table with actual results:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0..M4 green before editing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Shift contract read | roadmap M5, `../architecture/agent-loop.md`, specs M3/M4 | identified behavior |
| NETWORK | Text-only shift test written first | `turn_test.go` + `go test -run TestRunner_TextOnlyTurnPersistsEventsAndStops ./internal/session/runner` | expected failure (does not compile) |
| GREEN | Aggregates `llm`/`session` + `Publisher` + `runner.go`/`turn.go` minimums | `internal/llm/provider.go`, `internal/session/session.go`, `internal/session/runner/{publish,runner,turn}.go` | specific test passes |
| TRIANGULATE | Tool local (Called->Success), two concurrent, failure, request from history, provider-executed | `go test -run TestRunner ./internal/session/runner`, `go test -race ...` | cases pass, `-race` clean |
| REFACTOR | test helpers, `scaffold_test.go` retired, `doc.go` updated | `gofmt -w internal`, `go vet ./...`, `go test ./...` | green suite, M1..M4 intact |

## 11. Risks and decisions

- **`runTurn` is a single attempt, without control signals.** The
 architecture shows `runTurn` as a retry loop over `runTurnAttempt` (rebuild,
 post-compaction). M5 implements **happy attempt**: read history, materialize,
 weapon request, `Stream` once, consume. The retry wrapper and sentinels
 (`errRebuildTurn`, `errContinueAfterCompaction`) are added by M7 without rewriting the
 body. Advance them would be design without a test that exercises them.
- **`Tool.Success`/`Tool.Failed` materialize a `Message{Role: tool}`.** It is not enough
 to persist the event: the model sees the history via `Store.Messages`, so
 the result of the tool must enter the projection as a `tool` message for
 the next turn (M6) take it on your `Request`. That's why both methods
 set `Message`. The `ID == callID` links the result to its tool call (the
 actual M10 adapter uses it as `tool_use_id`).
- **`Error` in `SessionEvent`, introduced in M5.** M3 deferred the field "until
 a test exercises it". M5's `Tool.Failed` is that test; the field now lands and
 M8 reuses it for `Step.Failed`. A field dedicated to putting the
 error in `Text` was preferred: a fault is not text content, and the frontend (M9) renders it
 differently.
- **Tool fault is in-band, it does not cut the turn.** `Settle` that returns error is registered as `Tool.Failed` and the goroutine returns the error from the **store** (nil
 if the append worked), so it does not cancel the sister tools or cause
 `runTurn` to fail. Only a hard miss from `Stream`/`AppendEvent` cuts the turn. Distinguishes
 "the tool failure" (the model reacts) from "the turn failure" (M8 handles the
 out-of-band). The denied/unknown tool falls on this same path via the
 `UnknownToolError` of M4.
- **Lock on the `Publisher`, now yes.** M3 published serially and deferred the
 mutex. On M5 the `settle` goroutines publish `Tool.Success`/`Tool.Failed`
 while the main loop continues publishing: there is real concurrent access to the store,
 buffers and maps. The lock is taken on `Publish`/`ToolSuccess`/
 `ToolFailed` and serializes the appends in total log order. The test `-race`
 exercises it.
- **`ProviderExecuted` as an additive flag in `llm.Event`.** The roadmap asks
 to distinguish provider-executed from local. The minimal and testable form is a bool in
 tool call event: `consume` skips the local `settle` when it is `true` and the
 tool is only persisted. The complete round-trip of a server-side result is produced by
 only one real provider (fake has no such concept), so its
 end-to-end test is M10; M5 sets the **routing decision** (do not settle locally).
- **The fake ignores the `Request`; the history is verified with a recording.** Since
 the `FakeProvider` plays its script without looking at the `Request`, "build from the
 history" is tested with a test `recordingProvider` that captures the `Request`
 and delegates to the fake. This way, M2's `FakeProvider` is not touched (it remains pristine) and
 avoids duplicating the script playback logic.
- **`Request.Messages`/`Tools` now; `System`/`ProviderOpts` later.** The happy shift
 needs history and tools. The system context/baseline and the prompt cache
 key are from M7 (shift preparation with epoch/compaction); Adding them without that
 machinery would be populating fields that no one uses. It follows the pattern of M2/M4 (the
 `Request` grows without changing the interface).
- **`llm.Message` minimum (`Role`/`Text`).** The projected history of M1 is plain
 text; converting it to `llm.Message{Role, Text}` is enough for the happy turn. The
 rich parts (tool_use/tool_result as blocks) are assembled by the actual adapter in
 M10 from the `Role`/`ID`/`Text` of the `session.Message`. The
 format of the SDK in M5 is not speculated.
- **`nextID` injected.** The `assistantMessageID` of the turn is generated by the runner
 (M3 I anticipate it). Injecting `nextID func() string` leaves the deterministic tests
 (`"a1"`) without dragging a UUID/time dependency; M9 wires a real
 generator. It is the same criterion of "deterministic fakes" as the rest of the plan.
- **`scaffold_test.go` from runner retires.** I keep M3 because it anchored
 `golang.org/x/sync/errgroup` in `go.mod` until the production code would use it
. In M5 `turn.go` imports `errgroup` for `consume`, so the anchor already
 does not depend on the scaffold: it is removed and replaced by `turn_test.go`.
- **Provider-executed does not check continuation.** A tool that the provider executed is
 resolved within the same stream; There is nothing local to set nor a round-trip
 pending, so do not set `needsContinuation`. The continuation is triggered only by
 **local** tool calls (and, in M6, a pending `steer`).

## 12. Sources

- Roadmap: `../plans/agent-loop-roadmap.md` (milestone M5)
- Architecture: `../architecture/agent-loop.md` (sections "What does a turn
 (`runTurn`)", "Streaming of events and execution of tools" — `consume`, register
 before effects, wait for all — and "Provider-executed vs local")
- Way of working: `AGENTS.md`
- Previous specs: `atenea-m1-tipos-store-spec.md`,
 `atenea-m2-provider-fake-spec.md`, `atenea-m3-publisher-spec.md`,
 `atenea-m4-tool-registry-spec.md`
