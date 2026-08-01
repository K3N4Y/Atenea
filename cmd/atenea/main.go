// Command atenea is the atenea agent's command-line binary. It is the thin
// boundary equivalent to the Wails main.go: it hands the process — its arguments,
// its streams and its version — to internal/cli, which dispatches to a subcommand
// or, with no arguments, to the terminal interface assembled here.
//
// Everything testable lives behind that line: internal/cli owns the argument
// surface and the headless run, internal/tui owns the interactive one, and what
// stays in this file is only what needs the real process.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/K3N4Y/atenea/internal/checkpoint"
	"github.com/K3N4Y/atenea/internal/cli"
	"github.com/K3N4Y/atenea/internal/host"
	"github.com/K3N4Y/atenea/internal/paths"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tui"
	"github.com/K3N4Y/atenea/internal/tui/engine"
)

func main() {
	os.Exit(cli.Main(cli.Env{
		Args:            os.Args[1:],
		Stdin:           os.Stdin,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
		Identity:        paths.NewIdentity(version),
		Version:         versionString(),
		StdinIsTerminal: isTerminal(os.Stdin),
		Interactive:     runInteractive,
	}))
}

// isTerminal reports whether f is a terminal rather than a pipe, a file or a
// device, which is how `atenea run` knows whether there is anything on the other
// end of stdin to read a prompt from.
//
// It asks the terminal driver (an ioctl, through x/term) rather than testing for a
// character device, because /dev/null is a character device and is not a terminal.
// The heuristic got the safety direction right — a real terminal is always a
// character device, so it never read a prompt nobody was going to type — but it
// reported the wrong reason for the wrong inputs: `atenea run </dev/null`, the
// standard way a CI script guarantees a command cannot block, was refused for
// being interactive. Now it reads the empty device and says the prompt was empty,
// which is what happened.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func runInteractive() error {
	// The standard log (tool failures, skills that failed to be discovered) would go
	// to stderr and paint over Bubble Tea's alternate screen: it is redirected to a
	// file. This happens FIRST, so every warning the host bootstrap emits — an
	// unopenable SQLite, a provider config that will not load — lands in the file
	// instead of on the user's screen.
	//
	// A headless run does the opposite and leaves the log on stderr, because there
	// is no screen to corrupt and a CI job's diagnostics belong in its output. That
	// is why the redirection lives here and not in main.
	redirectLog()

	// The shared outer assembly: the .env of the working directory, the built-in
	// skills, the workspace root, the SQLite store and the provider service the
	// desktop app also reads, and the sitting. See internal/host.
	h := host.New(context.Background(), host.Config{
		Identity:             paths.NewIdentity(version),
		Dotenv:               ".env",
		ExtractBuiltinSkills: true,
	})
	// The active selection is read ONCE: the same value feeds the engine and the
	// composer footer.
	active := h.Providers.Active()

	eng := engine.New(engine.Config{
		Identity:    h.Identity,
		Root:        h.Root,
		Provider:    h.Providers.Provider(),
		Store:       h.Store,
		Models:      h.Providers,
		Checkpoints: checkpoint.NewGitStore(session.DefaultCheckpointPath()),
		Sitting:     h.Sitting,
	})
	history, err := eng.PromptHistory()
	if err != nil {
		log.Printf("atenea: could not load the composer history: %v", err)
	}

	// Every launch starts a fresh conversation: no transcript from previous
	// runs on screen. Older sessions of this workspace stay one /resume away.
	sessionID := eng.NewSessionID()

	// The composer's autocompletion comes from the engine: the skills' slash
	// commands for the "/" menu and the workspace listing for the @-menu.
	m := tui.NewModel(eng, sessionID, eng.Events()).
		WithHistory(history).
		WithStatus("build", active.Model).
		WithWorkspaceRoot(gitBranch(h.Root), displayDir(h.Root), h.Root).
		WithCompletions(eng.Commands(), eng.ProjectFiles)
	// Starting on the offline provider means there is no key anywhere (neither
	// environment nor stored credential): say so, and say how to get out of it,
	// instead of letting the user chat with the fake and find out the hard way.
	if active.ProviderID == host.OfflineProviderID {
		m = m.WithNotice("No provider connected — run /connect to connect an LLM provider. Demo mode: replies are canned.")
	}
	// WithMouseCellMotion enables mouse tracking: without it the terminal never
	// reports the wheel to the app (on the alternate screen it translates it to
	// arrows via "alternate scroll"); with the option real mouse events arrive.
	_, runErr := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus()).Run()
	shutdownErr := eng.Shutdown(context.Background())
	return errors.Join(runErr, shutdownErr, h.Close())
}

// gitBranch returns the current git branch of the repo at root (git rev-parse
// --abbrev-ref HEAD), or "" on any error and when root is not a repo. The top bar
// shows it on the left.
func gitBranch(root string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// displayDir abbreviates the home prefix to "~" for the working directory shown
// in the top bar; with no resolvable home or no common prefix it returns root
// unchanged.
func displayDir(root string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return root
	}
	if root == home {
		return "~"
	}
	if strings.HasPrefix(root, home+"/") {
		return "~/" + root[len(home)+1:]
	}
	return root
}

// redirectLog sends the standard log to a file in the temporary directory so it
// does not corrupt the terminal's rendering. If it cannot be opened, the log is
// discarded rather than painted over the screen.
func redirectLog() {
	path := filepath.Join(os.TempDir(), "atenea.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.SetOutput(devNull{})
		return
	}
	log.SetOutput(f)
}

// devNull discards the log when not even the temporary file could be opened.
type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }
