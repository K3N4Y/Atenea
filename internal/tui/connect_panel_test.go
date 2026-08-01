package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/providerconfig"
)

// fakeConnectAgent extends fakeAgent with the connectAgent surface: it records
// the keys it receives and flips the provider to connected on success, and it
// answers the device-code half the same way for a provider connected by logging
// in.
type fakeConnectAgent struct {
	*fakeAgent
	connectable []providerconfig.ConnectableProvider
	connects    []struct{ providerID, key string }
	connectErr  error
	// starts and cancels record the device-code calls; startErr and loginErr are
	// the two ways a login can fail (minting the code, and approving it). A cancel
	// carries the attempt it targets, because "cancel this provider's login" and
	// "cancel the login I started" are not the same call once two are in flight.
	starts  []string
	cancels []cancelledAttempt
	// attempts numbers the codes this agent mints, the way the service numbers them
	// by start order.
	attempts uint64
	startErr error
	loginErr error
	login    providerconfig.DeviceLogin
}

type cancelledAttempt struct {
	providerID string
	attempt    uint64
}

func (f *fakeConnectAgent) ConnectableProviders() []providerconfig.ConnectableProvider {
	return append([]providerconfig.ConnectableProvider(nil), f.connectable...)
}

func (f *fakeConnectAgent) StartDeviceLogin(providerID string) (providerconfig.DeviceLogin, error) {
	f.starts = append(f.starts, providerID)
	if f.startErr != nil {
		return providerconfig.DeviceLogin{}, f.startErr
	}
	f.attempts++
	login := f.login
	login.ProviderID = providerID
	login.Attempt = f.attempts
	return login, nil
}

func (f *fakeConnectAgent) AwaitDeviceLogin(providerID string) (providerconfig.Active, error) {
	if f.loginErr != nil {
		return providerconfig.Active{}, f.loginErr
	}
	for i := range f.connectable {
		if f.connectable[i].ID == providerID {
			f.connectable[i].Connected = true
		}
	}
	f.active = providerconfig.Active{ProviderID: providerID, ProviderName: "OpenAI (ChatGPT subscription)", Model: "gpt-5.5"}
	return f.active, nil
}

func (f *fakeConnectAgent) CancelDeviceLogin(providerID string, attempt uint64) {
	f.cancels = append(f.cancels, cancelledAttempt{providerID: providerID, attempt: attempt})
}

func (f *fakeConnectAgent) ConnectProvider(providerID, apiKey string) (providerconfig.Active, error) {
	f.connects = append(f.connects, struct{ providerID, key string }{providerID, apiKey})
	if f.connectErr != nil {
		return providerconfig.Active{}, f.connectErr
	}
	for i := range f.connectable {
		if f.connectable[i].ID == providerID {
			f.connectable[i].Connected = true
		}
	}
	f.active = providerconfig.Active{ProviderID: providerID, ProviderName: "OpenRouter", Model: "openrouter/free"}
	return f.active, nil
}

func newConnectTestAgent() *fakeConnectAgent {
	return &fakeConnectAgent{
		fakeAgent: &fakeAgent{},
		connectable: []providerconfig.ConnectableProvider{
			{ID: "openrouter", Name: "OpenRouter", Kind: providerconfig.ConnectAPIKey},
		},
	}
}

// newLoginTestAgent offers a provider connected by logging in, with a code the
// stub server would have issued.
func newLoginTestAgent() *fakeConnectAgent {
	return &fakeConnectAgent{
		fakeAgent: &fakeAgent{},
		connectable: []providerconfig.ConnectableProvider{
			{ID: "openai-codex", Name: "OpenAI (ChatGPT subscription)", Kind: providerconfig.ConnectDeviceCode},
		},
		login: providerconfig.DeviceLogin{
			ProviderName:    "OpenAI (ChatGPT subscription)",
			UserCode:        "V3H5-1MW96",
			VerificationURI: "https://auth.openai.com/codex/device",
			ExpiresAt:       time.Now().Add(10 * time.Minute),
		},
	}
}

