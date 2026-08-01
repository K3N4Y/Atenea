package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/K3N4Y/atenea/internal/host"
	"github.com/K3N4Y/atenea/internal/paths"
)

// Env is everything the dispatch takes from the process. Nothing in this package
// reads os.Args, os.Stdout or the real clock, which is what makes the whole
// surface — argument parsing, exit codes, the streams — testable without a
// terminal and without a subprocess.
type Env struct {
	// Identity is the release identity passed through to integrations opened by a
	// headless host. Its zero value identifies a development build.
	Identity paths.Identity
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
	Interactive func(InteractiveOptions) error
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
		name:    "mcp",
		summary: "List, declare and delete the MCP servers atenea can connect",
		run:     mcpCommand,
	},
	{
		name:    "skill",
		summary: "List the discovered skills, and report the ones that will not load",
		run:     skillCommand,
	},
	{
		name:    "agent",
		summary: "Validate the discovered or named subagent definitions",
		run:     agentCommand,
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
		return interactive(env, InteractiveOptions{})
	}
	name := env.Args[0]
	if (name == "--yolo" || name == "--dangerously-skip-permissions") && len(env.Args) == 1 {
		return interactive(env, InteractiveOptions{Yolo: true})
	}
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
type InteractiveOptions struct {
	Yolo bool
}

func interactive(env Env, options InteractiveOptions) int {
	if env.Interactive == nil {
		fmt.Fprintln(env.Stderr, "atenea: the interactive interface is unavailable in this build; try `atenea run -h`")
		return ExitUsage
	}
	if err := env.Interactive(options); err != nil {
		fmt.Fprintln(env.Stderr, "atenea:", err)
		// The interface failing is the generic failure of what the command was asked
		// to do, which is the same 1 a failed turn reports and the same 1 this
		// entrypoint has always exited with.
		return ExitFailure
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
  atenea --yolo             start interactively and allow almost all tool calls
  atenea --dangerously-skip-permissions
                            alias of --yolo
  atenea <command> [flags]

Commands:
`)
	// --version is listed with the commands because that is what it is: the one
	// spelling of one of them that is not a subcommand, kept because install.sh and
	// the release check call it.
	commandTable(w, commands, command{name: "--version", summary: `Alias of "atenea version"`})
	fmt.Fprint(w, `
Run "atenea <command> -h" for the flags of one command.
`)
}

// commandTable writes the "Commands:" block. Every help screen is generated from
// the table it dispatches on, at both levels, so a command cannot be added
// without appearing in its own help.
func commandTable(w io.Writer, table []command, extras ...command) {
	tabbed := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, candidate := range table {
		fmt.Fprintf(tabbed, "  %s\t%s\n", candidate.name, candidate.summary)
	}
	for _, candidate := range extras {
		fmt.Fprintf(tabbed, "  %s\t%s\n", candidate.name, candidate.summary)
	}
	_ = tabbed.Flush()
}

// verbs is the second dispatch level: `atenea mcp list` resolved the same way
// `atenea run` is, one level down. It is the same table, the same generated help
// and the same errors, because a wrong sub-verb deserves the answer a wrong
// command gets — a group whose mistakes read worse than the top level's would be
// two dispatches with one name.
//
// A group with no verb is a usage error rather than a default verb. Picking one
// (list, say) would make `atenea mcp` mean something the user did not type, and
// the reason the top level does not do that either: a bare `atenea` is the
// interactive interface because that is what it has always meant, not because a
// dispatch chose a favourite.
func verbs(env Env, path, blurb string, table []command, args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(env.Stderr, "%s: expected a command\n\n", path)
		verbUsage(env.Stderr, path, blurb, table)
		return ExitUsage
	}
	name := args[0]
	if name == "-h" || name == "--help" || name == "help" {
		verbUsage(env.Stdout, path, blurb, table)
		return ExitOK
	}
	for _, candidate := range table {
		if candidate.name == name {
			return candidate.run(env, args[1:])
		}
	}
	fmt.Fprintf(env.Stderr, "%s: unknown command %q\n\n", path, name)
	verbUsage(env.Stderr, path, blurb, table)
	return ExitUsage
}

func verbUsage(w io.Writer, path, blurb string, table []command) {
	fmt.Fprintf(w, "%s — %s\n\nUsage:\n  %s <command> [flags]\n\nCommands:\n", path, blurb, path)
	commandTable(w, table)
	fmt.Fprintf(w, "\nRun \"%s <command> -h\" for the flags of one command.\n", path)
}

// flags builds the flag set of one leaf command: stdlib flag, ContinueOnError so
// the package's own usage exit and ExitUsage are the same number, and the help
// this repo spells rather than the stdlib's.
//
// header is everything above the flag list — the one-line summary, the usage
// lines, whatever the command owes an integrator — and ends with "Flags:".
func flags(env Env, path, header string) *flag.FlagSet {
	fs := flag.NewFlagSet(path, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprint(env.Stderr, header)
		fmt.Fprint(env.Stderr, flagUsage(fs))
	}
	return fs
}

// parseFlags parses a command's flags and reports whether there is work left to
// do: -h printed the help and a bad flag printed the error, and both are the end
// of that invocation.
func parseFlags(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK, false
		}
		return ExitUsage, false
	}
	return ExitOK, true
}

// flagUsage renders a flag set the way the documented surface spells it. The
// stdlib prints one dash, accepts one or two, and every example an integrator will
// read uses two — so the help is rewritten rather than left to contradict the
// documentation. A one-character flag keeps its single dash, because that is how a
// short flag is written everywhere.
func flagUsage(fs *flag.FlagSet) string {
	var buf bytes.Buffer
	previous := fs.Output()
	fs.SetOutput(&buf)
	fs.PrintDefaults()
	fs.SetOutput(previous)

	lines := strings.Split(buf.String(), "\n")
	for i, line := range lines {
		const indent = "  -"
		if !strings.HasPrefix(line, indent) {
			continue
		}
		body := line[len(indent):]
		name := body
		if end := strings.IndexAny(body, " \t"); end >= 0 {
			name = body[:end]
		}
		if len(name) < 2 {
			continue
		}
		lines[i] = "  --" + body
	}
	return strings.Join(lines, "\n")
}

// flushTable writes a rendered listing out and reports the failure a full disk or
// a closed pipe would otherwise swallow. The data was the point of the command, so
// failing to deliver it cannot be an exit code of zero.
func flushTable(env Env, table *tabwriter.Writer) int {
	if err := table.Flush(); err != nil {
		fmt.Fprintln(env.Stderr, "atenea: could not write the listing:", err)
		return ExitFailure
	}
	return ExitOK
}

// workspaceRoot resolves the workspace a command reads its configuration from.
//
// It is resolveRoot plus the fallback host.New applies to an empty Root, because
// these commands have no host to apply it for them: `atenea mcp list` and
// `atenea run` must read the same .mcp.json when neither is given a --cwd, and
// two spellings of "the working directory" is how they would come to differ.
func workspaceRoot(cwd string) (string, error) {
	root, err := resolveRoot(cwd)
	if err != nil || root != "" {
		return root, err
	}
	root, err = os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not resolve the working directory: %w", err)
	}
	return root, nil
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
