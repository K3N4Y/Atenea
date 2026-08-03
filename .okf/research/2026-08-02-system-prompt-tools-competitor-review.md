---
updated_at: 2026-08-02
summary: Comparison of Atenea's runtime system prompts and model-facing tool contracts with oh-my-pi, OpenCode, OpenAI Codex CLI, Claude Code, and Qwen Code, with evidence-ranked improvement opportunities.
---

# System prompt and tool-contract competitor review

## Scope and method

This review compares the **runtime prompt and tool contracts in Atenea's Go core and standalone TUI scope** with primary-source material from oh-my-pi, OpenCode, OpenAI Codex CLI, Claude Code, and Qwen Code.

The comparison separates:

- **Fact** — directly observed in repository code, local documentation, or an official source.
- **Inference** — a design conclusion based on those facts, not a competitor claim.
- **Not established** — a behavior that is proprietary, undocumented, or not verified from the inspected source.

External repositories and documentation change frequently. Competitor observations were checked on 2026-08-02; source links point to the official project locations and should be rechecked before implementation decisions.

## Executive verdict

Atenea's core is stronger than its surface suggests in three areas:

1. **File correctness:** hashline snapshots, freshness checks, seen-line provenance, all-or-nothing patch preparation, and atomic replacement make single-file edits safer than conventional string replacement.
2. **Host enforcement:** permissions derive from declared effects and unknown/MCP tools default to asking when they are registered but undeclared; prompt text is not the security boundary.
3. **Completion discipline:** the default prompt requires research, reproduction for bugs, executable verification, evidence-backed claims, and cleanup after proof.

The most important weaknesses are above the loop:

1. **The strongest prompt is not the prompt every provider receives.** `default.txt` contains the full engineering and delivery contract, but `anthropic.txt` and `local.txt` are much shorter. `newsystempromp.md` is not referenced by the Go prompt assembly, so it is not the runtime source of truth.
2. **Tools are all disclosed at once.** Atenea has progressive disclosure for skills, but `Registry.Materialize` announces every permitted tool and MCP tool in the current registry. Qwen Code and Codex CLI show a stronger deferred-tool pattern for large catalogs.
3. **Descriptions mostly document fields and mechanics, not decisions.** oh-my-pi has an explicit authoring standard: a tool prompt should teach when to use the tool, its input grammar, recoverable failures, anti-patterns, examples, and a short critical recap.
4. **Instruction loading is nearest-file-only.** Claude Code and Codex use cumulative hierarchical instructions and path-scoped rules. Atenea currently returns the first `AGENTS.md` or `CLAUDE.md` found while walking upward.
5. **Delegated results are less standardized than the strongest competitors' specialist contracts.** Atenea already has agent manifests, output schemas, detached supervision, budgets, and worktrees; the opportunity is to make evidence, provenance, state, and verification first-class in the model-facing result.

The recommended strategy is **not feature-count parity**. Preserve Atenea's narrow safety contracts, then add measured improvements to prompt consistency, tool disclosure, instruction scope, delegated evidence, and host-observed verification.

## 1. What Atenea actually runs

### 1.1 Runtime source of truth

**Fact:** `internal/session/prompt/prompt.go:12-22` embeds four prompt files: `anthropic.txt`, `default.txt`, `local.txt`, and `plan.txt`.

**Fact:** `internal/session/prompt/prompt.go:58-83` selects `anthropic.txt` when the model ID contains `claude`, otherwise `default.txt`; local endpoints use `local.txt`. `BuildPlan` and `BuildLocalPlan` add the plan contract.

**Fact:** `internal/session/prompt/prompt.go:88-112` assembles ordered sections: base, instructions, skills, mode, and environment. Empty sections are omitted and equal-order sections are stable.

**Fact:** `internal/wiring/wiring.go:467-520` loads repository instructions once, formats skill metadata once, and builds the prompt per model/turn. The date is rendered dynamically while the stable prefix remains reusable.

