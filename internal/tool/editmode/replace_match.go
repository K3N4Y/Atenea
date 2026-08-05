package editmode

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

const (
	DefaultFuzzyThreshold = .95
	fallbackThreshold     = .80
	dominantMinConfidence = .97
	dominantDelta         = .08
	maxRecordedMatches    = 5
)

type FuzzyMatch struct {
	ActualText            string
	StartIndex, StartLine int
	Confidence            float64
}
type MatchOutcome struct {
	Match, Closest     *FuzzyMatch
	Occurrences        int
	OccurrencePreviews []string
	FuzzyMatches       int
}

func replaceAllFuzzy(content, oldText, newText string, allow bool, threshold float64) (string, int) {
	type repl struct {
		start, end int
		text       string
	}
	var rs []repl
	for {
		ranges := make([][2]int, len(rs))
		for i, r := range rs {
			ranges[i] = [2]int{r.start, r.end}
		}
		o := FindMatch(content, oldText, allow, threshold, ranges)
		m := o.Match
		if m == nil && allow && o.Closest != nil && o.Closest.Confidence >= threshold && o.FuzzyMatches <= 1 {
			m = o.Closest
		}
		if m == nil {
			break
		}
		text := adjustIndentation(oldText, m.ActualText, newText)
		if text == m.ActualText {
			break
		}
		rs = append(rs, repl{m.StartIndex, m.StartIndex + max(len(m.ActualText), 1), text})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].start < rs[j].start })
	var b strings.Builder
	pos := 0
	for _, r := range rs {
		b.WriteString(content[pos:r.start])
		b.WriteString(r.text)
		pos = r.end
	}
	b.WriteString(content[pos:])
	return b.String(), len(rs)
}

