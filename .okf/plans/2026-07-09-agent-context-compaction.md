---
updated_at: 2026-07-09
summary: Implementation plan for agent context compaction.
---

# Agent Context Compaction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement durable context compaction, 80% preventive and reactive to overflow, preserving the user's last prompt, complete tool groups and an auditable card in the UI.

**Architecture:** The event log remains immutable. A real compactor selects history, generates a JSON digest with the same provider/model and atomically commits a `Context.Compacted` along with `ContextEpoch.BaselineSeq`. The runner rebuilds the request from a durable `RunnerContext`; the frontend continues rehydrating the entire log and represents the checkpoint as a non-conversational item.

**Tech Stack:** Go 1.23+, SQLite (`modernc.org/sqlite`), Wails v2, Vue 3, Pinia, TypeScript, Vitest.

---

## Preconditions and discipline TDD

- Execute the plan from a dedicated worktree. If a branch is created, use prefix `posthog-code/`.
- Before each task, run the specific safety net test indicated.
- Follow by task: `Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`.
- Do not advance if the RED test fails due to compilation that does not correspond to the expected behavior.
- In concurrent or atomic code, repeat the gate with `-race` when the package uses it. allow.
- Each commit uses these trailers, after an empty line:

```text
Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
```

## File Map

### New backend files

- `internal/llm/context.go`: window metadata, conservative estimator and normalized overflow error.
- `internal/llm/context_test.go`: limits, threshold and estimation of system/tools/messages.
- `internal/session/compaction.go`: `StructuredSummary`, `CompactionCheckpoint`, `RunnerContext` types and domain errors.
- `internal/session/compaction_store_contract_test.go`: shared contract checkpoint for MemoryStore and SQLiteStore.
- `internal/session/runner/compaction_select.go`: semantic grouping and history selection.
- `internal/session/runner/compaction_select_test.go`: last user, tools and fallback by budget.
- `internal/session/runner/compaction_summary.go`: isolated call, JSON prompt and summary validation.
- `internal/session/runner/compaction_summary_test.go`: stream, timeout and responses invalid.
- `internal/session/runner/compactor.go`: preventive policy, checkpoint assembly and commit.
- `internal/session/runner/compactor_test.go`: real pipeline and successive compactions.

### New frontend files

- `frontend/src/components/ContextCompactedCard.vue`: non-conversational expandable card.
- `frontend/src/components/ContextCompactedCard.test.ts`: render, toggle and summary fields.

### Modified files

- `internal/llm/provider.go`: cause typed in `llm.Event`.
- `internal/llm/openai.go`: overflow classification of the OpenAI-compatible stream.
- `internal/llm/openai_test.go`: overflow vs generic error.
- `internal/session/session.go`: payload `Compaction` in `SessionEvent`.
- `internal/session/event.go`: `KindContextCompacted`.
- `internal/session/store.go`: `RunnerContext` and `CommitCompaction`.
- `internal/session/memstore.go`: epoch/atomic checkpoint in memory.
- `internal/session/sqlitestore.go`: `session_context` table, JSON payload and CAS transaction.
- `internal/event/store.go`: delegation and issuance of `CommitCompaction`.
- `internal/event/child_permission_store.go`: delegation of the new contract.
- `internal/session/runner/runner.go`: configurable real compactor.
- `internal/session/runner/turn.go`: compacted projection and bounded retry.
- `internal/session/runner/turn_signals_test.go`: replacement of the old fake with new signals.
- `internal/session/runner/turn_failure_test.go`: reactive overflow and second overflow.
- `internal/wiring/wiring.go`: installation of the real compactor.
- `internal/wiring/wiring_test.go`: wiring with and without known model.
- `internal/tui/engine.go`: propagate the resolved model to the shared runner.
- `internal/tui/engine_test.go`: TUI request uses the configured model tag.
- `cmd/atenea-tui/main.go`: deliver the resolved model to `EngineConfig`.
- `frontend/src/stores/chat.ts`: types and fold of `Context.Compacted`.
- `frontend/src/stores/chat.test.ts`: live and rehydrated projection.
- `frontend/src/components/MessageList.vue`: registration of the new item.
- `frontend/src/components/MessageList.test.ts`: dispatch to the card.
- `frontend/src/lib/contextWindow.ts`: align the visual catalog with the backend.
- `frontend/src/lib/contextWindow.test.ts`: known catalog without changing the current visual fallback.

---

### Task 1: Model limits, token estimation, and overflow type

**Files:**
- Create: `internal/llm/context.go`
- Create: `internal/llm/context_test.go`
- Modify: `internal/llm/provider.go`

- [ ] **Step 1: Run the LLM package safety net**

Run: `go test ./internal/llm`

Expected: PASS before changes.

- [ ] **Step 2: Write failing tests for known limits, unknown models, full-request estimation, and typed overflow**

Create `internal/llm/context_test.go`:

```go
package llm

import (
	"errors"
	"testing"
)

func TestContextWindow_KnownAndUnknownModels(t *testing.T) {
	if got, ok := ContextWindow("anthropic/claude-opus-4.8"); !ok || got != 200_000 {
		t.Fatalf("ContextWindow known = (%d, %v), want (200000, true)", got, ok)
	}
	if got, ok := ContextWindow("totally/unknown"); ok || got != 0 {
		t.Fatalf("ContextWindow unknown = (%d, %v), want (0, false)", got, ok)
	}
}

func TestEstimateRequestTokens_IncludesSystemToolsMessagesAndOutputReserve(t *testing.T) {
	req := Request{
		Model: "anthropic/claude-opus-4.8",
		System: "system text",
		Messages: []Message{{Role: "user", Text: "user text"}},
		Tools: []ToolDef{{Name: "read", Description: "read a file", Schema: []byte(`{"type":"object"}`)}},
		MaxOutputTokens: 4_096,
	}
	withoutTools := req
	withoutTools.Tools = nil
	withoutSystem := req
	withoutSystem.System = ""

	got := EstimateRequestTokens(req)
	if got <= EstimateRequestTokens(withoutTools) {
		t.Fatal("tool definitions must increase the estimate")
	}
	if got <= EstimateRequestTokens(withoutSystem) {
		t.Fatal("system prompt must increase the estimate")
	}
	if got < req.MaxOutputTokens {
		t.Fatalf("estimate = %d, must include output reserve %d", got, req.MaxOutputTokens)
	}
}

func TestNeedsPreventiveCompaction_TriggersAtEightyPercent(t *testing.T) {
	window := 100
	if NeedsPreventiveCompaction(79, window) {
		t.Fatal("79% must not compact")
	}
	if !NeedsPreventiveCompaction(80, window) {
		t.Fatal("80% must compact")
	}
}

func TestContextOverflowError_IsDiscoverableWithErrorsAs(t *testing.T) {
	wrapped := errors.Join(errors.New("provider failed"), &ContextOverflowError{Message: "maximum context length"})
	var overflow *ContextOverflowError
	if !errors.As(wrapped, &overflow) {
		t.Fatal("ContextOverflowError must be discoverable")
	}
}
```

Modify `internal/llm/provider.go` so `Request` reserves output and `Event` can carry a typed failure:

```go
type Request struct {
	Model           string
	System          string
	Messages        []Message
	Tools           []ToolDef
	MaxOutputTokens int
}

type Event struct {
	Kind             EventKind
	CallID           string
	ToolName         string
	Input            json.RawMessage
	Text             string
	Usage            *Usage
	Err              error
	ProviderExecuted bool
}
```

- [ ] **Step 3: Run RED**

Run: `go test -run 'Test(ContextWindow|EstimateRequestTokens|NeedsPreventiveCompaction|ContextOverflowError)' -v ./internal/llm`

Expected: FAIL because the context helpers and `MaxOutputTokens` do not exist.

- [ ] **Step 4: Implement the conservative catalog and estimator**

Create `internal/llm/context.go`:

```go
package llm

import (
	"encoding/json"
	"fmt"
)

const preventiveCompactionPercent = 80

var contextWindows = map[string]int{
	"anthropic/claude-opus-4.8":   200_000,
	"anthropic/claude-sonnet-4.5": 200_000,
	"anthropic/claude-3.5-sonnet":  200_000,
	"openai/gpt-4o":                128_000,
	"google/gemini-2.5-pro":        1_048_576,
}

type ContextOverflowError struct {
	Message string
}

func (e *ContextOverflowError) Error() string {
	if e.Message == "" {
		return "provider context window exceeded"
	}
	return e.Message
}

func ContextWindow(model string) (int, bool) {
	window, ok := contextWindows[model]
	return window, ok
}

func NeedsPreventiveCompaction(estimatedTokens, contextWindow int) bool {
	return contextWindow > 0 && estimatedTokens*100 >= contextWindow*preventiveCompactionPercent
}

func EstimateRequestTokens(req Request) int {
	bytes := len(req.System)
	for _, message := range req.Messages {
		bytes += len(message.Role) + len(message.Text) + len(message.ToolCallID) + 12
		for _, call := range message.ToolCalls {
			bytes += len(call.ID) + len(call.Name) + len(call.Arguments) + 12
		}
	}
	for _, tool := range req.Tools {
		bytes += len(tool.Name) + len(tool.Description) + len(tool.Schema) + 16
	}
	// Three UTF-8 bytes per token is deliberately conservative for mixed prose,
	// source code and JSON. Framing overhead is represented by the constants above.
	return (bytes+2)/3 + req.MaxOutputTokens
}

func EstimateJSONTokens(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return (len(encoded) + 2) / 3
}

func FormatContextUsage(estimated, window int) string {
	return fmt.Sprintf("%d/%d estimated tokens", estimated, window)
}
```

- [ ] **Step 5: Run GREEN and triangulate unknown-window behavior**

Run: `go test -run 'Test(ContextWindow|EstimateRequestTokens|NeedsPreventiveCompaction|ContextOverflowError)' -v ./internal/llm`

Expected: PASS.

Add this case to `internal/llm/context_test.go`:

```go
func TestNeedsPreventiveCompaction_UnknownWindowNeverTriggers(t *testing.T) {
	if NeedsPreventiveCompaction(1_000_000, 0) {
		t.Fatal("unknown window must rely on reactive overflow")
	}
}
```

Run: `go test ./internal/llm`

Expected: PASS.

- [ ] **Step 6: Commit Task 1**

```bash
git add internal/llm/context.go internal/llm/context_test.go internal/llm/provider.go
git commit -m "$(cat <<'EOF'
feat(llm): add context budgeting primitives

Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
EOF
)"
```

---

### Task 2: Compaction domain types and durable event payload

**Files:**
- Create: `internal/session/compaction.go`
- Create: `internal/session/compaction_test.go`
- Modify: `internal/session/session.go`
- Modify: `internal/session/event.go`

- [ ] **Step 1: Run the session safety net**

Run: `go test ./internal/session`

Expected: PASS.

- [ ] **Step 2: Write RED tests for summary validation and event round-trip shape**

Create `internal/session/compaction_test.go`:

