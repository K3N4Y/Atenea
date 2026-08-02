package tool

import (
	"context"
	"sync/atomic"
	"time"
)

// TaskSettlement is the compact execution summary persisted on a task result.
type TaskSettlement struct {
	Requests  int
	Tokens    int
	Duration  time.Duration
	ToolCalls int
	Workspace string
}

// SettlementRecorder carries private task settlement data between execution and
// the durable publisher. It is safe for execution and publication goroutines.
type SettlementRecorder struct {
	toolCalls atomic.Int64
	requests  atomic.Int64
	tokens    atomic.Int64
	duration  atomic.Int64
	detached  atomic.Bool
	workspace atomic.Value
}

func NewSettlementRecorder() *SettlementRecorder { return &SettlementRecorder{} }

func (r *SettlementRecorder) SetSubagentToolCalls(total int) {
	if total < 0 {
		total = 0
	}
	r.toolCalls.Store(int64(total))
}

func (r *SettlementRecorder) SubagentToolCalls() int { return int(r.toolCalls.Load()) }

func (r *SettlementRecorder) SetTaskDetached() { r.detached.Store(true) }

func (r *SettlementRecorder) TaskDetached() bool { return r.detached.Load() }

func (r *SettlementRecorder) SetTaskSettlement(s TaskSettlement) {
	r.SetSubagentToolCalls(s.ToolCalls)
	r.requests.Store(int64(max(s.Requests, 0)))
	r.tokens.Store(int64(max(s.Tokens, 0)))
	r.duration.Store(int64(max(s.Duration, 0)))
	if s.Workspace != "" {
		r.workspace.Store(s.Workspace)
	}
}

func (r *SettlementRecorder) TaskSettlement() TaskSettlement {
	s := TaskSettlement{Requests: int(r.requests.Load()), Tokens: int(r.tokens.Load()), Duration: time.Duration(r.duration.Load()), ToolCalls: r.SubagentToolCalls()}
	if workspace := r.workspace.Load(); workspace != nil {
		s.Workspace, _ = workspace.(string)
	}
	return s
}

type settlementRecorderKey struct{}

func WithSettlementRecorder(ctx context.Context, recorder *SettlementRecorder) context.Context {
	return context.WithValue(ctx, settlementRecorderKey{}, recorder)
}

func SettlementRecorderFrom(ctx context.Context) *SettlementRecorder {
	recorder, _ := ctx.Value(settlementRecorderKey{}).(*SettlementRecorder)
	return recorder
}
