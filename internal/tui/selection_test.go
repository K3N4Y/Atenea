package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/K3N4Y/atenea/internal/session"
)

func settledAssistant(t *testing.T, text string, width, height int) Model {
	t.Helper()
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: width, Height: height})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text}})
	m = drainReveal(t, m)
	return m
}

func assistantCell(t *testing.T, m Model, contains string, offset int) (x, y int) {
	t.Helper()
	for row, line := range m.entryLines() {
		plain := ansi.Strip(line.line)
		byteColumn := strings.Index(plain, contains)
		if line.idx >= 0 && byteColumn >= 0 && m.entries[line.idx].kind == entryAssistant {
			column := ansi.StringWidth(plain[:byteColumn])
			return column + offset, topBarHeight + row - m.viewport.YOffset
		}
	}
	t.Fatalf("assistant cell containing %q not found in %#v", contains, m.entryLines())
	return 0, 0
}

func updateWithCmd(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", updated)
	}
	return next, cmd
}

func TestModel_DragCopiesPreciseSettledAssistantSelection(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })
	m := settledAssistant(t, "Alpha bravo charlie delta", 60, 20)
	var copied string
	m.copyToClipboard = func(text string) error { copied = text; return nil }
	startX, startY := assistantCell(t, m, "bravo", 0)
	endX, endY := assistantCell(t, m, "bravo", len("bravo")-1)

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: startX, Y: startY})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: endX, Y: endY})
	if !strings.Contains(m.View(), "\x1b[7m") {
		t.Fatalf("View() = %q, active drag must render an inverse selection", m.View())
	}

	var cmd tea.Cmd
	m, cmd = updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: endX, Y: endY})
	if cmd == nil {
		t.Fatal("release returned nil command, want clipboard write")
	}
	if m.selection != nil {
		t.Fatal("selection state must disappear on release")
	}
	if copied != "bravo" {
		t.Fatalf("copied = %q, want %q", copied, "bravo")
	}
	if !strings.Contains(ansi.Strip(m.View()), "Copied to clipboard") {
		t.Fatalf("View() = %q, successful copy must show snackbar", ansi.Strip(m.View()))
	}
}

func TestAssistantCopySourceAllocationsStayBounded(t *testing.T) {
	text := strings.Repeat("A paragraph with **styled text**, a [link](https://example.com), and enough words to represent a realistic response.\n\n", 100)
	result := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = assistantCopySource(text)
		}
	})
	const maxBytesPerCopy = 16 << 20
	if got := result.AllocedBytesPerOp(); got > maxBytesPerCopy {
		t.Fatalf("assistantCopySource allocated %d bytes per copy, want at most %d", got, maxBytesPerCopy)
	}
}

func TestModel_DragHighlightDoesNotRewriteViewportContent(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })
	m := settledAssistant(t, strings.Repeat("selectable response text ", 20), 50, 18)
	x, y := assistantCell(t, m, "selectable", 0)
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 5, Y: y})
	if strings.Contains(m.viewport.View(), reverseVideoSGR) {
		t.Fatal("mouse motion rewrote viewport content; selection must be overlaid at View time")
	}
	if !strings.Contains(m.View(), reverseVideoSGR) {
		t.Fatal("View() must still render the active selection highlight")
	}
}

func TestRenderSelectionBoundsTraversalToVisibleSelection(t *testing.T) {
	const (
		graphemeCount = 100_000
		visibleLine   = 50_000
		firstLine     = 37
	)
	graphemes := make([]selectableGrapheme, graphemeCount)
	for i := range graphemes {
		graphemes[i] = selectableGrapheme{line: i, x: 0, width: 2, source: i}
	}
	selection := transcriptSelection{
		anchor:     selectionPoint{ordinal: graphemeCount - 1},
		active:     selectionPoint{ordinal: 0},
		projection: assistantProjection{graphemes: graphemes},
		firstLine:  firstLine,
		dragged:    true,
	}

	visible := selection.visibleSelectedGraphemes(visibleLine, visibleLine)
	if len(visible) != 1 || cap(visible) != 1 || visible[0].line != visibleLine {
		t.Fatalf("visible selection = len %d cap %d %#v, want one capacity-bounded grapheme on line %d", len(visible), cap(visible), visible, visibleLine)
	}

	m := Model{selection: &selection}
	transcript := "\x1b[31m界\x1b[0m"
	got := m.renderSelection(transcript, firstLine+visibleLine)
	if ansi.Strip(got) != "界" || !strings.Contains(got, reverseVideoSGR) {
		t.Fatalf("renderSelection() = %q, want the visible wide grapheme highlighted without changing content", got)
	}
}

