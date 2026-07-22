package tui

// layout is the TUI's single source of terminal geometry. Given a terminal
// size and the panel state, computeLayout returns the rectangles, widths,
// heights and mouse-offset origins the rest of the package reads. It is a PURE
// function: no Model, no lipgloss, no strings — only size/inset/clamp/rect
// arithmetic. The View() render helpers read the returned Layout and only build
// strings; the Update-side sizing (resizeViewport) applies its values to the
// viewport and textarea; and the mouse hit-tests read the same origins, so
// rendering and click-targeting share ONE geometry and cannot drift.
//
// The clamps (explorer width 25% clamped to [20,36]), the split gutter, the
// top-bar chrome height and the full-screen thresholds live here, once. What
// this module deliberately does NOT own is rendering-flavored "narrow-terminal
// degradation": the reserved-line count (which depends on how many menu items
// and permission-panel rows a render decides to draw) arrives as an INPUT, and
// the git-summary/permission-panel progressive fallbacks stay in their render
// helpers, consuming a width/flag from here. See the discipline clause in the
// architecture doc.

// topBarMargin is the vertical margin (in rows) above and below the top bar.
// It is 1, not composerOuterMargin (the horizontal margin): one row is the
// project's vertical rhythm (transcript blocks separate with a blank line) and
// reads as the two horizontal margin cells, because a terminal cell is nearly
// twice as tall as wide. Two rows would also overflow short terminals (the
// composer does not fit under ~9 body rows).
const topBarMargin = 1

// topBarHeight is the total height of the top-bar chrome: the top vertical
// margin, the bar row and the bottom margin. bodyHeight subtracts it from the
// terminal height, and the mouse handler subtracts it from a click's row,
// because the body starts right below all that chrome.
const topBarHeight = 2*topBarMargin + 1

// treeRowsStartY is the screen row of the explorer panel's first file row.
// Without a box or title the panel's first row is body row 0; the mouse router
// passes this to explorer.rowAtMouse when mapping a click to a row. It rides on
// Layout as an origin so the render and the hit-test read the same value.
const treeRowsStartY = 0

// Explorer column clamps: the panel is width/4 (25%) clamped to [treeMinWidth,
// treeMaxWidth]. treeFallbackWidth is the width used before the first
// WindowSizeMsg (no size known yet), matching the old inline default.
const (
	treeMinWidth      = 20
	treeMaxWidth      = 36
	treeFallbackWidth = 28
	// splitGutter is the one-column gap between the explorer and the chat/viewer
	// when the tree is open. contentWidth subtracts it so the two columns and the
	// gutter add up to the terminal width.
	splitGutter = 1
)

// layoutSize is the announced terminal size. ready is false before the first
// WindowSizeMsg: with no size known the body falls back to the full render and
// the geometry degrades to sentinels (viewport not bounded, tree at its
// fallback width).
type layoutSize struct {
	width  int
	height int
	ready  bool
}

// layoutState is the panel state computeLayout needs beyond the raw size. It is
// the small, honest input: flags and counts the render/update already know,
// never the Model itself. reservedLines is the rendering-derived count of rows
// the composer box, the menu, the working line and the permission panel occupy
// below the transcript; it is an INPUT (not pure geometry — it depends on how
// many rows a render decides to draw) that computeLayout subtracts to bound the
// viewport. inputHeight is the textarea's current visible-row count, used to
// keep the input from being clamped below the space the reserved count already
// budgeted for it.
type layoutState struct {
	explorerOpen  bool
	reservedLines int
	inputHeight   int
}

// Layout is the computed geometry of one frame: the rectangles, widths, heights
// and mouse-offset origins derived from a terminal size and panel state. Every
// field is a pure function of computeLayout's inputs, so a table test can pin it
// without going through View().
type Layout struct {
	// ready mirrors the input: consumers that degrade without a size (the
	// full-render fallback) branch on it instead of re-checking m.ready.
	ready bool
	// width/height echo the terminal size, clamped to >= 0. Consumers that pad
	// the canvas read these instead of re-clamping m.width/m.height.
	width  int
	height int

	// topBarHeight is the chrome carved off the top; bodyHeight is what remains
	// for the body (chat/tree/viewer). bodyHeight measures against this, never
	// against the full terminal height.
	topBarHeight int
	bodyHeight   int
	// mouseBodyYOffset is the row count to subtract from a click's Y so a body
	// widget receives body-relative coordinates; a click in the chrome then maps
	// to a negative row, which the body handlers already treat as a miss. It is
	// exactly topBarHeight when ready — one origin shared by the render (which
	// prints the chrome first) and the hit-test.
	mouseBodyYOffset int

	// explorerOpen echoes the input; explorerFullScreen is true when the explorer
	// column is as wide as the whole terminal (too narrow to also show the chat/
	// viewer beside it), so the viewer goes full-screen and the tree owns the
	// body. treePanelWidth is the explorer column width; treeRowsStartY is the
	// screen row of its first file row; treeVisibleRows is how many file rows fit.
	explorerOpen       bool
	explorerFullScreen bool
	treePanelWidth     int
	treeRowsStartY     int
	treeVisibleRows    int

	// contentWidth is the width of the chat/viewer column: the full width with no
	// explorer, else the width left of the explorer column and the split gutter.
	// chatContentWidth is the same clamped to >= 0 (the render helpers' idiom).
	contentWidth     int
	chatContentWidth int

	// chatMargin is the horizontal outer margin of the chat column's boxes (the
	// composer box, the working line, the permission panel, the git-summary line):
	// composerOuterMargin cells, clamped to chatContentWidth/2 so a tiny terminal
	// never over-insets past its own width. chatInnerWidth is the width left
	// inside those margins. The render helpers read these so the composer, the
	// permission panel and the status line all inset by the same amount.
	chatMargin     int
	chatInnerWidth int

	// topBarMargin/topBarInnerWidth are the same clamp for the top bar, but based
	// on the FULL terminal width (the bar spans the whole width, above the
	// explorer split), so the bar aligns with the composer box only when the tree
	// is closed. topBarLine reads these instead of recomputing the clamp.
	topBarMarginCells int
	topBarInnerWidth  int

	// viewportWidth/viewportHeight bound the transcript viewport. The height is
	// bodyHeight minus the reserved rows, clamped to >= 0 so bubbles/viewport
	// never slices out of range under a tiny terminal.
	viewportWidth  int
	viewportHeight int
	// inputWidth is the composer textarea's visible content width: the chat width
	// minus the outer margins, the box border, the horizontal padding, the prompt
	// and the cursor cell, clamped to >= 1. inputHeight is the textarea's row
	// count clamped so it never exceeds the space the reserved count budgeted.
	inputWidth  int
	inputHeight int

	// fileViewerHeight is the viewer body height in either full-screen or the
	// split column: the body height minus its one header row.
	fileViewerHeight int
}

