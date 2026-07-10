---
updated_at: 2026-07-09
summary: Specification for m3 publisher spec.
---

# Spec M3 — Publisher (events)

`../plans/agent-loop-roadmap.md` Milestone **M3** Executable Spec. Defines the
final state, scope, TDD plan and acceptance criteria to leave the
**publisher**: the piece that translates the provider stream (`llm.Event`) to
durable session events (`SessionEvent`) with the contract taxonomy
(`Step.* / Text.* / Reasoning.* / Tool.*`), buffering the deltas to emit
also a final event with the full content concatenated.

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

M1 left the durable domain (`Seq`, `Message`, `SessionEvent`, `Store`,
`MemoryStore`): the event log is the only source of truth and the messages are
a derived projection. M2 left the border with the model (`llm.Provider`,
`llm.Event`, `llm.EventKind`, `llm.Usage`) and a `FakeProvider` that reproduces a
deterministic script of events over a channel.

The next brick outwards (see the order
`tipos -> store -> provider -> publisher` in the roadmap) is the **publisher**: the
piece that is between the provider stream and the store. Your responsibility
(see `../architecture/agent-loop.md`, "Published events") is:

- translate each `llm.Event` into a durable `SessionEvent` with the name of the
 contract that the frontend (M9) maps 1:1 (`Step.Started`, `Text.Delta`,
 `Tool.Called`, ...);
- **buffer** the deltas of text, reasoning and tool input to emit,
 in addition to each delta, a final event with the complete content concatenated;
- maintain the `assistantMessageID` of the turn (to materialize the coalesced
 message from the wizard) and a map of tool calls by `callID` (which M5
 will consult when entering results).

The publisher lives at `internal/session/runner/publish.go`. In M5 the runner creates it
in turn and feeds it from the consumption loop (`for ev := range in`,
see "Streaming of events and execution of tools"). M3 builds it and tests it
**isolated**: feeding it with a `llm.Event` script (that of `FakeProvider` from
M2 or events by hand) and checking the `SessionEvent` that persists.

## 2. Objective

Prepare the publisher and the durable taxonomy that persists:

In `internal/session` (the form of the durable event is from the domain):

- the type `EventKind` (string) with the contract constants
 (`KindStepStarted`, `KindTextDelta`, `KindToolCalled`, ...);
- the type `Usage` (mirror of `llm.Usage`, so as not to couple `session` to `llm`); `SessionEvent` with `Kind` and the
 payload fields (`Text`, `CallID`, `ToolName`, `Input`, `Usage`).

In `internal/session/runner`:

- the `Publisher` (+ `NewPublisher`) with `Publish(ctx, llm.Event) error`, which:
 - translates each `llm.Event` to the corresponding `SessionEvent` and persists it with
    `AppendEvent`;
- buffers the text and reasoning deltas and outputs the closed block
    (`Text.Ended` / `Reasoning.Ended`) con el texto completo concatenado;
- buffers the JSON input of each tool by `callID` and closes it
    (`Tool.Input.Ended`) con el input completo;
- materializes the wizard's coalesced `Message` (with the
    `assistantMessageID` del turno) al cerrar un bloque de texto, de modo que la
    proyeccion `Messages` de M1 lo devuelva **sin** cambiar el fold ni la
    interface `Store`;
- maintains the `callID -> toolName` map of the tool calls of the turn;
- behavioral tests in `internal/session/runner` that feed the publisher
 with `llm.Event` scripts and verify the persisted `SessionEvent`.

M3 **does not** add `ToolSuccess`/`ToolFailed`, the consumer loop (`consume`,
`errgroup`), `runTurn`, nor touch Wails.

## 3. Scope

### Inside

- `internal/session/event.go`: type `EventKind` + contract constants and type
 `Usage`.
- `internal/session/session.go`: enrich `SessionEvent` with `Kind`, `Text`,
 `CallID`, `ToolName`, `Input`, `Usage` (additive; M1 remains green).
- `internal/session/runner/publish.go`: `Publisher`, `NewPublisher`, `Publish` and
 the minimum interface `eventAppender`.
- Behavior tests on `internal/session/runner/publish_test.go`.

### Out (do not do in M3)

