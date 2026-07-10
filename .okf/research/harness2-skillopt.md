---
updated_at: 2026-07-09
summary: Research on Microsoft SkillOpt and its implications for the harness.
---

# Harness2: Research on SkillOpt (Microsoft)

Research on **SkillOpt**, the skills optimizer in text space
published by Microsoft Research. Sister document of `Harness.md` (containing
the *Self-Harness* paper); At the end, both approaches are compared and their
relevance for `atenea` is discussed.

- Paper: *SkillOpt: Executive Strategy for Self-Evolving Agent Skills*,
 Yang et al., 2026. arXiv:2605.23904 (v1 22-May-2026, v2 25-May-2026).
 27 pages, 4 figures, 6 tables. Subjects: cs.AI, cs.CL.
- Code: https://github.com/microsoft/SkillOpt (MIT license).
- Project page: https://microsoft.github.io/SkillOpt/ (alias aka.ms/skillopt).
- Team led by Microsoft Research, with authors from Microsoft, Shanghai Jiao
 Tong University, Tongji University and Fudan University.

> Reliability note: the paper and several sources are dated 2026 and cite
> models such as GPT-5.5 / GPT-5.4 and Qwen3.5/3.6. The figures come from the paper,
> the project page and press articles (VentureBeat, AI Papers Academy). Where
> a claim comes from a third-party source with commercial interest (e.g.
> CodexOpt) it is explicitly marked.

## 1. Executive summary

SkillOpt is, according to the authors, the **first systematic and
controllable skills optimizer in text space** for LLM agents. The central idea: treat a
natural language skill document (a compact `.md`) as the **trainable
state** of an agent with a frozen model, and "train" it with the same
discipline as training neural networks: epochs, mini-batches,
learning rate and validation gates, but **without touching the model weights**.

Project motto: **"Train the procedure, not the weights"** (train the
procedure, not the weights).

The deployable artifact is a single `best_skill.md` (typically 300-2,000
tokens, median ~920) that is consumed against the target model unchanged and
**does not add any extra calls to the model at inference time**.

Headline result: in 6 benchmarks x 7 target models x 3 harnesses of
execution (direct chat, Codex, Claude Code), SkillOpt is **the best or tied in
the 52 cells evaluated** (model, benchmark, harness), beating all
competitors per cell: human skills, one-shot LLM, Trace2Skill, TextGrad,
GEPA and EvoSkill.

## 2. The problem that solves

Today agent skills are built in three ways, and none of them behave
like a deep learning optimizer:

1. **Hand-crafted** by human experts.
2. **Generated one-shot** by an LLM.
3. **Evolved by poorly controlled self-revision** (self-revision).

No reliable improvement over your starting point under feedback. The
authors argue that the skill should be trained as **external state of a
frozen agent**, with the same reproducibility as optimization in
weight space. SkillOpt fills that gap.

## 3. Central idea: the skill as a trainable state (analogy with deep learning)

| Concept in deep learning | Equivalent in SkillOpt |
| --- | --- |
| Model weights | The skill document (`skill.md`) |
| Gradient | The proposed edits (add/delete/replace) |
| Forward pass | **Rollout**: The frozen agent executes tasks with the current skill |
| Backward pass | **Reflect**: an optimizing model analyzes trajectories and proposes edits |
| Learning rate | The **edit budget** per iteration (textual learning rate; lr=4 by default) |
| Mini-batch | Subset of scored rollouts that are passed to the optimizer |
| Validation gate | Accept the edit only if it improves a **score held-out** |
| Epoch | Complete pass that triggers the **slow/meta update** |

Key points of the analogy:

- The skill plays the role of weights; the edits are like gradients that
 suggest how to change the "parameters".
- The number of edits allowed per iteration acts as the learning rate.
- The target model, the backend and the harness remain **fixed**; only changes
 the skill document.

## 4. How it works: the optimization loop

The complete pipeline is: **rollout -> reflect -> aggregate -> select -> update
-> evaluate**. It is decomposed into two paths: the *fast update* (continuous) and the
*slow update* (once per epoch).

### 4.1 Fast update path (continuous)

1. **Split of the dataset** in train / validation / test. The fixed agent operates with
 the skill file.
