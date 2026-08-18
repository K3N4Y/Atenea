package tui

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tui/theme"
)

const toolDiffPreviewLines = 16

const editDiffCardMaxRows = 40

const diffRailGlyph = "▌"

const noMarker byte = 0

var (
	diffAddStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Success))
	diffDelStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Error))
	diffPathStyle    = lipgloss.NewStyle().Background(lipgloss.Color(theme.DiffHeaderBg))
	diffHunkStyle    = metadataStyle.Background(lipgloss.Color(theme.DiffHeaderBg))
	diffDelBandStyle = diffDelStyle.Background(lipgloss.Color(theme.DiffDelBg))
	diffAddBandStyle = diffAddStyle.Background(lipgloss.Color(theme.DiffAddBg))
	diffDelRailStyle = diffDelStyle
	diffAddRailStyle = diffAddStyle
	diffCtxStyle     = metadataStyle
	writeBandStyle   = metadataStyle.Background(lipgloss.Color(theme.CodeBlockHex))
	writeRailStyle   = metadataStyle.Background(lipgloss.Color(theme.CodeBlockHex))
	writePathStyle   = lipgloss.NewStyle().Background(lipgloss.Color(theme.WriteCardPath)).Foreground(lipgloss.Color(theme.Canvas))
)

// diffSyntaxStyle reuses the assistant code-block palette. Diff direction is
// not encoded here: the rail, +/- marker, and row background keep that job,
// while syntax colors make the source itself scannable.
var diffSyntaxStyle = func() *chroma.Style {
	config := markdownStyle.CodeBlock.Chroma
	entry := func(style glamouransi.StylePrimitive) string {
		var parts []string
		if style.Color != nil {
			parts = append(parts, *style.Color)
		}
		if style.Bold != nil {
			if *style.Bold {
				parts = append(parts, "bold")
			} else {
				parts = append(parts, "nobold")
			}
		}
		if style.Italic != nil {
			if *style.Italic {
				parts = append(parts, "italic")
			} else {
				parts = append(parts, "noitalic")
			}
		}
		if style.Underline != nil {
			if *style.Underline {
				parts = append(parts, "underline")
			} else {
				parts = append(parts, "nounderline")
			}
		}
		return strings.Join(parts, " ")
	}
	return chroma.MustNewStyle("atenea-diff", chroma.StyleEntries{
		chroma.Text:                entry(config.Text),
		chroma.Error:               entry(config.Error),
		chroma.Comment:             entry(config.Comment),
		chroma.CommentPreproc:      entry(config.CommentPreproc),
		chroma.Keyword:             entry(config.Keyword),
		chroma.KeywordReserved:     entry(config.KeywordReserved),
		chroma.KeywordNamespace:    entry(config.KeywordNamespace),
		chroma.KeywordType:         entry(config.KeywordType),
		chroma.Operator:            entry(config.Operator),
		chroma.Punctuation:         entry(config.Punctuation),
		chroma.Name:                entry(config.Name),
		chroma.NameBuiltin:         entry(config.NameBuiltin),
		chroma.NameTag:             entry(config.NameTag),
		chroma.NameAttribute:       entry(config.NameAttribute),
		chroma.NameClass:           entry(config.NameClass),
		chroma.NameConstant:        entry(config.NameConstant),
		chroma.NameDecorator:       entry(config.NameDecorator),
		chroma.NameException:       entry(config.NameException),
		chroma.NameFunction:        entry(config.NameFunction),
		chroma.NameOther:           entry(config.NameOther),
		chroma.Literal:             entry(config.Literal),
		chroma.LiteralNumber:       entry(config.LiteralNumber),
		chroma.LiteralDate:         entry(config.LiteralDate),
		chroma.LiteralString:       entry(config.LiteralString),
		chroma.LiteralStringEscape: entry(config.LiteralStringEscape),
		chroma.GenericDeleted:      entry(config.GenericDeleted),
		chroma.GenericEmph:         entry(config.GenericEmph),
		chroma.GenericInserted:     entry(config.GenericInserted),
		chroma.GenericStrong:       entry(config.GenericStrong),
		chroma.GenericSubheading:   entry(config.GenericSubheading),
	})
}()

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
	var oldPath string
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case len(hunks) == 0 && strings.HasPrefix(line, "+++ "):
			// File headers only appear before the first hunk; guarding on that
			// keeps an added line whose content is "++ …" (diff line "+++ …")
			// from being mistaken for the header once inside a hunk.
			path = diffHeaderPath(strings.TrimPrefix(line, "+++ "), "b/")
		case len(hunks) == 0 && strings.HasPrefix(line, "--- "):
			oldPath = diffHeaderPath(strings.TrimPrefix(line, "--- "), "a/")
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
	if path == "" || path == "/dev/null" {
		path = oldPath
	}
	return path, hunks, len(hunks) > 0
}

