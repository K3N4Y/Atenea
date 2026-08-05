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
	if e != nil || r.Text != "a\n" {
		t.Fatalf("%q %v", r.Text, e)
	}
	p, _ = ParsePatch("PUT 2-3:\n+B")
	r, e = ApplyEdits([]string{"a", "b", ""}, p.Sections[0].Edits)
	if e != nil || r.Text != "a\nB\n" {
		t.Fatalf("%q %v", r.Text, e)
	}
}
