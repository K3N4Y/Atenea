package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/providerconfig"
)

// fakeConnectAgent extends fakeAgent with the connectAgent surface: it records
// the keys it receives and flips the provider to connected on success.
type fakeConnectAgent struct {
	*fakeAgent
	connectable []providerconfig.ConnectableProvider
	connects    []struct{ providerID, key string }
	connectErr  error
}

func (f *fakeConnectAgent) ConnectableProviders() []providerconfig.ConnectableProvider {
	return append([]providerconfig.ConnectableProvider(nil), f.connectable...)
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
		fakeAgent:   &fakeAgent{},
		connectable: []providerconfig.ConnectableProvider{{ID: "openrouter", Name: "OpenRouter"}},
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

func TestModel_ConnectMenuOffersTheCommand(t *testing.T) {
	m := NewModel(newConnectTestAgent(), "s1", nil)
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
