package hashline

import (
	"errors"
	"sync"
	"testing"
)

func TestMoveWithContentSettlementBoundaries(t *testing.T) {
	for _, stage := range []string{"createTemp", "write", "shortWrite", "chmod", "fileSync", "fileClose", "publish", "dirOpen", "dirSync", "dirClose", "sourceRemove", "sourceDirOpen", "sourceDirSync", "sourceDirClose"} {
		t.Run(stage, func(t *testing.T) {
			f := newFaultOps(map[string]string{"/s/a": "old"})
			f.fault = stage
			err := durableMoveContentWithOps(f, "/s/a", "/d/b", []byte("new"), 0640)
			_, sourceExists := f.files["/s/a"]
			destination, destinationExists := f.files["/d/b"]
			published := stage == "dirOpen" || stage == "dirSync" || stage == "dirClose" || stage == "sourceRemove" || stage == "sourceDirOpen" || stage == "sourceDirSync" || stage == "sourceDirClose"
			if published != destinationExists || destinationExists && string(destination.data) != "new" {
				t.Fatalf("stage=%s err=%v files=%v", stage, err, f.files)
			}
			if !published && !sourceExists {
				t.Fatalf("source changed before publication: stage=%s files=%v", stage, f.files)
			}
			if published {
				var committed *DestinationCommittedError
				var uncertain *CommitUncertainError
				if !errors.As(err, &committed) && !errors.As(err, &uncertain) {
					t.Fatalf("published stage error=%T %v", err, err)
				}
			}
		})
	}

	f := newFaultOps(map[string]string{"/s/a": "old", "/d/b": "racer"})
	if err := durableMoveContentWithOps(f, "/s/a", "/d/b", []byte("new"), 0640); err == nil || string(f.files["/s/a"].data) != "old" || string(f.files["/d/b"].data) != "racer" {
		t.Fatalf("collision mutated state: err=%v files=%v", err, f.files)
	}
}

func TestCollisionProvenanceUsesExactSnapshotIdentity(t *testing.T) {
	a, b := collisionFixture(t)
	store := NewMemSnapshotStore()
	hash, _ := store.Record("p", a)
	store.Record("p", b)

	store.RecordSeenLines("p", hash, []int{1})
	if len(store.ByContent(a)[0].Seen) != 0 || len(store.ByContent(b)[0].Seen) != 0 {
		t.Fatal("ambiguous path+hash granted provenance")
	}
	selected := store.ByContent(a)[0]
	store.RecordSeenSnapshot("p", selected.Version, []int{1})
	if _, ok := store.ByContent(a)[0].Seen[1]; !ok {
		t.Fatal("exact selected snapshot was not granted")
	}
	if _, ok := store.ByContent(b)[0].Seen[1]; ok {
		t.Fatal("selected collider granted the other candidate")
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.RecordSeenSnapshot("p", selected.Version, []int{1})
			_ = store.Candidates("p", hash)
		}()
	}
	wg.Wait()
}
