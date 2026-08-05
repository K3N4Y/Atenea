package hashline

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const maxLine = MaxExpandedLines

var numberedRow = regexp.MustCompile(`^([1-9][0-9]*):(.*)$`)
var bareRangeHeader = regexp.MustCompile(`^\s*([1-9][0-9]*)\s*(?:\.=|[-.=…]|\.\.|\s+)\s*([1-9][0-9]*)\s*:\s*$`)
var elisionRow = regexp.MustCompile(`(?i)^\s*(?:\[?…|[1-9][0-9]*[-.=…]+[1-9][0-9]*:.*….*)`)

// ParsePatchPartial parses the complete streaming prefix. Only an unfinished
// final body-bearing PUT is dropped; completed bodyless operations are kept.
func ParsePatchPartial(text string) (Patch, error) {
	lines := splitInputLines(text)
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if _, body, err := parseOperation(last); err == nil && body {
			lines = lines[:len(lines)-1]
		} else if partialOperationPrefix(last) {
			// Streaming can stop in the active operation token itself. It is not
			// part of the preceding PUT body because canonical body rows start
			// with '+'. Preserve every completed operation and drop only this
			// unambiguously incomplete final row.
			lines = lines[:len(lines)-1]
		}
	}
	if len(lines) == 0 {
		return Patch{}, nil
	}
	return ParsePatch(strings.Join(lines, "\n"))
}

func partialOperationPrefix(line string) bool {
	if line == "" || strings.HasPrefix(line, "+") {
		return false
	}
	for _, operation := range []string{"PUT", "CUT", "MV", "RM"} {
		if len(line) < len(operation) && strings.HasPrefix(operation, line) || line == operation {
			return true
		}
	}
	return false
}

func splitInputLines(text string) []string {
	text = strings.TrimPrefix(text, "\ufeff")
	if text == "" {
		return []string{}
	}
	rows := strings.Split(text, "\n")
	for i := range rows {
		rows[i] = strings.TrimSuffix(rows[i], "\r")
	}
	return rows
}

