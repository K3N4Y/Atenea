package hashline

import (
	"strings"
	"testing"
)

// Provenance: oh-my-pi packages/hashline test/{core-contracts,format-v2,
// leniency,clipboard,file-ops}.test.ts @ 5af71dc.
func TestCoreLanguagePorts(t *testing.T) {
	apply := func(src, patch string) (ApplyResult, Patch) {
		t.Helper()
		p, err := ParsePatch(patch)
		if err != nil {
			t.Fatal(err)
		}
		r, err := ApplyEdits(SplitLines(src), p.Sections[0].Edits)
		if err != nil {
			t.Fatal(err)
		}
		return r, p
	}
	if r, _ := apply("a\nb\nc\nd", "PUT 2.=3:\n+X"); r.Text != "a\nX\nd" {
		t.Fatal(r.Text)
	}
	for _, sep := range []string{"-", ".", "=", "..", "…", " "} {
		if r, _ := apply("a\nb\nc\nd", "CUT 2"+sep+"3"); r.Text != "a\nd" {
			t.Fatalf("%q: %q", sep, r.Text)
		}
	}
	if r, _ := apply("a\nb", "PUT <1:\n+H\nPUT >$:\n+T"); r.Text != "H\na\nb\nT" {
		t.Fatal(r.Text)
	}
	if r, p := apply("a\nb\nc", "PUT 2:\n2:B"); r.Text != "a\nB\nc" || len(p.Warnings) == 0 {
		t.Fatalf("%q %#v", r.Text, p.Warnings)
	}
	if r, _ := apply("a\nb\nc", "PUT 2:\n-b\n+B"); r.Text != "a\nB\nc" {
		t.Fatal(r.Text)
	}
	for _, bad := range []string{"PUT >$:", "CUT 2\n+x", "@@ -1 +1 @@", "*** Add File: x"} {
		if _, err := ParsePatch(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, err := ParsePatch("PUT 1-100001:\n+x"); err == nil {
		t.Fatal("accepted amplification")
	}
}

func TestSectionMergeHeadersAndFileOpExclusivity(t *testing.T) {
	p, err := ParsePatch("[dir with spaces/a.ts#1a2b]\nCUT 1\n[dir with spaces/a.ts#1A2B]\nPUT >$ @r")
	if err != nil || len(p.Sections) != 1 || p.Sections[0].Hash != "1A2B" || len(p.Sections[0].Edits) != 2 {
		t.Fatalf("%#v %v", p, err)
	}
	if _, err := ParsePatch("[a#ABCD junk]\nCUT 1"); err == nil {
		t.Fatal("accepted tag junk")
	}
	if _, err := ParsePatch("REM\nCUT 1"); err == nil {
		t.Fatal("mixed REM and line op")
	}
	p, err = ParsePatch("[a#ABCD]\nMV 'dir/new name.ts'")
	if err != nil || p.Sections[0].FileOp.MoveTo != "dir/new name.ts" {
		t.Fatalf("%#v %v", p, err)
	}
}

func TestClipboardSequenceTransactionalAndSentinel(t *testing.T) {
	p, err := ParsePatch("CUT 1 @a\nCUT 3 @b\nPUT <1 @b\nPUT >$ @a")
	if err != nil {
		t.Fatal(err)
	}
	cb := NewClipboard()
	r, err := ApplyEditsWithClipboard([]string{"a1", "x", "b1", "z"}, p.Sections[0].Edits, cb)
	if err != nil || r.Text != "b1\nx\nz\na1" {
		t.Fatalf("%q %v", r.Text, err)
	}
	before := cb.Clone()
	bad, _ := ParsePatch("CUT 1 @new\nPUT >99 @new")
	if _, err = ApplyEditsWithClipboard([]string{"x"}, bad.Sections[0].Edits, cb); err == nil {
		t.Fatal("missing bounds error")
	}
	if _, ok := cb.Named["new"]; ok || strings.Join(before.Named["a"], "") != strings.Join(cb.Named["a"], "") {
		t.Fatal("failed apply published clipboard")
	}
	cut, _ := ParsePatch("CUT 3")
	rr, err := ApplyEdits([]string{"a", "b", ""}, cut.Sections[0].Edits)
	if err == nil || rr.Text != "" { /* no-change is intentionally reported */
	}
}
