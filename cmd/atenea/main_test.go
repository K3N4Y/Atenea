package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"

	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// providerKeyEnvNames is every API-key variable the built-in catalog reads.
// Deriving the list keeps a newly shipped provider from silently making these
// tests depend on whatever the developer happens to have exported.
func providerKeyEnvNames() []string {
	seen := map[string]struct{}{}
	names := make([]string, 0, 4)
	for _, provider := range providerconfig.DefaultCatalog().Providers {
		if provider.APIKeyEnv == "" {
			continue
		}
		if _, ok := seen[provider.APIKeyEnv]; ok {
			continue
		}
		seen[provider.APIKeyEnv] = struct{}{}
		names = append(names, provider.APIKeyEnv)
	}
	return names
}

// blankProviderKeys renders those variables as empty assignments for a child
// process, so a launched binary lands on the demo provider instead of on a real
// gateway the developer's shell had configured.
func blankProviderKeys() []string {
	names := providerKeyEnvNames()
	assignments := make([]string, 0, len(names))
	for _, name := range names {
		assignments = append(assignments, name+"=")
	}
	return assignments
}

func TestAteneaVersion_PrintsReleaseMetadataAndExits(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command(
		"go", "build",
		"-tags", "production",
		"-ldflags", "-X main.version=v1.2.3 -X main.commit=abc1234 -X main.buildDate=2026-07-21T12:00:00Z",
		"-o", binary,
		"./cmd/atenea",
	)
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build release binary: %v\n%s", err, output)
	}

	cmd := exec.Command(binary, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("atenea --version: %v\n%s", err, output)
	}
	want := "atenea v1.2.3 (commit abc1234, built 2026-07-21T12:00:00Z)\n"
	if string(output) != want {
		t.Fatalf("atenea --version = %q, want %q", output, want)
	}
}

