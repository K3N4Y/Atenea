---
updated_at: 2026-07-09
summary: Specification for m4 tool registry spec.
---

# Spec M4 — Tool registry + settle

`../plans/agent-loop-roadmap.md` Milestone **M4** Executable Spec. Defines the
final state, the scope, the TDD plan and the acceptance criteria to leave the
**registry of tools**: the piece that, given a set of permissions, materializes the
**definitions** announceable to the model (`Definitions`) and a **setter**
(`Settle`) closed on that set. `Settle` executes a known tool call and
returns its `Result`; a denied tool does not appear in `Definitions` and an unknown/stale tool
 returns an error with **no** side effects. The large output is
bounded via a `ToolOutputStore`. Includes the first builtin executable (`echo`).

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

Previous milestones left the path from the inside out
(`tipos -> store -> provider -> publisher`, see roadmap):

- **M1** left the durable domain (`Seq`, `Message`, `SessionEvent`, `Store`,
 `MemoryStore`): the event log is the only source of truth.
- **M2** left the border with the model (`llm.Provider`, `llm.Request`,
 `llm.Event`, `llm.EventKind`, `llm.Usage`) and a scriptable `FakeProvider`.
- **M3** left the `Publisher` (`internal/session/runner/publish.go`), which translates
 the provider's stream to durable `SessionEvent` and maintains the
 `callID -> toolName` map that the runner will consult when settling tools.

The next brick is the **tool registry** (`internal/tool`). Your
responsibility (see `../architecture/agent-loop.md`, "Main Types" and
"Event Streaming and Execution Tools") is:

- **materialize** against the agent's permissions: return the `Definitions`
 (the schemas that the `llm.Request` announces to the model in M5) and a `Settle`;
- **seat** a tool call: validate that the tool exists and is in the set
 announced, execute it and return its `Result`;
- **close the set**: a tool denied due to permissions **does not** appear in
 `Definitions`, and an unknown or unannounced tool returns an error **without**
 execute anything (no side effects);
- **bound** the large output outside the message, via a `ToolOutputStore`: the
 model sees a bounded version and the complete output is referenceable by
 `callID`.

The registry lives in `internal/tool`. In M5 the runner materializes it in turn with
the agent's permissions and passes the `Settle` to the consumption loop (`consume`, see
"Streaming of events and execution of tools"), which invokes it **concurrently**
for each `Tool.Called`. M4 builds it and tests it **isolated**: registering a deterministic
builtin (`echo`), materializing with permissions by hand, and verifying
the `Definitions` and the `Result` of `Settle`.

## 2. Objective

Leave the registry ready, the set of package types `tool` and the first builtin:

In `internal/llm` (the advertiseable definition is from the contract with the supplier):

- the type `ToolDef` (name, description, JSON schema) that the registry
 materializes and that M5 will put in `llm.Request.Tools`. **additive** is added: does not
 change `Provider`, `Request`, `Event` or `Usage`.

In `internal/tool`:

- the `Tool` interface (`Name`, `Description`, `Schema`, `Execute`); with `Materialize(perms Permissions) Materialized`,
 that filters by permissions, arms deterministic `Definitions` and returns a closed `Settle`
 on the allowed tools;
- the `UnknownToolError` that `Settle` returns when faced with a tool outside the set;
- the `OutputStore` (+ `NewOutputStore`) that bounds the output and saves the complete
 by `callID`, safe for concurrent use (mutex);
- the `Echo` builtin: first executable tool (returns the `text` field of the input);
- behavioral tests that register `echo`, materialize with permissions and
 verify `Definitions` and `Settle`.

M4 **does not** build the `llm.Request`, nor the `consume`/`errgroup` loop, nor
`runTurn`, nor `read`/`edit` (hashline), nor does it touch Wails.

## 3. Scope

### Inside

- `internal/llm/tool.go`: type `ToolDef` (additive; M2 remains green).
- `internal/tool/registry.go`: `Tool`, `Call`, `Result`, `SettleFunc`,
 `Permissions`, `Materialized`, `Registry`, `NewRegistry`, `Materialize`,
 `UnknownToolError`.
