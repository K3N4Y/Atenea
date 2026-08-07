package tui

// Composer autocompletion: pure popup logic, mirror of frontend/src/lib. The slash-commands "/" menu mirrors command.ts (detectCommand/filterCommands) and the files @-menu mirrors mention.ts (detectMention/filterFiles); Unlike @, a command is the ENTIRE message: it only fires when "/" is the first character of the input. The composer methods (applySelection/refreshMenu/closeMenu and the listFiles cache) wire those tokens to the popup state; see composer.go.

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/providerconfig"
)

// menuLimit bounds popup height while keeping the shipped local controls visible
// in the unfiltered menu; typed queries still devote the same bound to matches.
const menuLimit = 10

// menuItem is a popup row, source agnostic: menu "/" populates it with "/<name>" and the skill description in faint style; the @-menu of files, with the path as a label and without description.
type menuItem struct {
	label       string
	description string
	builtin     bool
	providerID  string
	model       string
	empty       bool
}

// tokenQuery is the current autocomplete token under the caret, the common form returned by detectCommand and detectMention (mirror of CommandQuery in command.ts and MentionQuery in mention.ts): query is the text between the trigger ("/" or "@") and the caret (what it filters), start the index of the trigger ("/" always at 0; the "@" where the token starts) and end the position of the caret. The indices are per rune, just like the caret of the bubbles textinput.
type tokenQuery struct {
	active     bool
	query      string
	start, end int
}

// inactiveToken is the neutral result when there is no current token (mirror of INACTIVE from command.ts and mention.ts).
var inactiveToken = tokenQuery{start: -1, end: -1}

// detectCommand recognizes a command only when "/" is the first character of the text and the caret follows within the name (without any white space between the "/" and the caret). When you type the first space the menu closes: what follows are the command arguments. Caret out of range = inactive. It operates on []rune because the textinput caret is per rune, not per byte.
func detectCommand(text string, caret int) tokenQuery {
	runes := []rune(text)
	if caret <= 0 || caret > len(runes) {
		return inactiveToken
	}
	if len(runes) == 0 || runes[0] != '/' {
		return inactiveToken
	}
	for i := 1; i < caret; i++ {
		if unicode.IsSpace(runes[i]) {
			return inactiveToken
		}
	}
	return tokenQuery{active: true, query: string(runes[1:caret]), start: 0, end: caret}
}

func detectModelQuery(text string, caret int) tokenQuery {
	runes := []rune(text)
	const prefix = "/model "
	if caret < len([]rune(prefix)) || caret > len(runes) || !strings.HasPrefix(string(runes[:caret]), prefix) {
		return inactiveToken
	}
	return tokenQuery{active: true, query: strings.TrimSpace(string(runes[len([]rune(prefix)):caret])), start: 0, end: caret}
}

func filterModels(providers []providerconfig.ProviderModels, query string, limit int) []menuItem {
	query = strings.ToLower(strings.ReplaceAll(query, " ", ""))
	var items []menuItem
	for _, provider := range providers {
		for _, model := range provider.Models {
			haystack := strings.ToLower(strings.ReplaceAll(provider.ID+provider.Name+model, " ", ""))
			if query != "" && !strings.Contains(haystack, query) {
				continue
			}
			description := provider.Name
			if !strings.EqualFold(strings.ReplaceAll(provider.Name, " ", ""), strings.ReplaceAll(provider.ID, "-", "")) {
				description = fmt.Sprintf("%s · %s", provider.Name, provider.ID)
			}
			if context := formatContextWindow(provider.Capabilities, model); context != "" {
				description += " · " + context + " context"
			}
			items = append(items, menuItem{label: model, description: description, providerID: provider.ID, model: model})
			if len(items) == limit {
				return items
			}
		}
	}
	return items
}

func isCanonicalModelCommand(text string, providers []providerconfig.ProviderModels) bool {
	parts := strings.Fields(text)
	if len(parts) != 3 || parts[0] != "/model" {
		return false
	}
	for _, provider := range providers {
		if provider.ID != parts[1] {
			continue
		}
		for _, model := range provider.Models {
			if model == parts[2] {
				return true
			}
		}
	}
	return false
}

