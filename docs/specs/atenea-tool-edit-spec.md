# Spec — Tool `edit` (hashline, fase 2 del track read/edit)

Spec ejecutable de la **tool `edit`** estilo `can1357/oh-my-pi`. Es la **fase 2**
del track read/edit (`docs/atenea-read-edit-tools.md`); la fase 1 (`read`) ya dejo
el motor `internal/tool/hashline` con `ComputeFileHash`, `FormatHeader`,
`SplitLines`, `NumberLines`, y el `SnapshotStore` (`Record`/`Head`/`ByHash`/
`RecordSeenLines`/`Invalidate`, con `Seen` por snapshot). El `edit` **consume** ese
snapshot: direcciona por numero de linea y solo aplica si el archivo sigue
hasheando al `HASH` que el `read` mostro.

Es la pieza mas dificil del agente: su correctitud decide si corrompe archivos o
no. Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos.

## 1. Contexto

`docs/atenea-read-edit-tools.md` documenta el mecanismo **hashline**: el `read`
numera cada linea y antepone `[path#HASH]` (hash del archivo completo); el `edit`
direcciona por **numero de linea** pero solo es valido si el archivo **sigue
hasheando al mismo HASH**. El ancla es el numero de linea; el hash es la
**compuerta de frescura**. Si el archivo cambio, el HASH diverge y el edit **falla
seguro** (en v1: `MismatchError`; el recovery 3-way-merge llega en una pasada
posterior). Nunca aplica un diff stale a ciegas.

La fase 1 (`read`, ver `docs/specs/atenea-tool-read-spec.md`) ya implemento los
pasos 1, 2 y 5 del orden del doc (hash + normalizacion; formato + numeracion;
`SnapshotStore` con `Seen`). Esta fase implementa los pasos **3, 4 y 6**: parser
de patch (texto -> `[]Edit`), apply (`applyEdits`), y patcher (verificacion de
hash + chequeo de `seenLines` + all-or-nothing + commit). El recovery (paso 7) y
las ops de bloque (tree-sitter) quedan **fuera de v1**.

## 2. Objetivo

Dejar la tool `edit` que aplica un patch hashline a un archivo, verde contra un FS
fake, y registrada en el agente.

En `internal/tool/hashline` (motor puro, sin FS ni agente):

- `types.go`: `EditKind`, `Range`, `Edit`, `ApplyResult`, `Patch`/`Section`,
  `MismatchError`, `MissingTagError`.
- `parser.go`: `ParsePatch(text) (Patch, error)` — texto de patch hashline ->
  secciones con header `[path#HASH]` y `[]Edit` (ops `SWAP/DEL/INS.*`).
- `apply.go`: `ApplyEdits(lines []string, edits []Edit) (ApplyResult, error)` —
  aplica los edits a las lineas del archivo respetando la numeracion 1-indexed
  original aunque varios edits cambien el conteo.
- `patcher.go`: `Patcher.Apply(patch) (PatchResult, error)` — preflight de la(s)
  seccion(es), verificacion de hash contra el contenido vivo, chequeo de
  `seenLines`, all-or-nothing, commit (escribe, regraba snapshot, devuelve el
  nuevo header `[path#HASH]`).

En `internal/tool`:

- `edit.go`: `EditTool` (implementa la interface `Tool` de M4). Parsea el patch,
  resuelve la ruta dentro de `Root`, corre el `Patcher`, devuelve el nuevo header.
- tests de comportamiento.

En `app.go`:

- construir el `EditTool` con la **misma** raiz y el **mismo** `SnapshotStore` que
  el `read` (para que el snapshot que graba el `read` lo lea el `edit`), registrarlo
  y permitir `"edit": true`.

Esta fase **no** construye el recovery 3-way-merge, ni las ops de bloque
(tree-sitter), ni multi-archivo en un solo patch (v1: una seccion), ni la
restauracion de CRLF/BOM al escribir (v1 escribe LF; ver Recortes).

## 3. Alcance

### Dentro

- `internal/tool/hashline/types.go`: `EditKind` (Insert/Delete/Replace),
  `Cursor` (BOF/EOF/BeforeAnchor/AfterAnchor), `Range`, `Edit`, `ApplyResult`,
  `Section`, `Patch`, `MismatchError`, `MissingTagError`.
