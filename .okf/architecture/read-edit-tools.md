---
updated_at: 2026-08-01
summary: Design for hashline-based read and edit tools in Go.
---

# Tools `read` and `edit` oh-my-pi style (design for Go)

This implementation is pinned to upstream `can1357/oh-my-pi` commit
`09a7c865636457c50ed75fc3b1a7cc21ef72c105` (packages `coding-agent` and
`hashline`), adapted to Atenea's deliberately smaller public grammar.

The model-facing `read` and `write` definitions follow the upstream prompt
structure (`instruction`, `conditions`, and `critical`) and use English. They
advertise only Atenea's implemented subset: local text files, `:N`/`:N-M`
selectors, hashline output, and new-file-only writes. Upstream capabilities
such as directories, URLs, archives, SQLite, images, raw mode, and overwriting
existing files are intentionally not exposed.

It is the most difficult part of the agent, which is why it is written in detail: the
correctness of `edit` decides whether the agent corrupts files or not.

## The core idea: hashline

oh-my-pi does not edit by "find old text and replace with new" (fragile: the
model rewrites entire blocks or crashes because of a line of context). Nor does it edit
by line number simply (fragile: the file changes and the number points to something else
).

It uses a hybrid scheme called **hashline**:

- `read` numbers each line and prefixes a **header with a hash of the entire
 file**: `[path#HASH]`.
- `edit` addresses by **line number**, but is only valid if the file **still
 hashes to the same `HASH`**.
- If the file changed, the `HASH` diverges and the edit **fails for sure** (or recovers
 via a 3-way merge against the snapshot named by the hash). Never blindly apply a
 stale diff.

That is to say: the **anchor is the line number**, and the **hash of the full content is
the freshness gate**. That's all the magic.

### The hash (the most important thing to copy well)

From `packages/hashline/src/format.ts`:

```ts
// Normalize before hashing: remove trailing [ \t\r] from each line and the end.
function normalizeFileHashText(text) {
  return text.replace(/[ \t\r]+(?=\n|$)/g, "");
}
// Tag = 4 uppercase hex chars from the low 16 bits of xxHash32 over the normalized text.
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

Port to Go:

```go
// internal/tool/hashline/hash.go
var trailingWS = regexp.MustCompile(`[ \t\r]+(\n|$)`)

func normalizeForHash(text string) string {
    return trailingWS.ReplaceAllString(text, "$1")
}

// xxHash32 with seed 0; use, for example, github.com/pierrec/xxHash/xxHash32.
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
  path: "string", // e.g. "src/foo.ts", or with selector "src/foo.ts:50-100"
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

- First line: header `[path#HASH]`.
- Each content line: `NUM:TEXT` (number, colon, content).
- In range reads, the numbers reflect the actual position in the file.

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

In oh-my-pi, `write` has `{ path, content }`. If `content` starts with a
header `[path#HASH]` followed by hashline operations, it is treated as a **patch**;
otherwise, it is a **complete write** of the file. For Atenea, it is convenient to
separate it into an explicit `edit` tool, but the engine is the same.

### Operations

Atenea exposes exactly one non-empty `[path#HASH]` section per call and retains
its existing `SWAP`/`DEL`/`INS.*` grammar. Parsing is strict: extra headers,
orphan/unknown lines, malformed operations, empty required payloads, invalid
anchors/ranges, and conflicting ranges are rejected before filesystem access.

| Operation | Syntax | Effect |
| --- | --- | --- |
| Range replacement | `SWAP start.=end:` + `+...` payload | replace `[start,end]` |
| Delete | `DEL n` or `DEL start.=end` | delete line(s) |
| Insert before/after | `INS.PRE n:` / `INS.POST n:` + payload | anchored insert |
| Insert at boundaries | `INS.HEAD:` / `INS.TAIL:` + payload | boundary insert |

Payload lines are prefixed with `+`; `.=` is the only range separator.
### How to apply and verify (from `patcher.ts`)

The `Patcher` is **all-or-nothing**: it first `prepare` (preflight) all the
sections in memory, and only if they all pass, does it `commit` (write to disk). By
section:

1. Parse the operations; require that the section contain `HASH` (no tag -> error).
2. Read file, remove BOM, normalize to LF.
3. Calculate `live = ComputeFileHash(currentContent)`.
4. Compare it with the section's expected `HASH`:
 - **`live == expected` (no drift)**: the lines index the exact content.
     - Check `seenLines`: reject anchors on lines that `read` never showed.
     - Apply the operations (`applyEdits`).
