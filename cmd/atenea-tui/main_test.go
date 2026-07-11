package main

import (
	"bytes"
	"fmt"
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

func TestTUI_PromptHistorySurvivesRestartUnderPTY(t *testing.T) {
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
	database := filepath.Join(t.TempDir(), "atenea.db")
	workdir := filepath.Join(repoRoot, "cmd/atenea-tui/testdata/file-viewer/project")

	firstCmd, firstTerminal, firstOutput, firstDone := startTUIUnderPTY(t, binary, workdir, database)
	waitForPTYText(t, firstOutput, " demo ─╯")
	if _, err := firstTerminal.Write([]byte("mensaje persistente\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, firstOutput, "mensaje persistente")
	if _, err := firstTerminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitForPTYExit(t, firstDone)
	_ = firstTerminal.Close()
	_ = firstCmd.Wait()

	secondCmd, secondTerminal, secondOutput, secondDone := startTUIUnderPTY(t, binary, workdir, database)
	defer stopPTYProcess(secondCmd, secondTerminal)
	waitForPTYText(t, secondOutput, " demo ─╯")
	before := secondOutput.String()
	if _, err := secondTerminal.Write([]byte("\x1b[A")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, secondOutput, before, "mensaje persistente")
	if _, err := secondTerminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitForPTYExit(t, secondDone)
}

func TestTUI_ModelSelectorPersistsSelectionUnderPTY(t *testing.T) {
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

	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "atenea")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "providers.json")
	config := `{"providers":[{"id":"local","name":"Local","type":"openai-compatible","base_url":"http://127.0.0.1:1/v1","models":["old","new"]}],"selected":{"provider":"local","model":"old"}}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binary)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configRoot, "OPENROUTER_API_KEY=", "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	output := &lockedBuffer{}
	go func() { _, _ = io.Copy(output, terminal) }()
	waitForPTYText(t, output, " old ─╯")
	if _, err := terminal.Write([]byte("/")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "/model")
	if _, err := terminal.Write([]byte("model new\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "/model local new")
	if _, err := terminal.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, " new ─╯")

	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), `"model": "new"`) {
		t.Fatalf("selection was not persisted:\n%s", persisted)
	}
}

func TestTUI_DefaultOpenRouterModelsShowContextUnderPTY(t *testing.T) {
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
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+t.TempDir(), "OPENROUTER_API_KEY=test", "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	output := &lockedBuffer{}
	go func() { _, _ = io.Copy(output, terminal) }()
	waitForPTYText(t, output, " openrouter/free ─╯")
	if _, err := terminal.Write([]byte("/model ")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tencent/hy3:free", "poolside/laguna-xs-2.1:free", "cohere/north-mini-code:free", "262K context", "256K context"} {
		waitForPTYText(t, output, want)
	}
}

func TestTUI_CtrlJCreatesMultilineComposerUnderPTY(t *testing.T) {
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
	workdir := filepath.Join(repoRoot, "cmd/atenea-tui/testdata/file-viewer/project")
	cmd, terminal, output, _ := startTUIUnderPTY(t, binary, workdir, filepath.Join(t.TempDir(), "atenea.db"))
	defer stopPTYProcess(cmd, terminal)
	waitForPTYText(t, output, " demo ─╯")

	if _, err := terminal.Write([]byte("primera linea")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "❯ primera linea")
	before := output.String()
	if _, err := terminal.Write([]byte("\x0asegunda linea")); err != nil {
		t.Fatal(err)
	}
	if latest := waitForStablePTYOutputAfter(t, output, before); !strings.Contains(ansi.Strip(latest), "❯ segunda linea") {
		t.Fatalf("Ctrl+J debe crear una segunda fila visible del composer; salida PTY:\n%s", ansi.Strip(latest))
	}
}

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
	waitForPTYText(t, &output, " demo ─╯")
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
	waitForPTYText(t, &output, " demo ─╯")
	if _, err := terminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TUI did not exit")
	}
}

func TestTUI_FileViewerScrollsToLastLineUnderPTY(t *testing.T) {
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

	workdir := t.TempDir()
	var content strings.Builder
	for line := 1; line <= 31; line++ {
		fmt.Fprintf(&content, "line %02d\n", line)
	}
	if err := os.WriteFile(filepath.Join(workdir, "long.txt"), []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, terminal, output, _ := startTUIUnderPTY(t, binary, workdir, filepath.Join(t.TempDir(), "atenea.db"))
	defer stopPTYProcess(cmd, terminal)
	waitForPTYText(t, output, " demo ─╯")
	if _, err := terminal.Write([]byte(" e\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "long.txt")
	if _, err := terminal.Write([]byte("\r\x1b[6~")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "long.txt · 12-31/31")
	waitForPTYText(t, output, "line 31")
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
	waitForPTYText(t, &output, " demo ─╯")
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
	waitForPTYText(t, &output, " demo ─╯")

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

func startTUIUnderPTY(t *testing.T, binary, workdir, database string) (*exec.Cmd, *os.File, *lockedBuffer, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command(binary)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), "OPENROUTER_API_KEY=", "ATENEA_DB="+database)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	output := &lockedBuffer{}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(output, terminal); close(done) }()
	return cmd, terminal, output, done
}

func waitForPTYExit(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TUI did not exit")
	}
}

func waitForPTYTextAfter(t *testing.T, output *lockedBuffer, previous, want string) {
	t.Helper()
	previous = ansi.Strip(previous)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current := ansi.Strip(output.String())
		if len(current) >= len(previous) && strings.Contains(current[len(previous):], want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("PTY output after restart did not contain %q:\n%s", want, ansi.Strip(output.String()))
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
