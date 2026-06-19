# Tools `read` y `edit` estilo oh-my-pi (diseno para Go)

Investigado el 2026-06-19 sobre `can1357/oh-my-pi` (paquetes `coding-agent` y
`hashline`). Documenta como funcionan de verdad las tools `read` y `edit` de
oh-my-pi y como replicarlas en Go para Atenea.

Es la pieza mas dificil del agente, por eso queda escrita con detalle: la
correctitud del `edit` decide si el agente corrompe archivos o no.

## La idea central: hashline

oh-my-pi no edita por "buscar texto viejo y reemplazar por nuevo" (fragil: el
modelo reescribe bloques enteros o falla por una linea de contexto). Tampoco edita
por numero de linea a secas (fragil: el archivo cambia y el numero apunta a otra
cosa).

Usa un esquema hibrido llamado **hashline**:

- El `read` numera cada linea y antepone un **header con un hash del archivo
  completo**: `[ruta#HASH]`.
- El `edit` direcciona por **numero de linea**, pero solo es valido si el archivo
  **sigue hasheando al mismo `HASH`**.
- Si el archivo cambio, el `HASH` diverge y el edit **falla seguro** (o se recupera
  por 3-way-merge contra el snapshot que el hash nombra). Nunca aplica un diff
  stale a ciegas.

Es decir: el **ancla es el numero de linea**, y el **hash del contenido completo es
la compuerta de frescura**. Esa es toda la magia.

### El hash (lo mas importante de copiar bien)

De `packages/hashline/src/format.ts`:

```ts
// Normalizar antes de hashear: quitar [ \t\r] al final de cada linea y del final.
function normalizeFileHashText(text) {
  return text.replace(/[ \t\r]+(?=\n|$)/g, "");
}
// Tag = 4 hex chars, uppercase, de los 16 bits bajos de xxHash32 sobre el texto normalizado.
function computeFileHash(text) {
  const normalized = normalizeFileHashText(text);
  const low16 = Bun.hash.xxHash32(normalized, 0) & 0xffff;
  return low16.toString(16).padStart(4, "0").toUpperCase();
}
```

Detalles que importan:

- Son **4 hex** (`[0-9A-F]{4}`), 16 bits. Colision posible pero rara; el recovery
  cubre el resto.
- La **normalizacion** (trim de trailing whitespace y CR) hace que CRLF vs LF y
  espacios al final no invaliden el tag.
- Cualquier `read` de bytes identicos produce el **mismo** tag (read fusion).

Port a Go:

```go
// internal/tool/hashline/hash.go
var trailingWS = regexp.MustCompile(`[ \t\r]+(\n|$)`)

func normalizeForHash(text string) string {
    return trailingWS.ReplaceAllString(text, "$1")
}

// xxHash32 con seed 0; usar p.ej. github.com/pierrec/xxHash/xxHash32.
func ComputeFileHash(text string) string {
    sum := xxHash32.Checksum([]byte(normalizeForHash(text)), 0)
    low16 := sum & 0xFFFF
    return strings.ToUpper(fmt.Sprintf("%04x", low16))
}
```

## Tool `read`

### API (lo que ve el modelo)

oh-my-pi expone un solo parametro:

```ts
const readSchema = type({
  path: "string", // p.ej. "src/foo.ts", o con selector "src/foo.ts:50-100"
});
```

El **rango de lineas va embebido en el path** con `:<sel>` (`50-100`), no como
parametros aparte. Tambien soporta modo raw y URIs internas (`omp://`, etc.).

### Output (formato hashline)

Para un archivo de texto, el `read` emite:

```text
[src/foo.ts#1A2B]
1:package main
2:
3:func main() {
4:    fmt.Println("hi")
5:}
```

- Primera linea: header `[ruta#HASH]`.
- Cada linea de contenido: `NUM:TEXTO` (numero, dos puntos, contenido).
- En lecturas por rango, los numeros reflejan la posicion real en el archivo.

### Comportamiento clave

- **Limites por bytes y por lineas**: corta lecturas grandes; reporta truncacion.
- **Summarization/folding**: para archivos grandes puede colapsar cuerpos y mostrar
  solo el esqueleto, marcando spans elididos (esto usa tree-sitter; en Atenea es
  opcional, ver "Recortes para v1").
- **Context lines**: alrededor de un rango explicito agrega 1 linea de contexto
  delante para reducir fallos de ancla "por una linea".
- **Registro de snapshot**: al leer, graba el texto completo normalizado en el
  `SnapshotStore` y marca que lineas mostro (`seenLines`). Esto habilita dos cosas
  del `edit`: la recuperacion por drift y el chequeo "no edites lineas que no
  leiste".

## Tool `edit` (via `write` con contenido hashline)

En oh-my-pi el `write` tiene `{ path, content }`. Si el `content` empieza con un
header `[ruta#HASH]` seguido de operaciones hashline, se trata como **patch**; si
no, es un **write completo** del archivo. Para Atenea conviene separarlo en una
tool `edit` explicita, pero el motor es el mismo.

