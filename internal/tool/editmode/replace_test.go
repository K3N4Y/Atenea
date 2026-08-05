package editmode

import (
	"strings"
	"testing"
)

// Ported from oh-my-pi@5af71dc9 packages/coding-agent/test/edit-diff.test.ts.
// https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/edit-diff.test.ts
func TestFindMatchIndentationAndThreshold(t *testing.T) {
	tests := []struct{ name, content, target string }{
		{"tabs in file", "\tfoo\n\t\tbar\n\tbaz", "  foo\n    bar\n  baz"},
		{"spaces in file", "  foo\n    bar\n  baz", "\tfoo\n\t\tbar\n\tbaz"},
		{"different widths", "   foo\n      bar\n   baz", "  foo\n    bar\n  baz"},
		{"single line", "prefix\n\t\t\t\"value\",\nsuffix", "          \"value\","},
		{"inconsistent indentation fallback", "\t\t\tline1\n\t\t\tline2\n\t\tline3\n\t\t\tline4", "      line1\n      line2\n      line3\n      line4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := FindMatch(tt.content, tt.target, true, .95, nil)
			if o.Match == nil || o.Match.Confidence < .95 {
				t.Fatalf("outcome=%+v", o)
			}
		})
	}
	content, target := "hello world\nhello wurld", "hello warld"
	if o := FindMatch(content, target, true, .99, nil); o.Match != nil {
		t.Fatalf("high threshold matched: %+v", o)
	}
	if o := FindMatch(content, target, true, .7, nil); o.FuzzyMatches <= 1 {
		t.Fatalf("expected ambiguous fuzzy matches: %+v", o)
	}
}

// Ported from replace.ts exact occurrence diagnostics and immutable-source replace_all.
func TestReplaceTextUniquenessReplaceAllAndNoRematch(t *testing.T) {
	_, _, err := ReplaceText("foo\nbar\nfoo", ReplaceInput{Path: "x.txt", OldString: "foo", NewString: "x"}, false, .95)
	if err == nil || !strings.Contains(err.Error(), "Found 2 occurrences in x.txt") || !strings.Contains(err.Error(), "1 | foo") {
		t.Fatalf("err=%v", err)
	}
	got, n, err := ReplaceText("foo foo", ReplaceInput{Path: "x.txt", OldString: "foo", NewString: "bar", ReplaceAll: true}, false, .95)
	if err != nil || got != "bar bar" || n != 2 {
		t.Fatalf("got=%q n=%d err=%v", got, n, err)
	}
	old := strings.Repeat("a", 50)
	first := strings.Repeat("a", 49) + "b"
	second := strings.Repeat("a", 44) + "cccccc"
	newText := old + "\nexpanded"
	got, n, err = ReplaceText(first+"\n"+second, ReplaceInput{Path: "x", OldString: old, NewString: newText, ReplaceAll: true}, true, .8)
	if err != nil || n != 2 || strings.Count(got, newText) != 2 {
		t.Fatalf("count=%d err=%v got=%q", n, err, got)
	}
}

func TestReplaceTextEmptyNoopUnicodeAndFuzzyDisabled(t *testing.T) {
	if _, _, err := ReplaceText("x", ReplaceInput{Path: "x", OldString: "", NewString: "y"}, true, .95); err == nil || err.Error() != "old_string must not be empty." {
		t.Fatalf("err=%v", err)
	}
	got, n, err := ReplaceText("say “hello”", ReplaceInput{Path: "x", OldString: "say \"hello\"", NewString: "ok"}, true, .95)
	if err != nil || got != "ok" || n != 1 {
		t.Fatalf("got=%q n=%d err=%v", got, n, err)
	}
	_, _, err = ReplaceText("hello wurld", ReplaceInput{Path: "x", OldString: "hello world", NewString: "x"}, false, .95)
	if err == nil || !strings.Contains(err.Error(), "Fuzzy matching is disabled") {
		t.Fatalf("err=%v", err)
	}
}

// Ported from edit/streaming-matcher-paths.test.ts.
// https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/edit/streaming-matcher-paths.test.ts
func TestReplaceMatcherEntries(t *testing.T) {
	got := ReplaceMatcherEntries(ReplaceInput{Path: "src/foo.ts", NewString: "x = 1"})
	if len(got) != 1 || got[0].Path != "src/foo.ts" || got[0].Digest != "x = 1" {
		t.Fatalf("entries=%+v", got)
	}
	if got := ReplaceMatcherEntries(ReplaceInput{}); got != nil {
		t.Fatalf("empty entries=%+v", got)
	}
}
