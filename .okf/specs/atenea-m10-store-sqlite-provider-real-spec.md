---
updated_at: 2026-07-09
summary: Specification for m10 store sqlite provider real spec.
---

# Spec M10 — Store SQLite + provider real

`../plans/agent-loop-roadmap.md` Milestone **M10** Executable Spec. Defines the
final state, scope, TDD plan and acceptance criteria to change
the two remaining fakes (`MemoryStore` and `FakeProvider`) for real
implementations **behind the same interfaces**, **without touching the loop logic**
(`Run`, `runTurn`, `consume`, `Publisher`). The central rule, as in M9: the
real enters by injection into `app.go`; loop packages do not learn from SQLite
or Anthropic.

M10 adds two real parts and one wiring:

- **`session.SQLiteStore`**: implements `session.Store` on a single SQLite DB in
 the app's data directory. The event log is the durable source of truth
; `Messages`, `PendingToolCalls` and `Epoch` are reprojected from the log,
 the same as in `MemoryStore`. The difference is that it **survives reboots**: after
 closing and reopening the app, the `Seq` continues and the history is rebuilt from
 disk.
- **`llm.AnthropicProvider`**: implements `llm.Provider` on the official
 `anthropic-sdk-go` SDK (see `../architecture/llm-claude.md`). One call to
 `Messages.NewStreaming` per turn; maps the SDK stream to `llm.Event` and closes
 the channel when finished.
- **`app.go`**: `NewApp` assembles the `SQLiteStore` (decorated by the `EmittingStore` from
 M9) and the `AnthropicProvider`; `newApp` continues injecting store and provider so that
 tests use `MemoryStore` and `FakeProvider`.

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

M1..M9 left the loop complete and green, first against fakes and then wired to
Wails, without a single dependency on SQLite or Anthropic in the loop logic:

- **M1** left the durable domain (`Seq`, `Message`, `SessionEvent`, `Store`) and the
 `MemoryStore`: the event log is the only source of truth and the messages are
 a projection (`Messages(sinceSeq)`). `AppendEvent` assigns `SessionID`/`Seq` and
 ignores those brought by the event.
- **M2** left the border with the model (`llm.Provider`, `Request`, `Event`,
 `Usage`, `ToolDef`) and the replayable `FakeProvider`. `Stream` produces **a** turn
 and **closes** the channel; cancel `ctx` cuts it without hanging receivers.
- **M3** left the `Publisher`: translates the stream to durable `SessionEvent` and coalesces
 the deltas (`Text.*`, `Reasoning.*`, `Tool.Input.*`).
- **M4** left the `tool.Registry` (`Materialize -> {Definitions, Settle}`).
- **M5/M6** left the shift and the external loop (`runTurn`, `consume`, `Run`,
 `Inbox`, `MaxSteps`).
- **M7** left the control signals and the `ContextEpoch`. `MemoryStore.Epoch`
 returns the stable zero epoch, so the happy path never rebuilds; The
 comment already anticipated "the real driver... arrives with the real store (M10)."
- **M8** left the fault handling (`FailUnresolvedTools`, `Step.Failed`,
 `failInterruptedTools` when starting `Run`: resumption after crash).
- **M9** wired the loop to Wails: `event.Bus`, `event.EmittingStore` (decorate
 any `session.Store`) and `app.go` (`SendPrompt`/`Stop`). The comment from
 `newApp` already says "M10 changes provider/store to the real ones without touching this", and
 `newIDGen` "a stable ID between reboots comes with M10's persistent store".

What's missing is to make it **real and persistent**. The architecture is already described by
(`../architecture/agent-loop.md`, "Durable persistence": a single SQLite DB in the
app data directory; `Run` always rebuilds the request from the
store) and the model adapter already has its complete layout in
`../architecture/llm-claude.md` (which explicitly maps M10 to the real adapter).

## 2. Objective

