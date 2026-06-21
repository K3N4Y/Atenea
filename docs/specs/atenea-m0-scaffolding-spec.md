# Spec M0 — Scaffolding

Spec ejecutable del hito **M0** de `docs/atenea-agent-loop-roadmap.md`. Define el
estado final, el alcance, el plan TDD y los criterios de aceptacion para dejar
la estructura de paquetes y dependencias lista para M1..M10.

Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos, igual que el resto de `docs/`.

## 1. Contexto

El loop del agente (ver `docs/atenea-agent-loop.md`) se construye de adentro
hacia afuera: tipos -> store -> provider -> publisher -> tools -> turno -> loop.
Antes de escribir cualquier logica hace falta el esqueleto de paquetes que el
resto de los hitos va a ir llenando, sin tocar Wails ni proveedores reales.

Hoy el repo no tiene `internal/`. M0 lo crea vacio pero compilable y testeable.

## 2. Objetivo

Dejar el arbol `internal/` con los cinco paquetes del diseno, cada uno con un
nombre de paquete fijado por un test, la dependencia `golang.org/x/sync/errgroup`
declarada en `go.mod`/`go.sum`, y los tres quality gates verdes.

M0 **no** agrega tipos, interfaces ni logica de negocio: eso es M1 en adelante.

## 3. Alcance

### Dentro

- Crear los directorios y paquetes:
  `internal/session`, `internal/session/runner`, `internal/llm`,
  `internal/tool`, `internal/event`.
- Un archivo `doc.go` por paquete (clausula de paquete + comentario de
  responsabilidad) para que cada paquete tenga un archivo no-test compilable.
- Un test trivial por paquete que fija el nombre del paquete.
- Agregar y anclar `golang.org/x/sync/errgroup` como dependencia.

### Fuera (no hacer en M0)

- Tipos del dominio (`Session`, `Message`, `Seq`, `SessionEvent`, `Store`, ...) — M1.
- Interface `Provider` y fake — M2.
- Publisher, registry, runTurn, Run, senales de control — M3..M8.
- Cualquier toque a `app.go`, `main.go`, Wails o el frontend — M9.
- SQLite o adaptador real de proveedor — M10.

## 4. Estructura a crear

| Ruta | Paquete | Archivos M0 | Responsabilidad futura |
| --- | --- | --- | --- |
| `internal/session/` | `session` | `doc.go`, `scaffold_test.go` | Agregado durable: Session, Message, Seq, inbox, history, epoch, store (M1) |
| `internal/session/runner/` | `runner` | `doc.go`, `scaffold_test.go` | Loop externo `Run`, `runTurn`, `publish` (M5..M8) |
| `internal/llm/` | `llm` | `doc.go`, `scaffold_test.go` | Interface `Provider`, `Request`, `Event` y adaptadores (M2, M10) |
| `internal/tool/` | `tool` | `doc.go`, `scaffold_test.go` | `Registry.Materialize`, `settle`, builtins (M4) |
| `internal/event/` | `event` | `doc.go`, `scaffold_test.go` | `EventBus` hacia Wails (M9) |

El nombre del paquete del subdirectorio `runner` es `runner`, no `session`.

### Contenido de referencia

`doc.go` (ejemplo para `session`; analogo en cada paquete con su responsabilidad):

```go
// Package session concentra el dominio durable del agente: Session, Message,
// Seq, inbox, historial y epoch. M0 solo fija el paquete; los tipos llegan en M1.
package session
```

`scaffold_test.go` (ejemplo para `session`):

```go
package session

import "testing"

// TestSessionPackageCompiles fija el nombre del paquete y prueba que compila y
// es descubierto por `go test ./...`. El cuerpo queda vacio a proposito: M0 no
// agrega comportamiento. Se reemplaza por tests reales en M1.
func TestSessionPackageCompiles(t *testing.T) {}
```

`scaffold_test.go` del paquete `runner` (ademas **ancla** la dependencia
`errgroup` con un import real, para que `go mod tidy` no la elimine antes de que
M5 la use):

```go
package runner

import (
	"testing"

	"golang.org/x/sync/errgroup"
)

// TestRunnerPackageWiresErrgroup fija el nombre del paquete y deja cableada la
// dependencia errgroup que el loop de M5 usara para asentar tools concurrentes.
func TestRunnerPackageWiresErrgroup(t *testing.T) {
	var _ errgroup.Group
}
```

## 5. Dependencias

- `golang.org/x/sync/errgroup`: se agrega ahora (lo pide el roadmap) aunque el
  primer uso productivo sea M5.
- **Anclaje**: un `go get` sin un import real se pierde con `go mod tidy`. Por eso
  el test de `runner` importa `errgroup` (`var _ errgroup.Group`). Asi la
  dependencia queda fija en `go.mod`/`go.sum` y sobrevive a `go mod tidy`.

