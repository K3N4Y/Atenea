package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/session"
)

// The arrival hint is the only surface that names "/" , "@" and /help before
// the user types anything, so it must be on screen at launch and gone once the
// conversation exists: an onboarding line that outlives onboarding is noise.
func TestModel_ShowsArrivalHintUntilTheConversationStarts(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithStatus("", "model")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})

	view := ansi.Strip(m.View())
	for _, want := range []string{"/ commands", "@ files", "/help"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, the arrival hint must name %q", view, want)
		}
	}

	m = apply(t, m, EventMsg{Message: &session.Message{Role: session.RoleUser, Text: "hola"}})
	if view := ansi.Strip(m.View()); strings.Contains(view, "/help") {
		t.Fatalf("View() = %q, the arrival hint must disappear once the conversation starts", view)
	}
}

// A launch notice (no provider connected, YOLO active) is not a conversation:
// the hint that explains how to reach /connect must survive beside it.
func TestModel_KeepsArrivalHintBesideLaunchNotices(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithNotice("No provider connected — run /connect").WithStatus("", "model")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})

	if view := ansi.Strip(m.View()); !strings.Contains(view, "/help") {
		t.Fatalf("View() = %q, a launch notice must not count as a started conversation", view)
	}
}

// The hint shares one row with the git summary and the destructive-action
// confirmations. It is the least urgent tenant: it yields the row rather than
// pushing anything off or truncating into a half-taught gesture.
func TestModel_ArrivalHintYieldsTheStatusRow(t *testing.T) {
	tests := []struct {
		name    string
		width   int
		arm     bool
		summary gitSummary
		want    string
		absent  []string
	}{
		{
			name:    "shares the row with the git summary",
			width:   80,
			summary: gitSummary{Files: 1, Additions: 2, Deletions: 1},
			want:    "enter send · / commands · @ files · /help keys",
		},
		{
			name:    "shortens instead of truncating when the summary crowds it",
			width:   68,
			summary: gitSummary{Files: 3, Additions: 20, Deletions: 10},
			want:    "/ commands · @ files · /help keys",
			absent:  []string{"enter send"},
		},
		{
			name:   "yields entirely to an armed confirmation",
			width:  80,
			arm:    true,
			want:   "Esc again to cancel",
			absent: []string{"/help"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil).WithStatus("", "model")
			m = apply(t, m, tea.WindowSizeMsg{Width: tt.width, Height: 20})
			m = apply(t, m, workspaceRefreshedMsg{summary: tt.summary})
			if tt.arm {
				m.working = true
				m.activeRun = 1
				m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
			}
			view := ansi.Strip(m.View())
			if !strings.Contains(view, tt.want) {
				t.Fatalf("View() = %q, want %q", view, tt.want)
			}
			for _, absent := range tt.absent {
				if strings.Contains(view, absent) {
					t.Fatalf("View() = %q, must not contain %q", view, absent)
				}
			}
			assertNoLineWiderThan(t, m.View(), tt.width)
		})
	}
}

// /help is the second surface: everything the hint cannot fit, on demand. It
// resolves locally — no prompt reaches the agent — and names the gestures that
// are otherwise invisible without reading the source.
func TestModel_HelpCommandPrintsTheKeyMapWithoutPromptingTheAgent(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = typeRunes(t, m, "/help")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 0 {
		t.Fatalf("sent = %v, /help must resolve locally", fake.sent)
	}
	if m.composer.value() != "" {
		t.Fatalf("composer = %q, /help must clear the input", m.composer.value())
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"ctrl+j", "ctrl+v", "shift+tab", "pgup", "y allow once", "space toggle"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, the key map must document %q", view, want)
		}
	}
}
