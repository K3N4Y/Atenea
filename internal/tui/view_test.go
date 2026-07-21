package tui

import (
	"strings"
	"testing"

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
	out := ansi.Strip(renderMarkdown("# Uno\n\n## Dos\n\n### Tres\n\n#### Cuatro", 80))
	if strings.Contains(out, "#") {
		t.Fatalf("renderMarkdown() = %q, headings must not keep literal # prefixes: hierarchy comes from weight, not noise", out)
	}
	for _, title := range []string{"Uno", "Dos", "Tres", "Cuatro"} {
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
	out := ansi.Strip(renderMarkdown("intro\n\n## Seccion\n\ncuerpo", 80))
	lines := strings.Split(out, "\n")
	sectionLine := -1
	for i, line := range lines {
		if strings.Contains(line, "Seccion") {
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
	out := ansi.Strip(renderMarkdown("antes\n\n---\n\ndespues", 80))
	if !strings.Contains(out, strings.Repeat("─", markdownRuleWidth)) {
		t.Fatalf("renderMarkdown() = %q, --- must render as a solid %d-cell ─ rule", out, markdownRuleWidth)
	}
	if strings.Contains(out, "--------") {
		t.Fatalf("renderMarkdown() = %q, the stock loose-dashes rule must be gone", out)
	}
}

func TestRenderMarkdown_CodeBlockAlignsWithBodyMargin(t *testing.T) {
	out := ansi.Strip(renderMarkdown("texto\n\n```go\npackage main\n```", 80))
	line := lineWith(t, out, "package main")
	if !strings.HasPrefix(line, "  package main") {
		t.Fatalf("code block line = %q, code must sit at the document margin (column 2), not the stock extra indent", line)
	}
}

func TestRenderMarkdown_CodeBlockMarkersNeverLeak(t *testing.T) {
	out := renderMarkdown("texto\n\n```go\npackage main\n```\n\nmas texto\n\n```\nplain\n```", 80)
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
	md := "Ver [enlace](https://example.com/una/ruta/larguisima/que/excede/de/sobra/el/ancho/del/viewport) aqui"
	out := ansi.Strip(renderMarkdown(md, width))
	assertWrappedInRhythm(t, out, width)
	for _, seg := range []string{"https://example.", "larguisima", "viewport"} {
		if !strings.Contains(out, seg) {
			t.Fatalf("renderMarkdown() = %q, the URL segment %q must survive the wrap", out, seg)
		}
	}
}

func TestRenderMarkdown_LongCodeTokenWrapsInRhythm(t *testing.T) {
	const width = 40
	md := "```\nshort\nhttps://example.com/una/ruta/larguisima/que/excede/de/sobra/el/ancho\nfin\n```"
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
	out := ansi.Strip(renderMarkdown("# Titulo\n\nparrafo\n\n- item", 80))
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("line %q of %q, every rendered line must keep the 2-cell document margin", line, out)
		}
	}
}
