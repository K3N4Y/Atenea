---
updated_at: 2026-07-09
summary: Specification for tool glob spec.
---

# Spec - Tool `glob` (opencode style file search)

Executable spec of the **tool `glob`**, based on the real implementation of
opencode (`packages/core/src/tool/glob.ts`, `filesystem.ts`, `ripgrep.ts` and
`filesystem/schema.ts`, investigated on 2026-06-22). It is not a numbered milestone of the
loop (`../plans/agent-loop-roadmap.md` closes on M10): it is the next
code navigation tool after `read`/`write`/`edit`.

`glob` does not read content and does not modify files. Its job is to find files
per pattern within the workspace so that the model can choose particular paths and
then use `read`. We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context

Athena already has:

- `internal/tool/registry.go`: tools contract (`Tool`, `Result`, `Settle`,
 `Permissions`) and `OutputStore`.
- `internal/tool/read.go`: read files under `Root`, use paths relative to the
 workspace and reject sandbox escapes.
- `internal/tool/write.go` and `internal/tool/edit.go`: share the same contract
 relative path and the same gate `sandboxJoin`.
- `app.go`: register `echo`, `read`, `write` and `edit`; `glob` is still pending at
 `internal/tool/doc.go`.

The production reference in opencode does three important things:

1. The tool exposes `pattern`, optional `path` and optional `limit`.
2. Run the search with ripgrep using `rg --files --glob=<pattern>` and exclude
 `.git` with `--glob=!**/.git/**`.
3. Convert the result to line-oriented text: one path per line, or `No files
   found` if there are no results.

There is a deliberate divergence for Atenea: opencode ends up showing
absolute paths to the model in `toModelOutput`; Athena must return routes **relative to the
workspace**, because `read`, `write` and `edit` accept relative paths and reject
absolute. If `glob` returned absolutes, it would push the model to call `read` with
a path that Athena will reject.

## 2. Objective

Leave the `glob` tool specified to find files under `Root` with a
ripgrep glob pattern, with compact and safe output:

In `internal/tool`:

- `glob.go`: `GlobTool`, `NewGlobTool`, `GlobSearcher`, `RipgrepGlobSearcher`,
 internal types `GlobSearch`, `GlobEntry`, `GlobSearchResult`, validation of
 input and formatting of the output.
- `glob_test.go`: tests of the tool against a fake searcher and the adapter ripgrep
 against a fake runner.
- `doc.go`: update the list of builtins so that `glob` is no longer listed as
 pending.

In `app.go`:

- build `tool.NewGlobTool(root)` with the same root as `read/write/edit`,
 register it in `NewRegistry(...)` and allow `"glob": true`.

This phase **does not** implement `grep`, `bash`, content search, fuzzy search,
directory listing, hidden file inclusion, symlink tracking, or
rich path pattern permissions.

## 3. Scope

### Inside

- Input JSON:
 - `pattern` (string, required): ripgrep glob pattern.
 - `path` (string, optional): relative directory to search from; default
    `"."`.
- `limit` (positive int, optional): maximum results to be returned.
- Resolution of `path` under `Root` with `sandboxJoin`.
- In real FS, rejection of symlink/realpath outside of `Root` with
 `rejectRealPathOutside`.
- Execution with ripgrep:
 - `rg --no-config --files --glob=<pattern> --glob=!**/.git/** .`
 - cwd = `Root/path`.
 - without `--hidden` and without `--follow` in v1.
- Row normalization:
 - remove prefixes `./`, `/` and `\`.
 - convert `\` to `/`.
 - convert each result relative to the cwd into a path relative to the workspace.
 - fail closed if any resolved row escapes `Root`.
- Output:
 - zero results -> `No files found`.
 - results -> one relative path per line.
 - if any more results than `limit` -> attach notice in-band.
- Tests with fake searcher/runner, without depending on having `rg` installed.
- Wiring in `app.go` and `"glob": true` permission.

### Out

- `grep`/content search by regex.
- `find` fuzzy by name.
- Directory listing as a separate tool.
- Support for hidden files (`--hidden`) or following symlinks (`--follow`).
- Multiple include/exclude patterns.
- MIME types in the output of the model. Opencode uses `FileSystem.Entry{path,type,mime}`;
 Atenea v1 only needs routes because the contract `Tool.Result` is text.
- Rich permissions (`ask`, allow by pattern, resource audit). Follows the set of
 names from M4: `"glob": true`.
- Embedded ripgrep dependency. v1 uses the `rg` binary available on the system;
 if it does not exist, returns actionable error.

## 4. Contract visible tool

### 4.1 Name and description

```go
func (*GlobTool) Name() string { return "glob" }

