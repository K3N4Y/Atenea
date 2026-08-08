package tasksettlement

import (
	"context"
	"sync"
	"testing"
)

func TestRecorderContextAndConcurrency(t *testing.T) {
	r := NewRecorder()
	ctx := WithRecorder(context.Background(), r)
	if FromContext(ctx) != r {
		t.Fatal("recorder not recovered from context")
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) { defer wg.Done(); r.SetSubagentToolCalls(n) }(i)
	}
	wg.Wait()
	if got := r.SubagentToolCalls(); got < 0 || got >= 100 {
		t.Fatalf("total = %d", got)
	}
	r.SetSubagentToolCalls(-1)
	if r.SubagentToolCalls() != 0 {
		t.Fatal("negative total was not clamped")
	}
}
