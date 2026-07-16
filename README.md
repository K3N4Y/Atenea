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

## TUI provider selection

Install the terminal agent once and run it from any workspace:

```bash
go install ./cmd/atenea
cd /path/to/project
atenea
```

`atenea` uses the current directory as its workspace and supports a local
`/model` command. Type `/model ` followed by any
provider or model fragment, select a result from the normal composer popup,
then press Enter again to apply it. Provider definitions are read
from `providers.json` inside the Atenea directory returned by
`os.UserConfigDir()` (typically `~/.config/atenea/providers.json` on Linux).

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

Authenticated providers use `api_key_env` for the environment-variable name;
the key value is never stored. If the file is absent, the existing
`OPENROUTER_API_KEY`/`OPENROUTER_MODEL` startup behavior remains, with the
offline demo as the final fallback.

The built-in OpenRouter catalog includes `tencent/hy3:free` (262K),
`poolside/laguna-xs-2.1:free` (262K), and `cohere/north-mini-code:free` (256K).
Their context sizes appear beside each model in the `/model` popup.
