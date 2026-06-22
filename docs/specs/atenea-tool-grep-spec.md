# Spec - Tool `grep` (ripgrep, busqueda de contenido)

Spec ejecutable de la **tool `grep`** estilo opencode, adaptada al mecanismo
hashline de Atenea. No es una reimplementacion generica de `grep`: es una tool de
busqueda rapida de contenido que usa `rg --json`, devuelve paths + lineas
coincidentes y, en Atenea, graba snapshots para que un `edit` posterior pueda
anclar contra las lineas que el modelo acaba de ver.

Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos.

## 1. Contexto

El registry ya tiene tools reales (`read`, `write`, `edit`) y todas comparten
`Root` + `SnapshotProvider` por sesion. `read` y `write` graban snapshots; `edit`
consume esos snapshots para validar hashline y rechazar edits sobre lineas no
vistas. Falta la tool de busqueda de contenido: el agente hoy puede leer un
archivo si ya sabe cual es, pero no puede descubrir donde vive un simbolo,
mensaje, tipo, llamada o test.

opencode usa una tool `grep` de produccion con este contrato:

- parametros: `pattern` requerido, `path` opcional, `include` opcional;
- motor: ripgrep por JSON (`--json`) con `--hidden`, `--no-messages`,
  `--glob=!**/.git/**`, `--` antes del patron y limite 100;
- salida: "No files found" si no hay matches; si hay, "Found N matches" y grupos
  por archivo con lineas numeradas; si llega al limite avisa que hay mas matches.

Atenea copia esa superficie porque es simple y probada, pero cambia una cosa
deliberadamente: cada grupo de archivo se imprime con header hashline
`[path#HASH]` y lineas `NUM:TEXTO`, igual que `read`. Asi `grep` no solo ayuda a
encontrar codigo; tambien habilita un `edit` seguro sobre las lineas que mostro.

## 2. Objetivo

Dejar especificada la tool `grep` para que una siguiente implementacion pueda
aterrizarla con TDD sin reinterpretar el contrato.

En `internal/tool`:

- `grep.go`: `GrepTool` (implementa `Tool`). Parsea `{pattern,path,include}`,
  resuelve `path` dentro de `Root`, llama a un `Searcher`, agrupa matches por
  archivo, lee los archivos con matches para grabar snapshots completos, marca
  como vistas las lineas emitidas y devuelve output hashline.
- `ripgrep.go`: `RgSearcher`, `GrepRequest`, `GrepMatch`, `GrepResult`,
  `ParseRipgrepJSON` y errores tipados (`GrepInvalidPatternError`, error de rg no
  disponible). El searcher real ejecuta `rg`; los tests inyectan un fake.
- `grep_test.go` y `ripgrep_test.go`: comportamiento de tool, sandbox, salida,
  snapshot, truncado, parsing JSON y errores.
- `app.go`: registrar `grep` con el mismo `Root` y `SnapshotProvider` de
  `read/write/edit`; permitir `"grep": true`.
- `internal/tool/doc.go`: mover `grep` de pendientes a builtins reales.

Esta fase no implementa `glob`, `bash`, conteos analiticos, highlights de
submatches, contexto alrededor de cada match, reemplazo masivo, ni bundle de
binario `rg` dentro de Wails.

## 3. Alcance

### Dentro

- Schema compatible con opencode:

```json
{
  "type": "object",
  "properties": {
    "pattern": {
      "type": "string",
      "description": "Patron regex para buscar en el contenido de archivos."
    },
    "path": {
      "type": "string",
      "description": "Archivo o directorio relativo al workspace donde buscar. Default: '.'."
    },
    "include": {
      "type": "string",
      "description": "Glob de archivos a incluir, por ejemplo '*.go' o '*.{ts,tsx}'."
    }
  },
  "required": ["pattern"]
}
```