An end-to-end prompt works against the **real** provider (Claude/Anthropic) and its
history remains in a SQLite DB that **survives restarts** of the app. The two
implementations enter behind `session.Store` and `llm.Provider` **without changing
a line** of `Run`, `runTurn`, `consume`, `Publisher` or the rest of the loop: the
suite M1..M8 remains green, now also running against `SQLiteStore`. The network
(the live call to Anthropic) and the actual SQLite are the **only boundaries not
covered by deterministic `go test`**; the pure mapping of events and the contract
of the store if tested.

## 3. Scope

### Inside

- `internal/session/sqlitestore.go`: `SQLiteStore` + `NewSQLiteStore(dsn)` that
 satisfies `session.Store` (`AppendEvent`, `LoadSession`, `Messages`, `Epoch`,
 `PendingToolCalls`), with scheme, idempotent migration and full round-trip of
 `SessionEvent`.
- `internal/session/store_contract_test.go`: a **shared contract suite**
 (`testStoreContract(t, newStore)`) that runs the same behavior cases
 against any `session.Store`. Runs against `MemoryStore` and
 `SQLiteStore`; guarantees that the real store complies with the contract of M1..M8.
- `internal/session/sqlitestore_test.go`: what is specific to SQLite (persistence
 between two opens of the same file; `Seq` continues after reopening).
- `internal/llm/anthropic.go`: `AnthropicProvider` + `NewAnthropicProvider(opts)`
 which complies with `llm.Provider`, plus the pure function `mapEvent` (SDK -> `llm.Event`) and
 the converters `toMessages`/`toTools`.
- `internal/llm/anthropic_test.go`: tests of the pure function `mapEvent` and the
 converters (without network).
- `app.go`: `NewApp` weapon `SQLiteStore` (decorated by `EmittingStore`) and
 `AnthropicProvider`; resolution of the path of the DB and the API key; fallback to
 `FakeProvider`/`demoProvider` if there is no API key (so that `wails dev` continues without
 network). `newApp` does not change its signature (it continues injecting store/provider for tests).
- `go.mod`: `go get modernc.org/sqlite` (pure SQLite Go driver, without cgo) and
 `go get github.com/anthropics/anthropic-sdk-go`.

### Out (do not do in M10)

- **Real `ContextEpoch` driver** (move `Agent`/`Model`/`Revision`/`BaselineSeq`
 upon config changes, file/instruction reconciliation): `SQLiteStore.
  Epoch` returns the **same stable epoch** as `MemoryStore`, so the happy path
 does not rebuild and M1..M8 they remain green. The schematic leaves room (table `sessions`)
 for when the driver is needed; M10 only provides **durability**, not new
 epoch semantics. See Risks.
- **Real Compactor** (`Compactor.NeedsCompaction`/`Compact` which measures tokens against
 the model limit and summarizes): The `Runner` runs with `compactor == nil` (never
 compact), same as M6/M9. `Compactor`'s comment says "the real ... arrives
 in M10", but tying it to Anthropic's server-side compaction is a milestone of its own;
 M10 leaves the overflow path as is. See Risks.
- **Structured tool use history** (`assistant message` with blocks
 `tool_use` + `user message` with `tool_result` for `callID`, see
 `../architecture/llm-claude.md`): today `llm.Message` carries only `Role`/`Text` and The
 `Messages` projection only materializes text. The multi-shift with real tools
 asks for the rich projection (`History.EntriesForRunner`), which is still deferred. M10
 faithfully maps what the loop **today** produces (text history + `ToolDef`s);
 the `tool_result` round-trip arrives with the rich projection.
- **`Inbox` persistent**: the `MemoryInbox` is still in memory. The `Inbox`
 interface already anticipates "M10 can add a persistent version", but the M10 roadmap asks
 **Store** SQLite; persist inbox is added when you need to resume queued prompts after a crash, behind the same interface. thinking **adaptive** (the only thing supported in Opus 4.8) and `max_tokens` by
 default; The rest is added when a test requests it.
- **Changes in the loop logic**: hard invariant, the same as M9. If any test of
 M1..M8 changes, M10 was done wrong.

## 4. The SQLite store (`internal/session/sqlitestore.go`)

