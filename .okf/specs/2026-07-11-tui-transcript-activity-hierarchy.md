---
updated_at: 2026-08-01
summary: TUI transcript activity hierarchy, including live subagent tool batches and durable completed totals.
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
continuous visual column inset two spaces from the transcript edge:

```text
  ✓ bash     ls
  │ 18 matches
  ● grep     auth middleware
  ✓ edit     main.go  +14 -3
  │ -old line
  │ +new line
  ? bash     rm -rf /tmp/x (allow/deny)
  ✗ error    step failed
```

### Header grammar

`<marker> <name padded to 8 columns> <summary>`, built by `activityHeader`
(`internal/tui/view.go`). Names longer than 8 columns keep a single trailing
space (never truncated); a header without summary trims the padding so no
trailing spaces remain.

Status markers (one glyph after the two-space inset, whole line styled as ONE
segment so plain-text substrings stay assertable):

| Marker | Meaning                           | Style       |
| ------ | --------------------------------- | ----------- |
| `●`    | activity running                  | faint       |
| `✓`    | activity succeeded                | faint green |
| `✗`    | activity failed / hard step error | red         |
| `?`    | pending permission request        | bold yellow |

### Detail rail

Every detail line under a header opens with the rail `│ ` after the same
two-space inset (`activityRailPrefix`): output preview lines (up to 4), diff
lines (up to 16, `+`/`-` colored), the failure reason (`│ error: <msg>`), and
the truncation mark (`│ … +N lines`).

Bash output and errors are collapsed by default, while the header and command
remain visible. A left click anywhere on an individual settled Bash entry
toggles its ephemeral `entry.expanded` state through the same `entryLines`
mapping used by thought blocks. Expanded Bash entries reuse the capped,
terminal-sanitized detail rendering above. Pending permissions remain separate
visible entries, and non-Bash tools retain their existing detail behavior.

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
- Read (`tool == "read"`): renders `Reading <filename>` while running and
  `Read <filename>` on success, using only the basename and never showing the
  path, range selector, or output preview.
- Skill (`tool == "skill"`): same grammar with the skill name as summary
  (`✓ skill    demo`); no output/diff detail — the SKILL.md body that travels
  in the output is for the model, not the transcript.
 - Subagent task: the `task` tool renders as a regular activity row. While its child session is live, the TUI renders that child's latest tool-bearing turn immediately below it as indented `↳` activity rows. Calls from the same turn stay in provider call order and independently show running, success, or failure through the normal tool marker and header. Child rows show only the tool name and summarized parameters; their output, diffs, and failure details are intentionally omitted from the parent transcript. The first tool call in a later tool-bearing turn replaces the complete prior batch; a text-only turn does not erase it. When the parent task succeeds or fails, the live rows are replaced by one `↳ 0 tool calls`, `↳ 1 tool call`, or `↳ N tool calls` row.
 - The total counts every direct `Tool.Called` occurrence in that child, including repeats and a direct nested `task`, but not tools used inside its descendants. Concurrent children are anchored by the parent task call ID and their own session IDs, so colliding child call IDs cannot cross-settle. Live rows remain bus-only and ephemeral; only the integer total is stored on the durable parent settlement, so reopening restores the total without child names, inputs, outputs, or errors. Only the TUI opts into live child activity, and the Vue/Desktop transcript ignores the private total metadata. Nested permission requests still surface as their own `?` rows keyed by child `SessionID`.
  including repeats and a direct nested `task`, but not tools used inside its
  descendants. Concurrent children are anchored by the parent task call ID and
  their own session IDs, so colliding child call IDs cannot cross-settle. Live
  rows remain bus-only and ephemeral; only the integer total is stored on the
  durable parent settlement, so reopening restores the total without child
  names, inputs, outputs, or errors. Only the TUI opts into live child activity,
  and the Vue/Desktop transcript ignores the private total metadata. Nested
  permission requests still surface as their own `?` rows keyed by child
  `SessionID`.
- Permission (`entryPermission`): `? <tool> <summarized input> (allow/deny)`.
- Plan approval (`entryPlanApproval`): the same `?` grammar with `plan` as the
  name and `presented` as the summary, plus the resolving suffix
  (`? plan     presented (y run / n stay in plan)`). It is a decision
  gate like a permission, so it keeps its own paragraph and is not part of
  compact grouping.
- Step error (`entryError`): `✗ error    <message>`.
- Compaction (`entryCompaction`): unchanged (`[context]`/`[error]` status
  lines); it is a transient state, not an activity row.

User-facing strings are English (`(allow/deny)`, `presented`, and the plan
suffix) to keep the TUI consistent. The truncation marker was migrated to
`lines` here as the first step, aligned with the English documentation
convention.

### Deferred

- A distinct "final summary" presentation: no data model distinguishes the
  closing assistant message; narrative markdown already stands apart from the
  rail. Same deferral as the reverted desktop plan.

## Tests

`internal/tui/model_test.go`, `internal/tui/transcript_test.go`, and
`internal/tui/subagent_totals_test.go` cover the rendered and pure-projection
contracts: lifecycle markers, compact grouping, diff stats, permission/error
joins, click-target parity, ordered parallel child calls, out-of-order
settlement, latest-batch replacement, colliding child call IDs, spinner state,
0/1/N totals on success and failure, live-to-final replacement, and SQLite
close/reopen restoration without child details. Runner tests cover denied,
final-turn, unresolved, provider-executed, and interrupted task settlements.
