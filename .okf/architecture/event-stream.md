---
updated_at: 2026-07-27
summary: Forward-compatibility rules for the durable session event taxonomy and its UI projections.
---

# Durable event stream

`agentcore/session.SessionEvent` is an open event stream contract. `EventKind`
is a string deliberately: newer producers may emit kinds an older consumer does
not know. Every projection must therefore preserve an unknown event as a generic
entry and issue a debug log; silently dropping it is not a valid fallback. The
desktop and TUI projections show the kind together with the most useful payload
available (`Text`, `Error`, message text, or tool input).

Core owns the existing title-cased taxonomy such as `Text.Delta` and
`Tool.Success`. Extension-emitted kinds use one of two reserved namespaces:

- `ext.<vendor>.<event>` for stable extension events.
- `x-<name>` for private or experimental events.

`session.IsExtensionEventKind` is the shared classifier and rejects the bare
prefixes. These namespaces prevent extensions from accidentally claiming a core
kind while keeping the stream open to independent evolution.

## Exhaustiveness policy

`EventKind` switches are intentionally **not** subject to an exhaustive-switch
linter. Exhaustiveness would make the constants a closed enum and would conflict
with both extension namespaces and the requirement that an older consumer accept
a kind introduced by a newer producer.

Forward compatibility is enforced at the projection boundary instead. The TUI
and desktop projection tests pass an event kind absent from the core constants
and require it to survive as a generic entry. Any new projection must provide
the same behavior: handle known kinds richly, preserve unknown kinds generically,
and log the fallback for diagnosis. The generated TypeScript taxonomy is useful
for known-kind editor support, but it must retain its open `string` arm; it is not
an exhaustive enum.