// computeLayout is the single geometry pass: a pure function of the terminal
// size and panel state that returns the frame's rectangles. Everything else in
// the package reads the result — the render helpers build strings from it, the
// resize applies its viewport/input dimensions, and the mouse hit-tests read its
// origins — so the layout is computed once and shared.
func computeLayout(size layoutSize, state layoutState) Layout {
	width := max(size.width, 0)
	height := max(size.height, 0)

	l := Layout{
		ready:          size.ready,
		width:          width,
		height:         height,
		topBarHeight:   topBarHeight,
		explorerOpen:   state.explorerOpen,
		treeRowsStartY: treeRowsStartY,
	}

	// The top bar is fixed chrome above the body; the body measures against what
	// is left. Before the first size the body has no bounded rect yet.
	l.bodyHeight = max(size.height-topBarHeight, 0)
	if size.ready {
		l.mouseBodyYOffset = topBarHeight
	}

	l.treePanelWidth = computeTreePanelWidth(size)
	if size.ready && state.explorerOpen && l.treePanelWidth >= size.width {
		l.explorerFullScreen = true
	}

	l.contentWidth = computeContentWidth(size, state, l.treePanelWidth)
	l.chatContentWidth = max(l.contentWidth, 0)
	// The chat column's boxes inset by composerOuterMargin cells, clamped to half
	// the column so a tiny terminal never over-insets past its own width.
	l.chatMargin = min(composerOuterMargin, l.chatContentWidth/2)
	l.chatInnerWidth = max(l.chatContentWidth-2*l.chatMargin, 0)
	// The top bar spans the full terminal width, so its outer margin clamps
	// against width, not the chat column.
	l.topBarMarginCells = min(composerOuterMargin, width/2)
	l.topBarInnerWidth = width - 2*l.topBarMarginCells

	// The tree fills the body vertically; each file row is one body row.
	if size.ready {
		l.treeVisibleRows = max(l.bodyHeight, 0)
	}

	// The viewer fills the body in both full-screen and the split column, and in
	// both reserves only its one header row.
	l.fileViewerHeight = max(l.bodyHeight-1, 0)

	// The transcript viewport spans the chat width and the body height minus the
	// rows reserved below it. Both clamp to >= 0 so a tiny terminal yields an
	// empty (not negative) rect.
	l.viewportWidth = max(l.contentWidth, 0)
	l.viewportHeight = max(l.bodyHeight-state.reservedLines, 0)

	// The textarea's visible width is the chat width stripped of the outer
	// margins, the box border, its two padding cells, the prompt and the cursor
	// cell bubbles reserves. Clamped to >= 1 for tiny terminals.
	l.inputWidth = max(l.chatContentWidth-2*composerOuterMargin-composerBoxBorderWidth-2*composerBoxPadding-inputCursorWidth, 1)
	// The reserved count already budgeted inputHeight rows below the transcript;
	// keep the textarea from growing past the body height once the rest of the
	// reserved chrome is accounted for. reservedLines-inputHeight is the reserved
	// space that is NOT the textarea, so bodyHeight minus that is the room left
	// for it.
	l.inputHeight = min(state.inputHeight, max(l.bodyHeight-(state.reservedLines-state.inputHeight), 1))

	return l
}

// computeTreePanelWidth is the explorer column width: 25% of the terminal
// clamped to [treeMinWidth, treeMaxWidth]. Before the first size it is the
// fallback width; when the clamped column plus its gutter would leave no room
// for the chat/viewer it takes the whole terminal (the explorerFullScreen case).
func computeTreePanelWidth(size layoutSize) int {
	if !size.ready || size.width <= 0 {
		return treeFallbackWidth
	}
	width := size.width / 4
	width = max(width, treeMinWidth)
	width = min(width, treeMaxWidth)
	if width+splitGutter >= size.width {
		return max(size.width, 0)
	}
	return width
}

// computeContentWidth is the chat/viewer column width: the full width when the
// explorer is closed (or no size yet), else the width left of the explorer
// column and the one-column split gutter.
func computeContentWidth(size layoutSize, state layoutState, treePanelWidth int) int {
	if !size.ready || !state.explorerOpen {
		return size.width
	}
	return max(size.width-treePanelWidth-splitGutter, 0)
}
