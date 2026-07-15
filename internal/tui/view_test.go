package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

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

func TestRenderMarkdown_InlineCodeKeepsPadding(t *testing.T) {
	out := ansi.Strip(renderMarkdown("run `go vet` now", 80))
	if !strings.Contains(out, " go vet ") {
		t.Fatalf("renderMarkdown() = %q, inline code must keep one space of padding on each side", out)
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
