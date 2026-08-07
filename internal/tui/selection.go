package tui

import (
	"os"
	"sort"
	"strings"
	"time"

	osc52 "github.com/aymanbagabas/go-osc52/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	"github.com/K3N4Y/atenea/internal/tui/theme"
)

const copySnackbarDuration = 2 * time.Second

type selectionPoint struct {
	line    int
	x       int
	ordinal int
}

type transcriptSelection struct {
	entry         int
	anchor        selectionPoint
	active        selectionPoint
	projection    assistantProjection
	firstLine     int
	lastLine      int
	pointerStartX int
	pointerStartY int
	dragged       bool
}

func (s transcriptSelection) selectedGraphemes() []selectableGrapheme {
	start, end := s.anchor.ordinal, s.active.ordinal
	if start > end {
		start, end = end, start
	}
	start = max(start, 0)
	end = min(end, len(s.projection.graphemes)-1)
	if start > end || start >= len(s.projection.graphemes) || end < 0 {
		return nil
	}
	// Cap the slice at the selection boundary so downstream rendering cannot
	// accidentally extend work into the rest of a large response.
	return s.projection.graphemes[start : end+1 : end+1]
}

func (s transcriptSelection) visibleSelectedGraphemes(firstLine, lastLine int) []selectableGrapheme {
	selected := s.selectedGraphemes()
	start := sort.Search(len(selected), func(i int) bool {
		return selected[i].line >= firstLine
	})
	end := start + sort.Search(len(selected)-start, func(i int) bool {
		return selected[start+i].line > lastLine
	})
	return selected[start:end:end]
}

type copySnackbar struct {
	message    string
	success    bool
	generation uint64
}

type snackbarExpiredMsg struct{ generation uint64 }

type selectableGrapheme struct {
	line   int
	x      int
	width  int
	source int
}

type assistantProjection struct {
	graphemes []selectableGrapheme
	source    []string
}

func defaultCopyToClipboard(text string) error {
	_, err := osc52.New(text).WriteTo(os.Stderr)
	return err
}

func snackbarExpiryCmd(generation uint64) tea.Cmd {
	return tea.Tick(copySnackbarDuration, func(time.Time) tea.Msg {
		return snackbarExpiredMsg{generation: generation}
	})
}

func graphemeStrings(text string) []string {
	iter := uniseg.NewGraphemes(text)
	out := make([]string, 0, len(text))
	for iter.Next() {
		out = append(out, iter.Str())
	}
	return out
}

func stripMarkdownMargin(line string) string {
	for i := 0; i < markdownDocMargin && strings.HasPrefix(line, " "); i++ {
		line = strings.TrimPrefix(line, " ")
	}
	return line
}

func assistantCopySource(text string) []string {
	// A zero width asks Glamour for its natural, unwrapped document. A huge
	// width also prevents wrapping, but Glamour right-pads rows to that width
	// and turns selection startup into work proportional to an arbitrary cap.
	plain := ansi.Strip(renderMarkdown(text, 0))
	lines := strings.Split(plain, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(stripMarkdownMargin(lines[i]), " ")
	}
	return graphemeStrings(strings.Trim(strings.Join(lines, "\n"), "\n"))
}

func projectionForAssistant(e entry, renderedLines []entryLine) assistantProjection {
	lines := make([]string, len(renderedLines))
	for i, line := range renderedLines {
		lines[i] = ansi.Strip(line.line)
	}
	source := assistantCopySource(e.text)
	nextSource := 0
	projection := assistantProjection{source: source}
	for row, line := range lines {
		content := strings.TrimRight(stripMarkdownMargin(line), " ")
		margin := ansi.StringWidth(line) - ansi.StringWidth(stripMarkdownMargin(line))
		x := margin
		for _, grapheme := range graphemeStrings(content) {
			for nextSource < len(source) && (source[nextSource] == "\n" || source[nextSource] == " ") && source[nextSource] != grapheme {
				nextSource++
			}
			if nextSource >= len(source) {
				break
			}
			cellWidth := max(uniseg.StringWidth(grapheme), 1)
			if source[nextSource] == grapheme {
				projection.graphemes = append(projection.graphemes, selectableGrapheme{
					line: row, x: x, width: cellWidth, source: nextSource,
				})
				nextSource++
			}
			x += cellWidth
		}
	}
	return projection
}