// newBrowserLoginTestAgent offers a provider whose login is a browser redirect:
// there is no code to type, only a page to open, and the approval comes back on
// its own — the PostHog shape.
func newBrowserLoginTestAgent() *fakeConnectAgent {
	return &fakeConnectAgent{
		fakeAgent: &fakeAgent{},
		connectable: []providerconfig.ConnectableProvider{
			{ID: "posthog", Name: "PostHog", Kind: providerconfig.ConnectDeviceCode},
		},
		login: providerconfig.DeviceLogin{
			ProviderName:    "PostHog",
			UserCode:        "",
			VerificationURI: "https://us.posthog.com/oauth/authorize?client_id=x",
			ExpiresAt:       time.Now().Add(3 * time.Minute),
		},
	}
}

func openConnectPanel(t *testing.T, agent Agent, command string) Model {
	t.Helper()
	m := NewModel(agent, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeRunes(t, m, command)
	return apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
}

func TestModel_ConnectCommandOpensPanelListingProviders(t *testing.T) {
	m := openConnectPanel(t, newConnectTestAgent(), "/connect")
	if !m.connectPanel.open {
		t.Fatal("/connect must open the panel")
	}
	if m.input.Value() != "" {
		t.Fatalf("composer input = %q, want empty after /connect", m.input.Value())
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"Connect Provider", "OpenRouter", "not connected"} {
		if !strings.Contains(view, want) {
			t.Fatalf("panel view is missing %q:\n%s", want, view)
		}
	}
}

func TestModel_ConnectWithProviderArgumentJumpsToKeyEntry(t *testing.T) {
	m := openConnectPanel(t, newConnectTestAgent(), "/connect openrouter")
	if !m.connectPanel.open || !m.connectPanel.entering {
		t.Fatalf("panel open=%v entering=%v, want the key entry stage", m.connectPanel.open, m.connectPanel.entering)
	}
}

func TestModel_ConnectMasksTypedKeyAndConnectsOnEnter(t *testing.T) {
	agent := newConnectTestAgent()
	m := openConnectPanel(t, agent, "/connect openrouter")
	m = typeRunes(t, m, "sk-or-secret123")

	view := ansi.Strip(m.View())
	if strings.Contains(view, "sk-or-secret123") {
		t.Fatalf("the API key must never render in clear text:\n%s", view)
	}
	if !strings.Contains(view, strings.Repeat("•", len("sk-or-secret123"))) {
		t.Fatalf("the typed key must render masked:\n%s", view)
	}

	m, cmd := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter must launch the async connect command")
	}
	if !m.connectPanel.busy {
		t.Fatal("panel must show the validation in flight")
	}
	m = apply(t, m, cmd())

	if len(agent.connects) != 1 || agent.connects[0].providerID != "openrouter" || agent.connects[0].key != "sk-or-secret123" {
		t.Fatalf("connects = %#v", agent.connects)
	}
	if m.connectPanel.open {
		t.Fatal("panel must close after a successful connect")
	}
	if m.model != "openrouter/free" {
		t.Fatalf("footer model = %q, want the activated default model", m.model)
	}
	if agent.refreshes == 0 {
		t.Fatal("a successful connect must refresh the model catalog")
	}
	transcript := ansi.Strip(m.View())
	if !strings.Contains(transcript, "Connected to OpenRouter") {
		t.Fatalf("transcript must confirm the connection:\n%s", transcript)
	}
}

func TestModel_ConnectShowsValidationErrorAndStaysOpen(t *testing.T) {
	agent := newConnectTestAgent()
	agent.connectErr = errors.New("invalid API key")
	m := openConnectPanel(t, agent, "/connect openrouter")
	m = typeRunes(t, m, "sk-or-bad")
	m, cmd := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, cmd())

	if !m.connectPanel.open || !m.connectPanel.entering {
		t.Fatal("panel must stay on the key entry after a failed validation")
	}
	if m.connectPanel.busy {
		t.Fatal("busy must clear when the result lands")
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "invalid API key") {
		t.Fatalf("panel must show the validation error:\n%s", view)
	}
}

