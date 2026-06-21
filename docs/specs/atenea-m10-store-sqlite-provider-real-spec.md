# Spec M10 — Store SQLite + provider real

Spec ejecutable del hito **M10** de `docs/atenea-agent-loop-roadmap.md`. Define el
estado final, el alcance, el plan TDD y los criterios de aceptacion para cambiar
los dos fakes que quedaban (`MemoryStore` y `FakeProvider`) por implementaciones
reales **detras de las mismas interfaces**, **sin tocar la logica del loop**
(`Run`, `runTurn`, `consume`, `Publisher`). La regla central, igual que en M9: lo
real entra por inyeccion en `app.go`; los paquetes del loop no aprenden de SQLite
ni de Anthropic.

M10 agrega dos piezas reales y un cableado:

- **`session.SQLiteStore`**: implementa `session.Store` sobre una sola DB SQLite en
  el directorio de datos de la app. El log de eventos es la fuente de verdad
  durable; `Messages`, `PendingToolCalls` y `Epoch` se reproyectan desde el log,
  igual que en `MemoryStore`. La diferencia es que **sobrevive a reinicios**: tras
  cerrar y reabrir la app, el `Seq` continua y el historial se reconstruye desde
  disco.
- **`llm.AnthropicProvider`**: implementa `llm.Provider` sobre el SDK oficial
  `anthropic-sdk-go` (ver `docs/atenea-llm-claude.md`). Una llamada a
  `Messages.NewStreaming` por turno; mapea el stream del SDK a `llm.Event` y cierra
  el channel al terminar.
- **`app.go`**: `NewApp` arma el `SQLiteStore` (decorado por el `EmittingStore` de
  M9) y el `AnthropicProvider`; `newApp` sigue inyectando store y provider para que
  los tests usen el `MemoryStore` y el `FakeProvider`.

Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos, igual que el resto de `docs/`.

## 1. Contexto

M1..M9 dejaron el loop completo y verde, primero contra fakes y luego cableado a
Wails, sin una sola dependencia de SQLite ni de Anthropic en la logica del loop:

- **M1** dejo el dominio durable (`Seq`, `Message`, `SessionEvent`, `Store`) y el
  `MemoryStore`: el log de eventos es la unica fuente de verdad y los mensajes son
  una proyeccion (`Messages(sinceSeq)`). `AppendEvent` asigna `SessionID`/`Seq` e
  ignora los que traiga el evento.
- **M2** dejo la frontera con el modelo (`llm.Provider`, `Request`, `Event`,
  `Usage`, `ToolDef`) y el `FakeProvider` replayable. `Stream` produce **un** turno
  y **cierra** el channel; cancelar `ctx` lo corta sin colgar receptores.
- **M3** dejo el `Publisher`: traduce el stream a `SessionEvent` durables y coalesce
  los deltas (`Text.*`, `Reasoning.*`, `Tool.Input.*`).
- **M4** dejo el `tool.Registry` (`Materialize -> {Definitions, Settle}`).
- **M5/M6** dejaron el turno y el loop externo (`runTurn`, `consume`, `Run`,
  `Inbox`, `MaxSteps`).
- **M7** dejo las senales de control y el `ContextEpoch`. `MemoryStore.Epoch`
  devuelve el epoch cero estable, asi el camino feliz no reconstruye nunca; el
  comentario ya anticipo "el driver real ... llega con el store real (M10)".
- **M8** dejo el manejo de fallos (`FailUnresolvedTools`, `Step.Failed`,
  `failInterruptedTools` al arrancar `Run`: reanudacion tras crash).
- **M9** cableo el loop a Wails: `event.Bus`, `event.EmittingStore` (decora
  cualquier `session.Store`) y `app.go` (`SendPrompt`/`Stop`). El comentario de
  `newApp` ya dice "M10 cambia provider/store por los reales sin tocar esto", y
  `newIDGen` "un ID estable entre reinicios llega con el store persistente de M10".

Lo que falta es hacerlo **real y persistente**. La arquitectura ya lo describe
(`docs/atenea-agent-loop.md`, "Persistencia durable": una sola DB SQLite en el
directorio de datos de la app; `Run` siempre reconstruye el request desde el
store) y el adaptador del modelo ya tiene su diseno completo en
`docs/atenea-llm-claude.md` (que mapea explicitamente M10 al adaptador real).

