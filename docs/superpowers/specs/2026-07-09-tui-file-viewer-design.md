# Design: visor de archivos de solo lectura en la TUI

Fecha: 2026-07-09
Estado: aprobado en brainstorming; pendiente de plan de implementacion

## Objetivo

Extender el explorer existente de `atenea-tui` con un visor integrado de
archivos. Al presionar `Enter` sobre un archivo, el visor reemplaza el area
principal de chat (transcript y composer), conserva el explorer abierto y
muestra el contenido en modo solo lectura con numeros de linea y resaltado de
sintaxis. `Esc` vuelve al chat sin perder el arbol, el cursor ni el scroll del
explorer.

La feature es exclusiva de la TUI. No cambia la app Wails ni habilita editar,
guardar, crear, borrar o renombrar archivos.

## Motivacion

El explorer actual permite encontrar archivos, pero `Enter` inserta una
mencion `@ruta` en el composer y cierra el panel. Para inspeccionar codigo el
usuario debe abandonar el flujo o pedirle al agente que use `read`. Un visor
local y navegable permite revisar rapidamente el workspace con una interaccion
similar a LazyVim, sin ejecutar tools ni modificar el filesystem.

## Alcance v1

- Abrir un archivo desde el explorer con `Enter`.
- Reemplazar solo el area derecha de chat por el visor; el explorer queda
  abierto a la izquierda.
- Renderizar texto con numeros de linea, scroll vertical y resaltado ANSI.
- Elegir lexer a partir de la ruta del archivo con Chroma y usar un tema oscuro
  estable que preserve contraste con los estilos Lip Gloss existentes.
- Mostrar cabecera estable con ruta relativa y posicion del viewport.
- Navegar el contenido con `j`/Down, `k`/Up, PgUp y PgDn.
- Cerrar el visor con `Esc` y recuperar el chat exactamente como estaba.
- Rechazar archivos binarios, demasiado grandes o no legibles con un estado
  explicito dentro del visor; nunca intentar mostrarlos parcialmente.
- Mantener tolerancia a terminales estrechas o con alto cero, igual que el
  viewport actual.
- Cubrir los contratos con tests unitarios de `internal/tui` y una prueba E2E
  bajo PTY del binario TUI para la navegacion real de teclado.

## Fuera de alcance

- Edicion, guardado o dirty state.
- Tabs, archivos recientes, split panes o previsualizacion simultanea de varios
  archivos.
- Busqueda dentro del archivo, goto line, minimapa, folds o seleccion/copiar.
- Recarga automatica cuando el archivo cambia en disco.
- Resaltado semantico/LSP o analisis del proyecto.
- Render de imagenes, PDF, archivos Office o cualquier formato binario.
- Cambiar el comportamiento actual de `@ruta`: el menu `@` y el flujo de
  menciones se mantienen intactos.

## Experiencia de usuario

### Flujo principal

1. Con el composer vacio, `Space` + `e` abre el explorer como hoy.
2. `j`/Down y `k`/Up mueven el cursor; `h`/`l` navegan carpetas como hoy.
3. `Enter` sobre una carpeta conserva su semantica actual: expandir/colapsar.
4. `Enter` sobre un archivo intenta abrirlo en el visor; no inserta `@ruta` y
   no cierra el explorer.
5. En el visor, `j`/Down, `k`/Up, PgUp y PgDn desplazan el contenido. El foco
   de teclado queda en el visor, no en el composer ni en el arbol.
6. `Esc` vuelve al chat. El explorer sigue abierto, conserva el archivo
   seleccionado y puede abrirse otro archivo inmediatamente.
7. `q` y `Space` + `e` siguen cerrando el explorer desde el chat. Dentro del
   visor solo `Esc` sale del modo de lectura; no se sobrecarga `q` para evitar
   ambiguedad con contenido y mantener una salida unica, visible y segura.

### Layout

```
╭ explorer ───────────╮  ╭ internal/tui/model.go · 42-71/612 ─────────╮
│ 󰉋 cmd               │  │  42  func (m Model) Update(msg tea.Msg) ... │
│ 󰝰 internal          │  │  43      switch ev := msg.(type) {          │
│   󰝰 tui             │  │  44      case EventMsg:                     │
│     model.go        │  │  45          m = m.foldEvent(ev)           │
│     view.go         │  │  46          ...                           │
│  go.mod             │  │                                             │
╰─────────────────────╯  ╰─────────────────────────────────────────────╯
```

