package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestOpenFileViewer_NormalizesCRLFAndNumbersLines(t *testing.T) {
	viewer := openFileViewer("example.go", []byte("package x\r\nfunc main() {}\r\n"))
	got := ansi.Strip(viewer.render(80, 4))
	for _, want := range []string{"1", "package x", "2", "func main() {}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("render = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "\r") {
		t.Fatalf("render = %q contains CR", got)
	}
}

func TestOpenFileViewer_EmptyBinaryAndLargeStates(t *testing.T) {
	for _, test := range []struct {
		path string
		data []byte
		want string
	}{
		{"empty.txt", nil, "archivo vacio: empty.txt"},
		{"image.bin", []byte{'a', 0, 'b'}, "archivo binario: image.bin"},
		{"large.txt", make([]byte, maxFileViewerBytes+1), "archivo demasiado grande (> 1 MiB): large.txt"},
	} {
		t.Run(test.path, func(t *testing.T) {
			got := ansi.Strip(openFileViewer(test.path, test.data).render(100, 4))
			if !strings.Contains(got, test.want) {
				t.Fatalf("render = %q, want %q", got, test.want)
			}
		})
	}
}

func TestOpenFileViewer_UsesLexerAndPlainFallback(t *testing.T) {
	if got := openFileViewer("main.go", []byte("package main\n")).lines[0]; !strings.Contains(got, "\x1b[") {
		t.Fatalf("Go line = %q, want ANSI", got)
	}
	if got := ansi.Strip(openFileViewer("NOTICE.custom", []byte("plain\n")).lines[0]); got != "plain" {
		t.Fatalf("fallback = %q, want plain", got)
	}
	if got := openFileViewer("two.txt", []byte("one\ntwo\n")).lineCount; got != 2 {
		t.Fatalf("trailing newline count = %d, want 2", got)
	}
	if got := openFileViewer("blank.txt", []byte("\n")).lineCount; got != 1 {
		t.Fatalf("single blank line count = %d, want 1", got)
	}
}

func TestOpenFileViewer_ResetsSyntaxStyleAtEachLineBoundary(t *testing.T) {
	viewer := openFileViewer("comment.go", []byte("/* first comment\nsecond comment */\npackage main\n"))
	for index, line := range viewer.lines {
		if !strings.HasSuffix(line, ansi.ResetStyle) {
			t.Fatalf("line %d = %q, must reset ANSI style before the next terminal row", index, line)
		}
	}
}

func TestOpenFileViewer_ExpandsTabsBeforeHighlighting(t *testing.T) {
	viewer := openFileViewer("tabs.go", []byte("\tfield string // comment that would wrap\n"))
	for index, line := range viewer.lines {
		if strings.Contains(line, "\t") {
			t.Fatalf("line %d = %q, must not retain terminal tabs", index, line)
		}
	}
	if got := ansi.StringWidth(viewer.render(20, 1)); got > 20 {
		t.Fatalf("render width = %d, want <= 20", got)
	}
}

func TestWorkspaceFileReader_ReadsRelativePathAndRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	read := WorkspaceFileReader(root)
	if got, err := read("ok.txt"); err != nil || string(got) != "ok" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if _, err := read("../outside.txt"); err == nil {
		t.Fatal("escape must fail")
	}
}

func TestFileViewer_ScrollAndResizeClamp(t *testing.T) {
	viewer := openFileViewer("many.txt", []byte("1\n2\n3\n4\n5\n6\n"))
	viewer.scroll(99, 3)
	if viewer.offset != 3 {
		t.Fatalf("down = %d, want 3", viewer.offset)
	}
	viewer.scroll(-99, 3)
	if viewer.offset != 0 {
		t.Fatalf("up = %d, want 0", viewer.offset)
	}
	viewer.offset = 99
	viewer.clamp(10)
	if viewer.offset != 0 {
		t.Fatalf("resize = %d, want 0", viewer.offset)
	}
	viewer.offset = 2
	viewer.clamp(0)
	if viewer.offset != 0 {
		t.Fatalf("zero height = %d, want 0", viewer.offset)
	}
}

func TestFileViewer_RenderShowsRangeAndNeverOverflows(t *testing.T) {
	viewer := openFileViewer("many.go", []byte("one\ntwo\nthree\nfour\nfive\n"))
	viewer.offset = 2
	got := ansi.Strip(viewer.render(20, 2))
	for _, want := range []string{"3", "three", "4", "four"} {
		if !strings.Contains(got, want) {
			t.Fatalf("render = %q, want %q", got, want)
		}
	}
	if header := ansi.Strip(viewer.header(80, 2)); header != "many.go · 3-4/5" {
		t.Fatalf("header = %q", header)
	}
	for _, width := range []int{0, 1, 4, 12} {
		for _, line := range strings.Split(viewer.render(width, 2), "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("width %d: %q", width, line)
			}
		}
	}
}
