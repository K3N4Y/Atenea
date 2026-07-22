package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"atenea/internal/command"
	"atenea/internal/providerconfig"
)

// The tests below exercise the composer sub-model directly (value-in /
// value-out), asserting on the returned composer state and intent rather than
// on any View() string — the same discipline the explorer and fileViewerPanel
// tests follow. The completion sources are injected the way the root injects
// them, so no Model or agent is involved.

// noCompletions is the injected source set for tests that only care about
// editing/history: no slash commands, no "@" listing, no model catalog.
func noCompletions() ([]command.Command, func() ([]string, error), modelSource) {
	return nil, nil, modelSource{catalog: func() ([]providerconfig.ProviderModels, bool) { return nil, false }}
}

// key is a small helper for a single non-rune key.
func keyMsg(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// typeInto feeds each rune of s to the composer through handleKey, threading the
// injected sources, so the popup and draft evolve exactly as under the root.
func typeInto(c composer, s string, commands []command.Command, listFiles func() ([]string, error), models modelSource) composer {
	for _, r := range s {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			msg.Type = tea.KeySpace
		}
		c, _, _ = c.handleKey(msg, commands, listFiles, models)
	}
	return c
}

func TestNewComposerInput_UsesCalmCursorBlinkSpeed(t *testing.T) {
	input := newComposerInput()
	if got, want := input.Cursor.BlinkSpeed, 700*time.Millisecond; got != want {
		t.Fatalf("Cursor.BlinkSpeed = %s, want %s", got, want)
	}
}

func TestNewComposerInput_CursorLineIsTransparent(t *testing.T) {
	input := newComposerInput()
	for name, style := range map[string]lipgloss.Style{
		"focused": input.FocusedStyle.CursorLine,
		"blurred": input.BlurredStyle.CursorLine,
	} {
		if _, ok := style.GetBackground().(lipgloss.NoColor); !ok {
			t.Fatalf("%s CursorLine background = %v, want transparent", name, style.GetBackground())
		}
	}
}

// newTestComposer builds a composer with a sized, focused textarea so the
// growth math has a width to wrap against, matching the root's resizeViewport.
func newTestComposer() composer {
	c := composer{input: newComposerInput()}
	c.input.SetWidth(40)
	c.histIdx = 0
	return c
}

func TestComposer_CtrlJInsertsNewlineAndGrowsWithoutSubmitting(t *testing.T) {
	commands, listFiles, models := noCompletions()
	c := newTestComposer()
	c = typeInto(c, "primera linea", commands, listFiles, models)

	c, intent, _ := c.handleKey(keyMsg(tea.KeyCtrlJ), commands, listFiles, models)
	if intent.submit {
		t.Fatalf("Ctrl+J surfaced submit=%v, must only insert a newline", intent.submit)
	}
	c = typeInto(c, "segunda linea", commands, listFiles, models)

	if got, want := c.value(), "primera linea\nsegunda linea"; got != want {
		t.Fatalf("value() = %q, Ctrl+J must insert a literal newline and keep the draft %q", got, want)
	}
	if got := c.input.Height(); got != 2 {
		t.Fatalf("input.Height() = %d, two rows must grow the box to 2", got)
	}
}

func TestComposer_GrowthStopsAtFiveRowsPreservingNewlines(t *testing.T) {
	commands, listFiles, models := noCompletions()
	c := newTestComposer()
	for line := 0; line < composerMaxLines+2; line++ {
		c = typeInto(c, "linea", commands, listFiles, models)
		if line < composerMaxLines+1 {
			c, _, _ = c.handleKey(keyMsg(tea.KeyCtrlJ), commands, listFiles, models)
		}
	}
	if got := c.input.Height(); got != composerMaxLines {
		t.Fatalf("input.Height() = %d, the box must stop growing at %d rows", got, composerMaxLines)
	}
	// The buffer holds at most composerMaxLines rows (MaxHeight caps it), so the
	// draft keeps the literal newlines that separate those rows: exactly
	// composerMaxLines-1 of them survive, and none are collapsed within the cap.
	newlines := 0
	for _, r := range c.value() {
		if r == '\n' {
			newlines++
		}
	}
	if newlines != composerMaxLines-1 {
		t.Fatalf("value() has %d newlines, the %d visible rows must keep their %d literal separators", newlines, composerMaxLines, composerMaxLines-1)
	}
}

func TestComposer_EnterSurfacesSubmitIntent(t *testing.T) {
	commands, listFiles, models := noCompletions()
	c := typeInto(newTestComposer(), "hola", commands, listFiles, models)
	_, intent, _ := c.handleKey(keyMsg(tea.KeyEnter), commands, listFiles, models)
	if !intent.submit || !intent.handled {
		t.Fatalf("Enter intent = %+v, want submit+handled so the root routes the prompt", intent)
	}
}

