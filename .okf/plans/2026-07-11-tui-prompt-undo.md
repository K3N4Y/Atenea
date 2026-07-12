# TUI Prompt Undo Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` (recommended) or `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add durable prompt-level `/undo` to `atenea-tui`, restoring non-ignored workspace content and removing the reverted prompt range from effective session projections.

**Architecture:** `internal/checkpoint` owns an isolated Git repository and tree-based capture/restore. Session events persist prompt boundaries and revert markers, with one shared effective-event projection for memory and SQLite. The TUI engine serializes runs and undo per session; the Bubble Tea model executes `/undo` locally and rebuilds from effective events.

**Tech Stack:** Go 1.23+, Git CLI plumbing, event-sourced stores, SQLite, Bubble Tea, existing runner cancellation.

---

## File Map

- Create `internal/checkpoint/git.go` and `git_test.go` for isolated Git snapshots.
- Create `internal/session/undo.go` and `undo_store_contract_test.go` for durable boundaries and effective projections.
- Modify `internal/session/event.go`, `session.go`, `memstore.go`, and `sqlitestore.go` for checkpoint persistence.
- Modify `internal/tui/engine.go` and `engine_test.go` for checkpointed runs, cancellation, validation, and restore.
- Modify `internal/tui/model.go`, `model_test.go`, and `fold.go` for native `/undo` interaction and transcript replacement.
- Modify `internal/session/open.go`, `cmd/atenea-tui/main.go`, and their tests for durable checkpoint storage.
- Modify `.okf/architecture/tui.md` to document behavior and the desktop boundary.

Every implementation commit must end with:

```text
Generated-By: PostHog Code
Task-Id: 613e649f-973b-44cf-a6ab-0e5dbc38c873
```

Do not add co-author trailers.

---

### Task 1: Establish the Safety Net

**Files:**
- Read: `internal/tui/engine_test.go`
- Read: `internal/session/store_contract_test.go`
- Read: `internal/session/sqlitestore_test.go`

- [ ] **Step 1: Run relevant tests before editing**

Run:

```bash
go test ./internal/session ./internal/tui ./cmd/atenea-tui
```

Expected: PASS. Record any pre-existing failure and stop before RED.

- [ ] **Step 2: Run the broad safety net**

Run:

```bash
go test ./...
```

Expected: PASS. Preserve the command and result for final evidence.

---

### Task 2: RED — Reproduce Prompt Undo End to End

**Files:**
- Modify: `internal/tui/engine_test.go`

- [ ] **Step 1: Add the failing end-to-end test**

Add `TestEngine_UndoRestoresPrePromptWorkspaceAndEffectiveConversation`. It initializes a real Git workspace, commits `tracked.txt`, then leaves a modified tracked file and `notes.txt` untracked before sending a prompt. Use the existing real `write` tool through `wiring.Build` to overwrite `tracked.txt` and create `created.txt`.

Core assertions:

```go
result, err := engine.Undo("s1")
if err != nil {
    t.Fatal(err)
}
if result.Prompt != "cambia los archivos" {
    t.Fatalf("Prompt = %q", result.Prompt)
}
assertUndoFile(t, root, "tracked.txt", "preexisting-change\n")
assertUndoFile(t, root, "notes.txt", "preexisting-untracked\n")
assertUndoMissing(t, root, "created.txt")

messages, err := store.Messages(context.Background(), "s1", 0)
if err != nil {
    t.Fatal(err)
}
if len(messages) != 0 {
    t.Fatalf("effective messages = %+v, want none", messages)
}
```

- [ ] **Step 2: Verify RED**

Run:

```bash
go test -run TestEngine_UndoRestoresPrePromptWorkspaceAndEffectiveConversation -v ./internal/tui
```

Expected: FAIL to compile because `EngineConfig.Checkpoints`, `Engine.Undo`, and `UndoResult` do not exist.

- [ ] **Step 3: Commit RED**

```bash
git add internal/tui/engine_test.go
git commit -m "test(tui): reproduce prompt undo workflow" -m "Generated-By: PostHog Code" -m "Task-Id: 613e649f-973b-44cf-a6ab-0e5dbc38c873"
```

