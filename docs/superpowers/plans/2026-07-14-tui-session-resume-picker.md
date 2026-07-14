# TUI Session Resume Picker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/resume` open a searchable full-screen picker that shows each workspace session's title and durable last-activity date/time, then opens the explicitly selected session.

**Architecture:** Extend `session.SessionSummary` with durable activity metadata, expose explicit list/load operations from the TUI engine, and add an isolated picker state/render unit to the Bubble Tea model. Listing and loading remain asynchronous; the current chat is replaced only after a successful selected-session load.

**Tech Stack:** Go 1.23+, Bubble Tea, Bubbles `textinput`, Lip Gloss, SQLite via `modernc.org/sqlite`, Go testing.

---

## File Map

- Modify `internal/session/store.go`: add `LastActivity` to the shared summary contract.
- Modify `internal/session/memstore.go`: track last activity for in-memory sessions.
- Modify `internal/session/store_contract_test.go`: assert summary timestamps and activity ordering.
- Modify `internal/session/sqlitestore.go`: migrate and persist event activity timestamps.
- Modify `internal/session/sqlitestore_test.go`: cover legacy migration and reopen durability.
- Modify `internal/session/sqlitestore_multiproc_test.go`: keep stable summary assertions compatible with timestamps.
- Modify `internal/tui/engine.go`: replace sequential resume with list and exact-load operations.
- Modify `internal/tui/engine_test.go`: cover filtering, validation, exact loading, and active-run rejection.
- Create `internal/tui/resume_picker.go`: own picker input, filtering, selection, and date formatting.
- Create `internal/tui/resume_picker_test.go`: test picker state as a focused unit.
- Modify `internal/tui/model.go`: route `/resume`, async messages, keyboard handling, and successful session replacement.
- Modify `internal/tui/model_test.go`: reproduce and verify the end-user `/resume` flow.
- Modify `internal/tui/view.go`: render the full-screen picker before the normal chat layout.
- Modify `frontend/wailsjs/go/models.ts`: mirror the exported summary timestamp.
- Modify `frontend/src/stores/chat.ts`: keep the frontend summary type aligned.
- Modify `.okf/architecture/tui.md`: document the new picker and engine/store boundaries.

Implementation note: this worktree already contains unrelated uncommitted edits in several target files. Preserve them, inspect `git diff` before each patch, and stage only the files intentionally completed by each task.

### Task 1: Durable Summary Activity Contract

**Files:**
- Modify: `internal/session/store.go`
- Modify: `internal/session/memstore.go`
- Test: `internal/session/store_contract_test.go`

- [ ] **Step 1: Write the failing store contract assertions**

Extend the existing `SessionsOrdersByRecencyWithFirstUserMessageTitle` contract case so it captures the first summary time, appends another event, and asserts non-zero, monotonic last activity:

```go
first, err := store.Sessions(ctx)
if err != nil {
	t.Fatalf("Sessions before new activity: %v", err)
}
if len(first) != 2 || first[0].LastActivity.IsZero() || first[1].LastActivity.IsZero() {
	t.Fatalf("Sessions before new activity = %+v, want non-zero LastActivity", first)
}

appendContractMessage(t, store, "s2", Message{ID: "m5", Role: RoleAssistant, Text: "new activity"})
second, err := store.Sessions(ctx)
if err != nil {
	t.Fatalf("Sessions after new activity: %v", err)
}
if second[0].ID != "s2" {
	t.Fatalf("Sessions after new activity = %+v, want s2 first", second)
}
if second[0].LastActivity.Before(first[0].LastActivity) {
	t.Fatalf("s2 LastActivity = %v, want >= previous newest %v", second[0].LastActivity, first[0].LastActivity)
}
```

Replace dynamic whole-struct comparisons with this stable projection, while
dedicated assertions verify `LastActivity`:

```go
func stableSummaries(items []SessionSummary) []SessionSummary {
	out := make([]SessionSummary, len(items))
	for index, item := range items {
		out[index] = SessionSummary{ID: item.ID, Title: item.Title, Cwd: item.Cwd}
	}
	return out
}
```

