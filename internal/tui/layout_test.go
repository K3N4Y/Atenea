package tui

import "testing"

// TestComputeLayout_TopBarChromeAndBodyHeight pins the fixed top-bar chrome and
// the body sizing against it across terminal heights, including the survival
// cases (0x0, 1-line) the PTY smoke tests exercise: every dimension bounds to
// >= 0 so bubbles/viewport never slices out of range.
func TestComputeLayout_TopBarChromeAndBodyHeight(t *testing.T) {
	tests := []struct {
		name             string
		size             layoutSize
		wantTopBarHeight int
		wantBodyHeight   int
		wantMouseYOffset int
	}{
		{
			name:             "wide terminal",
			size:             layoutSize{width: 100, height: 40, ready: true},
			wantTopBarHeight: 3,
			wantBodyHeight:   37, // 40 - topBarHeight(3)
			wantMouseYOffset: 3,
		},
		{
			name:             "pty default 100x24",
			size:             layoutSize{width: 100, height: 24, ready: true},
			wantTopBarHeight: 3,
			wantBodyHeight:   21, // 24 - 3
			wantMouseYOffset: 3,
		},
		{
			name:             "pty 100x11 leaves 8 body rows",
			size:             layoutSize{width: 100, height: 11, ready: true},
			wantTopBarHeight: 3,
			wantBodyHeight:   8,
			wantMouseYOffset: 3,
		},
		{
			name:             "one-line terminal bounds to zero body",
			size:             layoutSize{width: 20, height: 1, ready: true},
			wantTopBarHeight: 3,
			wantBodyHeight:   0, // max(1-3, 0)
			wantMouseYOffset: 3,
		},
		{
			name:             "zero-by-zero survives",
			size:             layoutSize{width: 0, height: 0, ready: true},
			wantTopBarHeight: 3,
			wantBodyHeight:   0,
			wantMouseYOffset: 3,
		},
		{
			name:             "not ready has no mouse offset",
			size:             layoutSize{width: 80, height: 24, ready: false},
			wantTopBarHeight: 3,
			wantBodyHeight:   21,
			wantMouseYOffset: 0, // no chrome drawn yet, so no click subtraction
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := computeLayout(tc.size, layoutState{})
			if l.topBarHeight != tc.wantTopBarHeight {
				t.Errorf("topBarHeight = %d, want %d", l.topBarHeight, tc.wantTopBarHeight)
			}
			if l.bodyHeight != tc.wantBodyHeight {
				t.Errorf("bodyHeight = %d, want %d", l.bodyHeight, tc.wantBodyHeight)
			}
			if l.mouseBodyYOffset != tc.wantMouseYOffset {
				t.Errorf("mouseBodyYOffset = %d, want %d", l.mouseBodyYOffset, tc.wantMouseYOffset)
			}
		})
	}
}

// TestComputeLayout_ContentUsesFullWidth pins that the simplified TUI has one
// chat surface: content uses the terminal width directly.
func TestComputeLayout_ContentUsesFullWidth(t *testing.T) {
	tests := []struct {
		name             string
		size             layoutSize
		wantContentWidth int
	}{
		{
			name:             "ready terminal",
			size:             layoutSize{width: 100, height: 40, ready: true},
			wantContentWidth: 100,
		},
		{
			name:             "not ready terminal",
			size:             layoutSize{width: 80, height: 24, ready: false},
			wantContentWidth: 80,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := computeLayout(tc.size, layoutState{})
			if l.contentWidth != tc.wantContentWidth {
				t.Errorf("contentWidth = %d, want %d", l.contentWidth, tc.wantContentWidth)
			}
		})
	}
}

// TestComputeLayout_ViewportBoundingAgainstReserved pins the viewport height
// (bodyHeight minus the reserved rows, clamped >= 0) and the input-height clamp
// against the reserved budget, including the tiny-terminal case where the
// reserved chrome exceeds the body and the viewport bounds to zero.
func TestComputeLayout_ViewportBoundingAgainstReserved(t *testing.T) {
	tests := []struct {
		name               string
		size               layoutSize
		reservedLines      int
		inputHeight        int
		wantViewportHeight int
		wantInputHeight    int
	}{
		{
			name:               "roomy: viewport takes the rest",
			size:               layoutSize{width: 80, height: 40, ready: true},
			reservedLines:      4, // 1-line composer(3) + margin, say
			inputHeight:        1,
			wantViewportHeight: 33, // bodyHeight(37) - 4
			wantInputHeight:    1,  // min(1, max(37-(4-1),1)) = min(1,34)
		},
		{
			name:               "grown composer: 5 visible lines",
			size:               layoutSize{width: 80, height: 40, ready: true},
			reservedLines:      9, // 5-line input + 2 border + 2 margin
			inputHeight:        5,
			wantViewportHeight: 28, // 37 - 9
			wantInputHeight:    5,  // min(5, max(37-(9-5),1)) = min(5,33)
		},
		{
			name:               "tiny terminal: reserved exceeds body",
			size:               layoutSize{width: 40, height: 6, ready: true},
			reservedLines:      9,
			inputHeight:        1,
			wantViewportHeight: 0, // max(bodyHeight(3) - 9, 0)
			wantInputHeight:    1, // min(1, max(3-(9-1),1)) = min(1, max(-5,1)) = 1
		},
		{
			name:               "input clamped down under short body",
			size:               layoutSize{width: 40, height: 10, ready: true},
			reservedLines:      9,
			inputHeight:        5,
			wantViewportHeight: 0, // max(bodyHeight(7) - 9, 0)
			wantInputHeight:    3, // min(5, max(7-(9-5),1)) = min(5, max(3,1)) = 3
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := computeLayout(tc.size, layoutState{reservedLines: tc.reservedLines, inputHeight: tc.inputHeight})
			if l.viewportHeight != tc.wantViewportHeight {
				t.Errorf("viewportHeight = %d, want %d", l.viewportHeight, tc.wantViewportHeight)
			}
			if l.inputHeight != tc.wantInputHeight {
				t.Errorf("inputHeight = %d, want %d", l.inputHeight, tc.wantInputHeight)
			}
		})
	}
}

