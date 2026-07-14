package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const terminalTabWidth = 4

// sanitizeTerminalText removes terminal control sequences from untrusted text
// before any renderer can interpret them. Newlines remain structural, while
// tabs become spaces and all other C0/C1 controls are discarded.
func sanitizeTerminalText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = ansi.Strip(text)
	text = strings.ReplaceAll(text, "\t", strings.Repeat(" ", terminalTabWidth))
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
}
