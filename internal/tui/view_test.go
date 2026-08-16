package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// parseUnifiedDiff reads the path and hunk starts, and does not mistake an
// added line whose content is "++ foo" (diff line "+++ foo") for the file
// header once inside a hunk: file headers only precede the first hunk.
func TestParseUnifiedDiff_HunkStartsAndAddedTripledPlus(t *testing.T) {
	diff := "--- a/pkg/x.go\n+++ b/pkg/x.go\n@@ -5,1 +5,2 @@\n keep\n+++ foo"
	path, hunks, ok := parseUnifiedDiff(diff)
	if !ok || path != "pkg/x.go" {
		t.Fatalf("parseUnifiedDiff() path=%q ok=%v, want path %q ok true", path, ok, "pkg/x.go")
	}
	if len(hunks) != 1 || hunks[0].oldStart != 5 || hunks[0].newStart != 5 {
		t.Fatalf("hunks = %+v, want one hunk starting at old 5 / new 5", hunks)
	}
	want := []diffLine{{kind: ' ', text: "keep"}, {kind: '+', text: "++ foo"}}
	if len(hunks[0].lines) != len(want) {
		t.Fatalf("hunk lines = %+v, want %+v", hunks[0].lines, want)
	}
	for i, w := range want {
		if hunks[0].lines[i] != w {
			t.Fatalf("hunk line %d = %+v, want %+v: an added '++ foo' must not read as the +++ file header", i, hunks[0].lines[i], w)
		}
	}
}

// The markdown theme tests below assert over the ANSI-stripped render:
// colors follow the Ascii profile (tests run without a TTY) but glamour
// still emits attribute sequences (bold, underline), so structure —
// prefixes, padding, margins, glyphs — is what remains assertable.

func TestRenderMarkdown_HeadingsHaveNoLiteralHashPrefix(t *testing.T) {
	out := ansi.Strip(renderMarkdown("# One\n\n## Two\n\n### Three\n\n#### Four", 80))
	if strings.Contains(out, "#") {
		t.Fatalf("renderMarkdown() = %q, headings must not keep literal # prefixes: hierarchy comes from weight, not noise", out)
	}
	for _, title := range []string{"One", "Two", "Three", "Four"} {
		if !strings.Contains(out, title) {
			t.Fatalf("renderMarkdown() = %q, heading text %q must survive the render", out, title)
		}
	}
}

func TestRenderMarkdown_InlineCodeDoesNotFragmentProse(t *testing.T) {
	out := ansi.Strip(renderMarkdown("run `go vet` now", 80))
	if !strings.Contains(out, "run go vet now") {
		t.Fatalf("renderMarkdown() = %q, inline code must stay part of the sentence without visual padding", out)
	}
	if strings.Contains(out, "run  go vet  now") {
		t.Fatalf("renderMarkdown() = %q, inline code must not add a chip-like gap around the text", out)
	}
}

func TestRenderMarkdown_HeadingsSeparateSections(t *testing.T) {
	out := ansi.Strip(renderMarkdown("intro\n\n## Section\n\nbody", 80))
	lines := strings.Split(out, "\n")
	sectionLine := -1
	for i, line := range lines {
		if strings.Contains(line, "Section") {
			sectionLine = i
			break
		}
	}
	if sectionLine < 1 || strings.TrimSpace(lines[sectionLine-1]) != "" {
		t.Fatalf("renderMarkdown() = %q, a heading must be separated from the preceding section", out)
	}
	if sectionLine+1 >= len(lines) || strings.TrimSpace(lines[sectionLine+1]) != "" {
		t.Fatalf("renderMarkdown() = %q, a heading must leave one blank line before its section body", out)
	}
}

