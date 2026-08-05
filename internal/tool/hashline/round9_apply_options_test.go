package hashline

import (
	"errors"
	"testing"
)

func TestPostPreflightHookRunsOnceOnlyAfterCompleteFinalPreflight(t *testing.T) {
	fs := &transactionFS{files: map[string][]byte{"a": []byte("A"), "b": []byte("B")}}
	snaps := NewMemSnapshotStore()
	ha, _ := snaps.Record("a", "A")
	hb, _ := snaps.Record("b", "B")
	p := NewPatcher(fs, snaps)

	calls := 0
	_, err := p.ApplyConfiguredResultsWithOptions(Patch{Sections: []Section{
		{Path: "a", Hash: ha, Edits: []Edit{{Kind: Replace, Range: Range{1, 1}, Text: "AA"}}},
		{Path: "b", Hash: hb, Edits: []Edit{{Kind: Replace, Range: Range{2, 2}, Text: "invalid"}}},
	}}, false, ApplyOptions{PostPreflight: func() error { calls++; return nil }})
	if err == nil || calls != 0 || len(fs.writes) != 0 {
		t.Fatalf("invalid transaction: err=%v hook calls=%d writes=%v", err, calls, fs.writes)
	}

	boom := errors.New("parent preparation failed")
	_, err = p.ApplyConfiguredResultsWithOptions(Patch{Sections: []Section{
		{Path: "a", Hash: ha, Edits: []Edit{{Kind: Replace, Range: Range{1, 1}, Text: "AA"}}},
		{Path: "b", Hash: hb, Edits: []Edit{{Kind: Replace, Range: Range{1, 1}, Text: "BB"}}},
	}}, false, ApplyOptions{PostPreflight: func() error { calls++; return boom }})
	if !errors.Is(err, boom) || calls != 1 || len(fs.writes) != 0 {
		t.Fatalf("hook failure: err=%v hook calls=%d writes=%v", err, calls, fs.writes)
	}
}
