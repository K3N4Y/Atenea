---
updated_at: 2026-08-02
summary: Capability and architecture comparison between Atenea and oh-my-pi, with an impact-ranked improvement roadmap.
---

# Atenea compared with oh-my-pi

## Question

Why is [oh-my-pi](https://github.com/can1357/oh-my-pi) considered a strong coding-agent harness, how close is Atenea, and which improvements would materially increase Atenea's effectiveness?

This comparison uses oh-my-pi `main` as observed on 2026-08-02, plus the pinned source analysis at commit `09a7c865636457c50ed75fc3b1a7cc21ef72c105` already recorded in [read/edit](../architecture/read-edit-tools.md) and [subagent](harness-subagents.md) research. Product numbers in oh-my-pi's README are project claims unless independently demonstrated below.

## Executive verdict

Atenea does not lack a credible agent core. Its durable event-sourced loop, race-aware turn preparation, structured compaction, permission model, MCP lifecycle, session persistence, and hash-anchored editing are already strong foundations. In some areas—especially transactional compaction and effect-derived permission defaults—Atenea is at least as deliberate as the comparison target.

The large gap is the layer above the loop. oh-my-pi combines its loop with IDE-grade code intelligence, a much richer tool surface, model routing, isolated and supervised delegation, memory, broad extension APIs, editor/RPC/SDK entry points, and measured harness experiments. Atenea is currently a robust smaller harness; oh-my-pi is a broad coding environment and ecosystem.

The highest-value path is therefore not to rewrite the loop or port all 32 tools. It is to add a small number of force multipliers in this order: measurable evals, LSP-backed code intelligence, stronger delegated execution, explicit context pruning/memory, and a stable automation/editor surface.

## Why oh-my-pi is strong

### 1. It optimizes the model-to-action interface

The clearest evidence is hashline editing. The author's [Harness Problem benchmark](https://blog.can.ac/2026/02/12/the-harness-problem/) tested 16 models over 180 synthetic React repair tasks and reports roughly 15 percentage points average improvement over patch editing, with fewer retries for many models. The benchmark is useful but limited: it uses synthetic mutations in one codebase and exact-file scoring, and the results are self-published.

The underlying lesson is stronger than the headline number: a model may know the correct change yet fail mechanically because the harness demands brittle diff syntax or exact string reproduction. oh-my-pi repeatedly applies this principle through:

- hash-anchored edits with stale-write rejection;
- filesystem-like internal resources such as `pr://`, `issue://`, and `agent://`;
- LSP-aware writes and renames;
- proposed, atomic AST edits;
- structured subagent results instead of transcript parsing;
- stream-time corrective rules that activate only when needed.

Sources: [oh-my-pi README](https://github.com/can1357/oh-my-pi/blob/main/README.md), [`packages/hashline`](https://github.com/can1357/oh-my-pi/tree/main/packages/hashline), and [The Harness Problem](https://blog.can.ac/2026/02/12/the-harness-problem/).

### 2. It gives the model IDE-grade feedback

The current README documents 14 LSP operations and 28 DAP operations, plus tree-sitter summaries, AST search/editing, persistent Python and JavaScript runtimes, browser automation, rich web extraction, image tools, and debugger control. These capabilities shorten the feedback loop between a proposed change and semantic evidence that it is correct.

Native Rust components provide shared filesystem caching, AST summaries, search, shell/PTY, process management, image handling, and isolation. Performance claims such as “fastest” are not independently established here, but avoiding repeated process startup and sharing workspace traversal/cache are concrete architectural advantages.

Sources: [oh-my-pi README](https://github.com/can1357/oh-my-pi/blob/main/README.md), [`crates/pi-natives`](https://github.com/can1357/oh-my-pi/tree/main/crates/pi-natives), [`crates/pi-ast`](https://github.com/can1357/oh-my-pi/tree/main/crates/pi-ast), and [`docs/lsp-config.md`](https://github.com/can1357/oh-my-pi/blob/main/docs/lsp-config.md).

### 3. Delegation is a runtime, not a one-shot helper

oh-my-pi reuses the same loop in isolated child sessions, supports per-agent models and toolsets, bounded recursion, request/runtime budgets, schema-validated results, progress telemetry, worker-pool concurrency, optional worktree isolation, background supervision through `hub`, and DAG orchestration through the swarm extension.

The design lets the parent keep only the result rather than every child token. Isolation and typed outputs make parallel work safer and easier to consume. The detailed pinned analysis and source paths are recorded in [Harness subagents](harness-subagents.md).

Sources: [`packages/coding-agent/src/task`](https://github.com/can1357/oh-my-pi/tree/main/packages/coding-agent/src/task) and [`packages/swarm-extension`](https://github.com/can1357/oh-my-pi/tree/main/packages/swarm-extension).

### 4. Models are routed by role and failure mode

oh-my-pi documents more than 40 providers and role-specific model selection for normal, cheap, slow/deep, plan, and commit workloads. It also supports fallback chains and credential rotation with session affinity/backoff. The breadth itself is not intelligence, but assigning cost and reasoning depth to the task and surviving provider throttling makes the overall system more reliable.

Atenea already has a neutral provider registry and atomic provider/model snapshots; its gap is policy and breadth, not the basic seam.

Sources: [oh-my-pi provider documentation](https://omp.sh/docs/providers) and [oh-my-pi README](https://github.com/can1357/oh-my-pi/blob/main/README.md).

### 5. It is available through many surfaces

The same engine supports TUI, one-shot mode, JSONL RPC, ACP editor integration, a Node SDK, persistent/resumable sessions, and live collaboration. Extensions can register tools, commands, hotkeys, hooks, and TUI elements. This creates an ecosystem rather than only an application.

Sources: [oh-my-pi README](https://github.com/can1357/oh-my-pi/blob/main/README.md) and [`packages/coding-agent/DEVELOPMENT.md`](https://github.com/can1357/oh-my-pi/blob/main/packages/coding-agent/DEVELOPMENT.md).

### 6. It treats context as a controllable resource

Besides automatic compaction, oh-my-pi exposes checkpoint/rewind, project memory through local or Hindsight backends, separate advisor context, and persistent worker contexts. Its `snapcompact` package includes a SQuAD-oriented evaluation surface, although no result was independently verified here.

Sources: [`packages/coding-agent`](https://github.com/can1357/oh-my-pi/tree/main/packages/coding-agent), [`packages/snapcompact`](https://github.com/can1357/oh-my-pi/tree/main/packages/snapcompact), and [`packages/mnemopi`](https://github.com/can1357/oh-my-pi/tree/main/packages/mnemopi).

## Where Atenea is already strong

| Capability | Current Atenea assessment | Evidence |
| --- | --- | --- |
| Durable loop | Strong: event-sourced, resumable, queue/steer-aware, cleans interrupted calls | [Agent loop](../architecture/agent-loop.md) |
| Concurrent tools | Strong: calls settle concurrently after the declaring assistant stream is durable | `internal/session/runner/turn.go` |
| Read/edit safety | Strong: bounded collision-aware snapshots, seen-line provenance, all-or-nothing preflight, recovery, atomic replacement | [Read/edit](../architecture/read-edit-tools.md) |
| Context compaction | Strong: structured summaries, atomic checkpoints, epoch advancement, preventive and overflow paths | `internal/session/compaction.go`, `internal/session/runner/compactor.go`, `internal/session/sqlitestore.go` |
| Permissions | Strong: effects-derived ask-by-default, session grants, safe auto-accept, explicit YOLO, propagated child gate | [Tool capabilities](../architecture/tool-capabilities.md) |
| MCP | Strong: stdio and remote transports, tools/prompts/resources/sampling, health and restart supervision, shared across hosts | [MCP](../architecture/mcp.md) |
| Sessions | Strong: shared SQLite durability across desktop, TUI, and headless execution | `internal/host/host.go`, `internal/session/sqlitestore.go` |
| Headless use | Good: `atenea run`, aggregate JSON and ordered NDJSON stream, session continuation | [Headless CLI](../architecture/headless-cli.md) |
| Skills/agents | Good: Markdown discovery, project/global precedence, on-demand skills, bounded child sessions | `internal/skill`, `internal/agent`, `internal/session/subagent` |
| Distribution | Good but narrow: verified Linux/macOS standalone TUI/headless binaries and checksums | `.goreleaser.yml`, `install.sh` |

These are reasons to preserve the current architecture. Replacing Go, adopting a TypeScript monorepo, embedding a shell, or copying oh-my-pi's extension runtime would not by itself improve agent quality.

## Confirmed capability gaps

| Area | Atenea today | oh-my-pi today | Material impact |
| --- | --- | --- | --- |
| Code intelligence | No native LSP, DAP, tree-sitter summaries, AST search/edit, or semantic rename | Native LSP/DAP/AST surface | Very high |
| Evaluation | No coding-agent eval/benchmark harness in the executable or dependencies | Edit-format benchmark and package-specific eval surfaces | Very high because priorities cannot be measured |
| Delegation | Isolated in-memory children, allowlists, depth/concurrency cap, live events | Typed results, budgets, worktrees, background jobs, hub, advisor, swarm DAG | High for large tasks |
| Rich execution | Bash only; no persistent Python/JS kernel or tool re-entry | Persistent eval runtimes plus shell/PTY | Medium-high |
| Context control/memory | Automatic structured compaction and TUI prompt undo; no durable project memory or model-facing checkpoint/rewind tools | Checkpoint/rewind and project memory backends | Medium-high |
| Provider policy | Good registry and several adapters; no role routing/fallback chain | 40+ providers, model roles, fallback and credential rotation | Medium |
| Rich inputs/resources | Text-only message parts and local-text read | Images, PDFs, archives, SQLite, notebooks, internal URIs, rich extraction | Medium |
| Extension ecosystem | MCP, Markdown skills/agents, provider factory seams; no general in-process plugin loader | TS extensions with tools, hooks, commands, hotkeys and TUI primitives | Medium; high only if ecosystem growth is a goal |
| Editor/automation | Headless CLI but no RPC server, ACP, or high-level SDK | RPC, ACP and Node SDK | Medium-high for adoption and integration |
| Collaboration | Local multi-host persistence only | Live terminal/browser collaboration | Low for core agent quality, high for team product positioning |
| Sandbox | Permission gates and path/SSRF defenses, but Bash retains user-level host authority | Also not established here as a complete OS sandbox; worktrees isolate files, not authority | Important security gap, not a proven parity difference |
| Distribution | Linux/macOS TUI binaries; no Windows/package-manager/desktop release pipeline | macOS/Linux/Windows plus installer, Homebrew, Bun/npm and mise | Medium for adoption, low for reasoning quality |

The absence of native code-intelligence tools was checked against the complete production tool registration in `internal/wiring/wiring.go`; workspace searches found only MCP examples for Playwright, not built-in LSP/DAP/AST/browser implementations.

## Recommended roadmap

### P0 — Build a harness evaluation loop before expanding the tool list

Create a reproducible suite of real Atenea tasks that measures pass rate, tool retries, invalid calls, edit recovery, tokens, latency, cost, and permission interruptions. Include Go and frontend tasks from this repository, plus concurrency and context-overflow cases. Compare changes against fixed model snapshots.

Why first: without this, “as good as oh-my-pi” reduces to feature counting. oh-my-pi's most transferable advantage is that it tested a harness decision and found a large model-independent gain.

### P1 — Add LSP-backed diagnostics, navigation, and semantic rename

Start with one deep `lsp` tool rather than separate shallow tools. It should expose diagnostics, definitions/references, symbols, and rename, and it should feed diagnostics after writes/edits. Reuse installed language servers instead of implementing language semantics.

Status: implemented for the Go core, standalone CLI, and TUI. The unified `lsp` tool lazily reuses installed language servers for diagnostics, definitions, references, document/workspace symbols, and semantic rename. Successful `write` and `edit` calls append available diagnostics. Rename is classified as a file-writing operation per call, validates all workspace edits before applying them, and rolls back multi-file commit failures. Supported server mappings are gopls, rust-analyzer, typescript-language-server, pyright-langserver, and clangd.

Why second: this changes the agent from text manipulation to code-aware iteration and benefits almost every nontrivial coding task. DAP and AST mutation can follow only if evals show additional value.

### P2 — Complete delegated execution

Extend the existing subagent runtime with:

1. schema-validated final results;
2. request, wall-clock, and token budgets;
3. detached/background jobs with wait/cancel/status;
4. optional Git worktree isolation for concurrent writers;
5. per-role model selection;
6. compact progress/usage summaries.

Do not start with a swarm DSL. A reliable `task` plus background supervision and typed output covers most value. Add DAG orchestration only after real missions demonstrate that parent-directed parallel calls are insufficient.

Status: implemented for the Go core, standalone CLI, and TUI. `task` accepts an
optional JSON Schema for its final result plus request, cumulative token, and
wall-clock budgets. Detached calls run under a process-owned supervisor and are
controlled through `task_status`, `task_wait`, and `task_cancel`; status and wait
report compact request/token/tool/duration usage. Synchronous settlements persist
the same summary in session attrs, and the TUI replaces live child activity with
that durable compact line. Agent manifests can select a model on the active
provider without mutating the parent's selection. `worktree: true` creates a
detached Git worktree with root-bound native tools, retains successful work for
integration, and discards failed worktrees; typed isolated results use a stable
`{"result": ..., "worktree": ...}` envelope so artifact location stays outside
the validated child payload. No swarm DSL or DAG orchestration was added.

### P3 — Expose context pruning and project memory deliberately

Atenea's transactional compaction is already a strong base. Add model/user-facing checkpoint and rewind semantics across both hosts, then a small project-scoped memory contract with explicit retain/recall and provenance. Memory must not silently become prompt truth; recalled facts should identify source and age.

**Implemented (2026-08-02, Go core and standalone TUI):** `/checkpoint` and
`/rewind` expose durable conversation/workspace checkpoints to users, while the
`checkpoint` and `rewind` tools expose the same semantics to the model. Project
memory is explicit through `retain_memory` and `recall_memory`; facts are scoped
to the normalized workspace root, persist in the session SQLite database, and
every recall includes source, retention timestamp, and age. Memory is never
injected into the system prompt or recalled automatically.

### P4 — Add role routing and resilient provider fallback

Use the existing provider registry and capability metadata to configure `default`, `fast/cheap`, `deep`, `plan`, and `review` roles. Add explicit fallback only for retryable transport/quota failures, preserving session affinity and recording the model transition durably.

### P5 — Choose an integration strategy: ACP/RPC before a custom plugin runtime

A JSONL RPC mode or ACP adapter over the existing headless service gives editors and external automation a stable language-neutral surface. Continue using MCP for out-of-process extensions. Build a Go in-process plugin system only if a concrete extension cannot be expressed through MCP, skills, agents, provider factories, or RPC; Go plugin portability and version coupling make it a poor default ecosystem boundary.

### P6 — Broaden distribution and UX parity

Package the Wails desktop app, add Windows builds if supported end to end, generate shell completions, and consider Homebrew only after the product surface is stable. Bring TUI-only undo/checkpoint behavior to desktop. These improve adoption and consistency but should not displace P0–P3.

## What not to copy yet

- Do not port all 32 tools; every advertised tool consumes schema/context and maintenance attention.
- Do not embed Bash or rewrite hot paths in Rust without measured latency showing process startup is a bottleneck.
- Do not build swarm orchestration before typed, budgeted, supervised single-child delegation is proven.
- Do not add collaboration before editor/automation integration unless collaboration is a product goal independent of agent quality.
- Do not treat provider count as model quality; Atenea's registry already permits broad OpenAI-compatible coverage.
- Do not claim a sandbox from permissions or worktrees. A real sandbox needs an OS-level authority boundary.

## Bottom line

oh-my-pi is strong because it systematically reduces friction between model intent and verified action: safer edits, semantic code tools, fast feedback, isolated delegation, controlled context, model routing, and broad integration surfaces. Its reputation is partly justified by concrete architecture and partly amplified by a very large feature surface and self-reported benchmarks.

Atenea's opportunity is favorable: the difficult durable core already exists. The shortest credible route to comparable effectiveness is to make improvements measurable, add IDE-grade semantic feedback, and deepen the existing subagent and context systems. Chasing full feature parity first would make Atenea larger, not necessarily better.

## Primary sources

- [oh-my-pi repository and README](https://github.com/can1357/oh-my-pi)
- [oh-my-pi coding-agent development guide](https://github.com/can1357/oh-my-pi/blob/main/packages/coding-agent/DEVELOPMENT.md)
- [oh-my-pi task runtime](https://github.com/can1357/oh-my-pi/tree/main/packages/coding-agent/src/task)
- [oh-my-pi hashline package](https://github.com/can1357/oh-my-pi/tree/main/packages/hashline)
- [oh-my-pi swarm extension](https://github.com/can1357/oh-my-pi/tree/main/packages/swarm-extension)
- [The Harness Problem](https://blog.can.ac/2026/02/12/the-harness-problem/)
- [Atenea agent loop](../architecture/agent-loop.md)
- [Atenea read/edit architecture](../architecture/read-edit-tools.md)
- [Atenea tool capabilities](../architecture/tool-capabilities.md)
- [Atenea MCP architecture](../architecture/mcp.md)
- [Atenea subagent research](harness-subagents.md)
