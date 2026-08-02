package wailssession

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/event"
	"github.com/K3N4Y/atenea/internal/session"
)

type gatedVersioner struct {
	version      atomic.Int64
	firstStarted chan struct{}
	releaseFirst chan struct{}
	first        sync.Once
}

var _ event.DataVersioner = (*gatedVersioner)(nil)

func (v *gatedVersioner) DataVersion(context.Context) (int64, error) {
	v.first.Do(func() {
		close(v.firstStarted)
		<-v.releaseFirst
	})
	return v.version.Load(), nil
}

func TestManager_WatchWaitsForInitialDataVersion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	versioner := &gatedVersioner{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	versioner.version.Store(1)
	emitted := make(chan struct{}, 1)
	manager := New(Config{
		Versioner:   versioner,
		Emit:        func(string, ...any) { emitted <- struct{}{} },
		WatchPeriod: time.Millisecond,
	})

	watchReturned := make(chan struct{})
	go func() {
		manager.Watch(ctx)
		close(watchReturned)
	}()

	<-versioner.firstStarted
	select {
	case <-watchReturned:
		t.Fatal("Watch returned before the initial DataVersion attempt completed")
	default:
	}

	close(versioner.releaseFirst)
	<-watchReturned
	versioner.version.Store(2)
	select {
	case <-emitted:
	case <-time.After(time.Second):
		t.Fatal("watcher did not emit after a change following initialization")
	}
}

func TestManager_WatchWithoutVersionerIsNoOp(t *testing.T) {
	returned := make(chan struct{})
	go func() {
		New(Config{}).Watch(context.Background())
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("Watch blocked without a versioner")
	}
}

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
