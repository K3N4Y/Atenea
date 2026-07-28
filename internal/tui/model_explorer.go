package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func keyRune(msg tea.KeyMsg) string {
	if msg.Type != tea.KeyRunes {
		return ""
	}
	return string(msg.Runes)
}

// toggleTreeAsync flips the explorer panel open/closed. Opening resets the
// cursor, takes explorer focus, and schedules the workspace load; closing hands
// focus back per normalizedFocus. Either way the chat/viewer column resizes to
// its new width while the transcript keeps its scroll position. The explorer
// owns its own open/close/load state (see explorer.go); the root owns focus and
// the viewport resize, which are Model-level concerns.
func (m Model) toggleTreeAsync() (Model, tea.Cmd) {
	m.leaderPending = false
	viewportOffset := m.viewport.YOffset
	if m.explorer.isOpen() {
		m.explorer = m.explorer.close()
		m.focus = m.normalizedFocus()
		m = m.resizeViewport()
		m.viewport.SetYOffset(viewportOffset)
		return m, nil
	}
	m.focus = explorerFocus
	var cmd tea.Cmd
	m.explorer, cmd = m.explorer.open(m.listFiles)
	m = m.resizeViewport()
	m.viewport.SetYOffset(viewportOffset)
	return m, cmd
}

// reloadTree invalidates the cached listing and optionally reloads now. Called
// after a tool edits the workspace so the open panel reflects the change.
func (m Model) reloadTree(loadNow bool) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.explorer, cmd = m.explorer.reload(loadNow, m.listFiles)
	return m, cmd
}

// syncComposerFocus derives which widget holds terminal focus from the shared
// precedence resolver (see input_router.go): the composer textarea owns focus
// iff the active input target is the composer AND the terminal itself is
// focused. Everything else blurs the composer. The resume picker keeps its
// own query-box focus as leaf behavior, since that widget lives inside the
// overlay rather than in the composer.
func (m *Model) syncComposerFocus() tea.Cmd {
	target := m.activeInputTarget()
	if target == targetComposer && m.terminalFocused {
		if !m.composer.focused() {
			return m.composer.focus()
		}
		return nil
	}
	m.composer.blur()
	if target == targetResumePicker {
		if m.terminalFocused {
			if !m.resumePicker.query.Focused() {
				return m.resumePicker.query.Focus()
			}
			return nil
		}
		m.resumePicker.query.Blur()
	}
	return nil
}

// handleTreeKey routes the keyboard to the explorer panel while it holds focus.
// The leader arming (Space, then Space+e) is composer-level state and stays
// here; everything else is delegated to explorer.handleKey, whose outward
// intent the root applies: an open-file path drives the (still-in-Model) file
// viewer via startOpenTreeFile (moving focus to it), and a close reuses the
// toggle's close path so the chat resizes back to full width.
func (m Model) handleTreeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.leaderPending {
		m.leaderPending = false
		if keyRune(msg) == "e" {
			return m.toggleTreeAsync()
		}
		return m, nil
	}
	if msg.Type == tea.KeySpace || keyRune(msg) == " " {
		return m.startLeader()
	}
	intent, cmd := m.explorerKey(msg)
	if intent.closePanel {
		return m.toggleTreeAsync()
	}
	if intent.openPath != "" {
		m, cmd = m.startOpenTreeFile(intent.openPath)
		m.focus = viewerFocus
	}
	return m, cmd
}

// explorerKey delegates a key to the embedded explorer with the current panel
// row capacity and shared listFiles, storing the updated sub-model back on the
// Model and returning its intent and command.
func (m *Model) explorerKey(msg tea.KeyMsg) (explorerIntent, tea.Cmd) {
	var (
		intent explorerIntent
		cmd    tea.Cmd
	)
	m.explorer, intent, cmd = m.explorer.handleKey(msg, m.treeVisibleRowCount(), m.listFiles)
	return intent, cmd
}

// handleTreeMouse routes a pointer event to the explorer panel. It gates on the
// pointer being over the panel (a Model-level layout query), then delegates to
// explorer.handleMouse and applies its intent: a left click on a file yields an
// open-file path that drives the viewer via startOpenTreeFile. It reports
// whether it consumed the event.
func (m *Model) handleTreeMouse(msg tea.MouseMsg) (bool, tea.Cmd) {
	if !m.treeMouseOverPanel(msg) {
		return false, nil
	}
	if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
		m.focus = explorerFocus
	}
	var intent explorerIntent
	m.explorer, intent, _ = m.explorer.handleMouse(msg, treeRowsStartY, m.treeVisibleRowCount())
	if intent.openPath != "" {
		var cmd tea.Cmd
		*m, cmd = m.startOpenTreeFile(intent.openPath)
		return true, cmd
	}
	return true, nil
}

