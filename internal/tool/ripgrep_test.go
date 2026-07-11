package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseRipgrepJSON_MatchRecords(t *testing.T) {
	stdout := []byte(strings.Join([]string{
		`{"type":"begin","data":{"path":{"text":"internal/tool/read.go"}}}`,
		`{"type":"match","data":{"path":{"text":"internal/tool/read.go"},"line_number":42,"lines":{"text":"func read() {}\n"}}}`,
		`{"type":"end","data":{"path":{"text":"internal/tool/read.go"}}}`,
		`{"type":"summary","data":{"elapsed_total":{"secs":0,"nanos":1}}}`,
		`{"type":"match","data":{"path":{"text":"./internal\\tool\\write.go"},"line_number":7,"lines":{"text":"write call\n"}}}`,
		``,
	}, "\n"))

	got, err := ParseRipgrepJSON(stdout, 100)
	if err != nil {
		t.Fatalf("ParseRipgrepJSON returned error: %v", err)
	}

	want := GrepResult{Matches: []GrepMatch{
		{Path: "internal/tool/read.go", Line: 42, Text: "func read() {}\n"},
		{Path: "internal/tool/write.go", Line: 7, Text: "write call\n"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseRipgrepJSON() = %#v, want %#v", got, want)
	}
}

func TestParseRipgrepJSON_LimitAndTruncated(t *testing.T) {
	stdout := []byte(strings.Join([]string{
		rgMatchJSON("a.go", 1, "one"),
		rgMatchJSON("b.go", 2, "two"),
		rgMatchJSON("c.go", 3, "three"),
		"",
	}, "\n"))

	got, err := ParseRipgrepJSON(stdout, 2)
	if err != nil {
		t.Fatalf("ParseRipgrepJSON returned error: %v", err)
	}

	if len(got.Matches) != 2 {
		t.Fatalf("len(Matches) = %d, want 2", len(got.Matches))
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true")
	}
}

func TestParseRipgrepJSON_DefaultLimitAndLongLineTruncation(t *testing.T) {
	long := strings.Repeat("ñ", 2001)
	stdout := []byte(rgMatchJSON("long.go", 12, long) + "\n")

	got, err := ParseRipgrepJSON(stdout, 0)
	if err != nil {
		t.Fatalf("ParseRipgrepJSON returned error: %v", err)
	}

	if len(got.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1", len(got.Matches))
	}
	want := strings.Repeat("ñ", 2000) + "..."
	if got.Matches[0].Text != want {
		t.Fatalf("line text was not truncated to 2000 runes plus ellipsis: got %d runes", len([]rune(got.Matches[0].Text)))
	}
}

func TestParseRipgrepJSON_TruncatesVeryLongJSONLine(t *testing.T) {
	long := strings.Repeat("x", 1024*1024+1)
	stdout := []byte(rgMatchJSON("huge.go", 1, long) + "\n")

	got, err := ParseRipgrepJSON(stdout, 100)
	if err != nil {
		t.Fatalf("ParseRipgrepJSON returned error: %v", err)
	}

	if len(got.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1", len(got.Matches))
	}
	want := strings.Repeat("x", 2000) + "..."
	if got.Matches[0].Text != want {
		t.Fatalf("line text was not truncated to 2000 runes plus ellipsis: got %d runes", len([]rune(got.Matches[0].Text)))
	}
}

func TestBuildRipgrepArgs(t *testing.T) {
	req := GrepRequest{Path: "internal/tool", Pattern: "func\\s+Read", Include: "*.go"}

	got := buildRipgrepArgs(req)
	want := []string{
		"--no-config",
		"--json",
		"--hidden",
		"--no-messages",
		"--glob=*.go",
		"--glob=!**/.git/**",
		"--",
		"func\\s+Read",
		"internal/tool",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildRipgrepArgs() = %#v, want %#v", got, want)
	}
}

func TestBuildRipgrepArgs_DefaultPath(t *testing.T) {
	got := buildRipgrepArgs(GrepRequest{Pattern: "needle"})
	want := []string{
		"--no-config",
		"--json",
		"--hidden",
		"--no-messages",
		"--glob=!**/.git/**",
		"--",
		"needle",
		".",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildRipgrepArgs() = %#v, want %#v", got, want)
	}
}

func TestRgSearcher_NoMatchesExitOne(t *testing.T) {
	searcher := &RgSearcher{Binary: helperScript(t, "exit 1")}

	got, err := searcher.Grep(context.Background(), GrepRequest{Root: t.TempDir(), Path: ".", Pattern: "missing"})
	if err != nil {
		t.Fatalf("Grep returned error: %v", err)
	}
	if len(got.Matches) != 0 || got.Truncated {
		t.Fatalf("Grep() = %#v, want empty result", got)
	}
}

func TestRgSearcher_InvalidPatternError(t *testing.T) {
	searcher := &RgSearcher{Binary: helperScript(t, "echo 'regex parse error: missing ]' >&2\nexit 2")}

	_, err := searcher.Grep(context.Background(), GrepRequest{Root: t.TempDir(), Path: ".", Pattern: "["})
	if err == nil {
		t.Fatal("Grep returned nil error, want GrepInvalidPatternError")
	}
	var invalid *GrepInvalidPatternError
	if !errors.As(err, &invalid) {
		t.Fatalf("Grep error = %T %[1]v, want GrepInvalidPatternError", err)
	}
	if invalid.Pattern != "[" || !strings.Contains(invalid.Detail, "regex parse error") {
		t.Fatalf("invalid pattern error = %#v, want pattern and stderr detail", invalid)
	}
}

func TestRgSearcher_InvalidPatternErrorWithLargeStderr(t *testing.T) {
	searcher := &RgSearcher{Binary: helperScript(t, "printf 'regex parse error: missing ]\\n' >&2\nhead -c 131072 /dev/zero | tr '\\000' x >&2\nexit 2")}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := searcher.Grep(ctx, GrepRequest{Root: t.TempDir(), Path: ".", Pattern: "["})
	if err == nil {
		t.Fatal("Grep returned nil error, want GrepInvalidPatternError")
	}
	var invalid *GrepInvalidPatternError
	if !errors.As(err, &invalid) {
		t.Fatalf("Grep error = %T %[1]v, want GrepInvalidPatternError", err)
	}
}

func TestRgSearcher_UnavailableBinary(t *testing.T) {
	searcher := &RgSearcher{Binary: filepath.Join(t.TempDir(), "missing-rg")}

	_, err := searcher.Grep(context.Background(), GrepRequest{Root: t.TempDir(), Path: ".", Pattern: "needle"})
	if err == nil {
		t.Fatal("Grep returned nil error, want GrepUnavailableError")
	}
	var unavailable *GrepUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Grep error = %T %[1]v, want GrepUnavailableError", err)
	}
	if unavailable.Binary != searcher.Binary {
		t.Fatalf("Binary = %q, want %q", unavailable.Binary, searcher.Binary)
	}
}

func TestRgSearcher_ParsesExitZeroStdout(t *testing.T) {
	script := helperScript(t, "cat <<'JSON'\n"+rgMatchJSON("hit.go", 9, "needle here")+"\nJSON\n")
	searcher := &RgSearcher{Binary: script}

	got, err := searcher.Grep(context.Background(), GrepRequest{Root: t.TempDir(), Path: ".", Pattern: "needle", Limit: 10})
	if err != nil {
		t.Fatalf("Grep returned error: %v", err)
	}

	want := GrepResult{Matches: []GrepMatch{{Path: "hit.go", Line: 9, Text: "needle here"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Grep() = %#v, want %#v", got, want)
	}
}

func TestRgSearcher_StopsAfterLimitPlusOne(t *testing.T) {
	script := helperScript(t, strings.Join([]string{
		"cat <<'JSON'",
		rgMatchJSON("one.go", 1, "one"),
		rgMatchJSON("two.go", 2, "two"),
		rgMatchJSON("three.go", 3, "three"),
		"JSON",
		"sleep 5",
	}, "\n"))
	searcher := &RgSearcher{Binary: script}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	got, err := searcher.Grep(ctx, GrepRequest{Root: t.TempDir(), Path: ".", Pattern: "needle", Limit: 2})
	if err != nil {
		t.Fatalf("Grep returned error: %v", err)
	}
	if len(got.Matches) != 2 {
		t.Fatalf("len(Matches) = %d, want 2", len(got.Matches))
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true")
	}
}

func TestRgSearcher_ContextCancellation(t *testing.T) {
	searcher := &RgSearcher{Binary: helperScript(t, "sleep 5")}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := searcher.Grep(ctx, GrepRequest{Root: t.TempDir(), Path: ".", Pattern: "needle"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Grep error = %v, want context deadline exceeded", err)
	}
}

func TestRgSearcher_StopsProcessOnJSONParseError(t *testing.T) {
	searcher := &RgSearcher{Binary: helperScript(t, "printf '{not-json}\\n'\nsleep 5")}

	start := time.Now()
	_, err := searcher.Grep(context.Background(), GrepRequest{Root: t.TempDir(), Path: ".", Pattern: "needle"})
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "salida JSON de rg invalida") {
		t.Fatalf("Grep error = %v, want invalid JSON error", err)
	}
	if elapsed > time.Second {
		t.Fatalf("Grep took %v after parse error, want process stopped promptly", elapsed)
	}
}

func TestRgSearcher_OtherFailureIncludesBoundedStderr(t *testing.T) {
	detail := strings.Repeat("x", 5000)
	searcher := &RgSearcher{Binary: helperScript(t, "printf '%s' '"+detail+"' >&2\nexit 2")}

	_, err := searcher.Grep(context.Background(), GrepRequest{Root: t.TempDir(), Path: ".", Pattern: "needle"})
	if err == nil {
		t.Fatal("Grep returned nil error, want actionable failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "rg failed") {
		t.Fatalf("error %q does not contain actionable rg failure", msg)
	}
	if len(msg) > 1300 {
		t.Fatalf("error length = %d, want bounded stderr", len(msg))
	}
}

func rgMatchJSON(path string, line int, text string) string {
	text = strings.ReplaceAll(text, `\`, `\\`)
	text = strings.ReplaceAll(text, `"`, `\"`)
	return `{"type":"match","data":{"path":{"text":"` + path + `"},"line_number":` + intString(line) + `,"lines":{"text":"` + text + `"}}}`
}

func intString(n int) string {
	switch n {
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	case 9:
		return "9"
	case 12:
		return "12"
	case 42:
		return "42"
	default:
		return "0"
	}
}

func helperScript(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "rg-helper.sh")
	content := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write helper script: %v", err)
	}
	return path
}
