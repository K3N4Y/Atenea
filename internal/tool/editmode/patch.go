package editmode

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PatchHunk is the contextual hunk grammar used by the JSON patch mode.
// Ported from oh-my-pi@5af71dc9 packages/coding-agent/src/edit/diff.ts and
// src/edit/modes/patch.ts.
type PatchHunk struct {
	Anchor                     string
	OldStartLine, NewStartLine int
	HasLineHint, HasContext    bool
	OldLines, NewLines         []string
	EndOfFile                  bool
}

type PatchProjection struct{ Path, Digest string }

// PatchMatcherEntries projects only content introduced by each edit. Matcher
// digests are not diffs: removed/context rows and hunk syntax must not grant a
// content match.
func PatchMatcherEntries(path string, edits []PatchEntry) []PatchProjection {
	if path == "" || len(edits) == 0 {
		return nil
	}
	out := make([]PatchProjection, 0, len(edits))
	for _, edit := range edits {
		out = append(out, PatchProjection{Path: path, Digest: matcherAddedContent(edit)})
	}
	return out
}

func matcherAddedContent(edit PatchEntry) string {
	if edit.Op == "create" {
		return edit.Diff
	}
	var added []string
	for _, row := range strings.Split(strings.ReplaceAll(strings.ReplaceAll(edit.Diff, "\r\n", "\n"), "\r", "\n"), "\n") {
		if strings.HasPrefix(row, "+") {
			added = append(added, row[1:])
		}
	}
	return strings.Join(added, "\n")
}

var unifiedHeader = regexp.MustCompile(`^@@\s*-(\d+)(?:,(\d+))?\s+\+(\d+)(?:,(\d+))?\s*@@\s*(.*)$`)
var lineHintHeader = regexp.MustCompile(`(?i)^lines?\s+(\d+)(?:\s*-\s*\d+)?(?:\s*@@)?$`)
var numberedRow = regexp.MustCompile(`^([ +\-])\d+\|(.*)$`)
var looseNumberedRow = regexp.MustCompile(`^\s*\d{1,6}\s+(.+)$`)