```go
package session

import (
	"encoding/json"
	"errors"
	"testing"
)

func validSummary() StructuredSummary {
	return StructuredSummary{
		CurrentGoal: "compact the agent context",
		Constraints: []string{"keep the event log"},
		Decisions: []string{"use durable checkpoints"},
		Completed: []string{},
		Files: []string{"internal/session/store.go"},
		ToolResults: []string{},
		Failures: []string{},
		Pending: []string{"implement stores"},
		Invariants: []string{"tool calls stay paired"},
	}
}

func TestStructuredSummary_ValidateRequiresEveryJSONField(t *testing.T) {
	raw := []byte(`{"current_goal":"x"}`)
	var summary StructuredSummary
	if err := DecodeStructuredSummary(raw, &summary); err == nil {
		t.Fatal("partial summary must fail")
	}
}

func TestStructuredSummary_RoundTripsAllFields(t *testing.T) {
	want := validSummary()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got StructuredSummary
	if err := DecodeStructuredSummary(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.CurrentGoal != want.CurrentGoal || len(got.Invariants) != 1 {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestErrCompactionConflict_IsStableDomainError(t *testing.T) {
	if !errors.Is(ErrCompactionConflict, ErrCompactionConflict) {
		t.Fatal("conflict must support errors.Is")
	}
}
```

- [ ] **Step 3: Run RED**

Run: `go test -run 'TestStructuredSummary|TestErrCompactionConflict' -v ./internal/session`

Expected: FAIL because the domain types do not exist.

- [ ] **Step 4: Implement the domain types and add the event kind**

Create `internal/session/compaction.go`:

```go
package session

import (
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrCompactionConflict    = errors.New("compaction checkpoint conflicts with current epoch")
	ErrNoCompactableHistory  = errors.New("no compactable history before current activity")
	ErrActivityTooLarge      = errors.New("current user activity does not fit the model context")
	ErrInvalidSummary        = errors.New("invalid structured compaction summary")
)

type CompactionReason string

const (
	CompactionPreventive CompactionReason = "preventive"
	CompactionOverflow   CompactionReason = "overflow"
)

type StructuredSummary struct {
	CurrentGoal string   `json:"current_goal"`
	Constraints []string `json:"constraints_and_instructions"`
	Decisions   []string `json:"decisions"`
	Completed   []string `json:"completed_work"`
	Files       []string `json:"files_and_changes"`
	ToolResults []string `json:"relevant_tool_results"`
	Failures    []string `json:"failures_and_attempts"`
	Pending     []string `json:"pending_and_next_step"`
	Invariants  []string `json:"facts_not_to_reinterpret"`
}

var summaryKeys = []string{
	"current_goal",
	"constraints_and_instructions",
	"decisions",
	"completed_work",
	"files_and_changes",
	"relevant_tool_results",
	"failures_and_attempts",
	"pending_and_next_step",
	"facts_not_to_reinterpret",
}

func DecodeStructuredSummary(raw []byte, out *StructuredSummary) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSummary, err)
	}
	for _, key := range summaryKeys {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("%w: missing %s", ErrInvalidSummary, key)
		}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSummary, err)
	}
	if out.CurrentGoal == "" {
		return fmt.Errorf("%w: current_goal is empty", ErrInvalidSummary)
	}
	return nil
}

type CompactionCheckpoint struct {
	Summary              StructuredSummary `json:"summary"`
	ExpectedEpoch        ContextEpoch       `json:"expected_epoch"`
	CoveredThroughSeq    Seq                `json:"covered_through_seq"`
	AnchorUserSeq        Seq                `json:"anchor_user_seq"`
	PreservedFromSeq     Seq                `json:"preserved_from_seq"`
	Model                string             `json:"model"`
	Reason               CompactionReason   `json:"reason"`
	InputTokensBefore    int                `json:"input_tokens_before"`
	EstimatedTokensAfter int                `json:"estimated_tokens_after"`
}

type RunnerContext struct {
	Epoch      ContextEpoch
	Checkpoint *CompactionCheckpoint
	Anchor     *Message
	Messages   []Message
}
```

Add to `internal/session/event.go`:

```go
KindContextCompacted EventKind = "Context.Compacted"
```

Add to `SessionEvent` in `internal/session/session.go`:

```go
Compaction *CompactionCheckpoint
```

- [ ] **Step 5: Run GREEN and JSON serialization triangulation**

Run: `go test -run 'TestStructuredSummary|TestErrCompactionConflict' -v ./internal/session`

Expected: PASS.

Add:

```go
func TestSessionEvent_ContextCompactedIsJSONSerializable(t *testing.T) {
	event := SessionEvent{Kind: KindContextCompacted, Compaction: &CompactionCheckpoint{
		Summary: validSummary(), Reason: CompactionPreventive, CoveredThroughSeq: 4,
	}}
	if _, err := json.Marshal(event); err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
}
```

Run: `go test ./internal/session`

Expected: PASS.

- [ ] **Step 6: Commit Task 2**

```bash
git add internal/session/compaction.go internal/session/compaction_test.go internal/session/session.go internal/session/event.go
git commit -m "$(cat <<'EOF'
feat(session): define durable compaction checkpoint

Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
EOF
)"
```

---

### Task 3: Store contract and atomic MemoryStore checkpoint

**Files:**
- Create: `internal/session/compaction_store_contract_test.go`
- Modify: `internal/session/store.go`
- Modify: `internal/session/memstore.go`

- [ ] **Step 1: Write the shared failing contract for commit, projection, conflict, and successive checkpoints**

Create `internal/session/compaction_store_contract_test.go` with a reusable contract:

```go
package session

import (
	"context"
	"errors"
	"testing"
)

type compactionStoreFactory func(t *testing.T) Store

func runCompactionStoreContract(t *testing.T, factory compactionStoreFactory) {
	t.Helper()
	t.Run("commit advances epoch and projects summary", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		userSeq, _ := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "u1", Role: RoleUser, Text: "first"}})
		assistantSeq, _ := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "a1", Role: RoleAssistant, Text: "done"}})
		anchorSeq, _ := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "u2", Role: RoleUser, Text: "current"}})
		epoch, _ := store.Epoch(ctx, "s1")

		checkpoint := CompactionCheckpoint{
			Summary: validSummary(), ExpectedEpoch: epoch,
			CoveredThroughSeq: assistantSeq, AnchorUserSeq: anchorSeq,
			PreservedFromSeq: anchorSeq, Reason: CompactionPreventive,
		}
		seq, err := store.CommitCompaction(ctx, "s1", checkpoint)
		if err != nil {
			t.Fatalf("CommitCompaction: %v", err)
		}
		if seq <= anchorSeq || userSeq == 0 {
			t.Fatalf("checkpoint seq = %d, anchor = %d", seq, anchorSeq)
		}

		got, err := store.ContextForRunner(ctx, "s1")
		if err != nil {
			t.Fatalf("ContextForRunner: %v", err)
		}
		if got.Epoch.BaselineSeq != assistantSeq || got.Epoch.Revision != epoch.Revision+1 {
			t.Fatalf("epoch = %+v", got.Epoch)
		}
		if got.Checkpoint == nil || got.Checkpoint.Summary.CurrentGoal == "" {
			t.Fatalf("checkpoint = %+v", got.Checkpoint)
		}
		events, _ := store.Events(ctx, "s1", 0)
		last := events[len(events)-1]
		if last.Kind != KindContextCompacted || last.Compaction == nil || last.Compaction.CoveredThroughSeq != assistantSeq {
			t.Fatalf("durable compaction event = %+v", last)
		}
		if len(got.Messages) != 1 || got.Messages[0].Seq != anchorSeq {
			t.Fatalf("messages = %+v", got.Messages)
		}
	})

	t.Run("stale epoch is atomic conflict", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		first, _ := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "u1", Role: RoleUser, Text: "one"}})
		epoch, _ := store.Epoch(ctx, "s1")
		checkpoint := CompactionCheckpoint{Summary: validSummary(), ExpectedEpoch: epoch, CoveredThroughSeq: first, AnchorUserSeq: first, PreservedFromSeq: first}
		if _, err := store.CommitCompaction(ctx, "s1", checkpoint); err != nil {
			t.Fatal(err)
		}
		before, _ := store.Events(ctx, "s1", 0)
		if _, err := store.CommitCompaction(ctx, "s1", checkpoint); !errors.Is(err, ErrCompactionConflict) {
			t.Fatalf("second commit error = %v", err)
		}
		after, _ := store.Events(ctx, "s1", 0)
		if len(after) != len(before) {
			t.Fatalf("conflict appended event: before=%d after=%d", len(before), len(after))
		}
	})
}

func TestMemoryStore_CompactionContract(t *testing.T) {
	runCompactionStoreContract(t, func(t *testing.T) Store { return NewMemoryStore() })
}
```

- [ ] **Step 2: Extend the Store interface and run RED**

Add to `internal/session/store.go`:

```go
ContextForRunner(ctx context.Context, sessionID string) (RunnerContext, error)
CommitCompaction(ctx context.Context, sessionID string, checkpoint CompactionCheckpoint) (Seq, error)
```

Run: `go test -run TestMemoryStore_CompactionContract -v ./internal/session`

Expected: FAIL because `MemoryStore` does not implement the new methods.

- [ ] **Step 3: Add epoch state and implement atomic commit in MemoryStore**

Extend `MemoryStore`:

```go
type MemoryStore struct {
	mu          sync.Mutex
	sessions    map[string][]SessionEvent
	lastSeen    map[string]int
	epochs      map[string]ContextEpoch
	checkpoints map[string]CompactionCheckpoint
	clock       int
}
```

Initialize both maps in `NewMemoryStore`.

Replace `Epoch` with a locked lookup that returns the stored epoch, and add:

```go
func (s *MemoryStore) ContextForRunner(ctx context.Context, sessionID string) (RunnerContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, ok := s.sessions[sessionID]
	if !ok {
		return RunnerContext{}, ErrSessionNotFound
	}
	epoch := s.epochs[sessionID]
	messages := foldMessages(events, epoch.BaselineSeq)
	result := RunnerContext{Epoch: epoch, Messages: messages}
	if checkpoint, ok := s.checkpoints[sessionID]; ok {
		copy := checkpoint
		result.Checkpoint = &copy
		for _, message := range foldMessages(events, 0) {
			if message.Seq == checkpoint.AnchorUserSeq && message.Seq <= epoch.BaselineSeq {
				anchor := message
				result.Anchor = &anchor
				break
			}
		}
	}
	return result, nil
}

func (s *MemoryStore) CommitCompaction(ctx context.Context, sessionID string, checkpoint CompactionCheckpoint) (Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, ok := s.sessions[sessionID]
	if !ok {
		return 0, ErrSessionNotFound
	}
	current := s.epochs[sessionID]
	if current != checkpoint.ExpectedEpoch || checkpoint.CoveredThroughSeq < current.BaselineSeq {
		return 0, ErrCompactionConflict
	}
	if checkpoint.CoveredThroughSeq <= 0 || int(checkpoint.CoveredThroughSeq) > len(events) {
		return 0, ErrCompactionConflict
	}
	seq := Seq(len(events) + 1)
	event := SessionEvent{SessionID: sessionID, Seq: seq, Kind: KindContextCompacted, Compaction: &checkpoint}
	s.sessions[sessionID] = append(events, event)
	current.BaselineSeq = checkpoint.CoveredThroughSeq
	current.Revision++
	s.epochs[sessionID] = current
	s.checkpoints[sessionID] = checkpoint
	s.clock++
	s.lastSeen[sessionID] = s.clock
	return seq, nil
}
```

Extract the existing message fold into:

```go
func foldMessages(events []SessionEvent, sinceSeq Seq) []Message {
	out := make([]Message, 0)
	for _, event := range events {
		if event.Seq > sinceSeq && event.Message != nil {
			message := *event.Message
			message.Seq = event.Seq
			out = append(out, message)
		}
	}
	return out
}
```

- [ ] **Step 4: Run GREEN and race gate**

Run: `go test -run TestMemoryStore_CompactionContract -v ./internal/session && go test -race -run TestMemoryStore_CompactionContract ./internal/session`

Expected: PASS.

- [ ] **Step 5: Triangulate fallback anchor projection**

Add this subtest inside `runCompactionStoreContract`:

```go
t.Run("fallback rehydrates anchor before preserved suffix", func(t *testing.T) {
	store := factory(t)
	ctx := context.Background()
	oldSeq, _ := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "u1", Role: RoleUser, Text: "old"}})
	anchorSeq, _ := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "u2", Role: RoleUser, Text: "current"}})
	coveredSeq, _ := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "a1", Role: RoleAssistant, Text: "summarized middle"}})
	preservedSeq, _ := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "a2", Role: RoleAssistant, Text: "recent suffix"}})
	epoch, _ := store.Epoch(ctx, "s1")
	_, err := store.CommitCompaction(ctx, "s1", CompactionCheckpoint{
		Summary: validSummary(), ExpectedEpoch: epoch,
		CoveredThroughSeq: coveredSeq, AnchorUserSeq: anchorSeq,
		PreservedFromSeq: preservedSeq, Reason: CompactionPreventive,
	})
	if err != nil { t.Fatal(err) }
	got, err := store.ContextForRunner(ctx, "s1")
	if err != nil { t.Fatal(err) }
	if oldSeq == 0 { t.Fatal("seed failed") }
	if got.Anchor == nil || got.Anchor.Seq != anchorSeq || got.Anchor.Text != "current" {
		t.Fatalf("anchor = %+v", got.Anchor)
	}
	if len(got.Messages) != 1 || got.Messages[0].Seq != preservedSeq {
		t.Fatalf("preserved suffix = %+v", got.Messages)
	}
})
```

Run: `go test ./internal/session`

Expected: PASS.

- [ ] **Step 6: Commit Task 3**

```bash
git add internal/session/store.go internal/session/memstore.go internal/session/compaction_store_contract_test.go
git commit -m "$(cat <<'EOF'
feat(session): commit memory compaction atomically

Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
EOF
)"
```

---

### Task 4: SQLite migration, atomic CAS, restart reconstruction

**Files:**
- Modify: `internal/session/sqlitestore.go`
- Modify: `internal/session/sqlitestore_test.go`
- Modify: `internal/session/compaction_store_contract_test.go`

- [ ] **Step 1: Add SQLite to the shared contract and write restart RED**

Add:

```go
func TestSQLiteStore_CompactionContract(t *testing.T) {
	runCompactionStoreContract(t, func(t *testing.T) Store {
		store, err := NewSQLiteStore(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	})
}
```

Add to `internal/session/sqlitestore_test.go`:

```go
func TestSQLiteStore_CompactionSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	ctx := context.Background()
	first, err := NewSQLiteStore(path)
	if err != nil { t.Fatal(err) }
	oldSeq, _ := first.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "u1", Role: RoleUser, Text: "old"}})
	anchorSeq, _ := first.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "u2", Role: RoleUser, Text: "current"}})
	epoch, _ := first.Epoch(ctx, "s1")
	_, err = first.CommitCompaction(ctx, "s1", CompactionCheckpoint{
		Summary: validSummary(), ExpectedEpoch: epoch, CoveredThroughSeq: oldSeq,
		AnchorUserSeq: anchorSeq, PreservedFromSeq: anchorSeq, Reason: CompactionPreventive,
	})
	if err != nil { t.Fatal(err) }
	if err := first.Close(); err != nil { t.Fatal(err) }

	second, err := NewSQLiteStore(path)
	if err != nil { t.Fatal(err) }
	defer second.Close()
	got, err := second.ContextForRunner(ctx, "s1")
	if err != nil { t.Fatal(err) }
	if got.Epoch.BaselineSeq != oldSeq || got.Checkpoint == nil {
		t.Fatalf("restarted context = %+v", got)
	}
}
```

Run: `go test -run 'TestSQLiteStore_(CompactionContract|CompactionSurvivesRestart)' -v ./internal/session`

Expected: FAIL because SQLite lacks the new contract and schema.

- [ ] **Step 2: Add durable context schema and event payload column**

Extend `schema` in `internal/session/sqlitestore.go`:

```sql
CREATE TABLE IF NOT EXISTS session_context (
  session_id   TEXT PRIMARY KEY,
  agent        TEXT NOT NULL DEFAULT '',
  model        TEXT NOT NULL DEFAULT '',
  baseline_seq INTEGER NOT NULL DEFAULT 0,
  revision     INTEGER NOT NULL DEFAULT 0,
  checkpoint   BLOB
);
```

Add the idempotent event migration:

```go
`ALTER TABLE events ADD COLUMN compaction BLOB`,
```

Marshal `ev.Compaction` in `AppendEvent`, add `compaction` to the INSERT, and unmarshal it in `Events`.

- [ ] **Step 3: Implement SQLite Epoch, ContextForRunner, and CommitCompaction transaction**

Use a serializable transaction under `s.mu`:

```go
func (s *SQLiteStore) CommitCompaction(ctx context.Context, sessionID string, checkpoint CompactionCheckpoint) (Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return 0, err }
	defer tx.Rollback()

	current, err := epochTx(ctx, tx, sessionID)
	if err != nil { return 0, err }
	if current != checkpoint.ExpectedEpoch || checkpoint.CoveredThroughSeq < current.BaselineSeq {
		return 0, ErrCompactionConflict
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil { return 0, err }
	var seq int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO events (session_id, seq, kind, has_message, compaction)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE session_id = ?), ?, 0, ?)
		RETURNING seq`, sessionID, sessionID, string(KindContextCompacted), payload).Scan(&seq)
	if err != nil { return 0, err }

	checkpointPayload, err := json.Marshal(checkpoint)
	if err != nil { return 0, err }
	result, err := tx.ExecContext(ctx, `
		UPDATE session_context
		SET baseline_seq = ?, revision = revision + 1, checkpoint = ?
		WHERE session_id = ? AND agent = ? AND model = ? AND baseline_seq = ? AND revision = ?`,
		checkpoint.CoveredThroughSeq, checkpointPayload, sessionID,
		checkpoint.ExpectedEpoch.Agent, checkpoint.ExpectedEpoch.Model,
		checkpoint.ExpectedEpoch.BaselineSeq, checkpoint.ExpectedEpoch.Revision)
	if err != nil { return 0, err }
	rows, err := result.RowsAffected()
	if err != nil { return 0, err }
	if rows != 1 { return 0, ErrCompactionConflict }
	if err := tx.Commit(); err != nil { return 0, err }
	return Seq(seq), nil
}
```

For backward compatibility, `Epoch` and `ContextForRunner` must treat a missing
`session_context` row as the zero epoch. Inside `CommitCompaction`, before the CAS,
run in the same transaction:

```sql
INSERT INTO session_context (session_id) VALUES (?) ON CONFLICT(session_id) DO NOTHING
```

Implement `ContextForRunner` by reading epoch/checkpoint and selecting projected messages after `baseline_seq`; when `anchor_user_seq <= baseline_seq`, fetch that exact message separately.

- [ ] **Step 4: Run GREEN, restart test, and broad store suite**

Run: `go test -run 'Test(SQLiteStore|MemoryStore)_Compaction' -v ./internal/session && go test ./internal/session`

Expected: PASS.

- [ ] **Step 5: Triangulate rollback on CAS conflict**

In the shared contract, count both events and epoch before and after a stale commit. Assert both remain identical:

```go
if afterEpoch != beforeEpoch {
	t.Fatalf("conflict changed epoch: before=%+v after=%+v", beforeEpoch, afterEpoch)
}
```

Run: `go test -race ./internal/session`

Expected: PASS.

- [ ] **Step 6: Commit Task 4**

```bash
git add internal/session/sqlitestore.go internal/session/sqlitestore_test.go internal/session/compaction_store_contract_test.go
git commit -m "$(cat <<'EOF'
feat(session): persist compaction checkpoints in sqlite

Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
EOF
)"
```

---

### Task 5: Store decorators preserve atomic commit and live emission

**Files:**
- Modify: `internal/event/store.go`
- Modify: `internal/event/store_test.go`
- Modify: `internal/event/child_permission_store.go`
- Modify: `internal/event/child_permission_store_test.go`

- [ ] **Step 1: Write RED tests for delegation and `Context.Compacted` bus emission**

Add to `internal/event/store_test.go`:

```go
func recordingBus() (*Bus, *fakeEmit) {
	fake := &fakeEmit{}
	return NewBus(fake.emit), fake
}

func TestEmittingStore_CommitCompactionPublishesCommittedEvent(t *testing.T) {
	ctx := context.Background()
	inner := session.NewMemoryStore()
	bus, fake := recordingBus()
	store := NewEmittingStore(inner, bus)
	oldSeq, _ := store.AppendEvent(ctx, "s1", session.SessionEvent{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "old"}})
	anchorSeq, _ := store.AppendEvent(ctx, "s1", session.SessionEvent{Message: &session.Message{ID: "u2", Role: session.RoleUser, Text: "current"}})
	epoch, _ := store.Epoch(ctx, "s1")
	checkpoint := session.CompactionCheckpoint{
		Summary: session.StructuredSummary{CurrentGoal: "continue", Constraints: []string{}, Decisions: []string{}, Completed: []string{}, Files: []string{}, ToolResults: []string{}, Failures: []string{}, Pending: []string{}, Invariants: []string{}},
		ExpectedEpoch: epoch, CoveredThroughSeq: oldSeq, AnchorUserSeq: anchorSeq, PreservedFromSeq: anchorSeq,
	}
	seq, err := store.CommitCompaction(ctx, "s1", checkpoint)
	if err != nil { t.Fatal(err) }
	fake.mu.Lock()
	defer fake.mu.Unlock()
	last, ok := fake.payloads[len(fake.payloads)-1].(session.SessionEvent)
	if !ok { t.Fatalf("payload = %T", fake.payloads[len(fake.payloads)-1]) }
	if last.Kind != session.KindContextCompacted || last.Seq != seq || last.Compaction == nil {
		t.Fatalf("emitted = %+v", last)
	}
}
```

Add to `internal/event/child_permission_store_test.go`:

```go
func TestChildPermissionStore_DelegatesCompactionWithoutSurfacingToParent(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEmit{}
	inner := session.NewMemoryStore()
	store := NewChildPermissionStore("parent", inner, NewBus(fake.emit))
	oldSeq, _ := store.AppendEvent(ctx, "child", session.SessionEvent{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "old"}})
	anchorSeq, _ := store.AppendEvent(ctx, "child", session.SessionEvent{Message: &session.Message{ID: "u2", Role: session.RoleUser, Text: "current"}})
	epoch, _ := store.Epoch(ctx, "child")
	_, err := store.CommitCompaction(ctx, "child", session.CompactionCheckpoint{
		Summary: session.StructuredSummary{CurrentGoal: "continue", Constraints: []string{}, Decisions: []string{}, Completed: []string{}, Files: []string{}, ToolResults: []string{}, Failures: []string{}, Pending: []string{}, Invariants: []string{}},
		ExpectedEpoch: epoch, CoveredThroughSeq: oldSeq, AnchorUserSeq: anchorSeq, PreservedFromSeq: anchorSeq,
	})
	if err != nil { t.Fatal(err) }
	context, err := store.ContextForRunner(ctx, "child")
	if err != nil || context.Checkpoint == nil { t.Fatalf("context=%+v err=%v", context, err) }
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.payloads) != 0 { t.Fatalf("parent emissions = %+v", fake.payloads) }
}
```

Run: `go test -run 'Test(EmittingStore_CommitCompaction|ChildPermissionStore_Compaction)' -v ./internal/event`

Expected: FAIL because decorators do not implement the new Store methods.

- [ ] **Step 2: Implement decorator delegation**

In `EmittingStore`:

```go
func (s *EmittingStore) ContextForRunner(ctx context.Context, sessionID string) (session.RunnerContext, error) {
	return s.inner.ContextForRunner(ctx, sessionID)
}

