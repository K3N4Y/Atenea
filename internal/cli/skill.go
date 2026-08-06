package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/K3N4Y/atenea/internal/paths"
	"github.com/K3N4Y/atenea/internal/skill"
)

// skillCommand is `atenea skill`: what discovery found, and why it did not find
// the rest.
//
// Discovery is deliberately forgiving — a SKILL.md that does not parse is skipped
// so one broken skill cannot take the others down — and the cost of that is a
// contributor whose skill simply never appears, with nothing anywhere saying why.
// These two verbs are the other half of that trade. Neither runs a turn, opens a
// store or resolves a provider.
func skillCommand(env Env, args []string) int {
	return verbs(env, "atenea skill", skillBlurb, skillCommands, args)
}

const skillBlurb = "inspect the skills the agent discovers"

var skillCommands = []command{
	{
		name:    "list",
		summary: "Print every SKILL.md discovery walked and what became of it",
		run:     skillListCommand,
	},
	{
		name:    "validate",
		summary: "Report the skills that will not load, and exit non-zero if there are any",
		run:     skillValidateCommand,
	},
}

// The status of one scanned SKILL.md, as the listing spells it.
const (
	// statusActive: parsed, unshadowed, described — the model is told about it.
	statusActive = "active"
	// statusShadowed: a skill of the same name earlier in the search order won.
	// Precedence is a feature (a project skill overrides a global one), so this is
	// reported and never a failure.
	statusShadowed = "shadowed"
	// statusInvalid: validate fails on it. Either it does not parse, so nothing
	// discovers it, or it parses with no description, so nothing announces it.
	statusInvalid = "invalid"
)

func skillListCommand(env Env, args []string) int {
	fs := flags(env, "atenea skill list", skillListUsage)
	cwd := fs.String("cwd", "", "the workspace whose skills are discovered (default: the working directory)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "atenea skill list: unexpected argument %q\n", fs.Arg(0))
		return ExitUsage
	}
	dirs, entries, code := discovered(env, "atenea skill list", *cwd)
	if code != ExitOK {
		return code
	}
	if len(entries) == 0 {
		fmt.Fprintln(env.Stderr, "atenea skill list: no SKILL.md was found. Searched, in order:")
		for _, dir := range dirs {
			fmt.Fprintf(env.Stderr, "  %s\n", dir)
		}
		return ExitOK
	}

	table := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tSTATUS\tDESCRIPTION\tLOCATION")
	for _, entry := range byName(entries) {
		name, status, description := row(entry)
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", name, status, description, entry.Location)
	}
	return flushTable(env, table)
}

// byName orders the listing by skill name, the winner of a name before the
// entries it shadows.
//
// The alternative was walk order, which is the order precedence is *decided* in
// and the worse one to read it in: the shadowed copy of a name lives in another
// directory, so it lands pages away from the skill it explains. Sorted, the two
// rows are adjacent and the answer to "which file won" is one line long. Entries
// with no name — the ones that did not parse — have nothing to sort by and come
// first, which is also where a problem belongs.
func byName(entries []skill.Entry) []skill.Entry {
	sorted := append([]skill.Entry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Info.Name != sorted[j].Info.Name {
			return sorted[i].Info.Name < sorted[j].Info.Name
		}
		return sorted[i].ShadowedBy == "" && sorted[j].ShadowedBy != ""
	})
	return sorted
}

// row renders one scanned entry. A file that did not parse has no name to print,
// and the location is what identifies it, so the name column says so rather than
// guessing one from the directory.
func row(entry skill.Entry) (name, status, description string) {
	if entry.Err != nil {
		return "-", statusInvalid, oneLine(entry.Err.Error(), descriptionLimit)
	}
	name = entry.Info.Name
	description = oneLine(entry.Info.Description, descriptionLimit)
	switch {
	case entry.ShadowedBy != "":
		return name, statusShadowed, description
	case !entry.Info.Announced():
		return name, statusInvalid, "(no description: never announced to the model)"
	default:
		return name, statusActive, description
	}
}

const descriptionLimit = 64