- El explorer conserva su ancho y viewport actuales.
- El visor usa todo el ancho restante, incluyendo el espacio que antes ocupaban
  transcript, menu de autocompletado, composer y status.
- La cabecera contiene la ruta relativa del workspace y `primera-ultima/total`
  de lineas visibles. En un archivo de una sola linea usa `1-1/1`.
- Cada linea tiene gutter de ancho fijo calculado desde el total de lineas,
  alineado a la derecha. El gutter es tenue; el contenido resaltado es el foco.
- Las lineas largas no hacen wrap en v1: se recortan al ancho visible para
  preservar numeros de linea, rendimiento y navegacion vertical predecible.
- Al abrir un archivo, el viewport inicia en la linea 1. Al redimensionar, se
  conserva la primera linea visible, limitada al nuevo rango valido.

### Prioridad de teclado

Con el visor activo, los gates globales existentes conservan prioridad:

1. Ctrl+C (detiene la corrida y sale de la TUI).
2. Permiso pendiente y oferta de plan (`y`/`n`).
3. Visor activo (`Esc`, navegacion vertical).
4. Explorer abierto.
5. Leader `Space` + `e`, menu y composer.

No se permite abrir el visor mientras haya un permiso o una oferta de plan
pendientes. Si aparecen mientras el visor esta abierto, el gate se muestra y
toma el teclado; al resolverse, el visor permanece abierto.

## Arquitectura y contratos

### Estado del modelo

`internal/tui.Model` agrega un estado de modo de lectura separado del estado
del explorer. Debe guardar, como minimo:

- Ruta relativa seleccionada.
- Lineas originales del archivo, total de lineas y offset vertical.
- Contenido ya renderizado/resaltado por linea o una representacion equivalente
  que no vuelva a ejecutar el lexer por cada frame.
- Estado de error visible cuando la apertura no produce contenido.

El modo se activa solo despues de validar y cargar el archivo. Un fallo no
altera el transcript ni el composer: abre el visor con su estado de error y
`Esc` vuelve al chat normal.

`listFiles` sigue siendo la fuente de paths del arbol. La lectura recibe una
dependencia explicita e inyectable, por ejemplo `readFile(path string)
([]byte, error)`, configurada desde el mismo root de workspace que
`Engine.ProjectFiles`. La implementacion de produccion debe resolver la ruta y
rechazar escapes fuera del root antes de invocar `os.ReadFile`; los tests usan
un fake en memoria. El path mostrado y el path usado para leer siempre son
relativos, con separadores `/`.

### Clasificacion del contenido

La apertura sigue este orden, antes de crear el render:

1. Resolver y validar que la ruta queda dentro del workspace.
2. Leer el archivo completo una sola vez.
3. Rechazar si supera `maxFileViewerBytes` (valor v1: 1 MiB).
4. Rechazar como binario si contiene un byte NUL (`0x00`).
5. Normalizar CRLF a LF solo para el render y dividir en lineas. Un archivo
   vacio tiene total `0` y muestra una linea de estado vacio, no un gutter `1`.
6. Detectar lexer por nombre/ruta con Chroma. Si no hay lexer, usar lexer de
   texto plano; abrir un formato desconocido nunca es un error.

El limite se evalua sobre los bytes leidos. No se hacen previews ni se omite el
final de un archivo grande: se muestra el mensaje de limite para evitar una
vista incompleta que parezca fiel.

Los mensajes de estado son estables y asertables:

- `no se puede abrir <ruta>: <error>`
- `archivo binario: <ruta>`
- `archivo demasiado grande (> 1 MiB): <ruta>`
- `archivo vacio: <ruta>`

### Resaltado

El modulo usa `github.com/alecthomas/chroma/v2`, que ya esta en el grafo de
dependencias indirectas. La implementacion debe importarlo directamente y
actualizar `go.mod`/`go.sum` de forma deliberada. Chroma selecciona el lexer
por filename; el fallback es texto plano. Un formatter ANSI y un estilo oscuro
estable producen secuencias ANSI que Lip Gloss y el terminal pueden componer.

