---
updated_at: 2026-07-09
summary: Research on harness construction patterns for subagents.
---

# Subagents: how to build the harness (oh-my-pi and opencode)

Investigated on 2026-06-23 on the official repos `can1357/oh-my-pi` (omp) and
`anomalyco/opencode` (formerly `sst/opencode`), plus the `az9713/oh-my-pi` fork that
documents the internals of the harness. The objective is to understand how
subagents are added and how to transfer that pattern to Atenea (Go + Wails).

## The central idea (valid for both)

A subagent **is not a different agent**: it is the **same agent loop run
again**, with a different triplet `(modelo, set de tools, system prompt)` and in
an **isolated context** (own session, own context window). It is not a
separate OS process: both run it **in-process**. The parent agent only sees
the **final result**, not the child's internal context.

The invocation is **one more tool**: the parent LLM calls a tool `task`
(in opencode) / `task` (in omp) with a `subagent_type` and a `prompt`. The
description of that tool is **dynamically generated** from the registry of
available subagents. Optionally the user can invoke a direct one with
`@nombre`.

## Distilled common pattern (the invariants)

1. **Subagent = reentrant loop.** Reuse the same engine; change the triple
 `(modelo, tools, prompt)` and the session/context.
2. **Invocation via tool `task`.** Minimum parameters: `subagent_type` +
 `prompt`. The tool description lists the available agents.
3. **Context isolation.** The child starts a new session; the parent only
 receives the final report (last text or structured output). It is **stateless**:
 the parent cannot send follow-ups; the son communicates through a single report.
4. **Recursion control.** Depth counter; When reaching the maximum,
 **removes the tool `task`** from the child's set so that it does not nest infinitely.
 - opencode forces `task: false` (and also `todowrite/todoread: false`).
 - omp: `task.maxRecursionDepth` (default 2); `childDepth = parentDepth + 1`;
     si `atMaxDepth`, se elimina `task`.
5. **Tools and permissions per agent.** Each definition declares its subset of tools and
 permissions. Read-only ones (`explore`, `plan`) deny `edit/write/bash`.
 Subagents are turned off `todo` so as not to dirty the parent's list.
6. **Agent definitions = frontmatter + body.** Markdown with frontmatter
 (`name`, `description`, `tools`, `model`, `mode`/`spawns`, `thinkingLevel`) and
 the body as a system prompt. The filename is the identifier.
 - opencode: `.opencode/agents/*.md` (+ JSON config).
 - omp: embedded defs (`task`, `quick_task`) and `.md` (`explore`, `plan`,
     `designer`, `reviewer`, `librarian`, `oracle`).
