package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/K3N4Y/atenea/internal/host"
)

// Env is everything the dispatch takes from the process. Nothing in this package
// reads os.Args, os.Stdout or the real clock, which is what makes the whole
// surface — argument parsing, exit codes, the streams — testable without a
// terminal and without a subprocess.
type Env struct {
	// Args are the arguments after the program name, exactly as they arrived.
	Args []string
	// Stdin is the piped prompt. It is only read when StdinIsTerminal is false.
	Stdin io.Reader
	// Stdout carries the format's output: the answer, the result document, the
	// NDJSON. Nothing else is ever written to it.
	Stdout io.Writer
	// Stderr carries everything a person needs and a consumer does not: warnings,
	// usage, progress, the standard log.
	Stderr io.Writer
	// Version is what `atenea version` prints. It is injected because the values
	// behind it are stamped into package main by the release build, and a version
	// this package invented would be a second answer to the question.
	Version string
	// StdinIsTerminal says whether stdin is a terminal, which decides whether there
	// is a piped prompt to read at all. It is a field rather than a probe so a test
	// can state both cases; the entrypoint resolves it once from os.Stdin.
	StdinIsTerminal bool
	// Interrupts delivers the signals that stop a run. nil installs the real
	// handler for the duration of the run, which is why it is not installed
	// process-wide: the interactive interface has its own handling of Ctrl-C and
	// must not have it taken away.
	Interrupts <-chan os.Signal
	// Interactive launches the terminal interface, which is what a bare `atenea`
	// means. It is injected so this package never imports Bubble Tea: dispatching
	// is choosing, not launching, and a test of the dispatch must not need a
	// terminal.
	Interactive func() error
	// Host assembles the outer composition root. nil is host.New, which is what the
	// entrypoint leaves it as.
	//
	// It exists for the reason host.Config's own Store and Providers fields do: it
	// is not a hook for production behaviour, it is how a test drives the real
	// assembly without the real filesystem. It receives the Config the run
	// resolved, so a test can inject a memory store and a scripted provider while
	// --cwd still decides the root.
	Host func(ctx context.Context, cfg host.Config) *host.Host
}

// assemble builds the host for one run.
func (e Env) assemble(ctx context.Context, cfg host.Config) *host.Host {
	if e.Host != nil {
		return e.Host(ctx, cfg)
	}
	return host.New(ctx, cfg)
}

// command is one subcommand. Adding one is a struct literal in commands below plus
// a function of this shape — the dispatch has no other moving part, which is the
// point of not taking a CLI framework for a surface this size.
type command struct {
	name string
	// summary is one line, for the top-level help. The flags of a command are its
	// own business and documented by its -h.
	summary string
	run     func(env Env, args []string) int
}

// commands is the subcommand table, in help order.
var commands = []command{
	{
		name:    "run",
		summary: "Run one non-interactive turn and report the result",
		run:     runCommand,
	},
	{
		name:    "version",
		summary: "Print the version, commit and build date of this binary",
		run:     versionCommand,
	},
}

// Main dispatches one invocation and returns the process exit code. It never
// calls os.Exit: the entrypoint does that, so every path through here is reachable
// from a test.
func Main(env Env) int {
	if len(env.Args) == 0 {
		return interactive(env)
	}
	name := env.Args[0]
	// --version predates the subcommands and is the public spelling: install.sh
	// verifies an installation with it and the release smoke test asserts its
	// output. It stays an alias of `atenea version` rather than a second
	// implementation.
	if name == "--version" || name == "-version" {
		return versionCommand(env, nil)
	}
	if name == "-h" || name == "--help" || name == "help" {
		usage(env.Stdout, env)
		return ExitOK
	}
	for _, candidate := range commands {
		if candidate.name == name {
			return candidate.run(env, env.Args[1:])
		}
	}
	// A bare `atenea` opens the terminal interface, so an unrecognized first
	// argument used to open it too — the arguments were simply ignored. Reporting
	// it is the difference between a typo that runs the wrong thing and a typo that
	// says so.
	fmt.Fprintf(env.Stderr, "atenea: unknown command %q\n\n", name)
	usage(env.Stderr, env)
	return ExitUsage
}

// interactive runs the terminal interface. A build that did not provide it says so
// rather than doing nothing.
func interactive(env Env) int {
	if env.Interactive == nil {
		fmt.Fprintln(env.Stderr, "atenea: the interactive interface is unavailable in this build; try `atenea run -h`")
		return ExitUsage
	}
	if err := env.Interactive(); err != nil {
		fmt.Fprintln(env.Stderr, "atenea:", err)
		// The interface failing is the generic failure of what the command was asked
		// to do, which is the same 1 a failed turn reports and the same 1 this
		// entrypoint has always exited with.
		return ExitTurnFailed
	}
	return ExitOK
}

func versionCommand(env Env, _ []string) int {
	fmt.Fprintln(env.Stdout, env.Version)
	return ExitOK
}

func usage(w io.Writer, env Env) {
	fmt.Fprint(w, `atenea — a coding agent, in the terminal and headless.

Usage:
  atenea                    start the interactive terminal interface
  atenea <command> [flags]

Commands:
`)
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, candidate := range commands {
		fmt.Fprintf(table, "  %s\t%s\n", candidate.name, candidate.summary)
	}
	// --version is listed with the commands because that is what it is: the one
	// spelling of one of them that is not a subcommand, kept because install.sh and
	// the release check call it.
	fmt.Fprintf(table, "  %s\t%s\n", "--version", `Alias of "atenea version"`)
	_ = table.Flush()
	fmt.Fprint(w, `
Run "atenea <command> -h" for the flags of one command.
`)
}

// interrupts resolves the channel a run watches for a stop, and the cleanup that
// removes the handler again.
//
// The real handler is installed here, per run, rather than in the entrypoint. A
// process-wide handler would take SIGINT away from the interactive interface,
// which reads Ctrl-C itself, and reinstalling it would be one more thing the two
// paths could disagree about.
func (e Env) interrupts() (<-chan os.Signal, func()) {
	if e.Interrupts != nil {
		return e.Interrupts, func() {}
	}
	// Buffered, and both signals are watched: a second Ctrl-C has to be observable
	// while the first one is being handled, or an operator who wants out now would
	// have no way to say so.
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return signals, func() { signal.Stop(signals) }
}
