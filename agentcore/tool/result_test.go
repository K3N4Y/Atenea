package tool

import (
	"strings"
	"testing"
)

func TestResultPruneSnapshotTextCombinedCharacterBudget(t *testing.T) {
	within := strings.Repeat("é", SnapshotTextBudget/2)
	over := strings.Repeat("x", SnapshotTextBudget)
	result := Result{Output: over, Diff: over, Files: []FileResult{
		{OldText: within, NewText: within},
		{OldText: over, NewText: "x"},
	}}
	result.PruneSnapshotText()
	if result.Files[0].OldText == "" || result.Files[0].NewText == "" {
		t.Fatal("snapshot pair at exact character budget was pruned")
	}
	if result.Files[1].OldText != "" || result.Files[1].NewText != "" || !result.Files[1].SnapshotsPruned {
		t.Fatalf("over-budget child not pruned: %+v", result.Files[1])
	}
	if result.Output != over || result.Diff != over || result.Metadata["snapshot_text_pruned"] != true {
		t.Fatal("pruning altered visible evidence or omitted aggregate marker")
	}
}

func TestResultPruneSnapshotTextExactRuneBoundaryPerFileAndAggregate(t *testing.T) {
	exact := strings.Repeat("界", SnapshotTextBudget)
	over := exact + "x"
	result := Result{Output: "visible", Diff: "diff", Metadata: map[string]any{"durability": "uncertain"}, Files: []FileResult{
		{OldText: exact},
		{NewText: over, Diagnostics: []Diagnostic{{Severity: "warning", Message: "nonfatal"}}, Committed: true},
	}}
	result.PruneSnapshotText()
	if result.Files[0].OldText != exact || result.Files[0].SnapshotsPruned {
		t.Fatal("exact 32768-rune snapshot was pruned")
	}
	if result.Files[1].NewText != "" || !result.Files[1].SnapshotsPruned || len(result.Files[1].Diagnostics) != 1 || !result.Files[1].Committed {
		t.Fatalf("over-budget structured child=%+v", result.Files[1])
	}
	if result.Output != "visible" || result.Diff != "diff" || result.Metadata["durability"] != "uncertain" || result.Metadata["snapshot_text_pruned"] != true {
		t.Fatalf("aggregate evidence changed: %+v", result)
	}
}
