---
updated_at: 2026-08-06
summary: Filesystem roots, artifact ownership, extension discovery precedence, and compatibility promises shared by every Atenea host.
---

# Filesystem and compatibility contract

`internal/paths` is the only package that owns Atenea's product name and default
user filesystem layout. Desktop, terminal, and headless hosts consume the same
answers; a new entrypoint must not reconstruct these paths itself.

## User roots and environment precedence

Configuration resolves as `ATENEA_CONFIG_DIR`, then `XDG_CONFIG_HOME/atenea`,
then `os.UserConfigDir()/atenea`. Durable data resolves as
`XDG_DATA_HOME/atenea`, then the platform data directory (`~/.local/share/atenea`
on Unix). Disposable data resolves as `XDG_CACHE_HOME/atenea`, then
`os.UserCacheDir()/atenea`.

Artifacts belong to these roots:

| Root | Artifacts |
|---|---|
| config | `credentials.json`, `providers.json`, global `mcp.json` |
| data | `atenea.db`, `checkpoints/` |
| cache | `models-cache.json` |

`ATENEA_DB` and `ATENEA_CHECKPOINTS` override the individual data artifacts
after root resolution. `ATENEA_CONFIG_DIR` overrides only the config root; it
does not relocate data or cache. Provider credential environment names remain
provider-catalog data and are not derived from `ATENEA_`.

## Skills and agents

Skills and subagents use the same ordered discovery policy. For each kind, the
project is searched before the user's home, and each scope is searched in this
order:

1. `.atenea/<kind>` — native Atenea layout
2. `.agents/<kind>` — agent-neutral compatibility layout
3. `.claude/<kind>` — Claude Code compatibility layout

Here `<kind>` is `skills` or `agents`. Discovery is recursive, duplicate paths
are removed without changing order, and a duplicate name is first-wins.
Consequently project definitions override home definitions, and an Atenea
definition overrides `.agents` and `.claude` definitions at the same scope.
Both project and home compatibility layouts are a supported interface, not a
best-effort import path.

Callers may replace a discovery list explicitly; a non-nil empty list means
discover nothing.

## Workspace MCP overlay

The global MCP file is the config-root `mcp.json`. A workspace may additionally
commit `<workspace>/.mcp.json` using the de-facto `mcpServers` JSON schema.
Declarations are merged by server name: the workspace declaration wins over a
global declaration, while listings retain the shadowed global declaration and
identify both source files. Hosts never write the workspace file. This overlay
and schema are part of the compatibility promise alongside `.agents` and
`.claude` discovery.

## Integration identity

Product identity is immutable assembly data. The CLI constructs `atenea` plus
its release version and passes it through the shared host to the desktop or TUI
workspace manager, and then to each MCP client initialization. Source builds,
tests, and embedders that omit a version advertise `atenea dev`. No package-level
mutable version state is used, so multiple hosts can carry distinct identities.

## Contract tests

`internal/paths` pins root selection, artifact mapping, discovery ordering, and
deduplication. The wiring contract then proves that all six project/home layouts
for both skills and agents become model-visible and that precedence survives
assembly. `internal/mcpclient` separately pins the global/workspace merge,
workspace precedence, source scopes, and shadow reporting. Together these tests
guard the public compatibility behavior without repeating parser unit tests.
