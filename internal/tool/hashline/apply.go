package hashline

import (
	"errors"
	"fmt"
	"strings"
)

func NewClipboard() *Clipboard { return &Clipboard{Named: map[string][]string{}} }
func (c *Clipboard) Clone() *Clipboard {
	n := NewClipboard()
	if c == nil {
		return n
	}
	n.Anonymous = append([]string(nil), c.Anonymous...)
	n.PendingAnonCuts = append([]string(nil), c.PendingAnonCuts...)
	for k, v := range c.Named {
		n.Named[k] = append([]string(nil), v...)
	}
	return n
}
func (c *Clipboard) Replace(other *Clipboard) {
	n := other.Clone()
	c.Anonymous, c.Named, c.PendingAnonCuts = n.Anonymous, n.Named, n.PendingAnonCuts
}

func ApplyEdits(lines []string, edits []Edit) (ApplyResult, error) {
	return ApplyEditsWithClipboard(lines, edits, NewClipboard())
}

// ApplyEditsWithClipboard applies all locations in original-file coordinates.
// Validation and clipboard work happen on a fork, so failures publish nothing.
func ApplyEditsWithClipboard(lines []string, edits []Edit, cb *Clipboard) (ApplyResult, error) {
	if cb == nil {
		cb = NewClipboard()
	}
	work := cb.Clone()
	for _, e := range edits {
		if e.Block || e.AfterBlock {
			return ApplyResult{}, errors.New("hashline: unresolved block edit reached applier")
		}
		if e.Range.Start > 0 && (e.Range.Start < 1 || e.Range.End < e.Range.Start || e.Range.End > len(lines)) {
			return ApplyResult{}, fmt.Errorf("hashline: line range %d-%d is outside file (%d lines)", e.Range.Start, e.Range.End, len(lines))
		}
		if (e.Kind == Insert || e.Kind == Paste && e.Range.Start == 0) && (e.Anchor < 0 || e.Anchor > len(lines) || (e.Anchor == 0 && (e.Cursor == BeforeAnchor || e.Cursor == AfterAnchor))) {
			return ApplyResult{}, fmt.Errorf("hashline: line anchor %d is outside file", e.Anchor)
		}
	}
	concrete := make([]resolved, 0, len(edits))
	anonPending := append([]string(nil), work.PendingAnonCuts...)
	for _, e := range edits {
		switch e.Kind {
		case Cut:
			v := append([]string(nil), lines[e.Range.Start-1:e.Range.End]...)
			if e.Register == "" {
				work.Anonymous = v
				anonPending = append(anonPending, fmt.Sprintf("CUT %d-%d", e.Range.Start, e.Range.End))
				work.PendingAnonCuts = anonPending
			} else {
				work.Named[e.Register] = v
			}
			concrete = append(concrete, resolved{edit: Edit{Kind: Delete, Range: e.Range}})
		case Paste:
			var v []string
			if e.Register != "" {
				var ok bool
				v, ok = work.Named[e.Register]
				if !ok {
					return ApplyResult{}, fmt.Errorf("hashline: register @%s is empty", e.Register)
				}
			} else {
				if len(anonPending) > 1 {
					return ApplyResult{}, fmt.Errorf("hashline: anonymous paste is ambiguous: %d unlabeled CUTs are pending", len(anonPending))
				}
				if work.Anonymous == nil {
					return ApplyResult{}, errors.New("hashline: anonymous register is empty; nothing to paste")
				}
				v = work.Anonymous
				anonPending = nil
				work.PendingAnonCuts = nil
			}
			concrete = append(concrete, resolved{edit: e, text: append([]string(nil), v...)})
		case Replace, Insert:
			concrete = append(concrete, resolved{edit: e, text: strings.Split(e.Text, "\n")})
		case Delete:
			concrete = append(concrete, resolved{edit: e})
		}
	}
	// Newline-terminated text has a trailing empty sentinel. Deleting it is a
	// no-op; ranges ending there stop at the last real line.
	for i := range concrete {
		e := &concrete[i].edit
		if len(lines) > 0 && lines[len(lines)-1] == "" && e.Range.End == len(lines) && (e.Kind == Delete || e.Kind == Replace || e.Kind == Paste) {
			e.Range.End--
			if e.Range.Start > e.Range.End {
				e.Range = Range{}
			}
		}
	}
	warnings := []string{}
	for i := range concrete {
		if concrete[i].edit.Kind == Replace {
			if err := repairReplacement(&concrete[i], lines, &warnings); err != nil {
				return ApplyResult{}, err
			}
		}
	}
	repairMissingClosers(concrete, lines, &warnings)
	repairLandings(concrete, lines, &warnings)
	inserts := map[int][][]string{}
	deleted := map[int]bool{}
	repls := map[int][]string{}
	first := len(lines) + 1
	for _, r := range concrete {
		e := r.edit
		switch e.Kind {
		case Delete:
			for n := e.Range.Start; n <= e.Range.End; n++ {
				if n > 0 {
					deleted[n] = true
				}
			}
			if e.Range.Start > 0 && e.Range.Start < first {
				first = e.Range.Start
			}
		case Replace, Paste:
			if e.Range.Start > 0 {
				for n := e.Range.Start; n <= e.Range.End; n++ {
					deleted[n] = true
				}
				repls[e.Range.Start] = append([]string(nil), r.text...)
				if e.Range.Start < first {
					first = e.Range.Start
				}
				break
			}
			fallthrough
		case Insert:
			pos := e.Anchor
			if e.Cursor == BeforeAnchor {
				pos--
			}
			if e.Cursor == BOF {
				pos = 0
			}
			if e.Cursor == EOF {
				pos = len(lines)
			}
			inserts[pos] = append(inserts[pos], append([]string(nil), r.text...))
			if pos+1 < first {
				first = pos + 1
			}
		}
	}
	out := flatten(inserts[0])
	for n := 1; n <= len(lines); n++ {
		if v, ok := repls[n]; ok {
			out = append(out, v...)
		} else if !deleted[n] {
			out = append(out, lines[n-1])
		}
		out = append(out, flatten(inserts[n])...)
	}
	text := strings.Join(out, "\n")
	if text == strings.Join(lines, "\n") {
		return ApplyResult{}, errors.New("hashline edit makes no changes")
	}
	cb.Replace(work)
	return ApplyResult{Text: text, FirstChangedLine: first, Warnings: warnings}, nil
}