func TestRenderMarkdown_HorizontalRuleRendersAsSolidLine(t *testing.T) {
	out := ansi.Strip(renderMarkdown("before\n\n---\n\nafter", 80))
	if !strings.Contains(out, strings.Repeat("─", markdownRuleWidth)) {
		t.Fatalf("renderMarkdown() = %q, --- must render as a solid %d-cell ─ rule", out, markdownRuleWidth)
	}
	if strings.Contains(out, "--------") {
		t.Fatalf("renderMarkdown() = %q, the stock loose-dashes rule must be gone", out)
	}
}

const markdownTableTestDocument = "intro\n\n| Tool | Purpose | Notes |\n| --- | --- | --- |\n| read | **Read files** | ranges ok |\n| grep | Search | rg based |"

func renderedMarkdownTableLines(t *testing.T, rendered string) []string {
	t.Helper()
	lines := strings.Split(rendered, "\n")
	start, end := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "┌") {
			start = i
		}
		if start >= 0 && strings.Contains(line, "┘") {
			end = i
			break
		}
	}
	if start < 0 || end < start {
		t.Fatalf("rendered markdown = %q, want one framed table with top-left and bottom-right corners", rendered)
	}
	return lines[start : end+1]
}

func TestRenderMarkdown_TableHasCompleteOuterFrameAndExactMargins(t *testing.T) {
	for _, width := range []int{0, 20, 40, 80} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			lines := renderedMarkdownTableLines(t, ansi.Strip(renderMarkdown(markdownTableTestDocument, width)))
			if !strings.Contains(lines[0], "┌") || !strings.Contains(lines[0], "┬") || !strings.HasSuffix(lines[0], "┐") {
				t.Fatalf("table top = %q, want ┌/┬/┐ across the complete top edge", lines[0])
			}
			rule := lines[2]
			if !strings.Contains(rule, "├") || !strings.Contains(rule, "┼") || !strings.HasSuffix(rule, "┤") {
				t.Fatalf("table rule = %q, want ├/┼/┤ aligned with the outer sides", rule)
			}
			last := lines[len(lines)-1]
			if !strings.Contains(last, "└") || !strings.Contains(last, "┴") || !strings.HasSuffix(last, "┘") {
				t.Fatalf("table bottom = %q, want └/┴/┘ across the complete bottom edge", last)
			}

			rowWidth := ansi.StringWidth(lines[0])
			if width > 0 && rowWidth != width-markdownDocMargin {
				t.Fatalf("table width = %d at viewport width %d, want exactly %d: two cells on the left and two (not more) on the right", rowWidth, width, width-markdownDocMargin)
			}
			for i, line := range lines {
				if got := ansi.StringWidth(line); got != rowWidth {
					t.Fatalf("table row %d = %q has width %d, want uniform table width %d after framing", i, line, got, rowWidth)
				}
				if got := len(line) - len(strings.TrimLeft(line, " ")); got != markdownDocMargin {
					t.Fatalf("table row %d = %q starts at cell %d, want exact %d-cell left margin", i, line, got, markdownDocMargin)
				}
			}
			for i, line := range lines[1:] {
				if i == 1 || i == len(lines)-2 {
					continue // the internal rule is checked above
				}
				if !strings.HasPrefix(line, "  │") || !strings.HasSuffix(line, "│") {
					t.Fatalf("table data row = %q, want exterior vertical borders on both sides", line)
				}
			}
		})
	}
}

func TestRenderMarkdown_OneColumnAndHeaderOnlyTablesAreFramed(t *testing.T) {
	tests := []struct {
		name string
		md   string
		rows int
	}{
		{name: "one-column", md: "| Name |\n| --- |\n| Atenea |", rows: 5},
		{name: "header-only", md: "| Name | Value |\n| --- | --- |", rows: 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lines := renderedMarkdownTableLines(t, ansi.Strip(renderMarkdown(tc.md, 40)))
			if len(lines) != tc.rows {
				t.Fatalf("rendered table = %q has %d rows, want %d including complete outer frame", strings.Join(lines, "\n"), len(lines), tc.rows)
			}
			for i, line := range lines {
				if got := ansi.StringWidth(line); got != 40-markdownDocMargin {
					t.Fatalf("table row %d = %q has width %d, want exact two-cell margins", i, line, got)
				}
			}
		})
	}
}