func (s *EmittingStore) CommitCompaction(ctx context.Context, sessionID string, checkpoint session.CompactionCheckpoint) (session.Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, err := s.inner.CommitCompaction(ctx, sessionID, checkpoint)
	if err != nil { return seq, err }
	event := session.SessionEvent{SessionID: sessionID, Seq: seq, Kind: session.KindContextCompacted, Compaction: &checkpoint}
	s.bus.Publish(event)
	return seq, nil
}
```

In `ChildPermissionStore`, delegate both methods directly to `inner`; do not publish compaction on the parent channel.

- [ ] **Step 3: Run GREEN and package race suite**

Run: `go test ./internal/event && go test -race ./internal/event`

Expected: PASS.

- [ ] **Step 4: Commit Task 5**

```bash
git add internal/event/store.go internal/event/store_test.go internal/event/child_permission_store.go internal/event/child_permission_store_test.go
git commit -m "$(cat <<'EOF'
feat(event): emit committed context checkpoints

Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
EOF
)"
```

---

### Task 6: Semantic history grouping and budget fallback

**Files:**
- Create: `internal/session/runner/compaction_select.go`
- Create: `internal/session/runner/compaction_select_test.go`

- [ ] **Step 1: Run runner safety net**

Run: `go test ./internal/session/runner`

Expected: PASS.

- [ ] **Step 2: Write RED tests for last-user split and tool pairing**

Create `internal/session/runner/compaction_select_test.go`:

```go
package runner

import (
	"errors"
	"testing"

	"atenea/internal/session"
)

func TestSelectCompaction_NormalPreservesFromLastUser(t *testing.T) {
	messages := []session.Message{
		{Seq: 1, Role: session.RoleUser, Text: "old question"},
		{Seq: 2, Role: session.RoleAssistant, Text: "old answer"},
		{Seq: 3, Role: session.RoleUser, Text: "current question"},
		{Seq: 4, Role: session.RoleAssistant, Text: "working"},
	}
	selection, err := selectCompaction(messages, 10_000, func(group messageGroup) int { return 1 })
	if err != nil { t.Fatal(err) }
	if selection.CoveredThroughSeq != 2 || selection.Anchor.Seq != 3 || selection.PreservedFromSeq != 3 {
		t.Fatalf("selection = %+v", selection)
	}
}

func TestSelectCompaction_FallbackKeepsToolCallWithResult(t *testing.T) {
	messages := []session.Message{
		{Seq: 1, Role: session.RoleUser, Text: "old"},
		{Seq: 2, Role: session.RoleAssistant, Text: "old answer"},
		{Seq: 3, Role: session.RoleUser, Text: "current"},
		{Seq: 4, Role: session.RoleAssistant, ToolCalls: []session.ToolCall{{ID: "c1", Name: "read", Arguments: `{}`}}},
		{Seq: 5, Role: session.RoleTool, ToolCallID: "c1", Text: "large result"},
		{Seq: 6, Role: session.RoleAssistant, Text: "latest"},
	}
	selection, err := selectCompaction(messages, 2, func(group messageGroup) int { return len(group.Messages) })
	if err != nil { t.Fatal(err) }
	if selection.PreservedFromSeq != 6 {
		t.Fatalf("preserved from = %d, want 6", selection.PreservedFromSeq)
	}
	if selection.CoveredThroughSeq != 5 {
		t.Fatalf("covered through = %d, want complete tool result at 5", selection.CoveredThroughSeq)
	}
}

func TestSelectCompaction_AnchorAloneTooLarge(t *testing.T) {
	messages := []session.Message{{Seq: 1, Role: session.RoleUser, Text: "huge"}}
	_, err := selectCompaction(messages, 0, func(group messageGroup) int { return 1 })
	if !errors.Is(err, session.ErrActivityTooLarge) {
		t.Fatalf("error = %v", err)
	}
}
```

- [ ] **Step 3: Run RED**

Run: `go test -run TestSelectCompaction -v ./internal/session/runner`

Expected: FAIL because selection types/functions do not exist.

- [ ] **Step 4: Implement contiguous semantic groups and fallback selection**

Create `internal/session/runner/compaction_select.go`:

```go
package runner

import (
	"atenea/internal/session"
)

type messageGroup struct {
	Messages []session.Message
}

func (g messageGroup) FirstSeq() session.Seq { return g.Messages[0].Seq }
func (g messageGroup) LastSeq() session.Seq  { return g.Messages[len(g.Messages)-1].Seq }

type compactionSelection struct {
	ToSummarize       []session.Message
	Anchor            session.Message
	Preserved         []session.Message
	CoveredThroughSeq session.Seq
	PreservedFromSeq  session.Seq
}

func groupMessages(messages []session.Message) []messageGroup {
	groups := make([]messageGroup, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		group := messageGroup{Messages: []session.Message{message}}
		if message.Role == session.RoleAssistant && len(message.ToolCalls) > 0 {
			wanted := make(map[string]bool, len(message.ToolCalls))
			for _, call := range message.ToolCalls { wanted[call.ID] = true }
			for index+1 < len(messages) && messages[index+1].Role == session.RoleTool && wanted[messages[index+1].ToolCallID] {
				index++
				group.Messages = append(group.Messages, messages[index])
			}
		}
		groups = append(groups, group)
	}
	return groups
}

func selectCompaction(messages []session.Message, recentBudget int, estimate func(messageGroup) int) (compactionSelection, error) {
	anchorIndex := -1
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == session.RoleUser {
			anchorIndex = index
			break
		}
	}
	if anchorIndex < 0 { return compactionSelection{}, session.ErrNoCompactableHistory }
	anchor := messages[anchorIndex]
	if estimate(messageGroup{Messages: []session.Message{anchor}}) > recentBudget {
		return compactionSelection{}, session.ErrActivityTooLarge
	}
	if anchorIndex == 0 && len(messages) == 1 {
		return compactionSelection{}, session.ErrNoCompactableHistory
	}

	groups := groupMessages(messages[anchorIndex+1:])
	used := estimate(messageGroup{Messages: []session.Message{anchor}})
	preserveAt := len(groups)
	for index := len(groups) - 1; index >= 0; index-- {
		cost := estimate(groups[index])
		if used+cost > recentBudget { break }
		used += cost
		preserveAt = index
	}
	covered := anchor.Seq - 1
	toSummarize := append([]session.Message(nil), messages[:anchorIndex]...)
	if preserveAt > 0 {
		for _, group := range groups[:preserveAt] {
			toSummarize = append(toSummarize, group.Messages...)
			covered = group.LastSeq()
		}
	}
	preserved := []session.Message{anchor}
	preservedFrom := anchor.Seq
	if preserveAt < len(groups) {
		preservedFrom = groups[preserveAt].FirstSeq()
		for _, group := range groups[preserveAt:] { preserved = append(preserved, group.Messages...) }
	}
	return compactionSelection{ToSummarize: toSummarize, Anchor: anchor, Preserved: preserved, CoveredThroughSeq: covered, PreservedFromSeq: preservedFrom}, nil
}
```

- [ ] **Step 5: Run GREEN and triangulate orphan results**

Run: `go test -run TestSelectCompaction -v ./internal/session/runner`

Expected: PASS.

Add this test:

```go
func TestSelectCompaction_RejectsOrphanToolResult(t *testing.T) {
	messages := []session.Message{
		{Seq: 1, Role: session.RoleUser, Text: "current"},
		{Seq: 2, Role: session.RoleTool, ToolCallID: "missing", Text: "orphan"},
	}
	_, err := selectCompaction(messages, 10, func(group messageGroup) int { return 1 })
	if !errors.Is(err, errInvalidToolHistory) {
		t.Fatalf("error = %v", err)
	}
}
```

Add to `compaction_select.go`:

```go
var errInvalidToolHistory = errors.New("invalid tool call/result history")

func validateToolHistory(messages []session.Message) error {
	declared := map[string]bool{}
	for _, message := range messages {
		for _, call := range message.ToolCalls { declared[call.ID] = true }
		if message.Role == session.RoleTool && !declared[message.ToolCallID] {
			return errInvalidToolHistory
		}
	}
	return nil
}
```

Call `validateToolHistory` at the start of `selectCompaction`.

Run: `go test ./internal/session/runner`

Expected: PASS.

- [ ] **Step 6: Commit Task 6**

```bash
git add internal/session/runner/compaction_select.go internal/session/runner/compaction_select_test.go
git commit -m "$(cat <<'EOF'
feat(runner): select compactable semantic history

Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
EOF
)"
```

---

### Task 7: Isolated structured-summary generator

**Files:**
- Create: `internal/session/runner/compaction_summary.go`
- Create: `internal/session/runner/compaction_summary_test.go`

- [ ] **Step 1: Write RED tests for accumulation, no tools, validation, and cancellation**

Create a request-recording provider in `internal/session/runner/compaction_summary_test.go` and these tests:

```go
func TestSummaryGenerator_UsesSameModelWithoutToolsAndDecodesJSON(t *testing.T) {
	provider := &summaryProvider{events: []llm.Event{
		{Kind: llm.TextDelta, Text: `{"current_goal":"continue","constraints_and_instructions":[],`},
		{Kind: llm.TextDelta, Text: `"decisions":[],"completed_work":[],"files_and_changes":[],"relevant_tool_results":[],"failures_and_attempts":[],"pending_and_next_step":[],"facts_not_to_reinterpret":[]}`},
		{Kind: llm.StepEnded},
	}}
	generator := newSummaryGenerator(provider, time.Second)
	summary, err := generator.Generate(context.Background(), "model-a", nil, []session.Message{{Role: session.RoleUser, Text: "old"}})
	if err != nil { t.Fatal(err) }
	if summary.CurrentGoal != "continue" { t.Fatalf("summary = %+v", summary) }
	request := provider.requests[0]
	if request.Model != "model-a" || len(request.Tools) != 0 || request.MaxOutputTokens == 0 {
		t.Fatalf("request = %+v", request)
	}
}

func TestSummaryGenerator_InvalidJSONDoesNotSucceed(t *testing.T) {
	provider := &summaryProvider{events: []llm.Event{{Kind: llm.TextDelta, Text: `{}`}, {Kind: llm.StepEnded}}}
	_, err := newSummaryGenerator(provider, time.Second).Generate(context.Background(), "model-a", nil, nil)
	if !errors.Is(err, session.ErrInvalidSummary) { t.Fatalf("error = %v", err) }
}