El renderer debe medir ancho visible ignorando escapes ANSI y recortar sin
cortar una secuencia de escape ni dejar el estilo abierto. Para ello se apoya
en la infraestructura ANSI ya presente en el proyecto, no en `len(string)`.
El gutter no pasa por Chroma y se compone fuera del contenido resaltado.

La implementacion separa la transformacion pura de contenido (normalizacion,
clasificacion, lexer y lineas renderizadas) del layout Bubble Tea. Esto permite
probar binarios, limites, fallback y numeracion sin terminal real.

### Viewport

No se reutiliza el viewport del transcript para evitar que offsets y scroll de
chat se mezclen. El visor mantiene su propio offset y calcula su alto desde la
terminal menos una linea de cabecera. Toda operacion de scroll y resize lo
clampa a `[0, max(totalLineas - altoVisible, 0)]`.

El contenido se renderiza como ventana de lineas `[offset, offset+altoVisible)`
para no construir una cadena gigantesca por frame. La apertura puede preparar
el resaltado completo del archivo (maximo 1 MiB); los frames posteriores solo
seleccionan la ventana y aplican recorte horizontal.

## Cambios previstos

| Archivo | Responsabilidad |
| --- | --- |
| `internal/tui/file_viewer.go` | Modelo puro de contenido, limites, clasificacion, resaltado y helpers de scroll/recorte. |
| `internal/tui/file_viewer_test.go` | Tests unitarios del visor y sus bordes. |
| `internal/tui/model.go` | Dependencia de lectura, transicion `Enter` archivo -> visor, foco, teclado y resize. |
| `internal/tui/model_test.go` | Tests de integracion del modelo, prioridad y preservacion del explorer. |
| `internal/tui/view.go` | Layout y render del visor activo. |
| `internal/tui/view_test.go` o tests existentes | Asserts de cabecera, gutter, clipping y estados visibles. |
| `cmd/atenea-tui/main.go` | Cablear lector seguro desde el root ya usado por la TUI. |
| `cmd/atenea-tui/main_test.go` o PTY existente | E2E del flujo de teclado si el harness actual vive aqui. |
| `docs/atenea-tui.md` | Documentar atajos, modo solo lectura y limites. |

Los nombres exactos pueden ajustarse a la estructura encontrada al implementar,
sin romper los contratos de este documento.

## Contrato de pruebas

La implementacion sigue el ciclo obligatorio `Safety net -> Understand -> RED
-> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`. Primero se ejecuta la suite
amplia; despues, cada prueba nueva se crea roja y se verifica por separado antes
de la produccion minima.

| Test | Comportamiento |
| --- | --- |
| `TestFileViewer_OpensTextWithLineNumbers` | Archivo de texto produce lineas 1-indexadas y gutter alineado. |
| `TestFileViewer_SelectsLexerFromPath` | Una ruta conocida recibe ANSI de Chroma; una desconocida usa texto plano. |
| `TestFileViewer_NormalizesCRLF` | CRLF no deja `\r` visible ni cambia el conteo de lineas. |
| `TestFileViewer_RejectsBinaryFile` | Byte NUL produce el estado binario y no renderiza bytes del archivo. |
| `TestFileViewer_RejectsOversizedFile` | Mas de 1 MiB produce el estado de limite y no intenta resaltar. |
| `TestFileViewer_ClampsScrollAndResize` | Scroll, PgUp/PgDn y resize nunca salen de rango. |
| `TestFileViewer_TruncatesAnsiSafely` | Recorte estrecho conserva ancho visible y reset ANSI correcto. |
| `TestModel_TreeEnterFileOpensViewer` | Enter en archivo activa visor, no agrega `@ruta` y no cierra explorer. |
| `TestModel_FileViewerEscRestoresChatAndTreeSelection` | Esc apaga visor y conserva arbol, cursor y offset. |
| `TestModel_FileViewerCapturesNavigationKeys` | j/k/flechas/PgUp/PgDn desplazan visor y no cambian composer ni arbol. |
| `TestModel_FileViewerPermissionGateWins` | Un permiso pendiente conserva prioridad sobre las teclas del visor. |
| `TestModel_FileViewerReadFailureShowsState` | Error de lectura es visible y Esc vuelve al chat sin perder estado. |
| `TestTUI_FileViewerFlowUnderPTY` | Flujo end-to-end: abrir explorer, seleccionar archivo, Enter, verificar cabecera/contenido, Esc y retorno al chat. |

