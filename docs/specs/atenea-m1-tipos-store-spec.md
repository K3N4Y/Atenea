# Spec M1 — Tipos + Store en memoria

Spec ejecutable del hito **M1** de `docs/atenea-agent-loop-roadmap.md`. Define el
estado final, el alcance, el plan TDD y los criterios de aceptacion para dejar el
backbone durable del dominio: `Seq`, `Session`, `Message`, `SessionEvent` y un
`Store` en memoria que agrega eventos y reproyecta mensajes.

Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos, igual que el resto de `docs/`.

## 1. Contexto

M0 dejo el arbol `internal/` con cinco paquetes vacios pero compilables. El loop
(ver `docs/atenea-agent-loop.md`) se construye de adentro hacia afuera: el primer
ladrillo es el dominio durable de la sesion.

La decision arquitectonica central (seccion "Persistencia durable") es que **el
estado fuente son eventos, no memoria viva entre turnos**. El runner reconstruye
el request del proveedor en cada turno leyendo del `Store`. Por eso M1 fija dos
cosas: los tipos del dominio y un `Store` que se comporta como log append-only
con una proyeccion de mensajes derivada de ese log.

El `Store` arranca como implementacion en memoria. SQLite es M10 y entra detras
de la misma interface sin tocar el resto.

## 2. Objetivo

Dejar en `internal/session`:

- los tipos `Seq`, `Role`, `Message`, `SessionEvent`, `Session`;
- la interface `Store` con `AppendEvent`, `LoadSession` y `Messages`;
- una implementacion en memoria (`MemoryStore`) que:
  - asigna un `Seq` monotonico por sesion al agregar un evento,
  - reproyecta los mensajes desde el log de eventos en orden de `Seq`,
  - filtra por `sinceSeq` y reporta sesiones inexistentes con un error sentinel;
- tests de comportamiento que reemplazan al `scaffold_test.go` de M0, incluyendo
  un caso concurrente corrido con `-race`.

M1 **no** agrega provider, publisher, tools, inbox, epoch, runner ni Wails.

## 3. Alcance

### Dentro

- `internal/session/session.go`: tipos del dominio (`Seq`, `Role`, `Message`,
  `SessionEvent`, `Session`).
- `internal/session/store.go`: interface `Store` + `ErrSessionNotFound`.
- `internal/session/memstore.go`: `MemoryStore` + `NewMemoryStore`.
- Tests de comportamiento en `internal/session` (reemplazan `scaffold_test.go`).
- Assertion de compilacion `var _ Store = (*MemoryStore)(nil)`.

### Fuera (no hacer en M1)

- Interface `Provider`, `Request`, `Event` y fake — M2.
- Publisher y taxonomia de eventos de streaming
  (`Step.* / Text.* / Reasoning.* / Tool.*`) y coalescing de deltas — M3.
- `Registry.Materialize`, `settle`, builtins — M4.
- `runTurn`, `Run`, `MaxSteps`, continuacion — M5/M6.
- `Inbox` (`Admit`/`HasPending`/`Promote`) — M6.
- `ContextEpoch` y senales de control (`errRebuildTurn`,
  `errContinueAfterCompaction`) — M7.
- `History.EntriesForRunner` (proyeccion con baseline/compaction) — M5.
- `Store` SQLite y adaptador de proveedor real — M10.
- Cualquier toque a `app.go`, `main.go`, Wails o el frontend — M9.

## 4. Tipos del dominio

`internal/session/session.go`:

```go
package session

// Seq es la secuencia monotonica que el Store asigna a cada evento de una
// sesion. Empieza en 1 y crece de a 1 por sesion. Define el orden total durable
// del historial; el filtro sinceSeq de Messages se expresa contra este valor.
type Seq int64

// Role es el autor de un mensaje proyectado.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Message es la proyeccion de la conversacion para un turno. En M1 lleva texto
// plano y el Seq del evento que lo materializo (ordena y filtra por sinceSeq).
// Las partes ricas (tool calls, reasoning) y el coalescing de deltas de
// streaming llegan con el publisher en M3, sin cambiar esta forma minima.
type Message struct {
	ID   string
	Role Role
	Text string
	Seq  Seq
}

// SessionEvent es el evento durable: la unica fuente de verdad de la sesion. El
// Store asigna SessionID y Seq al agregarlo; el llamador los deja en cero. En M1
// un evento puede materializar un mensaje (Message != nil) o no aportar a la
// proyeccion (Message == nil). La taxonomia de streaming
// (Step.* / Text.* / Reasoning.* / Tool.*) la agrega el publisher en M3 sobre
// esta misma estructura.
type SessionEvent struct {
	SessionID string
	Seq       Seq
	Message   *Message
}

// Session es el agregado durable de una conversacion. En M1 es minimo: solo el
// ID. Agente, modelo y workspace se agregan cuando el runner los necesita
// (M5/M7), no antes.
type Session struct {
	ID string
}
```