- **Only HEAD/TAIL inserts** and stale tag: stable position, not fatal. Apply
     them to the live content and issue a warning.
- **Drift with concrete anchors**: try `recovery.tryRecover()` (3-way merge
     of the edit against the snapshot named by the tag, onto the live content). If
     it succeeds, apply the merge; otherwise, return **`MismatchError`** (re-read).
5. `commit`: restore line endings, BOM, and the complete file mode, then perform
   same-directory atomic replacement. Per canonical path, edit serializes the
   full read/validate/replace interval. A post-rename directory-sync failure is
   reported as committed-but-durability-uncertain with the actual new header;
   callers must not retry the old patch blindly. Finally record the new snapshot.

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
    Text      string          // full normalized text (LF, no BOM)
    Hash      string          // ComputeFileHash(Text)
    SeenLines map[int]struct{} // 1-indexed lines shown by a read/search
}

type SnapshotStore interface {
    Head(path string) *Snapshot
    ByHash(path, hash string) *Snapshot
    Record(path, fullText string) (hash string, recorded bool)
    RecordSeenLines(path, hash string, lines []int)
    Invalidate(path string)
}
```

- The implementation uses upstream-equivalent defaults: 30 paths, 4 versions
  per path, and 64 MiB total, with deterministic path LRU eviction.
- Exact text (not the 16-bit hash) controls read fusion. Ambiguous hash
  collisions fail closed both for lookup and seen-line provenance.
- A file too large to retain is readable only via an explicit non-editable
  notice: `read` never emits a hashline header that the bounded store discarded.
- Stores remain isolated per Atenea session.

## Design in Go for Atenea

Suggested package: `internal/tool/hashline` (pure engine, no FS or agent) + the
tools `read`/`edit` in `internal/tool` that use it.

```text
internal/tool/hashline/
  hash.go        // ComputeFileHash, normalizeForHash
  format.go      // [path#HASH] format, "NUM:TEXT", hunk headers
  types.go       // Anchor, Cursor, Edit, ApplyResult
  parser.go      // patch text -> []Edit  (SWAP/DEL/INS...)
  apply.go       // applyEdits(text, edits) -> ApplyResult
  patcher.go     // prepare/commit, hash verification, recovery
  snapshot.go    // SnapshotStore + in-memory implementation
  recovery.go    // 3-way merge against the tag's snapshot
internal/tool/
  read.go        // ReadTool: reads, numbers, formats, records snapshot
  edit.go        // EditTool: builds Patch, calls Patcher
```

Core types (mirror of `hashline/types.ts`):

```go
type Anchor struct{ Line int } // 1-indexed

type Edit struct {
    Kind   EditKind // Insert | Delete | Replace | Block
    Cursor Cursor   // for Insert: BOF/EOF/BeforeAnchor/AfterAnchor
    Anchor Anchor   // for Delete/Block
    Range  *Range   // for Replace (start.=end)
    Text   string   // payload (+ lines)
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
    // Optional BlockResolver (tree-sitter); nil in v1.
}

// All-or-nothing: prepare everything in memory, then commit.
func (p *Patcher) Apply(patch Patch) (PatchResult, error)
```

## Completed safety behavior and non-goals

- Anchored stale edits recover only when every original anchored region is
  unchanged, occurs exactly once in live text, and all regions share one line
  offset. Ambiguity, changed anchors, or non-uniform movement returns
  `MismatchError`; no fuzzy guesses are made. Stale HEAD/TAIL behavior remains.
- UTF-8 BOM, final newline, dominant/original EOL style, permissions, and
  supported special mode bits are restored. OS-backed edits preserve mode and
  commit through a same-directory temporary file, file sync, rename, and
  directory sync. Fake filesystems keep the compatibility `WriteFile` operation.
  Failed preflight performs no write.
- OS edits reject symlink components, final symlinks, hardlinks, and non-regular
  files before reading. Go's path-based APIs leave a documented residual race
  against a hostile process replacing directory components between checks.
- Snapshot history is bounded by eviction; oversized files receive no editable
  header rather than an immediately unusable one.
- Anchored inserts overlapping replaced/deleted ranges, duplicate HEAD/TAIL,
  and PRE/POST combinations sharing an anchor are rejected as ambiguous.
- Multi-file patches, alternate/dual grammars, block/tree-sitter operations,
  grammar migration, notebooks/LSP, folding, and rich selectors are non-goals.

1. `ComputeFileHash` + normalization. NETWORK: same text (CRLF/LF, trailing ws) ->
 same tag; real change -> different tag.
2. `format` + `read` numbering. RED: a file produces header + `NUM:TEXT`.
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
