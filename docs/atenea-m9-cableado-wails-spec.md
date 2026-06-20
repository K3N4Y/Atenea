# Spec M9 — Cableado Wails (EventBus + SendPrompt)

Spec ejecutable del hito **M9** de `docs/atenea-agent-loop-roadmap.md`. Define el
estado final, el alcance, el plan TDD y los criterios de aceptacion para conectar
el loop (M1..M8, hoy 100% verde contra fakes) a la app Wails real **sin cambiar la
logica del loop**. La regla central: la UI observa la sesion como un **stream del
log durable** y el unico punto que conoce Wails es `internal/event`.

M9 agrega tres piezas y un frontend minimo:

- **`event.Bus`**: reenvia un `session.SessionEvent` (o un error duro de `Run`) al
  canal `session:<id>` del frontend. No importa Wails: recibe una `EmitFunc`
  inyectada (en produccion `runtime.EventsEmit` ligado al `ctx` de la app; en tests
  un fake que registra).
- **`event.EmittingStore`**: decora un `session.Store`; tras cada `AppendEvent`
  exitoso reenvia el evento (ya con su `Seq` y `SessionID`) al `Bus`. Es el unico
  puente Store -> UI: como **todos** los appends del runner (`Publisher`, `promote`,
  `failInterruptedTools`) pasan por el `Store`, la UI ve cada evento sin tocar el
  loop.
- **`app.go`**: `SendPrompt(sessionID, text)` hace `Admit(queue)` y arranca `Run`
  en una goroutine con un `ctx` cancelable; `Stop(sessionID)` cancela ese `ctx`
  (boton stop). El provider sigue siendo el `FakeProvider` (el real es M10).

Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos, igual que el resto de `docs/`.

## 1. Contexto

Los hitos previos dejaron el loop completo de adentro hacia afuera, verde contra
fakes y sin una sola dependencia de Wails:

- **M1** dejo el dominio durable (`Seq`, `Message`, `SessionEvent`, `Store`,
  `MemoryStore`): el log de eventos es la unica fuente de verdad; los mensajes son
  una proyeccion (`Store.Messages(sinceSeq)`). `AppendEvent` asigna `SessionID` y
  `Seq` e **ignora** los que traiga el evento.
- **M2** dejo la frontera con el modelo (`llm.Provider`, `Request`, `Event`) y el
  `FakeProvider` replayable. M9 sigue usando el fake; el adaptador real es M10.
- **M3** dejo el `Publisher`: traduce el stream a `SessionEvent` durables via
  `eventAppender.AppendEvent`. El runner le pasa el `Store` de la sesion.
- **M4** dejo el `tool.Registry` (`Materialize -> {Definitions, Settle}`), el
  `OutputStore` y el builtin `Echo`.
- **M5** dejo el turno (`runTurnAttempt` + `consume`): asienta tools locales con
  `errgroup` y publica sus resultados.
- **M6** dejo el loop externo (`Run` + `Inbox` + `MaxSteps`). El comentario de
  `Runner` ya anticipo "un generador real en M9" para `nextID`, y
  `StepLimitExceededError` "para que la UI (M9) lo distinga con errors.As".
- **M7** dejo las senales de control (`errRebuildTurn`, `errContinueAfterCompaction`)
  y el `ContextEpoch` (estable en cero con `MemoryStore`, asi el camino feliz no
  reconstruye).
- **M8** dejo el manejo de fallos: `consume` cierra tools no resueltas y emite
  `Step.Failed` ante cancelacion del `ctx` o error de proveedor, con un contexto
  desacoplado para que el cierre sobreviva; `Run` corre `failInterruptedTools` al
  arrancar. `Step.Failed` no materializa `Message`: es un marcador que **la UI (M9)
  observa por el evento durable**.

Lo que falta es el cableado: hacer visible el log por la UI y arrancar/cortar el
`Run` desde el frontend. La arquitectura ya lo describe (`docs/atenea-agent-loop.md`,
seccion "Integracion con Wails"): el `EventBus` es el unico punto que conoce Wails,
`app.go` arranca el runner en una goroutine y el boton stop cancela el `ctx`.

## 2. Objetivo