2. **Rollout**: each iteration processes a batch of train samples with the current
 skill, producing execution trajectories (traces) and final outputs.
 Equivalent to *forward pass*.
3. **Reflect (optimizer analysis)**: the rollouts are divided into mini-batches and passed to an **optimizer model** (a strong frontier LLM; GPT-5.5 in the
 paper). The **full trajectory** (tool use, intermediate steps
 and final output) is analyzed, not just the response. The optimizer proposes
 edits: **replace, remove or add** rules. It is a "language-level backward pass". Successes and failures are reflected separately, to correct
 recurring errors while preserving what already works.
4. **Aggregate (consolidation and ranking)**: a second step consolidates and ranks
 all candidate editions through the mini-batches.
5. **Select (textual learning rate)**: only a limited number of the best-ranked
 editions advance. Avoid destructive rewrites and "overly aggressive updates
 that would destabilize the optimization."
6. **Gate (validation gate)**: selected editions create a candidate skill
; the agent runs on the **validation set held-out**. If it improves, the
 edition is accepted; If not, it is rejected and the previous skill is preserved. Failed
 edits are shown to the optimizer in future iterations (see
 rejected-edit buffer) so as not to repeat useless changes.

### 4.2 Slow update path (once per epoch)

See longer range patterns over many iterations:

1. **Comparison start vs end of epoch**: the same train samples are
 processed twice, once with the skill at the start of the epoch and once with the skill
 after the fast-update.
2. **Categorization of results** into four groups:
 - **Improvements**: before it failed, now it resolves.
 - **Regressions**: before it resolved, now it fails.
 - **Persistent Failures**: it fails in both.
 - **Stable Successes**: it succeeds in both.
3. **Reflection by epoch**: the optimizer looks for high-level patterns (what
 helps, what hurts, what failure modes persist) and modifies a dedicated **portion
 of the skill** that the fast path does not touch. These changes also pass
 the validation gate.
4. **Meta-skill / long-term memory**: record of which edits worked,
 which ones failed, and which challenges remain unresolved, to guide future epochs.

After several epochs you obtain the final optimized skill.

### 4.3 Figures from the paper (described)

- **Teaser**: target model, optimizing model, bounded editions, validation
 gate and the exported best skill.
- **Pipeline**: rollout, reflection, bounded editions, validation gate, slow
 update and meta skill.
- **Epoch trends**: compares "selection-best" checkpoints against the
 rollout score in train and the performance in test no seen.
- **ALFWorld evolution**: train rollout vs selection gate score, with the
 rejected edits drawn as points down.

## 5. Key stability mechanisms

- **Textual learning rate (edit budget)**: limits how much
 can change the skill per iteration. Prevents extensive rewrites that would lose
 useful rules, while maintaining plasticity for learning new procedures.
 Default value `lr=4`.
- **Gated held-out selection**: converts reflection into optimization
 *propose-and-test* instead of unconditional auto-editing. An edit is only accepted if it **strictly improves** the held-out score.Net result: these pieces make skill training stable **adding
zero calls to the model at inference time** at deployment.

## 6. The artifact: `best_skill.md`

- SkillOpt exports a **single compact file** `best_skill.md`.
- Typical size 300-2,000 tokens; in all benchmarks I never exceed 2,000,
 with median ~920 tokens.
- Readable and auditable: a human can review and manage it in minutes.
- In deployment, **the target model consumes only the final skill, not the
 memory of the optimizer**.

## 7. Experimental setup

- **6 benchmarks**: SearchQA, SpreadsheetBench (Sheet), Office/OfficeQA, DocVQA,
 LiveMathBench, ALFWorld.
- **7 target models**: GPT-5.x family (incl. GPT-5.5, GPT-5.4, GPT-5.4-mini,
 GPT-5.4-nano) and Qwen family (Qwen3.5-4B, Qwen3.6-35B-A3B), among others.
- **3 execution harnesses**: direct chat, Codex (agent CLI), Claude Code
 (agent CLI).
- **Optimizer**: a strong frontier LLM (GPT-5.5 in the paper); it can differ
 from the target model or be the same (self-optimizer).
- **Compared baselines**: human skills, one-shot LLM, Trace2Skill, TextGrad,
 GEPA, EvoSkill.

## 8. Results

### 8.1 Clean sweep (52/52)

