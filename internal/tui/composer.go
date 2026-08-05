package tui

// The composer module owns the chat crossroads: the editable input field, the
// in-memory prompt-history navigation, and the autocomplete popup (the "/"
// slash-command menu, the "@" file-mention menu, and the inline "/model
// <query>" search). The root Model owns one named composer field, routes
// keyboard input to it when the active target is targetComposer, and asks it
// for its input body / menu popup in the layout.
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
//   - Esc-cancel confirmation is run-control state and stays on Model.
//   - Composer focus is decided by the root via activeInputTarget: the composer
//     only exposes focus()/blur()/focused(); syncComposerFocus drives them.
//   - The model catalog behind the inline "/model" search is injected as a
//     modelSource, so the composer never imports the agent interface.
//

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
)

const composerMaxLines = 5

type composerInput struct {
	textarea.Model
}

func newComposerInput() composerInput {
	input := textarea.New()
	input.Placeholder = "Ask Atenea anything..."
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
	input.FocusedStyle.Prompt = focusStyle
	input.FocusedStyle.CursorLine = input.FocusedStyle.CursorLine.UnsetBackground()
	input.BlurredStyle.Prompt = secondaryTextStyle
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
type composerImage struct {
	marker string
	image  session.Image
}

type composer struct {
	// input is the editable textarea (draft, cursor, multi-row growth to
	// composerMaxLines then scroll, literal newlines).
	input composerInput

	// history keeps the last historyLimit prompts submitted (Enter with text,
	// either mode path). With an empty composer, Up/Down navigate them; histIdx
	// == len(history) means "not navigating". The root seeds history
	// (WithHistory, resume) and appends on submit (submitPrompt); the composer
	// only navigates it.
	history []string
	histIdx int

	// menuItems / menuSelected are the autocomplete popup state: refreshMenu
	// recomputes them after every key that feeds the input, and the view renders
	// one line per item above the composer box. modelSearch marks the inline
	// "/model <query>" mode. files/filesLoaded/... cache the workspace listing
	// while the "@" token stays active (loadFilesOnce/dropFileCache).
	menuItems    []menuItem
	menuSelected int
	modelSearch  bool
	files        []string
	filesLoaded  bool
	filesLoading bool
	filesError   string
	filesGen     uint64
	images       []composerImage
	nextImage    int
	generation   uint64
}

// composerIntent is what a key handler asks the root Model to do on the
// composer's behalf, keeping the composer from reaching into submission
// routing, prompt persistence, or run-control. At most one outward action is
// set; handled reports whether the composer fully consumed the key
// (so the root does not fall through to its run-control keys like Esc-cancel or
// Tab plan-toggle). The zero value means "not handled: let the root apply its
// composer-context keys to the returned composer".
type composerIntent struct {
	// submit asks the root to run its submitPrompt path over the current input
	// value: the local-command interception (/undo, /model, …), slash expansion,
	// mode routing (build/plan), and the history append all live there.
	submit bool
	// handled reports the composer already consumed the key internally (menu
	// navigation, menu apply, menu Esc-close, history recall). When false the
	// root applies its own composer-context keys (Esc-cancel, Tab, Enter, Up/Down
	// history, and finally feeding the input).
	handled bool
}

// modelSource injects the inline "/model" search's data into the composer,
// catalog returns the current model catalog and whether the agent supports model
// selection at all; refresh asks the agent to refresh its catalog (fired once
// when the search opens). The root fills this from its agent so the composer
// never imports the agent interface.
type modelSource struct {
	catalog func() ([]providerconfig.ProviderModels, bool)
	refresh func()
}

// value returns the current draft text.
func (c composer) value() string { return c.input.Value() }

func (c composer) attachImage(data []byte) composer {
	if len(data) == 0 {
		return c
	}
	if c.nextImage == 0 {
		c.nextImage = 1
	}
	marker := fmt.Sprintf("[image#%d]", c.nextImage)
	c.nextImage++
	position := min(c.input.Position(), len([]rune(c.input.Value())))
	runes := []rune(c.input.Value())
	c.input.SetValue(string(runes[:position]) + marker + string(runes[position:]))
	c.input.SetCursor(position + len([]rune(marker)))
	c.images = append(c.images, composerImage{marker: marker, image: session.Image{MediaType: "image/png", Data: append([]byte(nil), data...)}})
	return c.closeMenu()
}

func (c composer) prompt() session.Prompt {
	prompt := session.Prompt{Text: c.value()}
	for _, attachment := range c.images {
		if strings.Contains(prompt.Text, attachment.marker) {
			prompt.Images = append(prompt.Images, attachment.image)
		}
	}
	return prompt
}

// setValue replaces the draft text, re-growing the box to fit.
func (c composer) setValue(value string) composer {
	c.input.SetValue(value)
	return c
}

// clear discards the current draft and closes autocomplete without changing
// prompt history.
func (c composer) clear() composer {
	c.input.SetValue("")
	c.images = nil
	c.nextImage = 1
	c.generation++
	return c.closeMenu()
}

var imageMarkerPattern = regexp.MustCompile(`\[image#([1-9][0-9]*)\]`)

// restoreDraft replaces the draft and reconstructs attachment-to-marker
// associations from the durable prompt. Prompt images are ordered by marker
// number when submitted, so the same order deterministically restores them.
func (c composer) restoreDraft(prompt session.Prompt) composer {
	c = c.clear()
	c.input.SetValue(prompt.Text)
	markerNumbers := make([]int, 0)
	seen := make(map[int]struct{})
	for _, match := range imageMarkerPattern.FindAllStringSubmatch(prompt.Text, -1) {
		number, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		if _, exists := seen[number]; exists {
			continue
		}
		seen[number] = struct{}{}
		markerNumbers = append(markerNumbers, number)
	}
	sort.Ints(markerNumbers)
	for i, number := range markerNumbers {
		if i >= len(prompt.Images) {
			break
		}
		image := prompt.Images[i]
		image.Data = append([]byte(nil), image.Data...)
		c.images = append(c.images, composerImage{marker: fmt.Sprintf("[image#%d]", number), image: image})
	}
	if len(markerNumbers) > 0 {
		c.nextImage = markerNumbers[len(markerNumbers)-1] + 1
	}
	c.input.CursorEnd()
	return c.closeMenu()
}

func (c composer) inputHeight() int { return c.input.Height() }

func (c composer) setHeight(height int) composer {
	c.input.SetHeight(height)
	return c
}

func (c composer) inputView() string { return c.input.View() }

// visitMenuItems exposes each popup row as immutable rendering values without
// leaking the mutable backing slice or allocating a projection on every view.
func (c composer) visitMenuItems(visit func(label, description string, selected bool)) {
	for i := range c.menuItems {
		item := c.menuItems[i]
		visit(item.label, item.description, i == c.menuSelected)
	}
}

func (c composer) menuHeight() int { return len(c.menuItems) }

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
		c = c.clear()
		c.input.SetValue(c.history[c.histIdx])
	} else {
		if c.histIdx >= len(c.history) {
			return c, false
		}
		c.histIdx++
		if c.histIdx == len(c.history) {
			c = c.clear()
		} else {
			c = c.clear()
			c.input.SetValue(c.history[c.histIdx])
		}
	}
	c.input.CursorEnd()
	return c, true
}

