package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"atenea/internal/command"
	"atenea/internal/session"
)

type recordingInbox struct {
	*session.MemoryInbox
	mu       sync.Mutex
	admitted []string
}

func (r *recordingInbox) Admit(ctx context.Context, sessionID string, prompt session.Prompt, delivery session.Delivery) error {
	r.mu.Lock()
	r.admitted = append(r.admitted, prompt.Text)
	r.mu.Unlock()
	return r.MemoryInbox.Admit(ctx, sessionID, prompt, delivery)
}

func (r *recordingInbox) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.admitted[len(r.admitted)-1]
}

type runnerFunc func(context.Context, string, bool) error

func (f runnerFunc) Run(ctx context.Context, sessionID string, force bool) error {
	return f(ctx, sessionID, force)
}

func TestService_SendOwnsSharedTurnLifecycle(t *testing.T) {
	inbox := &recordingInbox{MemoryInbox: session.NewMemoryInbox()}
	var mu sync.Mutex
	var order []string
	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}
	service := NewService(inbox)
	service.Configure(runnerFunc(func(context.Context, string, bool) error {
		record("run")
		return nil
	}), command.New([]command.Command{{Name: "foo", Template: "expanded $ARGUMENTS"}}))

	handle, err := service.Send("s1", "/foo value", Hooks{
		BeforeAdmit: func() error {
			record("before")
			return nil
		},
		AfterAdmit: func() { record("after-admit") },
		AfterRun: func(result RunResult) {
			if result.ID == 0 || result.Err != nil || !result.Current {
				t.Errorf("result = %+v, want current clean run", result)
			}
			record("after-run")
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-handle.Done()

	if got := inbox.last(); got != "expanded value" {
		t.Fatalf("admitted = %q, want expanded value", got)
	}
	if got := service.Mode("s1"); got != session.ModeNormal {
		t.Fatalf("Mode = %v, want normal", got)
	}
	mu.Lock()
	gotOrder := append([]string(nil), order...)
	mu.Unlock()
	wantOrder := []string{"before", "after-admit", "run", "after-run"}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
		}
	}
}

func TestService_ReplacementWaitsForPreviousRun(t *testing.T) {
	inbox := session.NewMemoryInbox()
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	secondStarted := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	service := NewService(inbox)
	service.Configure(runnerFunc(func(ctx context.Context, _ string, _ bool) error {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			close(firstStarted)
			<-ctx.Done()
			<-firstRelease
			return ctx.Err()
		}
		close(secondStarted)
		return nil
	}), command.New(nil))

	first, err := service.Send("s1", "first", Hooks{})
	if err != nil {
		t.Fatalf("first Send: %v", err)
	}
	<-firstStarted
	second, err := service.Send("s1", "second", Hooks{})
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	select {
	case <-secondStarted:
		t.Fatal("replacement entered runner before previous run finished")
	case <-time.After(30 * time.Millisecond):
	}
	close(firstRelease)
	<-first.Done()
	<-second.Done()
	select {
	case <-secondStarted:
	default:
		t.Fatal("replacement never entered runner")
	}
}

func TestService_AcceptPlanUsesNormalModeAndFixedPrompt(t *testing.T) {
	inbox := &recordingInbox{MemoryInbox: session.NewMemoryInbox()}
	service := NewService(inbox)
	service.Configure(runnerFunc(func(context.Context, string, bool) error { return nil }), command.New(nil))

	plan, err := service.SendPlan("s1", "plan", Hooks{})
	if err != nil {
		t.Fatalf("SendPlan: %v", err)
	}
	<-plan.Done()
	if got := service.Mode("s1"); got != session.ModePlan {
		t.Fatalf("Mode after SendPlan = %v, want plan", got)
	}
	accepted, err := service.AcceptPlan("s1", Hooks{})
	if err != nil {
		t.Fatalf("AcceptPlan: %v", err)
	}
	<-accepted.Done()
	if got := service.Mode("s1"); got != session.ModeNormal {
		t.Fatalf("Mode after AcceptPlan = %v, want normal", got)
	}
	if got := inbox.last(); got != AcceptPlanPrompt {
		t.Fatalf("admitted = %q, want fixed accept-plan prompt", got)
	}
}

func TestService_RetryRunsWithoutAdmittingPromptAgain(t *testing.T) {
	inbox := &recordingInbox{MemoryInbox: session.NewMemoryInbox()}
	forced := make(chan bool, 1)
	service := NewService(inbox)
	service.Configure(runnerFunc(func(_ context.Context, _ string, force bool) error {
		forced <- force
		return nil
	}), command.New(nil))

	first, err := service.Send("s1", "hello", Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	<-first.Done()
	<-forced
	retry, err := service.Retry("s1", Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	<-retry.Done()
	if !<-forced {
		t.Fatal("Retry must force the runner with the existing conversation")
	}
	if len(inbox.admitted) != 1 {
		t.Fatalf("admitted prompts = %v, retry duplicated the user turn", inbox.admitted)
	}
}

func TestService_ConfigureWaitsForAdmissionAndCancelsOldRuntime(t *testing.T) {
	service := NewService(session.NewMemoryInbox())
	service.Configure(runnerFunc(func(ctx context.Context, _ string, _ bool) error {
		<-ctx.Done()
		return ctx.Err()
	}), command.New(nil))
	admissionStarted := make(chan struct{})
	releaseAdmission := make(chan struct{})
	handles := make(chan RunHandle, 1)
	go func() {
		handle, err := service.Send("s1", "prompt", Hooks{BeforeAdmit: func() error {
			close(admissionStarted)
			<-releaseAdmission
			return nil
		}})
		if err != nil {
			t.Errorf("Send: %v", err)
			return
		}
		handles <- handle
	}()
	<-admissionStarted

	configured := make(chan struct{})
	go func() {
		service.Configure(runnerFunc(func(context.Context, string, bool) error { return nil }), command.New(nil))
		close(configured)
	}()
	select {
	case <-configured:
		t.Fatal("Configure completed while admission still used the old runtime")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseAdmission)
	handle := <-handles
	<-configured
	<-handle.Done()
}