func (*GlobTool) Description() string {
	return "Encuentra archivos por patron glob dentro del workspace. Devuelve rutas relativas, una por linea; usa path para acotar el directorio y limit para acotar resultados."
}
```

### 4.2 Schema

The schema must maintain the opencode form (`pattern`, `path`, `limit`) and be
compatible with the current `llm.ToolDef`:

```json
{
  "type": "object",
  "properties": {
    "pattern": {
      "type": "string",
      "description": "Patron glob para encontrar archivos, con semantica de ripgrep (por ejemplo \"*.go\", \"**/*.go\" o \"internal/**/*.go\")."
    },
    "path": {
      "type": "string",
      "description": "Directorio relativo al workspace donde buscar. Default: \".\"."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "description": "Maximo de resultados a devolver."
    }
  },
  "required": ["pattern"]
}
```

### 4.3 Output

No results:

```text
No files found
```

With results:

```text
app.go
internal/tool/read.go
internal/tool/edit.go
```

With `path: "internal"` the output is still relative to the workspace, not relative
to the search cwd:

```text
internal/tool/read.go
internal/tool/edit.go
```

If the limit is exceeded:

```text
internal/tool/edit.go
internal/tool/read.go

[Limit reached: showing first 2 files. Use a narrower pattern or higher limit.]
```

The limit notice is an improvement by Atenea over the opencode leaf: the opencode adapter
 detects truncation when reading `limit + 1`, but `glob.ts` discards that flag
when converting to text. Atenea keeps the warning because the model only sees the string
of `Tool.Result` and `OutputStore` cuts by bytes without search semantics.

## 5. Types and internal contract

### 5.1 `internal/tool/glob.go`

```go
type GlobTool struct {
	Root          string
	Searcher      GlobSearcher
	DefaultLimit  int
	MaxLimit      int
}

type GlobSearch struct {
	Cwd     string // ruta absoluta desde la que se ejecuta la busqueda
	Pattern string
	Limit   int
}

type GlobEntry struct {
	Path string // relativo a Cwd, normalizado con slash
}

type GlobSearchResult struct {
	Entries   []GlobEntry
	Truncated bool
}

type GlobSearcher interface {
	Glob(ctx context.Context, input GlobSearch) (GlobSearchResult, error)
}
```

Suggested constants:

```go
const (
	defaultGlobLimit = 200
	maxGlobLimit     = 5000
)
```

`DefaultLimit` prevents `pattern: "**/*"` from listing an entire repository when the
model omits `limit`. `MaxLimit` prevents a poorly chosen input from forcing a giant output
; `OutputStore` remains the last defense by bytes.

### 5.2 `RipgrepGlobSearcher`

```go
type RipgrepGlobSearcher struct {
	Binary string // default "rg"
	Runner lineRunner
}

