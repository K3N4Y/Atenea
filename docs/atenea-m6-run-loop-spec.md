# Spec M6 — Loop externo (`Run`) + MaxSteps

Spec ejecutable del hito **M6** de `docs/atenea-agent-loop-roadmap.md`. Define el
estado final, el alcance, el plan TDD y los criterios de aceptacion para envolver
el turno de M5 (`runTurn`) en el **loop externo** del agente: el `Inbox` durable
(queue/steer) y el doble loop (actividad + pasos) con `MaxSteps = 25`. `Run` drena
el inbox de una sesion hasta dejarla idle: promueve el input pendiente a mensajes
del historial, ejecuta un turno, decide si continuar (solo por tool call local o
por steer pendiente, **nunca** por texto del asistente) y, al cerrar una
actividad, abre otra si quedo un `queue`. Exceder los pasos devuelve
`StepLimitExceededError`.

Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos, igual que el resto de `docs/`.

## 1. Contexto

Los hitos previos dejaron, de adentro hacia afuera, todas las piezas que el loop
externo orquesta (ver el orden `tipos -> store -> provider -> publisher -> tools
-> turno -> loop` en el roadmap):

- **M1** dejo el dominio durable (`Seq`, `Message`, `Role`, `SessionEvent`,
  `Store`, `MemoryStore`): el log de eventos es la unica fuente de verdad y los
  mensajes son una proyeccion derivada (`Store.Messages(sinceSeq)`).
- **M2** dejo la frontera con el modelo (`llm.Provider`, `llm.Request`,
  `llm.Event`, `llm.EventKind`, `llm.Usage`) y un `FakeProvider` que reproduce un
  guion determinista de eventos por un channel y lo cierra al terminar. M2 dejo el
  fake **replayable**: cada `Stream` reproduce el mismo guion (lo necesita el loop
  multi-turno de M6).
- **M3** dejo el `Publisher` (`internal/session/runner/publish.go`): traduce cada
  `llm.Event` a un `SessionEvent` durable, bufferiza los deltas, materializa el
  `Message` coalescido del asistente en `Text.Ended` y mantiene el mapa
  `callID -> toolName` del turno.
- **M4** dejo el `tool.Registry` (`Materialize(perms) -> {Definitions, Settle}`),
  los tipos del contrato (`Tool`, `Call`, `Result`, `SettleFunc`, `Permissions`,
  `Materialized`, `UnknownToolError`), el `OutputStore` y el builtin `Echo`.
- **M5** dejo el **turno** (`internal/session/runner/{runner,turn}.go`): el
  `Runner` (+ `NewRunner`), `runTurn(ctx, sessionID) (bool, error)` que arma el
  `llm.Request` desde el historial proyectado, llama `Provider.Stream` **una vez**,
  consume el stream con el `Publisher`, asienta las tool calls locales
  concurrentemente con `errgroup` y devuelve `needsContinuation`. El candado del
  `Publisher` y `ToolSuccess`/`ToolFailed` tambien aterrizaron en M5.

El siguiente ladrillo es el **loop externo** (`internal/session/runner/run.go`).
Su responsabilidad (ver `docs/atenea-agent-loop.md`, "Loop externo (`Run`)", y el
pseudocodigo de `docs/opencode-agent-loop.md`) es envolver el turno feliz de M5 en
la maquinaria durable que lo hace util:

- **leer** el `Inbox`: si no hay steer ni queue pendiente (y no es `force`), la
  sesion esta idle y `Run` retorna sin hacer nada;
- **promover** el input pendiente: convertir el siguiente `queue` (o todos los
  `steer`) en mensajes `Role: user` del `Store`, para que el proximo `runTurn` los
  vea en el historial;
- **ejecutar** un turno (`runTurn` de M5) y decidir la continuacion: continua si
  el turno hizo una tool call local (`needsContinuation == true`) o si quedo un
  `steer` admitido durante la corrida; **no** continua por texto del asistente;
- **cortar** loops improductivos: como mucho `MaxSteps = 25` pasos por actividad;
  excederlos devuelve `StepLimitExceededError`;
