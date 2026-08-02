package session

import (
	"context"
	"path/filepath"
	"testing"
)

func TestProjectMemoryPersistsFiltersAndIsolatesProjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := store.Retain(ctx, "/project/a", "Use SQLite for durable state", "docs/architecture.md:12")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Retain(ctx, "/project/a", "Run tests with race detection", "AGENTS.md:30"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Retain(ctx, "/project/b", "Private to project B", "user statement"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	facts, err := store.Recall(ctx, "/project/a", "sqlite", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].ID != first.ID || facts[0].Text != first.Text || facts[0].Source != first.Source || facts[0].CreatedAt.IsZero() {
		t.Fatalf("Recall = %+v, want persisted fact with provenance", facts)
	}
	facts, err = store.Recall(ctx, "/project/a", "private", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 0 {
		t.Fatalf("cross-project Recall = %+v, want none", facts)
	}
}

func TestMemoryStoreRecallsNewestFirstAndUsesDefaultLimit(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	for i := 0; i < DefaultMemoryRecallLimit+2; i++ {
		if _, err := store.Retain(ctx, "project", string(rune('a'+i)), "test"); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := store.Recall(ctx, "project", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != DefaultMemoryRecallLimit || facts[0].ID <= facts[len(facts)-1].ID {
		t.Fatalf("Recall = %+v, want default limit newest first", facts)
	}
}
