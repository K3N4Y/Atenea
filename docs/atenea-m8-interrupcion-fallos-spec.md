# Spec M8 — Interrupcion + manejo de fallos

Spec ejecutable del hito **M8** de `docs/atenea-agent-loop-roadmap.md`. Define el
estado final, el alcance, el plan TDD y los criterios de aceptacion para endurecer
el turno de M5/M6/M7 contra **interrupciones y fallos**: la cancelacion del `ctx`
a mitad de turno, los errores del stream del proveedor, las tools sin resolver y la
limpieza de restos tras un crash. La regla central: **tras cualquier fallo, el
historial no queda con tools colgadas** y el turno marca su fracaso con
`Step.Failed`. Una tool esta "colgada" cuando tiene un `Tool.Called` durable sin un
`Tool.Success` ni un `Tool.Failed` que lo cierre; M8 cierra ese estado ambiguo en
cada ruta de fallo y al reanudar.

El turno de M7 (`runTurnAttempt` + `consume`) ya asienta tools locales con
`errgroup` y maneja las senales de control. M8 agrega los caminos explicitos de
fallo: `consume` cierra las tools no resueltas y emite `Step.Failed` cuando el
turno se interrumpe o el proveedor falla, usando un contexto **desacoplado** de la
cancelacion para que esas escrituras de cierre sobrevivan al `ctx` cancelado; y
`Run` corre `failInterruptedTools` al arrancar para cerrar tools que un crash dejo
a medias.

Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos, igual que el resto de `docs/`.

## 1. Contexto

Los hitos previos dejaron, de adentro hacia afuera, todas las piezas que el turno
ensambla, que el loop externo orquesta y que las senales de control protegen:

- **M1** dejo el dominio durable (`Seq`, `Message`, `Role`, `SessionEvent`,
  `Store`, `MemoryStore`): el log de eventos es la unica fuente de verdad y los
  mensajes son una proyeccion derivada (`Store.Messages(sinceSeq)`).
- **M2** dejo la frontera con el modelo (`llm.Provider`, `llm.Request`,
  `llm.Event`, `llm.EventKind`, `llm.Usage`) y un `FakeProvider` replayable que
  reproduce un guion determinista por un channel y lo **cierra** al terminar.
  Cancelar `ctx` corta el stream (cut determinista) y cierra el channel igual:
  ningun receptor queda colgado. El kind `llm.StepFailed` ("fallo del stream") ya
  existe en el contrato de M2, anticipando que M8 lo consumiria.
- **M3** dejo el `Publisher` (`internal/session/runner/publish.go`): traduce cada
  `llm.Event` a un `SessionEvent` durable, bufferiza los deltas y mantiene el mapa
  `callID -> toolName` del turno. `SessionEvent` ya lleva el campo `Error` (el
  mensaje de un `Tool.Failed`); el comentario de M1 anticipo que "M8 lo reutiliza
  para `Step.Failed`". Las constantes `KindStepFailed` ("lo emite M8") y
  `KindToolFailed` ("lo emite el runner en M5/M8") ya estan en `event.go`.
- **M4** dejo el `tool.Registry` (`Materialize(perms) -> {Definitions, Settle}`),
  el `ToolOutputStore` y el builtin `Echo`. `Settle` **propaga** el error de
  `Execute` (incluido un `ctx.Err()` cuando la tool honra la cancelacion).
- **M5** dejo el **turno** (`internal/session/runner/turn.go`): `consume` consume
  el stream con el `Publisher` y asienta las tool calls locales concurrentemente
  con `errgroup`; cada goroutine publica `Tool.Success` o, ante un error de
  `Settle`, `Tool.Failed` (fallo in-band: no corta el turno). Una tool
  provider-executed solo se persiste.
- **M6** dejo el **loop externo** (`internal/session/runner/run.go`): el `Inbox`
  durable y el doble loop (actividad + pasos, `MaxSteps = 25`). `Run` dejo un
  hueco explicito: `// failInterruptedTools (limpieza de tools colgadas tras
  crash) entra en M8` (run.go), justo despues del chequeo de idle y antes de
  promover.
- **M7** dejo las **senales de control** (`errRebuildTurn`,
  `errContinueAfterCompaction`) y el `ContextEpoch`: `runTurn` es un retry loop
  sobre `runTurnAttempt`, que snapshotea el epoch al preparar, lo re-chequea antes
  de `Stream` y reconstruye o compacta sin streamear estado viejo.

El siguiente ladrillo es el **manejo explicito de fallos**. Su responsabilidad
(ver `docs/atenea-agent-loop.md`, "Interrupciones y manejo de fallos", y
`docs/opencode-agent-loop.md`) es no dejar el historial en estado ambiguo cuando
algo sale mal:

- **interrupcion**: el usuario cancela el `ctx` del turno (un boton "stop" en M9);
  las tools en vuelo se interrumpen y se cierran con `Tool.Failed`, el turno emite
  `Step.Failed` y devuelve el error de cancelacion;
- **error de proveedor**: el stream reporta un fallo (un `llm.StepFailed`) o
  `Stream` falla; el turno emite `Step.Failed` y cierra las tools no resueltas;
- **tool sin resolver**: una tool que el proveedor marco ejecutada pero que el
  turno fallido nunca resolvio se cierra con `Tool.Failed`;
- **reanudacion tras crash**: `failInterruptedTools`, al inicio de `Run`, cierra
  las tools que quedaron `Tool.Called` sin resultado en el log durable de una
  corrida anterior.

M8 construye y prueba esto **contra fakes**: una tool de test que bloquea hasta
que el `ctx` se cancela, un `FakeProvider` que emite un `llm.StepFailed`, y
eventos `Tool.Called` sembrados a mano en el store para simular el crash. No toca
Wails, ni el proveedor real, ni la persistencia SQLite.

