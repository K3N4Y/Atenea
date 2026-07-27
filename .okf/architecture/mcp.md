---
updated_at: 2026-07-27
summary: MCP tools, prompts, resources, and authorized host-model sampling shared by both interactive hosts.
---

# MCP servers

Atenea connects to local MCP servers over stdio and hosted servers over
Streamable HTTP or legacy HTTP+SSE. Server definitions live in
shared JSON config files — the single source of truth for the desktop app, the
TUI and the `atenea mcp` subcommand (see "Configuration files" below). Opening
Atenea never launches a configured process: every configured server starts only
after the user explicitly connects it.

Declaring a server and connecting one are separate acts, and the surfaces split
along that line. The desktop panel and the TUI picker do both, because they are
hosts with a running process to attach. `atenea mcp` only declares: a CLI
invocation owns no session, so it writes the config and exits, and the next host
to open connects what it finds.

## Flow

1. The Wails UI (Settings > MCPs) sends a name, command, and one argument per
   line to `ConnectMCP`; after a successful connection it persists the config
   via `SaveMCPConfig` into the global config file. `ListMCPs` returns every
   declared server (global config merged with the workspace `.mcp.json`)
   overlaid with its live connection state, and `RemoveMCPConfig` disconnects
   and deletes a global entry (workspace-declared servers are edited in their
   file instead). Configs saved by older versions in the frontend localStorage
   are migrated to the global file on the first refresh.
2. `internal/mcpclient.Manager` selects the official
   `github.com/modelcontextprotocol/go-sdk` transport from the declaration:
   `CommandTransport`, `StreamableClientTransport`, or `SSEClientTransport`.
3. The client advertises the current workspace as an MCP root, initializes the
   session, and discovers tools, prompts, and resources.
4. Each discovered tool is adapted to `internal/tool.Tool` and named
   `mcp_<server>_<tool>` to avoid collisions with built-in tools.
5. The app rebuilds the normal runner registry. The newly materialized tools are
   available to subsequent turns; disconnecting removes them and closes the
   subprocess.
6. The manager waits for every client session. If an MCP process exits or closes
   its stdio connection unexpectedly, it removes that server from the active
   status so the Settings panel shows it as disconnected.

Prompts and resources use one namespaced composer substrate in both hosts. An
MCP prompt `review` from server `docs` appears as `/docs:review`; invocation
fetches the prompt at send time, accepts a single plain argument or a JSON object
for multiple arguments, and preserves message roles. A resource `guide` appears
as `@docs:guide`; selecting it uses the existing mention menu and admission reads
the resource into a delimited context block. Namespacing prevents collisions
between servers, and the whole snapshot disappears on an explicit disconnect.

Sampling is opt-in per server with `allowSampling: true`. Only then does the
client advertise sampling and forward a server request through the active host
provider. The adapter bounds output by the requested maximum, accepts text
content only, propagates provider failure, and returns only the completed answer.
The default is deliberately false: sampling can disclose server-supplied content
to a model, spend quota, and return model output to the server.

The manager updates advertised roots when the workspace changes and closes every
MCP session during Wails shutdown. Configurations remain available as
disconnected entries after shutdown; MCP tool results retain text content, while
other content is represented as JSON for the model.

## Configuration files

Servers are declared in two places, both using the de-facto standard format
shared by other agent CLIs:

- **`<user config dir>/atenea/mcp.json`** — global servers, available in every
  workspace (`~/.config/atenea/mcp.json` on Linux, next to `providers.json`).
  This is where the desktop app's Settings > MCPs panel and `atenea mcp add` /
  `atenea mcp remove` save and delete entries. Written atomically with `0600`
  permissions (`env` can carry tokens).
- **`<workspace>/.mcp.json`** — project servers, edited by hand and committed with
  the project. On a name collision the workspace entry overrides the global one,
  the same project-over-global precedence as skills. Nothing writes this file:
  the desktop panel and `atenea mcp remove` both refuse a server declared here
  and name the file to edit, because it belongs to the repository rather than to
  the machine.

