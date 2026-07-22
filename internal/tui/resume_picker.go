package tui

import (
	"errors"
	"strings"
	"time"

	"atenea/internal/session"

	"github.com/charmbracelet/bubbles/textinput"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// resumePicker keeps its own search-box chrome (a live-filter textinput above
// plain rows, unlike the bordered /model, /mcp and /connect panels), so it
// reuses only the navigation core of the overlay module — the embedded
// overlayList owns the cursor (selected), its wrap movement (move) and the
// scroll window. The session list, live filter and async load stay here.
type resumePicker struct {
	open        bool
	loading     bool
	currentID   string
	query       textinput.Model
	sessions    []session.SessionSummary
	filtered    []session.SessionSummary
	overlayList // navigation over filtered: selected + move + window
	targetID    string
	err         error
}

func newResumePicker(currentID string) resumePicker {
	query := textinput.New()
	query.Prompt = ""
	query.Placeholder = "Search sessions"
	query.Focus()

	return resumePicker{
		open:      true,
		loading:   true,
		currentID: currentID,
		query:     query,
	}
}

func (p *resumePicker) setSessions(sessions []session.SessionSummary) {
	p.sessions = append([]session.SessionSummary(nil), sessions...)
	p.filtered = nil
	p.loading = false
	p.err = nil
	p.selected = 0
	p.targetID = ""
	p.filter()
}

func (p *resumePicker) filter() {
	selected, hadSelection := p.selectedSession()
	query := normalizeResumeSearch(p.query.Value())
	p.filtered = p.filtered[:0]
	for _, summary := range p.sessions {
		if query == "" || strings.Contains(normalizeResumeSearch(summary.Title), query) {
			p.filtered = append(p.filtered, summary)
		}
	}

	p.selected = 0
	if hadSelection {
		for i, summary := range p.filtered {
			if summary.ID == selected.ID {
				p.selected = i
				break
			}
		}
	}
	p.setCount(len(p.filtered))
}

func normalizeResumeSearch(value string) string {
	normalized := norm.NFKC.String(strings.TrimSpace(value))
	return norm.NFKC.String(cases.Fold().String(normalized))
}

func (p resumePicker) selectedSession() (session.SessionSummary, bool) {
	index, ok := p.hasSelection()
	if !ok || index >= len(p.filtered) {
		return session.SessionSummary{}, false
	}
	return p.filtered[index], true
}

func (p *resumePicker) close() {
	p.open = false
	p.loading = false
	p.err = nil
	p.targetID = ""
	p.query.Blur()
}

func (p *resumePicker) beginLoad(targetID string) {
	p.loading = true
	p.err = nil
	p.targetID = targetID
}

func (p *resumePicker) fail(message string) {
	p.loading = false
	p.targetID = ""
	p.err = errors.New(message)
}

func formatResumeActivity(activity time.Time) string {
	return activity.Local().Format("Jan 02, 2006 15:04")
}
