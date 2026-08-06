// Package selector is the pure logic under the throwaway selector prototype.
package selector

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	MaxLessons = 5
	MaxTokens  = 1500
	Threshold  = 4
)

type Lesson struct {
	ID, Statement, Scope, Exceptions string
}

type Result struct {
	Lesson                                  Lesson
	Score, Tokens                           int
	StatementHits, ScopeHits, ExceptionHits []string
	Selected                                bool
	Reason                                  string
}

var stop = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true,
	"de": true, "del": true, "el": true, "en": true, "for": true, "from": true, "in": true, "is": true,
	"la": true, "las": true, "los": true, "of": true, "on": true, "or": true, "para": true, "por": true,
	"the": true, "to": true, "un": true, "una": true, "use": true, "with": true, "y": true,
}

func Tokens(text string) []string {
	decomposed := norm.NFD.String(strings.ToLower(text))
	words := strings.FieldsFunc(decomposed, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.Is(unicode.Mn, r)
	})
	seen := map[string]bool{}
	out := make([]string, 0, len(words))
	for _, word := range words {
		word = strings.Map(func(r rune) rune {
			if unicode.Is(unicode.Mn, r) {
				return -1
			}
			return r
		}, word)
		if len(word) > 4 && strings.HasSuffix(word, "s") {
			word = strings.TrimSuffix(word, "s")
		}
		if len(word) < 2 || stop[word] || seen[word] {
			continue
		}
		seen[word] = true
		out = append(out, word)
	}
	return out
}

func Select(prompt string, lessons []Lesson) []Result {
	query := set(Tokens(prompt))
	results := make([]Result, 0, len(lessons))
	for _, lesson := range lessons {
		statement := hits(query, Tokens(lesson.Statement))
		scope := hits(query, Tokens(lesson.Scope))
		exceptions := hits(query, Tokens(lesson.Exceptions))
		score := 3*len(statement) + 2*len(scope) - 4*len(exceptions)
		rendered := fmt.Sprintf("<lesson>\nStatement: %s\nScope: %s\nExceptions: %s\n</lesson>", lesson.Statement, lesson.Scope, lesson.Exceptions)
		result := Result{Lesson: lesson, Score: score, Tokens: (len(rendered) + 2) / 3, StatementHits: statement, ScopeHits: scope, ExceptionHits: exceptions}
		if len(statement) == 0 {
			result.Reason = "no statement overlap"
		} else if score < Threshold {
			result.Reason = "below threshold"
		}
		results = append(results, result)
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Lesson.ID < results[j].Lesson.ID
	})
	used, count := 0, 0
	for i := range results {
		if results[i].Reason != "" {
			continue
		}
		if count == MaxLessons {
			results[i].Reason = "five-lesson cap"
			continue
		}
		if used+results[i].Tokens > MaxTokens {
			results[i].Reason = "token budget"
			continue
		}
		results[i].Selected = true
		used += results[i].Tokens
		count++
	}
	return results
}

func set(tokens []string) map[string]bool {
	out := map[string]bool{}
	for _, token := range tokens {
		out[token] = true
	}
	return out
}
func hits(query map[string]bool, tokens []string) []string {
	out := []string{}
	for _, token := range tokens {
		if query[token] {
			out = append(out, token)
		}
	}
	return out
}
