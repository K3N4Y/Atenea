---
updated_at: 2026-07-22
summary: Ownership and interface of Git operations used by the Wails workspace adapter.
---

# Workspace Git

Git process execution, porcelain parsing, diffs, repository initialization, and
commits belong to `internal/workspacegit`. The module is rooted at one workspace
through `workspacegit.Open(root)` and exposes those behaviors through
`Repository`.

The root Wails adapter owns only frontend translation and AI-assisted commit
message generation. Its `GitChange` and `GitStatus` aliases preserve the
existing `main.App` binding and JSON contract, while the underlying behavior is
tested through the workspace Git interface.

This dependency direction keeps Git subprocess and filesystem details out of
the desktop adapter:

```text
frontend -> main.App -> internal/workspacegit -> git process/filesystem
```

Commit-message generation remains in the adapter because it combines the Git
module's staged diff with the currently selected LLM provider.
