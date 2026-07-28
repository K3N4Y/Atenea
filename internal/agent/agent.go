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

// Discover recursively scans agent directories for Markdown definitions. The
// first definition of a name wins; missing directories and malformed files are
// skipped so one broken definition cannot prevent discovery of the others.
func Discover(agentsDirs ...string) ([]Def, error) {
	var out []Def
	seen := make(map[string]bool)
	for _, agentsDir := range agentsDirs {
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
				return nil // Skip a malformed definition without breaking discovery.
			}
			if seen[def.Name] {
				return nil // The first occurrence wins.
			}
			seen[def.Name] = true
			def.Location = path
			out = append(out, def)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