- `GrepTool.Execute(ctx, input)`:
  - `json.Unmarshal` del input;
  - `pattern` requerido y no vacio;
  - `path` default `"."`;
  - `sandboxJoin(Root, path, "grep")` y, con FS real, rechazo de symlink fuera de
    `Root` antes de ejecutar `rg`;
  - llamada a `Searcher.Grep`;
  - lectura de cada archivo con matches para normalizar, hashear y grabar snapshot;
  - output agrupado por archivo con header hashline + lineas numeradas;
  - `RecordSeenLines` con exactamente las lineas emitidas.

- `RgSearcher`:
  - ejecuta `rg` con `exec.CommandContext`;
  - args v1:

```text
rg --no-config --json --hidden --no-messages \
  [--glob=<include>] --glob=!**/.git/** -- <pattern> <path>
```

  - lee stdout en streaming y parsea solo records JSON con `type == "match"`;
  - normaliza paths a relativos con `/`;
  - trunca texto de linea largo a 2000 runes para no inflar el output;
  - corta el proceso al ver `limit + 1` matches para saber si hay mas, y devuelve
    solo `limit` (default 100). `ParseRipgrepJSON` existe como helper puro para
    tests y entradas chicas; el searcher real no debe bufferizar stdout completo.

- Output:

```text
Found 3 matches
[internal/tool/read.go#1A2B]
42:func (*ReadTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
58:displayPath := in.Path

[internal/tool/write.go#3C4D]
71:func (*WriteTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
```

- Sin matches:

```text
No files found
```

- Truncado:

```text
Found 100 matches (more matches available)
...

(Results truncated. Consider using a more specific path or pattern.)
```

### Fuera

- `glob`: tool distinta para listar archivos por patron.
- `bash`: para conteos, pipes o busquedas exploratorias complejas.
- Conteo exacto de todos los matches. Como opencode, `grep` esta para encontrar
  archivos/lineas, no para analitica; si el usuario necesita contar matches en
  todo el repo, la tool correcta posterior sera `bash` con `rg`.
- Contexto antes/despues de cada match. Si el modelo necesita mas contexto, usa
  `read path:N-M`.
- Highlights de submatches. `rg --json` los trae; v1 los ignora en output para
  mantenerlo compacto.
- Busqueda fuera del workspace, follows de symlink fuera de `Root`, permisos por
  patron de ruta.
- Bundle cross-platform de `rg` en Wails. v1 usa `rg` en `PATH`; si no existe,
  devuelve un error accionable.
- Indexacion persistente o cache de resultados.

## 4. Tipos y contrato

### 4.1 `internal/tool/grep.go`

```go
type GrepTool struct {
	Root             string
	Searcher         Searcher
	FS               FileReader
	Snapshots        hashline.SnapshotStore
	SnapshotProvider SnapshotProvider
	MaxMatches       int
}

func NewGrepTool(root string, snaps hashline.SnapshotStore) *GrepTool
func NewGrepToolWithSnapshotProvider(root string, provider SnapshotProvider) *GrepTool

func (*GrepTool) Name() string        // "grep"
func (*GrepTool) Description() string // busqueda regex con output hashline
func (*GrepTool) Schema() json.RawMessage
func (*GrepTool) Execute(ctx context.Context, input json.RawMessage) (Result, error)
```

`Searcher` es una dependencia inyectable para no requerir `rg` en los tests de la
tool:

```go
type Searcher interface {
	Grep(ctx context.Context, req GrepRequest) (GrepResult, error)
}
```

`GrepTool` no confia en el texto devuelto por `rg` para generar el output final.
Usa los paths + numeros de linea del searcher, luego lee el archivo completo con
`FS.ReadFile`, normaliza igual que `read`, graba el snapshot completo y renderiza
las lineas desde `hashline.SplitLines`. Esto evita drift de formato entre `grep`
y `read`, y asegura que el hash de salida sea el mismo que `edit` verificara.

### 4.2 `internal/tool/ripgrep.go`

