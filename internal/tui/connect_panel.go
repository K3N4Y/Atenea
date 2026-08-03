package tui

// The /connect panel: a full-screen modal (mirroring the /model and /mcp
// pickers) that connects a provider. It picks a provider from the connectable
// list and then takes one of two branches, because there are two kinds of
// provider: one is connected by typing or pasting a key into a masked input, the
// other by approving a code on another device. Which branch a provider takes is
// what it reports as its Kind — never something inferred from its id.
//
// A secret never leaves the panel state: the key never touches the composer
// input nor its persisted history, and nothing the device-code branch shows is
// secret at all (the code is single-use and worthless without the account it is
// approved from). Both branches run as asynchronous commands, so neither a slow
// endpoint nor a user walking to their browser freezes the UI.

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/providerconfig"
)

// connectAgent is the engine surface the panel needs. Everything here blocks —
// on a network validation, on the authorization server, on a human — so the
// Model always calls it from a tea.Cmd.
type connectAgent interface {
	ConnectableProviders() []providerconfig.ConnectableProvider
	ConnectProvider(providerID, apiKey string) (providerconfig.Active, error)
	// StartDeviceLogin mints the code to show. It is separate from awaiting it so
	// the code is on screen before anything waits on the user.
	StartDeviceLogin(providerID string) (providerconfig.DeviceLogin, error)
	AwaitDeviceLogin(providerID string) (providerconfig.Active, error)
	// CancelDeviceLogin retires one attempt, named by the handle its DeviceLogin
	// carries. The provider id alone is not enough: the panel retires attempts the
	// user abandoned, and by then the live code may belong to a later one.
	CancelDeviceLogin(providerID string, attempt uint64)
}

// connectDoneMsg reports the outcome of one connect attempt. generation
// invalidates results from a panel that was closed and reopened meanwhile.
type connectDoneMsg struct {
	generation uint64
	providerID string
	active     providerconfig.Active
	// login marks the outcome as a device-code login's rather than a key
	// validation's. Once stale the two want opposite things: a login the user
	// walked away from has nothing left to say — its code is retired and the reason
	// it died is about a panel that is gone — while a key the user typed and the
	// provider rejected has to be reported wherever the panel went, or silence
	// reads as success.
	login bool
	err   string
}

// deviceLoginStartedMsg carries the code a device-code login is waiting on, or
// the reason there is none.
type deviceLoginStartedMsg struct {
	generation uint64
	providerID string
	login      providerconfig.DeviceLogin
	err        string
}

// browserOpenFailedMsg reports that the sign-in page could not be opened.
// Silence here would read as a click that did nothing.
type browserOpenFailedMsg struct{ err error }

// openLoginPage opens the sign-in page in the user's browser, off the update
// loop the way every side effect runs.
func openLoginPage(url string) tea.Cmd {
	return func() tea.Msg {
		if err := openBrowser(url); err != nil {
			return browserOpenFailedMsg{err: err}
		}
		return nil
	}
}

type connectPanel struct {
	open        bool
	providers   []providerconfig.ConnectableProvider
	overlayList // navigation: selected + move + window over the provider list
	// entering is the key input stage for providers[selected]; key holds the
	// typed runes, rendered masked.
	entering bool
	key      []rune
	// login is the pending device-code login, and awaiting marks that the panel
	// is showing its code and waiting for the user to approve it elsewhere. The
	// stage carries the login rather than only a flag because the code, the page
	// and the deadline are what the user needs on screen.
	awaiting bool
	login    providerconfig.DeviceLogin
	// busy marks a request in flight; the panel ignores edits until the message
	// that resolves it lands.
	busy bool
	err  string
}

func newConnectPanel(providers []providerconfig.ConnectableProvider) connectPanel {
	panel := connectPanel{open: true, providers: append([]providerconfig.ConnectableProvider(nil), providers...)}
	panel.setCount(len(panel.providers))
	return panel
}

func (p connectPanel) selectedProvider() (providerconfig.ConnectableProvider, bool) {
	index, ok := p.hasSelection()
	if !ok || index >= len(p.providers) {
		return providerconfig.ConnectableProvider{}, false
	}
	return p.providers[index], true
}

