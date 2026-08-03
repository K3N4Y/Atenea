---
updated_at: 2026-08-02
summary: Research on Claude Code ultracode, Claude and Qwen dynamic workflows, comparable orchestration systems, and a concrete implementation path for Atenea.
---

# Ultracode and dynamic workflows

## Question

How do Claude Code **ultracode**, Qwen Code **Dynamic Workflows**, and similar coding-agent orchestration systems work, and how could Atenea implement the useful part safely?

This report was checked on 2026-08-02 against first-party documentation and source code. Claude Code behavior comes from Anthropic's current product documentation. Qwen Code behavior comes primarily from the official open-source repository because its public README advertises Dynamic Workflows but does not explain the runtime in detail. Existing Atenea documents and source are used only for the proposed integration.

## Executive verdict

`ultracode` is an official, recent Claude Code session setting, not UltraClaude and not a synonym for `ultrathink`. It combines model effort `xhigh` with automatic Dynamic Workflow orchestration for substantive tasks. The model decides when a workflow is warranted, plans it, emits a JavaScript orchestration program, and hands that program to a separate workflow runtime. The runtime can fan out up to 16 agents concurrently and 1,000 in total, keep intermediate results outside the parent conversation, expose progress by phase, and replay a completed prefix when a run is resumed in the same session.

Qwen Code has independently converged on almost the same architecture and exposes more implementation detail. Its model-facing `Workflow` tool accepts inline JavaScript or a saved script. A hardened `node:vm` context exposes only orchestration globals such as `agent`, `parallel`, `pipeline`, `phase`, `log`, nested `workflow`, `args`, and `budget`; all filesystem, shell, web, and MCP effects happen through normal subagents. It adds structured outputs, per-agent model and agent-type routing, optional Git worktrees, a shared concurrency limiter, a soft output-token budget, a stall watchdog, a prefix journal for resume, terminal snapshots, background supervision, and permission bubbling.

The important innovation is **not JavaScript**. It is the separation of control planes:

```text
conversation agent
  -> produces/chooses an orchestration program
  -> workflow runtime validates and supervises it
  -> leaf agents reuse the ordinary agent loop
  -> only compact typed results return to the parent
```

Atenea already owns most of the expensive leaf-runtime capabilities: a durable loop, isolated child sessions, typed subagent output, bounded recursion and concurrency, detached supervision, worktree isolation, permission propagation, usage summaries, checkpoints, and an open event taxonomy. The missing piece is the workflow control plane.

The recommended implementation is **not** to run model-generated JavaScript. Add a versioned declarative workflow plan plus a host scheduler above the existing `TaskTool`. Let the coordinator create and revise that plan through validated tools with compare-and-swap revisions. This retains dynamic decomposition, fan-out, joins, review gates, cancellation, and observability without creating a second general-purpose code interpreter or a second agent loop.

## 1. Terminology

### 1.1 Ultracode

Anthropic documents ultracode under Claude Code's effort configuration:

- It is a **Claude Code setting**, not a model effort level.
- It sends `xhigh` effort to the model.
- It asks Claude to plan and orchestrate Dynamic Workflows for substantive tasks.
- It is enabled with `/effort ultracode`, `claude --effort ultracode`, or the corresponding Agent SDK session control.
- It is session-only; persistent `effortLevel` and `CLAUDE_CODE_EFFORT_LEVEL` do not accept `ultracode`.
- If workflows are unavailable, the setting falls back to `xhigh` only.
- It requires Claude Code v2.1.203 or newer through the effort interface.

`xhigh` alone means deeper model reasoning. `ultracode` means `xhigh` **plus** workflow orchestration. `ultrathink` is a separate prompt keyword for deeper reasoning on one turn and does not itself select the API effort level or enable workflows.

An ultracode session is exempt from the normal limit of 20 concurrently running ordinary subagents. Workflow agents have their own runtime limits. This exemption should not be read as unbounded execution: Dynamic Workflows document a 16-agent concurrency cap and a 1,000-agent per-run cap.

Sources: [Claude model configuration][claude-model-config], [Claude subagents][claude-subagents].

### 1.2 Dynamic Workflow

A Dynamic Workflow is an orchestration program generated or selected at runtime. It is above an agent loop rather than a replacement for one:

- the **workflow runtime** owns topology, scheduling, joins, budgets, cancellation, replay, and progress;
- each **agent node** runs a normal model/tool loop in an isolated context;
- the **parent conversation** receives a compact final result instead of every child transcript.

“Dynamic” can describe several different properties:

1. The model decides whether a workflow is needed.
2. The model generates a task-specific program rather than selecting a fixed DAG.
3. JavaScript control flow can branch or loop based on intermediate results.
4. Workers or tasks can be added and assigned while the run is active.
5. A persistent goal can force the ordinary loop to continue until independent verification accepts completion.

Claude and Qwen implement several of these, but they are separate mechanisms and should remain separate concepts in Atenea.

## 2. Claude Code: ultracode and Dynamic Workflows

### 2.1 Activation and planning