```go
type GrepRequest struct {
	Root    string // workspace root absoluto
	Path    string // archivo/directorio relativo ya validado, default "."
	Pattern string
	Include string
	Limit   int
}

type GrepMatch struct {
	Path string // relativo al workspace, con '/'
	Line int    // 1-indexed
	Text string // texto de la linea segun rg, ya limitado
}

type GrepResult struct {
	Matches   []GrepMatch
	Truncated bool
}

type RgSearcher struct {
	Binary string // default: "rg"
}

func NewRgSearcher() *RgSearcher
func (s *RgSearcher) Grep(ctx context.Context, req GrepRequest) (GrepResult, error)
func ParseRipgrepJSON(stdout []byte, limit int) (GrepResult, error)
```

Errores:

```go
type GrepInvalidPatternError struct {
	Pattern string
	Detail  string
}

type GrepUnavailableError struct {
	Binary string
	Err    error
}
```

Semantica de exit codes:

- `0`: matches encontrados, parsear stdout;
- `1`: sin matches, devolver `GrepResult{}`;
- `2` con stderr que contiene `regex parse error` o `error parsing regex`: devolver
  `GrepInvalidPatternError`;
- cualquier otro error/exit code: error de tool accionable con stderr acotado.

### 4.3 Formato hashline de salida

Por cada archivo con al menos una linea emitida:

1. leer archivo completo;
2. quitar BOM inicial, normalizar CRLF/CR a LF;
3. `tag := snaps.Record(abs, normalized)`;
4. emitir `hashline.FormatHeader(displayPath, tag)`;
5. emitir las lineas match como `NUM:TEXTO`;
6. marcar `RecordSeenLines(abs, tag, emittedLines)`.

Si un archivo aparece con multiples matches en la misma linea, la linea se emite
una vez y se marca una vez. Las lineas se ordenan por numero ascendente dentro de
cada archivo. Los archivos se ordenan por la primera aparicion que reporto `rg`
para respetar relevancia local del motor.

## 5. Semantica de `Execute`

1. **Parse del input.** `json.Unmarshal` de `{pattern,path,include}`. Input invalido
   -> `grep: input invalido`.
2. **Validacion.** `pattern == ""` -> `grep: pattern requerido`. `path == ""` ->
   `"."`. `include == ""` se ignora.
3. **Sandbox.** Resolver `path` con `sandboxJoin`. Rechazar rutas absolutas, `..`
   fuera de `Root`, y symlinks fuera de `Root` con FS real antes de ejecutar `rg`.
4. **Buscar.** `Searcher.Grep(ctx, GrepRequest{Root, Path, Pattern, Include,
   Limit})`. `Limit` default 100.
5. **Sin matches.** Devolver `Result{Output:"No files found"}`. No grabar snapshots.
6. **Agrupar.** Dedupe por `(path,line)`, agrupar por archivo, mantener primer
   orden de aparicion por archivo y ordenar lineas ascendente dentro de archivo.
7. **Leer para snapshot.** Por cada archivo con matches:
   - resolver el path relativo con `sandboxJoin`;
   - leer con `FS.ReadFile`;
   - si el archivo ahora no existe o no se puede leer, fallar con error accionable
     (resultado de `rg` stale; reintentar);
   - si contiene NUL, omitir el archivo con notice
     `[Cannot grep binary file <path>; content contains NUL bytes]` y no grabar
     snapshot.
8. **Render.** `Found N matches` (N = matches emitidos) + grupos hashline.
9. **Truncado.** Si `GrepResult.Truncated`, agregar `(more matches available)` al
   encabezado y el notice final de opencode. No setear `Result.Truncated`: ese flag
   sigue reservado para `OutputStore` por bytes.
10. **Return.** `Result{Output: output}`.

## 6. Plan TDD

### Safety net

- Antes de implementar: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Si falla por cambios previos, reportarlo como preexistente y no seguir editando
  codigo a ciegas.

### Understand