// handleConnectPanelKey routes the keyboard while the panel is open. On the
// list stage arrows move and enter starts the provider's own kind of connection;
// on the key entry runes (including pastes, which arrive as rune batches) feed
// the masked key, backspace deletes, ctrl+u clears, and enter submits. Esc steps
// back one stage, and stepping back out of a pending login cancels it — a code
// nobody is waiting for is a goroutine polling something the server has already
// retired. While a request is in flight only esc works, and it closes the panel:
// what is already out cannot be recalled, but it stops being this panel's.
func (m Model) handleConnectPanelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.connectPanel.busy {
		if msg.Type == tea.KeyEsc {
			// The in-flight request has no handle to cancel yet — the code it is
			// minting does not exist — so the generation moves instead. That is what
			// keeps the answer from writing a panel it is no longer about: a code
			// arriving for a panel that is gone gets retired rather than polled for the
			// ten minutes it stays approvable, and neither branch may touch the busy or
			// the error of whatever the user started next.
			//
			// Moving the generation is NOT the same as silencing the attempt. Walking
			// away from a code the user never saw leaves nothing to report; walking away
			// from a key the provider is checking does not, because it can still come
			// back rejected and that is the only chance the user has to learn it. Which
			// of the two this is travels on the message, not on the generation.
			m.connectGen++
			m.connectPanel.busy = false
			m.connectPanel.open = false
			return m.resizeViewport(), nil
		}
		return m, nil
	}
	if msg.Type == tea.KeyEsc {
		switch {
		case m.connectPanel.awaiting:
			return m.abandonDeviceLogin(), nil
		case m.connectPanel.entering:
			m.connectPanel.entering = false
			m.connectPanel.key = nil
			m.connectPanel.err = ""
			return m, nil
		}
		m.connectPanel.open = false
		return m.resizeViewport(), nil
	}
	if m.connectPanel.awaiting {
		return m, nil
	}
	if !m.connectPanel.entering {
		switch msg.Type {
		case tea.KeyUp:
			m.connectPanel.move(-1)
		case tea.KeyDown:
			m.connectPanel.move(1)
		case tea.KeyEnter:
			return m.beginConnect()
		}
		return m, nil
	}
	switch msg.Type {
	case tea.KeyRunes:
		m.connectPanel.key = append(m.connectPanel.key, msg.Runes...)
	case tea.KeyBackspace:
		if len(m.connectPanel.key) > 0 {
			m.connectPanel.key = m.connectPanel.key[:len(m.connectPanel.key)-1]
		}
	case tea.KeyCtrlU:
		m.connectPanel.key = nil
	case tea.KeyEnter:
		return m.submitConnectKey()
	}
	return m, nil
}

// beginConnect opens the stage the selected provider is connected through: a
// masked key entry, or a device-code login that starts right away because the
// user has nothing to type here.
func (m Model) beginConnect() (tea.Model, tea.Cmd) {
	provider, ok := m.connectPanel.selectedProvider()
	if !ok {
		return m, nil
	}
	m.connectPanel.err = ""
	if provider.Kind != providerconfig.ConnectDeviceCode {
		m.connectPanel.entering = true
		return m, nil
	}
	controller, ok := m.agent.(connectAgent)
	if !ok {
		m.connectPanel.err = "provider connection is unavailable"
		return m, nil
	}
	m.connectPanel.busy = true
	generation := m.connectGen
	providerID := provider.ID
	return m, func() tea.Msg {
		login, err := controller.StartDeviceLogin(providerID)
		started := deviceLoginStartedMsg{generation: generation, providerID: providerID, login: login}
		if err != nil {
			started.err = err.Error()
		}
		return started
	}
}