## 2. Objetivo

Dejar listo el turno con caminos de fallo y la reanudacion tras crash:

En `internal/session` (la foto del contexto):

- el tipo `PendingTool` (`CallID`, `ToolName string`): una tool call del log
  durable que quedo sin cerrar (`internal/session/store.go`, junto a la interface);
- el metodo `PendingToolCalls(ctx, sessionID) ([]PendingTool, error)` en la
  interface `Store` y su implementacion en `MemoryStore` (proyecta sobre el log los
  `Tool.Called` sin un `Tool.Success`/`Tool.Failed` posterior).

En `internal/session/runner`:

- el tipo de error `ProviderError` (`Message string`): el fallo del stream del
  proveedor, distinguible con `errors.As` por la UI (M9), igual que
  `StepLimitExceededError` de M6;
- el `Publisher` gana, de forma aditiva, `StepFailed(ctx, cause)`,
  `FailUnresolvedTools(ctx, cause)` y un set `settled` (los `callID` ya cerrados
  con `Tool.Success`/`Tool.Failed`), para cerrar el estado ambiguo de un turno
  fallido;
- `consume` cierra las tools no resueltas y emite `Step.Failed` cuando el turno
  fracasa (interrupcion, error de stream), usando un contexto **desacoplado** de la
  cancelacion (`context.WithoutCancel`) para las escrituras de cierre; devuelve el
  error del fallo (la cancelacion como `context.Canceled`, el fallo de stream como
  `*ProviderError`);
- `failInterruptedTools(ctx, sessionID)` en `run.go` y su llamada al inicio de
  `Run` (el hueco que M6 dejo);
- tests de comportamiento que ejercen la interrupcion, el error de proveedor, la
  tool provider-executed sin resolver y la limpieza tras crash.

M8 **no** cambia las senales de control de M7, ni el `Inbox`, ni el doble loop de
`Run` (solo descomenta `failInterruptedTools`), ni toca `internal/llm` (el kind
`StepFailed` ya existe), ni agrega Wails o el provider/store reales.

## 3. Alcance

### Dentro

- `internal/session/store.go`: el tipo `PendingTool` y el metodo `PendingToolCalls`
  en la interface `Store`.
- `internal/session/memstore.go`: `MemoryStore.PendingToolCalls` (proyeccion de
  tools colgadas sobre el log; `ErrSessionNotFound` si la sesion no existe).
- `internal/session/runner/turn.go`: el tipo `ProviderError`; `consume` gana el
  cierre del estado ambiguo (cierre de tools no resueltas + `Step.Failed`) en las
  rutas de fallo, con `context.WithoutCancel` para las escrituras de cierre.
  `runTurn`/`runTurnAttempt` (retry loop + attempt de M7) **intactos**:
  `consume` propaga el error de fallo y el attempt lo devuelve tal cual (no es una
  senal de control, asi que `runTurn` no reintenta).
- `internal/session/runner/publish.go`: `StepFailed`, `FailUnresolvedTools`, el set
  `settled` y el helper privado `failTool` (aditivos; `ToolSuccess`/`ToolFailed`
  marcan `settled`).
- `internal/session/runner/run.go`: `failInterruptedTools` y su llamada al inicio
  de `Run` (descomenta el hueco de M6). El cuerpo del doble loop no cambia.
- Tests de comportamiento en `internal/session/runner/turn_failure_test.go`
  (nuevo): interrupcion por `ctx`, error de proveedor, tool provider-executed sin
  resolver y `failInterruptedTools`. Un test del contrato de
  `MemoryStore.PendingToolCalls` en `internal/session/memstore_test.go`.
- Actualizar `internal/session/runner/doc.go` (la interrupcion y el manejo de
  fallos aterrizaron) y `internal/session/doc.go` (`PendingToolCalls` aterrizo).

### Fuera (no hacer en M8)

- El **resultado** de una tool provider-executed (el evento con el que el proveedor
  devuelve el output de una tool que ejecuto el). M8 **no** lo modela: en el camino
  feliz una tool provider-executed se persiste como `Tool.Called` y nada mas (igual
  que M5). M8 cierra una provider-executed solo cuando el **turno fallo** (la dejo
  sin resolver) o cuando un crash la dejo colgada (`failInterruptedTools`). Cerrar
  una provider-executed en cada cierre limpio de turno requeriria primero modelar su
  evento de resultado, y eso llega con el adaptador real — **M10**. Ver "Riesgos y
  decisiones".
- **Reintentos/backoff** del proveedor ante un fallo transitorio. M8 reporta el
  fallo (`*ProviderError`, `Step.Failed`) y corta el turno; la politica de reintento
  (cuantas veces, con que espera) llega con el adaptador real — **M10**.
- **Pregunta rechazada por el usuario** (una tool que pide permiso y el usuario
  niega). La arquitectura la lista como una ruta de fallo, pero requiere una UI de
  permisos que aun no existe; los permisos de M4 son estaticos (`tool.Permissions`).
  Llega con la app — **M9**.
- `EventBus`, `runtime.EventsEmit`, `app.go`, `main.go`, Wails, frontend, y el
  cableado del boton "stop" que cancela el `ctx` — **M9**. M8 prueba la interrupcion
  cancelando el `ctx` directo en el test, no desde la UI.
- `Store` SQLite, `Inbox` persistente y el adaptador `Provider` real — **M10**. Los
  tests de M1..M8 deben seguir verdes con el store real.

## 4. Agregados al contrato (`session`)

### `internal/session/store.go` (tipo + metodo nuevos en la interface)

