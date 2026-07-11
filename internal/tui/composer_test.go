package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestNewComposerInput_CursorLineIsTransparent(t *testing.T) {
	input := newComposerInput()
	for name, style := range map[string]lipgloss.Style{
		"focused": input.FocusedStyle.CursorLine,
		"blurred": input.BlurredStyle.CursorLine,
	} {
		if _, ok := style.GetBackground().(lipgloss.NoColor); !ok {
			t.Fatalf("%s CursorLine background = %v, want transparent", name, style.GetBackground())
		}
	}
}
