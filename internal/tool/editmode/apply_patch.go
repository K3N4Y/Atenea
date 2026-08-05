package editmode

import (
	"fmt"
	"strings"
)

type PatchEntry struct{ Op, Path, Rename, Diff string }

const (
	beginPatch = "*** Begin Patch"
	endPatch   = "*** End Patch"
	addFile    = "*** Add File: "
	deleteFile = "*** Delete File: "
	updateFile = "*** Update File: "
	moveTo     = "*** Move to: "
)

// ParseApplyPatch ports packages/coding-agent/src/edit/apply-patch/parser.ts
// at oh-my-pi@5af71dc9cf132538e072806424f71f43f734d9ae.
func ParseApplyPatch(input string) ([]PatchEntry, error) { return parseApplyPatch(input, false) }

// ParseApplyPatchStreaming is the pure, best-effort preview parser.
func ParseApplyPatchStreaming(input string) []PatchEntry {
	entries, _ := parseApplyPatch(input, true)
	return entries
}

func parseApplyPatch(input string, streaming bool) ([]PatchEntry, error) {
	lines := strings.Split(strings.TrimSpace(input), "\n")
	if len(lines) >= 2 {
		first, last := lines[0], strings.TrimSpace(lines[len(lines)-1])
		if (first == "<<EOF" || first == "<<'EOF'" || first == `<<"EOF"`) && last == "EOF" {
			lines = lines[1 : len(lines)-1]
		}
	}
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != beginPatch {
		if streaming {
			return nil, nil
		}
		return nil, fmt.Errorf("The first line of the patch must be '*** Begin Patch'")
	}
	hasEnd := strings.TrimSpace(lines[len(lines)-1]) == endPatch
	if !hasEnd && !streaming {
		return nil, fmt.Errorf("The last line of the patch must be '*** End Patch'")
	}
	end := len(lines)
	if hasEnd {
		end--
	}
	remaining, lineNumber := lines[1:end], 2
	var out []PatchEntry
	for len(remaining) > 0 {
		if strings.TrimSpace(remaining[0]) == "" {
			remaining, lineNumber = remaining[1:], lineNumber+1
			continue
		}
		header := strings.TrimSpace(remaining[0])
		switch {
		case strings.HasPrefix(header, addFile):
			e := PatchEntry{Op: "create", Path: strings.TrimPrefix(header, addFile)}
			consumed := 1
			for consumed < len(remaining) && strings.HasPrefix(remaining[consumed], "+") {
				e.Diff += remaining[consumed][1:] + "\n"
				consumed++
			}
			out = append(out, e)
			remaining, lineNumber = remaining[consumed:], lineNumber+consumed
		case strings.HasPrefix(header, deleteFile):
			out = append(out, PatchEntry{Op: "delete", Path: strings.TrimPrefix(header, deleteFile)})
			remaining, lineNumber = remaining[1:], lineNumber+1
		case strings.HasPrefix(header, updateFile):
			e := PatchEntry{Op: "update", Path: strings.TrimPrefix(header, updateFile)}
			remaining, lineNumber = remaining[1:], lineNumber+1
			if len(remaining) > 0 && strings.HasPrefix(remaining[0], moveTo) {
				e.Rename = strings.TrimPrefix(remaining[0], moveTo)
				remaining, lineNumber = remaining[1:], lineNumber+1
			}
			var body []string
			for len(remaining) > 0 && !isFileOperation(remaining[0]) {
				body = append(body, remaining[0])
				remaining, lineNumber = remaining[1:], lineNumber+1
			}
			if len(body) == 0 && !streaming {
				return nil, fmt.Errorf("Line %d: Update file hunk for path '%s' is empty", lineNumber, e.Path)
			}
			e.Diff = strings.Join(body, "\n")
			out = append(out, e)
		default:
			if streaming {
				return out, nil
			}
			return nil, fmt.Errorf("Line %d: '%s' is not a valid hunk header. Valid hunk headers: '*** Add File: {path}', '*** Delete File: {path}', '*** Update File: {path}'", lineNumber, header)
		}
	}
	return out, nil
}

func isFileOperation(line string) bool {
	return strings.HasPrefix(line, "*** Add File:") || strings.HasPrefix(line, "*** Delete File:") || strings.HasPrefix(line, "*** Update File:")
}

// TrimUnfinishedTrailingLine keeps streaming projections monotonic.
func TrimUnfinishedTrailingLine(input string) string {
	if input == "" || strings.HasSuffix(input, "\n") {
		return input
	}
	if i := strings.LastIndexByte(input, '\n'); i >= 0 {
		return input[:i+1]
	}
	return ""
}

// ApplyPatchMatcherEntries isolates each file operation and projects only its
// introduced content. Create entries already carry their complete file body.
func ApplyPatchMatcherEntries(input string) []PatchProjection {
	entries := ParseApplyPatchStreaming(TrimUnfinishedTrailingLine(input))
	out := make([]PatchProjection, 0, len(entries))
	for _, entry := range entries {
		if entry.Path != "" {
			out = append(out, PatchProjection{Path: entry.Path, Digest: matcherAddedContent(entry)})
		}
	}
	return out
}

// ApplyUnified applies the stripped-down @@ format used by patch/apply_patch.
func ApplyUnified(content, diff string) (string, error) {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	chunks := strings.Split(diff, "\n@@")
	for ci, raw := range chunks {
		raw = strings.TrimPrefix(raw, "@@")
		if ci > 0 {
			raw = strings.TrimPrefix(raw, " ")
		}
		rows := strings.Split(raw, "\n")
		if len(rows) > 0 && rows[0] != "" && !strings.HasPrefix(rows[0], "+") && !strings.HasPrefix(rows[0], "-") && !strings.HasPrefix(rows[0], " ") {
			rows = rows[1:]
		} else if len(rows) > 0 && rows[0] == "" {
			rows = rows[1:]
		}
		var old, replacement []string
		for _, row := range rows {
			if row == "*** End of File" {
				continue
			}
			if row == "" {
				continue
			}
			switch row[0] {
			case ' ':
				old = append(old, row[1:])
				replacement = append(replacement, row[1:])
			case '-':
				old = append(old, row[1:])
			case '+':
				replacement = append(replacement, row[1:])
			default:
				return "", fmt.Errorf("invalid patch line %q", row)
			}
		}
		if len(old) == 0 && len(replacement) == 0 {
			return "", fmt.Errorf("empty update hunk")
		}
		found, occurrences := -1, 0
		for i := 0; i+len(old) <= len(lines); i++ {
			ok := true
			for j := range old {
				if lines[i+j] != old[j] {
					ok = false
					break
				}
			}
			if ok {
				found = i
				occurrences++
			}
		}
		if occurrences != 1 {
			if occurrences > 1 {
				return "", fmt.Errorf("Found multiple matches for patch context")
			}
			return "", fmt.Errorf("No match found for patch context")
		}
		lines = append(append(append([]string{}, lines[:found]...), replacement...), lines[found+len(old):]...)
	}
	return strings.Join(lines, "\n") + "\n", nil
}
