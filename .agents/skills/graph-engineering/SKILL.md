---
name: graph-engineering
description: Run a mission (feature, fix, refactor) through an engineered agent graph — Router → crew (Researcher / Architect / Builder, private work areas) → Shared State → Integrator → Reviewer → Human Checkpoint → Ship. Use when the user invokes /graph-engineering <mission>, or asks to run a task "through the graph", "with the crew", or wants a safe, staged multi-agent delivery of a change.
---

# Graph Engineering

One mission. A crew. An engineered path. You are the **graph runtime**: the graph
decides who runs next — you never skip ahead to writing code before the graph says so.

The mission is given in the skill args (or the user's message). If no mission is
given, ask for one before doing anything else.

## Shared State

All crew results land in one file: `.graph/<mission-slug>.md` (create `.graph/` if
needed and make sure it is gitignored). Structure:

```markdown
# Mission: <one-line mission>
Status: routing | researching | building | integrating | reviewing | checkpoint | shipped

## Facts        (from Researcher — evidence, constraints, relevant code paths)
## Decisions    (from Architect — chosen design, rejected alternatives, why)
## Artifacts    (from Builder — files changed, branch/worktree, how to run it)
## Review       (from Reviewer — verdict, test results, risks)
## Checkpoint   (human decision, if one was required)
```

Update this file after **every** node completes. Agents work in private areas
(their own context, worktree for the Builder); the shared state file is the only
cross-node memory. Never pass raw agent transcripts forward — pass the distilled
state file.

## The graph

### 01 · ROUTER — "where should this go?" (you, inline)

Classify the mission and pick the path. Decide:

- Which crew members are needed. Trivial/mechanical missions may skip Researcher
  or Architect; a pure investigation mission may need only the Researcher.
- Execution order. Researcher and Architect can run **in parallel** (one message,
  multiple Agent calls) when the design doesn't depend on unknown evidence;
  otherwise Researcher first. The Builder always runs **after** their results are
  in Shared State.
- Whether the mission is **high-impact** (see Human Checkpoint) — record this now.

Write the routing decision into Shared State before dispatching anyone.

### 02 · THE CREW — private work areas

Dispatch via the Agent tool. Every crew prompt must include: the mission, the
relevant Shared State content pasted inline, and the instruction to return a
structured summary (not a narrative).

- **RESEARCHER — finds evidence.** `subagent_type: Explore` (read-only). Finds the
  relevant code, prior art, constraints, invariants, and existing tests. Returns
  facts with `file:line` references. → append to **Facts**.
- **ARCHITECT — designs it.** `subagent_type: Plan`. Produces the implementation
  design: files to touch, API shape, trade-offs considered, test plan. → append to
  **Decisions**.
- **BUILDER — creates it.** `subagent_type: claude` with `isolation: "worktree"`
  so its edits can't corrupt the main tree. Its prompt includes Facts + Decisions
  and demands: implement, run the project's tests/lint, and report the diff summary
  and worktree path. → append to **Artifacts**.

### 03 · INTEGRATOR — combines the crew's work (you, inline)

Bring the Builder's changes from the worktree into the working tree (merge or
apply the diff). Reconcile conflicts against **Decisions** — the Architect's
design wins over improvisation. Verify the result compiles/builds. Record what was
integrated in **Artifacts**.

### 04 · REVIEWER — tests quality + safety

Spawn a fresh agent (never the Builder — no self-review) whose prompt is
adversarial: given the mission, Decisions, and the integrated diff, try to find
correctness bugs, missing tests, security issues, and deviations from the design.
It must actually run the test suite and lint, not just read the diff. → write
verdict into **Review**.

If the Reviewer finds real problems: route back — send the findings to a new
Builder pass (edge back to 02), then re-integrate and re-review. Maximum two
repair loops; after that, stop and report honestly.

### 05 · HUMAN CHECKPOINT — approves high-impact

Required when the mission touches: public API or published packages, data
migrations/schema, security/auth, deletion of user data or files, anything
outward-facing (releases, external services), or the Reviewer flagged residual
risk. Use **AskUserQuestion** with a concise summary of the diff, the Reviewer's
verdict, and the options (ship / revise / abort). Record the decision in
**Checkpoint**.

Low-impact missions pass through automatically — note "auto-approved: low impact"
in Shared State.

### 06 · SHIP — verified output

Only reachable through 04 (and 05 when required). Finalize: commit on a
`posthog-code/<mission-slug>` branch if the user asked for commits, otherwise
leave the working tree clean and staged. Set Status to `shipped`. Your final
message to the user leads with the outcome and includes: what shipped, the
Reviewer's verdict, and the path to the Shared State file.

## Rules of the graph

- The graph decides who runs next — never collapse nodes to "save time" unless
  the Router explicitly routed around them.
- Shared State is the single source of truth between nodes; keep it updated or
  the next node runs blind.
- Report failures faithfully: a red test suite or an unresolved review finding is
  reported as such, never shipped quietly.
- Scale with the mission: a one-line fix may legitimately route as
  Router → Builder → Reviewer → Ship. The full crew is for real features.
