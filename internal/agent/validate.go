package agent

import (
	"fmt"
	"strings"

	"github.com/K3N4Y/atenea/internal/tool"
)

// Validate checks each def's tool set against the tools that actually exist and
// returns one error per def that names something the registry does not have.
//
// It reports instead of rejecting. An unknown name costs the subagent that one
// tool and nothing else, so a typo in one definition must not take the rest of the
// catalog down with it — but it must not pass unnoticed either, which is what
// happened before: the name simply never turned into a permission and the subagent
// ran with fewer tools than its author wrote down, with nothing anywhere saying so.
//
// The message names what does exist, because the mistake is almost always a near
// miss ("bash_tool", "Read") and the list is short enough to read.
func Validate(defs []Def, catalog tool.Catalog) []error {
	if catalog == nil {
		return nil
	}
	var problems []error
	for _, def := range defs {
		var unknown []string
		for _, name := range def.Tools {
			if _, ok := catalog.Lookup(name); !ok {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) == 0 {
			continue
		}
		problems = append(problems, fmt.Errorf("subagent %q names %s, which %s not exist; available: %s%s",
			def.Name, quoteAll(unknown), plural(len(unknown)),
			strings.Join(catalog.Names(), ", "), at(def.Location)))
	}
	return problems
}

func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	return strings.Join(quoted, ", ")
}

func plural(n int) string {
	if n == 1 {
		return "does"
	}
	return "do"
}

// at names the file the def came from when there is one. A built-in has no
// Location, and pointing at nothing reads worse than saying nothing.
func at(location string) string {
	if location == "" {
		return ""
	}
	return " (" + location + ")"
}
