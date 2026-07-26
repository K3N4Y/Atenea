package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill puts a SKILL.md with the given frontmatter and body at
// <root>/<rel>/SKILL.md, which is the shape discovery walks for.
func writeSkill(t *testing.T, root, rel, content string) string {
	t.Helper()
	dir := filepath.Join(root, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const goodSkill = `---
name: good
description: A skill that loads. Use when a test needs one that works.
---

Do the thing.
`

// TestSkillValidate_FailsOnAMalformedSkill is R9.3's promise: a contributor gets
// a real error instead of silent non-discovery. Discovery skips this file without
// a word, so the exit code and the file name are the entire feature.
//
// Mutation-checked three ways, each reintroducing a plausible version of the bug
// this guards:
//   - dropping the `problems++` for a parse error: the command exits 0 and this
//     test fails on the exit code;
//   - having skill.Scan skip unparseable files the way Discover does (never
//     appending an Entry with Err): same failure, which is the point — validate is
//     only honest because Scan reports what Discover drops;
//   - printing the reason without the path: fails on the location assertion.
func TestSkillValidate_FailsOnAMalformedSkill(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	writeSkill(t, root, filepath.Join(".atenea", "skills", "good"), goodSkill)
	broken := writeSkill(t, root, filepath.Join(".atenea", "skills", "broken"), `---
description: I forgot to declare a name.
---

Body.
`)

	got := invoke(t, "skill", "validate", "--cwd", root)
	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitFailure, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("validate wrote to stdout: %q", got.stdout)
	}
	if !strings.Contains(got.stderr, broken) {
		t.Errorf("the finding does not name the file that has the problem:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "name") {
		t.Errorf("the finding does not say what is wrong with it:\n%s", got.stderr)
	}
}

// TestSkillValidate_PassesOnAGoodSkill: the other half of the assertion. A
// validator that fails on everything reports nothing.
func TestSkillValidate_PassesOnAGoodSkill(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	writeSkill(t, root, filepath.Join(".atenea", "skills", "good"), goodSkill)

	got := invoke(t, "skill", "validate", "--cwd", root)
	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("validate wrote to stdout: %q", got.stdout)
	}
	if !strings.Contains(got.stderr, "ok") {
		t.Errorf("a passing run says nothing about what it checked:\n%s", got.stderr)
	}
}

// TestSkillValidate_FailsOnASkillNothingAnnounces: it parses, so discovery keeps
// it and nothing ever complains — and Format drops it from the system prompt, so
// the model is never told the name it would have to ask for. It is the same
// silent non-discovery seen one step later, which is why it is a problem rather
// than a remark.
func TestSkillValidate_FailsOnASkillNothingAnnounces(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	quiet := writeSkill(t, root, filepath.Join(".atenea", "skills", "quiet"), `---
name: quiet
---

No description.
`)

	got := invoke(t, "skill", "validate", "--cwd", root)
	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitFailure, got.stderr)
	}
	if !strings.Contains(got.stderr, quiet) || !strings.Contains(got.stderr, "description") {
		t.Errorf("the finding does not name the file and the missing key:\n%s", got.stderr)
	}
}

// TestSkillValidate_NamedPathsAreValidatedInstead: the discovery set answers "why
// does my skill not show up"; a named path answers "is this file right", which is
// the question a contributor has before the skill is installed anywhere. A file
// is read whatever it is called, because naming it is already saying which one.
func TestSkillValidate_NamedPathsAreValidatedInstead(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	// A directory discovery would never walk: the point is that the path decides.
	draft := filepath.Join(root, "drafts")
	broken := writeSkill(t, draft, "broken", "no frontmatter at all\n")
	good := writeSkill(t, draft, "good", goodSkill)

	failed := invoke(t, "skill", "validate", broken)
	if failed.code != ExitFailure {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", failed.code, ExitFailure, failed.stderr)
	}
	if !strings.Contains(failed.stderr, broken) || !strings.Contains(failed.stderr, "frontmatter") {
		t.Errorf("the finding does not name the file and the reason:\n%s", failed.stderr)
	}

	passed := invoke(t, "skill", "validate", good)
	if passed.code != ExitOK {
		t.Fatalf("a good skill named directly = %d, want %d\nstderr: %s", passed.code, ExitOK, passed.stderr)
	}

	// A directory is walked for the SKILL.md files under it, so a repository can
	// check its own skills in CI before anyone installs them.
	walked := invoke(t, "skill", "validate", draft)
	if walked.code != ExitFailure {
		t.Fatalf("validating the directory = %d, want %d\nstderr: %s", walked.code, ExitFailure, walked.stderr)
	}
	if !strings.Contains(walked.stderr, broken) {
		t.Errorf("walking the directory missed the broken skill:\n%s", walked.stderr)
	}
}

// TestSkillValidate_NothingToValidateIsAFailure: a contributor who points this at
// the wrong path and is told "ok" ships the skill broken. Zero files checked is
// not a pass.
func TestSkillValidate_NothingToValidateIsAFailure(t *testing.T) {
	isolateConfig(t)
	empty := t.TempDir()

	got := invoke(t, "skill", "validate", empty)
	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitFailure, got.stderr)
	}
	if !strings.Contains(got.stderr, "no SKILL.md") {
		t.Errorf("stderr does not say nothing was found:\n%s", got.stderr)
	}
}

// TestSkillValidate_MissingPathIsAUsageError: the invocation named something that
// is not there, which is the same mistake `--cwd /nope` is and gets the same
// answer. Reporting it as a validation failure would say the skill is broken when
// the argument is.
func TestSkillValidate_MissingPathIsAUsageError(t *testing.T) {
	isolateConfig(t)
	got := invoke(t, "skill", "validate", filepath.Join(t.TempDir(), "nope"))
	if got.code != ExitUsage {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitUsage, got.stderr)
	}
}

// TestSkillList_ShowsPrecedenceAndTheFilesThatWillNotLoad: the listing has to
// answer which file won, because that is the question two directories declaring
// one name produces, and it is the one thing the running agent never shows.
func TestSkillList_ShowsPrecedenceAndTheFilesThatWillNotLoad(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	// .atenea/skills is searched before .agents/skills, so the first wins.
	winner := writeSkill(t, root, filepath.Join(".atenea", "skills", "dup"), `---
name: dup
description: The project's own copy.
---

Body.
`)
	loser := writeSkill(t, root, filepath.Join(".agents", "skills", "dup"), `---
name: dup
description: The one that loses.
---

Body.
`)
	broken := writeSkill(t, root, filepath.Join(".atenea", "skills", "broken"), "no frontmatter\n")

	got := invoke(t, "skill", "list", "--cwd", root)
	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(got.stdout), "\n") {
		for _, path := range []string{winner, loser, broken} {
			if strings.Contains(line, path) {
				rows[path] = line
			}
		}
	}
	if len(rows) != 3 {
		t.Fatalf("every scanned SKILL.md must be listed, got:\n%s", got.stdout)
	}
	if !strings.Contains(rows[winner], statusActive) {
		t.Errorf("the winner is not listed as %s: %q", statusActive, rows[winner])
	}
	if !strings.Contains(rows[loser], statusShadowed) {
		t.Errorf("the shadowed copy is not listed as %s: %q", statusShadowed, rows[loser])
	}
	if !strings.Contains(rows[broken], statusInvalid) {
		t.Errorf("the unparseable file is not listed as %s: %q", statusInvalid, rows[broken])
	}
}

// TestSkillList_MatchesWhatARunWouldDiscover: the listing and the agent read one
// ordered directory list and one walk. A second answer to "which skills are
// there" is the failure this command exists to prevent, so it must not be able to
// become one itself.
func TestSkillList_MatchesWhatARunWouldDiscover(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	writeSkill(t, root, filepath.Join(".atenea", "skills", "good"), goodSkill)

	got := invoke(t, "skill", "list", "--cwd", root)
	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d", got.code, ExitOK)
	}
	// The built-in skills are materialized by the same function every entrypoint
	// calls, so they are in the listing exactly as they are in a run's prompt.
	if !strings.Contains(got.stdout, "ponytail") {
		t.Errorf("the built-in skills are missing from the listing:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "good") {
		t.Errorf("the workspace skill is missing from the listing:\n%s", got.stdout)
	}
}

// TestSkill_BuiltinsValidate: the skills atenea ships are held to the rule it
// enforces on everyone else's. A built-in that fails validation would make a
// stock install report problems it cannot fix.
func TestSkill_BuiltinsValidate(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()

	got := invoke(t, "skill", "validate", "--cwd", root)
	if got.code != ExitOK {
		t.Fatalf("the built-in skills do not validate: exit %d\n%s", got.code, got.stderr)
	}
}

// TestSkillList_EmptyWorkspaceNamesTheDirectoriesSearched: with nothing to list,
// stdout stays empty for the pipe and the answer to "where would you have looked"
// goes to the person.
func TestSkillList_EmptyWorkspaceNamesTheDirectoriesSearched(t *testing.T) {
	isolateConfig(t)
	// The built-ins are extracted into $HOME and would fill any listing, so the one
	// case with nothing to list is a home that cannot be resolved at all — which
	// leaves the project directories, and this project has no skills.
	t.Setenv("HOME", "")
	root := t.TempDir()

	got := invoke(t, "skill", "list", "--cwd", root)
	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing", got.stdout)
	}
	if !strings.Contains(got.stderr, filepath.Join(root, ".atenea", "skills")) {
		t.Errorf("stderr does not name the directories searched:\n%s", got.stderr)
	}
}
