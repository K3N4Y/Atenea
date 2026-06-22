# Spec — Tool `read` (hashline, fase 1 del track read/edit)

Spec ejecutable de la **tool `read`** estilo `can1357/oh-my-pi`. No es un hito
numerado del loop (`docs/atenea-agent-loop-roadmap.md` cierra en M10): es la
**primera fase** del track read/edit descrito en `docs/atenea-read-edit-tools.md`,
que arranca ahora que el loop ya esta verde y cableado (M1..M10) y el unico
builtin real es `echo`.

Define el estado final, el alcance, el plan TDD y los criterios de aceptacion
para dejar el `read`: lee un archivo, lo numera y antepone el header hashline
`[path#HASH]`, grabando el snapshot que habilita al `edit` (fase 2). Se trabaja
con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos, igual que el resto de `docs/`.

## 1. Contexto

El registry de tools (M4) ya materializa `Definitions` y un `Settle` cerrado
sobre los permisos, acota el output grande via `OutputStore`, y el runner
(M5..M8) asienta tools concurrentes publicando `Tool.Called`/`Tool.Success`/
`Tool.Failed`. M9 cableo el loop a Wails y M10 lo dejo sobre `Store` SQLite y
proveedor real. El frontend ya renderiza `Tool.*` (`ToolCall.vue`). Falta lo
unico que convierte a Atenea en un agente de codigo util: **tools reales**. La
primera es `read`, por ser la mas segura (sin efectos laterales) y porque su
salida hashline es la **precondicion** del `edit`.

El diseno completo del par read/edit esta en `docs/atenea-read-edit-tools.md`
(investigado sobre oh-my-pi, paquetes `coding-agent` y `hashline`). La idea
central es **hashline**: el `read` numera cada linea y antepone un header con un
**hash del archivo completo** (`[path#HASH]`); el `edit` direcciona por numero de
linea pero solo aplica si el archivo **sigue hasheando al mismo HASH**. El ancla
es el numero de linea; el hash es la **compuerta de frescura**. Esta spec
construye la mitad `read` de ese mecanismo: el hash, el formato numerado y el
`SnapshotStore` que el `read` graba al leer (`docs/atenea-read-edit-tools.md`,
secciones "Tool `read`" y "El `SnapshotStore`").

Esta fase implementa los pasos **1, 2 y 5** del orden de implementacion del doc
(hash + normalizacion; formato + numeracion del `read`; `SnapshotStore` en
memoria). Los pasos 3, 4, 6, 7 (parser de patch, apply, patcher, recovery) son la
fase `edit`.

## 2. Objetivo

Dejar listo el motor hashline minimo y la tool `read`, probados en aislamiento
contra un FS fake, y registrados en el agente:

En `internal/tool/hashline` (motor puro, sin FS ni agente):

- `hash.go`: `ComputeFileHash(text) string` con su normalizacion (trailing
  whitespace + CR), que produce 4 hex mayusculas de los 16 bits bajos de
  `xxHash32` con seed 0. Mismo texto (CRLF/LF, trailing ws) -> mismo tag.
- `format.go`: `FormatHeader(path, hash) string` -> `[path#HASH]`, y el numerado
  `NUM:TEXTO` de un bloque de lineas con su numero real en el archivo.
- `snapshot.go`: la interfaz `SnapshotStore` (`Head`/`ByHash`/`Record`/
  `RecordSeenLines`/`Invalidate`) **completa** del doc y una implementacion en
  memoria con read-fusion (texto identico reusa tag). El `read` solo ejercita
  `Record`/`Head`/`RecordSeenLines`; `ByHash`/historial quedan listos para el
  recovery del `edit` (fase 2) sin rehacer la interfaz.

En `internal/tool`:

- `read.go`: `ReadTool` (implementa la interface `Tool` de M4). Lee el archivo,
  detecta binario, normaliza a LF (sin BOM), computa el hash del archivo
  **completo**, graba el snapshot, formatea el header + las lineas pedidas
  (archivo entero hasta el limite, o el rango `:N-M`/`:N`), marca las lineas
  vistas y devuelve el `Result`.
- tests de comportamiento en `read_test.go` (y los del motor en
  `hashline/*_test.go`).

