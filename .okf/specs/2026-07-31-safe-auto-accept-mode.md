---
updated_at: 2026-07-31
summary: Session-local auto-accept mode for proven-safe workspace edits.
---

# Safe auto-accept permission mode

`/mode:auto-accept` enables safe auto-accept for the current session and process
sitting. `/mode:ask` disables it and `/mode` reports it. Both hosts handle these
exact commands locally, outside the durable conversation log. The mode is
independent of plan mode, survives rewiring, does not transfer to subagents, and
is disabled after process restart.

Selecting any of the three commands in either host's autocomplete and pressing
Enter executes it immediately on that Enter, clears the composer, and shows the
resulting status. Tab and selection behavior for all other commands is unchanged.

The decorator preserves base `Allow` and `Deny`, upgrading only `Ask` for the
registered `write` and `edit` tools or a proven-safe `bash` call. MCP calls keep
their base decision.

The positive shell grammar accepts only `mkdir -p`, `touch`, single-file `cp`
and `mv`, non-recursive `rm -f`, `rmdir`, and one `sed` substitution (optionally
`-i`) over explicit files. It rejects control syntax, expansion, redirection,
environment assignments, wrappers, qualified executables, traversal, absolute
paths, symlink components, unknown flags, recursive deletion, and ambiguity.
Tilde, glob, bracket, brace, comment, and backslash syntax is rejected globally,
including when quoted, so the classifier's argv cannot be expanded differently
when `bash -c` executes it.

Existing mutable leaves (`touch`, an existing `cp` destination, `sed -i`, and
the dedicated edit tool) must be regular files with exactly one hard link.
Platforms where the link count cannot be inspected fail closed and ask. The
dedicated write tool remains new-file-only and is auto-accepted only after
proving that its leaf does not exist.

Filesystem classification occurs immediately before the permission decision.
Bash executes later, so another local process can race by replacing a path. This
is not an OS filesystem sandbox; adversarial-process isolation still requires
ask mode.
