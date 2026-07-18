---
updated_at: 2026-07-17
summary: Local MCP server integration for the Wails application, the TUI, and the agent tool registry.
---

# MCP servers

Atenea connects to **local MCP servers over stdio**. Server definitions live in
shared JSON config files — the single source of truth for both the desktop app
and the TUI (see "Configuration files" below). Opening Atenea never launches a
configured process: every configured server starts only after the user
explicitly connects it.

## Flow

1. The Wails UI (Settings > MCPs) sends a name, command, and one argument per
   line to `ConnectMCP`; after a successful connection it persists the config
   via `SaveMCPConfig` into the global config file. `ListMCPs` returns every
   declared server (global config merged with the workspace `.mcp.json`)
   overlaid with its live connection state, and `RemoveMCPConfig` disconnects
   and deletes a global entry (workspace-declared servers are edited in their
   file instead). Configs saved by older versions in the frontend localStorage
   are migrated to the global file on the first refresh.
2. `internal/mcpclient.Manager` starts the command with the official
   `github.com/modelcontextprotocol/go-sdk` `CommandTransport`.
3. The client advertises the current workspace as an MCP root, initializes the
   session, and paginates `tools/list`.
4. Each discovered tool is adapted to `internal/tool.Tool` and named
   `mcp_<server>_<tool>` to avoid collisions with built-in tools.
5. The app rebuilds the normal runner registry. The newly materialized tools are
   available to subsequent turns; disconnecting removes them and closes the
   subprocess.
6. The manager waits for every client session. If an MCP process exits or closes
   its stdio connection unexpectedly, it removes that server from the active
   status so the Settings panel shows it as disconnected.

The manager updates advertised roots when the workspace changes and closes every
MCP session during Wails shutdown. Configurations remain available as
disconnected entries after shutdown; MCP tool results retain text content, while
other content is represented as JSON for the model.

## Configuration files

Servers are declared in two places, both using the de-facto standard format
shared by other agent CLIs:

- **`<user config dir>/atenea/mcp.json`** — global servers, available in every
  workspace (`~/.config/atenea/mcp.json` on Linux, next to `providers.json`).
  This is where the desktop app's Settings > MCPs panel saves and removes
  entries. Written atomically with `0600` permissions (`env` can carry tokens).
- **`<workspace>/.mcp.json`** — project servers, edited by hand. On a name
  collision the workspace entry overrides the global one, the same
  project-over-global precedence as skills.

```json
{
  "mcpServers": {
    "playwright": { "command": "npx", "args": ["@playwright/mcp@latest"] }
  }
}
```

## TUI

The `/mcp` command opens a full-screen picker (mirroring the `/model` picker)
that lists every declared server with its on/off state, tool count, and
command. Enter, space, or a click toggles the selected server; toggles run
asynchronously so the UI stays responsive while a server process starts (the
row shows `starting…`/`stopping…` in flight), and `r` reloads the list. Both
files are re-read on every listing, so edits show up without restarting.

On each connect/disconnect the headless engine re-runs `wiring.Build` with the
manager's current tools and swaps the runner — the same move `App.wire` makes
in the Wails app. Startup never launches a configured server (same contract as
the desktop app), and engine shutdown closes every MCP subprocess after active
runs stop.

## Scope

This implementation supports stdio only. HTTP transports and OAuth are deferred:
they require durable credential storage, redirect/callback handling, and explicit
remote-server trust UX. The protocol client also does not expose MCP tools to
subagents yet; the initial scope is the primary agent registry.

## References

The design follows the official Model Context Protocol Go SDK and the MCP
lifecycle/configuration patterns in the `anomalyco/opencode` repository reviewed
on 2026-07-17: named server configurations, explicit connection state,
per-server processes, workspace roots, namespaced tools, and graceful cleanup.
