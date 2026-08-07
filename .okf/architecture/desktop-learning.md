---
updated_at: 2026-08-06
summary: Durable desktop learning runs, human approval, and bounded workspace lesson selection.
---

# Desktop learning

Desktop learning is owned by `internal/learning`; it is intentionally absent
from `agentcore` and from the TUI. `/learn` captures the latest completed
durable session sequence into a bounded immutable input before returning. A
workspace FIFO worker then makes one tool-free provider request using the
provider/model snapshot captured at enqueue time.

Runs, candidates, decisions, provenance, usage, and approved lessons are stored
outside session events. Production uses the private `learning.json` data file;
tests use the same store contract in memory. Approval atomically creates one
workspace-scoped lesson and is idempotent. Rejected, disabled, or deleted
lessons cannot be selected.

Selection is lexical, deterministic, limited to five lessons and an estimated
1,500 tokens, and never calls a provider. The rendered section is explicitly
user-approved guidance and belongs after project instructions and before the
runtime environment in a future turn's system prompt.
