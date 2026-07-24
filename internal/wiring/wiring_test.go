package wiring

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"atenea/internal/event"
	"atenea/internal/llm"
	"atenea/internal/permission"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// TestSkillDirs_ProjectBeforeGlobalDeduped: skillDirs lista primero las rutas del
// proyecto (root) y luego las globales (home), en el orden .atenea/.agents/.claude,
// para que una skill del proyecto override a una global homonima. Rutas identicas
// (root == home) se deduplican.
func TestSkillDirs_ProjectBeforeGlobalDeduped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := skillDirs("/proj")
	want := []string{
		filepath.Join("/proj", ".atenea", "skills"),
		filepath.Join("/proj", ".agents", "skills"),
		filepath.Join("/proj", ".claude", "skills"),
		filepath.Join(home, ".atenea", "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skillDirs orden = %v,\n want %v", got, want)
	}
	// root == home: las rutas coinciden, deben deduplicarse a las 3 del home.
	if d := skillDirs(home); len(d) != 3 {
		t.Fatalf("root==home debe deduplicar a 3 dirs, got %v", d)
	}
}

func TestBuild_InstallsContextCompactor(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	for _, message := range []session.Message{
		{ID: "u1", Role: session.RoleUser, Text: "old"},
		{ID: "a1", Role: session.RoleAssistant, Text: "answer"},
		{ID: "u2", Role: session.RoleUser, Text: "current"},
	} {
		message := message
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Message: &message}); err != nil {
			t.Fatal(err)
		}
	}
	provider := llm.NewFakeProvider(
		llm.Event{Kind: llm.TextDelta, Text: `{"current_goal":"continue","constraints_and_instructions":[],"decisions":[],"completed_work":[],"files_and_changes":[],"relevant_tool_results":[],"failures_and_attempts":[],"pending_and_next_step":[],"facts_not_to_reinterpret":[]}`},
		llm.Event{Kind: llm.StepEnded},
	)
	built := Build(Config{
		Root:     t.TempDir(),
		Provider: provider,
		Store:    store,
		Inbox:    session.NewMemoryInbox(),
		Gate:     permission.NewMemoryGate(),
		Snaps:    tool.NewSessionSnapshots(),
		Bus:      event.NewBus(func(string, ...interface{}) {}),
		NextID:   func() string { return "id" },
	})

	if err := built.Runner.CompactNow(ctx, "s1"); err != nil {
		t.Fatalf("CompactNow() error = %v", err)
	}
	events, err := store.Events(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if last := events[len(events)-1]; last.Kind != session.KindContextCompacted {
		t.Fatalf("last event = %+v, want Context.Compacted", last)
	}
}

// TestAskPolicy_GatesShellFSAndNetwork pins the agreed fixed classification:
// shell (bash), local FS mutations (write, edit) and outbound network
// (web_fetch) ask; reads and internal tools are allowed. This is the single
// source of truth shared by the main runner and the subagents.
func TestAskPolicy_GatesShellFSAndNetwork(t *testing.T) {
	cases := []struct {
		name string
		want permission.Decision
	}{
		{"bash", permission.Ask},
		{"write", permission.Ask},
		{"edit", permission.Ask},
		{"web_fetch", permission.Ask},
		{"read", permission.Allow},
		{"glob", permission.Allow},
		{"grep", permission.Allow},
		{"skill", permission.Allow},
		{"todo_write", permission.Allow},
		{"present_plan", permission.Allow},
		{"task", permission.Allow},
	}
	for _, tc := range cases {
		if got := askPolicy.Decide(tool.Call{Name: tc.name}); got != tc.want {
			t.Errorf("askPolicy.Decide(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
