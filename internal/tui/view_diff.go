package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/tui/theme"
)

func diffStat(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

func summarizeToolInput(raw string) string {
	if raw == "" {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || len(fields) == 0 {
		return ""
	}
	if len(fields) == 1 {
		for _, v := range fields {
			var s string
			if err := json.Unmarshal(v, &s); err == nil && s != "" {
				return s
			}
		}
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(raw)); err != nil {
		return ""
	}
	return buf.String()
}

func renderCappedLines(text string, maxLines int, renderLine func(line string) string) string {
	text = sanitizeTerminalText(text)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	shown := lines
	if len(shown) > maxLines {
		shown = shown[:maxLines]
	}
	rendered := make([]string, 0, len(shown)+1)
	for _, line := range shown {
		rendered = append(rendered, renderLine(line))
	}
	if hidden := len(lines) - len(shown); hidden > 0 {
		rendered = append(rendered, toolOutputStyle.Render(activityRailPrefix+"… +"+strconv.Itoa(hidden)+" lines"))
	}
	return strings.Join(rendered, "\n")
}

func renderOutputPreview(output string) string {
	return renderCappedLines(output, toolOutputPreviewLines, func(line string) string {
		return toolOutputStyle.Render(activityRailPrefix + line)
	})
}

func renderDiffPreview(diff string) string {
	return renderCappedLines(diff, toolDiffPreviewLines, func(line string) string {
		style := toolOutputStyle
		switch {
		case strings.HasPrefix(line, "+"):
			style = diffAddStyle
		case strings.HasPrefix(line, "-"):
			style = diffDelStyle
		}
		return style.Render(activityRailPrefix + line)
	})
}

// diffLine is one body line of a unified-diff hunk: kind is its marker byte
// (' ' context, '-' removed, '+' added) and text the content after the marker.
type diffLine struct {
	kind byte
	text string
}

// diffHunk is one hunk of a unified diff: header is the raw "@@ … @@" text,
// oldStart/newStart the 1-indexed first line numbers of each side (from the
// header), and lines its body in unified order.
type diffHunk struct {
	header   string
	oldStart int
	newStart int
	lines    []diffLine
}

// parseUnifiedDiff splits the unified diff produced by hashline.UnifiedDiff
// into the edited path (from the "+++ b/…" header) and its hunks. ok is false
// when the text is not a unified diff with at least one hunk (the caller then
// falls back to the plain diff preview).
func parseUnifiedDiff(diff string) (path string, hunks []diffHunk, ok bool) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case len(hunks) == 0 && strings.HasPrefix(line, "+++ "):
			// File headers only appear before the first hunk; guarding on that
			// keeps an added line whose content is "++ …" (diff line "+++ …")
			// from being mistaken for the header once inside a hunk.
			path = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
		case len(hunks) == 0 && strings.HasPrefix(line, "--- "):
			// The old-side file header carries no per-line data we render.
		case strings.HasPrefix(line, "@@"):
			oldStart, newStart, headerOK := parseHunkHeader(line)
			if !headerOK {
				continue
			}
			hunks = append(hunks, diffHunk{header: line, oldStart: oldStart, newStart: newStart})
		case line == "" || strings.HasPrefix(line, `\`):
			// Trailing blank split segment or a "\ No newline" marker: skip.
		default:
			if len(hunks) == 0 {
				continue
			}
			h := &hunks[len(hunks)-1]
			h.lines = append(h.lines, diffLine{kind: line[0], text: line[1:]})
		}
	}
	return path, hunks, len(hunks) > 0
}

// parseHunkHeader reads the 1-indexed start line of each side from a
// "@@ -A[,B] +C[,D] @@" header. difflib prints the start already 1-indexed
// (it omits ",length" when the length is 1), so only the start matters here.
func parseHunkHeader(line string) (oldStart, newStart int, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 || fields[0] != "@@" {
		return 0, 0, false
	}
	oldStart, okOld := parseHunkSide(fields[1], '-')
	newStart, okNew := parseHunkSide(fields[2], '+')
	return oldStart, newStart, okOld && okNew
}

// parseHunkSide reads the start line of one hunk side ("-A", "-A,B", "+C" …):
// the sign byte then digits up to an optional ",length" that is discarded.
func parseHunkSide(field string, sign byte) (int, bool) {
	if len(field) == 0 || field[0] != sign {
		return 0, false
	}
	num := field[1:]
	if i := strings.IndexByte(num, ','); i >= 0 {
		num = num[:i]
	}
	start, err := strconv.Atoi(num)
	if err != nil {
		return 0, false
	}
	return start, true
}

func renderEditDiff(diff string, width int) string {
	path, hunks, ok := parseUnifiedDiff(diff)
	if !ok {
		return ""
	}
	gutter := diffGutterWidth(hunks)
	contentW, margin := cardInset(width)
	rows := []string{diffPathBar(diffPathStyle, path, contentW)}
	for _, h := range hunks {
		rows = append(rows, diffHunkBar(h, contentW))
		rows = append(rows, diffBlockRows(h, gutter, contentW, false)...)
		rows = append(rows, diffBlockRows(h, gutter, contentW, true)...) // after (added, green)
	}
	return frameDiffCard(rows, margin)
}

// renderWriteCard renders a successful write as a diff card sibling to
// renderEditDiff, but in a single neutral gray instead of the red/green
// before/after pair. A write always creates a brand-new file (it refuses to
// overwrite), so there is no old side to show: the card is just the file-path
// bar and, below it, every written line on the gray band — no hunk bar, no
// "+N -M" stat, no +/- marker. Returns "" when the diff does not parse, so the
// caller falls back to renderActivity.
func renderWriteCard(diff string, width int) string {
	path, hunks, ok := parseUnifiedDiff(diff)
	if !ok {
		return ""
	}
	gutter := diffGutterWidth(hunks)
	contentW, margin := cardInset(width)
	rows := []string{diffPathBar(writePathStyle, path, contentW)}
	for _, h := range hunks {
		rows = append(rows, writeBlockRows(h, gutter, contentW)...)
	}
	return frameDiffCard(rows, margin)
}

// cardInset splits width into the card's content width and the left/right
// margin string that insets it by composerOuterMargin cells, so a card lines up
// with the rest of the chat content (activity lines, user messages) and the
// margin cells reveal the canvas background. A width too small to inset falls
// back to full bleed (no margin).
func cardInset(width int) (contentW int, margin string) {
	if width > 2*composerOuterMargin {
		return width - 2*composerOuterMargin, strings.Repeat(" ", composerOuterMargin)
	}
	return width, ""
}

// frameDiffCard finishes a diff card: it caps the rows at editDiffCardMaxRows
// (collapsing the overflow into a "… +N lines" tail) and insets every row by
// margin. Shared by renderEditDiff and renderWriteCard.
func frameDiffCard(rows []string, margin string) string {
	if len(rows) > editDiffCardMaxRows {
		hidden := len(rows) - editDiffCardMaxRows
		rows = rows[:editDiffCardMaxRows]
		rows = append(rows, diffCtxStyle.Render("… +"+strconv.Itoa(hidden)+" lines"))
	}
	for i, r := range rows {
		rows[i] = margin + r + margin
	}
	return strings.Join(rows, "\n")
}

// diffGutterWidth is the width of the line-number column: the digit count of
// the largest line number any hunk can show on either side, so numbers align
// across every block of the card.
func diffGutterWidth(hunks []diffHunk) int {
	maxNum := 1
	for _, h := range hunks {
		oldNum, newNum := h.oldStart, h.newStart
		for _, l := range h.lines {
			switch l.kind {
			case '+':
				newNum++
			case '-':
				oldNum++
			default:
				oldNum++
				newNum++
			}
		}
		maxNum = max(maxNum, oldNum, newNum)
	}
	return len(strconv.Itoa(maxNum))
}

// diffPathBar renders the file-path header bar of a card: a full-width band, in
// the given style, carrying the path. The edit card passes diffPathStyle (gray
// name); the write card passes writePathStyle (olive name on the same band).
func diffPathBar(style lipgloss.Style, path string, width int) string {
	return style.Render(fitBand(sanitizeTerminalText(path), width))
}

// diffHunkBar renders a hunk's "@@ … @@" header as a full-width muted band with
// the hunk's "+N -M" stat pinned to the right edge (green added, red removed).
func diffHunkBar(h diffHunk, width int) string {
	added, removed := 0, 0
	for _, l := range h.lines {
		switch l.kind {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	header := sanitizeTerminalText(h.header)
	// The stat rides the same muted band as the header, so its "+N"/"-M" and the
	// space between them carry the header background too (green/red foreground
	// on gray), not the bare canvas.
	bg := lipgloss.Color(theme.DiffHeaderBg)
	stat := diffAddStyle.Background(bg).Render("+"+strconv.Itoa(added)) +
		diffHunkStyle.Render(" ") +
		diffDelStyle.Background(bg).Render("-"+strconv.Itoa(removed))
	statWidth := ansi.StringWidth("+" + strconv.Itoa(added) + " -" + strconv.Itoa(removed))
	if width <= 0 {
		return diffHunkStyle.Render(header) + "  " + stat
	}
	// Truncate the header so the header text and the stat never collide, then
	// pad the gap between them so the stat sits flush against the right edge.
	headerRoom := max(width-statWidth-1, 1)
	header = ansi.Truncate(header, headerRoom, "…")
	gap := max(width-ansi.StringWidth(header)-statWidth, 1)
	return diffHunkStyle.Render(header+strings.Repeat(" ", gap)) + stat
}

func diffBlockRows(h diffHunk, gutter, width int, added bool) []string {
	bandStyle, railStyle, keep := diffDelBandStyle, diffDelRailStyle, byte('-')
	if added {
		bandStyle, railStyle, keep = diffAddBandStyle, diffAddRailStyle, '+'
	}
	num := h.oldStart
	if added {
		num = h.newStart
	}
	var rows []string
	for _, l := range h.lines {
		switch {
		case l.kind == keep:
			rows = append(rows, diffRow(railStyle, bandStyle, gutter, width, num, l.kind, l.text))
			num++
		case l.kind == ' ':
			rows = append(rows, diffRow(railStyle, diffCtxStyle, gutter, width, num, ' ', l.text))
			num++
		}
	}
	return rows
}

// writeBlockRows renders every added line of a write's hunk as a gray write-card
// row: the neutral band and rail, new-side line numbers, and no +/- marker
// (marker 0). A write diff is a pure insertion, so only '+' lines carry content;
// anything else is ignored.
func writeBlockRows(h diffHunk, gutter, width int) []string {
	num := h.newStart
	var rows []string
	for _, l := range h.lines {
		if l.kind != '+' {
			continue
		}
		rows = append(rows, diffRow(writeRailStyle, writeBandStyle, gutter, width, num, noMarker, l.text))
		num++
	}
	return rows
}

// diffRow renders one row of a diff block: the colored rail bar, then the line
// number, marker and content. A changed row (band != diffCtxStyle) fills the
// full width as a colored band; a context row stays plain (no trailing fill).
// Content truncates with an ellipsis so the row is always exactly one line and
// the gutter stays aligned.
func diffRow(railStyle, bodyStyle lipgloss.Style, gutter, width, num int, marker byte, text string) string {
	// noMarker drops the +/- column entirely (the write card): line number, two
	// spaces, then the text. Otherwise the marker sits between the number and the
	// text as in a unified diff.
	inner := fmt.Sprintf("%*d  %s", gutter, num, sanitizeTerminalText(text))
	if marker != noMarker {
		inner = fmt.Sprintf("%*d %c %s", gutter, num, marker, sanitizeTerminalText(text))
	}
	railCells := ansi.StringWidth(diffRailGlyph)
	innerWidth := width - railCells
	if width <= 0 {
		return railStyle.Render(diffRailGlyph) + bodyStyle.Render(inner)
	}
	if innerWidth < 1 {
		innerWidth = 1
	}
	inner = ansi.Truncate(inner, innerWidth, "…")
	// Changed rows fill the band to the full width; context rows stay unpadded
	// so they read as plain text without a trailing colored block.
	if bodyStyle.GetBackground() != diffCtxStyle.GetBackground() {
		if pad := innerWidth - ansi.StringWidth(inner); pad > 0 {
			inner += strings.Repeat(" ", pad)
		}
	}
	return railStyle.Render(diffRailGlyph) + bodyStyle.Render(inner)
}

// fitBand truncates text to width and pads it back to width so a header bar's
// background spans the full line. width <= 0 leaves the text untouched.
func fitBand(text string, width int) string {
	if width <= 0 {
		return text
	}
	text = ansi.Truncate(text, width, "…")
	if pad := width - ansi.StringWidth(text); pad > 0 {
		text += strings.Repeat(" ", pad)
	}
	return text
}
