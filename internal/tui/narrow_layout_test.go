package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestModel_WorkingStatusFitsNarrowTerminalWithExplorerOpen(t *testing.T) {
	for _, w := range []int{40, 36, 30, 25, 22} {
		m := NewModel(&fakeAgent{}, "s1", nil)
		m = apply(t, m, tea.WindowSizeMsg{Width: w, Height: 24})
		// Start from the composer, as a user must: opening the explorer moves
		// keyboard focus to the tree and Enter there does not submit prompts.
		m = typeRunes(t, m, "hola")
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		m, _ = m.toggleTreeAsync()
		// provide loaded tree
		m.treeLoaded = true
		m.treeLoading = false
		m.tree = newFileTree([]string{"main.go"})
		if !m.showsWorking() {
			t.Fatalf("width=%d: not working", w)
		}
		view := m.View()
		contentWidth := m.contentWidth()
		// Find the status line (with spinner)
		for _, line := range strings.Split(view, "\n") {
			plain := ansi.Strip(line)
			if strings.Contains(plain, m.spinner.View()) {
				if lw := lipgloss.Width(line); lw > w {
					t.Fatalf("width=%d contentWidth=%d: status line %d cells > terminal %d: %q", w, contentWidth, lw, w, line)
				}
			}
			// also check no line exceeds terminal width
			if lw := lipgloss.Width(line); lw > w {
				t.Fatalf("width=%d: line %d cells > %d: %q", w, lw, w, line)
			}
		}
		t.Logf("width=%d contentWidth=%d ok", w, contentWidth)
	}
}