Decision de diseno: en M1 un `SessionEvent` que materializa un mensaje carga el
`Message` ya formado y la proyeccion es **un evento -> un mensaje**. No es una
simplificacion casual: deja el contrato "Messages es derivado del log, no
almacenado aparte". M3 enriquece el fold para coalescer varios deltas
(`Text.Started/Delta/Ended`) en un solo mensaje **sin** cambiar la interface
`Store` ni la forma de `SessionEvent`.

## 5. Interface Store e implementacion en memoria

`internal/session/store.go`:

```go
package session

import (
	"context"
	"errors"
)

// ErrSessionNotFound se devuelve al leer una sesion que nunca recibio un evento.
var ErrSessionNotFound = errors.New("session not found")

// Store es la persistencia durable de la sesion. El log de eventos es la fuente
// de verdad; los mensajes son una proyeccion derivada. M1 implementa una version
// en memoria; M10 agrega SQLite detras de esta misma interface.
type Store interface {
	// AppendEvent agrega ev al log durable de sessionID, le asigna el siguiente
	// Seq monotonico y lo devuelve. Crea la sesion si es su primer evento. El
	// SessionID y Seq que traiga ev se ignoran: los fija el Store.
	AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error)

	// LoadSession devuelve el agregado de la sesion. ErrSessionNotFound si la
	// sesion nunca recibio un evento.
	LoadSession(ctx context.Context, sessionID string) (Session, error)

	// Messages reproyecta los mensajes de la sesion en orden de Seq y devuelve
	// solo los materializados por eventos con Seq > sinceSeq. sinceSeq = 0
	// reconstruye el historial completo desde cero. ErrSessionNotFound si la
	// sesion no existe.
	Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error)
}
```

`internal/session/memstore.go`:

```go
package session

import (
	"context"
	"sync"
)

// MemoryStore es la implementacion en memoria del Store para M1..M9. Guarda el
// log de eventos por sesion bajo un mutex y deriva los mensajes al vuelo.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string][]SessionEvent
}

// NewMemoryStore crea un store vacio listo para usar.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string][]SessionEvent)}
}

// var _ Store = (*MemoryStore)(nil) asegura en tiempo de compilacion que
// MemoryStore cumple la interface.
var _ Store = (*MemoryStore)(nil)

func (s *MemoryStore) AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log := s.sessions[sessionID]
	seq := Seq(len(log) + 1)
	ev.SessionID = sessionID
	ev.Seq = seq
	s.sessions[sessionID] = append(log, ev)
	return seq, nil
}

func (s *MemoryStore) LoadSession(ctx context.Context, sessionID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[sessionID]; !ok {
		return Session{}, ErrSessionNotFound
	}
	return Session{ID: sessionID}, nil
}

func (s *MemoryStore) Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log, ok := s.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	var out []Message
	for _, ev := range log {
		if ev.Message == nil || ev.Seq <= sinceSeq {
			continue
		}
		m := *ev.Message
		m.Seq = ev.Seq
		out = append(out, m)
	}
	return out, nil
}
```

Notas:

- `ctx` no se usa en memoria, pero se conserva en la firma por fidelidad con la
  interface (SQLite en M10 si lo necesita). No dispara `go vet`.
- Un solo mutex serializa append y lectura: garantiza `Seq` sin huecos ni
  duplicados y deja el camino para el chequeo `-race`.
- La sesion se crea de forma perezosa en el primer `AppendEvent`. Leer una sesion
  que nunca recibio un evento es el caso "sesion inexistente".

## 6. Modelo de reproyeccion

