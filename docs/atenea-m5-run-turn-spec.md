# Spec M5 — Un turno (`runTurn`) feliz

Spec ejecutable del hito **M5** de `docs/atenea-agent-loop-roadmap.md`. Define el
estado final, el alcance, el plan TDD y los criterios de aceptacion para
ensamblar M1..M4 en **un turno**: la pieza que construye el `llm.Request` desde el
historial proyectado, llama `Provider.Stream` **una vez**, consume el stream con
el `Publisher`, asienta las tool calls locales **concurrentemente** con `errgroup`
y devuelve `needsContinuation`. Un turno con solo texto persiste sus eventos y
devuelve `false`; una tool call local registra `Tool.Called` antes de ejecutar,
publica `Tool.Success` (o `Tool.Failed` si falla) y devuelve `true`; varias tool
calls corren a la vez y el turno **espera** a todas antes de decidir.

Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos, igual que el resto de `docs/`.

## 1. Contexto

Los hitos previos dejaron, de adentro hacia afuera, todas las piezas que el turno
ensambla (ver el orden `tipos -> store -> provider -> publisher -> tools -> turno`
en el roadmap):

- **M1** dejo el dominio durable (`Seq`, `Message`, `Role`, `SessionEvent`,
  `Store`, `MemoryStore`): el log de eventos es la unica fuente de verdad y los
  mensajes son una proyeccion derivada (`Store.Messages(sinceSeq)`).
- **M2** dejo la frontera con el modelo (`llm.Provider`, `llm.Request`,
  `llm.Event`, `llm.EventKind`, `llm.Usage`) y un `FakeProvider` que reproduce un
  guion determinista de eventos por un channel y lo cierra al terminar.
- **M3** dejo el `Publisher` (`internal/session/runner/publish.go`): traduce cada
  `llm.Event` a un `SessionEvent` durable con la taxonomia del contrato, bufferiza
  los deltas, materializa el `Message` coalescido del asistente en `Text.Ended` y
  mantiene el mapa `callID -> toolName` del turno.
- **M4** dejo el `tool.Registry` (`Materialize(perms) -> {Definitions, Settle}`),
  los tipos del contrato (`Tool`, `Call`, `Result`, `SettleFunc`, `Permissions`,
  `Materialized`, `UnknownToolError`), el `OutputStore` que acota el output grande
  por `callID` y el primer builtin ejecutable, `Echo`.

El siguiente ladrillo es el **turno** (`internal/session/runner/turn.go`). Su
responsabilidad (ver `docs/atenea-agent-loop.md`, "Que hace un turno (`runTurn`)"
y "Streaming de eventos y ejecucion de tools") es ensamblar lo anterior en una
unica llamada al proveedor:

- **construir** el `llm.Request` desde el historial proyectado (`Store.Messages`)
  y las tools materializadas con los permisos del agente
  (`Registry.Materialize`);
- **llamar una vez** a `Provider.Stream(ctx, req)` y obtener el channel del turno;
- **consumir** el stream con un `Publisher` del turno: cada evento se persiste; y
  cada tool call **local** lanza una goroutine que la asienta (`Settle`),
  publicando `Tool.Success` o `Tool.Failed`;
- **esperar** con `errgroup` a que **todas** las tools asienten antes de decidir;
- **devolver** `needsContinuation`: `true` si hubo al menos una tool call local
  (el loop externo de M6 hara otro turno), `false` si el turno fue solo texto.

M5 construye y prueba el turno **aislado**: con el `FakeProvider` guionado, el
`MemoryStore` y el `Registry` con `Echo`, llamando `runTurn` directamente (sin el
loop externo `Run`, que es M6). No toca Wails ni el proveedor real.

## 2. Objetivo

Dejar listo el turno ensamblado y los agregados que necesita:

En `internal/llm` (el request crece para llevar historial y tools):

- el tipo `Message` (`Role`, `Text`): el historial proyectado convertido al
  formato del proveedor. Se agrega **aditivo**;
