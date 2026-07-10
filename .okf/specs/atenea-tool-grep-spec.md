---
updated_at: 2026-07-09
summary: Specification for tool grep spec.
---

# Spec - Tool `grep` (ripgrep, content search)

Executable spec of the **tool `grep`** opencode style, adapted to the Atenea
hashline mechanism. It is not a generic reimplementation of `grep`: it is a
quick content search tool that uses `rg --json`, returns matching paths + lines
 and, in Atenea, records snapshots so that a later `edit` can
pin against the lines that the model has just seen.

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

The registry already has real tools (`read`, `write`, `edit`) and they all share
`Root` + `SnapshotProvider` per session. `read` and `write` record snapshots; `edit`
consumes those snapshots to validate hashlines and reject edits on lines not
seen. The content search tool is missing: the agent today can read a
file if it already knows what it is, but cannot discover where a symbol,
message, type, call or test lives.

opencode uses a production `grep` tool with this contract:

- parameters: required `pattern`, optional `path`, optional `include`; if there are no matches; if there is, "Found N matches" and groups
 per file with numbered lines; If it reaches the limit, it warns that there are more matches.

Athena copies that surface because it is simple and proven, but changes one thing
deliberately: each file group is printed with header hashline
`[path#HASH]` and lines `NUM:TEXTO`, just like `read`. So `grep` not only helps
find code; It also enables a secure `edit` on the lines you showed.

## 2. Objective

Leave the `grep` tool specified so that a subsequent implementation can
land it with TDD without reinterpreting the contract.

In `internal/tool`:

- `grep.go`: `GrepTool` (implements `Tool`). Parse `{pattern,path,include}`,
 resolves `path` into `Root`, calls a `Searcher`, groups matches by
 file, reads the files with matches to record full snapshots, marks
 as seen the output lines and returns output hashline.
- `ripgrep.go`: `RgSearcher`, `GrepRequest`, `GrepMatch`, `GrepResult`,
 `ParseRipgrepJSON` and typed errors (`GrepInvalidPatternError`, rg error not
 available). The actual searcher runs `rg`; the tests inject a fake.
- `grep_test.go` and `ripgrep_test.go`: tool behavior, sandbox, output,
 snapshot, truncation, JSON parsing and errors.
- `app.go`: register `grep` with the same `Root` and `SnapshotProvider` of
 `read/write/edit`; allow `"grep": true`.
- `internal/tool/doc.go`: move `grep` from pending to actual builtins.

This phase does not implement `glob`, `bash`, analytical counts, highlights of
submatches, context around each match, mass replacement, nor bundle of
binary `rg` within Wails.

## 3. Scope

### Inside

- Opencode compatible schema:

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
 - `json.Unmarshal` of input;
 - `pattern` required and not empty;
 - `path` default `"."`;
 - `sandboxJoin(Root, path, "grep")` and, with real FS, symlink rejection outside
    `Root` antes de ejecutar `rg`;
- call to `Searcher.Grep`;
 - reading of each file with matches to normalize, hash and record snapshot;
 - output grouped by file with header hashline + numbered lines;
 - `RecordSeenLines` with exactly the lines emitted.

- `RgSearcher`:
 - execute `rg` with `exec.CommandContext`;
 - args v1:

```text
rg --no-config --json --hidden --no-messages \
  [--glob=<include>] --glob=!**/.git/** -- <pattern> <path>
```

- read stdout in streaming and parse only JSON records with `type == "match"`;
 - normalize paths to relative ones with `/`;
 - truncate long line text to 2000 runes to avoid inflating the output;
 - kill the process when viewing `limit + 1` matches to see if there are more, and return
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

- No matches:

```text
No files found
```

- Truncated:

```text
Found 100 matches (more matches available)
...

(Results truncated. Consider using a more specific path or pattern.)
```

### Out

- `glob`: different tool to list files by pattern.
- `bash`: for counts, pipes or complex exploratory searches.
- Exact count of all matches. Like opencode, `grep` is for finding
 files/lines, not for analytics; If the user needs to count matches in
 the entire repo, the correct subsequent tool will be `bash` with `rg`.
- Context before/after each match. If the model needs more context, use
 `read path:N-M`.
- Submatch highlights. `rg --json` brings them; v1 ignores them in output to keep it compact. v1 uses `rg` in `PATH`; if it does not exist,
 returns an actionable error.
- Persistent indexing or results cache.

## 4. Types and contract

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

`Searcher` is an injectable dependency to not require `rg` in the tests of the
tool:

