package hashline

import (
	"strings"
	"testing"
)

func TestParserRangeVariantsSingleAndWarnings(t *testing.T) {
	for _, input := range []string{"CUT 2", "CUT 2-3", "CUT 2.3", "CUT 2=3", "CUT 2..3", "CUT 2…3", "CUT 2 3"} {
		p, err := ParsePatch("[x#ABCD]\n" + input)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if len(p.Sections[0].Edits) != 1 {
			t.Fatalf("%q: %#v", input, p)
		}
	}
	p, err := ParsePatch("[x#ABCD]\nPUT 2:\nraw")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) != 1 || p.Warnings[0].Code != "bare-body" {
		t.Fatalf("warnings=%#v", p.Warnings)
	}
}

func TestParserRejectsContaminationAndExpansion(t *testing.T) {
	for _, body := range []string{"*** Update File: x", "@@ -1,2 +1,2 @@", "+orphan"} {
		if _, err := ParsePatch("[x#ABCD]\n" + body); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
	if _, err := ParsePatch("[x#ABCD]\nPUT 1-100001:\n+x"); err == nil {
		t.Fatal("accepted oversized expansion")
	}
	unsafe := strings.Repeat("9", 40)
	if _, err := ParsePatch("[x#ABCD]\nCUT " + unsafe); err == nil {
		t.Fatal("accepted unsafe integer")
	}
}

func TestParsePartialOmitsUnfinishedPut(t *testing.T) {
	p, err := ParsePatchPartial("[x#ABCD]\nCUT 2\nPUT >$:")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sections) != 1 || len(p.Sections[0].Edits) != 1 || p.Sections[0].Edits[0].Kind != Cut {
		t.Fatalf("%#v", p)
	}
}
