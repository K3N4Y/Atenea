---
updated_at: 2026-08-03
summary: Architecture of snapshot-backed reads and the four-mode edit facade, including streaming and TUI projection.
---

# Read and edit tools

## Boundaries

`ReadTool` produces numbered text and records bounded, session-isolated
snapshots. `EditTool` is a turn-frozen facade over four implementations:
`hashline`, `apply_patch`, `patch`, and `replace`. Persisted provider settings and
environment overrides choose one strategy before definitions are sent to the
provider; the selected schema, wire name, description, grammar, executor,
previewer, and matcher therefore cannot diverge during a turn.

Hashline and apply-patch expose Lark custom formats. Patch and replace expose
strict JSON schemas. All modes return the same structured per-file result model,
which keeps runner persistence and TUI rendering strategy-agnostic.

## Snapshots and hashline

Read emits `[path#TAG]` plus `LINE:TEXT`. The four-hex tag hashes normalized text
and identifies an exact retained snapshot, not merely a claimed checksum.
Snapshot lookup fails closed on collisions and is bounded by path/version/byte
limits. Oversized content receives no unusable header. Seen-line provenance can
be enforced per edit configuration.

Hashline accepts multiple `[PATH#TAG]` sections. Its current grammar uses `PUT`
for range/block replacement, insertion, and register paste; `CUT` for capture
and deletion; and `REM`/`MV` for file operations. Named registers can cross
sections and calls. Parsing rejects malformed, conflicting, duplicate canonical,
or sandbox-escaping targets before unsafe effects.

The patcher normalizes for validation, applies against the named snapshot, and
may recover drift only when landing is exact and unambiguous. It never guesses.
Commit restores BOM/EOL/final-newline and supported mode conventions. OS-backed
commits reject aliases and non-regular files and use same-directory replacement.
Post-rename sync failure is reported as committed but durability-uncertain; the
caller must continue from the returned header or re-read, never retry blindly.

## Alternate strategies

- `apply_patch`: parses a multi-file Begin/End envelope with add, update, delete,
  and move sections. Validation occurs before ordered upstream-compatible
  application; partial success is explicit in ordered file results.
- `patch`: receives one path and JSON edit entries containing context hunks,
  optionally create/delete/move operations.
- `replace`: replaces a unique content occurrence, or all occurrences when
  requested, with optional configured fuzzy whitespace matching.

Every implementation shares workspace containment, path safety, permission,
result, and preview contracts rather than exposing its engine details upstream.

## Streaming and presentation

Provider `ToolInputDelta` fragments are accumulated by call ID in the runner.
The materialized registry's generic preview capability projects partial input
without effects. Hashline uses a cloned clipboard; no preview mutates disk,
snapshots, or live registers. Multi-file matcher path/digest entries remain
isolated. Duplicate digests are suppressed, cancellation is honored, and final
settlement replaces ephemeral state; previews cannot revive a settled call.

The runner persists structured results. The standalone TUI folds previews into
the matching running transcript entry and renders path, operation, line stats,
warnings, and before/after diffs. Resize and collapse/expand only change layout,
not transcript identity. One successful tool settlement causes one workspace
refresh even when its result contains multiple files.

## Adaptations and nonapplicability

The implementation is pinned and mapped in
[`docs/agents/oh-my-pi-edit-parity.md`](../../docs/agents/oh-my-pi-edit-parity.md).
Atenea replaces Bun hashing/runtime and upstream terminal code with Go and
Bubble Tea while retaining observable contracts. ACP transport, Bun-only
helpers, and notebook cell editing have no local subsystem and are deliberately
nonapplicable. They are not compatibility targets.
