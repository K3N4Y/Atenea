package hashline

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestPreviewDoesNotChangeSnapshotRecencyOrProvenance(t *testing.T) {
	root := t.TempDir()
	previewStore := NewMemSnapshotStore()
	controlStore := NewMemSnapshotStore()
	paths := make([]string, defaultMaxPaths)
	texts := make([]string, defaultMaxPaths)
	for i := range paths {
		paths[i] = filepath.Join(root, fmt.Sprintf("p%02d.txt", i))
		texts[i] = fmt.Sprintf("line %02d\n", i)
		if err := os.WriteFile(paths[i], []byte(texts[i]), 0o644); err != nil {
			t.Fatal(err)
		}
		previewStore.Record(paths[i], texts[i])
		controlStore.Record(paths[i], texts[i])
		previewStore.RecordSeenLines(paths[i], ComputeFileHash(texts[i]), []int{1})
		controlStore.RecordSeenLines(paths[i], ComputeFileHash(texts[i]), []int{1})
	}
	beforeHistory := cloneSnapshotHistory(previewStore.history)
	beforeClock := previewStore.clock
	patcher := NewPatcher(OSFilesystem{}, previewStore)
	for i := 0; i < 100; i++ {
		for _, index := range []int{0, len(paths) - 1} {
			patch := Patch{Sections: []Section{{Path: paths[index], Hash: ComputeFileHash(texts[index]), Edits: []Edit{{Kind: Replace, Range: Range{Start: 1, End: 1}, Text: "changed"}}}}}
			if _, err := patcher.Preview(patch); err != nil {
				t.Fatal(err)
			}
		}
	}
	if previewStore.clock != beforeClock || !reflect.DeepEqual(previewStore.history, beforeHistory) {
		t.Fatal("preview changed snapshot history, provenance, or recency")
	}
	newPath := filepath.Join(root, "new.txt")
	previewStore.Record(newPath, "new\n")
	controlStore.Record(newPath, "new\n")
	for _, path := range append(paths, newPath) {
		if (previewStore.Head(path) != nil) != (controlStore.Head(path) != nil) {
			t.Fatalf("eviction differs for %s", path)
		}
	}
	if previewStore.Head(paths[0]) != nil || previewStore.Head(paths[1]) == nil {
		t.Fatal("unexpected control eviction order")
	}
}

func TestConcurrentPreviewAndExecutionSnapshotLookupRace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewMemSnapshotStore()
	h, _ := store.Record(path, "old\n")
	patcher := NewPatcher(OSFilesystem{}, store)
	previewPatch := Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{Start: 1, End: 1}, Text: "preview"}}}}}
	executePatch := Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{Start: 1, End: 1}, Text: "execute"}}}}}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _ = patcher.Preview(previewPatch)
	}()
	go func() {
		defer wg.Done()
		<-start
		if _, err := patcher.Apply(executePatch); err != nil {
			t.Errorf("execute: %v", err)
		}
	}()
	close(start)
	wg.Wait()
	if got, _ := os.ReadFile(path); string(got) != "execute\n" {
		t.Fatalf("disk=%q", got)
	}
	if head := store.Head(path); head == nil || head.Text != "execute\n" {
		t.Fatalf("head=%+v", head)
	}
}

func cloneSnapshotHistory(history map[string][]*Snapshot) map[string][]*Snapshot {
	out := make(map[string][]*Snapshot, len(history))
	for path, snapshots := range history {
		for _, snapshot := range snapshots {
			out[path] = append(out[path], copySnapshot(snapshot))
		}
	}
	return out
}
