package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// Provenance: restored no-op loop protection and session-isolation coverage.
func TestHashlineNoopFirstSecondThirdResetAndSessionIsolation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x")
	os.WriteFile(path, []byte("A"), 0644)
	snaps := hashline.NewMemSnapshotStore()
	h, _ := snaps.Record(path, "A")
	et := NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, NewSessionSnapshots())
	// Seed each provider store as a real read would.
	for _, session := range []string{"one", "two"} {
		et.SnapshotProvider.Snapshots(WithSessionID(context.Background(), session)).Record(path, "A")
	}
	raw, _ := json.Marshal(map[string]string{"input": "[x#" + h + "]\nPUT 1:\n+A"})
	for _, session := range []string{"one", "two"} {
		ctx := WithSessionID(context.Background(), session)
		for attempt := 1; attempt <= 3; attempt++ {
			res, err := et.Execute(ctx, raw)
			if attempt < 3 {
				if err != nil || len(res.Files) != 1 || res.Files[0].Operation != contract.FileNoop {
					t.Fatalf("session=%s attempt=%d result=%+v err=%v", session, attempt, res, err)
				}
			} else {
				var repeated *RepeatedNoopError
				if !errors.As(err, &repeated) {
					t.Fatalf("session=%s third err=%v", session, err)
				}
			}
		}
	}
	got, _ := os.ReadFile(path)
	if string(got) != "A" {
		t.Fatalf("noop wrote %q", got)
	}

	ctx := WithSessionID(context.Background(), "reset")
	et.SnapshotProvider.Snapshots(ctx).Record(path, "A")
	et.Execute(ctx, raw)
	change, _ := json.Marshal(map[string]string{"input": "[x#" + h + "]\nPUT 1:\n+B"})
	if _, err := et.Execute(ctx, change); err != nil {
		t.Fatal(err)
	}
	newHash := hashline.ComputeFileHash("B")
	noopB, _ := json.Marshal(map[string]string{"input": "[x#" + newHash + "]\nPUT 1:\n+B"})
	if _, err := et.Execute(ctx, noopB); err != nil {
		t.Fatalf("reset first noop=%v", err)
	}
}

func TestHashlineNoopConcurrentSessionsRaceSafe(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x")
	os.WriteFile(path, []byte("A"), 0644)
	provider := NewSessionSnapshots()
	et := NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, provider)
	h := hashline.ComputeFileHash("A")
	raw, _ := json.Marshal(map[string]string{"input": "[x#" + h + "]\nPUT 1:\n+A"})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := WithSessionID(context.Background(), string(rune('a'+i)))
			provider.Snapshots(ctx).Record(path, "A")
			_, _ = et.Execute(ctx, raw)
		}(i)
	}
	wg.Wait()
}

func TestMultiSectionNoopPreflightWritesNothing(t *testing.T) {
	fs := &memoryEditFS{files: map[string][]byte{"a": []byte("A"), "b": []byte("B")}}
	snaps := hashline.NewMemSnapshotStore()
	ha, _ := snaps.Record("a", "A")
	hb, _ := snaps.Record("b", "B")
	et := NewEditTool(".", fs, snaps)
	raw, _ := json.Marshal(map[string]string{"input": "[a#" + ha + "]\nPUT 1:\n+A\n[b#" + hb + "]\nPUT 1:\n+BB"})
	if _, err := et.Execute(context.Background(), raw); err == nil {
		t.Fatal("multi-section no-op accepted")
	}
	if string(fs.files["a"]) != "A" || string(fs.files["b"]) != "B" {
		t.Fatalf("files=%q/%q", fs.files["a"], fs.files["b"])
	}
}
