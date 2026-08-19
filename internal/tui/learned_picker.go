package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/learning"
)

// learningAgent is the optional Engine surface used by /learn and /learned.
// Every operation may touch durable storage, so the Model invokes it only from
// a tea.Cmd.
type learningAgent interface {
	Learn(sessionID string) (learning.Run, error)
	LearningAudit() ([]learning.Run, []learning.Lesson, error)
	ApproveLearning(runID string, candidate learning.Candidate) (learning.Lesson, error)
	RejectLearning(runID string) error
	CancelLearning(runID string) error
	RetryLearning(runID string) (learning.Run, error)
	SetLessonEnabled(lessonID string, enabled bool) error
	DeleteLesson(lessonID string) error
}

type learnDoneMsg struct {
	run learning.Run
	err string
}

type learnedAuditDoneMsg struct {
	generation uint64
	runs       []learning.Run
	lessons    []learning.Lesson
	err        string
}

type learnedActionDoneMsg struct {
	generation uint64
	key        string
	err        string
}

type learnedPicker struct {
	open    bool
	loading bool
	runs    []learning.Run
	lessons []learning.Lesson
	overlayList
	busy map[string]bool
	err  string
}

func newLearnedPicker() learnedPicker {
	return learnedPicker{open: true, loading: true, busy: make(map[string]bool)}
}

func (p *learnedPicker) set(runs []learning.Run, lessons []learning.Lesson) {
	key, hadSelection := p.selectedKey()
	p.runs = runs
	p.lessons = lessons
	p.loading = false
	p.err = ""
	p.selected = 0
	p.setCount(len(runs) + len(lessons))
	if !hadSelection {
		return
	}
	for index := range p.runs {
		if learnedRunKey(p.runs[index].ID) == key {
			p.selected = index
			return
		}
	}
	for index := range p.lessons {
		if learnedLessonKey(p.lessons[index].ID) == key {
			p.selected = len(p.runs) + index
			return
		}
	}
}

func (p learnedPicker) selectedKey() (string, bool) {
	index, ok := p.hasSelection()
	if !ok {
		return "", false
	}
	if index < len(p.runs) {
		return learnedRunKey(p.runs[index].ID), true
	}
	index -= len(p.runs)
	if index >= len(p.lessons) {
		return "", false
	}
	return learnedLessonKey(p.lessons[index].ID), true
}

func (p learnedPicker) selectedRun() (learning.Run, bool) {
	index, ok := p.hasSelection()
	if !ok || index >= len(p.runs) {
		return learning.Run{}, false
	}
	return p.runs[index], true
}

func (p learnedPicker) selectedLesson() (learning.Lesson, bool) {
	index, ok := p.hasSelection()
	index -= len(p.runs)
	if !ok || index < 0 || index >= len(p.lessons) {
		return learning.Lesson{}, false
	}
	return p.lessons[index], true
}

func learnedRunKey(id string) string    { return "run:" + id }
func learnedLessonKey(id string) string { return "lesson:" + id }

func (m Model) submitLearnCommand(command string) (Model, tea.Cmd) {
	if command != "/learn" {
		return m.appendError("usage: /learn"), nil
	}
	controller, ok := m.agent.(learningAgent)
	if !ok {
		return m.appendError("learning is unavailable"), nil
	}
	m.composer = m.composer.clear()
	sessionID := m.sessionID
	return m, func() tea.Msg {
		run, err := controller.Learn(sessionID)
		done := learnDoneMsg{run: run}
		if err != nil {
			done.err = err.Error()
		}
		return done
	}
}

func (m Model) submitLearnedCommand(command string) (Model, tea.Cmd) {
	if command != "/learned" {
		return m.appendError("usage: /learned"), nil
	}
	if _, ok := m.agent.(learningAgent); !ok {
		return m.appendError("learning is unavailable"), nil
	}
	m.composer = m.composer.clear()
	m.learnedGen++
	m.learnedPicker = newLearnedPicker()
	return m.resizeViewport().beginLearningAudit()
}