func TestSelectionHighlightSurvivesMarkdownStyleResets(t *testing.T) {
	selected := "before\x1b[0mafter\x1b[mbeyond"
	got := keepReverseVideo(selected)
	for _, reset := range []string{"\x1b[0m", "\x1b[m"} {
		if !strings.Contains(got, reset+reverseVideoSGR) {
			t.Fatalf("keepReverseVideo(%q) = %q, reset %q must restore reverse video", selected, got, reset)
		}
	}
}

func TestModel_ReverseDragCopiesWholeGraphemes(t *testing.T) {
	m := settledAssistant(t, "Alpha cafe\u0301 界 omega", 60, 20)
	var copied string
	m.copyToClipboard = func(text string) error { copied = text; return nil }
	startX, startY := assistantCell(t, m, "界", 0)
	endX, endY := assistantCell(t, m, "cafe", 0)
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: startX, Y: startY})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: endX, Y: endY})
	m, cmd := updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: endX, Y: endY})
	_ = m
	if cmd == nil {
		t.Fatal("reverse drag returned nil clipboard command")
	}
	if copied != "cafe\u0301 界" {
		t.Fatalf("copied = %q, want complete graphemes %q", copied, "cafe\u0301 界")
	}
}

func TestModel_ReleasePositionCompletesSelectionWithoutMotion(t *testing.T) {
	m := settledAssistant(t, "Alpha bravo", 60, 20)
	var copied string
	m.copyToClipboard = func(text string) error { copied = text; return nil }
	startX, startY := assistantCell(t, m, "bravo", 0)
	endX, endY := assistantCell(t, m, "bravo", len("bravo")-1)
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: startX, Y: startY})
	m, cmd := updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: endX, Y: endY})
	_ = m
	if cmd == nil {
		t.Fatal("press-to-release drag returned nil clipboard command")
	}
	if copied != "bravo" {
		t.Fatalf("copied = %q, release coordinate must complete selection", copied)
	}
}

func TestModel_DragWithinWideGraphemeCopiesThatGrapheme(t *testing.T) {
	m := settledAssistant(t, "界", 60, 20)
	var copied string
	m.copyToClipboard = func(text string) error { copied = text; return nil }
	x, y := assistantCell(t, m, "界", 0)
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m, cmd := updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: x + 1, Y: y})
	_ = m
	if cmd == nil {
		t.Fatal("drag across a wide grapheme returned nil clipboard command")
	}
	if copied != "界" {
		t.Fatalf("copied = %q, want one complete wide grapheme", copied)
	}
}

func TestModel_DragOutsideAssistantClampsToStartingResponse(t *testing.T) {
	m := settledAssistant(t, "first response", 60, 20)
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "second response"})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Message: &session.Message{ID: "a2", Role: session.RoleAssistant, Text: "second response"}})
	m = drainReveal(t, m)
	var copied string
	m.copyToClipboard = func(text string) error { copied = text; return nil }
	x, y := assistantCell(t, m, "first", 0)
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 59, Y: y + 10})
	m, cmd := updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 59, Y: y + 10})
	_ = m
	if cmd == nil {
		t.Fatal("clamped drag returned nil clipboard command")
	}
	if copied != "first response" {
		t.Fatalf("copied = %q, drag must remain within starting response", copied)
	}
}

func TestModel_DragIntoWrappedLinePaddingClampsToThatLine(t *testing.T) {
	m := settledAssistant(t, "one two three four five six seven eight nine ten eleven twelve", 24, 20)
	var copied string
	m.copyToClipboard = func(text string) error { copied = text; return nil }
	startX, startY := assistantCell(t, m, "one", 0)
	_, targetY := assistantCell(t, m, "five", 0)
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: startX, Y: startY})
	m, _ = updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 23, Y: targetY})
	if strings.Contains(copied, "eleven") || strings.Contains(copied, "twelve") {
		t.Fatalf("copied = %q, trailing padding on a wrapped row must clamp to that row, not the response end", copied)
	}
	if !strings.Contains(copied, "five") {
		t.Fatalf("copied = %q, selection must reach the targeted wrapped row", copied)
	}
}