SkillOpt is **best or tied in all 52 cells** (model x benchmark x harness)
and beats every competitor per cell.

### 8.2 Accuracy gains (over baseline without skill)

About **GPT-5.5**, average gain over baseline without skill:

| Harness | Gain |
| --- | --- |
| Direct chat | +23.5 points |
| Codex (agent loop) | +24.8 points |
| Claude Code | +19.1 points |

(The project page also reports +21.8 for GPT-5.5 in Codex in another aggregation;
the exact figures vary depending on the specific table.)

### 8.3 Earnings by benchmark (examples)

| Benchmark | Without skill | With SkillOpt |
| --- | --- | --- |
| ALFWorld | 83.6 | 95.5 |
| SpreadsheetBench | 41.8 | 80.7 (~2x) |
| OfficeQA | 33.1 | 72.1 |

### 8.4 Small models

Profits are not limited to frontier models:

- GPT-5.4-nano: +24.9 on average.
- Qwen3.5-4B: +19.2.
- Qwen3.6-35B-A3B: +9.1.

### 8.5 Example of skill evolution (ALFWorld)

Target GPT-5.4-mini, GPT-5.5 optimizer. The selection score rose from 68.6% to
81.4%; The final "hard" test improved from 70.9% to 85.8%. A "slow update" in epoch
3 rescued a candidate; An intermediate step I trained higher but the
selection gate failed. Rules learned: expand the search after several failures
 in a row and maintain a numbered set of elements already searched.

## 9. Transferability

One of the most notable properties: the exported skill behaves as a
**reusable artifact** that transfers without re-optimizing the target side.

- **Cross-model** (between model scales): LiveMath skill in GPT-5.4
 brought to GPT-5.4-nano -> +15.2. A skill optimized on a small model gives
 a good starting point for a large one, reducing the cost of optimizing on
 a fleet.
- **Cross-harness**: SpreadsheetBench skill trained on Codex brought to
 Claude Code -> +31.8.
- **Self-optimizer**: GPT-5.4-nano as its own optimizer -> +10.4. This
 demonstrates that the loop **is not a mere distillation from a stronger model**:
 even with target = optimizer finds useful edits when they are
 bounded, buffered and validated.
- **Cross-benchmark**: transfers to a nearby math benchmark without
 additional optimization, with consistent although modest gains.

Honest nuance (according to AI Papers Academy): transferability "works, but it's not
very consistent"; the preservation between model variants is uneven and the
cross-benchmark gains are not huge.

## 10. Efficiency and costs

- **Compact and auditable artifacts**: <= 2,000 tokens, median ~920.
- **Training cost**: the paper mentions that training tokens
 can reach ~210 million in academic benchmarks; for day-to-day enterprise
 use cases it is much lighter.
- **Inherent overhead**: the method requires repeated rollouts of the agent plus a
 frontier optimizer (GPT-5.5), which implies non-trivial cost during
 optimization (although zero in deployment inference).

## 11. Availability and practical use

### 11.1 Installation

```bash
pip install skillopt        # SkillOpt v0.1.0 en PyPI; requiere Python 3.10+; MIT
```

The complete loop (rollout -> reflect -> aggregate -> select -> update ->
evaluate) is included.

### 11.2 Supported backends (multi-backend)

OpenAI, Azure, Claude, Qwen and MiniMax. Examples of backends:
`openai_chat`, `claude_chat`, `qwen_chat`, `minimax_chat`, `codex_exec`,
`claude_code_exec`.

Add a backend: create `skillopt/model/<name>_backend.py`, register it to
`skillopt/model/common.py` and `backend_config.py`, and route via
`skillopt/model/__init__.py`. Templates: `qwen_backend.py`, `minimax_backend.py`.

### 11.3 Benchmarks (envs)

Six integrated benchmarks. A benchmark is a package `skillopt/envs/<name>/`
with `dataloader.py`, `rollout.py` and a `initial.md` (seed skill). The simplest
reference is `skillopt/envs/searchqa/`.

### 11.4 WebUI dashboard

```bash
pip install -e ".[webui]"
python -m skillopt_webui.app
```

| Flag | Default | Description |
| --- | --- | --- |
| `--port` | 7860 | Server port |
| `--host` | `0.0.0.0` | Bind address |
| `--share` | off | Create a public Gradio link |

### 11.5 Repo structure

