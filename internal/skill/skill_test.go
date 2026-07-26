package skill

// White-box tests (same package), for consistency with prompt/tool. One per
// behavior. They start with Parse: separating a SKILL.md's frontmatter
// (name/description) from its Markdown body.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates <root>/<rel>/SKILL.md with the given frontmatter. Helper of
// the Scan and Discover tests.
func writeSkill(t *testing.T, root, rel, name, desc string) string {
	t.Helper()
	dir := filepath.Join(root, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	front := "---\nname: " + name + "\n"
	if desc != "" {
		front += "description: " + desc + "\n"
	}
	front += "---\nthe body of " + name + "\n"
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(front), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// writeFile puts arbitrary content at <root>/<rel>/SKILL.md, for the files that
// are not skills at all.
func writeFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	dir := filepath.Join(root, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// Parse extracts name/description from the frontmatter delimited by --- and
// leaves the rest as Content (without the frontmatter).
func TestParse_ExtractsNameDescriptionAndBody(t *testing.T) {
	raw := []byte("---\nname: demo\ndescription: Does something useful\n---\n# Demo\n\nthe skill's body\n")

	info, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if info.Name != "demo" {
		t.Errorf("Name = %q, want %q", info.Name, "demo")
	}
	if info.Description != "Does something useful" {
		t.Errorf("Description = %q, want %q", info.Description, "Does something useful")
	}
	if !strings.Contains(info.Content, "the skill's body") {
		t.Errorf("Content = %q, want it to hold the skill's body", info.Content)
	}
	if strings.Contains(info.Content, "name:") {
		t.Errorf("Content must not include the frontmatter; got: %q", info.Content)
	}
}

// Parse supports folded YAML block descriptions ('>'): the indented lines that
// follow are joined into one line (whitespace collapsed) for the menu and the
// prompt. Many global skills (~/.claude/skills, say) are written this way.
func TestParse_FoldedBlockDescription(t *testing.T) {
	raw := []byte("---\nname: demo\ndescription: >\n  First line\n  second line.\n---\nbody\n")
	info, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if info.Description != "First line second line." {
		t.Fatalf("Description = %q, want the folded block on one line", info.Description)
	}
	if strings.Contains(info.Content, "First line") {
		t.Errorf("the block description must not leak into Content: %q", info.Content)
	}
}

// TRIANGULATE: a continuation line of the block holding a ":" (as in "X: Y") must
// NOT be mistaken for a new key; it stays whole in the description. That is the
// real shape of several global skills (~/.claude/skills/ponytail, say).
func TestParse_FoldedBlockDescriptionWithColon(t *testing.T) {
	raw := []byte("---\nname: demo\ndescription: >\n  Use when X happens: it does the thing\n  and something else.\n---\nbody\n")
	info, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	want := "Use when X happens: it does the thing and something else."
	if info.Description != want {
		t.Fatalf("Description = %q, want %q", info.Description, want)
	}
}

// TRIANGULATE: a literal block ('|') with a blank line in the middle is
// normalized to one line too (a description is one-line metadata for the menu).
func TestParse_LiteralBlockDescription(t *testing.T) {
	raw := []byte("---\nname: demo\ndescription: |\n  One\n\n  Two\n---\nbody\n")
	info, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if info.Description != "One Two" {
		t.Fatalf("Description = %q, want \"One Two\"", info.Description)
	}
}

// TRIANGULATE: a skill with no description parses (with Description ""); Format
// filters it out later, but Parse must not reject it.
func TestParse_MissingDescription(t *testing.T) {
	info, err := Parse([]byte("---\nname: name-only\n---\nbody\n"))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if info.Name != "name-only" {
		t.Errorf("Name = %q, want %q", info.Name, "name-only")
	}
	if info.Description != "" {
		t.Errorf("Description = %q, want it empty", info.Description)
	}
	if info.Announced() {
		t.Error("a skill with no description must not report itself as announced")
	}
}

// TRIANGULATE: a frontmatter with no name is an error; a skill without a name
// cannot be referenced by the model.
func TestParse_NameRequired(t *testing.T) {
	if _, err := Parse([]byte("---\ndescription: no name\n---\nbody\n")); err == nil {
		t.Fatal("Parse with no name: want an error, got none")
	}
}

// TRIANGULATE: a file with no frontmatter (it does not start with ---) is an
// error rather than being taken whole as the body of an anonymous skill.
func TestParse_NoFrontmatter(t *testing.T) {
	if _, err := Parse([]byte("# Just Markdown\nno frontmatter\n")); err == nil {
		t.Fatal("Parse with no frontmatter: want an error, got none")
	}
}

// Discover scans for SKILL.md under the directory and returns each skill with its
// Location pointing at the SKILL.md.
func TestDiscover_FindsSkillInDir(t *testing.T) {
	root := t.TempDir()
	loc := writeSkill(t, root, "foo", "foo", "a skill")

	list, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("Discover returned %d skills, want 1", len(list))
	}
	if list[0].Name != "foo" {
		t.Errorf("Name = %q, want %q", list[0].Name, "foo")
	}
	if list[0].Location != loc {
		t.Errorf("Location = %q, want %q", list[0].Location, loc)
	}
}

// TRIANGULATE: it finds several skills, including ones nested in subdirectories.
func TestDiscover_MultipleAndNested(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "foo", "foo", "a")
	writeSkill(t, root, filepath.Join("group", "bar"), "bar", "b")

	list, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	got := map[string]bool{}
	for _, s := range list {
		got[s.Name] = true
	}
	if !got["foo"] || !got["bar"] {
		t.Fatalf("Discover = %v, want it to hold foo and bar", got)
	}
}

// TRIANGULATE: a missing directory is not an error, it contributes no skills.
func TestDiscover_DirMissing(t *testing.T) {
	list, err := Discover(filepath.Join(t.TempDir(), "not", "there"))
	if err != nil {
		t.Fatalf("Discover on a missing dir: unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("Discover on a missing dir = %d skills, want 0", len(list))
	}
}

// TRIANGULATE: an unreadable skill (frontmatter with no name) is skipped without
// breaking the discovery of the valid ones.
func TestDiscover_SkipsUnparseable(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "good", "good", "ok")
	writeFile(t, root, "bad", "---\ndescription: no name\n---\nx\n")

	list, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].Name != "good" {
		t.Fatalf("Discover = %v, want only the 'good' skill", list)
	}
}

