package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/K3N4Y/atenea/internal/frontmatter"
	"go.yaml.in/yaml/v4"
)

// Def is a subagent definition: its metadata, Markdown prompt, and source path.
type Def struct {
	Version     int
	Name        string
	Description string
	Tools       []string
	Model       string
	Prompt      string
	Location    string
}

// Parse decodes a subagent's YAML frontmatter and separates it from its prompt.
// Version defaults to 1 for legacy manifests; unsupported versions are rejected.
// Unknown frontmatter keys are intentionally tolerated so another host can extend
// a compatible manifest. Location is left unset for Discover to populate.
func Parse(raw []byte) (Def, error) {
	var manifest struct {
		Version     int      `yaml:"version"`
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Tools       toolList `yaml:"tools"`
		Model       string   `yaml:"model"`
	}
	body, err := frontmatter.Parse(raw, &manifest)
	if err != nil {
		return Def{}, fmt.Errorf("agent: %w", err)
	}
	version, err := frontmatter.Version(manifest.Version)
	if err != nil {
		return Def{}, fmt.Errorf("agent: %w", err)
	}
	def := Def{
		Version:     version,
		Name:        manifest.Name,
		Description: strings.Join(strings.Fields(manifest.Description), " "),
		Tools:       []string(manifest.Tools),
		Model:       manifest.Model,
		Prompt:      string(body),
	}
	if def.Name == "" {
		return Def{}, fmt.Errorf("agent: the frontmatter declares no 'name'")
	}
	return def, nil
}

type toolList []string

func (t *toolList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		for _, name := range strings.Split(node.Value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				*t = append(*t, name)
			}
		}
		return nil
	}
	return node.Decode((*[]string)(t))
}

// Entry records what happened to one Markdown definition during discovery.
// Malformed and shadowed entries remain visible here even though Discover omits
// them from the usable catalog.
type Entry struct {
	Def
	Location   string
	Err        error
	ShadowedBy string
}

// Scan recursively records every Markdown definition discovery walks. The first
// definition of a name wins; missing directories contribute no entries.
func Scan(agentDirs ...string) ([]Entry, error) {
	var entries []Entry
	winners := make(map[string]string)
	for _, agentsDir := range agentDirs {
		err := filepath.WalkDir(agentsDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) {
					return nil // A missing base directory contributes no definitions.
				}
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			def, parseErr := Parse(raw)
			if parseErr != nil {
				entries = append(entries, Entry{Location: path, Err: parseErr})
				return nil
			}
			entry := Entry{Def: def, Location: path}
			def.Location = path
			entry.Def = def
			if winner := winners[def.Name]; winner != "" {
				entry.ShadowedBy = winner
			} else {
				winners[def.Name] = path
			}
			entries = append(entries, entry)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// Discover returns the usable, unshadowed definitions from Scan. Malformed
// definitions stay non-fatal so one broken file cannot prevent other subagents
// from loading.
func Discover(agentDirs ...string) ([]Def, error) {
	entries, err := Scan(agentDirs...)
	if err != nil {
		return nil, err
	}
	var out []Def
	for _, entry := range entries {
		if entry.Err == nil && entry.ShadowedBy == "" {
			out = append(out, entry.Def)
		}
	}
	return out, nil
}