### Operaciones (de `format.ts`)

El patch es texto. Headers de hunk:

| Operacion | Sintaxis | Efecto |
| --- | --- | --- |
| Reemplazo de rango | `SWAP start.=end:` + lineas `+...` | reemplaza `[start,end]` por el payload |
| Borrado | `DEL n` o `DEL start.=end` | borra esa(s) linea(s) |
| Insertar antes | `INS.PRE n:` + `+...` | inserta payload antes de la linea `n` |
| Insertar despues | `INS.POST n:` + `+...` | inserta payload despues de la linea `n` |
| Insertar al inicio | `INS.HEAD:` + `+...` | inserta al principio del archivo |
| Insertar al final | `INS.TAIL:` + `+...` | inserta al final del archivo |
| Bloque (tree-sitter) | `SWAP.BLK n:`, `DEL.BLK n`, `INS.BLK.POST n:` | opera sobre el bloque sintactico que empieza en `n` |

- Las lineas de payload se prefijan con `+`.
- `.=` separa rangos (`5.=10`).
- Multiples archivos: varias secciones, cada una con su header `[ruta#HASH]`.

### Como se aplica y verifica (de `patcher.ts`)

El `Patcher` es **all-or-nothing**: primero `prepare` (preflight) de todas las
secciones en memoria, y solo si todas pasan, hace `commit` (escribe a disco). Por
seccion:

1. Parsear las operaciones; exigir que la seccion traiga `HASH` (sin tag -> error).
2. Leer el archivo, quitar BOM, normalizar a LF.
3. Calcular `live = ComputeFileHash(contenidoActual)`.
4. Comparar con el `HASH` esperado de la seccion:
   - **`live == esperado` (no drift)**: las lineas indexan el contenido exacto.
     - Chequear `seenLines`: rechazar anclas a lineas que el read nunca mostro.
     - Aplicar las operaciones (`applyEdits`).
   - **Solo inserts HEAD/TAIL** y tag stale: posicion estable, no es fatal. Aplica
     sobre el contenido vivo y avisa con warning.
   - **Drift con anclas concretas**: intentar `recovery.tryRecover()` (3-way-merge
     del edit contra el snapshot que el tag nombra, sobre el contenido vivo). Si
     funciona, aplica el merge; si no, **`MismatchError`** (re-leer).
5. `commit`: restaurar line endings + BOM, escribir, y **grabar snapshot nuevo**;
   devolver el header `[ruta#nuevoHASH]` para encadenar edits sin re-leer.

### Mensajes de error (de `mismatch.ts`)

El rechazo es accionable y distingue dos casos:

- **Hash no reconocido** (no es de esta sesion): "hash #XXXX is not from this
  session... re-read the file... never invent the tag".
- **Hash reconocido pero el archivo cambio**: "file changed between read and edit...
  copy the `[path#newhash]` header from the prior edit's response, or re-read".

Ademas adjunta contexto: las lineas ancladas con un par de lineas alrededor.

## El `SnapshotStore` (clave para el recovery)

De `snapshots.ts`. Es lo que permite recuperarse de un drift en lugar de solo
fallar. Es un store por-path con historial corto de versiones de archivo completo:

```go
// internal/tool/hashline/snapshot.go
type Snapshot struct {
    Path      string
    Text      string          // texto completo normalizado (LF, sin BOM)
    Hash      string          // ComputeFileHash(Text)
    SeenLines map[int]struct{} // lineas 1-indexed que un read/search mostro
}

type SnapshotStore interface {
    Head(path string) *Snapshot               // version mas reciente
    ByHash(path, hash string) *Snapshot       // version cuyo tag == hash
    Record(path, fullText string, seen []int) string // graba y devuelve el tag
    RecordSeenLines(path, hash string, lines []int)
    Invalidate(path string)
}
```

- Implementacion default: LRU acotado (oh-my-pi: 30 paths, 4 versiones/path, 64 MiB).
- `Record` de contenido identico **reusa el tag** y refresca recency (read fusion).
- El recovery usa `ByHash` para encontrar el texto exacto que el tag stale nombra
  y hacer el 3-way-merge.

Para Atenea, una version **en memoria por sesion** alcanza para v1; el contrato es
el mismo que el `Store` durable del loop (`docs/atenea-agent-loop.md`).

## Diseno en Go para Atenea

Paquete sugerido: `internal/tool/hashline` (motor puro, sin FS ni agente) + las
tools `read`/`edit` en `internal/tool` que lo usan.