```bash
go get golang.org/x/sync/errgroup
go mod tidy   # debe dejar errgroup presente, no removerlo
```

## 6. Plan TDD

M0 es estructural: varias fases se mapean de forma honesta y otras se marcan N/A.

### Safety net

- Estado base verde antes de tocar nada.
- Comando: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Resultado esperado: pasan limpio (solo existen los paquetes `main` actuales).
  Si algo falla, se reporta como preexistente y no se sigue a ciegas.

### Understand

- Leer `docs/atenea-agent-loop.md` (seccion "Layout de paquetes") y la entrada
  M0 del roadmap.
- Comportamiento esperado: cinco paquetes con su nombre fijado, sin logica.

### RED

- Antes de crear los paquetes, `go test ./internal/...` falla porque el
  directorio/paquete no existe (no hay nada que compilar).
- Para `runner`, el `scaffold_test.go` que importa `errgroup` no compila hasta
  que la dependencia este en el modulo.
- Comando: `go test ./internal/...` -> error esperado
  (`no such file or directory` / `cannot find package`).

### GREEN

- Crear, paquete por paquete, su `doc.go` + `scaffold_test.go` minimos.
- `go get golang.org/x/sync/errgroup` para que el test de `runner` compile.
- Comando por paquete (preferido durante el ciclo):
  `go test ./internal/session`, luego `./internal/session/runner`, etc.
- Resultado: cada `go test ./internal/<pkg>` pasa.

### TRIANGULATE

- Cada paquete adicional es un caso nuevo que prueba que la estructura
  generaliza (session -> runner -> llm -> tool -> event).
- Caso borde de dependencias: `go mod tidy` **no** debe quitar `errgroup`
  (lo verifica el import del test de `runner`).
- Comando: `go test ./...` (toda la suite) + `go mod tidy` y revisar `go.mod`.

### REFACTOR

- Limpieza sin cambiar comportamiento: comentarios de paquete consistentes en
  cada `doc.go`, `gofmt`, `go mod tidy` para ordenar `go.mod`/`go.sum`.
- Verificar que la suite sigue verde tras el formateo.
- Comando: `gofmt -w internal && go test ./...`.

## 7. Criterios de aceptacion (Done when)

1. Existen los cinco paquetes con su `doc.go` y su `scaffold_test.go`.
2. `go test ./...` pasa limpio (incluye los nuevos paquetes).
3. `go vet ./...` pasa sin hallazgos.
4. `gofmt -l .` no imprime nada (todo formateado).
5. `go.mod` declara `golang.org/x/sync` y `go mod tidy` lo mantiene.
6. No hubo cambios en `app.go`, `main.go`, Wails ni el frontend.

## 8. Comandos

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Dependencia
go get golang.org/x/sync/errgroup

# Ciclo (por paquete y luego completo)
go test ./internal/session
go test ./internal/session/runner
go test ./internal/llm
go test ./internal/tool
go test ./internal/event
go test ./...

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go mod tidy         # errgroup permanece
```

## 9. Tabla de evidencia esperada

Al cerrar M0, la respuesta/PR debe incluir esta tabla con resultados reales:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite previa verde | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Layout y entrada M0 leidos | `docs/atenea-agent-loop.md`, roadmap | estructura identificada |
| RED | Paquetes inexistentes no compilan | `go test ./internal/...` | fallo esperado |
| GREEN | `doc.go` + `scaffold_test.go` por paquete; `go get errgroup` | `go test ./internal/<pkg>` | cada paquete pasa |
| TRIANGULATE | Cinco paquetes + anclaje de dependencia | `go test ./...`, `go mod tidy` | todos pasan, errgroup permanece |
| REFACTOR | Formateo y doc comments sin cambiar comportamiento | `gofmt -w internal`, `go test ./...` | suite verde |

## 10. Riesgos y decisiones

- **`go mod tidy` elimina errgroup**: mitigado por el import real en el test de
  `runner` (decision explicita de M0, no de M5).
- **Paquete sin archivo no-test**: cada paquete lleva `doc.go` para evitar el
  caso "no non-test Go files" y dar un destino a los comentarios de paquete.
- **Nombre del subpaquete**: `internal/session/runner` es paquete `runner`; no
  reusar `session` para el subdirectorio.
- **Tests scaffold efimeros**: `scaffold_test.go` se reemplaza por tests de
  comportamiento cuando cada paquete reciba logica (M1+). Cuerpos vacios a
  proposito mientras tanto.

## 11. Fuentes

- Roadmap: `docs/atenea-agent-loop-roadmap.md` (hito M0)
- Arquitectura: `docs/atenea-agent-loop.md` (seccion "Layout de paquetes")
- Manera de trabajo: `AGENTS.md`
- `golang.org/x/sync/errgroup`: https://pkg.go.dev/golang.org/x/sync/errgroup
