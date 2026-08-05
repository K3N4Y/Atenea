package subagent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/K3N4Y/atenea/internal/tool"
)

const completedJobCap = 128

type jobProgress struct {
	mu        sync.RWMutex
	summary   tool.TaskSettlement
	workspace string
}

func (p *jobProgress) set(summary tool.TaskSettlement) {
	p.mu.Lock()
	p.summary = summary
	p.mu.Unlock()
}
func (p *jobProgress) setWorkspace(workspace string) {
	p.mu.Lock()
	p.workspace = workspace
	p.mu.Unlock()
}
func (p *jobProgress) get() (tool.TaskSettlement, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.summary, p.workspace
}

type job struct {
	id       string
	status   string
	result   string
	err      error
	started  time.Time
	ended    time.Time
	cancel   context.CancelFunc
	done     chan struct{}
	progress jobProgress
}

type Supervisor struct {
	mu        sync.RWMutex
	jobs      map[string]*job
	completed []string
	nextID    func() string
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closed    bool
}

func NewSupervisor(nextID func() string) *Supervisor {
	ctx, cancel := context.WithCancel(context.Background())
	return &Supervisor{jobs: make(map[string]*job), nextID: nextID, ctx: ctx, cancel: cancel}
}

// Close prevents new jobs, cancels every running job, and waits for them.
func (s *Supervisor) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Supervisor) start(run func(context.Context, *jobProgress) (string, error)) (tool.Result, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return tool.Result{}, errors.New("task supervisor is closed")
	}
	ctx, cancel := context.WithCancel(s.ctx)
	j := &job{id: s.nextID(), status: "running", started: time.Now(), cancel: cancel, done: make(chan struct{})}
	s.jobs[j.id] = j
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		result, err := run(ctx, &j.progress)
		s.mu.Lock()
		j.result, j.err, j.ended = result, err, time.Now()
		if errors.Is(err, context.Canceled) {
			j.status = "cancelled"
		} else if err != nil {
			j.status = "failed"
		} else {
			j.status = "completed"
		}
		s.completed = append(s.completed, j.id)
		if len(s.completed) > completedJobCap {
			old := s.completed[0]
			s.completed = s.completed[1:]
			delete(s.jobs, old)
		}
		close(j.done)
		s.mu.Unlock()
		cancel()
	}()
	return jsonResult(map[string]any{"job_id": j.id, "status": "running"}), nil
}

func (s *Supervisor) lookup(id string) (*job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j := s.jobs[id]
	if j == nil {
		return nil, fmt.Errorf("unknown task job %q", id)
	}
	return j, nil
}

func (s *Supervisor) snapshot(j *job, includeResult bool) tool.Result {
	s.mu.RLock()
	status, result, err, started, ended := j.status, j.result, j.err, j.started, j.ended
	s.mu.RUnlock()
	usage, workspace := j.progress.get()
	if ended.IsZero() {
		ended = time.Now()
	}
	out := map[string]any{"job_id": j.id, "status": status, "requests": usage.Requests, "tokens": usage.Tokens, "tool_calls": usage.ToolCalls, "duration_ms": ended.Sub(started).Milliseconds()}
	if workspace != "" {
		out["worktree"] = workspace
	}
	if includeResult && result != "" {
		out["result"] = result
	}
	if err != nil {
		out["error"] = err.Error()
	}
	return jsonResult(out)
}

func (s *Supervisor) tools() []tool.Tool {
	return []tool.Tool{&supervisorTool{name: "task_status", supervisor: s}, &supervisorTool{name: "task_wait", supervisor: s}, &supervisorTool{name: "task_cancel", supervisor: s}}
}

//go:embed task_status.txt
var taskStatusDescription string

//go:embed task_wait.txt
var taskWaitDescription string

//go:embed task_cancel.txt
var taskCancelDescription string

type supervisorTool struct {
	name       string
	supervisor *Supervisor
}

func (t *supervisorTool) Name() string { return t.name }
func (t *supervisorTool) Description() string {
	switch t.name {
	case "task_status":
		return taskStatusDescription
	case "task_wait":
		return taskWaitDescription
	case "task_cancel":
		return taskCancelDescription
	default:
		return ""
	}
}
func (t *supervisorTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string"}},"required":["job_id"]}`)
}
func (t *supervisorTool) Effects() tool.Effects { return tool.NoEffects }
func (t *supervisorTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	var in struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(input, &in); err != nil || in.JobID == "" {
		return tool.Result{}, fmt.Errorf("%s: valid job_id required", t.name)
	}
	j, err := t.supervisor.lookup(in.JobID)
	if err != nil {
		return tool.Result{}, err
	}
	switch t.name {
	case "task_status":
		return t.supervisor.snapshot(j, false), nil
	case "task_cancel":
		t.supervisor.mu.Lock()
		if j.status == "running" {
			j.status = "cancelling"
		}
		t.supervisor.mu.Unlock()
		j.cancel()
		return t.supervisor.snapshot(j, false), nil
	case "task_wait":
		select {
		case <-j.done:
			return t.supervisor.snapshot(j, true), nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	default:
		return tool.Result{}, fmt.Errorf("unknown supervision operation")
	}
}

func jsonResult(value any) tool.Result {
	raw, _ := json.Marshal(value)
	return tool.Result{Output: string(raw)}
}
