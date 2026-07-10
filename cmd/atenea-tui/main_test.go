package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

func TestTUI_FileViewerFlowUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea-tui")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea-tui")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	cmd := exec.Command(binary)
	cmd.Dir = filepath.Join(repoRoot, "cmd/atenea-tui/testdata/file-viewer/project")
	cmd.Env = append(os.Environ(), "OPENROUTER_API_KEY=", "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	var output lockedBuffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&output, terminal); close(done) }()
	waitForPTYText(t, &output, "build · demo")
	if _, err := terminal.Write([]byte(" e\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, &output, "hello.go")
	if _, err := terminal.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, &output, "hello.go · 1-3/3")
	waitForPTYText(t, &output, "hello from file viewer")
	if _, err := terminal.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, &output, "build · demo")
	if _, err := terminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TUI did not exit")
	}
}

func TestTUI_FileTreeMouseWheelAndClickUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea-tui")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea-tui")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	cmd := exec.Command(binary)
	cmd.Dir = filepath.Join(repoRoot, "cmd/atenea-tui/testdata/file-tree-mouse/project")
	cmd.Env = append(os.Environ(), "OPENROUTER_API_KEY=", "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	var output lockedBuffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&output, terminal); close(done) }()
	waitForPTYText(t, &output, "build · demo")
	if _, err := terminal.Write([]byte(" e")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, &output, "file-00.go")
	if _, err := terminal.Write([]byte("\x1b[<65;1;4M\x1b[<65;1;4M\x1b[<0;25;4M")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, &output, "file-03.go · 1-1/1")
	waitForPTYText(t, &output, "package file03")
	if _, err := terminal.Write([]byte("\x1b[<0;25;6M")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, &output, "file-05.go · 1-1/1")
	waitForPTYText(t, &output, "package file05")
	if _, err := terminal.Write([]byte("\x1b[<0;50;4M")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, &output, "viewer *")
	waitForPTYText(t, &output, "file-05.go · 1-1/1")
	if _, err := terminal.Write([]byte("\x1b[<0;1;1M")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, &output, "explorer *")
	waitForPTYText(t, &output, "file-05.go · 1-1/1")
	if _, err := terminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TUI did not exit")
	}
}

func TestTUI_ExplorerLeaderRapidSequencesUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea-tui")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea-tui")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	cmd := exec.Command(binary)
	cmd.Dir = filepath.Join(repoRoot, "cmd/atenea-tui/testdata/file-viewer/project")
	cmd.Env = append(os.Environ(), "OPENROUTER_API_KEY=", "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	var output lockedBuffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&output, terminal); close(done) }()
	waitForPTYText(t, &output, "build · demo")

	before := output.String()
	if _, err := terminal.Write(bytes.Repeat([]byte(" e"), 2001)); err != nil {
		t.Fatal(err)
	}
	latest := waitForStablePTYOutputAfter(t, &output, before)
	if !strings.Contains(latest, "explorer *") || !strings.Contains(latest, "hello.go") {
		t.Fatalf("rapid Space+e sequences should leave explorer open after an odd count; latest PTY output:\n%s", latest)
	}

	before = output.String()
	if _, err := terminal.Write([]byte(" e")); err != nil {
		t.Fatal(err)
	}
	latest = waitForStablePTYOutputAfter(t, &output, before)
	if strings.Contains(latest, "explorer") || strings.Contains(latest, "hello.go") {
		t.Fatalf("one more Space+e sequence should close the explorer after the rapid burst; latest PTY output:\n%s", latest)
	}
	if _, err := terminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TUI did not exit")
	}
}

func waitForPTYText(t *testing.T, output *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(ansi.Strip(output.String()), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("PTY output did not contain %q:\n%s", want, ansi.Strip(output.String()))
}

func stopPTYProcess(cmd *exec.Cmd, terminal *os.File) func() {
	return func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = terminal.Close()
		_ = cmd.Wait()
	}
}

func waitForStablePTYOutputAfter(t *testing.T, output *lockedBuffer, previous string) string {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	quietSince := time.Now()
	last := output.String()
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		current := output.String()
		if current == last {
			if len(current) > len(previous) && time.Since(quietSince) >= 200*time.Millisecond {
				return ansi.Strip(current[len(previous):])
			}
			continue
		}
		last = current
		quietSince = time.Now()
	}
	t.Fatalf("PTY output did not settle after rapid input:\n%s", ansi.Strip(last))
	return ""
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
