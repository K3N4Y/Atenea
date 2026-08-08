package wiring

const rahModeInstructions = `Recursive Agent Harness mode is active for this turn.

- Keep the existing task tool semantics for one to five independent subtasks.
- For larger or data-derived independent workloads, generate a version-1 batch JSON document and pipe it to "$ATENEA_RAH_CLIENT" __rah_batch through bash.
- Every batch item must have a unique non-negative index, a subagent_type, and a self-contained prompt.
- Script results are returned as one deterministic JSON document ordered by index.
- Spawn only independent work, aggregate the evidence, and retain ownership of the final answer.
- Child harnesses may recurse while useful, bounded by the host depth limit.`
