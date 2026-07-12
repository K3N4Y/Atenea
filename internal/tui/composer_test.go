package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestNewComposerInput_UsesCalmCursorBlinkSpeed(t *testing.T) {
	input := newComposerInput()
	if got, want := input.Cursor.BlinkSpeed, 700*time.Millisecond; got != want {
		t.Fatalf("Cursor.BlinkSpeed = %s, want %s", got, want)
	}
}

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