type lineRunner interface {
	RunLines(ctx context.Context, cwd, binary string, args []string, limit int) (lines []string, truncated bool, err error)
}
```

The real runner uses `os/exec` with `exec.CommandContext`. You must read stdout by lines
and stop after `limit + 1` rows to know if you have truncated without loading the entire
output into memory. `stderr` is bounded (e.g. 8 KiB) for actionable
errors, just as opencode bounds `ERROR_BYTES`.

Production args:

```text
--no-config
--files
--glob=<pattern>
--glob=!**/.git/**
.
```

Decisions:

- Do not pass `--hidden` in v1: same as the opencode `glob` tool, which only has
 `pattern/path/limit` and does not expose `hidden`.
- Do not pass `--follow` in v1: avoids following symlinks outside the workspace.
- Exit code `0`: normal results.
- Exit code `1`: no results -> empty list.
- Exit code `2` or higher: tool error with bounded stderr. Opencode
 allows some partials with code 2; Atenea v1 fails closed to not show
 incomplete results as if they were complete.

### 5.3 Flow of `Execute`

1. `json.Unmarshal` of the input.
2. Validate:
 - `pattern` not empty.
 - `limit` absent -> `DefaultLimit`.
 - `limit <= 0` -> tool error.
 - `limit > MaxLimit` -> tool error with message naming the maximum.
 - `path` absent or empty -> `"."`.
3. Solve `path` with `sandboxJoin(gt.Root, path, "glob")`.
4. If `Searcher` is the real one (`RipgrepGlobSearcher`), run
 `rejectRealPathOutside(gt.Root, cwd, path, "glob")` before searching. If path
 does not exist, the searcher will return actionable error.
5. Call `Searcher.Glob(ctx, GlobSearch{Cwd: cwd, Pattern: pattern, Limit: limit})`.
6. Convert each `GlobEntry.Path` (relative to `cwd`) to path relative to `Root`:
 - clean up prefixes `./`, `/`, `\`.
 - `filepath.Join(cwd, entry.Path)`.
 - check `insideRoot(rootAbs, absEntry)`.
 - `filepath.Rel(rootAbs, absEntry)`.
 - `filepath.ToSlash`.
7. If any row escapes `Root`, return error and do not emit partial output.
8. Format:
 - no entries -> `No files found`.
 - entries -> join with `\n`.
 - if `Truncated` -> empty line + limit notice.
9. Return `tool.Result{Output: text}`. `Result.Truncated` is decided by the
 `OutputStore`, not the tool.

## 6. TDD Plan

It is attacked from the outside in: first the behavior seen by the model
(`GlobTool` with fake searcher), then the adapter `RipgrepGlobSearcher` with runner
fake, and finally wiring.

### Safety net

- Base state before touching code: `go test ./...`, `go vet ./...`,
 `gofmt -l .`.
- If it fails before editing, report it as pre-existing and do not follow blindly.

### Understand

- Read this spec.
- Read `internal/tool/registry.go`, `output.go`, `path.go`, `read.go`, `write.go`,
 `edit.go`, `doc.go` and `app.go`.
- Read opencode references:
 - `packages/core/src/tool/glob.ts`
 - `packages/core/src/filesystem.ts`
 - `packages/core/src/ripgrep.ts`
 - `packages/core/src/filesystem/schema.ts`
- Expected behavior: search for `rg --files --glob`, optional path as
 cwd relative, positive limit, `.git` excluded, compact output of paths
 relative to workspace.

### NETWORK

Write these tests first. They must fail because `GlobTool`/`GlobSearcher` do not yet
exist or because the behavior is not yet implemented.

1. `TestGlobTool_FindsFilesByPattern`: fake searcher returns
 `{"app.go","internal/tool/read.go"}` -> output with those two routes, one per
 line.
2. `TestGlobTool_NoFilesFound`: fake searcher returns zero entries -> `No files
   found`.
3. `TestGlobTool_PathNarrowsSearchAndOutputsWorkspaceRelativePaths`: input
 `{pattern:"*.go", path:"internal"}` causes the fake to receive `Cwd == "/work/internal"`
 and an input `tool/read.go` to be output as `internal/tool/read.go`.
4. `TestGlobTool_LimitBoundsOutputAndShowsNotice`: input `limit:2`, fake returns
 `Truncated:true` with two inputs -> output of two routes + limit notice.
5. `TestRipgrepGlobSearcher_UsesProductionRipgrepArgs`: runner fake check args
 `--no-config --files --glob=*.go --glob=!**/.git/** .`.

Commands:

```bash
go test -run TestGlobTool ./internal/tool
go test -run TestRipgrepGlobSearcher ./internal/tool
```

Expected result in RED: does not compile (`undefined: GlobTool`) or fails the assert of
behavior.

### GREEN

- Add `internal/tool/glob.go` with:
 - `GlobTool`.
 - `NewGlobTool(root string) *GlobTool`.
 - `GlobSearcher` and internal types.
 - `formatGlobOutput`.
 - minimal implementation of `Execute` with fake searcher.
 - Add `RipgrepGlobSearcher` with runner injectable.
- Run only the red tests until they pass:

```bash
go test -run TestGlobTool ./internal/tool
go test -run TestRipgrepGlobSearcher ./internal/tool
```

### TRIANGULATE

Add cases that prevent false green:

- `TestGlobTool_DefaultLimit`: without `limit`, the fake receives `defaultGlobLimit`.
- `TestGlobTool_RejectsInvalidLimit`: `limit:0` and `limit:-1` -> tool error and the
 fake is not called.
- `TestGlobTool_RejectsLimitAboveMax`: `limit:maxGlobLimit+1` -> tool error and
 the fake is not called calls.
- `TestGlobTool_RejectsEmptyPattern`: `pattern:""` -> tool error.
- `TestGlobTool_InvalidInputErrors`: malformed JSON input -> tool error.
- `TestGlobTool_RejectsPathOutsideRoot`: `path:"../secret"` -> error before
 calling fake.
- `TestGlobTool_RejectsSearcherRowsOutsideRoot`: fake returns `../secret.txt` ->
 error fail-closed, no partial output.
- `TestGlobTool_NormalizesWindowsSeparators`: fake returns `tool\\read.go` under
 `path:"internal"` -> output `internal/tool/read.go`.
- `TestGlobTool_SearchErrorBecomesToolError`: fake returns error -> `Execute`
 returns actionable error including `glob`.
- `TestGlobTool_RejectsSymlinkSearchRootOutsideWorkspace`: with real FS, `path`
 points to a symlink inside `Root` that resolves out -> error before
 run ripgrep.
- `TestRipgrepGlobSearcher_EmptyExitCodeOneIsNoFiles`: runner fake simulates code 1
 -> empty list, no error.
- `TestRipgrepGlobSearcher_NonzeroFailureIncludesBoundedStderr`: runner fake simulates
 error -> actionable error, bounded stderr.
- `TestRipgrepGlobSearcher_StripsDotSlashAndBackslashes`: stdout `./a\\b.go` ->
 `GlobEntry{Path:"a/b.go"}`.
- `TestGlobTool_ContextCancellationPropagates`: fake observes `ctx.Done()` and
 returns `context.Canceled`; `Execute` propagates the error.

Command:

```bash
go test -run 'TestGlobTool|TestRipgrepGlobSearcher' ./internal/tool
```

### REFACTOR + wiring

- Extract test helpers:
 - `globInput(t, pattern, path string, limit *int) json.RawMessage`.
 - `fakeGlobSearcher` which registers `GlobSearch`.
 - `fakeLineRunner` which registers cwd/binary/args.
 - Keep `glob.go` simple. If the ripgrep adapter gets too big, move it to
 `ripgrep_glob.go`, but not before you need it.
- Update `internal/tool/doc.go`: `glob` no longer pending.
- Update `app.go`:
 - `tool.NewGlobTool(root)` in `NewRegistry`.
 - permission `"glob": true`.
- Closing gates:

```bash
gofmt -l .
go vet ./...
go test ./...
```

## 7. Acceptance criteria (Done when)

1. `tool.GlobTool` exists and implements `Tool`.
2. `Schema()` announces `pattern` required, `path` optional, and `limit` optional
 positive.
3. `Execute` validates JSON, `pattern`, `limit` and `path`; errors are actionable and
 do not call the searcher when the input is invalid.
4. `path` resolves within `Root` and rejects `..`/absolutes/symlinks outside
 workspace before searching actual FS.
5. `RipgrepGlobSearcher` uses the opencode args: `--no-config --files
   --glob=<pattern> --glob=!**/.git/** .`.
6. `--hidden` and `--follow` are not passed in v1.
7. The output of the tool is `No files found` or paths relative to the workspace, one per
 line, normalized with `/`.
8. `path` narrows the search but does not change the basis of the output: with `path:"internal"`
 `internal/...` is output.
9. `limit` delimits results; If there are more results, notice in-band is added.
10. A row that would escape `Root` is rejected fail-closed, without partial output.
11. `glob` is registered in `app.go` with `"glob": true` and appears in
    `Definitions` materializadas.
12. `go test ./...` passes; `go vet ./...` clean; `gofmt -l .` empty.

## 8. Commands

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Ciclo especifico
go test -run TestGlobTool ./internal/tool
go test -run TestRipgrepGlobSearcher ./internal/tool
go test -run 'TestGlobTool|TestRipgrepGlobSearcher' ./internal/tool

# Gates de cierre
gofmt -l .
go vet ./...
go test ./...
```

If you want to do a manual test with the real binary:

```bash
rg --no-config --files --glob='*.go' --glob='!**/.git/**' .
```

## 9. Table of expected evidence

When closing the phase, the response/PR must include this table with actual results:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Green suite before editing | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Specs and opencode references read | `glob.ts`, `ripgrep.ts`, `filesystem.ts`, `internal/tool/{registry,path,read,write,edit}.go` | identified behavior |
| NETWORK | Tool and adapter tests written first | `internal/tool/glob_test.go` + `go test -run TestGlobTool ./internal/tool` | expected failure |
| GREEN | `GlobTool` + `RipgrepGlobSearcher` minimums | `internal/tool/glob.go` + specific tests | specific tests pass |
| TRIANGULATE | Limits, sandbox, relative path, normalization, errors, truncation, cancellation | `go test -run 'TestGlobTool|TestRipgrepGlobSearcher' ./internal/tool` | cases happen |
| REFACTOR | Test, doc.go and wiring helpers on app.go | `gofmt -l .`, `go vet ./...`, `go test ./...` | green suite, registered `glob` |

## 10. Risks and decisions

- **Ripgrep as a production reference.** The core behavior of
 opencode is copied: `rg --files` + `--glob=<pattern>` + exclusion of `.git`. This avoids
 reimplementing `**` incorrectly, braces, ignore files and matching details.
- **External dependency on `rg`.** v1 assumes `rg` is installed. If not,
 actionable error. Packaging a binary or doing pure fallback in Go is out;
 you don't pay that cost before you need it.
- **Relative paths in Atenea.** Opencode ends up showing absolutes to the model,
 but Atenea returns relatives because `read/edit/write` are designed on
 paths relative to the workspace. It is a necessary divergence for the tools
 to compose.
- **Default limit.** Opencode uses `Number.MAX_SAFE_INTEGER` when `limit` is missing.
 Atenea uses `defaultGlobLimit` to not accidentally enumerate an entire repo.
 The model can raise `limit` if it needs more, up to `maxGlobLimit`.
- **Notice of truncation.** Opencode detects truncation in its generic adapter but the
 leaf `glob.ts` does not expose it. Atenea exposes it in-band because the textual output is
 the only thing the model sees.
- **No hidden/follow in v1.** Less surface area and less risk of symlink escape.
 If later needed, add `hidden`/`follow` as explicit parameters with
 sandbox tests.
- **Code 2 of ripgrep.** Opencode can preserve partials in some code 2.
 Atenea v1 fails closed: better to ask for a valid pattern than to deliver a potentially incomplete
 list without context.
- **Do not use `filepath.Glob`.** It does not support the same language as ripgrep and would be a
 source of differences with production. If Go fallback is required, you should use a
 library with actual `**` and braces support, and cover it with the same tests.
- **OutputStore does not replace `limit`.** The `OutputStore` cuts by bytes after
 running the tool. `limit` avoids unnecessary work and keeps the output
 semantically readable.

## 11. Safe Trims for v1

- Kept:
 - `pattern/path/limit`.
 - ripgrep `--files --glob`.
 - exclusion of `.git`.
 - sandbox by `Root`.
 - output line-oriented.
 - Ignored:
 - hidden/follow.
 - MIME/type in output.
 - rich permissions.
 - fallback without `rg`.
 - search by content (`grep`).
 - extra sorting. The order of the searcher is preserved; ripgrep decides.

## 12. Sources

- Real opencode (verified 2026-06-22):
 - `packages/core/src/tool/glob.ts`: input `pattern/path/limit`, permission, call
    a `ripgrep.glob`, output `No files found` o una ruta por linea.
- `packages/core/src/filesystem.ts`: `GlobInput{pattern,path,limit}` and
    `FileSystem.Interface.glob`.
- `packages/core/src/ripgrep.ts`: args `--no-config --files --glob=<pattern>
    --glob=!**/.git/** .`, normalizacion de `./`, `/` y `\`, limite por stream.
- `packages/core/src/filesystem/schema.ts`: `Entry{path,type,mime}`.
- Atenea:
 - `internal/tool/registry.go`, `output.go`, `path.go`.
 - `internal/tool/read.go`, `write.go`, `edit.go`.
 - `app.go`.
 - Working method: `AGENTS.md`.
