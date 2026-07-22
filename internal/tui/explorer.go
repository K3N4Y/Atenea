package tui

// The explorer module owns the workspace file tree panel: the left-column tree
// the user opens with the `Space e` leader, navigates with the keyboard, and
// clicks with the mouse. It is the first Model panel extracted into a
// self-contained sub-model behind the input router (see input_router.go): the
// root Model embeds one explorer, routes keyboard input to it when the active
// target is targetExplorer, routes pointer events when the pointer is over the
// panel, and asks it for its column in the split layout.
//
// The module owns everything about the tree EXCEPT two things it must not reach
// into: the file viewer and the shared workspace listing.
//
//   - The viewer stays on Model. Activating a file row does not open a viewer
//     here; it surfaces an intent (explorerIntent{openPath: P}) that the root
//     handles by driving its still-in-Model viewer. Closing the panel surfaces
//     explorerIntent{close: true} so the root can resize the chat back to full
//     width. This keeps the coupling explicit and one-directional.
//   - The workspace file listing (listFiles) is shared with the composer's "@"
//     autocomplete. The explorer does not own it: load lifecycle methods take
//     the shared listFiles func and return the shared listFilesCmd targeting
//     fileListTree, so the same listing feeds both without either panel owning
//     the other's cache.
//
// Model embeds explorer anonymously, so the tree state fields below (treeOpen,
// tree, treeCursor, …) promote onto Model unchanged — the same idiom the
// Transcript module and the overlay pickers use. Field names are preserved so
// the Model-level layout helpers and the existing behavior tests keep reading
// them as the Model's own.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// explorer is the workspace file tree panel sub-model. It holds exactly the
// tree-specific state that used to live scattered on Model plus the behavior
// over it. Value-in / value-out: mutating methods take a value receiver and
// return the updated explorer, mirroring the package idiom.
type explorer struct {
	// treeOpen reports whether the panel is open. The input router and the
	// layout read it (promoted onto Model as m.treeOpen) to decide focus and the
	// split-vs-full column arrangement.
	treeOpen bool
	// treeLoaded / treeLoading gate the async workspace listing: loaded caches a
	// finished load for the panel's lifetime, loading marks an in-flight request.
	treeLoaded  bool
	treeLoading bool
	// tree is the built file tree (roots + expanded set). Its type and pure
	// logic live in tree.go; the explorer wraps it with the panel's cursor,
	// scroll offset, and load state.
	tree fileTree
	// treeCursor is the selected visible row; treeOffset is the first visible row
	// (the scroll window top). Both are kept in range by syncViewport/clamp.
	treeCursor int
	treeOffset int
	// treeError carries the last listing error, rendered in place of the tree.
	treeError string
	// treeGen guards async load results: a stale filesListedMsg (generation !=
	// treeGen) is ignored. Bumped on every new or cancelled load.
	treeGen uint64
}

// explorerIntent is what a key or mouse handler asks the root Model to do on
// the explorer's behalf, keeping the explorer from reaching into viewer state.
// At most one of the outcomes is set; the zero value means "nothing outward,
// the returned explorer already reflects the change".
type explorerIntent struct {
	// openPath, when non-empty, asks the root to open (or replace) the file
	// viewer on that workspace-relative path.
	openPath string
	// closePanel asks the root to run its close path (toggle shut), which also
	// resizes the chat back to full width.
	closePanel bool
}

// isOpen reports whether the panel is open. Callers that hold an explorer value
// (the router, the layout) use this instead of touching the field directly.
func (e explorer) isOpen() bool { return e.treeOpen }

// open marks the panel open and resets the cursor/scroll to the top, then
// schedules the workspace load via listFiles (shared with the autocomplete).
// It returns the load command; the caller owns focus and viewport resizing,
// which are Model-level concerns.
func (e explorer) open(listFiles func() ([]string, error)) (explorer, tea.Cmd) {
	e.treeOpen = true
	e.treeCursor = 0
	e.treeOffset = 0
	return e.startLoad(listFiles)
}

// close marks the panel closed. Focus and viewport resizing stay with the root.
func (e explorer) close() explorer {
	e.treeOpen = false
	return e
}

// startLoad schedules the workspace listing the first time the panel needs it.
// A finished or in-flight load is a no-op. With no listFiles configured the
// tree loads empty synchronously. Otherwise it bumps the generation and returns
// the shared listFilesCmd targeting fileListTree.
func (e explorer) startLoad(listFiles func() ([]string, error)) (explorer, tea.Cmd) {
	if e.treeLoaded || e.treeLoading {
		return e, nil
	}
	e.treeError = ""
	if listFiles == nil {
		e.tree = newFileTree(nil)
		e.treeLoaded = true
		return e, nil
	}
	e.treeLoading = true
	e.treeGen++
	return e, listFilesCmd(listFiles, fileListTree, e.treeGen)
}

// reload invalidates the cached listing and, when loadNow is set, kicks off a
// fresh load. An in-flight load is cancelled (its result will be discarded by
// the generation guard) so the reload always wins. The workspace can change
// between loads (a tool edited a file), so callers reload after such events.
func (e explorer) reload(loadNow bool, listFiles func() ([]string, error)) (explorer, tea.Cmd) {
	e.treeLoaded = false
	if e.treeLoading {
		e.treeLoading = false
		e.treeGen++
	}
	if !loadNow {
		return e, nil
	}
	return e.startLoad(listFiles)
}

// applyListed folds an async listing result into the tree, guarded by the
// generation so a stale result is ignored. It rebuilds the tree from the new
// paths, preserves the previously expanded folders and the selected path, and
// re-windows the scroll offset. visibleRows is the panel's current row capacity
// (a Model-level layout quantity passed in).
func (e explorer) applyListed(msg filesListedMsg, visibleRows int) explorer {
	if msg.generation != e.treeGen {
		return e
	}
	e.treeLoading = false
	selectedPath := e.selectedPath()
	expanded := e.tree.expanded
	e.tree = newFileTree(msg.files)
	for nodePath := range expanded {
		e.tree.expanded[nodePath] = true
	}
	e.treeError = ""
	if msg.err != nil {
		e.tree = newFileTree(nil)
		e.treeError = msg.err.Error()
		return e
	}
	e.treeLoaded = true
	e = e.selectPath(selectedPath, visibleRows)
	e = e.syncViewport(visibleRows)
	return e
}

// handleKey processes the keyboard while the explorer holds focus. It returns
// the updated explorer and an intent for the root: activating a file row yields
// openPath, Esc/q yields closePanel. Navigation, expand/collapse and reload are
// handled internally and yield an empty intent. leaderKey is true when the key
// is Space or the leader-arm follow-up is Space+e (both handled by the root
// before this is called), so the explorer never sees the leader itself.
//
// visibleRows is the panel's current row capacity; reload uses listFiles.
func (e explorer) handleKey(msg tea.KeyMsg, visibleRows int, listFiles func() ([]string, error)) (explorer, explorerIntent, tea.Cmd) {
	rows := e.tree.visibleRows()
	switch {
	case msg.Type == tea.KeyEsc || keyRune(msg) == "q":
		return e, explorerIntent{closePanel: true}, nil
	case msg.Type == tea.KeyDown || keyRune(msg) == "j":
		if e.treeCursor < len(rows)-1 {
			e.treeCursor++
		}
	case msg.Type == tea.KeyUp || keyRune(msg) == "k":
		if e.treeCursor > 0 {
			e.treeCursor--
		}
	case keyRune(msg) == "r":
		e, cmd := e.reload(true, listFiles)
		return e, explorerIntent{}, cmd
	case msg.Type == tea.KeyEnter || keyRune(msg) == "l":
		if len(rows) == 0 {
			break
		}
		node := rows[e.treeCursor].node
		if node.dir {
			e.tree.toggle(node.path)
			e = e.clampCursor(visibleRows)
			break
		}
		e = e.syncViewport(visibleRows)
		return e, explorerIntent{openPath: node.path}, nil
	case keyRune(msg) == "h":
		if len(rows) == 0 {
			break
		}
		node := rows[e.treeCursor].node
		if node.dir && e.tree.expanded[node.path] {
			e.tree.toggle(node.path)
			e = e.clampCursor(visibleRows)
			break
		}
		parent := pathParent(node.path)
		for i, row := range rows {
			if row.node.path == parent {
				e.treeCursor = i
				break
			}
		}
	}
	e = e.syncViewport(visibleRows)
	return e, explorerIntent{}, nil
}

// handleMouse processes a pointer event known to fall over the panel (the root
// gates on overPanel before calling). The wheel moves the selection by three
// rows so the visual focus tracks the keyboard; a left press selects the row
// under the pointer and activates it exactly like Enter — a folder toggles, a
// file yields an openPath intent. rowStartY is the panel's first body row on
// screen (a layout quantity passed in). It reports whether it consumed the
// event.
func (e explorer) handleMouse(msg tea.MouseMsg, rowStartY, visibleRows int) (explorer, explorerIntent, bool) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if msg.Action != tea.MouseActionPress {
			return e, explorerIntent{}, true
		}
		e = e.moveCursor(-3, visibleRows)
		return e, explorerIntent{}, true
	case tea.MouseButtonWheelDown:
		if msg.Action != tea.MouseActionPress {
			return e, explorerIntent{}, true
		}
		e = e.moveCursor(3, visibleRows)
		return e, explorerIntent{}, true
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return e, explorerIntent{}, true
		}
		row, ok := e.rowAtMouse(msg.Y, rowStartY, visibleRows)
		if !ok {
			return e, explorerIntent{}, true
		}
		e.treeCursor = row
		node := e.tree.visibleRows()[row].node
		if node.dir {
			e.tree.toggle(node.path)
			e = e.clampCursor(visibleRows)
			return e, explorerIntent{}, true
		}
		return e, explorerIntent{openPath: node.path}, true
	}
	return e, explorerIntent{}, true
}