---

### Task 3: GREEN — Isolated Git Checkpoint Service

**Files:**
- Create: `internal/checkpoint/git.go`
- Create: `internal/checkpoint/git_test.go`

- [ ] **Step 1: Write failing checkpoint tests**

Add:

```go
func TestGitStore_CaptureAndRestoreNonIgnoredWorkspace(t *testing.T)
func TestGitStore_RestorePreservesMainGitMetadata(t *testing.T)
```

The first test covers committed, modified, untracked, ignored, executable, symlink, deleted, and newly created paths. The second records branch, `HEAD`, local refs, staged binary diff, and status columns before and after restore. Assert branch, `HEAD`, refs, and staged diff are byte-identical; only worktree status may change.

- [ ] **Step 2: Verify checkpoint RED**

Run:

```bash
go test -run 'TestGitStore_(CaptureAndRestoreNonIgnoredWorkspace|RestorePreservesMainGitMetadata)' -v ./internal/checkpoint
```

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement the minimal API**

```go
package checkpoint

type Tree string

type Store interface {
    Capture(ctx context.Context, workspace string) (Tree, error)
    Restore(ctx context.Context, workspace string, tree Tree) error
}

type GitStore struct {
    root string
    mu   sync.Mutex
}

func NewGitStore(root string) *GitStore
func (s *GitStore) Capture(ctx context.Context, workspace string) (Tree, error)
func (s *GitStore) Restore(ctx context.Context, workspace string, tree Tree) error
```

Rules:

```text
private git dir = <root>/<sha256(clean absolute workspace)>
git invocation = git --git-dir <private> --work-tree <workspace> ...
initialize = git init --bare <private>
capture = refresh private index, git add -A with normal ignore rules, git write-tree
restore = remove current non-ignored paths absent from target, git read-tree target, git checkout-index -a -f
```

Use `exec.CommandContext`, NUL-delimited literal paths, `core.autocrlf=false`, `core.symlinks=true`, and `core.quotepath=false`. Use the user's repository only for read-only `git -C <workspace> check-ignore --no-index --stdin -z`. Return `checkpoint requires a Git workspace` outside a Git worktree. Add no dependency.

- [ ] **Step 4: Verify checkpoint GREEN**

Run:

```bash
go test -run 'TestGitStore_(CaptureAndRestoreNonIgnoredWorkspace|RestorePreservesMainGitMetadata)' -v ./internal/checkpoint
```

Expected: PASS.

- [ ] **Step 5: Commit checkpoint service**

```bash
git add internal/checkpoint/git.go internal/checkpoint/git_test.go
git commit -m "feat(checkpoint): snapshot workspace with isolated git" -m "Generated-By: PostHog Code" -m "Task-Id: 613e649f-973b-44cf-a6ab-0e5dbc38c873"
```

---

### Task 4: GREEN — Durable Checkpoints and Effective Projections

**Files:**
- Create: `internal/session/undo.go`
- Create: `internal/session/undo_store_contract_test.go`
- Modify: `internal/session/event.go`
- Modify: `internal/session/session.go`
- Modify: `internal/session/memstore.go`
- Modify: `internal/session/sqlitestore.go`
- Modify: `internal/session/store_contract_test.go`

- [ ] **Step 1: Write the failing store contract**

Create `testUndoStoreContract` and run it for memory and SQLite. Cases:

```go
t.Run("revert hides range from events messages and context", ...)
t.Run("repeated revert selects previous effective checkpoint", ...)
t.Run("reverted composer prompt is hidden", ...)
t.Run("epoch changes after revert", ...)
t.Run("checkpoint payload survives sqlite reopen", ...)
```

Use event shapes:

```go
SessionEvent{Kind: KindPromptCheckpointStarted, Checkpoint: &PromptCheckpoint{ID: "cp-1", Prompt: "first", BeforeTree: "before-1"}}
SessionEvent{Kind: KindComposerPrompt, Text: "first"}
SessionEvent{Message: &Message{ID: "u1", Role: RoleUser, Text: "first"}}
SessionEvent{Kind: KindPromptCheckpointFinished, Checkpoint: &PromptCheckpoint{ID: "cp-1", AfterTree: "after-1"}}
SessionEvent{Kind: KindPromptCheckpointReverted, Checkpoint: &PromptCheckpoint{ID: "cp-1"}}
```