func TestSummaryGenerator_TimeoutCancelsStream(t *testing.T) {
	provider := &blockingSummaryProvider{cancelled: make(chan struct{})}
	_, err := newSummaryGenerator(provider, 10*time.Millisecond).Generate(context.Background(), "model-a", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) { t.Fatalf("error = %v", err) }
	select {
	case <-provider.cancelled:
	case <-time.After(time.Second): t.Fatal("provider did not observe cancellation")
	}
}
```

- [ ] **Step 2: Run RED**

Run: `go test -run TestSummaryGenerator -v ./internal/session/runner`

Expected: FAIL because the generator does not exist.

- [ ] **Step 3: Implement the isolated provider call and stable prompt**

Create `internal/session/runner/compaction_summary.go` with:

```go
const summaryMaxOutputTokens = 4_096

type summaryGenerator struct {
	provider llm.Provider
	timeout  time.Duration
}

func newSummaryGenerator(provider llm.Provider, timeout time.Duration) *summaryGenerator {
	return &summaryGenerator{provider: provider, timeout: timeout}
}

func (g *summaryGenerator) Generate(ctx context.Context, model string, previous *session.StructuredSummary, messages []session.Message) (session.StructuredSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	payload, err := json.Marshal(struct {
		Previous *session.StructuredSummary `json:"previous_summary,omitempty"`
		Messages []session.Message          `json:"messages"`
	}{Previous: previous, Messages: messages})
	if err != nil { return session.StructuredSummary{}, err }
	req := llm.Request{
		Model: model,
		System: "Return only one JSON object. Preserve facts; do not invent. All nine schema keys are required, and list fields must be JSON arrays.",
		Messages: []llm.Message{{Role: "user", Text: string(payload)}},
		MaxOutputTokens: summaryMaxOutputTokens,
	}
	stream, err := g.provider.Stream(ctx, req)
	if err != nil { return session.StructuredSummary{}, err }
	var text strings.Builder
	ended := false
	for event := range stream {
		switch event.Kind {
		case llm.TextDelta:
			text.WriteString(event.Text)
		case llm.StepFailed:
			if event.Err != nil { return session.StructuredSummary{}, event.Err }
			return session.StructuredSummary{}, errors.New(event.Text)
		case llm.StepEnded:
			ended = true
		}
	}
	if err := ctx.Err(); err != nil { return session.StructuredSummary{}, err }
	if !ended { return session.StructuredSummary{}, io.ErrUnexpectedEOF }
	var summary session.StructuredSummary
	if err := session.DecodeStructuredSummary([]byte(text.String()), &summary); err != nil {
		return session.StructuredSummary{}, err
	}
	return summary, nil
}
```

- [ ] **Step 4: Run GREEN and triangulate previous checkpoint input**

Run: `go test -run TestSummaryGenerator -v ./internal/session/runner`

Expected: PASS.

Add these tests:

```go
func TestSummaryGenerator_IncludesPreviousCheckpoint(t *testing.T) {
	provider := &summaryProvider{events: validSummaryEvents("next")}
	previous := session.StructuredSummary{CurrentGoal: "previous", Constraints: []string{}, Decisions: []string{}, Completed: []string{}, Files: []string{}, ToolResults: []string{}, Failures: []string{}, Pending: []string{}, Invariants: []string{}}
	_, err := newSummaryGenerator(provider, time.Second).Generate(context.Background(), "model-a", &previous, nil)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(provider.requests[0].Messages[0].Text, `"current_goal":"previous"`) {
		t.Fatalf("payload = %q", provider.requests[0].Messages[0].Text)
	}
}

func TestSummaryGenerator_RequiresStepEnded(t *testing.T) {
	provider := &summaryProvider{events: []llm.Event{{Kind: llm.TextDelta, Text: `{}`}}}
	_, err := newSummaryGenerator(provider, time.Second).Generate(context.Background(), "model-a", nil, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) { t.Fatalf("error = %v", err) }
}
```

Run: `go test ./internal/session/runner`

Expected: PASS.

- [ ] **Step 5: Commit Task 7**

```bash
git add internal/session/runner/compaction_summary.go internal/session/runner/compaction_summary_test.go
git commit -m "$(cat <<'EOF'
feat(runner): generate structured context summaries

Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
EOF
)"
```

---

### Task 8: Real pipeline compactor and runner-context projection

**Files:**
- Create: `internal/session/runner/compactor.go`
- Create: `internal/session/runner/compactor_test.go`
- Modify: `internal/session/runner/runner.go`
- Modify: `internal/session/runner/turn.go`
- Modify: `internal/session/runner/turn_signals_test.go`

- [ ] **Step 1: Write RED tests for preventive threshold, no-op path, durable retry, and invalid-summary atomicity**

Use injected limit and estimator functions instead of adding fake model IDs to the production catalog:

```go
type sequencedProvider struct {
	mu       sync.Mutex
	scripts  [][]llm.Event
	requests []llm.Request
}

func (p *sequencedProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	index := len(p.requests)
	p.requests = append(p.requests, req)
	script := append([]llm.Event(nil), p.scripts[index]...)
	p.mu.Unlock()
	return llm.NewFakeProvider(script...).Stream(ctx, req)
}

func summaryEvents(goal string) []llm.Event {
	text := fmt.Sprintf(`{"current_goal":%q,"constraints_and_instructions":[],"decisions":[],"completed_work":[],"files_and_changes":[],"relevant_tool_results":[],"failures_and_attempts":[],"pending_and_next_step":[],"facts_not_to_reinterpret":[]}`, goal)
	return []llm.Event{{Kind: llm.TextDelta, Text: text}, {Kind: llm.StepEnded}}
}

func seedCompactionConversation(t *testing.T, store session.Store) {
	t.Helper()
	ctx := context.Background()
	for _, message := range []session.Message{
		{ID: "u1", Role: session.RoleUser, Text: "old question"},
		{ID: "a1", Role: session.RoleAssistant, Text: "old answer"},
		{ID: "u2", Role: session.RoleUser, Text: "current question"},
	} {
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Message: &message}); err != nil { t.Fatal(err) }
	}
}

func requestHasUser(req llm.Request, text string) bool {
	for _, message := range req.Messages {
		if message.Role == "user" && message.Text == text { return true }
	}
	return false
}

func TestCompactor_PreventiveCommitReducesRequestAndPreservesCurrentUser(t *testing.T) {
	store := session.NewMemoryStore()
	seedCompactionConversation(t, store)
	provider := &sequencedProvider{scripts: [][]llm.Event{summaryEvents("continue"), textTurnScript()}}
	compactor := newCompactor(store, provider, compactorConfig{
		SummaryTimeout: time.Second,
		ContextWindow: func(string) (int, bool) { return 100, true },
		EstimateRequest: func(req llm.Request) int {
			if strings.Contains(req.System, "COMPACTED_SESSION_CONTEXT") { return 30 }
			return 80
		},
		EstimateGroup: func(messageGroup) int { return 1 },
		RecentBudgetPercent: 40,
	})
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	runner := NewRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"echo": true}, idCounter())
	runner.SetCompactor(compactor)
	if _, err := runner.runTurn(context.Background(), "s1"); err != nil { t.Fatal(err) }
	events, _ := store.Events(context.Background(), "s1", 0)
	count := 0
	for _, event := range events { if event.Kind == session.KindContextCompacted { count++ } }
	if count != 1 { t.Fatalf("events = %+v", events) }
	request := provider.requests[len(provider.requests)-1]
	if !strings.Contains(request.System, "COMPACTED_SESSION_CONTEXT") { t.Fatalf("system = %q", request.System) }
	if !requestHasUser(request, "current question") { t.Fatalf("request = %+v", request) }
}

func TestCompactor_BelowThresholdSkipsSummaryProviderCall(t *testing.T) {
	store := session.NewMemoryStore()
	seedCompactionConversation(t, store)
	provider := &sequencedProvider{scripts: [][]llm.Event{textTurnScript()}}
	compactor := newCompactor(store, provider, compactorConfig{
		SummaryTimeout: time.Second,
		ContextWindow: func(string) (int, bool) { return 100, true },
		EstimateRequest: func(llm.Request) int { return 79 },
		EstimateGroup: func(messageGroup) int { return 1 },
		RecentBudgetPercent: 40,
	})
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	runner := NewRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"echo": true}, idCounter())
	runner.SetCompactor(compactor)
	if _, err := runner.runTurn(context.Background(), "s1"); err != nil { t.Fatal(err) }
	if len(provider.requests) != 1 { t.Fatalf("provider requests = %d", len(provider.requests)) }
	events, _ := store.Events(context.Background(), "s1", 0)
	for _, event := range events {
		if event.Kind == session.KindContextCompacted { t.Fatalf("unexpected checkpoint: %+v", event) }
	}
}

func TestCompactor_InvalidSummaryLeavesEpochAndEventsUnchanged(t *testing.T) {
	store := session.NewMemoryStore()
	seedCompactionConversation(t, store)
	provider := &sequencedProvider{scripts: [][]llm.Event{{{Kind: llm.TextDelta, Text: `{}`}, {Kind: llm.StepEnded}}}}
	compactor := newCompactor(store, provider, compactorConfig{
		SummaryTimeout: time.Second,
		ContextWindow: func(string) (int, bool) { return 100, true },
		EstimateRequest: func(llm.Request) int { return 80 },
		EstimateGroup: func(messageGroup) int { return 1 },
		RecentBudgetPercent: 40,
	})
	beforeEvents, _ := store.Events(context.Background(), "s1", 0)
	beforeEpoch, _ := store.Epoch(context.Background(), "s1")
	err := compactor.Compact(context.Background(), CompactionInput{SessionID: "s1", Epoch: beforeEpoch, Request: llm.Request{Model: "model"}, Reason: session.CompactionPreventive})
	if !errors.Is(err, session.ErrInvalidSummary) { t.Fatalf("error = %v", err) }
	afterEvents, _ := store.Events(context.Background(), "s1", 0)
	afterEpoch, _ := store.Epoch(context.Background(), "s1")
	if !reflect.DeepEqual(beforeEvents, afterEvents) || beforeEpoch != afterEpoch {
		t.Fatalf("state changed: events %d->%d epoch %+v->%+v", len(beforeEvents), len(afterEvents), beforeEpoch, afterEpoch)
	}
}
```

Replace the old fake-compactor test in `turn_signals_test.go` with:

```go
type oneShotCompactor struct { calls int }
func (c *oneShotCompactor) NeedsCompaction(llm.Request) bool { return c.calls == 0 }
func (c *oneShotCompactor) Compact(ctx context.Context, input CompactionInput) error { c.calls++; return nil }

func TestRunner_CompactsAndRetriesOnceWhenRequestOverflows(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	seedUser(t, store, "s1")
	provider := &recordingProvider{FakeProvider: llm.NewFakeProvider(textTurnScript()...)}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	runner := NewRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"echo": true}, idCounter())
	compactor := &oneShotCompactor{}
	runner.SetCompactor(compactor)
	if _, err := runner.runTurn(ctx, "s1"); err != nil { t.Fatal(err) }
	if compactor.calls != 1 { t.Fatalf("compactor calls = %d", compactor.calls) }
	if provider.captured().Model != "" { t.Fatalf("unexpected request = %+v", provider.captured()) }
}
```

- [ ] **Step 2: Run RED**

Run: `go test -run 'TestCompactor|TestRunner_CompactsAndRetriesOnce' -v ./internal/session/runner`

Expected: FAIL because the real pipeline and runner projection do not exist.

- [ ] **Step 3: Replace the compactor seam with an explicit input contract**

In `internal/session/runner/runner.go`:

```go
type CompactionInput struct {
	SessionID string
	Epoch     session.ContextEpoch
	Request   llm.Request
	Reason    session.CompactionReason
}