// moveCursor shifts the selection by delta rows, clamped to the visible rows,
// and re-windows the scroll offset.
func (e explorer) moveCursor(delta, visibleRows int) explorer {
	rows := e.tree.visibleRows()
	if len(rows) == 0 {
		return e
	}
	e.treeCursor = min(max(e.treeCursor+delta, 0), len(rows)-1)
	return e.syncViewport(visibleRows)
}

// clampCursor pulls the cursor back into range (a toggle can shrink the visible
// rows below the cursor) and re-windows.
func (e explorer) clampCursor(visibleRows int) explorer {
	rows := e.tree.visibleRows()
	if len(rows) == 0 {
		e.treeCursor = 0
	} else if e.treeCursor >= len(rows) {
		e.treeCursor = len(rows) - 1
	}
	return e.syncViewport(visibleRows)
}

// selectedPath returns the workspace-relative path of the selected row, or ""
// when the cursor is out of range (empty tree).
func (e explorer) selectedPath() string {
	rows := e.tree.visibleRows()
	if e.treeCursor < 0 || e.treeCursor >= len(rows) {
		return ""
	}
	return rows[e.treeCursor].node.path
}

// selectPath moves the cursor to the row matching nodePath, keeping the same
// selection across a reload; when the path is gone (or empty) it just clamps.
func (e explorer) selectPath(nodePath string, visibleRows int) explorer {
	if nodePath == "" {
		return e.clampCursor(visibleRows)
	}
	for i, row := range e.tree.visibleRows() {
		if row.node.path == nodePath {
			e.treeCursor = i
			return e
		}
	}
	return e.clampCursor(visibleRows)
}

// syncViewport keeps the scroll window (treeOffset) around the cursor: it
// scrolls up when the cursor is above the window, down when below, and clamps
// the offset so the last page never scrolls past the end. visibleRows is the
// panel's row capacity; a zero capacity (unknown terminal size) pins the offset.
func (e explorer) syncViewport(visibleRows int) explorer {
	rows := e.tree.visibleRows()
	if len(rows) == 0 || visibleRows == 0 {
		e.treeOffset = 0
		return e
	}
	if e.treeCursor < e.treeOffset {
		e.treeOffset = e.treeCursor
	}
	if e.treeCursor >= e.treeOffset+visibleRows {
		e.treeOffset = e.treeCursor - visibleRows + 1
	}
	e.treeOffset = min(e.treeOffset, max(len(rows)-visibleRows, 0))
	return e
}

// rowAtMouse maps a body Y coordinate to a visible row index, returning false
// when the click falls above the first row or below the last visible one.
// rowStartY is the panel's first body row on screen (0 in the current
// borderless layout); visibleRows is the row capacity.
func (e explorer) rowAtMouse(y, rowStartY, visibleRows int) (int, bool) {
	if y < rowStartY {
		return 0, false
	}
	row := e.treeOffset + y - rowStartY
	rows := e.tree.visibleRows()
	if row < 0 || row >= len(rows) || row >= e.treeOffset+visibleRows {
		return 0, false
	}
	return row, true
}

// view renders the panel column. It shows the loading placeholder while a load
// is in flight, the error line when the last load failed, an empty-workspace
// hint when there are no rows, or the windowed rows (icons + names, the cursor
// row reversed) otherwise. innerWidth is the panel column width, bodyHeight the
// column height, visibleRows the row capacity, and ready reports whether the
// terminal size is known (only then is the column sized to fill its box).
func (e explorer) view(innerWidth, bodyHeight, visibleRows int, ready bool) string {
	width := max(innerWidth, 0)
	var lines []string
	if e.treeLoading {
		lines = append(lines, statusStyle.Render("cargando workspace…"))
	} else if e.treeError != "" {
		lines = append(lines, statusStyle.Render(sanitizeTerminalText(e.treeError)))
	} else {
		rows := e.tree.visibleRows()
		if len(rows) == 0 {
			lines = append(lines, statusStyle.Render("workspace vacio"))
		}
		start := min(e.treeOffset, len(rows))
		end := len(rows)
		if visibleRows > 0 {
			end = min(start+visibleRows, len(rows))
		}
		for i := start; i < end; i++ {
			row := rows[i]
			icon := iconForFile(row.node.name)
			if row.node.dir {
				icon = iconFolderClosed
				if e.tree.expanded[row.node.path] {
					icon = iconFolderOpen
				}
			}
			line := strings.Repeat("  ", row.depth) + icon + " " + sanitizeTerminalText(row.node.name)
			if width > 0 {
				line = ansi.Truncate(line, width, "…")
			}
			if i == e.treeCursor {
				line = treeCursorStyle.Render(line)
			}
			lines = append(lines, line)
		}
	}
	content := strings.Join(lines, "\n")
	style := treePanelStyle
	if ready {
		style = style.Width(width).Height(max(bodyHeight, 0))
	}
	return style.Render(content)
}
