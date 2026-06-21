# Spec M4 — Tool registry + settle

Spec ejecutable del hito **M4** de `docs/atenea-agent-loop-roadmap.md`. Define el
estado final, el alcance, el plan TDD y los criterios de aceptacion para dejar el
**registry de tools**: la pieza que, dado un set de permisos, materializa las
**definiciones** anunciables al modelo (`Definitions`) y un **asentador**
(`Settle`) cerrado sobre ese set. `Settle` ejecuta una tool call conocida y
devuelve su `Result`; una tool denegada no aparece en `Definitions` y una tool
desconocida/stale devuelve error **sin** efectos laterales. El output grande se
acota via un `ToolOutputStore`. Incluye el primer builtin ejecutable (`echo`).

Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos, igual que el resto de `docs/`.

## 1. Contexto

Los hitos previos dejaron el camino de adentro hacia afuera
(`tipos -> store -> provider -> publisher`, ver el roadmap):

- **M1** dejo el dominio durable (`Seq`, `Message`, `SessionEvent`, `Store`,
  `MemoryStore`): el log de eventos es la unica fuente de verdad.
- **M2** dejo la frontera con el modelo (`llm.Provider`, `llm.Request`,
  `llm.Event`, `llm.EventKind`, `llm.Usage`) y un `FakeProvider` scriptable.
- **M3** dejo el `Publisher` (`internal/session/runner/publish.go`), que traduce
  el stream del proveedor a `SessionEvent` durables y mantiene el mapa
  `callID -> toolName` que el runner consultara al asentar tools.

El siguiente ladrillo es el **tool registry** (`internal/tool`). Su
responsabilidad (ver `docs/atenea-agent-loop.md`, "Tipos principales" y
"Streaming de eventos y ejecucion de tools") es:

- **materializar** contra los permisos del agente: devolver las `Definitions`
  (los schemas que el `llm.Request` anuncia al modelo en M5) y un `Settle`;
- **asentar** una tool call: validar que la tool exista y este en el set
  anunciado, ejecutarla y devolver su `Result`;
- **cerrar el set**: una tool denegada por permisos **no** aparece en
  `Definitions`, y una tool desconocida o no anunciada devuelve error **sin**
  ejecutar nada (sin efectos laterales);
- **acotar** el output grande fuera del mensaje, via un `ToolOutputStore`: el
  modelo ve una version acotada y el output completo queda referenciable por
  `callID`.

El registry vive en `internal/tool`. En M5 el runner lo materializa por turno con
los permisos del agente y le pasa el `Settle` al loop de consumo (`consume`, ver
"Streaming de eventos y ejecucion de tools"), que lo invoca **concurrentemente**
por cada `Tool.Called`. M4 lo construye y lo prueba **aislado**: registrando un
builtin determinista (`echo`), materializando con permisos a mano y verificando
las `Definitions` y el `Result` de `Settle`.

## 2. Objetivo

Dejar listo el registry, el set de tipos del paquete `tool` y el primer builtin:

En `internal/llm` (la definicion anunciable es del contrato con el proveedor):

- el tipo `ToolDef` (nombre, descripcion, schema JSON) que el registry
  materializa y que M5 pondra en `llm.Request.Tools`. Se agrega **aditivo**: no
  cambia `Provider`, `Request`, `Event` ni `Usage`.

En `internal/tool`:

- la interface `Tool` (`Name`, `Description`, `Schema`, `Execute`);
- los tipos `Call`, `Result`, `SettleFunc`, `Permissions` y `Materialized`;
- el `Registry` (+ `NewRegistry`) con `Materialize(perms Permissions) Materialized`,
  que filtra por permisos, arma `Definitions` deterministas y devuelve un `Settle`
  cerrado sobre las tools permitidas;
- el `UnknownToolError` que `Settle` devuelve ante una tool fuera del set;
- el `OutputStore` (+ `NewOutputStore`) que acota el output y guarda el completo
  por `callID`, seguro para uso concurrente (mutex);
- el builtin `Echo`: primer tool ejecutable (devuelve el campo `text` del input);
- tests de comportamiento que registran `echo`, materializan con permisos y
  verifican `Definitions` y `Settle`.

