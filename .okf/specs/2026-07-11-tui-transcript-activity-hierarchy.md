---
updated_at: 2026-07-11
summary: TUI transcript activity hierarchy — activity-rail formatting, compact grouping, diff stats, and pre-size canvas rendering.
status: implemented
---

# TUI transcript activity hierarchy

## Problem

The transcript blocks already differentiated kinds of content, but tool
activity read as loose, verbose paragraphs (`[tool] bash(ls): ok` headers
separated by blank lines). The transcript did not read as a scannable
operational story: which tools ran, which succeeded, which failed, what
changed on disk, and what still waits for permission.

A first attempt shipped in the desktop frontend (pull request #6) and was
reverted (#8): it targeted the wrong interface. This spec re-lands the same
taxonomy in the interface it was meant for, the TUI (`internal/tui`).

## Contract

Activity entries (tool calls, pending permissions, step errors) render with a
continuous visual column at column 0 of the transcript:

```text
✓ bash     ls
│ 18 matches
● grep     auth middleware
✓ edit     main.go  +14 -3
│ -old line
│ +new line
? bash     rm -rf /tmp/x (aprobar/denegar)
✗ error    step failed
```

### Header grammar

`<marker> <name padded to 8 columns> <summary>`, built by `activityHeader`
(`internal/tui/view.go`). Names longer than 8 columns keep a single trailing
space (never truncated); a header without summary trims the padding so no
trailing spaces remain.

Status markers (one glyph, column 0, whole line styled as ONE segment so
plain-text substrings stay assertable):

| Marker | Meaning | Style |
| --- | --- | --- |
| `●` | activity running | faint |
| `✓` | activity succeeded | faint green |
| `✗` | activity failed / hard step error | red |
| `?` | pending permission request | bold yellow |

### Detail rail

Every detail line under a header opens with the rail `│ ` at column 0
(`activityRailPrefix`): output preview lines (up to 4), diff lines (up to 16,
`+`/`-` colored), the failure reason (`│ error: <msg>`), and the truncation
mark (`│ … +N lines`).

### File-change stat

A successful edit/write (any success carrying a unified diff) appends
`  +N -M` to its header summary: `diffStat` counts diff lines starting with
`+`/`-`, excluding the `+++`/`---` file headers. Successes without a diff show
no stat (never `+0 -0`).

### Compact grouping

Adjacent activity entries join with a single `\n` (no blank line), forming one
contiguous activity block; any other neighborhood (assistant narrative,
thinking, user messages, compaction) keeps its own paragraph (`\n\n`). The
shared predicate `compactActivityJoin` drives both `renderTranscript` and
`entryLines`; they must never diverge because `entryLines` replicates the
viewport content line by line to map mouse clicks back to entries.

### Kind mapping

- Assistant narrative: markdown, no marker; breaks activity groups.
- Tool call (`entryTool`): header grammar above; success detail is the diff
  when present, otherwise the output preview.
- Skill (`tool == "skill"`): same grammar with the skill name as summary
  (`✓ skill    demo`); no output/diff detail — the SKILL.md body that travels
  in the output is for the model, not the transcript.
- Subagent task: the `task` tool renders as a regular activity row (its name
  column identifies it); nested permission requests from the child session
  surface as their own `?` rows keyed by the event `SessionID`.
- Permission (`entryPermission`): `? <tool> <summarized input> (aprobar/denegar)`.
- Plan approval (`entryPlanApproval`): the same `?` grammar with `plan` as the
  name and `presentado` as the summary, plus the resolving suffix
  (`? plan     presentado (y ejecutar / n seguir en plan)`). It is a decision
  gate like a permission, so it keeps its own paragraph and is not part of
  compact grouping.
- Step error (`entryError`): `✗ error    <message>`.
- Compaction (`entryCompaction`): unchanged (`[context]`/`[error]` status
  lines); it is a transient state, not an activity row.

User-facing strings stay Spanish (`(aprobar/denegar)`, `presentado`, the plan
suffix) to match the rest of the TUI; migrating them to English is a separate,
gradual effort. The truncation marker was migrated to `lines` here as the
first step, aligned with the English documentation convention.

### Deferred

- A distinct "final summary" presentation: no data model distinguishes the
  closing assistant message; narrative markdown already stands apart from the
  rail. Same deferral as the reverted desktop plan.
- Nesting child-session activity under its `task` row: child events carry the
  child `SessionID` but no parent call-id back-reference.

## Tests

`internal/tui/model_test.go` — substring/line assertions over `Model.View()`,
per repo convention: lifecycle markers, compact grouping, diff stat (table
test on `diffStat`), no-stat on diff-less success, long-name and no-summary
headers, permission/error joining groups, and click-targeting alignment with
compact groups (`entryLines` separator parity).
