---
updated_at: 2026-07-17
summary: Local MCP server integration for the Wails application and agent tool registry.
---

# MCP servers

Atenea connects to **local MCP servers over stdio**. The Settings > MCPs panel
persists each server's name, command, and arguments in the local frontend
settings, so the configuration survives an application restart. Opening Atenea
never launches a configured process: every configured server starts only after
the user explicitly connects it.

## Flow

1. The Wails UI saves a name, command, and one argument per line locally after a
   successful connection, then sends it to `ConnectMCP`.
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