Notable directories: `ckpt`, `configs`, `data`, `docs`, `plugins`, `scripts`,
`skillopt`, `skillopt_sleep`, `skillopt_webui`, `tests`. Languages: Python
~87%, HTML ~12%, Shell ~1%.

### 11.6 SkillOpt-Sleep (preview)

Companion for **nightly offline auto-evolution** for local coding agents
(Claude Code / Codex / Copilot). Review past sessions, re-execute recurring tasks and consolidate validated skills behind a held-out gate. Documented
in the `sleep` project documentation. Very relevant for a harness that wants to improve their
own skills between sessions.

## 12. Application to coding agents: CodexOpt

> CodexOpt is a third party project (Superagent AI) that adapts the ideas of
> SkillOpt to Codex. Useful as an integration reference, but its figures are not
> independently verified.

In coding agents, the instruction files (`AGENTS.md`, `SKILL.md`) are
treated as **live components of the runtime**, not as passive notes: Codex incorporates them
directly into the agent loop, generating observable
execution trajectories that are the raw material of optimization.

CodexOpt rollout pipeline:

1. Deploy a candidate skill.
2. Execute tasks via `codex exec`.
3. Capture JSON event streams and results.
4. Score with verifiers, LLM judges or static analysis.
5. Generate bounded rewrites.
6. Validate in held-out tasks before accepting.

CodexOpt CLI:

```bash
uv run codexopt improve              # Preview seguro
uv run codexopt improve --live       # Optimizacion completa con Codex
uv run codexopt improve --live --apply  # Aplicar cambios validados
uv run codexopt report               # Revisar resultados
```

Integration notes: train/validation splits are automatically mined from
git history, issues and skill descriptions; Codex JSONL
path support; multiple reward signals (verifier + LLM judge);
evidence files (`tasks.md` or JSON) reinforce the signal. Mapping SkillOpt -> CodexOpt:
skill artifact = `SKILL.md`/`AGENTS.md`; rollout = `codex exec` or verifier;
bounded edit = edits budget; validation gate = held-out performance.

## 13. Comparison with previous methods

SkillOpt is positioned against:

- **Human skills**: static, do not improve under feedback.
- **One-shot LLM**: generated at once, without validation loop.
- **Trace2Skill**: derives skills from traces.
- **TextGrad / GEPA**: prompt/text optimization via "textual gradients";
 SkillOpt adds bounded learning rate + strict validation gate + buffer of
 rejections, which makes it more stable and reproducible.
- **EvoSkill**: evolution of skills.

Differential: it is the first that combines **limited editing (add/delete/replace) +
textual learning rate + gated held-out selection + buffer of rejected
editions + slow/meta update by epoch** in a systematic and controllable way.

## 14. Relationship with Self-Harness (`Harness.md`)

[`harness.md`](harness.md) contains the paper **Self-Harness: Harnesses That Improve
Themselves** (Zhang et al., Shanghai AI Lab). It is the close cousin of SkillOpt;
it is convenient to contrast them because they attack the same problem from different angles.

| Dimension | SkillOpt | Self-Harness |
| --- | --- | --- |
| Object to be optimized | The **skill document** (natural language text) | The **complete harness** (prompts, tools, memory, policies, config code) |
| Who proposes the editions | A **separate optimizing model** (may be stronger) | The **same fixed model** in the role of proposer (without a stronger external agent) |
| Editing granularity | add/delete/replace on a `skill.md` | Editions limited to declared harness surfaces (instruction, tools, verification...) |
| Magnitude control | Textual learning rate (lr=4) | Minimum edition per branch, diversity between branches (K parallel proposals) |
| Acceptance criteria | Strict improvement of the held-out score | Non-regression: improvement in >=1 split without degrading the other |
| Fault memory | Rejected-edit buffer + meta-skill | Log of rejected proposals (without changing the active harness) |
| Evidence of entry | Scored rollouts, mini-batches | Weakness Mining: clustering of failed traces by verified failure signature |
| Main benchmark | 6 (SearchQA, SpreadsheetBench, OfficeQA, DocVQA, LiveMath, ALFWorld) | Terminal-Bench-2.0 (64 tasks) |
| Models | GPT-5.x, Qwen3.x, MiniMax... (7) | MiniMax M2.5, Qwen3.5-35B-A3B, GLM-5 |
| Artifact | `best_skill.md` portable (300-2,000 tokens) | Harness definition file (DeepAgent code) |