- [ ] **Step 2: Run the contract test to verify it fails**

Run: `go test -run 'Test(MemoryStore|SQLiteStore.*)_Contract/SessionsOrdersByRecencyWithFirstUserMessageTitle' -v ./internal/session`

Expected: FAIL to compile because `SessionSummary.LastActivity` does not exist.

- [ ] **Step 3: Add the summary field and memory-store timestamp**

In `internal/session/store.go`:

```go
import (
	"context"
	"errors"
	"time"
)

type SessionSummary struct {
	ID           string
	Title        string
	Cwd          string
	LastActivity time.Time
}
```

In `internal/session/memstore.go`, add a `lastActivity map[string]time.Time`, initialize it in `NewMemoryStore`, set it after every successful append with `time.Now().UTC()`, delete it in `DeleteSession`, and project it:

```go
entries = append(entries, entry{
	id: id, title: title, cwd: cwd,
	last: s.lastSeen[id], lastActivity: s.lastActivity[id],
})

out = append(out, SessionSummary{
	ID: e.id, Title: e.title, Cwd: e.cwd, LastActivity: e.lastActivity,
})
```

Keep the existing monotonic `lastSeen` counter for deterministic ordering when multiple appends share a clock tick.

- [ ] **Step 4: Run focused store tests**

Run: `go test -run 'Test(MemoryStore|SQLiteStore.*)_Contract/(SessionsOrdersByRecencyWithFirstUserMessageTitle|SessionsTitleEmptyWhenNoUserMessage|DeleteSessionRemovesSessionLeavingOthers)' -v ./internal/session`

Expected: PASS.

- [ ] **Step 5: Commit the contract and memory implementation**

```bash
git add internal/session/store.go internal/session/memstore.go internal/session/store_contract_test.go
git commit -m "$(cat <<'EOF'
feat(session): expose last activity in summaries

Generated-By: PostHog Code
Task-Id: c847901d-cc83-43bc-82a6-0059a2fbde3e
EOF
)"
```

### Task 2: SQLite Activity Timestamp Migration

**Files:**
- Modify: `internal/session/sqlitestore.go`
- Test: `internal/session/sqlitestore_test.go`
- Test: `internal/session/sqlitestore_multiproc_test.go`
- Test: `internal/session/store_contract_test.go`
- Modify: `frontend/wailsjs/go/models.ts`
- Modify: `frontend/src/stores/chat.ts`

- [ ] **Step 1: Write the legacy migration test**

Add a test that creates the historical events table without `activity_at`, inserts two sessions, opens it through `NewSQLiteStore`, and verifies migration plus durable summaries:

```go
func TestSQLiteStore_MigratesLegacyEventsWithActivityTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil { t.Fatal(err) }
	_, err = db.Exec(`CREATE TABLE events (
		session_id TEXT NOT NULL, seq INTEGER NOT NULL, kind TEXT NOT NULL,
		has_message INTEGER NOT NULL, msg_id TEXT, role TEXT, text TEXT,
		call_id TEXT, tool_name TEXT, input BLOB, usage BLOB, error TEXT,
		PRIMARY KEY (session_id, seq)
	)`)
	if err != nil { t.Fatal(err) }
	_, err = db.Exec(`INSERT INTO events
		(session_id, seq, kind, has_message, msg_id, role, text)
		VALUES ('tui-1', 1, '', 1, 'm1', 'user', 'older'),
		       ('tui-2', 1, '', 1, 'm2', 'user', 'newer')`)
	if err != nil { t.Fatal(err) }
	if err := db.Close(); err != nil { t.Fatal(err) }

	store, err := NewSQLiteStore(path)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	summaries, err := store.Sessions(context.Background())
	if err != nil { t.Fatal(err) }
	if len(summaries) != 2 || summaries[0].LastActivity.IsZero() || summaries[1].LastActivity.IsZero() {
		t.Fatalf("Sessions = %+v, want migrated activity times", summaries)
	}
}
```

