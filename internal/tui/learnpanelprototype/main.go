// Command learnpanelprototype is a throwaway UI prototype for issue #14.
//
// It compares three structurally different learning-panel designs inside a
// terminal. Run it with: go run ./internal/tui/learnpanelprototype
// Nothing here is production code and every action changes fake, in-memory
// state only.
package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type item struct {
	kind, title, status, detail string
}

type model struct {
	variant       int
	selected      int
	action        int
	editing       bool
	width, height int
	items         []item
}

var (
	cyan     = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	muted    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	green    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellow   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	red      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	selected = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6")).Bold(true)
	box      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
)

func initialModel() model {
	return model{items: []item{
		{"run", "Prefer rg for repository search", "ready", "Scope: codebase exploration · Evidence: session a7f2 seq 18, 24\nExceptions: use another tool when rg is unavailable\nopenai / gpt-5 · 4.2s · 1,284 tokens · input truncated"},
		{"run", "Learning from current session", "running", "Captured through seq 91 · anthropic / claude-sonnet · 2.1s"},
		{"run", "Queued behind active run", "queued", "Captured through seq 44"},
		{"run", "No durable lesson found", "no_candidate", "Reason: the transcript contains no repeatable procedure"},
		{"run", "Provider returned invalid JSON", "failed", "Retry uses the original cut through seq 63"},
		{"run", "Cancellation requested", "cancelling", "Waiting for provider request to stop"},
		{"run", "Cancelled extraction", "cancelled", "Cancelled by user"},
		{"run", "Use contract kits for stores", "approved", "Approved unchanged · lesson L-18"},
		{"run", "Always rewrite the whole package", "rejected", "Rejected by user"},
		{"run", "Interrupted by restart", "interrupted", "Retry uses the original cut through seq 12"},
		{"lesson", "Use contract kits for stores", "active", "Scope: new store implementations · Approved from session b19 seq 30, 38"},
		{"lesson", "Run concurrent tests with -race", "disabled", "Scope: concurrent Go code · Disabled by user"},
	}}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "left", "h":
			m.variant = (m.variant + 2) % 3
			m.editing = false
		case "right", "l":
			m.variant = (m.variant + 1) % 3
			m.editing = false
		case "up", "k":
			m.selected = max(0, m.selected-1)
		case "down", "j":
			m.selected = min(len(m.items)-1, m.selected+1)
		case "tab":
			m.action = (m.action + 1) % len(m.actions())
		case "shift+tab":
			m.action = (m.action + len(m.actions()) - 1) % len(m.actions())
		case "e":
			m.editing = !m.editing
		case "enter":
			m.applyAction()
		}
	}
	return m, nil
}

func (m *model) applyAction() {
	actions := m.actions()
	if len(actions) == 0 {
		return
	}
	switch actions[min(m.action, len(actions)-1)] {
	case "Add", "Save & Add":
		m.items[m.selected].status = "approved"
	case "Reject":
		m.items[m.selected].status = "rejected"
	case "Cancel":
		m.items[m.selected].status = "cancelling"
	case "Retry":
		m.items[m.selected].status = "queued"
	case "Edit & Add":
		m.editing = true
	case "Disable":
		m.items[m.selected].status = "disabled"
	case "Enable":
		m.items[m.selected].status = "active"
	case "Delete":
		m.items[m.selected].status = "deleted"
	}
}

func (m model) actions() []string {
	s := m.items[m.selected].status
	if m.editing {
		return []string{"Save & Add", "Cancel edit"}
	}
	switch s {
	case "ready":
		return []string{"Add", "Edit & Add", "Reject"}
	case "queued", "running":
		return []string{"Cancel"}
	case "failed", "interrupted":
		return []string{"Retry"}
	case "active":
		return []string{"Disable", "Delete"}
	case "disabled":
		return []string{"Enable", "Delete"}
	default:
		return []string{"Close"}
	}
}

