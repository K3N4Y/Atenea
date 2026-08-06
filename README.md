# README

## About

This is the official Wails Vue-TS template.

You can configure the project by editing `wails.json`. More information about the project settings can be found
here: https://wails.io/docs/reference/project-config

## Live Development

To run in live development mode, run `wails dev` in the project directory. This will run a Vite development
server that will provide very fast hot reload of your frontend changes. If you want to develop in a browser
and have access to your Go methods, there is also a dev server that runs on http://localhost:34115. Connect
to this in your browser, and you can call your Go code from devtools.

## Building

To build a redistributable, production mode package, use `wails build`.

## Install the TUI

Install the latest Linux or macOS release (`amd64` and `arm64`) without sudo:

```bash
curl -fsSL https://raw.githubusercontent.com/K3N4Y/Atenea/main/install.sh | sh
```

The installer downloads the platform archive from GitHub Releases, verifies its
SHA-256 checksum, and writes `atenea` to `~/.local/bin`. Add that directory to
`PATH` if your shell does not already include it.

Install a specific version or destination with:

```bash
curl -fsSL https://raw.githubusercontent.com/K3N4Y/Atenea/main/install.sh |
  sh -s -- --version v0.1.0 --bin-dir "$HOME/bin"
```

Running the installer again updates or replaces the existing binary. To
uninstall it, remove the installed executable:

```bash
rm "$HOME/.local/bin/atenea"
```

Contributors can instead build the current checkout:

```bash
go build -tags production -o ./build/bin/atenea ./cmd/atenea
```

Verify any installation and then launch it from a workspace:

```bash
atenea --version
cd /path/to/project
atenea
```

For an explicitly unrestricted interactive session, launch `atenea --yolo`
(alias: `--dangerously-skip-permissions`). YOLO skips permission prompts for
recognized and unrecognized tools. It is not a sandbox: commands, network
access, and deletion elsewhere remain possible. Atenea only blocks positively
recognized direct `rm -r`/`rm -R` commands against filesystem root or the
resolved user home. This check parses shell syntax and examines executable
simple commands in lists, groups, subshells, and command substitutions, including
their redirections and the supported `env`, `sudo`, and `command` wrappers. It
does not resolve aliases, functions, generated commands, or arbitrary scripts.
The warning and `YOLO` composer indicator remain visible; `/mode:ask` or
`/mode:auto-accept` leaves YOLO, and `/mode:yolo` can re-enter it only in a
process launched with one of those flags. Authorization is never persisted.

`atenea` uses the current directory as its workspace and supports a local
`/model` command. Type `/model ` followed by any
provider or model fragment, select a result from the normal composer popup,
then press Enter again to apply it. Provider definitions are read from
`providers.json` under Atenea's config root. Set `ATENEA_CONFIG_DIR` to override
that root directly; otherwise it is `$XDG_CONFIG_HOME/atenea` when set, then the
platform user config directory (typically `~/.config/atenea` on Linux).

```json
{
  "providers": [{
    "id": "local",
    "name": "Local",
    "type": "openai-compatible",
    "base_url": "http://localhost:11434/v1",
    "models": ["qwen3:14b"]
  }],
  "selected": {"provider": "local", "model": "qwen3:14b"}
}
```

Authenticated providers use `api_key_env` for the environment-variable name.
The built-in `/connect` picker supports Anthropic, OpenRouter, OpenCode Zen,
and OpenCode Go. Anthropic uses its native Messages API through the official Go
SDK and reads `ANTHROPIC_API_KEY`; both OpenCode services use
`OPENCODE_API_KEY`. Keys entered through
`/connect` are stored in Atenea's private credentials file. If provider config
is absent, the environment startup fallback is OpenRouter, then OpenAI, then
Anthropic, and finally the offline demo. `ANTHROPIC_MODEL` can override the
built-in Anthropic default.
That default is `claude-opus-4-8`, Anthropic's recommended starting point for
complex agentic coding; set `ANTHROPIC_MODEL` to pin a specific snapshot.
Claude models that expose API effort can use the shared `/reasoning:<level>`
preference through both the Anthropic and PostHog providers. Atenea sends it as
Anthropic `output_config.effort`; supported levels depend on the selected Claude
model (`low`, `medium`, `high`, plus `xhigh` and/or `max` on newer families).
`/reasoning:default` restores Anthropic's default. Unsupported model/level
combinations fail before an API request instead of being silently downgraded.
This controls response effort without enabling raw extended thinking blocks.

OpenCode Zen and Go are separate providers: Zen is pay-as-you-go at
`https://opencode.ai/zen/v1`, while Go is the subscription endpoint at
`https://opencode.ai/zen/go/v1`. Atenea only lists their models documented as
OpenAI Chat Completions-compatible; models requiring Responses, Anthropic, or
Google protocols are intentionally omitted.

The built-in OpenRouter catalog includes `tencent/hy3:free` (262K),
`poolside/laguna-xs-2.1:free` (262K), and `cohere/north-mini-code:free` (256K).
Their context sizes appear beside each model in the `/model` popup.
