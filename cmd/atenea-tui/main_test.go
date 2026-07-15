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

func TestEnvironmentFallbackSnapshot_UsesCurrentOpenAIDefault(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("OPENAI_MODEL", "")

	got := environmentFallbackSnapshot()
	if got.ProviderID != "openai" || got.Model != "gpt-5.6-terra" {
		t.Fatalf("fallback = %#v, want OpenAI gpt-5.6-terra", got)
	}
}

func TestDefaultProviderConfig_UsesCuratedOpenAIModels(t *testing.T) {
	cfg := defaultProviderConfig()
	if len(cfg.Providers) != 2 {
		t.Fatalf("providers = %#v, want OpenRouter and OpenAI", cfg.Providers)
	}
	openAI := cfg.Providers[1]
	want := []string{"gpt-5.6", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o", "gpt-4o-mini"}
	if !openAI.DisableModelDiscovery {
		t.Fatal("OpenAI model discovery must stay disabled because GET /models includes incompatible model types")
	}
	if strings.Join(openAI.Models, ",") != strings.Join(want, ",") {
		t.Fatalf("OpenAI models = %#v, want %#v", openAI.Models, want)
	}
}

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
	beforeSubmit := firstOutput.String()
	if _, err := firstTerminal.Write([]byte("mensaje persistente\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, firstOutput, beforeSubmit, "Hola desde atenea.")
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

func TestTUI_ResumesLatestWorkspaceSessionUnderPTY(t *testing.T) {
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
	beforeSubmit := firstOutput.String()
	if _, err := firstTerminal.Write([]byte("\tcontinuidad tui\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, firstOutput, beforeSubmit, "Hola desde atenea.")
	if _, err := firstTerminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitForPTYExit(t, firstDone)
	_ = firstTerminal.Close()
	_ = firstCmd.Wait()

	secondCmd, secondTerminal, secondOutput, secondDone := startTUIUnderPTY(t, binary, workdir, database)
	defer stopPTYProcess(secondCmd, secondTerminal)
	waitForPTYText(t, secondOutput, "continuidad tui")
	waitForPTYText(t, secondOutput, " demo · plan ─╯")
	if _, err := secondTerminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitForPTYExit(t, secondDone)
}

func TestTUI_ResumeCommandOpensPreviousWorkspaceSessionUnderPTY(t *testing.T) {
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
	if _, err := firstTerminal.Write([]byte("\tsesion anterior\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, firstOutput, "sesion anterior")
	if _, err := firstTerminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitForPTYExit(t, firstDone)
	_ = firstTerminal.Close()
	_ = firstCmd.Wait()

	secondCmd, secondTerminal, secondOutput, secondDone := startTUIUnderPTY(t, binary, workdir, database)
	waitForPTYText(t, secondOutput, "sesion anterior")
	if _, err := secondTerminal.Write([]byte("/new\r")); err != nil {
		t.Fatal(err)
	}
	if _, err := secondTerminal.Write([]byte("sesion actual\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, secondOutput, "sesion actual")
	if _, err := secondTerminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitForPTYExit(t, secondDone)
	_ = secondTerminal.Close()
	_ = secondCmd.Wait()

	thirdCmd, thirdTerminal, thirdOutput, thirdDone := startTUIUnderPTY(t, binary, workdir, database)
	defer stopPTYProcess(thirdCmd, thirdTerminal)
	waitForPTYText(t, thirdOutput, "sesion actual")
	before := thirdOutput.String()
	if _, err := thirdTerminal.Write([]byte("/resume\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, thirdOutput, before, "sesion anterior")
	if _, err := thirdTerminal.Write([]byte("\x1b[B\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYTextAfter(t, thirdOutput, before, " demo · plan ─╯")
	if _, err := thirdTerminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitForPTYExit(t, thirdDone)
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
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configRoot, "OPENROUTER_API_KEY=", "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"), "ATENEA_CHECKPOINTS="+filepath.Join(t.TempDir(), "checkpoints"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	output := &lockedBuffer{}
	copyPTYAnsweringTerminalQueries(terminal, output)
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
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+t.TempDir(), "OPENROUTER_API_KEY=test", "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"), "ATENEA_CHECKPOINTS="+filepath.Join(t.TempDir(), "checkpoints"))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	output := &lockedBuffer{}
	copyPTYAnsweringTerminalQueries(terminal, output)
	waitForPTYText(t, output, " openrouter/free ─╯")
	if _, err := terminal.Write([]byte("/model ")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tencent/hy3:free", "poolside/laguna-xs-2.1:free", "cohere/north-mini-code:free", "262K context", "256K context"} {
		waitForPTYText(t, output, want)
	}
}

func TestTUI_FocusedComposerShowsBlinkingCursorUnderPTY(t *testing.T) {
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
	cmd := exec.Command(binary)
	cmd.Dir = workdir
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "NO_COLOR=") {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color", "CLICOLOR_FORCE=1", "OPENROUTER_API_KEY=", "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"), "ATENEA_CHECKPOINTS="+filepath.Join(t.TempDir(), "checkpoints"))
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
	binary := filepath.Join(t.TempDir(), "atenea-tui")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(repoRoot, "cmd/atenea-tui")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	workdir := filepath.Join(repoRoot, "cmd/atenea-tui/testdata/file-viewer/project")
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

func TestTUI_PlanModeAppearsAfterModelUnderPTY(t *testing.T) {
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

	if _, err := terminal.Write([]byte("\t")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, " demo · plan ─╯")
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
	workdir := filepath.Join(repoRoot, "cmd/atenea-tui/testdata/file-viewer/project")
	cmd, terminal, output, done := startTUIUnderPTY(t, binary, workdir, filepath.Join(t.TempDir(), "atenea.db"))
	defer stopPTYProcess(cmd, terminal)
	waitForPTYText(t, output, " demo ─╯")
	if _, err := terminal.Write([]byte(" e\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "hello.go")
	if _, err := terminal.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "hello.go · 1-3/3")
	waitForPTYText(t, output, "hello from file viewer")
	if _, err := terminal.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, " demo ─╯")
	if _, err := terminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitForPTYExit(t, done)
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
	// El chrome de la top bar toma dos filas del cuerpo: el visor a pantalla
	// completa muestra dos lineas menos, asi que la ventana al fondo abre en la
	// linea 15 (no 13).
	waitForPTYText(t, output, "long.txt · 15-31/31")
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
	cmd.Env = append(os.Environ(), "OPENROUTER_API_KEY=", "ATENEA_DB="+filepath.Join(t.TempDir(), "atenea.db"), "ATENEA_CHECKPOINTS="+filepath.Join(t.TempDir(), "checkpoints"))
	// Rows: 11 = 8 filas de cuerpo (la geometria del arbol/visor que este test
	// ejercita) mas las 3 filas del chrome de la top bar; asi el cuerpo conserva
	// el mismo alto que antes de la barra y los clics de mouse suman 3 a su fila.
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 11})
	if err != nil {
		t.Fatal(err)
	}
	defer stopPTYProcess(cmd, terminal)
	output := &lockedBuffer{}
	done := copyPTYAnsweringTerminalQueries(terminal, output)
	waitForPTYText(t, output, " demo ─╯")
	if _, err := terminal.Write([]byte(" e")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "file-00.go")
	// El chrome de la top bar ocupa las filas 1-3 (SGR 1-based) de la pantalla,
	// asi que el cuerpo (arbol y visor) empieza tres filas mas abajo: cada evento
	// de mouse suma 3 a su fila respecto al layout sin barra.
	if _, err := terminal.Write([]byte("\x1b[<65;1;7M\x1b[<65;1;7M\x1b[<0;25;7M")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "file-03.go · 1-1/1")
	waitForPTYText(t, output, "package file03")
	if _, err := terminal.Write([]byte("\x1b[<0;25;9M")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "file-05.go · 1-1/1")
	waitForPTYText(t, output, "package file05")
	if _, err := terminal.Write([]byte("\x1b[<0;50;7M")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "viewer *")
	waitForPTYText(t, output, "file-05.go · 1-1/1")
	if _, err := terminal.Write([]byte("\x1b[<0;1;4M")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, output, "explorer *")
	waitForPTYText(t, output, "file-05.go · 1-1/1")
	if _, err := terminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitForPTYExit(t, done)
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
	workdir := filepath.Join(repoRoot, "cmd/atenea-tui/testdata/file-viewer/project")
	cmd, terminal, output, done := startTUIUnderPTY(t, binary, workdir, filepath.Join(t.TempDir(), "atenea.db"))
	defer stopPTYProcess(cmd, terminal)
	waitForPTYText(t, output, " demo ─╯")

	before := output.String()
	if _, err := terminal.Write(bytes.Repeat([]byte(" e"), 2001)); err != nil {
		t.Fatal(err)
	}
	latest := waitForStablePTYOutputAfter(t, output, before)
	if !strings.Contains(latest, "explorer *") || !strings.Contains(latest, "hello.go") {
		t.Fatalf("rapid Space+e sequences should leave explorer open after an odd count; latest PTY output:\n%s", latest)
	}

	before = output.String()
	if _, err := terminal.Write([]byte(" e")); err != nil {
		t.Fatal(err)
	}
	latest = waitForStablePTYOutputAfter(t, output, before)
	if strings.Contains(latest, "explorer") || strings.Contains(latest, "hello.go") {
		t.Fatalf("one more Space+e sequence should close the explorer after the rapid burst; latest PTY output:\n%s", latest)
	}
	if _, err := terminal.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitForPTYExit(t, done)
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
	cmd.Env = append(os.Environ(), "OPENROUTER_API_KEY=", "ATENEA_DB="+database, "ATENEA_CHECKPOINTS="+filepath.Join(filepath.Dir(database), "checkpoints"))
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