type Compactor interface {
	NeedsCompaction(req llm.Request) bool
	Compact(ctx context.Context, input CompactionInput) error
}

func (r *Runner) SetCompactor(compactor Compactor) {
	r.compactor = compactor
}
```

- [ ] **Step 4: Implement the real compactor**

Create `internal/session/runner/compactor.go` with a `compactorConfig` containing `ContextWindow`, `EstimateRequest`, `EstimateGroup`, `SummaryTimeout`, and `RecentBudgetPercent` (default 40). `NeedsCompaction` returns false for unknown windows and otherwise calls `llm.NeedsPreventiveCompaction`.

The `Compact` method must:

```go
func (c *contextCompactor) Compact(ctx context.Context, input CompactionInput) error {
	context, err := c.store.ContextForRunner(ctx, input.SessionID)
	if err != nil { return err }
	if context.Epoch != input.Epoch { return session.ErrCompactionConflict }
	window, ok := c.config.ContextWindow(input.Request.Model)
	if !ok && input.Reason == session.CompactionPreventive { return nil }
	budgetBase := window
	if !ok {
		// Reactive overflow must still make progress for local/unknown models.
		budgetBase = c.config.EstimateRequest(input.Request)
	}
	budget := budgetBase * c.config.RecentBudgetPercent / 100
	selection, err := selectCompaction(context.Messages, budget, c.config.EstimateGroup)
	if err != nil { return err }
	var previous *session.StructuredSummary
	if context.Checkpoint != nil { previous = &context.Checkpoint.Summary }
	summary, err := c.generator.Generate(ctx, input.Request.Model, previous, selection.ToSummarize)
	if err != nil { return err }
	checkpoint := session.CompactionCheckpoint{
		Summary: summary, ExpectedEpoch: input.Epoch,
		CoveredThroughSeq: selection.CoveredThroughSeq,
		AnchorUserSeq: selection.Anchor.Seq,
		PreservedFromSeq: selection.PreservedFromSeq,
		Model: input.Request.Model, Reason: input.Reason,
		InputTokensBefore: c.config.EstimateRequest(input.Request),
	}
	after := compactedRequest(input.Request, summary, selection.Anchor, selection.Preserved)
	checkpoint.EstimatedTokensAfter = c.config.EstimateRequest(after)
	if checkpoint.EstimatedTokensAfter >= checkpoint.InputTokensBefore {
		return fmt.Errorf("%w: summary made no progress", session.ErrInvalidSummary)
	}
	if ok && llm.NeedsPreventiveCompaction(checkpoint.EstimatedTokensAfter, window) {
		return fmt.Errorf("%w: compacted request still crosses preventive threshold", session.ErrActivityTooLarge)
	}
	_, err = c.store.CommitCompaction(ctx, input.SessionID, checkpoint)
	return err
}
```

Use a stable renderer:

```go
func renderCompactedSystem(base string, summary session.StructuredSummary) string {
	encoded, _ := json.MarshalIndent(summary, "", "  ")
	return base + "\n\n<COMPACTED_SESSION_CONTEXT>\n" + string(encoded) + "\n</COMPACTED_SESSION_CONTEXT>"
}
```

- [ ] **Step 5: Build turn requests from `ContextForRunner`**

In `runTurnAttempt`, replace separate `Epoch` and `Messages` reads with `ContextForRunner`. Build messages in this order:

```go
messages := make([]session.Message, 0, len(context.Messages)+1)
if context.Anchor != nil { messages = append(messages, *context.Anchor) }
messages = append(messages, context.Messages...)
req := llm.Request{
	Model: context.Epoch.Model,
	System: systemForTurn(r, sessionID, context.Epoch.Model),
	Messages: toLLMMessages(messages),
	Tools: mat.Definitions,
	MaxOutputTokens: 4_096,
}
if context.Checkpoint != nil {
	req.System = renderCompactedSystem(req.System, context.Checkpoint.Summary)
}
```

Before provider streaming:

```go
if r.compactor != nil && r.compactor.NeedsCompaction(req) {
	if err := r.compactor.Compact(ctx, CompactionInput{SessionID: sessionID, Epoch: context.Epoch, Request: req, Reason: session.CompactionPreventive}); err != nil {
		return false, err
	}
	return false, errContinueAfterCompaction
}
```

Track a boolean in `runTurn` so the preventive signal can be consumed only once
per logical turn. If a rebuilt post-checkpoint request still asks for preventive
compaction, return `session.ErrActivityTooLarge` rather than creating a second
checkpoint or looping.

- [ ] **Step 6: Run GREEN, compact twice, and race gate**

Run: `go test -run 'TestCompactor|TestRunner_CompactsAndRetriesOnce' -v ./internal/session/runner`

Expected: PASS.

Add `TestCompactor_SuccessiveCheckpointIncludesPreviousSummary` with two commits and assert the second summary request contains the first summary and the epoch baseline advances monotonically.

Add `TestRunner_PostCompactionRequestStillOverThresholdFailsWithoutSecondCheckpoint` and assert one compactor call, zero provider calls, and `errors.Is(err, session.ErrActivityTooLarge)`.

Run: `go test -race ./internal/session/runner`

Expected: PASS.

- [ ] **Step 7: Commit Task 8**

```bash
git add internal/session/runner/compactor.go internal/session/runner/compactor_test.go internal/session/runner/runner.go internal/session/runner/turn.go internal/session/runner/turn_signals_test.go
git commit -m "$(cat <<'EOF'
feat(runner): compact context before model turns

Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
EOF
)"
```

---

### Task 9: Normalize provider overflow and allow exactly one reactive retry

**Files:**
- Modify: `internal/llm/openai.go`
- Modify: `internal/llm/openai_test.go`
- Modify: `internal/session/runner/turn.go`
- Modify: `internal/session/runner/turn_failure_test.go`

- [ ] **Step 1: Write provider RED for an OpenAI-compatible 400 overflow**

Add to `internal/llm/openai_test.go`:

```go
func TestOpenAIProvider_StreamClassifiesContextOverflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"maximum context length exceeded","code":"context_length_exceeded"}}`)
	}))
	defer server.Close()
	out, err := NewOpenAIProvider("key", server.URL, "model").Stream(context.Background(), Request{})
	if err != nil { t.Fatal(err) }
	events := drain(out)
	var failed Event
	found := false
	for _, event := range events {
		if event.Kind == StepFailed { failed, found = event, true; break }
	}
	if !found { t.Fatalf("events = %+v", events) }
	var overflow *ContextOverflowError
	if failed.Err == nil || !errors.As(failed.Err, &overflow) {
		t.Fatalf("failed event = %+v", failed)
	}
}
```

Run: `go test -run TestOpenAIProvider_StreamClassifiesContextOverflow -v ./internal/llm`

Expected: FAIL because `StepFailed.Err` is not classified.

- [ ] **Step 2: Implement narrow overflow classification**

In `internal/llm/openai.go`, when `stream.Err()` is available, classify only status/code/message forms known to mean context overflow:

```go
func normalizeProviderError(err error) error {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"context_length_exceeded", "maximum context length", "context window exceeded", "too many tokens"} {
		if strings.Contains(message, marker) {
			return &ContextOverflowError{Message: err.Error()}
		}
	}
	return err
}
```

Emit:

```go
cause := normalizeProviderError(stream.Err())
emit(ctx, out, Event{Kind: StepFailed, Text: cause.Error(), Err: cause})
```

Extend `TestOpenAIProvider_StreamEmitsStepFailedOnError`:

```go
for _, event := range got {
	if event.Kind != StepFailed { continue }
	var overflow *ContextOverflowError
	if errors.As(event.Err, &overflow) {
		t.Fatalf("generic 500 classified as overflow: %+v", event)
	}
}
```

- [ ] **Step 3: Run provider GREEN**

Run: `go test -run 'TestOpenAIProvider_Stream(ClassifiesContextOverflow|EmitsStepFailedOnError)' -v ./internal/llm`

Expected: PASS.

- [ ] **Step 4: Write runner RED for one retry and second-overflow failure**

Add to `internal/session/runner/turn_failure_test.go`:

```go
type overflowRecordingCompactor struct {
	calls []CompactionInput
}

func (c *overflowRecordingCompactor) NeedsCompaction(llm.Request) bool { return false }
func (c *overflowRecordingCompactor) Compact(ctx context.Context, input CompactionInput) error {
	c.calls = append(c.calls, input)
	return nil
}

type failureSequenceProvider struct {
	mu      sync.Mutex
	events  [][]llm.Event
	requests []llm.Request
}

func (p *failureSequenceProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	index := len(p.requests)
	p.requests = append(p.requests, req)
	script := append([]llm.Event(nil), p.events[index]...)
	p.mu.Unlock()
	return llm.NewFakeProvider(script...).Stream(ctx, req)
}

func overflowEvent() llm.Event {
	cause := &llm.ContextOverflowError{Message: "maximum context length exceeded"}
	return llm.Event{Kind: llm.StepFailed, Text: cause.Error(), Err: cause}
}

func TestRunner_ContextOverflowCompactsAndRetriesOnce(t *testing.T) {
	store := session.NewMemoryStore()
	seedUser(t, store, "s1")
	provider := &failureSequenceProvider{events: [][]llm.Event{{overflowEvent()}, textTurnScript()}}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	runner := NewRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"echo": true}, idCounter())
	compactor := &overflowRecordingCompactor{}
	runner.SetCompactor(compactor)
	if _, err := runner.runTurn(context.Background(), "s1"); err != nil { t.Fatal(err) }
	if len(provider.requests) != 2 { t.Fatalf("provider calls = %d", len(provider.requests)) }
	if len(compactor.calls) != 1 || compactor.calls[0].Reason != session.CompactionOverflow {
		t.Fatalf("compactor calls = %+v", compactor.calls)
	}
}

func TestRunner_SecondContextOverflowFailsWithoutThirdProviderCall(t *testing.T) {
	store := session.NewMemoryStore()
	seedUser(t, store, "s1")
	provider := &failureSequenceProvider{events: [][]llm.Event{{overflowEvent()}, {overflowEvent()}}}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	runner := NewRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"echo": true}, idCounter())
	compactor := &overflowRecordingCompactor{}
	runner.SetCompactor(compactor)
	_, err := runner.runTurn(context.Background(), "s1")
	var overflow *llm.ContextOverflowError
	if !errors.As(err, &overflow) { t.Fatalf("error = %v", err) }
	if len(provider.requests) != 2 || len(compactor.calls) != 1 {
		t.Fatalf("provider=%d compactor=%d", len(provider.requests), len(compactor.calls))
	}
}

func TestRunner_GenericProviderErrorDoesNotCompact(t *testing.T) {
	store := session.NewMemoryStore()
	seedUser(t, store, "s1")
	provider := &failureSequenceProvider{events: [][]llm.Event{{{Kind: llm.StepFailed, Text: "network down", Err: errors.New("network down")}}}}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	runner := NewRunner(store, session.NewMemoryInbox(), provider, reg, tool.Permissions{"echo": true}, idCounter())
	compactor := &overflowRecordingCompactor{}
	runner.SetCompactor(compactor)
	if _, err := runner.runTurn(context.Background(), "s1"); err == nil { t.Fatal("expected provider error") }
	if len(compactor.calls) != 0 { t.Fatalf("compactor calls = %+v", compactor.calls) }
}
```