- `internal/tool/hashline/parser.go`: `ParsePatch`.
- `internal/tool/hashline/apply.go`: `ApplyEdits`.
- `internal/tool/hashline/patcher.go`: `Patcher`, `NewPatcher`, `Apply`,
  `PatchResult`, la interface `Filesystem` (read+write) que el patcher usa.
- `internal/tool/edit.go`: `EditTool`, `NewEditTool`.
- Tests: `internal/tool/hashline/{parser,apply,patcher}_test.go`,
  `internal/tool/edit_test.go`.
- `app.go`: construir y registrar `edit` con el `SnapshotStore`/`Root` compartidos
  con `read`.
- Actualizar `internal/tool/hashline/doc.go` y `internal/tool/doc.go`.

### Fuera (no hacer en v1)

- **Recovery 3-way-merge** (`recovery.go`): ante drift con anclas concretas, v1
  devuelve `MismatchError` (re-leer). El merge contra el snapshot del tag stale es
  una pasada posterior. Es la razon de tener `SnapshotStore.ByHash` ya listo.
- **Ops de bloque** (`SWAP.BLK`, `DEL.BLK`, `INS.BLK.POST`, tree-sitter): fuera.
- **Multi-archivo en un patch** (varias secciones `[path#HASH]`): v1 acepta **una**
  seccion. El preflight all-or-nothing igual se respeta (una seccion es atomica).
- **Restauracion de line endings / BOM**: v1 normaliza a LF (como el `read`) y
  escribe LF. Restaurar CRLF/BOM original es un refinamiento posterior (la mayoria
  del codigo es LF; se documenta el limite).
- **Permisos por patron de ruta** para `edit`: sigue el set de nombres de M4
  (`"edit": true`). El modelo rico (ask/allow por ruta) llega cuando se necesite.

## 4. Tipos y contrato

### 4.1 Formato del patch (lo que ve el modelo)

Un solo parametro `patch`: un texto hashline con un header de seccion y una
secuencia de hunks. Ejemplo:

```text
[internal/foo.go#1A2B]
SWAP 3.=5:
+func main() {
+	fmt.Println("hola")
+}
DEL 8
INS.POST 10:
+// nota
```

- Header de seccion: `[ruta#HASH]` (lo parsea `FormatHeader` a la inversa). El
  `HASH` es el que el `read` mostro; sin header -> `MissingTagError`.
- Hunks (ops de v1):

| Operacion | Sintaxis | Efecto |
| --- | --- | --- |
| Reemplazo de rango | `SWAP start.=end:` + lineas `+...` | reemplaza `[start,end]` por el payload |
| Borrado | `DEL n` o `DEL start.=end` | borra esa(s) linea(s) |
| Insertar antes | `INS.PRE n:` + `+...` | inserta payload antes de la linea `n` |
| Insertar despues | `INS.POST n:` + `+...` | inserta payload despues de la linea `n` |
| Insertar al inicio | `INS.HEAD:` + `+...` | inserta al principio del archivo |
| Insertar al final | `INS.TAIL:` + `+...` | inserta al final del archivo |

- Las lineas de payload se prefijan con `+`. `.=` separa el rango (`5.=10`).
- Los numeros son 1-indexed sobre el archivo que hashea al `HASH` del header.

### 4.2 `internal/tool/hashline/types.go`

```go
type EditKind int
const (
	Replace EditKind = iota // SWAP: reemplaza [Range.Start, Range.End]
	Delete                  // DEL: borra n o [start,end]
	Insert                  // INS.*: inserta Text en Cursor
)

type Cursor int
const (
	BeforeAnchor Cursor = iota // INS.PRE n
	AfterAnchor                // INS.POST n
	BOF                        // INS.HEAD
	EOF                        // INS.TAIL
)

type Range struct{ Start, End int } // 1-indexed inclusive

// Edit es una operacion ya parseada. Replace/Delete usan Range; Insert usa Cursor
// (+ Anchor para PRE/POST). Text son las lineas de payload (sin el prefijo '+'),
// unidas por '\n'.
type Edit struct {
	Kind   EditKind
	Range  Range  // Replace/Delete
	Cursor Cursor // Insert
	Anchor int    // INS.PRE/POST: la linea n
	Text   string
}

// ApplyResult es el resultado de aplicar los edits a un texto.
type ApplyResult struct {
	Text             string
	FirstChangedLine int
	Warnings         []string
}

// Section es una seccion del patch: el archivo (path + hash esperado) y sus edits.
type Section struct {
	Path string
	Hash string
	Edits []Edit
}

type Patch struct{ Sections []Section }

// MissingTagError: el patch (o una seccion) no trae el header [path#HASH].
type MissingTagError struct{ Detail string }

// MismatchError: el archivo cambio entre el read y el edit (live != esperado) y v1
// no recupera. Lleva contexto accionable (lineas ancladas) para re-leer.
type MismatchError struct {
	Path     string
	Expected string // hash del header
	Live     string // hash del contenido actual
	Recognized bool // true si el hash era de esta sesion (ByHash != nil): "el archivo cambio"; false: "hash desconocido, re-lee"
	Context  string // lineas alrededor de las anclas
}
```

