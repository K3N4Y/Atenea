package tui

import (
	"reflect"
	"strings"

	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/K3N4Y/atenea/internal/tui/theme"
)

// markdownRuleWidth is the fixed width of the horizontal rule glyph run:
// glamour renders HR as a literal format string, so the rule cannot follow
// the terminal width. 40 cells reads as a deliberate separator at the usual
// widths without overflowing narrow terminals.
const markdownRuleWidth = 40

// markdownCodeBlockMarker brackets each rendered code block so
// paintCodeBlockBackgrounds can find it in glamour's output. NUL bytes never
// survive sanitizeTerminalText, so assistant content cannot forge a marker;
// the marker lines themselves are removed from the final render.
const markdownCodeBlockMarker = "\x00code\x00"

// markdownStyle is the TUI's own glamour theme for assistant markdown. It
// keeps editorial hierarchy neutral: headings use weight, lower levels use
// secondary gray, and links use underline. The document color stays nil so
// body text inherits the terminal default.
var markdownStyle = func() glamouransi.StyleConfig {
	str := func(v string) *string { return &v }
	yes := func() *bool { v := true; return &v }
	num := func(v uint) *uint { return &v }

	// Syntax colors reuse the stock dark chroma set (already curated for a
	// dark background), with the block background on EVERY token entry:
	// chroma's TTY formatters clear the style-level Background before
	// formatting, so a background only renders when each token carries its
	// own. The reflection loop covers every entry so none is left as a hole
	// in the block.
	chromaTheme := *styles.DarkStyleConfig.CodeBlock.Chroma
	entries := reflect.ValueOf(&chromaTheme).Elem()
	for i := 0; i < entries.NumField(); i++ {
		entry := entries.Field(i).Addr().Interface().(*glamouransi.StylePrimitive)
		entry.BackgroundColor = str(theme.CodeBlockHex)
	}

	return glamouransi.StyleConfig{
		Document: glamouransi.StyleBlock{
			StylePrimitive: glamouransi.StylePrimitive{BlockPrefix: "\n", BlockSuffix: "\n"},
			Margin:         num(2),
		},
		BlockQuote: glamouransi.StyleBlock{
			Indent:      num(1),
			IndentToken: str("│ "),
		},
		List: glamouransi.StyleList{LevelIndent: 2},
		Heading: glamouransi.StyleBlock{
			StylePrimitive: glamouransi.StylePrimitive{BlockSuffix: "\n\n", Bold: yes()},
		},
		H3:            glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: str(theme.Border)}},
		H4:            glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: str(theme.Border)}},
		H5:            glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: str(theme.Border)}},
		H6:            glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: str(theme.Border)}},
		Strikethrough: glamouransi.StylePrimitive{CrossedOut: yes()},
		Emph:          glamouransi.StylePrimitive{Italic: yes()},
		Strong:        glamouransi.StylePrimitive{Bold: yes()},
		HorizontalRule: glamouransi.StylePrimitive{
			Color:  str(theme.Border),
			Format: "\n" + strings.Repeat("─", markdownRuleWidth) + "\n",
		},
		Item:        glamouransi.StylePrimitive{BlockPrefix: "• "},
		Enumeration: glamouransi.StylePrimitive{BlockPrefix: ". "},
		Task:        glamouransi.StyleTask{Ticked: "[✓] ", Unticked: "[ ] "},
		// Links stay discoverable through underline without borrowing the focus
		// accent; editorial content remains neutral until it is selected.
		Link:      glamouransi.StylePrimitive{Underline: yes()},
		LinkText:  glamouransi.StylePrimitive{Underline: yes()},
		Image:     glamouransi.StylePrimitive{Underline: yes()},
		ImageText: glamouransi.StylePrimitive{Color: str(theme.Border), Format: "Image: {{.text}} →"},
		Code: glamouransi.StyleBlock{
			StylePrimitive: glamouransi.StylePrimitive{
				Color: str(theme.Border),
			},
		},
		// No CodeBlock margin: the block aligns with the body at the document
		// margin (column 2) instead of the stock extra indent (column 4). The
		// marker lines bracket the block for paintCodeBlockBackgrounds.
		CodeBlock: glamouransi.StyleCodeBlock{
			StyleBlock: glamouransi.StyleBlock{
				StylePrimitive: glamouransi.StylePrimitive{
					BlockPrefix: markdownCodeBlockMarker + "\n",
					BlockSuffix: markdownCodeBlockMarker + "\n",
				},
			},
			Chroma: &chromaTheme,
		},
	}
}()

var markdownDocMargin = func() int {
	if m := markdownStyle.Document.Margin; m != nil {
		return int(*m)
	}
	return 0
}()

// markdownCodeBlockPadStyle paints the spaces that square a code block line
// up to the width of the block's widest line, in the same background the
// chroma tokens carry.
var markdownCodeBlockPadStyle = lipgloss.NewStyle().Background(lipgloss.Color(theme.CodeBlock))