// Discover accepts several directories and merges their skills (.atenea/skills
// and the standard .agents/skills, say).
func TestDiscover_MergesMultipleDirs(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeSkill(t, dirA, "own", "own", "a")
	writeSkill(t, dirB, "standard", "standard", "b")

	list, err := Discover(dirA, dirB)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	got := map[string]bool{}
	for _, s := range list {
		got[s.Name] = true
	}
	if !got["own"] || !got["standard"] {
		t.Fatalf("Discover = %v, want the skills of both directories (own, standard)", got)
	}
}

// TRIANGULATE: with the same skill name in two directories, the one listed first
// wins (a local override of the standard). Checked through Location, which points
// at the winning directory's SKILL.md.
func TestDiscover_DedupesByNameFirstWins(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	locA := writeSkill(t, dirA, "dup", "dup", "from A")
	writeSkill(t, dirB, "dup", "dup", "from B")

	list, err := Discover(dirA, dirB)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("Discover = %d skills, want 1 (deduplicated by name)", len(list))
	}
	if list[0].Location != locA {
		t.Errorf("Location = %q, want the first directory's %q", list[0].Location, locA)
	}
}

// Scan reports every SKILL.md the walk found, including the ones Discover drops,
// and says why it dropped them. It is what `atenea skill validate` reads, and the
// reason a broken skill can be reported at all: Discover is defined as this list
// minus the failures, so the two cannot describe different sets of files.
func TestScan_ReportsWhatDiscoveryDrops(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	good := writeSkill(t, dirA, "good", "good", "fine")
	winner := writeSkill(t, dirA, "dup", "dup", "from A")
	loser := writeSkill(t, dirB, "dup", "dup", "from B")
	broken := writeFile(t, dirB, "bad", "no frontmatter here\n")

	entries, err := Scan(dirA, dirB)
	if err != nil {
		t.Fatalf("Scan: unexpected error: %v", err)
	}
	byLocation := map[string]Entry{}
	for _, entry := range entries {
		byLocation[entry.Location] = entry
	}
	if len(byLocation) != 4 {
		t.Fatalf("Scan = %d entries, want one per SKILL.md found: %v", len(byLocation), byLocation)
	}
	if entry := byLocation[good]; entry.Err != nil || entry.ShadowedBy != "" || entry.Info.Name != "good" {
		t.Errorf("a healthy skill must be reported as one: %+v", entry)
	}
	if entry := byLocation[winner]; entry.ShadowedBy != "" {
		t.Errorf("the first declaration of a name must win: %+v", entry)
	}
	if entry := byLocation[loser]; entry.ShadowedBy != winner {
		t.Errorf("the shadowed declaration must name the file that beat it: %+v", entry)
	}
	if entry := byLocation[broken]; entry.Err == nil {
		t.Errorf("an unparseable file must be reported with its reason: %+v", entry)
	}

	// And the fold: Discover is Scan minus what it drops.
	list, err := Discover(dirA, dirB)
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("Discover = %d skills, want the 2 usable ones: %+v", len(list), list)
	}
}

