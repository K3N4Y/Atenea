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
// The top-bar chrome height and body/composer bounds live here, once. What
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

	// contentWidth is the width of the chat column; chatContentWidth is the same
	// clamped to >= 0 (the render helpers' idiom).
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

	// topBarMargin/topBarInnerWidth are the same clamp for the top bar.
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
		ready:        size.ready,
		width:        width,
		height:       height,
		topBarHeight: topBarHeight,
	}

	// The top bar is fixed chrome above the body; the body measures against what
	// is left. Before the first size the body has no bounded rect yet.
	l.bodyHeight = max(size.height-topBarHeight, 0)
	if size.ready {
		l.mouseBodyYOffset = topBarHeight
	}

	l.contentWidth = size.width
	l.chatContentWidth = max(l.contentWidth, 0)
	// The chat column's boxes inset by composerOuterMargin cells, clamped to half
	// the column so a tiny terminal never over-insets past its own width.
	l.chatMargin = min(composerOuterMargin, l.chatContentWidth/2)
	l.chatInnerWidth = max(l.chatContentWidth-2*l.chatMargin, 0)
	// The top bar spans the full terminal width, so its outer margin clamps
	// against width, not the chat column.
	l.topBarMarginCells = min(composerOuterMargin, width/2)
	l.topBarInnerWidth = width - 2*l.topBarMarginCells

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
