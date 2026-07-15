---
updated_at: 2026-07-14
summary: Design of the TUI's own glamour markdown theme for assistant responses.
---

# TUI Markdown Theme

Settled (and live-revealed) assistant responses in the terminal UI render
through glamour with a theme owned by Atenea (`markdownStyle` in
`internal/tui/view.go`), replacing glamour's stock dark theme, whose look
(indigo H1 chip, literal `##`/`###` prefixes, `--------` rules, double-styled
links, unaligned code blocks) clashed with the TUI identity.

## Palette

Only the ANSI colors the rest of the TUI already uses over the `#141414`
canvas:

- Accent: ANSI `6` (H1, links).
- Muted: ANSI `8` (H3–H6, horizontal rule).
- Subtle background: ANSI `236` / `#303030` (inline code and code blocks).
- Everything else inherits the terminal default foreground (the document
  color is deliberately unset).

No fixed 256-color accents (the stock 63/228/39/203 are all gone).

## Rules

- **Headings**: hierarchy by weight, not noise. No literal `#` prefixes, no
  background chip. H1 bold accent, H2 bold, H3–H6 bold gray (`8`; glamour's
  `renderText` ignores the `Faint` field, so gray stands in for faint).
- **Inline code**: background `236`, terminal-default foreground, one space
  of padding on each side.
- **Code blocks**: aligned with the body at the document margin (column 2,
  no extra indent), stock dark chroma syntax colors, and their own `#303030`
  background as a full rectangle. Chroma's TTY formatters clear the
  style-level background, so the theme puts the background on every chroma
  token entry and `paintCodeBlockBackgrounds` squares the ragged right edge
  by padding each line of the block (bracketed by internal NUL marker lines,
  removed from the output) to the block's widest line.
- **Links**: one quiet style for text and URL alike — underlined accent.
- **Horizontal rule**: a solid run of 40 `─` in gray, not loose dashes.
- **Blockquote**: the existing quiet `│ ` rail.
- **Lists**: `•` bullets, 2-cell nesting indent.
- **Tables**: default cell padding next to the `│` separators (glamour
  stretches tables to the wrap width; library behavior).

## Constraints

- The `renderMarkdown` contract stays: one memoized renderer per wrap width
  (and color profile), document margin read from the style itself.
- In the Ascii profile (tests, no TTY) the render degrades to plain
  contiguous text so content stays assertable; behavior tests live in
  `internal/tui/view_test.go`.
- Requires glamour v1.0.0 (v0.8.0 wraps long list items and URLs past the
  terminal width). glamour word-wraps but never splits a token longer than the
  wrap width, so a long URL, path, or code identifier still overflows as one
  line; `hardWrapOverflow` hard-breaks only those lines (before the code-block
  background pass, and again in `syncViewport`), re-indenting each continuation
  to the line's own margin so it stays in rhythm instead of orphaning at
  column 0. Known upstream leftover: a blockquote that wraps at narrow widths
  can drop a word onto an unrailed continuation line.