type resolved struct {
	edit Edit
	text []string
}

func repairReplacement(r *resolved, lines []string, warnings *[]string) error {
	e := r.edit
	if e.Range.Start < 1 || e.Range.End > len(lines) || len(r.text) == 0 {
		return nil
	}
	old := lines[e.Range.Start-1 : e.Range.End]
	// Restore an omitted common base indent only when unchanged rows prove a
	// uniform shift inside a surviving brace opener. Ordinary reindentation is literal.
	if e.Range.Start > 1 {
		preceding := lines[e.Range.Start-2]
		if strings.HasSuffix(strings.TrimSpace(preceding), "{") && indentDeeper(leadingWhitespace(old[0]), leadingWhitespace(preceding)) && !indentDeeper(leadingWhitespace(r.text[0]), leadingWhitespace(preceding)) {
			shift, matches, consistent := "", 0, true
			for i := range r.text {
				if i >= len(old) || strings.TrimSpace(old[i]) == "" || strings.TrimLeft(old[i], " \t") != strings.TrimLeft(r.text[i], " \t") {
					continue
				}
				a, b := leadingWhitespace(old[i]), leadingWhitespace(r.text[i])
				if !strings.HasSuffix(a, b) {
					consistent = false
					break
				}
				candidate := a[:len(a)-len(b)]
				if matches > 0 && candidate != shift {
					consistent = false
					break
				}
				shift, matches = candidate, matches+1
			}
			if consistent && shift != "" && matches >= 2 && matches*2 > len(r.text) {
				for i := range r.text {
					if strings.TrimSpace(r.text[i]) != "" {
						r.text[i] = shift + r.text[i]
					}
				}
				*warnings = append(*warnings, "Auto-indented a replacement body to match its structural context")
			}
		}
	}

	leading := duplicateLeading(r.text, lines[:e.Range.Start-1])
	trailing := duplicateTrailing(r.text, lines[e.Range.End:])
	if leading > 0 && trailing > 0 && leading+trailing < len(r.text) && !payloadOpensJSX(r.text[:len(r.text)-trailing], r.text[len(r.text)-trailing:]) {
		dropped := balanceRows(r.text[:leading]).add(balanceRows(r.text[len(r.text)-trailing:]))
		delta := balanceRows(r.text).sub(balanceRows(old))
		if dropped.zero() || dropped == delta {
			r.text = append([]string(nil), r.text[leading:len(r.text)-trailing]...)
			*warnings = append(*warnings, "Auto-repaired a replacement boundary echo: dropped leading and trailing payload lines already present outside the range")
			return nil
		}
	}
	delta := balanceRows(r.text).sub(balanceRows(old))
	if !delta.zero() {
		if n := exactBalancedSuffix(r.text, lines[e.Range.End:], delta); n > 0 {
			r.text = r.text[:len(r.text)-n]
			*warnings = append(*warnings, "Auto-repaired a delimiter-balance mismatch: dropped duplicated trailing payload line(s)")
			return nil
		}
		if n := exactBalancedPrefix(r.text, lines[:e.Range.Start-1], delta); n > 0 {
			r.text = r.text[n:]
			*warnings = append(*warnings, "Auto-repaired a delimiter-balance mismatch: dropped duplicated leading payload line(s)")
			return nil
		}
		return nil
	}
	// Balance-neutral, one-sided keeper echoes are safe for multi-line rewrites;
	// single-line expansions are limited to structural closing boundaries.
	if (leading > 0) != (trailing > 0) {
		n, side := leading, "leading"
		if trailing > 0 {
			n, side = trailing, "trailing"
		}
		eligible := len(old) > 1
		if !eligible && side == "trailing" {
			eligible = allStructuralClosers(r.text[len(r.text)-n:]) && !payloadOpensJSX(r.text[:len(r.text)-n], r.text[len(r.text)-n:])
		}
		if eligible && n < len(r.text) {
			if len(r.text) < len(old)+n {
				return fmt.Errorf("hashline: replacement %d-%d rejected: the body %s by restating surviving boundary line(s); widen the range or issue only its final content", e.Range.Start, e.Range.End, map[bool]string{true: "ends", false: "opens"}[side == "trailing"])
			}
			if side == "leading" {
				r.text = r.text[n:]
			} else {
				r.text = r.text[:len(r.text)-n]
			}
			*warnings = append(*warnings, "Auto-repaired a replacement boundary echo: dropped one-sided payload line(s) already present outside the range")
		}
	}
	return nil
}