func selectionAt(lines []entryLine, entries []entry, x, viewportLine, width int) (*transcriptSelection, bool) {
	if viewportLine < 0 || viewportLine >= len(lines) {
		return nil, false
	}
	entryIndex := lines[viewportLine].idx
	if entryIndex < 0 || entryIndex >= len(entries) {
		return nil, false
	}
	e := entries[entryIndex]
	if e.kind != entryAssistant || !e.settled() {
		return nil, false
	}
	firstLine, lastLine := viewportLine, viewportLine
	for firstLine > 0 && lines[firstLine-1].idx == entryIndex {
		firstLine--
	}
	for lastLine+1 < len(lines) && lines[lastLine+1].idx == entryIndex {
		lastLine++
	}
	projection := projectionForAssistant(e, lines[firstLine:lastLine+1])
	row := viewportLine - firstLine
	for ordinal, grapheme := range projection.graphemes {
		if grapheme.line == row && x >= grapheme.x && x < grapheme.x+grapheme.width {
			point := selectionPoint{line: viewportLine, x: grapheme.x, ordinal: ordinal}
			return &transcriptSelection{
				entry: entryIndex, anchor: point, active: point, projection: projection,
				firstLine: firstLine, lastLine: lastLine,
			}, true
		}
	}
	return nil, false
}

func (m Model) clampSelectionPoint(x, viewportLine int, selection transcriptSelection) selectionPoint {
	first, last := selection.firstLine, selection.lastLine
	if first < 0 || last < first {
		return selection.active
	}
	originalLine := viewportLine
	viewportLine = min(max(viewportLine, first), last)
	projection := selection.projection
	relativeLine := viewportLine - first
	for ordinal, grapheme := range projection.graphemes {
		if grapheme.line == relativeLine && x >= grapheme.x && x < grapheme.x+grapheme.width {
			return selectionPoint{line: viewportLine, x: grapheme.x, ordinal: ordinal}
		}
	}
	if len(projection.graphemes) == 0 {
		return selection.active
	}
	relativeLine = viewportLine - first
	rowStart, rowEnd := -1, -1
	for ordinal, grapheme := range projection.graphemes {
		if grapheme.line == relativeLine {
			if rowStart < 0 {
				rowStart = ordinal
			}
			rowEnd = ordinal
		}
	}
	if rowStart >= 0 {
		firstGrapheme := projection.graphemes[rowStart]
		lastGrapheme := projection.graphemes[rowEnd]
		if x < firstGrapheme.x {
			return selectionPoint{line: viewportLine, x: firstGrapheme.x, ordinal: rowStart}
		}
		return selectionPoint{line: viewportLine, x: lastGrapheme.x, ordinal: rowEnd}
	}
	if originalLine < first {
		grapheme := projection.graphemes[0]
		return selectionPoint{line: first + grapheme.line, x: grapheme.x, ordinal: 0}
	}
	if viewportLine >= selection.anchor.line {
		for ordinal := len(projection.graphemes) - 1; ordinal >= 0; ordinal-- {
			grapheme := projection.graphemes[ordinal]
			if grapheme.line < relativeLine {
				return selectionPoint{line: first + grapheme.line, x: grapheme.x, ordinal: ordinal}
			}
		}
	} else {
		for ordinal, grapheme := range projection.graphemes {
			if grapheme.line > relativeLine {
				return selectionPoint{line: first + grapheme.line, x: grapheme.x, ordinal: ordinal}
			}
		}
	}
	return selection.active
}

