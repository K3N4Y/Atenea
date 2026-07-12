---
updated_at: 2026-07-10
summary: Navigation index for Atenea project documentation in the OKF convention.
---

# Atenea Documentation

This directory is the source of truth for project documentation. Every document
uses Markdown and begins with `updated_at` and `summary` YAML metadata.

## Directory layout

- [`architecture/`](architecture/): technical architecture, agent-loop, LLM,
  tool, and TUI design references.
- [`audits/`](audits/): project health, implementation-status, and risk audits.
- [`design/`](design/): product UX, frontend, and visual-identity guidance.
- [`plans/`](plans/): implementation plans and delivery roadmaps.
- [`research/`](research/): research notes and external-system investigations.
- [`specs/`](specs/): milestone, tool, and feature specifications.

## Architecture

- [Agent loop](architecture/agent-loop.md)
- [Claude LLM integration](architecture/llm-claude.md)
- [Continuous integration and automated review](architecture/continuous-integration.md)
- [OpenCode/OpenAI LLM integration](architecture/llm-opencode-openai.md)
- [OpenCode agent loop](architecture/opencode-agent-loop.md)
- [OpenCode architecture](architecture/opencode-architecture.md)
- [Read and edit tools](architecture/read-edit-tools.md)
- [Terminal UI](architecture/tui.md)

## Design

- [Frontend](design/frontend.md)
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
- [Transcript activity hierarchy](plans/2026-07-11-transcript-activity-hierarchy.md)

## Research

- [Harness](research/harness.md)
- [Harness and SkillOpt](research/harness2-skillopt.md)
- [Harness subagents](research/harness-subagents.md)
- [SLM tool-calling reliability](research/slm-tool-calling-reliability.md)

## Specifications

- [TUI file tree](specs/2026-07-08-tui-file-tree.md)
- [Agent context compaction](specs/2026-07-09-agent-context-compaction.md)
- [CI and CodeRabbit](specs/2026-07-10-ci-coderabbit.md)
- [TUI file viewer](specs/2026-07-09-tui-file-viewer.md)
- [TUI provider and model selector](specs/2026-07-10-tui-model-selector.md)
- [TUI dark canvas](specs/2026-07-10-tui-dark-canvas.md)
- [TUI inline model completion](specs/2026-07-10-tui-inline-model-completion.md)
- [TUI manual context compaction](specs/2026-07-11-tui-manual-context-compaction.md)
- [TUI prompt undo](specs/2026-07-11-tui-prompt-undo.md)
- [Transcript activity hierarchy](specs/2026-07-11-transcript-activity-hierarchy.md)
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

## Audits

- [Project state audit](audits/project-state-audit.md)