## 2. Objetivo

Un prompt end-to-end funciona contra el proveedor **real** (Claude/Anthropic) y su
historial queda en una DB SQLite que **sobrevive a reinicios** de la app. Las dos
implementaciones entran detras de `session.Store` y `llm.Provider` **sin cambiar
una linea** de `Run`, `runTurn`, `consume`, `Publisher` ni el resto del loop: la
suite M1..M8 sigue verde, ahora tambien corriendo contra el `SQLiteStore`. La red
(la llamada viva a Anthropic) y el SQLite real son las **unicas fronteras no
cubiertas por `go test`** deterministico; el mapeo puro de eventos y el contrato
del store si se testean.

## 3. Alcance

### Dentro

- `internal/session/sqlitestore.go`: `SQLiteStore` + `NewSQLiteStore(dsn)` que
  cumple `session.Store` (`AppendEvent`, `LoadSession`, `Messages`, `Epoch`,
  `PendingToolCalls`), con esquema, migracion idempotente y round-trip completo del
  `SessionEvent`.
- `internal/session/store_contract_test.go`: una **suite de contrato compartida**
  (`testStoreContract(t, newStore)`) que corre los mismos casos de comportamiento
  contra cualquier `session.Store`. Se ejecuta contra `MemoryStore` y
  `SQLiteStore`; garantiza que el store real cumple el contrato de M1..M8.
- `internal/session/sqlitestore_test.go`: lo especifico de SQLite (persistencia
  entre dos aperturas del mismo archivo; `Seq` continua tras reabrir).
- `internal/llm/anthropic.go`: `AnthropicProvider` + `NewAnthropicProvider(opts)`
  que cumple `llm.Provider`, mas la funcion pura `mapEvent` (SDK -> `llm.Event`) y
  los conversores `toMessages`/`toTools`.
- `internal/llm/anthropic_test.go`: tests de la funcion pura `mapEvent` y los
  conversores (sin red).
- `app.go`: `NewApp` arma `SQLiteStore` (decorado por `EmittingStore`) y
  `AnthropicProvider`; resolucion del path de la DB y de la API key; fallback al
  `FakeProvider`/`demoProvider` si no hay API key (para que `wails dev` siga sin
  red). `newApp` no cambia su firma (sigue inyectando store/provider para tests).
- `go.mod`: `go get modernc.org/sqlite` (driver SQLite puro Go, sin cgo) y
  `go get github.com/anthropics/anthropic-sdk-go`.

### Fuera (no hacer en M10)

- **Driver real del `ContextEpoch`** (mover `Agent`/`Model`/`Revision`/`BaselineSeq`
  ante cambios de config, reconciliacion de archivos/instrucciones): `SQLiteStore.
  Epoch` devuelve el **mismo epoch estable** que `MemoryStore`, asi el camino feliz
  no reconstruye y M1..M8 siguen verdes. El esquema deja lugar (tabla `sessions`)
  para cuando el driver se necesite; M10 solo aporta **durabilidad**, no nueva
  semantica de epoch. Ver Riesgos.
- **Compactor real** (`Compactor.NeedsCompaction`/`Compact` que mide tokens contra
  el limite del modelo y resume): el `Runner` corre con `compactor == nil` (nunca
  compacta), igual que M6/M9. El comentario del `Compactor` dice "el real ... llega
  en M10", pero atarlo a la compaction server-side de Anthropic es un hito propio;
  M10 deja la ruta de overflow tal como esta. Ver Riesgos.
- **Historial estructurado de tool use** (`assistant message` con bloques
  `tool_use` + `user message` con `tool_result` por `callID`, ver
  `docs/atenea-llm-claude.md`): hoy `llm.Message` lleva solo `Role`/`Text` y la
  proyeccion `Messages` solo materializa texto. El multi-turno con tools reales
  pide la proyeccion rica (`History.EntriesForRunner`), que sigue diferida. M10
  mapea fielmente lo que el loop **hoy** produce (historial de texto + `ToolDef`s);
  el round-trip de `tool_result` llega con la proyeccion rica.
