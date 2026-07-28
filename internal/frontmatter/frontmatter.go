// Package frontmatter decodes YAML metadata delimited by Markdown frontmatter.
package frontmatter

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v4"
)

// CurrentVersion is the schema version understood by the manifest parsers.
const CurrentVersion = 1

// Version resolves an omitted version as the legacy version and rejects schema
// versions whose semantics this build does not understand.
func Version(declared int) (int, error) {
	if declared == 0 {
		return CurrentVersion, nil
	}
	if declared != CurrentVersion {
		return 0, fmt.Errorf("unsupported manifest version %d (supported version: %d)", declared, CurrentVersion)
	}
	return declared, nil
}

// Parse decodes the opening YAML frontmatter into dst and returns the document
// body. Frontmatter delimiters must be complete lines at the start of the file.
func Parse(raw []byte, dst any) ([]byte, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, fmt.Errorf("no frontmatter: the document must start with a --- line")
	}

	front, body, ok := cutClosingDelimiter(rest)
	if !ok {
		return nil, fmt.Errorf("the frontmatter is never closed: no --- line ends it")
	}
	if err := yaml.Unmarshal([]byte(front), dst); err != nil {
		return nil, fmt.Errorf("decode frontmatter YAML: %w", err)
	}
	return []byte(body), nil
}

func cutClosingDelimiter(rest string) (front, body string, ok bool) {
	for offset := 0; ; {
		lineEnd := strings.IndexByte(rest[offset:], '\n')
		if lineEnd < 0 {
			if rest[offset:] == "---" {
				return rest[:offset], "", true
			}
			return "", "", false
		}
		lineEnd += offset
		if rest[offset:lineEnd] == "---" {
			return rest[:offset], rest[lineEnd+1:], true
		}
		offset = lineEnd + 1
	}
}