`failInterruptedTools` necesita saber que tools quedaron a medias en el log durable
de una corrida anterior. Esa respuesta es una **proyeccion** sobre el log (igual que
`Messages`): los `Tool.Called` que no tienen un `Tool.Success` ni un `Tool.Failed`
con el mismo `callID`. Se expone en el `Store`, no se reconstruye en el runner,
porque el log durable es del store y `PendingToolCalls` es del mismo tipo que
`Messages` (una vista derivada del log).

```go
// PendingTool es una tool call que quedo registrada (Tool.Called) sin un resultado
// (ni Tool.Success ni Tool.Failed) en el log durable: tipicamente porque un crash o
// una interrupcion cortaron la corrida antes de cerrarla. Run la cierra al arrancar
// con failInterruptedTools.
type PendingTool struct {
	CallID   string
	ToolName string
}

type Store interface {
	AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error)
	LoadSession(ctx context.Context, sessionID string) (Session, error)
	Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error)
	Epoch(ctx context.Context, sessionID string) (ContextEpoch, error)

	// PendingToolCalls proyecta sobre el log durable las tool calls colgadas: las
	// que tienen un Tool.Called sin un Tool.Success ni un Tool.Failed posterior, en
	// orden de Called. Run las usa al arrancar para cerrar restos de una corrida
	// previa. ErrSessionNotFound si la sesion no existe.
	PendingToolCalls(ctx context.Context, sessionID string) ([]PendingTool, error)
}
```

### `internal/session/memstore.go` (proyeccion sobre el log)

```go
// PendingToolCalls recorre el log de la sesion y devuelve las tool calls que tienen
// un Tool.Called sin un Tool.Success ni Tool.Failed con el mismo CallID: las tools
// que quedaron a medias (crash/interrupcion). Mantiene el orden de Called.
// ErrSessionNotFound si la sesion no existe.
func (s *MemoryStore) PendingToolCalls(ctx context.Context, sessionID string) ([]PendingTool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log, ok := s.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	resolved := make(map[string]bool)
	for _, ev := range log {
		if ev.Kind == KindToolSuccess || ev.Kind == KindToolFailed {
			resolved[ev.CallID] = true
		}
	}
	var pending []PendingTool
	for _, ev := range log {
		if ev.Kind == KindToolCalled && !resolved[ev.CallID] {
			pending = append(pending, PendingTool{CallID: ev.CallID, ToolName: ev.ToolName})
		}
	}
	return pending, nil
}
```

Nota: un `Tool.Called` cuyo resultado llego (`Tool.Success`/`Tool.Failed`) **no**
es pending; uno sin resultado si. El orden es el de aparicion del `Tool.Called`.

## 5. El turno con manejo de fallos (`runner`)

### `internal/session/runner/turn.go` (ProviderError + cierre del estado ambiguo)

```go
// ProviderError lo devuelve runTurn cuando el stream del proveedor reporta un fallo
// (un llm.StepFailed en el stream). Tipo (no sentinel) para que la UI (M9) lo
// distinga de una interrupcion (context.Canceled) o de un StepLimitExceededError con
// errors.As y muestre el mensaje del proveedor. No es una senal de control: escapa de
// runTurn (el retry loop no lo traga).
type ProviderError struct{ Message string }

func (e *ProviderError) Error() string {
	if e.Message == "" {
		return "provider stream failed"
	}
	return "provider stream failed: " + e.Message
}
```

`consume` gana el cierre del estado ambiguo. El esqueleto de M5/M7 (rango del
stream, goroutines de settle con `errgroup`, `g.Wait`) se conserva; lo nuevo es:
detectar el fallo (un `llm.StepFailed` en el stream, un error de `g.Wait`, o el
`ctx` cancelado), cerrar las tools no resueltas y emitir `Step.Failed`, todo con un
contexto **desacoplado** de la cancelacion para que las escrituras de cierre no se
pierdan cuando el `ctx` del turno ya esta cancelado.

```go
func (r *Runner) consume(ctx context.Context, in <-chan llm.Event,
	pub *Publisher, settle tool.SettleFunc) (bool, error) {

	// Las escrituras de CIERRE (Tool.Failed de una tool interrumpida, Step.Failed)
	// deben sobrevivir a la cancelacion del turno: si usaran el ctx cancelado, un
	// store real las rechazaria y el historial quedaria ambiguo. Se desacoplan de la
	// cancelacion (conservan valores/deadline, pierden el Done).
	cleanupCtx := context.WithoutCancel(ctx)

	g, gctx := errgroup.WithContext(ctx)
	needsContinuation := false
	var streamErr error
	for ev := range in {
		if ev.Kind == llm.StepFailed {
			streamErr = &ProviderError{Message: ev.Text} // fallo del proveedor: lo cierra al final
			continue
		}
		if err := pub.Publish(ctx, ev); err != nil {
			return false, err
		}
		if ev.Kind == llm.ToolCall && !ev.ProviderExecuted {
			ev := ev
			needsContinuation = true
			g.Go(func() error {
				res, err := settle(gctx, tool.Call{ID: ev.CallID, Name: ev.ToolName, Input: ev.Input})
				if err != nil {
					// El cierre de la tool usa cleanupCtx: si la cancelacion fue lo que
					// la corto, el Tool.Failed igual se persiste.
					return pub.ToolFailed(cleanupCtx, ev.CallID, err)
				}
				return pub.ToolSuccess(cleanupCtx, ev.CallID, res.Output)
			})
		}
	}
	waitErr := g.Wait()
	if waitErr != nil {
		return false, waitErr // fallo del store al publicar un resultado: error duro
	}

	// Causa del fallo del turno, si la hubo: error de stream o interrupcion. (waitErr
	// ya se devolvio arriba: solo es un fallo de store, no un cierre de tool.)
	cause := streamErr
	if cause == nil && ctx.Err() != nil {
		cause = ctx.Err()
	}
	if cause != nil {
		if err := pub.FailUnresolvedTools(cleanupCtx, cause); err != nil {
			return false, err
		}
		if err := pub.StepFailed(cleanupCtx, cause); err != nil {
			return false, err
		}
		return false, cause
	}
	return needsContinuation, nil
}
```