- Leer este spec.
- Leer `internal/tool/{read,write,edit,path,snapshots,registry}.go`.
- Leer `internal/tool/hashline/{format,hash,snapshot}.go`.
- Revisar referencias de opencode:
  - `packages/opencode/src/tool/grep.ts`;
  - `packages/core/src/ripgrep.ts`;
  - `packages/opencode/src/tool/grep.txt`.

Comportamiento esperado: schema `pattern/path/include`, search con ripgrep JSON,
limite 100, sandbox dentro del workspace, salida agrupada por archivo y snapshots
completos para que `edit` acepte las lineas emitidas.

### RED

1. `TestGrepTool_SearchesPatternAndFormatsHashlineGroups`: con `Searcher` fake que
   devuelve matches en dos archivos y `FS` fake con contenido real, `Execute`
   devuelve `Found 3 matches` + headers `[path#HASH]` + lineas numeradas. Falla
   porque `GrepTool` no existe.
2. `TestGrepTool_RecordsSnapshotsAndSeenLines`: despues del grep, `Head(abs)`
   existe para cada archivo y `Seen` contiene solo las lineas match.
3. `TestParseRipgrepJSON_MatchRecords`: parsea stdout JSON con records `match` y
   descarta records `begin/end/summary`. Falla porque `ParseRipgrepJSON` no existe.

Comandos:

```bash
go test -run TestGrepTool ./internal/tool
go test -run TestParseRipgrepJSON ./internal/tool
```

### GREEN

- Implementar lo minimo:
  - structs `GrepRequest`, `GrepMatch`, `GrepResult`;
  - `GrepTool` con `Searcher` fake-compatible;
  - render hashline agrupado;
  - `ParseRipgrepJSON` basico.
- Correr solo los tests rojos hasta verde:

```bash
go test -run TestGrepTool_SearchesPatternAndFormatsHashlineGroups ./internal/tool
go test -run TestGrepTool_RecordsSnapshotsAndSeenLines ./internal/tool
go test -run TestParseRipgrepJSON_MatchRecords ./internal/tool
```

### TRIANGULATE

Casos que evitan falso verde:

- `TestGrepTool_DefaultPathAndIncludePassedToSearcher`: sin `path`, request usa
  `"."`; con `include`, lo pasa intacto.
- `TestGrepTool_NoMatchesReturnsNoFilesFound`: output exacto `"No files found"` y
  sin snapshots.
- `TestGrepTool_TruncationNotice`: `Searcher` devuelve 100 matches + `Truncated`;
  output dice `(more matches available)` y notice final.
- `TestGrepTool_DedupesSameLine`: dos matches del mismo archivo/linea se emiten una
  vez y `Seen` tiene una entrada.
- `TestGrepTool_RejectsPathOutsideRoot`: `"../../etc/passwd"` falla sin llamar al
  searcher.
- `TestGrepTool_RejectsSymlinkOutsideRoot`: con FS real/tempdir, symlink fuera de
  root falla antes de `rg`.
- `TestGrepTool_InvalidInputErrors`: JSON invalido.
- `TestGrepTool_EmptyPatternErrors`: `pattern` vacio no ejecuta searcher.
- `TestGrepTool_ReadFailureAfterMatchErrors`: si `rg` encontro un archivo pero la
  lectura para snapshot falla, error accionable.
- `TestGrepTool_BinaryMatchedFileReturnsNoticeWithoutSnapshot`: archivo con NUL
  produce notice y no graba snapshot.
- `TestParseRipgrepJSON_TruncatesAtLimitPlusOne`: con `limit=2` y 3 records,
  devuelve 2 matches + `Truncated=true`.
- `TestParseRipgrepJSON_NormalizesPaths`: `./foo\bar.go` -> `foo/bar.go`.
- `TestParseRipgrepJSON_LimitsLongLineText`: linea > 2000 runes se corta con
  `...`.