En `app.go`:

- construir un `SnapshotStore` por app/sesion y un `ReadTool` con la raiz del
  workspace, registrarlo en el `Registry` y permitir `"read": true` (igual que ya
  esta `echo`).

Esta fase **no** construye el `edit`, ni el parser/apply/patcher, ni el recovery
3-way-merge, ni folding/tree-sitter, ni los selectores ricos (`:N+K`, open-ended,
listas, `:raw`), ni el manejo de imagenes/PDF/archivos/URIs/URLs.

## 3. Alcance

### Dentro

- `internal/tool/hashline/hash.go`: `ComputeFileHash`, `normalizeForHash`.
- `internal/tool/hashline/format.go`: `FormatHeader`, numerado `NUM:TEXTO`,
  split a lineas LF.
- `internal/tool/hashline/snapshot.go`: `Snapshot`, `SnapshotStore` (interfaz
  completa), impl en memoria `MemSnapshotStore` con read-fusion y candado.
- `internal/tool/read.go`: `ReadTool`, `NewReadTool`, parse del `path` +
  selector (`:N`, `:N-M`), abstraccion `FileReader` para tests.
- `internal/tool/hashline/*_test.go` y `internal/tool/read_test.go`.
- `app.go`: construir `SnapshotStore` + `ReadTool`, registrar y permitir `read`.
- Actualizar `internal/tool/doc.go` (aterrizo el primer builtin con FS).
- `go.mod`: agregar una implementacion Go de `xxHash32` (seed 0).

### Fuera (no hacer en esta fase)

- La tool `edit` y todo su motor: `parser.go` (texto de patch -> `[]Edit`),
  `apply.go` (`applyEdits`), `patcher.go` (prepare/commit, verificacion de hash,
  `seenLines` check, all-or-nothing), `recovery.go` (3-way-merge). Fase 2, ver
  `docs/atenea-read-edit-tools.md`. El `read` deja el `SnapshotStore` poblado;
  el `edit` lo consume.
- **Selectores ricos**: `:N+K`, open-ended `:N-`, listas por coma, `:raw`,
  URIs internas (`omp://`, `issue://`), URLs. v1 soporta archivo completo, `:N` y
  `:N-M`. El resto llega cuando el agente lo pida.
- **Context lines** (oh-my-pi: `RANGE_LEADING_CONTEXT_LINES=1`,
  `RANGE_TRAILING_CONTEXT_LINES=3`): es una optimizacion para reducir fallos de
  ancla del `edit` por una linea, no el mecanismo de seguridad. Se agrega cuando
  aterrice el `edit` (que es quien sufre el off-by-one). v1 emite exactamente las
  lineas pedidas y marca exactamente esas como vistas.
- **Folding/summarization** (tree-sitter, `MAX_SUMMARY_*`): optimizacion para
  archivos enormes; fuera de v1 (`docs/atenea-read-edit-tools.md`, "Recortes v1").
- **Imagenes, PDF/markit, zip, SQLite, notebooks, conflict://**: handlers
  dedicados de oh-my-pi; fuera de v1. v1 lee texto; binario -> notice.
- **Recovery por drift** y `ByHash`/historial como mecanismo activo: la interfaz
  los declara (para que el `edit` no la rehaga), pero esta fase solo los prueba
  minimamente; el 3-way-merge es fase 2.
- **Streaming por chunks** de archivos gigantes: v1 lee el archivo entero y
  corta por lineas en memoria (alcanza para un editor de escritorio). El chunking
  (`READ_CHUNK_SIZE`) llega si hace falta.
- **Modelo de permisos rico** (ask/por patron de ruta): sigue siendo el set de
  nombres de M4. `read` es solo-lectura; el permiso rico importa para `edit`/`bash`.

## 4. Tipos y contrato

### 4.1 `internal/tool/hashline/hash.go` — la pieza critica

Es lo unico que **no se puede equivocar**: si el hash no es estable y
determinista, el `edit` corrompe archivos. RE2 (el motor de `regexp` de Go) no
soporta el lookahead `(?=\n|$)` del original; se porta capturando el separador y
re-emitiendolo con `$1`.