A user can request a workflow explicitly by saying “use a workflow” or including `ultracode` in an eligible human-originated interactive prompt. In ultracode effort mode, Claude considers a workflow for every substantive task and may generate several sequential workflows—for example, understanding, modification, and verification.

Claude generates both the phase plan and JavaScript. Before launch, interactive users can inspect the phase plan, view or edit the raw script, approve once, approve the named workflow for that project, or deny it. Launch approval depends on the session permission mode; ultracode combined with Auto mode skips launch approval. Individual agent tool calls still follow tool permissions.

The exact classifier for “substantive” or “warrants a workflow” is not public. Neither is the planner prompt.

### 2.2 Runtime model

The generated program is plain JavaScript with top-level `await`. A saved workflow has metadata followed by executable code:

```js
export const meta = {
  name: 'audit-routes',
  description: 'Audit every route handler for missing auth checks',
}

const findings = await pipeline(routes, async (route) =>
  agent(`Audit ${route}`, { schema: findingSchema })
)
return findings
```

The public page shows `agent(prompt, options)`, `pipeline(items, callback)`, and structured `args`; it also states that scripts can use loops and branches. Workflow code cannot read files or execute shell commands directly. It delegates all effects to agents, which remain inside Claude Code's normal tool and sandbox controls.

Each agent normally uses the session model, although the script can route a stage to another model. `CLAUDE_CODE_SUBAGENT_MODEL` overrides script routing. A JSON-Schema-like `schema` option makes an agent return structured data suitable for later script steps.

### 2.3 Execution and context

The workflow runs outside the conversation loop and keeps intermediate results in script variables. This provides two benefits:

- Fan-out does not flood the main model context with every worker trajectory.
- Deterministic code performs mapping, filtering, joining, and routing without another LLM turn.

The runtime reports run, phase, and agent progress, including agent counts, elapsed time, and token usage. Users can inspect an agent's prompt, recent calls, and result, and can stop or restart it. The final workflow value is delivered back into the session.

Despite documentation calling this background execution, ordinary user input is not accepted mid-run; only permission prompts can interrupt it. Human approval between stages therefore requires separate workflows rather than an in-script human node.

### 2.4 Limits, persistence, and recovery

Documented hard limits:

- up to 16 concurrent workflow agents, possibly fewer based on CPU count;
- up to 1,000 total agents per run.

A size guideline advises the planner but is not an enforcement mechanism. The runtime warns above 25 agents or a projected 1.5 million tokens, except in ultracode mode. Anthropic does not document a hard token, money, wall-clock, memory, or disk budget per workflow.

Every run writes its script below `~/.claude/projects/`. Agent results are journaled so a run can resume in the same session. Replay follows agent-start order and stops at the first incomplete call; that call and every later one run again. An interrupted run does not resume across Claude Code sessions. Generated scripts can be saved under project or personal `workflows/` directories and become reusable slash commands.

### 2.5 Security boundary

Claude's design puts arbitrary effects behind agents rather than directly in the script, but the public documentation does not specify the JavaScript sandbox's exact process, VM, network, module, or memory boundary. Workflow agents always use `acceptEdits`, inherit the user's tool allowlist, and auto-approve file edits. Non-allowlisted shell, web, and MCP calls may prompt in interactive execution; noninteractive hosts rely on configured permission rules.

That means workflow launch approval is not a substitute for per-effect policy. It authorizes orchestration; tools still authorize authority.

Source: [Claude Dynamic Workflows][claude-workflows].

## 3. Qwen Code Dynamic Workflows

### 3.1 What is public and what is marketing

The official Qwen Code README advertises “Auto-Memory, Auto-Skills, SubAgents, Agent Teams, and MCP. Dynamic workflows, zero setup,” and lists built-in `/review`, `/batch`, `/loop`, and `/bugfix` skills. The README does not define Dynamic Workflows. The actual contract is in the open-source implementation under `packages/core/src/agents/runtime` and `packages/core/src/tools/workflow`.

The current implementation was active on 2026-08-01: the latest commit touching the orchestrator at the time of research was `f62fc76533442486ad545c15de123b797283b6aa`, “feat(workflows): bubble workflow agent approvals.”

Sources: [Qwen Code README][qwen-readme], [Qwen workflow tool][qwen-workflow-tool], [Qwen workflow orchestrator][qwen-orchestrator].

### 3.2 Model-facing contract

The `Workflow` tool accepts:

```text
script             inline JavaScript generated by the model
scriptPath         absolute path to a saved .js workflow; XOR with script
args               structured JSON exposed as the args global
resumeFromRunId    wf_<hex> run to replay
run_in_background  return a handle immediately in the interactive TUI
```

Inline authoring is explicitly the LLM path. The source description teaches the model the runtime grammar, concurrency semantics, schemas, worktree behavior, replay, and important failure modes.

The script is wrapped in an async IIFE and can use:

- `agent(prompt, opts)` for one leaf agent;
- `parallel([thunk, ...])` for fan-out and barrier join;
- `pipeline(items, ...stages)` for staggered per-item stages without inter-stage barriers;
- `workflow(nameOrRef, args)` for one level of saved-workflow composition;
- `phase(title)` and `log(value)` for observability;
- `args` for invocation input;
- `budget.total`, `budget.spent()`, and `budget.remaining()`.

`agent` options include label, phase, JSON schema, model, declarative `agentType`, Git-worktree isolation, and stall threshold.

### 3.3 Scheduling semantics

All leaf dispatches share one limiter. Default concurrency is:

```text
max(1, min(16, CPU count - 2))
```

It is configurable and hard-capped at 64. The default maximum is 1,000 `agent()` calls per run and has a hard ceiling of 10,000.

`parallel` uses an all-settled barrier. It preserves input ordering; a failed or non-serializable branch becomes `null`, while global abort rejects the batch. `pipeline` creates one chain per item: item A can reach stage three while item B remains in stage one. `null` or failure drops only that item.

This errors-as-data design protects sibling branches but can hide an important failure if the generated script forgets to inspect `null`. The workflow program, not the runtime, decides whether partial failure is acceptable.

### 3.4 Leaf-agent execution

The fast path creates a headless agent with:

- 50 maximum turns;
- a 10-minute maximum time;
- no `AskUserQuestion`, `SendMessage`, monitor, plan lifecycle, or nested `Agent` tool;
- a system contract that treats final text as a return value.

When a schema is supplied, the runtime injects a synthetic `structured_output` tool. The child must call it with a valid object. The runtime nudges validation failures and terminates after two failed corrections. Declarative agent definitions can override prompt, model, tools, and hooks, but the workflow's forbidden-tool floor remains.

Each dispatch can choose a model or agent role. `isolation: 'worktree'` provisions a Git worktree and rejects a dirty parent checkout. Clean unused worktrees are removed; changed or ambiguous worktrees are preserved for integration.

### 3.5 Sandbox

Qwen executes scripts inside a fresh Node `vm` context. It does not expose `process`, `require`, filesystem, shell, or the temporary host bridge. Host objects cross into the VM only after JSON serialization/revival; bridge containers have null prototypes. `Math.random()` and every `Date` operation throw to keep replay keys deterministic. Arguments reject functions, BigInts, cycles, and nesting deeper than 64.

Runtime bounds include:

- 30 seconds for synchronous VM execution, stopping `while (true) {}` before an `await`;
- 30 minutes whole-run wall clock by default;
- 10,000 phase entries and 10,000 log lines;
- no documented top-level result-size cap in the sandbox module.

This is useful hardening, but `node:vm` is still an interpreter embedded in the host process, not an OS authority boundary. Effects remain safer because scripts cannot perform them directly.

Source: [Qwen workflow sandbox][qwen-sandbox].

### 3.6 Budgets and stall recovery

`QWEN_CODE_MAX_TOKENS_PER_WORKFLOW` optionally applies a soft output-token budget. It is checked before queueing and again after obtaining a concurrency slot. Already admitted concurrent calls can overshoot the total. Cache hits consume neither dispatch count nor budget. The runtime does not expose request or monetary budgets in this component.

A progress watchdog considers streamed text, model rounds, usage, and tool activity. It arms only after first progress, pauses while tools run, defaults to 60 seconds without progress, and retries stall failures up to three total attempts. Parent cancellation, wall-clock expiration, normal agent errors, and schema exhaustion do not retry. Long-running tools are deliberately outside stall detection and rely on their own timeout or parent cancellation.

Sources: [Qwen workflow budget][qwen-budget], [Qwen stall watchdog][qwen-stall].

### 3.7 Journal, snapshots, and supervision

A run has the state machine:

```text
register -> running -> completed | failed | cancelled
```

The in-memory registry tracks current phase, phase history, dispatched/completed agents, total and per-phase tokens, recent logs, pending approvals, background state, result/error, and timing. It caps terminal in-memory history and exposes cancel/status callbacks.

A JSONL journal stores completed leaf calls. Replay derives a rolling key from each agent's prompt and options. The longest unchanged prefix is returned from cache; the first miss invalidates all later replay entries. This conservative rule avoids reusing a later result whose hidden dependency may have changed.

Terminal runs are written as best-effort JSON snapshots containing source script, provenance, phases, usage, logs, result/error, and timing. The registry itself remains process-local; snapshots describe past runs rather than reconstructing live execution. The repository keeps up to 30 snapshots.

Background runs return immediately, appear in the task view, can be cancelled, and inject one completion notification into the conversation. Permission requests from workflow agents can bubble to the leader UI.

Sources: [Qwen workflow registry][qwen-registry], [Qwen workflow snapshot][qwen-snapshot], [Qwen workflow runner][qwen-runner].

## 4. Qwen's adjacent dynamic mechanisms

Dynamic Workflows are not Qwen's only orchestration mechanism.

### 4.1 Skills and `/loop`