M4 **no** construye el `llm.Request`, ni el loop `consume`/`errgroup`, ni
`runTurn`, ni `read`/`edit` (hashline), ni toca Wails.

## 3. Alcance

### Dentro

- `internal/llm/tool.go`: tipo `ToolDef` (aditivo; M2 sigue verde).
- `internal/tool/registry.go`: `Tool`, `Call`, `Result`, `SettleFunc`,
  `Permissions`, `Materialized`, `Registry`, `NewRegistry`, `Materialize`,
  `UnknownToolError`.
- `internal/tool/output.go`: `OutputStore`, `NewOutputStore`, `Cap`, `Full`.
- `internal/tool/echo.go`: builtin `Echo`.
- Tests de comportamiento en `internal/tool/registry_test.go` (reemplaza el
  `scaffold_test.go` de M0, cuyo comentario ya anuncia "se reemplaza por tests
  reales en M4").
- Actualizar `internal/tool/doc.go` (el registry y el primer builtin aterrizaron).

### Fuera (no hacer en M4)

- Construir `llm.Request` desde el historial y poblar `Request.Tools` con las
  `Definitions` — **M5**. M4 deja el tipo `llm.ToolDef`; el campo `Request.Tools`
  se agrega cuando M5 arme el request (igual que M2 dejo `Request` minimo y crece
  sin cambiar la interface).
- El loop `consume`, `errgroup` y la ejecucion **concurrente** de tools, mas la
  espera del turno a que todas asienten — **M5**. M4 deja `Settle` seguro para
  uso concurrente (el `OutputStore` tiene mutex) y una prueba `-race` de higiene,
  pero la coreografia concurrente con su test `-race` real es de M5.
- Publicar `Tool.Called` antes de ejecutar y `Tool.Success`/`Tool.Failed` al
  asentar: lo hace el **runner** con el `Publisher` de M3 — **M5** (y **M8** en el
  camino de fallos). M4 solo devuelve `Result`/error; no persiste eventos.
- Tools **provider-executed** (el proveedor devuelve el resultado y el runner solo
  lo persiste, sin `Settle` local) — **M5**.
- Staleness por `ContextEpoch` (una tool valida al preparar deja de serlo al
  llamar) — **M7**. En M4 "stale" se cubre como "fuera del set materializado":
  `Settle` la rechaza con `UnknownToolError`.
- Interrupcion (`ctx` cancelado) de tools en vuelo y `failInterruptedTools` —
  **M8**.
- `read`/`edit` estilo hashline (`internal/tool/hashline`, ver
  `docs/atenea-read-edit-tools.md`): es la tool mas dificil y tiene su propio
  plan. M4 solo necesita **un** builtin ejecutable; `echo` alcanza. `bash`,
  `read`, `edit`, `write`, `grep`, `glob` llegan despues con sus tests.
- Modelo de permisos rico (ask/allow por patron, edicion/bash por ruta). En M4
  `Permissions` es el set de nombres permitidos; lo demas llega cuando el agente
  lo necesite.
- `EventBus`, `runtime.EventsEmit`, `app.go`, `main.go`, Wails, frontend — **M9**.
- `Store` SQLite y adaptador `Provider` real — **M10**.

## 4. Tipos del contrato (`llm.ToolDef`) y del registry (`tool`)

### `internal/llm/tool.go`

La definicion anunciable de una tool es parte del contrato con el proveedor: el
`llm.Request` (M5) la lleva para que el modelo sepa que puede invocar. Por eso
`ToolDef` vive en `internal/llm`, no en `internal/tool`. La direccion de
dependencia es `tool -> llm` (el registry materializa `[]llm.ToolDef`); ubicar
`ToolDef` en `llm` evita el ciclo que apareceria si `llm` importara `tool` al
agregar `Request.Tools` en M5.

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

`Provider`, `Request`, `Event`, `EventKind` y `Usage` no se tocan: el agregado es
un tipo nuevo en un archivo nuevo.

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
// fuera del mensaje via un ToolOutputStore" (ver docs/atenea-agent-loop.md). Es
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
// docs/atenea-read-edit-tools.md), que llega despues con su propio plan.
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

Notas:

- **El set anunciado es la compuerta.** `Materialize` filtra por `perms` y el
  `Settle` queda cerrado sobre `allowed`. Una tool denegada no esta en
  `Definitions` (no se anuncia) **ni** en `allowed` (no se asienta). Esto cumple
  "el registry valida contra el set anunciado antes de actuar".
- **Sin efectos laterales en el rechazo.** `Settle` consulta `allowed` y devuelve
  `UnknownToolError` **antes** de llamar `Execute`. Una tool fuera del set jamas
  ejecuta. El test lo verifica con un spy cuyo contador de `Execute` queda en 0.
- **Acotado fuera del mensaje.** El `OutputStore` guarda el output completo por
  `callID` y devuelve el acotado. El completo no se pierde; queda para UI/re-lectura.
- **`Definitions` deterministas.** Orden por nombre: estabiliza el request (cache
  de prompt) y hace los tests reproducibles sin depender del orden de iteracion
  del mapa.
- **`Result` minimo.** Solo `Output` + `Truncated` en M4. Partes ricas (p.ej. el
  header `[path#HASH]` que devuelve `edit`) se agregan cuando esa tool aterrice.

## 5. Semantica del registry

El contrato que M4 fija para el runner (M5) y para la frontera con el proveedor:

- **Materializar.** `Materialize(perms)` devuelve `Materialized{Definitions,
  Settle}`. `Definitions` son los `llm.ToolDef` de las tools permitidas, ordenados
  por nombre. `Settle` es el asentador cerrado sobre esas tools.
- **Asentar una tool conocida.** `Settle(ctx, Call{ID, Name, Input})` de una tool
  permitida ejecuta su `Execute(ctx, Input)` y devuelve su `Result` (acotado por el
  `OutputStore`). El happy path de M4.
- **Tool denegada.** Una tool registrada pero no permitida por `perms` **no**
  aparece en `Definitions` y `Settle` la rechaza con `UnknownToolError`.
- **Tool desconocida/stale.** Una `Call` cuyo `Name` no esta en el set
  materializado devuelve `UnknownToolError` **sin** ejecutar nada (sin efectos
  laterales). El error nombra la tool y se inspecciona con `errors.As`.
- **Error de la tool.** Si `Execute` devuelve error, `Settle` lo propaga tal cual
  (no lo envuelve como desconocido). M5 distingue "no permitida" de "fallo al
  ejecutar" al asentar (`Tool.Failed`).
- **Output acotado.** Un `Result` cuyo output supera el limite del `OutputStore`
  se devuelve con los primeros `limit` bytes y `Truncated == true`; el output
  completo queda en el store, recuperable por `callID`.
- **Input por JSON.** Las tools parsean `Input` con `json.Unmarshal`, nunca por
  match de string (consistente con la nota de `llm.Event.Input`).

## 6. Plan TDD

### Safety net

- Estado base verde antes de tocar nada. M4 agrega tipos nuevos en `internal/tool`
  y un `ToolDef` aditivo en `internal/llm`; primero se corre lo existente (M0..M3).
- Comando: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Resultado esperado: pasan limpio. Si algo falla, se reporta como preexistente y
  no se sigue a ciegas. Tras agregar `llm.ToolDef` se re-corre
  `go test ./internal/llm` para confirmar que M2 sigue verde (el cambio es aditivo).

### Understand

- Leer la entrada M4 del roadmap; "Tipos principales" (`Materialized`,
  `Registry.Materialize`) y "Streaming de eventos y ejecucion de tools"
  (`settle`, registrar antes de los efectos, acotar output, esperar a todas) de
  `docs/atenea-agent-loop.md`; y el spec de `read`/`edit`
  (`docs/atenea-read-edit-tools.md`) para entender por que `echo` (y no `read`)
  es el primer builtin.
- Comportamiento esperado: materializar filtrando por permisos con `Definitions`
  deterministas; asentar una tool conocida; rechazar denegada/desconocida sin
  efectos; acotar output grande via `OutputStore`.

### RED

- Escribir primero el test que falla:
  `TestRegistry_SettleExecutesAllowedTool`. Referencia a `NewRegistry`,
  `NewOutputStore`, `Echo`, `Materialize`, `Permissions`, `Call`, `Result` y
  `llm.ToolDef`, que aun no existen -> no compila -> falla (RED honesto en Go es
  fallo de compilacion del paquete de test).
- El test arma `NewRegistry(NewOutputStore(0), Echo{})`, materializa con
  `Permissions{"echo": true}`, asienta una `Call{ID: "c1", Name: "echo"}` con
  `Input` igual a `{"text":"hola"}` y afirma:
  - `Definitions` tiene un `llm.ToolDef` con `Name == "echo"`;
  - `Settle` devuelve `Result{Output: "hola"}` sin error.
- Comando:
  `go test -run TestRegistry_SettleExecutesAllowedTool ./internal/tool`
  -> fallo esperado.

### GREEN

- Escribir el minimo: `internal/llm/tool.go` (`ToolDef`), `internal/tool/registry.go`
  (interface `Tool`, `Call`, `Result`, `SettleFunc`, `Permissions`, `Materialized`,
  `Registry`, `NewRegistry`, `Materialize`, `UnknownToolError`),
  `internal/tool/output.go` (`OutputStore`, `NewOutputStore`, `Cap`, `Full`) y
  `internal/tool/echo.go` (`Echo`).
- Correr solo el test rojo hasta verde.
- Comando: `go test -run TestRegistry_SettleExecutesAllowedTool ./internal/tool`.

### TRIANGULATE

Agregar casos para evitar falso verde (los del roadmap):

- `TestRegistry_DeniedToolAbsentFromDefinitions`: registrar `Echo{}` y un segundo
  spy tool `spyTool{name: "secret"}`; materializar con `Permissions{"echo": true}`
  (sin `secret`); afirmar que `Definitions` lista **solo** `echo` y que `secret`
  no aparece.
- `TestRegistry_SettleUnknownToolHasNoSideEffects`: registrar un `spyTool` que
  cuenta sus `Execute`; materializar con permisos que lo **deniegan**; asentar
  `Call{Name: "secret"}`; afirmar que `Settle` devuelve `*UnknownToolError`
  (via `errors.As`) y que el contador de `Execute` del spy quedo en **0** (sin
  efectos laterales). Repetir con un nombre **no registrado** ("ghost"): mismo
  error, sin panico.
- `TestRegistry_LargeOutputCappedViaOutputStore`: un `bigTool` que devuelve un
  output de N bytes; `NewOutputStore(limit)` con `limit < N`; asentar; afirmar que
  `Result.Output` mide `limit`, `Result.Truncated == true`, y que
  `OutputStore.Full(callID)` devuelve el output completo de N bytes.
- `TestRegistry_DefinitionsSortedByName` (determinismo): registrar tools cuyos
  nombres no esten ya ordenados ("zeta", "alpha", "echo"), permitir todas y afirmar
  que `Definitions` sale ordenado por `Name`.
- `TestRegistry_SettleToolExecuteErrorPropagates` (camino de error de tool, no de
  permiso): `Execute` de la tool devuelve error; `Settle` lo propaga tal cual y
  **no** es `UnknownToolError`.
- `TestEcho_ExecuteReturnsText` y `TestEcho_InvalidInputErrors`: el builtin parsea
  el JSON y devuelve `text`; un input invalido (`{`) da error.
- Comandos:
  - `go test -run TestRegistry ./internal/tool`
  - `go test -run TestEcho ./internal/tool`
  - `go test -race -run TestRegistry ./internal/tool` (higiene: el `OutputStore`
    es estado mutable compartido; la concurrencia real de `settle` es de M5)

### REFACTOR

- Limpieza sin cambiar comportamiento: factorizar los helpers de test
  (`spyTool`/`bigTool` con su contador y su output; un `materializeEcho(t)` que
  arme el registry permitido) si reduce duplicacion; actualizar
  `internal/tool/doc.go` (el registry y el primer builtin aterrizaron en M4;
  `bash`/`read`/`edit`/`write`/`grep`/`glob` siguen pendientes) y, si aplica,
  `internal/llm/doc.go` (se agrego `ToolDef` al contrato).
- Verificar que la suite sigue verde tras el formateo.
- Comando: `gofmt -w internal && go vet ./... && go test ./...`.

## 7. Criterios de aceptacion (Done when)

1. Existe `llm.ToolDef` (`Name`, `Description`, `Schema json.RawMessage`) en
   `internal/llm/tool.go`, y `Provider`/`Request`/`Event`/`Usage` no cambiaron
   (M2 sigue verde).
2. Existe la interface `Tool` (`Name`, `Description`, `Schema`, `Execute`) y los
   tipos `Call`, `Result`, `SettleFunc`, `Permissions`, `Materialized` y
   `UnknownToolError` en `internal/tool/registry.go`.
3. Existe `Registry` (+ `NewRegistry`) con
   `Materialize(perms Permissions) Materialized`, que devuelve `Definitions`
   (los `llm.ToolDef` de las tools permitidas, ordenados por nombre) y un `Settle`.
4. `Settle` de una tool **permitida** ejecuta su `Execute` y devuelve su `Result`.
5. Una tool **denegada** por permisos no aparece en `Definitions`, y `Settle`
   sobre una tool fuera del set (denegada, desconocida o no registrada) devuelve
   `*UnknownToolError` **sin** ejecutar nada (verificado con un spy: 0 `Execute`).
6. Un error de `Execute` se propaga por `Settle` tal cual y **no** es
   `UnknownToolError`.
7. Existe `OutputStore` (+ `NewOutputStore`/`Cap`/`Full`): un output que supera el
   limite se devuelve acotado con `Truncated == true` y el completo queda
   recuperable por `callID`. El store es seguro para uso concurrente.
8. Existe el builtin `Echo`: `Execute` parsea el JSON y devuelve `text`; un input
   invalido da error.
9. `go test ./...` (y `-race` donde aplica) pasa; `go vet ./...` limpio;
   `gofmt -l .` vacio.
10. No hubo cambios en `app.go`, `main.go`, Wails ni el frontend; ni en
    `internal/session`, `internal/session/runner` o `internal/event`. En
    `internal/llm` solo se agrego `tool.go`. El `scaffold_test.go` de
    `internal/tool` se reemplazo por tests reales.

## 8. Comandos

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

## 9. Tabla de evidencia esperada

Al cerrar M4, la respuesta/PR debe incluir esta tabla con resultados reales:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0..M3 verde antes de editar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Contrato de registry/settle leido | roadmap M4, `docs/atenea-agent-loop.md`, `docs/atenea-read-edit-tools.md` | comportamiento identificado |
| RED | Test de settle de tool permitida escrito primero | `registry_test.go` + `go test -run TestRegistry_SettleExecutesAllowedTool ./internal/tool` | fallo esperado (no compila) |
| GREEN | `llm/tool.go` + `registry.go` + `output.go` + `echo.go` minimos | `internal/llm/tool.go`, `internal/tool/{registry,output,echo}.go` | test especifico pasa |
| TRIANGULATE | Denegada ausente, desconocida sin efectos, output acotado, orden, error de tool, echo | `go test -run TestRegistry ./internal/tool`, `go test -run TestEcho ./internal/tool`, `go test -race ...` | casos pasan, `-race` limpio |
| REFACTOR | Helpers de test, `doc.go` actualizados | `gofmt -w internal`, `go vet ./...`, `go test ./...` | suite verde, M2 intacto |

## 10. Riesgos y decisiones

- **`ToolDef` en `llm`, no en `tool`.** La definicion anunciable es del contrato
  con el proveedor (el `llm.Request` la lleva). Ubicarla en `llm` mantiene la
  direccion de dependencia `tool -> llm` y evita el ciclo que apareceria si `llm`
  importara `tool` cuando M5 agregue `Request.Tools []ToolDef`. El registry la
  referencia como `llm.ToolDef`.
- **`Request.Tools` diferido a M5.** M4 deja el tipo `ToolDef` pero no agrega el
  campo `Tools` a `Request` ni construye requests: eso es M5. Agregar un campo que
  nadie puebla seria especular. Sigue el patron de M2 ("Request crece sin cambiar
  la interface").
- **`Settle(ctx, Call)` en vez de tres args posicionales.** El pseudocodigo de la
  arquitectura muestra `settle(gctx, ev.CallID, ev.ToolName, ev.Input)`; M4 lo
  tipa como `Settle(ctx, Call{ID, Name, Input})`. Un struct nombrado se lee mejor,
  evita confundir el orden de los strings y crece (metadata de epoch en M7) sin
  romper la firma. M5 arma la `Call` desde el evento en una linea.
- **El set anunciado es la compuerta de seguridad.** `Settle` se cierra sobre el
  mapa `allowed` de la materializacion, no sobre el catalogo completo. Asi "valida
  contra el set anunciado antes de actuar" es estructural: una tool fuera del set
  no tiene como ejecutarse. Es la misma idea que el hash de frescura en `read`/`edit`
  (`docs/atenea-read-edit-tools.md`): fallar seguro antes de tocar nada.
- **Rechazo sin efectos, verificado por contador.** Que `Settle` devuelva el error
  "antes" de `Execute` no se asume: el test usa un spy que cuenta sus ejecuciones y
  afirma 0 en el caso denegado/desconocido. Es la unica forma de probar "sin
  efectos laterales" sin un FS de por medio.
- **`UnknownToolError` tipado, no sentinel.** Un tipo con `Name` da un mensaje
  accionable ("tool \"x\" desconocida o no permitida") y se inspecciona con
  `errors.As`, igual que los mensajes de mismatch del `edit`. Un error de
  `Execute` se propaga tal cual: M5 distingue "no permitida" de "fallo al ejecutar".
- **`Permissions` como set de nombres.** En M4 alcanza un `map[string]bool` con
  deny-por-ausencia: el agente declara explicitamente que anuncia. El modelo rico
  (ask, permisos por patron de ruta para `edit`/`bash`) llega cuando el agente lo
  necesite; adelantarlo seria diseno especulativo.
- **`OutputStore` con candado desde M4.** Aunque la concurrencia real de `settle`
  es de M5, el `OutputStore` es el unico estado mutable compartido que M4 introduce
  y M5 lo escribira desde varias goroutines. Se candadea ya (es barato) y M4 corre
  un `-race` de higiene; el test `-race` de la coreografia concurrente del turno es
  de M5.
- **`echo` y no `read` como primer builtin.** El roadmap pide "un builtin simple
  (p.ej. echo o read)" solo para tener algo ejecutable contra el registry. `read`/
  `edit` arrastran toda la maquinaria hashline (hash, snapshots, parser de patch,
  recovery; ver `docs/atenea-read-edit-tools.md`), que es la pieza mas dificil y
  tiene su propio plan. `echo` es determinista, sin FS y sin efectos: prueba el
  registry de punta a punta sin acoplar M4 a esa complejidad.
- **Acotar, no descartar.** El output grande no se trunca y se pierde: se guarda
  completo en el `OutputStore` y solo se acota la copia que ve el modelo. Asi la UI
  (M9) o una re-lectura pueden recuperar el output entero por `callID`. El acotado
  por bytes puede partir un caracter UTF-8 multibyte; refinarlo a corte por runes
  (con su test) se hace cuando una tool real produzca ese output, no en M4.
- **Provider-executed fuera de M4.** El registry solo conoce tools **locales**
  (las que Atenea ejecuta via `Settle`). Las provider-executed (el proveedor
  devuelve el resultado y el runner solo lo persiste) las maneja M5 en el loop de
  consumo, no el registry.
- **`scaffold_test.go` de `tool` se reemplaza.** A diferencia del de `runner` (que
  ancla `errgroup` en `go.mod` hasta M5), el scaffold de `tool` no ancla ninguna
  dependencia: su unico fin era fijar el paquete en M0 y su comentario ya dice "se
  reemplaza por tests reales en M4". M4 lo reemplaza por `registry_test.go`.

## 11. Fuentes

- Roadmap: `docs/atenea-agent-loop-roadmap.md` (hito M4)
- Arquitectura: `docs/atenea-agent-loop.md` (secciones "Tipos principales" —
  `Materialized`, `Registry.Materialize` — y "Streaming de eventos y ejecucion de
  tools" — `settle`, registrar antes de los efectos, acotar output, esperar a todas)
- Tools `read`/`edit`: `docs/atenea-read-edit-tools.md` (por que `echo` es el
  primer builtin y `read`/`edit` van despues con su plan)
- Manera de trabajo: `AGENTS.md`
- Specs previos: `docs/atenea-m1-tipos-store-spec.md`,
  `docs/atenea-m2-provider-fake-spec.md`, `docs/atenea-m3-publisher-spec.md`
