# TUI File Viewer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Anadir un visor TUI de solo lectura, abierto desde el explorer, que reemplaza el chat y mantiene explorer, cursor y scroll.

**Architecture:** `fileViewer` es un componente puro que clasifica, resalta y pagina un archivo. `Model` mantiene su foco y le delega el teclado despues de los gates de permisos/plan. Un `FileReader` inyectable lee solamente bajo la raiz del workspace.

**Tech Stack:** Go 1.25, Bubble Tea, Lip Gloss, Chroma v2, Charm ANSI y creack/pty.

---

## Reglas de ejecucion

- Usar el ciclo `Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence` en cada tarea.
- Ejecutar desde worktree dedicado; una rama nueva usa `posthog-code/`.
- Cada commit ejecuta `git commit -m '<titulo>' -m 'Generated-By: PostHog Code' -m 'Task-Id: 1e67e8ab-2799-4404-b66f-0d73583f7166'`.
- No introducir edicion, tabs, busqueda, recarga automatica, cambios Wails ni acciones de escritura de archivos.

## Mapa de archivos

- Crear `internal/tui/file_viewer.go` y `internal/tui/file_viewer_test.go` para el nucleo puro.
- Modificar `go.mod`, `go.sum` para importar Chroma directamente.
- Modificar `internal/tui/model.go`, `internal/tui/model_test.go` para foco y teclado.
- Modificar `internal/tui/view.go` para el layout alternativo.
- Modificar `cmd/atenea-tui/main.go` y crear `cmd/atenea-tui/main_test.go` mas `cmd/atenea-tui/testdata/file-viewer/project/hello.go` para wiring/E2E.
- Modificar `docs/atenea-tui.md` y `docs/superpowers/specs/2026-07-09-tui-file-viewer-design.md` para documentacion y evidencia.

## API base

```go
const maxFileViewerBytes = 1 << 20
var ErrFileViewerBinary = errors.New("archivo binario")
var ErrFileViewerTooLarge = errors.New("archivo demasiado grande")
type FileReader func(path string) ([]byte, error)
func WorkspaceFileReader(root string) FileReader
type fileViewer struct { path string; lines []string; offset int; message string; empty bool; lineCount int }
func openFileViewer(path string, content []byte) fileViewer
func openFileViewerError(path string, err error) fileViewer
func (v fileViewer) active() bool
func (v fileViewer) visibleRange(height int) (int, int)
func (v *fileViewer) scroll(delta, height int)
func (v *fileViewer) clamp(height int)
func (v fileViewer) header(width, height int) string
func (v fileViewer) render(width, height int) string
```

### Task 1: Nucleo de lectura, clasificacion y Chroma

**Files:** `internal/tui/file_viewer.go`, `internal/tui/file_viewer_test.go`, `go.mod`, `go.sum`.

- [ ] **Step 1: Safety net.** Run `go test ./internal/tui`; expected PASS.
- [ ] **Step 2: RED.** Crear `file_viewer_test.go` con estos casos: `TestOpenFileViewer_NormalizesCRLFAndNumbersLines`, `TestOpenFileViewer_EmptyBinaryAndLargeStates`, `TestOpenFileViewer_UsesLexerAndPlainFallback`, `TestWorkspaceFileReader_ReadsRelativePathAndRejectsEscape`.
- [ ] **Step 3: Verify RED.** Run `go test -run 'Test(OpenFileViewer|WorkspaceFileReader)' -v ./internal/tui`; expected failure por simbolos de la API base inexistentes.
- [ ] **Step 4: GREEN.** Implementar `WorkspaceFileReader` con `filepath.Abs`, `filepath.Clean(filepath.FromSlash(path))`, rechazo de absoluto, `.`, `..` y prefijo `../`, mas validacion final `filepath.Rel` antes de `os.ReadFile`. `openFileViewer` rechaza bytes `> 1<<20`, NUL, normaliza CRLF, conserva una linea para `"\n"`, y marca cero bytes como vacio. `openFileViewerError` usa exactamente `archivo binario: <path>`, `archivo demasiado grande (> 1 MiB): <path>`, y `no se puede abrir <path>: <error>`.
- [ ] **Step 5: Chroma.** Importar `formatters`, `lexers` y `styles` desde `github.com/alecthomas/chroma/v2`; `highlightFile` hace `lexers.Match(path)`, fallback `lexers.Text`, `Tokenise`, `formatters.TTY16m.Format(..., styles.Monokai, ...)` y vuelve a texto plano si hay error o cambian las lineas. Correr `go mod tidy`.
- [ ] **Step 6: GREEN y triangulacion.** Anadir asserts de `lineCount == 2` para `one\ntwo\n` y `lineCount == 1` para `\n`. Correr `go test ./internal/tui`; expected PASS.
- [ ] **Step 7: Commit.** Run `gofmt -w internal/tui/file_viewer.go internal/tui/file_viewer_test.go && go test ./internal/tui`; commit `feat(tui): add safe file viewer content loader`.