Qwen skills use progressive disclosure: metadata is discoverable first, while `SKILL.md` and supporting files are loaded when relevant. Skills may be selected by the model or invoked as slash commands. Path gates can activate a skill after a matching file is touched.

The bundled `/loop` skill creates session-scoped recurring work. The scheduler checks due jobs while the session is idle, enqueues them between turns, supports cron-like intervals, adds deterministic jitter, and caps the session at 50 scheduled tasks. This is temporal orchestration, not multiagent workflow execution.

Sources: [Qwen skills][qwen-skills], [Qwen scheduled tasks][qwen-scheduled].

### 4.2 Agent Teams

Qwen Agent Teams use a leader, reusable workers, a shared task list, per-worker queues, persistent mailboxes, permission bridging, and optional plan approval. Idle workers automatically claim pending unblocked tasks. Failed workers release in-progress tasks. Leader messages outrank peer messages, and workers can remain alive for later assignments.

This is dynamic work stealing and communication, whereas a workflow is a bounded orchestration program whose leaf agents return values. Teams are better for changing shared work and peer coordination; workflows are better for dataflow, bounded fan-out, and reproducible joins.

Source: [Qwen TeamManager][qwen-team-manager].

### 4.3 Persistent Goals

Qwen's `goals` subsystem turns “continue until verified” into durable state rather than prompt advice. A goal has status (`active`, `paused`, `blocked`, `usage_limited`, `complete`), activity (`idle`, `running`, `verifying`), revision, exact turn permit, evidence cursor, turn count, and active time.

Workers can read the current goal and propose `complete` or `blocked` with explicit evidence references. A proposal does not change lifecycle directly. An independent verifier accepts it or rejects it and returns controlled feedback for another turn. Goal ID, revision, and turn ID invalidate stale workers. Mutations are serialized and persistence-first. Automatic continuation stops after a 50-turn budget.

This is not a workflow scheduler. It is a completion gate around an adaptive agent loop, and it is highly relevant to preventing premature “done” claims.

Sources: [Qwen goal protocol][qwen-goal-protocol], [Qwen goal runtime][qwen-goal-runtime], [Qwen goal tools][qwen-goal-tools].

## 5. Comparable systems

| System | Control plane | Dynamic element | Durable state | Best fit |
| --- | --- | --- | --- | --- |
| Claude Dynamic Workflows | Generated JavaScript | Model writes loops, branches, pipelines | Same-session journal; reusable scripts | Large fan-out while preserving parent context |
| Qwen Dynamic Workflows | Generated JavaScript in `node:vm` | Same, plus model/agent/worktree routing | Prefix journal + terminal snapshots | Audits, migrations, structured multiagent dataflow |
| Qwen Agent Teams | Event-driven leader/workers | Work stealing, messages, new tasks | Team files/mailboxes | Collaborative changing work |
| Qwen Goals | Persisted state machine + verifier | Loop continues until evidence is accepted | Goal journal and revisioned permits | Long-running objective with trustworthy completion |
| OpenAI Agents SDK | LLM or code orchestration | Handoffs, chains, evaluator loops, parallel agents | Application-defined | Explicit Python control with agent leaves |
| LangGraph | Explicit graph | Conditional edges, cycles, human nodes | Checkpoints/shared state | Recoverable long-running business processes |
| Athena Graphs | Declarative graph around coding agents | Generated/selected topology, reducers, checkpoints | Detached graph runs | Cross-agent branching and human gates |
| oh-my-pi swarm | YAML DAG in waves | Agent DAG and shared filesystem/IRC | `.swarm_*` files | Multiagent coding with explicit dependencies |
| SkillOpt / Self-Harness | Offline optimization meta-loop | Harness or skill changes between evaluated runs | Candidate versions, traces, rejected edits | Improving a harness, not executing one user task |

The common invariant is that leaf workers still run ordinary agent loops. The systems differ in who controls the outer loop: an LLM, generated code, a graph runtime, a team manager, a verifier, or an offline optimizer.

Sources: [OpenAI orchestration][openai-orchestration], [LangGraph][langgraph], [Athena Graphs][athena-graphs], [oh-my-pi task runtime][omp-task], [oh-my-pi swarm][omp-swarm], [Atenea graph research](2026-07-27-graphs-over-agent-loops.md), and [Atenea SkillOpt research](harness2-skillopt.md).

## 6. What Atenea already has

Atenea should not build another agent engine. Its current primitives already map cleanly:

| Needed workflow primitive | Existing Atenea capability |
| --- | --- |
| Leaf agent invocation | `TaskTool` re-enters the same `Runner` in an isolated child session |
| Typed result | `output_schema` validates child final JSON |
| Role/model routing | Agent manifest `model` plus `ProviderResolver` |
| Tool isolation | Per-agent tool allowlist and bounded delegation depth |
| Parallel execution | Concurrent tool settlement plus `TaskTool` semaphore |
| Cancellation/time | `context.Context`, `timeout_ms`, detached cancel/wait/status |
| Workspace isolation | `worktree: true` environment resolver |
| Permissions | Parent policy/gate propagated to each real child tool call |
| Usage/progress | Request/token/tool/duration settlement summaries |
| Parent durability | Root-session events have a durable monotonic sequence; child stores and full tool outputs are currently in memory |
| Extensible events | `ext.*` / `x-*` event namespaces and `Attrs` |
| Context control | Compaction, checkpoint/rewind, explicit project memory |