// startedDeviceLogin lands the code and begins waiting for the user to approve
// it. The waiting is its own command so the code is painted first: a user looking
// at a spinner has nothing to type in.
//
// A message from an attempt the user already abandoned touches nothing. Its login
// is real server-side, but nobody approves a code that was never painted, so it is
// retired here instead of being polled until it expires — which would report that
// expiry minutes later, over whatever the user is doing by then. Neither may it
// write busy or err: those belong to whatever the current panel has in flight.
func (m Model) startedDeviceLogin(msg deviceLoginStartedMsg) (Model, tea.Cmd) {
	controller, connectable := m.agent.(connectAgent)
	if msg.generation != m.connectGen {
		// Retiring is aimed at THIS attempt. A dismissed mint is exactly the one that
		// can come back after the user already has another code on screen, and
		// "cancel whatever is pending for this provider" would take that one down and
		// leave its live wait reporting a cancellation the user never performed.
		if msg.err == "" && connectable {
			controller.CancelDeviceLogin(msg.providerID, msg.login.Attempt)
		}
		return m, nil
	}
	m.connectPanel.busy = false
	if msg.err != "" {
		if !m.connectPanel.open {
			return m.appendError(msg.err).syncViewport(), nil
		}
		m.connectPanel.err = msg.err
		return m, nil
	}
	if !connectable {
		m.connectPanel.err = "provider connection is unavailable"
		return m, nil
	}
	// A panel closed without its attempt being retired still gets the wait: the
	// login is real, and its outcome is the transcript's to report.
	if m.connectPanel.open {
		m.connectPanel.awaiting = true
		m.connectPanel.login = msg.login
	}
	generation := msg.generation
	providerID := msg.providerID
	return m, func() tea.Msg {
		active, err := controller.AwaitDeviceLogin(providerID)
		done := connectDoneMsg{generation: generation, providerID: providerID, active: active, login: true}
		if err != nil {
			done.err = err.Error()
		}
		return done
	}
}

// abandonDeviceLogin steps back out of the pending login this panel is showing,
// cancelling that attempt and no other. The connectDoneMsg its wait produces is
// expected and lands as a stale result.
func (m Model) abandonDeviceLogin() Model {
	if controller, ok := m.agent.(connectAgent); ok {
		controller.CancelDeviceLogin(m.connectPanel.login.ProviderID, m.connectPanel.login.Attempt)
	}
	m.connectGen++
	m.connectPanel.awaiting = false
	m.connectPanel.login = providerconfig.DeviceLogin{}
	m.connectPanel.err = ""
	return m
}

// submitConnectKey launches the asynchronous validate-and-store. The panel
// stays open showing the in-flight state until its connectDoneMsg lands.
func (m Model) submitConnectKey() (tea.Model, tea.Cmd) {
	provider, ok := m.connectPanel.selectedProvider()
	apiKey := strings.TrimSpace(string(m.connectPanel.key))
	if !ok || apiKey == "" {
		return m, nil
	}
	controller, ok := m.agent.(connectAgent)
	if !ok {
		m.connectPanel.err = "provider connection is unavailable"
		return m, nil
	}
	m.connectPanel.busy = true
	m.connectPanel.err = ""
	generation := m.connectGen
	providerID := provider.ID
	return m, func() tea.Msg {
		active, err := controller.ConnectProvider(providerID, apiKey)
		done := connectDoneMsg{generation: generation, providerID: providerID, active: active}
		if err != nil {
			done.err = err.Error()
		}
		return done
	}
}

// finishConnect lands the outcome of a connect attempt, whichever branch produced
// it. Success closes the panel, records the confirmation in the transcript, and
// refreshes the model catalog (discovery works now that a credential exists);
// failure keeps a key entry open with the error, ready to retype, and drops a
// failed login back to the list, since its code is gone. The credential may have
// been stored even if the user closed the panel early, so the success path also
// runs with the panel closed.
func (m Model) finishConnect(done connectDoneMsg) (Model, tea.Cmd) {
	m.connectPanel.busy = false
	if done.err != "" {
		if m.connectPanel.open {
			// A login that failed has no code left to approve, so the panel drops
			// back to the list with the reason rather than leaving a dead code up.
			m.connectPanel.awaiting = false
			m.connectPanel.login = providerconfig.DeviceLogin{}
			m.connectPanel.err = done.err
			return m, nil
		}
		return m.appendError(done.err).syncViewport(), nil
	}
	m.connectPanel.open = false
	m.connectPanel.key = nil
	m.connectPanel.awaiting = false
	m.connectPanel.login = providerconfig.DeviceLogin{}
	return m.applyConnectSuccess(done).resizeViewport().syncViewport(), nil
}

// applyStaleConnectSuccess lands a success whose panel was closed and reopened
// meanwhile (the generation moved on). The credential was stored and the
// provider possibly activated, so the globally-true effects still apply; the
// current panel only gets its connected flag updated — its busy/err state
// belongs to whatever attempt it is running now.
func (m Model) applyStaleConnectSuccess(done connectDoneMsg) (Model, tea.Cmd) {
	for index := range m.connectPanel.providers {
		if m.connectPanel.providers[index].ID == done.providerID {
			m.connectPanel.providers[index].Connected = true
		}
	}
	return m.applyConnectSuccess(done).syncViewport(), nil
}

