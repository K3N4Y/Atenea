# Prior art on lesson extraction prompts and schemas

**Date:** 2026-08-06
**Ticket:** [#13](https://github.com/K3N4Y/Atenea/issues/13) — feeds the candidate-lesson schema and validation-limits work for `/learn` ([#11](https://github.com/K3N4Y/Atenea/issues/11)).
**Question:** How do existing agent systems turn session transcripts into durable, reusable lessons, and what should Atenea's `/learn` borrow for its extraction prompt and candidate-lesson schema?
**Method:** primary sources only — arXiv papers, raw prompt/source files in the official repos, and official vendor docs. Every claim below carries a link to the source that owns it. Details the sources do not state are marked "not specified".

## Executive summary

- Every LLM-based extractor that works in production teaches the **empty outcome explicitly** — Mem0's few-shots return `{"facts": []}` for small talk, ACE's Curator is told "if no new content to add, return an empty list", Letta's sleep-time agent is told to "call the finish tool directly" when nothing warrants an edit. Nobody relies on the model volunteering restraint; `no_candidate` must be a first-class, exampled output.
- **Malformed output fails closed everywhere.** Mem0 falls back to an empty fact list, ACE skips the update and leaves the playbook untouched; only Voyager re-prompts, and even it fails closed after retries. Local salvage (strip code fences, balanced-brace extraction) is standard *before* the closed fail, but no system does a repair round-trip to the provider for memory writes. Atenea's "invalid output fails with no repair interaction" is the industry norm, not an austerity choice.
- **Structural evidence links are almost universally absent.** Only Generative Agents stores pointers from an insight back to the cited memory records (the `(because of 1, 5, 3)` format). Everyone else stores the lesson bare and substitutes usage feedback (ExpeL importance counters, ACE helpful/harmful counters) or timestamps. Atenea's session+seq evidence field is ahead of prior art, and Generative Agents shows the working mechanism: number the input items, make the model cite the numbers, resolve them to durable IDs at parse time.
- **Anti-overfitting is done with paired opposite instructions**, not one rule: abstract the incidental ("represent non-fixed elements with descriptive variable names" — AWM) while pinning the invariant ("keep the values of invariant elements" — AWM), and ban trial references outright ("Do not mention the trials in the rules because all the rules should be GENERALLY APPLICABLE" — ExpeL). ACE names the failure modes of over-compression: *brevity bias* and *context collapse*.
- **Nobody trusts self-reported confidence.** Confidence is tracked by counters updated from later use (ExpeL, ACE) or encoded as hedging templates ("X MAY BE / SHOULD BE necessary to Y" — CLIN). An extraction-time numeric confidence field has no precedent worth copying; human approval plus provenance covers the MVP.
- Size discipline in practice: one Reflexion reflection ≤ 250 tokens; 5 Generative Agents insights in 150 tokens; ExpeL rules "concise", soft cap 20; Letta blocks historically 5,000 chars with a validator that rejects oversized writes; Claude Code's memory index hard-fails past 200 lines/25KB. Per-field character caps validated by the host, sized so a lesson fits Atenea's 5-lesson/1,500-token injection budget, match practice.

---

## 1. Per-system findings

### 1.1 Reflexion (Shinn et al. 2023)

Paper: [arXiv 2303.11366](https://arxiv.org/abs/2303.11366). Repo: [noahshinn/reflexion](https://github.com/noahshinn/reflexion).

**Prompt.** One reflection per *failed* trial, free text, "a few sentences". Programming variant ([py_generate.py](https://github.com/noahshinn/reflexion/blob/main/programming_runs/generators/py_generate.py)): "Your goal is to write a few sentences to explain why your implementation is wrong as indicated by the tests. You will need this as a hint when you try again later. Only provide the few sentence description in your answer, not the implementation." HotPotQA variant ([prompts.py](https://github.com/noahshinn/reflexion/blob/main/hotpotqa_runs/prompts.py)): "Diagnose a possible reason for failure and devise a new, concise, high level plan that aims to mitigate the same failure." ALFWorld variant ([generate_reflections.py](https://github.com/noahshinn/reflexion/blob/main/alfworld_runs/generate_reflections.py)) demands specificity: "Devise a concise, new plan of action that accounts for your mistake with reference to specific actions that you should have taken."

**Shape and bounds.** Exactly one free-text reflection per failure; reflection never runs on success and there is no "produce nothing" path — the model must always emit a diagnosis. Memory is a sliding window: "we bound *mem* by a maximum number of stored experiences, Ω (usually set to 1-3)" ([paper §Limitations/experiments](https://ar5iv.labs.arxiv.org/html/2303.11366)); code applies `memory[-3:]` (ALFWorld) and caps generation at `max_tokens=250` ([agents.py](https://github.com/noahshinn/reflexion/blob/main/hotpotqa_runs/agents.py)).

**Relevance.** Reflexion lessons are deliberately task-specific ("You will need this later when you are solving the same task") — the *opposite* of a durable workspace lesson. Its lasting contribution is the two-part rhetorical shape: diagnosis first, then prescription. No evidence links, no parsing, no validation (nothing to parse).

### 1.2 Generative Agents (Park et al. 2023)

Paper: [arXiv 2304.03442](https://arxiv.org/abs/2304.03442). Repo: [joonspk-research/generative_agents](https://github.com/joonspk-research/generative_agents).

**Prompt.** Two-stage reflection. Stage 1 asks for questions over the ~100 most recent memory records ([generate_focal_pt_v1.txt](https://github.com/joonspk-research/generative_agents/blob/main/reverie/backend_server/persona/prompt_template/v2/generate_focal_pt_v1.txt)): "Given only the information above, what are [3] most salient high-level questions we can answer about the subjects in the statements?" Stage 2 retrieves evidence per question and asks ([insight_and_evidence_v1.txt](https://github.com/joonspk-research/generative_agents/blob/main/reverie/backend_server/persona/prompt_template/v2/insight_and_evidence_v1.txt)):

> What [5] high-level insights can you infer from the above statements? (example format: insight (because of 1, 5, 3))

**Evidence linking — the key mechanism.** The input statements are presented as a *numbered list*; the parenthetical citations are parsed and resolved to concrete memory-node IDs, stored on the new thought: "We parse and store the statement as a reflection in the memory stream, including pointers to the memory objects that were cited" ([paper §4.2](https://ar5iv.labs.arxiv.org/html/2304.03442); [reflect.py `generate_insights_and_evidence`](https://github.com/joonspk-research/generative_agents/blob/main/reverie/backend_server/persona/cognitive_modules/reflect.py)). This is the only system in this survey with structural lesson→evidence provenance.

**Bounds and validation — the cautionary tale.** Insights are generated with `max_tokens=150` for 5 insights; parsing is string-splitting; `safe_generate_response` retries 5 times and then stores a junk fail-safe (`["I am hungry"] * n`), and a bare `except` can write `{"this is blank": "node_1"}` into the memory stream ([run_gpt_prompt.py](https://github.com/joonspk-research/generative_agents/blob/main/reverie/backend_server/persona/prompt_template/run_gpt_prompt.py), [reflect.py](https://github.com/joonspk-research/generative_agents/blob/main/reverie/backend_server/persona/cognitive_modules/reflect.py)). Fail-open parsing pollutes memory — a direct argument for Atenea's fail-closed rule.

### 1.3 ExpeL (Zhao et al. 2023)

Paper: [arXiv 2308.10144](https://arxiv.org/abs/2308.10144). Repo: [LeapLabTHU/ExpeL](https://github.com/LeapLabTHU/ExpeL).

**Prompt.** Compares a *success/failure trajectory pair* of the same task (or batches of 8 successes) against the existing rule list, then asks for edit operations ([human.py](https://github.com/LeapLabTHU/ExpeL/blob/main/prompts/templates/human.py)):

> ...you can perform the following operations: add, edit, remove, or agree so that the new list of rules is GENERAL and HIGH LEVEL critiques of the failed trial or proposed way of Thought so they can be used to avoid similar failures when encountered with different questions in the future.

Operation spec, verbatim: "AGREE (if the existing rule is strongly relevant for the task), REMOVE (if one existing rule is contradictory or similar/duplicated to other existing rules), EDIT (if any existing rule is not general enough or can be enhanced, rewrite and improve it), ADD (add new rules that are very different from existing rules and relevant for other tasks)... **Do not mention the trials in the rules because all the rules should be GENERALLY APPLICABLE. Each rule should be concise and easy to follow.** Any operation can be used MULTIPLE times. Do at most 4 operations and each existing rule can only get a maximum of 1 operation." (The paper names the ops ADD/EDIT/UPVOTE/DOWNVOTE; the code implements AGREE/REMOVE.)

**Counters instead of confidence.** ADD starts a rule at importance 2; AGREE/EDIT +1; REMOVE −1 (−3 when the list is full); rules are deleted when the counter reaches 0; soft cap `max_num_rules: 20` enforced only as prompt pressure ("Focus on REMOVE rules first, and stop ADD rule unless the new rule is VERY insightful") ([agent/expel.py `update_rules`](https://github.com/LeapLabTHU/ExpeL/blob/main/agent/expel.py), [configs/agent/expel.yaml](https://github.com/LeapLabTHU/ExpeL/blob/main/configs/agent/expel.yaml)).

**Output hygiene.** Line-oriented `<OPERATION> <N>: <text>`, parsed by regex; lines are *dropped* (not repaired) if empty, if operation tokens leak into rule text, or if they "don't end with a period... avoid cut off sentences from llm"; duplicate ADDs discarded; EDITs matching an existing rule downgraded to AGREE; ops referencing nonexistent rules discarded ([agent/expel.py `parse_rules`](https://github.com/LeapLabTHU/ExpeL/blob/main/agent/expel.py)). No evidence links — linking is expressly forbidden by the generality rule.

### 1.4 AWM — Agent Workflow Memory (Wang et al. 2024)

Paper: [arXiv 2409.07429](https://arxiv.org/abs/2409.07429). Repo: [zorazrw/agent-workflow-memory](https://github.com/zorazrw/agent-workflow-memory).

**Prompt** ([webarena/prompt/instruction.txt](https://github.com/zorazrw/agent-workflow-memory/blob/main/webarena/prompt/instruction.txt), full file):

> Given a list of web navigation tasks, your task is to extract the common workflows to solve these tasks. ... You need to find the repetitive subset of actions across multiple tasks, and extract each of them out as a workflow. Each workflow should be a commonly-reused sub-routine of the tasks. Do not generate similar or overlapping workflows. Each workflow should have at least two steps. **Represent the non-fixed elements (input text, button strings) with descriptive variable names as shown in the example. Keep the values of invariant elements, e.g., id of "Search" or "Customers", as they will share and stay invariant across tasks.**

**The two-sided abstraction rule.** Paper: "we enhance workflow generality by abstracting out example-specific contexts", e.g. replacing "dry cat food" with `{product-name}` ([paper](https://ar5iv.labs.arxiv.org/html/2409.07429)); the one-shot demonstrates the placeholder vocabulary (`{your-origin-city}`, `{travel-date}`, `{best-popup-option}` — [one_shot_abstract.txt](https://github.com/zorazrw/agent-workflow-memory/blob/main/mind2web/prompt/one_shot_abstract.txt)). Abstract what varies, pin what is stable — the closest operationalization in this survey of "least specific formulation supported by the evidence".

**Gating and validation.** Online induction runs only on episodes judged successful; input trajectories are stripped of invalid steps before induction; the induced text itself gets essentially no validation — the raw response is written to the per-website workflow file ([induce_prompt.py](https://github.com/zorazrw/agent-workflow-memory/blob/main/webarena/induce_prompt.py)). No evidence links (workflows literally reuse trajectory steps, so evidence is implicit).

### 1.5 CLIN (Majumder et al. 2023)

Paper: [arXiv 2310.10134](https://arxiv.org/abs/2310.10134). Repo: [allenai/clin](https://github.com/allenai/clin).

**Prompt** ([model_utils.py](https://github.com/allenai/clin/blob/main/model_utils.py)): "Generate a summary of learning, as a numbered list, that will help the agent to successfully accomplish the SAME task AGAIN, in the SAME world. Each numbered item in the summary can ONLY be of the form: X MAY BE NECCESSARY to Y. / X SHOULD BE NECCESSARY to Y. / X MAY BE CONTRIBUTE to Y. / X DOES NOT CONTRIBUTE to Y." (typos in original).

**Why it matters.** Two ideas: (1) **uncertainty is encoded in the template** — "'X may ...' to denote moderate to high uncertainty, and 'X should ...' to indicate low uncertainty" ([paper §3.2](https://ar5iv.labs.arxiv.org/html/2310.10134)) — instead of a numeric confidence field; (2) the memory is **regenerated wholesale each trial** ("a persistent, dynamic, textual memory... regularly updated after each trial"), which ACE later identified as the collapse-prone design (§1.7). Generalization across environments is a second abstraction pass ("meta-memory") that rewrites specifics like "Using the lighter on the metal pot" into "Using a heat source (stove, lighter) on the container". Token budget enforced by evicting meta-memory first, then oldest memories, then truncating the trace; no output validation.

### 1.6 Voyager (Wang et al. 2023)

Paper: [arXiv 2305.16291](https://arxiv.org/abs/2305.16291). Repo: [MineDojo/Voyager](https://github.com/MineDojo/Voyager).

**Skill storage is verification-gated.** A skill (JS program + LLM-written description) enters the library only after a critic validates task success: "This iterative process repeats until self-verification validates the task's completion, at which point we add this new skill to the skill library" ([paper §2.3](https://ar5iv.labs.arxiv.org/html/2305.16291)); in code, `if info["success"]: self.skill_manager.add_new_skill(info)` ([voyager.py](https://github.com/MineDojo/Voyager/blob/main/voyager/voyager.py)). Atenea replaces this critic with human approval — same gate, cheaper judge.

**Description prompt** ([skill.txt](https://raw.githubusercontent.com/MineDojo/Voyager/main/voyager/prompts/skill.txt)): "Try to summarize the function in no more than 6 sentences. Your response should be a single line of text." Generality lives in the *authoring* prompt ([action_template.txt](https://raw.githubusercontent.com/MineDojo/Voyager/main/voyager/prompts/action_template.txt)): "Your function will be reused for building more complex functions. Therefore, you should make it generic and reusable. You should not make strong assumption about the inventory..."

**Validation.** The critic must emit JSON ("Ensure the response can be parsed by Python `json.loads`" — [critic.txt](https://raw.githubusercontent.com/MineDojo/Voyager/main/voyager/prompts/critic.txt)); parsing uses an Auto-GPT-derived fixer (strip → parse → regex repairs → brace slicing), retries up to 5 times, then **fails closed** (`return False, ""` — no skill stored, [critic.py](https://github.com/MineDojo/Voyager/blob/main/voyager/agents/critic.py)). Voyager is the only surveyed system that re-prompts on malformed output. No provenance: stored entries hold only `{"code", "description"}`.

### 1.7 ACE — Agentic Context Engineering (2025)

Paper: [arXiv 2510.04618](https://arxiv.org/abs/2510.04618). Repo: [ace-agent/ace](https://github.com/ace-agent/ace).

**Three-role split.** "the Generator, which produces reasoning trajectories; the Reflector, which distills concrete insights from successes and errors; and the Curator, which integrates these insights into structured context updates" ([paper §3](https://arxiv.org/html/2510.04618v3)).

**Reflector prompt** (paper Fig. 10): "Your job is to diagnose the current trajectory: identify what went wrong (or could be better), grounded in execution feedback, API usage, unit test report, and ground truth when applicable." Output JSON fields, in order: `reasoning`, `error_identification`, `root_cause_analysis`, `correct_approach`, `key_insight` ("What strategy, formula, or principle should be remembered to avoid this error?"). Diagnosis fields precede the lesson field — the generation order forces grounding before abstraction.

**Curator prompt** (paper Fig. 11; [repo curator.py](https://raw.githubusercontent.com/ace-agent/ace/main/ace/prompts/curator.py)): "Identify ONLY the NEW insights, strategies, or mistakes that are MISSING from the current playbook", "Do NOT regenerate the entire playbook - only provide the additions needed", "Focus on quality over quantity", "**For any operation if no new content to add, return an empty list for the operations field**", "Be concise and specific - each addition should be actionable", "CRITICAL: You MUST respond with valid JSON only. Do not use markdown formatting or code blocks."

**Schema.** A bullet = "(1) metadata, including a unique identifier and counters tracking how often it was marked helpful or harmful" + "(2) content, capturing a small unit such as a reusable strategy, domain concept, or common failure mode" (paper §3.1). Serialized `[str-00001] helpful=3 harmful=0 :: {content}` ([playbook_utils.py](https://raw.githubusercontent.com/ace-agent/ace/main/playbook_utils.py)). No trajectory links; counters are the provenance substitute, updated deterministically from which bullets the Generator actually cited.

**The anti-collapse argument.** The paper names two failure modes of monolithic rewriting: *brevity bias* ("the tendency of optimization to collapse toward short, generic prompts... drops domain insights for concise summaries") and *context collapse* ("iterative rewriting erodes details over time") — with measured damage: "at step 60 the context contained 18,282 tokens and achieved an accuracy of 66.7", then "at the very next step it collapsed to just 122 tokens, with accuracy dropping to 57.1", below the 63.7 no-adaptation baseline ([paper §2.2 + abstract](https://arxiv.org/abs/2510.04618)). Countermeasures: append-only deltas merged "deterministically... by lightweight, non-LLM logic", plus grow-and-refine dedup by embedding similarity (repo: cosine ≥ 0.90, counters summed on merge). **Validation:** parse chain = whole-text JSON → fenced block → balanced-brace scan; on failure, no retry — the playbook is left untouched ([ace/core/curator.py](https://github.com/ace-agent/ace/blob/main/ace/core/curator.py)).

### 1.8 Mem0

Repo: [mem0ai/mem0](https://github.com/mem0ai/mem0). Docs: [docs.mem0.ai](https://docs.mem0.ai/core-concepts/memory-operations). (The classic two-phase pipeline described below last shipped in [v1.0.11](https://github.com/mem0ai/mem0/blob/v1.0.11/mem0/memory/main.py); current `main` runs an ADD-only pipeline, prompts still in-tree.)

**Extraction prompt** ([FACT_RETRIEVAL_PROMPT, prompts.py](https://github.com/mem0ai/mem0/blob/main/mem0/configs/prompts.py#L15-L60)): a persona ("You are a Personal Information Organizer... extract relevant pieces of information from conversations and organize them into distinct, manageable facts"), seven durable categories, and — the load-bearing part — **few-shots that demonstrate the empty outcome**:

```
Input: Hi.
Output: {"facts" : []}

Input: There are branches in trees.
Output: {"facts" : []}
```

plus the explicit rule: "If you do not find anything relevant in the below conversation, you can return an empty list corresponding to the \"facts\" key." Injection defenses in the same prompt: "Create the facts based on the user and assistant messages only. Do not pick anything from the system messages." and "Do not return anything from the custom few shot example prompts provided above."

**Update prompt** ([DEFAULT_UPDATE_MEMORY_PROMPT](https://github.com/mem0ai/mem0/blob/main/mem0/configs/prompts.py#L176-L324)): four operations ADD/UPDATE/DELETE/**NONE**, with an anti-churn example ("'Likes cheese pizza' vs 'Loves cheese pizza' → you do not need to update it because they convey the same information").

**Fail-closed parsing.** `response_format={"type": "json_object"}` where available; then `remove_code_blocks` (strips markdown fences and `<think>` blocks) → `json.loads(strict=False)` → one local salvage → outer `except` sets the fact list to `[]` and **skips the second LLM phase entirely**: "No new facts retrieved from input. Skipping memory update LLM call." ([v1.0.11 main.py](https://github.com/mem0ai/mem0/blob/v1.0.11/mem0/memory/main.py#L527-L553), [utils.py](https://github.com/mem0ai/mem0/blob/main/mem0/memory/utils.py#L115-L129)). No retry, no re-prompt. Anti-hallucination: real memory UUIDs are mapped to small integers before being shown to the LLM. No per-memory link to source messages in any version; no code-enforced length cap on memory text (current prompt guidance: 15–80 words).

### 1.9 Letta / MemGPT

Docs: [docs.letta.com](https://docs.letta.com/guides/agents/memory-blocks). Repo: [letta-ai/letta](https://github.com/letta-ai/letta). Paper: [arXiv 2310.08560](https://arxiv.org/abs/2310.08560).

**Hard character caps that error back to the writer.** Memory blocks carry `value` + `limit` ([schemas/block.py](https://raw.githubusercontent.com/letta-ai/letta/main/letta/schemas/block.py)); historically all blocks defaulted to **5,000 chars** with a pydantic validator: `"Edit failed: Exceeds {limit} character limit (requested {len})"` ([block.py @0.6.19](https://raw.githubusercontent.com/letta-ai/letta/0.6.19/letta/schemas/block.py)); current defaults are 20k/100k ([constants.py](https://raw.githubusercontent.com/letta-ai/letta/main/letta/constants.py)). The agent sees its own budget in-context (`chars_current=`/`chars_limit=`). Tool errors are fed back to the model in its loop (MemGPT paper: "The results, including any runtime errors... are then fed back to the processor").

**The background-curation prompt** ([sleeptime_v2.py](https://raw.githubusercontent.com/letta-ai/letta/main/letta/prompts/system_prompts/sleeptime_v2.py)), verbatim highlights: "make sure to be precise when referencing dates and times (for example, do not write 'today' or 'recently'... because... the memory is persisted indefinitely)" and the explicit no-op path: "**Skipping memory edits: If there are no meaningful updates to make to the memory, you call the finish tool directly. Not every observation warrants a memory edit, be selective in your memory editing, but also aim to have high recall.**" The voice variant adds "Infer Sensibly: ... do not invent unsupported details" ([voice_sleeptime.py](https://raw.githubusercontent.com/letta-ai/letta/main/letta/prompts/system_prompts/voice_sleeptime.py)). No first-class provenance; the absolute-dates rule is the provenance substitute.

### 1.10 Claude Code auto-memory

Docs: [code.claude.com/docs/en/memory](https://code.claude.com/docs/en/memory) (canonical; the docs.anthropic.com URL redirects there). Changelog: [anthropics/claude-code CHANGELOG](https://raw.githubusercontent.com/anthropics/claude-code/main/CHANGELOG.md).

**Selectivity is documented as behavior, not as a published prompt:** "Claude doesn't save something every session. It decides what's worth remembering based on whether the information would be useful in a future conversation." What it saves: "build commands, debugging insights, architecture notes, code style preferences, and workflow habits." The actual system-prompt wording is **not publicly documented**.

**Size limits with actionable errors.** "The first 200 lines of `MEMORY.md`, or the first 25KB, whichever comes first, are loaded at the start of every conversation"; topic files are read on demand. Since v2.1.210, over-limit writes produce an explicit error instead of silent truncation; the error text is itself a curation instruction ([errors page](https://code.claude.com/docs/en/errors#memory-index-is-over-its-read-limit)): "Rewrite it to under 140 lines now: keep one line per entry, move detail into topic files, and merge or drop stale entries." Provenance: only a `modified` ISO-8601 frontmatter timestamp. Memory is plain markdown the user can audit and edit — human-in-the-loop by transparency rather than by approval gate.

### 1.11 ChatGPT memory

Docs: [Memory FAQ](https://help.openai.com/en/articles/8590148-memory-faq), [Feb 2024 announcement](https://openai.com/index/memory-and-new-controls-for-chatgpt/), [2026 "Dreaming" post](https://openai.com/index/chatgpt-memory-dreaming/).

Model-decided saving ("If you share information that might be useful for future conversations, ChatGPT may save those details as a memory without you needing to ask"), a user-visible write notification ("Memory updated"), and a trained *category* exclusion: "we... have trained ChatGPT not to proactively remember sensitive information, like health details, unless you explicitly ask it to." Memories are explicitly **not** linked to conversations ("aren't linked to specific conversations" — announcement page); a saved-memory count cap exists but is unquantified. No schema, no validation strategy documented; the `bio` tool description circulating online is known only via prompt extraction, not official docs. Main takeaway for Atenea: the *negative* example on provenance, and the precedent for excluding whole categories at extraction time.

---

## 2. Schema shapes observed

| System | Stored unit | Fields | Evidence link | Empty path | Confidence |
|---|---|---|---|---|---|
| Reflexion | free-text reflection | none | none | none (always emits on failure) | none |
| Generative Agents | thought node | text, evidence node IDs, importance, keywords, 30-day expiry | **yes — cited statement numbers → node IDs** | none (fixed count) | importance score (scored later) |
| ExpeL | rule | (text, importance counter) | forbidden | soft (untouched rules are "copied") | counter from ops |
| AWM | workflow | name, one-line applicability description, abstracted steps | implicit (steps come from trajectories) | not specified | none |
| CLIN | causal statement | one of 4 templates | none | not specified | hedging words (MAY/SHOULD) |
| Voyager | skill | code + ≤6-sentence description | none | yes — critic gate + exclusion list | none (binary critic gate) |
| ACE | bullet | id, helpful/harmful counters, content | none (counters substitute) | **yes — explicit empty operations list** | counters from use |
| Mem0 | fact | text, hash, timestamps, scope IDs | none | **yes — few-shot `{"facts": []}`** | none |
| Letta | block | label, description, value, limit | none | yes — "call the finish tool directly" | none |
| Claude Code | index line + topic file | markdown, `modified` timestamp | none | yes — "doesn't save something every session" | none |
| ChatGPT | opaque memory | not documented | explicitly none | usefulness-conditional | none |

Observations:

- **Statement/scope/exceptions as separate fields has no direct precedent** — prior art keeps lessons as single strings and pushes applicability into the wording ("Given that you are on the Delta flight booking page, this workflow..." — AWM descriptions; "X SHOULD BE NECESSARY to Y" — CLIN). The closest analogues are AWM's applicability sentence (a scope field in disguise) and ExpeL's REMOVE-on-contradiction (exceptions handled by list surgery instead of a field). Atenea's three-field split is novel but each field maps to a proven prior-art function: statement ↔ ExpeL rule text, scope ↔ AWM applicability line, exceptions ↔ CLIN's `DOES NOT CONTRIBUTE` contrastive template.
- **The `no_candidate`-with-reason variant is stronger than anything surveyed.** Prior art has empty lists (Mem0, ACE) and silent skips (Letta, Claude Code); none makes the extractor *justify* abstaining. The reason field gives the reviewer signal ("session was routine" vs "evidence contradictory") at negligible cost.
- **Evidence as input-anchored references works when the input is numbered.** Generative Agents' `(because of 1, 5, 3)` is the proven mechanism; Mem0's UUID→small-integer mapping is the same trick applied defensively ("handling UUID hallucinations"). Atenea's session+seq references should follow both: present transcript items to the learner tagged with their `seq`, require citations by `seq`, validate that every cited seq exists within the cut.

## 3. Anti-overfitting techniques

Four distinct mechanisms appear in practice:

1. **Ban trial references in the lesson body.** ExpeL, verbatim: "Do not mention the trials in the rules because all the rules should be GENERALLY APPLICABLE" ([human.py](https://github.com/LeapLabTHU/ExpeL/blob/main/prompts/templates/human.py)). Provenance lives in metadata (Atenea: the evidence field), never in the statement.
2. **Abstract the variable, pin the invariant — as a pair.** AWM: "Represent the non-fixed elements... with descriptive variable names" *and* "Keep the values of invariant elements, e.g., id of \"Search\"... as they will share and stay invariant across tasks" ([instruction.txt](https://github.com/zorazrw/agent-workflow-memory/blob/main/webarena/prompt/instruction.txt)). A lone "be general" instruction produces vagueness (ACE's brevity bias); the pinning half is what keeps lessons actionable. This is the closest operationalization of issue #11's Bennett criterion (least specific statement supported by the evidence): generalize exactly the parts the evidence shows to vary, keep exactly the parts it shows to be stable.
3. **Suppress the generic with a positive counter-instruction, not just a ban.** ACE's Curator pairs "Focus on quality over quantity" with "Be concise and specific - each addition should be actionable" ([curator.py](https://raw.githubusercontent.com/ace-agent/ace/main/ace/prompts/curator.py)); ExpeL reserves ADD for rules "VERY insightful and different from EXISTING RULES". Mem0 excludes the generic by example ("There are branches in trees" → `[]`). CLIN's paper motivates its causal template as the device that beats "general 'helpful hints'" ([paper](https://ar5iv.labs.arxiv.org/html/2310.10134)). A usable falsifiability test falls out of these: *if the statement would be equally good advice in any repository, it is too generic; if it names a value that appears only once in the evidence, it is too specific.*
4. **Category and source exclusions at extraction time.** Mem0: facts only from user/assistant messages, never system messages; ChatGPT: trained refusal to store sensitive categories; Letta: no relative dates ("do not write 'today' or 'recently'... the memory is persisted indefinitely"). Atenea's equivalents: treat transcript content as untrusted evidence (already in #11), and ban relative time references in statements.

Where the survey systems place the abstraction *pass* also matters: CLIN keeps episode lessons specific and generalizes in a separate meta-memory stage; ExpeL generalizes inline under counter pressure. Atenea's single-interaction constraint forces inline generalization — which is why the paired instruction (2) and the falsifiability test (3) belong in the prompt verbatim rather than as a hoped-for emergent behavior.

## 4. Validation and size limits in practice

**Parsing without provider-native structured output.** The converged recipe (Mem0, ACE, Voyager): (1) demand bare JSON in the prompt — "CRITICAL: You MUST respond with valid JSON only. Do not use markdown formatting or code blocks" (ACE), "Ensure the response can be parsed by Python `json.loads`, e.g.: no trailing commas, no single quotes" (Voyager); (2) apply *local* salvage before parsing — strip markdown fences and `<think>` blocks (Mem0 `remove_code_blocks`), fenced-block extraction, balanced-brace scan (ACE); (3) validate shape (ACE checks `operations` is a list of typed dicts; Voyager asserts `success in [True, False]`).

**Rejection vs repair.** On failure after local salvage: Mem0 → empty fact list, pipeline stops; ACE → "Skipping curator operation due to invalid JSON format", playbook untouched, no retry; ExpeL → drop the malformed lines, keep the rest; Generative Agents → retry 5× then store junk (the anti-pattern); Voyager → re-prompt up to 5×, then fail closed. **No surveyed system does a provider round-trip to repair a memory write except Voyager, and Voyager's write is gated by an environment check that Atenea replaces with human review.** Atenea's "invalid output fails with no repair interaction" matches the majority design; local fence-stripping before the strict parse is standard practice and does not constitute a repair round-trip.

**Size limits.**

- Generation caps: 250 tokens for one Reflexion reflection; 150 tokens for 5 Generative Agents insights; 1,000 tokens for a full CLIN memory; 2,048 for an AWM induction batch.
- Storage caps: Letta 5,000 chars/block historically (now 20k/100k), enforced by a validator that **rejects the write with an error** rather than truncating; Claude Code 200 lines/25KB loaded, over-limit writes error with rewrite instructions; ExpeL soft cap of 20 rules via prompt pressure; ACE 80k-token playbook budget with embedding dedup.
- Two enforcement styles: *reject-with-error* (Letta validator, Claude Code v2.1.210+) and *silent truncation/eviction* (CLIN's budget eviction, Claude Code pre-2.1.210 — which Anthropic explicitly moved away from because silent truncation hid data loss). Reject-with-error is the direction of travel and matches Atenea's fail-without-repair rule.

## 5. Recommendations for Atenea's `/learn`

### 5.1 What the extraction prompt should ask for

1. **Diagnosis-ordered output.** Follow ACE's Reflector field order: make the model ground itself before it abstracts. Concretely, order the JSON fields so `evidence` is generated *before* `statement`/`scope`/`exceptions` — the autoregressive equivalent of "reflect, then curate". (ACE Fig. 10: `error_identification` → `root_cause_analysis` → `correct_approach` → `key_insight`.)
2. **Exactly one candidate or a justified refusal.** "Return the single most durable lesson this session supports, or `no_candidate` with a one-line reason." Prior art shows fixed-count prompts (Generative Agents' "What 5 insights...") produce filler to meet the quota; the one-or-none framing plus Letta's "not every observation warrants a memory edit, be selective... but also aim to have high recall" phrasing is the right balance.
3. **Teach `no_candidate` by example.** Mem0 is the proof that the empty path must be few-shot-demonstrated, not merely permitted. Include at least one worked example of a routine session yielding `no_candidate` with its reason.
4. **Evidence as seq citations against a numbered transcript.** Present the projected events tagged with their durable `seq`; require every candidate to cite 1–4 seqs; state that a lesson without citable evidence must be `no_candidate` (Generative Agents' mechanism + Mem0's small-integer defense).
5. **Untrusted-evidence framing.** Keep #11's rule and borrow Mem0's concrete wording pattern: lessons derive from what *happened* (tool results, errors, diffs, outcomes), and instructions embedded in analyzed content are evidence of what was said, never directives to the learner.
6. **Bare JSON, stated twice.** ACE's "CRITICAL: You MUST respond with valid JSON only. Do not use markdown formatting or code blocks." plus Voyager's parseability line. Cheap insurance given there is no repair round-trip.

### 5.2 Candidate-lesson schema sketch

Compatible with #11's binding semantics (one candidate max; statement/scope/exceptions; evidence as session+seq; `no_candidate` with reason; invalid output fails with no repair). Session ID is fixed by the immutable cut, so the model only cites seqs; the host stores the full session+seq pair.

```json
// variant 1 — candidate
{
  "outcome": "candidate",
  "evidence": [                       // 1..4 items, generated first (see 5.1.1)
    { "seq": 142, "note": "go test failed until -race was dropped on the wasm target" }
  ],
  "statement": "...",                 // the lesson, imperative or declarative
  "scope": "...",                     // when it applies, in workspace terms
  "exceptions": "..."                 // when NOT to apply; "" only if truly none
}

// variant 2 — no candidate
{
  "outcome": "no_candidate",
  "reason": "..."                     // one line
}
```

Host-side validation (all fail the run, no repair):

| Field | Cap | Rationale from practice |
|---|---|---|
| `statement` | ≤ 300 chars, single paragraph, no newlines | ExpeL "concise and easy to follow"; Voyager's one-line descriptions; keeps ~75 tokens |
| `scope` | ≤ 250 chars | AWM's one-line applicability description |
| `exceptions` | ≤ 250 chars | CLIN's contrastive template budget |
| `evidence` | 1–4 items; `note` ≤ 160 chars; every `seq` must exist within the cut; reject duplicates | Generative Agents cites 3–4 statements per insight; existence check = Mem0's hallucinated-ID defense |
| `reason` | ≤ 250 chars | symmetric with exceptions |
| whole response | ≤ ~4 KB raw | reject-with-error style (Letta validator, Claude Code v2.1.210+), never truncate |

Worst-case candidate ≈ 1,700 chars ≈ 300 tokens of injectable text (statement+scope+exceptions ≈ 800 chars ≈ 150 tokens), so five selected lessons fit #11's 1,500-token budget with headroom even before evidence (which stays in the panel, per #11).

Deliberately omitted fields, with prior-art backing: **no self-reported confidence** (no surveyed system trusts one; ExpeL/ACE use post-hoc counters, CLIN uses hedged wording — if hedging is wanted, it belongs in the statement text); **no category/tag field** for the MVP (lexical selection works on statement/scope/exceptions text; ACE's sections and Mem0's categories organize retrieval Atenea does deterministically); **no free-form metadata** (provenance — provider, model, tokens, timestamps, cut — is host-recorded, immutable, and never model-authored, matching #11).

Parse pipeline: strip markdown fences and reasoning blocks (Mem0 `remove_code_blocks` equivalent) → strict JSON decode → exhaustive schema validation (unknown fields rejected, both variants mutually exclusive) → seq existence check. Any failure → run state `failed` with the validation error preserved for the panel. This is local salvage, not repair; it matches Mem0/ACE exactly.

### 5.3 The anti-overfitting instructions worth putting in the prompt verbatim

1. **The generality/anchoring pair** (ExpeL + AWM fused):
   > "Do not mention this specific session, its file names, ticket numbers, or one-off values in the statement — the lesson must hold for future tasks in this workspace. But keep the names of things that are stable in this workspace (commands, tools, directories, conventions): a lesson that names no workspace reality is too generic to act on."
2. **The falsifiability test** (operationalizes #11's Bennett criterion; distilled from ACE's "concise and specific... actionable" + Mem0's generic-fact exclusion):
   > "State the least specific claim the evidence still supports. Test both directions: if the statement would be good advice in any repository, it is too generic — make it more specific or return no_candidate; if it depends on a detail that appears only once in the evidence, it is too specific — generalize that detail or drop it."
3. **The abstention norm** (Mem0's empty path + Letta's selectivity, inverted for Atenea's human-review economics):
   > "Not every session teaches something durable. If the evidence supports only a description of what happened, or advice you could have given without reading the session, return no_candidate with the reason. A missing lesson costs one command; a fabricated one misleads every future task."

### 5.4 Post-MVP notes (out of scope for #11, recorded for later)

- **Usage counters over re-extraction.** If lessons ever get feedback, ACE's helpful/harmful counters updated deterministically from use — and ExpeL's delete-at-zero rule — are the proven design; both avoid another provider call.
- **Never rewrite the lesson store wholesale.** ACE's measured context collapse (18,282 → 122 tokens, accuracy below baseline) is the standing argument against any future "consolidate all lessons" pass done as a single monolithic rewrite.
- **Dedup at approval time.** ExpeL converts duplicate ADDs into AGREEs; Atenea's analogue is surfacing near-duplicate approved lessons in the panel when the user approves a new one (deterministic, lexical — no embeddings needed for a per-workspace list capped in the tens).