Evidence: `internal/session/subagent/subagent.go`, `internal/session/subagent/supervisor.go`, `internal/session/runner/run.go`, `agentcore/session/event.go`, and [Atenea agent-loop architecture](../architecture/agent-loop.md).

Four limitations matter:

1. Ordinary and worktree `TaskTool` children use `MemoryStore`/`MemoryInbox`; only their compact final settlement reaches the durable parent session.
2. `OutputStore` is also process-local. Atenea does not currently retain full child results durably enough to recover a settled workflow node after a crash.
3. Detached task supervision is process-owned and bounded in memory; it is not a durable workflow state machine.
4. `TaskTool` records token usage but intentionally does not enforce token or request budgets. A workflow layer must own aggregate limits if Atenea wants them.

## 7. Recommended Atenea design

### 7.1 Design decision

Implement Dynamic Workflows as a **versioned declarative plan interpreted by Go**, not as JavaScript generated by the model.

Reasons:

- Atenea does not embed a JavaScript runtime today.
- A VM adds a large dependency and a new in-process attack surface.
- Most workflow value comes from agent dispatch, dependencies, fan-out, joins, budgets, progress, and replay—not from arbitrary language semantics.
- A declarative plan is inspectable, schema-validatable, diffable, permission-reviewable, and easy to persist as events.
- Dynamic changes can be expressed through revisioned plan updates instead of unrestricted code execution.

### 7.2 Minimal domain model

Keep contracts under `agentcore/workflow`; implementation belongs under `internal/workflow`, preserving the public-contract boundary.

```go
type Spec struct {
    Version int
    Name    string
    Goal    string
    Nodes   []Node
    Limits  Limits
}

type Node struct {
    ID           string
    Agent        string
    Prompt       string
    Needs        []string
    OutputSchema json.RawMessage
    Worktree     bool
    Timeout      time.Duration
}

type Limits struct {
    MaxNodes       int
    MaxConcurrency int
    MaxDuration    time.Duration
    MaxTokens      int
}
```

A node is deliberately one thing: invoke an existing subagent and produce one typed value. `Needs` defines a DAG. Before dispatch, the scheduler renders predecessor outputs into a stable JSON input envelope rather than interpolating arbitrary strings:

```json
{
  "workflow_goal": "...",
  "node_goal": "...",
  "inputs": {
    "research": {"...": "..."},
    "tests": {"...": "..."}
  }
}
```

The first release should reject cycles, unknown dependencies, duplicate IDs, missing schemas at join boundaries, unsafe shared-workspace fan-out, and any limit above host policy. It should support read-only parallel nodes and sequential root-workspace nodes first. Worktree-producing nodes require the artifact and integration contract in section 7.6 before they can participate in dependencies.

### 7.3 Dynamic revision instead of generated code

Expose these model-facing tools in the dynamic phase:

```text
workflow_start(spec, dynamic=false) -> {run_id, revision, status}
workflow_update(run_id, expected_revision, add_nodes, cancel_pending_nodes)
workflow_finish(run_id, expected_revision)
```

Static `workflow_start` validates and displays the full plan before launch, waits for its fixed graph, and is the Phase 1 behavior. A dynamic start returns control when its current frontier drains into `awaiting_revision`, exposing settled typed results to the coordinator. `workflow_update` is accepted in `running` or `awaiting_revision`, uses compare-and-swap revision, and validates the whole prospective graph before commit. Completed node definitions and outputs remain immutable. Recovery never rewrites them: cancel an obsolete pending node and add a replacement with a new ID. `workflow_finish` explicitly closes an `awaiting_revision` run.

This supplies the useful meaning of “dynamic” without racing terminal completion:

- start with research nodes;
- enter `awaiting_revision` when the current frontier settles;
- inspect typed results;
- add implementation or verification nodes based on evidence, or explicitly finish;
- cancel obsolete pending work and replace it with new nodes;
- never execute arbitrary orchestration code.

`awaiting_revision` has a host-configured deadline and total revision budget. Expiry or budget exhaustion finishes or fails according to the launch policy; it never waits forever for an absent coordinator.

Normal status tools should mirror existing detached-task vocabulary:

```text
workflow_status(run_id)
workflow_wait(run_id)
workflow_cancel(run_id)
```

Do not overload `task_status`; task and workflow have different state and retention contracts.

### 7.4 Runtime state machine

```text
proposed -> awaiting_approval -> running
running  -> awaiting_approval | awaiting_revision | completed | failed | cancelled | budget_exhausted
awaiting_revision -> awaiting_approval | running | completed | cancelled | budget_exhausted
awaiting_approval -> running | awaiting_revision | failed | cancelled
```