Un prompt desde la UI Wails produce **streaming visible** del turno y un boton stop
lo interrumpe, **sin cambiar una linea de la logica del loop** (`Run`, `runTurn`,
`consume`, `Publisher`). El runner se testea contra un `Bus` fake (una `EmitFunc`
que registra), nunca contra Wails. La frontera con Wails (`runtime.EventsEmit`,
`wails.Run`, el Vue) queda aislada y es el unico tramo no cubierto por `go test`,
verificable a mano con `wails dev`.

## 3. Alcance

### Dentro

- `internal/event/bus.go`: `EmitFunc`, `Bus`, `Bus.Publish(ev)`,
  `Bus.PublishError(sessionID, err)`.
- `internal/event/store.go`: `EmittingStore` que decora `session.Store` y reenvia
  cada evento agregado al `Bus`; los metodos de solo lectura delegan sin cambios.
- `app.go`: `App` con `inbox`, `runner`, `bus` y un mapa de `cancel` por sesion;
  constructor inyectable `newApp(provider, emit)`; `NewApp()` de produccion;
  `SendPrompt`, `Stop`, `startup`; un generador de IDs real para `nextID`.
- `main.go`: bindear `App` (con `SendPrompt`/`Stop`) en `wails.Run`.
- Frontend minimo (`frontend/src/App.vue` + bindings generados): input de prompt,
  boton enviar, boton stop, lista que mapea `Step.* / Text.* / Tool.*` en streaming
  escuchando `session:<id>`.

### Fuera (no hacer en M9)

- **Provider real / Anthropic**: M9 arranca con el `FakeProvider` guionado. El
  adaptador real es **M10**.
- **Store SQLite**: M9 sigue con `MemoryStore` detras del `EmittingStore`. El store
  persistente es **M10**.
- **Reproyeccion del historial en la UI al abrir una sesion** (leer `Messages` y
  pintar lo viejo): M9 solo streamea lo nuevo. La UI rica (markdown, diffs de tools,
  reproyeccion) llega cuando se necesite; M9 prueba el canal.
- **Steering desde la UI** (`Admit(steer)` en vivo) y **multiples sesiones** en la
  UI: el canal `session:<id>` ya lo soporta, pero M9 cablea una sesion y el flujo
  `SendPrompt`/`Stop`. `Steer` se agrega cuando la UI lo pida.
- **UX fina de stop vs error**: `Run` interrumpido devuelve `context.Canceled` y se
  reenvia por `PublishError`; distinguir "stop deliberado" de "fallo" en la UI se
  difiere.

## 4. El bus de eventos (`internal/event/bus.go`)

`Bus` es el unico punto que conoce el canal de eventos del frontend. No importa
Wails: recibe una `EmitFunc` cuya forma copia la de `runtime.EventsEmit`
(`eventName` + payload variadico) sin arrastrar la dependencia.

```go
package event

import "atenea/internal/session"

// EmitFunc es la frontera con Wails. En produccion (main.go/app.go) envuelve
// runtime.EventsEmit ligado al ctx de la app; en tests es un fake que registra lo
// emitido. Su forma copia la de runtime.EventsEmit (eventName + payload variadico)
// para que envolverla sea trivial y para no acoplar event/ ni los tests a Wails.
type EmitFunc func(eventName string, optionalData ...interface{})

// Bus reenvia eventos de sesion al frontend. Es la unica pieza que conoce el
// nombre del canal (session:<id>); el runner publica eventos neutrales (via el
// EmittingStore) y el Bus decide a donde van.
type Bus struct {
	emit EmitFunc
}

// NewBus crea el bus sobre una EmitFunc. emit nil deja un bus inerte (util en
// produccion antes de startup, cuando aun no hay ctx de Wails).
func NewBus(emit EmitFunc) *Bus { return &Bus{emit: emit} }

// Publish reenvia un evento durable de sesion al canal session:<id>. El evento ya
// trae su Seq y SessionID (los asigno el Store): la UI ordena por Seq, no por
// orden de llegada.
func (b *Bus) Publish(ev session.SessionEvent) {
	if b.emit == nil {
		return
	}
	b.emit("session:"+ev.SessionID, ev)
}

// PublishError reenvia al canal session:<id>:error un error duro que corto una
// corrida de Run (fallo de proveedor, limite de pasos, interrupcion). No es un
// SessionEvent durable: es el cierre observable de una actividad que fallo.
func (b *Bus) PublishError(sessionID string, err error) {
	if b.emit == nil {
		return
	}
	b.emit("session:"+sessionID+":error", err.Error())
}
```