- `internal/tool/output.go`: `OutputStore`, `NewOutputStore`, `Cap`, `Full`.
- `internal/tool/echo.go`: builtin `Echo`.
- Behavior tests in `internal/tool/registry_test.go` (replaces the
 `scaffold_test.go` from M0, whose comment already announces "it is replaced by real tests
 on M4").
- Update `internal/tool/doc.go` (the registry and the first builtin landed).

### Out (do not do in M4)

- Build `llm.Request` from history and populate `Request.Tools` with the
 `Definitions` — **M5**. M4 leaves type `llm.ToolDef`; the `Request.Tools`
 field is added when M5 creates the request (just as M2 left `Request` minimum and grows
 without changing the interface).
- The loop `consume`, `errgroup` and the **concurrent** execution of tools, plus the
 wait for the turn for everyone to agree — **M5**. M4 leaves `Settle` safe for
 concurrent use (the `OutputStore` has a mutex) and a `-race` hygiene test,
 but the concurrent choreography with its actual `-race` test is from M5.
- Publish `Tool.Called` before running and `Tool.Success`/`Tool.Failed` when
 seat: done by the **runner** with the `Publisher` of M3 — **M5** (and **M8** on the
 fault path). M4 only returns `Result`/error; does not persist events.
- Tools **provider-executed** (the provider returns the result and the runner only
 persists it, without local `Settle`) — **M5**.
- Staleness by `ContextEpoch` (a tool valid when preparing stops being valid when calling
) — **M7**. In M4 "stale" is covered as "outside the materialized set":
 `Settle` rejects it with `UnknownToolError`.
- Interruption (`ctx` canceled) of tools in flight and `failInterruptedTools` —
 **M8**.
- `read`/`edit` hashline style (`internal/tool/hashline`, see
 `../architecture/read-edit-tools.md`): it is the most difficult tool and has its own
 plan. M4 only needs **one** executable builtin; `echo` reaches. `bash`,
 `read`, `edit`, `write`, `grep`, `glob` arrive later with their tests.
- Rich permissions model (ask/allow by pattern, edit/bash by path). In M4
 `Permissions` is the set of allowed names; the rest arrives when the agent
 needs it.
- `EventBus`, `runtime.EventsEmit`, `app.go`, `main.go`, Wails, frontend — **M9**.
- `Store` SQLite and real `Provider` adapter — **M10**.

## 4. Types of the contract (`llm.ToolDef`) and the registry (`tool`)

### `internal/llm/tool.go`

The announceable definition of a tool is part of the contract with the provider: the
`llm.Request` (M5) carries it so that the model knows what it can invoke. That's why
`ToolDef` lives in `internal/llm`, not `internal/tool`. The address of
dependency is `tool -> llm` (the registry materializes `[]llm.ToolDef`); placing
`ToolDef` in `llm` avoids the loop that would appear if `llm` imported `tool` when
adding `Request.Tools` in M5.

```go
package llm

import "encoding/json"

// ToolDef es el esquema anunciable de una tool: lo que el Request lleva al
// proveedor para que el modelo sepa que herramientas puede invocar y con que
// forma de input. El registry (internal/tool) lo materializa desde sus tools
// permitidas; M5 lo pone en Request.Tools al construir el turno. Schema es el
// JSON Schema crudo del input (lo emite cada tool); el proveedor real (M10) lo
// traduce al formato de su SDK.
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage
}
```

`Provider`, `Request`, `Event`, `EventKind` and `Usage` are not touched: the aggregate is
a new type in a new file.

### `internal/tool/registry.go`

```go
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"atenea/internal/llm"
)

// Tool es una herramienta registrada: su esquema anunciable y su ejecucion. El
// registry la materializa (Name/Description/Schema -> llm.ToolDef) y la asienta
// (Execute). Execute recibe el input JSON crudo del modelo y lo parsea con
// json.Unmarshal (nunca por match de string: el modelo escapa el JSON distinto
// entre turnos, ver llm.Event.Input). Devuelve el Result completo; el registry
// se encarga de acotarlo.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(ctx context.Context, input json.RawMessage) (Result, error)
}

// Call es una tool call que Settle debe asentar. En M5 el loop de consumo la
// arma desde el evento del proveedor: Call{ID: ev.CallID, Name: ev.ToolName,
// Input: ev.Input}. Un struct nombrado se lee mejor que tres args posicionales y
// crece (p.ej. metadata de epoch en M7) sin cambiar la firma de Settle.
type Call struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// Result es el resultado asentado de una tool call. Output es lo que vera el
// modelo en el siguiente turno (acotado por el OutputStore si era grande);
// Truncated marca que es una version acotada y que el output completo quedo en el
// OutputStore, referenciable por el CallID de la Call.
type Result struct {
	Output    string
	Truncated bool
}

// SettleFunc asienta una tool call: valida contra el set materializado, ejecuta y
// devuelve el Result. Esta cerrada sobre las tools permitidas de una
// materializacion: una tool fuera del set devuelve UnknownToolError sin ejecutar
// nada. M5 la invoca concurrentemente desde consume (errgroup); por eso es segura
// para uso concurrente (no muta estado compartido salvo el OutputStore, que tiene
// su candado).
type SettleFunc func(ctx context.Context, call Call) (Result, error)

// Permissions es el set de tools permitidas por nombre. Materialize solo anuncia
// (y Settle solo asienta) las que estan en true; una tool ausente o en false se
// trata como denegada: el agente declara explicitamente su set anunciado. El
// modelo de permisos rico (ask, edicion/bash por patron) llega cuando el agente
// lo necesite; en M4 alcanza el set de nombres.
type Permissions map[string]bool

// Materialized es el resultado de Materialize: las definiciones anunciables al
// modelo y el asentador cerrado sobre ese set. El runner (M5) pone Definitions en
// llm.Request.Tools y pasa Settle al loop de consumo.
type Materialized struct {
	Definitions []llm.ToolDef
	Settle      SettleFunc
}

// UnknownToolError lo devuelve Settle cuando la Call nombra una tool que no esta
// en el set materializado: desconocida para el registry o denegada por permisos
// (en M7 tambien una stale por epoch). No se ejecuta nada (sin efectos
// laterales). M5 lo traduce a Tool.Failed. Es un tipo (no un sentinel) para que
// el mensaje nombre la tool y el llamador la inspeccione con errors.As.
type UnknownToolError struct{ Name string }

func (e *UnknownToolError) Error() string {
	return fmt.Sprintf("tool %q desconocida o no permitida", e.Name)
}

// Registry es el catalogo de tools del agente y su acotador de output. Es
// inmutable tras NewRegistry (Materialize solo lee), asi que materializar y
// asentar desde varias goroutines es seguro; el unico estado mutable compartido
// es el OutputStore, que se candadea solo.
type Registry struct {
	tools   map[string]Tool
	outputs *OutputStore
}

// NewRegistry arma el registry con su OutputStore y las tools dadas, indexadas por
// nombre. Si dos tools comparten nombre gana la ultima (config del programa, no
// input del modelo).
func NewRegistry(outputs *OutputStore, tools ...Tool) *Registry {
	m := make(map[string]Tool, len(tools))
	for _, t := range tools {
		m[t.Name()] = t
	}
	return &Registry{tools: m, outputs: outputs}
}

// Materialize filtra el catalogo por permisos y devuelve las definiciones
// anunciables y un Settle cerrado sobre las tools permitidas. Las Definitions van
// ordenadas por nombre para que el request sea determinista (estabiliza el cache
// de prompt del proveedor y los tests). El Settle captura solo las permitidas:
// una Call fuera de ese set devuelve UnknownToolError ANTES de ejecutar, asi que
// una tool denegada o desconocida no produce efectos laterales.
func (r *Registry) Materialize(perms Permissions) Materialized {
	allowed := make(map[string]Tool, len(r.tools))
	defs := make([]llm.ToolDef, 0, len(r.tools))
	for name, t := range r.tools {
		if !perms[name] {
			continue
		}
		allowed[name] = t
		defs = append(defs, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })

	settle := func(ctx context.Context, call Call) (Result, error) {
		t, ok := allowed[call.Name]
		if !ok {
			return Result{}, &UnknownToolError{Name: call.Name}
		}
		res, err := t.Execute(ctx, call.Input)
		if err != nil {
			return Result{}, err
		}
		return r.outputs.Cap(call.ID, res.Output), nil
	}
	return Materialized{Definitions: defs, Settle: settle}
}
```

### `internal/tool/output.go`

```go
package tool

import "sync"

// OutputStore acota el output de cada tool call y guarda el completo por callID. El
// loop pone en el historial (que el modelo ve) el Output acotado; el completo
// queda referenciable para la UI o una re-lectura. Cumple "output grande se acota
// fuera del mensaje via un ToolOutputStore" (ver ../atenea-agent-loop.md). Es
// seguro para uso concurrente: en M5 varias goroutines de settle escriben a la
// vez.
type OutputStore struct {
	limit int
	mu    sync.Mutex
	full  map[string]string
}

// NewOutputStore crea el store con el limite de bytes que vera el modelo. Un
// limit <= 0 desactiva el acotado (todo el output pasa tal cual), util en tests.
func NewOutputStore(limit int) *OutputStore {
	return &OutputStore{limit: limit, full: make(map[string]string)}
}

// Cap guarda el output completo bajo callID y devuelve el Result que vera el
// modelo: el output entero si cabe, o los primeros limit bytes con Truncated =
// true. El completo siempre queda en el store, recuperable con Full.
func (s *OutputStore) Cap(callID, output string) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.full[callID] = output
	if s.limit > 0 && len(output) > s.limit {
		return Result{Output: output[:s.limit], Truncated: true}
	}
	return Result{Output: output}
}

// Full devuelve el output completo guardado para un callID.
func (s *OutputStore) Full(callID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.full[callID]
	return v, ok
}
```

### `internal/tool/echo.go`

```go
package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// Echo es el primer builtin: devuelve tal cual el texto recibido. No tiene
// efectos laterales ni toca el FS, asi que da algo ejecutable y determinista para
// probar el registry de punta a punta (materializar -> Settle -> Result) sin
// arrastrar la maquinaria de read/edit (hashline, ver
// ../atenea-read-edit-tools.md), que llega despues con su propio plan.
type Echo struct{}

func (Echo) Name() string        { return "echo" }
func (Echo) Description() string { return "Devuelve tal cual el texto recibido en el campo text." }

func (Echo) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
}

// Execute parsea el input JSON (nunca por match de string) y devuelve el campo
// text. Un input que no es el JSON esperado es un error de la tool, no del
// registry: Settle lo propaga y M5 lo asienta como Tool.Failed.
func (Echo) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("echo: input invalido: %w", err)
	}
	return Result{Output: in.Text}, nil
}
```

Notes:

- **The announced set is the gate.** `Materialize` filters through `perms` and the
 `Settle` is closed over `allowed`. A denied tool is not in
 `Definitions` (not announced) **nor** in `allowed` (not posted). This satisfies
 "the registry validates against the announced set before acting."
- **No side effects on rejection.** `Settle` queries `allowed` and returns
 `UnknownToolError` **before** calling `Execute`. A tool outside the set never
 executes. The test is verified with a spy whose `Execute` counter remains at 0.
- **Bounded outside the message.** The `OutputStore` saves the complete output by
 `callID` and returns the bounded one. The complete is not lost; remains for UI/re-reading.
- **`Definitions` deterministic.** Order by name: stabilizes the request (prompt cache
) and makes the tests reproducible without depending on the iteration order
 of the map.
- **`Result` minimum.** Only `Output` + `Truncated` in M4. Rich parts (e.g. the
 header `[path#HASH]` which returns `edit`) are added when that tool lands.

## 5. Semantics of the registry

The contract that M4 sets for the runner (M5) and for the border with the supplier:

- **Materialize.** `Materialize(perms)` returns `Materialized{Definitions,
  Settle}`. `Definitions` are the `llm.ToolDef` of the allowed tools, ordered
 by name. `Settle` is the closed setter on those tools.
- **Set a known tool.** `Settle(ctx, Call{ID, Name, Input})` of an allowed tool
 executes its `Execute(ctx, Input)` and returns its `Result` (bounded by the
 `OutputStore`). The happy path of M4.
- **Tool denied.** A tool registered but not allowed by `perms` **does not**
 appears in `Definitions` and is rejected by `Settle` with `UnknownToolError`.
- **Tool unknown/stale.** A `Call` whose `Name` is not in the set
 materialized returns `UnknownToolError` **without** executing anything (no
 side effects). The error names the tool and is checked with `errors.As`.
- **Tool error.** If `Execute` returns error, `Settle` propagates it as is
 (does not wrap it as unknown). M5 distinguishes "not allowed" from "failed to
 execute" when posting (`Tool.Failed`).
- **Output bounded.** A `Result` whose output exceeds the limit of `OutputStore`
 is returned with the first `limit` bytes and `Truncated == true`; the complete output
 remains in the store, recoverable by `callID`.
- **Input by JSON.** The tools parse `Input` with `json.Unmarshal`, never by
 string match (consistent with the `llm.Event.Input` note).

## 6. TDD Plan

### Safety net

- Green base state before touching anything. M4 adds new types in `internal/tool`
 and an additive `ToolDef` in `internal/llm`; first the existing one is run (M0..M3).
- Command: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Expected result: they pass clean. If something fails, it is reported as pre-existing and
 is not followed blindly. After adding `llm.ToolDef`,
 `go test ./internal/llm` is re-run to confirm that M2 is still green (the change is additive).

### Understand

- Read entry M4 of the roadmap; "Main types" (`Materialized`,
 `Registry.Materialize`) and "Event streaming and tool execution"
 (`settle`, register before effects, bound output, wait for all) from
 `../architecture/agent-loop.md`; and the spec of `read`/`edit`
 (`../architecture/read-edit-tools.md`) to understand why `echo` (and not `read`)
 is the first builtin.
- Expected behavior: materialize filtering by permissions with deterministic `Definitions`
; set a known tool; reject denied/unknown without
 effect; limit large output via `OutputStore`.

### NETWORK

- Write the test that fails first:
 `TestRegistry_SettleExecutesAllowedTool`. Reference to `NewRegistry`,
 `NewOutputStore`, `Echo`, `Materialize`, `Permissions`, `Call`, `Result` and
 `llm.ToolDef`, which do not yet exist -> does not compile -> fails (Honest RED in Go is
 compilation failure of the test package).
- The test arm `NewRegistry(NewOutputStore(0), Echo{})`, materializes with
 `Permissions{"echo": true}`, seats a `Call{ID: "c1", Name: "echo"}` with
 `Input` equal to `{"text":"hola"}` and asserts:
 - `Definitions` has a `llm.ToolDef` with `Name == "echo"`;
 - `Settle` returns `Result{Output: "hola"}` no error.
- Command:
 `go test -run TestRegistry_SettleExecutesAllowedTool ./internal/tool`
 -> expected failure.

### GREEN

- Write the minimum: `internal/llm/tool.go` (`ToolDef`), `internal/tool/registry.go`
 (interface `Tool`, `Call`, `Result`, `SettleFunc`, `Permissions`, `Materialized`,
 `Registry`, `NewRegistry`, `Materialize`,
- Command: `go test -run TestRegistry_SettleExecutesAllowedTool ./internal/tool`.

### TRIANGULATE

Add cases to avoid false green (those in the roadmap):

- `TestRegistry_DeniedToolAbsentFromDefinitions`: register `Echo{}` and a second
 spy tool `spyTool{name: "secret"}`; materialize with `Permissions{"echo": true}`
 (without `secret`); assert that `Definitions` lists **only** `echo` and that `secret`
 does not appear.
- `TestRegistry_SettleUnknownToolHasNoSideEffects`: register a `spyTool` that
 counts its `Execute`; materialize with permissions that **deny** it; seat
 `Call{Name: "secret"}`; assert that `Settle` returns `*UnknownToolError`
 (via `errors.As`) and that the spy's `Execute` counter remained at **0** (without
 side effects). Repeat with an **unregistered** name ("ghost"): same
 error, no panic.
- `TestRegistry_LargeOutputCappedViaOutputStore`: a `bigTool` that returns a
 output of N bytes; `NewOutputStore(limit)` with `limit < N`; settle; assert that
 `Result.Output` measures `limit`, `Result.Truncated == true`, and that
 `OutputStore.Full(callID)` returns the full output of N bytes.
- `TestRegistry_DefinitionsSortedByName` (determinism): register tools whose
 names are not already ordered ("zeta", "alpha", "echo"), allow all and assert
 that `Definitions` comes out sorted by `Name`.
- `TestRegistry_SettleToolExecuteErrorPropagates` (tool error path, not
 permission): tool `Execute` returns error; `Settle` propagates it as is and
 **is not** `UnknownToolError`.
- `TestEcho_ExecuteReturnsText` and `TestEcho_InvalidInputErrors`: the builtin parses
 the JSON and returns `text`; an invalid input (`{`) gives an error.
- Commands:
 - `go test -run TestRegistry ./internal/tool`
 - `go test -run TestEcho ./internal/tool`
 - `go test -race -run TestRegistry ./internal/tool` (hygiene: the `OutputStore`
    es estado mutable compartido; la concurrencia real de `settle` es de M5)

### REFACTOR

- Cleanup without changing behavior: factor the test
 helpers (`spyTool`/`bigTool` with its counter and output; a `materializeEcho(t)` that
 creates the allowed registry) if it reduces duplication; update
 `internal/tool/doc.go` (the registry and the first builtin landed on M4;
 `bash`/`read`/`edit`/`write`/`grep`/`glob` are still pending) and, if applicable,
 `internal/llm/doc.go` (`ToolDef` was added to the contract).
- Verify that the suite is still green after formatting.
- Command: `gofmt -w internal && go vet ./... && go test ./...`.

## 7. Acceptance criteria (Done when)

1. There is `llm.ToolDef` (`Name`, `Description`, `Schema json.RawMessage`) in
 `internal/llm/tool.go`, and `Provider`/`Request`/`Event`/`Usage` did not change
 (M2 is still green).
2. There is the interface `Tool` (`Name`, `Description`, `Schema`, `Execute`) and the
 types `Call`, `Result`, `SettleFunc`, `Permissions`, `Materialized` and
 `UnknownToolError` in `internal/tool/registry.go`.
3. There is `Registry` (+ `NewRegistry`) with
 `Materialize(perms Permissions) Materialized`, which returns `Definitions`
 (the `llm.ToolDef` of the allowed tools, sorted by name) and a `Settle`.
4. `Settle` from a **allowed** tool executes its `Execute` and returns its `Result`.
5. A tool **denied** due to permissions does not appear in `Definitions`, and `Settle`
 on a tool outside the set (denied, unknown or not registered) returns
 `*UnknownToolError` **without** executing anything (verified with a spy: 0 `Execute`).
6. A `Execute` error propagates through `Settle` as is and is **not**
 `UnknownToolError`.
7. There is `OutputStore` (+ `NewOutputStore`/`Cap`/`Full`): an output that exceeds the
 limit is returned bounded with `Truncated == true` and the complete output is
 recoverable by `callID`. The store is safe for concurrent use.
8. There is the `Echo` builtin: `Execute` parses the JSON and returns `text`; an invalid input
 gives an error.
9. `go test ./...` (and `-race` where applicable) passes; `go vet ./...` clean;
 `gofmt -l .` empty.
10. There were no changes to `app.go`, `main.go`, Wails or the frontend; nor in
    `internal/session`, `internal/session/runner` o `internal/event`. En
    `internal/llm` solo se agrego `tool.go`. El `scaffold_test.go` de
    `internal/tool` se reemplazo por tests reales.

## 8. Commands

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Confirmar que M2 sigue verde tras agregar llm.ToolDef
go test ./internal/llm

# Ciclo (test especifico primero)
go test -run TestRegistry_SettleExecutesAllowedTool ./internal/tool
go test -run TestRegistry ./internal/tool
go test -run TestEcho ./internal/tool

# Higiene de concurrencia (la real es de M5)
go test -race -run TestRegistry ./internal/tool

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
```

## 9. Table of expected evidence

When closing M4, the response/PR should include this table with actual results:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0..M3 green before editing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | registry/settle contract read | roadmap M4, `../architecture/agent-loop.md`, `../architecture/read-edit-tools.md` | identified behavior |
| NETWORK | Allowed tool settlement test written first | `registry_test.go` + `go test -run TestRegistry_SettleExecutesAllowedTool ./internal/tool` | expected failure (does not compile) |
| GREEN | `llm/tool.go` + `registry.go` + `output.go` + `echo.go` minimums | `internal/llm/tool.go`, `internal/tool/{registry,output,echo}.go` | specific test passes |
| TRIANGULATE | Denied absent, unknown without effects, bounded output, order, tool error, echo | `go test -run TestRegistry ./internal/tool`, `go test -run TestEcho ./internal/tool`, `go test -race ...` | cases pass, `-race` clean |
| REFACTOR | Test helpers, `doc.go` updated | `gofmt -w internal`, `go vet ./...`, `go test ./...` | green suite, M2 intact |

## 10. Risks and decisions

- **`ToolDef` in `llm`, not in `tool`.** The advertiseable definition is from the contract
 with the supplier (`llm.Request` has it). Placing it at `llm` maintains the
 `tool -> llm` dependency address and avoids the loop that would appear if `llm`
 imported `tool` when M5 adds `Request.Tools []ToolDef`. The registry references
 as `llm.ToolDef`.
- **`Request.Tools` deferred to M5.** M4 leaves the type `ToolDef` but does not add the
 field `Tools` to `Request` nor construct requests: that's M5. Adding a field that
 no one populates would be speculating. It follows the pattern of M2 ("Request grows without changing
 the interface").
- **`Settle(ctx, Call)` instead of three positional args.** The pseudocode of the
 architecture shows `settle(gctx, ev.CallID, ev.ToolName, ev.Input)`; M4 types it
 as `Settle(ctx, Call{ID, Name, Input})`. A named struct reads better,
 avoids confusing the order of strings, and grows (epoch metadata in M7) without
 breaking the signature. M5 assembles the `Call` from the event in one line.
- **The announced set is the security hatch.** `Settle` closes on the
 `allowed` map of the materialization, not on the complete catalog. Thus "validate
 against the announced set before acting" is structural: a tool outside the set
 has no way to be executed. It's the same idea as the freshness hash in `read`/`edit`
 (`../architecture/read-edit-tools.md`): fail safe before touching anything.
- **Rejection without effects, verified by counter.** That `Settle` returns the error
 "before" `Execute` is not assumed: the test uses a spy that counts its executions and
 asserts 0 in the denied/unknown case. It is the only way to test "without
 side effects" without an FS involved.
- **`UnknownToolError` typed, not sentinel.** A type with `Name` gives an actionable
 message ("tool \"x\" unknown or not allowed") and is inspected with
 `errors.As`, just like the `edit` mismatch messages. An error of
 `Execute` is propagated as is: M5 distinguishes "not allowed" from "failure to execute".
- **`Permissions` as a name set.** In M4 it reaches a `map[string]bool` with
 deny-by-absence: the agent explicitly declares that it announces. The rich model
 (ask, permissions per route pattern for `edit`/`bash`) arrives when the agent needs it
; advancing it would be speculative design.
- **`OutputStore` with lock from M4.** Although the real concurrency of `settle`
 is from M5, the `OutputStore` is the only shared mutable state that M4 introduces
 and M5 will write it from several goroutines. It is locked now (it's cheap) and M4 runs
 a hygiene `-race`; the `-race` test of the concurrent choreography of the turn is
 of M5.
- **`echo` and not `read` as the first builtin.** The roadmap asks for "a simple builtin
 (e.g. echo or read)" just to have something executable against the registry. `read`/
 `edit` carry all the hashline machinery (hash, snapshots, patch parser,
 recovery; see `../architecture/read-edit-tools.md`), which is the most difficult piece and
 has its own plan. `echo` is deterministic, without FS and without effects: it tests the
 registry from end to end without coupling M4 to that complexity.
- **Limit, do not discard.** The large output is not truncated and lost: the entire
 is saved in the `OutputStore` and only the copy that the model sees is limited. Thus the UI
 (M9) or a re-read can recover the entire output by `callID`. The byte-bounded
 can split a multibyte UTF-8 character; refining it to cut by runes
 (with its test) is done when a real tool produces that output, not in M4.
- **Provider-executed outside of M4.** The registry only knows **local**
 tools (those that Atenea executes via `Settle`). The provider-executed ones (the provider
 returns the result and the runner only persists it) are handled by M5 in the consumption
 loop, not the registry.
- **`scaffold_test.go` of `tool` is replaced.** Unlike that of `runner` (which
 anchors `errgroup` in `go.mod` up to M5), the scaffold `tool` does not anchor any
 dependency: its only purpose was to pin the package on M0 and its comment already says "it is
 replaced by real tests on M4". M4 replaces it with `registry_test.go`.

## 11. Sources

- Roadmap: `../plans/agent-loop-roadmap.md` (milestone M4)
- Architecture: `../architecture/agent-loop.md` (sections "Main types" —
 `Materialized`, `Registry.Materialize` — and "Event streaming and execution of
 tools" — `settle`, register before effects, limit output, wait for all)
- Tools `read`/`edit`: `../architecture/read-edit-tools.md` (because `echo` is the
 first builtin and `read`/`edit` come later with their plan)
- Way of working: `AGENTS.md`
- Previous specs: `atenea-m1-tipos-store-spec.md`,
 `atenea-m2-provider-fake-spec.md`, `atenea-m3-publisher-spec.md`