func TestRenderMarkdown_TableOuterFramePreservesCellPadding(t *testing.T) {
	lines := renderedMarkdownTableLines(t, ansi.Strip(renderMarkdown(markdownTableTestDocument, 40)))
	for _, line := range lines {
		if !strings.Contains(line, "read") {
			continue
		}
		cells := strings.Split(strings.TrimPrefix(line, "  │"), "│")
		if len(cells) != 4 || cells[len(cells)-1] != "" {
			t.Fatalf("table row = %q, want three framed cells", line)
		}
		for i, cell := range cells[:len(cells)-1] {
			if !strings.HasPrefix(cell, " ") || !strings.HasSuffix(cell, " ") {
				t.Fatalf("table cell %d = %q in row %q, outer framing must preserve one-cell padding on both sides", i, cell, line)
			}
		}
		return
	}
	t.Fatalf("rendered table = %q, missing fixture data row", strings.Join(lines, "\n"))
}

func TestModel_SettledMarkdownTableHasTwoCanvasCellsOnEachSide(t *testing.T) {
	const width = 40
	m := settledAssistant(t, markdownTableTestDocument, width, 30)
	for _, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if !strings.Contains(line, "┌") || !strings.Contains(line, "┬") {
			continue
		}
		left := strings.Index(line, "┌")
		right := strings.LastIndex(line, "┐")
		if left < 0 || right < left {
			continue
		}
		leftCells := ansi.StringWidth(line[:left])
		rightCells := ansi.StringWidth(line[right+len("┐"):])
		if leftCells != markdownDocMargin || rightCells != markdownDocMargin {
			t.Fatalf("table top in full TUI = %q has margins left=%d right=%d, want exactly %d cells per side", line, leftCells, rightCells, markdownDocMargin)
		}
		return
	}
	t.Fatalf("View() = %q, missing rendered table top", ansi.Strip(m.View()))
}

func TestRenderMarkdown_TablePreservesCellContentAndEmphasis(t *testing.T) {
	rendered := renderMarkdown(markdownTableTestDocument, 40)
	plain := ansi.Strip(rendered)
	for _, cell := range []string{"Tool", "Purpose", "Notes", "read", "Read files", "ranges ok", "grep", "Search", "rg based"} {
		if !strings.Contains(plain, cell) {
			t.Fatalf("renderMarkdown() = %q, table cell %q must survive outer-frame post-processing", plain, cell)
		}
	}
	if !strings.Contains(rendered, "\x1b[1mRead files\x1b[0m") {
		t.Fatalf("renderMarkdown() = %q, bold cell content must retain its SGR sequence while borders stay external", rendered)
	}
}

func TestRenderMarkdown_TableSeparatorsUseMutedBorderColor(t *testing.T) {
	forceANSI256Profile(t)
	md := "| Value | Note |\n| --- | --- |\n| │ literal | text |"
	line := lineWith(t, renderMarkdown(md, 40), "literal")
	plain := ansi.Strip(line)
	if got := strings.Count(plain, "│"); got != 4 {
		t.Fatalf("table row = %q has %d vertical glyphs, want three structural separators plus one literal cell glyph", plain, got)
	}
	mutedSeparator := markdownTableBorderStyle.Render("│")
	if mutedSeparator == "│" {
		t.Fatal("ANSI256 profile did not render a visible muted border color")
	}
	if got := strings.Count(line, mutedSeparator); got != 3 {
		t.Fatalf("table row = %q has %d muted separators, want left, internal, and right borders muted while the literal │ remains content-colored", line, got)
	}
}