`EmitFunc` nil-safe a proposito: `NewApp` arma el `Bus` antes de que Wails llame a
`startup` (donde llega el `ctx`); emitir antes de eso es un no-op, no un panic.

## 5. El puente Store -> UI (`internal/event/store.go`)

El runner nunca aprende del `Bus`: se le inyecta un `Store` que, ademas de
persistir, reenvia. `EmittingStore` decora cualquier `session.Store`.

```go
package event

import (
	"context"
	"sync"

	"atenea/internal/session"
)

// EmittingStore decora un session.Store: tras cada AppendEvent exitoso reenvia el
// evento (ya con su Seq y SessionID asignados) al Bus, asi la UI observa el log
// durable como un stream. Es el unico puente Store -> UI: como TODOS los appends
// del runner (Publisher de M3, promote y failInterruptedTools de M6/M8) pasan por
// el Store, la UI ve cada evento sin tocar la logica del loop. Los metodos de solo
// lectura delegan sin cambios.
type EmittingStore struct {
	inner session.Store
	bus   *Bus
	mu    sync.Mutex // serializa append+emit: la UI ve los eventos en orden de Seq
}

// NewEmittingStore envuelve inner para reenviar al bus cada evento agregado.
func NewEmittingStore(inner session.Store, bus *Bus) *EmittingStore {
	return &EmittingStore{inner: inner, bus: bus}
}

// var _ session.Store = (*EmittingStore)(nil) asegura que cumple la interface y es
// inyectable en el Runner sin cambios.
var _ session.Store = (*EmittingStore)(nil)

func (s *EmittingStore) AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, err := s.inner.AppendEvent(ctx, sessionID, ev)
	if err != nil {
		return seq, err // fallo del store: no se emite nada a la UI
	}
	ev.SessionID = sessionID
	ev.Seq = seq
	s.bus.Publish(ev)
	return seq, nil
}

// LoadSession / Messages / Epoch / PendingToolCalls delegan sin cambios.
```

Decisiones:

- **Decorador del `Store`, no hook en el `Publisher`.** El `Publisher` solo ve los
  eventos del stream del proveedor; **no** ve el `Message{Role:user}` que `promote`
  agrega ni el `Tool.Failed` que `failInterruptedTools` agrega tras un crash.
  Decorar el `Store` captura los tres sitios de append de forma uniforme y deja
  `Run`/`runTurn`/`consume`/`Publisher` intactos (requisito duro de M9: la logica
  del loop no cambia).
- **`mu` serializa append+emit para entregar en orden de `Seq`.** El `MemoryStore`
  ya asigna `Seq` bajo su propio candado, pero dos goroutines de settle podrian
  reenviar al `Bus` fuera de orden tras volver del append. El candado del decorador
  hace que la UI reciba los eventos en el mismo orden que su `Seq`. `EventsEmit` es
  no bloqueante, asi que el costo es minimo. Como cada evento igual lleva su `Seq`,
  la UI podria ordenar sola; el candado solo hace el contrato mas simple y los tests
  deterministas. (Orden de candados: `Publisher.mu` -> `EmittingStore.mu` ->
  `MemoryStore.mu`, siempre en ese sentido; sin ciclo.)
- **Se emite solo tras un append exitoso.** Si el store falla, no se reenvia un
  evento que no quedo en el log: la UI nunca ve algo que no es durable.

## 6. El cableado de la app (`app.go`)

`app.go` arma el loop con dependencias reales-pero-fake (memory store, fake
provider) y expone el control a Wails. La construccion se factoriza para que los
tests inyecten una `EmitFunc` que registra y un `FakeProvider` guionado.

