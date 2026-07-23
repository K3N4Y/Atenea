package wailssession

import (
	"context"
	"sync/atomic"
	"testing"

	"atenea/internal/session"
)

func TestTurnCapturesInitialCwdAndTitlesOnlyFirstCurrentRun(t *testing.T) {
	store := session.NewMemoryStore()
	m := New(Config{Store: store, Root: func() string { return "/work/project" }})
	var calls atomic.Int32
	m.SetTitler(func(string) string { calls.Add(1); return "Generated title" })

	first := m.Turn("s1", "first prompt")
	if err := first.BeforeAdmit(); err != nil {
		t.Fatalf("BeforeAdmit: %v", err)
	}
	if _, err := store.AppendEvent(context.Background(), "s1", session.SessionEvent{Message: &session.Message{Role: session.RoleUser, Text: "first prompt"}}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	first.AfterRun(true)

	second := m.Turn("s1", "second prompt")
	if err := second.BeforeAdmit(); err != nil {
		t.Fatalf("BeforeAdmit second: %v", err)
	}
	second.AfterRun(true)

	events, err := m.History(context.Background(), "s1")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(events) != 3 || events[0].Kind != session.KindSessionCwd || events[0].Text != "/work/project" || events[2].Kind != session.KindSessionTitle {
		t.Fatalf("events = %+v, want cwd, prompt, title", events)
	}
	if calls.Load() != 1 {
		t.Fatalf("titler calls = %d, want 1", calls.Load())
	}
}

func TestDeleteForgetsBeforeRemovingDurableSession(t *testing.T) {
	store := session.NewMemoryStore()
	if _, err := store.AppendEvent(context.Background(), "s1", session.SessionEvent{Message: &session.Message{Role: session.RoleUser, Text: "hello"}}); err != nil {
		t.Fatal(err)
	}
	forgot := ""
	m := New(Config{Store: store, Root: func() string { return "." }, Forget: func(id string) { forgot = id }})
	if err := m.Delete(context.Background(), "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if forgot != "s1" {
		t.Fatalf("forgot = %q, want s1", forgot)
	}
	if _, err := m.History(context.Background(), "s1"); err == nil {
		t.Fatal("History after Delete returned nil error")
	}
}