```go
package hashline

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/pierrec/xxHash/xxHash32" // o equivalente; seed 0
)

// hashHexLen son los 4 hex del tag (16 bits). Espejo de HL_FILE_HASH_LENGTH.
const hashHexLen = 4

// trailingWS matchea whitespace al final de cada linea o del texto. El original
// usa /[ \t\r]+(?=\n|$)/g; RE2 no tiene lookahead, asi que capturamos el
// separador (\n o fin) y lo re-emitimos con $1. Incluir \r hace que CRLF y LF
// hasheen igual; quitar trailing ws hace que el espacio al final no invalide el
// tag.
var trailingWS = regexp.MustCompile(`[ \t\r]+(\n|$)`)

func normalizeForHash(text string) string {
	return trailingWS.ReplaceAllString(text, "$1")
}

// ComputeFileHash devuelve el tag hashline del texto: 4 hex mayusculas de los 16
// bits bajos de xxHash32 (seed 0) sobre el texto normalizado. Bytes identicos
// (salvo trailing ws / CRLF) -> mismo tag (habilita read-fusion). El valor no
// necesita coincidir con oh-my-pi: solo necesita ser determinista y consistente
// DENTRO de Atenea (el read produce el tag, el edit lo verifica con la misma
// funcion). Un test-vector fija el valor para que un refactor no lo mueva.
func ComputeFileHash(text string) string {
	sum := xxHash32.Checksum([]byte(normalizeForHash(text)), 0)
	return strings.ToUpper(fmt.Sprintf("%0*x", hashHexLen, sum&0xFFFF))
}
```

### 4.2 `internal/tool/hashline/format.go`

```go
// FormatHeader arma el header de seccion hashline: "[path#HASH]". El edit lo
// re-parsea para sacar path y hash esperado.
func FormatHeader(path, hash string) string // -> "[" + path + "#" + hash + "]"

// SplitLines parte el texto YA normalizado a LF en lineas 1-indexed. Si el texto
// termina en "\n", el segmento vacio final NO cuenta como linea (un archivo
// "a\nb\n" tiene 2 lineas, no 3). Es la base de la numeracion y del conteo total.
func SplitLines(text string) []string

// NumberLines formatea lineas[from..to] (inclusive, 1-indexed sobre el archivo
// completo) como "NUM:TEXTO\n...". El separador linea/cuerpo es ":". Los numeros
// reflejan la posicion real en el archivo, no la del rango.
func NumberLines(lines []string, from, to int) string
```

### 4.3 `internal/tool/hashline/snapshot.go`

Interfaz **completa** del doc (para que el `edit` no la rehaga), impl en memoria.

```go
// Snapshot es una version completa de un archivo que un read mostro. Text es el
// texto normalizado a LF (sin BOM); Hash == ComputeFileHash(Text); Seen son las
// lineas 1-indexed que el read emitio (lo que el edit permitira tocar).
type Snapshot struct {
	Path string
	Text string
	Hash string
	Seen map[int]struct{}
}

// SnapshotStore guarda, por path, versiones completas de archivos leidos. Habilita
// dos cosas del edit (fase 2): validar frescura por hash y rechazar ediciones a
// lineas no leidas. En v1 el read solo usa Record/Head/RecordSeenLines; ByHash,
// Invalidate y el historial quedan para el recovery del edit.
type SnapshotStore interface {
	Head(path string) *Snapshot           // version mas reciente
	ByHash(path, hash string) *Snapshot   // version cuyo tag == hash (recovery, fase 2)
	Record(path, fullText string) string  // graba; devuelve el tag (reusa si el texto ya estaba)
	RecordSeenLines(path, hash string, lines []int)
	Invalidate(path string)
}

// MemSnapshotStore es la impl en memoria por sesion. Read-fusion: Record de texto
// identico al Head reusa el tag y no duplica version. Seguro para uso concurrente
// (mutex): el runner asienta tools en paralelo, igual que el OutputStore de M4.
// El historial por path es corto y acotado; afinarlo (LRU, bytes) se hace cuando
// el recovery del edit lo necesite.
type MemSnapshotStore struct { /* mu, byPath map[string][]*Snapshot */ }

func NewMemSnapshotStore() *MemSnapshotStore
```

### 4.4 `internal/tool/read.go`

