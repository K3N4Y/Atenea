---
updated_at: 2026-07-09
summary: Design for hashline-based read and edit tools in Go.
---

# Tools `read` and `edit` oh-my-pi style (design for Go)

Investigated on 2026-06-19 on `can1357/oh-my-pi` (packages `coding-agent` and
`hashline`). Documents how the `read` and `edit` tools from
oh-my-pi really work and how to replicate them in Go for Atenea.

It is the most difficult part of the agent, which is why it is written in detail: the
correctness of `edit` decides whether the agent corrupts files or not.

## The core idea: hashline

oh-my-pi does not edit by "find old text and replace with new" (fragile: the
model rewrites entire blocks or crashes because of a line of context). Nor does it edit
by line number simply (fragile: the file changes and the number points to something else
).

It uses a hybrid scheme called **hashline**:

- The `read` numbers each line and prefixes a **header with a hash of the entire
 file**: `[ruta#HASH]`.
- The `edit` addresses by **line number**, but is only valid if the file
 **continues hashing the same `HASH`**.
- If the file changed, the `HASH` diverges and the edit **fails for sure** (or recovers
 by 3-way-merge against the snapshot that the hash names). Never apply a diff
 stale blindly.

That is to say: the **anchor is the line number**, and the **hash of the full content is
the freshness gate**. That's all the magic.

### The hash (the most important thing to copy well)

From `packages/hashline/src/format.ts`:

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

Details that matter:

- They are **4 hex** (`[0-9A-F]{4}`), 16 bits. Collision possible but rare; recovery
 covers the rest.
- **normalization** (trim trailing whitespace and CR) means that CRLF vs LF and
 trailing spaces do not invalidate the tag.
- Any `read` of identical bytes produces the **same** tag (read fusion).

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

### API (what the model sees)

oh-my-pi exposes a single parameter:

```ts
const readSchema = type({
  path: "string", // p.ej. "src/foo.ts", o con selector "src/foo.ts:50-100"
});
```

The **range of lines is embedded in the path** with `:<sel>` (`50-100`), not as
 separate parameters. It also supports raw mode and internal URIs (`omp://`, etc.).

### Output (hashline format)

For a text file, `read` outputs:

```text
[src/foo.ts#1A2B]
1:package main
2:
3:func main() {
4:    fmt.Println("hi")
5:}
```

- First line: header `[ruta#HASH]`.
- Each line of content: `NUM:TEXTO` (number, colon, content).
- In readings by range, the numbers reflect the actual position in the file.

### Key behavior

- **Limits per byte and per line**: short large reads; reports truncation.
- **Summarization/folding**: for large files you can collapse bodies and show
 only the skeleton, marking elided spans (this uses tree-sitter; in Atenea it is
 optional, see "Snipping for v1").
- **Context lines**: around an explicit range add 1 line of context
 in front to reduce anchor failures "by a line".
- **Snapshot record**: when reading, records the full normalized text in the
 `SnapshotStore` and marks which lines it showed (`seenLines`). This enables two things
 of `edit`: drift recovery and the "do not edit lines you did not
 read" check.

## Tool `edit` (via `write` with hashline content)

On oh-my-pi the `write` has `{ path, content }`. If the `content` starts with a
header `[ruta#HASH]` followed by hashline operations, it is treated as **patch**; if
no, it is a **complete write** of the file. For Atenea it is convenient to separate it into an explicit
tool `edit`, but the engine is the same.

### Operations (from `format.ts`)

The patch is text. Hunk Headers:

| Operation | Syntax | Effect |
| --- | --- | --- |
| Range Replacement | `SWAP start.=end:` + lines `+...` | replace `[start,end]` with the payload |
| Deleted | `DEL n` or `DEL start.=end` | delete that line(s) |
| Insert before | `INS.PRE n:` + `+...` | insert payload before the line `n` |
| Insert after | `INS.POST n:` + `+...` | insert payload after the line `n` |
| Insert at start | `INS.HEAD:` + `+...` | inserted at the beginning of the file |
| Insert at the end | `INS.TAIL:` + `+...` | inserted at the end of the file |
| Block (tree-sitter) | `SWAP.BLK n:`, `DEL.BLK n`, `INS.BLK.POST n:` | operates on the syntax block starting at `n` |

- Payload lines are prefixed with `+`.
- `.=` separates ranges (`5.=10`).
- Multiple files: several sections, each with its header `[ruta#HASH]`.

### How to apply and verify (from `patcher.ts`)

The `Patcher` is **all-or-nothing**: it first `prepare` (preflight) all the
sections in memory, and only if they all pass, does it `commit` (write to disk). By
section:

1. Parse the operations; require that the section contain `HASH` (no tag -> error).
2. Read file, remove BOM, normalize to LF.
3. Calculate `live = ComputeFileHash(contenidoActual)`.
4. Compare with the expected `HASH` of the section:
 - **`live == esperado` (no drift)**: the lines index the exact content.
     - Chequear `seenLines`: rechazar anclas a lineas que el read nunca mostro.
     - Aplicar las operaciones (`applyEdits`).
