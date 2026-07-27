package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/K3N4Y/atenea/internal/mcpclient"
)

// mcpCommand is `atenea mcp`: the declared MCP servers, from the command line
// instead of the desktop panel or a text editor.
//
// It reads and writes the same two files every other surface does
// (mcpclient.Declarations, UpsertGlobalConfig, RemoveGlobalConfig). Nothing here
// starts a server, opens a session store or resolves a provider: declaring what
// atenea *can* connect is not connecting it, and a subcommand that needed an API
// key to list a config file would be a coupling nobody asked for.
func mcpCommand(env Env, args []string) int {
	return verbs(env, "atenea mcp", mcpBlurb, mcpCommands, args)
}

const mcpBlurb = "declare the MCP servers atenea can connect"

var mcpCommands = []command{
	{
		name:    "list",
		summary: "Print every declared server and the config it is declared in",
		run:     mcpListCommand,
	},
	{
		name:    "add",
		summary: "Declare a stdio server in the global config",
		run:     mcpAddCommand,
	},
	{
		name:    "remove",
		summary: "Delete a server from the global config",
		run:     mcpRemoveCommand,
	},
}

// mcpListCommand prints what is declared, which is not what is connected.
//
// A CLI invocation connects to nothing — the servers are subprocesses a running
// host owns — so there is deliberately no connected column here. It would be
// false on every row of every listing, which is worse than absent: a reader would
// take it for a report about their servers rather than about this process.
func mcpListCommand(env Env, args []string) int {
	fs := flags(env, "atenea mcp list", mcpListUsage)
	cwd := fs.String("cwd", "", "the workspace whose .mcp.json is read (default: the working directory)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "atenea mcp list: unexpected argument %q\n", fs.Arg(0))
		return ExitUsage
	}
	root, err := workspaceRoot(*cwd)
	if err != nil {
		fmt.Fprintln(env.Stderr, "atenea mcp list:", err)
		return ExitUsage
	}
	declarations, err := mcpclient.Declarations(root)
	if err != nil {
		// A malformed config is reported rather than skipped, and the message names
		// the file and the server, because a server that silently does not exist is
		// the failure this command exists to end.
		fmt.Fprintln(env.Stderr, "atenea mcp list:", err)
		return ExitFailure
	}
	if len(declarations) == 0 {
		fmt.Fprintln(env.Stderr, "atenea mcp list: no MCP server is declared.")
		for _, path := range mcpConfigPaths(root) {
			fmt.Fprintf(env.Stderr, "  %s\n", path)
		}
		fmt.Fprintln(env.Stderr, "  Declare one with `atenea mcp add NAME -- COMMAND [ARG...]`.")
		return ExitOK
	}

	table := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tSCOPE\tCOMMAND")
	for _, declaration := range declarations {
		scope := string(declaration.Scope)
		if declaration.Shadowed {
			// The one thing that explains a server running a command nobody
			// configured. LoadConfig drops it, so this is the only place it is visible.
			scope += " (shadowed)"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\n", declaration.Name, scope, commandLine(declaration))
	}
	return flushTable(env, table)
}

// mcpAddCommand declares a stdio server in the global config.
func mcpAddCommand(env Env, args []string) int {
	fs := flags(env, "atenea mcp add", mcpAddUsage)
	cwd := fs.String("cwd", "", "the workspace whose .mcp.json is checked for a conflicting name")
	environment := &envPairs{}
	fs.Var(environment, "env", "an environment variable for the server process, KEY=VALUE; repeatable")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	name, command, arguments, err := parseServer(fs.Args())
	if err != nil {
		fmt.Fprintln(env.Stderr, "atenea mcp add:", err)
		return ExitUsage
	}
	root, err := workspaceRoot(*cwd)
	if err != nil {
		fmt.Fprintln(env.Stderr, "atenea mcp add:", err)
		return ExitUsage
	}

	declarations, err := mcpclient.Declarations(root)
	if err != nil {
		fmt.Fprintln(env.Stderr, "atenea mcp add:", err)
		return ExitFailure
	}
	// The global config is a map, so writing over an existing name would succeed
	// and lose whatever was there — including an env carrying a token nothing else
	// has a copy of. Refusing is recoverable in one command; overwriting is not
	// recoverable at all.
	for _, declaration := range declarations {
		if declaration.Name != name {
			continue
		}
		fmt.Fprintf(env.Stderr, "atenea mcp add: MCP %q is already declared in %s\n", name, declaration.Path)
		if declaration.Scope == mcpclient.ScopeWorkspace {
			fmt.Fprintf(env.Stderr, "  A workspace declaration overrides the global one, so adding it here would change nothing.\n")
		} else {
			fmt.Fprintf(env.Stderr, "  Remove it first with `atenea mcp remove %s`, or edit that file.\n", name)
		}
		return ExitFailure
	}

	config := mcpclient.ServerConfig{Name: name, Type: "stdio", Command: command, Args: arguments, Env: environment.values}
	if err := mcpclient.UpsertGlobalConfig(config); err != nil {
		// The same validation Connect enforces — the name charset, a non-empty
		// command — so a config this command writes is a config a host can start.
		fmt.Fprintln(env.Stderr, "atenea mcp add:", err)
		return ExitFailure
	}
	fmt.Fprintf(env.Stderr, "declared %q in %s\n", name, mcpclient.GlobalConfigPath())
	if len(environment.keys) > 0 {
		// The keys, never the values: an env entry is where a server's token lives,
		// and a terminal is recorded in more places than the 0600 file is.
		fmt.Fprintf(env.Stderr, "  env: %s\n", strings.Join(environment.keys, ", "))
	}
	return ExitOK
}

// mcpRemoveCommand deletes a server from the global config.
func mcpRemoveCommand(env Env, args []string) int {
	fs := flags(env, "atenea mcp remove", mcpRemoveUsage)
	cwd := fs.String("cwd", "", "the workspace whose .mcp.json is consulted to explain a name it declares")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(env.Stderr, "atenea mcp remove: expected exactly one server name")
		return ExitUsage
	}
	name := fs.Arg(0)
	root, err := workspaceRoot(*cwd)
	if err != nil {
		fmt.Fprintln(env.Stderr, "atenea mcp remove:", err)
		return ExitUsage
	}

	removed, err := mcpclient.RemoveGlobalConfig(root, name)
	if err != nil {
		// Including the one the desktop panel already reports: a workspace-declared
		// server is not the global config's to delete, and the error names the file
		// to edit. Both hosts get that sentence from the same place.
		fmt.Fprintln(env.Stderr, "atenea mcp remove:", err)
		return ExitFailure
	}
	if !removed {
		// The desktop treats this as nothing to do, because its list may be stale.
		// A person who typed a name meant that name, so it is reported.
		fmt.Fprintf(env.Stderr, "atenea mcp remove: no MCP server named %q is declared\n", name)
		return ExitFailure
	}
	fmt.Fprintf(env.Stderr, "removed %q from %s\n", name, mcpclient.GlobalConfigPath())
	// Both files could declare the name, in which case the global one was the
	// shadowed half and the server the caller was looking at is still there. Saying
	// so is the whole difference between a removal and a removal the user will
	// conclude did not work.
	if path := stillDeclared(root, name); path != "" {
		fmt.Fprintf(env.Stderr, "  MCP %q is still declared in %s; edit that file to remove it too.\n", name, path)
	}
	return ExitOK
}

