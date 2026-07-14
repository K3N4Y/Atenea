package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"atenea/internal/command"
	"atenea/internal/session"
)

const AcceptPlanPrompt = "El plan fue aprobado. Implementalo ahora paso a paso siguiendo el plan."

// Runner is the headless execution boundary required by Service.
type Runner interface {
	Run(context.Context, string, bool) error
}

// Hooks keep adapter-specific behavior outside the shared turn lifecycle.
type Hooks struct {
	BeforeAdmit func() error
	AfterAdmit  func()
	AfterRun    func(RunResult)
}

// RunResult describes one completed run. Current reports whether the run was
// still the active run when its completion hook executed.
type RunResult struct {
	ID      uint64
	Err     error
	Current bool
}

// RunHandle identifies a concrete run and exposes its completion signal.
type RunHandle struct {
	ID   uint64
	done chan struct{}
}

// Done closes after the runner and completion hook finish.
func (h RunHandle) Done() <-chan struct{} { return h.done }

type activeRun struct {
	RunHandle
	cancel context.CancelFunc
}

// Service owns the shared headless turn lifecycle used by Wails and the TUI.
type Service struct {
	runtimeMu sync.RWMutex
	mu        sync.Mutex
	inbox     session.Inbox
	runner    Runner
	commands  *command.Set
	modes     map[string]session.Mode
	runs      map[string]*activeRun
	ops       map[string]*sync.Mutex
	nextID    uint64
	wg        sync.WaitGroup
}

// NewService creates an unconfigured service over inbox. Configure must be
// called before sending a turn.
func NewService(inbox session.Inbox) *Service {
	return &Service{
		inbox: inbox,
		modes: map[string]session.Mode{},
		runs:  map[string]*activeRun{},
		ops:   map[string]*sync.Mutex{},
	}
}

// Configure atomically replaces the runner and slash-command set, cancelling
// runs that still belong to the previous runtime.
func (s *Service) Configure(runner Runner, commands *command.Set) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelAllLocked()
	s.runner = runner
	s.commands = commands
}

// SetInbox replaces the admission boundary. It is intended for adapter tests;
// production callers configure the inbox once at construction time.
func (s *Service) SetInbox(inbox session.Inbox) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inbox = inbox
}

// Commands returns the currently configured slash commands.
func (s *Service) Commands() []command.Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commands == nil {
		return nil
	}
	return s.commands.List()
}

// SetCommands replaces only the command set. It is useful for adapter tests.
func (s *Service) SetCommands(commands *command.Set) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = commands
}

// Mode returns the current mode for wiring.Build and its runner.
func (s *Service) Mode(sessionID string) session.Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modes[sessionID]
}

// Send admits and runs a normal-mode user turn.
func (s *Service) Send(sessionID, text string, hooks Hooks) (RunHandle, error) {
	return s.send(sessionID, text, session.ModeNormal, hooks)
}

// SendPlan admits and runs a plan-mode user turn.
func (s *Service) SendPlan(sessionID, text string, hooks Hooks) (RunHandle, error) {
	return s.send(sessionID, text, session.ModePlan, hooks)
}

// AcceptPlan runs the fixed implementation prompt in normal mode.
func (s *Service) AcceptPlan(sessionID string, hooks Hooks) (RunHandle, error) {
	return s.send(sessionID, AcceptPlanPrompt, session.ModeNormal, hooks)
}

func (s *Service) send(sessionID, text string, mode session.Mode, hooks Hooks) (RunHandle, error) {
	operation := s.operationLock(sessionID)
	operation.Lock()
	defer operation.Unlock()
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()

	s.mu.Lock()
	runner := s.runner
	inbox := s.inbox
	commands := s.commands
	s.modes[sessionID] = mode
	s.mu.Unlock()
	if runner == nil {
		return RunHandle{}, fmt.Errorf("agent service: runner is not configured")
	}
	if inbox == nil {
		return RunHandle{}, fmt.Errorf("agent service: inbox is not configured")
	}
	if hooks.BeforeAdmit != nil {
		if err := hooks.BeforeAdmit(); err != nil {
			return RunHandle{}, err
		}
	}
	if commands != nil {
		if expanded, ok := commands.Resolve(text); ok {
			text = expanded
		}
	}
	if err := inbox.Admit(context.Background(), sessionID, session.Prompt{Text: text}, session.DeliveryQueue); err != nil {
		return RunHandle{}, err
	}
	if hooks.AfterAdmit != nil {
		hooks.AfterAdmit()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.mu.Lock()
	s.nextID++
	run := &activeRun{RunHandle: RunHandle{ID: s.nextID, done: done}, cancel: cancel}
	previous := s.runs[sessionID]
	if previous != nil {
		previous.cancel()
	}
	s.runs[sessionID] = run
	s.mu.Unlock()

	s.wg.Add(1)
	go s.execute(operation, runner, ctx, sessionID, run, previous, hooks.AfterRun)
	return run.RunHandle, nil
}

func (s *Service) execute(operation *sync.Mutex, runner Runner, ctx context.Context, sessionID string, run, previous *activeRun, afterRun func(RunResult)) {
	defer s.wg.Done()
	defer close(run.done)
	if previous != nil {
		<-previous.done
	}
	err := runner.Run(ctx, sessionID, false)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		err = nil
	}

	operation.Lock()
	s.mu.Lock()
	current := s.runs[sessionID] == run
	s.mu.Unlock()
	if afterRun != nil {
		afterRun(RunResult{ID: run.ID, Err: err, Current: current})
	}
	s.mu.Lock()
	if s.runs[sessionID] == run {
		delete(s.runs, sessionID)
	}
	s.mu.Unlock()
	operation.Unlock()
}

func (s *Service) operationLock(sessionID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	operation := s.ops[sessionID]
	if operation == nil {
		operation = &sync.Mutex{}
		s.ops[sessionID] = operation
	}
	return operation
}

// Synchronize runs fn exclusively with turn admission and completion hooks for
// the session. Adapters use it for operations such as undo and compaction.
func (s *Service) Synchronize(sessionID string, fn func() error) error {
	operation := s.operationLock(sessionID)
	operation.Lock()
	defer operation.Unlock()
	return fn()
}

// Stop cancels the active run and returns its completion handle.
func (s *Service) Stop(sessionID string) (RunHandle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[sessionID]
	if run == nil {
		return RunHandle{}, false
	}
	run.cancel()
	return run.RunHandle, true
}

// StopAll cancels every active run.
func (s *Service) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelAllLocked()
}

func (s *Service) cancelAllLocked() {
	for _, run := range s.runs {
		run.cancel()
	}
}

// Running reports whether a session currently has a registered run.
func (s *Service) Running(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[sessionID] != nil
}

// Forget cancels a session run and removes its mode.
func (s *Service) Forget(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[sessionID]; run != nil {
		run.cancel()
		delete(s.runs, sessionID)
	}
	delete(s.modes, sessionID)
}

// Wait blocks until all runs started so far have completed.
func (s *Service) Wait() { s.wg.Wait() }