func diffHeaderPath(path, sidePrefix string) string {
	if path == "/dev/null" {
		return path
	}
	return strings.TrimPrefix(path, sidePrefix)
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

func renderPreviewFile(file tool.FileResult, width int) string {
	if file.Diff != "" {
		if card := renderEditDiff(file.Diff, width); card != "" {
			return card
		}
	}
	path := file.Path
	if file.Destination != "" {
		path += " → " + file.Destination
	}
	status := string(file.Operation)
	if file.Error != "" {
		status += ": " + file.Error
	}
	if len(file.Warnings) > 0 {
		status += " · " + strings.Join(file.Warnings, "; ")
	}
	if path == "" && status == "" {
		return ""
	}
	return metadataStyle.Render(activityRailPrefix + sanitizeTerminalText(path+" ["+status+"]"))
}

type editDiffRenderCacheKey struct {
	diff    string
	width   int
	profile termenv.Profile
}

var editDiffRenderCache = struct {
	sync.Mutex
	entries map[editDiffRenderCacheKey]string
}{}

const editDiffRenderCacheCapacity = 128

func renderEditDiff(diff string, width int) string {
	key := editDiffRenderCacheKey{diff: diff, width: width, profile: lipgloss.ColorProfile()}
	editDiffRenderCache.Lock()
	if rendered, ok := editDiffRenderCache.entries[key]; ok {
		editDiffRenderCache.Unlock()
		return rendered
	}
	editDiffRenderCache.Unlock()

	path, hunks, ok := parseUnifiedDiff(diff)
	rendered := ""
	if ok {
		gutter := diffGutterWidth(hunks)
		contentW, margin := cardInset(width)
		rows := []string{diffPathBar(diffPathStyle, path, contentW)}
		for _, h := range hunks {
			rows = append(rows, diffHunkBar(h, contentW))
			rows = append(rows, diffBlockRows(path, h, gutter, contentW, false)...)
			rows = append(rows, diffBlockRows(path, h, gutter, contentW, true)...) // after (added, green)
		}
		rendered = frameDiffCard(rows, margin)
	}

	editDiffRenderCache.Lock()
	if editDiffRenderCache.entries == nil {
		editDiffRenderCache.entries = make(map[editDiffRenderCacheKey]string, editDiffRenderCacheCapacity)
	}
	if len(editDiffRenderCache.entries) >= editDiffRenderCacheCapacity {
		clear(editDiffRenderCache.entries)
	}
	editDiffRenderCache.entries[key] = rendered
	editDiffRenderCache.Unlock()
	return rendered
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

func diffBlockRows(path string, h diffHunk, gutter, width int, added bool) []string {
	bandStyle, railStyle, keep := diffDelBandStyle, diffDelRailStyle, byte('-')
	bandColor := theme.DiffDelBg
	if added {
		bandStyle, railStyle, keep = diffAddBandStyle, diffAddRailStyle, '+'
		bandColor = theme.DiffAddBg
	}

	var source []string
	var backgrounds []string
	for _, line := range h.lines {
		switch line.kind {
		case keep:
			source = append(source, line.text)
			backgrounds = append(backgrounds, bandColor)
		case ' ':
			source = append(source, line.text)
			backgrounds = append(backgrounds, "")
		}
	}
	highlighted, hasSyntax := highlightDiffLines(path, source, backgrounds)

	num := h.oldStart
	if added {
		num = h.newStart
	}
	rows := make([]string, 0, len(source))
	lineIndex := 0
	for _, line := range h.lines {
		switch {
		case line.kind == keep:
			rows = append(rows, diffRow(railStyle, bandStyle, gutter, width, num, line.kind, highlighted[lineIndex], hasSyntax))
			num++
			lineIndex++
		case line.kind == ' ':
			rows = append(rows, diffRow(railStyle, diffCtxStyle, gutter, width, num, ' ', highlighted[lineIndex], hasSyntax))
			num++
			lineIndex++
		}
	}
	return rows
}

// highlightDiffLines lexes one complete before/after side of a hunk rather
// than individual rows, so multi-line comments and strings retain their state.
// The path is the complete vocabulary available to this pure renderer: unknown
// extensions deliberately fall back to the existing direction-colored text.
func highlightDiffLines(path string, lines, backgrounds []string) ([]string, bool) {
	safe := make([]string, len(lines))
	for i, line := range lines {
		safe[i] = sanitizeTerminalText(line)
	}
	if len(safe) == 0 || len(backgrounds) != len(safe) || lipgloss.ColorProfile() == termenv.Ascii {
		return safe, false
	}
	lexer := lexers.Match(path)
	if lexer == nil {
		return safe, false
	}
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, strings.Join(safe, "\n"))
	if err != nil {
		return safe, false
	}

	rendered := make([]strings.Builder, len(safe))
	line := 0
	for token := iterator(); token != chroma.EOF && line < len(rendered); token = iterator() {
		parts := strings.Split(token.Value, "\n")
		for i, part := range parts {
			if part != "" && line < len(rendered) {
				rendered[line].WriteString(renderDiffToken(token.Type, part, backgrounds[line]))
			}
			if i < len(parts)-1 {
				line++
			}
		}
	}
	out := make([]string, len(rendered))
	for i := range rendered {
		out[i] = rendered[i].String()
	}
	return out, true
}

func renderDiffToken(tokenType chroma.TokenType, text, background string) string {
	entry := diffSyntaxStyle.Get(tokenType)
	style := lipgloss.NewStyle()
	if entry.Colour.IsSet() {
		style = style.Foreground(lipgloss.Color(entry.Colour.String()))
	}
	if background != "" {
		style = style.Background(lipgloss.Color(background))
	}
	if entry.Bold == chroma.Yes {
		style = style.Bold(true)
	}
	if entry.Italic == chroma.Yes {
		style = style.Italic(true)
	}
	if entry.Underline == chroma.Yes {
		style = style.Underline(true)
	}
	return style.Render(text)
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
		rows = append(rows, diffRow(writeRailStyle, writeBandStyle, gutter, width, num, noMarker, sanitizeTerminalText(l.text), false))
		num++
	}
	return rows
}