func TestRenderMarkdown_TableWideGraphemesKeepFrameAligned(t *testing.T) {
	const width = 40
	md := "| Emoji | CJK |\n| --- | --- |\n| 👩🏽‍💻 | 漢字 |"
	lines := renderedMarkdownTableLines(t, ansi.Strip(renderMarkdown(md, width)))
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != width-markdownDocMargin {
			t.Fatalf("Unicode table row %d = %q has display width %d, want %d with exact outer margins", i, line, got, width-markdownDocMargin)
		}
	}
	joined := strings.Join(lines, "\n")
	for _, cell := range []string{"👩🏽‍💻", "漢字"} {
		if !strings.Contains(joined, cell) {
			t.Fatalf("rendered Unicode table = %q, missing cell %q", joined, cell)
		}
	}
}

func TestRenderMarkdown_WrappedTableRowsKeepVerticalBorders(t *testing.T) {
	md := "| First | Second |\n| --- | --- |\n| a very long value that wraps across lines | short |\n| end | value |"
	lines := renderedMarkdownTableLines(t, ansi.Strip(renderMarkdown(md, 40)))
	foundContinuation := false
	for i, line := range lines {
		if strings.Contains(line, "that wraps across") || strings.Contains(line, "lines") {
			foundContinuation = true
		}
		if i == 0 || i == len(lines)-1 || i == 2 {
			continue // top/bottom/rule rows have horizontal geometry, not data borders
		}
		if strings.Count(line, "│") != 3 {
			t.Fatalf("wrapped table row = %q, every visual continuation must retain left, internal, and right borders", line)
		}
	}
	if !foundContinuation {
		t.Fatalf("rendered table = %q, fixture must produce a multi-line cell to exercise continuation borders", strings.Join(lines, "\n"))
	}
}

func TestRenderMarkdown_RightAlignedTableCellKeepsAlignment(t *testing.T) {
	md := "| Left | Right |\n| --- | ---: |\n| a | b |\n| longer | two |"
	line := lineWith(t, ansi.Strip(renderMarkdown(md, 40)), "Right")
	firstSeparator := strings.Index(line, "│")
	secondSeparator := strings.Index(line[firstSeparator+1:], "│")
	if firstSeparator < 0 || secondSeparator < 0 {
		t.Fatalf("right-aligned header row = %q, want exterior and internal separators", line)
	}
	secondSeparator += firstSeparator + 1
	if got := strings.Index(line, "Right"); got <= secondSeparator+2 {
		t.Fatalf("right-aligned header row = %q, content starts at display column %d; want padding before Right inside its cell", line, got)
	}
}

func TestRenderMarkdown_NoTableLeavesPlainRenderUnframed(t *testing.T) {
	got := ansi.Strip(renderMarkdown("plain prose", 0))
	if got != "  plain prose" {
		t.Fatalf("renderMarkdown() = %q, a document without a table must retain today's natural plain-text render byte-for-byte", got)
	}
	if strings.ContainsAny(got, "┌┐└┘├┤┬┴") {
		t.Fatalf("renderMarkdown() = %q, no-table output must not gain table border glyphs", got)
	}
}

func TestRenderMarkdown_CodeBlockBoxGlyphsStayLiteral(t *testing.T) {
	got := ansi.Strip(renderMarkdown("```\n│ literal box ─\n```", 0))
	line := lineWith(t, got, "literal box")
	if line != "  │ literal box ─" {
		t.Fatalf("code block line = %q, literal box glyphs must remain untouched while table framing scans around markers", line)
	}
	if strings.ContainsAny(got, "┌┐└┘├┤┬┴") {
		t.Fatalf("renderMarkdown() = %q, code-only content must not be mistaken for a table candidate", got)
	}
}

func TestRenderMarkdown_CodeBlockAlignsWithBodyMargin(t *testing.T) {
	out := ansi.Strip(renderMarkdown("text\n\n```go\npackage main\n```", 80))
	line := lineWith(t, out, "package main")
	if !strings.HasPrefix(line, "  package main") {
		t.Fatalf("code block line = %q, code must sit at the document margin (column 2), not the stock extra indent", line)
	}
}

