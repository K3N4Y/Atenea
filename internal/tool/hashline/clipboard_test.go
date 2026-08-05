package hashline

import (
	"reflect"
	"testing"
)

// Provenance: oh-my-pi packages/hashline/test/clipboard.test.ts @ 5af71dc.
func TestClipboardCategoryOrderRegisterContracts(t *testing.T) {
	cb := NewClipboard()
	cb.Named["r"] = []string{"x"}
	clone := cb.Clone()
	clone.Named["r"][0] = "changed"
	if cb.Named["r"][0] != "x" {
		t.Fatal("Clone aliases named register")
	}
	p, _ := ParsePatch("CUT 1 @a\nCUT 3 @b\nPUT <1 @b\nPUT >$ @a")
	r, e := ApplyEditsWithClipboard([]string{"a1", "x", "b1", "z"}, p.Sections[0].Edits, cb)
	if e != nil || r.Text != "b1\nx\nz\na1" {
		t.Fatalf("%q %v", r.Text, e)
	}
	if !reflect.DeepEqual(cb.Named["a"], []string{"a1"}) || !reflect.DeepEqual(cb.Named["b"], []string{"b1"}) {
		t.Fatalf("registers=%#v", cb.Named)
	}
	// Named registers persist and paste is non-consuming.
	p, _ = ParsePatch("PUT >$ @a\nPUT >$ @a")
	r, e = ApplyEditsWithClipboard([]string{"q"}, p.Sections[0].Edits, cb)
	if e != nil || r.Text != "q\na1\na1" {
		t.Fatalf("%q %v", r.Text, e)
	}
}

func TestClipboardFailuresAreTransactional(t *testing.T) {
	cb := NewClipboard()
	cb.Named["old"] = []string{"safe"}
	p, _ := ParsePatch("CUT 1 @new\nPUT >9 @new")
	if _, e := ApplyEditsWithClipboard([]string{"x"}, p.Sections[0].Edits, cb); e == nil {
		t.Fatal("expected bounds error")
	}
	if _, ok := cb.Named["new"]; ok || cb.Named["old"][0] != "safe" {
		t.Fatalf("published failure: %#v", cb)
	}
	p, _ = ParsePatch("CUT 1\nCUT 3\nPUT >$")
	if _, e := ApplyEditsWithClipboard([]string{"a", "b", "c"}, p.Sections[0].Edits, cb); e == nil {
		t.Fatal("expected ambiguous anonymous cuts")
	}
	p, _ = ParsePatch("PUT >1 @missing")
	if _, e := ApplyEditsWithClipboard([]string{"a"}, p.Sections[0].Edits, cb); e == nil {
		t.Fatal("expected empty named register")
	}
}

func TestClipboardNewlineCaptureContract(t *testing.T) {
	cb := NewClipboard()
	p, _ := ParsePatch("CUT 2 @r")
	r, e := ApplyEditsWithClipboard([]string{"a", "b", ""}, p.Sections[0].Edits, cb)
	if e != nil || r.Text != "a\n" || !reflect.DeepEqual(cb.Named["r"], []string{"b"}) {
		t.Fatalf("%q %v %#v", r.Text, e, cb.Named)
	}
	p, _ = ParsePatch("PUT >$ @r")
	r, e = ApplyEditsWithClipboard([]string{"x", ""}, p.Sections[0].Edits, cb)
	if e != nil || r.Text != "x\n\nb" {
		t.Fatalf("%q %v", r.Text, e)
	}
}
