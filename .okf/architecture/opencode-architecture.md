---
updated_at: 2026-07-09
summary: Reference architecture and design notes for OpenCode.
---

# OpenCode architecture

Researched on 2026-06-19 on the official OpenCode documentation and the
`anomalyco/opencode` repository in the `dev` branch. In that branch the main
 packages consulted report version `1.17.8`.

## Summary

OpenCode is a programming agent with several interfaces on the same
backend. The central architectural idea is to separate:

- **Clients**: TUI, non-interactive CLI, web app, desktop app, IDE and SDK extensions. JSON/JSONC, specialized agents, MCP,
 plugins, custom tools, skills, LSP and formatters.

In normal use, `opencode` boots the TUI and also a server. The TUI works
as a client of that server. In headless mode, `opencode serve` raises only the
HTTP server for another client to control. In browser mode, `opencode web`
raises the server and opens a local web app.

## High level view

```text
Usuario
  |
  | opencode / opencode run / opencode web / opencode serve / SDK
  v
Clientes
  - TUI terminal
  - CLI run
  - Web app
  - Desktop app
  - IDE extension
  - @opencode-ai/sdk
  |
  | HTTP + OpenAPI + SSE/eventos + endpoint TUI
  v
Servidor OpenCode
  - sesiones y mensajes
  - configuracion y providers
  - permisos
  - herramientas
  - archivos, busqueda, VCS, LSP, formatters, MCP
  - eventos
  |
  v
Core del agente
  - contexto de proyecto
  - runner de sesion
  - proveedor/modelo LLM
  - ejecucion de tool calls
  - persistencia local
```

## Monorepo and relevant packages

The upstream repo is a monorepo. The important folders to understand the
architecture are:

| Package | Role |
| --- | --- |
| `packages/opencode` | Assembles the `opencode` binary. Contains CLI commands, HTTP server, TUI integration and main runtime dependencies. |
| `packages/core` | Central domain: agents, configuration, credentials, database, events, filesystem, permissions, project, PTY, sessions, tools, snapshots and workspace. |
| `packages/llm` | Provider layer and LLM protocols. Exports adapters for Anthropic, OpenAI, Bedrock, Gemini, OpenRouter and compatible protocols. |
| `packages/server` | Shared server types/utilities. The HTTP implementation used by the binary lives mainly under `packages/opencode/src/server`. |
| `packages/sdk/js` | JS/TS SDK generated from the OpenAPI specification. Exposes `@opencode-ai/sdk` to create or attach to a server. |
| `packages/tui` | Terminal interface. Use OpenTUI/Solid and consume the SDK to talk to the backend. |
| `packages/app` | Solid/Vite web app used by web mode and visual packaging. |
| `packages/desktop` | Electron packaging for the desktop experience. |
| `packages/plugin` | Types/base for plugins. |
| `packages/web` | OpenCode public site/documentation, different from the runtime app. |

The important separation is that `core` concentrates the agent domain, while
that `opencode`, `tui`, `app`, `desktop` and `sdk` are input/output layers.

## Server as main contract

OpenCode documents the server as the programmatic way to interact with the
agent. `opencode serve` exposes HTTP in `127.0.0.1:4096` by default, with flags
for port, hostname, CORS and mDNS. It also supports HTTP Basic authentication through
via `OPENCODE_SERVER_PASSWORD` and `OPENCODE_SERVER_USERNAME`.

The server publishes an OpenAPI 3.1 specification at `/doc`. That specification
 is used to inspect types, generate clients, and feed the JS/TS SDK.

Main APIs exposed:

- `GET /global/health` and `GET /global/event`: health and global events.
- `GET /project`, `GET /project/current`: projects.
- `GET /path`, `GET /vcs`: current directory and VCS status.
- `GET/PATCH /config`: effective configuration.
- `GET /provider`, providers and models auth available.
- `GET/POST/PATCH/DELETE /session`: session life cycle.
- `POST /session/:id/message`, `prompt_async`, `command`, `shell`: execution of
 prompts, slash and shell commands.
- `GET /session/:id/message`: message history.
- `GET /session/:id/diff`, `revert`, `unrevert`: diffs and reversibility.
- `GET /find`, `/find/file`, `/find/symbol`, `/file/content`, `/file/status`:
 searching and reading files.
- `GET /experimental/tool`: schemas of available tools.
- `GET /lsp`, `/formatter`, `/mcp`: LSP status, formatters and MCP.
- `GET /agent`: available agents.
- `/tui/*`: remote control of the TUI, used by IDE integrations.
- `GET /event`: SSE stream of server events.

Internally, the upstream server uses `effect`, `@effect/platform-node`,
`HttpApi`, `OpenApi`, `NodeHttpServer`, a WebSockets tracker, and optional mDNS.

## Session execution flow

1. The user boots `opencode`, `opencode run`, `opencode web` or a client
 SDK.
2. The CLI resolves directory, configuration, provider/model, agent and permissions.
3. A session is created or resumed. A session can be continued, forked,
 shared, summarized, forked, rolled back, or compacted.
4. The server creates the context: previous messages, referenced files,
 project instructions, agent configuration, model, permissions and
 available tools.
5. The runner invokes the model through the LLM/provider layer.
6. If the model requests a tool, the server evaluates permissions (`allow`,
 `ask`, `deny`). If applicable, run the tool and add the result as part
 of the message.
