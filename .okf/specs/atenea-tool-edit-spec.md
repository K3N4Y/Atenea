---
updated_at: 2026-07-09
summary: Specification for tool edit spec.
---

# Spec — Tool `edit` (hashline, phase 2 of track read/edit)

Executable spec of **tool `edit`** style `can1357/oh-my-pi`. It is **phase 2**
of the read/edit track (`../architecture/read-edit-tools.md`); phase 1 (`read`) already left
the `internal/tool/hashline` motor with `ComputeFileHash`, `FormatHeader`,
`SplitLines`, `NumberLines`, and the `SnapshotStore` (`Record`/`Head`/`ByHash`/
`RecordSeenLines`/`Invalidate`, with `Seen` per snapshot). The `edit` **consumes** that
snapshot: addresses by line number and only applies if the file continues
hashing to the `HASH` that the `read` showed.

It is the most difficult part of the agent: its correctness decides whether it corrupts files or
not. We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

`../architecture/read-edit-tools.md` documents the **hashline** mechanism: the `read`
numbers each line and prepends `[path#HASH]` (hash of the entire file); The `edit`
 addresses by **line number** but is only valid if the file **continues
hashing to the same HASH**. The anchor is the line number; the hash is the
**freshness gate**. If the file changed, the HASH diverges and the edit **fails
for sure** (in v1: `MismatchError`; the 3-way-merge recovery comes in a later
pass). Never apply a diff stale blindly.

Phase 1 (`read`, see `atenea-tool-read-spec.md`) already implemented steps 1, 2 and 5 of the doc order (hashing + normalization; formatting + numbering;
`SnapshotStore` with `Seen`). This phase implements steps **3, 4 and 6**: parser
patch (text -> `[]Edit`), apply (`applyEdits`), and patcher (verification of
hash + check of `seenLines` + all-or-nothing + commit). The recovery (step 7) and
block ops (tree-sitter) are **outside of v1**.

## 2. Objective

Leave the `edit` tool that applies a hashline patch to a file, green against a FS
fake, and registered with the agent.

In `internal/tool/hashline` (pure engine, without FS or agent):

- `types.go`: `EditKind`, `Range`, `Edit`, `ApplyResult`, `Patch`/`Section`,
 `MismatchError`, `MissingTagError`.
- `parser.go`: `ParsePatch(text) (Patch, error)` — hashline patch text ->
 sections with header `[path#HASH]` and `[]Edit` (ops `SWAP/DEL/INS.*`).
- `apply.go`: `ApplyEdits(lines []string, edits []Edit) (ApplyResult, error)` —
 applies the edits to the lines of the file respecting the original 1-indexed
 numbering even if several edits change the count.
- `patcher.go`: `Patcher.Apply(patch) (PatchResult, error)` — preflight of
 section(s), hash check against live content, check of
 `seenLines`, all-or-nothing, commit (write, rewrite snapshot, return new
 header `[path#HASH]`).In `internal/tool`:

- `edit.go`: `EditTool` (implements the `Tool` interface of M4). Parse the patch,
 resolve the path within `Root`, run `Patcher`, return the new header.
- behavioral tests.

In `app.go`:

- build `EditTool` with the **same** root and **same** `SnapshotStore` as
 `read` (so that the snapshot recorded by `read` is read by `edit`), register it
 and allow `"edit": true`.

This phase **does not** build the 3-way-merge recovery, nor the
(tree-sitter) block ops, nor multi-file in a single patch (v1: one section), nor the
CRLF/BOM restore on write (v1 writes LF; see Clippings).

## 3. Scope

### Inside

- `internal/tool/hashline/types.go`: `EditKind` (Insert/Delete/Replace),
 `Cursor` (BOF/EOF/BeforeAnchor/AfterAnchor), `Range`, `Edit`, `ApplyResult`,
 `Section`, `Patch`, `MismatchError`, `MissingTagError`.
- `internal/tool/hashline/parser.go`: `ParsePatch`.
- `internal/tool/hashline/apply.go`: `ApplyEdits`.
- `internal/tool/hashline/patcher.go`: `Patcher`, `NewPatcher`, `Apply`,
 `PatchResult`, the interface `Filesystem` (read+write) that the patcher uses.
- `internal/tool/edit.go`: `EditTool`, `NewEditTool`.
- Tests: `internal/tool/hashline/{parser,apply,patcher}_test.go`,
 `internal/tool/edit_test.go`.