**Fact:** A workspace search found no Go/runtime reference to `newsystempromp.md`. It is therefore a useful prompt reference/template, but it must not be treated as the executable prompt source without a deliberate wiring change.

### 1.2 Prompt-variant inconsistency

**Fact:** `internal/session/prompt/default.txt:1-161` contains the full agent loop, tool policy, exploration rules, delegation gates, implementation workflow, verification requirements, delivery contract, completeness rules, evidence rules, and yielding checks.

**Fact:** `internal/session/prompt/anthropic.txt:1-17` contains only tone, objectivity, basic tool preference, batching, read-before-edit, and code-reference guidance.

**Fact:** `internal/session/prompt/local.txt:1-35` contains the tool-calling protocol, basic tool inventory, style, and engineering taste, but not the full default verification and delivery contract.

**Inference:** Claude and local-model sessions do not receive the same operational contract as the default provider path. This may be intentional provider tuning, but it creates a behavioral matrix that is harder to test and can make verification/delegation behavior depend on model selection rather than task mode.

**Opportunity:** split the prompt into a shared invariant core plus provider-specific additions. The shared core should include safety, research, delegation, verification, evidence, and delivery rules. Provider-specific files should only contain protocol/model adaptations.

### 1.3 Existing strengths

- **Stable composition:** `internal/session/prompt/prompt.go:98-112` makes ordering and omission explicit and tests protect it in `internal/session/prompt/sections_test.go:5-69`.
- **Skill progressive disclosure:** `internal/wiring/wiring.go:317-326` sends skill metadata to the prompt while `internal/skill/skill.go:167-196` loads the body through the skill tool on demand.
- **Provider-specific local tool-calling prompt:** `internal/session/prompt/local.txt:1-23` explicitly tells local models to call the function interface rather than print JSON-shaped fake calls.
- **Plan isolation:** `internal/session/prompt/plan.txt:1-8` makes plan mode read-only and requires `present_plan` before stopping.
- **Evidence contract:** `internal/session/prompt/default.txt:95-103` requires proof appropriate to the task; `:139-145` requires grounded claims and marks unsupported claims as `[INFERENCE]`.

## 2. Atenea's model-facing tool contract

### 2.1 Contract and execution boundary

**Fact:** `agentcore/tool/tool.go:8-32` defines the public tool contract as `Name`, `Description`, `Schema`, and `Execute`. It requires JSON parsing, concurrent safety, context handling, bounded completion, and model-readable errors for correctable input.

**Fact:** `agentcore/llm/tool.go:5-14` projects a tool into the provider as name, description, and raw JSON Schema without provider-specific rewriting.

**Fact:** `internal/tool/registry.go:124-166` filters by permission, sorts announced definitions by name, and settles only the same allowed set. `internal/wiring/wiring.go:381-403` registers all built-in tools and configured MCP tools for the normal registry.

**Fact:** `internal/tool/registry.go:200-230` repairs almost-valid input before execution, refuses irreparable input without side effects, and caps output while retaining the full result by call ID.

**Assessment:** the public contract is small and provider-neutral; host enforcement is stronger than the description alone. The main missing contract is not basic schema validation but **tool discovery policy** and **model-facing decision guidance**.

### 2.2 Tool description quality

**Fact:** Built-in descriptions are embedded separately and checked for non-empty, distinct content by `internal/tool/descriptions_test.go:8-41`.

**Fact:** The current descriptions contain strong safety details for hashline edits (`internal/tool/edit.txt:1-32`), LSP operations (`internal/tool/lsp.txt:1-1`), DAP lifecycle (`internal/tool/debug.txt:1-7`), and public web-fetch restrictions (`internal/tool/webfetch.txt:1-10`).

**Gap:** The description tests verify wiring and uniqueness, not whether a description teaches:

- when to choose the tool over another tool;
- the exact recoverable failures the model must correct;
- canonical valid examples;
- common invalid examples;
- what output shape to expect and how to chain it into the next call.