// filterCommands excludes skills because they are selected exclusively through
// the /skills picker. It keeps local commands ahead of other extensions. The
// caller controls how many matching commands are retained; the popup itself
// windows the result to menuLimit rows while arrows navigate the full result.
func filterCommands(cmds []command.Command, query string, limit int) []command.Command {
	if limit <= 0 {
		return nil
	}
	q := strings.ToLower(query)
	if q == "" {
		ordered := make([]command.Command, 0, len(cmds))
		for _, cmd := range cmds {
			if !cmd.Skill {
				ordered = append(ordered, cmd)
			}
		}
		sort.SliceStable(ordered, func(i, j int) bool {
			return ordered[i].BuiltIn && !ordered[j].BuiltIn
		})
		if len(ordered) > limit {
			return ordered[:limit]
		}
		return ordered
	}
	type scoredCmd struct {
		cmd   command.Command
		score int
	}
	var matches []scoredCmd
	for _, cmd := range cmds {
		if cmd.Skill {
			continue
		}
		name := strings.ToLower(cmd.Name)
		var score int
		switch {
		case strings.HasPrefix(name, q):
			score = 0
		case strings.Contains(name, q):
			score = 1
		case strings.Contains(strings.ToLower(cmd.Description), q):
			score = 2
		default:
			continue
		}
		matches = append(matches, scoredCmd{cmd: cmd, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.score != b.score {
			return a.score < b.score
		}
		if len(a.cmd.Name) != len(b.cmd.Name) {
			return len(a.cmd.Name) < len(b.cmd.Name)
		}
		return a.cmd.Name < b.cmd.Name
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]command.Command, len(matches))
	for i, s := range matches {
		out[i] = s.cmd
	}
	return out
}

// detectMention looks for an "@" token ending in caret (mirror of detectMention in mention.ts). It is active when there is an "@" before the caret without spaces in between and the "@" starts the word (beginning of the text or preceded by a space), so that an email like a@b does not trigger. The query is the text between the "@" and the caret; preserves the bars of a route. It operates on []rune because the textinput caret is per rune, not per byte.
func detectMention(text string, caret int) tokenQuery {
	runes := []rune(text)
	if caret < 0 || caret > len(runes) {
		return inactiveToken
	}
	i := caret - 1
	for i >= 0 {
		if runes[i] == '@' {
			break
		}
		if unicode.IsSpace(runes[i]) {
			return inactiveToken
		}
		i--
	}
	if i < 0 || runes[i] != '@' {
		return inactiveToken
	}
	if i > 0 && !unicode.IsSpace(runes[i-1]) {
		return inactiveToken
	}
	return tokenQuery{active: true, query: string(runes[i+1 : caret]), start: i, end: caret}
}

// filterFiles sorts paths against a query (case-insensitive), mirroring the ranking of filterFiles in mention.ts. Empty Query returns the head of the list. If not, it preserves the routes that contain the query, ranking the basename prefix (0) before the basename substring (1) and before the match in the full route (2); Break the tie by shortest route. Without a match it is discarded. Limit to limit; limit <= 0 returns empty.
func filterFiles(files []string, query string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	q := strings.ToLower(query)
	if q == "" {
		if len(files) > limit {
			return files[:limit]
		}
		return files
	}
	type scoredFile struct {
		path  string
		score int
	}
	var matches []scoredFile
	for _, p := range files {
		lower := strings.ToLower(p)
		base := path.Base(lower)
		var score int
		switch {
		case strings.HasPrefix(base, q):
			score = 0
		case strings.Contains(base, q):
			score = 1
		case strings.Contains(lower, q):
			score = 2
		default:
			continue
		}
		matches = append(matches, scoredFile{path: p, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.score != b.score {
			return a.score < b.score
		}
		return len(a.path) < len(b.path)
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]string, len(matches))
	for i, s := range matches {
		out[i] = s.path
	}
	return out
}

// applySelection applies the selected item from the popup on the input, recomputing the current token (same priority as refreshMenu): with token "/" replaces "/co" with "/<name> " (mirror of applyCommand: the label is already "/<name>", preserves what was after the caret); with token "@" replaces the token with "@<path> " preserving the text around it (mirror of applyMention: text[:start] + insert + text[end:]). In both, the caret remains after the space, ready to continue writing, and the final recomputation closes the menu (the token is no longer valid for the space). Without an open menu it is a no-op.
func (c composer) applySelection(commands []command.Command, listFiles func() ([]string, error), models modelSource) (composer, tea.Cmd) {
	if len(c.menuItems) == 0 {
		return c, nil
	}
	item := c.menuItems[c.menuSelected]
	if item.empty {
		return c, nil
	}
	if item.model != "" {
		value := "/model " + item.providerID + " " + item.model + " "
		c.input.SetValue(value)
		c.input.SetCursor(len([]rune(value)))
		return c.closeMenu(), nil
	}
	runes := []rune(c.input.Value())
	if q := detectCommand(c.input.Value(), c.input.Position()); q.active {
		insert := item.label + " "
		c.input.SetValue(insert + string(runes[q.end:]))
		c.input.SetCursor(len([]rune(insert)))
	} else if q := detectMention(c.input.Value(), c.input.Position()); q.active {
		insert := "@" + item.label + " "
		c.input.SetValue(string(runes[:q.start]) + insert + string(runes[q.end:]))
		c.input.SetCursor(q.start + len([]rune(insert)))
	}
	return c.refreshMenu(commands, listFiles, models)
}

// refreshMenu recomputes the autocomplete popup from the current input text and caret: with current "/" token populates the items with the filtered commands; with a valid "@" token, with the workspace files filtered (listFiles is scheduled ONCE when the token is activated and is cached as long as it is active; while it runs or fails, the menu shows the corresponding status). Without a valid token, it closes it, invalidates pending results and discards the cache. In all cases the first item is selected. It is called after each key that feeds the input. commands is the source of slash-commands, listFiles is the source of @-menu, and models is the inline search "/model". Value-in / value-out: The caller (the Model refreshMenu seam) recalculates the height of the viewport, which is a Model layout concern.
func (c composer) refreshMenu(commands []command.Command, listFiles func() ([]string, error), models modelSource) (composer, tea.Cmd) {
	c.menuItems = nil
	c.menuSelected = 0
	text, caret := c.input.Value(), c.input.Position()
	if q := detectModelQuery(text, caret); q.active {
		c = c.dropFileCache()
		catalog, ok := models.catalog()
		if ok && isCanonicalModelCommand(text, catalog) {
			c.modelSearch = false
			return c, nil
		}
		if ok {
			c.menuItems = filterModels(catalog, q.query, menuLimit)
			if !c.modelSearch && models.refresh != nil {
				models.refresh()
			}
		}
		c.modelSearch = true
		if len(c.menuItems) == 0 {
			label := "No matches"
			if ok && len(catalog) == 0 {
				label = "No models available"
			}
			c.menuItems = []menuItem{{label: label, empty: true}}
		}
	} else if q := detectCommand(text, caret); q.active {
		c.modelSearch = false
		c = c.dropFileCache()
		developmentItems := developmentBuiltinCommands(strings.ToLower(q.query))
		// Keep every matching command in the navigation model. The view shows
		// only menuLimit rows, but Up/Down must be able to reach the rest.
		for _, cmd := range filterCommands(commands, q.query, len(commands)) {
			c.menuItems = append(c.menuItems, menuItem{
				label: "/" + cmd.Name, description: cmd.Description, builtin: cmd.BuiltIn,
			})
		}
		c.menuItems = append(c.menuItems, developmentItems...)
	} else if q := detectMention(text, caret); q.active {
		c.modelSearch = false
		var cmd tea.Cmd
		c, cmd = c.loadFilesOnce(listFiles)
		if c.filesLoading {
			c.menuItems = []menuItem{{label: "Loading files…", empty: true}}
			return c, cmd
		}
		if c.filesError != "" {
			c.menuItems = []menuItem{{label: "Could not list files: " + c.filesError, empty: true}}
			return c, cmd
		}
		for _, f := range filterFiles(c.files, q.query, menuLimit) {
			c.menuItems = append(c.menuItems, menuItem{label: f})
		}
		return c, cmd
	} else {
		c.modelSearch = false
		c = c.dropFileCache()
	}
	return c, nil
}

// closeMenu closes the popup, discarding items and selection, without touching the input or the file cache (the next key that supplies the input recomputes the token and can reopen it). The caller recalculates the height of the viewport because the popup occupied lines under the transcript (reservedLines discounted them). refreshMenu does not reuse it: there the reset precedes the repopulation.
func (c composer) closeMenu() composer {
	c.menuItems = nil
	c.menuSelected = 0
	return c
}

// loadFilesOnce schedules listFiles the first time the "@" token is in effect and caches the result as long as it is active (dropFileCache discards it when disabled). The generation allows ignoring responses from previous tokens.
func (c composer) loadFilesOnce(listFiles func() ([]string, error)) (composer, tea.Cmd) {
	if c.filesLoaded || c.filesLoading {
		return c, nil
	}
	c.files = nil
	c.filesError = ""
	if listFiles == nil {
		c.filesLoaded = true
		return c, nil
	}
	c.filesLoading = true
	c.filesGen++
	return c, listFilesCmd(listFiles, fileListMenu, c.filesGen)
}

// dropFileCache discards the cached list from the @-menu: the next activation of the token calls listFiles again (the workspace was able to switch between tokens).
func (c composer) dropFileCache() composer {
	c.files = nil
	c.filesLoaded = false
	c.filesLoading = false
	c.filesError = ""
	c.filesGen++
	return c
}

// applyListedFiles folds an async "@"-menu listing result into the file cache, saved by the generation so a stale result is ignored, then rebuilds the popup from the current input. The caller (the Model refreshMenu seam) recomputes the viewport height.
func (c composer) applyListedFiles(msg filesListedMsg, commands []command.Command, listFiles func() ([]string, error), models modelSource) (composer, tea.Cmd, bool) {
	if msg.generation != c.filesGen {
		return c, nil, false
	}
	c.filesLoading = false
	c.filesLoaded = true
	c.files = msg.files
	if msg.err != nil {
		c.files = nil
		c.filesError = msg.err.Error()
	}
	c, cmd := c.refreshMenu(commands, listFiles, models)
	return c, cmd, true
}
