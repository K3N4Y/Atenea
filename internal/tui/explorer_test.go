package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// keyRunes builds a rune key message, the shape the explorer key handler reads
// for j/k/l/h/q/r.
func keyRunes(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// loadedExplorer opens an explorer, drains the synchronous or async load, and
// returns the panel populated with the given paths so a test can drive
// navigation without repeating the load dance.
func loadedExplorer(t *testing.T, paths []string) explorer {
	t.Helper()
	e, cmd := explorer{}.open(func() ([]string, error) { return paths, nil })
	if cmd == nil {
		t.Fatalf("open with a listFiles func must schedule a load")
	}
	msg, ok := cmd().(filesListedMsg)
	if !ok {
		t.Fatalf("load command produced %T, want filesListedMsg", cmd())
	}
	return e.applyListed(msg, 100)
}

func TestExplorer_OpenSchedulesLoadAndCloseFlagsClosed(t *testing.T) {
	e, cmd := explorer{}.open(func() ([]string, error) { return []string{"go.mod"}, nil })
	if !e.isOpen() {
		t.Fatal("open must set the panel open")
	}
	if !e.treeLoading || e.treeGen != 1 {
		t.Fatalf("open must start a load: loading=%v gen=%d", e.treeLoading, e.treeGen)
	}
	if cmd == nil {
		t.Fatal("open with a listFiles func must return a load command")
	}

	closed := e.close()
	if closed.isOpen() {
		t.Fatal("close must clear the open flag")
	}
}

func TestExplorer_OpenWithoutListFilesLoadsEmptySynchronously(t *testing.T) {
	e, cmd := explorer{}.open(nil)
	if cmd != nil {
		t.Fatal("open without a listFiles func must not schedule a command")
	}
	if !e.treeLoaded || e.treeLoading {
		t.Fatalf("open without listFiles must load empty synchronously: loaded=%v loading=%v", e.treeLoaded, e.treeLoading)
	}
	if got := len(e.tree.visibleRows()); got != 0 {
		t.Fatalf("empty synchronous load must yield no rows, got %d", got)
	}
}

func TestExplorer_CursorMoveClampsAtBothEnds(t *testing.T) {
	e := loadedExplorer(t, []string{"a.go", "b.go", "c.go"})
	if e.treeCursor != 0 {
		t.Fatalf("cursor starts at 0, got %d", e.treeCursor)
	}

	// Down past the top row.
	e, _, _ = e.handleKey(keyRunes('j'), 100, nil)
	if e.treeCursor != 1 {
		t.Fatalf("j must move down: cursor=%d", e.treeCursor)
	}

	// Down at the last row must NOT wrap.
	e, _, _ = e.handleKey(keyRunes('j'), 100, nil)
	e, _, _ = e.handleKey(keyRunes('j'), 100, nil)
	if e.treeCursor != 2 {
		t.Fatalf("j at the last row must clamp: cursor=%d, want 2", e.treeCursor)
	}

	// Up at the top row must NOT wrap.
	e, _, _ = e.handleKey(keyRunes('k'), 100, nil)
	e, _, _ = e.handleKey(keyRunes('k'), 100, nil)
	e, _, _ = e.handleKey(keyRunes('k'), 100, nil)
	if e.treeCursor != 0 {
		t.Fatalf("k at the top row must clamp: cursor=%d, want 0", e.treeCursor)
	}
}

func TestExplorer_EnterExpandsAndCollapsesFolder(t *testing.T) {
	e := loadedExplorer(t, []string{"internal/tui/model.go", "go.mod"})
	// Rows collapsed: [internal, go.mod]; cursor on "internal" (a folder).
	if got, want := rowPaths(e.tree.visibleRows()), []string{"internal", "go.mod"}; !equalStrings(got, want) {
		t.Fatalf("collapsed rows = %v, want %v", got, want)
	}

	e, intent, cmd := e.handleKey(keyRunes('l'), 100, nil)
	if intent.openPath != "" || intent.closePanel || cmd != nil {
		t.Fatalf("expanding a folder must not open a file or close: intent=%+v cmd=%v", intent, cmd != nil)
	}
	if got, want := rowPaths(e.tree.visibleRows()), []string{"internal", "internal/tui", "go.mod"}; !equalStrings(got, want) {
		t.Fatalf("expanded rows = %v, want %v", got, want)
	}

	// l again on the same folder collapses it.
	e, _, _ = e.handleKey(keyRunes('l'), 100, nil)
	if got, want := rowPaths(e.tree.visibleRows()), []string{"internal", "go.mod"}; !equalStrings(got, want) {
		t.Fatalf("re-collapsed rows = %v, want %v", got, want)
	}
}

func TestExplorer_HCollapsesThenMovesToParent(t *testing.T) {
	e := loadedExplorer(t, []string{"internal/tui/model.go"})
	// Expand internal, then internal/tui, then select the file row.
	e, _, _ = e.handleKey(tea.KeyMsg{Type: tea.KeyEnter}, 100, nil) // expand internal
	e, _, _ = e.handleKey(keyRunes('j'), 100, nil)                  // -> internal/tui
	e, _, _ = e.handleKey(tea.KeyMsg{Type: tea.KeyEnter}, 100, nil) // expand internal/tui
	e, _, _ = e.handleKey(keyRunes('j'), 100, nil)                  // -> model.go

	e, _, _ = e.handleKey(keyRunes('h'), 100, nil)
	if got := e.tree.visibleRows()[e.treeCursor].node.path; got != "internal/tui" {
		t.Fatalf("h from a file must move to its parent, got %q", got)
	}
	e, _, _ = e.handleKey(keyRunes('h'), 100, nil)
	if e.tree.expanded["internal/tui"] {
		t.Fatal("h on an expanded folder must collapse it")
	}
}

func TestExplorer_EnterOnFileYieldsOpenPathIntent(t *testing.T) {
	e := loadedExplorer(t, []string{"main.go"})
	// Single top-level file row selected.
	e, intent, cmd := e.handleKey(tea.KeyMsg{Type: tea.KeyEnter}, 100, nil)
	if intent.openPath != "main.go" {
		t.Fatalf("Enter on a file must surface openPath, got %+v", intent)
	}
	if intent.closePanel {
		t.Fatal("opening a file must not also request close")
	}
	if cmd != nil {
		t.Fatal("the explorer must not itself schedule a viewer command; the root drives the viewer")
	}
	if !e.isOpen() {
		t.Fatal("opening a file must not close the panel")
	}
}

func TestExplorer_EscAndQYieldCloseIntent(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, keyRunes('q')} {
		e := loadedExplorer(t, []string{"go.mod"})
		_, intent, _ := e.handleKey(key, 100, nil)
		if !intent.closePanel {
			t.Fatalf("%s must surface a close intent", key.String())
		}
		if intent.openPath != "" {
			t.Fatalf("%s must not open a file", key.String())
		}
	}
}