func TestRenderMarkdown_CodeBlockMarkersNeverLeak(t *testing.T) {
	out := renderMarkdown("text\n\n```go\npackage main\n```\n\nmore text\n\n```\nplain\n```", 80)
	if strings.Contains(out, "\x00") {
		t.Fatalf("renderMarkdown() = %q, the internal code block markers must never reach the rendered output", out)
	}
}

func TestRenderMarkdown_CodeBlockLinesSharePaddedWidth(t *testing.T) {
	out := ansi.Strip(renderMarkdown("```go\nx := 1\n\nlongerLineOfCode := somethingMuchLonger\n```", 80))
	short := lineWith(t, out, "x := 1")
	long := lineWith(t, out, "longerLineOfCode")
	if len(short) != len(long) {
		t.Fatalf("short line %q (len %d) vs long line %q (len %d): every code block line must be padded to the block's widest line so the background forms a rectangle", short, len(short), long, len(long))
	}
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.Contains(line, "x := 1") {
			if blank := lines[i+1]; strings.TrimSpace(blank) != "" || len(blank) != len(line) {
				t.Fatalf("line after %q = %q, the blank code line must be padded to the same width, margin included", line, blank)
			}
		}
	}
}

// assertWrappedInRhythm checks the two failures the emergency wrap used to
// cause on tokens longer than the wrap width: a line overflowing the viewport,
// and a continuation orphaned flush-left (column 0) after a blank line. Every
// non-empty visible line must fit the width and keep the document margin.
func assertWrappedInRhythm(t *testing.T, out string, width int) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if ansi.StringWidth(line) > width {
			t.Fatalf("line %q width %d exceeds viewport width %d: a token longer than the wrap width must be hard-broken", line, ansi.StringWidth(line), width)
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, strings.Repeat(" ", markdownDocMargin)) {
			t.Fatalf("line %q lost the %d-cell margin: a wrapped continuation must stay indented, not orphan at column 0", line, markdownDocMargin)
		}
	}
}

func TestRenderMarkdown_LongURLWrapsInRhythm(t *testing.T) {
	const width = 40
	md := "See [link](https://example.com/a/very/long/path/that/exceeds/the/viewport/width) here"
	out := ansi.Strip(renderMarkdown(md, width))
	assertWrappedInRhythm(t, out, width)
	for _, seg := range []string{"https://example.", "very/long", "viewport"} {
		if !strings.Contains(out, seg) {
			t.Fatalf("renderMarkdown() = %q, the URL segment %q must survive the wrap", out, seg)
		}
	}
}

func TestRenderMarkdown_LongCodeTokenWrapsInRhythm(t *testing.T) {
	const width = 40
	md := "```\nshort\nhttps://example.com/a/very/long/path/that/exceeds/the/width\nend\n```"
	out := ansi.Strip(renderMarkdown(md, width))
	assertWrappedInRhythm(t, out, width)
	// The overflow does not drag the whole block wider than the viewport: every
	// code line, padded to the block's widest, still fits.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "short") && ansi.StringWidth(line) > width {
			t.Fatalf("code line %q width %d: an unbreakable token must not pad the whole block past the viewport", line, ansi.StringWidth(line))
		}
	}
}

func TestRenderMarkdown_DocumentMarginStaysConsistent(t *testing.T) {
	if markdownDocMargin != 2 {
		t.Fatalf("markdownDocMargin = %d, the theme must keep the 2-cell document margin renderMarkdown discounts from the wrap width", markdownDocMargin)
	}
	out := ansi.Strip(renderMarkdown("# Title\n\nparagraph\n\n- item", 80))
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("line %q of %q, every rendered line must keep the 2-cell document margin", line, out)
		}
	}
}

func TestRenderMarkdown_CacheReturnsStableOutputForWidthAndProfile(t *testing.T) {
	text := "A settled assistant response with `inline code`."
	profile := lipgloss.ColorProfile()

	first := renderMarkdown(text, 80)
	second := renderMarkdown(text, 80)
	if first != second {
		t.Fatalf("cached render differs for profile %v: first %q, second %q", profile, first, second)
	}
}

