# Spec M7 — Senales de control (rebuild / compaction)

Spec ejecutable del hito **M7** de `docs/atenea-agent-loop-roadmap.md`. Define el
estado final, el alcance, el plan TDD y los criterios de aceptacion para endurecer
el turno de M5/M6 con las **senales de control internas**: `errRebuildTurn` y
`errContinueAfterCompaction` (sentinels chequeados con `errors.Is`) y el
`ContextEpoch`. El turno feliz de M5 (`runTurn`) pasa a ser un **intento**
(`runTurnAttempt`) envuelto en un retry loop: el attempt snapshotea el epoch al
empezar a preparar el request, lo re-lee justo antes de llamar al proveedor y, si
cambio (agente, modelo o revision), descarta el request y reconstruye **sin** haber
llamado a `Stream`. Si el request excede el contexto antes de empezar el mensaje del
asistente, compacta el historial y reintenta una vez por la ruta post-compaction. La
regla central: **ningun request se ejecuta representando estado viejo de la sesion**.

Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos, igual que el resto de `docs/`.

## 1. Contexto

Los hitos previos dejaron, de adentro hacia afuera, todas las piezas que el turno
ensambla y que el loop externo orquesta:

- **M1** dejo el dominio durable (`Seq`, `Message`, `Role`, `SessionEvent`,
  `Store`, `MemoryStore`): el log de eventos es la unica fuente de verdad y los
  mensajes son una proyeccion derivada (`Store.Messages(sinceSeq)`).
- **M2** dejo la frontera con el modelo (`llm.Provider`, `llm.Request`,
  `llm.Event`, `llm.EventKind`, `llm.Usage`) y un `FakeProvider` replayable que
  reproduce un guion determinista de eventos por un channel y lo cierra al terminar.
- **M3** dejo el `Publisher` (`internal/session/runner/publish.go`): traduce cada
  `llm.Event` a un `SessionEvent` durable, bufferiza los deltas y mantiene el mapa
  `callID -> toolName` del turno.
- **M4** dejo el `tool.Registry` (`Materialize(perms) -> {Definitions, Settle}`),
  los tipos del contrato y el builtin `Echo`.
- **M5** dejo el **turno** (`internal/session/runner/turn.go`): `runTurn(ctx,
  sessionID) (bool, error)` arma el `llm.Request` desde el historial proyectado,
  llama `Provider.Stream` **una vez**, consume el stream con el `Publisher`, asienta
  las tool calls locales concurrentemente con `errgroup` y devuelve
  `needsContinuation`. M5 dejo `runTurn` como **un solo intento feliz**, anticipando
  que M7 lo envolveria con el retry de senales de control.
- **M6** dejo el **loop externo** (`internal/session/runner/run.go`): el `Inbox`
  durable (queue/steer, `internal/session/inbox.go`) y el doble loop (actividad +
  pasos, `MaxSteps = 25`). `Run` promueve el input pendiente a `Message{Role: user}`
  con el helper `promote` (un **pre-paso** antes de `runTurn`), ejecuta el turno y
  decide la continuacion (tool local o steer, nunca texto). M6 dejo explicito que
  plegar la promocion dentro del attempt y agregar las senales de control era de M7.

