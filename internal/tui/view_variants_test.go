package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestVariantsPickerView_OverlaysCompactModalWithoutReplacingChat(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m.branch = "visible-under-modal"
	m.variantsPicker.openAt("")

	view := m.View()
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "visible-under-modal") {
		t.Fatalf("View() hid the underlying chat canvas: %q", plain)
	}
	if !strings.Contains(plain, "default") || !strings.Contains(plain, "enter select") {
		t.Fatalf("View() missing variants modal: %q", plain)
	}
	if !strings.Contains(plain, "┌") || !strings.Contains(plain, "┐") ||
		!strings.Contains(plain, "└") || !strings.Contains(plain, "┘") {
		t.Fatalf("View() must use square modal corners: %q", plain)
	}
	if strings.Contains(plain, "╭") || strings.Contains(plain, "╮") ||
		strings.Contains(plain, "╰") || strings.Contains(plain, "╯") {
		t.Fatalf("View() still uses rounded modal corners: %q", plain)
	}
	lines := strings.Split(view, "\n")
	if len(lines) != 20 {
		t.Fatalf("View() lines = %d, want terminal height 20", len(lines))
	}
	for _, line := range lines {
		if width := lipgloss.Width(line); width != 60 {
			t.Fatalf("line width = %d, want 60: %q", width, ansi.Strip(line))
		}
	}
}

func TestPlaceModal_DoesNotLeakUnderlyingStylesIntoModal(t *testing.T) {
	base := strings.Repeat("\x1b[44mabcdefghij\x1b[0m\n", 4)
	modal := "\x1b[48;2;36;36;36m┌──┐\x1b[0m\n" +
		"\x1b[48;2;36;36;36m│ok│\x1b[0m\n" +
		"\x1b[48;2;36;36;36m└──┘\x1b[0m"
	got := placeModal(base, modal, 10, 4)
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(ansi.Strip(line), "│ok│") && !strings.Contains(line, "48;2;36;36;36") {
			t.Fatalf("modal background style was lost: %q", line)
		}
	}
}

func TestPlaceModal_PreservesCellsOutsideCenteredRectangle(t *testing.T) {
	base := strings.Join([]string{
		"abcdefghijkl",
		"mnopqrstuvwx",
		"ABCDEFGHIJKL",
		"MNOPQRSTUVWX",
		"123456789012",
	}, "\n")

	got := placeModal(base, "XX\nYY", 12, 5)
	lines := strings.Split(got, "\n")
	if lines[0] != "abcdefghijkl" || lines[4] != "123456789012" {
		t.Fatalf("rows outside modal changed: %q", got)
	}
	if lines[1] != "mnopqXXtuvwx" || lines[2] != "ABCDEYYHIJKL" {
		t.Fatalf("modal did not replace only its occupied cells: %q", got)
	}
}