func TestModel_ConnectEscGoesBackThenCloses(t *testing.T) {
	m := openConnectPanel(t, newConnectTestAgent(), "/connect openrouter")
	m = typeRunes(t, m, "sk")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.connectPanel.open || m.connectPanel.entering {
		t.Fatal("esc must go back to the provider list first")
	}
	if len(m.connectPanel.key) != 0 {
		t.Fatal("leaving the key entry must clear the typed key")
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.connectPanel.open {
		t.Fatal("esc on the list must close the panel")
	}
}

func TestModel_ConnectRejectsUnknownProviderArgument(t *testing.T) {
	m := openConnectPanel(t, newConnectTestAgent(), "/connect nope")
	if m.connectPanel.open {
		t.Fatal("an unknown provider must not open the panel")
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "usage: /connect") {
		t.Fatalf("expected a usage error:\n%s", view)
	}
}

func TestModel_ConnectUnavailableWithoutConnectAgent(t *testing.T) {
	m := openConnectPanel(t, &fakeAgent{}, "/connect")
	if m.connectPanel.open {
		t.Fatal("panel must not open without a connect-capable agent")
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "unavailable") {
		t.Fatalf("expected an unavailability error:\n%s", view)
	}
}

func TestModel_ConnectNoticeOmitsModelWhenAnotherProviderStaysActive(t *testing.T) {
	agent := newConnectTestAgent()
	m := openConnectPanel(t, agent, "/connect openrouter")
	m = typeRunes(t, m, "sk-or-new")
	m, cmd := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	// The service left another selection active: the result reports it.
	done := cmd().(connectDoneMsg)
	done.active = providerconfig.Active{ProviderID: "local", ProviderName: "Local", Model: "llama"}
	m = apply(t, m, done)

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Connected to OpenRouter") {
		t.Fatalf("transcript must confirm the connection:\n%s", view)
	}
	if strings.Contains(view, "Connected to OpenRouter · llama") {
		t.Fatalf("the notice must not attribute another provider's model to OpenRouter:\n%s", view)
	}
}

func TestModel_StaleConnectSuccessStillLandsAfterReopen(t *testing.T) {
	agent := newConnectTestAgent()
	m := openConnectPanel(t, agent, "/connect openrouter")
	m = typeRunes(t, m, "sk-or-slow")
	m, cmd := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// The user closes the panel mid-validation and reopens it: the generation
	// moves on, but the in-flight connect still stored the credential and
	// activated the provider.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = typeRunes(t, m, "/connect")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	m = apply(t, m, cmd())

	if m.model != "openrouter/free" {
		t.Fatalf("footer model = %q, want the stale success still applied", m.model)
	}
	if agent.refreshes == 0 {
		t.Fatal("a stale success must still refresh the model catalog")
	}
	if provider, ok := m.connectPanel.selectedProvider(); !ok || !provider.Connected {
		t.Fatalf("reopened panel must show the provider connected, got %#v ok=%v", provider, ok)
	}
	if m.connectPanel.busy {
		t.Fatal("the reopened panel is not the one validating; busy must stay off")
	}
}

func TestModel_WithNoticeShowsInTranscript(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithNotice("No provider connected — run /connect")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "No provider connected — run /connect") {
		t.Fatalf("transcript must show the startup notice:\n%s", view)
	}
}

// TestModel_ConnectShowsTheDeviceCodeAndWaitsForApproval: the login branch. The
// user has nothing to type here, so selecting the provider starts the login and
// the panel's whole job is to show where to go and what to enter.
func TestModel_ConnectShowsTheDeviceCodeAndWaitsForApproval(t *testing.T) {
	agent := newLoginTestAgent()
	m, cmd := applyCmd(t, openConnectPanel(t, agent, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("selecting a login provider must launch the async start-login command")
	}
	if !m.connectPanel.busy {
		t.Fatal("the panel must show the request in flight while the code is minted")
	}
	if m.connectPanel.entering {
		t.Fatal("a login provider must not open a masked key entry: there is no key to type")
	}
	m, cmd = applyCmd(t, m, cmd())
	if cmd == nil {
		t.Fatal("landing the code must launch the wait for the user's approval")
	}
	if !m.connectPanel.awaiting || m.connectPanel.busy {
		t.Fatalf("panel awaiting=%v busy=%v, want the code shown and the wait in flight", m.connectPanel.awaiting, m.connectPanel.busy)
	}
	if agent.starts == nil || agent.starts[0] != "openai-codex" {
		t.Fatalf("starts = %#v, want the login started for the selected provider", agent.starts)
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"V3H5-1MW96", "https://auth.openai.com/codex/device", "waiting for approval", "esc cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("panel view is missing %q:\n%s", want, view)
		}
	}

	m = apply(t, m, cmd())
	if m.connectPanel.open {
		t.Fatal("panel must close after an approved login")
	}
	if m.model != "gpt-5.5" {
		t.Fatalf("footer model = %q, want the activated default model", m.model)
	}
	if transcript := ansi.Strip(m.View()); !strings.Contains(transcript, "Connected to OpenAI (ChatGPT subscription) · gpt-5.5") {
		t.Fatalf("transcript must confirm the connection:\n%s", transcript)
	}
}