// handleKey processes a single key while the composer holds input focus and the
// active target is targetComposer. It encodes the composer-internal precedence:
// the open autocomplete menu wins over everything (Up/Down cycle, Tab/Enter
// apply, Esc close), then Enter submits (surfaced outward), then Up/Down
// navigate history, and finally the key feeds the textarea and recomputes the
// popup.
//
// Run-control keys the root owns (Esc-cancel while working, Tab plan-toggle)
// are NOT handled here: when none of the composer's own cases fire, it returns
// handled=false so the root can apply them to the returned composer. Enter is
// always surfaced as submit=true (never handled here) so the root's submitPrompt
// stays the single dispatch point for local commands, slash expansion, mode
// routing, and history append.
//
// commands is the slash-command source, listFiles the "@"-mention source, and
// models the inline "/model" search source. It returns the updated composer,
// its intent, and any command (the file-listing fetch or the model refresh) the
// key triggered.
func (c composer) handleKey(msg tea.KeyMsg, commands []command.Command, listFiles func() ([]string, error), models modelSource) (composer, composerIntent, tea.Cmd) {
	// Enter on an exact local command with the menu closed submits it straight
	// away (the value already IS the command), matching the root's former
	// early-out before the menu block.
	if msg.Type == tea.KeyEnter && !c.menuOpen() &&
		(c.input.Value() == "/new" || c.input.Value() == "/compact" || c.input.Value() == "/checkpoint" || c.input.Value() == "/rewind" || c.input.Value() == "/resume") {
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
			if selected.builtin && (selected.label == "/new" || selected.label == "/compact" || selected.label == "/checkpoint" || selected.label == "/rewind" || selected.label == "/resume" || selected.label == "/model" || selected.label == "/mcp" || selected.label == "/connect" || selected.label == "/mode" || selected.label == "/mode:auto-accept" || selected.label == "/mode:ask" || selected.label == "/mode:yolo" || isDevelopmentBuiltinSelection(selected.label)) {
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

// update forwards a generic Bubble Tea message to the textarea. Autocomplete
// is intentionally not recomputed here: only handleKey refreshes it after keys
// that may change the text or caret. In particular, cursor blink messages must
// not reopen a menu explicitly closed with Esc.
func (c composer) update(msg tea.Msg) (composer, tea.Cmd) {
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	return c, cmd
}