```text
internal/tool/hashline/
  hash.go        // ComputeFileHash, normalizeForHash
  format.go      // formato [path#HASH], "NUM:TEXTO", headers de hunk
  types.go       // Anchor, Cursor, Edit, ApplyResult
  parser.go      // texto de patch -> []Edit  (SWAP/DEL/INS...)
  apply.go       // applyEdits(text, edits) -> ApplyResult
  patcher.go     // prepare/commit, verificacion de hash, recovery
  snapshot.go    // SnapshotStore + impl en memoria
  recovery.go    // 3-way-merge contra el snapshot del tag
internal/tool/
  read.go        // ReadTool: lee, numera, formatea, graba snapshot
  edit.go        // EditTool: arma Patch, llama Patcher
```

Tipos nucleo (espejo de `hashline/types.ts`):

```go
type Anchor struct{ Line int } // 1-indexed

type Edit struct {
    Kind   EditKind // Insert | Delete | Replace | Block
    Cursor Cursor   // para Insert: BOF/EOF/BeforeAnchor/AfterAnchor
    Anchor Anchor   // para Delete/Block
    Range  *Range   // para Replace (start.=end)
    Text   string   // payload (lineas +)
}

type ApplyResult struct {
    Text             string
    FirstChangedLine int
    Warnings         []string
}
```

Contrato del `Patcher` (espejo de `patcher.ts`):

```go
type Patcher struct {
    FS        Filesystem
    Snapshots SnapshotStore
    // BlockResolver opcional (tree-sitter); nil en v1.
}

// All-or-nothing: prepara todo en memoria, luego commitea.
func (p *Patcher) Apply(patch Patch) (PatchResult, error)
```

## Casos borde que NO se pueden saltar

Estos son los que hacen que valga la pena copiar el diseno, no inventarlo:

- **Tag ausente** en el edit -> error claro ("falta el header `[path#HASH]`").
- **Tag inventado / de otra sesion** -> rechazo con mensaje distinto a "cambio el
  archivo".
- **Drift recuperable** (un edit previo de la misma sesion cambio el archivo) ->
  3-way-merge contra el snapshot del tag previo, no re-leer.
- **Drift no recuperable** -> `MismatchError` con contexto de lineas.
- **Edit a lineas no leidas** (`seenLines`) -> rechazo: editar de memoria mangla
  archivos.
- **HEAD/TAIL con tag stale** -> warning, no fatal (posicion estable).
- **Normalizacion** CRLF/BOM/trailing-ws -> mantener al hashear y restaurar al
  escribir.
- **Multi-archivo** -> preflight de todas las secciones antes de tocar disco;
  reportar cuales se escribieron si una falla a mitad.
- **No-op** (el edit no cambia nada) -> error explicito, no escribir.

## Recortes seguros para v1 (lazy)

Para no morir en el intento, v1 puede dejar fuera lo dependiente de tree-sitter y
mantener el corazon intacto:

- **Omitir** operaciones de bloque (`SWAP.BLK`, `DEL.BLK`, `INS.BLK.POST`) y el
  folding/summarization del `read`. Son optimizaciones; no son el mecanismo de
  seguridad.
- **Mantener** si o si: `ComputeFileHash` + normalizacion, header `[path#HASH]`,
  numeracion `NUM:TEXTO`, ops `SWAP/DEL/INS.*`, verificacion de hash, `SnapshotStore`,
  chequeo `seenLines`, y mensajes de mismatch. Sin esto, no es "estilo oh-my-pi".
- El **recovery 3-way-merge** se puede agregar en una segunda pasada: arrancar
  fallando con `MismatchError` (re-leer) y sumar el merge despues.

## Orden de implementacion (TDD, ver AGENTS.md)

1. `ComputeFileHash` + normalizacion. RED: mismo texto (CRLF/LF, trailing ws) ->
   mismo tag; cambio real -> tag distinto.
2. `format` + numeracion del `read`. RED: un archivo produce header + `NUM:TEXTO`.
3. `parser`: texto de patch -> `[]Edit` para `SWAP/DEL/INS.*`. RED por operacion.
4. `apply`: aplicar edits a texto. RED: replace/delete/insert y combinaciones; los
   numeros de linea se respetan al hacer varios splices.
5. `SnapshotStore` en memoria. RED: record fusiona contenido identico; `ByHash`.
6. `patcher`: verificacion de hash + `seenLines` + all-or-nothing. RED: no-drift
   aplica; tag stale con ancla -> `MismatchError`; HEAD/TAIL stale -> warning.
7. (2da pasada) recovery 3-way-merge; (3ra) ops de bloque con tree-sitter.

Cada hito cierra con su tabla `TDD Cycle Evidence`.

## Fuentes

- Repo: https://github.com/can1357/oh-my-pi
- Tools: `packages/coding-agent/src/tools/{read,write,conflict-detect}.ts`
- Motor hashline: `packages/hashline/src/{format,types,parser,apply,patcher,mismatch,snapshots}.ts`
- README (vision de tool design): https://github.com/can1357/oh-my-pi/blob/main/README.md
- Loop del agente (donde se montan las tools): `docs/atenea-agent-loop.md`
- Manera de trabajo: `AGENTS.md`
