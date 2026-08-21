package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestVariantsPickerStateTracksCurrentAndClearsAgentTarget(t *testing.T) {
	var picker variantsPicker
	picker.openAgent("review", "openai", "gpt-5", llm.ReasoningEffortHigh)
	if picker.agent == nil || picker.agent.name != "review" {
		t.Fatalf("agent target = %#v", picker.agent)
	}
	if got, ok := picker.selectedEffort(); !ok || got != llm.ReasoningEffortHigh {
		t.Fatalf("selected effort = %q, %v; want high, true", got, ok)
	}

	picker.openAt(llm.ReasoningEffortLow)
	if picker.agent != nil {
		t.Fatalf("global open retained agent target: %#v", picker.agent)
	}
	if picker.current != llm.ReasoningEffortLow {
		t.Fatalf("current effort = %q, want low", picker.current)
	}

	picker.openAt("")
	picker.move(-1)
	if got, ok := picker.selectedEffort(); !ok || got != llm.ReasoningEffortMax {
		t.Fatalf("wrapped effort = %q, %v; want max, true", got, ok)
	}
	picker.close()
	if picker.open || picker.agent != nil || picker.count != 0 {
		t.Fatalf("closed picker retained state: %#v", picker)
	}
}

func TestVariantsPickerConfirmGlobalSelectionSyncsTranscript(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m.variantsPicker.openAt("")
	m.variantsPicker.selected = 4 // high

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.variantsPicker.open {
		t.Fatal("picker remained open after confirmation")
	}
	if fake.reasoning != llm.ReasoningEffortHigh {
		t.Fatalf("reasoning effort = %q, want high", fake.reasoning)
	}
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "reasoning effort: high") {
		t.Fatalf("confirmation notice was not synchronized into the view:\n%s", plain)
	}
}

type failingReasoningAgent struct {
	*fakeAgent
	err error
}

func (a *failingReasoningAgent) SetReasoningEffort(llm.ReasoningEffort) error {
	return a.err
}

func TestVariantsPickerConfirmErrorSyncsTranscript(t *testing.T) {
	fake := &failingReasoningAgent{fakeAgent: &fakeAgent{}, err: errors.New("save reasoning preference")}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m.variantsPicker.openAt("")
	m.variantsPicker.selected = 2

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "save reasoning preference") {
		t.Fatalf("selection error was not synchronized into the view:\n%s", plain)
	}
}

func TestVariantsPickerInvalidSelectionIsInert(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m.variantsPicker.openAt("")
	m.variantsPicker.selected = len(reasoningVariants)

	updated, cmd := m.handleVariantsPickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil || !m.variantsPicker.open {
		t.Fatalf("invalid selection changed picker: cmd=%v picker=%#v", cmd != nil, m.variantsPicker)
	}
}

func TestVariantsPickerMouseNavigatesSelectsAndBlocksChat(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m.variantsPicker.openAt("")

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if got, _ := m.variantsPicker.selectedEffort(); got != llm.ReasoningEffortMinimal {
		t.Fatalf("wheel-selected effort = %q, want minimal", got)
	}

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 0})
	if !m.variantsPicker.open || m.selection != nil {
		t.Fatalf("outside modal click reached chat: open=%v selection=%#v", m.variantsPicker.open, m.selection)
	}

	layout := variantsPickerLayoutFor(m.width, m.height, false)
	start, _ := m.variantsPicker.window(layout.itemRows)
	highRow := 4 - start
	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      layout.left + 1,
		Y:      layout.top + layout.itemOffset + highRow,
	})
	if m.variantsPicker.open || fake.reasoning != llm.ReasoningEffortHigh {
		t.Fatalf("mouse confirmation: open=%v effort=%q", m.variantsPicker.open, fake.reasoning)
	}
}

func TestVariantsPickerViewShowsAgentContextAndCurrentEffort(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m.variantsPicker.openAgent("review", "openai", "gpt-5", llm.ReasoningEffortHigh)

	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "Reasoning effort") || !strings.Contains(plain, "review · openai/gpt-5") {
		t.Fatalf("agent context is missing from modal:\n%s", plain)
	}
	if !strings.Contains(plain, "● high") {
		t.Fatalf("current effort is not marked independently:\n%s", plain)
	}
}

func TestVariantsPickerViewKeepsSelectionVisibleInSmallTerminal(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 24, Height: 6})
	m.variantsPicker.openAt(llm.ReasoningEffortMax)

	view := m.View()
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "max") {
		t.Fatalf("selected effort was clipped from compact modal:\n%s", plain)
	}
	lines := strings.Split(view, "\n")
	if len(lines) != 6 {
		t.Fatalf("View() lines = %d, want 6", len(lines))
	}
	for _, line := range lines {
		if width := lipgloss.Width(line); width != 24 {
			t.Fatalf("line width = %d, want 24: %q", width, ansi.Strip(line))
		}
	}
}