```go
type App struct {
	ctx    context.Context // ctx de Wails; lo fija startup. Solo lo usa la EmitFunc real.
	inbox  session.Inbox
	runner *runner.Runner
	bus    *event.Bus

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // sessionID -> cancel de la corrida en vuelo
	wg      sync.WaitGroup                // tests esperan a las corridas (wait); la UI no
}

// newApp arma la app con la frontera (emit) y el provider inyectados, para que los
// tests usen un emit fake y un FakeProvider guionado. NewApp (produccion) la llama
// con runtime.EventsEmit y el provider del momento (M9: el fake; M10: el real).
func newApp(provider llm.Provider, emit event.EmitFunc) *App {
	a := &App{cancels: map[string]context.CancelFunc{}}
	a.bus = event.NewBus(emit)
	store := event.NewEmittingStore(session.NewMemoryStore(), a.bus)
	a.inbox = session.NewMemoryInbox()
	registry := tool.NewRegistry(tool.NewOutputStore(outputLimit), tool.Echo{})
	a.runner = runner.NewRunner(store, a.inbox, provider, registry,
		tool.Permissions{"echo": true}, newIDGen())
	return a
}

// NewApp arma la app de produccion. La EmitFunc cierra sobre a para leer a.ctx
// (que startup fija despues): emitir antes de startup es un no-op (bus nil-safe).
func NewApp() *App {
	var a *App
	emit := func(name string, data ...interface{}) {
		runtime.EventsEmit(a.ctx, name, data...)
	}
	a = newApp(demoProvider(), emit)
	return a
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// SendPrompt admite el texto como prompt en cola y arranca Run en una goroutine.
// Es el binding que el frontend llama al enviar. Admit es durable y no bloquea; el
// loop drena el inbox.
func (a *App) SendPrompt(sessionID, text string) error {
	if err := a.inbox.Admit(context.Background(), sessionID,
		session.Prompt{Text: text}, session.DeliveryQueue); err != nil {
		return err
	}
	a.start(sessionID)
	return nil
}

// Stop cancela la corrida en vuelo de sessionID (boton stop). El ctx cancelado
// interrumpe el turno: M8 cierra las tools en vuelo con Tool.Failed y emite
// Step.Failed. No-op si la sesion no esta corriendo.
func (a *App) Stop(sessionID string) {
	a.mu.Lock()
	cancel := a.cancels[sessionID]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
```

`start` lanza el `Run` con un `ctx` cancelable registrado por sesion y lo limpia al
terminar; reenvia por `PublishError` el error duro con que `Run` corta una
actividad. `wait` (no exportado) bloquea hasta que terminen las corridas en vuelo:
solo lo usan los tests para ser deterministas sin `sleep`; la UI es fire-and-forget.

```go
func (a *App) start(sessionID string) {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	if old := a.cancels[sessionID]; old != nil {
		old() // una corrida previa de la misma sesion no debe quedar viva
	}
	a.cancels[sessionID] = cancel
	a.mu.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer a.clearCancel(sessionID, cancel)
		if err := a.runner.Run(ctx, sessionID, false); err != nil {
			a.bus.PublishError(sessionID, err)
		}
	}()
}
```

`newIDGen` devuelve un generador de `assistantMessageID` real (un contador atomico
con prefijo): unico por proceso, suficiente para M9 (el `MemoryStore` se reinicia
con la app). `demoProvider` arma un `FakeProvider` con un guion corto (texto +
`Step.Ended`) para que `wails dev` muestre streaming sin red. `outputLimit` es la
cota del `OutputStore` (un valor fijo razonable).

`main.go` bindea `App` y expone `SendPrompt`/`Stop` (se cae `Greet`).

## 7. El frontend minimo (frontera, verificacion manual)

`frontend/src/App.vue` se reemplaza por una UI minima: un input, un boton enviar
(`SendPrompt`), un boton stop (`Stop`) y una lista que mapea cada evento del canal
`session:<id>` a una linea. Usa `EventsOn` del runtime de Wails y los bindings
generados (`window.go.main.App.SendPrompt/Stop`). Es la **frontera no testeada por
`go test`** (igual que `runtime.EventsEmit`): se verifica a mano con `wails dev`.
Los bindings generados (`frontend/wailsjs/go/main/App.*`) se actualizan para
reflejar `SendPrompt`/`Stop`.

El mapeo es directo, 1:1 con la taxonomia: `Text.Delta` agrega texto al bloque del
asistente en curso; `Tool.Called`/`Tool.Success`/`Tool.Failed` muestran la tool y
su estado; `Step.Failed` marca el turno como interrumpido/fallido.

## 8. Plan TDD

### Safety net