Add a reopen assertion to an existing SQLite store test: append an event, save its summary time, close/reopen, and require `LastActivity.Equal(saved)`.

- [ ] **Step 2: Run migration tests to verify failure**

Run: `go test -run 'TestSQLiteStore_(MigratesLegacyEventsWithActivityTime|ReopenResumesLog)' -v ./internal/session`

Expected: FAIL because SQLite summaries do not populate `LastActivity`.

- [ ] **Step 3: Add the SQLite column and migration backfill**

Add this column to the current schema:

```sql
activity_at INTEGER NOT NULL DEFAULT 0,
```

Add it to the additive migration list:

```go
{"activity_at", "INTEGER NOT NULL DEFAULT 0"},
```

After all columns exist, backfill only legacy zero values once per migration call:

```go
legacyActivity := time.Now().UTC().UnixMilli()
if _, err := db.Exec(`UPDATE events SET activity_at = ? WHERE activity_at = 0`, legacyActivity); err != nil {
	return fmt.Errorf("backfill events activity_at: %w", err)
}
```

Persist current UTC milliseconds in `AppendEvent` by adding `activity_at` to the INSERT column/value lists:

```go
activityAt := time.Now().UTC().UnixMilli()
```

Update `Sessions` to select both session ID and `MAX(activity_at)`, retain `MAX(rowid) DESC` as the deterministic tie-breaker, and project:

```go
LastActivity: time.UnixMilli(activityAt).UTC(),
```

Update SQLite reopen and multiprocess tests to compare ID/title/Cwd through
`stableSummaries`, then assert every returned `LastActivity` is non-zero.

Keep the Wails and frontend mirrors aligned:

```ts
// frontend/wailsjs/go/models.ts
LastActivity: string;
// constructor
this.LastActivity = source["LastActivity"];

// frontend/src/stores/chat.ts
LastActivity: string
```

- [ ] **Step 4: Run the complete session package**

Run: `go test ./internal/session`

Expected: PASS.

- [ ] **Step 5: Commit SQLite durability**

```bash
git add internal/session/sqlitestore.go internal/session/sqlitestore_test.go internal/session/sqlitestore_multiproc_test.go internal/session/store_contract_test.go frontend/wailsjs/go/models.ts frontend/src/stores/chat.ts
git commit -m "$(cat <<'EOF'
feat(session): persist session activity timestamps

Generated-By: PostHog Code
Task-Id: c847901d-cc83-43bc-82a6-0059a2fbde3e
EOF
)"
```

### Task 3: Explicit Engine List and Load Operations

**Files:**
- Modify: `internal/tui/engine.go`
- Test: `internal/tui/engine_test.go`

- [ ] **Step 1: Write engine tests for the new boundary**

Add table-driven tests that create TUI sessions in two roots plus a non-TUI session, then assert:

```go
sessions, err := engine.ListResumeSessions("tui-current")
if err != nil { t.Fatal(err) }
if got := summaryIDs(sessions); !reflect.DeepEqual(got, []string{"tui-newer", "tui-current"}) {
	t.Fatalf("ListResumeSessions IDs = %v", got)
}

result, err := engine.ResumeSessionByID("tui-current", "tui-newer")
if err != nil { t.Fatal(err) }
if result.SessionID != "tui-newer" || result.Mode != session.ModePlan {
	t.Fatalf("ResumeSessionByID = %+v", result)
}
```

Add separate assertions for an active current run, a missing target, a non-`tui-` target, and a target whose `Cwd` differs from `engine.root`.

- [ ] **Step 2: Run focused engine tests to verify failure**

Run: `go test -run 'TestEngine_(ListResumeSessions|ResumeSessionByID)' -v ./internal/tui`

Expected: FAIL to compile because the methods do not exist.

- [ ] **Step 3: Implement list and exact load**

Replace `ResumePrevious` with:

```go
func (e *Engine) ListResumeSessions(sessionID string) ([]session.SessionSummary, error) {
	if e.agent.Running(sessionID) {
		return nil, errors.New("stop the active run before resuming another session")
	}
	summaries, err := e.store.Sessions(context.Background())
	if err != nil { return nil, err }
	root := filepath.Clean(e.root)
	out := make([]session.SessionSummary, 0, len(summaries))
	for _, summary := range summaries {
		if strings.HasPrefix(summary.ID, "tui-") && filepath.Clean(summary.Cwd) == root {
			out = append(out, summary)
		}
	}
	return out, nil
}

func (e *Engine) ResumeSessionByID(currentSessionID, targetSessionID string) (ResumeResult, error) {
	summaries, err := e.ListResumeSessions(currentSessionID)
	if err != nil { return ResumeResult{}, err }
	allowed := false
	for _, summary := range summaries {
		if summary.ID == targetSessionID { allowed = true; break }
	}
	if !allowed { return ResumeResult{}, errors.New("session is not resumable in this workspace") }
	events, err := e.store.Events(context.Background(), targetSessionID, 0)
	if err != nil { return ResumeResult{}, err }
	mode := modeFromEvents(events)
	if _, err := e.store.AppendEvent(context.Background(), targetSessionID, session.SessionEvent{Kind: session.KindSessionMode, Text: string(mode)}); err != nil {
		return ResumeResult{}, err
	}
	return ResumeResult{SessionID: targetSessionID, Events: events, Mode: mode}, nil
}
```

- [ ] **Step 4: Run focused and package tests**

Run: `go test -run 'TestEngine_(ListResumeSessions|ResumeSessionByID)' -v ./internal/tui && go test ./internal/tui`

Expected: PASS.

- [ ] **Step 5: Commit the engine boundary**

```bash
git add internal/tui/engine.go internal/tui/engine_test.go
git commit -m "$(cat <<'EOF'
refactor(tui): load explicitly selected sessions

Generated-By: PostHog Code
Task-Id: c847901d-cc83-43bc-82a6-0059a2fbde3e
EOF
)"
```

### Task 4: Picker State and Filtering Unit

**Files:**
- Create: `internal/tui/resume_picker.go`
- Create: `internal/tui/resume_picker_test.go`

- [ ] **Step 1: Write focused picker state tests**

Cover case-insensitive title filtering, selection clamping, current-session detection, empty results, and date formatting:

```go
func TestResumePicker_FiltersAndKeepsSelectionInRange(t *testing.T) {
	picker := newResumePicker("tui-current")
	picker.setSessions([]session.SessionSummary{
		{ID: "tui-current", Title: "Fix login", LastActivity: time.Date(2026, 7, 14, 9, 30, 0, 0, time.Local)},
		{ID: "tui-other", Title: "Add billing", LastActivity: time.Date(2026, 7, 13, 18, 5, 0, 0, time.Local)},
	})
	picker.query.SetValue("BILL")
	picker.filter()
	if len(picker.filtered) != 1 || picker.filtered[0].ID != "tui-other" || picker.selected != 0 {
		t.Fatalf("filtered=%+v selected=%d", picker.filtered, picker.selected)
	}
}

func TestFormatResumeActivity(t *testing.T) {
	got := formatResumeActivity(time.Date(2026, 7, 14, 9, 5, 0, 0, time.Local))
	if got != "Jul 14, 2026 09:05" { t.Fatalf("got %q", got) }
}
```

- [ ] **Step 2: Run the picker tests to verify failure**

Run: `go test -run 'TestResumePicker|TestFormatResumeActivity' -v ./internal/tui`

Expected: FAIL because `resumePicker` does not exist.

- [ ] **Step 3: Implement the isolated picker state**

Create `internal/tui/resume_picker.go` with a one-line Bubbles input and pure filtering helpers:

```go
type resumePicker struct {
	open      bool
	loading   bool
	currentID string
	query     textinput.Model
	sessions  []session.SessionSummary
	filtered  []session.SessionSummary
	selected  int
	err       string
}

func newResumePicker(currentID string) resumePicker {
	query := textinput.New()
	query.Prompt = ""
	query.Placeholder = "Search sessions"
	query.Focus()
	return resumePicker{open: true, loading: true, currentID: currentID, query: query}
}

func (p *resumePicker) filter() {
	needle := strings.ToLower(strings.TrimSpace(p.query.Value()))
	p.filtered = p.filtered[:0]
	for _, summary := range p.sessions {
		if needle == "" || strings.Contains(strings.ToLower(summary.Title), needle) {
			p.filtered = append(p.filtered, summary)
		}
	}
	if len(p.filtered) == 0 { p.selected = 0; return }
	p.selected = min(p.selected, len(p.filtered)-1)
}

func formatResumeActivity(value time.Time) string {
	return value.Local().Format("Jan 02, 2006 15:04")
}
```

Implement the remaining methods with no engine access:

```go
func (p *resumePicker) setSessions(items []session.SessionSummary) {
	p.loading = false
	p.err = ""
	p.sessions = append([]session.SessionSummary(nil), items...)
	p.selected = 0
	p.filter()
}

func (p *resumePicker) selectedSession() (session.SessionSummary, bool) {
	if len(p.filtered) == 0 || p.selected < 0 || p.selected >= len(p.filtered) {
		return session.SessionSummary{}, false
	}
	return p.filtered[p.selected], true
}

func (p *resumePicker) move(delta int) {
	if len(p.filtered) == 0 {
		return
	}
	p.selected = (p.selected + delta + len(p.filtered)) % len(p.filtered)
}

func (p *resumePicker) close() {
	p.open = false
	p.loading = false
	p.err = ""
	p.query.Blur()
}
```

- [ ] **Step 4: Run focused picker tests**

Run: `go test -run 'TestResumePicker|TestFormatResumeActivity' -v ./internal/tui`

Expected: PASS.

- [ ] **Step 5: Commit the picker unit**

```bash
git add internal/tui/resume_picker.go internal/tui/resume_picker_test.go
git commit -m "$(cat <<'EOF'
feat(tui): add resume picker state

Generated-By: PostHog Code
Task-Id: c847901d-cc83-43bc-82a6-0059a2fbde3e
EOF
)"
```

### Task 5: End-to-End Bubble Tea Resume Flow

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/resume_picker.go`

- [ ] **Step 1: Reproduce the user-visible bug in a model test**

Replace the fake agent's sequential resume method with list/load tracking and add this flow:

```go
func TestModel_ResumeOpensPickerAndLoadsSelectedSession(t *testing.T) {
	fake := &fakeAgent{
		resumeSessions: []session.SessionSummary{
			{ID: "tui-current", Title: "Current", LastActivity: time.Date(2026, 7, 14, 10, 0, 0, 0, time.Local)},
			{ID: "tui-other", Title: "Other session", LastActivity: time.Date(2026, 7, 13, 9, 0, 0, 0, time.Local)},
		},
		resumeByID: map[string]ResumeResult{
			"tui-other": {SessionID: "tui-other", Events: []session.SessionEvent{{Message: &session.Message{Role: session.RoleUser, Text: "restored"}}}},
		},
	}
	m := NewModel(fake, "tui-current", nil)
	m.entries = []entry{{kind: entryUser, text: "unchanged until selection"}}
	m = typeRunes(t, m, "/resume")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil || !m.resumePicker.open { t.Fatal("/resume must open and load picker") }
	m = apply(t, m, cmd())
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	m = apply(t, m, cmd())
	if m.sessionID != "tui-other" || !strings.Contains(m.View(), "restored") {
		t.Fatalf("session=%q view=%q", m.sessionID, ansi.Strip(m.View()))
	}
}
```

Add tests that Escape preserves the existing transcript, no-match filtering renders `No sessions found`, list/load failures preserve the current session, and an exact `/resume extra` remains rejected locally.

- [ ] **Step 2: Run the E2E model test to verify failure**

Run: `go test -run 'TestModel_Resume' -v ./internal/tui`

Expected: FAIL because `/resume` still calls `ResumePrevious` and no picker state exists on `Model`.

- [ ] **Step 3: Change the agent interface and async messages**

In `internal/tui/model.go`:

```go
type Agent interface {
	SendPrompt(sessionID, text string) (RunHandle, error)
	SendPlanPrompt(sessionID, text string) (RunHandle, error)
	AcceptPlan(sessionID string) (RunHandle, error)
	Undo(sessionID string) (UndoResult, error)
	ListResumeSessions(sessionID string) ([]session.SessionSummary, error)
	ResumeSessionByID(currentSessionID, targetSessionID string) (ResumeResult, error)
	ResolvePermission(sessionID, callID string, approved bool)
	Stop(sessionID string)
}