- `TestRgSearcher_BuildsProductionArgs`: usando un runner fake (o helper de args),
  afirma `--no-config --json --hidden --no-messages --glob=<include>
  --glob=!**/.git/** -- <pattern> <path>`.
- `TestRgSearcher_StopsAfterLimitPlusOne`: con stdout streaming largo, corta el
  proceso despues de `limit + 1` matches y marca `Truncated=true`.
- `TestRgSearcher_InvalidPatternError`: exit 2 + stderr de regex -> error tipado.
- `TestRgSearcher_NoMatchesExitOne`: exit 1 -> resultado vacio, no error.
- `TestRgSearcher_ContextCancellation`: `ctx` cancelado corta el proceso y devuelve
  error de contexto.

Comandos:

```bash
go test -run TestGrepTool ./internal/tool
go test -run TestParseRipgrepJSON ./internal/tool
go test -run TestRgSearcher ./internal/tool
```

### REFACTOR

- Extraer helpers de test:
  - `fakeSearcher`;
  - `spySearcher`;
  - `grepToolWithFiles(t, files, matches)`;
  - fixtures JSON de ripgrep compactas.
- Compartir normalizacion de contenido con `read/write/edit` si aparece
  duplicacion real; si no, dejar helper privado en `grep.go`.
- Actualizar `internal/tool/doc.go`.
- Wiring final en `app.go`.

Gates:

```bash
gofmt -l .
go vet ./...
go test ./...
```

## 7. Criterios de aceptacion

1. `grep` aparece en las `Definitions` materializadas cuando `Permissions{"grep":
   true}`.
2. Schema exacto: `pattern` requerido; `path` e `include` opcionales.
3. `pattern` vacio, input JSON invalido, ruta absoluta o escape de root fallan
   antes de ejecutar el searcher.
4. `RgSearcher` usa ripgrep con flags de produccion equivalentes a opencode:
   `--no-config`, `--json`, `--hidden`, `--no-messages`, include opcional,
   exclusion de `.git`, `--`, patron, path.
5. Exit code 1 de `rg` -> `"No files found"` sin error; regex invalida -> error
   tipado accionable; `rg` no disponible -> error accionable.
6. Output con matches empieza con `Found N matches`; agrupa por archivo; cada grupo
   lleva `[path#HASH]` y lineas `NUM:TEXTO`.
7. Para cada archivo emitido, `grep` graba snapshot del archivo completo y marca
   exactamente las lineas emitidas como `Seen`, para que `edit` pueda usarlas.
8. Duplicados `(path,line)` se emiten una sola vez.
9. MaxMatches default 100; si hay mas, output avisa `(more matches available)` y
   agrega notice final. No usa `Result.Truncated` para este limite semantico.
10. Binarios detectados por NUL no graban snapshot y emiten notice; archivos que
    desaparecen entre `rg` y snapshot fallan con error claro.
11. `app.go` registra `grep` con el mismo `Root` y `SnapshotProvider` de las otras
    file tools, y permisos `"grep": true`.
12. `go test ./...`, `go vet ./...`, `gofmt -l .` pasan al cierre.

## 8. Comandos

```bash
# Safety net / cierre
go test ./...
go vet ./...
gofmt -l .

# Ciclo especifico
go test -run TestGrepTool ./internal/tool
go test -run TestParseRipgrepJSON ./internal/tool
go test -run TestRgSearcher ./internal/tool

# Integracion manual opcional si rg esta instalado
rg --version
go test -run TestRgSearcher_Integration ./internal/tool
```

## 9. Tabla de evidencia esperada

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite verde antes de implementar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Contrato local + opencode grep leidos | `internal/tool/{read,edit,path,snapshots}.go`, opencode `grep.ts`, `ripgrep.ts`, `grep.txt` | comportamiento identificado |
| RED | Tests de tool y parser escritos primero | `grep_test.go`, `ripgrep_test.go` + `go test -run ...` | fallo esperado |
| GREEN | `GrepTool` + parser ripgrep minimos | `internal/tool/{grep,ripgrep}.go` | tests especificos pasan |
| TRIANGULATE | Sandbox, truncado, no matches, regex invalida, dedupe, snapshots, args rg | `go test -run 'TestGrepTool\|TestRgSearcher\|TestParseRipgrepJSON' ./internal/tool` | casos pasan |
| REFACTOR | Helpers, doc.go, app.go wiring | `gofmt -l .`, `go vet ./...`, `go test ./...` | suite verde, `grep` registrado |