### Task 2: Viewport y render ANSI seguro

**Files:** `internal/tui/file_viewer.go`, `internal/tui/file_viewer_test.go`.

- [ ] **Step 1: Safety net.** Run `go test ./internal/tui`; expected PASS.
- [ ] **Step 2: RED.** Agregar `TestFileViewer_ScrollAndResizeClamp` con seis lineas: scroll `99` a alto `3` termina offset `3`, scroll `-99` termina `0`, clamp de offset `99` a alto `10` termina `0`, clamp con alto `0` termina `0`. Agregar `TestFileViewer_RenderShowsRangeAndNeverOverflows`: offset `2`, alto `2`, header `many.go · 3-4/5`, contiene lineas 3/4 y `ansi.StringWidth` nunca excede `0`, `1`, `4` ni `12`.
- [ ] **Step 3: Verify RED.** Run `go test -run 'TestFileViewer_(Scroll|Render)' -v ./internal/tui`; expected FAIL.
- [ ] **Step 4: GREEN.** Implementar `active`, `visibleRange`, `clamp` y `scroll` con los limites `[0,max(lineCount-height,0)]`. `header` usa `ansi.Truncate`; para contenido devuelve `path · first-last/total`, y para mensaje/vacio devuelve solo ruta truncada. `render` calcula gutter con `len(strconv.Itoa(lineCount))`, muestra el rango y trunca cada fila final con `ansi.Truncate(gutter+lines[index], max(width,0), "…")`.
- [ ] **Step 5: Gate.** Run `gofmt -w internal/tui/file_viewer.go internal/tui/file_viewer_test.go && go test -run 'TestFileViewer_(Scroll|Render)' -v ./internal/tui && go test ./internal/tui`; expected PASS. Confirmar que no se corta strings ANSI con slices ni se invoca Chroma durante scroll.
- [ ] **Step 6: Commit.** Commit `feat(tui): render scrollable highlighted file content`.

### Task 3: Estado del modelo, explorer y prioridades

**Files:** `internal/tui/model.go`, `internal/tui/model_test.go`.

