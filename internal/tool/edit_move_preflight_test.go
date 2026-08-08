package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

func TestHashlineMoveCreatesParentsOnlyAfterFullPreflight(t *testing.T) {
	t.Run("stale hash", func(t *testing.T) {
		root := t.TempDir()
		snaps := hashline.NewMemSnapshotStore()
		path := filepath.Join(root, "source.txt")
		if err := os.WriteFile(path, []byte("live\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		snaps.Record(path, "stale\n")
		edit := NewEditTool(root, hashline.OSFilesystem{}, snaps)
		raw, _ := json.Marshal(map[string]string{"input": "[source.txt#DEAD]\nMV missing/nested/destination.txt"})
		if _, err := edit.Execute(context.Background(), raw); err == nil {
			t.Fatal("stale hash move succeeded")
		}
		if _, err := os.Stat(filepath.Join(root, "missing")); !os.IsNotExist(err) {
			t.Fatalf("stale preflight created parent: %v", err)
		}
	})

	t.Run("invalid later section", func(t *testing.T) {
		root := t.TempDir()
		snaps := hashline.NewMemSnapshotStore()
		first, second := filepath.Join(root, "first.txt"), filepath.Join(root, "second.txt")
		if err := os.WriteFile(first, []byte("first\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(second, []byte("second\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		h1, _ := snaps.Record(first, "first\n")
		h2, _ := snaps.Record(second, "second\n")
		edit := NewEditTool(root, hashline.OSFilesystem{}, snaps)
		input := "[first.txt#" + h1 + "]\nMV missing/nested/destination.txt\n[second.txt#" + h2 + "]\nPUT 9:\n+invalid"
		raw, _ := json.Marshal(map[string]string{"input": input})
		if _, err := edit.Execute(context.Background(), raw); err == nil {
			t.Fatal("invalid later section succeeded")
		}
		if _, err := os.Stat(filepath.Join(root, "missing")); !os.IsNotExist(err) {
			t.Fatalf("later preflight failure created parent: %v", err)
		}
	})

	t.Run("successful move", func(t *testing.T) {
		root := t.TempDir()
		snaps := hashline.NewMemSnapshotStore()
		source := filepath.Join(root, "source.txt")
		want := []byte("landed bytes\n")
		if err := os.WriteFile(source, want, 0o644); err != nil {
			t.Fatal(err)
		}
		h, _ := snaps.Record(source, string(want))
		edit := NewEditTool(root, hashline.OSFilesystem{}, snaps)
		raw, _ := json.Marshal(map[string]string{"input": "[source.txt#" + h + "]\nMV missing/nested/destination.txt"})
		if _, err := edit.Execute(context.Background(), raw); err != nil {
			t.Fatalf("move: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(root, "missing", "nested", "destination.txt"))
		if err != nil || string(got) != string(want) {
			t.Fatalf("destination=%q err=%v", got, err)
		}
		if _, err := os.Stat(source); !os.IsNotExist(err) {
			t.Fatalf("source remains: %v", err)
		}
	})
}