- [ ] **Step 2: Verify contract RED**

Run:

```bash
go test -run UndoStoreContract -v ./internal/session
```

Expected: FAIL because checkpoint types and projection do not exist.

- [ ] **Step 3: Add event types and payload**

```go
KindPromptCheckpointStarted  EventKind = "Prompt.Checkpoint.Started"
KindPromptCheckpointFinished EventKind = "Prompt.Checkpoint.Finished"
KindPromptCheckpointReverted EventKind = "Prompt.Checkpoint.Reverted"

type PromptCheckpoint struct {
    ID         string
    Prompt     string
    BeforeTree string
    AfterTree  string
}
```

Add `Checkpoint *PromptCheckpoint` to `SessionEvent`.

- [ ] **Step 4: Implement one shared projection rule**

Create:

```go
var ErrNothingToUndo = errors.New("nothing to undo")

type EffectiveCheckpoint struct {
    ID         string
    Prompt     string
    BeforeTree string
    AfterTree  string
    StartSeq   Seq
    FinishSeq  Seq
}

func EffectiveEvents(events []SessionEvent) []SessionEvent
func LatestEffectiveCheckpoint(events []SessionEvent) (EffectiveCheckpoint, error)

type UndoStore interface {
    Store
    LatestPromptCheckpoint(ctx context.Context, sessionID string) (EffectiveCheckpoint, error)
}
```

`EffectiveEvents` scans raw append-only events, collects reverted IDs, derives each inclusive start-through-finish range, and removes those ranges. Keep revert control events internally so repeated undo knows what is already reverted, but never materialize them as messages.

- [ ] **Step 5: Persist payload in SQLite**

Add nullable columns and scan/insert support:

```sql
checkpoint_id TEXT,
checkpoint_prompt TEXT,
checkpoint_before_tree TEXT,
checkpoint_after_tree TEXT
```

Use idempotent additive migrations and ignore only duplicate-column errors.

- [ ] **Step 6: Route projections through effective events**

Apply `EffectiveEvents` before existing projection logic in `LoadSession`, `Messages`, `Sessions`, `Events`, `Epoch`, `PendingToolCalls`, and `ContextForRunner`. Implement `LatestPromptCheckpoint` from raw events in both stores.

- [ ] **Step 7: Verify GREEN and nearby safety**

Run:

```bash
go test -run UndoStoreContract -v ./internal/session && go test ./internal/session
```

Expected: PASS.

- [ ] **Step 8: Commit session projection**

```bash
git add internal/session
git commit -m "feat(session): project reverted prompt checkpoints" -m "Generated-By: PostHog Code" -m "Task-Id: 613e649f-973b-44cf-a6ab-0e5dbc38c873"
```

---

### Task 5: GREEN — Engine Checkpoint Lifecycle and Undo

**Files:**
- Modify: `internal/tui/engine.go`
- Modify: `internal/tui/engine_test.go`

- [ ] **Step 1: Add engine contracts**

```go
type EngineConfig struct {
    // existing fields
    Checkpoints checkpoint.Store
}

type UndoResult struct {
    Prompt string
    Events []session.SessionEvent
}

type engineRun struct {
    cancel       context.CancelFunc
    done         chan struct{}
    checkpointID string
}
```

Add `checkpoints checkpoint.Store` and per-session operation locks to `Engine`. Production injects the durable store in Task 8; tests inject temporary stores.

- [ ] **Step 2: Capture before prompt admission**

Under the per-session operation lock:

```go
before, err := e.checkpoints.Capture(ctx, e.root)
if err != nil {
    return err
}
checkpointID := "checkpoint-" + strconv.FormatInt(time.Now().UnixNano(), 10)
_, err = e.store.AppendEvent(ctx, sessionID, session.SessionEvent{
    Kind: session.KindPromptCheckpointStarted,
    Checkpoint: &session.PromptCheckpoint{
        ID: checkpointID, Prompt: composerPrompt, BeforeTree: string(before),
    },
})
if err != nil {
    return err
}
```

