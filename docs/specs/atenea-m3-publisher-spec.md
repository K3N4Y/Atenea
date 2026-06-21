# Spec M3 — Publisher (eventos)

Spec ejecutable del hito **M3** de `docs/atenea-agent-loop-roadmap.md`. Define el
estado final, el alcance, el plan TDD y los criterios de aceptacion para dejar el
**publisher**: la pieza que traduce el stream del proveedor (`llm.Event`) a
eventos durables de sesion (`SessionEvent`) con la taxonomia del contrato
(`Step.* / Text.* / Reasoning.* / Tool.*`), bufferizando los deltas para emitir
tambien un evento final con el contenido completo concatenado.

Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos, igual que el resto de `docs/`.

## 1. Contexto

M1 dejo el dominio durable (`Seq`, `Message`, `SessionEvent`, `Store`,
`MemoryStore`): el log de eventos es la unica fuente de verdad y los mensajes son
una proyeccion derivada. M2 dejo la frontera con el modelo (`llm.Provider`,
`llm.Event`, `llm.EventKind`, `llm.Usage`) y un `FakeProvider` que reproduce un
guion determinista de eventos por un channel.

El siguiente ladrillo hacia afuera (ver el orden
`tipos -> store -> provider -> publisher` en el roadmap) es el **publisher**: la
pieza que esta entre el stream del proveedor y el store. Su responsabilidad
(ver `docs/atenea-agent-loop.md`, "Eventos publicados") es:

- traducir cada `llm.Event` a un `SessionEvent` durable con el nombre del
  contrato que el frontend (M9) mapea 1:1 (`Step.Started`, `Text.Delta`,
  `Tool.Called`, ...);
- **bufferizar** los deltas de texto, razonamiento e input de tools para emitir,
  ademas de cada delta, un evento final con el contenido completo concatenado;
- mantener el `assistantMessageID` del turno (para materializar el mensaje
  coalescido del asistente) y un mapa de tool calls por `callID` (que M5
  consultara al asentar resultados).

El publisher vive en `internal/session/runner/publish.go`. En M5 el runner lo
crea por turno y lo alimenta desde el loop de consumo (`for ev := range in`,
ver "Streaming de eventos y ejecucion de tools"). M3 lo construye y lo prueba
**aislado**: alimentandolo con un guion de `llm.Event` (el del `FakeProvider` de
M2 o eventos a mano) y verificando los `SessionEvent` que persiste.

## 2. Objetivo

Dejar listo el publisher y la taxonomia durable que persiste:

En `internal/session` (la forma del evento durable es del dominio):

- el tipo `EventKind` (string) con las constantes del contrato
  (`KindStepStarted`, `KindTextDelta`, `KindToolCalled`, ...);
- el tipo `Usage` (espejo de `llm.Usage`, para no acoplar `session` a `llm`);
- el enriquecimiento **aditivo** de `SessionEvent` con `Kind` y los campos de
  payload (`Text`, `CallID`, `ToolName`, `Input`, `Usage`).

En `internal/session/runner`:

- el `Publisher` (+ `NewPublisher`) con `Publish(ctx, llm.Event) error`, que:
  - traduce cada `llm.Event` al `SessionEvent` correspondiente y lo persiste con
    `AppendEvent`;
  - bufferiza los deltas de texto y razonamiento y emite el bloque cerrado
    (`Text.Ended` / `Reasoning.Ended`) con el texto completo concatenado;
  - bufferiza el input JSON de cada tool por `callID` y lo cierra
    (`Tool.Input.Ended`) con el input completo;
  - materializa el `Message` coalescido del asistente (con el
    `assistantMessageID` del turno) al cerrar un bloque de texto, de modo que la
    proyeccion `Messages` de M1 lo devuelva **sin** cambiar el fold ni la
    interface `Store`;
  - mantiene el mapa `callID -> toolName` de las tool calls del turno;
- tests de comportamiento en `internal/session/runner` que alimentan el publisher
  con guiones de `llm.Event` y verifican los `SessionEvent` persistidos.