func (m Model) selectedText() string {
	if m.selection == nil || !m.selection.dragged {
		return ""
	}
	projection := m.selection.projection
	if len(projection.graphemes) == 0 {
		return ""
	}
	start, end := m.selection.anchor.ordinal, m.selection.active.ordinal
	if start > end {
		start, end = end, start
	}
	start = max(start, 0)
	end = min(end, len(projection.graphemes)-1)
	from := projection.graphemes[start].source
	to := projection.graphemes[end].source
	if from > to {
		from, to = to, from
	}
	return strings.Join(projection.source[from:to+1], "")
}

func (m Model) cancelSelection() Model {
	if m.selection == nil {
		return m
	}
	m.selection = nil
	return m
}

func (m Model) startSelection(msg tea.MouseMsg) (Model, bool) {
	line, ok := m.transcriptLineAtMouse(msg)
	if !ok {
		return m, false
	}
	selection, ok := selectionAt(m.entryLines(), m.entries, msg.X, line, m.viewport.Width)
	if !ok {
		return m, false
	}
	selection.pointerStartX = msg.X
	selection.pointerStartY = msg.Y
	m.selection = selection
	return m, true
}

func (m Model) dragSelection(msg tea.MouseMsg) Model {
	if m.selection == nil {
		return m
	}
	line := m.viewport.YOffset + min(max(msg.Y, 0), max(m.viewport.Height-1, 0))
	point := m.clampSelectionPoint(msg.X, line, *m.selection)
	m.selection.active = point
	m.selection.dragged = m.selection.dragged || msg.X != m.selection.pointerStartX || msg.Y != m.selection.pointerStartY
	return m
}

func (m Model) finishSelection() (Model, tea.Cmd) {
	if m.selection == nil {
		return m, nil
	}
	text := m.selectedText()
	m.selection = nil
	if strings.TrimSpace(text) == "" {
		return m, nil
	}
	m.copyGeneration++
	copyFn := m.copyToClipboard
	if copyFn == nil {
		copyFn = defaultCopyToClipboard
	}
	err := copyFn(text)
	m.snackbar = copySnackbar{message: "Copied to clipboard", success: true, generation: m.copyGeneration}
	if err != nil {
		m.snackbar.message = "Could not copy selection"
		m.snackbar.success = false
	}
	return m, snackbarExpiryCmd(m.copyGeneration)
}

func (m Model) renderSelection(transcript string, lineOffset int) string {
	if m.selection == nil || !m.selection.dragged {
		return transcript
	}
	selected := m.selection.selectedGraphemes()
	if len(selected) == 0 {
		return transcript
	}
	lines := strings.Split(transcript, "\n")
	first := m.selection.firstLine
	if first < 0 {
		return transcript
	}
	firstVisibleLine := lineOffset - first
	lastVisibleLine := firstVisibleLine + len(lines) - 1
	selected = m.selection.visibleSelectedGraphemes(firstVisibleLine, lastVisibleLine)
	type selectedSpan struct{ left, right int }
	inverse := lipgloss.NewStyle().Reverse(true)
	renderSpan := func(relativeRow int, span selectedSpan) {
		row := first + relativeRow - lineOffset
		lineWidth := ansi.StringWidth(lines[row])
		left := ansi.Cut(lines[row], 0, span.left)
		selectedText := keepReverseVideo(ansi.Cut(lines[row], span.left, span.right))
		right := ansi.Cut(lines[row], span.right, lineWidth)
		lines[row] = left + inverse.Render(selectedText) + right
	}

	activeRow := -1
	var span selectedSpan
	for _, grapheme := range selected {
		if grapheme.line != activeRow {
			if activeRow >= 0 {
				renderSpan(activeRow, span)
			}
			activeRow = grapheme.line
			span = selectedSpan{left: grapheme.x, right: grapheme.x + grapheme.width}
			continue
		}
		span.left = min(span.left, grapheme.x)
		span.right = max(span.right, grapheme.x+grapheme.width)
	}
	if activeRow >= 0 {
		renderSpan(activeRow, span)
	}
	return strings.Join(lines, "\n")
}

