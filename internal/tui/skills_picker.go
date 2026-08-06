package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/command"
)

// skillsPicker is an in-terminal, full-canvas selector over discovered skills.
type skillsPicker struct {
	open  bool
	items []command.Command
	overlayList
}

func newSkillsPicker(commands []command.Command) skillsPicker {
	items := make([]command.Command, 0, len(commands))
	for _, cmd := range commands {
		if cmd.Skill {
			items = append(items, cmd)
		}
	}
	p := skillsPicker{open: true, items: items}
	p.setCount(len(items))
	return p
}

func (m Model) submitSkillsCommand(text string) (Model, tea.Cmd) {
	if text != "/skills" {
		return m.appendError("usage: /skills"), nil
	}
	m.composer = m.composer.clear()
	m.skillsPicker = newSkillsPicker(m.commands)
	return m.resizeViewport(), nil
}

func (m Model) handleSkillsPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.skillsPicker.open = false
		return m.resizeViewport(), nil
	case tea.KeyUp:
		m.skillsPicker.move(-1)
	case tea.KeyDown:
		m.skillsPicker.move(1)
	case tea.KeyEnter:
		return m.selectSkill()
	}
	return m, nil
}

func (m Model) handleSkillsPickerMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.skillsPicker.move(-1)
	case tea.MouseButtonWheelDown:
		m.skillsPicker.move(1)
	case tea.MouseButtonLeft:
		layout := overlayLayoutFor(m.width, m.height)
		row, ok := layout.rowAt(msg.X, msg.Y)
		if !ok {
			return m, nil
		}
		start, end := m.skillsPicker.window(layout.itemRows)
		index := start + row
		if index >= end {
			return m, nil
		}
		m.skillsPicker.selected = index
		return m.selectSkill()
	}
	return m, nil
}

func (m Model) selectSkill() (Model, tea.Cmd) {
	index, ok := m.skillsPicker.hasSelection()
	if !ok || index >= len(m.skillsPicker.items) {
		return m, nil
	}
	m.composer = m.composer.setValue("/" + m.skillsPicker.items[index].Name + " ")
	m.skillsPicker.open = false
	return m.resizeViewport(), nil
}

func (m Model) skillsPickerView() string {
	layout := overlayLayoutFor(m.width, m.height)
	rows := make([]string, 0, layout.itemRows)
	start, end := m.skillsPicker.window(layout.itemRows)
	for index := start; index < end; index++ {
		item := m.skillsPicker.items[index]
		prefix := "  "
		if index == m.skillsPicker.selected {
			prefix = "❯ "
		}
		row := overlayCell(prefix+"/"+sanitizeSkillPickerText(item.Name)+"  "+sanitizeSkillPickerText(item.Description), layout.innerWidth)
		if index == m.skillsPicker.selected {
			row = selectedRowStyle.Render(row)
		}
		rows = append(rows, row)
	}
	if len(m.skillsPicker.items) == 0 {
		rows = append(rows, metadataStyle.Render(overlayCell("  No skills available", layout.innerWidth)))
	}
	for len(rows) < layout.itemRows {
		rows = append(rows, strings.Repeat(" ", layout.innerWidth))
	}
	lines := []string{
		overlayCell(" Skill  Description", layout.innerWidth),
		strings.Repeat("─", layout.innerWidth),
	}
	lines = append(lines, rows...)
	lines = append(lines, strings.Repeat("─", layout.innerWidth), overlayCell(" ↑↓ move · enter select · esc close", layout.innerWidth))
	return m.renderOverlayPanel(layout, "Skills", lines)
}

// sanitizeSkillPickerText keeps each field inside its assigned picker row.
// General terminal text may contain intentional newlines; skill labels may not.
func sanitizeSkillPickerText(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(sanitizeTerminalText(value))
}
