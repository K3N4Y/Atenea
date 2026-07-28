package agent

import (
	"strings"
	"testing"
)

func TestParse_ManifestVersion(t *testing.T) {
	def, err := Parse([]byte("---\nversion: 1\nname: reviewer\n---\nprompt\n"))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if def.Version != 1 {
		t.Fatalf("Version = %d, want 1", def.Version)
	}
}

func TestParse_MissingManifestVersionMeansVersionOne(t *testing.T) {
	def, err := Parse([]byte("---\nname: reviewer\n---\nprompt\n"))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if def.Version != 1 {
		t.Fatalf("Version = %d, want legacy default 1", def.Version)
	}
}

func TestParse_RejectsUnsupportedManifestVersion(t *testing.T) {
	_, err := Parse([]byte("---\nversion: 2\nname: reviewer\n---\nprompt\n"))
	if err == nil || !strings.Contains(err.Error(), "unsupported manifest version 2") {
		t.Fatalf("Parse error = %v, want unsupported version", err)
	}
}

func TestParse_ToleratesUnknownManifestKeys(t *testing.T) {
	def, err := Parse([]byte("---\nversion: 1\nname: reviewer\nfuture_extension:\n  enabled: true\n---\nprompt\n"))
	if err != nil {
		t.Fatalf("Parse with unknown key: unexpected error: %v", err)
	}
	if def.Name != "reviewer" {
		t.Fatalf("Name = %q, want reviewer", def.Name)
	}
}
