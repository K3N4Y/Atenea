package hashline

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestHashVectors(t *testing.T) {
	for in, want := range map[string]string{"": "5D05", "hello": "77F9", "a  \n": "9585"} {
		if got := ComputeFileHash(in); got != want {
			t.Fatalf("%q: %s != %s", in, got, want)
		}
	}
}
func TestGrammarAndClipboard(t *testing.T) {
	p, e := ParsePatch("[a.txt#ABCD]\nCUT 1.=1 @x\n[b.txt#1234]\nPUT >1 @x")
	if e != nil || len(p.Sections) != 2 {
		t.Fatalf("parse: %#v %v", p, e)
	}
	c := NewClipboard()
	if _, e = ApplyEditsWithClipboard([]string{"A"}, p.Sections[0].Edits, c); e != nil {
		t.Fatal(e)
	}
	r, e := ApplyEditsWithClipboard([]string{"B"}, p.Sections[1].Edits, c)
	if e != nil || r.Text != "B\nA" {
		t.Fatalf("%q %v", r.Text, e)
	}
}
func TestRejectsLegacy(t *testing.T) {
	for _, s := range []string{"S" + "WAP 1.=1:\n+x", "D" + "EL 1", "I" + "NS.PRE 1:\n+x"} {
		if _, e := ParsePatch("[a#ABCD]\n" + s); e == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}

func TestBlockLocatorsAndTrailingContent(t *testing.T) {
	p, err := ParsePatch("[a#ABCD]\nPUT 2*:\n+x\nCUT 4* @r\nPUT >6* @r")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Sections[0].Edits[0].Block || !p.Sections[0].Edits[1].Block || !p.Sections[0].Edits[2].AfterBlock {
		t.Fatalf("block flags not retained: %#v", p.Sections[0].Edits)
	}
	if _, err := ParsePatch("*** Begin Patch\n[a#ABCD]\nREM\n*** End Patch\njunk"); err == nil {
		t.Fatal("accepted trailing content")
	}
}

func TestSnapshotCollisionRetentionAndRelocate(t *testing.T) {
	seen := map[string]string{}
	var first, second string
	for i := 0; i < 200000; i++ {
		text := fmt.Sprintf("collision-%d", i)
		h := ComputeFileHash(text)
		if old, ok := seen[h]; ok && old != text {
			first, second = old, text
			break
		}
		seen[h] = text
	}
	if first == "" {
		t.Fatal("failed to find collision")
	}
	s := NewMemSnapshotStore()
	h, ok := s.Record("a", first)
	if !ok {
		t.Fatal("first not recorded")
	}
	if got, ok := s.Record("a", second); !ok || got != h {
		t.Fatalf("collision rejected: %q %v", got, ok)
	}
	if got := s.ByHash("a", h); got == nil || got.Text != second {
		t.Fatalf("newest not selected: %#v", got)
	}
	s.Relocate("a", "b")
	if s.Head("a") != nil || s.Head("b") == nil || s.Head("b").Path != "b" {
		t.Fatal("not relocated")
	}
}

type transactionFS struct {
	files      map[string][]byte
	writes     []string
	failWrites map[string]error
}

func (f *transactionFS) ReadFile(path string) ([]byte, error) {
	b, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}
func (f *transactionFS) WriteFile(path string, data []byte, _ os.FileMode) error {
	if err := f.failWrites[path]; err != nil {
		return err
	}
	f.writes = append(f.writes, path)
	f.files[path] = append([]byte(nil), data...)
	return nil
}

func TestApplyPreflightsAllSectionsBeforeWriting(t *testing.T) {
	fs := &transactionFS{files: map[string][]byte{"a": []byte("A"), "b": []byte("B")}}
	snaps := NewMemSnapshotStore()
	ha, _ := snaps.Record("a", "A")
	hb, _ := snaps.Record("b", "B")
	p := NewPatcher(fs, snaps)
	_, err := p.Apply(Patch{Sections: []Section{
		{Path: "a", Hash: ha, Edits: []Edit{{Kind: Replace, Range: Range{1, 1}, Text: "changed"}}},
		{Path: "b", Hash: hb, Edits: []Edit{{Kind: Replace, Range: Range{2, 2}, Text: "invalid"}}},
	}})
	if err == nil || len(fs.writes) != 0 || string(fs.files["a"]) != "A" {
		t.Fatalf("err=%v writes=%v a=%q", err, fs.writes, fs.files["a"])
	}
}

func TestFailedPreflightDoesNotPublishRegisters(t *testing.T) {
	fs := &transactionFS{files: map[string][]byte{"a": []byte("A"), "b": []byte("B")}}
	snaps := NewMemSnapshotStore()
	ha, _ := snaps.Record("a", "A")
	hb, _ := snaps.Record("b", "B")
	p := NewPatcher(fs, snaps)
	_, err := p.Apply(Patch{Sections: []Section{
		{Path: "a", Hash: ha, Edits: []Edit{{Kind: Cut, Range: Range{1, 1}, Register: "leak"}}},
		{Path: "b", Hash: hb, Edits: []Edit{{Kind: Replace, Range: Range{2, 2}, Text: "invalid"}}},
	}})
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	if _, ok := p.Clipboard.Named["leak"]; ok {
		t.Fatal("failed transaction published register")
	}
}

func TestWriteDriftSnapshotsReadback(t *testing.T) {
	fs := &transactionFS{files: map[string][]byte{"a": []byte("A")}}
	snaps := NewMemSnapshotStore()
	h, _ := snaps.Record("a", "A")
	base := fs
	drift := &driftingFS{transactionFS: base}
	p := NewPatcher(drift, snaps)
	res, err := p.Apply(Patch{Sections: []Section{{Path: "a", Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{1, 1}, Text: "B"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if head := snaps.Head("a"); head == nil || head.Text != "B\nformatted" {
		t.Fatalf("snapshot=%#v", head)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("write drift was not warned")
	}
}

type driftingFS struct{ *transactionFS }

func (f *driftingFS) WriteFile(path string, data []byte, mode os.FileMode) error {
	if err := f.transactionFS.WriteFile(path, data, mode); err != nil {
		return err
	}
	f.files[path] = append(f.files[path], []byte("\nformatted")...)
	return nil
}

func TestCommitFailureDoesNotPublishFailedSectionRegister(t *testing.T) {
	boom := errors.New("boom")
	fs := &transactionFS{files: map[string][]byte{"a": []byte("A"), "b": []byte("B")}, failWrites: map[string]error{"b": boom}}
	snaps := NewMemSnapshotStore()
	ha, _ := snaps.Record("a", "A")
	hb, _ := snaps.Record("b", "B")
	p := NewPatcher(fs, snaps)
	_, err := p.Apply(Patch{Sections: []Section{
		{Path: "a", Hash: ha, Edits: []Edit{{Kind: Replace, Range: Range{1, 1}, Text: "AA"}}},
		{Path: "b", Hash: hb, Edits: []Edit{{Kind: Cut, Range: Range{1, 1}, Register: "failed"}}},
	}})
	if err == nil || string(fs.files["a"]) != "AA" {
		t.Fatalf("err=%v a=%q", err, fs.files["a"])
	}
	if _, ok := p.Clipboard.Named["failed"]; ok {
		t.Fatal("failed section published register")
	}
}