- **`Inbox` persistente**: el `MemoryInbox` sigue en memoria. La interface `Inbox`
  ya anticipa "M10 puede agregar una version persistente", pero el roadmap M10 pide
  **Store** SQLite; persistir el inbox se agrega cuando se necesite reanudar
  prompts en cola tras un crash, detras de la misma interface.
- **Prompt caching, thinking summarized, steering mid-session, betas de
  compaction**: todo lo "verificar el binding en el SDK" de
  `docs/atenea-llm-claude.md` que no hace falta para un turno de texto/tool basico.
  M10 usa thinking **adaptive** (lo unico soportado en Opus 4.8) y `max_tokens` por
  defecto; lo demas se suma cuando un test lo pida.
- **Cambios en la logica del loop**: invariante duro, igual que M9. Si algun test de
  M1..M8 cambia, M10 se hizo mal.

## 4. El store SQLite (`internal/session/sqlitestore.go`)

`SQLiteStore` guarda el log de eventos en una tabla y reproyecta todo desde ahi,
igual que `MemoryStore` reproyecta desde su slice. La unica diferencia observable
es la durabilidad: el estado vive en un archivo, no en un mapa.

### Esquema

Una sola tabla de eventos, con round-trip **completo** del `SessionEvent` para que
las tres proyecciones (`Messages`, `PendingToolCalls`, y el `Usage` de
`Step.Ended`) se reconstruyan identicas tras reabrir:

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