### 4.3 `internal/tool/hashline/parser.go`

```go
// ParsePatch convierte el texto del patch en un Patch. Exige el header
// [path#HASH] (sin el -> MissingTagError). Parsea los hunks SWAP/DEL/INS.* y sus
// payloads (+...). Una op malformada es un error de parseo accionable. v1: una
// sola seccion (header al inicio); varias secciones es error "multi-archivo no
// soportado en v1".
func ParsePatch(text string) (Patch, error)
```

### 4.4 `internal/tool/hashline/apply.go`

```go
// ApplyEdits aplica los edits a las lineas (1-indexed sobre el archivo original).
// Los numeros de todos los edits refieren al MISMO archivo original; se aplican de
// forma que un splice no corra los indices de otro (procesar de mayor a menor
// posicion, o construir el resultado en una pasada). No-op (ningun cambio) ->
// error explicito (el patcher no escribe). Devuelve el texto nuevo, la primera
// linea cambiada y warnings.
func ApplyEdits(lines []string, edits []Edit) (ApplyResult, error)
```

### 4.5 `internal/tool/hashline/patcher.go`

```go
// Filesystem es lo que el patcher necesita del FS: leer y escribir un archivo por
// ruta absoluta. El default envuelve os.ReadFile/os.WriteFile; los tests inyectan
// un fake en memoria.
type Filesystem interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
}

// Patcher aplica un Patch all-or-nothing: prepara (preflight) la seccion en
// memoria y, solo si pasa, commitea (escribe). Comparte el SnapshotStore con el
// read (para Seen y, en el futuro, ByHash/recovery).
type Patcher struct {
	FS        Filesystem
	Snapshots SnapshotStore
}

func NewPatcher(fs Filesystem, snaps SnapshotStore) *Patcher

// PatchResult es lo que devuelve un Apply exitoso: el nuevo header [path#newHASH]
// (para encadenar edits sin re-leer) y la primera linea cambiada / warnings.
type PatchResult struct {
	Header           string
	FirstChangedLine int
	Warnings         []string
}

// Apply preflighta y commitea la seccion:
//  1. exigir el header (path + hash esperado).
//  2. leer el archivo (abs), quitar BOM, normalizar a LF -> live text.
//  3. liveHash = ComputeFileHash(live).
//  4. si liveHash == esperado (no drift): chequear seenLines (rechazar anclas a
//     lineas que el read no mostro); aplicar edits.
//     si liveHash != esperado: si TODOS los edits son INS.HEAD/INS.TAIL (posicion
//     estable) -> aplicar con warning; si no -> MismatchError (Recognized segun
//     Snapshots.ByHash(path, esperado) != nil).
//  5. commit: escribir el texto nuevo (LF), Snapshots.Record(abs, nuevo) +
//     RecordSeenLines de las lineas tocadas, devolver [path#newHASH].
func (p *Patcher) Apply(patch Patch) (PatchResult, error)
```

### 4.6 `internal/tool/edit.go`

```go
type EditTool struct {
	Root      string
	Patcher   *hashline.Patcher
}

func NewEditTool(root string, fs hashline.Filesystem, snaps hashline.SnapshotStore) *EditTool

func (*EditTool) Name() string        // "edit"
func (*EditTool) Description() string // explica el formato del patch hashline
func (*EditTool) Schema() json.RawMessage // { patch: string } requerido

// Execute parsea el input { patch }, parsea el patch (ParsePatch), resuelve la
// ruta de la seccion dentro de Root (sandbox fail-closed como el read), corre el
// Patcher y devuelve el nuevo header [path#HASH]. Un MissingTagError/MismatchError
// se devuelve como error de tool accionable (Settle -> Tool.Failed).
func (rt *EditTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error)
```

