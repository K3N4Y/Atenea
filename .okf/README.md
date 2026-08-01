---
updated_at: 2026-07-27
summary: Navigation index for Atenea project documentation in the OKF convention.
---

# Atenea Documentation

This directory is the source of truth for project documentation. Every document
uses Markdown and begins with `updated_at` and `summary` YAML metadata.

The project-wide vocabulary lives in the root [domain glossary](../CONTEXT.md).

## Directory layout

- [`architecture/`](architecture/): technical architecture, agent-loop, LLM,
  tool, and TUI design references.
- [`audits/`](audits/): project health, implementation-status, and risk audits.
- [`design/`](design/): product UX, frontend, and visual-identity guidance.
- [`plans/`](plans/): implementation plans and delivery roadmaps.
- [`research/`](research/): research notes and external-system investigations.
- [`specs/`](specs/): milestone, tool, and feature specifications.

## Architecture

- [Architecture decision records](architecture/adr/)
- [ADR 0001: Keep contracts public and the loop private](architecture/adr/0001-keep-contracts-public-and-the-loop-private.md)
- [ADR 0002: Use MCP as the only third-party code boundary](architecture/adr/0002-use-mcp-as-the-only-third-party-code-boundary.md)
- [Agent loop](architecture/agent-loop.md)
- [Claude LLM integration](architecture/llm-claude.md)
- [Composition root](architecture/composition-root.md)
- [Continuous integration and automated review](architecture/continuous-integration.md)
- [Durable event stream](architecture/event-stream.md)
- [Filesystem and compatibility contract](architecture/filesystem-compatibility.md)
- [CLI distribution](architecture/distribution.md)
- [Headless CLI](architecture/headless-cli.md)
- [OpenCode/OpenAI LLM integration](architecture/llm-opencode-openai.md)
- [OpenCode agent loop](architecture/opencode-agent-loop.md)
- [OpenCode architecture](architecture/opencode-architecture.md)
- [MCP servers](architecture/mcp.md)
- [Message content](architecture/message-content.md)
- [Provider capabilities](architecture/provider-capabilities.md)
- [Provider catalog](architecture/provider-catalog.md)
- [Provider credentials](architecture/provider-credentials.md)
- [Provider registry](architecture/provider-registry.md)
- [Published contracts (`agentcore/`)](architecture/public-contracts.md)
- [Read and edit tools](architecture/read-edit-tools.md)
- [Session module](architecture/session.md)
- [Terminal UI](architecture/tui.md)
- [Tool capabilities](architecture/tool-capabilities.md)
- [Workspace Git](architecture/workspace-git.md)
- [Wails provider surface](architecture/wails-provider.md)
- [Wails session lifecycle](architecture/wails-sessions.md)
- [Wails workspace lifecycle](architecture/wails-workspace.md)

## Design

- [Frontend](design/frontend.md)
- [TUI markdown theme](design/tui-markdown-theme.md)
- [Visual identity and UX](design/visual-identity.md)

## Plans

- [Agent-loop roadmap](plans/agent-loop-roadmap.md)
- [Agent context compaction](plans/2026-07-09-agent-context-compaction.md)
- [CI and CodeRabbit](plans/2026-07-10-ci-coderabbit.md)
- [TUI file viewer](plans/2026-07-09-tui-file-viewer.md)
- [TUI provider and model selector](plans/2026-07-10-tui-model-selector.md)
- [TUI dark canvas](plans/2026-07-10-tui-dark-canvas.md)
- [TUI manual context compaction](plans/2026-07-11-tui-manual-context-compaction.md)
- [TUI prompt undo](plans/2026-07-11-tui-prompt-undo.md)

## Research

- [Harness](research/harness.md)
- [Harness and SkillOpt](research/harness2-skillopt.md)
- [Harness subagents](research/harness-subagents.md)
- [SLM tool-calling reliability](research/slm-tool-calling-reliability.md)
- [OpenCode Zen and Go provider integration](research/2026-07-22-opencode-zen-go-provider-integration.md)
- [Anthropic Go SDK provider integration](research/2026-07-22-anthropic-go-sdk-provider-integration.md)
- [LLM prompt-cache hit research and provider audit](research/2026-07-23-llm-prompt-cache-hit.md)
- [Agent loops and graph orchestration](research/2026-07-27-graphs-over-agent-loops.md)
- [OpenAI ChatGPT subscription: device-code OAuth and the codex backend](research/2026-07-27-openai-chatgpt-oauth-device-code.md)

## Specifications

- [Shared headless agent service](specs/2026-07-13-headless-agent-service-design.md)
- [TUI file tree](specs/2026-07-08-tui-file-tree.md)
- [Agent context compaction](specs/2026-07-09-agent-context-compaction.md)
- [CI and CodeRabbit](specs/2026-07-10-ci-coderabbit.md)
- [TUI file viewer](specs/2026-07-09-tui-file-viewer.md)
- [TUI provider and model selector](specs/2026-07-10-tui-model-selector.md)
- [/connect command and credential store](specs/2026-07-18-connect-command.md)
- [Driving atenea with a ChatGPT subscription](specs/2026-07-27-openai-subscription-oauth.md)
- [Driving atenea with a PostHog account](specs/2026-07-31-posthog-oauth-provider.md)
- [Single permission gate for shell and local FS tools](specs/2026-07-23-single-permission-gate.md)
- [Session-scoped permission grants](specs/2026-07-24-session-permission-grants.md)
- [Safe auto-accept permission mode](specs/2026-07-31-safe-auto-accept-mode.md)
- [TUI dark canvas](specs/2026-07-10-tui-dark-canvas.md)
- [TUI inline model completion](specs/2026-07-10-tui-inline-model-completion.md)
- [TUI manual context compaction](specs/2026-07-11-tui-manual-context-compaction.md)
- [TUI prompt undo](specs/2026-07-11-tui-prompt-undo.md)
- [TUI transcript activity hierarchy](specs/2026-07-11-tui-transcript-activity-hierarchy.md)
- [Milestone M0: scaffolding](specs/atenea-m0-scaffolding-spec.md)
- [Milestone M1: types and in-memory store](specs/atenea-m1-tipos-store-spec.md)
- [Milestone M2: provider and scriptable fake](specs/atenea-m2-provider-fake-spec.md)
- [Milestone M3: event publisher](specs/atenea-m3-publisher-spec.md)
- [Milestone M4: tool registry and settlement](specs/atenea-m4-tool-registry-spec.md)
- [Milestone M5: successful `runTurn`](specs/atenea-m5-run-turn-spec.md)
- [Milestone M6: external `Run` loop and `MaxSteps`](specs/atenea-m6-run-loop-spec.md)
- [Milestone M7: control signals](specs/atenea-m7-control-signals-spec.md)
- [Milestone M8: interruption and failure handling](specs/atenea-m8-interrupcion-fallos-spec.md)
- [Milestone M9: Wails wiring](specs/atenea-m9-cableado-wails-spec.md)
- [Milestone M10: SQLite store and real provider](specs/atenea-m10-store-sqlite-provider-real-spec.md)
- [Edit tool](specs/atenea-tool-edit-spec.md)
- [Glob tool](specs/atenea-tool-glob-spec.md)
- [Grep tool](specs/atenea-tool-grep-spec.md)
- [Read tool](specs/atenea-tool-read-spec.md)
- [Tool input repair](specs/atenea-tool-input-repair-spec.md)

## Audits

- [Project state audit](audits/project-state-audit.md)
- [Agnosticism and extensibility audit](audits/2026-07-24-agnostic-extensibility-audit.md)