**Inference:** several descriptions behave more like API reference material than a compact decision interface. This is especially relevant for `bash`, `lsp`, `ast`, `read`, `edit`, and `task`, where choosing the wrong operation causes retries or unsafe text-based work.

**Additional consistency issue:** model-facing text is mixed between English and Spanish across the prompt variants, schemas, and tool descriptions. This is not inherently incorrect, but it creates an avoidable localization variable. **[Inference]** A single model-facing language policy, or explicit provider-localization support, would make prompt evaluation more interpretable.

### 2.3 File tools

**Fact:** `internal/tool/read.go:122-170` records the complete normalized snapshot even for a ranged read, emits a `[path#HASH]` header, numbers output lines, and records only displayed lines as seen.

**Fact:** `internal/tool/edit.go:94-145` rejects malformed patches before filesystem access, applies one hashline section, protects workspace paths and aliases, returns a new chainable header, and reports committed-but-durability-uncertain results without inviting a blind retry.

**Fact:** `internal/tool/write.go:112-175` is new-file-only and records authored lines as seen for a subsequent edit.

**Assessment:** this is a major Atenea strength. The deliberately smaller grammar in `internal/tool/edit.txt:8-23` is easier to validate and permission than a general multi-file patch language.

## 3. Competitor comparison

### 3.1 Summary matrix

| Area | Atenea | oh-my-pi | OpenCode | Codex CLI | Claude/Qwen | Current winner / lesson |
|---|---|---|---|---|---|---|
| Stable prompt composition | Explicit ordered sections and tests | Dynamic capability/agent prompt composition | Provider/model/agent/MCP composition | Baseline plus independently rendered world-state fragments | Claude behavior partly proprietary; Qwen has explicit layered assembly | Atenea is easiest to audit; competitors are more adaptive |
| Instruction hierarchy | Nearest `AGENTS.md` or `CLAUDE.md` only | Rules/skills/agent discovery | Rules, agents, skills, MCP configuration | Hierarchical `AGENTS.md` with provenance | Claude cumulative/path-scoped rules; Qwen context layers | Competitors are better at scope and precedence |
| Tool disclosure | Skills deferred; tools normally all announced | Broad catalog and extensions; progressive capability surface | Configurable tool catalog and MCP | Deferred tool search with metadata/schema reveal | Qwen lazy factories, summaries, and token-budget preload | Qwen/Codex are better at context-budget control |
| Tool prompt discipline | Good safety details, no formal authoring/eval standard | Explicit purpose/grammar/examples/failures/anti-patterns/critical recap standard | Colocated descriptions and schemas | Operational schemas and freeform tool grammars | Tool registries and dynamic descriptions | oh-my-pi is the clearest model-facing authoring reference |
| File editing | Hashline, snapshots, seen-line provenance, atomic safety, single-file grammar | Rich hashline grammar and broader read/edit substrates | Conventional edit/apply-patch variants | Freeform `apply_patch` with parser/verifier/sandbox | Conventional edit tools | Atenea is safer/narrower; oh-my-pi is richer |
| Code intelligence | LSP, AST search/rewrite, DAP are present | Broader native LSP/AST/debug ecosystem | LSP and broader integrations | Shell/search plus growing protocol/tool surface | Varies by product/source visibility | Core parity exists; breadth is not automatically quality |
| Permissions | Effect-derived ask-by-default; per-call refinement; host gate | Context-dependent multi-host permissions | `allow`/`deny`/`ask` configuration | Separate approval policy and OS sandbox | Claude distinguishes context from hooks/settings enforcement | Atenea has a clean policy seam; Codex has a stronger physical sandbox |
| Delegation | Agent manifests, allowlists, output schema, detached supervision, budgets, worktrees | Specialist agents with narrow tools and structured evidence contracts | Configurable primary/subagent modes and background task semantics | Spawn/message/follow-up/resume lifecycle | Claude/Qwen expose subagents/workflows/teams to different degrees | Competitors expose richer role/lifecycle contracts |
| Context/memory | Durable structured compaction, explicit memory provenance, checkpoints | Compaction, rewind, memory backends | Session compaction and internal summary agents | Cache-aware normalization, context guidance, compaction | Claude auto memory; Qwen layered memory and structured compaction | Atenea is strong on explicitness; competitors are broader |
| Automation surface | CLI text/JSON/NDJSON over durable session events | TUI, one-shot, RPC, ACP, SDK | Client/server and integrations | Versioned app-server JSON/RPC | Varies | Codex/OpenCode/oh-my-pi are better integration platforms |

