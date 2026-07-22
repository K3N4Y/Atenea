package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestOverlayList_MoveWrapsAtBothEnds(t *testing.T) {
	var list overlayList
	list.setCount(3)

	list.move(-1)
	if list.selected != 2 {
		t.Fatalf("selected after moving up from 0 = %d, want 2", list.selected)
	}
	list.move(1)
	if list.selected != 0 {
		t.Fatalf("selected after moving down from 2 = %d, want 0", list.selected)
	}
	list.move(4)
	if list.selected != 1 {
		t.Fatalf("selected after moving four = %d, want 1", list.selected)
	}
}

func TestOverlayList_MoveOnEmptyListIsInert(t *testing.T) {
	var list overlayList
	list.move(1)
	list.move(-1)
	if _, ok := list.hasSelection(); ok {
		t.Fatal("empty list reports a selection")
	}
	if list.selected != 0 {
		t.Fatalf("selected = %d, want 0 on an empty list", list.selected)
	}
}

func TestOverlayList_SetCountClampsCursorIntoRange(t *testing.T) {
	var list overlayList
	list.setCount(5)
	list.selected = 4

	list.setCount(2)
	if list.selected != 0 {
		t.Fatalf("selected = %d, want 0 after shrinking below it", list.selected)
	}

	list.selected = 1
	list.setCount(3)
	if list.selected != 1 {
		t.Fatalf("selected = %d, want 1 preserved when still in range", list.selected)
	}

	list.setCount(0)
	if list.selected != 0 {
		t.Fatalf("selected = %d, want 0 on an emptied list", list.selected)
	}
}

func TestOverlayList_SelectedGuardsRange(t *testing.T) {
	var list overlayList
	list.setCount(2)

	index, ok := list.hasSelection()
	if !ok || index != 0 {
		t.Fatalf("hasSelection = %d, %v; want 0, true", index, ok)
	}

	list.selected = 9
	if _, ok := list.hasSelection(); ok {
		t.Fatal("out-of-range cursor must report no selection")
	}
}

func TestOverlayList_WindowKeepsCursorCenteredAndBounded(t *testing.T) {
	tests := []struct {
		name    string
		total   int
		cursor  int
		visible int
		want    [2]int
	}{
		{name: "fits without scrolling", total: 3, cursor: 2, visible: 5, want: [2]int{0, 3}},
		{name: "zero visible", total: 10, cursor: 4, visible: 0, want: [2]int{0, 10}},
		{name: "centers mid-list", total: 10, cursor: 5, visible: 4, want: [2]int{3, 7}},
		{name: "clamps at the top", total: 10, cursor: 0, visible: 4, want: [2]int{0, 4}},
		{name: "clamps at the bottom", total: 10, cursor: 9, visible: 4, want: [2]int{6, 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list := overlayList{count: tt.total, selected: tt.cursor}
			start, end := list.window(tt.visible)
			if start != tt.want[0] || end != tt.want[1] {
				t.Fatalf("window = (%d, %d), want (%d, %d)", start, end, tt.want[0], tt.want[1])
			}
		})
	}
}

func TestOverlayLayoutFor_UsesFallbacksAndMargins(t *testing.T) {
	layout := overlayLayoutFor(80, 24)
	if layout.innerWidth != 80-2*composerOuterMargin-composerBoxBorderWidth {
		t.Fatalf("innerWidth = %d", layout.innerWidth)
	}
	if layout.marginLeft != composerOuterMargin {
		t.Fatalf("marginLeft = %d, want %d", layout.marginLeft, composerOuterMargin)
	}
	if layout.itemRows != layout.innerHeight-4 {
		t.Fatalf("itemRows = %d, innerHeight = %d", layout.itemRows, layout.innerHeight)
	}

	fallback := overlayLayoutFor(0, 0)
	if fallback.innerWidth <= 0 || fallback.itemRows < 0 {
		t.Fatalf("fallback layout is degenerate: %+v", fallback)
	}
}

func TestOverlayLayout_RowAtMapsScreenToItemRow(t *testing.T) {
	layout := overlayLayoutFor(80, 24)

	// First item row lives at Y=4 (blank, border, header, separator).
	row, ok := layout.rowAt(layout.marginLeft+1, 4)
	if !ok || row != 0 {
		t.Fatalf("rowAt first item = %d, %v; want 0, true", row, ok)
	}
	row, ok = layout.rowAt(layout.marginLeft+1, 5)
	if !ok || row != 1 {
		t.Fatalf("rowAt second item = %d, %v; want 1, true", row, ok)
	}
	// Above the item rows: inert.
	if _, ok := layout.rowAt(layout.marginLeft+1, 3); ok {
		t.Fatal("clicks in the header must be inert")
	}
	// Left of the content: inert.
	if _, ok := layout.rowAt(0, 4); ok {
		t.Fatal("clicks left of the content must be inert")
	}
	// Right of the content: inert.
	if _, ok := layout.rowAt(layout.marginLeft+1+layout.innerWidth, 4); ok {
		t.Fatal("clicks past the content must be inert")
	}
}

func TestOverlayCell_PadsAndTruncates(t *testing.T) {
	if got := overlayCell("ab", 5); got != "ab   " {
		t.Fatalf("overlayCell pad = %q, want %q", got, "ab   ")
	}
	got := ansi.Strip(overlayCell("abcdef", 4))
	if []rune(got)[len([]rune(got))-1] != '…' {
		t.Fatalf("overlayCell overflow = %q, want an ellipsis suffix", got)
	}
}

func TestOverlayRightCell_RightAligns(t *testing.T) {
	if got := overlayRightCell("ab", 5); got != "   ab" {
		t.Fatalf("overlayRightCell = %q, want %q", got, "   ab")
	}
}

func TestOverlayPanelTitle_EmbedsTitleInTopBorder(t *testing.T) {
	panel := strings.Join([]string{
		"┌" + strings.Repeat("─", 20) + "┐",
		"│" + strings.Repeat(" ", 20) + "│",
	}, "\n")
	titled := ansi.Strip(overlayPanelTitle(panel, "Demo"))
	top := strings.SplitN(titled, "\n", 2)[0]
	if !strings.Contains(top, "Demo") || !strings.HasPrefix(top, "┌─ ") || !strings.HasSuffix(top, "┐") {
		t.Fatalf("titled top border = %q", top)
	}
}