// TRIANGULATE: a SKILL.md that cannot be read is an entry with an error, not a
// failed scan. One unreadable file must not cost the caller every other skill —
// the same promise Discover makes for one that does not parse.
func TestScan_UnreadableFileIsAnEntryNotAFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file regardless of its mode")
	}
	root := t.TempDir()
	writeSkill(t, root, "good", "good", "fine")
	unreadable := writeFile(t, root, "locked", "---\nname: locked\n---\nx\n")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o644) })

	entries, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: unexpected error: %v", err)
	}
	found := false
	for _, entry := range entries {
		if entry.Location == unreadable {
			found = true
			if entry.Err == nil {
				t.Errorf("the unreadable file must carry its error: %+v", entry)
			}
		}
	}
	if !found {
		t.Error("the unreadable file was not reported at all")
	}
	list, err := Discover(root)
	if err != nil || len(list) != 1 || list[0].Name != "good" {
		t.Fatalf("Discover = %+v, %v; want the readable skill and no error", list, err)
	}
}

// Format builds the verbose <available_skills> block (name + description +
// location) that travels in the system prompt.
func TestFormat_RendersAvailableSkillsBlock(t *testing.T) {
	got := Format([]Info{{Name: "foo", Description: "does foo", Location: "/abs/foo/SKILL.md"}})

	for _, want := range []string{
		"<available_skills>",
		"<name>foo</name>",
		"<description>does foo</description>",
		"<location>/abs/foo/SKILL.md</location>",
		"</available_skills>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Format does not hold %q; got:\n%s", want, got)
		}
	}
}

// TRIANGULATE: with no skills (or none with a description) the block is "" so the
// system prompt does not append an empty section.
func TestFormat_Empty(t *testing.T) {
	if got := Format(nil); got != "" {
		t.Errorf("Format(nil) = %q, want it empty", got)
	}
	if got := Format([]Info{{Name: "x"}}); got != "" {
		t.Errorf("Format(with no description) = %q, want it empty", got)
	}
}

// TRIANGULATE: a skill with a description is included and one without is filtered
// out, in the same list.
func TestFormat_FiltersWithoutDescription(t *testing.T) {
	got := Format([]Info{
		{Name: "visible", Description: "yes"},
		{Name: "hidden", Description: ""},
	})
	if !strings.Contains(got, "<name>visible</name>") {
		t.Errorf("Format must include the skill with a description; got:\n%s", got)
	}
	if strings.Contains(got, "<name>hidden</name>") {
		t.Errorf("Format must not include the skill without a description; got:\n%s", got)
	}
}

// TRIANGULATE: the output order is by name, not the input order.
func TestFormat_SortedByName(t *testing.T) {
	got := Format([]Info{
		{Name: "bravo", Description: "b"},
		{Name: "alpha", Description: "a"},
	})
	if strings.Index(got, "alpha") > strings.Index(got, "bravo") {
		t.Errorf("Format must sort alpha before bravo; got:\n%s", got)
	}
}
