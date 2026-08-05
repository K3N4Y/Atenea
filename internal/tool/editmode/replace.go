package editmode

import (
	"fmt"
	"strings"
)

// Replace implements the replace mode's exact matching and the upstream 0.95
// line-window fuzzy fallback. The caller owns BOM/EOL serialization.
type ReplaceInput struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// StreamingEntry is the pure replace projection for a future general matcher
// seam. It does not expose the upstream internal batch form to the model.
type StreamingEntry struct {
	Path   string
	Digest string
}

func ReplaceMatcherEntries(input ReplaceInput) []StreamingEntry {
	if input.Path == "" {
		return nil
	}
	return []StreamingEntry{{Path: input.Path, Digest: input.NewString}}
}

func ReplaceText(content string, input ReplaceInput, allowFuzzy bool, threshold float64) (string, int, error) {
	if input.OldString == "" {
		return "", 0, fmt.Errorf("old_string must not be empty.")
	}
	if threshold == 0 {
		threshold = DefaultFuzzyThreshold
	}
	if input.ReplaceAll {
		if count := strings.Count(content, input.OldString); count > 0 {
			return strings.ReplaceAll(content, input.OldString, input.NewString), count, nil
		}
		updated, count := replaceAllFuzzy(content, input.OldString, input.NewString, allowFuzzy, threshold)
		if count == 0 {
			o := FindMatch(content, input.OldString, allowFuzzy, threshold, nil)
			return "", 0, matchError(input.Path, input.OldString, o, allowFuzzy, threshold)
		}
		return updated, count, nil
	}
	o := FindMatch(content, input.OldString, allowFuzzy, threshold, nil)
	if o.Occurrences > 1 {
		return "", 0, fmt.Errorf("%s", formatOccurrences(input.Path, o))
	}
	if o.Match == nil {
		return "", 0, matchError(input.Path, input.OldString, o, allowFuzzy, threshold)
	}
	m := o.Match
	replacement := adjustIndentation(input.OldString, m.ActualText, input.NewString)
	return content[:m.StartIndex] + replacement + content[m.StartIndex+len(m.ActualText):], 1, nil
}