`SQLiteStore` saves the event log in a table and reprojects everything from there,
just like `MemoryStore` reprojects from its slice. The only observable difference
is durability: the state lives in a file, not a map.

### Outline

A single event table, with a **complete** round-trip of `SessionEvent` so that
the three projections (`Messages`, `PendingToolCalls`, and the `Usage` of
`Step.Ended`) are reconstructed identically after reopening:

```sql
CREATE TABLE IF NOT EXISTS events (
    session_id  TEXT    NOT NULL,
    seq         INTEGER NOT NULL,
    kind        TEXT    NOT NULL,            -- EventKind ("" para eventos sin taxonomia)
    has_message INTEGER NOT NULL,            -- 1 si Message != nil (distingue de Message vacio)
    msg_id      TEXT,
    role        TEXT,
    text        TEXT,
    call_id     TEXT,
    tool_name   TEXT,
    input       BLOB,                        -- json.RawMessage cruda (Tool.Called/Input.*)
    usage       BLOB,                        -- *Usage en JSON (NULL salvo Step.Ended)
    error       TEXT,
    PRIMARY KEY (session_id, seq)
);
```

The `PRIMARY KEY (session_id, seq)` gives the total durable order per session and prohibits
gaps or duplicates at the engine level. There is no message table: `Messages` is
derived from the log, true to the M1 contract ("Messages is derived from the log, not
stored separately").

### Implementation (sketch)

```go
package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"

	_ "modernc.org/sqlite" // driver puro Go, sin cgo
)

// SQLiteStore es la implementacion durable del Store (M10): guarda el log de
// eventos en SQLite y reproyecta mensajes, tool calls pendientes y epoch desde el
// log, igual que MemoryStore pero sobreviviendo a reinicios. Cumple la MISMA
// interface; el runner no aprende de SQLite.
type SQLiteStore struct {
	db *sql.DB
	mu sync.Mutex // serializa AppendEvent: asigna Seq sin huecos (SQLite es single-writer)
}

// NewSQLiteStore abre (o crea) la DB en dsn y aplica el esquema de forma
// idempotente. dsn es un path de archivo (produccion) o ":memory:" (tests). Si el
// archivo ya existe, el log previo queda disponible: la sesion es reanudable.
func NewSQLiteStore(dsn string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

// var _ Store = (*SQLiteStore)(nil) asegura en compilacion que cumple la interface
// y es inyectable en el Runner sin cambios.
var _ Store = (*SQLiteStore)(nil)

func (s *SQLiteStore) AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var max sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT MAX(seq) FROM events WHERE session_id = ?`, sessionID).Scan(&max); err != nil {
		return 0, err
	}
	seq := Seq(max.Int64 + 1) // 1 si la sesion es nueva (NULL -> 0)

	// round-trip completo: Message (si lo hay), payload de streaming y Usage.
	// has_message distingue Message == nil de un Message vacio; input/usage van
	// como JSON crudo. La columna kind preserva la taxonomia para PendingToolCalls.
	if _, err := s.db.ExecContext(ctx, insertEvent, /* ...campos de ev... */); err != nil {
		return 0, err
	}
	return seq, nil
}
```

`LoadSession`, `Messages`, `Epoch` and `PendingToolCalls` are **reads** that
rebuild exactly what `MemoryStore` rebuilds, but with SQL:

- `LoadSession`: `SELECT 1 FROM events WHERE session_id = ? LIMIT 1`; if no
 row -> `ErrSessionNotFound`; if there is -> `Session{ID: sessionID}`.
- `Messages(sinceSeq)`: `SELECT ... WHERE session_id = ? AND has_message = 1 AND
  seq > ? ORDER BY seq`; each row returns to a `Message` with `Seq = seq`. If the
 session does not exist (there is no event), `ErrSessionNotFound` (check for
 existence first, like `MemoryStore`).
- `Epoch`: existence first (`ErrSessionNotFound` if there are no events); then
 `ContextEpoch{}` (stable zero epoch, identical to `MemoryStore`; see Out/Risks).
- `PendingToolCalls`: reads `kind IN (Tool.Called, Tool.Success, Tool.Failed)` in
 order of `seq` and applies the same fold as `MemoryStore` (Called opens, Success/
 Failed closes), preserving the order call.

Decisions:

- **Mutex on `AppendEvent`, same as `MemoryStore`.** SQLite is single-writer; the
 lock makes **atomic** the read-`MAX(seq)`+insert sequence and leaves the contract
 of `Seq` byte-identical to that of the store in memory (no gaps or duplicates under
 concurrent appends, clean `-race`). It is the simplest thing that fulfills the contract; no
 need an explicit transaction or retries due to conflict.
- **Full round-trip of the event, not just the `Message`.** Persist `Kind`,
 `CallID`, `ToolName`, `Input`, `Usage`, `Error` is what allows
 `PendingToolCalls` and `Step.Ended`'s `Usage` survive the reboot. Saving
 only the text would break `failInterruptedTools` after a crash (M8) and restart.
- **`has_message` explicit.** An event may not contribute to the projection
 (`Message == nil`) or materialize a `Message` with empty text; the column
 distinguishes both unambiguously from NULL.
- **`ctx` honored.** Unlike `MemoryStore` (which ignores it), here the
 queries use `...Context`: actual cancellation/timeout. Close the contract that the
 firm promised from M1.

## 5. The Store Shared Contract (`store_contract_test.go`)

The "Done when" of the roadmap is "M1..M8 tests remain green with store
real". The clean way to guarantee this is **a single set of behavioral tests
that runs against any `Store`**, instead of duplicating those of `MemoryStore`.

```go
// testStoreContract corre el contrato durable del Store contra cualquier
// implementacion. newStore devuelve un store vacio y listo (un MemoryStore nuevo,
// o un SQLiteStore sobre ":memory:" o un archivo temporal). Cubre lo que M1..M8
// asumen: Seq monotonico, proyeccion de Messages con sinceSeq, ErrSessionNotFound,
// PendingToolCalls y Epoch estable, mas concurrencia con -race.
func testStoreContract(t *testing.T, newStore func(t *testing.T) session.Store) {
	t.Run("AppendEventAssignsMonotonicSeq", func(t *testing.T) { /* ... */ })
	t.Run("MessagesReturnInSeqOrder", func(t *testing.T) { /* ... */ })
	t.Run("MessagesSinceSeqFiltersOlder", func(t *testing.T) { /* ... */ })
	t.Run("UnknownSessionReturnsNotFound", func(t *testing.T) { /* ... */ })
	t.Run("PendingToolCallsFoldsCalledVsResolved", func(t *testing.T) { /* ... */ })
	t.Run("EpochStableWithinTurn", func(t *testing.T) { /* ... */ })
	t.Run("ConcurrentAppendsAssignUniqueSeqs", func(t *testing.T) { /* ... */ }) // -race
}