`mcpclient.Declarations(root)` is the one reader of both files. It returns every
declaration with the scope and path it came from, *including* a global entry the
workspace overrides, and `LoadConfig` — what a host connects — is defined as that
list minus the shadowed entries. That is what lets `atenea mcp list` show an
override that would otherwise be invisible without it being able to disagree with
what actually starts.

Stdio subprocesses inherit only the operational environment needed to launch
portable local commands (`PATH`, home/temp, locale, timezone, and Windows
process variables). Provider tokens and other application secrets are passed
only when explicitly declared in the server's `env` map.

```json
{
  "mcpServers": {
    "playwright": {
      "type": "stdio",
      "command": "npx",
      "args": ["@playwright/mcp@latest"],
      "allowSampling": false
    },
    "hosted": {
      "type": "streamable-http",
      "url": "https://example.com/mcp"
    },
    "legacy-hosted": {
      "type": "sse",
      "url": "https://example.com/sse"
    }
  }
}
```

`type` accepts `stdio`, `http`, `streamable-http`, and `sse`; `http` is a short
alias for `streamable-http`. An omitted type remains `stdio`, preserving existing
`.mcp.json` files. Stdio declarations require `command` and may use `args`, `env`,
and `cwd`. Remote declarations require an absolute HTTP(S) `url` and reject
process-only fields, so an ambiguous mixed declaration fails at load time.

### Permissions

MCP retains R2's safe default: a server that omits `sensitivity` declares
nothing, so every discovered tool asks before each call (and can still receive
an in-memory session grant). Existing configuration files therefore migrate
without a rewrite and do not become more permissive.

`sensitivity` classifies every tool discovered from one server using the shared
tool-effects vocabulary: `read-only`, `writes-files`, `runs-commands`, or
`reaches-network`. Only `read-only` runs without asking by classification; the
other declarations document why the normal gate asks.

`allowedTools` is a durable trust decision, separate from sensitivity. Entries
use the server's original MCP tool names; `"*"` trusts every current and future
tool of that server. A listed tool skips `Ask` in every session but cannot
overrule `Deny`. Rules are namespaced with the server when loaded, so equal tool
names from different servers never share trust. Both hosts compose these rules
over the effects policy and then layer the existing session grants on top.

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

## Command line

`atenea mcp list | add | remove` (R4.4) is the third surface over the same two
files, and the one a script, a CI image or another agent can use:

```console
$ atenea mcp add --env GITHUB_TOKEN=$TOKEN github -- npx github-mcp
declared "github" in /home/k/.config/atenea/mcp.json
  env: GITHUB_TOKEN

$ atenea mcp list
NAME    SCOPE              COMMAND
github  workspace          docker run ghcr.io/github/github-mcp-server
github  global (shadowed)  npx github-mcp
```

It reuses `LoadConfig`/`Declarations`, `UpsertGlobalConfig` and
`RemoveGlobalConfig` rather than adding a second config path, needs no model
provider, and **starts nothing** — so its listing has no connected column, which
would be `false` on every row of every listing and would be read as a report about
the user's servers rather than about the process printing it. The full surface,
the refusals and the exit codes are in
[Headless CLI](headless-cli.md#atenea-mcp--the-servers-from-a-script).

## Scope

Remote transports currently support unauthenticated endpoints. OAuth and custom
request headers remain deferred because they require durable credential storage,
redirect/callback handling, and explicit remote-server trust UX.

Connected tools enter both the primary and subagent registries from the same
snapshot on every assembly. A subagent definition must still name each tool it
may use; merely connecting a server grants no child new authority. Its calls use
the primary runner's policy and gate, keyed to the child session, so remote tools
retain ask-by-default and durable-rule behavior. Since both Wails and the TUI
rewire through `wiring.Build`, connecting or disconnecting replaces both
registries together and new child runs cannot retain stale adapters.

## References

The design follows the official Model Context Protocol Go SDK and the MCP
lifecycle/configuration patterns in the `anomalyco/opencode` repository reviewed
on 2026-07-17: named server configurations, explicit connection state,
per-server processes, workspace roots, namespaced tools, and graceful cleanup.