// FindMatch is the pure replace matcher used by execution and streaming previews.
func FindMatch(content, target string, allow bool, threshold float64, excluded [][2]int) MatchOutcome {
	if target == "" {
		return MatchOutcome{}
	}
	var indices []int
	occurrences := 0
	for start := 0; start <= len(content)-len(target); {
		i := strings.Index(content[start:], target)
		if i < 0 {
			break
		}
		i += start
		end := i + len(target)
		if !overlaps(i, end, excluded) {
			occurrences++
			if len(indices) < maxRecordedMatches {
				indices = append(indices, i)
			}
		}
		start = end
	}
	if occurrences == 1 {
		i := indices[0]
		return MatchOutcome{Match: &FuzzyMatch{target, i, lineAt(content, i), 1}}
	}
	if occurrences > 1 {
		lines := strings.Split(content, "\n")
		ps := make([]string, 0, len(indices))
		for _, i := range indices {
			ps = append(ps, preview(lines, lineAt(content, i)-1))
		}
		return MatchOutcome{Occurrences: occurrences, OccurrencePreviews: ps}
	}
	best, above, second := bestFuzzy(content, target, threshold, true, excluded)
	if best != nil && best.Confidence < threshold && best.Confidence >= fallbackThreshold {
		if alt, n, s := bestFuzzy(content, target, threshold, false, excluded); alt != nil && alt.Confidence > best.Confidence {
			best, above, second = alt, n, s
		}
	}
	o := MatchOutcome{Closest: best, FuzzyMatches: above}
	if allow && best != nil && best.Confidence >= threshold && (above == 1 || (above > 1 && best.Confidence >= dominantMinConfidence && best.Confidence-second >= dominantDelta)) {
		o.Match = best
	}
	return o
}
func bestFuzzy(content, target string, threshold float64, depth bool, excluded [][2]int) (*FuzzyMatch, int, float64) {
	lines, targets := strings.Split(content, "\n"), strings.Split(target, "\n")
	if target == "" || len(targets) > len(lines) {
		return nil, 0, 0
	}
	offs := make([]int, len(lines))
	for i := 1; i < len(lines); i++ {
		offs[i] = offs[i-1] + len(lines[i-1]) + 1
	}
	tn := normalizeLines(targets, depth)
	var best *FuzzyMatch
	bs, second := -1.0, -1.0
	above := 0
	for start := 0; start+len(targets) <= len(lines); start++ {
		last := start + len(targets) - 1
		begin, end := offs[start], max(offs[start]+1, offs[last]+len(lines[last]))
		if overlaps(begin, end, excluded) {
			continue
		}
		window := lines[start : start+len(targets)]
		wn := normalizeLines(window, depth)
		score := 0.0
		for i := range wn {
			score += similarity(wn[i], tn[i])
		}
		score /= float64(len(wn))
		if score >= threshold {
			above++
		}
		if score > bs {
			second = bs
			bs = score
			best = &FuzzyMatch{strings.Join(window, "\n"), begin, start + 1, score}
		} else if score > second {
			second = score
		}
	}
	return best, above, second
}
func normalizeLines(lines []string, depth bool) []string {
	ind := make([]int, len(lines))
	minimum := int(^uint(0) >> 1)
	for i, l := range lines {
		ind[i] = leading(l)
		if strings.TrimSpace(l) != "" && ind[i] < minimum {
			minimum = ind[i]
		}
	}
	if minimum == int(^uint(0)>>1) {
		minimum = 0
	}
	unit := int(^uint(0) >> 1)
	for i, l := range lines {
		d := ind[i] - minimum
		if strings.TrimSpace(l) != "" && d > 0 && d < unit {
			unit = d
		}
	}
	if unit == int(^uint(0)>>1) {
		unit = 1
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		d := 0
		if depth && strings.TrimSpace(l) != "" {
			d = (ind[i] - minimum + unit/2) / unit
		}
		out[i] = fmt.Sprintf("%d|%s", d, normalizeFuzzy(l))
	}
	return out
}
func normalizeFuzzy(s string) string {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("“", "\"", "”", "\"", "„", "\"", "‟", "\"", "«", "\"", "»", "\"", "‘", "'", "’", "'", "‚", "'", "‛", "'", "`", "'", "´", "'", "‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "−", "-").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
func similarity(a, b string) float64 {
	aa, bb := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	if len(aa) == 0 && len(bb) == 0 {
		return 1
	}
	prev := make([]int, len(bb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(aa); i++ {
		cur := make([]int, len(bb)+1)
		cur[0] = i
		for j := 1; j <= len(bb); j++ {
			c := 0
			if aa[i-1] != bb[j-1] {
				c = 1
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return 1 - float64(prev[len(bb)])/float64(max(len(aa), len(bb)))
}
func overlaps(a, b int, rs [][2]int) bool {
	for _, r := range rs {
		if a < r[1] && b > r[0] {
			return true
		}
	}
	return false
}
func lineAt(s string, i int) int { return strings.Count(s[:i], "\n") + 1 }
func leading(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' && r != '\t' {
			break
		}
		n++
	}
	return n
}
func preview(lines []string, c int) string {
	a, b := max(0, c-5), min(len(lines), c+6)
	out := make([]string, 0, b-a)
	for i := a; i < b; i++ {
		l := lines[i]
		rr := []rune(l)
		if len(rr) > 80 {
			l = string(rr[:79]) + "…"
		}
		out = append(out, fmt.Sprintf("  %d | %s", i+1, l))
	}
	return strings.Join(out, "\n")
}
func formatOccurrences(path string, o MatchOutcome) string {
	more := ""
	if o.Occurrences > maxRecordedMatches {
		more = fmt.Sprintf(" (showing first %d of %d)", maxRecordedMatches, o.Occurrences)
	}
	return fmt.Sprintf("Found %d occurrences in %s%s:\n\n%s\n\nAdd more context lines to disambiguate.", o.Occurrences, path, more, strings.Join(o.OccurrencePreviews, "\n\n"))
}
func matchError(path, target string, o MatchOutcome, allow bool, threshold float64) error {
	if o.Closest == nil {
		if allow {
			return fmt.Errorf("Could not find a close enough match in %s.", path)
		}
		return fmt.Errorf("Could not find the exact text in %s. The old text must match exactly including all whitespace and newlines.", path)
	}
	old, newLine := firstDifferent(strings.Split(target, "\n"), strings.Split(o.Closest.ActualText, "\n"))
	hint := "Fuzzy matching is disabled. Enable 'Edit fuzzy match' in settings to accept high-confidence matches."
	prefix := "Could not find the exact text"
	if allow {
		prefix = "Could not find a close enough match"
		if o.FuzzyMatches > 1 {
			hint = fmt.Sprintf("Found %d high-confidence matches. Provide more context to make it unique.", o.FuzzyMatches)
		} else {
			hint = fmt.Sprintf("Closest match was below the %.0f%% similarity threshold.", threshold*100)
		}
	}
	return fmt.Errorf("%s in %s.\n\nClosest match (%.0f%% similar) at line %d:\n  - %s\n  + %s\n%s", prefix, path, o.Closest.Confidence*100, o.Closest.StartLine, old, newLine, hint)
}
func firstDifferent(a, b []string) (string, string) {
	for i := 0; i < max(len(a), len(b)); i++ {
		x, y := "", ""
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x, y
		}
	}
	if len(a) > 0 {
		return a[0], a[0]
	}
	return "", ""
}
func adjustIndentation(old, actual, newText string) string {
	if old == actual {
		return newText
	}
	ol, al := strings.Split(old, "\n"), strings.Split(actual, "\n")
	set := false
	delta := 0
	for i := 0; i < min(len(ol), len(al)); i++ {
		if strings.TrimSpace(ol[i]) == "" || strings.TrimSpace(al[i]) == "" {
			continue
		}
		d := leading(al[i]) - leading(ol[i])
		if set && d != delta {
			return newText
		}
		delta, set = d, true
	}
	if !set || delta == 0 {
		return newText
	}
	nl := strings.Split(newText, "\n")
	for i, l := range nl {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := leading(l) + delta
		if n < 0 {
			return newText
		}
		nl[i] = strings.Repeat(indentChar(actual), n) + strings.TrimLeft(l, " \t")
	}
	return strings.Join(nl, "\n")
}
func indentChar(s string) string {
	for _, l := range strings.Split(s, "\n") {
		for _, r := range l {
			if r == '\t' {
				return "\t"
			}
			if r == ' ' {
				return " "
			}
			break
		}
	}
	return " "
}