func TestExplorer_LeftClickOnFileYieldsOpenPathIntent(t *testing.T) {
	e := loadedExplorer(t, []string{"a.go", "b.go"})
	// Row 1 ("b.go") is the second body row; rowStartY 0, so Y=1.
	e, intent, handled := e.handleMouse(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		Y:      1,
	}, treeRowsStartY, 100)
	if !handled {
		t.Fatal("a click over the panel must be consumed")
	}
	if intent.openPath != "b.go" {
		t.Fatalf("clicking the second row must open b.go, got %+v", intent)
	}
	if e.treeCursor != 1 {
		t.Fatalf("clicking a row must select it: cursor=%d, want 1", e.treeCursor)
	}
}

func TestExplorer_LeftClickOnFolderTogglesWithoutIntent(t *testing.T) {
	e := loadedExplorer(t, []string{"internal/tui/model.go", "go.mod"})
	// Row 0 is the "internal" folder.
	e, intent, handled := e.handleMouse(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		Y:      0,
	}, treeRowsStartY, 100)
	if !handled {
		t.Fatal("a click over a folder row must be consumed")
	}
	if intent.openPath != "" || intent.closePanel {
		t.Fatalf("clicking a folder must not surface an intent, got %+v", intent)
	}
	if !e.tree.expanded["internal"] {
		t.Fatal("clicking a folder row must toggle its expansion")
	}
}

func TestExplorer_WheelMovesSelectionWithoutIntent(t *testing.T) {
	e := loadedExplorer(t, []string{"a.go", "b.go", "c.go", "d.go", "e.go"})
	e, intent, handled := e.handleMouse(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelDown,
	}, treeRowsStartY, 100)
	if !handled || intent.openPath != "" || intent.closePanel {
		t.Fatalf("wheel down must move selection silently: handled=%v intent=%+v", handled, intent)
	}
	if e.treeCursor != 3 {
		t.Fatalf("wheel down must move the cursor by three rows: cursor=%d", e.treeCursor)
	}
	e, _, _ = e.handleMouse(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelUp,
	}, treeRowsStartY, 100)
	if e.treeCursor != 0 {
		t.Fatalf("wheel up must move the cursor back by three rows: cursor=%d", e.treeCursor)
	}
}