// ParsePatch parses the hashline language, including its deliberately safe
// repairs for common copied/model-authored forms.
func ParsePatch(text string) (Patch, error) {
	lines := splitInputLines(text)
	var patch Patch
	var cur *Section
	ended := false
	warn := func(s *Section, code, message string, line int) {
		w := ParseWarning{Code: code, Message: message, Line: line}
		patch.Warnings = append(patch.Warnings, w)
		if s != nil {
			s.Warnings = append(s.Warnings, w)
		}
	}
	for i := 0; i < len(lines); {
		lineNum := i + 1
		line, trimmed := lines[i], strings.TrimSpace(lines[i])
		if ended {
			if trimmed != "" {
				return Patch{}, fmt.Errorf("hashline: trailing content after patch end")
			}
			i++
			continue
		}
		if trimmed == "" || trimmed == "*** Begin Patch" {
			i++
			continue
		}
		if trimmed == "*** Abort" {
			ended = true
			i++
			continue
		}
		if trimmed == "*** End Patch" {
			ended = true
			i++
			continue
		}
		if isContamination(trimmed) {
			return Patch{}, contaminationError(trimmed)
		}
		if strings.HasPrefix(trimmed, "[") {
			s, err := parseHeader(trimmed)
			if err != nil {
				return Patch{}, err
			}
			idx := -1
			for j := range patch.Sections {
				if patch.Sections[j].Path == s.Path {
					idx = j
					break
				}
			}
			if idx >= 0 {
				if patch.Sections[idx].Hash != s.Hash && s.Hash != "" {
					return Patch{}, fmt.Errorf("hashline: repeated section %s has conflicting hashes", s.Path)
				}
				cur = &patch.Sections[idx]
			} else {
				patch.Sections = append(patch.Sections, s)
				cur = &patch.Sections[len(patch.Sections)-1]
			}
			i++
			continue
		}
		// Headerless operation text is valid for the parser core. The input facade
		// supplies path binding separately; represent it as one anonymous section.
		if cur == nil {
			if _, _, err := parseOperation(trimmed); err != nil && !bareRangeHeader.MatchString(trimmed) && !numberedRow.MatchString(trimmed) && !elisionRow.MatchString(trimmed) {
				return Patch{}, &MissingTagError{Detail: "input must begin with [PATH#HASH]; example [src/foo.ts#1A2B]"}
			}
			patch.Sections = append(patch.Sections, Section{})
			cur = &patch.Sections[0]
		}
		if elisionRow.MatchString(trimmed) {
			warn(cur, "read-elision", "Ignored copied read-output elision", lineNum)
			i++
			continue
		}
		if m := numberedRow.FindStringSubmatch(line); m != nil {
			n, err := lineNumber(m[1])
			if err != nil {
				return Patch{}, malformedOperation(line)
			}
			e := Edit{Kind: Replace, Range: Range{n, n}, Text: m[2]}
			if err := addEdit(cur, e, lineNum, warn); err != nil {
				return Patch{}, err
			}
			warn(cur, "snapshot-row", "Recovered copied snapshot row as single-line PUT N.=N:", lineNum)
			i++
			continue
		}
		if m := bareRangeHeader.FindStringSubmatch(line); m != nil {
			line = "PUT " + m[1] + ".=" + m[2] + ":"
			warn(cur, "bare-range", "Recovered bare N.=M: header as PUT", lineNum)
		}
		if trimmed == "REM" {
			if len(cur.Edits) != 0 || cur.FileOp.Remove || cur.FileOp.MoveTo != "" {
				return Patch{}, fmt.Errorf("hashline: REM is exclusive with line ops and other file ops")
			}
			cur.FileOp.Remove = true
			i++
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "MV ") {
			if cur.FileOp.Remove || cur.FileOp.MoveTo != "" {
				return Patch{}, fmt.Errorf("hashline: multiple file operations")
			}
			dest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "MV "))
			dest = strings.Trim(dest, `"'`)
			if dest == "" {
				return Patch{}, malformedOperation(line)
			}
			cur.FileOp.MoveTo = filepath.Clean(dest)
			i++
			continue
		}
		if cur.FileOp.Remove {
			return Patch{}, fmt.Errorf("hashline: REM is exclusive with line ops")
		}
		e, body, err := parseOperation(strings.TrimSpace(line))
		if err != nil {
			return Patch{}, err
		}
		i++
		if !body {
			if strings.HasSuffix(strings.TrimSpace(line), ":") && e.Kind == Cut {
				warn(cur, "cut-colon", "Ignored a trailing : on bodyless CUT", lineNum)
			}
			if i < len(lines) && strings.HasPrefix(lines[i], "+") {
				return Patch{}, fmt.Errorf("line %d: bodyless CUT/PUT takes no body rows", i+1)
			}
		} else {
			var payload []string
			var bareFlags, numberedFlags []bool
			bullet := false
			for i < len(lines) {
				row, t := lines[i], strings.TrimSpace(lines[i])
				if t == "*** Abort" || t == "*** End Patch" || strings.HasPrefix(t, "[") || t == "REM" || strings.HasPrefix(t, "MV ") || isRecognizedOp(t) || bareRangeHeader.MatchString(t) {
					break
				}
				if isContamination(t) {
					return Patch{}, contaminationError(t)
				}
				if strings.HasPrefix(row, "+") {
					payload = append(payload, row[1:])
					bareFlags = append(bareFlags, false)
					numberedFlags = append(numberedFlags, false)
					i++
					continue
				}
				if row == "" {
					payload = append(payload, "")
					bareFlags = append(bareFlags, true)
					numberedFlags = append(numberedFlags, false)
					i++
					continue
				}
				if strings.HasPrefix(row, "-") {
					if strings.HasPrefix(row, "- ") || strings.HasPrefix(strings.TrimLeft(row, " \t"), "- ") {
						bullet = true
					} else {
						// Old diff rows are safe to discard only when an explicit new row follows.
						if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+") {
							warn(cur, "old-diff-row", "Ignored unified-diff -old row", i+1)
							i++
							continue
						}
						return Patch{}, fmt.Errorf("line %d: '-' rows are not valid PUT payload; Markdown bullets must be written as '+- item'", i+1)
					}
				}
				m := numberedRow.FindStringSubmatch(row)
				payload = append(payload, row)
				bareFlags = append(bareFlags, true)
				numberedFlags = append(numberedFlags, m != nil)
				i++
			}
			for len(payload) > 0 && payload[len(payload)-1] == "" {
				payload = payload[:len(payload)-1]
				bareFlags = bareFlags[:len(bareFlags)-1]
				numberedFlags = numberedFlags[:len(numberedFlags)-1]
			}
			if len(payload) == 0 {
				if e.Kind == Insert {
					return Patch{}, fmt.Errorf("line %d: insert PUT promises body rows but has none", lineNum)
				}
				e.Kind = Delete
				warn(cur, "empty-put-cut", "Treated empty PUT body as deletion", lineNum)
			} else {
				allNumbered, anyNumbered := true, false
				for x := range payload {
					if payload[x] == "" {
						continue
					}
					if bareFlags[x] {
						anyNumbered = anyNumbered || numberedFlags[x]
						allNumbered = allNumbered && numberedFlags[x]
					}
				}
				// Avoid interpreting numeric-keyed YAML/objects as copied read rows.
				if allNumbered && anyNumbered {
					for x := range payload {
						if numberedFlags[x] {
							payload[x] = numberedRow.FindStringSubmatch(payload[x])[2]
						}
					}
				}
				e.Text = strings.Join(payload, "\n")
				for _, b := range bareFlags {
					if b {
						warn(cur, "bare-body", "Auto-prefixed bare body row", lineNum)
						break
					}
				}
				if bullet {
					warn(cur, "bare-bullet", "Auto-prefixed bare Markdown bullet row", lineNum)
				}
			}
		}
		if err := addEdit(cur, e, lineNum, warn); err != nil {
			return Patch{}, err
		}
	}
	// A final empty header is a harmless streaming artifact.
	kept := patch.Sections[:0]
	for _, s := range patch.Sections {
		if len(s.Edits) > 0 || s.FileOp.Remove || s.FileOp.MoveTo != "" {
			kept = append(kept, s)
		}
	}
	patch.Sections = kept
	if len(patch.Sections) == 0 && strings.TrimSpace(text) != "" {
		return Patch{}, fmt.Errorf("hashline: no operations")
	}
	return patch, nil
}

