package hashline

import (
	"errors"
	"strings"
	"testing"
)

// Provenance: behavior ported from oh-my-pi packages/coding-agent/test/edit/
// seen-line-guard.test.ts @ 5af71dc9cf132538e072806424f71f43f734d9ae.
func TestSeenGuardDefaultOffAndEnabledBounds(t *testing.T) {
	const path = "/work/seen"
	makeCase := func() (*Patcher, *transactionFS, string) {
		fs := &transactionFS{files: map[string][]byte{path: []byte("a\nb\nc")}}
		s := NewMemSnapshotStore()
		h, _ := s.Record(path, "a\nb\nc")
		s.RecordSeenLines(path, h, []int{1})
		return NewPatcher(fs, s), fs, h
	}
	p, fs, h := makeCase()
	if _, err := p.Apply(Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{3, 3}, Text: "C"}}}}}); err != nil {
		t.Fatalf("default guard must be off: %v", err)
	}
	if string(fs.files[path]) != "a\nb\nC" {
		t.Fatal("default-off edit did not land")
	}
	p, fs, h = makeCase()
	p.EnforceSeenLines = true
	_, err := p.Apply(Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{2, 3}, Text: "BC"}}}}})
	if err == nil || string(fs.files[path]) != "a\nb\nc" || !strings.Contains(err.Error(), "line 2 was not seen") {
		t.Fatalf("err=%v disk=%q", err, fs.files[path])
	}
	// Complete inline reveal grants provenance for an identical retry.
	if _, err = p.Apply(Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{2, 3}, Text: "BC"}}}}}); err != nil {
		t.Fatalf("same-tag retry after complete reveal: %v", err)
	}
}

func TestSeenGuardTruncatedRevealNeverGrants(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		end        int
	}{
		{"over 40 lines", strings.Repeat("x\n", 41), 41},
		{"over 512 runes", strings.Repeat("x", 513), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const path = "/work/cap"
			s := NewMemSnapshotStore()
			h, _ := s.Record(path, tc.text)
			fs := &transactionFS{files: map[string][]byte{path: []byte(tc.text)}}
			p := NewPatcher(fs, s)
			p.EnforceSeenLines = true
			patch := Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{1, tc.end}, Text: "z"}}}}}
			for i := 0; i < 2; i++ {
				_, err := p.Apply(patch)
				if err == nil || !strings.Contains(err.Error(), "preview was truncated") {
					t.Fatalf("attempt %d err=%v", i, err)
				}
			}
			if string(fs.files[path]) != tc.text {
				t.Fatal("rejected edit changed disk")
			}
		})
	}
}

func TestSeenGuardAnchorsRangeInsertCutPasteAndStablePositions(t *testing.T) {
	const path = "/work/kinds"
	text := "a\nb\nc"
	s := NewMemSnapshotStore()
	h, _ := s.Record(path, text)
	s.RecordSeenLines(path, h, []int{2})
	for _, e := range []Edit{{Kind: Replace, Range: Range{2, 2}, Text: "B"}, {Kind: Cut, Range: Range{2, 2}}, {Kind: Insert, Anchor: 2, Cursor: AfterAnchor, Text: "x"}, {Kind: Paste, Anchor: 2, Cursor: AfterAnchor, Register: "r"}} {
		if line, ok := firstUnseenAnchoredLine([]Edit{e}, s.ByHash(path, h).Seen); !ok || line != 0 {
			t.Fatalf("seen kind %#v rejected line=%d", e, line)
		}
	}
	for _, e := range []Edit{{Kind: Replace, Range: Range{1, 2}, Text: "x"}, {Kind: Insert, Anchor: 3, Cursor: BeforeAnchor, Text: "x"}} {
		if _, ok := firstUnseenAnchoredLine([]Edit{e}, s.ByHash(path, h).Seen); ok {
			t.Fatalf("unseen kind %#v accepted", e)
		}
	}
	for _, cursor := range []Cursor{BOF, EOF} {
		if _, ok := firstUnseenAnchoredLine([]Edit{{Kind: Insert, Cursor: cursor, Text: "x"}}, nil); !ok {
			t.Fatalf("stable cursor %v needs seen line", cursor)
		}
	}
}

func TestCollidingRecoveryUniqueAndAmbiguousCandidates(t *testing.T) {
	const path = "/work/collision"
	a, b := collisionFixture(t)
	h := ComputeFileHash(a)
	s := NewMemSnapshotStore()
	s.Record(path, a)
	s.Record(path, b)
	fs := &transactionFS{files: map[string][]byte{path: []byte("prefix\n" + a)}}
	_, err := NewPatcher(fs, s).Apply(Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{1, 1}, Text: "A"}}}}})
	if err != nil {
		t.Fatalf("one recoverable collider should disambiguate: %v", err)
	}
	// Stable positions cannot distinguish two stale colliders and must fail closed.
	fs = &transactionFS{files: map[string][]byte{path: []byte("different")}}
	_, err = NewPatcher(fs, s).Apply(Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Insert, Cursor: EOF, Text: "x"}}}}})
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) || string(fs.files[path]) != "different" {
		t.Fatalf("ambiguous err=%v disk=%q", err, fs.files[path])
	}
}