M3 **no** agrega `ToolSuccess`/`ToolFailed`, el loop de consumo (`consume`,
`errgroup`), `runTurn`, ni toca Wails.

## 3. Alcance

### Dentro

- `internal/session/event.go`: tipo `EventKind` + constantes del contrato y tipo
  `Usage`.
- `internal/session/session.go`: enriquecer `SessionEvent` con `Kind`, `Text`,
  `CallID`, `ToolName`, `Input`, `Usage` (aditivo; M1 sigue verde).
- `internal/session/runner/publish.go`: `Publisher`, `NewPublisher`, `Publish` y
  la interface minima `eventAppender`.
- Tests de comportamiento en `internal/session/runner/publish_test.go`.

### Fuera (no hacer en M3)

- `Publisher.ToolSuccess` / `Publisher.ToolFailed`: los emite el **runner** al
  asentar tools (no el stream del proveedor); se escriben con su test en **M5**
  (resultado de `settle`) y **M8** (interrupcion/fallo). El mapa `callID ->
  toolName` que M3 mantiene es justamente lo que esos metodos consultaran.
- Manejo de `llm.StepFailed` -> `Step.Failed` con su `Error`: es del camino de
  fallos — **M8**.
- `consume`, `errgroup`, ejecucion concurrente de tools y la espera del turno —
  **M5**.
- Concurrencia sobre el `Publisher` (mutex): en M3 `Publish` se llama en serie
  desde el loop de consumo; el acceso concurrente (settle escribiendo
  `Tool.Success`/`Tool.Failed` mientras el loop consume) llega con su test
  `-race` en **M5**.
- Persistencia de tools provider-executed (el proveedor devuelve el resultado y
  el runner solo lo persiste) — **M5**.
- Construir el `llm.Request` desde el historial proyectado, `EntriesForRunner`,
  baseline/compaction — **M5/M7**.
- `EventBus` y `runtime.EventsEmit` (la frontera con Wails) — **M9**.
- `Store` SQLite y adaptador real — **M10**.
- Cualquier toque a `app.go`, `main.go`, Wails o el frontend — **M9**.

## 4. Taxonomia durable (`event.go`) y `SessionEvent` enriquecido

La forma del evento durable es del dominio `session`: el frontend (M9) consume
`SessionEvent` con su `Kind`, y el publisher (en `runner`) es el productor. Por
eso la taxonomia vive en `internal/session`, no en `runner`.

`internal/session/event.go`:

```go
package session

// EventKind nombra cada evento durable de sesion dentro de la taxonomia del
// contrato de streaming (ver "Eventos publicados" en docs/atenea-agent-loop.md).
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

`internal/session/session.go` (enriquecimiento aditivo de `SessionEvent`):

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

`session.go` agrega `import "encoding/json"`.

Decision de diseno: el enriquecimiento es **aditivo**. Los tests de M1 construyen
`SessionEvent{}` y `SessionEvent{Message: &m}` y comparan `Message`; los campos
nuevos arrancan en cero y no rompen nada. Y la clave: el publisher escribe un
`Message` **ya completo** en `Text.Ended`, asi que el fold de M1 (`Messages`
recorre los eventos y toma los `Message != nil`) **no cambia**. Los deltas se
persisten con `Message == nil` y no contaminan la proyeccion. Esto cumple la
nota de M1 ("M3 coalesce varios deltas en un solo mensaje sin cambiar la
interface Store ni la forma de SessionEvent") moviendo el coalescing al publisher
en vez de al fold: `memstore.go` no se toca.

## 5. El publisher (`publish.go`)

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

Notas:

- **El buffer vive en el publisher, no en el fold.** Texto y razonamiento se
  acumulan en `strings.Builder`; el input de tools en `input[callID]`. En cada
  `*.Ended` se vuelca el contenido completo. Esto cumple "bufferiza deltas y
  emite un evento final con el contenido completo" del roadmap.
- **`Message` solo en `Text.Ended`.** Es el unico evento que materializa la
  proyeccion del asistente, con el `assistantMessageID` del turno. El
  razonamiento bufferiza igual que el texto pero **no** materializa `Message`:
  no es contenido conversacional de la proyeccion simplificada (y el adaptador
  real omite el contenido de thinking por defecto, ver
  `docs/atenea-llm-claude.md`). Si M5/M7 necesitan thinking en el historial, se
  agrega ahi con su test.
- **Mapa de tool calls por `callID`.** `tools[callID] = toolName` se llena en
  `ToolCall`. M3 no lo consume aun; es el estado que `ToolSuccess`/`ToolFailed`
  (M5) leeran para nombrar la tool al asentar su resultado.
- **`Input` se clona en `ToolCall`** (`append([]byte(nil), ev.Input...)`) para no
  aliasar el slice del evento del proveedor mientras se acumulan los deltas.
- **Sin error de setup ni concurrencia en M3.** `Publish` solo propaga el error
  de `AppendEvent`. El candado del publisher y el camino concurrente llegan en M5.

## 6. Semantica del publisher

El contrato que M3 fija para el frontend (M9) y para la proyeccion (M1):

- **Traduccion 1:1.** Cada `llm.Event` con semantica de sesion produce
  exactamente un `SessionEvent` cuyo `Kind` es el nombre del contrato
  (`Step.Started`, `Text.Delta`, `Tool.Called`, ...). El orden de los eventos
  persistidos respeta el orden del stream.
- **Deltas + bloque completo.** Un bloque `Text.Started / Delta / Delta / Ended`
  persiste cuatro eventos: tres marcan el streaming (con su fragmento en `Text`,
  `Message == nil`) y `Text.Ended` lleva el texto completo concatenado en `Text`
  y materializa el `Message`. Razonamiento e input de tools siguen el mismo
  patron (input via `Tool.Input.*`, completo en `Tool.Input.Ended`).
- **Proyeccion coalescida.** Tras un turno de texto, `Store.Messages(sessionID,
  0)` devuelve **un** mensaje del asistente con el texto completo y el
  `assistantMessageID` del turno. Los deltas no aparecen en la proyeccion
  (tienen `Message == nil`). El fold de M1 no cambia.
- **Tokens en `Step.Ended`.** `llm.StepEnded` con `*llm.Usage` produce un
  `Step.Ended` cuyo `Usage` es el espejo `session.Usage` con
  input/output/reasoning/cache. Un Step sin tokens deja `Usage == nil`.
- **Mapa de tool calls.** El publisher recuerda `callID -> toolName` de cada
  `Tool.Called` del turno; es el estado que M5 usa al publicar el resultado.
- **Kinds no mapeados en M3.** `llm.StepFailed` (y cualquier kind sin semantica
  de sesion todavia) no persiste evento en M3: su traduccion a `Step.Failed`
  pertenece al camino de fallos (M8).

## 7. Plan TDD

### Safety net

- Estado base verde antes de tocar nada. M3 agrega `publish_test.go` y enriquece
  `SessionEvent`; primero se corre lo existente (M0 + `session` de M1 + `llm` de
  M2).
- Comando: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Resultado esperado: pasan limpio. Si algo falla, se reporta como preexistente y
  no se sigue a ciegas. En particular, tras enriquecer `SessionEvent` se vuelve a
  correr `go test ./internal/session` para confirmar que M1 sigue verde (el
  cambio es aditivo).

### Understand

- Leer la entrada M3 del roadmap; "Eventos publicados", "Streaming de eventos y
  ejecucion de tools" y "Tipos principales" de `docs/atenea-agent-loop.md`; la
  tabla de mapeo de `docs/atenea-llm-claude.md`; y la nota de coalescing del
  spec M1.
- Comportamiento esperado: traducir cada `llm.Event` a su `SessionEvent` con el
  nombre del contrato; bufferizar deltas y emitir el bloque completo; materializar
  el `Message` coalescido del asistente; mantener el mapa `callID -> toolName`.

### RED

- Escribir primero el test que falla:
  `TestPublisher_TextBlockEmitsDeltasAndFinalConcatenatedText`. Referencia a
  `NewPublisher`, `Publish`, `session.KindText*` y los campos nuevos de
  `SessionEvent`, que aun no existen -> no compila -> falla (RED honesto en Go es
  fallo de compilacion del paquete de test).
- El test alimenta el guion `Text.Started, Text.Delta("Hola, "),
  Text.Delta("mundo"), Text.Ended` a traves de un `recordingAppender` (spy que
  captura cada `SessionEvent`), y afirma:
  - los `Kind` capturados son `[Text.Started, Text.Delta, Text.Delta,
    Text.Ended]`;
  - los deltas llevan su fragmento en `Text` y `Message == nil`;
  - `Text.Ended` lleva `Text == "Hola, mundo"` y `Message == &{ID:"a1",
    Role:assistant, Text:"Hola, mundo"}`.
- Comando:
  `go test -run TestPublisher_TextBlockEmitsDeltasAndFinalConcatenatedText ./internal/session/runner`
  -> fallo esperado.

### GREEN

- Escribir el minimo: `event.go` (`EventKind` + constantes + `Usage`), el
  enriquecimiento de `SessionEvent` en `session.go`, y `publish.go` (`Publisher`,
  `NewPublisher`, `Publish`, `eventAppender`, `emit`, `toUsage`).
- Correr solo el test rojo hasta verde.
- Comando:
  `go test -run TestPublisher_TextBlockEmitsDeltasAndFinalConcatenatedText ./internal/session/runner`.

### TRIANGULATE

Agregar casos para evitar falso verde:

- `TestPublisher_ReasoningBuffersLikeText`: `Reasoning.Started/Delta/Delta/Ended`
  -> kinds `Reasoning.*`; `Reasoning.Ended.Text` es la concatenacion completa y
  `Message == nil` (el razonamiento no se proyecta como mensaje).
- `TestPublisher_StepEndedCarriesUsageTokens`: `StepStarted` + `StepEnded` con
  `*llm.Usage{In:10,Out:20,Reasoning:5,CacheRead:3,CacheWrite:1}` -> el evento
  `Step.Ended` lleva un `*session.Usage` con esos cinco campos; un `StepEnded`
  sin usage deja `Usage == nil`.
- `TestPublisher_ToolCallAndInputDeltasCoalesce`: `ToolCall{CallID:"c1",
  ToolName:"read", Input: nil}` + `ToolInputDelta(c1, "{\"path\":")` +
  `ToolInputDelta(c1, "\"/x\"}")` + `ToolInputEnded(c1)` -> kinds `Tool.Called,
  Tool.Input.Delta, Tool.Input.Delta, Tool.Input.Ended`; `Tool.Called` lleva
  `CallID`/`ToolName`; `Tool.Input.Ended` lleva el input completo
  `{"path":"/x"}` concatenado.
- `TestPublisher_ProjectsCoalescedAssistantMessage` (atadura M3 <-> M1): usar el
  `MemoryStore` real; publicar `StepStarted`, un bloque de texto y `StepEnded`;
  luego `store.Messages(ctx, "s1", 0)` devuelve **exactamente un** mensaje
  `{ID:"a1", Role:assistant, Text: completo}`. Demuestra que los deltas no
  contaminan la proyeccion y que el coalescing del publisher aterriza en el fold
  de M1 sin tocarlo.
- `TestPublisher_AppendErrorPropagates` (camino de error): un `recordingAppender`
  configurado para devolver error -> `Publish` devuelve ese error.
- Comandos:
  - `go test -run TestPublisher ./internal/session/runner`
  - `go test -race -run TestPublisher ./internal/session/runner` (higiene; la
    concurrencia real del publisher es de M5)

### REFACTOR

- Limpieza sin cambiar comportamiento: factorizar el patron de bloque
  bufferizado de texto y razonamiento (mismo `Started/Delta/Ended` sobre un
  `strings.Builder`) en un helper si reduce duplicacion; un helper de test
  `record(p, evs...) []session.SessionEvent` para drenar el guion; actualizar el
  comentario de paquete de `internal/session/runner/doc.go` (el publisher ya
  aterrizo en M3) y, si aplica, el de `internal/session/doc.go` (la taxonomia de
  streaming se agrego sobre `SessionEvent`).
- Verificar que la suite sigue verde tras el formateo.
- Comando: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Criterios de aceptacion (Done when)

1. Existe `EventKind` (string) con las constantes del contrato
   (`KindStep* / KindText* / KindReasoning* / KindToolInput* / KindToolCalled /
   KindToolSuccess / KindToolFailed`) y el tipo `Usage` en
   `internal/session/event.go`.
2. `SessionEvent` quedo enriquecido de forma aditiva con `Kind`, `Text`,
   `CallID`, `ToolName`, `Input`, `Usage`, y los tests de M1 siguen verdes.
3. Existe `Publisher` (+ `NewPublisher`) con `Publish(ctx, llm.Event) error` en
   `internal/session/runner/publish.go`, dependiendo de `eventAppender`.
4. Un bloque `Text.Started/Delta/Delta/Ended` persiste los cuatro eventos con su
   `Kind`, y `Text.Ended` lleva el texto completo concatenado y materializa el
   `Message` del asistente con el `assistantMessageID` del turno.
5. El razonamiento bufferiza igual que el texto (`Reasoning.*`, completo en
   `Reasoning.Ended`) sin materializar `Message`.
6. `StepEnded` con tokens produce `Step.Ended` con `*session.Usage`
   (input/output/reasoning/cache); sin tokens deja `Usage == nil`.
7. `ToolCall` produce `Tool.Called` (con `CallID`/`ToolName`) y registra
   `callID -> toolName`; los `Tool.Input.Delta` coalescen y `Tool.Input.Ended`
   lleva el input JSON completo.
8. `Store.Messages(sessionID, 0)` tras un turno de texto devuelve un unico
   mensaje del asistente coalescido; los deltas no aparecen en la proyeccion y
   `memstore.go` no fue modificado.
9. `go test ./...` (y `-race` donde aplica) pasa; `go vet ./...` limpio;
   `gofmt -l .` vacio.
10. No hubo cambios en `app.go`, `main.go`, Wails ni el frontend; ni en
    `internal/llm`, `internal/tool` o `internal/event`. En `internal/session`
    solo se agrego `event.go` y se enriquecio `SessionEvent` (sin tocar
    `memstore.go` ni `store.go`).

## 9. Comandos

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

## 10. Tabla de evidencia esperada

Al cerrar M3, la respuesta/PR debe incluir esta tabla con resultados reales:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0+M1+M2 verde antes de editar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Contrato de eventos publicados leido | roadmap M3, `docs/atenea-agent-loop.md`, `docs/atenea-llm-claude.md`, spec M1 | comportamiento identificado |
| RED | Test de deltas+texto completo escrito primero | `publish_test.go` + `go test -run TestPublisher_TextBlockEmitsDeltasAndFinalConcatenatedText ./internal/session/runner` | fallo esperado (no compila) |
| GREEN | `event.go` + `SessionEvent` enriquecido + `publish.go` minimos | `internal/session/event.go`, `internal/session/session.go`, `internal/session/runner/publish.go` | test especifico pasa |
| TRIANGULATE | Reasoning, Usage, Tool.Input coalescido, proyeccion M1, error | `go test -run TestPublisher ./internal/session/runner`, `go test -race ...` | casos pasan, `-race` limpio |
| REFACTOR | helper de bloque/test, `doc.go` actualizados | `gofmt -w internal`, `go vet ./...`, `go test ./...` | suite verde, M1 intacto |

## 11. Riesgos y decisiones

- **Taxonomia en `session`, no en `runner`.** La forma del evento durable
  (incluido su `Kind`) es del dominio que se persiste y que el frontend consume
  (`session.SessionEvent`); el publisher (en `runner`) es solo el productor. Por
  eso `EventKind`/`Usage` viven en `internal/session`. El runner las referencia
  como `session.KindTextDelta`.
- **`EventKind` como string, no int.** A diferencia de `llm.EventKind` (un `int`
  interno del stream), el `session.EventKind` se serializa a Wails (M9) y se ve
  en el store; un string nombrado (`"Text.Delta"`) es estable, legible y se mapea
  1:1 en el frontend sin tabla de traduccion. Es el mismo criterio con que el
  diseno fijo los nombres del contrato.
- **Coalescing en el publisher, no en el fold.** M1 anticipo "coalescer varios
  deltas en un solo mensaje". Se hace bufferizando en el publisher y escribiendo
  un `Message` completo en `Text.Ended`, en vez de enriquecer el fold para sumar
  fragmentos por ID. Resultado: `memstore.go` no se toca y la proyeccion de M1
  sigue identica. Es mas simple y deja un solo lugar responsable del coalescing.
- **`session.Usage` espejo de `llm.Usage`.** Para no acoplar `session` a `llm`
  (la direccion de dependencia es `runner -> {session, llm}`), se duplica el
  tipo de tokens y el publisher copia campo a campo. La duplicacion es de cinco
  ints; el desacople vale mas que el DRY aqui.
- **`eventAppender` en vez del `Store` completo.** El publisher solo necesita
  `AppendEvent`. Depender de una interface de un metodo lo hace testeable con un
  spy minimo y honra "acepta interfaces chicas". El `session.Store` real (y el
  `MemoryStore`) la cumplen sin cambios.
- **Reasoning no materializa `Message`.** Bufferiza y emite `Reasoning.Ended`
  con el texto completo (simetrico al texto en streaming), pero no entra en la
  proyeccion: no es contenido conversacional simplificado y el adaptador real
  omite el thinking por defecto. Si un test de M5/M7 necesita thinking en el
  historial, se agrega ahi.
- **Sin candado en M3.** En M3 `Publish` se llama en serie desde el (futuro) loop
  de consumo. El acceso concurrente aparece en M5, cuando las goroutines de
  `settle` publiquen `Tool.Success`/`Tool.Failed` mientras el loop sigue
  consumiendo; ahi se agrega el mutex con su test `-race`. Adelantarlo seria
  especular.
- **`ToolSuccess`/`ToolFailed` fuera de M3.** Son eventos que produce el
  **runner** al asentar tools, no el stream del proveedor (por eso `llm.EventKind`
  no los incluye, ver spec M2). M3 deja listo el estado que necesitan (el mapa
  `callID -> toolName`); los metodos se escriben con su test en M5/M8.
- **`StepFailed` diferido.** El stream puede traer `llm.StepFailed`, pero su
  traduccion a `Step.Failed` con el `Error` del turno es del camino de fallos
  (M8). En M3 `Publish` lo ignora; no se inventa el campo `Error` en
  `SessionEvent` hasta que M8 lo ejercite con un test.
- **`scaffold_test.go` de runner se conserva.** A diferencia de M1/M2 (que
  reemplazaron su scaffold), el de `runner` ancla `golang.org/x/sync/errgroup`
  en `go.mod` para M5. M3 no usa errgroup, asi que se conserva el scaffold (que
  mantiene el anclaje) y se **agrega** `publish_test.go`. El scaffold se retira en
  M5, cuando `consume` importe errgroup en codigo de produccion.

## 12. Fuentes

- Roadmap: `docs/atenea-agent-loop-roadmap.md` (hito M3)
- Arquitectura: `docs/atenea-agent-loop.md` (secciones "Eventos publicados",
  "Streaming de eventos y ejecucion de tools", "Tipos principales")
- Integracion LLM: `docs/atenea-llm-claude.md` (tabla de mapeo de eventos del SDK
  a `llm.Event`; thinking omitido por defecto)
- Manera de trabajo: `AGENTS.md`
- Specs previos: `docs/atenea-m1-tipos-store-spec.md`,
  `docs/atenea-m2-provider-fake-spec.md`