Each node has:

```text
pending -> ready -> running -> succeeded | failed | cancelled | skipped
```

Rules:

- A node becomes `ready` only when every dependency succeeded.
- A failed required dependency skips downstream nodes. Recovery adds a replacement node with a new ID and dependencies; it never mutates a settled or skipped node.
- Independent ready nodes run under one workflow semaphore.
- A static run completes when its graph drains. A dynamic run enters `awaiting_revision` instead and completes only through `workflow_finish`, deadline policy, or a terminal failure/cancellation.
- A material revision submitted during `running` pauses new scheduling and enters `awaiting_approval`; already running nodes may settle. Approval revalidates the delta against the new revision/current node states before atomic commit, then returns to `running`. Denial or approval timeout discards only the proposed delta and returns to the prior `running` or `awaiting_revision` state; cancellation remains available throughout.
- Cancellation propagates through one root `context.Context`.
- Terminal transition is one-shot and rejects new revisions.
- Callbacks and UI failures never mutate runtime state.
- Aggregate token accounting is checked before dispatch and updated after every child settlement; concurrent overshoot must be documented if no reservation protocol is added.

Unlike Qwen's errors-as-`null` default, Atenea should make failure explicit in node state. A join may opt into partial inputs in a later version, but silent partial success is a poor default for code changes.

### 7.5 Persistence and replay

Use the existing durable event stream rather than a second SQLite database. Add namespaced workflow events or a dedicated workflow store projection:

```text
Workflow.Created
Workflow.Approved
Workflow.Revised
Workflow.Node.Started
Workflow.Node.Succeeded
Workflow.Node.Failed
Workflow.Budget.Updated
Workflow.Completed
Workflow.Failed
Workflow.Cancelled
```

If these become a public UI/host contract, publish them in `agentcore/session`; if they remain experimental, use `x-workflow.*` first. Events should carry `run_id`, `revision`, `node_id`, compact usage, a durable result reference, and error.

That result reference requires a new durable workflow artifact store. The existing child `MemoryStore` and `OutputStore` are insufficient. A successful node must atomically commit its full typed result or artifact manifest and its terminal node transition, so a crash cannot produce “succeeded” with missing data or persisted data that no state references. Define retention, size caps, checksums, and garbage collection with this store. Child transcripts may remain ephemeral; the node result needed by dependents may not.

Resume should initially be **node-granular**, not Qwen's call-order prefix cache:

- a succeeded node is reusable only when its durable result exists and the canonical hash of its immutable definition, dependency output hashes, selected agent definition, model identity, and workspace baseline still matches;
- a missing or invalid result/artifact never demotes that settled node or reruns its ID: recovery becomes blocked/failed until an approved revision adds a replacement node with a new ID;
- nodes with workspace effects are not considered reusable merely because their text result exists—the retained worktree or verified workspace checkpoint is part of the durable artifact manifest.

This is more expensive than optimistic replay but preserves immutable settlement and avoids treating missing results or stale code edits as valid cached work.

### 7.6 Permission model

Separate two approvals:

1. **Workflow launch approval:** authorizes the displayed topology, maximum concurrency, budgets, agents, and requested worktree policy.
2. **Effect approval:** every child call still passes through Atenea's existing policy and gate with the real tool input.

Never switch children to YOLO merely because a workflow was approved. Atenea already has the safer propagation behavior. Session grants can reduce repeated prompts without weakening the authority boundary.

Every material `workflow_update` needs the same preview/consent semantics as launch. The host shows added/cancelled nodes and the resulting limits before commit. An initial approval may suppress repeated prompts only when it explicitly grants a bounded revision policy: allowed agent names, maximum nodes/revisions/concurrency/tokens/duration, workspace mode, and whether topology expansion is permitted. A revision outside that envelope asks again. Per-tool effect approval remains separate and cannot substitute for topology/resource consent.

Shared-workspace fan-out must be fail-closed. A node qualifies as read-only only when the complete reachable tool set—including MCP tools—positively declares effects and every declaration is harmless to the workspace. `WritesFiles`, `RunsCommands`, an undeclared effect, or any unknown/future effect bit makes the node unsafe for shared parallel execution. Unsafe nodes must run sequentially or in isolated worktrees; never infer safety merely because `write` and `edit` are absent from an agent manifest.

Current Atenea worktrees are created from `HEAD`, not from the user's uncommitted working tree, and a successful task returns only the retained path. Before workflows can schedule worktree writers, define a workspace-artifact contract:

- reject a dirty root or create an explicit checkpoint/snapshot whose intended uncommitted state becomes the worktree baseline;
- persist the worktree path, branch/base identity, and content hash as the node artifact;
- let dependent nodes mount or inspect that exact predecessor artifact rather than creating a fresh worktree from `HEAD`;
- add an explicit integration node or user action for patch/cherry-pick/merge, with conflict reporting and no automatic destructive merge;
- retain successful artifacts until integration or workflow retention expiry, and discard failed artifacts only when their evidence is no longer needed.