// paintCodeBlockBackgrounds squares up the background of every code block in
// glamour's rendered output. Chroma's TTY formatters only paint background
// behind the tokens they emit (the style-level background is cleared before
// formatting), which leaves each line's background ragged on the right; this
// pass pads every line of a block — bracketed by markdownCodeBlockMarker
// lines, which are dropped — with background-styled spaces up to the block's
// widest line. Glamour's own right-padding on wrapped code lines is unstyled,
// so it is trimmed before measuring and repainted by the pad; blank code
// lines lose their unstyled document margin to the same trim and get it back
// before the pad so the background never starts at column 0.
func paintCodeBlockBackgrounds(rendered string) string {
	if !strings.Contains(rendered, markdownCodeBlockMarker) {
		return rendered
	}
	isMarker := func(line string) bool { return strings.Contains(line, markdownCodeBlockMarker) }
	lines := strings.Split(rendered, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if !isMarker(lines[i]) {
			out = append(out, lines[i])
			continue
		}
		start := i + 1
		end := start
		for end < len(lines) && !isMarker(lines[end]) {
			end++
		}
		block := lines[start:end]
		width := 0
		for j, line := range block {
			line = strings.TrimRight(line, " ")
			if w := lipgloss.Width(line); w < markdownDocMargin {
				line += strings.Repeat(" ", markdownDocMargin-w)
			}
			block[j] = line
			if w := lipgloss.Width(line); w > width {
				width = w
			}
		}
		for _, line := range block {
			if pad := width - lipgloss.Width(line); pad > 0 {
				line += markdownCodeBlockPadStyle.Render(strings.Repeat(" ", pad))
			}
			out = append(out, line)
		}
		i = end // skip the closing marker; the loop increment moves past it
	}
	return strings.Join(out, "\n")
}

var markdownRendererCache struct {
	wrap     int
	profile  termenv.Profile
	renderer *glamour.TermRenderer
}

func markdownRenderer(wrap int) (*glamour.TermRenderer, error) {
	profile := lipgloss.ColorProfile()
	if markdownRendererCache.renderer != nil && markdownRendererCache.wrap == wrap && markdownRendererCache.profile == profile {
		return markdownRendererCache.renderer, nil
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle),
		glamour.WithWordWrap(wrap),
		glamour.WithColorProfile(profile),
	)
	if err != nil {
		return nil, err
	}
	markdownRendererCache.wrap = wrap
	markdownRendererCache.profile = profile
	markdownRendererCache.renderer = r
	return r, nil
}

// hardWrapOverflow hard-breaks only the lines whose display width exceeds the
// limit, leaving every other line — and its layout and color — byte-identical.
// glamour word-wraps but never splits a token longer than the wrap width, so a
// long URL, path, or code identifier overflows the viewport as a single line;
// the stock emergency word-wrap then re-broke it at column 0 with a blank line
// in front, orphaning the continuation out of rhythm. This breaks the overflow
// inside the line and re-indents every continuation to the line's own leading
// margin, so a wrapped long token stays aligned like any other wrapped line.
// ANSI-aware throughout: widths and breaks count display cells, and
// ansi.Hardwrap carries the active SGR state onto each continuation. limit <= 0
// disables wrapping.
func hardWrapOverflow(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	changed := false
	for i, line := range lines {
		if ansi.StringWidth(line) <= limit {
			continue
		}
		indent, body := splitLeadingSpaces(line)
		if indent >= limit {
			indent = 0
		}
		pad := strings.Repeat(" ", indent)
		segs := strings.Split(ansi.Hardwrap(body, limit-indent, false), "\n")
		for j := range segs {
			segs[j] = pad + segs[j]
		}
		lines[i] = strings.Join(segs, "\n")
		changed = true
	}
	if !changed {
		return s
	}
	return strings.Join(lines, "\n")
}

// splitLeadingSpaces peels a line's leading margin — the run of spaces before
// the first visible cell — off the rest, returning the margin's display width
// and the body with those spaces removed but every ANSI escape kept in place
// (so the body's color state, and thus each continuation's, is unchanged).
func splitLeadingSpaces(line string) (spaces int, body string) {
	var b strings.Builder
	i := 0
	for i < len(line) {
		if line[i] == 0x1b { // ESC: copy a CSI/SGR sequence through to its final byte
			j := i + 1
			if j < len(line) && line[j] == '[' { // skip the CSI intro before the params
				j++
			}
			for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) { // params/intermediates
				j++
			}
			if j < len(line) { // include the final byte (0x40–0x7e)
				j++
			}
			b.WriteString(line[i:j])
			i = j
			continue
		}
		if line[i] == ' ' {
			spaces++
			i++
			continue
		}
		break
	}
	b.WriteString(line[i:])
	return spaces, b.String()
}

func renderMarkdown(text string, width int) string {
	text = sanitizeTerminalText(text)
	wrap := width
	if wrap > 0 {
		wrap = max(wrap-markdownDocMargin, 0)
	}
	r, err := markdownRenderer(wrap)
	if err != nil {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	out = hardWrapOverflow(out, width)
	return strings.Trim(paintCodeBlockBackgrounds(out), "\n")
}