Only then admit the expanded prompt and append `Composer.Prompt`. `AcceptPlan` has an empty composer prompt and remains in the plan prompt's existing checkpoint rather than creating another user-undo boundary.

- [ ] **Step 3: Capture after the runner stops**

After `runner.Run` returns, under the same session operation lock, capture `after` and append `Prompt.Checkpoint.Finished`. Close `engineRun.done` only after finalization and map cleanup. Finalization failure becomes `RunDoneMsg.Err`.

- [ ] **Step 4: Implement undo**

```go
var ErrWorkspaceDiverged = errors.New("workspace changed after the prompt; undo refused")

func (e *Engine) Undo(sessionID string) (UndoResult, error)
```

Algorithm:

```text
1. Cancel a current run and wait for run.done without holding e.mu.
2. Load LatestPromptCheckpoint from session.UndoStore.
3. For a finished checkpoint, capture current and compare to AfterTree.
4. Restore BeforeTree.
5. Append Prompt.Checkpoint.Reverted.
6. If append fails, restore AfterTree when present; report persistence and compensation errors.
7. Load effective Events(sessionID, 0).
8. Return literal prompt and effective events.
```

Do not append `/undo` to composer history.

- [ ] **Step 5: Verify original GREEN**

Run:

```bash
go test -run TestEngine_UndoRestoresPrePromptWorkspaceAndEffectiveConversation -v ./internal/tui
```

Expected: PASS.

- [ ] **Step 6: Run TUI package tests**

Run:

```bash
go test ./internal/tui
```

Expected: PASS.

- [ ] **Step 7: Commit engine orchestration**

```bash
git add internal/tui/engine.go internal/tui/engine_test.go
git commit -m "feat(tui): checkpoint prompts and undo runs" -m "Generated-By: PostHog Code" -m "Task-Id: 613e649f-973b-44cf-a6ab-0e5dbc38c873"
```

---

### Task 6: TRIANGULATE — Safety and Persistence Cases

**Files:**
- Modify: `internal/checkpoint/git_test.go`
- Modify: `internal/session/undo_store_contract_test.go`
- Modify: `internal/tui/engine_test.go`

- [ ] **Step 1: Add repeated undo**

Add `TestEngine_UndoTwiceRestoresEachPromptBoundary`. Two prompts create distinct states; each undo returns the matching literal prompt, workspace tree, and effective conversation.

- [ ] **Step 2: Add divergence and ignored-file cases**

```go
func TestEngine_UndoRejectsWorkspaceDivergence(t *testing.T)
func TestEngine_UndoIgnoresIgnoredFileDivergence(t *testing.T)
```

The first expects `errors.Is(err, ErrWorkspaceDiverged)` and zero state changes. The second changes a `.gitignore`-matched file, succeeds, and preserves ignored content.

- [ ] **Step 3: Add active cancellation**

Add `TestEngine_UndoCancelsActiveRunBeforeRestore`. Block the provider after one real tool write, call `Undo`, observe context cancellation, then assert no late write or event survives after undo returns.

- [ ] **Step 4: Add SQLite restart persistence**

Add `TestEngine_UndoPersistsAcrossSQLiteReopen`. Run and undo, close/reopen SQLite, and assert `Messages`, `Events`, and `ContextForRunner` omit the reverted range and `LatestPromptCheckpoint` returns `ErrNothingToUndo`.

- [ ] **Step 5: Strengthen Git metadata test**

Stage a file before capture and compare `git diff --cached --binary` before and after restore. Expected: identical.

- [ ] **Step 6: Run race triangulation**

Run:

```bash
go test -race -run 'TestEngine_Undo|TestGitStore_|UndoStoreContract' -v ./internal/checkpoint ./internal/session ./internal/tui
```

Expected: PASS without race reports.

- [ ] **Step 7: Commit triangulation**

```bash
git add internal/checkpoint internal/session internal/tui
git commit -m "test(tui): triangulate prompt undo safety" -m "Generated-By: PostHog Code" -m "Task-Id: 613e649f-973b-44cf-a6ab-0e5dbc38c873"
```