Notas:

- **`runTurn`/`runTurnAttempt` no cambian.** El fallo que `consume` devuelve
  (`context.Canceled` o `*ProviderError`) **no** es una senal de control: el
  `switch` de `runTurn` cae en `default` y lo devuelve sin reintentar. Esto es
  importante: una interrupcion **no** se reintenta (el usuario corto a proposito) y
  un error de proveedor tampoco (M8 no reintenta; eso es M10). El attempt sigue
  devolviendo el error de `consume` tal cual.
- **El cierre de tools usa `cleanupCtx`.** Las goroutines de settle publican su
  resultado con `cleanupCtx` (no con `ctx`): si la cancelacion corto la tool, su
  `Tool.Failed` igual se escribe. Las del camino feliz (sin cancelacion) no cambian:
  `WithoutCancel` de un ctx vigente se comporta igual. `Publish` del stream sigue con
  `ctx`: si el `ctx` se cancela, el `FakeProvider` ya corto el stream, asi que no hay
  mas eventos que publicar; lo unico que importa preservar es el cierre.
- **`Step.Failed` cierra el turno fallido.** Ante interrupcion o error de stream, el
  turno emite un `Step.Failed` durable (con el mensaje de la causa en `Error`) para
  que el log marque que ese turno fracaso. Es el contrapunto de `Step.Ended`.
- **Tools no resueltas.** `FailUnresolvedTools` cierra cualquier `Tool.Called` del
  turno que no haya recibido `Tool.Success`/`Tool.Failed`: en la practica, una tool
  provider-executed que el turno fallido nunca resolvio (las locales se cierran solas
  en su goroutine). El set `settled` del `Publisher` evita doble cierre.
- **Fallo de store = error duro.** Si `g.Wait` devuelve un error (solo ocurre si
  `AppendEvent` fallo al publicar un resultado de tool), `consume` lo propaga sin
  intentar mas escrituras: el store esta roto, no hay como cerrar nada.

### `internal/session/runner/publish.go` (StepFailed + FailUnresolvedTools + settled)

```go
type Publisher struct {
	// ...campos de M3/M5...
	settled map[string]bool // callID -> ya cerrado (Tool.Success o Tool.Failed)
}

// NewPublisher inicializa tambien settled (ademas de input/tools).

// StepFailed persiste un Step.Failed durable con el mensaje de la causa: marca que
// el turno fracaso (interrupcion o error de proveedor). No materializa Message: es un
// marcador de turno, no parte de la conversacion que ve el modelo.
func (p *Publisher) StepFailed(ctx context.Context, cause error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.emit(ctx, session.SessionEvent{Kind: session.KindStepFailed, Error: cause.Error()})
}

// FailUnresolvedTools cierra con Tool.Failed cada tool call del turno (mapa tools)
// que aun no se haya cerrado (no esta en settled): tipicamente una provider-executed
// que el turno fallido nunca resolvio. Usa el mensaje de la causa. El orden de cierre
// no esta garantizado (iteracion de mapa); cada cierre es independiente.
func (p *Publisher) FailUnresolvedTools(ctx context.Context, cause error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for callID := range p.tools {
		if p.settled[callID] {
			continue
		}
		if err := p.failTool(ctx, callID, cause.Error()); err != nil {
			return err
		}
	}
	return nil
}

// failTool persiste un Tool.Failed (con su Message{Role: tool}) y marca el callID
// como cerrado. Asume el candado tomado: lo comparten ToolFailed (publico) y
// FailUnresolvedTools.
func (p *Publisher) failTool(ctx context.Context, callID, msg string) error {
	p.settled[callID] = true
	return p.emit(ctx, session.SessionEvent{
		Kind:     session.KindToolFailed,
		CallID:   callID,
		ToolName: p.tools[callID],
		Error:    msg,
		Message:  &session.Message{ID: callID, Role: session.RoleTool, Text: msg},
	})
}
```

`ToolSuccess` y `ToolFailed` de M5 marcan `settled[callID] = true` (ToolFailed via
`failTool`; ToolSuccess agrega la marca antes de emitir). Asi `FailUnresolvedTools`
no re-cierra una tool que ya se asento.

### `internal/session/runner/run.go` (failInterruptedTools)

```go
// failInterruptedTools cierra, al arrancar Run, las tools que un crash o una
// interrupcion dejaron colgadas en una corrida anterior: un Tool.Called durable sin
// resultado. Por cada una apendea un Tool.Failed (con su Message{Role: tool}) para
// que el historial no quede ambiguo y el modelo vea, en el proximo turno, que esa
// tool no completo. Una sesion sin eventos previos (ErrSessionNotFound) no tiene nada
// que limpiar.
func (r *Runner) failInterruptedTools(ctx context.Context, sessionID string) error {
	pending, err := r.store.PendingToolCalls(ctx, sessionID)
	if errors.Is(err, session.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, pt := range pending {
		if _, err := r.store.AppendEvent(ctx, sessionID, session.SessionEvent{
			Kind:     session.KindToolFailed,
			CallID:   pt.CallID,
			ToolName: pt.ToolName,
			Error:    interruptedToolMsg,
			Message:  &session.Message{ID: pt.CallID, Role: session.RoleTool, Text: interruptedToolMsg},
		}); err != nil {
			return err
		}
	}
	return nil
}

const interruptedToolMsg = "tool interrumpida antes de completar"
```