// TestModel_ConnectShowsTheBrowserLoginWithoutACodeStep: a browser-redirect
// login has nothing to type, so painting a numbered "enter the code" step with
// an empty code would send the user hunting for a code that does not exist.
// The panel shows the page to open and waits.
func TestModel_ConnectShowsTheBrowserLoginWithoutACodeStep(t *testing.T) {
	agent := newBrowserLoginTestAgent()
	m, cmd := applyCmd(t, openConnectPanel(t, agent, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("selecting a login provider must launch the async start-login command")
	}
	m, cmd = applyCmd(t, m, cmd())
	if cmd == nil {
		t.Fatal("landing the login must launch the wait for the user's approval")
	}
	if !m.connectPanel.awaiting {
		t.Fatal("the panel must be in the awaiting stage")
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"https://us.posthog.com/oauth/authorize?client_id=x", "in your browser", "waiting for approval", "esc cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("panel view is missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Enter the code") {
		t.Fatalf("panel view offers a code step with no code to enter:\n%s", view)
	}
}

// awaitingLoginModel drives a browser login up to the awaiting stage, where
// the panel shows the link and waits.
func awaitingLoginModel(t *testing.T, agent *fakeConnectAgent) Model {
	t.Helper()
	m, cmd := applyCmd(t, openConnectPanel(t, agent, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("selecting a login provider must launch the async start-login command")
	}
	m, _ = applyCmd(t, m, cmd())
	if !m.connectPanel.awaiting {
		t.Fatal("the panel must be in the awaiting stage")
	}
	return m
}

// TestModel_ConnectCtrlClickOpensTheSignInPage: mouse tracking means the
// terminal reports the click to the app instead of opening the link itself,
// so the affordance a link promises has to be honored here.
func TestModel_ConnectCtrlClickOpensTheSignInPage(t *testing.T) {
	var opened []string
	restore := openBrowser
	openBrowser = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	t.Cleanup(func() { openBrowser = restore })

	m := awaitingLoginModel(t, newBrowserLoginTestAgent())

	// A plain click must not launch anything: it is how a user focuses the
	// terminal or selects text.
	_, cmd := m.handleConnectPanelMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if cmd != nil {
		t.Fatal("a plain click while awaiting must not open the browser")
	}

	_, cmd = m.handleConnectPanelMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Ctrl: true})
	if cmd == nil {
		t.Fatal("ctrl+click while awaiting must produce the open-browser command")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("opening the browser succeeded but reported %#v", msg)
	}
	if len(opened) != 1 || opened[0] != "https://us.posthog.com/oauth/authorize?client_id=x" {
		t.Fatalf("opened = %v, want the full sign-in URL exactly once", opened)
	}
}

// TestModel_ConnectBrowserFailureIsNamedOnThePanel: the login is still
// perfectly waitable when only the shortcut failed, so the panel stays put and
// says why the click did nothing.
func TestModel_ConnectBrowserFailureIsNamedOnThePanel(t *testing.T) {
	restore := openBrowser
	openBrowser = func(string) error { return errors.New("xdg-open: not found") }
	t.Cleanup(func() { openBrowser = restore })

	m := awaitingLoginModel(t, newBrowserLoginTestAgent())
	_, cmd := m.handleConnectPanelMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Ctrl: true})
	if cmd == nil {
		t.Fatal("ctrl+click must produce the open-browser command")
	}
	m = apply(t, m, cmd())
	if !m.connectPanel.awaiting || !m.connectPanel.open {
		t.Fatal("a failed browser launch must not abandon the login")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "could not open the browser") {
		t.Fatalf("panel view does not say why the click did nothing:\n%s", view)
	}
}