---

### Task 7: Native `/undo` TUI Interaction

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/fold.go`

- [ ] **Step 1: Write failing model tests**

Add:

```go
func TestModel_UndoIsNativeCommandAndRestoresComposer(t *testing.T)
func TestModel_UndoFailureKeepsTranscriptAndComposer(t *testing.T)
func TestModel_UndoAppearsInSlashCompletion(t *testing.T)
func TestModel_UndoWithArgumentsIsRejectedLocally(t *testing.T)
```

Extend the fake agent with `Undo(sessionID string) (UndoResult, error)`. Success makes one undo call, no `SendPrompt`, replaces entries from returned events, restores composer/cursor, and keeps `/undo` out of history. Failure keeps transcript/input and appends a local error.

- [ ] **Step 2: Verify model RED**

Run:

```bash
go test -run 'TestModel_Undo' -v ./internal/tui
```

Expected: FAIL because `Agent.Undo` and result handling do not exist.

- [ ] **Step 3: Add asynchronous native command handling**

Extend `Agent` and add:

```go
type UndoDoneMsg struct {
    Result UndoResult
    Err    string
}
```

Before `/model`, `/new`, plan dispatch, history append, and `working=true`:

```go
trimmed := strings.TrimSpace(text)
if strings.HasPrefix(trimmed, "/undo") {
    if trimmed != "/undo" {
        return m.appendError("usage: /undo"), nil
    }
    return m, func() tea.Msg {
        result, err := m.agent.Undo(m.sessionID)
        if err != nil {
            return UndoDoneMsg{Err: err.Error()}
        }
        return UndoDoneMsg{Result: result}
    }
}
```

Keep the input until success so failed undo preserves `/undo` exactly.

- [ ] **Step 4: Rebuild transcript from effective events**

Extract:

```go
func (m Model) replaceEvents(events []session.SessionEvent) Model
```

Reset entries, streaming buffers, usage, pending permission/plan state, then fold through the existing event path. On success set the returned prompt, move cursor to end, clear menu, stop working, and resize/sync. On error keep transcript/input and append the error.

- [ ] **Step 5: Register completion**

Merge this native row into `Engine.Commands()`:

```go
command.Command{Name: "undo", Description: "Undo the last prompt and its file changes"}
```

Do not resolve it through a prompt template.

- [ ] **Step 6: Verify model GREEN and package safety**

Run:

```bash
go test -run 'TestModel_Undo' -v ./internal/tui && go test ./internal/tui
```

Expected: PASS.

- [ ] **Step 7: Commit TUI interaction**

```bash
git add internal/tui
git commit -m "feat(tui): add native undo command" -m "Generated-By: PostHog Code" -m "Task-Id: 613e649f-973b-44cf-a6ab-0e5dbc38c873"
```

---

### Task 8: Durable Checkpoint Path and Binary Wiring

**Files:**
- Modify: `internal/session/open.go`
- Modify: `internal/session/open_test.go`
- Modify: `cmd/atenea-tui/main.go`
- Modify: `cmd/atenea-tui/main_test.go`

- [ ] **Step 1: Write failing path tests**

```go
func TestDefaultCheckpointPath_UsesEnvOverride(t *testing.T)
func TestDefaultCheckpointPath_DefaultsToUserConfigDir(t *testing.T)
```

Expect `ATENEA_CHECKPOINTS` to win; otherwise use `<UserConfigDir>/atenea/checkpoints` and create it with mode `0700`.

- [ ] **Step 2: Verify path RED**

Run:

```bash
go test -run TestDefaultCheckpointPath -v ./internal/session
```

Expected: FAIL because the helper does not exist.

- [ ] **Step 3: Implement the helper**

```go
func DefaultCheckpointPath() string {
    if path := os.Getenv("ATENEA_CHECKPOINTS"); path != "" {
        return path
    }
    dir, err := os.UserConfigDir()
    if err != nil {
        return filepath.Join(os.TempDir(), "atenea", "checkpoints")
    }
    path := filepath.Join(dir, "atenea", "checkpoints")
    _ = os.MkdirAll(path, 0o700)
    return path
}
```

- [ ] **Step 4: Inject the store into the TUI binary**

```go
checkpoints := checkpoint.NewGitStore(session.DefaultCheckpointPath())
engine := tui.NewEngine(tui.EngineConfig{
    Root: root, Provider: providerService.Provider(), Store: store,
    Models: providerService, Checkpoints: checkpoints,
})
```

Add `ATENEA_CHECKPOINTS=<temp>/checkpoints` to every subprocess test environment so tests never share user data.

- [ ] **Step 5: Verify wiring**

Run:

```bash
go test ./internal/session ./cmd/atenea-tui
```

Expected: PASS.

- [ ] **Step 6: Commit wiring**

```bash
git add internal/session/open.go internal/session/open_test.go cmd/atenea-tui/main.go cmd/atenea-tui/main_test.go
git commit -m "feat(tui): persist workspace checkpoints" -m "Generated-By: PostHog Code" -m "Task-Id: 613e649f-973b-44cf-a6ab-0e5dbc38c873"
```

---

### Task 9: REFACTOR — Simplify, Document, and Verify

**Files:**
- Modify: `.okf/architecture/tui.md`
- Modify: Task 3–8 files only where cleanup is justified

- [ ] **Step 1: Perform bounded cleanup**

Allowed cleanup only:

```text
share Git command construction;
share Engine operation-lock lookup;
retain one effective-event projection helper;
retain one transcript replacement helper;
remove duplicated test helpers.
```

Do not add redo, retention, background cleanup, a transaction framework, a snapshot factory, or desktop adapters.

- [ ] **Step 2: Update TUI architecture documentation**

Document that `/undo` is local, uses isolated Git data, restores tracked and non-ignored untracked workspace content, leaves ignored files and main `.git` metadata untouched, rejects finished-checkpoint divergence, and has no desktop frontend control.

- [ ] **Step 3: Format and run focused race tests**

Run:

```bash
gofmt -w internal/checkpoint internal/session internal/tui cmd/atenea-tui
go test -race ./internal/checkpoint ./internal/session ./internal/tui
```

Expected: PASS without race reports.

- [ ] **Step 4: Run full quality gates**

Run:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test ./...
```