// diffRow renders one row of a diff block: the colored rail bar, then the line
// number, marker and content. A changed row (band != diffCtxStyle) fills the
// full width as a colored band; a context row stays plain (no trailing fill).
// Content truncates with an ellipsis so the row is always exactly one line and
// the gutter stays aligned.
func diffRow(railStyle, bodyStyle lipgloss.Style, gutter, width, num int, marker byte, text string, syntaxHighlighted bool) string {
	// noMarker drops the +/- column entirely (the write card): line number, two
	// spaces, then the text. Otherwise the marker sits between the number and the
	// text as in a unified diff.
	prefix := fmt.Sprintf("%*d  ", gutter, num)
	if marker != noMarker {
		prefix = fmt.Sprintf("%*d %c ", gutter, num, marker)
	}
	railCells := ansi.StringWidth(diffRailGlyph)
	innerWidth := width - railCells
	if width > 0 && innerWidth < 1 {
		innerWidth = 1
	}

	if !syntaxHighlighted {
		inner := prefix + text
		if width > 0 {
			inner = ansi.Truncate(inner, innerWidth, "…")
			// Changed rows fill the band to the full width; context rows stay
			// unpadded so they read as plain text without a trailing block.
			if bodyStyle.GetBackground() != diffCtxStyle.GetBackground() {
				inner += strings.Repeat(" ", max(innerWidth-ansi.StringWidth(inner), 0))
			}
		}
		return railStyle.Render(diffRailGlyph) + bodyStyle.Render(inner)
	}

	// Syntax tokens carry their row background individually so their foreground
	// colors do not punch holes through the red/green band when they reset SGR.
	inner := bodyStyle.Render(prefix) + text
	if width > 0 {
		inner = ansi.Truncate(inner, innerWidth, bodyStyle.Render("…"))
		if bodyStyle.GetBackground() != diffCtxStyle.GetBackground() {
			inner += bodyStyle.Render(strings.Repeat(" ", max(innerWidth-ansi.StringWidth(inner), 0)))
		}
	}
	return railStyle.Render(diffRailGlyph) + inner
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