func TestModel_DragIntoSemanticBlankLineStopsAtPreviousText(t *testing.T) {
	m := settledAssistant(t, "first paragraph\n\nsecond paragraph", 50, 20)
	var copied string
	m.copyToClipboard = func(text string) error { copied = text; return nil }
	x, y := assistantCell(t, m, "first", 0)
	_, secondY := assistantCell(t, m, "second", 0)
	blankY := secondY - 1
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m, _ = updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: x, Y: blankY})
	if copied != "first paragraph" {
		t.Fatalf("copied = %q, blank semantic row must stop at preceding text", copied)
	}
}

func TestModel_ActiveSelectionReleaseOverSnackbarCancels(t *testing.T) {
	m := settledAssistant(t, strings.Repeat("selectable words ", 10), 50, 18)
	m.snackbar = copySnackbar{message: "Copied to clipboard", success: true, generation: 1}
	x, y := assistantCell(t, m, "selectable", 0)
	_, rect, ok := m.snackbarView()
	if !ok {
		t.Fatal("snackbarView() did not render")
	}
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 3, Y: y})
	m, cmd := updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: rect.x, Y: topBarHeight + rect.y})
	if cmd != nil || m.selection != nil {
		t.Fatal("release over snackbar must cancel active selection without copying")
	}
}

func TestModel_NonLeftReleaseCancelsSelection(t *testing.T) {
	m := settledAssistant(t, "selectable words", 50, 18)
	copied := false
	m.copyToClipboard = func(string) error { copied = true; return nil }
	x, y := assistantCell(t, m, "selectable", 0)
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 3, Y: y})
	m, _ = updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonRight, X: x + 3, Y: y})
	if copied || m.selection != nil {
		t.Fatal("non-left release must cancel without copying")
	}
}

func TestModel_SelectionOnlyStartsOnSettledAssistantText(t *testing.T) {
	tests := []struct {
		name  string
		model func(*testing.T) Model
		text  string
	}{
		{
			name: "user message",
			model: func(t *testing.T) Model {
				m := NewModel(nil, "s1", nil)
				m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
				return apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "user words"}})
			},
			text: "user",
		},
		{
			name: "streaming assistant",
			model: func(t *testing.T) Model {
				m := NewModel(nil, "s1", nil)
				m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
				m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
				m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "live words"})
				return drainReveal(t, m)
			},
			text: "live",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := test.model(t)
			plain := ansi.Strip(m.View())
			line := lineIndexWith(t, plain, test.text)
			x := strings.Index(strings.Split(plain, "\n")[line], test.text)
			m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: line})
			m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 2, Y: line})
			_, cmd := updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: x + 2, Y: line})
			if cmd != nil {
				t.Fatal("non-settled-assistant drag returned clipboard command")
			}
		})
	}
}

func TestModel_SelectionPreservesSemanticBreaksAndDropsVisualWraps(t *testing.T) {
	m := settledAssistant(t, "First paragraph has enough words to wrap naturally in a narrow terminal.\n\nSecond paragraph.", 32, 20)
	var copied string
	m.copyToClipboard = func(text string) error { copied = text; return nil }
	startX, startY := assistantCell(t, m, "First", 0)
	endX, endY := assistantCell(t, m, "paragraph.", len("paragraph.")-1)
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: startX, Y: startY})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: endX, Y: endY})
	m, cmd := updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: endX, Y: endY})
	_ = m
	if cmd == nil {
		t.Fatal("release returned nil clipboard command")
	}
	want := "First paragraph has enough words to wrap naturally in a narrow terminal.\n\nSecond paragraph."
	if copied != want {
		t.Fatalf("copied = %q, want exact text without visual-wrap newlines %q", copied, want)
	}
}

func TestModel_SelectionAcrossWrappedListIgnoresVisualContinuationPrefix(t *testing.T) {
	text := "- first list item contains enough words to wrap across several narrow terminal rows"
	m := settledAssistant(t, text, 28, 20)
	var copied string
	m.copyToClipboard = func(text string) error { copied = text; return nil }
	startX, startY := assistantCell(t, m, "first", 0)
	endX, endY := assistantCell(t, m, "rows", len("rows")-1)
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: startX, Y: startY})
	m, _ = updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: endX, Y: endY})
	want := "first list item contains enough words to wrap across several narrow terminal rows"
	if copied != want {
		t.Fatalf("copied = %q, want semantic list text %q", copied, want)
	}
}