Run: `go test -run 'TestRunner_(ContextOverflow|SecondContextOverflow|GenericProviderError)' -v ./internal/session/runner`

Expected: FAIL because `consume` discards the typed cause and `runTurn` has no reactive retry state.

- [ ] **Step 5: Preserve typed provider errors and bound the retry loop**

Change `ProviderError` to unwrap its cause:

```go
type ProviderError struct {
	Message string
	Cause   error
}

func (e *ProviderError) Error() string { return e.Message }
func (e *ProviderError) Unwrap() error { return e.Cause }
```

In `consume`:

```go
streamErr = &ProviderError{Message: ev.Text, Cause: ev.Err}
```

In `runTurn`, track `compacted` per logical turn. On a typed overflow returned by `runTurnAttempt`, call:

```go
if errors.As(err, &overflow) && r.compactor != nil && !compacted {
	if compactErr := r.compactor.Compact(ctx, CompactionInput{
		SessionID: sessionID,
		Epoch: attempt.Epoch,
		Request: attempt.Request,
		Reason: session.CompactionOverflow,
	}); compactErr != nil {
		return false, compactErr
	}
	compacted = true
	promotion = session.DeliverySteer
	continue
}
```

Return the second overflow unchanged. To expose `Epoch` and `Request` without mutable runner state, make `runTurnAttempt` return a small `turnAttemptResult` containing `NeedsContinuation`, `Epoch`, and `Request` alongside `error`.

- [ ] **Step 6: Run GREEN and full LLM/runner suites**

Run: `go test ./internal/llm ./internal/session/runner && go test -race ./internal/session/runner`

Expected: PASS.

- [ ] **Step 7: Commit Task 9**

```bash
git add internal/llm/openai.go internal/llm/openai_test.go internal/session/runner/turn.go internal/session/runner/turn_failure_test.go
git commit -m "$(cat <<'EOF'
feat(runner): recover once from context overflow

Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
EOF
)"
```

---

### Task 10: Install the real compactor and resolved model in production wiring

**Files:**
- Modify: `internal/wiring/wiring.go`
- Modify: `internal/wiring/wiring_test.go`
- Modify: `internal/tui/engine.go`
- Modify: `internal/tui/engine_test.go`
- Modify: `cmd/atenea-tui/main.go`
- Modify: `app.go`
- Modify: `app_test.go`

- [ ] **Step 1: Write RED tests that production wiring installs compaction and rewiring uses the new provider**

Add a package-visible test hook in `Runner`:

```go
func (r *Runner) HasCompactor() bool { return r.compactor != nil }
```

Add to `internal/wiring/wiring_test.go`:

```go
func minimalConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Root: t.TempDir(),
		Model: "anthropic/claude-opus-4.8",
		Provider: llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded}),
		Store: session.NewMemoryStore(),
		Inbox: session.NewMemoryInbox(),
		Gate: session.NewMemoryPermissionGate(),
		Snaps: tool.NewSessionSnapshots(),
		Bus: event.NewBus(func(string, ...interface{}) {}),
		NextID: NewIDGen(),
	}
}

func TestBuild_InstallsContextCompactor(t *testing.T) {
	built := Build(minimalConfig(t))
	if !built.Runner.HasCompactor() {
		t.Fatal("production runner must install context compactor")
	}
}

func TestBuild_RunnerUsesResolvedModelWhenEpochModelIsEmpty(t *testing.T) {
	provider := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded})}
	config := minimalConfig(t)
	config.Provider = provider
	built := Build(config)
	if _, err := config.Store.AppendEvent(context.Background(), "s1", session.SessionEvent{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hello"}}); err != nil { t.Fatal(err) }
	if err := built.Runner.Run(context.Background(), "s1", true); err != nil { t.Fatal(err) }
	if got := provider.captured().Model; got != config.Model {
		t.Fatalf("request model = %q, want %q", got, config.Model)
	}
}
```

Add to `app_test.go`:

```go
func TestApp_SetProviderRewiresRunnerWithReplacementModel(t *testing.T) {
	oldProvider := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(textTurnScript()...)}
	newProvider := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(textTurnScript()...)}
	app := newApp(oldProvider, func(string, ...interface{}) {})
	app.newProvider = func(ProviderConfig) llm.Provider { return newProvider }
	if err := app.SetProvider(providerKindLocal, "http://localhost:1234/v1", "model-b"); err != nil { t.Fatal(err) }
	if err := app.SendPrompt("s1", "hello"); err != nil { t.Fatal(err) }
	app.wait()
	if got := newProvider.captured().Model; got != "model-b" { t.Fatalf("new request model = %q", got) }
	if got := oldProvider.captured().Model; got != "" { t.Fatalf("old provider reused: %+v", oldProvider.captured()) }
}
```

Run: `go test -run 'TestBuild_InstallsContextCompactor|TestApp_CompactorUsesReplacementProvider' -v ./internal/wiring .`

Expected: FAIL because wiring does not install the compactor.

- [ ] **Step 2: Add exported constructor and wire it with the same store/provider snapshot**

Add `Model string` to `wiring.Config` and add a configured-model fallback to `Runner`:

```go
// runner.go
func (r *Runner) SetModel(model string) { r.model = model }

func (r *Runner) turnModel(epoch session.ContextEpoch) string {
	if epoch.Model != "" { return epoch.Model }
	return r.model
}
```

Use `turnModel(context.Epoch)` for `llm.Request.Model`, system prompt selection and the summary generator. The epoch remains the concurrency token; rewiring cancels old runs before swapping provider/model.

In `internal/session/runner/compactor.go`:

```go
func NewContextCompactor(store session.Store, provider llm.Provider) Compactor {
	return newCompactor(store, provider, compactorConfig{
		SummaryTimeout: 45 * time.Second,
		ContextWindow: llm.ContextWindow,
		EstimateRequest: llm.EstimateRequestTokens,
		EstimateGroup: func(group messageGroup) int {
			request := llm.Request{Messages: toLLMMessages(group.Messages)}
			return llm.EstimateRequestTokens(request)
		},
		RecentBudgetPercent: 40,
	})
}
```

In `wiring.Build` immediately after `NewRunner`:

```go
r.SetModel(cfg.Model)
r.SetCompactor(runner.NewContextCompactor(cfg.Store, cfg.Provider))
```

In `app.go`, snapshot the complete provider config and pass its model:

```go
providerConfig := a.ProviderConfig()
built := wiring.Build(wiring.Config{
	Root: root, Provider: provider, Model: providerConfig.Model,
	// existing dependencies unchanged
})
```

In `internal/tui/engine.go` add `Model string` to `EngineConfig` and pass it to `wiring.Config`. In `cmd/atenea-tui/main.go` use the already resolved value:

```go
engine := tui.NewEngine(tui.EngineConfig{
	Root: root, Provider: provider, Model: model, Store: store,
})
```

Add to `internal/tui/engine_test.go`:

```go
type recordingEngineProvider struct {
	*llm.FakeProvider
	mu      sync.Mutex
	request llm.Request
}

func (p *recordingEngineProvider) Stream(ctx context.Context, request llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.request = request
	p.mu.Unlock()
	return p.FakeProvider.Stream(ctx, request)
}

func (p *recordingEngineProvider) captured() llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.request
}

func TestEngine_UsesConfiguredModel(t *testing.T) {
	provider := &recordingEngineProvider{FakeProvider: llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded})}
	engine := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Model: "model-a", Store: session.NewMemoryStore()})
	if err := engine.SendPrompt("s1", "hello"); err != nil { t.Fatal(err) }
	for message := range engine.Events() {
		if _, ok := message.(RunDoneMsg); ok { break }
	}
	if got := provider.captured().Model; got != "model-a" { t.Fatalf("request model = %q", got) }
}
```

Because `App.wire` and `NewEngine` snapshot provider/model together before `Build`, every rewire constructs a matching runner and compactor. No second mutable provider hook is needed.

- [ ] **Step 3: Run GREEN and app safety net**

Run: `go test ./internal/wiring ./internal/tui .`

Expected: PASS.

- [ ] **Step 4: Commit Task 10**

```bash
git add internal/session/runner/compactor.go internal/session/runner/runner.go internal/wiring/wiring.go internal/wiring/wiring_test.go internal/tui/engine.go internal/tui/engine_test.go cmd/atenea-tui/main.go app.go app_test.go
git commit -m "$(cat <<'EOF'
feat(wiring): enable context compaction in app runners

Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
EOF
)"
```

---

### Task 11: Frontend event projection and expandable compaction card

**Files:**
- Create: `frontend/src/components/ContextCompactedCard.vue`
- Create: `frontend/src/components/ContextCompactedCard.test.ts`
- Modify: `frontend/src/stores/chat.ts`
- Modify: `frontend/src/stores/chat.test.ts`
- Modify: `frontend/src/components/MessageList.vue`
- Modify: `frontend/src/components/MessageList.test.ts`

- [ ] **Step 1: Run frontend safety net**

Run: `cd frontend && npm test`

Expected: PASS.

- [ ] **Step 2: Write RED store tests for live event and rehydration**

Add types to the test fixture and these assertions in `frontend/src/stores/chat.test.ts`:

```ts
it('Context.Compacted creates a non-assistant item with durable summary', () => {
  const store = useChatStore()
  store.applyEvent({
    Kind: 'Context.Compacted',
    Compaction: {
      Summary: {
        CurrentGoal: 'continue implementation',
        Constraints: ['keep history'],
        Decisions: [],
        Completed: [],
        Files: ['internal/session/store.go'],
        ToolResults: [],
        Failures: [],
        Pending: ['wire UI'],
        Invariants: ['tool pairs stay complete'],
      },
      CoveredThroughSeq: 8,
      AnchorUserSeq: 9,
      PreservedFromSeq: 9,
      Reason: 'preventive',
      InputTokensBefore: 160000,
      EstimatedTokensAfter: 50000,
    },
  })
  expect(store.items).toHaveLength(1)
  expect(store.items[0]).toMatchObject({ kind: 'compaction', reason: 'preventive', coveredThroughSeq: 8 })
  expect(store.items.some((item) => item.kind === 'assistant')).toBe(false)
})
```

Add to `frontend/src/stores/chat.test.ts`:

```ts
it('loadSession preserves compaction position between conversation items', async () => {
  vi.mocked(App.SessionHistory).mockResolvedValueOnce([
    { Message: { Role: 'user', Text: 'hola' } },
    { Kind: 'Context.Compacted', Compaction: compactionFixture },
    { Kind: 'Text.Started' },
    { Kind: 'Text.Delta', Text: 'respuesta' },
    { Kind: 'Text.Ended', Text: 'respuesta' },
    { Kind: 'Step.Ended' },
  ] as never)
  const store = useChatStore()
  await store.loadSession('s1')
  expect(store.items.map((item) => item.kind)).toEqual(['user', 'compaction', 'assistant'])
})
```