func TestComposer_TabAndEscWithMenuClosedAreLeftToTheRoot(t *testing.T) {
	commands, listFiles, models := noCompletions()
	c := typeInto(newTestComposer(), "hola", commands, listFiles, models)
	for _, tc := range []struct {
		name string
		key  tea.KeyType
	}{{"tab", tea.KeyTab}, {"esc", tea.KeyEsc}} {
		next, intent, _ := c.handleKey(keyMsg(tc.key), commands, listFiles, models)
		if intent.handled || intent.submit || intent.leaderArm {
			t.Fatalf("%s intent = %+v, must be left to the root (not handled)", tc.name, intent)
		}
		if got := next.value(); got != "hola" {
			t.Fatalf("%s changed the draft to %q, run-control keys must not feed the textarea", tc.name, got)
		}
	}
}

func TestComposer_EmptyComposerSpaceArmsLeader(t *testing.T) {
	commands, listFiles, models := noCompletions()
	c := newTestComposer()
	next, intent, _ := c.handleKey(keyMsg(tea.KeySpace), commands, listFiles, models)
	if !intent.leaderArm || intent.handled {
		t.Fatalf("empty-composer Space intent = %+v, want leaderArm (root arms Space+e)", intent)
	}
	if got := next.value(); got != "" {
		t.Fatalf("empty-composer Space inserted %q, must not feed the textarea", got)
	}
	// With text present the Space is an ordinary character, not a leader.
	// bubbletea reports the space as KeySpace carrying the rune, so the textarea
	// inserts it; the composer must not intercept it as a leader.
	spaceKey := tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	c = typeInto(newTestComposer(), "x", commands, listFiles, models)
	next, intent, _ = c.handleKey(spaceKey, commands, listFiles, models)
	if intent.leaderArm {
		t.Fatalf("Space with text present armed the leader; it must type a space")
	}
	if got := next.value(); got != "x " {
		t.Fatalf("Space with text = %q, want the space typed", got)
	}
}

func TestComposer_HistoryPrevNextAndPastNewestClears(t *testing.T) {
	c := newTestComposer().seedHistory([]string{"older", "newer"})

	c, ok := c.recallHistory(-1)
	if !ok || c.value() != "newer" {
		t.Fatalf("first Up = ok:%v value:%q, want the most recent prompt", ok, c.value())
	}
	c, ok = c.recallHistory(-1)
	if !ok || c.value() != "older" {
		t.Fatalf("second Up = ok:%v value:%q, want the older prompt", ok, c.value())
	}
	if c, ok = c.recallHistory(-1); ok {
		t.Fatalf("Up past the oldest = ok:%v value:%q, want no step", ok, c.value())
	}
	c, ok = c.recallHistory(1)
	if !ok || c.value() != "newer" {
		t.Fatalf("Down = ok:%v value:%q, want back to the more recent prompt", ok, c.value())
	}
	c, ok = c.recallHistory(1)
	if !ok || c.value() != "" {
		t.Fatalf("Down past the newest = ok:%v value:%q, want the composer cleared", ok, c.value())
	}
}

func TestComposer_HistoryDoesNotStartWithTextInComposer(t *testing.T) {
	commands, listFiles, models := noCompletions()
	c := typeInto(newTestComposer().seedHistory([]string{"older"}), "draft", commands, listFiles, models)
	next, ok := c.recallHistory(-1)
	if ok {
		t.Fatalf("Up with a non-empty composer = ok:%v value:%q, must not start history nav", ok, next.value())
	}
	if got := next.value(); got != "draft" {
		t.Fatalf("Up with text = %q, the draft must be untouched", got)
	}
}

func TestComposer_MenuTakesPriorityOverHistoryKeys(t *testing.T) {
	commands := []command.Command{{Name: "commit", Description: "commit"}, {Name: "review", Description: "review"}}
	models := modelSource{catalog: func() ([]providerconfig.ProviderModels, bool) { return nil, false }}
	c := newTestComposer().seedHistory([]string{"older", "newer"})
	c = typeInto(c, "/", commands, nil, models)
	if !c.menuOpen() {
		t.Fatalf("typing / did not open the slash menu: %+v", c.menuItems)
	}
	before := c.menuSelected
	// Down moves the menu selection, NOT the prompt history (which stays parked).
	c, intent, _ := c.handleKey(keyMsg(tea.KeyDown), commands, nil, models)
	if !intent.handled {
		t.Fatalf("menu Down intent = %+v, want handled by the menu", intent)
	}
	if c.menuSelected == before {
		t.Fatalf("menu Down did not move the selection (%d)", c.menuSelected)
	}
	if got := c.value(); got != "/" {
		t.Fatalf("menu Down recalled history into the draft %q; the menu must win over history", got)
	}
	if c.histIdx != len(c.history) {
		t.Fatalf("histIdx = %d, history navigation must stay parked while the menu is open", c.histIdx)
	}
}