func TestMemoryStore_Contract(t *testing.T) {
	testStoreContract(t, func(t *testing.T) session.Store { return session.NewMemoryStore() })
}

func TestSQLiteStore_Contract(t *testing.T) {
	testStoreContract(t, func(t *testing.T) session.Store {
		s, err := session.NewSQLiteStore(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}
```

The `memstore_test.go` tests that already cover these cases are **migrated** to the shared
contract (REFACTOR without losing coverage): thus the same scenario verifies the two
implementations and any divergence from the real store is skipped in CI.


## 6. The actual provider adapter (`internal/llm/anthropic.go`)

`AnthropicProvider` implements `llm.Provider` exactly as described by
`../architecture/llm-claude.md`: one call to `Messages.NewStreaming` per turn,
accumulating with `msg.Accumulate`, mapping each event from the SDK to `llm.Event`, and
closing the channel upon completion.

```go
package llm

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"
)

// AnthropicProvider es el Provider real (M10): traduce el llm.Request neutral a
// MessageNewParams, consume el stream del SDK y emite llm.Event por un channel,
// cerrandolo al terminar. Cumple la MISMA interface que FakeProvider; el runner no
// aprende del SDK.
type AnthropicProvider struct {
	client    anthropic.Client
	model     string // default cuando req.Model == "" (claude-opus-4-8)
	maxTokens int64  // default 64000 (req aun no lo lleva; ver Fuera)
}

var _ Provider = (*AnthropicProvider)(nil)

func (p *AnthropicProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	out := make(chan Event)
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.resolveModel(req.Model)),
		MaxTokens: p.maxTokens,
		Messages:  toMessages(req.Messages), // historial de texto proyectado
		Tools:     toTools(req.Tools),       // schemas materializados (orden determinista)
		Thinking: anthropic.ThinkingConfigParamUnion{
			OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}, // unico soportado en Opus 4.8
		},
	}
	stream := p.client.Messages.NewStreaming(ctx, params)
	go func() {
		defer close(out)
		msg := anthropic.Message{}
		for stream.Next() {
			ev := stream.Current()
			msg.Accumulate(ev)
			if mapped, ok := mapEvent(ev); ok {
				select {
				case <-ctx.Done():
					return
				case out <- mapped:
				}
			}
		}
		if err := stream.Err(); err != nil {
			out <- Event{Kind: StepFailed, Text: err.Error()} // -> Step.Failed (M8)
			return
		}
		out <- Event{Kind: StepEnded, Usage: toUsage(msg.Usage)} // -> Step.Ended con tokens
	}()
	return out, nil
}
```

### Mapping (the pure, testable part)

`mapEvent` is a **pure** `(anthropic.MessageStreamEventUnion) -> (Event,
bool)` function (the bool discards events without an equivalent). It is the heart of the adapter and
**yes** it is tested without a network, with synthetic SDK events, following the table of
`../architecture/llm-claude.md`:

| Anthropic block/event | `llm.Event` |
| --- | --- |
| content_block_start (text) | `{Kind: TextStarted}` |
| content_block_delta (TextDelta) | `{Kind: TextDelta, Text: d.Text}` |
| content_block_stop (text) | `{Kind: TextEnded}` |
| content_block_start (thinking) | `{Kind: ReasoningStarted}` |
| content_block_delta (ThinkingDelta) | `{Kind: ReasoningDelta, Text: d.Thinking}` |
| content_block_start (tool_use) | `{Kind: ToolCall, CallID, ToolName, Input}` |
| content_block_delta (InputJSONDelta) | `{Kind: ToolInputDelta, CallID, Input}` |
| content_block_stop (tool_use) | `{Kind: ToolInputEnded, CallID}` |
| message_stop | (issued by the closing of the stream as `StepEnded`) |

Rules true to Claude's doc:

- The input of `tool_use` is taken from the **raw JSON** of the SDK (`...JSON.Input.Raw()`),
 never by string match: Opus 4.8 can escape the distinct JSON between turns.
- The exact names of the SDK types (`ThinkingDelta`, `InputJSONDelta`,
 binding of `effort`/thinking) are **verified against the SDK** when implementing, they are not
 invented (the doc itself says so). The test contract is the `llm.Event` of
 output, not the internal name of the SDK.
- `toTools` orders the tools **deterministically (by name)**: invariant of
 prompt caching of the doc (do not reorder the prefix).

### The untested frontier

The live call `Messages.NewStreaming` (network, API key, SDK retries) is the
**border not covered by `go test`**, same as `runtime.EventsEmit` and the Vue in
M9. `Stream` builds `params`, delegates to the SDK and translates; The only non-deterministic thing is
the network. It is verified by hand with a real API key (section 8). Optional as
triangulation: a `httptest.Server` that plays a recorded SSE, injected with
`option.WithBaseURL`, to exercise `Stream` end-to-end without Anthropic; it is not
closure requirement (pure mapping already covers the logic).

## 7. The wiring of the app (`app.go`)

`newApp(provider, emit)` **does not change signature**: it continues injecting a store
(via its internal use) and a provider so that the tests use `MemoryStore` +
`FakeProvider`. What changes is `NewApp` (production), which now assembles the real
pieces:

```go
// NewApp arma la app de produccion con el store SQLite durable y el provider real.
// Si no hay ANTHROPIC_API_KEY, cae al demoProvider (fake) para que `wails dev`
// muestre streaming sin red, como en M9.
func NewApp() *App {
	var a *App
	emit := func(name string, data ...interface{}) {
		runtime.EventsEmit(a.ctx, name, data...)
	}
	store, err := session.NewSQLiteStore(dbPath()) // directorio de datos de la app
	if err != nil {
		// log + fallback a MemoryStore: la app abre aunque el disco falle
	}
	a = newAppWithStore(store, provider(), emit)
	return a
}
```

So that `NewApp` can inject an **already built** store (the real SQLite) without
breaking the symmetry of M9, the construction is factored around the store: the store
 is still wrapped by the `EmittingStore` (Store bridge -> M9 UI) **before** passing it to the `Runner`, so the UI sees each durable event the same as with the
`MemoryStore`. The registry, the inbox (`MemoryInbox` continues), the permissions and
`newIDGen` do not change.

Resource resolution:

- **DB Path**: user data directory (`os.UserConfigDir()` +
 `atenea/atenea.db`), with override by `ATENEA_DB` (useful in dev). The
 directory is created if it is missing. It is "a single SQLite DB in the app's data directory",
 as requested by the architecture.
- **API key**: `ANTHROPIC_API_KEY` of the environment (the SDK reads it by default). Never on
 disk or at the prompt (invariant of `../architecture/llm-claude.md`). Without key ->
 `demoProvider` (fake), so as not to break `wails dev`.
- **`newIDGen`**: with the persistent store, the `assistantMessageID` should be
 stable between reboots; M10 can pass UUID (`github.com/google/uuid`, already
 indirect in `go.mod`) or leave the atomic counter if there is no observable collision.
 Minor decision, it does not block closure.

`main.go` does not change: it already binds `App` with `SendPrompt`/`Stop`.

## 8. TDD Plan

### Safety net

Before touching anything, confirm that M1..M9 are green:

```bash
go test ./...        # toda la suite verde
go vet ./...         # limpio
gofmt -l .           # vacio
```

### Understand

Read "Durable persistence" of `../architecture/agent-loop.md` (single DB, `Run`
rebuilds from the store), full `../architecture/llm-claude.md` (one turn = one
streaming call, mapping table, which NOT to use), the contract of `session.Store`
(the five operations and `ErrSessionNotFound`), the fold of `MemoryStore`
(`Messages`/`PendingToolCalls`/`Epoch`) and the signature of `llm.Provider`/`Event`.
Identify that the real store only changes the **backend** of the log and the real provider
only changes the **origin** of the stream: no loop logic is touched.

### NETWORK

First test, store contract against SQLite, in `store_contract_test.go` +
`TestSQLiteStore_Contract`: `AppendEventAssignsMonotonicSeq` on a
`NewSQLiteStore(":memory:")`. Failure: `SQLiteStore` does not exist (does not compile).

### GREEN

Implement `SQLiteStore` minimum (scheme + `AppendEvent` with `MAX(seq)+1` +
`Messages` + `LoadSession`) until passing the first case of the contract.

### TRIANGULATE

- Complete the shared contract and run it against **both** stores
 (`TestMemoryStore_Contract`, `TestSQLiteStore_Contract`): order of `Messages`,
 `sinceSeq`, `ErrSessionNotFound`, `PendingToolCalls` (fold Called vs Resolved),
 `Epoch` stable, and `ConcurrentAppendsAssignUniqueSeqs` with `-race`.
- `TestSQLiteStore_ReopenResumesLog` (SQLite specific): write events on
 a temporary file, `Close`, `NewSQLiteStore` on the same path, check that
 `Messages(0)` rebuilds the history and the next `Seq` continues. It is the
 heart of "resumable after restart".
- `TestRunner_*` end-to-end on `SQLiteStore`: run a turn of the real `Runner`
 (with `FakeProvider`) against the `SQLiteStore` and verify that the same
 durable sequence persists as with `MemoryStore` (you can reuse M5/M9 helpers). Ensures
 that the loop runs **without changes** behind the real store, with `-race`.
- Provider mapping, without network (`anthropic_test.go`): `TestMapEvent_*` for each row
 of the table (text start/delta/stop, thinking delta, tool_use start with
 CallID/ToolName/Input raw, input json delta), and `TestMapEvent_IgnoresUnknown`
 (returns `ok == false`). `TestToTools_SortsByName` (cache determinism).
 `TestToMessages_MapsRoleAndText`.

### REFACTOR

Migrate `memstore_test.go` tests covered by the shared contract (without
losing coverage), `doc.go`/package comments, test helpers, format.
Verify entire suite green and `-race` clean.

### Border (manual, not `go test`)

With `ANTHROPIC_API_KEY` set: `wails dev`, send a real prompt, see streaming
of model text, confirm that the history remains in the DB and that after restarting
the app the `Seq` continues. It is the only section not covered by `go test`.

## 9. Acceptance criteria (Done when)

- `session.SQLiteStore` complies with `session.Store` (verified by `var _ Store`),
 persists the log in SQLite and reprojects `Messages`/`PendingToolCalls`/`Epoch`/
 `LoadSession` identical to `MemoryStore`.
- The **shared contract** passes against `MemoryStore` and `SQLiteStore`, including the
 concurrent case with clean `-race`.
- `SQLiteStore` is **resumable**: reopening the same file rebuilds the history
 and continues the `Seq` (`TestSQLiteStore_ReopenResumesLog`).
- The `Runner` runs **unchanged** behind the `SQLiteStore`: the M1..M8 suite remains
 green also against the real store.
- `llm.AnthropicProvider` meets `llm.Provider`; `mapEvent` and the converters are
 covered by pure tests (without network) that reflect the table of
 `../architecture/llm-claude.md`; `toTools` sorts by name.
- `app.go`: `NewApp` builds the `SQLiteStore` (decorated by `EmittingStore`) and the
 `AnthropicProvider`, with fallback to the fake without API key; `newApp` maintains
 injection for tests.
- `go test ./...` green, `go test -race` clean in concurrent tests,
 `gofmt -l .` empty, `go vet ./...` clean.
- Manual verification: an end-to-end prompt against the real provider produces
 visible streaming and the history survives a reboot. (Border not covered
 by `go test`.)

## 10. Commands

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Dependencias
go get modernc.org/sqlite
go get github.com/anthropics/anthropic-sdk-go

# Ciclo (test especifico primero)
go test -run 'TestSQLiteStore_Contract/AppendEventAssignsMonotonicSeq' ./internal/session

# Triangulacion (contrato en ambos stores, reanudacion, mapeo del provider)
go test -run 'TestMemoryStore_Contract|TestSQLiteStore_Contract' ./internal/session
go test -run 'TestSQLiteStore_ReopenResumesLog' ./internal/session
go test -run 'TestMapEvent_|TestToTools_|TestToMessages_' ./internal/llm

# Concurrencia (append concurrente + runner end-to-end sobre SQLite)
go test -race -run 'Contract/ConcurrentAppends|TestRunner_' ./...

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde

# Frontera red + SQLite (verificacion manual, no go test)
ANTHROPIC_API_KEY=... wails dev   # prompt real, ver streaming, reiniciar y comprobar historial
```

