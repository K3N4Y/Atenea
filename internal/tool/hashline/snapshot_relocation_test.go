package hashline

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestMoveRelocatesSnapshotHistoryExactly(t *testing.T) {
	store := NewMemSnapshotStore()
	store.Record("destination", "destination-old\n")
	versions := map[string]uint64{}
	for _, text := range []string{"one\n", "two\n", "three\n", "four\n"} {
		h, _ := store.Record("source", text)
		s := store.ByContent(text)[0]
		versions[text] = s.Version
		store.RecordSeenSnapshot("source", s.Version, []int{1})
		if store.ByHash("source", h) == nil {
			t.Fatalf("missing source version for %q", text)
		}
	}
	store.Relocate("source", "destination")
	if store.Head("source") != nil || len(store.FindByHash(ComputeFileHash("destination-old\n"))) != 0 {
		t.Fatalf("source=%+v destination-old unexpectedly retained=%+v", store.Head("source"), store.ByContent("destination-old\n"))
	}
	want := []string{"four\n", "three\n", "two\n", "one\n"}
	for i, text := range want {
		matches := store.ByContent(text)
		if len(matches) != 1 || matches[0].Path != "destination" || matches[0].Version != versions[text] {
			t.Fatalf("version %d text=%q matches=%+v", i, text, matches)
		}
		if _, ok := matches[0].Seen[1]; !ok {
			t.Fatalf("seen provenance lost for %q: %+v", text, matches[0])
		}
	}

	// DestinationCommitted with SourceRemains must preserve separate histories.
	separate := NewMemSnapshotStore()
	h, _ := separate.Record("source", "old\n")
	fs := &round4MoveFS{files: map[string][]byte{"source": []byte("old\n")}, err: &DestinationCommittedError{Err: errors.New("source remove failed"), SourceRemains: true}}
	results, err := NewPatcher(fs, separate).ApplyConfiguredResults(Patch{Sections: []Section{{Path: "source", Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{1, 1}, Text: "new"}}, FileOp: FileOp{MoveTo: "destination"}}}}, false)
	var committed *CommittedError
	if !errors.As(err, &committed) || len(results) != 1 || separate.Head("source") == nil || separate.Head("source").Text != "old\n" || separate.Head("destination") == nil || separate.Head("destination").Text != "new\n" {
		t.Fatalf("results=%+v err=%v source=%+v destination=%+v", results, err, separate.Head("source"), separate.Head("destination"))
	}

	// Relocated Seen identity remains enforceable and chainable.
	p := NewPatcher(&round4MoveFS{files: map[string][]byte{"destination": []byte("four\n")}}, store)
	p.EnforceSeenLines = true
	head := store.Head("destination")
	got, err := p.ApplyConfiguredResults(Patch{Sections: []Section{{Path: "destination", Hash: head.Hash, Edits: []Edit{{Kind: Replace, Range: Range{1, 1}, Text: "FOUR"}}}}}, true)
	if err != nil || len(got) != 1 || got[0].NewText != "FOUR\n" {
		t.Fatalf("enforced chain=%+v err=%v", got, err)
	}
}

type round4MoveFS struct {
	files map[string][]byte
	err   error
}

func (f *round4MoveFS) ReadFile(p string) ([]byte, error) {
	b, ok := f.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}
func (f *round4MoveFS) WriteFile(p string, b []byte, _ os.FileMode) error {
	f.files[p] = append([]byte(nil), b...)
	return nil
}
func (f *round4MoveFS) MoveWithContent(a, b string, data []byte, _ os.FileMode) error {
	f.files[b] = append([]byte(nil), data...)
	var dc *DestinationCommittedError
	if !errors.As(f.err, &dc) || !dc.SourceRemains {
		delete(f.files, a)
	}
	return f.err
}

func TestRelocateDestinationMergeOrderIsStable(t *testing.T) {
	s := NewMemSnapshotStore()
	s.Record("source", "shared")
	s.RecordSeenContent("source", "shared", []int{1})
	s.Record("destination", "shared")
	s.RecordSeenContent("destination", "shared", []int{2})
	s.Relocate("source", "destination")
	got := s.ByContent("shared")
	if len(got) != 1 || !reflect.DeepEqual(got[0].Seen, map[int]struct{}{1: {}, 2: {}}) {
		t.Fatalf("merged=%+v", got)
	}
}
