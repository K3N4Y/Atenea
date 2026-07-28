package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/paths"
)

func agentCommand(env Env, args []string) int {
	return verbs(env, "atenea agent", agentBlurb, agentCommands, args)
}

const agentBlurb = "inspect the subagent definitions the agent discovers"

var agentCommands = []command{
	{
		name:    "validate",
		summary: "Report subagent definitions that will not load or cannot be used",
		run:     agentValidateCommand,
	},
}

func agentValidateCommand(env Env, args []string) int {
	fs := flags(env, "atenea agent validate", agentValidateUsage)
	cwd := fs.String("cwd", "", "the workspace whose subagents are validated when no path is given")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	var (
		entries []agent.Entry
		subject string
		code    int
	)
	if fs.NArg() > 0 {
		entries, code = namedAgentEntries(env, fs.Args())
		subject = "the paths given"
	} else {
		entries, subject, code = discoveredAgentEntries(env, *cwd)
	}
	if code != ExitOK {
		return code
	}

	problems := 0
	for _, entry := range entries {
		if entry.Err != nil {
			problems++
			fmt.Fprintf(env.Stderr, "%s: %v\n", entry.Location, entry.Err)
			continue
		}
		if entry.ShadowedBy != "" {
			continue
		}
		if strings.TrimSpace(entry.Description) == "" {
			problems++
			fmt.Fprintf(env.Stderr, "%s: subagent %q declares no 'description', so the task tool cannot describe it to the model\n", entry.Location, entry.Name)
		}
		if strings.TrimSpace(entry.Prompt) == "" {
			problems++
			fmt.Fprintf(env.Stderr, "%s: subagent %q declares no prompt, so a child has no instructions to run\n", entry.Location, entry.Name)
		}
	}
	if problems > 0 {
		fmt.Fprintf(env.Stderr, "%d %s in %d agent %s under %s\n", problems,
			plural(problems, "problem", "problems"), len(entries),
			plural(len(entries), "definition", "definitions"), subject)
		return ExitFailure
	}
	if len(entries) == 0 {
		fmt.Fprintf(env.Stderr, "atenea agent validate: no agent definitions found under %s\n", subject)
		return ExitFailure
	}
	fmt.Fprintf(env.Stderr, "ok: %d subagent %s under %s, no problems\n", len(entries),
		plural(len(entries), "definition", "definitions"), subject)
	return ExitOK
}

func discoveredAgentEntries(env Env, cwd string) ([]agent.Entry, string, int) {
	root, err := workspaceRoot(cwd)
	if err != nil {
		fmt.Fprintln(env.Stderr, "atenea agent validate:", err)
		return nil, "", ExitUsage
	}
	dirs := paths.AgentDirs(root)
	entries, err := agent.Scan(dirs...)
	if err != nil {
		fmt.Fprintln(env.Stderr, "atenea agent validate:", err)
		return nil, "", ExitFailure
	}
	return entries, fmt.Sprintf("%d discovery %s", len(dirs), plural(len(dirs), "directory", "directories")), ExitOK
}

func namedAgentEntries(env Env, named []string) ([]agent.Entry, int) {
	var entries []agent.Entry
	for _, path := range named {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintln(env.Stderr, "atenea agent validate:", err)
			return nil, ExitUsage
		}
		if info.IsDir() {
			found, err := agent.Scan(path)
			if err != nil {
				fmt.Fprintln(env.Stderr, "atenea agent validate:", err)
				return nil, ExitFailure
			}
			entries = append(entries, found...)
			continue
		}
		entries = append(entries, parseNamedAgent(path))
	}
	return entries, ExitOK
}

func parseNamedAgent(path string) agent.Entry {
	entry := agent.Entry{Location: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		entry.Err = err
		return entry
	}
	def, err := agent.Parse(raw)
	if err != nil {
		entry.Err = err
		return entry
	}
	def.Location = path
	entry.Def = def
	return entry
}

var agentDirsSummary = strings.Join([]string{
	filepath.Join("<workspace>", ".atenea", "agents"),
	filepath.Join("<workspace>", ".agents", "agents"),
	filepath.Join("<workspace>", ".claude", "agents"),
	filepath.Join("$HOME", ".atenea", "agents"),
	filepath.Join("$HOME", ".agents", "agents"),
	filepath.Join("$HOME", ".claude", "agents"),
}, "\n  ")

var agentValidateUsage = `atenea agent validate — report subagent definitions that will not load or cannot be used.

Usage:
  atenea agent validate [flags]
  atenea agent validate [flags] PATH [PATH...]

With no PATH it validates every Markdown definition in the workspace's discovery
directories, searched in order:

  ` + agentDirsSummary + `

With a PATH it validates that file, regardless of its name, or every .md file
under that directory. Malformed definitions, missing descriptions, and empty
prompts are findings. Shadowed definitions are valid but do not win discovery.

Findings go to stderr and nothing goes to stdout. Exit status is 1 when a
definition has a problem and 2 when the invocation itself is invalid.

Flags:
`
