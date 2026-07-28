---
updated_at: 2026-07-27
summary: Use MCP as Atenea's sole boundary for executing third-party extension code instead of adding an in-process or proprietary plugin mechanism.
---

# ADR 0002: Use MCP as the only third-party code boundary

## Status

Accepted on 2026-07-27.

## Context

Atenea needs third-party extensions that can be written independently, survive
host upgrades, work across operating systems, and fail without taking down the
host. MCP already provides a language-neutral protocol, stdio and remote
transports, process isolation, capability discovery, and an ecosystem shared
with other coding agents. A second executable-plugin protocol would create two
security, lifecycle, and compatibility models for the same job.

The public Go contracts remain useful for Atenea's own implementations and for
compile-time interoperability. They are not an authorization to load arbitrary
third-party code into the Atenea process.

## Decision

MCP is the only supported boundary across which Atenea executes third-party
extension code. Native Go implementations are linked by Atenea as first-party
code; independently distributed executable extensions run as MCP servers.

The MCP boundary covers tools, prompts, resources, and authorized model
sampling. The host owns transport validation, capability namespacing,
ask-before-run policy, durable trust, timeouts, health, restart, and shutdown.
Adding a new category of extension must first be considered as an MCP capability
or ordinary data manifest, not as another executable plugin API.

## Consequences

- Extensions can use any language and do not share Atenea's compiler or
  dependency graph.
- Local server crashes are isolated from the host; remote and local extensions
  use one capability model across the desktop app, TUI, and headless CLI.
- Protocol calls and process boundaries add serialization and operational
  overhead compared with in-process calls.
- MCP process isolation is not a sandbox. Untrusted-code sandboxing would
  require a separate security decision and threat model.
- Atenea maintains one extension lifecycle and trust model instead of parallel
  plugin ecosystems.

## Rejected alternatives

- **Go's `plugin` package.** It requires closely matched compiler and dependency
  versions, is not portable to Windows, and cannot unload plugins safely.
- **HashiCorp go-plugin or a bespoke gRPC protocol.** Either duplicates MCP with
  an Atenea-specific schema and no shared ecosystem.
- **WASM as the default extension runtime.** It offers stronger sandboxing, but
  imposes a second capability and host-ABI design before Atenea has a concrete
  untrusted-code requirement. It may be reconsidered only for that distinct
  security need.

## Further reading

- [MCP servers](../mcp.md)
- [Agnosticism and extensibility audit](../../audits/2026-07-24-agnostic-extensibility-audit.md#r8--make-mcp-the-one-and-only-third-party-code-boundary)

