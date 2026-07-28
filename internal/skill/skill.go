package skill

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/K3N4Y/atenea/internal/frontmatter"
)

// Info is a discovered skill: its metadata (Name, Description) plus its Content
// (the SKILL.md body without the frontmatter) and Location (the absolute path of
// the SKILL.md, which resolves the base directory and lists the skill's resources
// when it is loaded).
type Info struct {
	Version     int
	Name        string
	Description string
	Location    string
	Content     string
}

// Announced reports whether this skill reaches the model at all. Format puts only
// described skills in the system prompt, so one without a description is
// discovered, indexed by the skill tool, and never mentioned to the model that
// would have to name it to load it. It is a method so the rule has one home: the
// prompt applies it, and `atenea skill validate` reports it.
func (i Info) Announced() bool { return i.Description != "" }

// Parse separates a SKILL.md's frontmatter from its body. The frontmatter is the
// block delimited by "---" at the start of the file. The rest is Content. A file with no
// frontmatter, or with no name, is an error: a skill without a name cannot be
// referenced by the model. Version defaults to 1 for legacy manifests; unsupported
// versions are rejected. Unknown frontmatter keys are intentionally tolerated so
// another host can extend a compatible manifest. Parse does not set Location (Scan does).
//
// The errors are written to be read by a contributor whose skill did not show up,
// because that is where they surface: `atenea skill validate` prints them next to
// the file that produced them.
func Parse(raw []byte) (Info, error) {
	var manifest struct {
		Version     int    `yaml:"version"`
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	body, err := frontmatter.Parse(raw, &manifest)
	if err != nil {
		return Info{}, err
	}
	version, err := frontmatter.Version(manifest.Version)
	if err != nil {
		return Info{}, err
	}
	info := Info{
		Version:     version,
		Name:        manifest.Name,
		Description: strings.Join(strings.Fields(manifest.Description), " "),
		Content:     string(body),
	}
	if info.Name == "" {
		return Info{}, fmt.Errorf("the frontmatter declares no 'name'")
	}
	return info, nil
}

// Entry is one SKILL.md the walk found and what became of it. Every file is
// reported, including the ones discovery drops, which is the difference between
// Scan and Discover.
type Entry struct {
	// Location is the path of the SKILL.md, as walked.
	Location string
	// Info is the parsed skill, with Location set. The zero value when Err is set.
	Info Info
	// Err is why this file is not a usable skill: it could not be read, or its
	// frontmatter does not declare one. Discovery skips it in silence, which is the
	// non-discovery `atenea skill validate` exists to make loud.
	Err error
	// ShadowedBy is the Location of the same-named skill that won, empty when this
	// one did. Precedence is a feature, not a fault — a project skill is meant to
	// override a global one — so this is reported, never treated as an error.
	ShadowedBy string
}

// Scan walks each skillsDir recursively looking for SKILL.md and reports one
// Entry per file found, in walk order. It accepts several directories (the
// project's .atenea/skills and the standard .agents/skills, say) and merges them
// in order: on a duplicate name the FIRST occurrence wins, so a directory listed
// earlier takes precedence over the ones after it. A missing directory is not an
// error (it contributes no skills).
//
// It is the one walk. Discover is this list with the unusable and the shadowed
// entries dropped, so what the agent loads and what `atenea skill list` prints
// cannot disagree — and the only difference between "not there" and "there and
// rejected" is a field rather than a second implementation.
func Scan(skillsDirs ...string) ([]Entry, error) {
	var entries []Entry
	winner := make(map[string]string)
	for _, skillsDir := range skillsDirs {
		err := filepath.WalkDir(skillsDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) {
					return nil // the base directory is absent: no skills here
				}
				return walkErr
			}
			if d.IsDir() || d.Name() != "SKILL.md" {
				return nil
			}
			entries = append(entries, scanFile(path, winner))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// scanFile reads and parses one SKILL.md, recording the name's winner so a later
// declaration of it is reported as shadowed rather than silently dropped.
func scanFile(path string, winner map[string]string) Entry {
	entry := Entry{Location: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		entry.Err = err
		return entry
	}
	info, err := Parse(raw)
	if err != nil {
		entry.Err = err
		return entry
	}
	info.Location = path
	entry.Info = info
	if first, taken := winner[info.Name]; taken {
		entry.ShadowedBy = first
		return entry
	}
	winner[info.Name] = path
	return entry
}

// Discover returns the skills the agent runs with: every scanned entry that
// parsed and was not shadowed by an earlier one of the same name.
//
// A SKILL.md that cannot be read or parsed is skipped rather than fatal, so one
// broken skill cannot take the others down with it — and `atenea skill validate`
// is where that silence is broken.
func Discover(skillsDirs ...string) ([]Info, error) {
	entries, err := Scan(skillsDirs...)
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, entry := range entries {
		if entry.Err != nil || entry.ShadowedBy != "" {
			continue
		}
		out = append(out, entry.Info)
	}
	return out, nil
}

// Format builds the verbose block of available skills for the system prompt: a
// preamble plus <available_skills> with name/description/location per skill,
// sorted by name. Skills with no description are filtered out (they give the model
// nothing to decide when to load them by). If none is left it returns "", so the
// prompt assembler omits the block entirely.
func Format(list []Info) string {
	described := make([]Info, 0, len(list))
	for _, s := range list {
		if s.Announced() {
			described = append(described, s)
		}
	}
	if len(described) == 0 {
		return ""
	}
	sort.Slice(described, func(i, j int) bool { return described[i].Name < described[j].Name })

	var b strings.Builder
	b.WriteString("Skills provide specialized instructions and workflows for specific tasks.\n")
	b.WriteString("Use the skill tool to load a skill when a task matches its description.\n")
	b.WriteString("<available_skills>\n")
	for _, s := range described {
		b.WriteString("  <skill>\n")
		b.WriteString("    <name>" + s.Name + "</name>\n")
		b.WriteString("    <description>" + s.Description + "</description>\n")
		b.WriteString("    <location>" + s.Location + "</location>\n")
		b.WriteString("  </skill>\n")
	}
	b.WriteString("</available_skills>")
	return b.String()
}
