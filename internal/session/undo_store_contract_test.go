package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestUndoStoreContract_Memory(t *testing.T) {
	testUndoStoreContract(t, func(t *testing.T) UndoStore { return NewMemoryStore() })
}

func TestUndoStoreContract_SQLite(t *testing.T) {
	testUndoStoreContract(t, func(t *testing.T) UndoStore {
		store, err := NewSQLiteStore(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		return store
	})
}

func testUndoStoreContract(t *testing.T, newStore func(*testing.T) UndoStore) {
	appendEvent := func(t *testing.T, store Store, event SessionEvent) {
		t.Helper()
		if _, err := store.AppendEvent(context.Background(), "s1", event); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint := func(id, prompt, before, after string) *PromptCheckpoint {
		return &PromptCheckpoint{ID: id, Prompt: prompt, BeforeTree: before, AfterTree: after}
	}

	t.Run("revert hides range from events messages and context", func(t *testing.T) {
		store := newStore(t)
		appendEvent(t, store, SessionEvent{Kind: KindPromptCheckpointStarted, Checkpoint: checkpoint("cp-1", "first", "before-1", "")})
		appendEvent(t, store, SessionEvent{Kind: KindComposerPrompt, Text: "first"})
		appendEvent(t, store, SessionEvent{Message: &Message{ID: "u1", Role: RoleUser, Text: "first"}})
		appendEvent(t, store, SessionEvent{Kind: KindPromptCheckpointFinished, Checkpoint: checkpoint("cp-1", "", "", "after-1")})
		appendEvent(t, store, SessionEvent{Kind: KindPromptCheckpointReverted, Checkpoint: checkpoint("cp-1", "", "", "")})
		if events, err := store.Events(context.Background(), "s1", 0); err != nil || len(events) != 0 {
			t.Fatalf("Events = %+v, err = %v", events, err)
		}
		if messages, err := store.Messages(context.Background(), "s1", 0); err != nil || len(messages) != 0 {
			t.Fatalf("Messages = %+v, err = %v", messages, err)
		}
		if contextual, ok := store.(CompactionStore); ok {
			got, err := contextual.ContextForRunner(context.Background(), "s1")
			if err != nil || len(got.Messages) != 0 {
				t.Fatalf("ContextForRunner = %+v, err = %v", got, err)
			}
		}
	})

	t.Run("repeated revert selects previous effective checkpoint", func(t *testing.T) {
		store := newStore(t)
		for _, event := range []SessionEvent{
			{Kind: KindPromptCheckpointStarted, Checkpoint: checkpoint("cp-1", "first", "before-1", "")},
			{Message: &Message{ID: "u1", Role: RoleUser, Text: "first"}},
			{Kind: KindPromptCheckpointFinished, Checkpoint: checkpoint("cp-1", "", "", "after-1")},
			{Kind: KindPromptCheckpointStarted, Checkpoint: checkpoint("cp-2", "second", "before-2", "")},
			{Message: &Message{ID: "u2", Role: RoleUser, Text: "second"}},
			{Kind: KindPromptCheckpointFinished, Checkpoint: checkpoint("cp-2", "", "", "after-2")},
			{Kind: KindPromptCheckpointReverted, Checkpoint: checkpoint("cp-2", "", "", "")},
		} {
			appendEvent(t, store, event)
		}
		got, err := store.LatestPromptCheckpoint(context.Background(), "s1")
		if err != nil || got.ID != "cp-1" {
			t.Fatalf("LatestPromptCheckpoint = %+v, err = %v", got, err)
		}
	})

	t.Run("reverted composer prompt is hidden", func(t *testing.T) {
		store := newStore(t)
		appendEvent(t, store, SessionEvent{Kind: KindPromptCheckpointStarted, Checkpoint: checkpoint("cp-1", "first", "before", "")})
		appendEvent(t, store, SessionEvent{Kind: KindComposerPrompt, Text: "first"})
		appendEvent(t, store, SessionEvent{Kind: KindPromptCheckpointFinished, Checkpoint: checkpoint("cp-1", "", "", "after")})
		appendEvent(t, store, SessionEvent{Kind: KindPromptCheckpointReverted, Checkpoint: checkpoint("cp-1", "", "", "")})
		events, err := store.Events(context.Background(), "s1", 0)
		if err != nil || len(events) != 0 {
			t.Fatalf("Events = %+v, err = %v", events, err)
		}
	})

	t.Run("epoch changes after revert", func(t *testing.T) {
		store := newStore(t)
		appendEvent(t, store, SessionEvent{Kind: KindPromptCheckpointStarted, Checkpoint: checkpoint("cp-1", "first", "before", "")})
		before, err := store.Epoch(context.Background(), "s1")
		if err != nil {
			t.Fatal(err)
		}
		appendEvent(t, store, SessionEvent{Kind: KindPromptCheckpointReverted, Checkpoint: checkpoint("cp-1", "", "", "")})
		after, err := store.Epoch(context.Background(), "s1")
		if err != nil || after.Revision != before.Revision+1 {
			t.Fatalf("epoch before=%+v after=%+v err=%v", before, after, err)
		}
	})

	t.Run("nothing remains after all checkpoints revert", func(t *testing.T) {
		store := newStore(t)
		appendEvent(t, store, SessionEvent{Kind: KindPromptCheckpointStarted, Checkpoint: checkpoint("cp-1", "first", "before", "")})
		appendEvent(t, store, SessionEvent{Kind: KindPromptCheckpointReverted, Checkpoint: checkpoint("cp-1", "", "", "")})
		_, err := store.LatestPromptCheckpoint(context.Background(), "s1")
		if !errors.Is(err, ErrNothingToUndo) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestUndoStoreContract_SQLiteCheckpointSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendEvent(context.Background(), "s1", SessionEvent{Kind: KindPromptCheckpointStarted, Checkpoint: &PromptCheckpoint{ID: "cp-1", Prompt: "first", BeforeTree: "before"}})
	if err != nil {
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
	got, err := store.LatestPromptCheckpoint(context.Background(), "s1")
	if err != nil || got.ID != "cp-1" || got.Prompt != "first" || got.BeforeTree != "before" {
		t.Fatalf("checkpoint = %+v, err = %v", got, err)
	}
}