```go
// FileReader abstrae la lectura del FS para testear el read sin tocar disco. El
// default envuelve os.ReadFile; los tests inyectan un mapa en memoria.
type FileReader interface {
	ReadFile(name string) ([]byte, error)
}

// ReadTool es el builtin read: lee un archivo de texto bajo la raiz del workspace,
// lo numera con header hashline y graba el snapshot que habilita al edit. Solo
// lectura: sin efectos laterales sobre el FS.
type ReadTool struct {
	Root      string                  // raiz del workspace; las rutas se resuelven dentro
	FS        FileReader              // os por defecto; fake en tests
	Snapshots hashline.SnapshotStore
	MaxLines  int                     // limite por defecto (v1: 2000); <=0 usa el default
}

func NewReadTool(root string, snaps hashline.SnapshotStore) *ReadTool

func (*ReadTool) Name() string        // "read"
func (*ReadTool) Description() string // explica el path con :N-M y el formato hashline
func (*ReadTool) Schema() json.RawMessage

// Execute parsea el input (path con selector embebido), resuelve la ruta dentro
// de Root, lee y formatea. Ver "Semantica".
func (*ReadTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error)
```

Schema (lo que ve el modelo) — un solo `path`, con el selector embebido como en
oh-my-pi:

```json
{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Ruta del archivo relativa al workspace. Opcional: ':N' una linea, ':N-M' un rango (p.ej. 'internal/tool/read.go:10-40'). La salida viene numerada con un header [path#HASH]; usa ese header al editar."
    }
  },
  "required": ["path"]
}
```

## 5. Semantica del `read`

`Execute` hace, en orden:

1. **Parse del input.** `json.Unmarshal` del `path` (nunca match de string). Input
   invalido -> error de tool (Settle -> `Tool.Failed`).
2. **Parse del selector.** Separa el sufijo `:<sel>` del path. v1 reconoce `:N` y
   `:N-M` (enteros, 1-indexed). Sin sufijo valido -> archivo completo. Un sufijo
   con forma de selector pero invalido (`:0`, `:5-2`) -> error de tool accionable.
3. **Resolver la ruta.** `filepath.Clean(filepath.Join(Root, rel))`; si el
   resultado escapa de `Root` (`..`) -> error **sin** leer (compuerta de sandbox).
4. **Leer.** `FS.ReadFile`. No existe / sin permiso -> error de tool propagado.
5. **Binario.** Si el contenido tiene un byte NUL -> devolver el notice
   `[Cannot read binary file <path>; content contains NUL bytes (binary or UTF-16)]`
   y **no** grabar snapshot (no es editable por hashline).
6. **Normalizar.** Quitar BOM, CRLF -> LF. Este es el texto que se guarda y se
   numera. (Restaurar line endings/BOM al escribir es problema del `edit`.)
7. **Hash del archivo COMPLETO.** `tag = Snapshots.Record(absPath, normalized)`.
   Incluso en un read por rango el hash es del archivo entero: asi el header
   fingerprinttea todo el archivo y cualquier ancla del `edit` valida mientras el
   archivo no cambie (comportamiento de oh-my-pi: el range-read re-lee el archivo
   completo). `Record` reusa el tag si el texto ya estaba (read-fusion).
8. **Elegir la ventana.**
   - Sin selector: lineas `1..min(total, MaxLines)`. Si `total > MaxLines`,
     truncar y anexar el notice de continuacion (paso 10).
   - `:N`: la linea `N`. `:N-M`: `N..M`. Fuera de rango (`N > total`) -> notice
     `Line N is beyond end of file (<total> lines total).` y `Seen` vacio (el
     snapshot igual se graba: el archivo existe).
9. **Formatear.** `FormatHeader(displayPath, tag) + "\n" + NumberLines(...)`.
   `displayPath` es la ruta relativa al workspace (la que el `edit` volvera a
   resolver). Los numeros son la posicion real en el archivo.
10. **Notice de truncado.** Si se corto por `MaxLines`, anexar
    `\n\n[<N> more lines in file. Use :<nextOffset> to continue]`. Es in-band: el
    modelo lo lee y pide el siguiente rango. (Esto es distinto de
    `tool.Result.Truncated`, que lo setea el `OutputStore` por bytes; ver Riesgos.)