- **Only inserts HEAD/TAIL** and tag stale: stable position, not fatal. Applies
     sobre el contenido vivo y avisa con warning.
- **Drift with concrete anchors**: try `recovery.tryRecover()` (3-way-merge
     del edit contra el snapshot que el tag nombra, sobre el contenido vivo). Si
     funciona, aplica el merge; si no, **`MismatchError`** (re-leer).
5. `commit`: restore line endings + BOM, write, and **record new snapshot**;
 return the `[ruta#nuevoHASH]` header to chain edits without re-reading.

### Error messages (from `mismatch.ts`)

The rejection is actionable and distinguishes two cases:

- **Hash not recognized** (not from this session): "hash #XXXX is not from this
 session... re-read the file... never invent the tag".
- **Hash recognized but the file changed**: "file changed between read and edit...
 copy the `[path#newhash]` header from the prior edit's response, or re-read".

It also attaches context: the anchored lines with a couple of lines around them.

## The `SnapshotStore` (key to recovery)

From `snapshots.ts`. It is what allows you to recover from a drift instead of just
failing. It is a per-path store with short full file version history:

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

- Default implementation: Bounded LRU (oh-my-pi: 30 paths, 4 versions/path, 64 MiB).
- `Record` of identical content **reuses the tag** and refreshes recency (read fusion).
- The recovery uses `ByHash` to find the exact text that the stale tag names
 and do the 3-way-merge.

For Atenea, one version **in memory per session** is enough for v1; the contract is
the same as loop durable `Store` (`agent-loop.md`).

## Design in Go for Atenea

Suggested package: `internal/tool/hashline` (pure engine, no FS or agent) + the
tools `read`/`edit` in `internal/tool` that use it.

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

Core types (mirror of `hashline/types.ts`):

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

`Patcher` Contract (`patcher.ts` Mirror):

```go
type Patcher struct {
    FS        Filesystem
    Snapshots SnapshotStore
    // BlockResolver opcional (tree-sitter); nil en v1.
}

// All-or-nothing: prepara todo en memoria, luego commitea.
func (p *Patcher) Apply(patch Patch) (PatchResult, error)
```

## Edge cases that CANNOT be skipped

These are what make the design worth copying, not inventing:

- **Tag missing** in the edit -> clear error ("header `[path#HASH]` is missing").
- **Invented tag / from another session** -> rejection with message other than "change the
 file".
- **Recoverable drift** (a previous edit of the same session changed the file) ->
 3-way-merge against the snapshot of the previous tag, no re-read.
- **Non-recoverable drift** -> `MismatchError` with lines context.
- **Edit to unread lines** (`seenLines`) -> rejection: edit mangla memory
 files.
- **HEAD/TAIL with stale tag** -> warning, non-fatal (stable position).
- **Normalization** CRLF/BOM/trailing-ws -> keep hashing and restore on
 write.
- **Multi-file** -> preflight all sections before touching disk;
 report which ones were written if a failure in the middle.
- **No-op** (the edit does not change anything) -> explicit error, do not write.

## Safe trims for v1 (lazy)

To avoid dying trying, v1 can leave out the tree-sitter dependent and
keep the heart intact:

- **Skip** block operations (`SWAP.BLK`, `DEL.BLK`, `INS.BLK.POST`) and the
 folding/summarization of `read`. They are optimizations; They are not the security mechanism. Without this, it's not oh-my-pi style.## Deployment order (TDD, see AGENTS.md)

1. `ComputeFileHash` + normalization. NETWORK: same text (CRLF/LF, trailing ws) ->
 same tag; real change -> different tag.
2. `format` + `read` numbering. RED: a file produces header + `NUM:TEXTO`.
3. `parser`: patch text -> `[]Edit` for `SWAP/DEL/INS.*`. NETWORK per operation.
4. `apply`: apply edits to text. RED: replace/delete/insert and combinations; The
 line numbers are respected when making several splices.
5. `SnapshotStore` in memory. NETWORK: record merges identical content; `ByHash`.
6. `patcher`: hash verification + `seenLines` + all-or-nothing. RED: no-drift
 applies; tag stale with anchor -> `MismatchError`; HEAD/TAIL stale -> warning.
7. (2nd pass) recovery 3-way-merge; (3rd) block ops with tree-sitter.

Each milestone closes with its table `TDD Cycle Evidence`.

## Sources

- Repo: https://github.com/can1357/oh-my-pi
- Tools: `packages/coding-agent/src/tools/{read,write,conflict-detect}.ts`
- Hashline engine: `packages/hashline/src/{format,types,parser,apply,patcher,mismatch,snapshots}.ts`
- README (tool design vision): https://github.com/can1357/oh-my-pi/blob/main/README.md
- Agent loop (where the tools are mounted): `agent-loop.md`
- Way of working: `AGENTS.md`