// skillValidateCommand is R9.3's verb: a contributor gets a real error instead of
// silent non-discovery.
//
// With no argument it validates the discovery set — every directory this
// workspace would actually search — because "my skill does not show up" is a
// question about that set and about nothing else. With paths it validates those
// instead, which is the other half of the same question: a skill being authored
// somewhere that is not a discovery directory yet, or a repository checking its
// own skills in CI without installing them first. Neither answers the other's
// question, so both exist.
func skillValidateCommand(env Env, args []string) int {
	fs := flags(env, "atenea skill validate", skillValidateUsage)
	cwd := fs.String("cwd", "", "the workspace whose skills are validated when no path is given")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	var (
		entries []skill.Entry
		subject string
	)
	if fs.NArg() > 0 {
		named, code := namedEntries(env, fs.Args())
		if code != ExitOK {
			return code
		}
		entries, subject = named, "the paths given"
	} else {
		dirs, discoveredEntries, code := discovered(env, "atenea skill validate", *cwd)
		if code != ExitOK {
			return code
		}
		entries, subject = discoveredEntries, fmt.Sprintf("%d discovery %s", len(dirs), plural(len(dirs), "directory", "directories"))
	}

	problems := 0
	for _, entry := range entries {
		switch {
		case entry.Err != nil:
			problems++
			fmt.Fprintf(env.Stderr, "%s: %v\n", entry.Location, entry.Err)
		case !entry.Info.Announced():
			// It parses, so discovery keeps it and the skill tool can load it by name —
			// but Format only puts described skills in the system prompt, so the model
			// is never told the name it would have to ask for. Undiscoverable in every
			// way that matters, and invisible in every way a contributor can see.
			problems++
			fmt.Fprintf(env.Stderr, "%s: skill %q declares no 'description', so it is never announced to the model\n",
				entry.Location, entry.Info.Name)
		}
	}
	if problems > 0 {
		fmt.Fprintf(env.Stderr, "%d %s in %d %s under %s\n",
			problems, plural(problems, "problem", "problems"),
			len(entries), plural(len(entries), "SKILL.md", "SKILL.md files"), subject)
		return ExitFailure
	}
	if len(entries) == 0 {
		// Nothing was checked, so "ok" would be an answer about nothing. A contributor
		// who pointed this at their skill and got a pass would ship it broken.
		fmt.Fprintf(env.Stderr, "atenea skill validate: no SKILL.md found under %s\n", subject)
		return ExitFailure
	}
	fmt.Fprintf(env.Stderr, "ok: %d %s under %s, no problems\n",
		len(entries), plural(len(entries), "skill", "skills"), subject)
	return ExitOK
}

// discovered resolves the workspace's skill directories and scans them — the same
// ordered list wiring gives the agent, so this command cannot report on a
// different set than the one a run uses.
func discovered(env Env, path, cwd string) ([]string, []skill.Entry, int) {
	root, err := workspaceRoot(cwd)
	if err != nil {
		fmt.Fprintln(env.Stderr, path+":", err)
		return nil, nil, ExitUsage
	}
	dirs := paths.SkillDirs(root)
	entries, err := skill.Scan(dirs...)
	if err != nil {
		fmt.Fprintln(env.Stderr, path+":", err)
		return nil, nil, ExitFailure
	}
	return dirs, entries, ExitOK
}

// namedEntries validates exactly what the caller named: a SKILL.md, or a
// directory to walk. A path that cannot be read is a usage error — the same
// answer `--cwd` gives a directory that is not there — because the invocation
// named something that does not exist, which no amount of validating fixes.
func namedEntries(env Env, paths []string) ([]skill.Entry, int) {
	var entries []skill.Entry
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintln(env.Stderr, "atenea skill validate:", err)
			return nil, ExitUsage
		}
		if info.IsDir() {
			found, err := skill.Scan(path)
			if err != nil {
				fmt.Fprintln(env.Stderr, "atenea skill validate:", err)
				return nil, ExitFailure
			}
			entries = append(entries, found...)
			continue
		}
		// A named file is parsed whatever it is called. Discovery only looks at
		// SKILL.md, but a contributor who points this at a file has already said which
		// file they mean, and answering "nothing to validate" would be a technicality.
		entries = append(entries, parseNamed(path))
	}
	return entries, ExitOK
}

func parseNamed(path string) skill.Entry {
	entry := skill.Entry{Location: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		entry.Err = err
		return entry
	}
	info, err := skill.Parse(raw)
	if err != nil {
		entry.Err = err
		return entry
	}
	info.Location = path
	entry.Info = info
	return entry
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// skillDirsSummary renders the discovery order for the help text, relative to
// nothing: the real paths are workspace-dependent, so the help names the shape
// and `atenea skill list` on an empty workspace prints the actual list.
var skillDirsSummary = strings.Join([]string{
	filepath.Join("<workspace>", ".atenea", "skills"),
	filepath.Join("<workspace>", ".agents", "skills"),
	filepath.Join("<workspace>", ".claude", "skills"),
	filepath.Join("$HOME", ".atenea", "skills"),
	filepath.Join("$HOME", ".agents", "skills"),
	filepath.Join("$HOME", ".claude", "skills"),
}, "\n  ")

var skillListUsage = `atenea skill list — print every SKILL.md discovery walked and what became of it.

Usage:
  atenea skill list [flags]

Searched in order, first declaration of a name winning:

  ` + skillDirsSummary + `

Columns: NAME, STATUS, DESCRIPTION, LOCATION. STATUS is one of

  active     discovered and announced to the model
  shadowed   a skill of the same name earlier in the order won; LOCATION is
             the file that lost, so a surprising skill can be traced to it
  invalid    it does not parse, or it declares no description and is therefore
             never announced — ` + "`atenea skill validate`" + ` says which

Flags:
`

const skillValidateUsage = `atenea skill validate — report the skills that will not load.

Usage:
  atenea skill validate [flags]
  atenea skill validate [flags] PATH [PATH...]

With no PATH it validates the discovery set: everything a run in this workspace
would walk. With a PATH it validates that file, or every SKILL.md under that
directory, which is what a skill being written somewhere else needs — including
a repository checking its own skills in CI before anyone installs them.

Discovery skips a SKILL.md it cannot parse so one broken skill cannot take the
others down with it. This is where that silence is broken: every problem is
printed with the file that has it, and the exit code is 1 if there is one.

Findings go to stderr and nothing goes to stdout, as go vet does, so the exit
code is the answer and the detail is the explanation.

Flags:
`