func TestOpenInteractiveLearningStoreFailsAndClosesHost(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data-file"))
	if err := os.WriteFile(os.Getenv("XDG_DATA_HOME"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	closed := 0
	store, err := openInteractiveLearningStore(func() error {
		closed++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "open durable learning store") {
		t.Fatalf("store=%v err=%v", store, err)
	}
	if closed != 1 {
		t.Fatalf("host close calls=%d, want 1", closed)
	}
}

func TestTUI_PromptHistorySurvivesRestartUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	database := filepath.Join(t.TempDir(), "atenea.db")
	workdir := repoRoot

	firstCmd, firstTerminal, firstOutput, firstDone := startTUIUnderPTY(t, binary, workdir, database)
	waitForPTYText(t, firstOutput, " demo ─┘")
	beforeSubmit := firstOutput.String()
	if _, err := firstTerminal.Write([]byte("mensaje persistente\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, firstOutput, beforeSubmit, "Hello from atenea.")
	quitPTY(t, firstTerminal, firstOutput)
	waitForPTYExit(t, firstDone)
	_ = firstTerminal.Close()
	_ = firstCmd.Wait()

	secondCmd, secondTerminal, secondOutput, secondDone := startTUIUnderPTY(t, binary, workdir, database)
	defer stopPTYProcess(secondCmd, secondTerminal)
	waitForPTYText(t, secondOutput, " demo ─┘")
	before := secondOutput.String()
	if _, err := secondTerminal.Write([]byte("\x1b[A")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, secondOutput, before, "mensaje persistente")
	quitPTY(t, secondTerminal, secondOutput)
	waitForPTYExit(t, secondDone)
}

func TestTUI_DragAssistantTextCopiesSelectionUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	cmd, terminal, output, done := startTUIUnderPTY(t, binary, repoRoot, filepath.Join(t.TempDir(), "atenea.db"))
	defer stopPTYProcess(cmd, terminal)
	waitForPTYText(t, output, " demo ─┘")
	if _, err := terminal.Write([]byte("copy this\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "Hello from atenea.")

	// SGR mouse coordinates are one-based. Try every body row because the exact
	// transcript row depends on startup notices; only the row containing the
	// settled assistant response can begin a selection. Columns 3..7 select
	// "Hello", whose OSC 52 payload is SGVsbG8=.
	before := output.String()
	for row := 4; row <= 20; row++ {
		sequence := fmt.Sprintf("\x1b[<0;3;%dM\x1b[<32;7;%dM\x1b[<0;7;%dm", row, row, row)
		if _, err := terminal.Write([]byte(sequence)); err != nil {
			t.Fatal(err)
		}
	}
	waitForPTYRawAfter(t, output, before, "\x1b]52;c;SGVsbG8=\a")
	waitForPTYTextAfter(t, output, before, "Copied to clipboard")
	quitPTY(t, terminal, output)
	waitForPTYExit(t, done)
}

func TestTUI_YoloLaunchShowsWarningIndicatorAndModeTransitionsUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	cmd, terminal, output, _ := startTUIUnderPTY(t, binary, repoRoot, filepath.Join(t.TempDir(), "atenea.db"), "--yolo")
	defer stopPTYProcess(cmd, terminal)
	waitForPTYText(t, output, "YOLO mode is active")
	waitForPTYText(t, output, "demo · YOLO")
	before := output.String()
	if _, err := terminal.Write([]byte("/mode:ask\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, output, before, "permission mode: ask")
	before = output.String()
	if _, err := terminal.Write([]byte("/mode:yolo\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, output, before, "permission mode: yolo")
}

func TestTUI_YoloSendsPromptOutsideGitWorkspaceUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}

	cmd, terminal, output, _ := startTUIUnderPTY(t, binary, t.TempDir(), filepath.Join(t.TempDir(), "atenea.db"), "--yolo")
	defer stopPTYProcess(cmd, terminal)
	waitForPTYText(t, output, "YOLO mode is active")
	before := output.String()
	if _, err := terminal.Write([]byte("prompt outside Git\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, output, before, "Hello from atenea.")
}

func TestTUI_LearningCommandsUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}

	cmd, terminal, output, _ := startTUIUnderPTY(t, binary, repoRoot, filepath.Join(t.TempDir(), "atenea.db"))
	defer stopPTYProcess(cmd, terminal)
	waitForPTYText(t, output, " demo ─┘")
	if _, err := terminal.Write([]byte("/learned")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "│ › /learned")
	if _, err := terminal.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "Learned Guidance")
	waitForPTYText(t, output, "Nothing learned in this workspace")
	beforeClose := output.String()
	if _, err := terminal.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, output, beforeClose, " demo ─┘")
	if _, err := terminal.Write([]byte("/learn")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "│ › /learn")
	beforeLearn := output.String()
	if _, err := terminal.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, output, beforeLearn, "session not found")
}

// TestTUI_StartsFreshSessionOnLaunchUnderPTY pins the launch contract end to
// end: a new run of the binary starts an empty conversation, without the
// transcript or the plan mode of the previous run. Old sessions stay
// reachable only through /resume.
func TestTUI_StartsFreshSessionOnLaunchUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	database := filepath.Join(t.TempDir(), "atenea.db")
	workdir := repoRoot

	firstCmd, firstTerminal, firstOutput, firstDone := startTUIUnderPTY(t, binary, workdir, database)
	waitForPTYText(t, firstOutput, " demo ─┘")
	beforeSubmit := firstOutput.String()
	if _, err := firstTerminal.Write([]byte("\tcontinuidad tui\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, firstOutput, beforeSubmit, "Hello from atenea.")
	quitPTY(t, firstTerminal, firstOutput)
	waitForPTYExit(t, firstDone)
	_ = firstTerminal.Close()
	_ = firstCmd.Wait()

	secondCmd, secondTerminal, secondOutput, secondDone := startTUIUnderPTY(t, binary, workdir, database)
	defer stopPTYProcess(secondCmd, secondTerminal)
	// The build-mode footer (no "· plan") is the full-render signal: the plan
	// mode of the previous run must not survive the restart either.
	waitForPTYText(t, secondOutput, " demo ─┘")
	if rendered := ansi.Strip(secondOutput.String()); strings.Contains(rendered, "continuidad tui") {
		t.Fatalf("a fresh launch must not show transcripts from previous runs:\n%s", rendered)
	}
	quitPTY(t, secondTerminal, secondOutput)
	waitForPTYExit(t, secondDone)
}

func TestTUI_ResumeCommandOpensPreviousWorkspaceSessionUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	database := filepath.Join(t.TempDir(), "atenea.db")
	workdir := repoRoot

	firstCmd, firstTerminal, firstOutput, firstDone := startTUIUnderPTY(t, binary, workdir, database)
	waitForPTYText(t, firstOutput, " demo ─┘")
	// A session is titled after its first user message, so the wait has to be
	// on the reply — the composer echoes the text a frame before Enter is
	// handled, and quitting on that frame leaves an untitled session behind.
	beforeFirstSubmit := firstOutput.String()
	if _, err := firstTerminal.Write([]byte("\tsesion anterior\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, firstOutput, beforeFirstSubmit, "Hello from atenea.")
	quitPTY(t, firstTerminal, firstOutput)
	waitForPTYExit(t, firstDone)
	_ = firstTerminal.Close()
	_ = firstCmd.Wait()

	// The second launch starts fresh, so its conversation becomes a second
	// resumable session without needing /new.
	secondCmd, secondTerminal, secondOutput, secondDone := startTUIUnderPTY(t, binary, workdir, database)
	waitForPTYText(t, secondOutput, " demo ─┘")
	beforeSecondSubmit := secondOutput.String()
	if _, err := secondTerminal.Write([]byte("sesion actual\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, secondOutput, beforeSecondSubmit, "Hello from atenea.")
	quitPTY(t, secondTerminal, secondOutput)
	waitForPTYExit(t, secondDone)
	_ = secondTerminal.Close()
	_ = secondCmd.Wait()

	thirdCmd, thirdTerminal, thirdOutput, thirdDone := startTUIUnderPTY(t, binary, workdir, database)
	defer stopPTYProcess(thirdCmd, thirdTerminal)
	waitForPTYText(t, thirdOutput, " demo ─┘")
	before := thirdOutput.String()
	if _, err := thirdTerminal.Write([]byte("/resume\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, thirdOutput, before, "sesion anterior")
	if _, err := thirdTerminal.Write([]byte("\x1b[B\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, thirdOutput, before, " demo · plan ─┘")
	quitPTY(t, thirdTerminal, thirdOutput)
	waitForPTYExit(t, thirdDone)
}

func TestTUI_ModelSelectorPersistsSelectionUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
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

	workdir := t.TempDir()
	database := filepath.Join(t.TempDir(), "atenea.db")
	checkpoints := filepath.Join(t.TempDir(), "checkpoints")
	start := func() (*exec.Cmd, *os.File, *lockedBuffer) {
		cmd := exec.Command(binary)
		cmd.Dir = workdir
		cmd.Env = append(append(os.Environ(), blankProviderKeys()...), "XDG_CONFIG_HOME="+configRoot, "ATENEA_DB="+database, "ATENEA_CHECKPOINTS="+checkpoints)
		terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 24})
		if err != nil {
			t.Fatal(err)
		}
		output := &lockedBuffer{}
		copyPTYAnsweringTerminalQueries(terminal, output)
		return cmd, terminal, output
	}

	firstCmd, firstTerminal, firstOutput := start()
	waitForPTYText(t, firstOutput, " old ─┘")
	if _, err := firstTerminal.Write([]byte("/reasoning:high\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, firstOutput, " old(high) ─┘")
	quitPTY(t, firstTerminal, firstOutput)
	if err := firstCmd.Wait(); err != nil {
		t.Fatal(err)
	}
	_ = firstTerminal.Close()

	cmd, terminal, output := start()
	defer stopPTYProcess(cmd, terminal)
	waitForPTYText(t, output, " old(high) ─┘")
	if _, err := terminal.Write([]byte("/")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "/learn")
	if _, err := terminal.Write([]byte("model new\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "/model local new")
	if _, err := terminal.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, " new ─┘")

	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), `"model": "new"`) || strings.Contains(string(persisted), "reasoning_effort") {
		t.Fatalf("model selection and default effort were not persisted:\n%s", persisted)
	}
}

func TestTUI_DefaultOpenRouterModelsShowContextUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	cmd := exec.Command(binary)
	cmd.Dir = t.TempDir()
	cmd.Env = append(append(os.Environ(), blankProviderKeys()...), "XDG_CONFIG_HOME="+t.TempDir(), "OPENROUTER_API_KEY=test", "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"), "ATENEA_CHECKPOINTS="+filepath.Join(t.TempDir(), "checkpoints"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	output := &lockedBuffer{}
	copyPTYAnsweringTerminalQueries(terminal, output)
	waitForPTYText(t, output, " openrouter/free ─┘")
	if _, err := terminal.Write([]byte("/model ")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tencent/hy3:free", "poolside/laguna-xs-2.1:free", "262K context"} {
		waitForPTYText(t, output, want)
	}
	// The complete built-in catalog is taller than this PTY. Filter through the
	// public composer seam instead of assuming every model fits in the initial
	// viewport, then verify the last OpenRouter model keeps its context label.
	if _, err := terminal.Write([]byte("cohere")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cohere/north-mini-code:free", "256K context"} {
		waitForPTYText(t, output, want)
	}
}

func TestTUI_FocusedComposerShowsBlinkingCursorUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	workdir := repoRoot
	cmd := exec.Command(binary)
	cmd.Dir = workdir
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "NO_COLOR=") {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	cmd.Env = append(append(cmd.Env, blankProviderKeys()...), "TERM=xterm-256color", "CLICOLOR_FORCE=1", "XDG_CONFIG_HOME="+t.TempDir(), "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"), "ATENEA_CHECKPOINTS="+filepath.Join(t.TempDir(), "checkpoints"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	output := &lockedBuffer{}
	copyPTYAnsweringTerminalQueries(terminal, output)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), "\x1b[7m") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("focused composer never rendered an ANSI reverse-video cursor; raw PTY output:\n%q", output.String())
}

func TestTUI_EnablesTerminalFocusReportingUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	workdir := repoRoot
	cmd, terminal, output, _ := startTUIUnderPTY(t, binary, workdir, filepath.Join(t.TempDir(), "atenea.db"))
	defer stopPTYProcess(cmd, terminal)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), "\x1b[?1004h") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("TUI never enabled terminal focus reporting; raw PTY output:\n%q", output.String())
}

func TestTUI_CtrlJCreatesMultilineComposerUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	workdir := repoRoot
	cmd, terminal, output, _ := startTUIUnderPTY(t, binary, workdir, filepath.Join(t.TempDir(), "atenea.db"))
	defer stopPTYProcess(cmd, terminal)
	waitForPTYText(t, output, " demo ─┘")

	before := output.String()
	if _, err := terminal.Write([]byte("primera linea\x0asegunda linea\x0atercera linea\x0acuarta linea")); err != nil {
		t.Fatal(err)
	}
	latest := ansi.Strip(waitForStablePTYOutputAfter(t, output, before))
	for _, line := range []string{"primera linea", "segunda linea", "tercera linea", "cuarta linea"} {
		if !strings.Contains(latest, line) {
			t.Fatalf("Ctrl+J debe mantener visible %q al crecer a cuatro filas; salida PTY:\n%s", line, latest)
		}
	}
	for _, line := range []string{"segunda linea", "tercera linea", "cuarta linea"} {
		if strings.Contains(latest, "❯ "+line) {
			t.Fatalf("las filas posteriores del composer no deben repetir el prompt antes de %q; salida PTY:\n%s", line, latest)
		}
	}
}

func TestTUI_PlanModeAppearsAfterModelUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	workdir := repoRoot
	cmd, terminal, output, _ := startTUIUnderPTY(t, binary, workdir, filepath.Join(t.TempDir(), "atenea.db"))
	defer stopPTYProcess(cmd, terminal)
	waitForPTYText(t, output, " demo ─┘")

	if _, err := terminal.Write([]byte("\t")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, " demo · plan ─┘")
}

// fakeOpenRouter is a local OpenAI-compatible gateway for the /connect E2E
// flow: GET /v1/key validates the API key (200 good, 401 anything else),
// GET /v1/models lists the catalog, and POST /v1/chat/completions streams one
// SSE completion. Every endpoint checks Authorization, so a chat reply proves
// the stored credential (not the empty environment) reached the wire.
func fakeOpenRouter(goodKey string) *httptest.Server {
	authorized := func(r *http.Request) bool {
		return r.Header.Get("Authorization") == "Bearer "+goodKey
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/key", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			http.Error(w, `{"error":{"message":"invalid key"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"label":"e2e"}}`)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			http.Error(w, `{"error":{"message":"invalid key"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"openrouter/free"}]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			http.Error(w, `{"error":{"message":"invalid key"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"openrouter/free","choices":[{"index":0,"delta":{"role":"assistant","content":"CONNECTED-OK from fake gateway"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"openrouter/free","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	return httptest.NewServer(mux)
}

// TestTUI_ProductionEditApprovalUnderPTY drives the shipped binary through a
// local OpenAI-compatible provider, read provenance, the real permission panel,
// and hashline settlement without bypassing the terminal UI.
func TestTUI_ProductionEditApprovalUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-tags", "production", "-o", binary, "./cmd/atenea")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("production build: %v\n%s", err, output)
	}

	workspace := t.TempDir()
	path := filepath.Join(workspace, "note.txt")
	const oldText, newText = "old value\n", "new value\n"
	if err := os.WriteFile(path, []byte(oldText), 0o640); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []map[string]any
	turn := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		turn++
		current := turn
		mu.Unlock()
		name, arguments := "read", `{"path":"note.txt"}`
		if current > 1 {
			name = "edit"
			patch := "[note.txt#" + hashline.ComputeFileHash(oldText) + "]\nPUT 1.=1:\n+new value"
			encoded, _ := json.Marshal(map[string]string{"input": patch})
			arguments = string(encoded)
		}
		chunk, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("turn-%d", current), "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call-%d", current), "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}}}, "finish_reason": nil}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "atenea")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`{"providers":[{"id":"local","name":"Local","type":"openai-compatible","base_url":%q,"models":["e2e"]}],"selected":{"provider":"local","model":"e2e"},"edit":{"mode":"hashline"}}`, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(configDir, "providers.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary)
	cmd.Dir = workspace
	cmd.Env = append(append(os.Environ(), blankProviderKeys()...), "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+configRoot, "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"), "ATENEA_CHECKPOINTS="+filepath.Join(t.TempDir(), "checkpoints"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 110, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	output := &lockedBuffer{}
	copyPTYAnsweringTerminalQueries(terminal, output)
	waitForPTYText(t, output, " e2e ─┘")
	if _, err := terminal.Write([]byte("update note\r")); err != nil {
		t.Fatal(err)
	}
	// Wait for the complete first approval card, not merely its title. The PTY
	// reader can receive the title before Bubble Tea finishes the frame.
	waitForPTYText(t, output, "Permission required")
	waitForPTYText(t, output, "Allow edit this session")
	beforeEditReview := output.String()
	if _, err := terminal.Write([]byte("\x1b[C\r")); err != nil {
		t.Fatal(err)
	}
	// The review is a redraw of the same permission panel. Bubble Tea's
	// line-diff renderer need not emit the unchanged "Permission required"
	// title again, so use the newly visible diff to identify this state.
	waitForPTYTextAfter(t, output, beforeEditReview, "1 + new value")
	if _, err := terminal.Write([]byte("\x1b[C\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		got, readErr := os.ReadFile(path)
		if readErr == nil && string(got) == newText {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("workspace bytes=%q err=%v, want %q; PTY:\n%s", got, readErr, newText, ansi.Strip(output.String()))
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitForPTYTextAfter(t, output, beforeEditReview, "1 + new value")
	mu.Lock()
	defer mu.Unlock()
	if len(requests) < 2 {
		t.Fatalf("provider requests=%d, want read and edit turns", len(requests))
	}
	for i, name := range []string{"read", "edit"} {
		encoded, _ := json.Marshal(requests[i]["tools"])
		if !strings.Contains(string(encoded), `"name":"`+name+`"`) {
			t.Fatalf("request %d tools do not advertise %s: %s", i+1, name, encoded)
		}
	}
}

// TestTUI_ConnectCommandFullFlowUnderPTY pins the /connect journey end to end
// through the real binary, exactly as a user drives it: launch with no key
// anywhere (demo mode with the /connect notice), open the panel, fail with a
// wrong key, retry with the right one, and land connected — credential
// persisted privately, selection saved, the live provider swapped without a
// restart (the next chat reply comes from the connected gateway), and the
// panel reflecting the connected state when reopened.
func TestTUI_ConnectCommandFullFlowUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}

	const goodKey = "sk-or-good-e2e"
	gateway := fakeOpenRouter(goodKey)
	defer gateway.Close()

	// The user's config shape before ever connecting: OpenRouter declared (its
	// base_url pointed at the fake gateway) but no selection and no credential.
	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "atenea")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"providers":[{"id":"openrouter","name":"OpenRouter","type":"openai-compatible","base_url":"` + gateway.URL + `/v1","api_key_env":"OPENROUTER_API_KEY","openrouter_reasoning":true,"models":["openrouter/free"]},{"id":"opencode","name":"OpenCode Zen","type":"openai-compatible","base_url":"https://opencode.ai/zen/v1","api_key_env":"OPENCODE_API_KEY","models":["big-pickle"]},{"id":"opencode-go","name":"OpenCode Go","type":"openai-compatible","base_url":"https://opencode.ai/zen/go/v1","api_key_env":"OPENCODE_API_KEY","models":["kimi-k2.7-code"]}]}`
	if err := os.WriteFile(filepath.Join(configDir, "providers.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binary)
	// Inside the repo so prompt checkpoints find a Git workspace, like the
	// other PTY tests.
	cmd.Dir = repoRoot
	cmd.Env = append(append(os.Environ(), blankProviderKeys()...), "XDG_CONFIG_HOME="+configRoot, "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"), "ATENEA_CHECKPOINTS="+filepath.Join(t.TempDir(), "checkpoints"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	output := &lockedBuffer{}
	copyPTYAnsweringTerminalQueries(terminal, output)

	// No key anywhere: demo mode, and the launch notice points at /connect.
	waitForPTYText(t, output, " demo ─┘")
	waitForPTYText(t, output, "run /connect to connect an LLM provider")

	// /connect opens the panel on the provider list, not yet connected.
	if _, err := terminal.Write([]byte("/connect\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "Connect Provider")
	waitForPTYText(t, output, "❯   OpenRouter")
	waitForPTYText(t, output, "    OpenAI")
	waitForPTYText(t, output, "    OpenCode Zen")
	waitForPTYText(t, output, "    OpenCode Go")
	waitForPTYText(t, output, "not connected")

	// Enter on the provider opens the masked key entry.
	if _, err := terminal.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "API key:")

	// Edge case: a wrong key is rejected by the gateway and the entry stays
	// open for a retry instead of dumping the user back to the chat.
	if _, err := terminal.Write([]byte("sk-or-bad-e2e\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "invalid API key")

	// Ctrl+U clears the rejected key; the right one connects and activates the
	// provider's default model without a restart.
	if _, err := terminal.Write([]byte("\x15" + goodKey + "\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "Connected to OpenRouter · openrouter/free")
	waitForPTYText(t, output, " openrouter/free ─┘")

	// The credential is persisted privately and the selection is saved.
	credentialsPath := filepath.Join(configDir, "credentials.json")
	credentials, err := os.ReadFile(credentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(credentials), goodKey) {
		t.Fatalf("credentials.json does not hold the connected key:\n%s", credentials)
	}
	if info, err := os.Stat(credentialsPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials.json permissions = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	persisted, err := os.ReadFile(filepath.Join(configDir, "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), `"provider": "openrouter"`) {
		t.Fatalf("selection was not persisted after /connect:\n%s", persisted)
	}

	// The live provider swapped: the next chat turn streams from the fake
	// gateway using the stored credential (the environment key is empty).
	before := output.String()
	if _, err := terminal.Write([]byte("hola\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, output, before, "CONNECTED-OK from fake gateway")

	// Reopening the panel reflects the stored credential.
	if _, err := terminal.Write([]byte("/connect\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "● OpenRouter")
}

// fakeChatGPTAuth is OpenAI's device-code authorization server, locally. The token
// endpoint answers "not approved yet" until approve is closed, which is what lets
// the test see the code on screen before the login completes — the state a real
// user spends the whole flow in.
//
// The access token is an unsigned JWT carrying the account claim; nothing verifies
// these signatures, on purpose, because we are not their audience.
func fakeChatGPTAuth(approve <-chan struct{}, userCode, refreshToken string) *httptest.Server {
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"chatgpt_account_id":"acct_e2e"}`))
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/deviceauth/usercode", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"device_auth_id":"deviceauth_e2e","user_code":%q,"interval":"1","expires_at":%q}`,
			userCode, time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339))
	})
	mux.HandleFunc("/api/accounts/deviceauth/token", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case <-approve:
			fmt.Fprint(w, `{"authorization_code":"ac_e2e","code_verifier":"cv_e2e"}`)
		default:
			// The status a login nobody has approved yet really answers with.
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"detail":"not approved"}`)
		}
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"access_token":"h.%s.s","refresh_token":%q,"id_token":"h.%s.s","expires_in":3600}`,
			claims, refreshToken, claims)
	})
	return httptest.NewServer(mux)
}