### 3.2 oh-my-pi

**Observed from official source:** `.omp/skills/tool-prompt-optimization/SKILL.md` states that tool prompts are not API docs. They must teach when to reach for the tool, the input shape, and failures the agent owns. It recommends a purpose line, concrete grammar, 3–8 worked examples, recoverable failure guidance, WRONG/RIGHT anti-patterns, and a short `<critical>` recap. It also recommends schema/prompt ablation across models before pruning text.

Source: [oh-my-pi tool-prompt optimization](https://github.com/can1357/oh-my-pi/blob/main/.omp/skills/tool-prompt-optimization/SKILL.md).

**Observed from official source:** oh-my-pi has a richer hashline/read substrate and broader LSP, AST, debug, runtime, and resource tooling. Its agent prompts define narrow specialist roles such as scout, reviewer, librarian, and security reviewer, with structured evidence expectations.

Sources:

- [oh-my-pi repository](https://github.com/can1357/oh-my-pi)
- [hashline prompt](https://github.com/can1357/oh-my-pi/blob/main/packages/hashline/src/prompt.md)
- [coding-agent task prompts](https://github.com/can1357/oh-my-pi/tree/main/packages/coding-agent/src/prompts/agents)
- [coding-agent tools](https://github.com/can1357/oh-my-pi/tree/main/packages/coding-agent/src/tools)

**Where oh-my-pi is better:** model-facing tool pedagogy, specialist-agent contracts, breadth of resources and editor-like code intelligence.

**Where Atenea is better or more deliberate:** a smaller edit language, explicit workspace/snapshot invariants, effect-derived permission policy, and a simpler contract surface.

**Do not copy blindly:** oh-my-pi's full edit grammar and broad native runtime would increase maintenance and context cost. The transferable advantage is its description/evaluation discipline.

### 3.3 OpenCode

**Observed from official source:** OpenCode composes prompts from model/provider variants, agent prompts, environment/reference data, skills, MCP instructions, and permission state. Its agent model distinguishes primary and subagent modes and allows per-agent prompt, model, steps, and permission rules.

Sources:

- [system prompt assembly](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/session/system.ts)
- [agent definitions](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/agent/agent.ts)
- [task tool](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/task.ts)
- [official permissions documentation](https://opencode.ai/docs/permissions/)
- [official tools documentation](https://opencode.ai/docs/tools/)

**Observed from official source:** the task contract includes specialized agent selection, prompt, task title, and background/resumption behavior; the source also contains explicit guidance not to poll or duplicate background work.

**Observed from official docs:** OpenCode tools are configurable through `allow`, `deny`, and `ask`, and tools include read, edit, write, apply-patch, LSP, web, skills, and todo capabilities.

**Where OpenCode is better:** adaptive prompt composition, agent configurability, background task UX, and MCP ecosystem breadth such as OAuth/resources/prompts in the inspected source.

**Where Atenea is better or more deliberate:** the default prompt's explicit verification/evidence contract, plan mode's stricter tool set, hashline freshness safety, and fail-safe effect policy for undeclared registered tools.

**Caution:** OpenCode's product surface is broader, but the official tools documentation does not by itself establish a stronger general subagent or background-task contract. Those claims require source-level evidence.

### 3.4 OpenAI Codex CLI

**Observed from official source:** Codex separates model-family/base prompt material from dynamically rendered context fragments such as tools, multi-agent mode, context-window guidance, model state, permissions, and world state. Its `AGENTS.md` loader is hierarchical and provenance-aware.

Sources:

- [Codex base prompts](https://github.com/openai/codex/tree/main/codex-rs/core)
- [AGENTS.md loader](https://github.com/openai/codex/blob/main/codex-rs/core/src/agents_md.rs)
- [world-state context](https://github.com/openai/codex/tree/main/codex-rs/core/src/context/world_state)
- [context manager](https://github.com/openai/codex/tree/main/codex-rs/core/src/context_manager)

**Observed from official source:** `apply_patch` is a freeform tool with a grammar, rather than a JSON function containing a patch string. The shell contract exposes operational parameters such as workdir, yield time, output limits, and session behavior. Codex also provides deferred tool search and a versioned app-server protocol.

Sources:

- [apply_patch spec](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/apply_patch_spec.rs)
- [apply_patch handler](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/apply_patch.rs)
- [tool search](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/tool_search_spec.rs)
- [app-server protocol](https://github.com/openai/codex/tree/main/codex-rs/app-server-protocol)

**Observed from official documentation/source:** Codex separates approval policy from OS/filesystem/network sandboxing and exposes explicit approval requests through the app-server protocol.

Sources:

- [Codex security](https://developers.openai.com/codex/security)
- [Codex sandbox documentation](https://github.com/openai/codex/blob/main/docs/sandbox.md)
- [Codex execution policy](https://github.com/openai/codex/blob/main/docs/execpolicy.md)

**Where Codex is better:** dynamic context fragments, operational tool schemas, physical sandbox separation, multi-agent protocol richness, and long-lived machine integration.

**Where Atenea is better or more deliberate:** provider-neutral Go tool contracts, hashline freshness/seen-line provenance, and a narrower edit surface with fewer ambiguous operations.

**Important distinction:** Atenea's permission policy is not an OS sandbox. The repository already documents this in `README.md:61-72` and `SECURITY.md:59-69`; Codex's architecture makes the distinction more explicit at runtime.

### 3.5 Claude Code and Qwen Code

#### Claude Code

**Observed from official documentation:** Claude Code loads hierarchical `CLAUDE.md` files cumulatively, supports path-scoped rules, and treats `CLAUDE.md` and auto memory as context rather than technical enforcement. Hooks and permission settings are the enforcement mechanisms.

Source: [Claude Code memory and instructions](https://code.claude.com/docs/en/memory).

**Not established:** the complete Claude Code system prompt and exact internal tool schemas are not public in the inspected official documentation.

**Where Claude Code is better:** path-scoped instruction/context management and persistent auto-memory UX.

**Where Atenea is better or more explicit:** host-enforced effects, explicit memory provenance, structured compaction validation, and Git-backed checkpoints.

#### Qwen Code

**Observed from official source:** Qwen's tool registry supports lazy/deferred factories, summaries, revealing tools on demand, and preloading within a token budget. Its prompt assembly separates stable, context, caller, and volatile memory layers, and its compaction prompt preserves goal, constraints, completed work, files, tool results, failures, and next steps.

Sources:

- [Qwen prompt assembly](https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/core/prompts.ts)
- [Qwen tool registry](https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/tools/tool-registry.ts)
- [Qwen Code repository](https://github.com/QwenLM/qwen-code)

**Where Qwen is better:** deferred tool disclosure, explicit token-budgeted tool loading, and model-facing context layers.

**Where Atenea is better or more explicit:** memory is never silently injected as prompt truth; facts carry source and age, and compaction is validated as a durable structured contract.

## 4. Ranked improvement opportunities

### P0 — Make one invariant prompt contract reach every provider

**Problem:** `default.txt`, `anthropic.txt`, and `local.txt` do not contain equivalent completion, safety, delegation, and evidence rules.

**Smallest useful shape:** define a shared prompt core containing:

- system-content authority and injection handling;
- specialized-tool policy;
- research-before-editing and read-before-editing;
- delegation gates;
- verification and smoke-test rules;
- evidence/uncertainty rules;
- delivery/completeness rules.

Keep provider-specific additions for local function-calling quirks, model-family behavior, and protocol differences.

**Evidence of success:** a prompt matrix test asserts that normal, Claude, local, and plan variants all contain the invariant contract while preserving their intended provider/mode differences. Test stable-prefix behavior separately from dynamic environment data.

### P0 — Adopt a tool-prompt authoring standard and measure it

For each high-leverage tool, document only decisions the model must make:

1. purpose and when to use it;
2. input grammar and required invariants;
3. two to four canonical examples;
4. recoverable failures and the correct next action;
5. WRONG/RIGHT anti-patterns drawn from actual traces;
6. a short critical recap.

Start with `read`, `edit`, `bash`, `lsp`, `ast`, and `task`. Keep implementation details, telemetry, and host-only enforcement out of model-facing text.

**Evidence of success:** a fixed probe set measures invalid calls, retries, stale-edit recovery, wrong-tool selection, tokens, and time-to-first-correct-call before and after description changes. Use schema-only ablations before deleting prose.

Primary reference: [oh-my-pi tool-prompt optimization](https://github.com/can1357/oh-my-pi/blob/main/.omp/skills/tool-prompt-optimization/SKILL.md).

### P1 — Add cumulative and path-scoped instruction loading

**Problem:** `internal/session/prompt/prompt.go:148-170` returns the nearest instruction file only.

**Smallest useful shape:** preserve `AGENTS.md`/`CLAUDE.md` compatibility, but load root-to-current instructions cumulatively, with explicit provenance and deterministic precedence. Add optional path-scoped rule files rather than putting every language/tool rule into the root prompt.

**Guardrails:** do not silently merge contradictory rules; report loaded paths and scope in a debug/status surface; keep stable instructions before dynamic environment data.

**Evidence of success:** tests cover root plus nested instructions, same-directory precedence, path-scoped activation, missing files, and prompt-cache prefix stability.

### P1 — Introduce deferred tool discovery for secondary and MCP tools

**Problem:** skills are progressive, but `internal/tool/registry.go:124-146` announces every permitted tool and `internal/wiring/wiring.go:381-403` adds all configured MCP tools to the normal registry.

**Smallest useful shape:** keep a small always-visible core (`read`, `grep`, `glob`, `edit`, `write`, `bash`, `task` where applicable), then expose summaries for advanced/MCP tools. Add a `tool_search`-style discovery path that reveals the exact schema for the next call. Sort results deterministically and enforce a schema/token budget.

**Guardrails:** permission, registration, and execution must still use the same catalog; hiding a tool must never weaken permissions; tool descriptions and MCP metadata are untrusted data, not instructions.

**Evidence of success:** compare prompt tokens/cache reads and tool-call success on tasks requiring LSP, AST, DAP, web, and MCP tools. Do not defer the tools needed for basic exploration or safety recovery.

Primary references:

- [Qwen deferred tool registry](https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/tools/tool-registry.ts)
- [Codex tool search](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/tool_search_spec.rs)

### P1 — Turn packaged subagents into typed evidence interfaces

**Current base:** `internal/agent/agent.go:15-24` already models name, description, tools, model, steps, prompt, and location; `internal/session/subagent/subagent.go:238-315` already supports dynamic descriptions, output schemas, timeouts, detached execution, and worktrees.

**Opportunity:** make the manifest/result contract explicit per role:

- explorer: findings, evidence paths/lines, confidence, coverage, open questions;
- reviewer: severity, impact, evidence, recommendation, verification gaps;
- tester: strategy, exact checks, failures, coverage gaps, verdict;
- coder: implemented behavior, files, verification, residual risks.

Keep the final result compact, but include task ID, child session, model, workspace/worktree, status, tool count, usage, and provenance outside the user payload when the host can provide it.

**Evidence of success:** parent prompts can consume a stable result envelope without parsing prose, detached tasks can be correlated and resumed, and a failed child cannot be mistaken for a successful empty report.

Primary comparison: [oh-my-pi specialist agent prompts](https://github.com/can1357/oh-my-pi/tree/main/packages/coding-agent/src/prompts/agents).

### P2 — Expose host-observed verification state

**Problem:** Atenea strongly instructs verification, but the model-facing contract still primarily receives ordinary tool output and final prose.

**Smallest useful shape:** record a typed verification observation containing command/check, workspace/session/checkpoint identity, exit status, duration, and output reference. Render a compact dynamic section such as:

```text
Verification state:
- Last observed check: go test ./agentcore/... ./internal/... ./cmd/atenea/...
- Result: exit 0
- Workspace state: unchanged since check
```

Only the host may mark a check observed; model claims remain claims.

**Evidence of success:** a final answer cannot honestly report a successful check unless a corresponding host observation exists, and compaction preserves verification observations with their source event/sequence.

### P2 — Preserve cache-friendly dynamic context as the prompt grows

**Current base:** prompt tests already prove that changing the date changes the environment suffix without changing the stable prefix (`internal/session/prompt/prompt_test.go:106-133`).

**Opportunity:** model the prompt as stable base, instruction snapshot, tool/catalog snapshot, skill summary, mode, and volatile environment/world-state fragments. Give each fragment a version/fingerprint and test that only the changed fragment invalidates the relevant cache boundary.

**Primary comparison:** Codex's independently rendered world-state fragments and cache-aware context normalization.

### P3 — Make the policy/sandbox boundary visible and independent

**Current base:** `agentcore/tool/effects.go:5-19` defines effects; `internal/permission/policy.go:5-16` asks for undeclared registered tools; the README warns that YOLO is not a sandbox.

**Opportunity:** render the active authority boundary in the environment/policy context: permission mode, workspace root, whether commands run with OS sandboxing, and what remains unrestricted. Keep effect policy and physical sandbox enforcement separate.

**Evidence of success:** a user/model cannot infer sandboxing from `allow`, `auto`, YOLO, or a worktree alone; headless and TUI modes expose the same semantics.

### P3 — Improve integration only after the prompt/tool loop is measured

Codex's app-server, OpenCode's client/server architecture, and oh-my-pi's RPC/ACP/SDK surfaces are stronger adoption platforms. Atenea already has a useful language-neutral NDJSON stream in `.okf/architecture/headless-cli.md:95-165` and a durable JSON result in `:167-180`.

The next integration step should reuse `SessionEvent`, tool, approval, model, and subagent contracts rather than inventing a second vocabulary. Do not build an RPC surface before the P0/P1 evaluation loop identifies which events consumers actually need.

## 5. What not to copy yet

- **Do not copy an entire competitor system prompt.** Prompt wording is coupled to tools, provider behavior, permissions, and UI.
- **Do not expose every advanced schema permanently.** More tools can increase context cost and wrong-tool selection.
- **Do not replace hashline safety with conventional string replacement** merely to match a competitor's shorter schema.
- **Do not treat prompt instructions as security.** Claude's own documentation distinguishes context from hooks/settings enforcement; Atenea should preserve the same conceptual boundary.
- **Do not build a generated-JavaScript workflow runtime as the first orchestration upgrade.** Typed, host-validated plans can provide fan-out, joins, cancellation, budgets, and evidence without adding a second interpreter.
- **Do not claim OS sandboxing from permissions, worktrees, or YOLO.** They solve different problems.
- **Do not add provider-specific prompt variants without a shared invariant test matrix.** The current short Anthropic/local prompts demonstrate how drift can happen.

## 6. Recommended order of work

1. **Prompt parity matrix:** shared invariant contract across default, Claude, local, and plan variants.
2. **Tool-prompt standard:** rewrite six high-leverage descriptions and add trace-based evaluation.
3. **Instruction hierarchy:** cumulative `AGENTS.md`/`CLAUDE.md` plus optional path-scoped rules.
4. **Deferred tool catalog:** summaries, search/reveal, deterministic ordering, and token budget.
5. **Typed subagent evidence:** role-specific output schemas and compact host metadata.
6. **Host-observed verification:** durable check observations and compaction preservation.
7. **Context/cache instrumentation:** fragment fingerprints and measured prompt/cache effects.
8. **Only then:** RPC/editor breadth or additional rich tools, driven by task evidence.

## Sources

### Atenea

- `internal/session/prompt/prompt.go:12-170`
- `internal/session/prompt/default.txt:1-161`
- `internal/session/prompt/anthropic.txt:1-17`
- `internal/session/prompt/local.txt:1-35`
- `internal/session/prompt/plan.txt:1-8`
- `internal/wiring/wiring.go:310-405`
- `internal/wiring/wiring.go:467-520`
- `agentcore/tool/tool.go:8-54`
- `agentcore/llm/tool.go:5-14`
- `internal/tool/registry.go:124-230`
- `internal/tool/read.go:122-170`
- `internal/tool/edit.go:94-145`
- `internal/tool/write.go:112-175`
- `internal/tool/descriptions_test.go:8-41`
- `internal/agent/agent.go:15-70`
- `internal/session/subagent/subagent.go:236-315`
- `agentcore/tool/effects.go:5-88`
- `internal/permission/policy.go:5-55`
- `internal/session/compaction.go:21-109`
- `.okf/architecture/headless-cli.md:95-180`
- `.okf/architecture/read-edit-tools.md`
- `.okf/architecture/tool-capabilities.md`

### oh-my-pi

- [Repository](https://github.com/can1357/oh-my-pi)
- [Tool-prompt optimization skill](https://github.com/can1357/oh-my-pi/blob/main/.omp/skills/tool-prompt-optimization/SKILL.md)
- [Hashline prompt](https://github.com/can1357/oh-my-pi/blob/main/packages/hashline/src/prompt.md)
- [Coding-agent specialist prompts](https://github.com/can1357/oh-my-pi/tree/main/packages/coding-agent/src/prompts/agents)
- [Coding-agent tools](https://github.com/can1357/oh-my-pi/tree/main/packages/coding-agent/src/tools)

### OpenCode

- [Repository](https://github.com/anomalyco/opencode)
- [System prompt assembly](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/session/system.ts)
- [Agent definitions](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/agent/agent.ts)
- [Task tool](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/task.ts)
- [Tools documentation](https://opencode.ai/docs/tools/)
- [Permissions documentation](https://opencode.ai/docs/permissions/)

### OpenAI Codex CLI

- [Repository](https://github.com/openai/codex)
- [AGENTS.md loader](https://github.com/openai/codex/blob/main/codex-rs/core/src/agents_md.rs)
- [World-state context](https://github.com/openai/codex/tree/main/codex-rs/core/src/context/world_state)
- [Context manager](https://github.com/openai/codex/tree/main/codex-rs/core/src/context_manager)
- [apply_patch specification](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/apply_patch_spec.rs)
- [Tool search](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/tool_search_spec.rs)
- [App-server protocol](https://github.com/openai/codex/tree/main/codex-rs/app-server-protocol)
- [Security documentation](https://developers.openai.com/codex/security)
- [Sandbox documentation](https://github.com/openai/codex/blob/main/docs/sandbox.md)

### Claude Code and Qwen Code

- [Claude Code memory and instructions](https://code.claude.com/docs/en/memory)
- [Claude Code subagents](https://code.claude.com/docs/en/sub-agents)
- [Claude Code permissions](https://code.claude.com/docs/en/permissions)
- [Qwen prompt assembly](https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/core/prompts.ts)
- [Qwen tool registry](https://github.com/QwenLM/qwen-code/blob/main/packages/core/src/tools/tool-registry.ts)
- [Qwen Code repository](https://github.com/QwenLM/qwen-code)
