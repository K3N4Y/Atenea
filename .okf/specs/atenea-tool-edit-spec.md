---
updated_at: 2026-07-10
summary: Specification for tool edit spec.
---

# Spec — Tool `edit` (hashline, phase 2 of track read/edit)

Executable specification for Atenea's `edit`, inspired by and pinned to
`can1357/oh-my-pi` commit `09a7c865636457c50ed75fc3b1a7cc21ef72c105`.
The adaptation intentionally exposes only Atenea's single-file
`SWAP`/`DEL`/`INS.*` grammar. It consumes the session-isolated snapshots created
by read/write/grep and applies strict provenance and freshness checks.

## 1. Context

`../architecture/read-edit-tools.md` is authoritative for the completed core:
strict one-section parsing, bounded collision-safe snapshots, exact and
unambiguous uniform-offset drift recovery, complete supported mode preservation,
per-path read-through-commit serialization, alias rejection, and atomic OS
replacement with a committed-but-durability-uncertain result for post-rename
failures. Stale boundary inserts retain their documented behavior. Malformed
patches and failed preflight never write; oversized reads emit no unusable header.

Multi-file patches, dual grammar, block/tree-sitter operations, notebooks/LSP,
folding, and rich selectors are explicitly outside this specification.

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
- `patcher.go`: `Patcher.Apply(patch) (PatchResult, error)` — strict preflight,
  freshness/provenance checks, exact unique uniform-offset recovery, and commit
  preserving BOM, EOL, final newline, and supported mode bits. Successful
  retainable commits return a new `[path#HASH]` header.

- `edit.go`: `EditTool` (implements the `Tool` interface of M4). Parse the patch,
 resolve the path within `Root`, run `Patcher`, return the new header.
- behavioral tests.

In `app.go`:

- build `EditTool` with the **same** root and **same** `SnapshotStore` as
 `read` (so that the snapshot recorded by `read` is read by `edit`), register it
 and allow `"edit": true`.

The completed core includes exact, fail-closed uniform-offset drift recovery and
storage convention preservation. Block/tree-sitter operations and multi-file
patches remain outside the public grammar.

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

### Out (explicit non-goals)

- **Block ops** (`SWAP.BLK`, `DEL.BLK`, `INS.BLK.POST`, tree-sitter): out.
- **Multi-file patches** (multiple `[path#HASH]` sections): one section remains
  the public contract.
- **Fuzzy recovery**: completed recovery is exact, unique, and uniform-offset;
  changed, ambiguous, or non-uniform anchors fail closed.
- **Permissions per path pattern** for `edit`: follows the name set of M4
  (`"edit": true`). The rich ask/allow-per-route model remains out.

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

// MismatchError: el archivo cambio entre read y edit y la recuperacion exacta no
// es segura (ancla cambiada, ambigua o desplazamiento no uniforme). Lleva contexto.
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

// Patcher preflighta y aplica una seccion de forma all-or-nothing. Comparte el
// SnapshotStore con read para provenance y recuperacion exacta contra la version
// que nombra el hash stale.
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

// Apply exige el header, lee y normaliza para comparar hashes, valida seenLines
// y aplica directamente cuando no hay drift. Con drift, INS.HEAD/TAIL reconocidos
// conservan su posicion estable y avisan; edits anclados solo se recuperan cuando
// cada region original esta intacta, aparece exactamente una vez y todas comparten
// un desplazamiento uniforme. El commit restaura BOM, EOL dominante, newline final
// y modo soportado, reemplaza atomicamente y registra el nuevo snapshot. Si el
// snapshot nuevo no puede retenerse por capacidad o colision, no devuelve un header inutilizable.
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
 - read + normalize for hashing and validation.
 - **no drift**: validate `seenLines`, then apply.
 - **recognized stale HEAD/TAIL**: apply at the live boundary with a warning.
 - **anchored drift**: recover only for intact, unique anchors sharing one uniform
   line offset; changed, ambiguous, or non-uniform anchors return `MismatchError`.
 - **no-op**: explicit error, no write.
5. **Commit.** Restore BOM, dominant/original EOL, final newline and supported
 mode bits; atomically replace; record the normalized result and touched lines.
 Return a header only when the new snapshot was retained.

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
3. `Patcher.Apply` validates freshness and `seenLines`; safely recovers unique
uniform anchored drift, rejects changed/ambiguous/non-uniform drift, and preserves
recognized stale HEAD/TAIL behavior. Failed preflight does not write. Commit
preserves storage conventions and supported modes and never returns an
unresolvable header.
4. `EditTool` is sandboxed and returns actionable parser, mismatch, alias, and
commit-uncertainty outcomes.
5. `edit` is registered with the same session-isolated store and root as `read`.
6. Full, race, vet, formatting, description, and production build gates pass.
7. Block operations and multi-file patches remain non-goals.

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

- **The hash gates freshness; recovery is exact.** No-drift edits use their line
  anchors directly. Anchored stale edits recover only if every original region is
  unchanged, uniquely present in live text, and all regions share one line offset;
  otherwise `MismatchError` fails closed. Recognized stale HEAD/TAIL inserts keep
  their stable-boundary behavior.
- **`seenLines` avoids editing from memory.** The `edit` rejects anchors to lines that the
 `read` did not show (`Snapshot.Seen`). Without this, the model edits lines it didn't see and
 mangles the file. It is the second safety net after the hash.
- **All-or-nothing.** The patcher preflights in memory and only writes if everything passes.
 The test is tested with a fake FS that counts writes: 0 writes if the
 preflight fails.
- **Shared bounded snapshots.** `read` and `edit` share the same session-isolated
  store. An individually unretainable snapshot is rejected before any history,
  LRU, or byte accounting mutation, so prior editable snapshots survive and no
  unusable header is emitted.
- **Storage preservation.** Commit restores BOM, dominant/original EOL, final
  newline, and supported mode bits and uses atomic replacement; CRLF is not
  rewritten to LF.
- **One section.** Multi-file joint preflight remains outside the public contract.
- **Patch as text, not structured JSON.** The hashline format of
 oh-my-pi (header + hunks `SWAP/DEL/INS.*`) is maintained instead of a structured `{path, hash, ops}`
, so as not to invent a format different from that of the design and reuse
 `FormatHeader`. Decision to be confirmed upon review.
- **Stable numbering in `ApplyEdits`.** All edits refer to the original
 file; They are applied from highest to lowest position (or in one pass constructing the
 result) so that one splice does not run the indices of another. It is the classic bug of
 one editor per line; It has its combination test.

## 11. Remaining non-goals

- Block/tree-sitter operations and multi-file patches are omitted surface area.
- Recovery is deliberately not fuzzy: changed, duplicate/ambiguous, or
  non-uniformly moved anchors fail closed.
- BOM, EOL, final-newline, mode preservation, and exact uniform-offset recovery
  are completed core behavior, not deferred work.

## 12. Sources

- Design: `../architecture/read-edit-tools.md` (Tool `edit`,
 `format.ts` operations, `patcher.ts` patcher, `mismatch.ts` mismatch, snapshots,
 edge cases, v1 cuts, implementation order).
 - Phase 1: `atenea-tool-read-spec.md` and `internal/tool/hashline/*`
 (`ComputeFileHash`, `FormatHeader`, `SplitLines`, `NumberLines`, `SnapshotStore`
 with `Seen`/`ByHash`).
- Tools registry: `internal/tool/registry.go` (`Tool`, `Result`).
- How to work: `AGENTS.md`. Track: `../architecture/read-edit-tools.md`.