// applyConnectSuccess applies the shared effects of a successful connect:
// footer model when the connected provider is the active one, a transcript
// confirmation, and a model-catalog refresh (discovery works now that a key
// exists). The active model is only attributed to the provider when it really
// belongs to it — connecting while another provider stays selected must not
// label that provider's model as the connected one.
func (m Model) applyConnectSuccess(done connectDoneMsg) Model {
	name := done.providerID
	for _, provider := range m.connectPanel.providers {
		if provider.ID == done.providerID {
			name = provider.Name
		}
	}
	notice := "Connected to " + name
	if done.active.ProviderID == done.providerID && done.active.Model != "" {
		m.model = done.active.Model
		notice += " · " + done.active.Model
	}
	if controller, ok := m.agent.(modelAgent); ok {
		controller.RefreshModels()
	}
	return m.appendNotice(notice)
}

// handleConnectPanelMouse mirrors the keyboard on the list stage: the wheel
// moves the selection and a left click on a provider row opens its key entry.
// The key entry stage is keyboard-only.
//
// The awaiting stage answers ctrl+click by opening the sign-in page. The click
// reaches this handler and not the terminal's own link opener because mouse
// tracking is on — the terminal reports the press instead of acting on it — so
// the affordance the user expects from a link has to be provided here. Ctrl is
// required so a stray click while waiting cannot launch a browser.
func (m Model) handleConnectPanelMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	if m.connectPanel.awaiting {
		if msg.Button == tea.MouseButtonLeft && msg.Ctrl {
			return m, openLoginPage(m.connectPanel.login.VerificationURI)
		}
		return m, nil
	}
	if m.connectPanel.busy || m.connectPanel.entering {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.connectPanel.move(-1)
	case tea.MouseButtonWheelDown:
		m.connectPanel.move(1)
	case tea.MouseButtonLeft:
		layout := overlayLayoutFor(m.width, m.height)
		row, ok := layout.rowAt(msg.X, msg.Y)
		if !ok {
			return m, nil
		}
		errRows := m.connectPanel.errRows()
		start, end := m.connectPanel.window(layout.itemRows - errRows)
		index := start + row - errRows
		if row < errRows || index >= end {
			return m, nil
		}
		m.connectPanel.overlayList.selected = index
		next, cmd := m.beginConnect()
		return next.(Model), cmd
	}
	return m, nil
}

// errRows is the number of header rows the inline error banner consumes; the
// window and hit-testing offset item rows past it.
func (p connectPanel) errRows() int {
	if p.err != "" {
		return 1
	}
	return 0
}

