package tui

// The composer module owns the chat crossroads: the editable input field, the
// in-memory prompt-history navigation, and the autocomplete popup (the "/"
// slash-command menu, the "@" file-mention menu, and the inline "/model
// <query>" search). It is the fourth Model panel extracted into a
// self-contained sub-model behind the input router (see input_router.go),
// following the explorer/fileViewerPanel idiom: the root Model embeds one
// composer, routes keyboard input to it when the active target is
// targetComposer, and asks it for its input body / menu popup in the layout.
//
// The module owns everything about the editable field, the history slice, and
// the popup EXCEPT the cross-panel concerns it must not reach into, which it
// surfaces as intents (composerIntent) the root interprets:
//
//   - Submission ROUTING stays on Model. Enter on routable content surfaces
//     composerIntent{submit: true}; the root's submitPrompt decides the local
//     command (/undo, /resume, /mcp, /connect, /model, /new, /compact) vs slash
//     expansion vs prompt, and the build/plan mode path. The composer never
//     dispatches a command; it only reports "run the current input".
//   - Prompt-history PERSISTENCE stays on Model/engine. The composer owns only
//     the in-memory history slice + navigation; the root seeds it (WithHistory,
//     resume) and appends to it on submit (submitPrompt).
//   - The leader key and the Esc-cancel confirmation are run-control /
//     cross-panel concerns and stay on Model. An empty-composer Space surfaces
//     composerIntent{leaderArm: true}; the root arms the Space+e leader.
//   - Composer focus is decided by the root via activeInputTarget: the composer
//     only exposes focus()/blur()/focused(); syncComposerFocus drives them.
//   - The model catalog behind the inline "/model" search is injected as a
//     modelSource (mirroring how the explorer takes listFiles), so the composer
//     never imports the agent interface.
//
// Model embeds composer anonymously, so the state fields below (input, history,
// histIdx, menuItems, menuSelected, modelSearch, files, filesLoaded,
// filesLoading, filesError, filesGen) promote onto Model — the same idiom the
// explorer, the fileViewerPanel, the Transcript module, and the overlay pickers
// use. Field names are preserved so the Model-level layout helpers and the
// existing behavior tests keep reading them as the Model's own.

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/command"
	"atenea/internal/providerconfig"
)

const composerMaxLines = 5

type composerInput struct {
	textarea.Model
}

func newComposerInput() composerInput {
	input := textarea.New()
	input.SetPromptFunc(ansi.StringWidth(inputPrompt), func(line int) string {
		if line == 0 {
			return inputPrompt
		}
		return ""
	})
	input.ShowLineNumbers = false
	input.EndOfBufferCharacter = ' '
	input.MaxHeight = composerMaxLines
	input.SetHeight(1)
	input.Cursor.BlinkSpeed = 700 * time.Millisecond
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"))
	input.FocusedStyle.Prompt = accentStyle
	input.FocusedStyle.CursorLine = input.FocusedStyle.CursorLine.UnsetBackground()
	input.BlurredStyle.Prompt = accentStyle
	input.Focus()
	return composerInput{Model: input}
}

func (input *composerInput) SetValue(value string) {
	input.Model.SetValue(value)
	input.resize()
}

func (input composerInput) Position() int {
	lines := strings.Split(input.Value(), "\n")
	position := 0
	for index := 0; index < input.Line() && index < len(lines); index++ {
		position += len([]rune(lines[index])) + 1
	}
	return position + input.LineInfo().StartColumn + input.LineInfo().ColumnOffset
}

func (input *composerInput) SetCursor(position int) {
	position = max(position, 0)
	lines := strings.Split(input.Value(), "\n")
	row := 0
	for row < len(lines)-1 {
		lineLength := len([]rune(lines[row]))
		if position <= lineLength {
			break
		}
		position -= lineLength + 1
		row++
	}
	for input.Line() > row {
		input.CursorUp()
	}
	for input.Line() < row {
		input.CursorDown()
	}
	input.Model.SetCursor(position)
}