7. The event bus emits session, message, tool, permission and state changes.
 The TUI/web/SDK renders those events.
8. When the session returns to `idle`, the client terminates the prompt or is ready
 for the next interaction.

## Agents

OpenCode distinguishes two types:

- **Primary agents**: they are the main personality/mode of a conversation.
 The most important integrations are `build` and `plan`.
- **Subagents**: they are invoked by a primary agent for specialized tasks or are mentioned manually with `@`.
 Integrated ones include `general`, `explore` and
 `scout`.

The agent model is connected with permissions. `build` has broad access
for development work. `plan` is restricted for analysis and planning
without modifying the default repository. `explore` and `scout` are readable for
local exploration or external research.

## Tools and permissions

The tools are the mechanism by which the LLM acts on the environment. OpenCode
includes tools for:

- `bash`: execute commands.
- `edit`, `write`, `apply_patch`: modify files.
- `read`: read files.
- `grep`, `glob`: search for content or paths.
- `lsp`: consult language/IDE when enabled.
 - `skill`: load skills.
 - `todowrite`: manage task lists.The `permission` configuration decides whether an action runs without approval, requests
approval, or is blocked. It can be global (`"*": "ask"`), per tool
(`"bash": "allow"`) or granular per pattern (`"rm *": "deny"`). There is also
`external_directory` to allow access outside the current workspace.

## Configuration

OpenCode uses JSON/JSONC. Configuration files are merged by layers; subsequent
layers overwrite conflicting keys:

1. Organizational remote configuration from `.well-known/opencode`.
2. Global config in `~/.config/opencode/opencode.json`.
3. `OPENCODE_CONFIG`.
4. Project Config `opencode.json`.
5. `.opencode/` directories for agents, commands, plugins, skills, tools and
 themes.
6. `OPENCODE_CONFIG_CONTENT`.
7. Managed OS Config.
8. MDM Managed Preferences on macOS.

The config covers server, shell, models, providers, agents, permissions,
formatters, LSP, MCP, plugins, instructions, themes, keybinds, sharing,
compaction and experimental flags.

## Providers LLM

OpenCode uses AI SDK and Models.dev to support many providers. The
providers layer separates credentials, model selection, and protocol details. The
credentials added with `/connect` are saved locally to
`~/.local/share/opencode/auth.json`.

Custom providers or compatible endpoints can also be configured,
for example by setting `baseURL` in the `provider` section of the config.

## Extensibility

OpenCode is mainly extended in four ways:

- **MCP**: local or remote servers add tools to the model context. They are
 configured under `mcp` and can be enabled/disabled per server.
- **Plugins**: JS/TS modules loaded from `.opencode/plugins/`,
 `~/.config/opencode/plugins/` or npm. They can be hooked to events like
 `tool.execute.before`, `tool.execute.after`, `session.updated`,
 `permission.asked`, `file.edited`, etc.
- **Custom agents**: agent files with prompt, mode, model, permissions and
 metadata.
- **Custom tools/skills**: local mechanisms to add capabilities
 controlled to the agent.

The practical caveat to MCP is that each server adds tools and
schemas to the context, so it can consume tokens quickly if
too many servers are enabled.

## Integration from this Wails project

This current repo is a Wails Go + Vue app. If you wanted to integrate OpenCode, the
healthiest options are:

1. **Sidecar local via process**: Go starts `opencode serve` or `opencode web`,
 manages the life cycle of the process and the Vue UI consults the backend for its own
 API in Go. It is the option most aligned with Wails.
2. **Direct HTTP Client**: Go calls the OpenCode HTTP/OpenAPI. Avoids
 putting Node/Bun in the frontend and allows controlling auth, CORS and paths from the
 native backend.
3. **JS/TS SDK**: useful if a Node/Bun part exists in the app. In a pure Wails,
 the frontend SDK is not ideal because the browser is exposed to CORS/auth and
 to the process lifecycle.
4. **Simple CLI**: for specific tasks, running `opencode run "..."` from Go
 and capturing stdout is the simplest, although it gives less control over sessions,
 events and tools.

For serious integration, I would treat OpenCode as an external backend controlled
by HTTP contract, not as an embedded library. The upstream core is made for the
TypeScript/Bun runtime and changes more than a versioned OpenAPI.

## Sources consulted

- Main documentation: https://opencode.ai/../
- Server and API: https://opencode.ai/../server/
- SDK: https://opencode.ai/../sdk/
- CLI: https://opencode.ai/../cli/
- TUI: https://opencode.ai/../tui/
- Web: https://opencode.ai/../web/
- IDE: https://opencode.ai/../ide/
- Config: https://opencode.ai/../config/
- Agents: https://opencode.ai/../agents/
- Tools: https://opencode.ai/../tools/
- Permissions: https://opencode.ai/../permissions/
- MCP: https://opencode.ai/../mcp-servers/
- Plugins: https://opencode.ai/../plugins/
- Repo: https://github.com/anomalyco/opencode
- Monorepo packages: https://github.com/anomalyco/opencode/tree/dev/packages
- Core source layout: https://github.com/anomalyco/opencode/tree/dev/packages/core/src
- Runtime source layout: https://github.com/anomalyco/opencode/tree/dev/packages/opencode/src
