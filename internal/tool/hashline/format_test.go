package hashline

import "testing"

// Provenance: oh-my-pi format-v2.test.ts canonical format contracts @ 5af71dc.
func TestCanonicalFormattingContracts(t *testing.T) {
	if got := FormatHeader("dir/a b.ts", "1A2B"); got != "[dir/a b.ts#1A2B]" {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		in   string
		want []string
	}{{"", nil}, {"a", []string{"a"}}, {"a\nb\n", []string{"a", "b"}}, {"a\nb", []string{"a", "b"}}, {"a\n\n", []string{"a", ""}}} {
		got := SplitLines(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("SplitLines(%q)=%#v", tc.in, got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("SplitLines(%q)=%#v", tc.in, got)
			}
		}
	}
	if got := NumberLines([]string{"a", "b", "c"}, 2, 3); got != "2:b\n3:c" {
		t.Fatal(got)
	}
}

func TestApplyFormattingOrderAndNewlineContracts(t *testing.T) {
	edits := []Edit{{Kind: Insert, Cursor: BeforeAnchor, Anchor: 2, Text: "before"}, {Kind: Replace, Range: Range{2, 2}, Text: "B1\nB2"}, {Kind: Insert, Cursor: AfterAnchor, Anchor: 2, Text: "after"}}
	r, e := ApplyEdits([]string{"a", "b", "c"}, edits)
	if e != nil || r.Text != "a\nbefore\nB1\nB2\nafter\nc" || r.FirstChangedLine != 2 {
		t.Fatalf("%q %v %#v", r.Text, e, r)
	}
	p, _ := ParsePatch("CUT 2-3")
	r, e = ApplyEdits([]string{"a", "b", ""}, p.Sections[0].Edits)
	if e != nil || r.Text != "a" {
		t.Fatalf("%q %v", r.Text, e)
	}
	p, _ = ParsePatch("PUT 2-3:\n+B")
	r, e = ApplyEdits([]string{"a", "b", ""}, p.Sections[0].Edits)
	if e != nil || r.Text != "a\nB" {
		t.Fatalf("%q %v", r.Text, e)
	}
}

// SplitLines already drops the newline sentinel, so a trailing empty row is a
// real blank line and edits landing on it apply literally.
func TestApplyEditsTreatTrailingBlankLineAsRealLine(t *testing.T) {
	lines := SplitLines("a\nb\n\n")
	for _, tc := range []struct{ name, patch, want string }{
		{"replace blank line", "PUT 3:\n+c", "a\nb\nc"},
		{"cut blank line", "CUT 3", "a\nb"},
		{"span ending on blank line", "PUT 2-3:\n+B", "a\nB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParsePatch(tc.patch)
			if err != nil {
				t.Fatal(err)
			}
			r, err := ApplyEdits(lines, p.Sections[0].Edits)
			if err != nil || r.Text != tc.want {
				t.Fatalf("text = %q, err = %v", r.Text, err)
			}
		})
	}
}

func TestPatcherKeepsFinalNewlineWhenEditingATrailingBlankLine(t *testing.T) {
	const path, text = "/w/f", "a\nb\n\n"
	fs := &transactionFS{files: map[string][]byte{path: []byte(text)}}
	store := NewMemSnapshotStore()
	hash, _ := store.Record(path, text)
	p, err := ParsePatch("PUT 3:\n+c")
	if err != nil {
		t.Fatal(err)
	}
	section := p.Sections[0]
	section.Path, section.Hash = path, hash
	if _, err := NewPatcher(fs, store).Apply(Patch{Sections: []Section{section}}); err != nil {
		t.Fatal(err)
	}
	if got := string(fs.files[path]); got != "a\nb\nc\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestApplyEditsRejectARangelessReplace(t *testing.T) {
	_, err := ApplyEdits([]string{"a"}, []Edit{{Kind: Replace, Text: "x"}})
	if err == nil {
		t.Fatal("a Replace without a range must be rejected, not applied")
	}
}