func (input *composerInput) CursorEnd() {
	for input.Line() < input.LineCount()-1 {
		input.CursorDown()
	}
	input.Model.CursorEnd()
}

func (input composerInput) Update(msg tea.Msg) (composerInput, tea.Cmd) {
	var cmd tea.Cmd
	input.Model, cmd = input.Model.Update(msg)
	input.resize()
	return input, cmd
}

func (input *composerInput) resize() {
	probe := input.Model
	lines := 0
	for row := 0; row < probe.LineCount(); row++ {
		lines += probe.LineInfo().Height
		for probe.Line() == row && row < probe.LineCount()-1 {
			probe.CursorDown()
		}
	}
	height := min(max(lines, 1), composerMaxLines)
	if height == input.Height() {
		return
	}
	value, position := input.Value(), input.Position()
	input.SetHeight(height)
	input.Model.SetValue(value)
	input.SetCursor(position)
}

func (input *composerInput) SetWidth(width int) {
	input.Model.SetWidth(width)
	input.resize()
}

// composer is the chat input crossroads sub-model. It holds the editable field,
// the in-memory prompt history, and the autocomplete popup state that used to
// live scattered on Model, plus the behavior over them. Value-in / value-out:
// mutating methods take a value receiver and return the updated composer,
// mirroring the package idiom.
type composer struct {
	// input is the editable textarea (draft, cursor, multi-row growth to
	// composerMaxLines then scroll, literal newlines). Promoted onto Model as
	// m.input; the behavior tests read and drive it there.
	input composerInput

	// history keeps the last historyLimit prompts submitted (Enter with text,
	// either mode path). With an empty composer, Up/Down navigate them; histIdx
	// == len(history) means "not navigating". The root seeds history
	// (WithHistory, resume) and appends on submit (submitPrompt); the composer
	// only navigates it. Promoted onto Model as m.history / m.histIdx.
	history []string
	histIdx int

	// menuItems / menuSelected are the autocomplete popup state: refreshMenu
	// recomputes them after every key that feeds the input, and the view renders
	// one line per item above the composer box. modelSearch marks the inline
	// "/model <query>" mode. files/filesLoaded/... cache the workspace listing
	// while the "@" token stays active (loadFilesOnce/dropFileCache). All
	// promoted onto Model so the layout helpers and behavior tests read them as
	// the Model's own.
	menuItems    []menuItem
	menuSelected int
	modelSearch  bool
	files        []string
	filesLoaded  bool
	filesLoading bool
	filesError   string
	filesGen     uint64
}

// composerIntent is what a key handler asks the root Model to do on the
// composer's behalf, keeping the composer from reaching into submission
// routing, prompt persistence, the leader, or run-control. At most one outward
// action is set; handled reports whether the composer fully consumed the key
// (so the root does not fall through to its run-control keys like Esc-cancel or
// Tab plan-toggle). The zero value means "not handled: let the root apply its
// composer-context keys to the returned composer".
type composerIntent struct {
	// submit asks the root to run its submitPrompt path over the current input
	// value: the local-command interception (/undo, /model, …), slash expansion,
	// mode routing (build/plan), and the history append all live there.
	submit bool
	// leaderArm asks the root to arm the Space+e leader (an empty-composer
	// Space). The root owns leader state; the composer never inserts the Space.
	leaderArm bool
	// handled reports the composer already consumed the key internally (menu
	// navigation, menu apply, menu Esc-close, history recall). When false the
	// root applies its own composer-context keys (Esc-cancel, Tab, Enter, Up/Down
	// history, and finally feeding the input).
	handled bool
}

// modelSource injects the inline "/model" search's data into the composer,
// mirroring how the explorer takes listFiles: catalog returns the current model
// catalog and whether the agent supports model selection at all; refresh asks
// the agent to refresh its catalog (fired once when the search opens). The root
// fills this from its agent so the composer never imports the agent interface.
type modelSource struct {
	catalog func() ([]providerconfig.ProviderModels, bool)
	refresh func()
}