Antes de tocar nada, confirmar que M1..M8 estan verdes:

```bash
go test ./...        # toda la suite verde
go vet ./...         # limpio
gofmt -l .           # vacio
```

### Understand

Leer la seccion "Integracion con Wails" de `docs/atenea-agent-loop.md`, el contrato
de `session.Store` (`AppendEvent` asigna `Seq`/`SessionID`), el `Publisher` (unico
productor del stream durable) y los tres sitios de `AppendEvent` del runner
(`Publisher.emit`, `promote`, `failInterruptedTools`). Identificar que decorar el
`Store` captura los tres sin tocar el loop.

### RED

Primer test (puente Store -> UI), en `internal/event/store_test.go`:
`TestEmittingStore_ForwardsAppendedEventToBusWithSeq` — un `AppendEvent` sobre el
decorador reenvia al `Bus` un evento **con el `Seq` y `SessionID` asignados** por el
store interno. Falla: `EmittingStore` no existe.

### GREEN

Implementar `event.Bus` (Publish/PublishError sobre `EmitFunc`) y `EmittingStore`
minimos para pasar el test especifico.

### TRIANGULATE

- `TestBus_PublishEmitsOnSessionChannel` / `TestBus_PublishErrorEmitsOnErrorChannel`:
  el `Bus` arma el nombre de canal correcto (`session:<id>`, `session:<id>:error`) y
  pasa el payload; `EmitFunc` nil es no-op (no panic).
- `TestEmittingStore_ReadMethodsDelegate`: `Messages`/`Epoch`/`PendingToolCalls`
  delegan al inner sin reenviar (solo `AppendEvent` emite).
- `TestEmittingStore_AppendErrorDoesNotEmit`: si el inner falla, no se reenvia nada.
- `TestEmittingStore_TurnStreamsEventsInSeqOrder` (`-race`): un `Runner` real con
  `FakeProvider` (texto + una tool call) sobre `EmittingStore(MemoryStore, busFake)`
  reenvia al bus la misma secuencia durable que el log, en orden de `Seq`, incluido
  el `Message{Role:user}` de `promote`. Corre con `-race` (settle concurrente).
- `TestApp_SendPromptStreamsTurnToBus`: `SendPrompt` admite, arranca `Run`, y tras
  `wait()` el emit fake registro los eventos del turno en el canal `session:s1`.
- `TestApp_StopInterruptsInflightTurn`: con un provider guionado a una tool
  bloqueante, `SendPrompt` arranca el turno, `Stop` cancela, `wait()` retorna y el
  bus registro `Tool.Failed` + `Step.Failed` (la interrupcion de M8 viaja por el
  cableado). `-race`.

### REFACTOR

Helpers de test compartidos (armar un runner end-to-end, un bus fake concurrente),
`doc.go` del paquete `event`, comentarios. Verificar que la suite entera sigue verde
y `-race` limpio.

## 9. Criterios de aceptacion (Done when)

- `event.Bus` reenvia `Publish`/`PublishError` al canal correcto via `EmitFunc`;
  nil es no-op.
- `event.EmittingStore` cumple `session.Store`, reenvia cada evento agregado con su
  `Seq`/`SessionID`, delega las lecturas y no emite ante error del inner.
- El `Runner` corre **sin cambios** detras del `EmittingStore`: la suite M1..M8
  sigue verde.
- `app.go`: `SendPrompt` admite + arranca `Run`; `Stop` cancela la corrida; ambos
  testeados contra un emit fake y un `FakeProvider`, incluida la ruta de
  interrupcion (`Tool.Failed` + `Step.Failed` en el bus).
- `go test ./...` verde, `go test -race` limpio en los tests concurrentes,
  `gofmt -l .` vacio, `go vet ./...` limpio.
- Frontend minimo cableado (verificable con `wails dev`): un prompt produce
  streaming visible y stop lo corta. (Frontera no cubierta por `go test`.)

## 10. Comandos

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Ciclo (test especifico primero)
go test -run TestEmittingStore_ForwardsAppendedEventToBusWithSeq ./internal/event

# Triangulacion (bus, delegacion, error, turno end-to-end, app)
go test -run 'TestBus_|TestEmittingStore_' ./internal/event
go test -run 'TestApp_' .