## 11. Table of expected evidence

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M1..M9 green before editing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Persistence, Claude adapter and Store contract read | `../architecture/agent-loop.md` (Durable persistence), `../architecture/llm-claude.md`, `store.go`, `memstore.go`, `provider.go` | identified behavior |
| NETWORK | Store contract against SQLite written first | `internal/session/store_contract_test.go` + `go test -run 'TestSQLiteStore_Contract/AppendEventAssignsMonotonicSeq' ./internal/session` | expected failure (does not compile) |
| GREEN | `SQLiteStore` minimum (schema + Append + Messages) | `internal/session/sqlitestore.go` | contract case passes |
| TRIANGULATE | Contract in both stores, resumption, E2E runner, provider mapping | `go test -run 'TestMemoryStore_Contract\|TestSQLiteStore_Contract' ./internal/session`, `go test -run TestMapEvent_ ./internal/llm`, `go test -race ...` | cases pass, `-race` clean |
| REFACTOR | Migrate memstore_test to contract, `anthropic.go`, `doc.go`, wiring `app.go` | `gofmt -w .`, `go vet ./...`, `go test ./...` | green suite, M1..M8 intact |

## 12. Risks and decisions

- **The logic of the loop does not change (invariant of M10).** All reality enters by
 injection into `app.go`: `SQLiteStore` behind `session.Store`,
 `AnthropicProvider` behind `llm.Provider`. If any test of M1..M8 changes, M10
 was done wrong. The store's shared contract is the network that tests it.
