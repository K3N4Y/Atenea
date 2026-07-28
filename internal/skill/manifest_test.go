package skill

import (
	"strings"
	"testing"
)

func TestParse_ManifestVersion(t *testing.T) {
	info, err := Parse([]byte("---\nversion: 1\nname: demo\n---\nbody\n"))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if info.Version != 1 {
		t.Fatalf("Version = %d, want 1", info.Version)
	}
}

func TestParse_MissingManifestVersionMeansVersionOne(t *testing.T) {
	info, err := Parse([]byte("---\nname: demo\n---\nbody\n"))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if info.Version != 1 {
		t.Fatalf("Version = %d, want legacy default 1", info.Version)
	}
}

func TestParse_RejectsUnsupportedManifestVersion(t *testing.T) {
	_, err := Parse([]byte("---\nversion: 2\nname: demo\n---\nbody\n"))
	if err == nil || !strings.Contains(err.Error(), "unsupported manifest version 2") {
		t.Fatalf("Parse error = %v, want unsupported version", err)
	}
}

func TestParse_ToleratesUnknownManifestKeys(t *testing.T) {
	info, err := Parse([]byte("---\nversion: 1\nname: demo\nfuture_extension:\n  enabled: true\n---\nbody\n"))
	if err != nil {
		t.Fatalf("Parse with unknown key: unexpected error: %v", err)
	}
	if info.Name != "demo" {
		t.Fatalf("Name = %q, want demo", info.Name)
	}
}
