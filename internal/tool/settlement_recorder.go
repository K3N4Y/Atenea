package tool

import (
	"context"
	"sync/atomic"
)

// SettlementRecorder carries private task settlement data between execution and
// the durable publisher. It is safe for execution and publication goroutines.
type SettlementRecorder struct {
	total atomic.Int64
}

func NewSettlementRecorder() *SettlementRecorder { return &SettlementRecorder{} }

func (r *SettlementRecorder) SetSubagentToolCalls(total int) {
	if total < 0 {
		total = 0
	}
	r.total.Store(int64(total))
}

func (r *SettlementRecorder) SubagentToolCalls() int { return int(r.total.Load()) }

type settlementRecorderKey struct{}

func WithSettlementRecorder(ctx context.Context, recorder *SettlementRecorder) context.Context {
	return context.WithValue(ctx, settlementRecorderKey{}, recorder)
}

func SettlementRecorderFrom(ctx context.Context) *SettlementRecorder {
	recorder, _ := ctx.Value(settlementRecorderKey{}).(*SettlementRecorder)
	return recorder
}