- **abrir nuevas actividades**: al cerrar una (el modelo dejo la sesion estable),
  revisa si hay otro `queue` y, si lo hay, lo procesa como una actividad nueva.

M6 construye y prueba el loop **contra fakes**: el `MemoryInbox` nuevo, el
`MemoryStore`, el `Registry` con `Echo` y proveedores de test (el `FakeProvider`
replayable de M2 y, para los turnos que cambian de guion turno a turno, un
proveedor multi-turno de test). No toca Wails, ni el proveedor real, ni las
senales de control internas de M7.

## 2. Objetivo

Dejar listo el loop externo y el input durable que necesita:

En `internal/session` (input durable de la sesion):

- el tipo `Delivery` (`DeliveryNone`, `DeliveryQueue`, `DeliverySteer`): como entra
  un prompt al runner;
- el tipo `Prompt` (`Text`): el texto que el usuario admite;
- la interface `Inbox` (`Admit`, `HasPending`, `Promote`) y su implementacion en
  memoria `MemoryInbox` (+ `NewMemoryInbox`): mismo patron que `Store`/`MemoryStore`
  (M10 puede agregar una persistente detras de la misma interface).

En `internal/session/runner`:

- la constante `MaxSteps = 25`;
- el tipo `StepLimitExceededError` (`Max int`);
- el campo `inbox session.Inbox` en el `Runner` y el parametro nuevo de
  `NewRunner` (aditivo a la estructura de M5; `runTurn`/`consume` no cambian);
- `Run(ctx, sessionID string, force bool) error`: el loop externo (actividad) +
  el loop de pasos (`MaxSteps`), con la semantica del pseudocodigo;
- el helper `promote(ctx, sessionID, d)`: saca del inbox los prompts de la entrega
  `d` y los materializa como `Message{Role: user}` en el `Store`;
- tests de comportamiento que ejercen `Run` contra el inbox y los proveedores de
  test.

M6 **no** cambia el cuerpo de `runTurn` (sigue siendo el turno feliz aislado de
M5), ni agrega senales de control, ni interrupcion por `ctx`, ni `failInterruptedTools`,
ni `Step.Failed`, ni toca Wails.

## 3. Alcance

### Dentro

- `internal/session/inbox.go` (nuevo): `Delivery` (+ constantes), `Prompt`, la
  interface `Inbox` y `MemoryInbox` (+ `NewMemoryInbox`).
- `internal/session/runner/run.go` (nuevo): `MaxSteps`, `StepLimitExceededError`,
  `Run` y el helper `promote`.
- `internal/session/runner/runner.go`: campo `inbox` en `Runner` y parametro en
  `NewRunner` (aditivo).
- Tests de comportamiento en `internal/session/inbox_test.go` (nuevo, el contrato
  del inbox) y `internal/session/runner/run_test.go` (nuevo, el loop).
- Actualizar la llamada a `NewRunner` en `internal/session/runner/turn_test.go`
  (M5): pasa un `session.NewMemoryInbox()` extra. Cambio **mecanico**; los tests de
  M5 siguen llamando `runTurn` directamente (que ignora el inbox) y su
  comportamiento no cambia.
- Actualizar `internal/session/runner/doc.go` (el `Run` externo aterrizo en M6) y
  `internal/session/doc.go` (el `Inbox` aterrizo: queue/steer durable).

### Fuera (no hacer en M6)

- Las senales de control (`errRebuildTurn`, `errContinueAfterCompaction`), el
  `runTurn` con su retry loop sobre `runTurnAttempt`, el `ContextEpoch`, la
  reseleccion de agente/modelo, el system context/baseline y la compaction por
  overflow — **M7**. En M6 `runTurn` sigue siendo el unico intento feliz de M5; el
  loop solo lo llama en bucle. La promocion de M6 es un **pre-paso fino** en `Run`
  (materializa el prompt y listo); plegarla dentro de `runTurnAttempt` (junto con
  epoch/agente/compaction) es de M7.
