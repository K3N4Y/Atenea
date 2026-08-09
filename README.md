<div align="center">

# Atenea

**An open-source coding agent built for the terminal.**

Work interactively in a full-screen TUI or run one-off tasks from scripts and CI.
Atenea can inspect and edit code, execute commands, delegate work to specialized
agents, load reusable skills, and connect to local or remote tools through MCP.

[![CI](https://github.com/K3N4Y/Atenea/actions/workflows/ci.yml/badge.svg)](https://github.com/K3N4Y/Atenea/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/K3N4Y/Atenea)](https://github.com/K3N4Y/Atenea/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/K3N4Y/Atenea)](go.mod)

[Install](#installation) · [Quick start](#quick-start) · [Configuration](#configuration) · [Contributing](CONTRIBUTING.md)

</div>

> [!IMPORTANT]
> Atenea is under active development. It can modify files, run commands, and
> access network services with your user account's permissions. Review tool
> requests before approving them and avoid using it in untrusted workspaces.

## Why Atenea?

Atenea keeps the coding-agent workflow close to the codebase and makes its
extension points explicit:

- **Interactive and headless workflows** — use the terminal UI for exploration
  or `atenea run` for automation.
- **Permission-aware tools** — control file writes, command execution, and
  network access instead of granting every action by default.
- **Multi-provider models** — connect Anthropic, OpenRouter, OpenCode, OpenAI,
  or any OpenAI-compatible endpoint.
- **Specialized subagents** — delegate focused implementation, review,
  exploration, and testing tasks.
- **Reusable skills** — discover project-level and user-level `SKILL.md`
  instructions.
- **MCP support** — extend the agent with local stdio or remote Model Context
  Protocol servers.
- **Persistent sessions** — resume previous conversations and compact long
  sessions without leaving the terminal.
- **Open contracts** — build integrations against the public interfaces in
  [`agentcore/`](agentcore/).

## Installation

### Install a release

Linux and macOS releases are available for `amd64` and `arm64`. The installer
downloads the latest archive, verifies its SHA-256 checksum, and installs the
binary in `~/.local/bin` without `sudo`:

```bash
curl -fsSL https://raw.githubusercontent.com/K3N4Y/Atenea/main/install.sh | sh
```

Make sure `~/.local/bin` is on your `PATH`, then verify the installation:

```bash
atenea --version
```

To install a specific version or choose another destination:

```bash
curl -fsSL https://raw.githubusercontent.com/K3N4Y/Atenea/main/install.sh \
  | sh -s -- --version v0.1.0 --bin-dir "$HOME/bin"
```

Running the installer again upgrades or replaces the existing binary. To
uninstall Atenea, remove the executable:

```bash
rm "$HOME/.local/bin/atenea"
```

### Build from source

Atenea requires the Go version declared in [`go.mod`](go.mod).

```bash
git clone https://github.com/K3N4Y/Atenea.git
cd Atenea
go build -tags production -o ./build/bin/atenea ./cmd/atenea
./build/bin/atenea --version
```

## Quick start

Open a project and launch the interactive interface:

```bash
cd /path/to/your/project
atenea
```

Atenea treats the current directory as the workspace. If no provider is
configured, it starts in an offline demo mode and prompts you to connect one.

Useful commands inside the TUI include:

| Command | Purpose |
| --- | --- |
| `/connect` | Connect a supported model provider |
| `/model` | Search and select a provider and model |
| `/new` | Start a new conversation |
| `/resume` | Resume a previous workspace session |
| `/compact` | Compact the current session context |
| `/skills` | Browse discovered skills |
| `/agents` | Browse available subagents |
| `/mcp` | Inspect MCP integrations |
| `/mode` | View or change the permission mode |
| `/reasoning:<level>` | Set the model's reasoning effort when supported |

Run `atenea --help` to see the complete command-line interface.

## Headless usage

Use `atenea run` for scripts, editor integrations, and CI jobs:

```bash
atenea run -p "Summarize the architecture of this repository"
```

Prompts can also come from standard input:

```bash
git diff | atenea run -p "Review this diff for correctness" --stdin
```

The command supports structured output and explicit permission budgets. See all
available options with:

```bash
atenea run --help
```

The CLI also exposes utilities for extensions:

```bash
atenea skill list
atenea skill validate
atenea agent validate
atenea mcp list
atenea mcp add my-server -- command --flag
```

## Configuration

Atenea reads `providers.json` from its configuration directory. Set
`ATENEA_CONFIG_DIR` to choose that directory explicitly. Otherwise it uses
`$XDG_CONFIG_HOME/atenea` when set, then the platform's user configuration
directory (typically `~/.config/atenea` on Linux).

Example configuration for a local OpenAI-compatible server:

```json
{
  "providers": [
    {
      "id": "local",
      "name": "Local",
      "type": "openai-compatible",
      "base_url": "http://localhost:11434/v1",
      "models": ["qwen3:14b"]
    }
  ],
  "selected": {
    "provider": "local",
    "model": "qwen3:14b"
  }
}
```

Authenticated providers use `api_key_env` to name the environment variable that
contains their API key. The built-in `/connect` flow supports Anthropic,
OpenRouter, OpenCode Zen, and OpenCode Go, and stores credentials in Atenea's
private credentials file.

When no provider configuration exists, startup checks for credentials in this
order: OpenRouter, OpenAI, Anthropic, then offline demo mode. Common environment
variables include:

```bash
export OPENROUTER_API_KEY="..."
export OPENAI_API_KEY="..."
export ANTHROPIC_API_KEY="..."
```

Use only the variable required by your selected provider. Anthropic users can
also set `ANTHROPIC_MODEL` to override the default model.

## Permissions and safety

The default interactive mode asks before sensitive tool calls. You can change
the active mode with `/mode:ask` or `/mode:auto-accept`.

Atenea also provides an unrestricted interactive mode:

```bash
atenea --yolo
# Alias: atenea --dangerously-skip-permissions
```

> [!WARNING]
> YOLO mode skips permission prompts for recognized and unrecognized tools. It
> is not a sandbox. Commands, network access, and file changes execute with your
> account's authority. Atenea blocks only positively recognized direct recursive
> deletion attempts against the filesystem root or resolved home directory;
> aliases, generated commands, scripts, and other indirect execution can bypass
> that narrow safeguard.

For the threat model and private vulnerability reporting process, read
[`SECURITY.md`](SECURITY.md).

## Extending Atenea

### Skills

Skills are reusable instructions stored in `SKILL.md` files. Atenea discovers
skills from the workspace and user configuration directories, allowing a
project to ship its own workflows without recompiling the binary.

```bash
atenea skill list
atenea skill validate path/to/SKILL.md
```

### Subagents

Subagent definitions give delegated tasks focused roles and instructions. Use
the validation command before sharing a definition:

```bash
atenea agent validate path/to/agent.md
```

### MCP servers

Atenea supports Model Context Protocol servers over local stdio and remote
transports. Manage global stdio declarations from the CLI:

```bash
atenea mcp add filesystem -- npx -y @modelcontextprotocol/server-filesystem /path
atenea mcp list
atenea mcp remove filesystem
```

Only connect MCP servers you trust: local servers run as your user, and remote
servers may receive tool input and workspace context.

## Architecture

The repository follows a **contracts public, loop private** design:

```text
agentcore/        Public contracts for LLMs, tools, memory, permissions, and sessions
internal/         Agent loop, providers, built-in tools, stores, TUI, and wiring
agents/           Built-in subagent manifests
cmd/atenea/       Standalone terminal entry point
.okf/             Architecture and project documentation
```

`agentcore/` is intentionally independent of `internal/` and third-party
modules. Contract test kits protect extension behavior across implementations.
See the [project documentation index](.okf/README.md) for design decisions and
technical references.

## Contributing

Contributions are welcome. Before making a substantial change:

1. Search the [issue tracker](https://github.com/K3N4Y/Atenea/issues).
2. Read [`CONTRIBUTING.md`](CONTRIBUTING.md) for setup, architecture rules, and
   quality gates.
3. Keep changes focused and add behavior-level verification where the contract
   changes.
4. Open a pull request that explains the user-visible outcome and how it was
   tested.

For security issues, do not open a public issue. Follow the private reporting
instructions in [`SECURITY.md`](SECURITY.md).

## License

Atenea is available under the [MIT License](LICENSE).
