---
updated_at: 2026-07-11
summary: Visual and interaction specification for a continuous, scannable operational activity column in the desktop transcript.
---

# Transcript Activity Hierarchy

## Problem

The transcript already renders assistant narrative, reasoning, tool calls,
results, failures, diffs, and permission requests, but operational events read
as unrelated blocks. A user scanning a long turn cannot quickly reconstruct
what the agent attempted, what target it acted on, or where the operation
finished.

The transcript needs a stronger hierarchy between conversational narrative and
operational activity without changing the durable event stream or hiding
important details.

## Goals

- Keep assistant narrative visually independent from operational activity.
- Render consecutive tool activity as one continuous vertical story.
- Distinguish active, successful, failed, and permission-pending operations at
  a glance.
- Show the operation verb, primary target, and a short result in the collapsed
  state.
- Collapse verbose output, errors, and diffs by default while preserving access
  to their full content.
- Preserve event order, keyboard interaction, screen-reader semantics, and the
  existing approval flow.
- Make the presentation model extensible to future subagent and final-summary
  rows without rendering a special final-summary category in this change.

## Non-goals

- Changing backend session events or persistence.
- Inferring or merging multiple tool calls into a synthetic operation.
- Adding a special final-summary block now.
- Adding subagent events that the current frontend store does not receive.
- Redesigning user messages, reasoning blocks, or the transcript scroller.

## Information hierarchy

The transcript has two visual levels:

1. **Narrative:** user, assistant, and reasoning content remain normal transcript
   blocks outside the activity rail.
2. **Operational activity:** consecutive `tool` items render inside a continuous
   vertical activity column.

Narrative interrupts the rail. A later sequence of tool items starts a new rail
instead of visually connecting across prose.

## Activity row anatomy

Each operational item has a compact summary row:

```text
● grep    auth middleware
│ 18 matches
● read    internal/auth.go
● edit    internal/auth.go       +14 -3
✓ test    go test ./internal/auth
```

The summary row contains:

- A rail marker that communicates status.
- A normalized, short action label derived from the tool name.
- The most useful target derived from structured input.
- An optional compact result derived from output or diff metadata.
- A disclosure control when full details exist.

Action and target use monospace typography. Secondary result text is quieter
than the action but remains readable without hover.

## Status language

| State | Marker | Meaning |
| --- | --- | --- |
| `running` | filled neutral dot with motion | Operation is active |
| `success` | check | Operation completed successfully |
| `failed` | cross | Operation failed |
| `pending` | diamond | Permission is required |

The vertical connector uses a low-contrast neutral color. Failure uses red only
for its marker and essential status text; it does not turn the whole row into a
heavy alert card. Permission uses an amber accent and keeps the existing approve
and deny actions visible in the collapsed row.

Future presentation variants reserve semantic markers for `subagent` and
`summary`. They are part of the presentation type vocabulary only; this change
does not synthesize either kind from current events.

## Tool summaries

Summary extraction is deterministic and presentation-only:

- `read`, `edit`, and `write`: show the normalized repository-relative path
  when available; do not reduce it to only the basename.
- `grep`: show the search pattern, followed by the searched path when it adds
  context.
- `glob`: show the glob pattern.
- `bash`: show the command, truncated to one visual line.
- `skill`: show the skill name.
- Unknown tools: show a compact serialization of the first useful scalar input;
  if none exists, show only the tool name.

The action label remains the actual tool name unless an existing product label
is clearer. Long targets truncate visually and expose their full value through
the native title/accessible label.

## Compact results

The collapsed row may show a result that can be calculated reliably:

- Unified diffs show `+N -N` from added and removed content lines.
- Grep output shows a match count when the output format permits an unambiguous
  count.
- Empty successful output shows no synthetic message.
- Failed operations show a short, single-line error excerpt.
- Other output remains available in details without guessing a summary.

Result extraction must never alter or discard the original output, error, or
diff.

## Disclosure behavior

Verbose tool details are collapsed by default for every settled operation.
Expanding a row reveals the existing rich content:

- `edit` and `write` reveal `DiffView` when a unified diff exists.
- Other successful tools reveal their complete output in a preformatted block.
- Failed tools reveal the complete error.
- Structured input may remain visible where the existing component already
  presents it usefully.

The summary row is a real button when expandable. It exposes `aria-expanded`
and an accessible name containing action, target, and status. Expanding one row
does not expand neighboring rows.

Permission-pending rows are not hidden behind disclosure: command context and
approve/deny controls remain immediately actionable.

## Grouping and layout

`MessageList` groups only adjacent `tool` items into an activity group. The
group owns the continuous connector, while each row owns its marker and detail
content. This prevents every tool component from guessing whether it is first
or last and leaves the flat `TurnItem[]` data model unchanged.

The rail must:

- Continue cleanly between adjacent activity rows.
- Stop at the center of the final row marker.
- Avoid a dangling connector above the first row or below the last row.
- Maintain alignment when a row is expanded.
- Remain readable at narrow desktop widths without horizontal scrolling.

## Extensibility

The visual layer defines an internal activity presentation kind capable of
representing:

- `tool`
- `permission`
- `file-change`
- `subagent`
- `summary`

Current `ToolItem` values map to tool, permission, and file-change
presentations based on their status and tool name. The store's public
`TurnItem` union does not gain unused event types. When durable subagent or
summary events arrive later, they can map into the same rail primitives without
redesigning layout or status semantics.

## Accessibility

- Preserve the transcript's `role="log"` and `aria-live="polite"` behavior.
- Do not communicate state by color alone.
- Keep disclosure and permission actions keyboard reachable.
- Ensure decorative rail connectors are hidden from assistive technology.
- Announce tool status with text in the disclosure label or row description.
- Preserve readable focus indicators within the dark visual system.

## Acceptance scenarios

1. Consecutive running and successful tools share one continuous rail and retain
   their original order.
2. Assistant narrative between tools splits them into separate activity groups.
3. Successful output is collapsed initially and expands independently on click.
4. A diff is collapsed initially, shows `+N -N`, and reveals `DiffView` when
   expanded.
5. A failed tool shows a red cross and short error excerpt, with the complete
   error collapsed initially.
6. A pending command shows a permission marker, command context, and working
   approve/deny actions without requiring expansion.
7. Read, grep, glob, bash, skill, edit, write, and unknown tools produce stable,
   useful summaries from their current inputs.
8. Existing scrolling, live-region, and new-activity behavior remain unchanged.
9. The presentation vocabulary contains a future `summary` variant, but no
   transcript item is detected or rendered as a final summary in this release.
