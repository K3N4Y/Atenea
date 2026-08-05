package editmode

import (
	"strings"
	"testing"
)

// Provenance: oh-my-pi@5af71dc9 packages/coding-agent/test/core/apply-patch.test.ts
// parser and streaming cases.
func TestParseApplyPatchEnvelope(t *testing.T) {
	input := "<<'EOF'\n*** Begin Patch\n*** Add File: 新.txt\n+hello\n*** Delete File: old.txt\n*** Update File: src/a.txt\n*** Move to: dst/a.txt\n@@ function f\n-old\n+new\n*** End Patch\nEOF\n"
	got, err := ParseApplyPatch(input)
	if err != nil || len(got) != 3 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if got[0] != (PatchEntry{Op: "create", Path: "新.txt", Diff: "hello\n"}) || got[1].Op != "delete" || got[2].Rename != "dst/a.txt" || !strings.Contains(got[2].Diff, "+new") {
		t.Fatalf("got=%+v", got)
	}
}

// Provenance: apply-patch core malformed/empty/adversarial parser tests.
func TestParseApplyPatchErrors(t *testing.T) {
	cases := []struct{ input, want string }{
		{"bad", "first line"},
		{"*** Begin Patch\n*** Add File: x\n+x", "last line"},
		{"*** Begin Patch\n*** Update File: x\n*** End Patch", "is empty"},
		{"*** Begin Patch\nnot a header\n*** End Patch", "not a valid hunk header"},
	}
	for _, tc := range cases {
		_, err := ParseApplyPatch(tc.input)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("input=%q err=%v", tc.input, err)
		}
	}
	got, err := ParseApplyPatch("  *** Begin Patch  \n*** Add File: x\n+ok\n  *** End Patch  ")
	if err != nil || len(got) != 1 {
		t.Fatalf("padded markers: %+v %v", got, err)
	}
}

// Provenance: edit/streaming-matcher-paths.test.ts and apply-patch renderer
// streaming tests: unfinished lines are trimmed and projections remain isolated.
func TestApplyPatchStreamingProjection(t *testing.T) {
	partial := "*** Begin Patch\n*** Add File: a\n+one\n*** Update File: b\n@@\n-x\n+y"
	got := ApplyPatchMatcherEntries(partial)
	if len(got) != 2 || got[0].Path != "a" || got[0].Digest != "one\n" || got[1].Path != "b" || strings.Contains(got[1].Digest, "y") {
		t.Fatalf("got=%+v", got)
	}
	complete := ApplyPatchMatcherEntries(partial + "\n")
	if len(complete) != 2 || complete[1].Digest != "y" {
		t.Fatalf("complete=%+v", complete)
	}
}