`Messages` no guarda mensajes: los **deriva** del log cada vez. Reglas:

- recorre los eventos en orden de insercion (que coincide con orden de `Seq`);
- ignora eventos sin mensaje (`Message == nil`);
- ignora eventos con `Seq <= sinceSeq`;
- copia el `Message` y le fija `Seq = ev.Seq` (el evento es la autoridad del
  orden), para que el filtro `sinceSeq` y el orden sean consistentes.

Propiedad clave (Done when del roadmap): con `sinceSeq = 0`, `Messages`
reconstruye el historial completo desde cero a partir del log. No hay estado de
mensajes vivo fuera del log.

## 7. Plan TDD

### Safety net

- Estado base verde antes de tocar nada. M1 reemplaza
  `internal/session/scaffold_test.go`, asi que primero se corre lo existente.
- Comando: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Resultado esperado: pasan limpio (paquetes M0). Si algo falla, se reporta como
  preexistente y no se sigue a ciegas.

### Understand

- Leer la entrada M1 del roadmap, "Tipos principales" y "Persistencia durable"
  de `docs/atenea-agent-loop.md`, y "Sesion / Input durable / Historial
  proyectado" de `docs/opencode-agent-loop.md`.
- Comportamiento esperado: log append-only con `Seq` monotonico y mensajes
  derivados; lecturas de sesion inexistente fallan con un sentinel.

### RED

- Escribir primero el test que falla:
  `TestMemoryStore_AppendEventAssignsMonotonicSeq`. Referencia a
  `NewMemoryStore`, `AppendEvent` y `SessionEvent`, que aun no existen -> no
  compila -> falla (RED honesto en Go es fallo de compilacion del paquete de
  test).
- Comando: `go test -run TestMemoryStore_AppendEventAssignsMonotonicSeq ./internal/session`
  -> fallo esperado.

### GREEN

- Escribir el minimo: `session.go` (tipos), `store.go` (interface +
  `ErrSessionNotFound`), `memstore.go` (`MemoryStore` con `AppendEvent` que
  asigna `Seq` y `Messages` que reproyecta).
- Correr solo el test rojo hasta verde.
- Comando: `go test -run TestMemoryStore_AppendEventAssignsMonotonicSeq ./internal/session`.

### TRIANGULATE

Agregar casos para evitar falso verde:

- `TestMemoryStore_MessagesReturnsInSeqOrder`: varios eventos con y sin mensaje;
  `Messages(0)` devuelve los mensajes en orden de `Seq`.
- `TestMemoryStore_MessagesSinceSeqFiltersOlder`: `Messages(k)` devuelve solo los
  de `Seq > k`.
- `TestMemoryStore_MessagesSinceSeqBeyondLastReturnsEmpty`: `sinceSeq` mayor que
  el ultimo `Seq` -> slice vacio, sin error.
- `TestMemoryStore_UnknownSessionReturnsNotFound`: `LoadSession` y `Messages`
  sobre una sesion sin eventos -> `ErrSessionNotFound` (`errors.Is`).
- `TestMemoryStore_ConcurrentAppendsAssignUniqueSeqs`: N goroutines hacen
  `AppendEvent` sobre la misma sesion; los `Seq` devueltos forman exactamente
  `{1..N}` sin huecos ni duplicados. Se corre con `-race`.
- Comandos:
  - `go test -run TestMemoryStore ./internal/session`
  - `go test -race -run TestMemoryStore_ConcurrentAppendsAssignUniqueSeqs ./internal/session`

### REFACTOR

- Limpieza sin cambiar comportamiento: comentario de paquete en `doc.go`
  actualizado (ya no "los tipos llegan en M1"), assertion `var _ Store`,
  nombres y helpers de test consistentes.
- Verificar que la suite sigue verde tras el formateo.
- Comando: `gofmt -w internal && go vet ./... && go test ./...`.

## 8. Criterios de aceptacion (Done when)

1. Existen `Seq`, `Role`, `Message`, `SessionEvent`, `Session` en
   `internal/session/session.go`.
2. Existe la interface `Store` con `AppendEvent`, `LoadSession`, `Messages` y el
   sentinel `ErrSessionNotFound` en `store.go`.