- Interrupcion por `ctx` cancelado a mitad de corrida, errores del stream del
  proveedor, `failInterruptedTools` al inicio de `Run` (limpieza tras crash) y el
  cierre de tools colgadas — **M8**. En M6 el loop propaga el error duro que
  devuelvan `runTurn`/`HasPending`/`Promote`/`AppendEvent`, pero no abre los
  caminos de fallo out-of-band ni la limpieza de reanudacion.
- El refinamiento "al promover `queue` tambien se drena `steer` hasta un cutoff"
  (paso 5 de `runTurnAttempt`): en M6 `Promote(queue)` saca **solo** el siguiente
  prompt encolado y `Promote(steer)` drena los steers; la prioridad
  steer-sobre-queue al inicio de `Run` y el `promotion = steer` tras el primer
  turno cubren los escenarios del hito. El cutoff fino (admitted/promoted seq) y el
  tipo `Promotion` rico llegan cuando M7 lo necesite.
- `Request.System`/`ProviderOpts`, el historial proyectado avanzado
  (`EntriesForRunner` con baseline/compaction) — **M7**.
- `EventBus`, `runtime.EventsEmit`, `app.go`, `main.go`, Wails, frontend — **M9**.
  M9 cablea `SendPrompt -> inbox.Admit(queue) + go Run(...)` sin cambiar la logica
  del loop.
- `Store` SQLite y un `Inbox` persistente y el adaptador `Provider` real — **M10**.

## 4. Agregados al contrato (`session`)

### `internal/session/inbox.go` (nuevo)

El input durable de la sesion. Los prompts no entran directo al modelo: primero se
admiten con una `Delivery` y el runner los promueve a mensajes del historial
cuando arma el turno. Se modela como una interface (igual que `Store`) con una
implementacion en memoria; M10 puede agregar una persistente sin tocar el runner.

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

## 5. El loop externo (`runner`)

### `internal/session/runner/runner.go` (campo `inbox`, aditivo)

El `Runner` gana el `Inbox` como dependencia: `Run` lee y promueve el input
durable. `runTurn`/`consume` no cambian. `NewRunner` suma el parametro.

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

### `internal/session/runner/run.go` (nuevo)

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

Notas:

- **Un solo `runTurn` por paso.** El loop llama `runTurn` (el turno feliz de M5)
  una vez por paso. `runTurn` por dentro llama `Provider.Stream` una vez. El
  proveedor produce un turno, no toda la sesion; el loop decide si invocar otro.
- **Promover antes de leer el historial.** `promote` corre **antes** de `runTurn`
  en cada paso: materializa el prompt promovido como `Message{Role: user}` en el
  `Store`, asi `runTurn` (que lee `Store.Messages(sessionID, 0)`) lo ve. En una
  continuacion (promotion `steer` sin steers pendientes), `promote` es no-op y el
  turno corre con el historial que dejaron las tools.
- **Continuacion: tool local o steer, no texto.** `needsContinuation` viene del
  turno (true si hubo tool call local). Si es false, el loop chequea
  `HasPending(steer)`: un steer admitido durante el turno fuerza otra vuelta. Un
  turno de solo texto sin steer pendiente cierra la actividad. Es la regla central
  del loop (no continuar por texto del asistente).
- **`promotion = steer` tras el primer turno.** Dentro de una actividad, despues
  del primer paso la promocion baja a `steer`: el `queue` ya entro y solo el
  steering en vivo afecta las continuaciones siguientes. Cuando se abre una
  actividad nueva (hay otro `queue`), vuelve a `queue`.
- **`MaxSteps` corta el loop.** Si tras 25 pasos el turno sigue pidiendo
  continuacion (tool tras tool sin cerrar), el loop sale con
  `StepLimitExceededError`. Protege contra loops improductivos del modelo.
- **Nuevas actividades.** Al cerrar una actividad (la sesion quedo estable), el
  loop revisa `HasPending(queue)`: si hay otro prompt, abre una actividad nueva con
  `promotion = queue`. Asi `Run` drena varios prompts encolados en una sola
  corrida.
- **Errores duros se propagan.** Cualquier error de `HasPending`/`Promote`/
  `AppendEvent`/`runTurn` corta `Run` y se devuelve. Los caminos de fallo
  out-of-band (cancelacion, stream caido, tools colgadas) son de M8.