11. **Marcar lineas vistas.** `Snapshots.RecordSeenLines(absPath, tag, ventana)`
    con exactamente las lineas emitidas (whole-file: `1..fin`; rango: `N..M`
    clamp). El `edit` rechazara anclas a lineas fuera de este set.
12. **Devolver** `tool.Result{Output: formatted}`. El `OutputStore` del registry
    acota por bytes si hiciera falta; normalmente el self-limit por lineas ya lo
    mantiene chico.

## 6. Plan TDD

Se ataca de adentro hacia afuera, en sub-ciclos (igual que el orden del doc): hash
-> format -> snapshot -> tool. Cada sub-ciclo RED/GREEN; al final TRIANGULATE de
la tool.

### Safety net

- Estado base verde antes de tocar nada: la fase agrega un paquete nuevo
  (`hashline`), un archivo nuevo (`read.go`) y toca `app.go` solo para registrar.
- Comando: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Resultado esperado: pasa limpio (M1..M10). Si algo falla, se reporta como
  preexistente y no se sigue a ciegas. Tras `go get` del xxHash32, re-correr
  `go build ./...` para confirmar que la dependencia resuelve.

### Understand

- Leer `docs/atenea-read-edit-tools.md` (secciones "La idea central: hashline",
  "El hash", "Tool `read`", "El `SnapshotStore`", "Recortes v1") y este spec.
- Leer `internal/tool/registry.go` (la interface `Tool`, `Result`) y `echo.go`
  (el patron de builtin) para seguir el contrato de M4.
- Comportamiento esperado: header `[path#HASH]` + `NUM:TEXTO`; hash estable a
  CRLF/trailing-ws; snapshot grabado con texto completo y lineas vistas; rango y
  truncado con notices accionables; binario/inexistente/escape de ruta seguros.

### RED (sub-ciclos, cada uno falla primero)

1. `TestComputeFileHash_StableAcrossCRLFAndTrailingWhitespace`: `"a \nb\r\n"` y
   `"a\nb\n"` -> el mismo tag. Referencia a `ComputeFileHash`, que no existe -> no
   compila -> RED.
2. `TestComputeFileHash_FourUppercaseHex`: el tag matchea `^[0-9A-F]{4}$`, y un
   test-vector fija el valor para una entrada conocida (ancla el determinismo).
3. `TestFormatHashline_HeaderAndNumberedLines`: dado `["package main","",
   "func main(){}"]` y un tag, produce `[p#TAG]\n1:package main\n2:\n3:func main(){}`.
4. `TestReadTool_WholeFileHasHashHeaderAndNumberedLines`: leer un archivo de 3
   lineas via FS fake -> `Output` empieza con `[path#HASH]` y numera 1..3.
5. `TestReadTool_RecordsSnapshotAndSeenLines`: tras leer, `Snapshots.Head(abs)`
   tiene el texto normalizado completo, `Hash` == el del header, y `Seen` == {1,2,3}.

- Comandos: `go test -run TestComputeFileHash ./internal/tool/hashline`,
  `go test -run TestReadTool ./internal/tool` -> fallos esperados.

### GREEN

- Escribir el minimo por sub-ciclo: `hash.go`, luego `format.go`, luego
  `snapshot.go` (solo `Record`/`Head`/`RecordSeenLines` funcionando; `ByHash`/
  `Invalidate` pueden quedar con impl simple), luego `read.go`.
- `go get` de la lib xxHash32 al llegar al GREEN del hash.
- Correr solo el test rojo de cada sub-ciclo hasta verde.

### TRIANGULATE

Casos que evitan falso verde (los borde del doc, "Casos que NO se pueden saltar"
aplicables al `read`):

- `TestComputeFileHash_ChangesOnContentChange`: un cambio real de contenido mueve
  el tag (con entradas elegidas para no colisionar en 16 bits).
- `TestReadTool_RangeSelectorReadsSubsetButHashesFullFile`: `path:2-3` numera solo
  2 y 3 (con sus numeros reales), el header tag == hash del archivo **completo**,
  el snapshot guarda el archivo entero y `Seen` == {2,3}.