```go
type Searcher interface {
	Grep(ctx context.Context, req GrepRequest) (GrepResult, error)
}
```

`GrepTool` does not trust the text returned by `rg` to generate the final output.
Uses the paths + line numbers from the searcher, then reads the entire file with
`FS.ReadFile`, normalizes the same as `read`, saves the full snapshot and renders
the lines from `hashline.SplitLines`. This avoids format drift between `grep`
and `read`, and ensures that the output hash is the same as what `edit` will verify.

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

Errors:

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

Semantics of exit codes:

- `0`: matches found, parse stdout;
- `1`: no matches, return `GrepResult{}`;
- `2` with stderr containing `regex parse error` or `error parsing regex`: return
 `GrepInvalidPatternError`;
- any other error/exit code: tool error actionable with bounded stderr.

### 4.3 Output hashline format

For each file with at least one line emitted:

1. read entire file;
2. remove initial BOM, normalize CRLF/CR to LF;
3. `tag := snaps.Record(abs, normalized)`;
4. issue `hashline.FormatHeader(displayPath, tag)`;
5. cast the match lines as `NUM:TEXTO`;
6. dial `RecordSeenLines(abs, tag, emittedLines)`.

If a file appears with multiple matches on the same line, the line is output
once and marked once. The lines are ordered by ascending number within
each file. The files are ordered by the first occurrence reported by `rg`
 to respect local relevance of the engine.

## 5. Semantics of `Execute`

1. **Parse input.** `json.Unmarshal` from `{pattern,path,include}`. Invalid input
 -> `grep: input invalido`.
2. **Validation.** `pattern == ""` -> `grep: pattern requerido`. `path == ""` ->
 `"."`. `include == ""` is ignored.
3. **Sandbox.** Resolve `path` with `sandboxJoin`. Reject absolute paths, `..`
 outside of `Root`, and symlinks outside of `Root` with real FS before running `rg`.
4. **Search.** `Searcher.Grep(ctx, GrepRequest{Root, Path, Pattern, Include,
   Limit})`. `Limit` default 100.
5. **No matches.** Return `Result{Output:"No files found"}`. Do not record snapshots.
6. **Group.** Dedupe by `(path,line)`, group by file, keep first
 appearance order by file and sort ascending lines within file.
7. **Read for snapshot.** For each file with matches:
 - resolve the relative path with `sandboxJoin`;
 - read with `FS.ReadFile`;
 - if the file now does not exist or cannot be read, fail with actionable error
     (resultado de `rg` stale; reintentar);
- if it contains NUL, skip the file with notice
     `[Cannot grep binary file <path>; content contains NUL bytes]` y no grabar
     snapshot.
8. **Render.** `Found N matches` (N = matches issued) + hashline groups.
9. **Truncated.** If `GrepResult.Truncated`, add `(more matches available)` to the
 header and final opencode notice. Do not set `Result.Truncated`: that flag
 is still reserved for `OutputStore` by bytes.
10. **Return.** `Result{Output: output}`.

## 6. TDD Plan

### Safety net

- Before implementing: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- If it fails due to previous changes, report it as pre-existing and do not continue editing
 code blindly.

### Understand

- Read this spec.
- Read `internal/tool/{read,write,edit,path,snapshots,registry}.go`.
- Read `internal/tool/hashline/{format,hash,snapshot}.go`.
- Check opencode references:
 - `packages/opencode/src/tool/grep.ts`;
 - `packages/core/src/ripgrep.ts`;
 - `packages/opencode/src/tool/grep.txt`.

Expected behavior: schema `pattern/path/include`, search with ripgrep JSON,
limit 100, sandbox within the workspace, output grouped by file and full
snapshots so that `edit` accepts the emitted lines.

### NETWORK

1. `TestGrepTool_SearchesPatternAndFormatsHashlineGroups`: with `Searcher` fake that
 returns matches in two files and `FS` fake with real content, `Execute`
 returns `Found 3 matches` + headers `[path#HASH]` + numbered lines. Fails
 because `GrepTool` does not exist.
2. `TestGrepTool_RecordsSnapshotsAndSeenLines`: after grep, `Head(abs)`
 exists for each file and `Seen` contains only the match lines.
3. `TestParseRipgrepJSON_MatchRecords`: parse stdout JSON with records `match` and
 discard records `begin/end/summary`. It fails because `ParseRipgrepJSON` does not exist.

Commands:

```bash
go test -run TestGrepTool ./internal/tool
go test -run TestParseRipgrepJSON ./internal/tool
```