## 6. Semantica del loop

El contrato que M6 fija (y que M9 cablea a la UI sin cambiarlo):

- **Idle.** `Run(ctx, sessionID, false)` sin steer ni queue pendiente devuelve
  `nil` sin ejecutar ningun turno (no llama al proveedor).
- **Un queue, un turno, idle.** Tras `Admit(queue, p)`, `Run` promueve `p` a un
  `Message{Role: user}`, ejecuta un turno; si el turno es de solo texto
  (`needsContinuation == false`) y no hay steer pendiente, cierra la actividad; sin
  otro queue, `Run` devuelve `nil`. La proyeccion queda con el usuario y el
  asistente.
- **Continua mientras haya tool calls.** Si el turno hace una tool call local,
  `needsContinuation == true` y el loop ejecuta otro turno (con el historial que
  incluye el resultado de la tool). Encadena turnos hasta que uno deja la sesion
  estable.
- **Texto del asistente no continua solo.** Un turno de solo texto, sin steer
  pendiente, **cierra** la actividad: el loop no encadena turnos por texto.
- **Steer admitido durante la corrida.** Un `steer` admitido mientras el turno
  corre se detecta al terminar (`HasPending(steer)`), fuerza una continuacion y en
  el siguiente paso `promote(steer)` lo materializa como `Message{Role: user}`. La
  proyeccion gana ese mensaje y el turno lo ve.
- **Segundo queue abre nueva actividad.** Con dos prompts en `queue`, `Run` procesa
  el primero (una actividad), lo cierra, detecta el segundo pendiente y abre una
  actividad nueva que lo procesa. La proyeccion queda con los dos pares
  usuario/asistente, en orden.
- **Exceder pasos.** Si una actividad agota `MaxSteps` con continuacion siempre
  pendiente, `Run` devuelve `*StepLimitExceededError{Max: 25}`.
- **Reconstruccion desde el store.** En cada turno `runTurn` relee el historial del
  `Store`; `Run` no arrastra mensajes vivos entre turnos.

## 7. Plan TDD

### Safety net

- Estado base verde antes de tocar nada. M6 agrega tipos nuevos en `session`
  (`Inbox`/`MemoryInbox`/`Delivery`/`Prompt`), un archivo nuevo en `runner`
  (`run.go`) y un parametro a `NewRunner`; primero se corre lo existente (M0..M5).
- Comando: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Resultado esperado: pasan limpio. El unico punto que **deja de compilar** por
  diseno es `turn_test.go` (M5): sus llamadas a `NewRunner` quedan con un argumento
  de menos al sumar `inbox`. Se actualizan de forma mecanica (pasar
  `session.NewMemoryInbox()`); su comportamiento (llamar `runTurn` directo) no
  cambia. Tras el cambio se re-corre `go test ./internal/session/runner` para
  confirmar que M5 sigue verde.

### Understand

- Leer la entrada M6 del roadmap; "Loop externo (`Run`)" y "Que hace un turno" de
  `docs/atenea-agent-loop.md`; el pseudocodigo y la seccion "Input durable",
  "Condiciones de continuacion" de `docs/opencode-agent-loop.md`; y el contrato de
  M5 (`runTurn` devolviendo `needsContinuation`).
- Comportamiento esperado: drenar el inbox; promover input antes del turno;
  continuar solo por tool local o steer; cortar a `MaxSteps`; abrir nuevas
  actividades por `queue`.

### RED

- Escribir primero el test que falla:
  `TestRunner_RunProcessesQueuedPromptThenIdle`. Referencia `NewRunner` (con el
  `inbox` nuevo), `Run`, `session.NewMemoryInbox`, `session.Prompt`,
  `session.DeliveryQueue`, que aun no existen -> no compila -> falla (RED honesto
  en Go es fallo de compilacion del paquete de test).
