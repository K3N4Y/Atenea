package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/command"
)

func skillsTestModel() Model {
	return NewModel(&fakeAgent{}, "s1", nil).WithCompletions([]command.Command{
		{Name: "alpha", Description: "Alpha skill", Skill: true},
		{Name: "mcp", Description: "builtin", BuiltIn: true},
		{Name: "remote", Description: "MCP prompt"},
		{Name: "zeta", Description: "Zeta skill", Skill: true},
	}, nil)
}

func TestModel_SkillsCommandOpensModalWithOnlySkillCommands(t *testing.T) {
	m := skillsTestModel()
	m.composer.input.SetValue("/skills")
	next, cmd := m.submitPrompt()
	if cmd != nil || !next.skillsPicker.open {
		t.Fatalf("submit /skills = open %v cmd %v", next.skillsPicker.open, cmd)
	}
	if len(next.skillsPicker.items) != 2 || next.skillsPicker.items[0].Name != "alpha" || next.skillsPicker.items[1].Name != "zeta" {
		t.Fatalf("picker items = %+v", next.skillsPicker.items)
	}
	if next.composer.value() != "" || next.activeInputTarget() != targetSkillsPicker {
		t.Fatalf("composer = %q target = %v", next.composer.value(), next.activeInputTarget())
	}
	view := next.View()
	if !strings.Contains(view, "Alpha skill") || strings.Contains(view, "builtin") || strings.Contains(view, "MCP prompt") {
		t.Fatalf("unexpected picker view:\n%s", view)
	}
}

func TestModel_SkillsPickerSelectsWithoutSending(t *testing.T) {
	m := skillsTestModel()
	m.skillsPicker = newSkillsPicker(m.commands)
	m.skillsPicker.move(-1)
	updated, cmd := m.handleSkillsPickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	next := updated.(Model)
	if cmd != nil || next.skillsPicker.open || next.composer.value() != "/zeta " {
		t.Fatalf("selection = open %v value %q cmd %v", next.skillsPicker.open, next.composer.value(), cmd)
	}
	if next.composer.input.Position() != len([]rune(next.composer.value())) {
		t.Fatalf("caret = %d, want end", next.composer.input.Position())
	}
}

func TestModel_SkillsPickerKeyboardMouseAndEmptyState(t *testing.T) {
	m := skillsTestModel()
	m.width, m.height = 80, 16
	m.skillsPicker = newSkillsPicker(m.commands)
	updated, _ := m.handleSkillsPickerKey(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.skillsPicker.selected != 1 {
		t.Fatalf("down selected = %d", m.skillsPicker.selected)
	}
	m, _ = m.handleSkillsPickerMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m.skillsPicker.selected != 0 {
		t.Fatalf("wheel down wrap selected = %d", m.skillsPicker.selected)
	}
	m, _ = m.handleSkillsPickerMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.skillsPicker.selected != 1 {
		t.Fatalf("wheel wrap selected = %d", m.skillsPicker.selected)
	}
	m.skillsPicker = newSkillsPicker(nil)
	if !strings.Contains(m.skillsPickerView(), "No skills available") {
		t.Fatal("empty picker has no stable empty state")
	}
	updated, _ = m.handleSkillsPickerKey(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(Model).skillsPicker.open {
		t.Fatal("escape did not close picker")
	}
}

func TestModel_SkillsPickerSanitizesHostileDisplayNameButPreservesInvocation(t *testing.T) {
	const hostileName = "unsafe\x1b[31m\r\nname"
	const hostileDescription = "description\x1b[2J\nsecond line\rthird"
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions([]command.Command{{
		Name: hostileName, Description: hostileDescription, Skill: true,
	}}, nil)
	m.width, m.height = 80, 16
	m.skillsPicker = newSkillsPicker(m.commands)
	if m.skillsPicker.items[0].Name != hostileName {
		t.Fatalf("picker mutated underlying name: %q", m.skillsPicker.items[0].Name)
	}
	for label, value := range map[string]string{
		"name": sanitizeSkillPickerText(hostileName), "description": sanitizeSkillPickerText(hostileDescription),
	} {
		if strings.ContainsAny(value, "\r\n") || strings.Contains(value, "\x1b") {
			t.Fatalf("sanitized %s is not safe for one row: %q", label, value)
		}
	}
	view := m.skillsPickerView()
	if strings.Count(view, "second line") != 1 || strings.Count(view, "third") != 1 {
		t.Fatalf("sanitized description was not rendered on its row: %q", view)
	}
	updated, cmd := m.handleSkillsPickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	next := updated.(Model)
	expected := newTestComposer().setValue("/" + hostileName + " ").value()
	if cmd != nil || next.composer.value() != expected {
		t.Fatalf("selection changed underlying invocation: %q", next.composer.value())
	}
}

func TestModel_SkillsPickerClickSelectsVisibleRow(t *testing.T) {
	m := skillsTestModel()
	m.width, m.height = 80, 16
	m.skillsPicker = newSkillsPicker(m.commands)
	next, cmd := m.handleSkillsPickerMouse(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      3,
		Y:      5,
	})
	if cmd != nil || next.skillsPicker.open || next.composer.value() != "/zeta " {
		t.Fatalf("click selection = open %v value %q cmd %v", next.skillsPicker.open, next.composer.value(), cmd)
	}
}

func TestModel_SkillsArgumentsAreRejectedAndSimilarCommandIsNotIntercepted(t *testing.T) {
	m := skillsTestModel()
	m.composer.input.SetValue("/skills extra")
	next, _ := m.submitPrompt()
	if next.skillsPicker.open {
		t.Fatal("/skills with arguments opened picker")
	}
	if _, ok := parseLocalCommand("/skillset"); ok {
		t.Fatal("similar command was parsed as /skills")
	}
}