El E2E usa un workspace temporal y un binario/harness real bajo PTY, no un fake
de `Model`, para cubrir secuencias de teclas, dimensiones terminales y escapes
ANSI. Debe comprobar visualmente el resultado durante la implementacion: gutter
alineado, explorer estable, cabecera sin solapamiento y clipping correcto en
terminal estrecha.

## Casos limite

- Explorer vacio o con error: `Enter` no abre visor ni panic.
- Cursor invalido despues de colapsar una carpeta: se aplica el clamp existente
  antes de abrir.
- Archivo desaparece entre listar y abrir: estado `no se puede abrir...`.
- Archivo sin newline final: la ultima linea se muestra y numera.
- Archivo con solo newline: muestra una linea vacia numerada; archivo de cero
  bytes usa el estado `archivo vacio`.
- Ruta con unicode: se muestra y recorta por ancho de celda, no por bytes.
- Terminal de 0x0, una fila o ancho menor al gutter: no panic; cabecera y
  contenido degradan al espacio disponible.
- Actividad del agente puede seguir llegando mientras se lee; transcript y
  composer conservan su estado subyacente y se muestran actualizado al volver.
- Cambios en disco despues de abrir no se reflejan hasta cerrar y abrir de
  nuevo, comportamiento intencional de v1.

## Criterios de exito

1. `Enter` sobre un archivo del explorer abre una vista de solo lectura en el
   area principal y no modifica el composer.
2. La vista tiene ruta, numeros de linea, scroll y resaltado por lenguaje con
   fallback de texto plano.
3. `Esc` devuelve al chat y conserva explorer, cursor y scroll del arbol.
4. Binarios, archivos mayores de 1 MiB y errores de lectura se reportan sin
   panic ni render parcial.
5. No existen acciones que modifiquen el filesystem.
6. La prueba E2E bajo PTY valida el flujo real y la inspeccion visual no muestra
   defectos de layout.
7. `go test ./...`, `go test -race ./...`, `gofmt -l .` y `go vet ./...`
   terminan limpios antes de cerrar la implementacion.

## TDD Cycle Evidence

Implementacion validada el 2026-07-09. El safety net inicial del worktree
ejecuto todos los paquetes salvo la raiz: `go test ./...` fallo al inicio porque
`main.go` embebe `frontend/dist`, directorio que no existe en un worktree nuevo.
Despues de `npm ci && npm run build` el gate completo paso. La advertencia de
chunks grandes de Vite y sus dos vulnerabilidades reportadas por `npm audit` no
fueron introducidas ni modificadas por esta feature.

| Fase | Evidencia requerida | Estado actual |
| --- | --- | --- |
| Safety net | `go test ./internal/tui` PASS antes de cambios; el `go test ./...` inicial solo fallo por `frontend/dist` ausente en la raiz | PASS focal |
| Understand | Lectura de `model.go`, `view.go`, `tree.go`, tests de explorer y wiring `cmd/atenea-tui` | PASS |
| RED | `Test(OpenFileViewer|WorkspaceFileReader)` fallo por API ausente; `TestModel_(TreeEnterFile|FileViewer)` fallo por builder/estado ausentes | PASS verificado |
| GREEN | Tests focales de contenido, viewport, modelo y layout verdes despues de implementar | PASS |
| TRIANGULATE | CRLF, vacio, binario, >1 MiB, fallback plano, alto 0, ancho estrecho, error de lectura, permiso y PTY | PASS |
| REFACTOR | `go test ./internal/tui` verde despues de separar `file_viewer.go`; E2E usa buffer sincronizado tras el hallazgo de `-race` | PASS |
| Evidence | `go test -race ./internal/tui ./cmd/atenea-tui`, `go test ./...`, `gofmt -l .`, `go vet ./...` PASS tras `npm ci && npm run build` | PASS |