## 10. Riesgos y decisiones

- **Salida hashline aunque opencode no la usa.** opencode imprime paths + lineas
  con `Line N`. Atenea necesita que `edit` valide frescura y `seenLines`, asi que
  cada archivo se emite como seccion hashline. Es una adaptacion local, no una
  desviacion accidental.
- **`grep` graba snapshot completo.** Aunque solo muestre lineas match, el hash del
  header es del archivo completo. Esto mantiene la misma regla de `read`: el hash
  es la compuerta de frescura del archivo vivo.
- **Lineas vistas exactas.** Solo las lineas emitidas quedan en `Seen`. Si el modelo
  quiere editar alrededor del match, debe pedir `read path:N-M`. Esto evita editar
  codigo que no vio.
- **No conteo exacto.** El limite 100 y el aviso de truncado hacen que `grep` sea
  una herramienta de descubrimiento, no de metricas. Para contar, `bash` con `rg`
  sera la via.
- **`rg` en PATH en v1.** Es la ruta mas barata y testeable. Empaquetar binarios
  por plataforma en Wails es otra fase, con pruebas de instalacion/build.
- **No `Result.Truncated`.** El truncado por match es in-band para el modelo; el
  flag `Result.Truncated` queda reservado al `OutputStore`, igual que en `read`.
- **Orden de archivos.** Se mantiene el primer orden de aparicion de `rg` porque
  suele reflejar el recorrido del repo; dentro de cada archivo se ordena por linea
  para que el output sea legible y estable.
- **`--hidden` con exclusion `.git`.** Se copia opencode: buscar en archivos ocultos
  utiles, pero no dentro de `.git`. `rg` sigue respetando ignore files porque no se
  usa `--no-ignore`.
- **No se confia en el texto de `rg` para el hashline.** El texto de `rg` sirve
  para localizar; el output final se renderiza desde el archivo leido para snapshot.
  Asi el hash, la numeracion y la normalizacion quedan alineados con `read`.

## 11. Recortes seguros para v1

- Se mantiene: `pattern/path/include`, `rg --json`, limite 100, sandbox, error de
  regex invalida, output hashline, snapshots completos y `Seen` por linea emitida.
- Se omite: contexto, highlights, conteos exactos, reemplazo masivo, cache,
  bundle de `rg`, busqueda fuera del workspace, permisos por patron de ruta.

## 12. Fuentes

- opencode `grep.ts` (verificado 2026-06-22):
  `https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/grep.ts`
  - schema `pattern/path/include`, permiso `grep`, path default, limit 100, output
    "No files found" / "Found N matches", grupos por archivo y notice de truncado.
- opencode `ripgrep.ts`:
  `https://github.com/anomalyco/opencode/blob/dev/packages/core/src/ripgrep.ts`
  - `GrepInput`, `RawMatch`, `InvalidPatternError`, `rg --no-config --json
    --hidden --no-messages --glob=!**/.git/** -- pattern file`, parse JSON y
    normalizacion de paths.
- opencode `grep.txt`:
  `https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/grep.txt`
  - descripcion de uso: busqueda regex rapida, `include`, paths + numeros de
    linea, y recomendacion de usar `rg` directo para conteos.
- Atenea local: `internal/tool/{read,write,edit,path,snapshots,registry}.go`,
  `internal/tool/hashline/*`, `docs/specs/atenea-tool-read-spec.md`,
  `docs/specs/atenea-tool-edit-spec.md`, `AGENTS.md`.
