package tool

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/agentcore/memory"
)

type memoryStub struct {
	mu   sync.Mutex
	fact memory.Fact
}

func (s *memoryStub) Retain(_ context.Context, project, text, source string) (memory.Fact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fact = memory.Fact{ID: 7, Project: project, Text: text, Source: source, CreatedAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)}
	return s.fact, nil
}
func (s *memoryStub) Recall(context.Context, string, string, int) ([]memory.Fact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []memory.Fact{s.fact}, nil
}

func TestMemoryToolsExposeProvenanceAndAge(t *testing.T) {
	store := &memoryStub{}
	retain := NewRetainMemoryTool("/project", store)
	result, err := retain.Execute(context.Background(), json.RawMessage(`{"text":"Use SQLite","source":"docs/architecture.md:12"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "docs/architecture.md:12") || !strings.Contains(result.Output, "2026-08-01T12:00:00Z") {
		t.Fatalf("retain output = %q, want source and timestamp", result.Output)
	}
	recall := NewRecallMemoryTool("/project", store)
	recall.now = func() time.Time { return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC) }
	result, err = recall.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"historical evidence", "Use SQLite", "source: docs/architecture.md:12", "retained_at: 2026-08-01T12:00:00Z", "age: 24h0m0s"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("recall output = %q, missing %q", result.Output, want)
		}
	}
}