func (m Model) connectPanelView() string {
	layout := overlayLayoutFor(m.width, m.height)
	innerWidth := layout.innerWidth
	itemRows := layout.itemRows

	rows := make([]string, 0, itemRows)
	if m.connectPanel.err != "" {
		rows = append(rows, dangerStyle.Render(overlayCell(" "+sanitizeTerminalText(m.connectPanel.err), innerWidth)))
	}
	hint := " ↑↓ move · enter select · esc close"
	if m.connectPanel.awaiting {
		login := m.connectPanel.login
		rows = append(rows,
			overlayCell(" Sign in to "+sanitizeTerminalText(login.ProviderName), innerWidth),
			strings.Repeat(" ", max(innerWidth, 0)),
		)
		// A device-code login has something to type; a browser-redirect login
		// (empty code) only has the page — the approval comes back on its own.
		if login.UserCode != "" {
			rows = append(rows,
				linkCell(" 1. Open ", login.VerificationURI, innerWidth),
				overlayCell(" 2. Enter the code "+warningStyle.Render(sanitizeTerminalText(login.UserCode)), innerWidth),
			)
		} else {
			rows = append(rows,
				linkCell(" Open ", login.VerificationURI, innerWidth),
				overlayCell(" in your browser and approve the sign-in.", innerWidth),
			)
		}
		rows = append(rows,
			strings.Repeat(" ", max(innerWidth, 0)),
			secondaryTextStyle.Render(overlayCell(" waiting for approval…"+deviceCodeDeadline(login.ExpiresAt), innerWidth)),
		)
		hint = " ctrl+click opens the link · esc cancel"
	} else if m.connectPanel.entering {
		provider, _ := m.connectPanel.selectedProvider()
		rows = append(rows, overlayCell(" Connect "+sanitizeTerminalText(provider.Name)+" with an API key", innerWidth), strings.Repeat(" ", max(innerWidth, 0)))
		masked := strings.Repeat("•", len(m.connectPanel.key))
		switch {
		case m.connectPanel.busy:
			rows = append(rows, overlayCell(" API key: "+masked, innerWidth), strings.Repeat(" ", max(innerWidth, 0)), secondaryTextStyle.Render(overlayCell(" validating…", innerWidth)))
			hint = " validating… · esc close"
		case len(m.connectPanel.key) == 0:
			rows = append(rows, overlayCell(" API key: ", innerWidth)+"", metadataStyle.Render(overlayCell(" paste or type the key; it is stored privately, never shown", innerWidth)))
			hint = " enter connect · ctrl+u clear · esc back"
		default:
			rows = append(rows, overlayCell(" API key: "+focusStyle.Render(masked+"▌"), innerWidth))
			hint = " enter connect · ctrl+u clear · esc back"
		}
	} else {
		start, end := m.connectPanel.window(itemRows - len(rows))
		for index := start; index < end; index++ {
			rows = append(rows, m.connectPanelRow(m.connectPanel.providers[index], index == m.connectPanel.selected, innerWidth))
		}
		if len(m.connectPanel.providers) == 0 {
			rows = append(rows, overlayCell("  No connectable providers", innerWidth))
		}
	}
	for len(rows) < itemRows {
		rows = append(rows, strings.Repeat(" ", max(innerWidth, 0)))
	}

	lines := []string{
		overlayCell(" Provider", innerWidth),
		strings.Repeat("─", max(innerWidth, 0)),
	}
	for index := 0; index < itemRows; index++ {
		lines = append(lines, overlayCell(rows[index], innerWidth))
	}
	lines = append(lines,
		strings.Repeat("─", max(innerWidth, 0)),
		overlayCell(hint, innerWidth),
	)

	return m.renderOverlayPanel(layout, "Connect Provider", lines)
}

// deviceCodeDeadline renders when the code dies, as a clock time rather than as a
// countdown. Nothing redraws this panel while it waits for a human — the spinner
// only ticks while a turn runs, and the awaiting stage swallows every key — so a
// "expires in 9 minutes" rendered once still reads nine minutes after nine of them
// passed, and keeps reading it after the code is dead. An absolute time stays true
// with nothing having to tick, and one already past reads as past.
//
// Nothing is rendered when the server named no expiry: a deadline invented here
// would be a worse lie than silence.
// linkCell renders one row carrying a link. The display text is truncated to
// fit BEFORE the OSC 8 wrapping, so the terminal always sees a balanced
// open/close pair — truncating afterwards could cut the closer off and leak
// the hyperlink into everything painted next. The target stays the full URL
// even when the display is not: an authorize URL rarely fits a terminal row,
// and a clipped target would open a broken page.
func linkCell(prefix, url string, width int) string {
	url = sanitizeTerminalText(url)
	display := ansi.Truncate(url, max(width-lipgloss.Width(prefix), 0), "…")
	return overlayCell(prefix+ansi.SetHyperlink(url)+secondaryTextStyle.Underline(true).Render(display)+ansi.ResetHyperlink(), width)
}

func deviceCodeDeadline(expiresAt time.Time) string {
	if expiresAt.IsZero() {
		return ""
	}
	return " the code expires at " + expiresAt.Local().Format("15:04")
}

func (m Model) connectPanelRow(provider providerconfig.ConnectableProvider, selected bool, width int) string {
	prefix := "  "
	if selected {
		prefix = "❯ "
	}
	glyph := "  "
	status := "not connected"
	if provider.Connected {
		glyph = "● "
		status = "connected"
	}
	statusWidth := min(16, max(width/4, 0))
	nameWidth := max(width-statusWidth, 0)
	row := overlayCell(prefix+glyph+sanitizeTerminalText(provider.Name), nameWidth)
	statusCell := overlayCell(status, statusWidth)
	if selected {
		return selectedRowStyle.Render(row + statusCell)
	}
	return row + metadataStyle.Render(statusCell)
}