- `Publisher.ToolSuccess` / `Publisher.ToolFailed`: issued by the **runner** when
 seating tools (not the provider's stream); They are written with their test in **M5**
 (result of `settle`) and **M8** (interruption/failure). The `callID ->
  toolName` map that M3 maintains is precisely what those methods will consult.
- Management of `llm.StepFailed` -> `Step.Failed` with its `Error`: it is the path of
 failures — **M8**.
- `consume`, `errgroup`, concurrent execution of tools and waiting for the turn —
 **M5**.
- Concurrency on the `Publisher` (mutex): in M3 `Publish` is called serially
 from the consumption loop; concurrent access (settle by writing
 `Tool.Success`/`Tool.Failed` while the loop consumes) arrives with its test
 `-race` in **M5**.
- Persistence of tools provider-executed (the provider returns the result and
 the runner only persists it) — **M5**.
- Build the `llm.Request` from projected history, `EntriesForRunner`,
 baseline/compaction — **M5/M7**.
- `EventBus` and `runtime.EventsEmit` (the border with Wails) — **M9**.
- `Store` SQLite and real adapter — **M10**.
- Any touch to `app.go`, `main.go`, Wails or the frontend — **M9**.

## 4. Durable taxonomy (`event.go`) and enriched `SessionEvent`

The form of the durable event is from the domain `session`: the frontend (M9) consumes
`SessionEvent` with its `Kind`, and the publisher (in `runner`) is the producer. That's why the taxonomy lives in `internal/session`, not in `runner`.

`internal/session/event.go`:

```go
package session

// EventKind nombra cada evento durable de sesion dentro de la taxonomia del
// contrato de streaming (ver "Eventos publicados" en ../atenea-agent-loop.md).
// Es un string estable, no un int: el frontend (M9) lo mapea 1:1 a la UI y un
// nombre legible se entiende en logs y en el store. El publisher (M3) es el
// unico productor. Los eventos de M1 sin taxonomia quedan con Kind == "".
type EventKind string

const (
	KindStepStarted EventKind = "Step.Started"
	KindStepEnded   EventKind = "Step.Ended"
	KindStepFailed  EventKind = "Step.Failed" // lo emite M8 (manejo de fallos)

	KindTextStarted EventKind = "Text.Started"
	KindTextDelta   EventKind = "Text.Delta"
	KindTextEnded   EventKind = "Text.Ended"

	KindReasoningStarted EventKind = "Reasoning.Started"
	KindReasoningDelta   EventKind = "Reasoning.Delta"
	KindReasoningEnded   EventKind = "Reasoning.Ended"

	KindToolInputStarted EventKind = "Tool.Input.Started"
	KindToolInputDelta   EventKind = "Tool.Input.Delta"
	KindToolInputEnded   EventKind = "Tool.Input.Ended"

	KindToolCalled  EventKind = "Tool.Called"
	KindToolSuccess EventKind = "Tool.Success" // lo emite el runner en M5
	KindToolFailed  EventKind = "Tool.Failed"  // lo emite el runner en M5/M8
)

// Usage son los tokens del turno que el publisher persiste en Step.Ended. Es un
// espejo de llm.Usage: la direccion de import es runner -> {session, llm}, asi
// que session no depende de llm; el publisher copia los campos al cruzar la
// frontera. Solo viene en eventos Step.Ended.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	ReasoningTokens  int
	CacheReadTokens  int
	CacheWriteTokens int
}
```

`internal/session/session.go` (additive enrichment of `SessionEvent`):

```go
// SessionEvent es el evento durable: la unica fuente de verdad de la sesion. El
// Store asigna SessionID y Seq al agregarlo; el llamador los deja en cero.
//
// M1 dejo solo SessionID/Seq/Message: un evento podia materializar un mensaje
// (Message != nil) o no aportar a la proyeccion (Message == nil). M3 lo enriquece
// de forma ADITIVA con la taxonomia de streaming. Kind nombra el evento del
// contrato; los campos de payload (Text, CallID, ToolName, Input, Usage) llevan
// el dato segun el Kind y el resto queda en cero. Los eventos delta (Text.Delta,
// Reasoning.Delta, Tool.Input.*) dejan Message == nil para no aportar a la
// proyeccion; solo el evento que cierra un bloque de texto del asistente
// (Text.Ended) materializa el Message ya coalescido. Asi la proyeccion de M1
// (Messages) sigue funcionando sin cambiar el fold ni la interface Store.
type SessionEvent struct {
	SessionID string
	Seq       Seq
	Kind      EventKind // taxonomia del contrato; "" en eventos sin taxonomia

	Message *Message // proyeccion: set solo al cerrar un bloque de texto del asistente

	// Payload de streaming, relevante segun Kind:
	Text     string          // Text.Delta/Ended, Reasoning.Delta/Ended (fragmento o texto completo)
	CallID   string          // Tool.*
	ToolName string          // Tool.Called (y Tool.Success/Tool.Failed en M5)
	Input    json.RawMessage // Tool.Called / Tool.Input.* (input JSON crudo o coalescido)
	Usage    *Usage          // solo Step.Ended
}
```

`session.go` adds `import "encoding/json"`.

Design Decision: Enrichment is **additive**. The M1 tests construct
`SessionEvent{}` and `SessionEvent{Message: &m}` and compare `Message`; the new
fields start at zero and do not break anything. And the key: the publisher writes an
`Message` **already complete** in `Text.Ended`, so the fold of M1 (`Messages`
goes through the events and takes the `Message != nil`) **does not change**. The deltas persist with `Message == nil` and do not contaminate the projection. This satisfies M1's
note ("M3 coalesces multiple deltas into a single message without changing the
Interface Store or the shape of the SessionEvent") by moving the coalescing to the publisher
rather than the fold: `memstore.go` is left untouched.

## 5. The publisher (`publish.go`)

`internal/session/runner/publish.go`:

```go
package runner

import (
	"context"
	"strings"

	"atenea/internal/llm"
	"atenea/internal/session"
)

// eventAppender es lo unico que el Publisher necesita del Store: agregar eventos
// durables. Aceptar la interface minima (no el Store completo) deja el publisher
// testeable con un spy de un solo metodo y honra "acepta interfaces chicas". El
// session.Store real la cumple; en M5 el runner le pasa el Store de la sesion.
type eventAppender interface {
	AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error)
}

// Publisher traduce el stream de un turno (llm.Event) a eventos durables de
// sesion (SessionEvent) con la taxonomia del contrato, y bufferiza los deltas
// para emitir tambien el bloque cerrado con el contenido completo. Es de un solo
// turno: el runner (M5) crea uno por turno con el assistantMessageID de ese
// turno. En M3 Publish se llama en serie; el acceso concurrente (Tool.Success
// desde settle) llega en M5 con su candado y su test -race.
type Publisher struct {
	store     eventAppender
	sessionID string
	asstMsgID string // assistantMessageID del turno

	text   strings.Builder          // buffer del bloque de texto en curso
	reason strings.Builder          // buffer del bloque de razonamiento en curso
	input  map[string][]byte        // input JSON acumulado por callID
	tools  map[string]string        // callID -> toolName (mapa de tool calls del turno)
}

// NewPublisher crea el publisher de un turno. assistantMessageID es el ID con el
// que se materializa el Message coalescido del asistente (lo genera el runner en
// M5; en los tests se pasa fijo para poder afirmarlo).
func NewPublisher(store eventAppender, sessionID, assistantMessageID string) *Publisher {
	return &Publisher{
		store:     store,
		sessionID: sessionID,
		asstMsgID: assistantMessageID,
		input:     make(map[string][]byte),
		tools:     make(map[string]string),
	}
}

// Publish traduce un evento del stream a un SessionEvent durable y lo persiste.
// Bufferiza los deltas: en cada *.Ended emite el bloque completo concatenado, y
// en Text.Ended ademas materializa el Message del asistente para la proyeccion.
// Devuelve el error del store si AppendEvent falla.
func (p *Publisher) Publish(ctx context.Context, ev llm.Event) error {
	switch ev.Kind {
	case llm.StepStarted:
		return p.emit(ctx, session.SessionEvent{Kind: session.KindStepStarted})
	case llm.StepEnded:
		return p.emit(ctx, session.SessionEvent{Kind: session.KindStepEnded, Usage: toUsage(ev.Usage)})

	case llm.TextStarted:
		p.text.Reset()
		return p.emit(ctx, session.SessionEvent{Kind: session.KindTextStarted})
	case llm.TextDelta:
		p.text.WriteString(ev.Text)
		return p.emit(ctx, session.SessionEvent{Kind: session.KindTextDelta, Text: ev.Text})
	case llm.TextEnded:
		full := p.text.String()
		p.text.Reset()
		return p.emit(ctx, session.SessionEvent{
			Kind:    session.KindTextEnded,
			Text:    full,
			Message: &session.Message{ID: p.asstMsgID, Role: session.RoleAssistant, Text: full},
		})

	case llm.ReasoningStarted:
		p.reason.Reset()
		return p.emit(ctx, session.SessionEvent{Kind: session.KindReasoningStarted})
	case llm.ReasoningDelta:
		p.reason.WriteString(ev.Text)
		return p.emit(ctx, session.SessionEvent{Kind: session.KindReasoningDelta, Text: ev.Text})
	case llm.ReasoningEnded:
		full := p.reason.String()
		p.reason.Reset()
		return p.emit(ctx, session.SessionEvent{Kind: session.KindReasoningEnded, Text: full})

	case llm.ToolCall:
		p.tools[ev.CallID] = ev.ToolName
		p.input[ev.CallID] = append([]byte(nil), ev.Input...)
		return p.emit(ctx, session.SessionEvent{
			Kind: session.KindToolCalled, CallID: ev.CallID, ToolName: ev.ToolName, Input: ev.Input,
		})
	case llm.ToolInputStarted:
		p.input[ev.CallID] = nil
		return p.emit(ctx, session.SessionEvent{Kind: session.KindToolInputStarted, CallID: ev.CallID})
	case llm.ToolInputDelta:
		p.input[ev.CallID] = append(p.input[ev.CallID], ev.Input...)
		return p.emit(ctx, session.SessionEvent{Kind: session.KindToolInputDelta, CallID: ev.CallID, Input: ev.Input})
	case llm.ToolInputEnded:
		return p.emit(ctx, session.SessionEvent{
			Kind: session.KindToolInputEnded, CallID: ev.CallID, Input: p.input[ev.CallID],
		})
	}
	return nil // StepFailed (M8) y kinds sin semantica de sesion en M3 se ignoran
}

// emit fija el SessionID del turno y persiste el evento. Aisla el unico punto que
// toca el store.
func (p *Publisher) emit(ctx context.Context, ev session.SessionEvent) error {
	_, err := p.store.AppendEvent(ctx, p.sessionID, ev)
	return err
}

// toUsage copia los tokens de llm.Usage al espejo de session, cruzando la
// frontera sin acoplar session a llm. nil -> nil (un Step sin tokens).
func toUsage(u *llm.Usage) *session.Usage {
	if u == nil {
		return nil
	}
	return &session.Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		ReasoningTokens:  u.ReasoningTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
}
```

Notes:

- **The buffer lives in the publisher, not in the fold.** Text and reasoning are
 accumulated in `strings.Builder`; the tools input in `input[callID]`. In each
 `*.Ended` the entire content is dumped. This fulfills "buffers deltas and
 emits a final event with the complete content" of the roadmap.
- **`Message` only in `Text.Ended`.** It is the only event that materializes the
 projection of the assistant, with the `assistantMessageID` of the turn. The
 reasoning buffers just like the text but **does not** materialize `Message`:
 is not conversational content of the simplified projection (and the real
 adapter omits the thinking content by default, see
 `../architecture/llm-claude.md`). If M5/M7 need thinking in history,
 is added there with its test.
- **Map of tool calls by `callID`.** `tools[callID] = toolName` is filled in
 `ToolCall`. M3 does not consume it yet; is the state that `ToolSuccess`/`ToolFailed`
 (M5) will read to name the tool when entering its result.
- **`Input` is cloned in `ToolCall`** (`append([]byte(nil), ev.Input...)`) so as not to
 alias the supplier event slice while the deltas are accumulated.
- **No setup error or concurrency on M3.** `Publish` only propagates error
 from `AppendEvent`. The publisher lock and the concurrent path arrive at M5.

## 6. Publisher semantics

The contract that M3 sets for the frontend (M9) and for the projection (M1):

- **Translation 1:1.** Each `llm.Event` with session semantics produces
 exactly one `SessionEvent` whose `Kind` is the name of the contract
 (`Step.Started`, `Text.Delta`, `Tool.Called`, ...). The order of the persisted events
 respects the order of the stream.
- **Deltas + complete block.** A block `Text.Started / Delta / Delta / Ended`
 persists four events: three mark the streaming (with their fragment in `Text`,
 `Message == nil`) and `Text.Ended` carries the complete text concatenated in `Text`
 and materializes the `Message`. Reasoning and tool input follow the same
 pattern (input via `Tool.Input.*`, complete in `Tool.Input.Ended`).
- **Coalesced projection.** After a turn of text, `Store.Messages(sessionID,
  0)` returns **a** wizard message with the full text and the
 `assistantMessageID` of the turn. The deltas do not appear in the
 projection (they have `Message == nil`). The fold of M1 does not change.
- **Tokens in `Step.Ended`.** `llm.StepEnded` with `*llm.Usage` produces a
 `Step.Ended` whose `Usage` is the mirror `session.Usage` with
 input/output/reasoning/cache. A Step without tokens leaves `Usage == nil`.
- **Map of tool calls.** The publisher remembers `callID -> toolName` for each
 `Tool.Called` of the turn; is the state that M5 uses when publishing the result.
- **Kinds not mapped in M3.** `llm.StepFailed` (and any kind without session semantics
 yet) does not persist the event in M3: its translation to `Step.Failed`
 belongs to the fault path (M8).

## 7. TDD Plan

### Safety net

- Green base state before touching anything. M3 adds `publish_test.go` and enriches
 `SessionEvent`; First, the existing one is run (M0 + `session` of M1 + `llm` of
 M2).
- Command: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Expected result: they pass clean. If something fails, it is reported as pre-existing and
 is not followed blindly. In particular, after enriching `SessionEvent`,
 runs `go test ./internal/session` again to confirm that M1 is still green (the
 change is additive).

### Understand

- Read entry M3 of the roadmap; "Published events", "Event streaming and
 tools execution" and "Main types" of `../architecture/agent-loop.md`; the
 `../architecture/llm-claude.md` mapping table; and the coalescing note of the
 spec M1.
- Expected behavior: translate each `llm.Event` to its `SessionEvent` with the
 contract name; buffer deltas and emit the entire block; materialize
 the wizard's coalesced `Message`; keep map `callID -> toolName`.

### NETWORK

- Write the test that fails first:
 `TestPublisher_TextBlockEmitsDeltasAndFinalConcatenatedText`. Reference to
 `NewPublisher`, `Publish`, `session.KindText*` and the new fields of
 `SessionEvent`, which do not yet exist -> does not compile -> fails (Honest RED in Go is
 compilation failure of the test package).
- The test feeds the `Text.Started, Text.Delta("Hola, "),
  Text.Delta("mundo"), Text.Ended` script through a `recordingAppender` (spy which
 captures each `SessionEvent`), and asserts:
 - the captured `Kind` are `[Text.Started, Text.Delta, Text.Delta,
    Text.Ended]`;
- deltas carry their fragment in `Text` and `Message == nil`;
 - `Text.Ended` carry `Text == "Hola, mundo"` and `Message == &{ID:"a1",
    Role:assistant, Text:"Hola, mundo"}`.
- Command:
 `go test -run TestPublisher_TextBlockEmitsDeltasAndFinalConcatenatedText ./internal/session/runner`
 -> expected failure.

### GREEN

- Write the minimum: `event.go` (`EventKind` + constants + `Usage`), the
 enrichment of `SessionEvent` in `session.go`, and `publish.go` (`Publisher`,
 `NewPublisher`, `Publish`, `eventAppender`, `emit`, `toUsage`).
- Run only the red test to green.
- Command:
 `go test -run TestPublisher_TextBlockEmitsDeltasAndFinalConcatenatedText ./internal/session/runner`.

### TRIANGULATE

Add cases to avoid false green:

- `TestPublisher_ReasoningBuffersLikeText`: `Reasoning.Started/Delta/Delta/Ended`
 -> kinds `Reasoning.*`; `Reasoning.Ended.Text` is the complete concatenation and
 `Message == nil` (the reasoning is not projected as a message).
- `TestPublisher_StepEndedCarriesUsageTokens`: `StepStarted` + `StepEnded` with
 `*llm.Usage{In:10,Out:20,Reasoning:5,CacheRead:3,CacheWrite:1}` -> the event
 `Step.Ended` carries a `*session.Usage` with those five fields; a `StepEnded`
 without usage leaves `Usage == nil`.
- `TestPublisher_ToolCallAndInputDeltasCoalesce`: `ToolCall{CallID:"c1",
  ToolName:"read", Input: nil}` + `ToolInputDelta(c1, "{\"path\":")` +
 `ToolInputDelta(c1, "\"/x\"}")` + `ToolInputEnded(c1)` -> kinds `Tool.Called,
  Tool.Input.Delta, Tool.Input.Delta, Tool.Input.Ended`; `Tool.Called` carries
 `CallID`/`ToolName`; `Tool.Input.Ended` carries the full input
 `{"path":"/x"}` concatenated.
- `TestPublisher_ProjectsCoalescedAssistantMessage` (binding M3 <-> M1): use the actual
 `MemoryStore`; post `StepStarted`, a block of text and `StepEnded`;
 then `store.Messages(ctx, "s1", 0)` returns **exactly one** message
 `{ID:"a1", Role:assistant, Text: completo}`. Prove that the deltas do not
 contaminate the projection and that the publisher's coalescing lands on M1's
 fold without touching it.
- `TestPublisher_AppendErrorPropagates` (error path): a `recordingAppender`
 configured to return error -> `Publish` returns that error.
- Commands:
 - `go test -run TestPublisher ./internal/session/runner`
 - `go test -race -run TestPublisher ./internal/session/runner` (hygiene; the
    concurrencia real del publisher es de M5)

### REFACTOR

- Cleanup without changing behavior: factor the buffered
 block pattern of text and reasoning (same `Started/Delta/Ended` on a
 `strings.Builder`) in a helper if it reduces duplication; a test
 `record(p, evs...) []session.SessionEvent` helper to drain the script; update the
 package comment of `internal/session/runner/doc.go` (the publisher already
 landed on M3) and, if applicable, that of `internal/session/doc.go` (the
 streaming taxonomy was added on `SessionEvent`).
- Verify that the suite is still green after formatting.
- Command: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Acceptance criteria (Done when)

1. There is `EventKind` (string) with contract constants
 (`KindStep* / KindText* / KindReasoning* / KindToolInput* / KindToolCalled /
   KindToolSuccess / KindToolFailed`) and type `Usage` in
 `internal/session/event.go`.
2. `SessionEvent` was additively enriched with `Kind`, `Text`,
 `CallID`, `ToolName`, `Input`, `Usage`, and M1 tests remain green.
3. There is `Publisher` (+ `NewPublisher`) with `Publish(ctx, llm.Event) error` in
 `internal/session/runner/publish.go`, depending on `eventAppender`.
4. A `Text.Started/Delta/Delta/Ended` block persists the four events with its
 `Kind`, and `Text.Ended` carries the full concatenated text and materializes the wizard's
 `Message` with the shift's `assistantMessageID`.
5. The reasoning buffers just like the text (`Reasoning.*`, complete in
 `Reasoning.Ended`) without materializing `Message`.
6. `StepEnded` with tokens produces `Step.Ended` with `*session.Usage`
 (input/output/reasoning/cache); without tokens leaves `Usage == nil`.
7. `ToolCall` produces `Tool.Called` (with `CallID`/`ToolName`) and registers
 `callID -> toolName`; The `Tool.Input.Delta` coalesce and `Tool.Input.Ended`
 carries the full JSON input.
8. `Store.Messages(sessionID, 0)` after a text turn returns a single
 message from the coalesced wizard; The deltas do not appear in the projection and
 `memstore.go` was not modified.
9. `go test ./...` (and `-race` where applicable) passes; `go vet ./...` clean;
 `gofmt -l .` empty.
10. There were no changes to `app.go`, `main.go`, Wails or the frontend; nor in
    `internal/llm`, `internal/tool` o `internal/event`. En `internal/session`
    solo se agrego `event.go` y se enriquecio `SessionEvent` (sin tocar
    `memstore.go` ni `store.go`).

## 9. Commands

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Confirmar que M1 sigue verde tras enriquecer SessionEvent
go test ./internal/session

# Ciclo (test especifico primero)
go test -run TestPublisher_TextBlockEmitsDeltasAndFinalConcatenatedText ./internal/session/runner
go test -run TestPublisher ./internal/session/runner

# Higiene de concurrencia (la real es de M5)
go test -race -run TestPublisher ./internal/session/runner

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
```

## 10. Table of expected evidence

When closing M3, the answer/PR should include this table with actual results:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0+M1+M2 green before editing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Published event contract read | roadmap M3, `../architecture/agent-loop.md`, `../architecture/llm-claude.md`, spec M1 | identified behavior |
| NETWORK | Delta test + full text written first | `publish_test.go` + `go test -run TestPublisher_TextBlockEmitsDeltasAndFinalConcatenatedText ./internal/session/runner` | expected failure (does not compile) |
| GREEN | `event.go` + rich `SessionEvent` + minimum `publish.go` | `internal/session/event.go`, `internal/session/session.go`, `internal/session/runner/publish.go` | specific test passes |
| TRIANGULATE | Reasoning, Usage, Tool.Input coalesced, M1 projection, error | `go test -run TestPublisher ./internal/session/runner`, `go test -race ...` | cases pass, `-race` clean |
| REFACTOR | block/test helper, `doc.go` updated | `gofmt -w internal`, `go vet ./...`, `go test ./...` | green suite, M1 intact |

## 11. Risks and decisions

- **Taxonomy in `session`, not in `runner`.** The form of the durable
 event (including its `Kind`) is from the domain that is persisted and consumed by the frontend
 (`session.SessionEvent`); the publisher (in `runner`) is just the producer. For
 that `EventKind`/`Usage` live in `internal/session`. The runner references them
 as `session.KindTextDelta`.
- **`EventKind` as a string, not int.** Unlike `llm.EventKind` (a `int`
 internal to the stream), the `session.EventKind` is serialized to Wails (M9) and is seen
 in the store; a string named (`"Text.Delta"`) is stable, readable and maps
 1:1 on the frontend without a translation table. It is the same criterion with which the
 design fixed the contract names.
- **Coalescing in the publisher, not in the fold.** M1 advance "coalesce several
 deltas in a single message." It is done by buffering in the publisher and writing
 a complete `Message` in `Text.Ended`, instead of enriching the fold to add
 fragments by ID. Result: `memstore.go` is not touched and the projection of M1
 remains identical. It is simpler and leaves a single place responsible for coalescing.
- **`session.Usage` mirror of `llm.Usage`.** In order not to couple `session` to `llm`
 (the dependency address is `runner -> {session, llm}`), the
 token type is duplicated and the publisher copies it field by field. The duplication is five
 ints; the decoupling is worth more than the DRY here.
- **`eventAppender` instead of the complete `Store`.** The publisher only needs
 `AppendEvent`. Relying on a method interface makes it testable with minimal
 spy and honors "accepts small interfaces". The real `session.Store` (and the
 `MemoryStore`) comply with it without changes.
- **Reasoning does not materialize `Message`.** It buffers and emits `Reasoning.Ended`
 with the full text (symmetrical to the streaming text), but it does not enter the
 projection: it is not simplified conversational content and the real adapter
 omits thinking by default. If an M5/M7 test needs thinking in the
 history, it is added there.
- **No lock in M3.** In M3 `Publish` is called serially from the (future) consumer loop
. Concurrent access appears in M5, when the goroutines of
 `settle` publish `Tool.Success`/`Tool.Failed` while the loop is still consuming
; There the mutex is added with its test `-race`. Going forward would be
 specular.
- **`ToolSuccess`/`ToolFailed` outside of M3.** These are events produced by the
 **runner** when setting up tools, not the provider's stream (that's why `llm.EventKind`
 does not include them, see spec M2). M3 leaves the state they need ready (the map
 `callID -> toolName`); The methods are written with their test in M5/M8.
- **`StepFailed` deferred.** The stream can carry `llm.StepFailed`, but its
 translation to `Step.Failed` with the `Error` of the turn is from the fault path
 (M8). On M3 `Publish` ignores it; The `Error` field is not invented in
 `SessionEvent` until M8 exercises it with a test.
- **`scaffold_test.go` of runner is preserved.** Unlike M1/M2 (which
 replaced their scaffold), that of `runner` anchors `golang.org/x/sync/errgroup`
 in `go.mod` for M5. M3 does not use errgroup, so the scaffold is kept (which
 maintains the anchor) and `publish_test.go` is **added. The scaffold is removed in
 M5, when `consume` imports errgroup in production code.

## 12. Sources

- Roadmap: `../plans/agent-loop-roadmap.md` (milestone M3)
- Architecture: `../architecture/agent-loop.md` (sections "Published events",
 "Event streaming and tool execution", "Main types")
- LLM integration: `../architecture/llm-claude.md` (SDK event mapping table
 to `llm.Event`; thinking omitted by default)
- Way of working: `AGENTS.md`
- Previous specs: `atenea-m1-tipos-store-spec.md`,
 `atenea-m2-provider-fake-spec.md`