// ParsePatchHunks accepts contextual @@ anchors and standard unified line hints.
func ParsePatchHunks(diff string) ([]PatchHunk, error) {
	diff = strings.ReplaceAll(strings.ReplaceAll(diff, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(diff, "\n")
	for len(lines) > 0 && patchWrapper(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && patchWrapper(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	if countPatchFiles(lines) > 1 {
		return nil, fmt.Errorf("Diff contains %d file markers. Single-file patches cannot contain multi-file markers.", countPatchFiles(lines))
	}
	var hunks []PatchHunk
	var current *PatchHunk
	flush := func() error {
		if current == nil {
			return nil
		}
		if len(current.OldLines) == 0 && len(current.NewLines) == 0 {
			return fmt.Errorf("Hunk does not contain any lines")
		}
		stripLooseLineNumbers(current)
		hunks = append(hunks, *current)
		return nil
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "@@") {
			anchorText := strings.TrimSpace(strings.TrimPrefix(trimmed, "@@"))
			if current != nil && len(current.OldLines) == 0 && len(current.NewLines) == 0 {
				anchorText = strings.TrimSpace(strings.TrimPrefix(anchorText, "@@"))
				if anchorText != "" {
					if current.Anchor != "" {
						current.Anchor += "\n"
					}
					current.Anchor += anchorText
				}
				continue
			}
			if err := flush(); err != nil {
				return nil, err
			}
			h := PatchHunk{}
			if m := unifiedHeader.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				h.OldStartLine, _ = strconv.Atoi(m[1])
				h.NewStartLine, _ = strconv.Atoi(m[3])
				if h.OldStartLine < 1 || h.NewStartLine < 1 {
					return nil, fmt.Errorf("Line %d: Line numbers in @@ header must be >= 1", i+1)
				}
				h.HasLineHint = true
				h.Anchor = m[5]
			} else if m := lineHintHeader.FindStringSubmatch(anchorText); m != nil {
				h.OldStartLine, _ = strconv.Atoi(m[1])
				h.NewStartLine = h.OldStartLine
				h.HasLineHint = true
			} else if regexp.MustCompile(`(?i)^(top|start|beginning)\s+of\s+file$`).MatchString(anchorText) {
				h.OldStartLine, h.NewStartLine, h.HasLineHint = 1, 1, true
			} else {
				h.Anchor = strings.TrimSpace(strings.TrimPrefix(anchorText, "@@"))
			}
			current = &h
			continue
		}
		if current == nil {
			if trimmed == "" || patchMetadata(trimmed) {
				continue
			}
			// A prefix-only -/+ patch is a valid single hunk.
			current = &PatchHunk{}
		}
		if trimmed == "*** End of File" {
			current.EndOfFile = true
			continue
		}
		if trimmed == "..." || trimmed == "…" {
			continue
		}
		if m := numberedRow.FindStringSubmatch(line); m != nil {
			line = m[1] + m[2]
		}
		if line == "" { // Upstream accepts interior unprefixed blank context rows.
			if i == len(lines)-1 {
				continue
			}
			current.HasContext = true
			current.OldLines = append(current.OldLines, "")
			current.NewLines = append(current.NewLines, "")
			continue
		}
		switch line[0] {
		case ' ':
			current.HasContext = true
			current.OldLines = append(current.OldLines, line[1:])
			current.NewLines = append(current.NewLines, line[1:])
		case '-':
			current.OldLines = append(current.OldLines, line[1:])
		case '+':
			current.NewLines = append(current.NewLines, line[1:])
		default:
			// Legacy model output commonly omits the context prefix.
			current.HasContext = true
			current.OldLines = append(current.OldLines, line)
			current.NewLines = append(current.NewLines, line)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("Diff does not contain any lines")
	}
	return hunks, nil
}

func patchWrapper(line string) bool {
	s := strings.TrimSpace(line)
	return s == "*** Begin Patch" || s == "*** End Patch" || s == "***"
}

func patchMetadata(s string) bool {
	for _, p := range []string{"diff --git ", "index ", "--- ", "+++ ", "new file mode ", "deleted file mode ", "rename from ", "rename to ", "similarity index ", "dissimilarity index ", "old mode ", "new mode ", "*** Update File:", "*** Add File:", "*** Delete File:"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func countPatchFiles(lines []string) int {
	paths := map[string]bool{}
	for _, line := range lines {
		s := strings.TrimSpace(line)
		for _, p := range []string{"*** Update File:", "*** Add File:", "*** Delete File:"} {
			if strings.HasPrefix(s, p) {
				paths[strings.TrimSpace(strings.TrimPrefix(s, p))] = true
			}
		}
		if strings.HasPrefix(s, "diff --git ") {
			parts := strings.Fields(s)
			if len(parts) >= 4 {
				paths[strings.TrimPrefix(parts[3], "b/")] = true
			}
		}
	}
	return len(paths)
}

func stripLooseLineNumbers(h *PatchHunk) {
	all := append(append([]string{}, h.OldLines...), h.NewLines...)
	numbers := []int{}
	for _, line := range all {
		if m := looseNumberedRow.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(strings.Fields(line)[0])
			numbers = append(numbers, n)
		}
	}
	if len(numbers) < 2 || len(numbers)*10 < len(all)*6 {
		return
	}
	sequential := 0
	for i := 1; i < len(numbers); i++ {
		if numbers[i] == numbers[i-1]+1 {
			sequential++
		}
	}
	if len(numbers) >= 3 && sequential < max(1, len(numbers)-2) {
		return
	}
	strip := func(lines []string) {
		for i, line := range lines {
			if m := looseNumberedRow.FindStringSubmatch(line); m != nil {
				lines[i] = m[1]
			}
		}
	}
	strip(h.OldLines)
	strip(h.NewLines)
}

// NormalizePatchCreateContent implements JSON patch create-as-overwrite content semantics.
func NormalizePatchCreateContent(content string) string {
	lines := strings.Split(content, "\n")
	nonempty, prefixed := 0, true
	for _, line := range lines {
		if line != "" {
			nonempty++
			if !strings.HasPrefix(line, "+") {
				prefixed = false
			}
		}
	}
	if nonempty > 0 && prefixed {
		for i, line := range lines {
			if strings.HasPrefix(line, "+ ") {
				lines[i] = line[2:]
			} else if strings.HasPrefix(line, "+") {
				lines[i] = line[1:]
			}
		}
		content = strings.Join(lines, "\n")
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content
}

// ApplyContextPatch applies all hunks against one immutable source, while
// advancing search position between hunks, and preserves final-newline policy.
func ApplyContextPatch(content, path, diff string, allowFuzzy bool, threshold float64) (string, []string, error) {
	hunks, err := ParsePatchHunks(diff)
	if err != nil {
		return "", nil, err
	}
	hadFinal := strings.HasSuffix(content, "\n")
	lines := strings.Split(content, "\n")
	if hadFinal {
		lines = lines[:len(lines)-1]
	}
	type replacement struct {
		at, old int
		lines   []string
	}
	var replacements []replacement
	var warnings []string
	currentIndex := 0
	for _, h := range hunks {
		start, actual, confidence, strategy, _, err := findPatchHunk(lines, h, currentIndex, allowFuzzy, threshold)
		if err != nil {
			return "", nil, fmt.Errorf("%s in %s", err, path)
		}
		patchOldLines, patchNewLines := h.OldLines, h.NewLines
		for _, variant := range patchFallbackVariants(h, h.Anchor != "" || h.HasLineHint || h.EndOfFile) {
			if equalStringSlices(actual, variant.OldLines) {
				patchOldLines, patchNewLines = variant.OldLines, variant.NewLines
				break
			}
		}
		if (strategy == "prefix" || strategy == "substring") && !partialMatchSafe(h.OldLines, actual, patchNewLines) {
			return "", nil, fmt.Errorf("Refusing partial-line match in %s at line %d: the replacement would silently drop text. Provide the complete line in the hunk", path, start+1)
		}
		newLines := adjustPatchIndentation(patchOldLines, actual, patchNewLines)
		replacements = append(replacements, replacement{start, len(actual), newLines})
		currentIndex = start + len(actual)
		if strategy == "fuzzy-dominant" {
			warnings = append(warnings, fmt.Sprintf("Dominant fuzzy match selected in %s near line %d (%.0f%% similar).", path, start+1, confidence*100))
		} else if strategy == "comment-prefix" || strategy == "prefix" || strategy == "substring" || strategy == "fuzzy" || strategy == "character" {
			warnings = append(warnings, fmt.Sprintf("Inexact match in %s near line %d: matched via %s strategy (%.0f%% similar). Re-read the file if the result is not what you intended.", path, start+1, strategy, confidence*100))
		}
	}
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].at < replacements[j].at })
	for i := 1; i < len(replacements); i++ {
		for j := 0; j < i; j++ {
			if replacements[i].at < replacements[j].at+replacements[j].old && replacements[j].at < replacements[i].at+replacements[i].old {
				return "", nil, fmt.Errorf("Overlapping hunks detected in %s", path)
			}
		}
	}
	for i := len(replacements) - 1; i >= 0; i-- {
		r := replacements[i]
		lines = append(append(append([]string{}, lines[:r.at]...), r.lines...), lines[r.at+r.old:]...)
	}
	out := strings.Join(lines, "\n")
	if hadFinal {
		out += "\n"
	}
	if out == content {
		return "", nil, fmt.Errorf("Edits to %s resulted in no changes being made.", path)
	}
	return out, warnings, nil
}

func findPatchHunk(lines []string, h PatchHunk, currentIndex int, allowFuzzy bool, threshold float64) (int, []string, float64, string, int, error) {
	if len(h.OldLines) == 0 {
		at := len(lines)
		if h.Anchor != "" {
			a, _, err := findAnchor(lines, h.Anchor, currentIndex)
			if err != nil {
				return 0, nil, 0, "", 0, err
			}
			at = a + 1
		} else if h.HasLineHint {
			if h.OldStartLine < 1 || h.OldStartLine > len(lines)+1 {
				return 0, nil, 0, "", 0, fmt.Errorf("Line hint %d is out of range for insertion", h.OldStartLine)
			}
			at = h.OldStartLine - 1
		}
		return at, nil, 1, "exact", 1, nil
	}
	searchStart := currentIndex
	if h.Anchor != "" {
		a, _, err := findAnchor(lines, h.Anchor, currentIndex)
		if err != nil {
			if c := sequenceIndicesNormalized(lines, h.OldLines, 0, "exact"); len(c) == 1 {
				searchStart = c[0]
			} else {
				return 0, nil, 0, "", 0, err
			}
		} else {
			searchStart = a
			if strings.TrimSpace(h.OldLines[0]) != lastAnchor(h.Anchor) {
				searchStart++
			}
		}
	}
	for _, strategy := range []string{"exact", "trim-trailing", "trim", "comment-prefix", "unicode", "prefix", "substring"} {
		if !allowFuzzy && (strategy == "prefix" || strategy == "substring") {
			continue
		}
		candidates := sequenceIndicesNormalized(lines, h.OldLines, searchStart, strategy)
		if len(candidates) == 0 && searchStart > 0 {
			candidates = sequenceIndicesNormalized(lines, h.OldLines, 0, strategy)
		}
		if h.EndOfFile && len(candidates) > 0 {
			candidates = []int{candidates[len(candidates)-1]}
		}
		if h.HasLineHint && len(candidates) > 1 {
			hint, selected := h.OldStartLine-1, []int{}
			for _, c := range candidates {
				if c == hint {
					selected = []int{c}
					break
				}
			}
			if len(selected) == 0 {
				for _, c := range candidates {
					if abs(c-hint) <= 200 {
						selected = append(selected, c)
					}
				}
			}
			if len(selected) == 1 {
				candidates = selected
			}
		}
		if len(candidates) == 1 {
			i := candidates[0]
			return i, lines[i : i+len(h.OldLines)], strategyConfidence(strategy), strategy, 1, nil
		}
		if len(candidates) > 1 {
			return 0, nil, 0, "", len(candidates), patchAmbiguityError(lines, candidates, len(candidates), "text", strategy)
		}
	}
	for _, variant := range patchFallbackVariants(h, h.Anchor != "" || h.HasLineHint || h.EndOfFile) {
		for _, strategy := range []string{"exact", "trim-trailing", "trim", "comment-prefix", "unicode", "prefix", "substring"} {
			if !allowFuzzy && (strategy == "prefix" || strategy == "substring") {
				continue
			}
			candidates := sequenceIndicesNormalized(lines, variant.OldLines, searchStart, strategy)
			if len(candidates) == 0 && searchStart > 0 {
				candidates = sequenceIndicesNormalized(lines, variant.OldLines, 0, strategy)
			}
			if len(candidates) == 1 {
				i := candidates[0]
				return i, lines[i : i+len(variant.OldLines)], strategyConfidence(strategy), strategy, 1, nil
			}
		}
	}
	if allowFuzzy {
		if i, score, count, indices := patchFuzzySequence(lines, h.OldLines, searchStart); i >= 0 {
			strategy := "fuzzy"
			if count > 1 && score >= .97 && patchSecondScore(lines, h.OldLines, searchStart, i) <= score-.08 {
				strategy, count = "fuzzy-dominant", 1
			}
			if count == 1 {
				return i, lines[i : i+len(h.OldLines)], score, strategy, count, nil
			}
			return 0, nil, 0, "", count, patchAmbiguityError(lines, indices, count, "text", strategy)
		}
	}
	i, score := closestPatchSequence(lines, h.OldLines, searchStart)
	if i >= 0 && score > 0 {
		return 0, nil, 0, "", 0, fmt.Errorf("Failed to find expected lines:\n%s\n\nClosest match (%.0f%% similar) near line %d:\n%s", strings.Join(h.OldLines, "\n"), score*100, i+1, patchPreview(lines, i))
	}
	return 0, nil, 0, "", 0, fmt.Errorf("Failed to find expected lines:\n%s", strings.Join(h.OldLines, "\n"))
}

func lastAnchor(anchor string) string {
	p := strings.Split(anchor, "\n")
	return strings.TrimSpace(p[len(p)-1])
}
func findAnchor(lines []string, anchor string, start int) (int, string, error) {
	parts := strings.Split(anchor, "\n")
	at := start
	if len(parts) == 1 && !strings.ContainsAny(anchor, "(){}[]") {
		words := strings.Fields(anchor)
		if len(words) > 2 {
			parts = []string{strings.Join(words[:len(words)-1], " "), words[len(words)-1]}
		}
	}
	for _, p := range parts {
		matches := lineMatches(lines, strings.TrimSpace(p), at)
		if len(matches) == 0 && at > 0 {
			matches = lineMatches(lines, strings.TrimSpace(p), 0)
		}
		if len(matches) > 1 {
			return 0, "", fmt.Errorf("Found %d matches for context '%s'", len(matches), p)
		}
		if len(matches) == 0 {
			return 0, "", fmt.Errorf("Failed to find context '%s'", strings.Join(parts, " > "))
		}
		at = matches[0] + 1
	}
	return at - 1, "substring", nil
}
func lineMatches(lines []string, pattern string, start int) []int {
	var out []int
	p := normalizeFuzzy(pattern)
	for i := start; i < len(lines); i++ {
		l := normalizeFuzzy(lines[i])
		if l == p || (len(p) >= 6 && strings.Contains(l, p)) {
			out = append(out, i)
		}
	}
	return out
}
func strategyConfidence(s string) float64 {
	switch s {
	case "exact":
		return 1
	case "trim-trailing":
		return .99
	case "trim":
		return .98
	case "comment-prefix":
		return .975
	case "unicode":
		return .97
	case "prefix":
		return .965
	default:
		return .94
	}
}
func stripPatchComment(s string) string {
	s = strings.TrimLeft(s, " \t")
	for _, p := range []string{"/*", "*/", "//", "*", "#", ";"} {
		if strings.HasPrefix(s, p) {
			return strings.TrimLeft(strings.TrimPrefix(s, p), " \t")
		}
	}
	if strings.HasPrefix(s, "/ ") {
		return strings.TrimLeft(s[1:], " \t")
	}
	return s
}
func sequenceIndicesNormalized(lines, pattern []string, start int, strategy string) []int {
	var out []int
	for i := start; i+len(pattern) <= len(lines); i++ {
		ok := true
		for j, p := range pattern {
			l := lines[i+j]
			switch strategy {
			case "exact":
				ok = l == p
			case "trim-trailing":
				ok = strings.TrimRight(l, " \t") == strings.TrimRight(p, " \t")
			case "trim":
				ok = strings.TrimSpace(l) == strings.TrimSpace(p)
			case "comment-prefix":
				ok = stripPatchComment(l) == stripPatchComment(p)
			case "unicode":
				ok = normalizeFuzzy(l) == normalizeFuzzy(p)
			case "prefix":
				q, n := normalizeFuzzy(p), normalizeFuzzy(l)
				ok = strings.HasPrefix(n, q)
			case "substring":
				q, n := normalizeFuzzy(p), normalizeFuzzy(l)
				ok = len(q) >= 6 && len(q)*10 >= len(n)*3 && strings.Contains(n, q)
			}
			if !ok {
				break
			}
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

func patchScoreAt(lines, pattern []string, i int) float64 {
	if len(pattern) == 0 {
		return 1
	}
	total := 0.0
	for j, p := range pattern {
		total += similarity(normalizeFuzzy(lines[i+j]), normalizeFuzzy(p))
	}
	return total / float64(len(pattern))
}
func patchFuzzySequence(lines, pattern []string, start int) (int, float64, int, []int) {
	best, score, count := -1, 0.0, 0
	indices := []int{}
	for i := start; i+len(pattern) <= len(lines); i++ {
		s := patchScoreAt(lines, pattern, i)
		if s >= .92 {
			count++
			if len(indices) < 5 {
				indices = append(indices, i)
			}
		}
		if s > score {
			best, score = i, s
		}
	}
	if score < .92 {
		return -1, score, count, indices
	}
	return best, score, count, indices
}
func patchSecondScore(lines, pattern []string, start, skip int) float64 {
	second := 0.0
	for i := start; i+len(pattern) <= len(lines); i++ {
		if i != skip {
			second = max(second, patchScoreAt(lines, pattern, i))
		}
	}
	return second
}
func closestPatchSequence(lines, pattern []string, start int) (int, float64) {
	best, score := -1, 0.0
	for i := start; i+len(pattern) <= len(lines); i++ {
		s := patchScoreAt(lines, pattern, i)
		if s > score {
			best, score = i, s
		}
	}
	return best, score
}
func patchPreview(lines []string, center int) string {
	a, b := max(0, center-5), min(len(lines), center+6)
	out := []string{}
	for i := a; i < b; i++ {
		l := lines[i]
		r := []rune(l)
		if len(r) > 80 {
			l = string(r[:79]) + "…"
		}
		out = append(out, fmt.Sprintf("  %d | %s", i+1, l))
	}
	return strings.Join(out, "\n")
}
func patchAmbiguityError(lines []string, indices []int, count int, subject, strategy string) error {
	previews := []string{}
	for _, i := range indices {
		previews = append(previews, patchPreview(lines, i))
	}
	more := ""
	if count > len(indices) {
		more = fmt.Sprintf(" (showing first %d of %d)", len(indices), count)
	}
	return fmt.Errorf("Found %d matches for the %s. Matching strategy: %s.\n\n%s%s\n\nAdd more surrounding context or additional @@ anchors to make it unique.", count, subject, strategy, strings.Join(previews, "\n\n"), more)
}

type patchVariant struct{ OldLines, NewLines []string }

func patchFallbackVariants(h PatchHunk, aggressive bool) []patchVariant {
	baseOld, baseNew := h.OldLines, h.NewLines
	start, endOld, endNew := 0, len(baseOld), len(baseNew)
	for start < endOld && start < endNew && baseOld[start] == baseNew[start] {
		start++
	}
	for endOld > start && endNew > start && baseOld[endOld-1] == baseNew[endNew-1] {
		endOld--
		endNew--
	}
	oldLines, newLines := baseOld, baseNew
	out := []patchVariant{}
	if (start > 0 || endOld < len(baseOld) || endNew < len(baseNew)) && (endOld > start || endNew > start) {
		oldLines, newLines = baseOld[start:endOld], baseNew[start:endNew]
		out = append(out, patchVariant{oldLines, newLines})
	}
	shared, ns := map[string]bool{}, map[string]bool{}
	for _, l := range newLines {
		ns[l] = true
	}
	for _, l := range oldLines {
		if ns[l] {
			shared[l] = true
		}
	}
	collapseRuns := func(in []string) []string {
		r := []string{}
		for i, l := range in {
			if i == 0 || l != in[i-1] || !shared[l] {
				r = append(r, l)
			}
		}
		return r
	}
	do, dn := collapseRuns(oldLines), collapseRuns(newLines)
	if len(do) != len(oldLines) || len(dn) != len(newLines) {
		out = append(out, patchVariant{do, dn})
		oldLines, newLines = do, dn
	}
	collapseBlocks := func(in []string) []string {
		r := append([]string{}, in...)
		for i := 0; i < len(r); {
			changed := false
			for size := (len(r) - i) / 2; size >= 2; size-- {
				same := true
				for j := 0; j < size; j++ {
					if r[i+j] != r[i+size+j] || !shared[r[i+j]] {
						same = false
						break
					}
				}
				if same {
					r = append(r[:i+size], r[i+2*size:]...)
					changed = true
					break
				}
			}
			if !changed {
				i++
			}
		}
		return r
	}
	co, cn := collapseBlocks(oldLines), collapseBlocks(newLines)
	if aggressive && (len(co) != len(oldLines) || len(cn) != len(newLines)) {
		out = append(out, patchVariant{co, cn})
	}
	if aggressive && len(oldLines) == len(newLines) {
		changed := -1
		for i := range oldLines {
			if oldLines[i] != newLines[i] {
				if changed >= 0 {
					changed = -2
					break
				}
				changed = i
			}
		}
		if changed >= 0 {
			out = append(out, patchVariant{[]string{oldLines[changed]}, []string{newLines[changed]}})
		}
	}
	return out
}

func partialMatchSafe(pattern, actual, replacement []string) bool {
	all := normalizeFuzzy(strings.Join(replacement, "\n"))
	for i, p := range pattern {
		a, q := normalizeFuzzy(actual[i]), normalizeFuzzy(p)
		if a == q {
			continue
		}
		at := strings.Index(a, q)
		if at < 0 {
			continue
		}
		for _, part := range []string{strings.TrimSpace(a[:at]), strings.TrimSpace(a[at+len(q):])} {
			if part != "" && !strings.Contains(all, part) {
				return false
			}
		}
	}
	return true
}

func sequenceIndices(lines, pattern []string) []int {
	var out []int
	for i := 0; i+len(pattern) <= len(lines); i++ {
		ok := true
		for j := range pattern {
			if lines[i+j] != pattern[j] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}
func anchorIndices(lines []string, anchor string) []int {
	var out []int
	for i, l := range lines {
		if l == anchor || strings.Contains(l, anchor) {
			out = append(out, i)
		}
	}
	return out
}
func constrainByAnchor(lines []string, candidates []int, anchor string) []int {
	anchors := anchorIndices(lines, anchor)
	if len(anchors) != 1 {
		return candidates
	}
	a := anchors[0]
	var out []int
	for _, i := range candidates {
		if i >= a {
			out = append(out, i)
		}
	}
	return out
}
func ambiguity(n int) error {
	if n > 1 {
		return fmt.Errorf("Found %d matches for the text", n)
	}
	return fmt.Errorf("No match found for patch context")
}
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
func adjustPatchIndentation(pattern, actual, replacement []string) []string {
	if equalStringSlices(pattern, actual) || len(pattern) == 0 || len(actual) == 0 || len(replacement) == 0 {
		return replacement
	}
	trimEqual := len(pattern) == len(replacement)
	if trimEqual {
		for i := range pattern {
			if strings.TrimSpace(pattern[i]) != strings.TrimSpace(replacement[i]) {
				trimEqual = false
				break
			}
		}
	}
	if trimEqual {
		return replacement
	}
	class := func(lines []string) (bool, bool, bool) {
		tabs, spaces, mixed := true, true, false
		for _, l := range lines {
			ws := l[:leading(l)]
			if strings.Contains(ws, " ") {
				tabs = false
			}
			if strings.Contains(ws, "\t") {
				spaces = false
			}
			if strings.Contains(ws, " ") && strings.Contains(ws, "\t") {
				mixed = true
			}
		}
		return tabs, spaces, mixed
	}
	pt, ps, pm := class(pattern)
	at, as, am := class(actual)
	_, _, nm := class(replacement)
	if pm || am || nm {
		return replacement
	}
	if pt && as {
		ratio, ok := 0, true
		for i := range min(len(pattern), len(actual)) {
			p, a := leading(pattern[i]), leading(actual[i])
			if p == 0 || strings.TrimSpace(pattern[i]) == "" {
				continue
			}
			if a%p != 0 || (ratio != 0 && ratio != a/p) {
				ok = false
				break
			}
			ratio = a / p
		}
		if ok && ratio > 0 {
			out := append([]string{}, replacement...)
			for i, l := range out {
				n := leading(l)
				out[i] = strings.Repeat(" ", n*ratio) + strings.TrimLeft(l, " \t")
			}
			return out
		}
	}
	if ps && at {
		samples := map[int]int{}
		ok := true
		for i := range min(len(pattern), len(actual)) {
			s, t := leading(pattern[i]), leading(actual[i])
			if t == 0 {
				continue
			}
			if v, x := samples[t]; x && v != s {
				ok = false
			}
			samples[t] = s
		}
		if ok && len(samples) > 0 {
			w, b := 0, 0
			entries := [][2]int{}
			for t, s := range samples {
				entries = append(entries, [2]int{t, s})
			}
			if len(entries) == 1 && entries[0][1]%entries[0][0] == 0 {
				w = entries[0][1] / entries[0][0]
			} else if len(entries) > 1 {
				d := entries[1][0] - entries[0][0]
				if d != 0 && (entries[1][1]-entries[0][1])%d == 0 {
					w = (entries[1][1] - entries[0][1]) / d
					b = entries[0][1] - entries[0][0]*w
				}
			}
			if w > 0 {
				out := append([]string{}, replacement...)
				for i, l := range out {
					n := leading(l)
					adj := n - b
					if adj >= 0 {
						out[i] = strings.Repeat("\t", adj/w) + strings.Repeat(" ", adj%w) + strings.TrimLeft(l, " \t")
					}
				}
				return out
			}
		}
	}
	delta, set := 0, false
	for i := range min(len(pattern), len(actual)) {
		if strings.TrimSpace(pattern[i]) == "" || strings.TrimSpace(actual[i]) == "" {
			continue
		}
		d := leading(actual[i]) - leading(pattern[i])
		if set && d != delta {
			return replacement
		}
		delta, set = d, true
	}
	if !set || delta == 0 {
		return replacement
	}
	out := append([]string{}, replacement...)
	ch := " "
	for _, l := range actual {
		if strings.HasPrefix(l, "\t") {
			ch = "\t"
			break
		}
	}
	for i, l := range out {
		if strings.TrimSpace(l) != "" {
			n := leading(l) + delta
			if n < 0 {
				return replacement
			}
			out[i] = strings.Repeat(ch, n) + strings.TrimLeft(l, " \t")
		}
	}
	return out
}