type ResumeSessionsDoneMsg struct {
	Sessions []session.SessionSummary
	Err      string
}
```

Add `resumePicker resumePicker` to `Model`. On exact `/resume`, clear the composer, initialize the picker, and return a command that calls `ListResumeSessions`.

Reject an active run before opening the picker:

```go
if trimmed == "/resume" && m.working {
	return m.appendError("stop the active run before resuming another session"), nil
}
```

- [ ] **Step 4: Route picker messages and keys**

Handle `ResumeSessionsDoneMsg` before normal chat input. While the picker is open:

```go
switch msg.Type {
case tea.KeyEsc:
	m.resumePicker.close()
	return m, nil
case tea.KeyUp:
	m.resumePicker.move(-1)
	return m, nil
case tea.KeyDown:
	m.resumePicker.move(1)
	return m, nil
case tea.KeyEnter:
	target, ok := m.resumePicker.selectedSession()
	if !ok { return m, nil }
	currentID, agent := m.sessionID, m.agent
	m.resumePicker.loading = true
	return m, func() tea.Msg {
		result, err := agent.ResumeSessionByID(currentID, target.ID)
		if err != nil { return ResumeDoneMsg{Err: err.Error()} }
		return ResumeDoneMsg{Result: result}
	}
default:
	var cmd tea.Cmd
	m.resumePicker.query, cmd = m.resumePicker.query.Update(msg)
	m.resumePicker.filter()
	return m, cmd
}
```

On successful `ResumeDoneMsg`, close the picker and apply the existing replacement logic. On failure, keep it open, set `resumePicker.err`, and leave `sessionID`, entries, history, and mode unchanged.

- [ ] **Step 5: Run all resume model tests**

Run: `go test -run 'TestModel_Resume' -v ./internal/tui`

Expected: PASS.

- [ ] **Step 6: Commit the interactive flow**

```bash
git add internal/tui/model.go internal/tui/model_test.go internal/tui/resume_picker.go
git commit -m "$(cat <<'EOF'
feat(tui): open session picker from resume command

Generated-By: PostHog Code
Task-Id: c847901d-cc83-43bc-82a6-0059a2fbde3e
EOF
)"
```

### Task 6: Full-Screen Picker Rendering

**Files:**
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/model_test.go`
- Test: `internal/tui/resume_picker_test.go`

- [ ] **Step 1: Write visual contract tests**

At a known terminal size, assert the picker replaces chat content, contains the rounded search border, aligns title/date text on one row, marks the current session, and never exceeds terminal width:

```go
func TestModel_ResumePickerRendersFullScreenRows(t *testing.T) {
	m := NewModel(&fakeAgent{}, "tui-current", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 72, Height: 18})
	m.resumePicker = newResumePicker("tui-current")
	m.resumePicker.loading = false
	m.resumePicker.setSessions([]session.SessionSummary{{
		ID: "tui-current", Title: "Current session",
		LastActivity: time.Date(2026, 7, 14, 9, 5, 0, 0, time.Local),
	}})
	plain := ansi.Strip(m.View())
	if strings.Contains(plain, "chat *") || !strings.Contains(plain, "Current session") || !strings.Contains(plain, "Jul 14, 2026 09:05") {
		t.Fatalf("View() = %q", plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if ansi.StringWidth(line) > 72 { t.Fatalf("line width > 72: %q", line) }
	}
}
```

Add narrow-terminal and `No sessions found` assertions.