const reverseVideoSGR = "\x1b[7m"

func keepReverseVideo(selected string) string {
	selected = strings.ReplaceAll(selected, "\x1b[0m", "\x1b[0m"+reverseVideoSGR)
	return strings.ReplaceAll(selected, "\x1b[m", "\x1b[m"+reverseVideoSGR)
}

type snackbarRect struct{ x, y, width, height int }

func (m Model) snackbarView() (string, snackbarRect, bool) {
	if m.snackbar.message == "" || !m.ready || m.width <= 0 {
		return "", snackbarRect{}, false
	}
	composerTop := m.viewport.Height + m.composerMenuReservedLines()
	if m.showsWorking() {
		composerTop++
	}
	composerTop += m.permissionPanelHeight()
	availableHeight := composerTop - 1
	if availableHeight <= 0 {
		return "", snackbarRect{}, false
	}
	// Degrade width before height: first remove horizontal padding and truncate
	// the one-line message; only after that may the vertical padding collapse.
	verticalPadding := 1
	horizontalPadding := 2
	maxWidth := max(m.width-m.baseLayout().chatMargin, 0)
	messageWidth := ansi.StringWidth(m.snackbar.message)
	width := 1 + 2*horizontalPadding + messageWidth
	if width > maxWidth {
		horizontalPadding = 0
		width = min(1+messageWidth, maxWidth)
	}
	if width <= 1 {
		return "", snackbarRect{}, false
	}
	if availableHeight < 3 {
		verticalPadding = 0
	}
	height := 1 + 2*verticalPadding
	if height > availableHeight {
		return "", snackbarRect{}, false
	}
	textWidth := max(width-1-2*horizontalPadding, 0)
	message := ansi.Truncate(m.snackbar.message, textWidth, "…")
	background := lipgloss.Color(theme.CodeBlockHex)
	railStyle := successStyle
	if !m.snackbar.success {
		railStyle = dangerStyle
	}
	rail := railStyle.Background(background).Render("┃")
	fill := lipgloss.NewStyle().Background(background)
	rows := make([]string, height)
	for i := range rows {
		body := strings.Repeat(" ", width-1)
		if i == verticalPadding {
			body = strings.Repeat(" ", horizontalPadding) + message + strings.Repeat(" ", max(width-1-horizontalPadding-ansi.StringWidth(message), 0))
		}
		rows[i] = rail + fill.Render(body)
	}
	x := max(m.width-m.baseLayout().chatMargin-width, 0)
	y := composerTop - 1 - height
	return strings.Join(rows, "\n"), snackbarRect{x: x, y: y, width: width, height: height}, true
}

func overlayAt(base, overlay string, rect snackbarRect) string {
	lines := strings.Split(base, "\n")
	overlayLines := strings.Split(overlay, "\n")
	for i, row := range overlayLines {
		y := rect.y + i
		if y < 0 || y >= len(lines) {
			continue
		}
		lineWidth := ansi.StringWidth(lines[y])
		left := ansi.Cut(lines[y], 0, rect.x)
		right := ansi.Cut(lines[y], min(rect.x+rect.width, lineWidth), lineWidth)
		lines[y] = left + row + right
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderSnackbar(canvas string) string {
	view, rect, ok := m.snackbarView()
	if !ok {
		return canvas
	}
	return overlayAt(canvas, view, rect)
}

func (m Model) snackbarHit(msg tea.MouseMsg) bool {
	_, rect, ok := m.snackbarView()
	return ok && msg.X >= rect.x && msg.X < rect.x+rect.width && msg.Y >= rect.y && msg.Y < rect.y+rect.height
}