- `app.go`: build and register `edit` with the `SnapshotStore`/`Root` shared
 with `read`.
- Update `internal/tool/hashline/doc.go` and `internal/tool/doc.go`.

### Out (do not do in v1)

- **Recovery 3-way-merge** (`recovery.go`): when drifting with specific anchors, v1
 returns `MismatchError` (re-read). The merge against the stale tag snapshot is
 a later pass. It is the reason to have `SnapshotStore.ByHash` already ready.
- **Block ops** (`SWAP.BLK`, `DEL.BLK`, `INS.BLK.POST`, tree-sitter): out.
- **Multi-file in a patch** (multiple sections `[path#HASH]`): v1 accepts **one**
 section. The all-or-nothing preflight is still respected (one section is atomic).
- **Restoration of line endings / BOM**: v1 normalizes to LF (like `read`) and
 writes LF. Restoring original CRLF/BOM is a later refinement (most
 code is LF; limit is documented).
- **Permissions per path pattern** for `edit`: follows the name set of M4
 (`"edit": true`). The rich model (ask/allow per route) arrives when needed.

## 4. Types and contract

### 4.1 Patch format (what the model sees)

A single parameter `patch`: a hashline text with a section header and a
sequence of hunks. Example:

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

- Section header: `[ruta#HASH]` (parses `FormatHeader` in reverse). The
 `HASH` is the one that `read` showed; no header -> `MissingTagError`.
- Hunks (v1 ops):

| Operation | Syntax | Effect |
| --- | --- | --- |
| Range Replacement | `SWAP start.=end:` + lines `+...` | replace `[start,end]` with the payload |
| Deleted | `DEL n` or `DEL start.=end` | delete that line(s) |
| Insert before | `INS.PRE n:` + `+...` | insert payload before the line `n` |
| Insert after | `INS.POST n:` + `+...` | insert payload after the line `n` |
| Insert at start | `INS.HEAD:` + `+...` | inserted at the beginning of the file |
| Insert at the end | `INS.TAIL:` + `+...` | inserted at the end of the file |

- The payload lines are prefixed with `+`. `.=` separates the range (`5.=10`).
- The numbers are 1-indexed on the file that hashes the `HASH` header.

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

Schema (what the model sees):

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

## 5. Semantics of `edit`

`Execute` does, in order:

1. **Parse of input.** `json.Unmarshal` of `patch` (string). Invalid input ->
 tool error.
2. **Parse patch.** `ParsePatch(patch)`. No header -> `MissingTagError`
 ("missing header [path#HASH]"). Multiple sections -> error "multi-file not
 supported in v1". Malformed op -> parsing error.
3. **Resolve the path.** The path of the header, inside `Root` (sandbox: reject
 `..` outside of `Root` **without** touching the FS), same as `read`.
4. **Patcher.Apply** (all-or-nothing):
 - read + normalize (BOM/LF), `liveHash`.
 - **no drift** (`liveHash == esperado`): check `seenLines` against snapshot
     (`Snapshots.ByHash(abs, esperado)` o `Head`); una ancla a una linea fuera de
     `Seen` -> error "no edites lineas que no leiste". Aplicar `ApplyEdits`.