Y en `Run`, el hueco de M6 se descomenta (despues del chequeo de idle, antes de
promover):

```go
	if !force && !hasSteer && !hasQueue {
		return nil // sesion idle, nada que hacer
	}

	if err := r.failInterruptedTools(ctx, sessionID); err != nil {
		return err
	}
```

Notas:

- **`failInterruptedTools` corre una vez por `Run`, antes del primer turno.** Limpia
  el estado durable heredado; las tools de la corrida actual se cierran dentro de su
  turno (`consume`), no aca.
- **Escribe con el `ctx` de `Run`** (no cancelado en el arranque normal): no necesita
  `cleanupCtx`, a diferencia de `consume`.
- **`ErrSessionNotFound` no es un fallo.** Una sesion nueva (su primer prompt aun en
  el inbox, sin eventos en el store) no tiene tools colgadas; se trata como "nada que
  limpiar". Los tests de `Run` de M6 (sesiones que arrancan con un queue) siguen
  verdes: `PendingToolCalls` devuelve vacio o `ErrSessionNotFound`.

## 6. Semantica del turno con manejo de fallos

El contrato que M8 fija (y que M9/M10 conservan):

- **Camino feliz intacto.** Sin interrupcion ni `llm.StepFailed`, `consume` no
  detecta causa de fallo, no emite `Step.Failed` ni cierra tools, y devuelve
  `needsContinuation` igual que M5/M7. Toda la suite previa sigue verde, incluido el
  test de tool provider-executed de M5 (cierre limpio: la provider-executed queda
  como `Tool.Called` y M8 no la cierra, porque el turno no fallo).
- **Interrupcion (`ctx` cancelado a mitad de turno).** Las tools locales en vuelo
  reciben `gctx` cancelado: su `Settle` devuelve `ctx.Err()` y la goroutine las cierra
  con `Tool.Failed` (escrito con `cleanupCtx`, durable pese a la cancelacion). El
  turno emite `Step.Failed` y `consume` devuelve `context.Canceled`; `runTurn` lo
  propaga (no es senal de control). Tras la interrupcion no quedan tools colgadas.
- **Error de proveedor (`llm.StepFailed` en el stream).** `consume` registra la causa
  (`*ProviderError` con el mensaje del evento), cierra las tools no resueltas, emite
  `Step.Failed` y devuelve el `*ProviderError`. Si `Stream` falla de entrada (antes de
  cualquier evento), el attempt devuelve ese error tal cual, sin `Step.Failed` (el
  turno no llego a arrancar). 
- **Tool provider-executed sin resolver.** En un turno **fallido** (interrupcion o
  error de stream), una tool provider-executed que el proveedor marco ejecutada pero
  nunca resolvio se cierra con `Tool.Failed` via `FailUnresolvedTools`. No queda
  colgada.
- **Reanudacion tras crash.** Al arrancar, `Run` cierra con `Tool.Failed` cada
  `Tool.Called` sin resultado que un crash dejo en el log durable
  (`failInterruptedTools`). El proximo turno ve esos cierres en el historial.
- **Las escrituras de cierre sobreviven a la cancelacion.** Cierre de tools y
  `Step.Failed` usan `context.WithoutCancel(ctx)`: aunque el turno se interrumpio,
  el historial queda cerrado, no ambiguo. Ese es el criterio "Done when" del hito.
- **Error duro de store corta el turno.** Si `AppendEvent` falla al publicar un
  resultado de tool, `g.Wait` lo propaga y `consume` lo devuelve sin mas escrituras.

## 7. Plan TDD

### Safety net

- Estado base verde antes de tocar nada. M8 agrega un tipo y un metodo de interface
  en `session` (`PendingTool`, `Store.PendingToolCalls`), crece `consume`/`Publisher`
  y descomenta `failInterruptedTools`; primero se corre lo existente (M0..M7).
- Comando: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Resultado esperado: pasan limpio. Al sumar `PendingToolCalls` a la interface
  `Store`, el unico implementador real es `MemoryStore` (los decoradores de test
  `recordingStore`, `epochFlipStore` lo **embeben** y heredan el metodo); los fakes
  del `Publisher` (`recordingAppender`/`failingAppender`) implementan solo
  `eventAppender` (un metodo), no `Store`, asi que no se rompen. Tras agregar
  `MemoryStore.PendingToolCalls` se re-corre `go build ./...` y
  `go test ./internal/session/...` para confirmar que M1..M7 siguen verdes.

### Understand

