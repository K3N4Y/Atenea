package wailsworkspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"atenea/internal/agent"
	"atenea/internal/command"
	"atenea/internal/event"
	"atenea/internal/llm"
	"atenea/internal/permission"
	"atenea/internal/session"
	"atenea/internal/tool"
)

type recordingProvider struct {
	mu       sync.Mutex
	requests []llm.Request
}

func (p *recordingProvider) Stream(_ context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	out := make(chan llm.Event, 1)
	out <- llm.Event{Kind: llm.StepEnded}
	close(out)
	return out, nil
}

func (p *recordingProvider) lastRequest() llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[len(p.requests)-1]
}

func newTestManager(t *testing.T, root string, state func() ProviderState) (*Manager, *agent.Service) {
	t.Helper()
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()
	service := agent.NewService(inbox)
	bus := event.NewBus(func(string, ...interface{}) {})
	manager := New(Config{
		Root: root, ProviderState: state, Store: store, Inbox: inbox,
		Gate: permission.NewMemoryGate(), Snapshots: tool.NewSessionSnapshots(),
		Bus: bus, Agent: service,
	})
	t.Cleanup(manager.Close)
	return manager, service
}

func writeSkill(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: test " + name + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManager_SetRootPublishesFilesCommandsAndRunnerTogether(t *testing.T) {
	root1, root2 := t.TempDir(), t.TempDir()
	writeSkill(t, root1, "alpha")
	writeSkill(t, root2, "beta")
	if err := os.WriteFile(filepath.Join(root2, "beta.txt"), []byte("beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := &recordingProvider{}
	manager, service := newTestManager(t, root1, func() ProviderState {
		return ProviderState{Provider: provider}
	})

	if err := manager.SetRoot(root2); err != nil {
		t.Fatal(err)
	}
	if got := manager.Root(); got != root2 {
		t.Fatalf("Root() = %q, want %q", got, root2)
	}
	files, err := manager.Files(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !contains(files, "beta.txt") {
		t.Fatalf("Files() = %v, want beta.txt", files)
	}
	commands := manager.Commands()
	if !hasCommand(commands, "beta") || hasCommand(commands, "alpha") {
		t.Fatalf("Commands() = %#v, want beta and no alpha", commands)
	}
	if _, err := service.Send("session", "/beta work", agent.Hooks{}); err != nil {
		t.Fatal(err)
	}
	service.Wait()
	req := provider.lastRequest()
	if !strings.Contains(req.System, root2) {
		t.Fatalf("runner system prompt does not contain new root %q", root2)
	}
	if got := req.Messages[len(req.Messages)-1].Text; !strings.Contains(got, `skill "beta"`) {
		t.Fatalf("expanded prompt = %q, want beta command", got)
	}
}

func TestManager_AdmitExcludesSetRootAndReconfigure(t *testing.T) {
	root1, root2 := t.TempDir(), t.TempDir()
	provider := llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded})
	manager, _ := newTestManager(t, root1, func() ProviderState {
		return ProviderState{Provider: provider}
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	admitDone := make(chan error, 1)
	go func() {
		admitDone <- manager.Admit(func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	setStarted, setDone := make(chan struct{}), make(chan error, 1)
	go func() {
		close(setStarted)
		setDone <- manager.SetRoot(root2)
	}()
	reconfigureStarted := make(chan struct{})
	changeCalled, reconfigureDone := make(chan struct{}), make(chan error, 1)
	go func() {
		close(reconfigureStarted)
		reconfigureDone <- manager.Reconfigure(func() error {
			close(changeCalled)
			return nil
		})
	}()
	<-setStarted
	<-reconfigureStarted
	runtime.Gosched()
	if got := manager.Root(); got != root1 {
		t.Fatalf("root changed while Admit held lifecycle: %q", got)
	}
	select {
	case <-changeCalled:
		t.Fatal("Reconfigure change ran while Admit held lifecycle")
	default:
	}

	close(release)
	if err := <-admitDone; err != nil {
		t.Fatal(err)
	}
	if err := <-setDone; err != nil {
		t.Fatal(err)
	}
	if err := <-reconfigureDone; err != nil {
		t.Fatal(err)
	}
	if got := manager.Root(); got != root2 {
		t.Fatalf("Root() = %q after release, want %q", got, root2)
	}
}

func TestManager_ReconfigureFailureLeavesWiringUntouched(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha")
	provider := llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded})
	var snapshots atomic.Int32
	manager, _ := newTestManager(t, root, func() ProviderState {
		snapshots.Add(1)
		return ProviderState{Provider: provider}
	})
	beforeCommands := manager.Commands()
	wantErr := errors.New("invalid provider")
	if err := manager.Reconfigure(func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("Reconfigure error = %v, want %v", err, wantErr)
	}
	if got := snapshots.Load(); got != 1 {
		t.Fatalf("provider snapshots = %d, want initial build only", got)
	}
	afterCommands := manager.Commands()
	if len(afterCommands) != len(beforeCommands) || afterCommands[0].Name != beforeCommands[0].Name {
		t.Fatalf("commands changed after failed reconfigure: before=%v after=%v", beforeCommands, afterCommands)
	}
	if got := manager.Root(); got != root {
		t.Fatalf("Root() = %q, want unchanged %q", got, root)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasCommand(commands []command.Command, want string) bool {
	for _, item := range commands {
		if item.Name == want {
			return true
		}
	}
	return false
}