func TestModel_SelectionCancelsWhenCoordinatesBecomeStale(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.Msg
	}{
		{name: "resize", msg: tea.WindowSizeMsg{Width: 61, Height: 20}},
		{name: "scroll", msg: tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}},
		{name: "focus loss", msg: tea.BlurMsg{}},
		{name: "transcript change", msg: EventMsg{Message: &session.Message{ID: "u2", Role: session.RoleUser, Text: "new"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := settledAssistant(t, "select these words", 60, 20)
			x, y := assistantCell(t, m, "select", 0)
			m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
			m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 3, Y: y})
			m = apply(t, m, test.msg)
			_, cmd := updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: x + 3, Y: y})
			if cmd != nil {
				t.Fatal("stale selection returned clipboard command")
			}
		})
	}
}

func TestModel_CopySnackbarOverlaysTranscriptAboveComposer(t *testing.T) {
	m := settledAssistant(t, strings.Repeat("background transcript words ", 40), 50, 18)
	m.snackbar = copySnackbar{message: "Copied to clipboard", success: true, generation: 1}
	beforeHeight := m.viewport.Height
	view := ansi.Strip(m.View())
	lines := strings.Split(view, "\n")
	messageRow := lineIndexWith(t, view, "Copied to clipboard")
	composerRow := lineIndexWith(t, view, "╭")
	if messageRow >= composerRow-1 {
		t.Fatalf("snackbar message row %d must sit above composer row %d with a gap", messageRow, composerRow)
	}
	if got := m.viewport.Height; got != beforeHeight {
		t.Fatalf("viewport.Height = %d, want unchanged %d: snackbar must overlay instead of reserve space", got, beforeHeight)
	}
	for _, row := range lines[messageRow-1 : messageRow+2] {
		if !strings.Contains(row, "│") {
			t.Fatalf("snackbar row = %q, every padded row must carry the left rail", row)
		}
	}
}

func TestSnackbarPaintsRailCellAndPaddingBackground(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })
	m := settledAssistant(t, "answer", 50, 18)
	m.snackbar = copySnackbar{message: "Copied to clipboard", success: true, generation: 1}
	view, _, ok := m.snackbarView()
	if !ok {
		t.Fatal("snackbarView() did not render")
	}
	backgroundParams := "48;2;48;48;48"
	for i, row := range strings.Split(view, "\n") {
		if !strings.Contains(row, backgroundParams) {
			t.Fatalf("row %d = %q, snackbar rail cell and padding must share #303030 background", i, row)
		}
		beforeRail, _, found := strings.Cut(row, "│")
		if !found || !strings.Contains(beforeRail, backgroundParams) {
			t.Fatalf("row %d = %q, the cell containing the left rail must paint its background", i, row)
		}
	}
}

func TestModel_CopyFailureAndExpiryReplaceSnackbarByGeneration(t *testing.T) {
	m := settledAssistant(t, "copy me", 50, 18)
	m.copyToClipboard = func(string) error { return errors.New("denied") }
	x, y := assistantCell(t, m, "copy", 0)
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 3, Y: y})
	m, _ = updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: x + 3, Y: y})
	if !strings.Contains(ansi.Strip(m.View()), "Could not copy selection") {
		t.Fatalf("View() = %q, failed write must show failure snackbar", ansi.Strip(m.View()))
	}
	old := m.snackbar.generation
	m.copyToClipboard = func(string) error { return nil }
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m, _ = updateWithCmd(t, m, tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: x + 3, Y: y})
	newGeneration := m.snackbar.generation
	m = apply(t, m, snackbarExpiredMsg{generation: old})
	if m.snackbar.generation != newGeneration || m.snackbar.message == "" {
		t.Fatal("stale expiry removed the replacement snackbar")
	}
	m = apply(t, m, snackbarExpiredMsg{generation: newGeneration})
	if m.snackbar.message != "" {
		t.Fatal("current expiry did not remove snackbar")
	}
}

func TestSnackbarExpiryDuration(t *testing.T) {
	if copySnackbarDuration != 2*time.Second {
		t.Fatalf("copySnackbarDuration = %v, want 2s", copySnackbarDuration)
	}
}
