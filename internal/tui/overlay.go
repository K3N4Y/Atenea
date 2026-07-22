package tui

// The overlay module is the shared UX core of the full-screen list pickers
// (/model, /mcp, /connect, /resume). Each of those hand-rolled the same list
// shape from scratch: a cursor that wraps at the ends, a scroll window that
// keeps the cursor visible, and — for the bordered pickers — a titled panel
// with cell padding, width/height fit and mouse hit-testing. This module owns
// that UX once so the pickers only supply their items, per-row rendering and a
// select callback. It deliberately owns no domain or async state: connect's
// masked key entry, mcp's busy map and every *Gen/*DoneMsg counter stay in the
// respective picker.

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/tui/theme"
)

// overlayList is the navigation core every picker embeds: a cursor over a
// list of a given length, with wrap movement and a centered scroll window.
// The owner sets count whenever its item source changes (via setCount) and
// reads selected to know which item is highlighted; the list never holds the
// items themselves, so it stays free of any picker's element type. Pickers
// embed it anonymously, so `picker.selected` and `picker.move(...)` read as if
// they were the picker's own.
type overlayList struct {
	count    int
	selected int
}

// setCount refreshes the list length and clamps the cursor into range. A
// count of zero parks the cursor at zero so hasSelection() reports "none".
func (l *overlayList) setCount(count int) {
	if count < 0 {
		count = 0
	}
	l.count = count
	if l.selected < 0 || l.selected >= count {
		l.selected = 0
	}
}

// move steps the cursor by delta, wrapping past either end. An empty list is
// inert. This is the one correct wrap implementation the pickers used to each
// copy (or, in resume's case, hand-roll a subtly different modulo).
func (l *overlayList) move(delta int) {
	if l.count == 0 {
		return
	}
	value := (l.selected + delta) % l.count
	if value < 0 {
		value += l.count
	}
	l.selected = value
}

// hasSelection reports the cursor when it points at a real item. An empty list
// or an out-of-range cursor reports (0, false) so callers can guard uniformly.
func (l overlayList) hasSelection() (int, bool) {
	if l.selected < 0 || l.selected >= l.count {
		return 0, false
	}
	return l.selected, true
}

// window returns the [start, end) slice of items to draw so that at most
// visible rows show with the cursor kept in view, centered when possible. It
// is the single windowing rule the model and resume pickers used to split
// between modelPickerWindow and resumePickerWindowStart.
func (l overlayList) window(visible int) (int, int) {
	return listWindow(l.count, l.selected, visible)
}

func listWindow(total, cursor, visible int) (int, int) {
	if visible <= 0 || total <= visible {
		return 0, total
	}
	cursor = min(max(cursor, 0), total-1)
	start := min(max(cursor-visible/2, 0), total-visible)
	return start, min(start+visible, total)
}

// overlayLayout freezes the measurements of a bordered picker panel so its
// view and its mouse hit-testing share one geometry. It is the shape the
// /model, /mcp and /connect panels render into.
type overlayLayout struct {
	marginLeft  int
	innerWidth  int
	innerHeight int
	itemRows    int
}

// overlayLayoutFor derives the panel geometry from the terminal size, using
// the same fallbacks and margins the pickers relied on. leftWidth for the
// two-column model picker is computed by the caller from innerWidth.
func overlayLayoutFor(width, height int) overlayLayout {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 16
	}
	innerWidth := max(width-2*composerOuterMargin-composerBoxBorderWidth, 0)
	innerHeight := max(max(height-2, 0)-composerBoxBorderWidth, 0)
	return overlayLayout{
		marginLeft:  min(composerOuterMargin, width),
		innerWidth:  innerWidth,
		innerHeight: innerHeight,
		itemRows:    max(innerHeight-4, 0),
	}
}

// rowAt maps single-column screen coordinates to a visible item row. The
// picker screen is: row 0 blank, 1 top border, 2 header, 3 separator, then the
// item rows; in X, content starts after the left margin and the left border.
func (l overlayLayout) rowAt(x, y int) (row int, ok bool) {
	row = y - 4
	if row < 0 || row >= l.itemRows {
		return 0, false
	}
	x -= l.marginLeft + 1
	if x < 0 || x >= l.innerWidth {
		return 0, false
	}
	return row, true
}

// overlayCell left-pads value to width, truncating with an ellipsis when it
// overflows. It is the render primitive the bordered pickers used for every
// left-aligned cell.
func overlayCell(value string, width int) string {
	value = ansi.Truncate(value, max(width, 0), "…")
	return value + strings.Repeat(" ", max(width-lipgloss.Width(value), 0))
}

// overlayRightCell is overlayCell for right-aligned content (prices).
func overlayRightCell(value string, width int) string {
	value = ansi.Truncate(value, max(width, 0), "…")
	return strings.Repeat(" ", max(width-lipgloss.Width(value), 0)) + value
}

// overlayPanelTitle embeds title into the top border of a rendered panel.
func overlayPanelTitle(panel, title string) string {
	lines := strings.Split(panel, "\n")
	if len(lines) == 0 {
		return panel
	}
	width := ansi.StringWidth(lines[0])
	remaining := max(width-ansi.StringWidth(title)-5, 0)
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Border))
	lines[0] = border.Render("┌─ ") + accentStyle.Render(title) + border.Render(" "+strings.Repeat("─", remaining)+"┐")
	return strings.Join(lines, "\n")
}

// overlayPanel renders the shared bordered, titled, full-screen chrome of the
// single-panel pickers: it takes the pre-built body lines (header, separator,
// item rows, footer hint) and wraps them in the border with the title and the
// left margin, then paints the full canvas. The model picker builds its own
// two-column body but reuses the very same wrapping via renderOverlayPanel.
func (m Model) renderOverlayPanel(layout overlayLayout, title string, lines []string) string {
	panelStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(theme.Border)).
		Width(layout.innerWidth)
	if layout.innerHeight > 0 {
		panelStyle = panelStyle.Height(layout.innerHeight)
	}
	panel := overlayPanelTitle(panelStyle.Render(strings.Join(lines, "\n")), title)
	panel = lipgloss.NewStyle().MarginLeft(layout.marginLeft).Render(panel)
	return m.renderFullCanvas("\n" + panel)
}