- `TestReadTool_SingleLineSelector`: `path:2` -> solo la linea 2.
- `TestReadTool_TruncatesAtLineLimitWithContinuationNotice`: archivo > `MaxLines`
  -> primeras `MaxLines` lineas + `[<N> more lines in file. Use :<offset> to
  continue]`; `Seen` == 1..MaxLines.
- `TestReadTool_OutOfRangeSelectorReportsBeyondEOF`: `:100-200` en archivo de 5
  lineas -> notice `Line 100 is beyond end of file (5 lines total).`; snapshot
  grabado, `Seen` vacio.
- `TestReadTool_InvalidSelectorErrors`: `:0`, `:5-2`, `:abc` -> error de tool
  accionable (no panico).
- `TestReadTool_MissingFileReturnsToolError`: FS devuelve not-exist -> error
  propagado por `Settle` (sera `Tool.Failed`); sin snapshot.
- `TestReadTool_BinaryFileReturnsNotice`: contenido con NUL -> notice de binario;
  `Snapshots.Head` nil (no grabo).
- `TestReadTool_RejectsPathOutsideRoot`: `"../../etc/passwd"` -> error **sin**
  llamar a `FS.ReadFile` (verificado con un spy: 0 lecturas).
- `TestReadTool_InvalidInputErrors`: input JSON malo (`{`) -> error.
- `TestSnapshotStore_RecordIdenticalReusesTag`: `Record` del mismo texto dos veces
  -> mismo tag, una sola version (read-fusion).
- `TestSnapshotStore_ByHashFindsRecordedVersion`: grabar dos versiones distintas;
  `ByHash` encuentra la vieja por su tag (minimo, para el recovery del edit).
- `TestSnapshotStore_ConcurrentRecord` con `-race`: varios `Record` en paralelo
  (el runner asienta tools concurrentes; el store es estado mutable compartido).

- Comandos:
  - `go test -run TestComputeFileHash ./internal/tool/hashline`
  - `go test -run TestSnapshotStore ./internal/tool/hashline`
  - `go test -run TestReadTool ./internal/tool`
  - `go test -race -run TestSnapshotStore ./internal/tool/hashline`

### REFACTOR

- Limpieza sin cambiar comportamiento: factorizar helpers de test (FS fake
  map-backed; un `readInto(t, files, input)` que arme el `ReadTool` con un store
  fresco); extraer el parse de selector si crece; actualizar
  `internal/tool/doc.go` (aterrizo el primer builtin con FS; `edit` sigue
  pendiente con su fase).
- Verificar la suite verde tras formatear.
- Comando: `gofmt -w internal app.go && go vet ./... && go test ./...`.

### Wiring (al final, como `echo`)

- `app.go`: construir `hashline.NewMemSnapshotStore()` y
  `tool.NewReadTool(workspaceRoot, snaps)`, sumarlo al `NewRegistry(...)` y
  agregar `"read": true` a `Permissions`. Un test ligero en `app_test.go` puede
  confirmar que `read` aparece en las `Definitions` materializadas.

## 7. Criterios de aceptacion (Done when)

1. Existe `hashline.ComputeFileHash`: mismo texto modulo CRLF/trailing-ws -> mismo
   tag; cambio real -> tag distinto; el tag es `^[0-9A-F]{4}$` y un test-vector
   fija su valor.
2. Existen `hashline.FormatHeader`, `SplitLines` y `NumberLines`: producen
   `[path#HASH]` y `NUM:TEXTO` con numeros reales; `SplitLines` no cuenta el
   segmento vacio final de un archivo terminado en `\n`.
3. Existe `hashline.SnapshotStore` (interfaz completa) y `MemSnapshotStore` con
   `Record` (read-fusion), `Head`, `RecordSeenLines`, `ByHash`, `Invalidate`,
   seguro para uso concurrente.
4. Existe `tool.ReadTool` (implementa `Tool`): lee un archivo, numera con header
   hashline, graba el snapshot del archivo **completo** y marca las lineas vistas.
5. Selector v1: archivo completo, `:N`, `:N-M`; numeros reales; fuera de rango y
   selector invalido dan notice/error accionable, no panico.
6. Truncado por `MaxLines` emite el notice de continuacion in-band y `Seen` cubre
   solo lo emitido.