Until this exists, workflow v1 should allow read-only parallelism and sequential mutation of the user's current root only. It should not claim safe parallel writers or worktree-to-worktree dataflow.

### 7.7 “Ultracode-like” session policy

Add orchestration policy separately from model effort:

```text
workflow: off | ask | auto
reasoning: provider-specific preference
```

An optional `/workflow auto` mode can add a system instruction:

- use a workflow only when the task has independent branches, a large homogeneous fan-out, or an explicit review/verification boundary;
- keep the ordinary loop for small or tightly coupled tasks;
- scope the plan before dispatch;
- do not create a one-node workflow;
- estimate node count and concurrency before launch.

Do not name this mode `ultracode`: that is Anthropic product terminology and couples orchestration to an effort level that not every provider exposes. Atenea can map a provider's deepest supported reasoning preference independently.

Start with model judgment plus a launch preview. A hard-coded “substantive task” classifier would be brittle and unmeasured.

### 7.8 Verification and goals

After workflows are reliable, add a separate Goal contract inspired by Qwen:

- revisioned objective;
- exact turn permit;
- explicit evidence catalog;
- terminal proposal rather than self-declared completion;
- independent verifier;
- bounded continuation;
- persisted pause/resume/block/usage-limited states.

Do not put this in workflow v1. A workflow coordinates known work; a goal decides whether adaptive work is actually finished. Combining both in one abstraction would make neither contract clear.

## 8. Delivery sequence

### Phase 0 — Measure before productizing

Build a small harness evaluation corpus with tasks that genuinely benefit from fan-out: repository audit, multi-package migration, independent test investigation, and implement/review. Compare ordinary `task` delegation against workflow execution on completion rate, tokens, elapsed time, permission interruptions, edit conflicts, and recovery.

Without this gate, an ultracode-like mode may only spend more tokens.

### Phase 1 — Synchronous declarative DAG

- Add versioned `Spec`, validation, and pure DAG scheduling.
- Reuse `TaskTool` execution rather than calling providers directly.
- Support typed node outputs, dependencies, read-only parallelism, sequential root-workspace mutation, cancellation, and aggregate limits.
- Expose `workflow_start` synchronously and return the final structured result.
- Publish progress through experimental events.

The tracer bullet is `parallel research -> implement -> review`: research branches are positively read-only, implementation mutates the root only after their join, and review runs afterward against that same root. Worktree writers are intentionally deferred until their baseline, artifact, dependency, and integration semantics exist.

### Phase 2 — Durable results and supervision

- Add the durable workflow-node result/artifact store and atomic terminal settlement.
- Add `status`, `wait`, and `cancel`.
- Persist workflow and node transitions.
- Recover interrupted runs without replaying a node only when its durable result and effect artifact prove it settled.
- Store compact terminal snapshots and enforce result/artifact retention.
- Add TUI progress only after the headless runtime is proven.

### Phase 3 — Worktree artifacts and revisioned dynamic expansion

- Add `dynamic=true`, `awaiting_revision`, `workflow_finish`, revision deadlines, and a total revision budget.
- Add `workflow_update` with expected revision and delta preview/approval.
- Permit adding nodes and cancelling pending nodes; replace failed/skipped work with new IDs rather than mutating settled definitions.
- Keep settled nodes immutable.
- Add conservative node replay keyed by inputs, agent/model, and workspace artifact.
- Add dirty-root baseline policy, predecessor artifact mounting, explicit integration, and conflict handling before enabling worktree writer nodes.

### Phase 4 — Auto orchestration policy

- Add session-scoped `workflow=auto`.
- Keep reasoning effort independent.
- Preview topology and limits before launch unless an explicit session policy grants it.
- Measure against Phase 0 baselines and disable auto mode if it regresses quality/cost.

### Phase 5 — Verified goals, only if demanded

Add persistent goal continuation and independent verification as its own module. Do not add Agent Teams or a general scripting VM unless real workloads show that revisioned DAGs and existing detached subagents cannot express them.

## 9. What not to copy

- **Do not embed JavaScript first.** It is a large security and maintenance cost for control flow that a small scheduler can express.
- **Do not remove per-tool permission checks.** Workflow approval is not blanket authority.
- **Do not allow concurrent writers in one checkout.** Use worktrees or reject the plan.
- **Do not expose 1,000-agent defaults.** Atenea's current default concurrency of four is a better initial operational bound.
- **Do not silently convert failed branches to `null`.** Preserve typed failure and require an explicit partial-success policy.
- **Do not persist raw child transcripts in the parent context.** Persist observability, return compact typed values.
- **Do not conflate workflows, teams, scheduled loops, goals, and self-improvement.** They solve different control problems.
- **Do not add an offline self-optimizing harness as part of this feature.** SkillOpt/Self-Harness need evaluation datasets and held-out gates, not a workflow runtime.

## 10. Risks and open questions