func parseHeader(h string) (Section, error) {
	if !strings.HasPrefix(h, "[") || !strings.HasSuffix(strings.TrimSpace(h), "]") {
		return Section{}, &MissingTagError{Detail: "header must be exactly [PATH#HASH]"}
	}
	inner := strings.TrimSpace(h[1 : len(strings.TrimSpace(h))-1])
	for _, p := range []string{"*** Update File:", "Update File:", "*** Add File:", "Add File:"} {
		inner = strings.TrimSpace(strings.TrimPrefix(inner, p))
	}
	at := strings.LastIndex(inner, "#")
	if at < 0 {
		if inner == "" || strings.ContainsAny(inner, "[]\r\n") {
			return Section{}, &MissingTagError{Detail: "header must be [PATH#HASH]"}
		}
		return Section{Path: filepath.Clean(inner)}, nil
	}
	path, hash := strings.TrimSpace(inner[:at]), inner[at+1:]
	if path == "" || !validHash(hash) {
		return Section{}, &MissingTagError{Detail: "Input header must be [PATH#HASH] with exactly four hexadecimal digits"}
	}
	return Section{Path: filepath.Clean(path), Hash: strings.ToUpper(hash)}, nil
}

func parseOperation(line string) (Edit, bool, error) {
	line = strings.TrimSpace(line)
	verb := ""
	if strings.HasPrefix(line, "PUT") {
		verb = "PUT"
	} else if strings.HasPrefix(line, "CUT") {
		verb = "CUT"
	} else {
		return Edit{}, false, fmt.Errorf("hashline: payload line has no preceding hunk header: %q", line)
	}
	if len(line) == len(verb) || (line[len(verb)] != ' ' && line[len(verb)] != '\t') {
		return Edit{}, false, malformedOperation(line)
	}
	spec := strings.TrimSpace(line[len(verb):])
	hadColon := strings.HasSuffix(spec, ":")
	if hadColon {
		spec = strings.TrimSpace(strings.TrimSuffix(spec, ":"))
	}
	reg := ""
	fields := strings.Fields(spec)
	if len(fields) > 0 && strings.HasPrefix(fields[len(fields)-1], "@") {
		reg = strings.TrimPrefix(fields[len(fields)-1], "@")
		if !validRegister(reg) {
			return Edit{}, false, malformedOperation(line)
		}
		spec = strings.TrimSpace(strings.TrimSuffix(spec, fields[len(fields)-1]))
	}
	if verb == "CUT" {
		block := strings.HasSuffix(spec, "*")
		spec = strings.TrimSuffix(spec, "*")
		a, b, err := parseRangeOrSingle(spec)
		if err != nil || b < a {
			return Edit{}, false, fmt.Errorf("hashline: malformed CUT; use CUT N or CUT N.=M")
		}
		return Edit{Kind: Cut, Range: Range{a, b}, Register: reg, Block: block}, false, nil
	}
	e := Edit{Kind: Insert, Register: reg}
	if strings.HasPrefix(spec, "<") || strings.HasPrefix(spec, ">") {
		sigil := spec[0]
		loc := strings.TrimSpace(spec[1:])
		if sigil == '>' && loc == "$" {
			e.Cursor = EOF
		} else {
			block := strings.HasSuffix(loc, "*")
			loc = strings.TrimSpace(strings.TrimSuffix(loc, "*"))
			n, err := lineNumber(loc)
			if err != nil {
				return Edit{}, false, malformedOperation(line)
			}
			e.Anchor = n
			if sigil == '<' {
				if n == 1 {
					e.Cursor = BOF
				} else {
					e.Cursor = BeforeAnchor
				}
			} else {
				e.Cursor = AfterAnchor
				e.AfterBlock = block
			}
		}
		if !hadColon {
			e.Kind = Paste
		}
		if hadColon && reg != "" {
			return Edit{}, false, fmt.Errorf("hashline: register PUT never takes ':'")
		}
		return e, hadColon, nil
	}
	block := strings.HasSuffix(spec, "*")
	spec = strings.TrimSuffix(spec, "*")
	a, b, err := parseRangeOrSingle(spec)
	if err != nil {
		return Edit{}, false, malformedOperation(line)
	}
	e.Range = Range{a, b}
	e.Block = block
	e.Kind = Replace
	if !hadColon {
		if reg == "" {
			return Edit{}, false, fmt.Errorf("hashline: span PUT without ':' requires a named register")
		}
		e.Kind = Paste
	}
	return e, hadColon, nil
}