func TestExplorer_ScrollOffsetTracksCursorInSmallWindow(t *testing.T) {
	e := loadedExplorer(t, []string{"a.go", "b.go", "c.go", "d.go", "e.go"})
	const window = 3

	// Move down to the last row through the small window; the offset must scroll
	// so the cursor stays visible but never past the end.
	for i := 0; i < 4; i++ {
		e, _, _ = e.handleKey(keyRunes('j'), window, nil)
	}
	if e.treeCursor != 4 {
		t.Fatalf("cursor at last row = %d, want 4", e.treeCursor)
	}
	// 5 rows, window 3 -> last page starts at offset 2.
	if e.treeOffset != 2 {
		t.Fatalf("offset must clamp to the last page: offset=%d, want 2", e.treeOffset)
	}

	// Move back to the top; the window follows up to offset 0.
	for i := 0; i < 4; i++ {
		e, _, _ = e.handleKey(keyRunes('k'), window, nil)
	}
	if e.treeCursor != 0 || e.treeOffset != 0 {
		t.Fatalf("returning to the top must reset the window: cursor=%d offset=%d", e.treeCursor, e.treeOffset)
	}
}

func TestExplorer_ApplyListedIgnoresStaleGeneration(t *testing.T) {
	e, cmd := explorer{}.open(func() ([]string, error) { return []string{"a.go"}, nil })
	if cmd == nil {
		t.Fatal("expected a load command")
	}
	fresh := cmd().(filesListedMsg)

	// A result from an earlier generation must be discarded.
	stale := filesListedMsg{target: fileListTree, generation: fresh.generation - 1, files: []string{"stale.go"}}
	after := e.applyListed(stale, 100)
	if after.treeLoaded || len(after.tree.visibleRows()) != 0 {
		t.Fatalf("stale result must be ignored: loaded=%v rows=%d", after.treeLoaded, len(after.tree.visibleRows()))
	}

	// The matching generation applies.
	after = e.applyListed(fresh, 100)
	if !after.treeLoaded {
		t.Fatal("matching generation must mark the tree loaded")
	}
	if got, want := rowPaths(after.tree.visibleRows()), []string{"a.go"}; !equalStrings(got, want) {
		t.Fatalf("applied rows = %v, want %v", got, want)
	}
}

func TestExplorer_ApplyListedErrorSurfacesAndKeepsPanelUnloaded(t *testing.T) {
	e, cmd := explorer{}.open(func() ([]string, error) { return nil, errors.New("glob failed") })
	msg := cmd().(filesListedMsg)
	after := e.applyListed(msg, 100)
	if after.treeError != "glob failed" {
		t.Fatalf("error listing must surface its message, got %q", after.treeError)
	}
	if after.treeLoaded {
		t.Fatal("a failed load must not mark the tree loaded")
	}
	if after.treeLoading {
		t.Fatal("a failed load must clear the loading flag")
	}
}

func TestExplorer_ApplyListedPreservesExpandedAndSelection(t *testing.T) {
	e := loadedExplorer(t, []string{"internal/tui/model.go", "go.mod"})
	// Expand "internal" and select "internal/tui".
	e, _, _ = e.handleKey(tea.KeyMsg{Type: tea.KeyEnter}, 100, nil)
	e, _, _ = e.handleKey(keyRunes('j'), 100, nil)
	if got := e.selectedPath(); got != "internal/tui" {
		t.Fatalf("precondition: selected %q, want internal/tui", got)
	}

	// A reload with the same paths must keep the expansion and the selection.
	reload := filesListedMsg{target: fileListTree, generation: e.treeGen, files: []string{"internal/tui/model.go", "go.mod"}}
	after := e.applyListed(reload, 100)
	if !after.tree.expanded["internal"] {
		t.Fatal("reload must preserve the expanded folders")
	}
	if got := after.selectedPath(); got != "internal/tui" {
		t.Fatalf("reload must preserve the selection, got %q", got)
	}
}

func TestExplorer_ReloadInvalidatesAndReschedules(t *testing.T) {
	e := loadedExplorer(t, []string{"a.go"})
	if !e.treeLoaded {
		t.Fatal("precondition: tree must be loaded")
	}
	genBefore := e.treeGen

	reloaded, cmd := e.reload(true, func() ([]string, error) { return []string{"a.go", "b.go"}, nil })
	if reloaded.treeLoaded {
		t.Fatal("reload must invalidate the loaded flag before the fresh load lands")
	}
	if !reloaded.treeLoading || reloaded.treeGen != genBefore+1 {
		t.Fatalf("reload(true) must start a fresh load: loading=%v gen=%d (was %d)", reloaded.treeLoading, reloaded.treeGen, genBefore)
	}
	if cmd == nil {
		t.Fatal("reload(true) with a listFiles func must return a load command")
	}

	// reload(false) invalidates without rescheduling.
	lazy, cmd := e.reload(false, func() ([]string, error) { return nil, nil })
	if lazy.treeLoaded || cmd != nil {
		t.Fatalf("reload(false) must invalidate without loading: loaded=%v cmd=%v", lazy.treeLoaded, cmd != nil)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