1. **Planner quality:** a model can generate a technically valid but wasteful topology. Launch preview and evaluation are required.
2. **Join context size:** typed outputs can still be huge. Node outputs need the same capping/reference strategy as tool outputs.
3. **Replay correctness:** model identity, prompts, agent definitions, dependency outputs, workspace baseline, and retained artifacts all affect validity.
4. **Budget races:** aggregate token enforcement is approximate when several child requests are already in flight.
5. **Provider portability:** per-role model routing must fail clearly when a provider cannot resolve the requested model.
6. **Human gates:** a background run cannot safely block forever waiting for an unavailable UI. Permission requests need timeout/cancel semantics.
7. **Dynamic cycles:** review/revise loops are useful, but unrestricted cycles complicate convergence. Prefer revisioned node expansion with a total-node/turn budget before adding cyclic graph syntax.
8. **Output trust:** child output is data, not authority. It must not be interpolated into system instructions without a stable envelope and explicit provenance.

## Bottom line

Claude's ultracode validates a product direction: a capable coding agent benefits from a second control plane that can generate and supervise task-specific multiagent programs. Qwen's source shows the engineering required to make that credible: bounded dispatch, typed returns, context isolation, a sandbox, worktrees, budgets, stall handling, journals, snapshots, permission bridging, and lifecycle supervision.

Atenea should copy the **architecture**, not the JavaScript. A revisioned declarative workflow runtime over the existing `TaskTool` is the smallest safe design that preserves dynamic planning, parallel work, joins, resumability, and provider neutrality. Add automatic orchestration only after synchronous workflows are measurable and durable; add verified goals separately when premature completion becomes the next demonstrated bottleneck.

## Primary sources

### Claude Code

- [Claude Code model configuration: effort and ultracode][claude-model-config]
- [Claude Code Dynamic Workflows][claude-workflows]
- [Claude Code subagents][claude-subagents]
- [Claude Code skills][claude-skills]
- [Claude Code official repository][claude-repo]

### Qwen Code

- [Qwen Code official repository and README][qwen-readme]
- [Workflow model-facing tool][qwen-workflow-tool]
- [Workflow orchestrator][qwen-orchestrator]
- [Workflow runner][qwen-runner]
- [Workflow sandbox][qwen-sandbox]
- [Workflow budget][qwen-budget]
- [Workflow stall watchdog][qwen-stall]
- [Workflow registry][qwen-registry]
- [Workflow snapshots][qwen-snapshot]
- [Agent Teams manager][qwen-team-manager]
- [Goal protocol][qwen-goal-protocol]
- [Goal runtime][qwen-goal-runtime]
- [Goal tools][qwen-goal-tools]
- [Qwen skills documentation][qwen-skills]
- [Qwen scheduled tasks documentation][qwen-scheduled]

### Comparable orchestration

- [OpenAI Agents SDK multi-agent orchestration][openai-orchestration]
- [LangGraph official repository][langgraph]
- [Athena Graphs official repository][athena-graphs]
- [oh-my-pi subagent runtime][omp-task]
- [oh-my-pi swarm extension][omp-swarm]

[claude-model-config]: https://code.claude.com/docs/en/model-config#adjust-effort-level
[claude-workflows]: https://code.claude.com/docs/en/workflows
[claude-subagents]: https://code.claude.com/docs/en/sub-agents
[claude-skills]: https://code.claude.com/docs/en/skills
[claude-repo]: https://github.com/anthropics/claude-code
[qwen-readme]: https://github.com/QwenLM/qwen-code/blob/main/README.md
[qwen-workflow-tool]: https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/tools/workflow/workflow.ts
[qwen-orchestrator]: https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/agents/runtime/workflow-orchestrator.ts
[qwen-runner]: https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/agents/runtime/workflow-runner.ts
[qwen-sandbox]: https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/agents/runtime/workflow-sandbox.ts
[qwen-budget]: https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/agents/runtime/workflow-budget.ts
[qwen-stall]: https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/agents/runtime/workflow-stall.ts
[qwen-registry]: https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/agents/workflow-run-registry.ts
[qwen-snapshot]: https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/agents/workflow-snapshot.ts
[qwen-team-manager]: https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/agents/team/TeamManager.ts
[qwen-goal-protocol]: https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/goals/goal-protocol.ts
[qwen-goal-runtime]: https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/goals/goal-runtime.ts
[qwen-goal-tools]: https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/goals/goal-tools.ts
[qwen-skills]: https://qwenlm.github.io/qwen-code-docs/en/users/features/skills
[qwen-scheduled]: https://qwenlm.github.io/qwen-code-docs/en/users/features/scheduled-tasks
[openai-orchestration]: https://github.com/openai/openai-agents-python/blob/421deb75061c6dc4e5c8ee2352ef2390413906da/docs/multi_agent.md
[langgraph]: https://github.com/langchain-ai/langgraph
[athena-graphs]: https://github.com/luckeyfaraday/athena-graphs
[omp-task]: https://github.com/can1357/oh-my-pi/tree/main/packages/coding-agent/src/task
[omp-swarm]: https://github.com/can1357/oh-my-pi/tree/main/packages/swarm-extension