- **Pure SQLite Go driver (`modernc.org/sqlite`), not cgo.** Avoid the C
 toolchain in Wails builds (cross-compile Windows/macOS without cgo). The trade-off is
 slightly less performance than `mattn/go-sqlite3`; for a single local DB of a
 desktop app is irrelevant and build simplicity wins. Behind the
 `Store` interface, switching drivers is local to `sqlitestore.go`.
- **Write mutex, not retry transactions.** SQLite serializes
 writes; a `sync.Mutex` around read-`MAX(seq)`+insert makes the contract
 of `Seq` identical to that of `MemoryStore` (the concurrent case of the contract proves it
 with `-race`). It is the simplest option that works; Explicit transactions/WAL are added if real contention appears, without changing the interface. (`failInterruptedTools`)
 work after a reboot. Saving only messages would break the resume.
- **Epoch stable, not real driver.** `SQLiteStore.Epoch` returns zero epoch
 stable, same as `MemoryStore`: M1..M8 are still green and happy path does not
 rebuild. The actual driver (moving `Revision`/`BaselineSeq`/`Model` due to
 config changes) is its own milestone that the durable store **enables** but M10 does not need
 to fulfill the "Done when". The schema makes room (a `sessions` table) for
 when it arrives.
- **Compactor remains nil.** Anthropic's server-side compaction (betas) is a separate
 milestone; the `Runner` runs with `compactor == nil` as in M6/M9. M10 does not touch
 the overflow path.