- los campos `Request.Messages []Message` y `Request.Tools []ToolDef`: M5 los
  puebla al construir el turno (cumple la nota de M2 "Request crece sin cambiar la
  interface" y la de M4 "Request.Tools diferido a M5"). `Provider`, `Event`,
  `Usage` y el `FakeProvider` no cambian su contrato;
- el campo `Event.ProviderExecuted bool`: marca una tool call que el proveedor
  ejecuto el mismo (no se asienta localmente). Aditivo.

En `internal/session` (el evento durable crece para el fallo de una tool):

- el campo `SessionEvent.Error string`: el mensaje de fallo de una tool
  (`Tool.Failed`). Aditivo; M8 lo reutiliza para `Step.Failed`.

En `internal/session/runner`:

- los metodos `Publisher.ToolSuccess(ctx, callID, output)` y
  `Publisher.ToolFailed(ctx, callID, cause)`: publican el resultado sintetico de
  una tool (consultando el mapa `callID -> toolName` de M3) y materializan un
  `Message{Role: tool}` para que el modelo lo vea en el siguiente turno;
- un **candado** (`sync.Mutex`) en el `Publisher`: en M5 el loop de consumo
  publica desde la goroutine principal mientras las goroutines de `settle`
  publican `Tool.Success`/`Tool.Failed`. M3 difirio el candado a aqui;
- el `Runner` (+ `NewRunner`) con sus dependencias (`Store`, `Provider`,
  `Registry`, `Permissions`, generador de IDs);
- `runTurn(ctx, sessionID) (bool, error)`: arma el request, llama `Stream` una
  vez, crea el `Publisher` del turno y delega en `consume`;
- `consume(ctx, in, pub, settle) (bool, error)`: el loop de la arquitectura
  (`for ev := range in`) con `errgroup` para asentar tools concurrentes;
- tests de comportamiento que ejercen `runTurn` contra el fake guionado.

M5 **no** construye el loop externo `Run`, ni el `Inbox`/steer, ni las senales de
control, ni la interrupcion por `ctx`, ni `Step.Failed`, ni toca Wails.

## 3. Alcance

### Dentro

- `internal/llm/provider.go`: tipo `Message` (`Role`, `Text`); campos
  `Request.Messages` y `Request.Tools`; campo `Event.ProviderExecuted` (todo
  aditivo; M2 y M4 siguen verdes).
- `internal/session/session.go`: campo `SessionEvent.Error` (aditivo; M1/M3
  siguen verdes).
- `internal/session/runner/publish.go`: candado `sync.Mutex` y los metodos
  `ToolSuccess`/`ToolFailed`.
- `internal/session/runner/runner.go` (nuevo): `Runner`, `NewRunner` y sus campos.
- `internal/session/runner/turn.go` (nuevo): `runTurn`, `consume` y el helper
  `toLLMMessages`.
- Tests de comportamiento en `internal/session/runner/turn_test.go` (nuevo).
- Quitar `internal/session/runner/scaffold_test.go`: M3 dejo dicho que el scaffold
  se retira en M5, cuando `consume` importe `errgroup` en codigo de produccion
  (`turn.go`); el import real ancla `errgroup` en `go.mod` sin el scaffold.
- Actualizar `internal/session/runner/doc.go` (el loop `runTurn`/`consume`
  aterrizo) y `internal/llm/doc.go` (el `Request` crecio con `Messages`/`Tools` y
  el `Event` con `ProviderExecuted`); si aplica, `internal/session/doc.go`.

### Fuera (no hacer en M5)

- El loop externo `Run`, el `Inbox` (queue/steer), `Promote`/`HasPending`,
  `MaxSteps`, `StepLimitExceededError`, abrir nuevas actividades — **M6**. M5 deja
  `runTurn` devolviendo `needsContinuation`; quien lo llama en loop es M6.
- Las senales de control (`errRebuildTurn`, `errContinueAfterCompaction`), el
  `runTurn` con su retry loop sobre `runTurnAttempt`, el `ContextEpoch`, la
  reseleccion de agente/modelo, el system context/baseline y la compaction por
  overflow — **M7**. En M5 `runTurn` es **un solo intento** feliz: lee historial,
  materializa, arma request, llama `Stream` una vez y consume. El wrapper de
  reintento por senales internas lo agrega M7.
- Interrupcion por `ctx` cancelado a mitad de turno, errores del stream del
  proveedor, tools que el proveedor marco ejecutadas pero nunca resolvio, y
  `failInterruptedTools` (limpieza tras crash) — **M8**. En M5 el unico fallo que
  se maneja es el **in-band**: `Settle` devuelve error -> `Tool.Failed` (incluye la
  tool denegada/desconocida, que `Settle` rechaza con `UnknownToolError`). Los
  fallos out-of-band (cancelacion, error de stream, estado ambiguo) son de M8.
- La traduccion `llm.StepFailed -> Step.Failed` con su `Error`: M5 introduce el
  campo `SessionEvent.Error` para `Tool.Failed`, pero `Step.Failed` (camino de
  fallos del turno) es **M8**. El `Publisher` sigue ignorando `llm.StepFailed`.
- `Request.System []Part` y `Request.ProviderOpts` (system context, prompt cache
  key): el turno feliz no los necesita; llegan con el system context/baseline en
  **M7**.
- El mapeo rico de partes en `llm.Message` (tool_use/tool_result como bloques con
  su `tool_use_id`): el fake ignora el request, asi que M5 alcanza con
  `Role`/`Text`. El adaptador real traduce `Role: "tool"` (ID == callID) a un
  bloque `tool_result` en **M10**.
- `read`/`edit`/`bash`/`write`/`grep`/`glob`: M5 usa `Echo` (M4) como tool local
  ejecutable. Los builtins reales llegan con sus tests.
- `EventBus`, `runtime.EventsEmit`, `app.go`, `main.go`, Wails, frontend — **M9**.
- `Store` SQLite y adaptador `Provider` real — **M10**.

## 4. Agregados al contrato (`llm`, `session`)

### `internal/llm/provider.go` (aditivo)

El turno construye el request desde el historial. Para llevar el historial y las
tools, el `Request` crece con dos campos y aparece el tipo `Message` (el historial
proyectado en el formato del proveedor). El `Event` suma `ProviderExecuted` para
distinguir una tool que el proveedor ya ejecuto. Nada de esto cambia la interface
`Provider` ni el `FakeProvider`.

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

### `internal/session/session.go` (aditivo)

`SessionEvent` suma `Error`: el mensaje de fallo de una tool. M3 difirio inventar
este campo "hasta que un test lo ejercite"; el `Tool.Failed` de M5 es ese test.
M8 lo reutiliza para `Step.Failed`.

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

## 5. El turno (`runner`)

### `internal/session/runner/publish.go` (candado + resultado de tools)

El `Publisher` gana un candado y los dos metodos que publican el resultado
sintetico de una tool. M3 dejo el mapa `callID -> toolName` justamente para que
estos metodos nombren la tool al asentar.

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

`emit` no cambia (se llama con el candado tomado). `publish.go` agrega
`import "sync"`.

### `internal/session/runner/runner.go` (nuevo)

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

### `internal/session/runner/turn.go` (nuevo)

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

Notas:

- **Un `Stream` por turno.** `runTurn` llama `Provider.Stream` exactamente una
  vez. El proveedor produce un turno, no toda la sesion; el loop de M6 decide si
  invocar otro turno tras asentar tools. Esto es el corazon del diseno durable
  (ver `docs/atenea-agent-loop.md`, "Que hace un turno").
- **`Tool.Called` antes del efecto.** El `Publish(ev)` del `ToolCall` persiste
  `Tool.Called` **antes** de lanzar la goroutine de `settle`. Asi el efecto
  lateral siempre queda registrado primero (regla que se conserva de OpenCode).
- **Concurrencia con espera.** Cada tool local corre en su goroutine de
  `errgroup`; el `g.Wait()` hace que el turno espere a **todas** antes de decidir
  `needsContinuation`. El test `-race` lo ejerce con dos tools en rendezvous.
- **`gctx` para settle, `ctx` para publicar.** `settle` usa `gctx` (si una tool
  devuelve un error duro de store, cancela a las hermanas); las publicaciones del
  resultado usan `ctx`. Un fallo de ejecucion de tool no es un error duro: se
  convierte en `Tool.Failed` y la goroutine devuelve el error del store (nil si el
  append anduvo), asi que no cancela a las demas.
- **Provider-executed solo se persiste.** Una `ToolCall` con
  `ProviderExecuted == true` se publica como `Tool.Called` pero no lanza `settle`
  ni marca `needsContinuation`: el proveedor la ejecuto dentro del mismo stream.
- **`needsContinuation` solo por tool local.** El turno de solo texto devuelve
  `false`: el loop no continua por texto del asistente (regla del loop externo).

## 6. Semantica del turno

El contrato que M5 fija para el loop externo (M6) y la proyeccion:

- **Construir desde el historial.** `runTurn` lee `Store.Messages(sessionID, 0)`,
  lo convierte a `[]llm.Message` y materializa las tools con los permisos del
  agente; el `Request` lleva `Messages` (historial) y `Tools` (definiciones
  ordenadas por nombre). El request se reconstruye del store, nunca de estado vivo.
- **Turno de solo texto.** Un guion `StepStarted, Text.Started/Delta*/Ended,
  StepEnded` persiste sus eventos via el `Publisher` (incluido el `Message`
  coalescido del asistente) y `runTurn` devuelve `needsContinuation == false`.
- **Tool call local.** Una `ToolCall` (local) persiste `Tool.Called` **antes** de
  ejecutar; al asentar publica `Tool.Success` con el output (acotado por el
  `OutputStore`) y materializa un `Message{Role: tool, ID: callID}`; `runTurn`
  devuelve `true`.
- **Varias tools concurrentes.** Dos `ToolCall` lanzan dos goroutines que corren a
  la vez; el turno espera a ambas (`g.Wait`) y ambos `Tool.Success` quedan
  persistidos antes de que `runTurn` retorne.
- **Fallo de tool (in-band).** Si `Settle` devuelve error (fallo de `Execute` o
  `UnknownToolError` por tool denegada/desconocida), se publica `Tool.Failed` con
  el mensaje en `Error` y un `Message{Role: tool}` con ese texto; `runTurn` sigue
  devolviendo `true` (hubo tool call local) y **no** error de turno.
- **Provider-executed.** Una `ToolCall` con `ProviderExecuted == true` solo se
  persiste (`Tool.Called`); no se asienta localmente (su `Execute` no corre) y no
  marca continuacion.
- **Error duro corta el turno.** Si `Provider.Stream` o cualquier `AppendEvent`
  (via `Publish`/`ToolSuccess`/`ToolFailed`) devuelve error, `runTurn` lo propaga.
- **Proyeccion lista para el siguiente turno.** Tras el turno,
  `Store.Messages(sessionID, 0)` incluye el mensaje del asistente y los mensajes
  `Role: tool` de los resultados, en orden de `Seq`: el proximo turno (M6) los
  vera en su `Request`.

## 7. Plan TDD

### Safety net

- Estado base verde antes de tocar nada. M5 agrega tipos/campos aditivos en `llm`
  y `session`, candado y metodos en el `Publisher`, y el `Runner`; primero se corre
  lo existente (M0..M4).
- Comando: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Resultado esperado: pasan limpio. Si algo falla, se reporta como preexistente y
  no se sigue a ciegas. Tras los agregados aditivos se re-corre
  `go test ./internal/llm ./internal/session` para confirmar que M2/M4 (llm) y
  M1/M3 (session) siguen verdes.

### Understand

- Leer la entrada M5 del roadmap; "Que hace un turno (`runTurn`)", "Streaming de
  eventos y ejecucion de tools" y "Provider-executed vs local" de
  `docs/atenea-agent-loop.md`; y los contratos ya fijados por M3 (`Publisher`,
  mapa `callID -> toolName`) y M4 (`Materialize`, `Settle`, `Call`).
- Comportamiento esperado: construir el request desde el historial; consumir el
  stream publicando cada evento; asentar tools locales concurrentes y esperar a
  todas; devolver `needsContinuation`; provider-executed solo persiste.

### RED

- Escribir primero el test que falla:
  `TestRunner_TextOnlyTurnPersistsEventsAndStops`. Referencia a `NewRunner`,
  `runTurn` y los campos nuevos, que aun no existen -> no compila -> falla (RED
  honesto en Go es fallo de compilacion del paquete de test).
- El test siembra el `MemoryStore` con un mensaje de usuario
  (`AppendEvent(SessionEvent{Message: &Message{ID:"u1", Role: RoleUser,
  Text:"hola"}})`), arma un `FakeProvider` con el guion `StepStarted,
  Text.Started, Text.Delta("hola "), Text.Delta("mundo"), Text.Ended, StepEnded`,
  un `Registry` con `Echo` y `NewRunner(store, fake, reg, Permissions{"echo":true},
  func() string {return "a1"})`, y afirma:
  - `cont, err := r.runTurn(ctx, "s1")` devuelve `err == nil` y `cont == false`;
  - `Store.Messages(ctx, "s1", 0)` devuelve el usuario y un mensaje del asistente
    `{ID:"a1", Role:assistant, Text:"hola mundo"}`.
- Comando:
  `go test -run TestRunner_TextOnlyTurnPersistsEventsAndStops ./internal/session/runner`
  -> fallo esperado.

### GREEN

- Escribir el minimo: los campos aditivos en `llm` (`Message`, `Request.Messages`,
  `Request.Tools`, `Event.ProviderExecuted`) y `session` (`SessionEvent.Error`);
  el candado y los metodos `ToolSuccess`/`ToolFailed` en `publish.go`; el `Runner`
  en `runner.go`; `runTurn`/`consume`/`toLLMMessages` en `turn.go`.
- Correr solo el test rojo hasta verde.
- Comando:
  `go test -run TestRunner_TextOnlyTurnPersistsEventsAndStops ./internal/session/runner`.

### TRIANGULATE

Agregar casos para evitar falso verde (los del roadmap):

- `TestRunner_LocalToolCallRegistersCalledThenSettlesSuccess`: guion con una
  `ToolCall{CallID:"c1", ToolName:"echo", Input:{"text":"pong"}}`; `Echo`
  permitido. Afirma `cont == true`; que en el log persistido `Tool.Called` (de
  `c1`) aparece **antes** que `Tool.Success` (de `c1`); que `Tool.Success` lleva
  `Text == "pong"`; y que la proyeccion incluye un `Message{ID:"c1", Role:tool,
  Text:"pong"}`.
- `TestRunner_TwoToolCallsSettleConcurrentlyAndTurnWaits` (`-race`): un
  `barrierTool` cuyo `Execute` hace `wg.Done(); wg.Wait()` sobre un
  `sync.WaitGroup` con `Add(2)`; guion con dos `ToolCall` (`c1`, `c2`) a esa tool.
  Si las tools corrieran en serie, la primera bloquearia para siempre en `Wait`
  (deadlock -> timeout): que el test pase prueba la concurrencia. Afirma `cont ==
  true` y que **ambos** `Tool.Success` quedaron persistidos al retornar `runTurn`.
- `TestRunner_LocalToolFailureRecordsToolFailed`: una tool cuyo `Execute` devuelve
  error (p.ej. `Echo` con input invalido `{`, o un spy con `execErr`). Afirma
  `cont == true`, `err == nil` (el fallo es in-band), que se persistio
  `Tool.Failed` con el mensaje en `Error` (y **no** hay `Tool.Success`), y que la
  proyeccion tiene un `Message{Role:tool}` con el texto del error.
- `TestRunner_BuildsRequestFromHistoryAndMaterializedTools`: usar un
  `recordingProvider` (captura el `Request` y delega en un `FakeProvider`). Sembrar
  un mensaje de usuario; permitir `echo`. Tras `runTurn`, afirmar que el `Request`
  capturado tiene `Messages` reflejando el historial (el mensaje de usuario) y
  `Tools` con la definicion de `echo`. Verifica "construir el request desde el
  historial".
- `TestRunner_ProviderExecutedToolIsOnlyPersisted`: guion con una `ToolCall`
  marcada `ProviderExecuted: true` sobre un spy permitido. Afirma que el contador
  de `Execute` del spy quedo en **0** (no se asento localmente), que `Tool.Called`
  se persistio, y que `cont == false` (no hubo tool local que continue).
- Comandos:
  - `go test -run TestRunner ./internal/session/runner`
  - `go test -race -run TestRunner ./internal/session/runner` (la concurrencia
    real del turno y del candado del `Publisher`)

### REFACTOR

- Limpieza sin cambiar comportamiento: factorizar los helpers de test (un
  `seedUser(store, ...)`, un `drain`/lectura del log del `MemoryStore`, un
  `turnFixture` que arme store+fake+registry+runner) si reduce duplicacion; quitar
  `scaffold_test.go` (errgroup ya viene de `turn.go`); actualizar
  `internal/session/runner/doc.go` (el loop `runTurn`/`consume` aterrizo en M5; el
  `Run` externo sigue en M6) y `internal/llm/doc.go` (el `Request` crecio y el
  `Event` sumo `ProviderExecuted`); si aplica, `internal/session/doc.go` (el campo
  `Error`).
- Verificar que la suite sigue verde tras el formateo.
- Comando: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Criterios de aceptacion (Done when)

1. Existe `llm.Message` (`Role`, `Text`) y `Request` tiene `Messages []Message` y
   `Tools []ToolDef`; `Event` tiene `ProviderExecuted bool`. Los agregados son
   aditivos: `Provider`, `Usage` y el `FakeProvider` no cambiaron (M2/M4 verdes).
2. `SessionEvent` tiene `Error string` (aditivo); M1/M3 siguen verdes.
3. El `Publisher` tiene candado y los metodos `ToolSuccess(ctx, callID, output)` y
   `ToolFailed(ctx, callID, cause)`, que consultan el mapa `callID -> toolName` de
   M3 y materializan un `Message{Role: tool, ID: callID}`.
4. Existe `Runner` (+ `NewRunner`) con `Store`, `Provider`, `Registry`,
   `Permissions` y el generador de IDs.
5. `runTurn` construye el `Request` desde `Store.Messages` y `Registry.Materialize`,
   llama `Provider.Stream` **una vez**, consume el stream y devuelve
   `needsContinuation`.
6. Un turno de solo texto persiste sus eventos (con el `Message` coalescido del
   asistente) y devuelve `false`.
7. Una tool call local persiste `Tool.Called` **antes** de ejecutar, publica
   `Tool.Success` con el output (y un `Message{Role: tool}`) y devuelve `true`.
8. Dos tool calls corren concurrentemente y el turno espera a ambas antes de
   retornar; verificado con `-race` y un rendezvous que deadlockea si fuera
   secuencial.
9. Un fallo de tool (`Execute` con error o tool denegada/desconocida) publica
   `Tool.Failed` con el mensaje en `Error`, no produce `Tool.Success` y no hace
   fallar el turno (`runTurn` devuelve `true`, `err == nil`).
10. Una tool call `ProviderExecuted` solo se persiste (`Tool.Called`); no se
    asienta localmente (0 `Execute`) y no marca continuacion.
11. `go test ./...` (y `-race` donde aplica) pasa; `go vet ./...` limpio;
    `gofmt -l .` vacio.
12. No hubo cambios en `app.go`, `main.go`, Wails, el frontend ni
    `internal/event`. En `internal/llm` solo crecieron `provider.go`/`doc.go`; en
    `internal/session` solo `session.go` (campo `Error`) y, si aplica, `doc.go`;
    `memstore.go`/`store.go` intactos. El `scaffold_test.go` de `runner` se
    elimino (errgroup queda anclado por `turn.go`).

## 9. Comandos

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

## 10. Tabla de evidencia esperada

Al cerrar M5, la respuesta/PR debe incluir esta tabla con resultados reales:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0..M4 verde antes de editar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Contrato del turno leido | roadmap M5, `docs/atenea-agent-loop.md`, specs M3/M4 | comportamiento identificado |
| RED | Test de turno de solo texto escrito primero | `turn_test.go` + `go test -run TestRunner_TextOnlyTurnPersistsEventsAndStops ./internal/session/runner` | fallo esperado (no compila) |
| GREEN | Agregados `llm`/`session` + `Publisher` + `runner.go`/`turn.go` minimos | `internal/llm/provider.go`, `internal/session/session.go`, `internal/session/runner/{publish,runner,turn}.go` | test especifico pasa |
| TRIANGULATE | Tool local (Called->Success), dos concurrentes, fallo, request desde historial, provider-executed | `go test -run TestRunner ./internal/session/runner`, `go test -race ...` | casos pasan, `-race` limpio |
| REFACTOR | helpers de test, `scaffold_test.go` retirado, `doc.go` actualizados | `gofmt -w internal`, `go vet ./...`, `go test ./...` | suite verde, M1..M4 intactos |

## 11. Riesgos y decisiones

- **`runTurn` es un solo intento, sin senales de control.** La arquitectura
  muestra `runTurn` como un retry loop sobre `runTurnAttempt` (rebuild,
  post-compaction). M5 implementa el **intento feliz**: lee historial, materializa,
  arma request, `Stream` una vez, consume. El wrapper de reintento y los sentinels
  (`errRebuildTurn`, `errContinueAfterCompaction`) los agrega M7 sin reescribir el
  cuerpo. Adelantarlos seria diseno sin test que los ejerza.
- **`Tool.Success`/`Tool.Failed` materializan un `Message{Role: tool}`.** No basta
  con persistir el evento: el modelo ve el historial via `Store.Messages`, asi que
  el resultado de la tool debe entrar en la proyeccion como un mensaje `tool` para
  que el siguiente turno (M6) lo lleve en su `Request`. Por eso ambos metodos
  setean `Message`. El `ID == callID` enlaza el resultado con su tool call (el
  adaptador real de M10 lo usa como `tool_use_id`).
- **`Error` en `SessionEvent`, introducido en M5.** M3 difirio el campo "hasta que
  un test lo ejerza". El `Tool.Failed` de M5 es ese test; el campo aterriza ahora y
  M8 lo reutiliza para `Step.Failed`. Se prefirio un campo dedicado a meter el
  error en `Text`: un fallo no es contenido de texto, y el frontend (M9) lo
  renderiza distinto.
- **Fallo de tool es in-band, no corta el turno.** `Settle` que devuelve error se
  registra como `Tool.Failed` y la goroutine devuelve el error del **store** (nil
  si el append anduvo), asi que no cancela a las tools hermanas ni hace fallar
  `runTurn`. Solo un fallo duro de `Stream`/`AppendEvent` corta el turno. Distingue
  "la tool fallo" (el modelo reacciona) de "el turno fallo" (M8 maneja lo
  out-of-band). La tool denegada/desconocida cae en este mismo camino via el
  `UnknownToolError` de M4.
- **Candado en el `Publisher`, ahora si.** M3 publicaba en serie y difirio el
  mutex. En M5 las goroutines de `settle` publican `Tool.Success`/`Tool.Failed`
  mientras el loop principal sigue publicando: hay acceso concurrente real al store,
  a los buffers y a los mapas. El candado se toma en `Publish`/`ToolSuccess`/
  `ToolFailed` y serializa los appends en un orden total del log. El test `-race`
  lo ejerce.
- **`ProviderExecuted` como flag aditivo en `llm.Event`.** El roadmap pide
  distinguir provider-executed de local. La forma minima y testeable es un bool en
  el evento de tool call: `consume` salta el `settle` local cuando es `true` y la
  tool solo se persiste. El round-trip completo de un resultado server-side lo
  produce solo un proveedor real (el fake no tiene ese concepto), asi que su prueba
  end-to-end es de M10; M5 fija la **decision de ruteo** (no asentar localmente).
- **El fake ignora el `Request`; el historial se verifica con un recording.** Como
  el `FakeProvider` reproduce su guion sin mirar el `Request`, "construir desde el
  historial" se prueba con un `recordingProvider` de test que captura el `Request`
  y delega en el fake. Asi no se toca el `FakeProvider` de M2 (queda pristino) y se
  evita duplicar la logica de reproduccion del guion.
- **`Request.Messages`/`Tools` ahora; `System`/`ProviderOpts` despues.** El turno
  feliz necesita historial y tools. El system context/baseline y el prompt cache
  key son de M7 (preparacion del turno con epoch/compaction); agregarlos sin esa
  maquinaria seria poblar campos que nadie usa. Sigue el patron de M2/M4 (el
  `Request` crece sin cambiar la interface).
- **`llm.Message` minimo (`Role`/`Text`).** El historial proyectado de M1 es texto
  plano; convertirlo a `llm.Message{Role, Text}` alcanza para el turno feliz. Las
  partes ricas (tool_use/tool_result como bloques) las arma el adaptador real en
  M10 desde el `Role`/`ID`/`Text` del `session.Message`. No se especula el formato
  del SDK en M5.
- **`nextID` inyectado.** El `assistantMessageID` del turno lo genera el runner
  (M3 lo anticipo). Inyectar `nextID func() string` deja los tests deterministas
  (`"a1"`) sin arrastrar una dependencia de UUID/tiempo; M9 cablea un generador
  real. Es el mismo criterio de "fakes deterministas" del resto del plan.
- **`scaffold_test.go` de runner se retira.** M3 lo conservo porque anclaba
  `golang.org/x/sync/errgroup` en `go.mod` hasta que el codigo de produccion lo
  usara. En M5 `turn.go` importa `errgroup` para `consume`, asi que el anclaje ya
  no depende del scaffold: se elimina y se reemplaza por `turn_test.go`.
- **Provider-executed no marca continuacion.** Una tool que el proveedor ejecuto se
  resuelve dentro del mismo stream; no hay nada local que asentar ni un round-trip
  pendiente, asi que no setea `needsContinuation`. La continuacion la disparan solo
  las tool calls **locales** (y, en M6, un `steer` pendiente).

## 12. Fuentes

- Roadmap: `docs/atenea-agent-loop-roadmap.md` (hito M5)
- Arquitectura: `docs/atenea-agent-loop.md` (secciones "Que hace un turno
  (`runTurn`)", "Streaming de eventos y ejecucion de tools" — `consume`, registrar
  antes de los efectos, esperar a todas — y "Provider-executed vs local")
- Manera de trabajo: `AGENTS.md`
- Specs previos: `docs/atenea-m1-tipos-store-spec.md`,
  `docs/atenea-m2-provider-fake-spec.md`, `docs/atenea-m3-publisher-spec.md`,
  `docs/atenea-m4-tool-registry-spec.md`