La `PRIMARY KEY (session_id, seq)` da el orden total durable por sesion y prohibe
huecos o duplicados a nivel de motor. No hay tabla de mensajes: `Messages` se
deriva del log, fiel al contrato de M1 ("Messages es derivado del log, no
almacenado aparte").

### Implementacion (bosquejo)

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

`LoadSession`, `Messages`, `Epoch` y `PendingToolCalls` son **lecturas** que
reconstruyen exactamente lo que `MemoryStore` reconstruye, pero con SQL:

- `LoadSession`: `SELECT 1 FROM events WHERE session_id = ? LIMIT 1`; si no hay
  fila -> `ErrSessionNotFound`; si la hay -> `Session{ID: sessionID}`.
- `Messages(sinceSeq)`: `SELECT ... WHERE session_id = ? AND has_message = 1 AND
  seq > ? ORDER BY seq`; cada fila vuelve a un `Message` con `Seq = seq`. Si la
  sesion no existe (no hay ningun evento), `ErrSessionNotFound` (chequeo de
  existencia primero, como `MemoryStore`).
- `Epoch`: existencia primero (`ErrSessionNotFound` si no hay eventos); luego
  `ContextEpoch{}` (epoch cero estable, identico a `MemoryStore`; ver Fuera/Riesgos).
- `PendingToolCalls`: lee `kind IN (Tool.Called, Tool.Success, Tool.Failed)` en
  orden de `seq` y aplica el mismo fold que `MemoryStore` (Called abre, Success/
  Failed cierran), preservando el orden de llamada.

Decisiones:

- **Mutex en `AppendEvent`, igual que `MemoryStore`.** SQLite es single-writer; el
  candado hace **atomica** la secuencia leer-`MAX(seq)`+insertar y deja el contrato
  de `Seq` byte-identico al del store en memoria (sin huecos ni duplicados bajo
  appends concurrentes, `-race` limpio). Es lo mas simple que cumple el contrato; no
  se necesita una transaccion explicita ni reintentos por conflicto.
- **Round-trip completo del evento, no solo el `Message`.** Persistir `Kind`,
  `CallID`, `ToolName`, `Input`, `Usage`, `Error` es lo que permite que
  `PendingToolCalls` y el `Usage` de `Step.Ended` sobrevivan al reinicio. Guardar
  solo el texto romperia `failInterruptedTools` tras un crash (M8) y la reanudacion.
- **`has_message` explicito.** Un evento puede no aportar a la proyeccion
  (`Message == nil`) o materializar un `Message` con texto vacio; la columna
  distingue ambos sin ambiguedad de NULL.
- **`ctx` honrado.** A diferencia de `MemoryStore` (que lo ignora), aqui las
  queries usan `...Context`: cancelacion/timeout reales. Cierra el contrato que la
  firma prometia desde M1.

## 5. El contrato compartido del Store (`store_contract_test.go`)

El "Done when" del roadmap es "los tests de M1..M8 siguen verdes con el store
real". La forma limpia de garantizarlo es **un solo set de tests de comportamiento
que corre contra cualquier `Store`**, en vez de duplicar los de `MemoryStore`.

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

Los tests de `memstore_test.go` que ya cubren estos casos se **migran** al contrato
compartido (REFACTOR sin perder cobertura): asi un mismo escenario verifica las dos
implementaciones y cualquier divergencia del store real salta en CI.

`sqlitestore_test.go` agrega lo que el contrato compartido **no** puede expresar
(es lo unico propio de SQLite): abrir un archivo, escribir eventos, **cerrar**,
**reabrir** el mismo archivo y comprobar que `Messages(0)` reconstruye el historial
y que el siguiente `AppendEvent` continua el `Seq` (reanudable tras reinicio).

## 6. El adaptador de proveedor real (`internal/llm/anthropic.go`)

`AnthropicProvider` implementa `llm.Provider` exactamente como lo describe
`docs/atenea-llm-claude.md`: una llamada a `Messages.NewStreaming` por turno,
acumulando con `msg.Accumulate`, mapeando cada evento del SDK a `llm.Event` y
cerrando el channel al terminar.

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

### Mapeo (la parte testeable, pura)

`mapEvent` es una funcion **pura** `(anthropic.MessageStreamEventUnion) -> (Event,
bool)` (el bool descarta eventos sin equivalente). Es el corazon del adaptador y
**si** se testea sin red, con eventos del SDK sinteticos, siguiendo la tabla de
`docs/atenea-llm-claude.md`:

| Bloque/evento Anthropic | `llm.Event` |
| --- | --- |
| content_block_start (text) | `{Kind: TextStarted}` |
| content_block_delta (TextDelta) | `{Kind: TextDelta, Text: d.Text}` |
| content_block_stop (text) | `{Kind: TextEnded}` |
| content_block_start (thinking) | `{Kind: ReasoningStarted}` |
| content_block_delta (ThinkingDelta) | `{Kind: ReasoningDelta, Text: d.Thinking}` |
| content_block_start (tool_use) | `{Kind: ToolCall, CallID, ToolName, Input}` |
| content_block_delta (InputJSONDelta) | `{Kind: ToolInputDelta, CallID, Input}` |
| content_block_stop (tool_use) | `{Kind: ToolInputEnded, CallID}` |
| message_stop | (lo emite el cierre del stream como `StepEnded`) |

Reglas fieles al doc de Claude:

- El input de `tool_use` se toma del **JSON crudo** del SDK (`...JSON.Input.Raw()`),
  nunca por match de string: Opus 4.8 puede escapar el JSON distinto entre turnos.
- Los nombres exactos de los tipos del SDK (`ThinkingDelta`, `InputJSONDelta`,
  binding de `effort`/thinking) se **verifican contra el SDK** al implementar, no se
  inventan (lo dice el propio doc). El contrato del test es el `llm.Event` de
  salida, no el nombre interno del SDK.
- `toTools` ordena las tools de forma **determinista (por nombre)**: invariante de
  prompt caching del doc (no reordenar el prefijo).

### La frontera no testeada

La llamada viva `Messages.NewStreaming` (red, API key, reintentos del SDK) es la
**frontera no cubierta por `go test`**, igual que `runtime.EventsEmit` y el Vue en
M9. `Stream` arma `params`, delega en el SDK y traduce; lo unico no determinista es
la red. Se verifica a mano con una API key real (seccion 8). Opcional como
triangulacion: un `httptest.Server` que reproduce un SSE grabado, inyectado con
`option.WithBaseURL`, para ejercitar `Stream` end-to-end sin Anthropic; no es
requisito de cierre (el mapeo puro ya cubre la logica).

## 7. El cableado de la app (`app.go`)

`newApp(provider, emit)` **no cambia de firma**: sigue inyectando un store
(via su uso interno) y un provider para que los tests usen `MemoryStore` +
`FakeProvider`. Lo que cambia es `NewApp` (produccion), que ahora arma las piezas
reales:

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

Para que `NewApp` pueda inyectar un store **ya construido** (el SQLite real) sin
romper la simetria de M9, se factoriza la construccion en torno al store: el store
sigue envuelto por el `EmittingStore` (puente Store -> UI de M9) **antes** de
pasarlo al `Runner`, asi la UI ve cada evento durable igual que con el
`MemoryStore`. El registry, el inbox (sigue `MemoryInbox`), los permisos y
`newIDGen` no cambian.

Resolucion de recursos:

- **Path de la DB**: directorio de datos del usuario (`os.UserConfigDir()` +
  `atenea/atenea.db`), con override por `ATENEA_DB` (util en dev). Se crea el
  directorio si falta. Es "una sola DB SQLite en el directorio de datos de la app",
  como pide la arquitectura.
- **API key**: `ANTHROPIC_API_KEY` del entorno (el SDK la lee por defecto). Nunca en
  disco ni en el prompt (invariante de `docs/atenea-llm-claude.md`). Sin key ->
  `demoProvider` (fake), para no romper `wails dev`.
- **`newIDGen`**: con el store persistente, el `assistantMessageID` deberia ser
  estable entre reinicios; M10 puede pasar a UUID (`github.com/google/uuid`, ya
  indirecto en `go.mod`) o dejar el contador atomico si no hay colision observable.
  Decision menor, no bloquea el cierre.

`main.go` no cambia: ya bindea `App` con `SendPrompt`/`Stop`.

## 8. Plan TDD

### Safety net

Antes de tocar nada, confirmar que M1..M9 estan verdes:

```bash
go test ./...        # toda la suite verde
go vet ./...         # limpio
gofmt -l .           # vacio
```

### Understand

Leer "Persistencia durable" de `docs/atenea-agent-loop.md` (DB unica, `Run`
reconstruye desde el store), `docs/atenea-llm-claude.md` completo (un turno = una
llamada streaming, tabla de mapeo, que NO usar), el contrato de `session.Store`
(las cinco operaciones y `ErrSessionNotFound`), el fold de `MemoryStore`
(`Messages`/`PendingToolCalls`/`Epoch`) y la firma de `llm.Provider`/`Event`.
Identificar que el store real solo cambia el **backend** del log y el provider real
solo cambia el **origen** del stream: ninguna logica del loop se toca.

### RED

Primer test, contrato del store contra SQLite, en `store_contract_test.go` +
`TestSQLiteStore_Contract`: `AppendEventAssignsMonotonicSeq` sobre un
`NewSQLiteStore(":memory:")`. Falla: `SQLiteStore` no existe (no compila).

### GREEN

Implementar `SQLiteStore` minimo (esquema + `AppendEvent` con `MAX(seq)+1` +
`Messages` + `LoadSession`) hasta pasar el primer caso del contrato.

### TRIANGULATE

- Completar el contrato compartido y correrlo contra **ambos** stores
  (`TestMemoryStore_Contract`, `TestSQLiteStore_Contract`): orden de `Messages`,
  `sinceSeq`, `ErrSessionNotFound`, `PendingToolCalls` (fold Called vs Resolved),
  `Epoch` estable, y `ConcurrentAppendsAssignUniqueSeqs` con `-race`.
- `TestSQLiteStore_ReopenResumesLog` (especifico de SQLite): escribir eventos sobre
  un archivo temporal, `Close`, `NewSQLiteStore` sobre el mismo path, comprobar que
  `Messages(0)` reconstruye el historial y el siguiente `Seq` continua. Es el
  corazon de "reanudable tras reinicio".
- `TestRunner_*` end-to-end sobre `SQLiteStore`: correr un turno del `Runner` real
  (con `FakeProvider`) contra el `SQLiteStore` y verificar que persiste la misma
  secuencia durable que con `MemoryStore` (puede reusar helpers de M5/M9). Asegura
  que el loop corre **sin cambios** detras del store real, con `-race`.
- Mapeo del provider, sin red (`anthropic_test.go`): `TestMapEvent_*` por cada fila
  de la tabla (text start/delta/stop, thinking delta, tool_use start con
  CallID/ToolName/Input crudo, input json delta), y `TestMapEvent_IgnoresUnknown`
  (devuelve `ok == false`). `TestToTools_SortsByName` (determinismo de cache).
  `TestToMessages_MapsRoleAndText`.

### REFACTOR

Migrar los tests de `memstore_test.go` cubiertos por el contrato compartido (sin
perder cobertura), `doc.go`/comentarios de paquete, helpers de test, formato.
Verificar suite entera verde y `-race` limpio.

### Frontera (manual, no `go test`)

Con `ANTHROPIC_API_KEY` seteada: `wails dev`, enviar un prompt real, ver streaming
de texto del modelo, confirmar que el historial queda en la DB y que tras reiniciar
la app el `Seq` continua. Es el unico tramo no cubierto por `go test`.

## 9. Criterios de aceptacion (Done when)

- `session.SQLiteStore` cumple `session.Store` (verificado por `var _ Store`),
  persiste el log en SQLite y reproyecta `Messages`/`PendingToolCalls`/`Epoch`/
  `LoadSession` identico a `MemoryStore`.
- El **contrato compartido** pasa contra `MemoryStore` y `SQLiteStore`, incluido el
  caso concurrente con `-race` limpio.
- `SQLiteStore` es **reanudable**: reabrir el mismo archivo reconstruye el historial
  y continua el `Seq` (`TestSQLiteStore_ReopenResumesLog`).
- El `Runner` corre **sin cambios** detras del `SQLiteStore`: la suite M1..M8 sigue
  verde tambien contra el store real.
- `llm.AnthropicProvider` cumple `llm.Provider`; `mapEvent` y los conversores estan
  cubiertos por tests puros (sin red) que reflejan la tabla de
  `docs/atenea-llm-claude.md`; `toTools` ordena por nombre.
- `app.go`: `NewApp` arma el `SQLiteStore` (decorado por `EmittingStore`) y el
  `AnthropicProvider`, con fallback al fake sin API key; `newApp` mantiene la
  inyeccion para tests.
- `go test ./...` verde, `go test -race` limpio en los tests concurrentes,
  `gofmt -l .` vacio, `go vet ./...` limpio.
- Verificacion manual: un prompt end-to-end contra el proveedor real produce
  streaming visible y el historial sobrevive a un reinicio. (Frontera no cubierta
  por `go test`.)

## 10. Comandos

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

## 11. Tabla de evidencia esperada

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M1..M9 verde antes de editar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Persistencia, adaptador Claude y contrato del Store leidos | `docs/atenea-agent-loop.md` (Persistencia durable), `docs/atenea-llm-claude.md`, `store.go`, `memstore.go`, `provider.go` | comportamiento identificado |
| RED | Contrato del Store contra SQLite escrito primero | `internal/session/store_contract_test.go` + `go test -run 'TestSQLiteStore_Contract/AppendEventAssignsMonotonicSeq' ./internal/session` | fallo esperado (no compila) |
| GREEN | `SQLiteStore` minimo (esquema + Append + Messages) | `internal/session/sqlitestore.go` | caso del contrato pasa |
| TRIANGULATE | Contrato en ambos stores, reanudacion, runner E2E, mapeo del provider | `go test -run 'TestMemoryStore_Contract\|TestSQLiteStore_Contract' ./internal/session`, `go test -run TestMapEvent_ ./internal/llm`, `go test -race ...` | casos pasan, `-race` limpio |
| REFACTOR | Migrar memstore_test al contrato, `anthropic.go`, `doc.go`, cableado `app.go` | `gofmt -w .`, `go vet ./...`, `go test ./...` | suite verde, M1..M8 intactos |

## 12. Riesgos y decisiones

- **La logica del loop no cambia (invariante de M10).** Toda la realidad entra por
  inyeccion en `app.go`: `SQLiteStore` detras de `session.Store`,
  `AnthropicProvider` detras de `llm.Provider`. Si algun test de M1..M8 cambia, M10
  se hizo mal. El contrato compartido del store es la red que lo prueba.
- **Driver SQLite puro Go (`modernc.org/sqlite`), no cgo.** Evita el toolchain de C
  en los builds de Wails (cross-compile a Windows/macOS sin cgo). El trade-off es
  algo menos de rendimiento que `mattn/go-sqlite3`; para una sola DB local de una
  app de escritorio es irrelevante y la simplicidad de build gana. Detras de la
  interface `Store`, cambiar de driver es local a `sqlitestore.go`.
- **Mutex de escritura, no transacciones con reintento.** SQLite serializa
  escrituras; un `sync.Mutex` alrededor de leer-`MAX(seq)`+insertar hace el contrato
  de `Seq` identico al de `MemoryStore` (el caso concurrente del contrato lo prueba
  con `-race`). Es la opcion mas simple que cumple; transacciones explicitas/WAL se
  suman si aparece contencion real, sin cambiar la interface.
- **Round-trip completo del evento.** Se persisten todos los campos durables del
  `SessionEvent` (no solo el texto) para que `PendingToolCalls`, el `Usage` de
  `Step.Ended` y la limpieza de tools colgadas de M8 (`failInterruptedTools`)
  funcionen tras un reinicio. Guardar solo mensajes romperia la reanudacion.
- **Epoch estable, no driver real.** `SQLiteStore.Epoch` devuelve el epoch cero
  estable, igual que `MemoryStore`: M1..M8 siguen verdes y el camino feliz no
  reconstruye. El driver real (mover `Revision`/`BaselineSeq`/`Model` por cambios de
  config) es un hito propio que el store durable **habilita** pero M10 no necesita
  para cumplir el "Done when". El esquema deja lugar (una tabla `sessions`) para
  cuando llegue.
- **Compactor sigue nil.** La compaction server-side de Anthropic (betas) es un
  hito aparte; el `Runner` corre con `compactor == nil` como en M6/M9. M10 no toca
  la ruta de overflow.
- **Historial de tool use estructurado, diferido.** `llm.Message` lleva solo
  `Role`/`Text` y la proyeccion solo materializa texto, asi que el multi-turno con
  `tool_result` real depende de la proyeccion rica (`History.EntriesForRunner`), no
  del adaptador. M10 mapea fielmente lo que el loop hoy produce; el round-trip de
  `tool_result` llega con esa proyeccion. Es una limitacion **consciente**, no un
  bug del adaptador.
- **El mapeo del provider se TDD-ea; la red no.** `mapEvent` y los conversores son
  funciones puras testeables con eventos del SDK sinteticos (RED/GREEN/TRIANGULATE).
  La llamada viva a Anthropic es frontera, igual que `runtime.EventsEmit`/el Vue en
  M9: se verifica a mano con una API key. Coherente con `AGENTS.md` ("test the
  runner against a fake, not against Wails/red").
- **Nombres exactos del SDK se verifican, no se inventan.** `docs/atenea-llm-claude.md`
  ya avisa que solo el delta de texto esta 100% documentado; el resto
  (`ThinkingDelta`, `InputJSONDelta`, binding de effort/thinking adaptive) se
  confirma contra los tipos del SDK al implementar. El contrato del test es el
  `llm.Event` de salida, robusto ante el nombre interno.
- **API key fuera de disco/prompt.** `ANTHROPIC_API_KEY` por entorno; sin ella, la
  app abre con el fake (no rompe `wails dev`). Invariante de seguridad del doc.

## 13. Fuentes

- Roadmap: `docs/atenea-agent-loop-roadmap.md` (hito M10)
- Arquitectura: `docs/atenea-agent-loop.md` (seccion "Persistencia durable")
- Adaptador del modelo: `docs/atenea-llm-claude.md` (cliente, un turno = una
  llamada streaming, tabla de mapeo, tool use, thinking adaptive, que NO usar)
- Manera de trabajo: `AGENTS.md`
- SDK Go de Anthropic: https://github.com/anthropics/anthropic-sdk-go
- Driver SQLite puro Go: https://pkg.go.dev/modernc.org/sqlite
- Specs previos: `docs/atenea-m1-tipos-store-spec.md` ..
  `docs/atenea-m9-cableado-wails-spec.md`
```