- El test arma `store := session.NewMemoryStore()`, `inbox := session.NewMemoryInbox()`,
  `inbox.Admit(ctx, "s1", session.Prompt{Text: "hola"}, session.DeliveryQueue)`, un
  `FakeProvider` de solo texto (`StepStarted, TextStarted, TextDelta("ok"),
  TextEnded, StepEnded`), un `Registry` con `Echo`, y
  `r := NewRunner(store, inbox, fake, reg, tool.Permissions{"echo": true}, ids())`
  con `ids()` un contador (`"m1","m2",...`). Afirma:
  - `r.Run(ctx, "s1", false)` devuelve `err == nil`;
  - `Store.Messages(ctx, "s1", 0)` tiene 2 mensajes: el usuario `"hola"` y un
    asistente con `Text == "ok"`, en ese orden (el queue se promovio y corrio un
    solo turno que cerro la actividad).
- Comando:
  `go test -run TestRunner_RunProcessesQueuedPromptThenIdle ./internal/session/runner`
  -> fallo esperado.

### GREEN

- Escribir el minimo: `internal/session/inbox.go` (`Delivery`, `Prompt`, `Inbox`,
  `MemoryInbox`); el campo `inbox` y el parametro de `NewRunner`; `run.go`
  (`MaxSteps`, `StepLimitExceededError`, `Run`, `promote`). Actualizar las llamadas
  a `NewRunner` en `turn_test.go`.
- Correr solo el test rojo hasta verde.
- Comando:
  `go test -run TestRunner_RunProcessesQueuedPromptThenIdle ./internal/session/runner`.

### TRIANGULATE

Agregar casos para evitar falso verde (los del roadmap). Dos proveedores de test
nuevos (en `run_test.go`), porque el `FakeProvider` de M2 replayea el mismo guion
en cada `Stream` y el loop llama `Stream` una vez por turno:

- `scriptedProvider`: devuelve un guion **distinto** por turno (una lista de
  guiones; el i-esimo `Stream` reproduce el i-esimo; agotados, stream vacio). Cada
  `Stream` delega en un `FakeProvider` fresco con el guion del turno; cuenta las
  llamadas (`calls`).
- `steeringProvider`: envuelve un proveedor y, en su **primer** `Stream` (con
  `sync.Once`), admite un `steer` en el inbox antes de delegar. Simula "steer
  admitido durante la corrida".

Tests:

- `TestRunner_RunContinuesWhileToolCalls`: `scriptedProvider` con dos turnos
  -> turno 1 `ToolCall{echo}`, turno 2 solo texto. `Admit(queue, "hola")`. Afirma
  `Run` devuelve `nil`; que se ejecutaron **dos** turnos (`prov.calls == 2`); que
  la proyeccion incluye el `Message{Role: tool}` del resultado de `echo` y el texto
  del asistente del turno 2 (continuo por la tool y paro al quedar estable).
- `TestRunner_RunAssistantTextDoesNotContinueAlone`: `scriptedProvider` (o el
  `FakeProvider` replayable) de solo texto. `Admit(queue, "hola")`. Afirma que se
  ejecuto **un solo** turno (`prov.calls == 1`) y `Run == nil` (el texto no
  encadena otro turno). Es el complemento del caso anterior: aisla la regla "no
  continuar por texto".
- `TestRunner_RunSteerAdmittedDuringRunEntersNextContinuation`: `steeringProvider`
  que en el primer `Stream` admite `steer "sigue"`, envolviendo un
  `scriptedProvider` de dos turnos de solo texto. `Admit(queue, "hola")`. Afirma
  `Run == nil`; que corrieron **dos** turnos; y que la proyeccion contiene un
  `Message{Role: user, Text: "sigue"}` (el steer entro en la siguiente
  continuacion, aunque el turno 1 fue de solo texto). Prueba a la vez que el steer
  continua y que la promocion lo materializo.
- `TestRunner_RunSecondQueueOpensNewActivity`: `FakeProvider` de solo texto
  (replayable). `Admit(queue, "p1")` y `Admit(queue, "p2")`. Afirma `Run == nil`;
  que la proyeccion queda con la secuencia `user "p1"`, asistente, `user "p2"`,
  asistente (dos actividades, en orden de admision FIFO).
