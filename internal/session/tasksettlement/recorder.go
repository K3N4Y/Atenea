// Package tasksettlement carries private task execution metadata from a task
// tool call to its durable session event.
package tasksettlement

import (
	"context"
	"sync/atomic"
	"time"
)

// Summary is the compact execution summary persisted on a task result.
type Summary struct {
	Requests  int
	Tokens    int
	Duration  time.Duration
	ToolCalls int
	Workspace string
}

// Recorder carries private task settlement data between execution and the
// durable publisher. It is safe for execution and publication goroutines.
type Recorder struct {
	toolCalls atomic.Int64
	requests  atomic.Int64
	tokens    atomic.Int64
	duration  atomic.Int64
	detached  atomic.Bool
	workspace atomic.Value
}

func NewRecorder() *Recorder { return &Recorder{} }

func (r *Recorder) SetSubagentToolCalls(total int) {
	if total < 0 {
		total = 0
	}
	r.toolCalls.Store(int64(total))
}

func (r *Recorder) SubagentToolCalls() int { return int(r.toolCalls.Load()) }

func (r *Recorder) SetTaskDetached() { r.detached.Store(true) }

func (r *Recorder) TaskDetached() bool { return r.detached.Load() }

func (r *Recorder) SetSummary(s Summary) {
	r.SetSubagentToolCalls(s.ToolCalls)
	r.requests.Store(int64(max(s.Requests, 0)))
	r.tokens.Store(int64(max(s.Tokens, 0)))
	r.duration.Store(int64(max(s.Duration, 0)))
	if s.Workspace != "" {
		r.workspace.Store(s.Workspace)
	}
}

func (r *Recorder) Summary() Summary {
	s := Summary{Requests: int(r.requests.Load()), Tokens: int(r.tokens.Load()), Duration: time.Duration(r.duration.Load()), ToolCalls: r.SubagentToolCalls()}
	if workspace := r.workspace.Load(); workspace != nil {
		s.Workspace, _ = workspace.(string)
	}
	return s
}

type recorderKey struct{}

func WithRecorder(ctx context.Context, recorder *Recorder) context.Context {
	return context.WithValue(ctx, recorderKey{}, recorder)
}

func FromContext(ctx context.Context) *Recorder {
	recorder, _ := ctx.Value(recorderKey{}).(*Recorder)
	return recorder
}
