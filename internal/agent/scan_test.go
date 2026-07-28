package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScan_ReportsMalformedDefinitionsThatDiscoverySkips(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, "broken.md")
	if err := os.WriteFile(broken, []byte("---\ndescription: no name\n---\nprompt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Scan() returned %d entries, want 1", len(entries))
	}
	if entries[0].Location != broken || entries[0].Err == nil {
		t.Fatalf("Scan() entry = %+v, want the malformed file and its error", entries[0])
	}
	if !strings.Contains(entries[0].Err.Error(), "no 'name'") {
		t.Errorf("Scan() error = %q, want the parse failure", entries[0].Err)
	}
}

func TestScan_ReportsTheDefinitionThatAnEarlierDirectoryShadows(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	winner := writeAgent(t, first, "reviewer", "reviewer", "first")
	loser := writeAgent(t, second, "reviewer", "reviewer", "second")

	entries, err := Scan(first, second)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Scan() returned %d entries, want 2", len(entries))
	}
	if entries[0].Location != winner || entries[0].ShadowedBy != "" {
		t.Errorf("winner = %+v", entries[0])
	}
	if entries[1].Location != loser || entries[1].ShadowedBy != winner {
		t.Errorf("shadowed entry = %+v, want ShadowedBy %q", entries[1], winner)
	}
}