// TestLinkCell_TargetsTheFullURLBehindATruncatedDisplay: an authorize URL
// rarely fits a terminal row. The display may be clipped; the OSC 8 target the
// terminal acts on must never be, and the hyperlink must close on the same row
// or it leaks into everything painted next.
func TestLinkCell_TargetsTheFullURLBehindATruncatedDisplay(t *testing.T) {
	url := "https://us.posthog.com/oauth/authorize?client_id=abcdef&code_challenge=0123456789012345678901234567890123456789012&state=xyz"
	cell := linkCell(" Open ", url, 40)
	if width := ansi.StringWidth(cell); width != 40 {
		t.Fatalf("cell width = %d, want exactly the given 40", width)
	}
	if !strings.Contains(cell, "\x1b]8;;"+url+"\x07") {
		t.Fatal("the OSC 8 target must carry the full URL, not the clipped display")
	}
	if !strings.HasSuffix(strings.TrimRight(cell, " "), "\x1b]8;;\x07") {
		t.Fatal("the hyperlink must be closed before the row ends")
	}
	if display := ansi.Strip(cell); !strings.Contains(display, "…") {
		t.Fatalf("a URL wider than the row must show its truncation: %q", display)
	}
}

// TestModel_ConnectStatesTheCodeDeadlineAsAClockTimeNothingHasToRedraw: this
// panel is painted once and then waits for a human. Nothing ticks while it does —
// the spinner only runs during a turn and the awaiting stage swallows every key —
// so a remaining-time countdown would still read the same nine minutes nine
// minutes later, and would keep reading it after the code was dead. The instant
// the code dies stays true with nothing redrawing it.
func TestModel_ConnectStatesTheCodeDeadlineAsAClockTimeNothingHasToRedraw(t *testing.T) {
	agent := newLoginTestAgent()
	expiresAt := time.Now().Add(9 * time.Minute)
	agent.login.ExpiresAt = expiresAt
	m, cmd := applyCmd(t, openConnectPanel(t, agent, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = applyCmd(t, m, cmd())

	view := ansi.Strip(m.View())
	if want := "expires at " + expiresAt.Local().Format("15:04"); !strings.Contains(view, want) {
		t.Fatalf("panel view is missing %q:\n%s", want, view)
	}
	if strings.Contains(view, "expires in") {
		t.Fatalf("panel counts down without anything to advance it, so it freezes on the first frame:\n%s", view)
	}

	// A code that already died says so by naming an instant in the past instead of
	// falling silent, which is what a countdown clamped at zero does.
	dead := newLoginTestAgent()
	dead.login.ExpiresAt = time.Now().Add(-time.Minute)
	expired, cmd := applyCmd(t, openConnectPanel(t, dead, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	expired, _ = applyCmd(t, expired, cmd())
	if view := ansi.Strip(expired.View()); !strings.Contains(view, "expires at "+dead.login.ExpiresAt.Local().Format("15:04")) {
		t.Fatalf("an expired code must still name its deadline:\n%s", view)
	}

	// A server that named no expiry gets no deadline invented for it.
	silent := newLoginTestAgent()
	silent.login.ExpiresAt = time.Time{}
	quiet, cmd := applyCmd(t, openConnectPanel(t, silent, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	quiet, _ = applyCmd(t, quiet, cmd())
	if view := ansi.Strip(quiet.View()); strings.Contains(view, "expires") {
		t.Fatalf("panel invented a deadline the authorization server never named:\n%s", view)
	}
}

// TestModel_ConnectWithProviderArgumentStartsTheLoginDirectly: naming a login
// provider skips the list, the same way naming a key provider jumps to the key
// entry.
func TestModel_ConnectWithProviderArgumentStartsTheLoginDirectly(t *testing.T) {
	agent := newLoginTestAgent()
	m := openConnectPanel(t, agent, "/connect openai-codex")
	if !m.connectPanel.open || !m.connectPanel.busy {
		t.Fatalf("panel open=%v busy=%v, want the login started right away", m.connectPanel.open, m.connectPanel.busy)
	}
	if m.connectPanel.entering {
		t.Fatal("a login provider must never reach the key entry stage")
	}
}

// TestModel_ConnectEscCancelsAPendingLogin: a code nobody is waiting for is a
// goroutine polling something the server has already retired.
func TestModel_ConnectEscCancelsAPendingLogin(t *testing.T) {
	agent := newLoginTestAgent()
	m, cmd := applyCmd(t, openConnectPanel(t, agent, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	m, waitCmd := applyCmd(t, m, cmd())

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.connectPanel.open || m.connectPanel.awaiting {
		t.Fatalf("panel open=%v awaiting=%v, want esc to step back to the provider list", m.connectPanel.open, m.connectPanel.awaiting)
	}
	if len(agent.cancels) != 1 || agent.cancels[0].providerID != "openai-codex" {
		t.Fatalf("cancels = %#v, want the pending login cancelled", agent.cancels)
	}
	if m.connectPanel.login.UserCode != "" {
		t.Fatalf("panel still holds the code %q of a cancelled login", m.connectPanel.login.UserCode)
	}
	// The abandoned wait still resolves. It belongs to a retired attempt, so it
	// must not reopen anything nor report an error the user already dismissed.
	m = apply(t, m, waitCmd())
	if m.connectPanel.awaiting || !m.connectPanel.open {
		t.Fatalf("panel awaiting=%v open=%v after the abandoned wait resolved", m.connectPanel.awaiting, m.connectPanel.open)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.connectPanel.open {
		t.Fatal("esc on the list must close the panel")
	}
}

// TestModel_ConnectEscWhileTheCodeIsMintedRetiresTheAttempt: the round trip that
// mints the code is the one part of the flow with nothing on screen, and it is
// bounded by a 30s request timeout while the code it produces stays approvable for
// ten minutes. A user who pressed esc before ever seeing one must not leave a
// poller running behind a closed panel, nor read that code's expiry in the
// transcript ten minutes later with nothing on screen to connect it to.
func TestModel_ConnectEscWhileTheCodeIsMintedRetiresTheAttempt(t *testing.T) {
	agent := newLoginTestAgent()
	m, startCmd := applyCmd(t, openConnectPanel(t, agent, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	if !m.connectPanel.busy {
		t.Fatal("selecting a login provider must show the mint in flight")
	}
	retired := m.connectGen

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.connectPanel.open {
		t.Fatal("esc must close the panel while the code is being minted")
	}

	// The mint lands on a panel that is gone. The login is real server-side, so it
	// is retired rather than left polling a code nobody will ever approve.
	m, waitCmd := applyCmd(t, m, startCmd())
	if waitCmd != nil {
		t.Fatal("an abandoned login must not be awaited: nobody approves a code that was never painted")
	}
	if len(agent.cancels) != 1 || agent.cancels[0] != (cancelledAttempt{providerID: "openai-codex", attempt: 1}) {
		t.Fatalf("cancels = %#v, want the abandoned attempt cancelled by handle", agent.cancels)
	}
	if m.connectPanel.awaiting || m.connectPanel.busy {
		t.Fatalf("panel awaiting=%v busy=%v, want a closed panel untouched by a retired attempt", m.connectPanel.awaiting, m.connectPanel.busy)
	}
	if view := ansi.Strip(m.View()); strings.Contains(view, "V3H5-1MW96") {
		t.Fatalf("a code the user walked away from is on screen:\n%s", view)
	}

	// And if that attempt answers anyway — the wait the old flow dispatched, ten
	// minutes on — the transcript is not where a dismissed attempt reports.
	m = apply(t, m, connectDoneMsg{
		generation: retired,
		providerID: "openai-codex",
		login:      true,
		err:        "the ChatGPT login code expired before it was approved; start the login again",
	})
	if view := ansi.Strip(m.View()); strings.Contains(view, "expired") {
		t.Fatalf("a login the user abandoned reported its expiry in the transcript:\n%s", view)
	}
}

// TestModel_ConnectEscReportsARejectedKeyButNotADismissedLogin: esc closes the
// panel over a request that is already out, and the two kinds of request want
// opposite answers when it comes back.
//
// A key is at the provider being checked. It can still come back rejected, and
// that answer is the only chance the user has to learn nothing was stored —
// swallow it and they walk away believing they connected, until the next turn
// tells them "no credential stored for provider". A login is the reverse: its code
// was never painted and the attempt is retired, so reporting its death minutes
// later is an error about something the user cannot see.
func TestModel_ConnectEscReportsARejectedKeyButNotADismissedLogin(t *testing.T) {
	key := newConnectTestAgent()
	key.connectErr = errors.New("invalid API key")
	m := openConnectPanel(t, key, "/connect openrouter")
	m = typeRunes(t, m, "sk-or-bad")
	m, validate := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.connectPanel.open || m.connectPanel.busy {
		t.Fatalf("panel open=%v busy=%v, want esc to close it while the key is being checked", m.connectPanel.open, m.connectPanel.busy)
	}
	m = apply(t, m, validate())
	if view := ansi.Strip(m.View()); !strings.Contains(view, "invalid API key") {
		t.Fatalf("the provider rejected the key and nothing told the user:\n%s", view)
	}

	login := newLoginTestAgent()
	login.loginErr = errors.New("the ChatGPT login code expired before it was approved; start the login again")
	n, start := applyCmd(t, openConnectPanel(t, login, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	n, wait := applyCmd(t, n, start())
	n = apply(t, n, tea.KeyMsg{Type: tea.KeyEsc})
	n = apply(t, n, wait())
	n = apply(t, n, tea.KeyMsg{Type: tea.KeyEsc})
	if n.connectPanel.open {
		t.Fatal("esc on the list must close the panel")
	}
	if view := ansi.Strip(n.View()); strings.Contains(view, "expired") {
		t.Fatalf("a login the user walked away from reported its expiry:\n%s", view)
	}
}

// TestModel_ConnectALateMintRetiresItselfAndNotTheCodeOnScreen: the dismissed mint
// answers after the retry already painted its code. Retiring "whatever is pending
// for this provider" there takes down the login the user is looking at, and its
// live wait comes back reporting a cancellation nobody performed.
func TestModel_ConnectALateMintRetiresItselfAndNotTheCodeOnScreen(t *testing.T) {
	agent := newLoginTestAgent()
	m, dismissed := applyCmd(t, openConnectPanel(t, agent, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	// The first attempt's code exists — the mint is what esc could not recall — but
	// its message has not been delivered yet.
	stale := dismissed()

	// The user starts again. This second code is the one that reaches the screen.
	m = typeRunes(t, m, "/connect openai-codex")
	m, retry := applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m, wait := applyCmd(t, m, retry())
	if !m.connectPanel.awaiting || wait == nil {
		t.Fatalf("panel awaiting=%v wait=%v, want the retry's code shown and its wait dispatched", m.connectPanel.awaiting, wait != nil)
	}

	m = apply(t, m, stale)
	if len(agent.cancels) != 1 || agent.cancels[0] != (cancelledAttempt{providerID: "openai-codex", attempt: 1}) {
		t.Fatalf("cancels = %#v, want only the dismissed attempt retired, by its own handle", agent.cancels)
	}
	if !m.connectPanel.awaiting || m.connectPanel.login.Attempt != 2 {
		t.Fatalf("panel awaiting=%v attempt=%d, want the retry's code left on screen", m.connectPanel.awaiting, m.connectPanel.login.Attempt)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "V3H5-1MW96") {
		t.Fatalf("the live code left the screen when an older mint landed:\n%s", view)
	}
}

// TestModel_ConnectStaleLoginStartLeavesTheCurrentPanelAlone: the retired attempt
// from the test above lands while the user is already connecting something else.
// Its outcome is not this panel's to write — an error from a subscription login the
// user dismissed, over an API key entry, is an error about nothing they can see,
// and clearing busy would clear it for whatever is actually in flight.
func TestModel_ConnectStaleLoginStartLeavesTheCurrentPanelAlone(t *testing.T) {
	agent := newLoginTestAgent()
	agent.connectable = append(agent.connectable, providerconfig.ConnectableProvider{
		ID: "openrouter", Name: "OpenRouter", Kind: providerconfig.ConnectAPIKey,
	})
	m, startCmd := applyCmd(t, openConnectPanel(t, agent, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})

	// The user moves on to the other provider and starts typing their key.
	m = typeRunes(t, m, "/connect openrouter")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = typeRunes(t, m, "sk-or-typing")

	agent.startErr = errors.New("deviceauth is unavailable")
	stale := startCmd()
	m = apply(t, m, stale)

	if !m.connectPanel.entering || len(m.connectPanel.key) != len("sk-or-typing") {
		t.Fatalf("panel entering=%v key length=%d, want the key entry the user is on", m.connectPanel.entering, len(m.connectPanel.key))
	}
	if m.connectPanel.err != "" {
		t.Fatalf("panel err = %q, want nothing: it belongs to an attempt the user dismissed", m.connectPanel.err)
	}
	if view := ansi.Strip(m.View()); strings.Contains(view, "deviceauth is unavailable") {
		t.Fatalf("a dismissed subscription attempt reported over the key entry:\n%s", view)
	}

	// Same message, now while this panel really does have a request in flight: it
	// may not clear a busy that is not its own.
	m, _ = applyCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.connectPanel.busy {
		t.Fatal("enter must show the validation in flight")
	}
	m = apply(t, m, stale)
	if !m.connectPanel.busy {
		t.Fatal("a retired attempt cleared the busy of the request this panel is waiting on")
	}
	if m.connectPanel.err != "" {
		t.Fatalf("panel err = %q, want the in-flight validation left alone", m.connectPanel.err)
	}
}

// TestModel_ConnectReportsALoginThatCouldNotStart: an authorization server that
// will not mint a code has to say so on the list, not leave a blank stage.
func TestModel_ConnectReportsALoginThatCouldNotStart(t *testing.T) {
	agent := newLoginTestAgent()
	agent.startErr = errors.New("deviceauth is unavailable")
	m, cmd := applyCmd(t, openConnectPanel(t, agent, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, cmd())

	if !m.connectPanel.open || m.connectPanel.awaiting || m.connectPanel.busy {
		t.Fatalf("panel open=%v awaiting=%v busy=%v, want the list with the reason", m.connectPanel.open, m.connectPanel.awaiting, m.connectPanel.busy)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "deviceauth is unavailable") {
		t.Fatalf("panel must show why the login could not start:\n%s", view)
	}
}

// TestModel_ConnectReportsALoginThatExpired: the code died before the user got to
// it, so the panel drops back to the list rather than leaving a dead code up.
func TestModel_ConnectReportsALoginThatExpired(t *testing.T) {
	agent := newLoginTestAgent()
	agent.loginErr = errors.New("the ChatGPT login code expired before it was approved; start the login again")
	m, cmd := applyCmd(t, openConnectPanel(t, agent, "/connect"), tea.KeyMsg{Type: tea.KeyEnter})
	m, waitCmd := applyCmd(t, m, cmd())
	m = apply(t, m, waitCmd())

	if !m.connectPanel.open || m.connectPanel.awaiting {
		t.Fatalf("panel open=%v awaiting=%v, want the list with the reason", m.connectPanel.open, m.connectPanel.awaiting)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "expired") {
		t.Fatalf("panel must show why the login failed:\n%s", view)
	}
}

func TestModel_ConnectMenuOffersTheCommand(t *testing.T) {
	m := NewModel(newConnectTestAgent(), "s1", nil).WithCompletions([]command.Command{{Name: "connect", BuiltIn: true}}, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeRunes(t, m, "/conn")
	found := false
	for _, item := range m.menuItems {
		if item.label == "/connect" {
			found = true
		}
	}
	if !found {
		t.Fatalf("menu items = %#v, want /connect offered", m.menuItems)
	}
}