// value returns the current draft text.
func (c composer) value() string { return c.input.Value() }

// setValue replaces the draft text, re-growing the box to fit.
func (c composer) setValue(value string) composer {
	c.input.SetValue(value)
	return c
}

// focus focuses the textarea, returning the blink command; blur removes focus.
// The root decides which to call via syncComposerFocus (activeInputTarget).
func (c *composer) focus() tea.Cmd { return c.input.Focus() }
func (c *composer) blur()          { c.input.Blur() }
func (c composer) focused() bool   { return c.input.Focused() }

// setWidth sets the visible textarea width (the composer box interior). The root
// computes it from the chat content width and its box chrome in resizeViewport.
func (c composer) setWidth(width int) composer {
	c.input.SetWidth(width)
	return c
}

// menuOpen reports whether the autocomplete popup currently shows items. The
// root reads it to give the menu precedence over history and its default keys.
func (c composer) menuOpen() bool { return len(c.menuItems) > 0 }

// pushHistory appends a submitted prompt to the in-memory history, trims to the
// limit, and resets navigation to the end (the sentinel histIdx == len(history)
// means "not navigating"). The root calls it from submitPrompt after a real
// send; durable persistence stays on the engine.
func (c composer) pushHistory(text string) composer {
	c.history = append(c.history, text)
	if len(c.history) > historyLimit {
		c.history = c.history[len(c.history)-historyLimit:]
	}
	c.histIdx = len(c.history)
	return c
}

// seedHistory replaces the in-memory history with a fresh slice and parks
// navigation at the end. The root calls it when it restores a durable session
// (WithHistory, resume) or clears it (/new).
func (c composer) seedHistory(history []string) composer {
	if len(history) > historyLimit {
		history = history[len(history)-historyLimit:]
	}
	c.history = append([]string(nil), history...)
	c.histIdx = len(c.history)
	return c
}

// recallHistory moves the history navigation one step: dir < 0 steps back (most
// recent first), dir > 0 steps forward. Navigation may only start with an empty
// composer; stepping past the most recent prompt clears the composer. The
// recalled prompt enters the input with the cursor at the end. It reports
// ok=false when the step does not apply (so the key falls through to the input).
func (c composer) recallHistory(dir int) (composer, bool) {
	if dir < 0 {
		if c.histIdx == len(c.history) && c.input.Value() != "" {
			return c, false
		}
		if c.histIdx == 0 {
			return c, false
		}
		c.histIdx--
		c.input.SetValue(c.history[c.histIdx])
	} else {
		if c.histIdx >= len(c.history) {
			return c, false
		}
		c.histIdx++
		if c.histIdx == len(c.history) {
			c.input.SetValue("")
		} else {
			c.input.SetValue(c.history[c.histIdx])
		}
	}
	c.input.CursorEnd()
	return c, true
}