// fakeCodexBackend is the backend a subscription talks to: one Responses turn over
// SSE. It refuses a request that does not carry the bearer AND the account header,
// so a reply proves the whole credential — not just the token — reached the wire.
func fakeCodexBackend() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer h.") || r.Header.Get("chatgpt-account-id") != "acct_e2e" {
			http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: "+
			`{"type":"response.output_text.delta","sequence_number":1,"item_id":"m","output_index":0,"content_index":0,"delta":"SUBSCRIPTION-OK from fake codex"}`+"\n\n")
		fmt.Fprint(w, "event: response.completed\ndata: "+
			`{"type":"response.completed","sequence_number":2,"response":{"id":"r","status":"completed","usage":{"input_tokens":8,"output_tokens":4,"total_tokens":12}}}`+"\n\n")
	})
	return httptest.NewServer(mux)
}

// TestTUI_ChatGPTSubscriptionLoginUnderPTY pins the other half of /connect through
// the real binary: a provider connected by approving a code somewhere else rather
// than by pasting a secret.
//
// It drives what a user actually does — run /connect, read the code, approve it
// elsewhere — and then insists on the four things that make the feature real rather
// than demonstrated: the code and the page are on screen, no token ever is, the
// credential lands in credentials.json as an oauth arm at 0600, and the very next
// prompt streams from the codex backend using it.
func TestTUI_ChatGPTSubscriptionLoginUnderPTY(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atenea")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}

	const userCode = "V3H5-1MW96"
	const refreshToken = "refresh-token-e2e-secret"
	approve := make(chan struct{})
	auth := fakeChatGPTAuth(approve, userCode, refreshToken)
	defer auth.Close()
	backend := fakeCodexBackend()
	defer backend.Close()

	// The subscription declared with its issuer and its backend pointed at the local
	// stubs. It is the only provider, so there is nothing else the flow could pick.
	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "atenea")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"providers":[{"id":"openai-codex","name":"OpenAI (ChatGPT subscription)","type":"openai-codex","base_url":"` +
		backend.URL + `","oauth_issuer":"` + auth.URL + `","disable_model_discovery":true,"models":["gpt-5.5"]}]}`
	if err := os.WriteFile(filepath.Join(configDir, "providers.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binary)
	cmd.Dir = repoRoot
	cmd.Env = append(append(os.Environ(), blankProviderKeys()...), "XDG_CONFIG_HOME="+configRoot,
		"ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"), "ATENEA_CHECKPOINTS="+filepath.Join(t.TempDir(), "checkpoints"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	output := &lockedBuffer{}
	copyPTYAnsweringTerminalQueries(terminal, output)

	waitForPTYText(t, output, " demo ─┘")

	// The panel says the subscription is not connected, and selecting it starts the
	// login straight away: there is no key to type.
	if _, err := terminal.Write([]byte("/connect\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "❯   OpenAI (ChatGPT subscription)")
	if _, err := terminal.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, userCode)
	waitForPTYText(t, output, "/codex/device")
	waitForPTYText(t, output, "waiting for approval")
	if strings.Contains(ansi.Strip(output.String()), "API key:") {
		t.Fatalf("the subscription must not be offered a key entry:\n%s", ansi.Strip(output.String()))
	}

	// The user approves the code in their browser. The next poll is an interval
	// away, which is why this wait is longer than the UI's own.
	close(approve)
	waitForPTYTextWithin(t, output, "Connected to OpenAI (ChatGPT subscription)", 30*time.Second)
	waitForPTYText(t, output, " gpt-5.5 ─┘")

	// The credential is stored as its own arm, privately, and the selection is saved.
	credentialsPath := filepath.Join(configDir, "credentials.json")
	credentials, err := os.ReadFile(credentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(credentials), `"type": "oauth"`) || !strings.Contains(string(credentials), refreshToken) {
		t.Fatalf("credentials.json does not hold the subscription login:\n%s", credentials)
	}
	if info, err := os.Stat(credentialsPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials.json permissions = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	persisted, err := os.ReadFile(filepath.Join(configDir, "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), `"provider": "openai-codex"`) {
		t.Fatalf("selection was not persisted after the login:\n%s", persisted)
	}

	// Nothing secret was ever drawn. The code is single-use and worthless without the
	// account that approves it; the tokens are not, and a terminal gets scrolled
	// back, screenshotted and pasted into issues.
	screen := ansi.Strip(output.String())
	for _, secret := range []string{refreshToken, "ac_e2e", "cv_e2e", "acct_e2e"} {
		if strings.Contains(screen, secret) {
			t.Fatalf("the terminal rendered the secret %q:\n%s", secret, screen)
		}
	}

	// The live provider swapped: the next turn streams from the codex backend, which
	// answers only a request carrying both halves of the credential.
	before := output.String()
	if _, err := terminal.Write([]byte("hola\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, output, before, "SUBSCRIPTION-OK from fake codex")

	// Reopening the panel reflects the stored login.
	if _, err := terminal.Write([]byte("/connect\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "● OpenAI (ChatGPT subscription)")
}

func waitForPTYText(t *testing.T, output *lockedBuffer, want string) {
	t.Helper()
	waitForPTYTextWithin(t, output, want, 3*time.Second)
}

// waitForPTYTextWithin is waitForPTYText with the deadline named, for the waits
// whose length is not the UI's to control: an OAuth device flow only asks the
// authorization server again after the interval it was given, so the second poll
// is seconds away by protocol rather than by slowness.
func waitForPTYTextWithin(t *testing.T, output *lockedBuffer, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(ansi.Strip(output.String()), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("PTY output did not contain %q:\n%s", want, ansi.Strip(output.String()))
}

func startTUIUnderPTY(t *testing.T, binary, workdir, database string, args ...string) (*exec.Cmd, *os.File, *lockedBuffer, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = workdir
	// These tests depend on the demo provider: every API key the built-in catalog
	// reads is blanked and XDG_CONFIG_HOME is isolated, so neither the environment
	// nor the developer's real providers.json can slip a network provider in.
	//
	// HOME is isolated for the same reason, one level up: skill discovery scans
	// $HOME/.atenea/skills, $HOME/.agents/skills and $HOME/.claude/skills, so
	// whatever the developer happens to have installed there was reaching the "/"
	// menu and the system prompt of these tests.
	dataHome := t.TempDir()
	cmd.Env = append(append(os.Environ(), blankProviderKeys()...),
		"HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir(), "XDG_DATA_HOME="+dataHome,
		"ATENEA_DB="+database, "ATENEA_CHECKPOINTS="+filepath.Join(filepath.Dir(database), "checkpoints"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	output := &lockedBuffer{}
	done := copyPTYAnsweringTerminalQueries(terminal, output)
	return cmd, terminal, output, done
}

// Consultas de estado que el TUI emite al arrancar: el init de bubbletea
// (via termenv) pregunta al terminal el color de fondo (OSC 11), a veces el
// de primer plano (OSC 10) y la posicion del cursor (DSR \x1b[6n), y se
// bloquea hasta 5 segundos esperando cada respuesta.
var terminalStatusQueries = []struct {
	query    string
	response string
}{
	{"\x1b]11;?", "\x1b]11;rgb:1414/1414/1414\x1b\\"},
	{"\x1b]10;?", "\x1b]10;rgb:c0c0/c0c0/c0c0\x1b\\"},
	{"\x1b[6n", "\x1b[1;1R"},
}

// copyPTYAnsweringTerminalQueries vuelca en output todo lo que el TUI escribe
// en la PTY y ademas responde las terminalStatusQueries como lo haria un
// terminal real. Sin esas respuestas el TUI queda bloqueado 5 segundos sin
// renderizar nada y los tests solo ven una pantalla vacia. Devuelve un canal
// que se cierra cuando la PTY deja de poder leerse.
func copyPTYAnsweringTerminalQueries(terminal io.ReadWriter, output *lockedBuffer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		var pending []byte
		buffer := make([]byte, 4096)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				_, _ = output.Write(buffer[:n])
				pending = answerTerminalStatusQueries(terminal, append(pending, buffer[:n]...))
			}
			if err != nil {
				return
			}
		}
	}()
	return done
}

// answerTerminalStatusQueries contesta cada consulta completa presente en
// pending y devuelve la cola de bytes sin emparejar, por si una consulta
// llega partida entre dos lecturas de la PTY.
func answerTerminalStatusQueries(terminal io.Writer, pending []byte) []byte {
	for {
		matchIndex, matchLength, response := -1, 0, ""
		for _, status := range terminalStatusQueries {
			index := bytes.Index(pending, []byte(status.query))
			if index >= 0 && (matchIndex < 0 || index < matchIndex) {
				matchIndex, matchLength, response = index, len(status.query), status.response
			}
		}
		if matchIndex < 0 {
			break
		}
		_, _ = terminal.Write([]byte(response))
		pending = pending[matchIndex+matchLength:]
	}
	longestQuery := 0
	for _, status := range terminalStatusQueries {
		if len(status.query) > longestQuery {
			longestQuery = len(status.query)
		}
	}
	if len(pending) >= longestQuery {
		pending = append([]byte(nil), pending[len(pending)-longestQuery+1:]...)
	}
	return pending
}

// El contrato de answerTerminalStatusQueries es sutil: retiene la cola sin
// emparejar para cazar una consulta partida entre dos lecturas de la PTY, y no
// debe re-responder una consulta ya consumida. Los tests PTY end-to-end solo lo
// ejercitan de forma indirecta; este lo fija de forma directa.
func TestAnswerTerminalStatusQueries(t *testing.T) {
	const (
		bgQuery     = "\x1b]11;?"
		bgResponse  = "\x1b]11;rgb:1414/1414/1414\x1b\\"
		fgQuery     = "\x1b]10;?"
		fgResponse  = "\x1b]10;rgb:c0c0/c0c0/c0c0\x1b\\"
		curQuery    = "\x1b[6n"
		curResponse = "\x1b[1;1R"
	)

	cases := []struct {
		name   string
		chunks []string // se alimentan en orden, arrastrando el pending devuelto
		want   string   // respuestas escritas, concatenadas en orden
	}{
		{"consulta completa en un chunk", []string{bgQuery}, bgResponse},
		{"consulta partida entre dos lecturas", []string{"\x1b]11;", "?"}, bgResponse},
		{"consulta partida tras ruido largo", []string{"mucho ruido\x1b]11;", "?"}, bgResponse},
		{"dos consultas distintas seguidas", []string{bgQuery + curQuery}, bgResponse + curResponse},
		{"bytes ajenos alrededor de la consulta", []string{"ruido" + fgQuery + "mas ruido"}, fgResponse},
		{"sin consulta no responde", []string{"texto suelto de la TUI"}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var written strings.Builder
			var pending []byte
			for _, chunk := range tc.chunks {
				pending = answerTerminalStatusQueries(&written, append(pending, chunk...))
			}
			if got := written.String(); got != tc.want {
				t.Fatalf("respuestas escritas = %q, want %q", got, tc.want)
			}
			// Una consulta ya consumida no debe re-responder al llegar mas bytes.
			before := written.Len()
			answerTerminalStatusQueries(&written, append(pending, []byte("cola")...))
			if extra := written.Len() - before; extra != 0 {
				t.Fatalf("una consulta ya consumida no debe re-responder: %d bytes extra", extra)
			}
		})
	}
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
	t.Fatalf("PTY output after interaction did not contain %q:\n%s", want, ansi.Strip(output.String()))
}

func waitForPTYRawAfter(t *testing.T, output *lockedBuffer, previous, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current := output.String()
		if len(current) >= len(previous) && strings.Contains(current[len(previous):], want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("PTY raw output after interaction did not contain %q:\n%q", want, output.String())
}

// quitPTY sends the double Ctrl+C the TUI requires to exit: the first press
// only arms the confirmation notice so a stray Ctrl+C cannot discard a draft.
func quitPTY(t *testing.T, terminal *os.File, output *lockedBuffer) {
	t.Helper()
	before := output.String()
	if _, err := terminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, output, before, "Ctrl+C again to quit")
	if _, err := terminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
}
func stopPTYProcess(cmd *exec.Cmd, terminal *os.File) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = terminal.Close()
	_ = cmd.Wait()
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
			if len(current) > len(previous) && time.Since(quietSince) >= 500*time.Millisecond {
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

// TestIsTerminal_DevNullAndPipesAreNotTerminals pins the check `atenea run` uses to
// decide whether there is anything on the other end of stdin to read a prompt from.
//
// It exists because the first implementation tested for a character device, and
// /dev/null is a character device. That got the safety direction right — a real
// terminal is always a character device, so it never waited on a prompt nobody was
// going to type — while refusing `atenea run < /dev/null`, which is how a CI script
// says "this command has no input and must not block", with the untrue explanation
// that stdin was interactive. Both cases below are reachable from a pipeline, and
// neither is a terminal.
func TestIsTerminal_DevNullAndPipesAreNotTerminals(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()
	if isTerminal(devNull) {
		t.Errorf("isTerminal(%s) = true, want false: it is a character device, not a terminal", os.DevNull)
	}

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer read.Close()
	defer write.Close()
	if isTerminal(read) {
		t.Error("isTerminal(pipe) = true, want false: a piped prompt is the case the flag exists to detect")
	}
}
