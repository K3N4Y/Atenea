---
updated_at: 2026-07-09
summary: Evidence-based research on why small language models fail at tool calling and how to make tool integrations reliable.
---

# Small Language Models and Tool-Calling Reliability

## Research question

Do small language models (SLMs) have an inherent problem with tool calling
because they receive less relevant training, or because tool-calling harnesses
do not use one standard protocol?

## Short answer

Both factors are real, but they explain different failure modes.

- **Model capacity and specialised training** primarily affect semantic
  reliability: deciding whether to call a tool, selecting the right one,
  filling arguments from user intent, handling multi-step state, and repairing
  failures.
- **Protocol and harness fragmentation** primarily affect integration
  reliability: serialisation, special tokens, message roles, tool-call IDs,
  result delivery, parsing, and schema dialects.
- These effects compound. A small model has less room to recover when the
  prompt, chat template, or tool-result format differs from the distribution it
  was trained on.

The useful conclusion is not that SLMs cannot call tools. A purpose-trained
SLM can be excellent for a narrow and stable tool surface. Reliability declines
rapidly as the tool catalogue, schema complexity, ambiguity, context length,
or number of execution steps grows.

## What correct tool calling entails

A parseable JSON object is only the first layer. A reliable agent must:

1. decide whether a tool is needed;
2. select the correct tool among similar alternatives;
3. map the request to required and optional arguments;
4. obey types, enums, formats, and JSON Schema constraints;
5. emit the model-specific wire format expected by the serving stack;
6. associate a result with the correct tool call;
7. interpret that result and maintain state across further steps; and
8. stop, retry, or repair appropriately after an error.

ToolBench, API-Bank, and ToolSandbox were created because conventional QA or
"valid JSON" metrics do not cover this full execution loop. ToolBench evaluates
large-scale API use, API-Bank includes multi-turn tool interactions, and
ToolSandbox evaluates stateful, constrained tool-use tasks.

## Evidence for the training explanation

Tool use is not a uniformly represented capability in ordinary web text.
Models need supervised or synthetic trajectories containing tool definitions,
selection decisions, structured calls, execution results, and recovery after
errors.

Toolformer demonstrated that a language model can learn to decide where to use
tools and insert calls using self-supervised annotations. Gorilla and ToolLLM
then focused on learning API selection and invocation from API documentation
and tool-use data. This supports the claim that tool calling depends heavily on
targeted data and post-training, rather than emerging reliably from general
language modelling alone.

For an SLM, the problem is amplified by limited representational capacity. The
same parameter budget must represent language, world knowledge, instruction
following, API semantics, schemas, output syntax, and execution state. The
typical consequence is lower tolerance for ambiguity and distribution shift:
renaming a field, changing the order of definitions, adding distractor tools,
or requiring a second call can cause disproportionate degradation.

This is a capacity-and-training effect, not a proof that small models are
unsuitable. Google's FunctionGemma is a deliberately small model specialised
for function calling, demonstrating that focused training can make a compact
model useful when the interface and task scope are controlled.

## Evidence for the protocol-fragmentation explanation

There is no universal end-to-end serialisation that every model, provider,
server, and harness shares.

- **OpenAI** tool calling uses structured tool definitions based on JSON Schema
  and associates tool-call outputs with generated call identifiers. Its strict
  mode can constrain arguments to a supported schema subset.
- **Anthropic** uses content blocks such as `tool_use` and `tool_result`, with
  its own message structure and correlation rules.
- **Hugging Face Transformers** exposes tool use through each checkpoint's
  `chat_template`; the required special tokens, roles, and serialisation differ
  across model families.
- **Model Context Protocol (MCP)** standardises client-server discovery and
  invocation such as `tools/list` and `tools/call` over JSON-RPC. It does not
  by itself make all base models emit an identical internal tool-call format.

Therefore a statement such as "this model supports function calling" is
insufficient. The checkpoint's expected chat template, the inference server's
template application, the harness's tool-definition adapter, and the
tool-result transport must agree.

## How the mismatch happens in practice

The proposed mechanism is valid: a model can emit the pattern it learned and
still fail in a different harness. Common examples include:

- The checkpoint produces special tool-call tokens, but the harness expects
  plain JSON.
- The serving layer applies a generic chat template instead of the model's
  official template.
- Tool outputs are inserted as ordinary user or assistant text rather than the
  tool-result role/block the checkpoint saw during training.
- A harness drops, rewrites, or incorrectly associates tool-call IDs.
- The model was trained on sequential calls but the runtime requests parallel
  calls, or the inverse.
- A schema uses descriptive names, nesting, or constraints outside the model's
  training distribution.
- A regex parser partially repairs output and silently changes valid argument
  content or makes invalid content appear executable.

These failures often look like model hallucination, but can instead be a
template or protocol integration bug. Larger models sometimes recover through
generalisation; SLMs are usually less forgiving of the mismatch.

## What protocol mismatch does not explain

Even with a perfectly aligned interface, a model may still:

- call the wrong tool;
- omit a required argument;
- invent an unavailable value;
- violate an enum or type;
- forget a user constraint from earlier context;
- repeat a non-transient failed call;
- mistake an error response for successful data; or
- fail to plan a multi-step workflow.