func (m Model) normalizedFocus() panelFocus {
	if m.treeOpen && m.ready && m.treePanelWidth() >= m.width {
		return explorerFocus
	}
	if m.focus == explorerFocus && !m.treeOpen {
		return chatFocus
	}
	if m.focus == viewerFocus && !m.viewer.active() {
		return chatFocus
	}
	return m.focus
}

func (m Model) focusAtMouse(msg tea.MouseMsg) panelFocus {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return m.normalizedFocus()
	}
	if m.viewer.active() && msg.Y >= 0 && msg.Y < m.fileViewerHeight() {
		return viewerFocus
	}
	return chatFocus
}

func (m Model) transcriptLineAtMouse(msg tea.MouseMsg) (int, bool) {
	if !m.ready || m.viewer.active() || msg.Y < 0 {
		return 0, false
	}
	y := msg.Y
	if m.chatPanelVisible() {
		if msg.X < m.treePanelWidth()+1 {
			return 0, false
		}
	}
	if y < 0 || y >= m.viewport.Height {
		return 0, false
	}
	return m.viewport.YOffset + y, true
}

// layoutSize projects the announced terminal size into the layout module's
// input. It is the raw size seam: the ready flag decides whether the geometry is
// bounded or degrades to sentinels.
func (m Model) layoutSize() layoutSize {
	return layoutSize{width: m.width, height: m.height, ready: m.ready}
}

// layout is the Model's single geometry pass: it gathers the panel state
// (explorer open, the reserved-line count below the transcript, the textarea's
// current row count) and hands it to computeLayout, which returns the frame's
// rectangles. Every geometry query method below is a thin seam onto a field of
// this result, so the render, the resize and the mouse hit-tests read ONE
// geometry. reservedLines is rendering-derived (it counts how many menu and
// permission-panel rows a render draws), so it is passed in as state rather than
// recomputed inside the pure module.
func (m Model) layout() Layout {
	return computeLayout(m.layoutSize(), layoutState{
		explorerOpen:  m.treeOpen,
		reservedLines: m.reservedLines(),
		inputHeight:   m.input.Height(),
	})
}

// baseLayout computes the geometry that does NOT depend on the reserved-line
// count: the body height, the explorer/chat widths, the viewer height and the
// mouse origins. reservedLines is itself derived from bodyHeight and
// chatContentWidth (the permission panel sizes against them), so those methods
// must read a layout that does not, in turn, ask for reservedLines — that would
// recurse. computeLayout is pure, and its reserved-independent fields are the
// same whatever reservedLines is, so passing 0 here is exact for every field
// those methods read; only viewportHeight and inputHeight (which layout()
// supplies with the real count) differ.
func (m Model) baseLayout() Layout {
	return computeLayout(m.layoutSize(), layoutState{explorerOpen: m.treeOpen})
}

func (m Model) treeMouseOverPanel(msg tea.MouseMsg) bool {
	return m.ready && m.treeVisible() && msg.X >= 0 && msg.X < m.treePanelWidth()
}

func (m Model) treeVisible() bool {
	return m.treeOpen && (!m.viewer.active() || m.treePanelWidth() < m.width)
}

func (m Model) fileViewerHeight() int {
	return m.baseLayout().fileViewerHeight
}

// startOpenTreeFile drives the viewer's open from the explorer's openPath
// intent. It stays a thin Model-level seam (exercised directly by the behavior
// tests) that hands the current transcript scroll offset to the panel so the
// panel can restore it on close, then stores the updated sub-model back on the
// Model. The caller (handleTreeKey) owns the focus flip to viewerFocus.
func (m Model) startOpenTreeFile(path string) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.fileViewerPanel, cmd = m.fileViewerPanel.open(path, m.viewport.YOffset)
	return m, cmd
}

func listFilesCmd(listFiles func() ([]string, error), target fileListTarget, generation uint64) tea.Cmd {
	return func() tea.Msg {
		files, err := listFiles()
		return filesListedMsg{target: target, generation: generation, files: files, err: err}
	}
}

// viewerKey routes the keyboard to the file viewer panel while it holds focus,
// then applies the panel's outward intent: a closeToChat asks the root to
// return focus to the chat and restore the transcript scroll offset the panel
// captured at open (its returnY). Scrolling stays inside the panel and yields an
// empty intent. Focus and the viewport are Model-level concerns, so they stay
// here rather than in the panel.
func (m Model) viewerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var intent viewerIntent
	m.fileViewerPanel, intent = m.fileViewerPanel.handleKey(msg, m.fileViewerHeight())
	if intent.closeToChat {
		m.viewport.SetYOffset(m.fileViewerPanel.returnY())
		m.focus = chatFocus
	}
	return m, nil
}

// selectedTreePath is a thin Model-level seam onto the embedded explorer's
// selected row, kept because it is exercised directly by the behavior tests.
func (m Model) selectedTreePath() string { return m.explorer.selectedPath() }

func pathParent(nodePath string) string {
	if index := strings.LastIndex(nodePath, "/"); index >= 0 {
		return nodePath[:index]
	}
	return ""
}