Define `compactionFixture` once at the top of the describe block and reuse it in the live-event test.

Run: `cd frontend && npm test -- src/stores/chat.test.ts`

Expected: FAIL because the event and item types do not exist.

- [ ] **Step 3: Extend frontend types and fold**

In `frontend/src/stores/chat.ts` add:

```ts
export interface StructuredSummary {
  CurrentGoal: string
  Constraints: string[]
  Decisions: string[]
  Completed: string[]
  Files: string[]
  ToolResults: string[]
  Failures: string[]
  Pending: string[]
  Invariants: string[]
}

export interface CompactionItem {
  kind: 'compaction'
  id: string
  summary: StructuredSummary
  reason: 'preventive' | 'overflow'
  coveredThroughSeq: number
  preservedFromSeq: number
  inputTokensBefore: number
  estimatedTokensAfter: number
}

export type TurnItem = UserItem | AssistantItem | ReasoningItem | ToolItem | CompactionItem
```

Extend `SessionEvent` with the PascalCase `Compaction` payload, then add to `applyEvent`:

```ts
case 'Context.Compacted':
  if (ev.Compaction) {
    items.value.push({
      kind: 'compaction',
      id: nextId(),
      summary: ev.Compaction.Summary,
      reason: ev.Compaction.Reason,
      coveredThroughSeq: ev.Compaction.CoveredThroughSeq,
      preservedFromSeq: ev.Compaction.PreservedFromSeq,
      inputTokensBefore: ev.Compaction.InputTokensBefore,
      estimatedTokensAfter: ev.Compaction.EstimatedTokensAfter,
    })
  }
  break
```

- [ ] **Step 4: Write and implement the expandable card**

Create `frontend/src/components/ContextCompactedCard.test.ts`:

```ts
it('starts collapsed and expands structured sections', async () => {
  const wrapper = mount(ContextCompactedCard, { props: { item } })
  expect(wrapper.text()).toContain('Contexto compactado')
  expect(wrapper.text()).not.toContain('continue implementation')
  await wrapper.get('button').trigger('click')
  expect(wrapper.text()).toContain('continue implementation')
  expect(wrapper.text()).toContain('tool pairs stay complete')
})
```

Create `frontend/src/components/ContextCompactedCard.vue`:

```vue
<script setup lang="ts">
import { ref } from 'vue'
import type { CompactionItem } from '../stores/chat'

defineProps<{ item: CompactionItem }>()
const expanded = ref(false)

const sections = [
  ['Restricciones', 'Constraints'],
  ['Decisiones', 'Decisions'],
  ['Trabajo completado', 'Completed'],
  ['Archivos y cambios', 'Files'],
  ['Resultados de tools', 'ToolResults'],
  ['Errores e intentos', 'Failures'],
  ['Pendientes', 'Pending'],
  ['Hechos preservados', 'Invariants'],
] as const
</script>

<template>
  <article class="rounded-xl border border-white/10 bg-white/[0.03] px-4 py-3 text-sm">
    <button
      type="button"
      class="flex w-full items-center justify-between text-left"
      :aria-expanded="expanded"
      @click="expanded = !expanded"
    >
      <span class="font-medium">Contexto compactado</span>
      <span class="opacity-50">{{ item.reason === 'overflow' ? 'por limite' : 'preventivo' }}</span>
    </button>
    <div v-if="expanded" class="mt-3 space-y-3">
      <section>
        <h3 class="text-xs uppercase opacity-50">Objetivo actual</h3>
        <p class="mt-1 whitespace-pre-wrap">{{ item.summary.CurrentGoal }}</p>
      </section>
      <section v-for="([label, key]) in sections" :key="key" v-show="item.summary[key].length">
        <h3 class="text-xs uppercase opacity-50">{{ label }}</h3>
        <ul class="mt-1 list-disc space-y-1 pl-5">
          <li v-for="entry in item.summary[key]" :key="entry">{{ entry }}</li>
        </ul>
      </section>
      <p class="text-xs opacity-40">
        Eventos cubiertos hasta {{ item.coveredThroughSeq }} · {{ item.inputTokensBefore }} → {{ item.estimatedTokensAfter }} tokens estimados
      </p>
    </div>
  </article>
</template>
```

- [ ] **Step 5: Register the card in MessageList and run GREEN**

In `frontend/src/components/MessageList.vue`:

```ts
import ContextCompactedCard from './ContextCompactedCard.vue'

const registry: Record<TurnItem['kind'], Component> = {
  user: UserMessage,
  assistant: AssistantMessage,
  reasoning: ThinkingBlock,
  tool: ToolCall,
  compaction: ContextCompactedCard,
}
```

Update the `tail` computed branch:

```ts
if (last.kind === 'compaction') return `${last.reason}:${last.coveredThroughSeq}`
```

Add to `frontend/src/components/MessageList.test.ts`:

```ts
it('dispatches compaction items to ContextCompactedCard', () => {
  const items: TurnItem[] = [{
    kind: 'compaction', id: 'c1', reason: 'preventive', coveredThroughSeq: 8,
    preservedFromSeq: 9, inputTokensBefore: 160000, estimatedTokensAfter: 50000,
    summary: { CurrentGoal: 'continue', Constraints: [], Decisions: [], Completed: [], Files: [], ToolResults: [], Failures: [], Pending: [], Invariants: [] },
  }]
  const wrapper = mount(MessageList, { props: { items } })
  expect(wrapper.text()).toContain('Contexto compactado')
})
```

Run: `cd frontend && npm test -- src/stores/chat.test.ts src/components/ContextCompactedCard.test.ts src/components/MessageList.test.ts`

Expected: PASS.

- [ ] **Step 6: Run frontend type, lint, and format gates**

Run: `cd frontend && npm run build && npm run lint && npm run format:check`

Expected: PASS.

- [ ] **Step 7: Commit Task 11**

```bash
git add frontend/src/stores/chat.ts frontend/src/stores/chat.test.ts frontend/src/components/ContextCompactedCard.vue frontend/src/components/ContextCompactedCard.test.ts frontend/src/components/MessageList.vue frontend/src/components/MessageList.test.ts
git commit -m "$(cat <<'EOF'
feat(frontend): show durable context compaction cards

Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
EOF
)"
```

---

### Task 12: Align the visual context catalog and close end-to-end evidence

**Files:**
- Modify: `frontend/src/lib/contextWindow.ts`
- Modify: `frontend/src/lib/contextWindow.test.ts`
- Modify: `../architecture/agent-loop.md`
- Modify: `agent-loop-roadmap.md`

- [ ] **Step 1: Add a catalog parity test for known production models**

Keep the frontend fallback `DEFAULT_CONTEXT_WINDOW` for display only, but ensure all backend-known IDs are present in `WINDOWS`. Add table-driven cases:

```ts
it.each([
  ['anthropic/claude-opus-4.8', 200000],
  ['anthropic/claude-sonnet-4.5', 200000],
  ['anthropic/claude-3.5-sonnet', 200000],
  ['openai/gpt-4o', 128000],
  ['google/gemini-2.5-pro', 1048576],
])('%s uses the backend catalog window', (model, window) => {
  expect(contextWindowFor(model)).toBe(window)
})
```

Run: `cd frontend && npm test -- src/lib/contextWindow.test.ts`

Expected: PASS after catalog alignment.

- [ ] **Step 2: Document the implemented loop contract**

Update `../architecture/agent-loop.md` so the compaction section states:

```text
- preventive threshold: estimated request at 80% of known model window;
- durable Context.Compacted checkpoint plus atomic BaselineSeq/Revision update;
- structured summary produced by the same provider/model without tools;
- literal last-user anchor and complete recent tool groups;
- one reactive retry for normalized ContextOverflowError;
- full event history remains visible to UI.
```

Mark the real M7/M10 compactor work complete in `agent-loop-roadmap.md` without claiming unrelated roadmap items are complete.

- [ ] **Step 3: Run the complete backend and frontend quality gates**

Run:

```bash
gofmt -w internal/llm internal/session internal/event internal/wiring app.go app_test.go
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
go test -race ./...
cd frontend && npm test && npm run build && npm run lint && npm run format:check
```

Expected: every command exits 0; `gofmt -l .` prints nothing.

- [ ] **Step 4: Perform the E2E acceptance scenario in the real Wails app**

Run: `wails dev`

Use a temporary test-only model limit injection or a long seeded SQLite session so the next request crosses 80%. Verify visually and behaviorally:

1. The conversation shows old messages before compaction.
2. The next turn creates one collapsed `Contexto compactado` card.
3. Expanding the card shows all structured sections without layout overflow.
4. Old messages remain visible above the card.
5. The assistant continues after the card and retains the latest user request.
6. Restarting the app rehydrates the card in the same position.
7. A forced normalized overflow returns once; a second overflow shows one clean error and no infinite spinner.

Capture the exact command, screenshots or screen recording path, and observed result in the task's `TDD Cycle Evidence` table.

- [ ] **Step 5: Commit Task 12**

```bash
git add frontend/src/lib/contextWindow.ts frontend/src/lib/contextWindow.test.ts ../atenea-agent-loop.md ../atenea-agent-loop-roadmap.md
git commit -m "$(cat <<'EOF'
docs: finalize agent context compaction rollout

Generated-By: PostHog Code
Task-Id: 330e931e-1417-4cb9-9a9d-ffd33beec51d
EOF
)"
```

---

## Final TDD Cycle Evidence template

The implementation handoff must include one consolidated table using real command output:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Existing backend/frontend suites checked before changes | `go test ./...`; `cd frontend && npm test` | pass or preexisting failure documented |
| Understand | Spec, runner, store, provider, event decorators and UI fold read | `../specs/2026-07-09-agent-context-compaction.md` plus mapped files | behavior identified |
| NETWORK | Each task's behavior test failed first | exact `go test -run ... -v` / `vitest` commands | expected behavioral failure, gate ok |
| GREEN | Minimal implementation passed each focused test | exact focused commands | pass, gate ok |
| TRIANGULATE | Boundary, conflict, restart, tool grouping, timeout and second-overflow cases | focused tests plus `-race` | pass, gate ok |
| REFACTOR | Formatting, vet, whole suites and UI gates | `gofmt -l .`, `go vet ./...`, `go test ./...`, `go test -race ./...`, frontend gates | clean, gate ok |
| Evidence | Real Wails preventive and reactive scenarios inspected | `wails dev` plus captured artifact paths | acceptance criteria verified |

## Completion checklist

- [ ] Every checkpoint is durable and reconstructable after restart.
- [ ] `BaselineSeq` and `Revision` change in the same atomic commit as the event.
- [ ] Unknown model windows never trigger preventive compaction.
- [ ] Known models compact at exactly 80% estimated occupancy.
- [ ] Summary calls use the same provider/model, no tools, timeout, and JSON validation.
- [ ] The last user prompt is always literal; fallback preserves a complete recent suffix.
- [ ] Tool calls and results are never separated or orphaned.
- [ ] Failed summary, cancellation, CAS conflict, and store error leave no partial checkpoint.
- [ ] Provider overflow triggers at most one compact-and-retry cycle.
- [ ] Full historical events remain visible in UI and `SessionHistory`.
- [ ] The UI card is collapsed by default, expandable, durable, and not an assistant item.
- [ ] Backend, race, frontend, lint, format, build, and E2E gates are green.