func parseRangeOrSingle(s string) (int, int, error) {
	if n, e := lineNumber(strings.TrimSpace(s)); e == nil {
		return n, n, nil
	}
	return parseRange(s)
}
func parseRange(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, 0, fmt.Errorf("range")
	}
	j := i
	for j < len(s) {
		c := s[j]
		if c >= '0' && c <= '9' {
			break
		}
		if c != '-' && c != '.' && c != '=' && c != ' ' && c != '\t' && !strings.HasPrefix(s[j:], "…") {
			return 0, 0, fmt.Errorf("range")
		}
		if strings.HasPrefix(s[j:], "…") {
			j += len("…")
		} else {
			j++
		}
	}
	if j >= len(s) {
		return 0, 0, fmt.Errorf("range")
	}
	a, e := lineNumber(s[:i])
	if e != nil {
		return 0, 0, e
	}
	b, e := lineNumber(strings.TrimSpace(s[j:]))
	if e != nil || b < a || b-a+1 > maxLine {
		return 0, 0, fmt.Errorf("range spans more than %d lines", maxLine)
	}
	return a, b, nil
}
func lineNumber(s string) (int, error) {
	if s == "" || s[0] == '0' {
		return 0, fmt.Errorf("line")
	}
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil || n < 1 || n > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("line")
	}
	return int(n), nil
}
func validHash(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}
func validRegister(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}
func malformedOperation(line string) error {
	return fmt.Errorf("hashline: malformed operation %q", line)
}
func isRecognizedOp(s string) bool { _, _, e := parseOperation(s); return e == nil }
func isContamination(s string) bool {
	return strings.HasPrefix(s, "*** Update File:") || strings.HasPrefix(s, "*** Add File:") || strings.HasPrefix(s, "*** Delete File:") || strings.HasPrefix(s, "@@ ")
}
func contaminationError(s string) error {
	if strings.HasPrefix(s, "@@ ") {
		return fmt.Errorf("hashline: unified-diff hunk header is not valid hashline input")
	}
	return fmt.Errorf("hashline: apply_patch sentinel is not valid hashline input")
}

func addEdit(s *Section, e Edit, line int, warn func(*Section, string, string, int)) error {
	if e.Range.Start > 0 && e.Range.End-e.Range.Start+1 > maxLine {
		return fmt.Errorf("hashline: operation spans more than %d lines", maxLine)
	}
	for i := len(s.Edits) - 1; i >= 0; i-- {
		old := s.Edits[i]
		if old.Range.Start == e.Range.Start && old.Range.End == e.Range.End && e.Range.Start > 0 {
			// Exact duplicate ownership is resolved in authored order. This handles
			// copied context followed by the real hunk and CUT superseding a placeholder.
			s.Edits = append(s.Edits[:i], s.Edits[i+1:]...)
			warn(s, "duplicate-target", "Multiple hunks targeted the same exact range; kept only the last", line)
			continue
		}
		if rangesOverlap(old.Range, e.Range) {
			return fmt.Errorf("line %d: anchor line %d is already targeted by another hunk", line, max(old.Range.Start, e.Range.Start))
		}
	}
	s.Edits = append(s.Edits, e)
	return nil
}
func rangesOverlap(a, b Range) bool {
	return a.Start > 0 && b.Start > 0 && a.Start <= b.End && b.Start <= a.End
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func validateEditConflicts(edits []Edit) error {
	for i := range edits {
		for j := 0; j < i; j++ {
			if rangesOverlap(edits[i].Range, edits[j].Range) {
				return fmt.Errorf("hashline: overlapping operations")
			}
		}
	}
	return nil
}