3. `MemoryStore` implementa `Store` (verificado por `var _ Store`).
4. `AppendEvent` asigna `Seq` monotonico por sesion empezando en 1, sin huecos
   ni duplicados, incluso bajo appends concurrentes (`-race` limpio).
5. `Messages(0)` reconstruye el historial completo en orden de `Seq`;
   `Messages(k)` filtra por `Seq > k`; `sinceSeq` mayor que el ultimo devuelve
   vacio sin error.
6. `LoadSession` y `Messages` sobre sesion inexistente devuelven
   `ErrSessionNotFound`.
7. El `scaffold_test.go` de M0 fue reemplazado por tests de comportamiento.
8. `go test ./...` (y `-race` donde aplica) pasa; `go vet ./...` limpio;
   `gofmt -l .` vacio.
9. No hubo cambios en `app.go`, `main.go`, Wails ni el frontend; ni en otros
   paquetes `internal/` fuera de `session`.

## 9. Comandos

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Ciclo (test especifico primero)
go test -run TestMemoryStore_AppendEventAssignsMonotonicSeq ./internal/session
go test -run TestMemoryStore ./internal/session

# Concurrencia
go test -race -run TestMemoryStore_ConcurrentAppendsAssignUniqueSeqs ./internal/session

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
go test -race ./internal/session
```

## 10. Tabla de evidencia esperada

Al cerrar M1, la respuesta/PR debe incluir esta tabla con resultados reales:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M0 verde antes de editar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Tipos y persistencia leidos | roadmap M1, `docs/atenea-agent-loop.md`, `docs/opencode-agent-loop.md` | comportamiento identificado |
| RED | Test de Seq monotonico escrito primero | `session_test.go` + `go test -run TestMemoryStore_AppendEventAssignsMonotonicSeq ./internal/session` | fallo esperado (no compila) |
| GREEN | Tipos + `Store` + `MemoryStore` minimos | `session.go`, `store.go`, `memstore.go` | test especifico pasa |
| TRIANGULATE | Orden, `sinceSeq`, no-encontrado y concurrencia | `go test -run TestMemoryStore ./internal/session`, `go test -race ...` | casos pasan, `-race` limpio |
| REFACTOR | `doc.go`, assertion `var _ Store`, formato | `gofmt -w internal`, `go vet ./...`, `go test ./...` | suite verde |

## 11. Riesgos y decisiones

- **Proyeccion uno-a-uno en M1**: se elige a proposito para que el `Store` quede
  como log append-only con mensajes derivados, sin pre-construir la maquinaria de
  deltas. M3 enriquece el fold (coalescer `Text.*`) sin cambiar la interface.
- **Creacion perezosa de sesion**: el primer `AppendEvent` crea la sesion. Evita
  un `CreateSession` que M1 no necesita y deja "sesion inexistente" como una
  lectura sobre log vacio. Si M5/M9 piden lifecycle explicito, se agrega ahi.
- **`Session` minimo (solo ID)**: agente/modelo/workspace son del runner (M5) y
  del epoch (M7). Agregarlos ahora seria especular; se suman cuando un test los
  exija.
- **`Seq` no se confia desde el llamador**: `AppendEvent` ignora `SessionID`/`Seq`
  entrantes y los fija el store. Asi el orden total es responsabilidad unica del
  store y no se puede corromper desde afuera.
- **Concurrencia con mutex, no canales**: para un store en memoria un `sync.Mutex`
  es lo mas simple que garantiza `Seq` monotonico. Se valida con `-race`. SQLite
  (M10) movera la serializacion a transacciones detras de la misma interface.
- **`ctx` sin uso en memoria**: se mantiene en la firma por la interface; M10 lo
  honra (cancelacion/timeout de SQLite). No es deuda, es contrato.

## 12. Fuentes

- Roadmap: `docs/atenea-agent-loop-roadmap.md` (hito M1)
- Arquitectura: `docs/atenea-agent-loop.md` (secciones "Tipos principales" y
  "Persistencia durable")
- Loop de referencia: `docs/opencode-agent-loop.md` (secciones "Sesion", "Input
  durable", "Historial proyectado")
- Manera de trabajo: `AGENTS.md`
- Spec previo: `docs/atenea-m0-scaffolding-spec.md`