### GREEN

- Implement the minimum:
 - structs `GrepRequest`, `GrepMatch`, `GrepResult`;
 - `GrepTool` with `Searcher` fake-compatible;
 - grouped hashline render;
 - `ParseRipgrepJSON` basic.
- Run only the red tests until green:

```bash
go test -run TestGrepTool_SearchesPatternAndFormatsHashlineGroups ./internal/tool
go test -run TestGrepTool_RecordsSnapshotsAndSeenLines ./internal/tool
go test -run TestParseRipgrepJSON_MatchRecords ./internal/tool
```

### TRIANGULATE

Cases that avoid false green:

- `TestGrepTool_DefaultPathAndIncludePassedToSearcher`: without `path`, request uses
 `"."`; with `include`, it passes it intact.
- `TestGrepTool_NoMatchesReturnsNoFilesFound`: exact output `"No files found"` and
 without snapshots.
- `TestGrepTool_TruncationNotice`: `Searcher` returns 100 matches + `Truncated`;
 output says `(more matches available)` and final notice.
- `TestGrepTool_DedupesSameLine`: two matches from the same file/line are output once
 once and `Seen` has one entry.
- `TestGrepTool_RejectsPathOutsideRoot`: `"../../etc/passwd"` fails without calling
 searcher.
- `TestGrepTool_RejectsSymlinkOutsideRoot`: with real FS/tempdir, symlink outside of
 root fails before `rg`.
- `TestGrepTool_InvalidInputErrors`: invalid JSON.
- `TestGrepTool_EmptyPatternErrors`: empty `pattern` does not run searcher.
- `TestGrepTool_ReadFailureAfterMatchErrors`: if `rg` found a file but the
 read for snapshot fails, actionable error.
- `TestGrepTool_BinaryMatchedFileReturnsNoticeWithoutSnapshot`: file with NUL
 produces notice and does not save snapshot.
- `TestParseRipgrepJSON_TruncatesAtLimitPlusOne`: with `limit=2` and 3 records,
 returns 2 matches + `Truncated=true`.
- `TestParseRipgrepJSON_NormalizesPaths`: `./foo\bar.go` -> `foo/bar.go`.
- `TestParseRipgrepJSON_LimitsLongLineText`: line > 2000 runes is cut with
 `...`.
- `TestRgSearcher_BuildsProductionArgs`: using a fake runner (or args helper),
 asserts `--no-config --json --hidden --no-messages --glob=<include>
  --glob=!**/.git/** -- <pattern> <path>`.
- `TestRgSearcher_StopsAfterLimitPlusOne`: with long stdout streaming, kills the
 process after `limit + 1` matches and flags `Truncated=true`.
- `TestRgSearcher_InvalidPatternError`: exit 2 + regex stderr -> type error.
- `TestRgSearcher_NoMatchesExitOne`: exit 1 -> empty result, no error.
- `TestRgSearcher_ContextCancellation`: `ctx` canceled cuts the process and returns
 context error.

Commands:

```bash
go test -run TestGrepTool ./internal/tool
go test -run TestParseRipgrepJSON ./internal/tool
go test -run TestRgSearcher ./internal/tool
```

### REFACTOR

- Extract test helpers:
 - `fakeSearcher`;
 - `spySearcher`;
 - `grepToolWithFiles(t, files, matches)`;
 - compact ripgrep JSON fixtures.
- Share content normalization with `read/write/edit` if real duplication appears; if not, leave helper private in `grep.go`.
- Update `internal/tool/doc.go`.
- Final wiring in `app.go`.

Gates:

```bash
gofmt -l .
go vet ./...
go test ./...
```

## 7. Acceptance criteria

1. `grep` appears in the materialized `Definitions` when `Permissions{"grep":
   true}`.
2. Exact Schema: `pattern` required; `path` and `include` optional.
3. `pattern` empty, invalid JSON input, absolute path or root escape fail
 before running the searcher.
4. `RgSearcher` uses ripgrep with production flags equivalent to opencode:
 `--no-config`, `--json`, `--hidden`, `--no-messages`, optional include,
 exclusion of `.git`, `--`, pattern, path.
5. Exit code 1 from `rg` -> `"No files found"` without error; regex invalidate -> error
 actionable typing; `rg` not available -> actionable error.
6. Output with matches starts with `Found N matches`; group by file; each group
 carries `[path#HASH]` and `NUM:TEXTO` lines.