func (m Model) beginLearningAudit() (Model, tea.Cmd) {
	controller, ok := m.agent.(learningAgent)
	if !ok {
		return m, nil
	}
	m.learnedAuditGen++
	m.learnedPicker.loading = true
	m.learnedPicker.err = ""
	generation := m.learnedAuditGen
	return m, func() tea.Msg {
		runs, lessons, err := controller.LearningAudit()
		done := learnedAuditDoneMsg{generation: generation, runs: runs, lessons: lessons}
		if err != nil {
			done.err = err.Error()
		}
		return done
	}
}

func (m Model) handleLearnedPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.learnedPicker.open = false
		return m.resizeViewport(), nil
	case tea.KeyUp:
		m.learnedPicker.move(-1)
		return m, nil
	case tea.KeyDown:
		m.learnedPicker.move(1)
		return m, nil
	case tea.KeyEnter, tea.KeySpace:
		return m.applyLearnedPrimaryAction()
	}
	switch keyRune(msg) {
	case " ":
		return m.applyLearnedPrimaryAction()
	case "d":
		return m.applyLearnedDestructiveAction()
	case "r":
		return m.beginLearningAudit()
	}
	return m, nil
}

func (m Model) handleLearnedPickerMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.learnedPicker.move(-1)
	case tea.MouseButtonWheelDown:
		m.learnedPicker.move(1)
	case tea.MouseButtonLeft:
		layout := overlayLayoutFor(m.width, m.height)
		row, ok := layout.rowAt(msg.X, msg.Y)
		if !ok {
			return m, nil
		}
		headerRows := m.learnedPicker.headerRows()
		start, end := m.learnedPicker.window(layout.itemRows - headerRows)
		index := start + row - headerRows
		if row < headerRows || index >= end {
			return m, nil
		}
		m.learnedPicker.selected = index
		return m.applyLearnedPrimaryAction()
	}
	return m, nil
}

func (m Model) applyLearnedPrimaryAction() (Model, tea.Cmd) {
	controller, ok := m.agent.(learningAgent)
	if !ok {
		m.learnedPicker.err = "learning is unavailable"
		return m, nil
	}
	if run, ok := m.learnedPicker.selectedRun(); ok {
		key := learnedRunKey(run.ID)
		if m.learnedPicker.busy[key] {
			return m, nil
		}
		var action func() error
		switch run.Status {
		case learning.Ready:
			if run.Candidate == nil {
				m.learnedPicker.err = "ready learning run has no candidate"
				return m, nil
			}
			candidate := *run.Candidate
			action = func() error {
				_, err := controller.ApproveLearning(run.ID, candidate)
				return err
			}
		case learning.Failed, learning.Cancelled, learning.Interrupted:
			action = func() error {
				_, err := controller.RetryLearning(run.ID)
				return err
			}
		case learning.Queued, learning.Running, learning.Cancelling:
			action = func() error { return controller.CancelLearning(run.ID) }
		default:
			return m, nil
		}
		return m.startLearnedAction(key, action)
	}
	if lesson, ok := m.learnedPicker.selectedLesson(); ok {
		key := learnedLessonKey(lesson.ID)
		return m.startLearnedAction(key, func() error {
			return controller.SetLessonEnabled(lesson.ID, !lesson.Enabled)
		})
	}
	return m, nil
}

func (m Model) applyLearnedDestructiveAction() (Model, tea.Cmd) {
	controller, ok := m.agent.(learningAgent)
	if !ok {
		m.learnedPicker.err = "learning is unavailable"
		return m, nil
	}
	if run, ok := m.learnedPicker.selectedRun(); ok && run.Status == learning.Ready {
		key := learnedRunKey(run.ID)
		return m.startLearnedAction(key, func() error { return controller.RejectLearning(run.ID) })
	}
	if lesson, ok := m.learnedPicker.selectedLesson(); ok {
		key := learnedLessonKey(lesson.ID)
		return m.startLearnedAction(key, func() error { return controller.DeleteLesson(lesson.ID) })
	}
	return m, nil
}

func (m Model) startLearnedAction(key string, action func() error) (Model, tea.Cmd) {
	if m.learnedPicker.busy[key] {
		return m, nil
	}
	m.learnedPicker.busy[key] = true
	m.learnedPicker.err = ""
	generation := m.learnedGen
	return m, func() tea.Msg {
		done := learnedActionDoneMsg{generation: generation, key: key}
		if err := action(); err != nil {
			done.err = err.Error()
		}
		return done
	}
}