7. Binario (NUL) -> notice, sin snapshot. Archivo inexistente -> error de tool.
   Ruta fuera de `Root` -> error **sin** leer (0 lecturas en el spy).
8. `read` esta registrado en `app.go` con `"read": true` y aparece en las
   `Definitions` materializadas.
9. `go test ./...` (y `-race` donde aplica) pasa; `go vet ./...` limpio;
   `gofmt -l .` vacio.
10. No se construyo el `edit` ni su motor (parser/apply/patcher/recovery), ni
    folding, ni selectores ricos, ni handlers de imagen/PDF/archivo/URI.

## 8. Comandos

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Dependencia del hash (al GREEN del hash)
go get github.com/pierrec/xxHash/xxHash32   # o equivalente xxHash32 seed 0
go build ./...

# Ciclo (test especifico primero)
go test -run TestComputeFileHash ./internal/tool/hashline
go test -run TestFormatHashline ./internal/tool/hashline
go test -run TestSnapshotStore ./internal/tool/hashline
go test -run TestReadTool ./internal/tool

# Higiene de concurrencia (el store es estado mutable compartido)
go test -race -run TestSnapshotStore ./internal/tool/hashline

# Gates de cierre
gofmt -l .          # vacio
go vet ./...        # limpio
go test ./...       # toda la suite verde
```

## 9. Tabla de evidencia esperada

Al cerrar la fase, la respuesta/PR debe incluir esta tabla con resultados reales:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M1..M10 verde antes de editar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Diseno hashline del read leido | `docs/atenea-read-edit-tools.md`, `internal/tool/{registry,echo}.go` | comportamiento identificado |
| RED | Test de hash y de read escritos primero | `hashline/hash_test.go`, `read_test.go` + `go test -run ...` | fallo esperado (no compila) |
| GREEN | `hash.go` + `format.go` + `snapshot.go` + `read.go` minimos | `internal/tool/hashline/*`, `internal/tool/read.go` | tests especificos pasan |
| TRIANGULATE | Rango, truncado, fuera de rango, binario, ruta fuera de root, read-fusion | `go test -run TestReadTool ./internal/tool`, `go test -race -run TestSnapshotStore ...` | casos pasan, `-race` limpio |
| REFACTOR | Helpers de test, `doc.go` actualizado, wiring en `app.go` | `gofmt -w`, `go vet ./...`, `go test ./...` | suite verde, `read` registrado |

## 10. Riesgos y decisiones

- **El hash solo necesita consistencia interna, no paridad con oh-my-pi.** El
  `read` produce el tag y el `edit` lo verifica con la **misma** funcion Go: no hay
  intercambio con oh-my-pi. Por eso la lib exacta de xxHash32 da igual mientras sea
  determinista; se pinea una y un **test-vector** fija el valor para que un
  refactor no lo mueva. Se mantiene xxHash32-low16-4hex para no inventar un formato
  distinto al del diseno.
- **RE2 no tiene lookahead.** El `/[ \t\r]+(?=\n|$)/g` del original se porta a
  `[ \t\r]+(\n|$)` con replacement `$1` (capturar el separador y re-emitirlo). Es
  el gotcha mas facil de equivocar; tiene su test de CRLF/trailing-ws.
- **El range-read hashea el archivo completo.** Aunque el modelo lea `:50-60`, el
  header lleva el hash de **todo** el archivo y el snapshot guarda el texto
  completo. Asi el ancla por numero de linea del `edit` indexa el archivo real y la
  compuerta de frescura cubre cambios en cualquier parte, no solo en el rango
  leido. Es el comportamiento de oh-my-pi y la razon de re-leer el archivo entero.
- **`Result.Truncated` (OutputStore) vs notice de continuacion (read).** Son dos
  truncados distintos: el `OutputStore` (M4) corta por **bytes** y marca
  `Truncated` para que la UI recupere el completo por `callID`; el `read` corta por
  **lineas** y avisa **in-band** (`[N more lines... Use :offset]`) para que el
  modelo pida el siguiente rango. El `read` **no** setea `Result.Truncated` por su
  limite de lineas; deja ese flag para el `OutputStore`. Se documenta para no
  confundirlos.
- **`SnapshotStore` con interfaz completa pero uso parcial.** Se decide dejar
  `ByHash`/`Invalidate`/historial en la interfaz (aunque el `read` no los ejerza a
  fondo) para que la fase `edit` no tenga que rehacer la firma del store al sumar
  el recovery 3-way-merge. Es la unica concesion a "diseno por adelantado", y es
  barata: el doc ya define ese contrato. El comportamiento activo (recovery) es de
  la fase 2; aqui solo se prueba que `ByHash` encuentra una version grabada.
- **Sandbox por `Root`, fail-closed.** Resolver dentro de `Root` y rechazar `..`
  fuera de el **antes** de leer es la unica defensa de v1 contra leer fuera del
  workspace. Se prueba con un spy que afirma 0 lecturas en el caso de escape (misma
  idea que "rechazo sin efectos" del registry en M4).
- **Binario detectado por NUL, no por extension.** Un byte NUL -> notice y sin
  snapshot (no es editable por hashline; numerarlo produciria mojibake). No se
  intenta adivinar encoding en v1.
- **Context lines diferidas a `edit`.** oh-my-pi agrega 1 linea de contexto antes
  y 3 despues de un rango para reducir fallos de ancla por una linea. Eso solo le
  importa al `edit`; el `read` v1 emite exactamente lo pedido y marca exactamente
  eso como visto. Se suma cuando el `edit` aterrice (con su efecto en `seenLines`).
- **Lectura del archivo entero, sin chunking.** v1 lee todo el archivo y corta en
  memoria: simple y suficiente para un editor de escritorio. El chunking
  (`READ_CHUNK_SIZE`) y los topes de bytes del snapshot se agregan si un archivo
  gigante lo obliga, no por especular.
- **`displayPath` relativo al workspace.** El header usa la ruta relativa (no la
  absoluta) para que sea legible y para que el `edit` la resuelva con la misma
  regla (`Root` + relativa). El snapshot se indexa por la ruta absoluta/canonica.
- **`echo` sigue.** Esta fase no toca `echo`: el agente puede tener `echo` y
  `read` permitidos a la vez. `bash`/`edit`/`write`/`grep`/`glob` llegan despues.

## 11. Recortes seguros para v1 (lazy)

Del propio `docs/atenea-read-edit-tools.md` ("Recortes seguros para v1"), aplicados
al `read`:

- **Se mantiene si o si**: `ComputeFileHash` + normalizacion, header `[path#HASH]`,
  numeracion `NUM:TEXTO`, `SnapshotStore` con `seenLines`. Sin esto no es "estilo
  oh-my-pi" y el `edit` no puede fallar-seguro.
- **Se omite en v1**: folding/summarization (tree-sitter), selectores ricos
  (`:N+K`, open-ended, listas, `:raw`), context lines, imagenes/PDF/archivo/SQLite/
  URIs/URLs, chunking, recovery activo. Son optimizaciones o superficie extra, no
  el mecanismo de seguridad.

## 12. Fuentes

- Diseno read/edit: `docs/atenea-read-edit-tools.md` (idea hashline, el hash, Tool
  `read`, `SnapshotStore`, casos borde, recortes v1, orden de implementacion).
- Codigo de oh-my-pi (verificado 2026-06-21): `packages/hashline/src/format.ts`
  (`computeFileHash`, normalizacion `/[ \t\r]+(?=\n|$)/g`, `HL_FILE_HASH_LENGTH=4`,
  header `[path#HASH]`, separador de linea `:`), `packages/coding-agent/src/tools/
  read.ts` (selector `:<sel>`, `RANGE_LEADING_CONTEXT_LINES=1`/
  `RANGE_TRAILING_CONTEXT_LINES=3`, notice de continuacion, beyond-EOF, binario por
  NUL, snapshot full-file + `recordSeenLines`).
- Registry de tools: `internal/tool/registry.go`, `output.go`, `echo.go` (contrato
  `Tool`, `Result`, `Settle`, `OutputStore`).
- Spec previa del registry: `docs/specs/atenea-m4-tool-registry-spec.md`.
- Manera de trabajo: `AGENTS.md`. Roadmap del loop: `docs/atenea-agent-loop-roadmap.md`.