- **drift**, only `INS.HEAD/TAIL`: stable position -> apply with warning.
 - **drift** with specific anchors: `MismatchError` (v1, without recovery). The message
     distingue "hash no reconocido (re-lee, nunca inventes el tag)" de "el archivo
     cambio entre read y edit (copia el [path#newhash] del edit previo o re-lee)".
- **no-op** (edits do not change anything) -> explicit error, do not write.
5. **Commit.** Write the new text (LF). `Snapshots.Record(abs, nuevo)` +
 `RecordSeenLines` of the touched lines (to chain edits). Return
 `Result{Output: "[ruta#nuevoHASH]"}` (+ summary/warnings) so that model
 chains without re-reading.

## 6. TDD Plan

Inside-out sub-cycles: parser -> apply -> patcher -> tool. Each
RED/GREEN; at the TRIANGULATE end of the EDGE and REFACTOR + wiring cases.

### Safety net

- Green suite before touching (engine `hashline` and `read` already exist). The
 phase adds new files to `hashline` and `edit.go`, and taps `app.go` to register.
- `go test ./...`, `go vet ./...`, `gofmt -l .`.

### Understand

- Read `../architecture/read-edit-tools.md` (Tool `edit`, operations, patcher,
 mismatch messages, edge cases, v1 cuts) and this spec.
- Read `internal/tool/hashline/{hash,format,snapshot}.go` (what `read` left) and
 `internal/tool/read.go` (tool pattern, sandbox, FileReader).

### RED (sub-cycles, each one fails first)

1. `TestParsePatch_HeaderAndSwap`: a patch with `[p#1A2B]` + `SWAP 3.=5:` + payload
 parses a `Section{Path:"p", Hash:"1A2B", Edits:[{Replace, Range{3,5}, ...}]}`.
2. `TestParsePatch_MissingHeaderErrors`: without `[p#HASH]` -> `MissingTagError`.
3. `TestApplyEdits_ReplaceRange`: `ApplyEdits(["a","b","c","d"], [SWAP 2.=3 -> "X"])`
 -> `["a","X","d"]`.
4. `TestPatcher_NoDriftAppliesAndRecordsNewHash`: FS fake with a file whose hash
 == that of the header and a snapshot with `Seen` covering the lines; `Apply` writes
 the new text and returns `[path#nuevoHASH]` with `nuevoHASH ==
   ComputeFileHash(nuevo)`.
5. `TestEditTool_AppliesPatchReturnsNewHeader`: `Execute({patch})` happy path ->
 `Result.Output` starts with `[path#`.

### GREEN

- Minimum per sub-cycle: `types.go` + `parser.go`; then `apply.go`; then
 `patcher.go`; then `edit.go`. Run only the red test of each one.

### TRIANGULATE (cases that CANNOT be skipped, from the doc)

- `TestApplyEdits_DeleteAndInsertKeepLineNumbers`: combine `DEL` + `INS.POST` with
 numbers from the original file; the splices do not move the indexes between themselves.
- `TestApplyEdits_NoOpErrors`: edits that do not change anything -> error (do not write).
- `TestParsePatch_AllInsertVariants`: `INS.PRE/POST/HEAD/TAIL` parse to the correct
 `Cursor`.
- `TestPatcher_DriftWithAnchorReturnsMismatch`: `liveHash != esperado` with a
 `SWAP` -> `MismatchError` (unwritten). `Recognized` according to `ByHash`.
- `TestPatcher_HeadTailOnStaleTagAppliesWithWarning`: `liveHash != esperado` but
 only `INS.TAIL` -> applies + warning (not fatal).
- `TestPatcher_EditUnseenLineRejected`: anchor to a line outside `Seen` of the
 snapshot -> error "do not edit lines you did not read", without writing.
- `TestPatcher_AllOrNothingDoesNotWriteOnPreflightError`: if the preflight fails, the
 FS fake **does not** record any writes.
- `TestEditTool_MissingTagErrors`, `TestEditTool_RejectsPathOutsideRoot`,
 `TestEditTool_InvalidInputErrors`.
- `-race` where applicable (the `SnapshotStore` is shared and concurrent).

### REFACTOR + wiring

- Cleaning (test helpers, `doc.go`). Wiring on `app.go`: `EditTool` with the
 **same** `Root` and `SnapshotStore` as `read`, permission `"edit": true`.
- Gates: `gofmt -l .`, `go vet ./...`, `go test ./...`, `-race`.

## 7. Acceptance criteria (Done when)

1. `ParsePatch` parse header `[path#HASH]` + ops `SWAP/DEL/INS.PRE/POST/HEAD/TAIL`
 with payloads `+...`; no header -> `MissingTagError`; multi-section -> error v1.
2. `ApplyEdits` applies replace/delete/insert respecting the original numbering with
 several splices; no-op -> error.
3. `Patcher.Apply` checks `liveHash` against expected: no-drift applies (after
 check `seenLines`); drift with anchor -> `MismatchError`; HEAD/TAIL stale ->
 warning; anchor to unseen line -> rejection; **all-or-nothing** (preflight fails
 -> does not write). Commit rewrites snapshot and returns `[path#nuevoHASH]`.
4. `EditTool` (implements `Tool`): applies a patch, sandbox fail-closed, returns the
 new header; actionable errors (`MissingTagError`/`MismatchError`/path outside
 of root/invalid input).
5. `edit` registered on `app.go` with the `SnapshotStore`/`Root` shared with
 `read` and `"edit": true`; appears in `Definitions`.
6. `go test ./...` (and `-race`) green; `go vet ./...` clean; `gofmt -l .` empty.
7. No 3-way-merge recovery, block ops, multi-file, nor
 CRLF/BOM restore was built.

## 8. Commands

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

## 9. Table of expected evidence

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite (read + engine) green before editing | `go test ./...`, `go vet`, `gofmt -l .` | pass |
| Understand | Edit design + read engine | `../architecture/read-edit-tools.md`, `internal/tool/hashline/*`, `read.go` | identified behavior |
| NETWORK | Parser/apply/patcher/tool ​​tests written first | `hashline/{parser,apply,patcher}_test.go`, `edit_test.go` | expected failure |
| GREEN | `types/parser/apply/patcher` + `edit.go` minimums | those files | specific tests pass |
| TRIANGULATE | Drift/mismatch, seenLines, HEAD-TAIL stale, all-or-nothing, no-op, sandbox | `go test -run 'TestPatcher\|TestApplyEdits\|TestEditTool' ...`, `-race` | cases happen |
| REFACTOR | Cleanup + wiring `edit` on `app.go` | `gofmt`, `go vet`, `go test ./...` | green suite, registered `edit` |

## 10. Risks and decisions

- **The hash is the gate, the line number is the anchor.** The `edit` never
 applies to a file whose `liveHash` is not expected, except `INS.HEAD/TAIL`
 (stable position). Thus a file that changes between `read` and `edit` fails
 for sure instead of mangrove. It is the heart of "oh-my-pi style".
- **Recovery deferred to a later pass.** v1 fails with `MismatchError` before
 drift with anchors. The 3-way-merge (using `Snapshots.ByHash(path, esperado)` to
 retrieve the text that the stale tag names) is the next pass. `ByHash` is now
 ready (I leave it on `read`); It is documented so as not to redo the interface.
- **`seenLines` avoids editing from memory.** The `edit` rejects anchors to lines that the
 `read` did not show (`Snapshot.Seen`). Without this, the model edits lines it didn't see and
 mangles the file. It is the second safety net after the hash.
- **All-or-nothing.** The patcher preflights in memory and only writes if everything passes.
 The test is tested with a fake FS that counts writes: 0 writes if the
 preflight fails.
- **`SnapshotStore`/`Root` shared read+edit.** In `app.go`, `read` and
 `edit` receive the **same** instance of `MemSnapshotStore` and the same root: the
 snapshot that `read` records is the one that `edit` reads. If they were built separately, the `edit` would never see the `Seen` of the `read` and everything would be mismatch. Restoring the original line-ending/BOM is a later refinement;
 v1 documents it as a limit (most of the code is LF). Risk: a
 CRLF file is rewritten as LF.
- **One section in v1.** The patcher is all-or-nothing per section; v1 accepts a
 patch from a file. Multi-file (several sections with joint preflight)
 arrives when a real case requests it.
- **Patch as text, not structured JSON.** The hashline format of
 oh-my-pi (header + hunks `SWAP/DEL/INS.*`) is maintained instead of a structured `{path, hash, ops}`
, so as not to invent a format different from that of the design and reuse
 `FormatHeader`. Decision to be confirmed upon review.
- **Stable numbering in `ApplyEdits`.** All edits refer to the original
 file; They are applied from highest to lowest position (or in one pass constructing the
 result) so that one splice does not run the indices of another. It is the classic bug of
 one editor per line; It has its combination test.

## 11. Safe cuts for v1 (lazy)

From `../architecture/read-edit-tools.md` ("Safe Cuts for v1"):

- **Keeps yes or yes**: ops `SWAP/DEL/INS.*`, hash verification,
 `seenLines` check, all-or-nothing, actionable mismatch messages. Without this it is not
 "oh-my-pi style" and does not crash for sure.
- **Omitted in v1**: 3-way-merge recovery (boot with `MismatchError`),
 block ops (tree-sitter), multi-file in one patch, CRLF/BOM restore. They are
 optimizations or extra surface area, not the security mechanism.

## 12. Sources

- Design: `../architecture/read-edit-tools.md` (Tool `edit`,
 `format.ts` operations, `patcher.ts` patcher, `mismatch.ts` mismatch, snapshots,
 edge cases, v1 cuts, implementation order).
 - Phase 1: `atenea-tool-read-spec.md` and `internal/tool/hashline/*`
 (`ComputeFileHash`, `FormatHeader`, `SplitLines`, `NumberLines`, `SnapshotStore`
 with `Seen`/`ByHash`).
- Tools registry: `internal/tool/registry.go` (`Tool`, `Result`).
- How to work: `AGENTS.md`. Track: `../architecture/read-edit-tools.md`.