- Leer la entrada M8 del roadmap; "Interrupciones y manejo de fallos" y la lista
  numerada de `runTurnAttempt` (paso 17: "Maneja fallos, interrupciones, context
  overflow y tools sin resolver") de `docs/atenea-agent-loop.md`; el manejo de fallos
  de `docs/opencode-agent-loop.md`; el contrato de `consume` de M5 (goroutines de
  settle, `Tool.Failed` in-band) y el hueco que M6 dejo en `Run`.
- Comportamiento esperado: cancelar `ctx` cierra tools en vuelo y emite
  `Step.Failed`; un `llm.StepFailed` corta el turno con `*ProviderError` cerrando
  tools no resueltas; `failInterruptedTools` limpia restos al arrancar.

### RED

- Escribir primero el test que falla:
  `TestRunner_CancelDuringTurnFailsInFlightTool`. Referencia simbolos que aun NO
  existen (`pub.StepFailed`/`FailUnresolvedTools`, el cierre en `consume`,
  `context.WithoutCancel` en el codigo) y un comportamiento nuevo: en Go el cambio de
  comportamiento es el que falla (no necesariamente compilacion), asi que se corre y
  se captura el fallo.
- El test usa una `blockingTool` de test: su `Execute` cierra un channel `started`
  (avisa que arranco) y luego bloquea en `<-ctx.Done()`, devolviendo `ctx.Err()`. El
  `FakeProvider` guiona `StepStarted, ToolCall c1 (blocking), StepEnded`. El test:
  arranca `r.runTurn` en una goroutine con un `ctx` cancelable, espera `<-started`,
  cancela el `ctx`, y recoge `(cont, err)`. Afirma:
  - `errors.Is(err, context.Canceled)`: la interrupcion se propaga;
  - el log durable tiene un `Tool.Failed` de `c1` y **ningun** `Tool.Success` de `c1`
    (la tool en vuelo se cerro pese a la cancelacion);
  - el log tiene un `Step.Failed` (el turno marco su fracaso);
  - `store.PendingToolCalls` de la sesion devuelve **vacio** (no quedan colgadas).
- Comando:
  `go test -run TestRunner_CancelDuringTurnFailsInFlightTool ./internal/session/runner`
  -> fallo esperado.

### GREEN

- Escribir el minimo: `PendingTool` + `Store.PendingToolCalls` (interface y
  `MemoryStore`); `ProviderError`, el cierre del estado ambiguo en `consume`
  (`cleanupCtx`, deteccion de causa, `FailUnresolvedTools`, `StepFailed`); el set
  `settled` y los metodos `StepFailed`/`FailUnresolvedTools`/`failTool` en el
  `Publisher`; `failInterruptedTools` y su llamada en `Run`.
- Correr solo el test rojo hasta verde.
- Comando:
  `go test -run TestRunner_CancelDuringTurnFailsInFlightTool ./internal/session/runner`.

### TRIANGULATE

Agregar casos para evitar falso verde (los del roadmap):

- `TestRunner_ProviderStreamErrorEmitsStepFailed`: `FakeProvider` con guion
  `StepStarted`, `StepFailed{Text: "boom"}` (sin tools). Afirma que `runTurn` devuelve
  un error que `errors.As(err, *ProviderError)` reconoce con `Message` conteniendo
  `"boom"`, que el log tiene un `Step.Failed` con ese mensaje en `Error`, y que no hay
  tools colgadas. Aisla la ruta de error de proveedor.
- `TestRunner_ProviderExecutedToolNeverResolvesIsClosed`: guion `StepStarted`,
  `ToolCall c1 {ProviderExecuted: true}`, `StepFailed{Text: "boom"}`. La
  provider-executed solo se persiste (no se asienta) y el stream falla antes de
  resolverla. Afirma: `runTurn` devuelve `*ProviderError`; el log tiene el
  `Tool.Called` de `c1` **y** un `Tool.Failed` de `c1` (la provider-executed sin
  resolver se cerro); `PendingToolCalls` vacio. Verifica "tool marcada ejecutada por
  el provider que nunca resuelve".
- `TestRunner_RunFailsInterruptedToolsBeforeTurn`: siembra a mano en el store una
  sesion con un `Message{Role: user}` y un `Tool.Called` de `c1` (sin resultado:
  simula el crash). `Admit` un prompt en `queue`, arma un `Runner` con un
  `recordingProvider` de solo texto y corre `r.Run(ctx, "s1", false)`. Afirma:
  - tras `Run`, `store.PendingToolCalls("s1")` devuelve **vacio** (la colgada se
    cerro);
  - la proyeccion contiene un `Message{ID: c1, Role: tool}` con el texto de
    interrupcion (el modelo vera que `c1` no completo);
  - el turno nuevo corrio (hay un mensaje de asistente). Verifica
    "`failInterruptedTools` al inicio limpia restos de una corrida previa".
- (Contrato del store) en `internal/session/memstore_test.go`,
  `TestMemoryStore_PendingToolCallsFindsUnresolved`: en una sesion con `Tool.Called
  c1`, `Tool.Called c2`, `Tool.Success c2`, `PendingToolCalls` devuelve solo `c1`;
  una sesion inexistente devuelve `ErrSessionNotFound`; una sesion sin tools devuelve
  vacio. Fija el contrato que `failInterruptedTools` asume.
- Comandos:
  - `go test -run 'TestRunner_Cancel|TestRunner_ProviderStream|TestRunner_ProviderExecuted|TestRunner_RunFailsInterrupted|TestMemoryStore_PendingToolCalls' ./internal/session/...`
  - `go test -race -run 'TestRunner_Cancel|TestRunner_ProviderExecuted' ./internal/session/runner`
    (la interrupcion y el cierre concurrente ejercen `errgroup`, el candado del
    `Publisher` y `cleanupCtx`: el caso critico de `-race`).

### REFACTOR

- Limpieza sin cambiar comportamiento: factorizar la `blockingTool` y los helpers de
  siembra (`seedToolCalled`) si reducen duplicacion; reutilizar
  `seedUser`/`recordingProvider`/`idCounter`/`newRecordingStore`/`seqOfKind` de los
  tests de M5/M6/M7 donde aplique. Actualizar `internal/session/runner/doc.go` (la
  interrupcion por `ctx`, el cierre de tools no resueltas, `Step.Failed` y
  `failInterruptedTools` aterrizaron en M8; M9/M10 conectan Wails y lo real) y
  `internal/session/doc.go` (`PendingToolCalls` aterrizo: la proyeccion de tools
  colgadas para la reanudacion tras crash).
- Verificar que la suite sigue verde tras el formateo.
- Comando: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Criterios de aceptacion (Done when)

1. Existen `session.PendingTool` (`CallID`, `ToolName`) y
   `Store.PendingToolCalls(ctx, sessionID) ([]PendingTool, error)`; `MemoryStore` lo
   implementa proyectando los `Tool.Called` sin resultado (`ErrSessionNotFound` si la
   sesion no existe). M1..M7 siguen verdes.
2. Existe `ProviderError` (`Message string`) en `runner`, distinguible con
   `errors.As`; no es una senal de control (escapa de `runTurn`).
3. El `Publisher` tiene `StepFailed`, `FailUnresolvedTools` y el set `settled`;
   `ToolSuccess`/`ToolFailed` marcan `settled`; `FailUnresolvedTools` no re-cierra una
   tool ya asentada.
4. Cancelar el `ctx` a mitad de turno cierra las tools en vuelo con `Tool.Failed`
   (escrito con `context.WithoutCancel`, durable pese a la cancelacion), emite
   `Step.Failed` y hace que `runTurn` devuelva `context.Canceled` (verificado con
   `errors.Is`). Tras la interrupcion `PendingToolCalls` esta vacio.
5. Un `llm.StepFailed` en el stream hace que `runTurn` devuelva un `*ProviderError`
   con el mensaje del evento, emita `Step.Failed` y cierre las tools no resueltas.
6. Una tool provider-executed sin resolver en un turno fallido se cierra con
   `Tool.Failed` (no queda colgada).
7. `Run` corre `failInterruptedTools` al arrancar (el hueco de M6): cierra con
   `Tool.Failed` cada `Tool.Called` sin resultado del log durable y materializa su
   `Message{Role: tool}`; una sesion sin eventos no falla.
8. El camino feliz (sin interrupcion ni `StepFailed`) no emite `Step.Failed` ni
   cierra tools: el turno se comporta como M5/M7 (sus tests verdes sin modificar,
   incluido el de tool provider-executed, que en un cierre limpio no se cierra).
9. `go test ./...` (y `-race` donde aplica) pasa; `go vet ./...` limpio;
   `gofmt -l .` vacio.
10. No hubo cambios en `app.go`, `main.go`, Wails, el frontend ni `internal/event`.
    En `internal/llm` no se toco nada (el kind `StepFailed` ya existia). En
    `internal/session` solo se agrego `PendingTool` y `PendingToolCalls` en
    `store.go`/`memstore.go` y se actualizo `doc.go`; `session.go`, `event.go`,
    `epoch.go`, `inbox.go` intactos. En `runner` crecieron `turn.go` (`ProviderError`
    + cierre en `consume`), `publish.go` (`StepFailed`/`FailUnresolvedTools`/`settled`)
    y `run.go` (`failInterruptedTools` + su llamada) y `doc.go`; `runner.go` intacto.
    Las senales de control de M7 (`runTurn`/`runTurnAttempt`) no cambiaron.

## 9. Comandos

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Tras sumar PendingToolCalls a la interface Store, confirmar que compila y que M1..M7 siguen verdes
go build ./...
go test ./internal/session/...

# Ciclo (test especifico primero)
go test -run TestRunner_CancelDuringTurnFailsInFlightTool ./internal/session/runner

# Triangulacion (error de proveedor, provider-executed sin resolver, failInterruptedTools, contrato del store)
go test -run 'TestRunner_Cancel|TestRunner_ProviderStream|TestRunner_ProviderExecuted|TestRunner_RunFailsInterrupted|TestMemoryStore_PendingToolCalls' ./internal/session/...

# Concurrencia real (interrupcion + cierre concurrente: errgroup, candado del Publisher, cleanupCtx)
go test -race -run 'TestRunner_Cancel|TestRunner_ProviderExecuted' ./internal/session/runner

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
```

## 10. Tabla de evidencia esperada

Al cerrar M8, la respuesta/PR debe incluir esta tabla con resultados reales:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0..M7 verde antes de editar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Contrato de interrupcion y fallos leido | roadmap M8, `docs/atenea-agent-loop.md` (interrupciones/manejo de fallos), `docs/opencode-agent-loop.md`, `consume` de M5, hueco de M6 | comportamiento identificado |
| RED | Test de interrupcion por ctx escrito primero | `turn_failure_test.go` + `go test -run TestRunner_CancelDuringTurnFailsInFlightTool ./internal/session/runner` | fallo esperado |
| GREEN | `PendingToolCalls` + cierre en `consume` + `StepFailed`/`FailUnresolvedTools` + `failInterruptedTools` minimos | `internal/session/{store,memstore}.go`, `internal/session/runner/{turn,publish,run}.go` | test especifico pasa |
| TRIANGULATE | Error de proveedor, provider-executed sin resolver, failInterruptedTools, contrato del store | `go test -run 'TestRunner_Cancel|TestRunner_ProviderStream|TestRunner_ProviderExecuted|TestRunner_RunFailsInterrupted|TestMemoryStore_PendingToolCalls' ./internal/session/...`, `go test -race ...` | casos pasan, `-race` limpio |
| REFACTOR | helpers de test, `doc.go` actualizados | `gofmt -w internal`, `go vet ./...`, `go test ./...` | suite verde, M1..M7 intactos |

## 11. Riesgos y decisiones

- **`consume` cierra el estado ambiguo; `runTurn`/`runTurnAttempt` no cambian.** M8
  concentra el manejo de fallos en `consume` (el unico que tiene el `Publisher`, las
  goroutines de tools y el stream a la vista) y deja el retry loop y el attempt de M7
  intactos. El error de fallo (`context.Canceled`, `*ProviderError`) no es una senal
  de control: cae en el `default` de `runTurn` y se devuelve. Asi M7 (rebuild/
  compaction) y M6 (`Run`) no se tocan, y el fallo sube como un error duro que corta
  la actividad.
- **Contexto desacoplado para las escrituras de cierre (`context.WithoutCancel`).**
  El punto fino del hito: si el cierre de una tool interrumpida o el `Step.Failed`
  usaran el `ctx` ya cancelado, un store real los rechazaria y el historial quedaria
  con la tool colgada — justo lo que M8 evita. `context.WithoutCancel` (Go 1.21+,
  disponible en go 1.23) conserva valores y deadline pero descarta el `Done`, asi la
  escritura de cierre sobrevive a la cancelacion. Con `MemoryStore` (que ignora el
  `ctx`) el efecto no se observa, pero el codigo queda correcto para el store real de
  M10; se documenta para que el adaptador real no lo rompa. El `Settle` de la tool si
  recibe `gctx` (cancelable): se quiere que la tool **se corte**, pero que su cierre
  **se escriba**.
- **`Step.Failed` solo tras arrancar el turno.** Si `provider.Stream` falla de
  entrada (antes de cualquier evento), el attempt devuelve ese error sin emitir
  `Step.Failed`: no hubo `Step.Started`, asi que no hay turno que marcar como fallido,
  y el `Publisher` ni siquiera se creo (se crea tras `Stream`). El `Step.Failed` es el
  cierre simetrico de un turno que **si** arranco (un `StepStarted` ya salio) y luego
  se interrumpio o fallo. Es una regla simple y fiel: cada `Step.Failed` tiene su
  `Step.Started`.
- **Provider-executed sin resolver: solo se cierra en fallo, no en cierre limpio.**
  M8 cierra una tool provider-executed sin resolver cuando el **turno fallo**
  (`FailUnresolvedTools` en la ruta de fallo) o cuando un crash la dejo colgada
  (`failInterruptedTools`). **No** la cierra en un cierre limpio de turno, porque el
  resultado de una provider-executed (el evento con el que el proveedor devuelve su
  output) aun no se modela: hacerlo cerraria como "fallida" toda provider-executed
  legitima. Eso mantiene verde el `TestRunner_ProviderExecutedToolIsOnlyPersisted` de
  M5 (cierre limpio: la provider-executed queda solo persistida) sin cambiar su
  semantica. El resultado de provider-executed y su cierre limpio llegan con el
  adaptador real — **M10**. Es una decision de scope explicita, no un olvido.
- **`PendingToolCalls` en el `Store`, no en el runner.** La pregunta "que tools
  quedaron colgadas en el log" es una proyeccion del log durable, igual que
  `Messages`. Ponerla en el `Store` (que es dueno del log) en vez de exponer el log
  crudo y reconstruir en el runner mantiene la interface chica y la logica de
  proyeccion junto a su data. `EventKind` y `SessionEvent` ya viven en `session`, asi
  que el scan no acopla el store a nada nuevo. El driver real (SQLite) implementara la
  misma proyeccion con una query — **M10**.
- **`failInterruptedTools` con `AppendEvent` directo, no con un `Publisher`.** Corre
  al arrancar `Run`, fuera de cualquier turno (no hay stream, ni `assistantMessageID`,
  ni concurrencia de settle): un `Publisher` (que es de un turno) seria ruido. Apendea
  el `Tool.Failed` directo al store con su `Message{Role: tool}`, igual que lo haria
  `failTool` pero sin el estado de turno. Mantiene `failInterruptedTools` como una
  limpieza fina y autocontenida.
- **`ErrSessionNotFound` como "nada que limpiar".** Una sesion nueva arranca `Run`
  con su prompt aun en el inbox (sin eventos en el store): `PendingToolCalls` da
  `ErrSessionNotFound`. `failInterruptedTools` lo traga y sigue. Se evita exigir que
  el caller siembre la sesion antes de `Run`, y los tests de `Run` de M6 (que arrancan
  de cero) siguen verdes sin cambios.
- **Interrupcion no se reintenta.** A diferencia de las senales de control de M7
  (que `runTurn` traga y reintenta), `context.Canceled` y `*ProviderError` se
  devuelven: una interrupcion es intencional (el usuario corto) y un error de
  proveedor en M8 corta el turno (el reintento con backoff es M10). El retry loop de
  M7 distingue exactamente eso con su `switch`: solo las dos senales reintentan.
- **`Step.Failed` sin `Message` (marcador de turno).** A diferencia de `Tool.Failed`
  (que materializa un `Message{Role: tool}` para que el modelo vea el fallo de la
  tool), `Step.Failed` no materializa `Message`: es un marcador de que el turno
  fracaso, no parte de la conversacion. La proyeccion (`Messages`) no lo incluye; la
  UI (M9) lo observa por el evento durable. Asi un turno interrumpido no inyecta un
  mensaje espurio al historial que veria el modelo.
- **Orden de cierre de `FailUnresolvedTools` no garantizado.** Itera el mapa `tools`
  (orden no determinista). Para un solo dangling es irrelevante; con varios, los tests
  afirman pertenencia al conjunto, no orden. Un orden estable (por `Seq` del
  `Tool.Called`) se difiere a cuando un test lo pida; agregarlo ahora seria precision
  sin un consumidor.

## 12. Fuentes

- Roadmap: `docs/atenea-agent-loop-roadmap.md` (hito M8)
- Arquitectura: `docs/atenea-agent-loop.md` (secciones "Interrupciones y manejo de
  fallos", la lista numerada de `runTurnAttempt` —paso 17— y "Streaming de eventos y
  ejecucion de tools")
- Loop de referencia: `docs/opencode-agent-loop.md` (manejo de fallos, interrupciones
  y `failInterruptedTools`)
- Manera de trabajo: `AGENTS.md`
- Specs previos: `docs/atenea-m1-tipos-store-spec.md`,
  `docs/atenea-m2-provider-fake-spec.md`, `docs/atenea-m3-publisher-spec.md`,
  `docs/atenea-m4-tool-registry-spec.md`, `docs/atenea-m5-run-turn-spec.md`,
  `docs/atenea-m6-run-loop-spec.md`, `docs/atenea-m7-control-signals-spec.md`
