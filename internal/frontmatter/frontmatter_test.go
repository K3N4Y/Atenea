package frontmatter

import (
	"strings"
	"testing"
)

func TestParse_DecodesYAMLAndReturnsBody(t *testing.T) {
	type manifest struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Tools       []string `yaml:"tools"`
	}
	var got manifest
	body, err := Parse([]byte("---\nname: reviewer\ndescription: >\n  Reviews code with\n  careful attention.\ntools:\n  - read\n  - grep\n---\n# Prompt\n"), &got)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if got.Name != "reviewer" || got.Description != "Reviews code with careful attention.\n" {
		t.Fatalf("manifest = %#v, want decoded YAML fields", got)
	}
	if len(got.Tools) != 2 || got.Tools[0] != "read" || got.Tools[1] != "grep" {
		t.Fatalf("Tools = %v, want [read grep]", got.Tools)
	}
	if string(body) != "# Prompt\n" {
		t.Fatalf("body = %q, want prompt without frontmatter", body)
	}
}

func TestParse_NormalizesCRLF(t *testing.T) {
	var got struct {
		Name string `yaml:"name"`
	}
	body, err := Parse([]byte("---\r\nname: demo\r\n---\r\nbody\r\n"), &got)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if got.Name != "demo" || string(body) != "body\n" {
		t.Fatalf("got name %q, body %q", got.Name, body)
	}
}

func TestParse_RejectsMissingFrontmatter(t *testing.T) {
	if _, err := Parse([]byte("# Markdown\n"), &struct{}{}); err == nil || !strings.Contains(err.Error(), "no frontmatter") {
		t.Fatalf("Parse error = %v, want missing-frontmatter error", err)
	}
}

func TestParse_RejectsUnclosedFrontmatter(t *testing.T) {
	if _, err := Parse([]byte("---\nname: demo\n"), &struct{}{}); err == nil || !strings.Contains(err.Error(), "never closed") {
		t.Fatalf("Parse error = %v, want unclosed-frontmatter error", err)
	}
}

func TestParse_RejectsInvalidYAML(t *testing.T) {
	var got struct {
		Tools []string `yaml:"tools"`
	}
	if _, err := Parse([]byte("---\ntools: [read\n---\nbody\n"), &got); err == nil || !strings.Contains(err.Error(), "YAML") {
		t.Fatalf("Parse error = %v, want YAML error", err)
	}
}