# Concurrencia (settle concurrente + stop interrumpe)
go test -race -run 'TestEmittingStore_TurnStreams|TestApp_Stop' ./...

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde

# Frontera Wails (verificacion manual, no go test)
wails dev           # enviar un prompt, ver streaming, probar stop
```

## 11. Tabla de evidencia esperada

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M1..M8 verde antes de editar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Frontera Wails y sitios de append leidos | `docs/atenea-agent-loop.md` (Integracion con Wails), `store.go`, `publish.go`, `run.go` | comportamiento identificado |
| RED | Puente Store -> UI escrito primero | `internal/event/store_test.go` + `go test -run TestEmittingStore_ForwardsAppendedEventToBusWithSeq ./internal/event` | fallo esperado |
| GREEN | `Bus` + `EmittingStore` minimos | `internal/event/{bus,store}.go` | test especifico pasa |
| TRIANGULATE | Bus, delegacion, error, turno end-to-end, app + stop | `go test -run 'TestBus_|TestEmittingStore_|TestApp_' ./...`, `go test -race ...` | casos pasan, `-race` limpio |
| REFACTOR | Helpers, `doc.go`, frontend minimo | `gofmt -w .`, `go vet ./...`, `go test ./...` | suite verde, M1..M8 intactos |

## 12. Riesgos y decisiones

- **La logica del loop no cambia.** Es el invariante de M9. Toda la visibilidad
  hacia la UI se logra decorando el `Store` (inyeccion), no editando `Run`,
  `runTurn`, `consume` ni el `Publisher`. Si algun test de M1..M8 cambia, M9 se
  hizo mal.
- **Decorador del `Store` vs hook en el `Publisher`.** Ver seccion 5: el decorador
  captura los tres sitios de append (incluido el prompt del usuario y la limpieza
  tras crash); un hook en el `Publisher` solo veria el stream del proveedor.
- **`EmitFunc` inyectada, no `runtime.EventsEmit` directo.** Mantiene `event/` y los
  tests sin dependencia de Wails y permite un emit fake. El unico lugar que nombra
  Wails es la closure de `NewApp` (y `main.go`).
- **`Bus` nil-safe.** `NewApp` arma el `Bus` antes de `startup` (donde llega el
  `ctx`). Emitir antes de eso es un no-op, no un panic; ademas evita pasar un `ctx`
  nil a `runtime.EventsEmit`.
- **Provider fake en M9.** El cableado se prueba con el `FakeProvider`; cambiarlo por
  el real es solo el argumento de `newApp` (M10). Asi M9 verifica el canal sin red.
- **`wait()` solo para tests.** Arrancar `Run` en goroutine es fire-and-forget en
  produccion; los tests necesitan un punto de sincronizacion deterministico. Un
  `WaitGroup` con un `wait()` no exportado lo da sin `sleep` ni flakiness, y no
  cambia el comportamiento de produccion.
- **Stop reenvia `context.Canceled` por `PublishError`.** Faithful a la arquitectura
  (`a.bus.PublishError(sessionID, err)`). Distinguir en la UI un stop deliberado de
  un fallo real es UX que se difiere; el canal de error ya transporta la causa.
- **`nextID` real (contador atomico).** Unico por proceso, suficiente con
  `MemoryStore` (se reinicia con la app). Un ID estable entre reinicios (uuid o
  ULID) llega con el store persistente de M10.
- **El frontend es frontera, no se TDD-ea.** `go test` cubre el `Bus`, el
  `EmittingStore` y `app.go`; el Vue y `runtime.EventsEmit` se verifican con
  `wails dev`. Coherente con `AGENTS.md`: "test the runner against a fake EventBus,
  not against Wails".

## 13. Fuentes

- Roadmap: `docs/atenea-agent-loop-roadmap.md` (hito M9)
- Arquitectura: `docs/atenea-agent-loop.md` (seccion "Integracion con Wails",
  "Streaming de eventos y ejecucion de tools", "Diferencias clave Go vs TS")
- Manera de trabajo: `AGENTS.md`
- Wails runtime (eventos): https://wails.io/docs/reference/runtime/events
- Specs previos: `docs/atenea-m1-tipos-store-spec.md` ..
  `docs/atenea-m8-interrupcion-fallos-spec.md`