Both share the underlying thesis: **it is not necessary to touch the weights; it is enough to
optimize, in a testable and reversible way, the external state (skill or harness)
that governs the behavior of the agent**, validating against a held-out gate.
SkillOpt produces a text artifact transferable between models/harnesses;
Self-Harness produces a specific harness per model from its own
failure modes.

## 15. Relevance to Athena

`atenea` is an agent harness (Wails + Go) with nascent skill system
(`internal/skill/builtin`, `.claude/skills/`) and a terminal tab with real pty.
SkillOpt actionable reads for this project:

1. **Skills as a trainable state, not as static notes.** The compact (<2k tokens) and auditable
 `best_skill.md` pattern fits with a
 skill file per domain that the harness can version and improve.
2. **Validation gate held-out as invariant.** Any self-improvement of skills
 in atenea should accept a change only if it improves strictly on a reserved
 set: this is the skills equivalent of the TDD-with-evidence cycle of the
 repo (RED/GREEN/TRIANGULATE with a regression gate).
3. **Rollout -> reflect -> gate** is a loop implementable on top of the existing runner
: execute tasks, collect traces, propose edits limited to the
 skill, validate. Athena's `EventBus`/runner already produces exploitable traces.
4. **SkillOpt-Sleep** is the most directly applicable pattern: an offline pass
 that reviews past sessions and consolidates validated skills between sessions, without
 cost in deployment inference.
5. **Transferability of skills between harnesses** (Codex <-> Claude Code) suggests
 that a `best_skill.md` learned in another environment could serve as a seed in
 athena with immediate benefit.

This ties in with `[[subagent-harness-research]]` and
`[[agent-next-additions-roadmap]]`'s roadmap: a SkillOpt-style skills optimizer
 would be a natural future Tier once snapshot persistence and context compactor
 are in place.

## 16. Limitations and cautions

- **Optimization cost**: repeated rollouts + border optimizer; up to
 ~210M tokens in academic benchmarks (much less in real use).
- **Inconsistent transferability**: works but preservation between
 model variants is uneven; modest cross-benchmark gains.
- **Dependency on good verifiers**: the gate held-out is only as good
 as the scoring signal (verifier / LLM judge). Tasks without a reliable
 verifier weaken the loop.
- **Risk of overfitting to the benchmark**: as in Self-Harness, the accepted
 editions can reflect specific patterns of the benchmark.
- **Reliability of sources**: dates 2026 and GPT-5.5/Qwen3.5 models are
 ahead of previous knowledge; the press and CodexOpt figures
 (third party product) should be taken with caution compared to the original paper.

## 17. Sources

- [SkillOpt — project page (microsoft.github.io)](https://microsoft.github.io/SkillOpt/)
- [GitHub — microsoft/SkillOpt](https://github.com/microsoft/SkillOpt)
- [arXiv:2605.23904 — SkillOpt: Executive Strategy for Self-Evolving Agent Skills](https://arxiv.org/abs/2605.23904)
- [Microsoft Research — publication page](https://www.microsoft.com/en-us/research/publication/skillopt-executive-strategy-for-self-evolving-agent-skills/)
- [Hugging Face — paper page](https://huggingface.co/papers/2605.23904)
- [AI Papers Academy — SkillOpt: 2x Accuracy Without Touching the Model](https://aipapersacademy.com/skillopt/)
- [VentureBeat — Microsoft's open-source SkillOpt](https://venturebeat.com/orchestration/microsofts-open-source-skillopt-automatically-upgrades-ai-agent-skills-without-touching-model-weights)
- [Superagent AI — CodexOpt brings SkillOpt to Codex](https://shashikantjagtap.net/codexopt-brings-microsoft-skillopt-to-codex-optimizing-agent-skills-with-execution-feedback/)
- [Medium — How Microsoft SkillOpt Optimizes LLM Agents by Rewriting skills.md](https://medium.com/@tort_mario/how-microsoft-skillopt-optimizes-llm-agents-by-rewriting-skills-md-25-gain-6d170a07a380)
- Related document in this repo: [`harness.md`](harness.md) (paper *Self-Harness*).