- [ ] **Step 1: Safety net.** Run `go test ./internal/tui`; expected PASS.
- [ ] **Step 2: RED.** Anadir `viewerReader(map[string][]byte) FileReader` que devuelve `fs.ErrNotExist` para paths faltantes. Anadir: `TestModel_TreeEnterFileOpensViewerWithoutMention` (Enter deja `viewer.active`, `treeOpen` y composer vacio); `TestModel_FileViewerEscapePreservesExplorerCursor`; `TestModel_FileViewerScrollCapturesKeysButPermissionWins` (Down cambia offset; tras `EventMsg{Kind: session.KindToolPermissionRequested}` Down no lo cambia); y un test de error de lectura que muestra `no se puede abrir gone.go` y conserva arbol tras Esc.
- [ ] **Step 3: Verify RED.** Run `go test -run 'TestModel_(TreeEnterFile|FileViewer)' -v ./internal/tui`; expected FAIL.
- [ ] **Step 4: GREEN.** Agregar `fileReader FileReader` y `viewer fileViewer` junto al estado del arbol. Agregar builder `WithFileReader(read FileReader) Model`. Implementar `openTreeFile`: nil reader usa `errors.New("lector de archivos no configurado")`; error usa `openFileViewerError`; exito usa `openFileViewer` y `clamp`.
- [ ] **Step 5: Key routing.** Implementar `fileViewerHeight() int { return max(m.height-1, 0) }` y `handleFileViewerKey`: Esc limpia `viewer`, Down/j desplaza 1, Up/k -1, PgDown/PgUp desplazan `max(height,1)` y su negativo. En `handleTreeKey`, la rama archivo de Enter/l pasa a `m = m.openTreeFile(node.path)` sin cerrar arbol ni insertar mencion. En `handleKey`, despues de Ctrl+C, permiso y plan, pero antes de PgUp/PgDn y `treeOpen`, insertar `if m.viewer.active() { return m.handleFileViewerKey(msg) }`. En `tea.WindowSizeMsg` hacer `m.viewer.clamp(m.fileViewerHeight())`.
- [ ] **Step 6: Gate and commit.** Run `gofmt -w internal/tui/model.go internal/tui/model_test.go && go test -run 'TestModel_(TreeEnterFile|FileViewer)' -v ./internal/tui && go test ./internal/tui`; expected PASS. Verificar que Esc normal sigue deteniendo run fuera del visor y q solo cierra el arbol sin visor. Commit `feat(tui): open explorer files in read-only viewer`.

### Task 4: Reemplazar layout de chat por visor

**Files:** `internal/tui/view.go`, `internal/tui/model_test.go`.

- [ ] **Step 1: Safety net.** Run `go test ./internal/tui`; expected PASS.
- [ ] **Step 2: RED.** Agregar `TestModel_FileViewerReplacesChatWithHeaderAndGutter`: a 80x8 espera `explorer`, `main.go · 1-2/2`, numeros/contenido y ausencia de `build ·`. Agregar `TestModel_FileViewerNarrowTerminalNeverOverflows`: a 12x4 cada fila cumple `ansi.StringWidth <= 12`.
- [ ] **Step 3: Verify RED.** Run `go test -run 'TestModel_FileViewer(Replaces|Narrow)' -v ./internal/tui`; expected FAIL.
- [ ] **Step 4: GREEN.** Agregar `renderFileViewer(width,height)` que usa `statusStyle.Render(m.viewer.header(width,max(height-1,0)))`, cuerpo `m.viewer.render(width,max(height-1,0))` y un salto solo si hay cuerpo. Al inicio de `View`, si hay visor, renderizarlo como derecho del explorer, no como transcript/composer.
- [ ] **Step 5: Reuse layout math.** Extraer de la rama existente del arbol `contentWidth()` y `renderTreeAndContent(left,right string)` si faltan. Chat y visor usan exactamente ese calculo. `renderTreeAndContent` trunca cada fila unida con `ansi.Truncate(line,max(m.width,0),"")`; no usar `viewport` de chat ni `syncViewport` al ver archivo.
- [ ] **Step 6: Gate, inspeccion y commit.** Run `gofmt -w internal/tui/view.go internal/tui/model_test.go && go test -run 'TestModel_FileViewer(Replaces|Narrow)' -v ./internal/tui && go test ./internal/tui`; expected PASS. Run `go run ./cmd/atenea-tui`; inspeccionar Space+e, Enter en Go, resize estrecho, j/k, PgUp/PgDn y Esc: arbol fijo, gutter alineado, sin composer/status, colores sin bleed. Commit `feat(tui): render file viewer in chat area`.