func repairMissingClosers(edits []resolved, lines []string, warnings *[]string) {
	total := delimiterBalance{}
	for _, r := range edits {
		if r.edit.Kind == Replace {
			total = total.add(balanceRows(r.text).sub(balanceRows(lines[r.edit.Range.Start-1 : r.edit.Range.End])))
		} else if r.edit.Kind == Delete && r.edit.Range.Start > 0 {
			total = total.sub(balanceRows(lines[r.edit.Range.Start-1 : r.edit.Range.End]))
		} else if r.edit.Kind == Insert || r.edit.Kind == Paste {
			total = total.add(balanceRows(r.text))
		}
	}
	for i := range edits {
		r := &edits[i]
		if r.edit.Kind != Replace || r.edit.Range.End < r.edit.Range.Start || total.zero() {
			continue
		}
		end := r.edit.Range.End
		start := end
		for start >= r.edit.Range.Start && isStructuralCloserLine(lines[start-1]) {
			start--
		}
		start++
		if start > end {
			continue
		}
		suffix := lines[start-1 : end]
		restated := 0
		for restated < len(suffix) && restated < len(r.text) && r.text[len(r.text)-len(suffix)+restated] == suffix[restated] {
			// Only compare when the payload actually has the whole candidate tail.
			restated++
		}
		if len(r.text) < len(suffix) {
			restated = 0
		}
		keep := suffix[restated:]
		for len(keep) > 0 && end < len(lines) && lines[end] == keep[len(keep)-1] {
			keep = keep[:len(keep)-1]
		}
		if len(keep) == 0 {
			continue
		}
		need := balanceRows(keep).neg()
		if !total.covers(need) {
			continue
		}
		indent := bodyTargetIndent(r.text)
		payloadOpens := balanceRows(r.text).covers(need)
		if !payloadOpens && (indent == "" || !indentDeeper(indent, leadingWhitespace(keep[0]))) {
			continue // conservative: literal replacement remains unchanged when evidence is absent
		}
		r.text = append(r.text, keep...)
		total = total.add(balanceRows(keep))
		*warnings = append(*warnings, fmt.Sprintf("delimiter-balance: kept %d structural closing line(s)", len(keep)))
	}
}

