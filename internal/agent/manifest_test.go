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

func TestParse_StepsIsOptionalOrPositiveInteger(t *testing.T) {
	def, err := Parse([]byte("---\nname: reviewer\nsteps: 30\n---\nprompt\n"))
	if err != nil || def.Steps != 30 {
		t.Fatalf("Parse steps = %d, %v; want 30, nil", def.Steps, err)
	}
	for _, value := range []string{"0", "-1", "2.5", `"2"`, "true", "null", "[2]", "{value: 2}", "999999999999999999999999999999999999"} {
		if _, err := Parse([]byte("---\nname: reviewer\nsteps: " + value + "\n---\nprompt\n")); err == nil {
			t.Errorf("Parse steps %s: want error", value)
		}
	}
}