func TestComposer_SlashMenuBuildsAndAppliesSelection(t *testing.T) {
	commands := []command.Command{{Name: "commit", Description: "Commit changes"}, {Name: "review", Description: "Review"}}
	models := modelSource{catalog: func() ([]providerconfig.ProviderModels, bool) { return nil, false }}
	c := typeInto(newTestComposer(), "/co", commands, nil, models)
	if len(c.menuItems) == 0 || c.menuItems[0].label != "/commit" {
		t.Fatalf("slash menu = %+v, want /commit ranked first", c.menuItems)
	}
	// Tab applies the top selection, completing the command with a trailing space
	// and closing the popup.
	c, intent, _ := c.handleKey(keyMsg(tea.KeyTab), commands, nil, models)
	if !intent.handled {
		t.Fatalf("Tab apply intent = %+v, want handled", intent)
	}
	if got := c.value(); got != "/commit " {
		t.Fatalf("value() = %q after apply, want %q", got, "/commit ")
	}
	if c.menuOpen() {
		t.Fatalf("menu stayed open after applying the selection: %+v", c.menuItems)
	}
}

func TestComposer_BuiltinMenuSelectionSurfacesSubmit(t *testing.T) {
	models := modelSource{catalog: func() ([]providerconfig.ProviderModels, bool) { return nil, false }}
	c := typeInto(newTestComposer(), "/ne", nil, nil, models)
	if len(c.menuItems) == 0 || c.menuItems[0].label != "/new" {
		t.Fatalf("slash menu = %+v, want /new builtin", c.menuItems)
	}
	c, intent, _ := c.handleKey(keyMsg(tea.KeyEnter), nil, nil, models)
	if !intent.submit || !intent.handled {
		t.Fatalf("Enter on /new intent = %+v, want submit+handled so the root dispatches", intent)
	}
	if got := c.value(); got != "/new" {
		t.Fatalf("value() = %q, the builtin label must be completed onto the input before submit", got)
	}
	if c.menuOpen() {
		t.Fatalf("menu must close before the builtin submit: %+v", c.menuItems)
	}
}

func TestComposer_MentionMenuLoadsFilesOnceThenShowsThem(t *testing.T) {
	models := modelSource{catalog: func() ([]providerconfig.ProviderModels, bool) { return nil, false }}
	listFiles := func() ([]string, error) { return []string{"internal/tui/model.go"}, nil }
	c := typeInto(newTestComposer(), "@", nil, listFiles, models)
	if !c.filesLoading || len(c.menuItems) != 1 || c.menuItems[0].label != "Loading files…" {
		t.Fatalf("mention loading state = loading:%v items:%+v", c.filesLoading, c.menuItems)
	}
	// The async listing lands via applyListedFiles, guarded by the generation.
	c, _, applied := c.applyListedFiles(filesListedMsg{target: fileListMenu, generation: c.filesGen, files: []string{"internal/tui/model.go"}}, nil, listFiles, models)
	if !applied {
		t.Fatalf("current-generation listing was not applied")
	}
	if c.filesLoading || len(c.menuItems) != 1 || c.menuItems[0].label != "internal/tui/model.go" {
		t.Fatalf("mention result state = loading:%v items:%+v", c.filesLoading, c.menuItems)
	}
}

func TestComposer_InlineModelSearchFiltersCatalog(t *testing.T) {
	catalog := []providerconfig.ProviderModels{
		{ID: "openrouter", Name: "OpenRouter", Models: []string{"old", "openai/chatgpt5.5"}},
	}
	refreshed := 0
	models := modelSource{
		catalog: func() ([]providerconfig.ProviderModels, bool) { return catalog, true },
		refresh: func() { refreshed++ },
	}
	c := typeInto(newTestComposer(), "/model chatgpt", nil, nil, models)
	if !c.modelSearch {
		t.Fatalf("inline /model search did not activate: modelSearch=%v", c.modelSearch)
	}
	if len(c.menuItems) != 1 || c.menuItems[0].model != "openai/chatgpt5.5" {
		t.Fatalf("model search items = %+v, want the matching model only", c.menuItems)
	}
	if refreshed != 1 {
		t.Fatalf("refresh fired %d times, want exactly once when the search opened", refreshed)
	}
	// Applying the model selection completes the canonical command and closes.
	c, _ = c.applySelection(nil, nil, models)
	if got := c.value(); got != "/model openrouter openai/chatgpt5.5 " {
		t.Fatalf("value() = %q after model apply, want the canonical command", got)
	}
	if c.menuOpen() {
		t.Fatalf("menu must close after applying the model: %+v", c.menuItems)
	}
}

func TestComposer_FocusAndBlur(t *testing.T) {
	c := newTestComposer()
	c.blur()
	if c.focused() {
		t.Fatalf("composer still focused after blur()")
	}
	c.focus()
	if !c.focused() {
		t.Fatalf("composer not focused after focus()")
	}
}

func TestComposer_PushHistoryTrimsAndParksNavigation(t *testing.T) {
	c := newTestComposer()
	for i := 0; i < historyLimit+5; i++ {
		c = c.pushHistory("prompt")
	}
	if len(c.history) != historyLimit {
		t.Fatalf("history length = %d, want capped at %d", len(c.history), historyLimit)
	}
	if c.histIdx != len(c.history) {
		t.Fatalf("histIdx = %d, push must park navigation at the end (%d)", c.histIdx, len(c.history))
	}
}