// stillDeclared reports the file that keeps a just-removed name alive, empty when
// nothing does.
func stillDeclared(root, name string) string {
	declarations, err := mcpclient.Declarations(root)
	if err != nil {
		return ""
	}
	for _, declaration := range declarations {
		if declaration.Name == name {
			return declaration.Path
		}
	}
	return ""
}

// parseServer reads `NAME [--] COMMAND [ARG...]` off what the flags left behind.
//
// The `--` is optional and recommended: nothing after NAME is parsed as one of
// atenea's flags — the stdlib stops at the first non-flag argument — so it
// separates nothing here, but it is what a user's hands type and what the same
// command means in every other CLI, and accepting it costs one line.
//
// A COMMAND that starts with a dash is refused rather than declared. It is almost
// always a flag written after NAME, which the stdlib silently hands over as the
// command, and the result would be a server declared to run `--env`: a
// configuration that looks written and fails at connect time, far from the typo.
func parseServer(args []string) (name, command string, arguments []string, err error) {
	if len(args) == 0 {
		return "", "", nil, fmt.Errorf("expected a name: atenea mcp add NAME -- COMMAND [ARG...]")
	}
	name, rest := args[0], args[1:]
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return "", "", nil, fmt.Errorf("expected a command for %q: atenea mcp add %s -- COMMAND [ARG...]", name, name)
	}
	if strings.HasPrefix(rest[0], "-") {
		return "", "", nil, fmt.Errorf("%q is not a command; atenea's own flags go before the name, "+
			"as in `atenea mcp add --env KEY=VALUE %s -- COMMAND [ARG...]`", rest[0], name)
	}
	return name, rest[0], rest[1:], nil
}