- **Structured, deferred tool use history.** `llm.Message` carries only
 `Role`/`Text` and the projection only materializes text, so the multi-turn with actual
 `tool_result` depends on the rich projection (`History.EntriesForRunner`), no
 of the adapter. M10 faithfully maps what the loop produces today; the round-trip of
 `tool_result` arrives with that projection. It is a **conscious** limitation, not a
 adapter bug.
- **The provider mapping is TDD-ea; the network no.** `mapEvent` and the converters are
 pure functions testable with synthetic SDK events (RED/GREEN/TRIANGULATE).
 The live call to Anthropic is border, just like `runtime.EventsEmit`/the Vue in
 M9: it is verified by hand with an API key. Consistent with `AGENTS.md` ("test the
 runner against a fake, not against Wails/red").
- **Exact SDK names are verified, not invented.** `../architecture/llm-claude.md`
 already warns that only the text delta is 100% documented; the rest
 (`ThinkingDelta`, `InputJSONDelta`, effort/thinking adaptive binding) are
 committed against the SDK types when deploying. The test contract is the output
 `llm.Event`, robust to the internal name.
- **API key off disk/prompt.** `ANTHROPIC_API_KEY` per environment; without it, the
 app opens with the fake (it does not break `wails dev`). Doc Security Invariant

## 13. Sources

- Roadmap: `../plans/agent-loop-roadmap.md` (milestone M10)
- Architecture: `../architecture/agent-loop.md` (section "Durable persistence")
- Model adapter: `../architecture/llm-claude.md` (client, one turn = one
 streaming call, mapping table, tool use, adaptive thinking, what NOT to use)
- Way of working: `AGENTS.md`
- Go SDK Anthropic: https://github.com/anthropics/anthropic-sdk-go
- Pure SQLite Go driver: https://pkg.go.dev/modernc.org/sqlite
- Previous specs: `atenea-m1-tipos-store-spec.md` ..
 `atenea-m9-cableado-wails-spec.md`
```
