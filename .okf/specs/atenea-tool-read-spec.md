---
updated_at: 2026-07-09
summary: Specification for tool read spec.
---

# Spec — Tool `read` (hashline, phase 1 of track read/edit)

Executable spec of **tool `read`** style `can1357/oh-my-pi`. It is not a numbered
milestone of the loop (`../plans/agent-loop-roadmap.md` closes in M10): it is the
**first phase** of the track read/edit described in `../architecture/read-edit-tools.md`,
which starts now that the loop is already green and wired (M1..M10) and the only real
builtin is `echo`.

Defines the final state, the scope, the TDD plan and the acceptance criteria
 to leave the `read`: reads a file, numbers it and prepends the hashline header
`[path#HASH]`, recording the snapshot that enables the `edit` (phase 2). We work
with the cycle of `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

The tools registry (M4) already materializes `Definitions` and a closed `Settle`
on permissions, limits the large output via `OutputStore`, and the runner
(M5..M8) posts concurrent tools publishing `Tool.Called`/`Tool.Success`/
`Tool.Failed`. M9 wired the loop to Wails and M10 left it on `Store` SQLite and
real provider. The frontend already renders `Tool.*` (`ToolCall.vue`). The only thing that makes Athena a useful code agent is missing: **real tools**. The
first is `read`, because it is the safest (without side effects) and because its
hashline output is the **precondition** of `edit`.

The complete layout of the read/edit pair is in `../architecture/read-edit-tools.md`
 (researched on oh-my-pi, packages `coding-agent` and `hashline`). The
central idea is **hashline**: `read` numbers each line and prepends a header with a
**hash of the complete file** (`[path#HASH]`); `edit` addresses by line number
line but only applies if the file **continues hashing to the same HASH**. The anchor
is the line number; the hash is the **freshness gate**. This spec
builds the `read` half of that mechanism: the hash, the numbered format, and the
`SnapshotStore` that the `read` writes when reading (`../architecture/read-edit-tools.md`,
sections "Tool `read`" and "The `SnapshotStore`").

This phase implements steps **1, 2 and 5** of the doc
 implementation order (hash + normalization; format + numbering of `read`; `SnapshotStore` in
memory). Steps 3, 4, 6, 7 (patch parser, apply, patcher, recovery) are the
`edit` phase.

## 2. Objective

Prepare the minimum hashline engine and the `read` tool, tested in isolation
against a fake FS, and registered in the agent:

In `internal/tool/hashline` (pure engine, without FS or agent):

- `hash.go`: `ComputeFileHash(text) string` with its normalization (trailing
 whitespace + CR), which produces 4 uppercase hex of the 16 low bits of
 `xxHash32` with seed 0. Same text (CRLF/LF, trailing ws) -> same tag.
- `format.go`: `FormatHeader(path, hash) string` -> `[path#HASH]`, and the numbered
 `NUM:TEXTO` of a block of lines with its real number in the file.
- `snapshot.go`: the interface `SnapshotStore` (`Head`/`ByHash`/`Record`/
 `RecordSeenLines`/`Invalidate`) **complete** from the doc and an implementation in
 memory with read-fusion (identical text reuses tag). The `read` only exercises
 `Record`/`Head`/`RecordSeenLines`; `ByHash`/history are ready for
 recovery of `edit` (phase 2) without redoing the interface.

In `internal/tool`:

- `read.go`: `ReadTool` (implements the `Tool` interface of M4). Reads the file,
 detects binary, normalizes to LF (without BOM), computes the hash of the
 **complete** file, records the snapshot, formats the header + the requested lines
 (entire file up to the limit, or the range `:N-M`/`:N`), marks the
 lines seen and returns the `Result`.
- tests behavior in `read_test.go` (and those of the engine in
 `hashline/*_test.go`).

In `app.go`:

- build a `SnapshotStore` per app/session and a `ReadTool` with the root of the
 workspace, register it in the `Registry` and allow `"read": true` (just like there is already
 `echo`).

This phase **does not** build the `edit`, nor the parser/apply/patcher, nor the recovery
3-way-merge, nor folding/tree-sitter, nor the rich selectors (`:N+K`, open-ended,
lists, `:raw`), nor the handling of images/PDFs/files/URIs/URLs.

## 3. Scope

### Inside

- `internal/tool/hashline/hash.go`: `ComputeFileHash`, `normalizeForHash`.
- `internal/tool/hashline/format.go`: `FormatHeader`, numbered `NUM:TEXTO`,
 split to LF lines.
- `internal/tool/hashline/snapshot.go`: `Snapshot`, `SnapshotStore` (complete
 interface), impl in memory `MemSnapshotStore` with read-fusion and lock.
- `internal/tool/read.go`: `ReadTool`, `NewReadTool`, `path` parse +
 selector (`:N`, `:N-M`), `FileReader` abstraction for tests.
- `internal/tool/hashline/*_test.go` and `internal/tool/read_test.go`.
- `app.go`: build `SnapshotStore` + `ReadTool`, register and allow `read`.
- Update `internal/tool/doc.go` (landed the first buildin with FS).
- `go.mod`: add a Go implementation of `xxHash32` (seed 0).

### Out (do not do in this phase)

- The `edit` tool and its entire engine: `parser.go` (patch text -> `[]Edit`),
 `apply.go` (`applyEdits`), `patcher.go` (prepare/commit, hash verification,
 `seenLines` check, all-or-nothing), `recovery.go` (3-way-merge). Phase 2, see
 `../architecture/read-edit-tools.md`. The `read` leaves the `SnapshotStore` populated; v1 supports full file, `:N` and
 `:N-M`. The rest arrives when the agent requests it.
- **Context lines** (oh-my-pi: `RANGE_LEADING_CONTEXT_LINES=1`,
 `RANGE_TRAILING_CONTEXT_LINES=3`): it is an optimization to reduce failures of
 `edit` anchor by one line, not the safety mechanism. It is added when
 `edit` lands (which is the one who suffers the off-by-one). v1 outputs exactly the
 requested lines and marks exactly those as viewed.
- **Folding/summarization** (tree-sitter, `MAX_SUMMARY_*`): optimization for
 huge files; out of v1 (`../architecture/read-edit-tools.md`, "Trips v1").
- **Images, PDF/markit, zip, SQLite, notebooks, conflict://**: dedicated oh-my-pi handlers
; out of v1. v1 read text; binary -> notice.
- **Recovery by drift** and `ByHash`/history as active mechanism: the interface
 declares them (so that `edit` does not redo it), but this phase only tests them
 minimally; 3-way-merge is phase 2.
- **Streaming by chunks** of giant files: v1 reads the entire file and
 cuts by lines in memory (enough for a desktop editor). Chunking
 (`READ_CHUNK_SIZE`) arrives if necessary.
- **Rich permissions model** (ask/per route pattern): still the set of
 M4 names. `read` is read-only; rich permission matters for `edit`/`bash`.

## 4. Types and contract

### 4.1 `internal/tool/hashline/hash.go` — the critical piece

It's the only thing that **can't go wrong**: if the hash is not stable and
deterministic, `edit` corrupts files. RE2 (the `regexp` Go engine) does not support the `(?=\n|$)` lookahead of the original; It behaves by capturing the separator and
re-issuing it with `$1`.

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

**complete** interface from the doc (so that `edit` does not remake it), impl in memory.

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

Schema (what the model sees) — a single `path`, with the selector embedded as in
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

## 5. Semantics of `read`

`Execute` does, in order:

1. **Parse input.** `json.Unmarshal` from `path` (never match string). Invalid Input
 -> tool error (Settle -> `Tool.Failed`).
2. **Parse the selector.** Separates the `:<sel>` suffix from the path. v1 recognizes `:N` and
 `:N-M` (integers, 1-indexed). No valid suffix -> complete file. A suffix
 in the form of a selector but invalid (`:0`, `:5-2`) -> actionable tool error.
3. **Resolve path.** `filepath.Clean(filepath.Join(Root, rel))`; if
 result escapes `Root` (`..`) -> **unread** error (sandbox gate).
4. **Read.** `FS.ReadFile`. Does not exist / no permission -> tool error propagated.
5. **Binary.** If the content has a NUL byte -> return the notice
 `[Cannot read binary file <path>; content contains NUL bytes (binary or UTF-16)]`
 and **do not** record snapshot (it is not editable by hashline).
6. **Normalize.** Remove BOM, CRLF -> LF. This is the text that is saved and numbered. (Restoring line endings/BOM when writing is a `edit` problem.)
7. **Hash of the COMPLETE file.** `tag = Snapshots.Record(absPath, normalized)`.
 Even in a read by range the hash is of the entire file: thus the header
 fingerprints the entire file and any `edit` anchor validates as long as the
 file does not change (oh-my-pi behavior: the range-read re-reads the entire
 file). `Record` reuses the tag if the text was already there (read-fusion).
8. **Choose the window.**
 - Without selector: lines `1..min(total, MaxLines)`. If `total > MaxLines`,
     truncar y anexar el notice de continuacion (paso 10).
- `:N`: the line `N`. `:N-M`: `N..M`. Out of range (`N > total`) -> notice
     `Line N is beyond end of file (<total> lines total).` y `Seen` vacio (el
     snapshot igual se graba: el archivo existe).
9. **Format.** `FormatHeader(displayPath, tag) + "\n" + NumberLines(...)`.
 `displayPath` is the relative path to the workspace (the one that `edit` will resolve to
 again). The numbers are the actual position in the file.
10. **Notice of truncation.** If it was cut by `MaxLines`, append
    `\n\n[<N> more lines in file. Use :<nextOffset> to continue]`. Es in-band: el
    modelo lo lee y pide el siguiente rango. (Esto es distinto de
    `tool.Result.Truncated`, que lo setea el `OutputStore` por bytes; ver Riesgos.)
11. **Mark seen lines.** `Snapshots.RecordSeenLines(absPath, tag, ventana)`
    con exactamente las lineas emitidas (whole-file: `1..fin`; rango: `N..M`
    clamp). El `edit` rechazara anclas a lineas fuera de este set.
12. **Return** `tool.Result{Output: formatted}`. The `OutputStore` of the registry
    acota por bytes si hiciera falta; normalmente el self-limit por lineas ya lo
    mantiene chico.

## 6. TDD Plan

It is attacked from the inside out, in sub-cycles (same as the order in the doc): hash
-> format -> snapshot -> tool. Each RED/GREEN sub-cycle; at the TRIANGULATE end of
the tool.

### Safety net

- Green base state before touching anything: The phase adds a new package
 (`hashline`), a new file (`read.go`) and touches `app.go` only to register.
- Command: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- Expected result: passes clean (M1..M10). If something fails, it is reported as
 pre-existing and is not followed blindly. After `go get` of xxHash32, re-run
 `go build ./...` to confirm that the dependency resolves.

### Understand

- Read `../architecture/read-edit-tools.md` (sections "The central idea: hashline",
 "The hash", "Tool `read`", "The `SnapshotStore`", "Snippets v1") and this spec.
 - Read `internal/tool/registry.go` (the interface `Tool`, `Result`) and `echo.go`
 (the pattern builtin) to follow the M4 contract.
- Expected behavior: header `[path#HASH]` + `NUM:TEXTO`; stable hash a
 CRLF/trailing-ws; recorded snapshot with full text and line views; range and
 truncated with actionable notices; binary/nonexistent/safe path escape.

### RED (sub-cycles, each one fails first)

1. `TestComputeFileHash_StableAcrossCRLFAndTrailingWhitespace`: `"a \nb\r\n"` and
 `"a\nb\n"` -> the same tag. Reference to `ComputeFileHash`, which does not exist -> does not
 compile -> RED.
2. `TestComputeFileHash_FourUppercaseHex`: the tag matches `^[0-9A-F]{4}$`, and a
 test-vector sets the value for a known input (anchors determinism).
3. `TestFormatHashline_HeaderAndNumberedLines`: given `["package main","",
   "func main(){}"]` and a tag, produces `[p#TAG]\n1:package main\n2:\n3:func main(){}`.
4. `TestReadTool_WholeFileHasHashHeaderAndNumberedLines`: reading a 3
 line file via FS fake -> `Output` starts with `[path#HASH]` and numbers 1..3.
5. `TestReadTool_RecordsSnapshotAndSeenLines`: after reading, `Snapshots.Head(abs)`
 has the full normalized text, `Hash` == the header, and `Seen` == {1,2,3}.

- Commands: `go test -run TestComputeFileHash ./internal/tool/hashline`,
 `go test -run TestReadTool ./internal/tool` -> expected failures.

### GREEN

- Write the minimum per sub-cycle: `hash.go`, then `format.go`, then
 `snapshot.go` (only `Record`/`Head`/`RecordSeenLines` working; `ByHash`/
 `Invalidate` can be left with simple impl), then `read.go`.
- `go get` from the xxHash32 lib when reaching the GREEN of the hash.
- Run only the red test of each sub-cycle until green.

### TRIANGULATE

Cases that avoid false green (the borders of the doc, "Cases that CANNOT be skipped"
applicable to `read`):

- `TestComputeFileHash_ChangesOnContentChange`: a real content change moves
 the tag (with entries chosen not to collide in 16 bits). {2,3}.
- `TestReadTool_SingleLineSelector`: `path:2` -> only line 2.
- `TestReadTool_TruncatesAtLineLimitWithContinuationNotice`: file > `MaxLines`
 -> first lines `MaxLines` + `[<N> more lines in file. Use :<offset> to
  continue]`; `Seen` == 1..MaxLines.
- `TestReadTool_OutOfRangeSelectorReportsBeyondEOF`: `:100-200` in file of 5
 lines -> notice `Line 100 is beyond end of file (5 lines total).`; snapshot
 recorded, `Seen` empty.
- `TestReadTool_InvalidSelectorErrors`: `:0`, `:5-2`, `:abc` -> actionable tool
 error (non-panic).
- `TestReadTool_MissingFileReturnsToolError`: FS returns not-exist -> error
 propagated by `Settle` (will be `Tool.Failed`); no snapshot.
- `TestReadTool_BinaryFileReturnsNotice`: content with NUL -> binary notice;
 `Snapshots.Head` nil (do not save).
- `TestReadTool_RejectsPathOutsideRoot`: `"../../etc/passwd"` -> error **without**
 call `FS.ReadFile` (verified with a spy: 0 readings).
- `TestReadTool_InvalidInputErrors`: bad JSON input (`{`) -> error.
- `TestSnapshotStore_RecordIdenticalReusesTag`: `Record` of the same text twice
 -> same tag, a single version (read-fusion).
- `TestSnapshotStore_ByHashFindsRecordedVersion`: save two different versions;
 `ByHash` finds the old one by its tag (minimum, for the recovery from edit).
- `TestSnapshotStore_ConcurrentRecord` with `-race`: several `Record` in parallel
 (the runner supports concurrent tools; the store is shared mutable state).

- Commands:
 - `go test -run TestComputeFileHash ./internal/tool/hashline`
 - `go test -run TestSnapshotStore ./internal/tool/hashline`
 - `go test -run TestReadTool ./internal/tool`
 - `go test -race -run TestSnapshotStore ./internal/tool/hashline`

### REFACTOR

- Cleanup without changing behavior: factorize test helpers (FS fake
 map-backed; a `readInto(t, files, input)` that creates the `ReadTool` with a fresh store
); extract selector parse if it grows; update
 `internal/tool/doc.go` (I landed the first builtin with FS; `edit` is still
 pending with its phase).
- Verify the green suite after formatting.
- Command: `gofmt -w internal app.go && go vet ./... && go test ./...`.

### Wiring (at the end, like `echo`)

- `app.go`: build `hashline.NewMemSnapshotStore()` and
 `tool.NewReadTool(workspaceRoot, snaps)`, add it to `NewRegistry(...)` and
 add `"read": true` to `Permissions`. A light test on `app_test.go` can
 confirm that `read` appears in the materialized `Definitions`.

## 7. Acceptance criteria (Done when)

1. There is `hashline.ComputeFileHash`: same text module CRLF/trailing-ws -> same
 tag; actual change -> distinct tag; the tag is `^[0-9A-F]{4}$` and a test-vector
 sets its value.
2. There are `hashline.FormatHeader`, `SplitLines` and `NumberLines`: they produce
 `[path#HASH]` and `NUM:TEXTO` with real numbers; `SplitLines` does not count the
 final empty segment of a file ending in `\n`.
3. There are `hashline.SnapshotStore` (full interface) and `MemSnapshotStore` with
 `Record` (read-fusion), `Head`, `RecordSeenLines`, `ByHash`, `Invalidate`,
 safe for concurrent use.
4. There is `tool.ReadTool` (implements `Tool`): reads a file, numbers with header
 hashline, records the snapshot of the **complete** file and marks the lines seen.
5. Selector v1: full file, `:N`, `:N-M`; real numbers; out of range and
 invalid selector give notice/actionable error, not panic.
6. Truncated by `MaxLines` issues the in-band continuation notice and `Seen` covers
 only what was issued.
7. Binary (NUL) -> notice, without snapshot. Non-existent file -> tool error.
 Path outside `Root` -> **unread** error (0 reads in spy).
8. `read` is registered in `app.go` with `"read": true` and appears in the materialized
 `Definitions`.
9. `go test ./...` (and `-race` where applicable) passes; `go vet ./...` clean;
 `gofmt -l .` empty.
10. The `edit` and its engine (parser/apply/patcher/recovery) were not built, nor
    folding, ni selectores ricos, ni handlers de imagen/PDF/archivo/URI.

## 8. Commands

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

## 9. Table of expected evidence

When closing the phase, the response/PR must include this table with actual results:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite M1..M10 green before editing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Hashline layout of read read | `../architecture/read-edit-tools.md`, `internal/tool/{registry,echo}.go` | identified behavior |
| NETWORK | Hash and read tests written first | `hashline/hash_test.go`, `read_test.go` + `go test -run ...` | expected failure (does not compile) |
| GREEN | `hash.go` + `format.go` + `snapshot.go` + `read.go` minimums | `internal/tool/hashline/*`, `internal/tool/read.go` | specific tests pass |
| TRIANGULATE | Range, truncated, out of range, binary, non-root path, read-merge | `go test -run TestReadTool ./internal/tool`, `go test -race -run TestSnapshotStore ...` | cases pass, `-race` clean |
| REFACTOR | Test helpers, updated `doc.go`, wiring on `app.go` | `gofmt -w`, `go vet ./...`, `go test ./...` | green suite, registered `read` |

## 10. Risks and decisions

- **The hash only needs internal consistency, not parity with oh-my-pi.** The
 `read` produces the tag and the `edit` verifies it with the **same** Go function: there is no
 exchange with oh-my-pi. That's why the exact lib of xxHash32 doesn't matter as long as it is
 deterministic; one is pinned and a **test-vector** sets the value so that a
 refactor does not move it. xxHash32-low16-4hex is maintained so as not to invent a
 format different from the one in the design.
- **RE2 does not have a lookahead.** The `/[ \t\r]+(?=\n|$)/g` of the original is ported to
 `[ \t\r]+(\n|$)` with replacement `$1` (capture the separator and re-emit it). It is
 the easiest gotcha to make mistakes; has its CRLF/trailing-ws test.
- **The range-read hashes the entire file.** Although the model reads `:50-60`, the
 header carries the hash of **the entire** file and the snapshot saves the entire
 text. So the `edit` line number anchor indexes the actual file and the
 freshness gate covers changes anywhere, not just in the read
 range. It is the behavior of oh-my-pi and the reason for re-reading the entire file.
- **`Result.Truncated` (OutputStore) vs continuation notice (read).** They are two different
 truncations: the `OutputStore` (M4) cuts by **bytes** and marks
 `Truncated` so that the UI recovers the complete one by `callID`; the `read` cuts by
 **lines** and warns **in-band** (`[N more lines... Use :offset]`) so that the
 model requests the next range. The `read` **does not** set `Result.Truncated` by its
 line limit; Leave that flag for `OutputStore`. It is documented so as not to
 confuse them.
- **`SnapshotStore` with complete interface but partial use.** It is decided to leave
 `ByHash`/`Invalidate`/history in the interface (although the `read` does not exercise them in depth) so that the `edit` phase does not have to redo the store signature at the same time. add
 3-way-merge recovery. It is the only concession to "design in advance", and it is
 cheap: the doc already defines that contract. Active behavior (recovery) is phase 2; here we only test that `ByHash` finds a recorded version.
- **Sandbox by `Root`, fail-closed.** Resolving inside `Root` and rejecting `..`
 outside the **before** reading is v1's only defense against reading outside the
 workspace. It is tested with a spy that claims 0 readings in the case of escape (same
 idea as "rejection without effects" of the registry in M4).
- **Binary detected by NUL, not by extension.** A NUL byte -> notice and without
 snapshot (it is not editable by hashline; numbering it would produce mojibake). No
 attempts to guess encoding in v1.
- **Context lines deferred to `edit`.** oh-my-pi adds 1 line of context before
 and 3 after a range to reduce anchor misses by one line. That only matters to `edit`; the `read` v1 outputs exactly what was requested and marks exactly
 that as seen. It is added when the `edit` lands (with its effect on `seenLines`).
- **Reading the entire file, without chunking.** v1 reads the entire file and cuts into
 memory: simple and sufficient for a desktop editor. Chunking
 (`READ_CHUNK_SIZE`) and snapshot byte caps are added if a giant
 file forces it, not for speculation.
- **`displayPath` relative to the workspace.** The header uses the relative path (not the absolute
) so that it is readable and so that the `edit` resolves it with the same
 rule (`Root` + relative). The snapshot is indexed by the absolute/canonical path.
- **`echo` follows.** This phase does not touch `echo`: the agent can have `echo` and
 `read` allowed at the same time. `bash`/`edit`/`write`/`grep`/`glob` arrive later.

## 11. Safe cuts for v1 (lazy)

From `../architecture/read-edit-tools.md` itself ("Safe Cuts for v1"), applied
to `read`:

- **Maintained yes or yes**: `ComputeFileHash` + normalization, header `[path#HASH]`,
 numbering `NUM:TEXTO`, `SnapshotStore` with `seenLines`. Without this it is not "
 oh-my-pi style" and the `edit` cannot fail-safe.
- **Omitted in v1**: folding/summarization (tree-sitter), rich selectors
 (`:N+K`, open-ended, lists, `:raw`), context lines, images/PDF/file/SQLite/
 URIs/URLs, chunking, active recovery. They are optimizations or extra surface, not
 the security mechanism.

## 12. Sources

- Read/edit design: `../architecture/read-edit-tools.md` (hashline idea, the hash, Tool
 `read`, `SnapshotStore`, edge cases, cuts v1, implementation order).
- oh-my-pi code (verified 2026-06-21): `packages/hashline/src/format.ts`
 (`computeFileHash`, normalization `/[ \t\r]+(?=\n|$)/g`, `HL_FILE_HASH_LENGTH=4`,
 header `[path#HASH]`, line separator `:`), `packages/coding-agent/src/tools/
  read.ts` (selector `:<sel>`, `RANGE_LEADING_CONTEXT_LINES=1`/
 `RANGE_TRAILING_CONTEXT_LINES=3`, continuation notice, beyond-EOF, binary for
 NUL, snapshot full-file + `recordSeenLines`) Way of working: `AGENTS.md`. Loop roadmap: `../plans/agent-loop-roadmap.md`.