// TestComputeLayout_InputWidthAndChatMargin pins the composer textarea width
// (chat width stripped of the outer margins, box border, padding, prompt and
// cursor cell, clamped >= 1) and the shared chat-column margin/inner width that
// the composer box, permission panel and working line all inset by.
func TestComputeLayout_InputWidthAndChatMargin(t *testing.T) {
	tests := []struct {
		name               string
		size               layoutSize
		wantChatMargin     int
		wantChatInnerWidth int
		wantInputWidth     int
	}{
		{
			name:               "wide terminal",
			size:               layoutSize{width: 80, height: 24, ready: true},
			wantChatMargin:     2,  // composerOuterMargin, chatContentWidth/2 large
			wantChatInnerWidth: 76, // 80 - 2*2
			// 80 - 2*2(outer) - 2(border) - 2*1(padding) - 1(cursor) = 71
			wantInputWidth: 71,
		},
		{
			name:               "narrow terminal clamps margin",
			size:               layoutSize{width: 3, height: 24, ready: true},
			wantChatMargin:     1, // min(2, 3/2=1)
			wantChatInnerWidth: 1, // 3 - 2*1
			wantInputWidth:     1, // clamped to >= 1
		},
		{
			name:               "zero width",
			size:               layoutSize{width: 0, height: 24, ready: true},
			wantChatMargin:     0, // min(2, 0)
			wantChatInnerWidth: 0,
			wantInputWidth:     1, // clamped to >= 1
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := computeLayout(tc.size, layoutState{inputHeight: 1})
			if l.chatMargin != tc.wantChatMargin {
				t.Errorf("chatMargin = %d, want %d", l.chatMargin, tc.wantChatMargin)
			}
			if l.chatInnerWidth != tc.wantChatInnerWidth {
				t.Errorf("chatInnerWidth = %d, want %d", l.chatInnerWidth, tc.wantChatInnerWidth)
			}
			if l.inputWidth != tc.wantInputWidth {
				t.Errorf("inputWidth = %d, want %d", l.inputWidth, tc.wantInputWidth)
			}
		})
	}
}

// TestComputeLayout_TopBarMarginSpansFullWidth pins that the top-bar margin
// clamps against the full terminal width, matching the single chat column.
func TestComputeLayout_TopBarMarginSpansFullWidth(t *testing.T) {
	l := computeLayout(layoutSize{width: 120, height: 40, ready: true}, layoutState{})
	if l.topBarMarginCells != 2 {
		t.Errorf("topBarMarginCells = %d, want 2", l.topBarMarginCells)
	}
	if l.topBarInnerWidth != 116 { // 120 - 2*2
		t.Errorf("topBarInnerWidth = %d, want 116", l.topBarInnerWidth)
	}
	if l.chatInnerWidth != 116 {
		t.Errorf("chatInnerWidth = %d, want 116", l.chatInnerWidth)
	}

	// Tiny terminal: the top-bar margin clamps to width/2 so the bar never
	// over-insets past its own width.
	narrow := computeLayout(layoutSize{width: 3, height: 12, ready: true}, layoutState{})
	if narrow.topBarMarginCells != 1 { // min(2, 3/2)
		t.Errorf("narrow topBarMarginCells = %d, want 1", narrow.topBarMarginCells)
	}
	if narrow.topBarInnerWidth != 1 { // 3 - 2*1
		t.Errorf("narrow topBarInnerWidth = %d, want 1", narrow.topBarInnerWidth)
	}
}

// TestComputeLayout_NegativeSizeClampsToZero pins that a negative announced size
// (never expected from a real WindowSizeMsg, but defensive) keeps every APPLIED
// dimension bounded >= 0: the width/height echoes, the body height and the
// clamped chat/viewport widths and the viewport height.
func TestComputeLayout_NegativeSizeClampsToZero(t *testing.T) {
	l := computeLayout(layoutSize{width: -5, height: -5, ready: true}, layoutState{inputHeight: 1})
	if l.width != 0 || l.height != 0 {
		t.Fatalf("width/height = %d/%d, want 0/0", l.width, l.height)
	}
	if l.bodyHeight != 0 {
		t.Errorf("bodyHeight = %d, want 0", l.bodyHeight)
	}
	if l.chatContentWidth < 0 || l.viewportWidth < 0 || l.viewportHeight < 0 {
		t.Errorf("applied rect went negative: chat=%d vp=%dx%d", l.chatContentWidth, l.viewportWidth, l.viewportHeight)
	}
	if l.chatMargin < 0 || l.chatInnerWidth < 0 || l.inputWidth < 1 {
		t.Errorf("applied dimension out of bounds: margin=%d inner=%d input=%d",
			l.chatMargin, l.chatInnerWidth, l.inputWidth)
	}
}
