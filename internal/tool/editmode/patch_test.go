package editmode

import (
	"strings"
	"testing"
)

// Provenance: oh-my-pi@5af71dc9 packages/coding-agent/test/core/apply-patch.test.ts
// and core/apply-patch-adverserial.test.ts.
func TestApplyPatchPreservesCRLF_BOMAndMissingTrailingNewline(t *testing.T) {
	got, _, err := ApplyContextPatch("alpha\nbeta", "x", "@@\n-alpha\n+ALPHA", false, .95)
	if err != nil || got != "ALPHA\nbeta" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	got, _, err = ApplyContextPatch("alpha\nbeta\n", "x", "@@ beta\n-beta\n+BETA", false, .95)
	if err != nil || got != "alpha\nBETA\n" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

// Provenance: oh-my-pi@5af71dc9 core/apply-patch-regression.test.ts
// "single-hunk simple diff rejects multiple occurrences" and
// "unified diff line numbers help locate correct position".
func TestSingleHunkSimpleDiffRejectsMultipleOccurrences(t *testing.T) {
	_, _, err := ApplyContextPatch("x\nx\n", "x.txt", "@@\n-x\n+y", false, .95)
	if err == nil || !strings.Contains(err.Error(), "Found 2 matches") || !strings.Contains(err.Error(), "1 | x") || !strings.Contains(err.Error(), "Matching strategy: exact") {
		t.Fatalf("err=%v", err)
	}
	got, _, err := ApplyContextPatch("x\nx\n", "x.txt", "@@ -2,1 +2,1 @@\n-x\n+y", false, .95)
	if err != nil || got != "x\ny\n" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

// Provenance: core/apply-patch-regression.test.ts create prefix normalization.
func TestCreateFileStripsPlusPrefixWhenAllLinesHaveIt(t *testing.T) {
	if got := NormalizePatchCreateContent("+ one\n+two"); got != "one\ntwo\n" {
		t.Fatalf("got=%q", got)
	}
	if got := NormalizePatchCreateContent("+one\ntwo"); got != "+one\ntwo\n" {
		t.Fatalf("got=%q", got)
	}
}

// Provenance: edit/streaming-matcher-paths.test.ts.
func TestPatchMatcherEntries(t *testing.T) {
	got := PatchMatcherEntries("src/foo.ts", []PatchEntry{{Diff: "@@\n-a\n+b"}, {Op: "delete"}})
	if len(got) != 2 || got[0].Path != "src/foo.ts" || got[0].Digest == "" {
		t.Fatalf("got=%+v", got)
	}
}

// Provenance: apply-patch-regression.test.ts "nested @@ anchors", "space-separated anchors",
// "@@ line N syntax", and "partial line matching for @@ context".
func TestPatchHierarchicalAnchorsLineHintsAndPartialSafety(t *testing.T) {
	content := "class Alpha {\n\tprocess() {\n\t\treturn \"alpha\";\n\t}\n}\nclass Beta {\n\tprocess() {\n\t\treturn \"beta\";\n\t}\n}\n"
	diff := "@@ class Beta\n@@ process\n \tprocess() {\n-\t\treturn \"beta\";\n+\t\treturn \"BETA\";\n \t}"
	got, _, err := ApplyContextPatch(content, "x.ts", diff, true, .95)
	if err != nil || !strings.Contains(got, `return "BETA"`) || !strings.Contains(got, `return "alpha"`) {
		t.Fatalf("got=%q err=%v", got, err)
	}

	got, _, err = ApplyContextPatch("same\nsame\n", "x", "@@ line 2\n-same\n+changed", true, .95)
	if err != nil || got != "same\nchanged\n" {
		t.Fatalf("got=%q err=%v", got, err)
	}

	_, _, err = ApplyContextPatch("prefix target suffix\n", "x", "@@\n-target\n+changed", true, .95)
	if err == nil || !strings.Contains(err.Error(), "Refusing partial-line match") {
		t.Fatalf("err=%v", err)
	}
}

// Provenance: apply-patch-regression.test.ts fallback variants and
// apply-patch-adverserial.test.ts overlap diagnostics.
func TestPatchFallbackNoOpAndOverlap(t *testing.T) {
	got, _, err := ApplyContextPatch("header\nold\nfooter\n", "x", "@@ header\n stale\n-old\n+new\n stale2", true, .95)
	if err != nil || got != "header\nnew\nfooter\n" {
		t.Fatalf("got=%q err=%v", got, err)
	}

	_, _, err = ApplyContextPatch("a\nb\nc\n", "x", "@@\n a\n-b\n+B\n@@\n-b\n+B2\n c", true, .95)
	if err == nil || !strings.Contains(err.Error(), "Overlapping hunks") {
		t.Fatalf("err=%v", err)
	}

	_, _, err = ApplyContextPatch("a\n", "x", "@@\n a", true, .95)
	if err == nil || !strings.Contains(err.Error(), "no changes") {
		t.Fatalf("err=%v", err)
	}
}

// Provenance: oh-my-pi@5af71dc9 core/apply-patch-regression.test.ts
// "duplicate context lines collapse for matching" and "repeated context blocks collapse when duplicated".
func TestPatchCollapseFallbackVariants(t *testing.T) {
	got, _, err := ApplyContextPatch("alpha\nbeta\ngamma\n", "x", "@@\n alpha\n beta\n beta\n-gamma\n+GAMMA", true, .95)
	if err != nil || got != "alpha\nbeta\nGAMMA\n" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	got, _, err = ApplyContextPatch("if (ready) {\n  handle();\n}\n", "x", "@@\n if (ready) {\n  handle();\n}\n if (ready) {\n  handle();\n}\n-  handle();\n+  handleNext();", true, .95)
	if err != nil || got != "if (ready) {\n  handleNext();\n}\n" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

// Provenance: oh-my-pi@5af71dc9 core/apply-patch-adverserial.test.ts
// "normalizes tab-indented diff to space-indented file" and regression
// "space-to-tab conversion with offset (ax+b model)".
func TestPatchIndentationConversionMatrix(t *testing.T) {
	content := "class Foo {\n    method() {\n        const x = 1;\n        return x;\n    }\n}\n"
	diff := "@@ method() {\n \tmethod() {\n \t\tconst x = 1;\n+\t\tconsole.log(x);\n \t\treturn x;"
	got, _, err := ApplyContextPatch(content, "x", diff, true, .95)
	if err != nil || strings.Contains(got, "\t") || !strings.Contains(got, "        console.log(x);") {
		t.Fatalf("got=%q err=%v", got, err)
	}
	pattern := []string{"    bar() {", "       if (true) {", "          this.x = 1;"}
	actual := []string{"\tbar() {", "\t\tif (true) {", "\t\t\tthis.x = 1;"}
	repl := []string{"    bar() {", "       if (true) {", "          this.x = 2;"}
	converted := adjustPatchIndentation(pattern, actual, repl)
	if converted[2] != "\t\t\tthis.x = 2;" {
		t.Fatalf("converted=%q", converted)
	}
}

// Provenance: oh-my-pi@5af71dc9 core/apply-patch-regression.test.ts
// "comment-prefix mismatches", "strip line-number prefixes", and closest diagnostics.
func TestPatchRepairAndClosestDiagnostics(t *testing.T) {
	got, _, err := ApplyContextPatch("/*\n * LICENSE file\n */\n", "x", "@@\n-/ LICENSE file\n+ / LICENSE changed", true, .95)
	if err != nil || !strings.Contains(got, "/ LICENSE changed") {
		t.Fatalf("got=%q err=%v", got, err)
	}
	_, _, err = ApplyContextPatch("alpha\nbeta\ngamma\n", "x", "@@\n-alpha\n-delta\n+changed", false, .95)
	if err == nil || !strings.Contains(err.Error(), "Closest match") || !strings.Contains(err.Error(), "near line 1") || !strings.Contains(err.Error(), "1 | alpha") {
		t.Fatalf("err=%v", err)
	}
}
