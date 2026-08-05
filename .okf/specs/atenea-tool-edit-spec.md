---
updated_at: 2026-08-03
summary: Executable contract for Atenea's four-mode edit facade, structured results, streaming previews, and TUI settlement.
---

# Edit tool specification

## Public facade

One turn-frozen `EditTool` exposes exactly one of four strategies selected from
`providers.json` (`edit.mode`, optional model variants) or `PI_EDIT_VARIANT`:

| Mode | Wire name | Input | Purpose |
| --- | --- | --- | --- |
| `hashline` | `edit` | `{input}` custom grammar | Snapshot-addressed multi-file editing with modern `PUT`/`CUT`, registers, block locators, `REM`, and `MV`. |
| `apply_patch` | `apply_patch` | `{input}` custom grammar | Multi-file `*** Begin Patch` add/update/delete/move envelope. |
| `patch` | `edit` | `{path, edits}` | JSON create/update/delete/move entries using context hunks. |
| `replace` | `edit` | `{path, old_string, new_string, replace_all?}` | Exact or configured fuzzy content replacement. |

Definitions, schemas, descriptions, and custom Lark grammars are frozen with the
turn. A configuration change affects the next turn, never an in-flight call.

## Hashline contract

Every section starts `[PATH#TAG]`, where `TAG` names a bounded,
session-isolated snapshot produced by read/search/edit. Modern operations are
`PUT N.=M:`, `PUT N*:`, gap `PUT`, bodyless register `PUT`, `CUT`, `REM`, and
`MV`. Body rows begin with `+`. Multiple sections are supported and named
registers can carry text between sections and calls.

The engine verifies provenance, seen lines when configured, live freshness,
canonical target uniqueness, and workspace containment. Recognized drift may be
recovered only by exact, unambiguous landing; unsafe or ambiguous recovery fails
with an actionable re-read instruction. Commits preserve supported BOM/EOL,
final-newline, and mode conventions. The OS implementation rejects mutable
aliases and uses same-directory replacement. A post-commit durability error is
reported as committed and must not be retried.

## Other modes

`apply_patch` parses the advertised envelope and applies entries in order after
global validation; upstream-compatible partial success is represented per file.
`patch` applies one or more context hunks to its top-level path. `replace`
requires a unique match unless `replace_all` is true. All modes enforce the
workspace sandbox and return structured file results.

## Results, previews, and TUI

Every changed target reports path/source/destination, operation, old/new text,
unified diff, first changed line, warnings, committed state, and an actionable
error when applicable. Multi-file partial failures preserve ordered committed,
failed, and skipped results.

Provider input deltas flow through the runner's generic preview capability.
Preview evaluation is pure: it does not write disk, snapshots, or hashline
registers. Matcher path/digest state is isolated per file. Duplicate preview
digests are ignored; cancellation stops projection; final settlement replaces
ephemeral files and prevents late previews from overwriting it. The TUI renders
path, operation, stats/warnings, and diff cards, remains stable across resize and
collapse/expand, and requests one workspace refresh for one successful tool
settlement regardless of file count.

## Acceptance gates

- Representative examples from every description and both custom grammars
  execute through public `EditTool` on temporary workspaces.
- Provider/runner tests cover all modes, permissions, structured settlement,
  multi-file streaming, cancellation, and stale preview ordering.
- Transcript/model/view tests cover all modes, final replacement, stable layout,
  and one refresh per successful settlement.
- A production-tag binary runs under a PTY with isolated HOME/config/database/
  checkpoints/workspace against a local OpenAI-compatible server, performs read
  then edit, accepts the real permission UI, renders the change, and lands exact
  bytes.

## Local adaptations and nonapplicability

Atenea uses Go filesystem and hashing implementations rather than Bun APIs, and
Bubble Tea rather than upstream terminal components. Bun runtime helpers, ACP
transport, and notebook cell editing have no corresponding local subsystem and
are not part of this contract. This is deliberate nonapplicability, not deferred
compatibility work. See `../../docs/agents/oh-my-pi-edit-parity.md` for pinned
source and test provenance.