- `TestRunner_RunExceedingStepsReturnsStepLimitExceeded`: `FakeProvider`
  **replayable** con un guion que **siempre** hace una `ToolCall{echo}`
  (`StepStarted, ToolCall{c1, echo, {"text":"x"}}, StepEnded`). Cada turno continua
  -> tras 25 pasos el loop sale. `Admit(queue, "loop")`. Afirma que `Run` devuelve
  un error y que `errors.As` lo reconoce como `*StepLimitExceededError` con
  `Max == 25`.
- (Inbox aislado) `TestMemoryInbox_AdmitHasPendingPromote` en
  `internal/session/inbox_test.go`: `Admit(queue)` -> `HasPending(queue) == true`;
  `Promote(queue)` devuelve el prompt y lo saca (`HasPending(queue) == false`);
  `Promote(steer)` drena todos los steers admitidos; `Promote` sin pendientes
  devuelve `nil`. Fija el contrato del inbox que el loop asume.
- Comandos:
  - `go test -run 'TestRunner_Run|TestMemoryInbox' ./internal/session/...`
  - `go test -race -run 'TestRunner_Run' ./internal/session/runner` (el loop sigue
    asentando tools con `errgroup`/candado del `Publisher`; el caso de tool calls
    corre con `-race`)

### REFACTOR

- Limpieza sin cambiar comportamiento: factorizar los helpers de test de `run_test.go`
  (un `idCounter()` para `nextID`, un `runFixture` que arme store+inbox+provider+
  registry+runner, un lector de la proyeccion) si reduce duplicacion; reutilizar
  `seedUser`/`hasToolMessage` de `turn_test.go` donde aplique. Actualizar
  `internal/session/runner/doc.go` (el `Run` externo aterrizo en M6; las senales de
  control siguen en M7) y `internal/session/doc.go` (el `Inbox` aterrizo:
  queue/steer durable; epoch/historial proyectado siguen pendientes).
- Verificar que la suite sigue verde tras el formateo.
- Comando: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Criterios de aceptacion (Done when)

1. Existe `session.Inbox` (`Admit`, `HasPending`, `Promote`) con `MemoryInbox`
   (+ `NewMemoryInbox`), y los tipos `Delivery` (`DeliveryNone`/`DeliveryQueue`/
   `DeliverySteer`) y `Prompt` (`Text`).
2. El `Runner` tiene el campo `inbox session.Inbox` y `NewRunner` lo recibe
   (aditivo: `runTurn`/`consume` no cambiaron; M5 sigue verde tras actualizar la
   construccion en `turn_test.go`).
3. Existe `MaxSteps = 25` y `StepLimitExceededError` (`Max int`), inspeccionable
   con `errors.As`.
4. `Run(ctx, sessionID, force)` con la sesion idle (sin steer ni queue y sin force)
   devuelve `nil` sin ejecutar ningun turno.
5. Tras `Admit(queue, p)`, `Run` promueve `p` a `Message{Role: user}`, ejecuta un
   turno de solo texto y deja la sesion idle (`nil`); la proyeccion queda con el
   usuario y el asistente.
6. Un turno con tool call local hace que el loop ejecute otro turno; encadena hasta
   que un turno deja la sesion estable. Verificado contando los turnos y la
   proyeccion (mensaje `Role: tool` + asistente final).
7. Un turno de solo texto, sin steer pendiente, **no** encadena otro turno (cierra
   la actividad). Verificado con conteo de turnos `== 1`.
8. Un `steer` admitido durante la corrida fuerza una continuacion y se materializa
   como `Message{Role: user}` en el siguiente paso; la proyeccion lo incluye.
9. Con dos prompts en `queue`, `Run` procesa ambos en actividades sucesivas; la
   proyeccion queda con los dos pares usuario/asistente en orden FIFO.
10. Una actividad que agota `MaxSteps` con continuacion siempre pendiente devuelve
    `*StepLimitExceededError{Max: 25}`.
11. `go test ./...` (y `-race` donde aplica) pasa; `go vet ./...` limpio;
    `gofmt -l .` vacio.