7. For each issued file, `grep` records a snapshot of the entire file and marks
 exactly the issued lines as `Seen`, so that `edit` can use them.
8. Duplicates `(path,line)` are issued only once.
9. MaxMatches default 100; if there are more, output warns `(more matches available)` and
 adds final notice. Do not use `Result.Truncated` for this semantic limit.
10. Binaries detected by NUL do not record snapshots and issue notice; files that
    desaparecen entre `rg` y snapshot fallan con error claro.
11. `app.go` registers `grep` with the same `Root` and `SnapshotProvider` as the other
    file tools, y permisos `"grep": true`.
12. `go test ./...`, `go vet ./...`, `gofmt -l .` go to closing.

## 8. Commands

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

## 9. Table of expected evidence

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Green suite before implementing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Local contract + opencode grep read | `internal/tool/{read,edit,path,snapshots}.go`, opencode `grep.ts`, `ripgrep.ts`, `grep.txt` | identified behavior |
| NETWORK | Tool and parser tests written first | `grep_test.go`, `ripgrep_test.go` + `go test -run ...` | expected failure |
| GREEN | `GrepTool` + minimal ripgrep parser | `internal/tool/{grep,ripgrep}.go` | specific tests pass |
| TRIANGULATE | Sandbox, truncated, no matches, invalid regex, dedupe, snapshots, args rg | `go test -run 'TestGrepTool\|TestRgSearcher\|TestParseRipgrepJSON' ./internal/tool` | cases happen |
| REFACTOR | Helpers, doc.go, app.go wiring | `gofmt -l .`, `go vet ./...`, `go test ./...` | green suite, registered `grep` |

## 10. Risks and decisions

- **Hashline output although opencode does not use it.** opencode prints paths + lines
 with `Line N`. Athena needs `edit` to validate freshness and `seenLines`, so
 each file is output as a hashline section. It is a local adaptation, not an
 accidental deviation.
- **`grep` records complete snapshot.** Although it only shows match lines, the hash of the
 header is of the complete file. This maintains the same rule as `read`: the hash
 is the freshness gate of the live file.
- **Exact lines seen.** Only the emitted lines remain in `Seen`. If model
 wants to edit around the match, it should ask for `read path:N-M`. This avoids editing
 code you didn't see.
- **Not exact count.** The 100 limit and truncation warning make `grep` a
 discovery tool, not a metrics tool. To count, `bash` with `rg`
 will be the route.
- **`rg` in PATH in v1.** It is the cheapest and most testable route. Packaging binaries
 by platform in Wails is another phase, with installation/build tests.
- **No `Result.Truncated`.** Match truncation is in-band for the model; The
 flag `Result.Truncated` is reserved for `OutputStore`, the same as in `read`.
- **File order.** The first order of appearance of `rg` is maintained because
 usually reflects the path of the repo; within each file it is ordered by line
 so that the output is readable and stable.
- **`--hidden` with exclusion `.git`.** Opencode is copied: search in hidden files
 useful, but not within `.git`. `rg` still honors ignore files because `--no-ignore` is not used
. the final output is rendered from the file read for snapshot.
 Thus the hash, numbering and normalization are aligned with `read`.

## 11. Safe Trims for v1

- Maintained: `pattern/path/include`, `rg --json`, limit 100, sandbox, invalid
 regex error, output hashline, full snapshots and `Seen` per line emitted.
- Omitted: context, highlights, exact counts, mass replacement, cache,
 bundle of `rg`, search outside the workspace, permissions by pattern route.

## 12. Sources

- opencode `grep.ts` (verified 2026-06-22):
 `https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/grep.ts`
 - schema `pattern/path/include`, permission `grep`, path default, limit 100, output
    "No files found" / "Found N matches", grupos por archivo y notice de truncado.
- opencode `ripgrep.ts`:
 `https://github.com/anomalyco/opencode/blob/dev/packages/core/src/ripgrep.ts`
 - `GrepInput`, `RawMatch`, `InvalidPatternError`, `rg --no-config --json
    --hidden --no-messages --glob=!**/.git/** -- pattern file`, parse JSON y
    normalizacion de paths.
- opencode `grep.txt`:
 `https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/grep.txt`
 - description of use: fast regex search, `include`, paths + numbers of
    linea, y recomendacion de usar `rg` directo para conteos.
- Local Athena: `internal/tool/{read,write,edit,path,snapshots,registry}.go`,
 `internal/tool/hashline/*`, `atenea-tool-read-spec.md`,
 `atenea-tool-edit-spec.md`, `AGENTS.md`.