func (m model) View() string {
	var body string
	switch m.variant {
	case 0:
		body = m.masterDetail()
	case 1:
		body = m.reviewInbox()
	default:
		body = m.compactLedger()
	}
	footer := selected.Render(" ← ") + "  " + variantNames[m.variant] + "  " + selected.Render(" → ") +
		muted.Render("   switch design · ↑↓ navigate · tab actions · enter apply · e edit · q quit")
	return lipgloss.JoinVertical(lipgloss.Left, body, footer)
}

var variantNames = []string{"A — Master/detail", "B — Review inbox", "C — Compact ledger"}

func (m model) masterDetail() string {
	left := []string{cyan.Render("LEARNING  ● 1 READY"), muted.Render("Runs and approved lessons")}
	for i, it := range m.items {
		line := fmt.Sprintf("%-12s %s", status(it.status), trim(it.title, 27))
		if i == m.selected {
			line = selected.Render(line)
		}
		left = append(left, line)
	}
	right := []string{cyan.Render(m.items[m.selected].title), colorStatus(m.items[m.selected].status), "", m.items[m.selected].detail, "", actionRow(m.actions(), m.action)}
	if m.editing {
		right = append(right, "", yellow.Render("INLINE EDIT"), "Statement  [Prefer rg for repository search____]", "Scope      [codebase exploration______________]", "Exceptions [when rg is unavailable____________]")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, box.Width(43).Render(strings.Join(left, "\n")), box.Width(62).Render(strings.Join(right, "\n")))
}

func (m model) reviewInbox() string {
	lines := []string{cyan.Render("LEARNING REVIEW INBOX"), muted.Render("Ready proposals lead; history collapses below")}
	for i, it := range m.items {
		if it.status != "ready" && i != m.selected {
			continue
		}
		card := colorStatus(it.status) + "  " + it.title
		if i == m.selected {
			card = selected.Render(card)
		}
		lines = append(lines, "", card)
		if i == m.selected {
			lines = append(lines, it.detail, actionRow(m.actions(), m.action))
		}
	}
	lines = append(lines, "", muted.Render("── HISTORY: 1 running · 1 queued · 1 failed · 1 interrupted · 6 settled"), muted.Render("── APPROVED LESSONS: 1 active · 1 disabled"))
	if m.editing {
		lines = append(lines, "", box.Width(78).Render(yellow.Render("DEDICATED EDIT STEP")+"\n1 Statement  →  2 Scope  →  3 Exceptions  →  4 Review & Add\n\nCurrent field: Statement\n[Prefer rg for repository search____________________________________]"))
	}
	return box.Width(108).Render(strings.Join(lines, "\n"))
}

func (m model) compactLedger() string {
	lines := []string{cyan.Render("LESSONS LEDGER") + muted.Render("  /lessons · global indicator: Learn[1]")}
	for i, it := range m.items {
		line := fmt.Sprintf("%-7s %-13s %-50s", it.kind, status(it.status), trim(it.title, 50))
		if i == m.selected {
			line = selected.Render(line)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", m.items[m.selected].detail, actionRow(m.actions(), m.action))
	if m.editing {
		lines = append(lines, "", yellow.Render("$EDITOR HANDOFF"), "Atenea writes a temporary JSON document, suspends the TUI, and validates it on return.", muted.Render("$ nvim /tmp/atenea-lesson-….json"))
	}
	return box.Width(108).Render(strings.Join(lines, "\n"))
}

func actionRow(actions []string, active int) string {
	parts := make([]string, len(actions))
	for i, a := range actions {
		if i == active {
			parts[i] = selected.Render(" " + a + " ")
		} else {
			parts[i] = "[ " + a + " ]"
		}
	}
	return strings.Join(parts, "   ")
}

func status(s string) string { return "[" + strings.ToUpper(s) + "]" }
func colorStatus(s string) string {
	switch s {
	case "approved", "active":
		return green.Render(status(s))
	case "failed", "rejected":
		return red.Render(status(s))
	case "queued", "running", "ready", "cancelling", "interrupted":
		return yellow.Render(status(s))
	default:
		return muted.Render(status(s))
	}
}
func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func main() {
	if _, err := tea.NewProgram(initialModel(), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