Expected: all commands exit 0; formatter output is empty.

- [ ] **Step 5: Inspect final diff**

Run:

```bash
git diff --check && git status --short
```

Expected: no whitespace errors and only planned files changed.

- [ ] **Step 6: Commit documentation/refactor**

```bash
git add .okf/architecture/tui.md internal/checkpoint internal/session internal/tui cmd/atenea-tui
git commit -m "docs(tui): document prompt undo behavior" -m "Generated-By: PostHog Code" -m "Task-Id: 613e649f-973b-44cf-a6ab-0e5dbc38c873"
```

---

## Final TDD Evidence

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Existing suites checked before edits | `go test ./internal/session ./internal/tui ./cmd/atenea-tui`; `go test ./...` | Record actual result |
| Understand | Approved design and architecture mapped | `.okf/specs/2026-07-11-tui-prompt-undo.md`; this plan | Behavior identified |
| RED | End-to-end undo test failed before production API existed | Focused Task 2 command | Record expected failure |
| GREEN | Snapshot, projection, and engine passed focused tests | Tasks 3–5 commands | Record gate result |
| TRIANGULATE | Repeated, divergence, ignored, active, restart, and Git metadata cases passed | Task 6 race command | Record gate result |
| REFACTOR | Formatting, vet, race, and full suite stayed green | Task 9 commands | Record gate result |

## Self-Review

- All specification sections map to Tasks 2–9.
- No `frontend/` file or desktop UI control is included.
- The plan uses only Git and the Go standard library; no dependency, redo, retention service, or cleanup daemon.
- `checkpoint.Store`, `PromptCheckpoint`, `EffectiveCheckpoint`, `UndoStore`, `UndoResult`, and `Agent.Undo` remain consistent throughout.
- Prompt admission stops on before-snapshot failure; revert persistence occurs after restore; failed persistence attempts after-tree compensation.
