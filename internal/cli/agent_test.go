package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodAgent = `---
name: reviewer
description: Reviews a change carefully.
tools: [read, grep]
---

Review the requested change and report concrete findings.
`

func writeAgentDefinition(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentValidate_ReportsMalformedDiscoveredDefinitions(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	broken := writeAgentDefinition(t, filepath.Join(root, ".atenea", "agents", "broken.md"), "no frontmatter\n")

	got := invoke(t, "agent", "validate", "--cwd", root)
	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitFailure, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("validate wrote to stdout: %q", got.stdout)
	}
	if !strings.Contains(got.stderr, broken) || !strings.Contains(got.stderr, "frontmatter") {
		t.Errorf("finding does not name the malformed file and reason:\n%s", got.stderr)
	}
}

func TestAgentValidate_ReportsDefinitionsThatCannotDescribeOrRunAChild(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	quiet := writeAgentDefinition(t, filepath.Join(root, "quiet.md"), "---\nname: quiet\n---\n")

	got := invoke(t, "agent", "validate", quiet)
	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitFailure, got.stderr)
	}
	for _, want := range []string{quiet, "description", "prompt"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr does not contain %q:\n%s", want, got.stderr)
		}
	}
}

func TestAgentValidate_ValidatesNamedFilesAndDirectories(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	draft := writeAgentDefinition(t, filepath.Join(root, "draft.agent"), goodAgent)
	writeAgentDefinition(t, filepath.Join(root, "nested", "reviewer.md"), goodAgent)

	for _, path := range []string{draft, root} {
		got := invoke(t, "agent", "validate", path)
		if got.code != ExitOK {
			t.Errorf("validate %q exit code = %d, want %d\nstderr: %s", path, got.code, ExitOK, got.stderr)
		}
		if !strings.Contains(got.stderr, "ok") {
			t.Errorf("validate %q did not summarize success:\n%s", path, got.stderr)
		}
	}
}

func TestAgentValidate_ZeroFilesIsNotAPass(t *testing.T) {
	isolateConfig(t)
	got := invoke(t, "agent", "validate", t.TempDir())
	if got.code != ExitFailure || !strings.Contains(got.stderr, "no agent definitions") {
		t.Fatalf("exit code = %d, stderr = %q", got.code, got.stderr)
	}
}

func TestAgentCommand_AppearsInGeneratedHelp(t *testing.T) {
	got := invoke(t, "help")
	if got.code != ExitOK || !strings.Contains(got.stdout, "agent") {
		t.Fatalf("help exit code = %d, stdout = %q", got.code, got.stdout)
	}
	got = invoke(t, "agent", "help")
	if got.code != ExitOK || !strings.Contains(got.stdout, "validate") {
		t.Fatalf("agent help exit code = %d, stdout = %q", got.code, got.stdout)
	}
}