El siguiente ladrillo son las **senales de control** y el `ContextEpoch`. Su
responsabilidad (ver `docs/atenea-agent-loop.md`, "Que hace un turno (`runTurn`)" y
"Senales de control via errores", y `docs/opencode-agent-loop.md`, "Transiciones
internas") es proteger al loop de ejecutar un request que ya no representa el estado
real de la sesion:

- **snapshotear** el contexto (`ContextEpoch`) al empezar a preparar el turno;
- **re-chequear** el epoch justo antes de llamar al proveedor: si el agente, el
  modelo o la revision cambiaron entre preparar y llamar, el request quedo viejo;
  se descarta y se reconstruye desde el store (`errRebuildTurn`), **sin** haber
  llamado `Stream`;
- **compactar** ante overflow: si el request excede el contexto antes de arrancar el
  mensaje del asistente, se compacta el historial y se reintenta una vez por la ruta
  post-compaction (`errContinueAfterCompaction`).

M7 construye y prueba esto **contra fakes**: el `ContextEpoch` lo expone el `Store`
(`MemoryStore` devuelve un epoch estable, asi el camino feliz de M5/M6 no
reconstruye); el cambio concurrente y el overflow se simulan con decoradores de
test (un `Store` que devuelve epochs distintos entre lecturas y un `Compactor`
fake). No toca Wails, ni el proveedor real, ni la interrupcion/fallos de M8.

## 2. Objetivo

Dejar listo el turno con senales de control y el epoch que necesita:

En `internal/session` (la foto del contexto):

- el tipo `ContextEpoch` (`Agent`, `Model string`; `BaselineSeq Seq`; `Revision
  int`): la foto que el runner compara para detectar cambios concurrentes
  (`internal/session/epoch.go`, nuevo);
- el metodo `Epoch(ctx, sessionID) (ContextEpoch, error)` en la interface `Store` y
  su implementacion en `MemoryStore` (devuelve un epoch estable, todo en cero).

En `internal/session/runner`:

- los sentinels `errRebuildTurn` y `errContinueAfterCompaction` (no exportados:
  control de flujo interno del paquete, chequeados con `errors.Is`);
- `runTurn(ctx, sessionID) (bool, error)` reescrito como **retry loop** sobre
  `runTurnAttempt`: traga las senales y reintenta; cualquier otro error (o el exito)
  se devuelve;
- `runTurnAttempt(ctx, sessionID) (bool, error)`: el intento del turno, que es el
  cuerpo de M5 mas el snapshot/recheck del epoch, la resolucion del modelo desde el
  epoch y el chequeo de compaction;
- la interface `Compactor` (`NeedsCompaction(req) bool`, `Compact(ctx, sessionID)
  error`) y un campo `compactor Compactor` **opcional** (nil) en el `Runner`: nil
  significa "nunca compacta" (camino feliz de M5/M6); los tests lo inyectan
  white-box (mismo paquete);
- tests de comportamiento que ejercen el rebuild (cambio de agente/modelo y mismatch
  de revision) y la compaction.

M7 **no** cambia `consume`, ni el `Publisher`, ni el `Inbox`, ni el cuerpo de `Run`
(la promocion sigue siendo el pre-paso de M6), ni agrega interrupcion por `ctx`,
`Step.Failed`, `failInterruptedTools` ni toca Wails.

## 3. Alcance

### Dentro

- `internal/session/epoch.go` (nuevo): el tipo `ContextEpoch`.
- `internal/session/store.go`: el metodo `Epoch` en la interface `Store`.
- `internal/session/memstore.go`: `MemoryStore.Epoch` (epoch estable en cero).
- `internal/session/runner/turn.go`: los sentinels `errRebuildTurn`/
  `errContinueAfterCompaction`, el retry loop `runTurn` y el nuevo `runTurnAttempt`
  (que reemplaza el cuerpo del `runTurn` de M5). `consume`/`toLLMMessages` intactos.
- `internal/session/runner/runner.go`: la interface `Compactor` y el campo opcional
  `compactor` (aditivo; `NewRunner` **no** cambia de firma: el campo queda nil).
- Tests de comportamiento en `internal/session/runner/turn_signals_test.go` (nuevo):
  rebuild por cambio de epoch y compaction por overflow, con sus decoradores de
  test. Si conviene, un test minimo del contrato de `MemoryStore.Epoch` en
  `internal/session/memstore_test.go`.
- Actualizar `internal/session/runner/doc.go` (las senales de control aterrizaron) y
  `internal/session/doc.go` (el `ContextEpoch` aterrizo).

### Fuera (no hacer en M7)

- La **fuente real** del epoch (reconciliacion de archivos/instrucciones, watch del
  workspace, seleccion real de agente, avance real del `BaselineSeq`): en M7
  `MemoryStore.Epoch` devuelve un epoch estable en cero; lo que mueve el epoch en los
  tests son **decoradores**. El driver real del epoch (con agente/modelo de config y
  revision que cambia por contexto) llega con el store real y la app — **M10/M9**.
- `Request.System []Part` (prompt del agente + baseline de contexto) y
  `Request.ProviderOpts` (prompt cache key): M6 los referencio como "M7", pero
  poblar campos que nadie lee viola el criterio del plan (M5/M6 no agregaron campos
  sin un consumidor). El turno con senales de control **no** los necesita: el epoch y
  el `BaselineSeq` ya cubren "no correr con estado viejo". `System`/`ProviderOpts`
  llegan cuando exista una **fuente real de system context**, junto al adaptador real
  — **M10**.
- Plegar la **promocion** dentro de `runTurnAttempt` (paso 5 de `runTurnAttempt` en
  la arquitectura). M6 dejo `promote` como pre-paso de `Run` y M7 lo conserva: el
  retry de `errContinueAfterCompaction` reintenta el attempt **sin** re-promover (la
  promocion ya ocurrio en `Run`, una vez), asi que no necesita el parametro
  `promotion` ni re-setearlo. Ver "Riesgos y decisiones".
- Interrupcion por `ctx` cancelado a mitad de turno, errores del stream del
  proveedor, `Step.Failed`, tools que el proveedor marco ejecutadas pero nunca
  resolvio, y `failInterruptedTools` (limpieza tras crash) — **M8**.
- `EventBus`, `runtime.EventsEmit`, `app.go`, `main.go`, Wails, frontend — **M9**.
- `Store` SQLite, `Inbox` persistente y el adaptador `Provider` real — **M10**. Los
  tests de M1..M7 deben seguir verdes con el store real.

## 4. Agregados al contrato (`session`)

### `internal/session/epoch.go` (nuevo)

La foto del contexto que el runner compara para detectar cambios concurrentes entre
preparar un turno y llamarlo. Se modela como un valor comparable (`==`/`!=`): el
attempt lo snapshotea al empezar y lo re-lee antes de `Stream`; cualquier diferencia
fuerza la reconstruccion.

```go
package session

// ContextEpoch es una foto del contexto de la sesion que el runner usa para
// detectar cambios concurrentes entre preparar un turno y llamar al proveedor.
// Agent y Model identifican la configuracion activa del turno (el modelo del epoch
// es el que el runner pone en el Request). Revision se incrementa cuando el contexto
// cambia de una forma que invalida un request ya preparado (cambio de agente/modelo,
// reconciliacion de archivos/instrucciones). BaselineSeq marca desde donde cuenta el
// historial proyectado del turno (Store.Messages lee con sinceSeq = BaselineSeq):
// una compaction futura lo avanza para dejar fuera lo ya resumido.
//
// Es comparable a proposito (solo campos comparables): el runner decide el rebuild
// con un simple after != before. M7 lo usa minimo: MemoryStore.Epoch devuelve un
// epoch estable en cero, asi el camino feliz no reconstruye nunca; el driver real
// (que mueve Agent/Model/Revision/BaselineSeq por cambios de contexto) llega con el
// store real (M10).
type ContextEpoch struct {
	Agent       string
	Model       string
	BaselineSeq Seq
	Revision    int
}
```

### `internal/session/store.go` (metodo nuevo en la interface)

El epoch se lee del `Store`, igual que el historial: es estado durable de la sesion.
`Run` reconstruye el request del store en cada turno, asi que el epoch tambien sale
de ahi (nunca de estado vivo entre turnos).

```go
type Store interface {
	AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error)
	LoadSession(ctx context.Context, sessionID string) (Session, error)
	Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error)

	// Epoch devuelve la foto del contexto vigente de la sesion. El runner la
	// snapshotea al preparar un turno y la re-lee antes de llamar al proveedor: si
	// cambio, descarta el request y reconstruye. ErrSessionNotFound si la sesion no
	// existe.
	Epoch(ctx context.Context, sessionID string) (ContextEpoch, error)
}
```

### `internal/session/memstore.go` (implementacion estable)

`MemoryStore` devuelve un epoch estable en cero: el camino feliz de M5/M6 snapshotea
y re-lee el mismo valor, asi que nunca reconstruye y el comportamiento previo no
cambia. El epoch que de verdad evoluciona (agente/modelo/revision) lo aporta el
store real o un decorador de test.

```go
// Epoch devuelve la foto del contexto de la sesion. M7 no tiene aun una fuente real
// de contexto (agente/modelo de config, reconciliacion de archivos), asi que
// MemoryStore devuelve el epoch cero estable: snapshot y recheck coinciden y el
// runner no reconstruye. ErrSessionNotFound si la sesion no existe. El driver real
// del epoch llega en M10.
func (s *MemoryStore) Epoch(ctx context.Context, sessionID string) (ContextEpoch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[sessionID]; !ok {
		return ContextEpoch{}, ErrSessionNotFound
	}
	return ContextEpoch{}, nil
}
```

## 5. El turno con senales de control (`runner`)

### `internal/session/runner/runner.go` (Compactor opcional, aditivo)

El `Runner` gana un colaborador **opcional**: el `Compactor`. nil significa "nunca
compacta" (M5/M6 corren con nil y no cambian). `NewRunner` **no** cambia de firma: el
campo queda nil; los tests de M7 lo inyectan white-box (mismo paquete `runner`). Un
`Compactor` real y su cableado llegan con la app (M9/M10), cuando exista una nocion
real de "el request excede el contexto".

```go
type Runner struct {
	store     session.Store
	inbox     session.Inbox
	provider  llm.Provider
	registry  *tool.Registry
	perms     tool.Permissions
	nextID    func() string
	compactor Compactor // opcional; nil = nunca compacta (camino feliz de M5/M6)
}

// Compactor decide si un Request excede el contexto del modelo y, si pasa, compacta
// el historial durable de la sesion para que el siguiente intento entre. nil en el
// Runner significa "nunca compacta". M7 lo inyecta con un fake en tests; el real
// (que mide tokens contra el limite del modelo y resume el historial) llega en M10.
type Compactor interface {
	// NeedsCompaction informa si req excede el contexto y hay que compactar antes de
	// llamar al proveedor.
	NeedsCompaction(req llm.Request) bool
	// Compact reduce el historial durable de la sesion (resumen/baseline) para que el
	// siguiente intento arme un request que entre. Debe hacer progreso: tras Compact,
	// NeedsCompaction del nuevo request debe terminar siendo false.
	Compact(ctx context.Context, sessionID string) error
}
```

`NewRunner` queda igual que M6 (seis parametros); el `compactor` no se cablea ahi.

### `internal/session/runner/turn.go` (sentinels + retry loop + attempt)

```go
package runner

import (
	"context"
	"errors"

	"golang.org/x/sync/errgroup"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// errRebuildTurn y errContinueAfterCompaction son senales de control internas del
// turno: nunca escapan de runTurn (el retry loop las traga). En vez de excepciones,
// Go usa sentinels envueltos y errors.Is.
//
//   - errRebuildTurn: el contexto cambio mientras se preparaba el turno (cambio de
//     agente/modelo o mismatch de la revision del epoch). El request quedo viejo: se
//     descarta y se reconstruye desde el store, SIN haber llamado al proveedor.
//   - errContinueAfterCompaction: hubo overflow de contexto antes de empezar el
//     mensaje del asistente. Se compacto el historial y se reintenta una vez por la
//     ruta post-compaction.
var (
	errRebuildTurn             = errors.New("rebuild prepared turn")
	errContinueAfterCompaction = errors.New("continue after overflow compaction")
)

// runTurn ejecuta un turno reintentando ante las senales de control internas. El
// cuerpo del turno vive en runTurnAttempt; runTurn solo decide que hacer con su
// resultado: ante errRebuildTurn o errContinueAfterCompaction reintenta (el attempt
// se reconstruye desde el store, ya con el epoch reconciliado o el historial
// compactado); cualquier otro error, o el exito, se devuelve. La terminacion la
// garantiza el contrato de cada senal: el rebuild solo se dispara mientras el epoch
// siga cambiando (se estabiliza) y la compaction debe hacer progreso (el request
// deja de exceder el contexto).
func (r *Runner) runTurn(ctx context.Context, sessionID string) (bool, error) {
	for {
		cont, err := r.runTurnAttempt(ctx, sessionID)
		switch {
		case errors.Is(err, errRebuildTurn):
			continue // algo cambio mientras se preparaba: reconstruir desde el store
		case errors.Is(err, errContinueAfterCompaction):
			continue // hubo overflow: se compacto, reintentar por la ruta post-compaction
		default:
			return cont, err
		}
	}
}

// runTurnAttempt es UN intento del turno. Snapshotea el epoch al empezar a preparar,
// arma el Request desde el historial proyectado (a partir del BaselineSeq del epoch)
// y las tools materializadas, resolviendo el modelo del epoch. Si el request excede
// el contexto, compacta y devuelve errContinueAfterCompaction. Re-lee el epoch antes
// de llamar al proveedor: si cambio (agente/modelo/revision), devuelve errRebuildTurn
// SIN llamar a Stream. Si el epoch sigue vigente, llama Stream UNA vez y consume el
// stream igual que M5. Devuelve needsContinuation.
func (r *Runner) runTurnAttempt(ctx context.Context, sessionID string) (bool, error) {
	// Snapshot del contexto al empezar la preparacion.
	before, err := r.store.Epoch(ctx, sessionID)
	if err != nil {
		return false, err
	}

	// Historial proyectado desde el baseline del epoch y tools materializadas.
	msgs, err := r.store.Messages(ctx, sessionID, before.BaselineSeq)
	if err != nil {
		return false, err
	}
	mat := r.registry.Materialize(r.perms)
	req := llm.Request{Model: before.Model, Messages: toLLMMessages(msgs), Tools: mat.Definitions}

	// Overflow antes del mensaje del asistente: compactar y reintentar una vez.
	if r.compactor != nil && r.compactor.NeedsCompaction(req) {
		if err := r.compactor.Compact(ctx, sessionID); err != nil {
			return false, err
		}
		return false, errContinueAfterCompaction
	}

	// Re-leer el epoch antes de llamar al proveedor: si cambio, el request quedo
	// viejo. Se descarta y se reconstruye SIN haber llamado a Stream.
	after, err := r.store.Epoch(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if after != before {
		return false, errRebuildTurn
	}

	// Epoch vigente: una sola llamada al proveedor y consumo del stream (M5).
	in, err := r.provider.Stream(ctx, req)
	if err != nil {
		return false, err
	}
	pub := NewPublisher(r.store, sessionID, r.nextID())
	return r.consume(ctx, in, pub, mat.Settle)
}
```

`consume` y `toLLMMessages` no cambian respecto a M5.

Notas:

- **Snapshot/recheck del epoch alrededor de la preparacion.** El attempt lee el
  epoch al principio (`before`) y lo re-lee justo antes de `Stream` (`after`). Toda
  la preparacion (leer historial, materializar tools, armar request, decidir
  compaction) ocurre entre las dos lecturas: es la ventana donde un cambio
  concurrente de contexto puede invalidar el request. `after != before` lo detecta y
  corta antes del efecto (la llamada al proveedor).
- **El proveedor nunca ve un request viejo.** El `errRebuildTurn` se devuelve
  **antes** de `provider.Stream`. Un request que representa estado viejo se descarta;
  el retry lo reconstruye con el epoch ya estable. Ese es el criterio "Done when" del
  hito.
- **El modelo sale del epoch.** `req.Model = before.Model`. Asi el rebuild se
  observa: tras un cambio de modelo, el request reconstruido lleva el modelo nuevo
  (el viejo nunca se streamea). `MemoryStore` devuelve `Model: ""`, igual que el
  request de M5/M6 (que no seteaba `Model`): el camino feliz no cambia.
- **Compaction antes del recheck.** El chequeo de overflow va despues de armar el
  request y antes del recheck del epoch (paso 11 de la arquitectura: compactar y
  reconstruir). `Compact` modifica el historial durable; el retry re-lee el historial
  compactado y arma un request que entra. El `BaselineSeq` del epoch es donde un
  compactor real dejaria el corte; en M7 el fake no lo avanza (re-lee todo el
  historial, incluido un marcador de compaction) y `NeedsCompaction` deja de pedir
  compaction tras el primer `Compact`.
- **`compactor` nil = camino feliz.** Sin compactor, el bloque de overflow se salta:
  `runTurnAttempt` queda igual que el `runTurn` de M5 mas el snapshot/recheck del
  epoch (que con `MemoryStore` siempre coincide). M5/M6 verdes sin tocar sus tests.
- **Las senales no escapan.** `runTurn` traga `errRebuildTurn`/
  `errContinueAfterCompaction` y reintenta. `Run` (M6) sigue viendo solo
  `needsContinuation` o un error duro; su cuerpo no cambia.

## 6. Semantica del turno con senales de control

El contrato que M7 fija (y que M8/M9/M10 conservan):

- **Camino feliz intacto.** Con `MemoryStore` (epoch estable) y `compactor` nil,
  `runTurn` snapshotea y re-lee el mismo epoch, no reconstruye, llama `Stream` una
  vez y se comporta exactamente como M5/M6. Toda la suite previa sigue verde.
- **Rebuild por cambio de agente/modelo.** Si entre el snapshot y el recheck el epoch
  cambia de agente o modelo, el attempt devuelve `errRebuildTurn` **antes** de
  `Stream`; `runTurn` reintenta. El request que llega al proveedor refleja el estado
  **nuevo** (p.ej. el modelo nuevo), y el viejo nunca se streamea. Se ejecuta una
  sola vez (un solo turno en la proyeccion).
- **Rebuild por mismatch de revision.** Igual que el anterior pero el campo que
  cambia es `Revision`: cualquier diferencia del epoch (no solo agente/modelo) fuerza
  el rebuild. Es el chequeo "el context epoch sigue vigente" de la arquitectura.
- **Compaction por overflow.** Si el request excede el contexto, el attempt llama
  `Compact` (que reduce el historial durable) y devuelve
  `errContinueAfterCompaction`; `runTurn` reintenta una vez por la ruta
  post-compaction. El segundo attempt arma un request que entra y lo streamea. La
  compaction corre **una sola vez** y el turno produce su mensaje del asistente.
- **Reconstruccion desde el store.** Cada attempt relee epoch e historial del
  `Store`: el rebuild y la post-compaction no arrastran estado vivo del intento
  anterior.
- **Error duro corta el turno.** Si `Epoch`/`Messages`/`Compact`/`Stream` o cualquier
  `AppendEvent` devuelve un error que **no** es una senal de control, `runTurnAttempt`
  lo propaga y `runTurn` lo devuelve sin reintentar.

## 7. Plan TDD

### Safety net

- Estado base verde antes de tocar nada. M7 agrega un tipo y un metodo de interface
  en `session` (`ContextEpoch`, `Store.Epoch`), reescribe `turn.go` (retry loop +
  attempt) y suma un campo opcional al `Runner`; primero se corre lo existente
  (M0..M6).
- Comando: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Resultado esperado: pasan limpio. Al sumar `Epoch` a la interface `Store`, el unico
  implementador real es `MemoryStore` (y `recordingStore` de `turn_test.go`, que lo
  **embebe** y hereda el metodo); los fakes del `Publisher`
  (`recordingAppender`/`failingAppender`) implementan solo `eventAppender` (una
  interface de un metodo), no `Store`, asi que **no** se rompen. Tras agregar
  `MemoryStore.Epoch` se re-corre `go build ./...` y `go test ./internal/session/...`
  para confirmar que M1..M6 siguen verdes.

### Understand

- Leer la entrada M7 del roadmap; "Que hace un turno (`runTurn`)", "Senales de
  control via errores" y la lista numerada de `runTurnAttempt` de
  `docs/atenea-agent-loop.md`; "Transiciones internas" y "Context epoch" de
  `docs/opencode-agent-loop.md`; y el contrato de M5 (`runTurn` devolviendo
  `needsContinuation`) y M6 (`Run` con `promote` como pre-paso).
- Comportamiento esperado: snapshotear el epoch, re-chequearlo antes de `Stream`,
  reconstruir ante cambio (sin streamear viejo), compactar ante overflow y reintentar
  una vez.

### RED

- Escribir primero el test que falla:
  `TestRunner_RebuildsTurnWhenModelChangesBeforeStream`. Referencia simbolos que aun
  NO existen: `session.ContextEpoch` y el chequeo del epoch en el runner. En Go eso es
  un fallo de compilacion del paquete de test -> RED honesto (igual que M5/M6).
- El test usa un decorador `epochFlipStore` (embebe `*session.MemoryStore` y
  sobre-escribe `Epoch`): devuelve `before = ContextEpoch{Model: "viejo"}` en su
  **primera** llamada y `after = ContextEpoch{Model: "nuevo"}` en las siguientes. Asi,
  en el primer attempt el snapshot ve `"viejo"` y el recheck ve `"nuevo"` (mismatch
  -> rebuild); en el segundo attempt ambas lecturas ven `"nuevo"` (coinciden ->
  streamea). Siembra un mensaje de usuario, arma un `recordingProvider` (captura el
  `Request`) con un guion de solo texto (`StepStarted, TextStarted, TextDelta("ok"),
  TextEnded, StepEnded`), un `Registry` con `Echo` y el `Runner` con `idCounter()`.
  Afirma:
  - `cont, err := r.runTurn(ctx, "s1")` devuelve `err == nil`, `cont == false`;
  - `prov.captured().Model == "nuevo"`: el request streameado lleva el modelo nuevo
    (el viejo se descarto antes de `Stream`);
  - la proyeccion tiene **un solo** mensaje de asistente (`Text == "ok"`): el rebuild
    descarto el primer attempt antes de streamear, no encadeno dos turnos.
- Comando:
  `go test -run TestRunner_RebuildsTurnWhenModelChangesBeforeStream ./internal/session/runner`
  -> fallo esperado.

### GREEN

- Escribir el minimo: `internal/session/epoch.go` (`ContextEpoch`); `Store.Epoch` en
  la interface y `MemoryStore.Epoch` (epoch cero estable); los sentinels y el retry
  loop `runTurn` + `runTurnAttempt` en `turn.go` (snapshot/recheck del epoch,
  `req.Model` del epoch); el campo `compactor` y la interface `Compactor` en
  `runner.go`.
- Correr solo el test rojo hasta verde.
- Comando:
  `go test -run TestRunner_RebuildsTurnWhenModelChangesBeforeStream ./internal/session/runner`.

### TRIANGULATE

Agregar casos para evitar falso verde (los del roadmap):

- `TestRunner_RebuildsTurnWhenEpochRevisionChanges`: mismo `epochFlipStore` pero
  `before = {Revision: 1}` y `after = {Revision: 2}` (mismo modelo). Afirma que
  `runTurn` corre sin error, que el provider se llamo **una** vez (un solo mensaje de
  asistente en la proyeccion) y, si se quiere fijar la senal, que un
  `runTurnAttempt` directo devuelve un error que `errors.Is(err, errRebuildTurn)`
  reconoce (test white-box del attempt aislado). Verifica "mismatch de epoch fuerza
  rebuild" por un campo distinto de agente/modelo.
- `TestRunner_CompactsAndRetriesOnceWhenRequestOverflows`: `MemoryStore` plano (epoch
  estable, sin rebuild) y un `Compactor` fake inyectado white-box (`r.compactor =
  &fakeCompactor{store: store}`). El fake: `NeedsCompaction` devuelve `true` hasta
  que `Compact` corre una vez (luego `false`); `Compact` marca `compacted = true`,
  incrementa un contador y apendea un `Message{Role: system, Text: "[compactado]"}`
  al store. Siembra un usuario, guion de solo texto. Afirma:
  - `runTurn` devuelve `err == nil`, `cont == false`;
  - el contador de `Compact` quedo en **1** (compacto una sola vez);
  - el provider se llamo **una** vez (un mensaje de asistente);
  - la proyeccion contiene el `Message{Role: system, Text: "[compactado]"}` (la
    compaction corrio antes del turno que streameo).
- `TestRunner_HappyPathDoesNotRebuildOrCompact` (regresion explicita): `MemoryStore`
  + `compactor` nil + un `recordingProvider`. Afirma que `runTurn` streamea una vez,
  que `captured().Model == ""` (epoch cero) y que la proyeccion es la esperada
  (usuario + asistente). Ancla que el camino feliz no dispara ninguna senal.
- (Contrato del epoch) en `internal/session/memstore_test.go`,
  `TestMemoryStore_EpochIsStableAndNotFound`: `Epoch` de una sesion inexistente
  devuelve `ErrSessionNotFound`; tras un `AppendEvent`, dos `Epoch` consecutivos
  devuelven el **mismo** valor (estable, sin rebuild espurio). Fija el contrato que
  el runner asume del store.
- Comandos:
  - `go test -run 'TestRunner_Rebuilds|TestRunner_Compacts|TestRunner_HappyPath|TestMemoryStore_Epoch' ./internal/session/...`
  - `go test -race -run TestRunner ./internal/session/runner` (el turno sigue
    asentando tools con `errgroup`/candado del `Publisher`; los decoradores de epoch
    y el compactor usan mutex)

### REFACTOR

- Limpieza sin cambiar comportamiento: factorizar los decoradores de test
  (`epochFlipStore`, `fakeCompactor`) si reduce duplicacion; reutilizar
  `seedUser`/`recordingProvider`/`idCounter` de los tests de M5/M6 donde aplique.
  Actualizar `internal/session/runner/doc.go` (las senales de control
  `errRebuildTurn`/`errContinueAfterCompaction` y el snapshot/recheck del epoch
  aterrizaron en M7; la interrupcion y los fallos siguen en M8) y
  `internal/session/doc.go` (el `ContextEpoch` aterrizo: la foto del contexto que el
  runner compara; el driver real del epoch sigue pendiente).
- Verificar que la suite sigue verde tras el formateo.
- Comando: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Criterios de aceptacion (Done when)

1. Existe `session.ContextEpoch` (`Agent`, `Model`, `BaselineSeq`, `Revision`),
   comparable con `==`/`!=`.
2. La interface `Store` tiene `Epoch(ctx, sessionID) (ContextEpoch, error)` y
   `MemoryStore` lo implementa devolviendo un epoch estable en cero
   (`ErrSessionNotFound` si la sesion no existe); M1..M6 siguen verdes.
3. Existen los sentinels `errRebuildTurn` y `errContinueAfterCompaction` (no
   exportados), chequeados con `errors.Is` dentro del paquete.
4. `runTurn` es un retry loop sobre `runTurnAttempt`: traga las dos senales y
   reintenta; cualquier otro error o el exito se devuelven. `consume`/`toLLMMessages`
   no cambiaron.
5. `runTurnAttempt` snapshotea el epoch al empezar, arma el `Request` (modelo desde
   el epoch, historial desde `BaselineSeq`) y re-lee el epoch antes de `Stream`; si
   `after != before` devuelve `errRebuildTurn` **sin** llamar a `Stream`.
6. Un cambio de agente/modelo entre snapshot y recheck reconstruye el turno: el
   request streameado refleja el estado nuevo y el viejo nunca llega al proveedor
   (verificado capturando el `Request` y contando un solo turno).
7. Un mismatch de `Revision` del epoch tambien fuerza el rebuild (mismo
   `errRebuildTurn`).
8. Existe la interface `Compactor` y el campo opcional `compactor` (nil = nunca
   compacta); `NewRunner` no cambio de firma. Un request que excede el contexto hace
   que `runTurnAttempt` compacte (una vez) y devuelva `errContinueAfterCompaction`;
   `runTurn` reintenta y streamea el request compactado.
9. El camino feliz (`MemoryStore` + `compactor` nil) no dispara ninguna senal: el
   turno se comporta como M5/M6 (M5 `turn_test.go` y M6 `run_test.go` verdes sin
   modificar).
10. `go test ./...` (y `-race` donde aplica) pasa; `go vet ./...` limpio;
    `gofmt -l .` vacio.
11. No hubo cambios en `app.go`, `main.go`, Wails, el frontend ni `internal/event`.
    En `internal/llm` no se toco nada (`Request.System`/`ProviderOpts` siguen
    diferidos). En `internal/session` solo se agrego `epoch.go`, el metodo `Epoch`
    en `store.go`/`memstore.go` y `doc.go`; `session.go`, `event.go`, `inbox.go`
    intactos. En `runner` solo crecieron `turn.go` (retry loop + attempt + sentinels)
    y `runner.go` (interface `Compactor` + campo) y `doc.go`; `publish.go`/`run.go`
    intactos.

## 9. Comandos

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Tras sumar Epoch a la interface Store, confirmar que compila y que M1..M6 siguen verdes
go build ./...
go test ./internal/session/...

# Ciclo (test especifico primero)
go test -run TestRunner_RebuildsTurnWhenModelChangesBeforeStream ./internal/session/runner

# Triangulacion (rebuild por revision, compaction, camino feliz, contrato del epoch)
go test -run 'TestRunner_Rebuilds|TestRunner_Compacts|TestRunner_HappyPath|TestMemoryStore_Epoch' ./internal/session/...

# Concurrencia real (tools asentadas en errgroup + candado; decoradores con mutex)
go test -race -run TestRunner ./internal/session/runner

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
```

## 10. Tabla de evidencia esperada

Al cerrar M7, la respuesta/PR debe incluir esta tabla con resultados reales:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0..M6 verde antes de editar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Contrato de las senales de control leido | roadmap M7, `docs/atenea-agent-loop.md` (runTurn/senales), `docs/opencode-agent-loop.md` (transiciones internas, epoch) | comportamiento identificado |
| RED | Test de rebuild por cambio de modelo escrito primero | `turn_signals_test.go` + `go test -run TestRunner_RebuildsTurnWhenModelChangesBeforeStream ./internal/session/runner` | fallo esperado (no compila) |
| GREEN | `epoch.go` + `Store.Epoch` + retry loop/attempt + `Compactor` minimos | `internal/session/epoch.go`, `internal/session/{store,memstore}.go`, `internal/session/runner/{turn,runner}.go` | test especifico pasa |
| TRIANGULATE | Rebuild por revision, compaction una vez, camino feliz, contrato del epoch | `go test -run 'TestRunner_Rebuilds|TestRunner_Compacts|TestRunner_HappyPath|TestMemoryStore_Epoch' ./internal/session/...`, `go test -race ...` | casos pasan, `-race` limpio |
| REFACTOR | decoradores de test, `doc.go` actualizados | `gofmt -w internal`, `go vet ./...`, `go test ./...` | suite verde, M1..M6 intactos |

## 11. Riesgos y decisiones

- **`runTurn` como retry loop, `runTurnAttempt` como cuerpo.** M5 dejo `runTurn` como
  "un solo intento feliz" anticipando exactamente este wrapper. M7 mueve el cuerpo de
  M5 a `runTurnAttempt` (mas el snapshot/recheck del epoch) y deja `runTurn` como el
  loop que traga las senales. La firma `runTurn(ctx, sessionID) (bool, error)` no
  cambia, asi que M6 (`Run` lo llama) y los tests de M5 (lo llaman directo) siguen
  verdes sin tocarse.
- **El epoch se compara entero (`after != before`), unificando los dos chequeos de la
  arquitectura.** La arquitectura separa el chequeo de agente/modelo (paso 6,
  re-leer la sesion) del chequeo de revision del epoch (paso 13). M7 los unifica: el
  `ContextEpoch` lleva `Agent`, `Model` y `Revision`, y cualquier diferencia entre
  snapshot y recheck dispara el mismo `errRebuildTurn`. Es mas simple y igual de
  fiel: el efecto observable (no streamear un request viejo) es identico, y un solo
  punto de comparacion evita duplicar logica. Por eso `Session` **no** crece con
  `Agent`/`Model` en M7: el epoch es el portador del contexto del turno.
- **La promocion sigue siendo el pre-paso de `Run` (decision de M6 conservada).** La
  arquitectura promueve dentro de `runTurnAttempt` (paso 5) y el retry de
  `errContinueAfterCompaction` re-setea `promotion = steer`. M6 eligio dejar
  `promote` como un pre-paso fino en `Run` (una sola promocion por paso, antes de
  `runTurn`). M7 lo conserva: como la promocion ya ocurrio en `Run`, el retry del
  attempt **no** re-promueve (no duplica el prompt del usuario), asi que no necesita
  el parametro `promotion` ni re-setearlo. Se evita reescribir `Run` (M6 verde sin
  cambios) y se mantiene el mismo efecto que la ruta post-compaction de la
  arquitectura (no re-consumir el queue tras compactar).
- **`MemoryStore.Epoch` estable; el cambio se simula con decoradores.** No hay aun una
  fuente real de contexto (agente/modelo de config, watch de archivos), asi que
  `MemoryStore` devuelve el epoch cero estable: el camino feliz snapshotea y re-lee lo
  mismo y nunca reconstruye. El cambio concurrente se prueba con un `epochFlipStore`
  de test (devuelve `before` la primera vez y `after` despues), igual que M6 simulo el
  steering concurrente con un `steeringProvider`. Asi no se inventa un driver de epoch
  sin un consumidor real; ese driver llega en M10 con el store real.
- **`Compactor` opcional (nil) en vez de parametro de `NewRunner`.** A diferencia del
  `inbox` de M6 (que `Run` siempre necesita y por eso entro al constructor), la
  compaction es un colaborador **opcional**: la mayoria de los turnos no compactan y
  no hay aun una nocion real de "el request excede el contexto". Modelarlo como un
  campo nil-able (no como parametro obligatorio) evita tocar las ~8 construcciones de
  `NewRunner` en los tests de M5/M6 con un `nil` ruidoso, y deja que los tests de M7
  lo inyecten white-box (mismo paquete `runner`). Un `Compactor` real y su cableado en
  `NewRunner`/`app.go` llegan cuando exista la medicion real de tokens (M10). Es el
  mismo criterio "fakes deterministas, lo real al final" del plan.
- **`req.Model` desde el epoch.** Resolver el modelo del epoch (en vez de dejarlo en
  cero como M5/M6) es lo que hace **observable** el rebuild: tras un cambio de modelo,
  el request reconstruido lleva el modelo nuevo y el test lo captura. `MemoryStore`
  devuelve `Model: ""`, identico al request de M5/M6, asi que el camino feliz no
  cambia. Es el paso 7 de `runTurnAttempt` ("resuelve el modelo") en su forma minima.
- **`BaselineSeq` se usa, no se especula.** `runTurnAttempt` lee el historial con
  `Messages(ctx, sessionID, before.BaselineSeq)` en vez de `0`. `MemoryStore` devuelve
  `BaselineSeq: 0`, asi que lee todo el historial igual que M5/M6; pero el campo queda
  **cableado** a su unico consumidor real (el corte del historial proyectado), no como
  un campo muerto. Una compaction real lo avanzaria; el fake de M7 no lo toca (re-lee
  todo, incluido el marcador) y eso alcanza para probar "compacta y reintenta una vez".
- **`Request.System`/`ProviderOpts` siguen diferidos (refinamiento sobre M6).** M6 los
  listo como "M7", pero el turno con senales de control no los ejercita: el epoch y el
  `BaselineSeq` cubren "no correr con estado viejo". Poblar `System`/`ProviderOpts` sin
  una fuente real de system context seria agregar campos que nadie lee, justo lo que
  M5/M6 evitaron. Se difieren a M10 (adaptador real + system context real). Es una
  decision de scope explicita, no un olvido.
- **Terminacion del retry loop por contrato, no por cota dura.** `runTurn` usa un
  `for` sin limite (como la arquitectura). La terminacion la garantiza el contrato de
  cada senal: el rebuild solo se dispara mientras el epoch siga cambiando (un cambio
  concurrente se estabiliza en una o dos vueltas) y `Compact` **debe** hacer progreso
  (tras compactar, el request deja de exceder el contexto). Un decorador/compactor que
  nunca converja colgaria el loop; el `MaxSteps` de `Run` (M6) no cubre este loop
  interno. Se documenta el contrato y se difiere una cota dura (un contador de
  reintentos con su error) a cuando un driver real lo justifique; agregarla ahora seria
  proteccion sin un test que la pida. Los fakes de M7 convergen a proposito (el epoch se
  estabiliza tras la primera vuelta; el compactor pone `NeedsCompaction` en false).
- **Sentinels no exportados.** `errRebuildTurn`/`errContinueAfterCompaction` son
  control de flujo interno del paquete `runner`: nunca escapan de `runTurn`. Quedan no
  exportados, igual que en la arquitectura. Los tests white-box (mismo paquete) pueden
  afirmarlos con `errors.Is` sobre `runTurnAttempt` si se quiere fijar la senal, pero
  el grueso de los tests afirma el **comportamiento observable** (el request refleja el
  estado nuevo, el provider corre una vez, el marcador de compaction aparece), no el
  error interno.

## 12. Fuentes

- Roadmap: `docs/atenea-agent-loop-roadmap.md` (hito M7)
- Arquitectura: `docs/atenea-agent-loop.md` (secciones "Que hace un turno
  (`runTurn`)", la lista numerada de `runTurnAttempt`, "Senales de control via
  errores" y "Persistencia durable" — `Store.Epoch`)
- Loop de referencia: `docs/opencode-agent-loop.md` (secciones "Context epoch",
  "Transiciones internas" y los pasos de `runTurnAttempt`)
- Manera de trabajo: `AGENTS.md`
- Specs previos: `docs/atenea-m1-tipos-store-spec.md`,
  `docs/atenea-m2-provider-fake-spec.md`, `docs/atenea-m3-publisher-spec.md`,
  `docs/atenea-m4-tool-registry-spec.md`, `docs/atenea-m5-run-turn-spec.md`,
  `docs/atenea-m6-run-loop-spec.md`
