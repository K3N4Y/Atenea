package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// branchGlyph is the powerline branch glyph that precedes the git branch name
// in the top bar (nerd-font PUA, like the tree icons in tree.go).
const branchGlyph = ""

// bodyHeight is the body's vertical space (chat/tree/viewer): the terminal
// height minus the top-bar chrome. The layout module computes it
// (computeLayout); this method is a thin seam that reads it. It uses baseLayout
// (not layout) because reservedLines depends on bodyHeight: the permission
// panel sizes against it, and layout() requests reservedLines — reading it here
// would recurse.
func (m Model) bodyHeight() int { return m.baseLayout().bodyHeight }

// topBar renders the top-bar chrome: topBarMargin blank rows, the bar row, and
// another topBarMargin blank rows, all with the canvas background. This keeps
// the bar separated from the terminal edge and body by the same margin the
// composer uses on its sides.
func (m Model) topBar() string {
	width := m.baseLayout().width
	blank := restoreCanvasBackground(canvasStyle.Width(width).Render(""))
	rows := make([]string, 0, topBarHeight)
	for range topBarMargin {
		rows = append(rows, blank)
	}
	rows = append(rows, m.topBarLine())
	for range topBarMargin {
		rows = append(rows, blank)
	}
	return strings.Join(rows, "\n")
}

// topBarLine builds the full-width bar content row: the git branch (with its
// glyph) and working directory on the left, and context usage (used / window)
// on the right. It applies the shared canvas background (#141414) and restores
// it after inner style resets, like the rest of the view.
func (m Model) topBarLine() string {
	left := ""
	if m.branch != "" {
		left = metadataStyle.Render(branchGlyph + " " + sanitizeTerminalText(m.branch))
	}
	if m.workDir != "" {
		if left != "" {
			left += "  " + metadataStyle.Render(sanitizeTerminalText(m.workDir))
		} else {
			left = metadataStyle.Render(sanitizeTerminalText(m.workDir))
		}
	}
	right := m.topBarContext()
	// The same external horizontal margin as the composer and user messages
	// (composerOuterMargin): content does not touch the terminal edges and the
	// branch aligns with the composer's box. The width/2 clamp (so the bar keeps
	// its exact width on tiny terminals) lives in the layout module; this only
	// reads the margin and inner width.
	l := m.baseLayout()
	width := l.width
	margin := l.topBarMarginCells
	inner := l.topBarInnerWidth
	if lipgloss.Width(left)+lipgloss.Width(right)+1 > inner {
		// The right context label must always fit: truncate the left side
		// (branch + directory), leaving at least one space.
		left = ansi.Truncate(left, max(inner-lipgloss.Width(right)-1, 0), "…")
	}
	gap := max(inner-lipgloss.Width(left)-lipgloss.Width(right), 0)
	pad := strings.Repeat(" ", margin)
	line := pad + left + strings.Repeat(" ", gap) + right + pad
	return restoreCanvasBackground(canvasStyle.Width(width).Render(line))
}

// topBarContext builds the context-usage label on the right side of the bar:
// used input tokens and, when the model has a known window, the total window
// (used / window). Without usage it returns "" and the bar shows nothing.
func (m Model) topBarContext() string {
	if m.usage == nil {
		return ""
	}
	used := formatTokenCount(m.usage.InputTokens)
	if window := m.contextWindowLabel(); window != "" {
		return metadataStyle.Render(used + " / " + window)
	}
	return metadataStyle.Render(used)
}

// contextWindowLabel returns the active model's context window as a label
// ("256k"), or "" when its adapter does not declare one.
func (m Model) contextWindowLabel() string {
	if window, ok := m.contextWindow(m.model); ok {
		return strings.ReplaceAll(formatContextWindowTokens(window), "K", "k")
	}
	return ""
}