12. No hubo cambios en `app.go`, `main.go`, Wails, el frontend ni `internal/event`.
    En `internal/llm` no se toco nada (el fake replayable de M2 alcanza; los
    proveedores multi-turno y de steering son helpers de **test** en `run_test.go`).
    En `internal/session` solo se agrego `inbox.go` (y `doc.go`); `session.go`,
    `event.go`, `store.go`, `memstore.go` intactos. En `runner` solo se agrego
    `run.go`, crecio `runner.go` (campo/param) y `doc.go`; `turn.go`/`publish.go`
    intactos salvo el `turn_test.go` actualizado.

## 9. Comandos

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

## 10. Tabla de evidencia esperada

Al cerrar M6, la respuesta/PR debe incluir esta tabla con resultados reales:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0..M5 verde antes de editar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Contrato del loop externo leido | roadmap M6, `docs/atenea-agent-loop.md` (Run), `docs/opencode-agent-loop.md` (pseudocodigo) | comportamiento identificado |
| RED | Test queue->idle escrito primero | `run_test.go` + `go test -run TestRunner_RunProcessesQueuedPromptThenIdle ./internal/session/runner` | fallo esperado (no compila) |
| GREEN | `inbox.go` + `run.go` + `inbox` en `NewRunner` minimos; `turn_test.go` actualizado | `internal/session/inbox.go`, `internal/session/runner/{run,runner}.go` | test especifico pasa |
| TRIANGULATE | Continua por tools, texto no continua, steer en continuacion, segundo queue, step limit, contrato del inbox | `go test -run 'TestRunner_Run|TestMemoryInbox' ./internal/session/...`, `go test -race ...` | casos pasan, `-race` limpio |
| REFACTOR | helpers de test, `doc.go` actualizados | `gofmt -w internal`, `go vet ./...`, `go test ./...` | suite verde, M1..M5 intactos |

## 11. Riesgos y decisiones

- **Promocion en `Run`, no dentro de `runTurn`.** La arquitectura muestra
  `runTurn(ctx, sessionID, promotion)` promoviendo dentro (paso 5 de
  `runTurnAttempt`). M6 lo deja como un **pre-paso fino** en `Run` (`promote` antes
  de `runTurn`) y mantiene `runTurn(ctx, sessionID)` exactamente como M5: el turno
  feliz aislado. Asi los tests de turno de M5 siguen siendo unitarios y validos, y
  el cambio es aditivo. Plegar la promocion dentro de `runTurnAttempt` (junto a
  epoch/agente/compaction) es de M7, cuando la preparacion del turno crece; hacerlo
  ahora seria mover logica sin un test que la pida en ese nivel.
- **`NewRunner` crece con `inbox`; M5 `turn_test.go` se actualiza.** El `Runner`
  necesita el inbox de verdad (lo lee y promueve en `Run`), asi que la dependencia
  entra al constructor. Las llamadas a `NewRunner` en `turn_test.go` suman un
  `session.NewMemoryInbox()`; siguen llamando `runTurn` directo (que ignora el
  inbox), su comportamiento no cambia. Se prefirio el parametro honesto a un setter
  opcional o un constructor paralelo: es la forma idiomatica en Go y deja la
  dependencia explicita.
- **`Inbox` como interface con `MemoryInbox`, igual que `Store`.** El input durable
  se modela detras de una interface para que M10 pueda agregar una version
  persistente sin tocar el runner. La implementacion en memoria con dos colas FIFO
  por sesion alcanza para M6..M9. Es el mismo criterio "fakes deterministas, lo
  real al final" del plan.
- **`Promote` devuelve `[]Prompt`; el runner materializa el `Message`.** El inbox
  solo retiene y libera prompts; quien los convierte en `Message{Role: user}` del
  `Store` es el runner (`promote`). Asi el inbox no depende del store y la fuente de
  verdad del historial sigue siendo el log de eventos. Se difirio el tipo
  `Promotion` rico (con `admitted_seq`/`promoted_seq`/cutoff) de la arquitectura:
  M6 no lo ejercita y agregarlo seria poblar campos que nadie lee hasta M7.
