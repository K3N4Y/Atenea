package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"atenea/internal/session"
)

// openPermissionGate drives the real fold path so the transcript carries a
// pending permission entry, matching how the running app reaches that state.
func openPermissionGate(t *testing.T, m Model) Model {
	t.Helper()
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash"})
	return apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash"})
}

// openPlanGate drives the real fold path so the transcript carries a pending
// plan-approval entry (a present_plan tool settled with success).
func openPlanGate(t *testing.T, m Model) Model {
	t.Helper()
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	return apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "p1"})
}

// TestActiveInputTarget_ResolvesEachContext pins the resolver against a Model
// placed in exactly one active state, walking the whole target vocabulary.
func TestActiveInputTarget_ResolvesEachContext(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, m Model) Model
		want  inputTarget
	}{
		{
			name:  "empty model falls to composer",
			setup: func(_ *testing.T, m Model) Model { return m },
			want:  targetComposer,
		},
		{
			name: "resume picker open",
			setup: func(_ *testing.T, m Model) Model {
				m.resumePicker.open = true
				return m
			},
			want: targetResumePicker,
		},
		{
			name: "model picker open",
			setup: func(_ *testing.T, m Model) Model {
				m.modelPicker.open = true
				return m
			},
			want: targetModelPicker,
		},
		{
			name: "mcp picker open",
			setup: func(_ *testing.T, m Model) Model {
				m.mcpPicker.open = true
				return m
			},
			want: targetMCPPicker,
		},
		{
			name: "connect panel open",
			setup: func(_ *testing.T, m Model) Model {
				m.connectPanel.open = true
				return m
			},
			want: targetConnectPanel,
		},
		{
			name:  "pending permission",
			setup: openPermissionGate,
			want:  targetPermissionGate,
		},
		{
			name:  "pending plan",
			setup: openPlanGate,
			want:  targetPlanGate,
		},
		{
			name: "viewer focus",
			setup: func(_ *testing.T, m Model) Model {
				m.viewer = fileViewer{path: "example.go"}
				m.focus = viewerFocus
				return m
			},
			want: targetViewer,
		},
		{
			name: "explorer focus",
			setup: func(_ *testing.T, m Model) Model {
				m.treeOpen = true
				m.focus = explorerFocus
				return m
			},
			want: targetExplorer,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(t, NewModel(&fakeAgent{}, "s1", nil))
			if got := m.activeInputTarget(); got != tc.want {
				t.Fatalf("activeInputTarget() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestActiveInputTarget_PrecedenceTieBreaks pins the ORDER: when several
// contexts are simultaneously active, the higher-priority one must win. Each
// case stacks a lower gate under a higher one and asserts the higher wins,
// walking the whole chain from the top down.
func TestActiveInputTarget_PrecedenceTieBreaks(t *testing.T) {
	// withEverythingBelowResume stacks every lower gate so the case proves the
	// higher gate under test truly wins over ALL of them, not just the next one.
	tests := []struct {
		name  string
		setup func(t *testing.T, m Model) Model
		want  inputTarget
	}{
		{
			name: "resume picker beats every other overlay and gate",
			setup: func(t *testing.T, m Model) Model {
				m = openPermissionGate(t, m)
				m = openPlanGate(t, m)
				m.connectPanel.open = true
				m.mcpPicker.open = true
				m.modelPicker.open = true
				m.resumePicker.open = true
				m.treeOpen = true
				m.focus = explorerFocus
				return m
			},
			want: targetResumePicker,
		},
		{
			name: "model picker beats mcp, connect and the gates",
			setup: func(t *testing.T, m Model) Model {
				m = openPermissionGate(t, m)
				m.connectPanel.open = true
				m.mcpPicker.open = true
				m.modelPicker.open = true
				return m
			},
			want: targetModelPicker,
		},
		{
			name: "mcp picker beats connect and the gates",
			setup: func(t *testing.T, m Model) Model {
				m = openPermissionGate(t, m)
				m.connectPanel.open = true
				m.mcpPicker.open = true
				return m
			},
			want: targetMCPPicker,
		},
		{
			name: "connect panel beats the permission gate",
			setup: func(t *testing.T, m Model) Model {
				m = openPermissionGate(t, m)
				m.connectPanel.open = true
				return m
			},
			want: targetConnectPanel,
		},
		{
			name: "permission gate beats the plan gate",
			setup: func(t *testing.T, m Model) Model {
				m = openPlanGate(t, m)
				m = openPermissionGate(t, m)
				return m
			},
			want: targetPermissionGate,
		},
		{
			name: "permission gate beats panel focus",
			setup: func(t *testing.T, m Model) Model {
				m.treeOpen = true
				m.focus = explorerFocus
				return openPermissionGate(t, m)
			},
			want: targetPermissionGate,
		},
		{
			name: "plan gate beats panel focus",
			setup: func(t *testing.T, m Model) Model {
				m.viewer = fileViewer{path: "example.go"}
				m.focus = viewerFocus
				return openPlanGate(t, m)
			},
			want: targetPlanGate,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(t, NewModel(&fakeAgent{}, "s1", nil))
			if got := m.activeInputTarget(); got != tc.want {
				t.Fatalf("activeInputTarget() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestActiveInputTarget_ExplorerForcedByNarrowTerminal pins the normalizedFocus
// edge that the resolver inherits: on a terminal too narrow to show both panels
// an open tree owns focus even when m.focus still says chat.
func TestActiveInputTarget_ExplorerForcedByNarrowTerminal(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m.ready = true
	m.width = 20
	m.treeOpen = true
	m.focus = chatFocus

	if got := m.treePanelWidth(); got < m.width {
		t.Fatalf("treePanelWidth() = %d, want >= %d so the tree fills the width", got, m.width)
	}
	if got := m.activeInputTarget(); got != targetExplorer {
		t.Fatalf("activeInputTarget() = %v, want targetExplorer (narrow terminal forces the tree)", got)
	}
}

// TestInputTarget_ModalActive pins which targets count as an active overlay or
// gate for the mouse router's shared short-circuit notion.
func TestInputTarget_ModalActive(t *testing.T) {
	modal := map[inputTarget]bool{
		targetResumePicker:   true,
		targetModelPicker:    true,
		targetMCPPicker:      true,
		targetConnectPanel:   true,
		targetPermissionGate: true,
		targetPlanGate:       true,
		targetViewer:         false,
		targetExplorer:       false,
		targetComposer:       false,
	}
	for target, want := range modal {
		if got := target.modalActive(); got != want {
			t.Fatalf("inputTarget(%d).modalActive() = %v, want %v", target, got, want)
		}
	}
}

// TestSyncComposerFocus_MatchesResolver ties the composer-focus consumer back
// to the resolver: the composer holds terminal focus iff the active target is
// the composer, and blurs for every overlay/gate/panel above it.
func TestSyncComposerFocus_MatchesResolver(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T, m Model) Model
		wantFocused bool
	}{
		{
			name:        "composer focused when nothing else claims input",
			setup:       func(_ *testing.T, m Model) Model { return m },
			wantFocused: true,
		},
		{
			name: "blurred while resume picker open",
			setup: func(_ *testing.T, m Model) Model {
				m.resumePicker = newResumePicker("s1")
				return m
			},
			wantFocused: false,
		},
		{
			name: "blurred while model picker open",
			setup: func(_ *testing.T, m Model) Model {
				m.modelPicker.open = true
				return m
			},
			wantFocused: false,
		},
		{
			name: "blurred while connect panel open",
			setup: func(_ *testing.T, m Model) Model {
				m.connectPanel.open = true
				return m
			},
			wantFocused: false,
		},
		{
			name:        "blurred while permission gate pending",
			setup:       openPermissionGate,
			wantFocused: false,
		},
		{
			name:        "blurred while plan gate pending",
			setup:       openPlanGate,
			wantFocused: false,
		},
		{
			name: "blurred while explorer holds focus",
			setup: func(_ *testing.T, m Model) Model {
				m.treeOpen = true
				m.focus = explorerFocus
				return m
			},
			wantFocused: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(t, NewModel(&fakeAgent{}, "s1", nil))
			m.terminalFocused = true
			m.syncComposerFocus()
			gotFocused := m.input.Focused()
			wantComposer := m.activeInputTarget() == targetComposer
			if gotFocused != tc.wantFocused {
				t.Fatalf("input.Focused() = %v, want %v", gotFocused, tc.wantFocused)
			}
			if gotFocused != wantComposer {
				t.Fatalf("composer focus %v disagrees with resolver target %v", gotFocused, m.activeInputTarget())
			}
		})
	}
}

// TestSyncComposerFocus_BlurredWhenTerminalUnfocused pins that even when the
// composer is the active target, an unfocused terminal keeps it blurred.
func TestSyncComposerFocus_BlurredWhenTerminalUnfocused(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m.terminalFocused = false
	if m.activeInputTarget() != targetComposer {
		t.Fatalf("precondition: active target = %v, want targetComposer", m.activeInputTarget())
	}
	m.syncComposerFocus()
	if m.input.Focused() {
		t.Fatal("composer must stay blurred while the terminal is unfocused")
	}
}

// TestHandleKey_PermissionGatePgUpStillScrolls pins the leaf exception that
// must survive the resolver refactor: during the permission gate PgUp keeps
// scrolling the transcript instead of being swallowed by approval mode.
func TestHandleKey_PermissionGatePgUpStillScrolls(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
	// Fill well past a screen so the viewport starts pinned to a scrollable tail.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("message-%02d", i),
		}})
	}
	m = openPermissionGate(t, m)
	if m.activeInputTarget() != targetPermissionGate {
		t.Fatalf("precondition: active target = %v, want targetPermissionGate", m.activeInputTarget())
	}
	if !m.viewport.AtBottom() {
		t.Fatal("precondition: viewport must start at the tail")
	}
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	if got := next.(Model); got.viewport.AtBottom() {
		t.Fatal("PgUp during the permission gate must scroll the transcript off the tail, not be swallowed")
	}
}
