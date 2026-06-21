# Spec M2 — Provider + fake scriptable

Spec ejecutable del hito **M2** de `docs/atenea-agent-loop-roadmap.md`. Define el
estado final, el alcance, el plan TDD y los criterios de aceptacion para dejar la
frontera con el modelo: la interface `Provider`, los tipos `Request`, `Event`,
`EventKind` y `Usage`, y un `FakeProvider` scriptable que emite una secuencia
determinista de `llm.Event` por un channel y lo cierra al terminar.

Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos, igual que el resto de `docs/`.

## 1. Contexto

M1 dejo el dominio durable (`Seq`, `Message`, `SessionEvent`, `Store`,
`MemoryStore`). El siguiente ladrillo hacia afuera (ver el orden
`tipos -> store -> provider -> publisher` en el roadmap) es la **frontera con el
modelo**: el paquete `internal/llm`.

La decision arquitectonica central (ver `docs/atenea-agent-loop.md`, "Tipos
principales" y "Streaming de eventos") es que **un turno es una sola llamada al
proveedor que produce un channel de eventos y lo cierra al terminar**. El runner
(M5) consume ese channel con `for ev := range in`; el publisher (M3) traduce cada
evento a un `SessionEvent` durable. Cancelar el `ctx` interrumpe el turno, igual
que un boton "stop" en la UI.

Para construir M3..M8 con tests deterministas y sin red hace falta un proveedor
de mentira: el `FakeProvider`. Reproduce un guion fijo de eventos, lo que permite
escribir escenarios reproducibles (un turno con texto, otro con tool calls, otro
que se cancela) sin tocar Anthropic. El adaptador real (Claude/Anthropic, ver
`docs/atenea-llm-claude.md`) entra en **M10**, detras de esta misma interface.

## 2. Objetivo

Dejar en `internal/llm`:

- la interface `Provider` con `Stream(ctx, Request) (<-chan Event, error)`;
- los tipos del contrato del stream: `Request`, `Event`, `EventKind`, `Usage`;
- un `FakeProvider` (+ `NewFakeProvider`) que:
  - emite su guion de eventos en orden por un channel nuevo,
  - **cierra** el channel al terminar (ningun receptor queda colgado),
  - corta el envio si el `ctx` esta cancelado (interrupcion del turno),
  - reproduce el mismo guion en cada llamada a `Stream` (determinista, sin
    mutar el guion);
- la assertion de compilacion `var _ Provider = (*FakeProvider)(nil)`;
- tests de comportamiento que reemplazan al `scaffold_test.go` de M0, con el caso
  concurrente (goroutine + channel) corrido con `-race`.

M2 **no** agrega el publisher, el mapeo a `SessionEvent`, el adaptador real, el
registry de tools ni el runner.

## 3. Alcance

### Dentro

- `internal/llm/provider.go`: interface `Provider`, tipos `Request`, `Event`,
  `EventKind` (+ constantes) y `Usage`.
- `internal/llm/fake.go`: `FakeProvider` + `NewFakeProvider` + assertion
  `var _ Provider`.
- Tests de comportamiento en `internal/llm` (reemplazan `scaffold_test.go`).

### Fuera (no hacer en M2)

- `publish.go`: traduccion `llm.Event -> SessionEvent`, coalescing de deltas y
  la taxonomia `Step.* / Text.* / Reasoning.* / Tool.*` — M3.
- Adaptador real `AnthropicProvider` (mapeo del stream del SDK a `llm.Event`,
  caching, thinking adaptive, compaction) — M10.
- Enriquecer `Request` con `System []Part`, `Messages`, `Tools []ToolDef` y
  `ProviderOpts`, y construirlo desde el historial proyectado — M5.
- `Registry.Materialize`, `Settle`, builtins — M4.
- `runTurn`, `consume`, `errgroup`, `needsContinuation` — M5.
- Manejo de error de proveedor, interrupcion a mitad de turno y tools sin
  resolver — M8.
- Guionar **varios** turnos distintos por corrida (cola de guiones) — llega
  cuando M5/M6 lo pidan con un test.
- Cualquier toque a `app.go`, `main.go`, Wails o el frontend — M9.

## 4. Contrato del provider (`provider.go`)

`internal/llm/provider.go`:

```go
package llm

import (
	"context"
	"encoding/json"
)

// Provider es la frontera con el modelo. Stream produce exactamente UN turno:
// emite los eventos del turno por el channel y lo CIERRA al terminar. El runner
// (M5) lo consume con `for ev := range out`. Cancelar ctx interrumpe el turno
// (equivale a una interrupcion de usuario) y tambien cierra el channel: ningun
// receptor queda colgado. M2 implementa un fake en memoria; el adaptador real
// (Claude/Anthropic) entra en M10 detras de esta misma interface.
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

// Request es la entrada de un turno. En M2 lleva solo el modelo; el fake lo
// ignora (el guion es la fuente de verdad del turno). El runner (M5) le agrega
// System, Messages, Tools y ProviderOpts cuando construye el request desde el
// historial proyectado. Crece sin cambiar la interface Provider.
type Request struct {
	Model string
}

// EventKind clasifica cada evento del stream del proveedor. El conjunto refleja
// 1:1 los eventos de sesion del contrato del loop (ver "Eventos publicados" en
// docs/atenea-agent-loop.md), menos los que produce el runner y no el proveedor
// (Tool.Success / Tool.Failed). El publisher (M3) mapea estos kinds a eventos
// durables de sesion; M2 solo los define y el fake los reproduce.
type EventKind int

const (
	StepStarted EventKind = iota // arranca el turno            -> Step.Started
	StepEnded                    // cierra el turno con tokens  -> Step.Ended (lleva Usage)
	StepFailed                   // fallo del stream            -> Step.Failed

	TextStarted // abre un bloque de texto      -> Text.Started
	TextDelta   // fragmento de texto           -> Text.Delta   (lleva Text)
	TextEnded   // cierra el bloque de texto    -> Text.Ended

	ReasoningStarted // abre razonamiento        -> Reasoning.Started
	ReasoningDelta   // fragmento de razonamiento -> Reasoning.Delta (lleva Text)
	ReasoningEnded   // cierra razonamiento      -> Reasoning.Ended

	ToolCall // el modelo invoca una tool       -> Tool.Called (lleva CallID, ToolName, Input)

	ToolInputStarted // abre el input de la tool -> Tool.Input.Started (lleva CallID)
	ToolInputDelta   // fragmento del input JSON -> Tool.Input.Delta   (lleva CallID, Input)
	ToolInputEnded   // cierra el input de la tool -> Tool.Input.Ended (lleva CallID)
)

// Event es un evento del stream de un turno. Kind decide que campos son
// relevantes; el resto queda en cero. Input es el JSON crudo del input de una
// tool: se parsea con json.Unmarshal, nunca por match de string (el modelo puede
// escapar el JSON distinto entre turnos). Usage solo viene en StepEnded.
type Event struct {
	Kind     EventKind
	CallID   string          // ToolCall / ToolInput*
	ToolName string          // ToolCall
	Input    json.RawMessage // ToolCall / ToolInputDelta: input JSON (crudo)
	Text     string          // TextDelta / ReasoningDelta
	Usage    *Usage          // solo StepEnded
}

// Usage son los tokens reportados al cerrar el turno (StepEnded). El proveedor
// real (M10) los completa; el fake los guiona; el publisher (M3) los persiste en
// Step.Ended.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	ReasoningTokens  int
	CacheReadTokens  int
	CacheWriteTokens int
}
```

Decision de diseno: `Event` y `EventKind` son **transcripcion** del contrato ya
fijado por el diseno (`docs/atenea-agent-loop.md` "Tipos principales" y "Eventos
publicados", y la tabla de mapeo de `docs/atenea-llm-claude.md`), no invencion de
M2. Se define la taxonomia completa una vez para que el fake guione turnos
realistas y M3 tenga un blanco estable sobre el que escribir su mapeo. En cambio
`Request` se deja minimo (solo `Model`) y crece en M5, igual que M1 dejo
`Message` con solo `Text` y lo enriquece M3. El mapeo `EventKind -> SessionEvent`
es de M3; M2 no lo implementa.

## 5. El fake scriptable (`fake.go`)

`internal/llm/fake.go`:

```go
package llm

import "context"

// FakeProvider es un Provider determinista para tests sin red. Reproduce un
// guion fijo de eventos en cada llamada a Stream y cierra el channel al
// terminar. Ignora Request (como MemoryStore ignora ctx en M1): el guion es la
// fuente de verdad del turno. Vive fuera de un _test.go a proposito, para que
// los tests del publisher (M3) y del runner (M5+) puedan importarlo.
type FakeProvider struct {
	Script []Event
}

// NewFakeProvider crea un fake que reproducira script en cada Stream.
func NewFakeProvider(script ...Event) *FakeProvider {
	return &FakeProvider{Script: script}
}

// var _ Provider = (*FakeProvider)(nil) asegura en compilacion que FakeProvider
// cumple la interface.
var _ Provider = (*FakeProvider)(nil)

// Stream emite el guion por un channel nuevo y lo cierra al terminar (defer
// close). Si ctx ya esta cancelado al inicio de una iteracion, corta el envio y
// cierra igual; si el productor queda bloqueado en un envio, el case ctx.Done lo
// desbloquea. En ningun caso queda una goroutine colgada.
func (p *FakeProvider) Stream(ctx context.Context, _ Request) (<-chan Event, error) {
	out := make(chan Event)
	go func() {
		defer close(out)
		for _, ev := range p.Script {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out, nil
}
```

Notas:

- **Channel sin buffer + goroutine productora**: el receptor marca el ritmo
  (back-pressure), igual que el stream real. Por eso el caso se corre con
  `-race`.
- **Cierre garantizado**: `defer close(out)` cierra al drenar el guion o al
  cortar por `ctx`. El receptor termina su `for range` sin bloquearse.
- **`Request` ignorado**: el fake no lee `Request`; el guion define el turno. Es
  el mismo patron que M1 (el `MemoryStore` ignora `ctx` por fidelidad con la
  interface). El parametro se nombra `_` para dejarlo explicito.
- **Guion inmutable**: `Stream` solo lee `p.Script`, no lo muta ni lo consume, asi
  que dos llamadas reproducen lo mismo (lo necesita un loop multi-turno en M6).
- **Sin error de setup en M2**: `Stream` siempre devuelve `nil` como error. El
  segundo valor del contrato queda para fallos de setup del proveedor real (M10)
  o de inyeccion de fallos (M8); un fallo a mitad de turno se modela cancelando
  `ctx`, no devolviendo error aca.

## 6. Semantica del stream

El contrato que el fake fija para todos los consumidores aguas abajo:

- **Orden**: los eventos salen en el orden del guion.
- **Fidelidad**: cada `Event` llega con sus campos intactos (`Text`, `CallID`,
  `ToolName`, `Input`, `Usage`); el fake no fabrica ni descarta campos.
- **Terminacion**: al agotar el guion, el channel se cierra; el `for range`
  termina solo.
- **Guion vacio**: `Stream` abre y cierra el channel sin emitir nada; el
  `for range` no entrega eventos y no se bloquea.
- **Cancelacion (cut determinista)**: con un `ctx` ya cancelado al llamar
  `Stream`, el chequeo `ctx.Err()` al tope de la primera iteracion corta antes de
  emitir: el receptor recibe cero eventos y el channel se cierra. Asi se
  demuestra, sin flakiness, que "cancelar el ctx corta el stream".
- **Cancelacion a mitad de turno**: si el `ctx` se cancela cuando el productor ya
  esta bloqueado en un envio, el `case <-ctx.Done()` lo desbloquea y cierra el
  channel (sin goroutine colgada). Un envio ya "en vuelo" hacia un receptor listo
  puede completarse antes del corte; la garantia es que el stream **termina**,
  no un conteo exacto de eventos entregados. El conteo exacto bajo carrera es de
  M8 (interrupcion), no de M2.

## 7. Plan TDD

### Safety net

- Estado base verde antes de tocar nada. M2 reemplaza
  `internal/llm/scaffold_test.go`, asi que primero se corre lo existente
  (paquetes M0 + `session` de M1).
- Comando: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Resultado esperado: pasan limpio. Si algo falla, se reporta como preexistente y
  no se sigue a ciegas.

### Understand

- Leer la entrada M2 del roadmap; "Tipos principales", "Streaming de eventos" y
  "Eventos publicados" de `docs/atenea-agent-loop.md`; la tabla de mapeo de
  `docs/atenea-llm-claude.md`.
- Comportamiento esperado: `Stream` emite un guion de eventos y cierra el channel;
  cancelar `ctx` corta el stream; guion vacio cierra de inmediato.

### RED

- Escribir primero el test que falla:
  `TestFakeProvider_StreamEmitsScriptedEventsThenClosesChannel`. Referencia a
  `NewFakeProvider`, `Stream`, `Event`, `EventKind`, `Usage`, que aun no existen
  -> no compila -> falla (RED honesto en Go es fallo de compilacion del paquete de
  test).
- El test guiona un turno realista (Step/Reasoning/Text/Tool/StepEnded), drena el
  channel con `for range`, y compara lo recibido contra el guion con
  `reflect.DeepEqual`.
- Comando:
  `go test -run TestFakeProvider_StreamEmitsScriptedEventsThenClosesChannel ./internal/llm`
  -> fallo esperado.

### GREEN

- Escribir el minimo: `provider.go` (interface + tipos) y `fake.go`
  (`FakeProvider`, `NewFakeProvider`, `Stream`).
- Correr solo el test rojo hasta verde.
- Comando:
  `go test -run TestFakeProvider_StreamEmitsScriptedEventsThenClosesChannel ./internal/llm`.

### TRIANGULATE

Agregar casos para evitar falso verde:

- `TestFakeProvider_StreamEmptyScriptClosesImmediately`: guion vacio ->
  `for range` entrega cero eventos y termina sin bloquear.
- `TestFakeProvider_CanceledCtxCutsStream`: `ctx` cancelado antes de `Stream` ->
  cero eventos (`< len(script)`) y el channel cierra. Se corre con `-race`.
- `TestFakeProvider_StreamPreservesToolAndUsageFields`: un `ToolCall` con
  `CallID`/`ToolName`/`Input` y un `StepEnded` con `*Usage` -> los campos llegan
  intactos (guarda contra un fake que fabrica o descarta campos).
- `TestFakeProvider_StreamIsReplayable`: dos llamadas a `Stream` del mismo fake
  entregan el mismo guion (guarda contra mutar/consumir `Script`).
- Comandos:
  - `go test -run TestFakeProvider ./internal/llm`
  - `go test -race -run TestFakeProvider ./internal/llm`

### REFACTOR

- Limpieza sin cambiar comportamiento: actualizar el comentario de paquete en
  `internal/llm/doc.go` (ya no "la interface y el fake llegan en M2"), borrar
  `scaffold_test.go`, dejar nombres y helpers de test consistentes (p.ej. un
  helper `drain(out) []Event`).
- Verificar que la suite sigue verde tras el formateo.
- Comando: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Criterios de aceptacion (Done when)

1. Existe la interface `Provider` con `Stream(ctx, Request) (<-chan Event, error)`
   en `internal/llm/provider.go`.
2. Existen los tipos `Request`, `Event`, `EventKind` (con su set de constantes) y
   `Usage` en `provider.go`.
3. `FakeProvider` (+ `NewFakeProvider`) implementa `Provider` (verificado por
   `var _ Provider`).
4. `Stream` emite el guion en orden, con los campos de cada `Event` intactos, y
   **cierra** el channel al terminar.
5. Guion vacio -> el `for range` no entrega eventos y termina.
6. `ctx` cancelado antes de `Stream` -> cero eventos y channel cerrado; ninguna
   goroutine queda colgada (`-race` limpio).
7. Dos llamadas a `Stream` del mismo fake reproducen el mismo guion.
8. El `scaffold_test.go` de M0 fue reemplazado por tests de comportamiento.
9. `go test ./...` (y `-race` donde aplica) pasa; `go vet ./...` limpio;
   `gofmt -l .` vacio.
10. No hubo cambios en `app.go`, `main.go`, Wails ni el frontend; ni en otros
    paquetes `internal/` fuera de `llm`.

## 9. Comandos

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Ciclo (test especifico primero)
go test -run TestFakeProvider_StreamEmitsScriptedEventsThenClosesChannel ./internal/llm
go test -run TestFakeProvider ./internal/llm

# Concurrencia (goroutine + channel)
go test -race -run TestFakeProvider ./internal/llm

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
go test -race ./internal/llm
```

## 10. Tabla de evidencia esperada

Al cerrar M2, la respuesta/PR debe incluir esta tabla con resultados reales:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0+M1 verde antes de editar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Contrato del stream leido | roadmap M2, `docs/atenea-agent-loop.md`, `docs/atenea-llm-claude.md` | comportamiento identificado |
| RED | Test de guion+cierre escrito primero | `fake_test.go` + `go test -run TestFakeProvider_StreamEmitsScriptedEventsThenClosesChannel ./internal/llm` | fallo esperado (no compila) |
| GREEN | `Provider`/tipos + `FakeProvider` minimos | `provider.go`, `fake.go` | test especifico pasa |
| TRIANGULATE | Guion vacio, cancelacion, fidelidad, replay | `go test -run TestFakeProvider ./internal/llm`, `go test -race ...` | casos pasan, `-race` limpio |
| REFACTOR | `doc.go`, borrar `scaffold_test.go`, helper `drain` | `gofmt -w internal`, `go vet ./...`, `go test ./...` | suite verde |

## 11. Riesgos y decisiones

- **Taxonomia completa de `EventKind` en M2**: se define el set completo a
  proposito, no solo los cuatro que nombra el roadmap (texto, reasoning,
  tool-call, step-finish). No es especulacion: es transcripcion del contrato ya
  decidido en `docs/atenea-agent-loop.md` ("Eventos publicados") y la tabla de
  `docs/atenea-llm-claude.md`. Fijarlo una vez le da a M3 un blanco estable y
  evita churn del enum entre hitos. El fake guiona un turno que toca casi todos
  los kinds, asi que el set esta ejercitado por tests, no muerto.
- **`Request` minimo (solo `Model`)**: el fake lo ignora, asi que agregar
  `System`/`Messages`/`Tools`/`ProviderOpts` ahora seria especular. Se suman en M5
  cuando el runner construya el request desde el historial, sin tocar la interface
  `Provider`. Mismo criterio que M1 con `Message`.
- **Cancelacion determinista via `ctx` pre-cancelado**: el corte a mitad de turno
  bajo carrera es inherentemente no determinista (un envio en vuelo puede
  completarse). Para un test sin flakiness se demuestra el corte con un `ctx` ya
  cancelado y el chequeo `ctx.Err()` al tope de la iteracion. El conteo exacto
  bajo interrupcion concurrente es de M8.
- **Fake fuera de `_test.go`**: `FakeProvider` es infraestructura de test
  reutilizable por otros paquetes (publisher M3, runner M5+), asi que vive en
  `fake.go` exportado, no en un archivo de test. Mismo criterio que `MemoryStore`
  en M1.
- **Un solo guion por fake**: M2 no guiona varios turnos distintos por corrida.
  El loop externo (M6) llama `Stream` varias veces; cuando un test lo exija, se
  agrega una cola de guiones detras del mismo `FakeProvider` sin cambiar la
  interface. No se adelanta sin test.
- **`Stream` no devuelve error en M2**: el camino de error de setup queda para el
  proveedor real (M10) y la inyeccion de fallos (M8). Un fallo de turno se modela
  cancelando `ctx`. Mantener `error` en la firma es contrato, no deuda.

## 12. Fuentes

- Roadmap: `docs/atenea-agent-loop-roadmap.md` (hito M2)
- Arquitectura: `docs/atenea-agent-loop.md` (secciones "Tipos principales",
  "Streaming de eventos y ejecucion de tools", "Eventos publicados")
- Integracion LLM: `docs/atenea-llm-claude.md` (tabla de mapeo de eventos del SDK
  a `llm.Event`; M2 = fake, M10 = adaptador real)
- Manera de trabajo: `AGENTS.md`
- Specs previos: `docs/atenea-m0-scaffolding-spec.md`,
  `docs/atenea-m1-tipos-store-spec.md`