- **`Promote(queue)` saca uno; `Promote(steer)` drena todos.** Es la semantica
  minima que cubre los escenarios: el `queue` es "uno por actividad" y el `steer`
  es "todo el direccionamiento pendiente". El refinamiento "al promover queue
  tambien drenar steer hasta cutoff" no hace falta porque la prioridad
  steer-sobre-queue al inicio de `Run` y el `promotion = steer` tras el primer
  turno ya encaminan el steering. Ese cutoff fino llega con el epoch en M7.
- **El loop continua por tool local o steer, nunca por texto.** `needsContinuation`
  del turno (M5) marca la tool local; el `HasPending(steer)` posterior marca el
  steering en vivo. Un turno de solo texto sin steer cierra la actividad. Es la
  regla que distingue un agente durable de un chat que responde y para; se prueba
  aislada (`...DoesNotContinueAlone`) y en contraste (`...ContinuesWhileToolCalls`,
  `...SteerAdmittedDuringRun`).
- **`DeliveryNone` como valor cero seguro.** Se agrega `DeliveryNone` (no esta en el
  pseudocodigo) para que `promote(none)` sea un no-op limpio cuando `Run` corre con
  `force` y sin input pendiente, y para que el zero value de `Delivery` no
  signifique "queue" por accidente. Es una desviacion minima y defensiva del
  pseudocodigo.
- **`force` se incluye ya, aunque M6 lo ejercite poco.** La firma
  `Run(ctx, sessionID, force bool)` es la que M9 (`app.go`) va a llamar
  (`Run(..., false)`); incluir `force` ahora evita cambiar la firma despues y
  cumple "M9 no cambia la logica del loop". Los tests de M6 se centran en los
  caminos queue/steer; `force` queda cableado (corre aunque no haya input) sin un
  test dedicado, o con uno minimo si se quiere fijar el comportamiento.
- **Proveedores multi-turno como helpers de test.** El `FakeProvider` de M2
  replayea el mismo guion en cada `Stream`, lo cual sirve para los casos de
  texto-repetido y de step-limit (tool en cada turno), pero no para un turno-1
  distinto del turno-2. Para eso M6 agrega `scriptedProvider` (un guion por turno) y
  `steeringProvider` (admite un steer en el primer `Stream`) **en `run_test.go`**:
  no se toca el `FakeProvider` de produccion ni la interface `Provider`. Cada
  `Stream` del `scriptedProvider` delega en un `FakeProvider` fresco, asi no se
  duplica la logica de reproduccion del guion.
- **`MaxSteps` con el `FakeProvider` replayable.** El caso de step-limit aprovecha
  que el fake replayea: un guion con una tool call se repite cada turno -> el loop
  continua siempre -> a los 25 pasos sale con `StepLimitExceededError`. Es la forma
  mas simple de provocar el loop improductivo sin un proveedor especial.
- **`promote` usa `nextID` para el ID del mensaje de usuario.** El mensaje
  promovido necesita un ID; se toma del mismo generador inyectado que el
  `assistantMessageID`. En tests, `nextID` es un contador (`"m1","m2",...`) para que
  user y asistente no colisionen y la proyeccion sea legible. Se evito inventar un
  esquema de IDs propio del inbox: el generador del runner ya cumple ese rol y
  M9 cablea uno real.

## 12. Fuentes

- Roadmap: `docs/atenea-agent-loop-roadmap.md` (hito M6)
- Arquitectura: `docs/atenea-agent-loop.md` (secciones "Loop externo (`Run`)",
  "Que hace un turno (`runTurn`)" y "Persistencia durable")
- Loop de referencia: `docs/opencode-agent-loop.md` (pseudocodigo del loop, "Input
  durable", "Condiciones de continuacion")
- Manera de trabajo: `AGENTS.md`
- Specs previos: `docs/atenea-m1-tipos-store-spec.md`,
  `docs/atenea-m2-provider-fake-spec.md`, `docs/atenea-m3-publisher-spec.md`,
  `docs/atenea-m4-tool-registry-spec.md`, `docs/atenea-m5-run-turn-spec.md`
</content>
</invoke>
