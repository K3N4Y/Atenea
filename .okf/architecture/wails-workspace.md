---
updated_at: 2026-07-22
summary: Workspace-dependent wiring lifecycle used by the Wails desktop adapter.
---

# Wails workspace lifecycle

`internal/wailsworkspace.Manager` is the seam between the Wails bindings and
workspace-dependent agent wiring. The module owns the active root, project-file
glob, MCP manager, and calls to `wiring.Build`; `main.App` remains a thin Wails
adapter and keeps its existing public bindings.

Prompt admission, workspace changes, provider changes, and MCP tool changes are
serialized by one lifecycle lock. A newly admitted turn therefore observes one
complete configuration: root, runner, slash commands and MCP tools all come from
the same published build. Root and glob reads use a
separate state lock so hooks admitted under the lifecycle lock can safely record
the current session working directory.

The interface is deliberately small: `Root`, `SetRoot`, `Files`, `Admit`, and
the provider/MCP lifecycle operations. Validation and `wiring.Build` stay behind
the seam. `SelectWorkspace` remains in `main.App` because the native Wails
directory dialog is a GUI adapter concern.

Session snapshots, the permission gate, inbox, store, event bus, the provider
handle, and `agent.Service` are stable dependencies supplied once at
construction. The provider is the switchable handle rather than the adapter of the
moment, so a model change swaps what it delegates to instead of requiring a
rebuild; what a rebuild still buys on a selection change is cutting the runs that
were streaming from the model the user just left. Rebuilds preserve those objects while replacing the complete
root-dependent runner configuration.
