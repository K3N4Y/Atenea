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