func TestRenderMarkdown_CacheUsesWidthInKey(t *testing.T) {
	text := "A settled assistant response containing a deliberately long sentence that must wrap."
	narrow := renderMarkdown(text, 32)
	wide := renderMarkdown(text, 80)
	if narrow == wide {
		t.Fatalf("renders for different widths should differ: %q", narrow)
	}
	assertWrappedInRhythm(t, ansi.Strip(narrow), 32)
	if strings.Contains(ansi.Strip(wide), "\n") && ansi.StringWidth(strings.Split(ansi.Strip(wide), "\n")[0]) > 80 {
		t.Fatalf("wide render exceeds requested width: %q", wide)
	}
}

func BenchmarkRenderMarkdown_Cached(b *testing.B) {
	const text = "A settled assistant response with **stable Markdown** and a link."
	for i := 0; i < b.N; i++ {
		renderMarkdown(text, 80)
	}
}

func TestMenuView_RendersCommandsInsideRectangularBox(t *testing.T) {
	m := Model{composer: composer{
		menuItems: []menuItem{{label: "/new", description: "Start a new session"}},
	}}

	view := ansi.Strip(m.menuView())
	if !strings.Contains(view, "┌") || !strings.Contains(view, "┐") ||
		!strings.Contains(view, "└") || !strings.Contains(view, "┘") {
		t.Fatalf("menuView() = %q, want a rectangular command box", view)
	}
	if strings.Contains(view, "╭") || strings.Contains(view, "╮") ||
		strings.Contains(view, "╰") || strings.Contains(view, "╯") {
		t.Fatalf("menuView() = %q, want square corners rather than rounded corners", view)
	}
	for _, line := range strings.Split(strings.TrimSuffix(view, "\n"), "\n") {
		if !strings.HasPrefix(line, "┌") && !strings.HasPrefix(line, "│") &&
			!strings.HasPrefix(line, "└") {
			t.Fatalf("menu line = %q, rectangular box geometry is invalid", line)
		}
	}
}
func TestMenuView_PutsSelectionInsideTheRectangle(t *testing.T) {
	m := Model{composer: composer{
		menuItems: []menuItem{{label: "/new", description: "Start a new session"}},
	}}

	lines := strings.Split(strings.TrimSuffix(ansi.Strip(m.menuView()), "\n"), "\n")
	var top, selected string
	for _, line := range lines {
		if strings.Contains(line, "┌") {
			top = line
		}
		if strings.Contains(line, "❯") {
			selected = line
		}
	}
	if !strings.Contains(selected, "│ ❯ ") {
		t.Fatalf("selected menu line = %q, want the selector inside the left border", selected)
	}
	if strings.Index(top, "┌") != strings.Index(selected, "│") {
		t.Fatalf("top border = %q and selected row = %q, rectangle sides must align", top, selected)
	}
}

func TestMenuView_DoesNotHideTopBarWhenMenuOpens(t *testing.T) {
	m := NewModel(nil, "s1", nil).
		WithWorkspace("main", "~/workspace").
		WithCompletions(nil, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m.composer.menuItems = []menuItem{{label: "/new", description: "Start a new session"}}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "main") {
		t.Fatalf("View() = %q, opening the slash menu must not remove the top bar", view)
	}
	lines := strings.Split(view, "\n")
	top, selected := -1, -1
	for index, line := range lines {
		if strings.Contains(line, "┌") && top < 0 {
			top = index
		}
		if strings.Contains(line, "│ ❯ ") {
			selected = index
		}
	}
	if top < 0 || selected < 0 || len(lines[top])-len(strings.TrimLeft(lines[top], " ")) != len(lines[selected])-len(strings.TrimLeft(lines[selected], " ")) {
		t.Fatalf("menu borders are not aligned in ready view:\n%s", view)
	}
}