Those are semantic, planning, grounding, and state-management failures. They
are expected to be more frequent in smaller or insufficiently tool-trained
models, even when the output parses correctly.

## How to distinguish a model limitation from a harness limitation

Use controlled tests rather than a single aggregate "tool-call success" rate.

| Test | Interpretation |
| --- | --- |
| Run the exact prompt with the checkpoint's official chat template. | A large improvement points to serving or template misalignment. |
| Offer one simple tool with a minimal schema. | Success here but failure with many tools suggests selection overload or context pressure. |
| Compare explicit arguments with arguments that must be inferred. | A gap indicates semantic extraction or grounding limits. |
| Measure valid JSON separately from schema-valid arguments. | Parsing success alone is not execution reliability. |
| Return results in the model's native tool-result structure. | A failure after a correct first call may be result-transport incompatibility. |
| Run the same harness with a stronger model. | A shared failure implicates integration; a small-model-only failure implicates capacity or tuning. |
| Score selection, arguments, execution, result use, and repair independently. | Pinpoints the broken stage and prevents misleading averages. |

## Recommended architecture for reliable SLM tool use

1. Use the checkpoint's official chat template without a generic fallback.
2. Prefer models explicitly trained and evaluated for function/tool calling.
3. Keep the visible tool set small; retrieve or route the relevant subset per
   turn rather than presenting every tool.
4. Make tool names and descriptions discriminative, concise, and unambiguous.
5. Keep schemas shallow where possible and avoid hidden assumptions in
   required arguments.
6. Validate arguments against JSON Schema before execution; never use parsing
   success as authorisation to execute.
7. Return structured, actionable tool errors so the model can repair a call.
8. Permit a bounded repair loop, with idempotency and side-effect safeguards.
9. Adapt the model-facing representation to the provider, even if MCP is used
   as the transport-level integration standard.
10. Evaluate against representative production traces, including failures and
    multi-step tasks, rather than only synthetic one-call examples.

## Scope guidance

SLMs are a strong fit for a small, repetitive, deterministic tool surface with
explicit user-provided arguments. They are a weaker fit for a large and
ambiguous catalogue of APIs, deeply nested schemas, implicit parameter
extraction, long contexts, irreversible actions, or multi-step recovery.

For demanding tasks, use a stronger model, a dedicated router/reranker,
constrained decoding, or a hybrid system in which the SLM handles narrow tasks
and escalates uncertain requests.

## Conclusion

The hypothesis is substantially correct when stated precisely:

> SLMs tend to be less reliable at tool calling because they have less capacity
> and often less specialised tool-use training. Protocol fragmentation between
> checkpoint, inference server, and harness adds integration failures: a model
> may emit the correct pattern for its training distribution but the wrong one
> for the runtime consuming it.

Standardising transport through MCP helps tool discovery and invocation, but
does not remove the need to align the model's native template, schema exposure,
result format, and evaluation harness. The best solution is end-to-end
alignment plus constrained execution—not merely adopting a single protocol.

## Sources

- Schick et al., [Toolformer: Language Models Can Teach Themselves to Use
  Tools](https://arxiv.org/abs/2302.04761), 2023.
- Patil et al., [Gorilla: Large Language Model Connected with Massive
  APIs](https://arxiv.org/abs/2305.15334), 2023.
- Qin et al., [ToolLLM: Facilitating Large Language Models to Master 16000+
  Real-world APIs](https://arxiv.org/abs/2307.16789), 2023.
- Li et al., [API-Bank: A Comprehensive Benchmark for Tool-Augmented LLMs](https://arxiv.org/abs/2304.08244), 2023.
- Ruan et al., [ToolSandbox: A Stateful, Conversational, Interactive Evaluation
  Benchmark for LLM Tool Use Capabilities](https://arxiv.org/abs/2407.14584),
  2024.
- OpenAI, [Function calling in the API](https://platform.openai.com/docs/guides/function-calling).
- Anthropic, [Tool use](https://docs.anthropic.com/en/docs/build-with-claude/tool-use).
- Hugging Face, [Tool use and chat templates](https://huggingface.co/docs/transformers/chat_templating#advanced-tool-use--function-calling).
- Model Context Protocol, [Tools specification](https://modelcontextprotocol.io/specification/2025-06-18/server/tools).
- Google, [FunctionGemma](https://ai.google.dev/gemma/docs/functiongemma), accessed 2026-07-09.

## TDD Cycle Evidence

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Documentation-only addition; no production behavior or test suite affected. | `git status --short` | N/A |
| Understand | Read documentation index, research category, and repository TDD guidance. | `.okf/README.md`, `.okf/research/harness.md`, `.claude/skills/tdd-cycle-evidence/SKILL.md` | Documentation conventions identified |
| RED | No executable behavior changed. | N/A | N/A |
| GREEN | Research report created and index updated. | `.okf/research/slm-tool-calling-reliability.md`, `.okf/README.md` | Complete |
| TRIANGULATE | Report separates format failures from semantic failures and proposes controlled discrimination tests. | `## How to distinguish a model limitation from a harness limitation` | Complete |
| REFACTOR | Markdown structure, metadata, and whitespace verified after patching. | `rg -n '^(---|updated_at:|summary:|#|## )' .okf/research/slm-tool-calling-reliability.md`; `git diff --check` | Pass |