7. **Parallelism.** The parent can launch several `task` at the same time; the
 harness runs with a concurrency pool. opencode recommends it explicitly in the
 description of the tool ("launch multiple agents concurrently whenever
 possible").
8. **Progress and events.** The child broadcasts progress to the parent, coalesced
 (omp: 150 ms), with lifecycle events over a dedicated channel. The
 results are aggregated by index.
9. **Structured output (optional but useful).** omp closes with a tool
 `submit_result`/`yield` validated against a schema (a failure gives outcome
 `schema_violation`). opencode returns the last text + a `metadata.summary`
 from the tool calls.
10. **Shared resources.** Child reuses MCP connections, auth and registry from
    modelos del padre. Sin UI, corre en modo "yolo": el llamado a `task` del
    padre **es** la frontera de autorizacion.
11. **Budgets and abort.** omp: soft request budget (default 90,
    abort a 1.5x), limite de wall-clock (`task.maxRuntimeMs`), razones de abort
    tipadas (`signal | terminate | timeout | budget`).

## opencode — how does

Client/server architecture based on Effect. Distinguishes **primary agents**
(`build`, `plan`) from **sub-agents** (`general`, `explore`, `scout`). Scheme of
agent in `agent.ts` (`Info`): `mode` (`primary` | `subagent` | `all`), `model`,
`prompt`, `permission` (a `Permission.Ruleset`), `steps` (max iterations).

The tool `task`:

- Its description is dynamically assembled with the list of non-primary subagents.
- `execute(params)`:
 1. `Agent.get(params.subagent_type)` resolves the agent.
 2. `Session.create(ctx.sessionID, ...)` creates a **child session** linked to
     padre (contexto aislado).
3. `Session.prompt(...)` runs the prompt with `modelID/providerID` from the agent,
     tools del agente con `todowrite/todoread/task: false` forzados (corta
     recursion) y el `prompt` del usuario como parte de texto.
4. Returns the last text as `output` and summarizes the tool calls in
     `metadata.summary`.
- The child loop is the same as any agent: `Session.prompt` ->
 `streamText` (AI SDK), with `stopWhen` controlling the cut.

Permissions: deep merge of hardcoded defaults + agent defaults + user config
. Agent filtering examples: `plan` denies all `edit`
except `.opencode/plans/*.md`; `explore` is readonly
(`grep/glob/list/bash/read` + web); `general` denies `todowrite`. Hidden system agents
: `compaction`, `title`, `summary`.

## oh-my-pi (omp) — as it does

Monorepo TS + Rust cores (`pi-natives`). The loop lives in
`packages/agent/src/agent-loop.ts` (`agentLoop()`), with `Agent.prompt()`
queuing messages. Tools concurrency modes: `shared` (parallel, default)
and `exclusive` (runs alone). Messages in queue: **steering** (interrupts after the current
tools batch) and **follow-up** (continues when the agent has no more
work).

Subagents at `packages/coding-agent/src/task/` (reveal files:
`executor.ts`, `parallel.ts`, `isolation-runner.ts`, `worktree.ts`,
`subprocess-tool-registry.ts`, `agents.ts`, `discovery.ts`, `output-manager.ts`).

- **Spawn** (`runSubprocess(options): Promise<SingleResult>` in `executor.ts`):
 runs the **in-process** subagent in the main thread and forwards events. The
 session is created with `createAgentSession(buildSubagentSessionOptions(...))`; the
 `SessionManager` is file-backed (`SessionManager.open`) or
 `SessionManager.inMemory(cwd)`. Isolated Settings with `createSubagentSettings`,
 notable `"tools.approvalMode": "yolo"` (no UI; parent `task` is the
 authorization).
- **Depth**: `maxRecursionDepth` (default 2); at maximum the tool
 `task` is removed. Child Tools derived from `agent.tools`; `irc` is always appended;
 `exec` is expanded into `eval`/`bash`; those of the father like `todo` are filtered.
- **Result**: `driveSessionToYield(...)` sends the prompt, waits idle and
 remembers to do `yield` (up to `MAX_YIELD_RETRIES = 3`). The payload is validated
 against an optional schema; If the yield is missing there is fallback and warnings. Canceled
 -> rescues the last text of the wizard.
- **Progress**: `createSubagentRunMonitor(...)` creates a `SubagentRunMonitor` that
 subscribes to session events (tool counts, tokens, `Usage`) and emits by
 `onProgress` + `EventBus`, coalesced at 150 ms; lifecycle by
 `TASK_SUBAGENT_LIFECYCLE_CHANNEL`.
- **Agent definition**:
  ```ts
  interface AgentFrontmatter {
    name: string; description: string;
    tools?: string[]; spawns?: string;       // "*" = puede invocar a cualquiera
    model?: string | string[];
    thinkingLevel?: string; blocking?: boolean;
  }
  ```

**Parallelism** (`parallel.ts`) — pool of workers, not fixed batches:

```ts
export async function mapWithConcurrencyLimit<T, R>(
  items: T[],
  concurrency: number,
  fn: (item: T, index: number, signal: AbortSignal) => Promise<R>,
  signal?: AbortSignal,
): Promise<ParallelResult<R>>
```

Clamp `max(1, min(concurrency, items.length))`, workers that pull tasks from a shared
`nextIndex++`, results mapped by index. **Fail-fast**: the first
error triggers an internal `AbortController` (`AbortSignal.any([signal, internal])`)
and propagates; abort no-error returns partial results with `aborted: true`.
There is also a `Semaphore` FIFO for non-precollected work.

**Advanced orchestration** (`packages/swarm-extension`): you define a swarm in YAML;
the orchestrator builds a DAG from `waits_for`/`reports_to`, does **topological
sort** and groups in **waves** (same wave = parallel; waves in sequence). Modes:
`pipeline` (repeats the graph `target_count` times), `sequential` (default),
`parallel`. Communication between agents does **not** pass data through the orchestrator:
it is through the **shared filesystem** (signal files, structured output, tracking
files). Files: `dag.ts`, `executor.ts` (uses `runSubprocess`), `pipeline.ts`,
`state.ts` (states `.swarm_<name>/`). Additionally, dropping the word `orchestrate`
puts omp into a multi-phase contract of parallel subagents, and the `irc`
 tool allows messaging between agents within the same run.

## Key differences

| Appearance | opencode | oh-my-pi (omp) |
| --- | --- | --- |
| Base | Effect, client/server | Monorepo TS + Rust natives |
| Son | `Session.create(parent)` | `runSubprocess` in-process |
| Result | last text + `metadata.summary` | `yield`/`submit_result` with schema |
| Recursion | `task: false` in the son | remove `task` from `maxRecursionDepth` |
| Extra insulation | daughter session | **git worktree** option per agent |
| Inter-agent | via father | tool `irc` + swarm (filesystem) |
| Orchestration | simple delegation | swarm-extension (DAG/waves in YAML) |

## Integration in Atenea (mapping to what already exists)

Atenea already has all the parts for this (no need to invent a new engine):

- `internal/session/runner`: the loop (`Run`, `runTurn`), `Inbox` (queue/steer),
 `MaxSteps`. **A subagent = another `Run` with its own session.**
- `internal/tool`: `Registry.Materialize(perms) -> Definitions + Settle`,
 `ToolOutputStore`. **The `task` tool is another builtin.**
- `internal/session` Store event-sourced (`AppendEvent/Seq/Messages(sinceSeq)`,
 `inMemory`): the child session uses its own **Store in memory** = isolated
 context, equivalent to `SessionManager.inMemory`.
- `internal/event`: `EventBus` + Wails border (`runtime.EventsEmit`). The child
 broadcasts to a child EventBus; the parent forwards a subset as progress.
- `internal/skill`: now discovery of `.md` skills with on-demand body. **Reuse
 that pattern** to discover agent definitions `.md` (frontmatter + body).
- Permissions: `PermissionGate`, `Tool.Permission.Requested`,
 `ResolveToolPermission`. The child can run with auto permissions (toolset already
 limited) or upload the request to the parent's gate.

### Proposed design of the `task` tool

A builtin in `internal/tool` whose `Settle`:

1. Solve the definition for `subagent_type` (from a `internal/agent` or
 extending `internal/skill`).
2. Create a **child session**: new `SessionID`, `Store` in new memory.
3. Materializes a child `Registry` with the subset of tools and permissions of the agent;
 **if it is `maxDepth`, it does not include `task`** (thread `taskDepth` by
 `context`).
4. Resolves model/provider of the agent (default: the parent).
5. Run `runner.Run` to idle (or to a `submit_result` builtin with schema).
6. Returns the final report as `tool.Result` to the parent.

### What is almost free

- **Parallelism**: the M5 roadmap already records several concurrent tool calls with
 `errgroup`. If the LLM emits N `task` in a step, they already run in parallel. Only
 is missing a **concurrency cap** (equivalent to `mapWithConcurrencyLimit`).
- **Interruption/abort**: M8 already cancels for `context`; the son inherits `ctx`.
- **Streaming events**: M3/M9 already translate `llm.Event -> SessionEvent`. Add
 `Task.Started/Progress/Ended` coalesces to parent.

### Decisions to make (where omp and opencode differ)

- **Output**: free text (opencode) vs `submit_result` with schema (omp). The
 schema is more testable and deterministic for Go -> recommended.
- **Permissions boundary**: child in "yolo" with bounded toolset (omp) vs proxy
 each permission to the parent's gate (more secure, more noise). Start with toolset
 bounded + auto-allow inside it.
- **FS isolation**: per-agent worktree (omp `worktree.ts`) only if the
 sub-agents write in parallel and would collide; at first it is not necessary.

## Proposed TDD roadmap (AGENTS.md style)

```text
S0 Tipos: AgentDef (frontmatter+prompt), TaskDepth en context
S1 Registro/discovery de agentes (.md) reusando internal/skill
S2 task tool: un hijo con Store en memoria, Settle devuelve reporte (fakes)
S3 Control de recursion: maxDepth quita task del set del hijo
S4 Tool/permiso por agente: read-only deniega edit/write/bash
S5 submit_result builtin + validacion de schema (schema_violation)
S6 Concurrencia: N task en paralelo con cap (errgroup + limite)
S7 Progreso: Task.Started/Progress/Ended coalescidos al EventBus padre
S8 Abort/budget: cancelacion por ctx, limite de pasos del hijo
S9 Cableado Wails: roster de subagentes visible en UI (como Ctrl+S de omp)
```

Each milestone 100% testable against fakes (Provider scriptable + EventBus fake),
with `-race` concurrently (S6), before touching the UI.

## Sources

- omp repo: https://github.com/can1357/oh-my-pi
- omp DEVELOPMENT.md: https://github.com/can1357/oh-my-pi/blob/main/packages/coding-agent/DEVELOPMENT.md
- omp `src/task/` (executor, parallel, agents, worktree, isolation-runner)
- omp swarm-extension README: https://github.com/can1357/oh-my-pi/tree/main/packages/swarm-extension
- Fork with internals: https://github.com/az9713/oh-my-pi/blob/main/../ARCHITECTURE.md
- opencode repo: https://github.com/anomalyco/opencode
- opencode agent system (DeepWiki): https://deepwiki.com/sst/opencode/3.2-agent-system
- Deep-dive subagents: https://cefboud.com/posts/coding-agents-internals-opencode-deepdive/
- Issue origin subagents: https://github.com/sst/opencode/issues/1293
- Internal context: `../architecture/agent-loop.md`, `../plans/agent-loop-roadmap.md`,
 `../architecture/opencode-architecture.md`