// handleKey processes a single key while the composer holds input focus and the
// active target is targetComposer. It encodes the composer-internal precedence:
// the open autocomplete menu wins over everything (Up/Down cycle, Tab/Enter
// apply, Esc close), then an empty-composer Space arms the leader (surfaced
// outward), then Enter submits (surfaced outward), then Up/Down navigate
// history, and finally the key feeds the textarea and recomputes the popup.
//
// Run-control keys the root owns (Esc-cancel while working, Tab plan-toggle)
// are NOT handled here: when none of the composer's own cases fire, it returns
// handled=false so the root can apply them to the returned composer. Enter is
// always surfaced as submit=true (never handled here) so the root's submitPrompt
// stays the single dispatch point for local commands, slash expansion, mode
// routing, and history append.
//
// commands is the slash-command source, listFiles the "@"-mention source, and
// models the inline "/model" search source (all injected, like the explorer's
// listFiles). It returns the updated composer, its intent, and any command
// (the file-listing fetch or the model refresh) the key triggered.
func (c composer) handleKey(msg tea.KeyMsg, commands []command.Command, listFiles func() ([]string, error), models modelSource) (composer, composerIntent, tea.Cmd) {
	// Enter on an exact local command with the menu closed submits it straight
	// away (the value already IS the command), matching the root's former
	// early-out before the menu block.
	if msg.Type == tea.KeyEnter && !c.menuOpen() &&
		(c.input.Value() == "/new" || c.input.Value() == "/compact" || c.input.Value() == "/resume") {
		return c, composerIntent{submit: true, handled: true}, nil
	}
	if c.menuOpen() {
		switch msg.Type {
		case tea.KeyUp:
			c.menuSelected = (c.menuSelected - 1 + len(c.menuItems)) % len(c.menuItems)
			return c, composerIntent{handled: true}, nil
		case tea.KeyDown:
			c.menuSelected = (c.menuSelected + 1) % len(c.menuItems)
			return c, composerIntent{handled: true}, nil
		case tea.KeyTab:
			// Tab applies the selection; it never toggles build/plan mode.
			c, cmd := c.applySelection(commands, listFiles, models)
			return c, composerIntent{handled: true}, cmd
		case tea.KeyEnter:
			// A builtin selection completes the command onto the input and submits
			// it through the root; every other selection completes inline.
			selected := c.menuItems[c.menuSelected]
			if selected.builtin && (selected.label == "/new" || selected.label == "/compact" || selected.label == "/resume" || selected.label == "/model" || selected.label == "/mcp" || selected.label == "/connect") {
				c.input.SetValue(selected.label)
				c.input.SetCursor(len([]rune(selected.label)))
				c = c.closeMenu()
				return c, composerIntent{submit: true, handled: true}, nil
			}
			c, cmd := c.applySelection(commands, listFiles, models)
			return c, composerIntent{handled: true}, cmd
		case tea.KeyEsc:
			// Esc closes the popup without stopping the run or touching the input;
			// the next key that feeds the input recomputes and can reopen it.
			return c.closeMenu(), composerIntent{handled: true}, nil
		}
		// Any other key keeps feeding the input (the default branch below).
	}
	if c.input.Value() == "" && (msg.Type == tea.KeySpace || keyRune(msg) == " ") {
		return c, composerIntent{leaderArm: true}, nil
	}
	switch msg.Type {
	case tea.KeyEsc, tea.KeyTab:
		// Run-control keys the root owns with the menu closed: Esc arms/confirms
		// the run cancel, Tab toggles build/plan mode. Neither feeds the textarea.
		// The composer reports not-handled so the root applies them.
		return c, composerIntent{}, nil
	case tea.KeyEnter:
		return c, composerIntent{submit: true, handled: true}, nil
	case tea.KeyUp:
		if next, ok := c.recallHistory(-1); ok {
			return next, composerIntent{handled: true}, nil
		}
		// No applicable step: the key falls through to the textarea (which
		// ignores it) — return not handled so the root feeds it once.
	case tea.KeyDown:
		if next, ok := c.recallHistory(1); ok {
			return next, composerIntent{handled: true}, nil
		}
	}
	var inputCmd tea.Cmd
	c.input, inputCmd = c.input.Update(msg)
	// The key may have changed the text or caret: recompute the popup from the
	// input's new state.
	c, refreshCmd := c.refreshMenu(commands, listFiles, models)
	return c, composerIntent{handled: true}, tea.Batch(inputCmd, refreshCmd)
}

// feed pushes a message the composer did not special-case (a rune batch, a
// cursor blink) into the textarea and recomputes the popup. It is the composer
// counterpart of the root's former default `m.input.Update` fallthrough.
func (c composer) feed(msg tea.Msg, commands []command.Command, listFiles func() ([]string, error), models modelSource) (composer, tea.Cmd) {
	var inputCmd tea.Cmd
	c.input, inputCmd = c.input.Update(msg)
	c, refreshCmd := c.refreshMenu(commands, listFiles, models)
	return c, tea.Batch(inputCmd, refreshCmd)
}
