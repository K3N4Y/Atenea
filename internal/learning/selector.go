package learning

import (
	"sort"
	"strings"
	"unicode"
)

func Select(prompt string, lessons []Lesson) []Lesson {
	query := terms(prompt)
	type ranked struct {
		lesson Lesson
		score  int
	}
	var rs []ranked
	for _, l := range lessons {
		if !l.Enabled || l.Deleted {
			continue
		}
		hay := terms(l.Candidate.Statement + " " + l.Candidate.Scope)
		exceptions := terms(l.Candidate.Exceptions)
		score := 0
		for t := range query {
			if hay[t] {
				score++
			}
			// A matching contraindication is a deterministic veto: explicit user
			// exceptions outweigh positive lexical relevance.
			if exceptions[t] {
				score -= 1000
			}
		}
		if score > 0 {
			rs = append(rs, ranked{l, score})
		}
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].score != rs[j].score {
			return rs[i].score > rs[j].score
		}
		return rs[i].lesson.ID < rs[j].lesson.ID
	})
	var out []Lesson
	tokens := 0
	for _, r := range rs {
		cost := (len([]rune(r.lesson.Candidate.Statement+r.lesson.Candidate.Scope+r.lesson.Candidate.Exceptions)) + 3) / 4
		if len(out) == 5 || tokens+cost > 1500 {
			break
		}
		out = append(out, r.lesson)
		tokens += cost
	}
	return out
}
func terms(s string) map[string]bool {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	out := map[string]bool{}
	for _, x := range strings.Fields(b.String()) {
		if len([]rune(x)) >= 3 {
			out[x] = true
		}
	}
	return out
}
func RenderLessons(ls []Lesson) string {
	if len(ls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<approved_workspace_lessons>\nUser-approved guidance; apply only within its stated scope.\n")
	for _, l := range ls {
		b.WriteString("- " + l.Candidate.Statement + "\n  Scope: " + l.Candidate.Scope)
		if l.Candidate.Exceptions != "" {
			b.WriteString("\n  Do not apply: " + l.Candidate.Exceptions)
		}
		b.WriteByte('\n')
	}
	b.WriteString("</approved_workspace_lessons>")
	return b.String()
}