### Task 5: Wiring, E2E PTY y docs

**Files:** `cmd/atenea-tui/main.go`, `cmd/atenea-tui/main_test.go`, `cmd/atenea-tui/testdata/file-viewer/project/hello.go`, `docs/atenea-tui.md`.

- [ ] **Step 1: Safety net.** Run `go test ./cmd/atenea-tui`; expected PASS.
- [ ] **Step 2: RED fixture/E2E.** Crear fixture `hello.go` con tres lineas logicas y el texto `hello from file viewer`. Crear `TestTUI_FileViewerFlowUnderPTY`: build del binario en `t.TempDir`, `pty.StartWithSize` 100x24 desde el fixture, `OPENROUTER_API_KEY=` y `ATENEA_DB` temporal, reader goroutine a `bytes.Buffer`, poll `waitForPTYText` de 3 segundos usando `ansi.Strip` cada 20ms. Secuencia: esperar `build · demo`; escribir `" e\r"`; esperar `hello.go`; escribir Enter; esperar `hello.go · 1-3/3` y `hello from file viewer`; Esc; esperar `build · demo`; Ctrl+C; esperar salida. Cerrar PTY y esperar proceso en defer.
- [ ] **Step 3: Verify RED.** Run `go test -run TestTUI_FileViewerFlowUnderPTY -v ./cmd/atenea-tui`; expected FAIL por falta de reader en wiring o flujo actual de mencion.
- [ ] **Step 4: GREEN wiring.** Anadir al builder de `main.go`: `.WithFileReader(tui.WorkspaceFileReader(root))`; root ya es el mismo usado por `Engine` y `ProjectFiles`.
- [ ] **Step 5: GREEN/docs.** Run `go test -run TestTUI_FileViewerFlowUnderPTY -v ./cmd/atenea-tui`; expected PASS. Si el arbol inicia en carpeta, anadir al E2E solo la secuencia minima explicita l/Down, sin debilitar asserts. Documentar: Enter abre solo lectura sin `@ruta`/cierre, Esc conserva arbol, j/k/Up/Down/PgUp/PgDn scroll, ruta+numeros+Chroma, y estados de binario/>1MiB/vacio/error.
- [ ] **Step 6: Commit.** Run `gofmt -w cmd/atenea-tui/main.go cmd/atenea-tui/main_test.go && go test ./cmd/atenea-tui`; expected PASS. Commit `test(tui): cover file viewer flow under pty`.

### Task 6: Quality gates y evidencia TDD

**Files:** `docs/superpowers/specs/2026-07-09-tui-file-viewer-design.md`.

- [ ] **Step 1: Run final gates.** Ejecutar en orden `go test -race ./internal/tui ./cmd/atenea-tui`, `go test ./...`, `gofmt -l .`, `go vet ./...`. Expected: tests PASS, gofmt sin salida, vet exit 0.
- [ ] **Step 2: Record evidence.** Reemplazar cada `Pendiente de implementacion` de `TDD Cycle Evidence` con comandos realmente ejecutados: safety net TUI, REDs de contenido/modelo/PTY, GREENs, CRLF/binario/>1MiB/alto 0/ancho estrecho/PTY, refactor y gates finales. Registrar fecha y nombres reales de tests, sin afirmar comandos no ejecutados.
- [ ] **Step 3: Commit.** Commit `docs: record TUI file viewer validation evidence`.

## Self-review

- Tareas 1-2 cubren root seguro, CRLF, vacio, binario, limite 1MiB, Chroma/fallback, gutter, scroll y ANSI.
- Tareas 3-4 cubren Enter, Esc, gates, preservacion del explorer, reemplazo visual y terminal estrecha.
- Tarea 5 aporta raiz de produccion, E2E real PTY y docs; tarea 6 exige race, suite completa, gofmt, vet y evidencia honesta.
- Todas las APIs usadas despues se fijan arriba; el plan no excede el alcance aprobado.
