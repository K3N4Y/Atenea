// Package memory defines explicit project-memory contracts.
package memory

import (
	"context"
	"time"
)

const (
	DefaultRecallLimit = 10
	MaxRecallLimit     = 50
)

// Fact is an explicitly retained project fact. Source and CreatedAt ensure a
// recall can present provenance instead of silently treating text as truth.
type Fact struct {
	ID        int64
	Project   string
	Text      string
	Source    string
	CreatedAt time.Time
}

// Store persists facts under a project identity. Implementations never inject
// facts into prompts; callers must explicitly retain and recall them.
type Store interface {
	Retain(ctx context.Context, project, text, source string) (Fact, error)
	Recall(ctx context.Context, project, query string, limit int) ([]Fact, error)
}