// envPairs collects the repeatable --env KEY=VALUE. It keeps the keys in the
// order they were given so the confirmation can name them without their values.
type envPairs struct {
	values map[string]string
	keys   []string
}

func (e *envPairs) String() string { return strings.Join(e.keys, ",") }

func (e *envPairs) Set(raw string) error {
	key, value, ok := strings.Cut(raw, "=")
	if !ok {
		return fmt.Errorf("expected KEY=VALUE, got %q", raw)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("expected KEY=VALUE, got an empty key in %q", raw)
	}
	if _, repeated := e.values[key]; repeated {
		// Last-wins would be a silent choice between two things the caller asked
		// for, and the one it discards is invisible in a file written at 0600.
		return fmt.Errorf("--env %s was given twice", key)
	}
	if e.values == nil {
		e.values = map[string]string{}
	}
	e.values[key] = value
	e.keys = append(e.keys, key)
	return nil
}

// commandLine renders a declaration's command as a reader would type it.
func commandLine(declaration mcpclient.Declaration) string {
	if declaration.URL != "" {
		return oneLine(declaration.URL, commandLimit)
	}
	parts := append([]string{declaration.Command}, declaration.Args...)
	return oneLine(strings.Join(parts, " "), commandLimit)
}

const commandLimit = 120

// mcpConfigPaths names the two files a declaration can live in, in precedence
// order, for a listing that has nothing to list. Answering "there are none" with
// nothing else is what sends a person looking for the file in the documentation.
func mcpConfigPaths(root string) []string {
	paths := []string{"workspace: " + filepath.Join(root, mcpclient.ConfigFile)}
	if global := mcpclient.GlobalConfigPath(); global != "" {
		paths = append(paths, "global:    "+global)
	}
	return paths
}

const mcpListUsage = `atenea mcp list — print every declared MCP server and the config it comes from.

Usage:
  atenea mcp list [flags]

Servers are declared in two files, the workspace one winning a name collision:

  <workspace>/.mcp.json                 the project's servers
  <user config dir>/atenea/mcp.json     the global ones, in every workspace

The listing is what is declared, not what is running: this process connects to
nothing, so a server's state belongs to whichever host has it open.

Columns: NAME, SCOPE (global | workspace, with "(shadowed)" on a global
declaration a workspace one overrides), COMMAND.

Flags:
`

const mcpAddUsage = `atenea mcp add — declare a stdio MCP server in the global config.

Usage:
  atenea mcp add [flags] NAME -- COMMAND [ARG...]

Examples:
  atenea mcp add playwright -- npx @playwright/mcp@latest
  atenea mcp add --env GITHUB_TOKEN=$TOKEN github -- npx github-mcp

The server is written to the global config, so every workspace and both hosts
see it. atenea's own flags go before NAME; everything after NAME (or after --)
is the server's command and its arguments. Nothing is started: a declared server
connects when a host connects it.

A name already declared — here or in the workspace .mcp.json — is refused rather
than overwritten.

Flags:
`

const mcpRemoveUsage = `atenea mcp remove — delete a server from the global MCP config.

Usage:
  atenea mcp remove [flags] NAME

Only the global config is written. A server declared in the workspace .mcp.json
is left alone and the error names the file to edit, because that file belongs to
the project rather than to this machine.

Flags:
`