type resolvedAlias = resolved

func leadingWhitespace(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }

type delimiterBalance struct{ paren, bracket, brace int }

func (a delimiterBalance) add(b delimiterBalance) delimiterBalance {
	return delimiterBalance{a.paren + b.paren, a.bracket + b.bracket, a.brace + b.brace}
}
func (a delimiterBalance) sub(b delimiterBalance) delimiterBalance {
	return delimiterBalance{a.paren - b.paren, a.bracket - b.bracket, a.brace - b.brace}
}
func (a delimiterBalance) neg() delimiterBalance {
	return delimiterBalance{-a.paren, -a.bracket, -a.brace}
}
func (a delimiterBalance) zero() bool { return a == delimiterBalance{} }
func (a delimiterBalance) covers(b delimiterBalance) bool {
	cover := func(x, y int) bool { return y == 0 || x != 0 && (x > 0) == (y > 0) && abs(x) >= abs(y) }
	return cover(a.paren, b.paren) && cover(a.bracket, b.bracket) && cover(a.brace, b.brace)
}
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
func balanceRows(rows []string) delimiterBalance {
	b := delimiterBalance{}
	block := false
	quote := byte(0)
	for _, s := range rows {
		for i := 0; i < len(s); i++ {
			ch := s[i]
			if block {
				if ch == '*' && i+1 < len(s) && s[i+1] == '/' {
					block = false
					i++
				}
				continue
			}
			if quote != 0 {
				if ch == '\\' {
					i++
				} else if ch == quote {
					quote = 0
				}
				continue
			}
			if ch == '\'' || ch == '"' || ch == '`' {
				quote = ch
				continue
			}
			if ch == '/' && i+1 < len(s) && s[i+1] == '/' {
				break
			}
			if ch == '/' && i+1 < len(s) && s[i+1] == '*' {
				block = true
				i++
				continue
			}
			switch ch {
			case '(':
				b.paren++
			case ')':
				b.paren--
			case '[':
				b.bracket++
			case ']':
				b.bracket--
			case '{':
				b.brace++
			case '}':
				b.brace--
			}
		}
		if quote == '\'' || quote == '"' {
			quote = 0
		}
	}
	return b
}
func duplicateLeading(p, a []string) int {
	m := min(len(p), len(a))
	for n := m; n > 0; n-- {
		if equalLines(p[:n], a[len(a)-n:]) && hasContent(p[:n]) {
			return n
		}
	}
	return 0
}
func duplicateTrailing(p, b []string) int {
	m := min(len(p), len(b))
	for n := m; n > 0; n-- {
		if equalLines(p[len(p)-n:], b[:n]) && hasContent(p[len(p)-n:]) {
			return n
		}
	}
	return 0
}
func equalLines(a, b []string) bool {
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
func hasContent(a []string) bool {
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}
func exactBalancedSuffix(p, b []string, d delimiterBalance) int {
	for n := min(len(p), len(b)); n > 0; n-- {
		if equalLines(p[len(p)-n:], b[:n]) && balanceRows(p[len(p)-n:]) == d {
			return n
		}
	}
	return 0
}
func exactBalancedPrefix(p, a []string, d delimiterBalance) int {
	for n := min(len(p), len(a)); n > 0; n-- {
		if equalLines(p[:n], a[len(a)-n:]) && balanceRows(p[:n]) == d {
			return n
		}
	}
	return 0
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func indentDeeper(d, s string) bool { return len(d) > len(s) && strings.HasPrefix(d, s) }
func bodyTargetIndent(rows []string) string {
	target := ""
	seen := false
	for _, s := range rows {
		if strings.TrimSpace(s) == "" || isStructuralCloserLine(s) {
			continue
		}
		ind := leadingWhitespace(s)
		if !seen {
			target = ind
			seen = true
		} else if strings.HasPrefix(ind, target) {
		} else if strings.HasPrefix(target, ind) {
			target = ind
		} else {
			return "\x00"
		}
	}
	return target
}
func allStructuralClosers(rows []string) bool {
	for _, s := range rows {
		if !isStructuralCloserLine(s) {
			return false
		}
	}
	return len(rows) > 0
}
func isStructuralCloserLine(s string) bool {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "</") || t == "/>" {
		return true
	}
	for len(t) > 0 && strings.ContainsRune(")]}", rune(t[0])) {
		t = t[1:]
	}
	return strings.Trim(strings.TrimSpace(t), ";,") == ""
}
func payloadOpensJSX(payload, closers []string) bool {
	joined := strings.Join(payload, "\n")
	for _, c := range closers {
		t := strings.TrimSpace(c)
		if strings.HasPrefix(t, "</") && t != "</>" {
			name := strings.TrimSuffix(strings.TrimPrefix(t, "</"), ">")
			if strings.Contains(joined, "<"+name+">") || strings.Contains(joined, "<"+name+" ") {
				return true
			}
		}
	}
	return false
}
func structuralDelta(rows []string) int { b := balanceRows(rows); return b.paren + b.bracket + b.brace }

func repairLandings(edits []resolvedAlias, lines []string, warnings *[]string) {
	targeted := map[int]bool{}
	for _, r := range edits {
		if r.edit.Range.Start > 0 {
			for n := r.edit.Range.Start; n <= r.edit.Range.End; n++ {
				targeted[n] = true
			}
		}
	}
	for i := range edits {
		e := &edits[i]
		if e.edit.Kind != Insert || e.edit.Cursor != AfterAnchor || len(e.text) == 0 || e.edit.Anchor < 1 || e.edit.Anchor > len(lines) {
			continue
		}
		body := bodyTargetIndent(e.text)
		if body == "\x00" || (body == "" && allStructuralClosers(e.text)) {
			continue
		}
		anchorIndent := leadingWhitespace(lines[e.edit.Anchor-1])
		if indentDeeper(anchorIndent, body) {
			landing, crossed := e.edit.Anchor, 0
			for n := landing + 1; n <= len(lines); n++ {
				t := strings.TrimSpace(lines[n-1])
				if t == "" {
					continue
				}
				if !isCloser(t) {
					break
				}
				ind := leadingWhitespace(lines[n-1])
				if !strings.HasPrefix(ind, body) || targeted[n] {
					landing = e.edit.Anchor
					crossed = 0
					break
				}
				landing = n
				crossed++
				if len(ind) == len(body) {
					break
				}
			}
			if landing != e.edit.Anchor {
				old := e.edit.Anchor
				e.edit.Anchor = landing
				*warnings = append(*warnings, fmt.Sprintf("PUT >%d: moved past %d closing line(s) to after line %d", old, crossed, landing))
			}
			continue
		}
		// Only block-lowered inserts may move inward; plain after-closer inserts
		// retain their literal contract.
		if e.edit.BlockStart == 0 || !isCloser(strings.TrimSpace(lines[e.edit.Anchor-1])) || !indentDeeper(body, anchorIndent) {
			continue
		}
		landing := e.edit.Anchor
		for n := e.edit.Anchor; n > e.edit.BlockStart; n-- {
			t := strings.TrimSpace(lines[n-1])
			if t == "" {
				landing = n - 1
				continue
			}
			if !isCloser(t) {
				break
			}
			ind := leadingWhitespace(lines[n-1])
			if !indentDeeper(body, ind) {
				break
			}
			if n != e.edit.Anchor && targeted[n] {
				landing = e.edit.Anchor
				break
			}
			landing = n - 1
		}
		if landing != e.edit.Anchor {
			old := e.edit.Anchor
			e.edit.Anchor = landing
			*warnings = append(*warnings, fmt.Sprintf("PUT >%d*: block ended at line %d; placed inside the block, after line %d", e.edit.BlockStart, old, landing))
		}
	}
}
func isCloser(s string) bool {
	return strings.HasPrefix(s, "}") || strings.HasPrefix(s, ")") || strings.HasPrefix(s, "]") || strings.HasPrefix(s, "</")
}
func flatten(v [][]string) []string {
	var out []string
	for _, x := range v {
		out = append(out, x...)
	}
	return out
}