func (p learnedPicker) headerRows() int {
	if p.err != "" || p.loading {
		return 1
	}
	return 0
}

func (m Model) learnedPickerView() string {
	layout := overlayLayoutFor(m.width, m.height)
	innerWidth := layout.innerWidth
	typeWidth := min(10, max(innerWidth/7, 0))
	statusWidth := min(14, max(innerWidth/6, 0))
	detailWidth := max(innerWidth-typeWidth-statusWidth, 0)
	rows := make([]string, 0, layout.itemRows)
	if m.learnedPicker.err != "" {
		rows = append(rows, dangerStyle.Render(overlayCell(" "+sanitizeTerminalText(m.learnedPicker.err), innerWidth)))
	} else if m.learnedPicker.loading {
		rows = append(rows, metadataStyle.Render(overlayCell(" Loading learning audit…", innerWidth)))
	}
	start, end := m.learnedPicker.window(layout.itemRows - len(rows))
	for index := start; index < end; index++ {
		rows = append(rows, m.learnedPickerRow(index, typeWidth, statusWidth, detailWidth))
	}
	if len(m.learnedPicker.runs) == 0 && len(m.learnedPicker.lessons) == 0 && !m.learnedPicker.loading && m.learnedPicker.err == "" {
		rows = append(rows,
			overlayCell("  Nothing learned in this workspace", innerWidth),
			metadataStyle.Render(overlayCell("  Run /learn after a completed conversation", innerWidth)),
		)
	}
	for len(rows) < layout.itemRows {
		rows = append(rows, strings.Repeat(" ", innerWidth))
	}
	lines := []string{
		overlayCell(" Type", typeWidth) + overlayCell("Status", statusWidth) + overlayCell("Guidance / result", detailWidth),
		strings.Repeat("─", max(innerWidth, 0)),
	}
	for index := 0; index < layout.itemRows; index++ {
		lines = append(lines, overlayCell(rows[index], innerWidth))
	}
	lines = append(lines,
		strings.Repeat("─", max(innerWidth, 0)),
		overlayCell(" ↑↓ move · enter approve/retry/cancel/toggle · d reject/delete · r reload · esc close", innerWidth),
	)
	return m.renderOverlayPanel(layout, "Learned Guidance", lines)
}

func (m Model) learnedPickerRow(index, typeWidth, statusWidth, detailWidth int) string {
	selected := index == m.learnedPicker.selected
	prefix := "  "
	if selected {
		prefix = "❯ "
	}
	var kind, status, detail, key string
	if index < len(m.learnedPicker.runs) {
		run := m.learnedPicker.runs[index]
		kind = "run"
		status = string(run.Status)
		key = learnedRunKey(run.ID)
		switch {
		case run.Candidate != nil:
			detail = run.Candidate.Statement
		case run.Error != "":
			detail = run.Error
		case run.NoCandidateReason != "":
			detail = run.NoCandidateReason
		default:
			detail = run.ID
		}
	} else {
		lesson := m.learnedPicker.lessons[index-len(m.learnedPicker.runs)]
		kind = "lesson"
		key = learnedLessonKey(lesson.ID)
		switch {
		case lesson.Deleted:
			status = "deleted"
		case lesson.Enabled:
			status = "enabled"
		default:
			status = "disabled"
		}
		detail = lesson.Candidate.Statement
	}
	if m.learnedPicker.busy[key] {
		status = "working…"
	}
	row := overlayCell(prefix+kind, typeWidth) + overlayCell(status, statusWidth) + overlayCell(sanitizeTerminalText(detail), detailWidth)
	if selected {
		return selectedRowStyle.Render(row)
	}
	if strings.HasPrefix(key, "lesson:") {
		return successStyle.Render(row)
	}
	return row
}

func learningQueuedNotice(run learning.Run) string {
	if run.ProviderID != "" && run.Model != "" {
		return fmt.Sprintf("learning queued: %s (%s/%s)", run.ID, run.ProviderID, run.Model)
	}
	return fmt.Sprintf("learning queued: %s", run.ID)
}