- [ ] **Step 2: Run visual tests to verify failure**

Run: `go test -run 'TestModel_ResumePickerRenders|TestResumePicker_.*Width' -v ./internal/tui`

Expected: FAIL because `View` still renders the chat canvas.

- [ ] **Step 3: Render picker before normal layout**

At the start of `Model.View`:

```go
if m.resumePicker.open {
	return m.resumePickerView()
}
```

Implement `resumePickerView` and `resumePickerRow` using the existing `canvasStyle`, `accentStyle`, `statusStyle`, `composerBorderStyle`, and `lipgloss.RoundedBorder()`. Use `ansi.Truncate` before padding. Reserve the timestamp width first; when the terminal is too narrow, truncate or omit the timestamp before truncating the title. Render the current-session marker as `• current` in muted style and use `No sessions found` exactly for an empty filtered list.

The search field should use this shape:

```go
search := lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("8")).
	Padding(0, 1).
	Width(max(m.width-6, 0)).
	Render(m.resumePicker.query.View())
```

Pad the full content through `renderCanvas` so the picker occupies the complete terminal body and remains on the dark background.

- [ ] **Step 4: Run visual and package tests**

Run: `go test -run 'TestModel_ResumePicker|TestResumePicker' -v ./internal/tui && go test ./internal/tui`

Expected: PASS.

- [ ] **Step 5: Commit the full-screen view**

```bash
git add internal/tui/view.go internal/tui/model_test.go internal/tui/resume_picker_test.go
git commit -m "$(cat <<'EOF'
feat(tui): render full-screen session picker

Generated-By: PostHog Code
Task-Id: c847901d-cc83-43bc-82a6-0059a2fbde3e
EOF
)"
```

### Task 7: Documentation and Quality Gates

**Files:**
- Modify: `.okf/architecture/tui.md`

- [ ] **Step 1: Document the final behavior**

Add a concise `/resume` section describing:

```markdown
### Session resume picker

`/resume` opens a full-screen, keyboard-driven picker for TUI sessions whose
`Session.Cwd` matches the current workspace. Session summaries include the
durable timestamp of their latest event. The model filters titles locally and
loads only the session explicitly confirmed with Enter; Escape preserves the
current chat unchanged.
```

Also update the engine boundary list to name `ListResumeSessions` and `ResumeSessionByID`.

- [ ] **Step 2: Format all changed Go files**

Run: `gofmt -w internal/session/store.go internal/session/memstore.go internal/session/store_contract_test.go internal/session/sqlitestore.go internal/session/sqlitestore_test.go internal/tui/engine.go internal/tui/engine_test.go internal/tui/resume_picker.go internal/tui/resume_picker_test.go internal/tui/model.go internal/tui/model_test.go internal/tui/view.go`

Expected: command exits 0.

- [ ] **Step 3: Run focused race tests for concurrent stores and TUI**

Run: `go test -race ./internal/session ./internal/tui`

Expected: PASS with no race reports.

- [ ] **Step 4: Run repository-wide quality gates**

Run: `go test ./... && go vet ./... && test -z "$(gofmt -l .)"`

Expected: all tests pass, `go vet` is clean, and `gofmt -l` prints nothing.

- [ ] **Step 5: Inspect the actual terminal UI**

Run: `wails dev` or the repository's TUI entry command, create at least two sessions, execute `/resume`, type a filter, navigate with arrows, cancel once with Escape, reopen, and confirm another session.

Expected: the picker fills the dark canvas, search and rows do not wrap, the title/date alignment remains readable, `No sessions found` appears in English, Escape preserves the chat, and Enter restores the chosen transcript.

- [ ] **Step 6: Commit documentation and final cleanup**

```bash
git add .okf/architecture/tui.md
git commit -m "$(cat <<'EOF'
docs(tui): document session resume picker

Generated-By: PostHog Code
Task-Id: c847901d-cc83-43bc-82a6-0059a2fbde3e
EOF
)"
```

Do not include unrelated pre-existing worktree changes in this commit.
