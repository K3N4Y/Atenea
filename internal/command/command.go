// Package command implements the composer's slash-commands: "/<name> args" that the user types are resolved to an expanded prompt before being sent to the agent. Today the commands are derived from the discovered skills (FromSkills), but the model is general: a command is just a Name + Description (for the menu) and a Prompt Template with the placeholder $ARGUMENTS. Adding a new command (e.g. /commit) is adding another Command with its template, without touching the rest of the wiring.
package command

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/K3N4Y/atenea/internal/skill"
)

// argumentsPlaceholder is the placeholder that Expand replaces with the args that the user types after the command name.
const argumentsPlaceholder = "$ARGUMENTS"

// Command is a slash-command: Name invokes it ("/"+Name), Description describes it in the composer menu, and Template is the prompt sent to the agent with $ARGUMENTS replaced by the user's args.
type Command struct {
	Name        string
	Description string
	Template    string
	// BuiltIn marks a command handled locally by a host instead of expanded into
	// an agent prompt. Both UIs use this registry metadata when presenting it.
	BuiltIn bool
	resolve func(context.Context, string) (string, error)
}

// Mention is external content addressable from the composer with @Name.
type Mention struct {
	Name        string
	Description string
	resolve     func(context.Context) (string, error)
}

func DynamicMention(name, description string, resolve func(context.Context) (string, error)) Mention {
	return Mention{Name: name, Description: description, resolve: resolve}
}

// Dynamic creates a command resolved at invocation time. This lets remote
// prompt implementations stay behind the command module instead of leaking
// protocol details into either UI.
func Dynamic(name, description string, resolve func(context.Context, string) (string, error)) Command {
	return Command{Name: name, Description: description, resolve: resolve}
}

// FromSkills derives a command for each discovered skill: "/<name>" references the skill. The template instructs the agent to use it by name (loads it via its tool skill, maintaining progressive disclosure) and appends the user's args.
func FromSkills(skills []skill.Info) []Command {
	cmds := make([]Command, 0, len(skills))
	for _, s := range skills {
		cmds = append(cmds, Command{
			Name:        s.Name,
			Description: s.Description,
			Template:    fmt.Sprintf("Usa la skill %q.\n\n%s", s.Name, argumentsPlaceholder),
		})
	}
	return cmds
}

// Expand produces the final prompt of a template and the args. If the template contains $ARGUMENTS, it replaces it with the args; If not, append the args at the end (separated by a blank line) when there are any. The result is trimmed so as not to drag loose line breaks when there are no args.
func Expand(template, args string) string {
	args = strings.TrimSpace(args)
	if strings.Contains(template, argumentsPlaceholder) {
		return strings.TrimSpace(strings.ReplaceAll(template, argumentsPlaceholder, args))
	}
	if args == "" {
		return strings.TrimSpace(template)
	}
	return strings.TrimSpace(template) + "\n\n" + args
}

// Set indexes commands by name and preserves the ordered list for the menu. It is read-only after being constructed, so Resolve/List are concurrently safe.
type Set struct {
	list     []Command
	byName   map[string]Command
	mentions map[string]Mention
}

// New indexes the commands by name (if there is a duplicate name, the last one, program config, wins) and memorizes the list ordered by name for the menu.
func New(cmds []Command, mentions ...Mention) *Set {
	byName := make(map[string]Command, len(cmds))
	for _, cmd := range cmds {
		byName[cmd.Name] = cmd
	}
	deduplicated := make([]Command, 0, len(byName))
	for _, cmd := range byName {
		deduplicated = append(deduplicated, cmd)
	}
	set, _ := NewChecked(deduplicated, mentions...)
	return set
}

// NewChecked builds a set and rejects ambiguous command names. A local command, skill, or MCP prompt may never silently replace another source in the menu.
func NewChecked(cmds []Command, mentions ...Mention) (*Set, error) {
	byName := make(map[string]Command, len(cmds))
	for _, c := range cmds {
		if _, exists := byName[c.Name]; exists {
			return nil, fmt.Errorf("duplicate slash command %q", c.Name)
		}
		byName[c.Name] = c
	}
	list := make([]Command, 0, len(byName))
	for _, c := range byName {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	indexedMentions := make(map[string]Mention, len(mentions))
	for _, mention := range mentions {
		indexedMentions[mention.Name] = mention
	}
	return &Set{list: list, byName: byName, mentions: indexedMentions}, nil
}

// List returns commands sorted by name for the composer menu.
func (s *Set) List() []Command { return s.list }

// Resolve interprets input as a slash-command: if it starts with "/" and its first token names a registered command, it returns the expanded prompt and true. If it is not a command (does not start with "/", "/" with no name, or unknown name) returns ("", false) so that the text is sent without transforming.
func (s *Set) Resolve(input string) (string, bool) {
	expanded, ok, _ := s.ResolveContext(context.Background(), input)
	return expanded, ok
}

// ResolveContext is Resolve for commands whose expansion requires I/O.
func (s *Set) ResolveContext(ctx context.Context, input string) (string, bool, error) {
	name, args, ok := parse(input)
	if !ok {
		return "", false, nil
	}
	cmd, ok := s.byName[name]
	if !ok {
		return "", false, nil
	}
	if cmd.BuiltIn {
		return "", false, nil
	}
	if cmd.resolve != nil {
		expanded, err := cmd.resolve(ctx, args)
		return expanded, true, err
	}
	return Expand(cmd.Template, args), true, nil
}

// ExpandMentions replaces exact whitespace-delimited @mentions with remote
// content blocks. Unknown mentions, including ordinary workspace files, remain
// untouched for the agent's normal read-tool flow.
func (s *Set) ExpandMentions(ctx context.Context, input string) (string, error) {
	fields := strings.Fields(input)
	for _, field := range fields {
		name := strings.TrimPrefix(field, "@")
		mention, ok := s.mentions[name]
		if field == name || !ok {
			continue
		}
		content, err := mention.resolve(ctx)
		if err != nil {
			return "", fmt.Errorf("resolve @%s: %w", name, err)
		}
		input = strings.ReplaceAll(input, field, "<resource name=\""+name+"\">\n"+content+"\n</resource>")
	}
	return input, nil
}

// parse separates "/name args" into (name, args, true). The name goes from the initial "/" to the first space; the rest are the (trimmed) args. It is not a command if it does not start with "/" or if the name is empty.
func parse(input string) (name, args string, ok bool) {
	rest, found := strings.CutPrefix(input, "/")
	if !found {
		return "", "", false
	}
	// The name goes up to the first white space (space, tab or break): the rest are the args. Thus a Shift+Enter jump also separates name from args.
	cut := strings.IndexFunc(rest, unicode.IsSpace)
	if cut < 0 {
		name = rest
	} else {
		name, args = rest[:cut], rest[cut:]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", false
	}
	return name, strings.TrimSpace(args), true
}