Schema (lo que ve el modelo):

```json
{
  "type": "object",
  "properties": {
    "patch": {
      "type": "string",
      "description": "Patch hashline: una linea de header [ruta#HASH] (el HASH viene del read) seguida de hunks SWAP/DEL/INS.PRE/INS.POST/INS.HEAD/INS.TAIL con lineas de payload prefijadas por '+'. Edita solo lineas que leiste con read."
    }
  },
  "required": ["patch"]
}
```

## 5. Semantica del `edit`

`Execute` hace, en orden:

1. **Parse del input.** `json.Unmarshal` del `patch` (string). Input invalido ->
   error de tool.
2. **Parse del patch.** `ParsePatch(patch)`. Sin header -> `MissingTagError`
   ("falta el header [ruta#HASH]"). Varias secciones -> error "multi-archivo no
   soportado en v1". Op malformada -> error de parseo.
3. **Resolver la ruta.** La ruta del header, dentro de `Root` (sandbox: rechazar
   `..` fuera de `Root` **sin** tocar el FS), igual que el `read`.
4. **Patcher.Apply** (all-or-nothing):
   - leer + normalizar (BOM/LF), `liveHash`.
   - **no drift** (`liveHash == esperado`): chequear `seenLines` contra el snapshot
     (`Snapshots.ByHash(abs, esperado)` o `Head`); una ancla a una linea fuera de
     `Seen` -> error "no edites lineas que no leiste". Aplicar `ApplyEdits`.
   - **drift**, solo `INS.HEAD/TAIL`: posicion estable -> aplicar con warning.
   - **drift** con anclas concretas: `MismatchError` (v1, sin recovery). El mensaje
     distingue "hash no reconocido (re-lee, nunca inventes el tag)" de "el archivo
     cambio entre read y edit (copia el [path#newhash] del edit previo o re-lee)".
   - **no-op** (los edits no cambian nada) -> error explicito, no escribir.
5. **Commit.** Escribir el texto nuevo (LF). `Snapshots.Record(abs, nuevo)` +
   `RecordSeenLines` de las lineas tocadas (para encadenar edits). Devolver
   `Result{Output: "[ruta#nuevoHASH]"}` (+ resumen/warnings) para que el modelo
   encadene sin re-leer.

## 6. Plan TDD

Sub-ciclos de adentro hacia afuera: parser -> apply -> patcher -> tool. Cada uno
RED/GREEN; al final TRIANGULATE de los casos borde y REFACTOR + wiring.

### Safety net

- Suite verde antes de tocar (el motor `hashline` y el `read` ya existen). La fase
  agrega archivos nuevos en `hashline` y `edit.go`, y toca `app.go` para registrar.
- `go test ./...`, `go vet ./...`, `gofmt -l .`.

### Understand

- Leer `docs/atenea-read-edit-tools.md` (Tool `edit`, operaciones, patcher,
  mensajes de mismatch, casos borde, recortes v1) y este spec.
- Leer `internal/tool/hashline/{hash,format,snapshot}.go` (lo que el `read` dejo) y
  `internal/tool/read.go` (patron de tool, sandbox, FileReader).

### RED (sub-ciclos, cada uno falla primero)

1. `TestParsePatch_HeaderAndSwap`: un patch con `[p#1A2B]` + `SWAP 3.=5:` + payload
   parsea a una `Section{Path:"p", Hash:"1A2B", Edits:[{Replace, Range{3,5}, ...}]}`.
2. `TestParsePatch_MissingHeaderErrors`: sin `[p#HASH]` -> `MissingTagError`.
3. `TestApplyEdits_ReplaceRange`: `ApplyEdits(["a","b","c","d"], [SWAP 2.=3 -> "X"])`
   -> `["a","X","d"]`.
4. `TestPatcher_NoDriftAppliesAndRecordsNewHash`: FS fake con un archivo cuyo hash
   == el del header y un snapshot con `Seen` cubriendo las lineas; `Apply` escribe
   el texto nuevo y devuelve `[path#nuevoHASH]` con `nuevoHASH ==
   ComputeFileHash(nuevo)`.
5. `TestEditTool_AppliesPatchReturnsNewHeader`: `Execute({patch})` happy path ->
   `Result.Output` empieza con `[path#`.

### GREEN

- Minimo por sub-ciclo: `types.go` + `parser.go`; luego `apply.go`; luego
  `patcher.go`; luego `edit.go`. Correr solo el test rojo de cada uno.

### TRIANGULATE (casos que NO se pueden saltar, del doc)

- `TestApplyEdits_DeleteAndInsertKeepLineNumbers`: combinar `DEL` + `INS.POST` con
  numeros del archivo original; los splices no corren los indices entre si.
- `TestApplyEdits_NoOpErrors`: edits que no cambian nada -> error (no escribir).
- `TestParsePatch_AllInsertVariants`: `INS.PRE/POST/HEAD/TAIL` parsean a los
  `Cursor` correctos.
- `TestPatcher_DriftWithAnchorReturnsMismatch`: `liveHash != esperado` con un
  `SWAP` -> `MismatchError` (sin escribir). `Recognized` segun `ByHash`.
- `TestPatcher_HeadTailOnStaleTagAppliesWithWarning`: `liveHash != esperado` pero
  solo `INS.TAIL` -> aplica + warning (no fatal).
- `TestPatcher_EditUnseenLineRejected`: ancla a una linea fuera de `Seen` del
  snapshot -> error "no edites lineas que no leiste", sin escribir.
- `TestPatcher_AllOrNothingDoesNotWriteOnPreflightError`: si el preflight falla, el
  FS fake **no** registra ninguna escritura.
- `TestEditTool_MissingTagErrors`, `TestEditTool_RejectsPathOutsideRoot`,
  `TestEditTool_InvalidInputErrors`.
- `-race` donde aplique (el `SnapshotStore` es compartido y concurrente).

### REFACTOR + wiring

- Limpieza (helpers de test, `doc.go`). Wiring en `app.go`: `EditTool` con el
  **mismo** `Root` y `SnapshotStore` que el `read`, permiso `"edit": true`.
- Gates: `gofmt -l .`, `go vet ./...`, `go test ./...`, `-race`.

## 7. Criterios de aceptacion (Done when)

1. `ParsePatch` parsea header `[path#HASH]` + ops `SWAP/DEL/INS.PRE/POST/HEAD/TAIL`
   con payloads `+...`; sin header -> `MissingTagError`; multi-seccion -> error v1.
2. `ApplyEdits` aplica replace/delete/insert respetando la numeracion original con
   varios splices; no-op -> error.
3. `Patcher.Apply` verifica `liveHash` contra el esperado: no-drift aplica (tras
   chequear `seenLines`); drift con ancla -> `MismatchError`; HEAD/TAIL stale ->
   warning; ancla a linea no vista -> rechazo; **all-or-nothing** (preflight falla
   -> no escribe). Commit regraba snapshot y devuelve `[path#nuevoHASH]`.
4. `EditTool` (implementa `Tool`): aplica un patch, sandbox fail-closed, devuelve el
   nuevo header; errores accionables (`MissingTagError`/`MismatchError`/ruta fuera
   de root/input invalido).
5. `edit` registrado en `app.go` con el `SnapshotStore`/`Root` compartidos con
   `read` y `"edit": true`; aparece en las `Definitions`.
6. `go test ./...` (y `-race`) verde; `go vet ./...` limpio; `gofmt -l .` vacio.
7. No se construyo recovery 3-way-merge, ops de bloque, multi-archivo, ni
   restauracion CRLF/BOM.

## 8. Comandos

```bash
go test ./...                 # safety net / cierre
go vet ./...
gofmt -l .

go test -run TestParsePatch ./internal/tool/hashline
go test -run TestApplyEdits ./internal/tool/hashline
go test -run TestPatcher ./internal/tool/hashline
go test -run TestEditTool ./internal/tool
go test -race ./internal/tool ./internal/tool/hashline
```

## 9. Tabla de evidencia esperada

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite (read + motor) verde antes de editar | `go test ./...`, `go vet`, `gofmt -l .` | pass |
| Understand | Diseno del edit + motor del read leidos | `docs/atenea-read-edit-tools.md`, `internal/tool/hashline/*`, `read.go` | comportamiento identificado |
| RED | Tests de parser/apply/patcher/tool escritos primero | `hashline/{parser,apply,patcher}_test.go`, `edit_test.go` | fallo esperado |
| GREEN | `types/parser/apply/patcher` + `edit.go` minimos | esos archivos | tests especificos pasan |
| TRIANGULATE | Drift/mismatch, seenLines, HEAD-TAIL stale, all-or-nothing, no-op, sandbox | `go test -run 'TestPatcher\|TestApplyEdits\|TestEditTool' ...`, `-race` | casos pasan |
| REFACTOR | Cleanup + wiring `edit` en `app.go` | `gofmt`, `go vet`, `go test ./...` | suite verde, `edit` registrado |

## 10. Riesgos y decisiones

- **El hash es la compuerta, el numero de linea es el ancla.** El `edit` nunca
  aplica sobre un archivo cuyo `liveHash` no es el esperado, salvo `INS.HEAD/TAIL`
  (posicion estable). Asi un archivo que cambio entre el `read` y el `edit` falla
  seguro en vez de manglar. Es el corazon de "estilo oh-my-pi".
- **Recovery diferido a una pasada posterior.** v1 falla con `MismatchError` ante
  drift con anclas. El 3-way-merge (usando `Snapshots.ByHash(path, esperado)` para
  recuperar el texto que el tag stale nombra) es la pasada siguiente. `ByHash` ya
  esta listo (lo dejo el `read`); se documenta para no rehacer la interfaz.
- **`seenLines` evita editar de memoria.** El `edit` rechaza anclas a lineas que el
  `read` no mostro (`Snapshot.Seen`). Sin esto, el modelo edita lineas que no vio y
  mangla el archivo. Es la segunda red de seguridad despues del hash.
- **All-or-nothing.** El patcher preflighta en memoria y solo escribe si todo pasa.
  El test lo prueba con un FS fake que cuenta escrituras: 0 escrituras si el
  preflight falla.
- **`SnapshotStore`/`Root` compartidos read+edit.** En `app.go` el `read` y el
  `edit` reciben la **misma** instancia de `MemSnapshotStore` y la misma raiz: el
  snapshot que graba el `read` es el que el `edit` lee. Si se construyeran por
  separado, el `edit` nunca veria el `Seen` del `read` y todo seria mismatch.
- **LF en v1, CRLF/BOM diferido.** El `edit` normaliza a LF (como el `read`) y
  escribe LF. Restaurar el line-ending/BOM original es un refinamiento posterior;
  v1 lo documenta como limite (la mayoria del codigo es LF). Riesgo: un archivo
  CRLF se reescribe como LF.
- **Una seccion en v1.** El patcher es all-or-nothing por seccion; v1 acepta un
  patch de un archivo. Multi-archivo (varias secciones con preflight conjunto)
  llega cuando un caso real lo pida.
- **Patch como texto, no JSON estructurado.** Se mantiene el formato hashline de
  oh-my-pi (header + hunks `SWAP/DEL/INS.*`) en vez de un `{path, hash, ops}`
  estructurado, para no inventar un formato distinto al del diseno y reusar
  `FormatHeader`. Decision a confirmar al revisar.
- **Numeracion estable en `ApplyEdits`.** Todos los edits refieren al archivo
  original; se aplican de mayor a menor posicion (o en una pasada construyendo el
  resultado) para que un splice no corra los indices de otro. Es el bug clasico de
  un editor por linea; tiene su test de combinaciones.

## 11. Recortes seguros para v1 (lazy)

Del `docs/atenea-read-edit-tools.md` ("Recortes seguros para v1"):

- **Se mantiene si o si**: ops `SWAP/DEL/INS.*`, verificacion de hash, chequeo de
  `seenLines`, all-or-nothing, mensajes de mismatch accionables. Sin esto no es
  "estilo oh-my-pi" y no falla seguro.
- **Se omite en v1**: recovery 3-way-merge (arrancar con `MismatchError`), ops de
  bloque (tree-sitter), multi-archivo en un patch, restauracion CRLF/BOM. Son
  optimizaciones o superficie extra, no el mecanismo de seguridad.

## 12. Fuentes

- Diseno: `docs/atenea-read-edit-tools.md` (Tool `edit`, operaciones de
  `format.ts`, patcher de `patcher.ts`, mismatch de `mismatch.ts`, snapshots,
  casos borde, recortes v1, orden de implementacion).
- Fase 1: `docs/specs/atenea-tool-read-spec.md` y `internal/tool/hashline/*`
  (`ComputeFileHash`, `FormatHeader`, `SplitLines`, `NumberLines`, `SnapshotStore`
  con `Seen`/`ByHash`).
- Registry de tools: `internal/tool/registry.go` (`Tool`, `Result`).
- Manera de trabajo: `AGENTS.md`. Track: `docs/atenea-read-edit-tools.md`.
