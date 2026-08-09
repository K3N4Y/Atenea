package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/providerconfig"
)

type fakeAgentModels struct {
	*fakeAgent
	agents    []agent.Def
	overrides map[string]providerconfig.AgentModelSelection
	effective map[string]providerconfig.AgentModelSelection
	set       []struct {
		name      string
		selection providerconfig.AgentModelSelection
	}
	setErr  error
	cleared []string
}

func (f *fakeAgentModels) AgentCatalog() []agent.Def { return cloneAgentDefs(f.agents) }
func (f *fakeAgentModels) AgentModel(name string) (providerconfig.AgentModelSelection, bool) {
	selection, ok := f.overrides[name]
	return selection, ok
}
func (f *fakeAgentModels) EffectiveAgentModel(name, _ string) (providerconfig.AgentModelSelection, bool) {
	selection, ok := f.effective[name]
	return selection, ok
}
func (f *fakeAgentModels) SetAgentModel(_ context.Context, name string, selection providerconfig.AgentModelSelection) error {
	f.set = append(f.set, struct {
		name      string
		selection providerconfig.AgentModelSelection
	}{name, selection})
	return f.setErr
}
func (f *fakeAgentModels) ClearAgentModel(name string) error {
	f.cleared = append(f.cleared, name)
	return nil
}

func submitText(t *testing.T, m Model, text string) Model {
	t.Helper()
	m = typeRunes(t, m, text)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(Model)
}

func TestAgentsCommandRoutingAndDirectMutation(t *testing.T) {
	fake := &fakeAgentModels{fakeAgent: &fakeAgent{}, agents: []agent.Def{{Name: "review"}}}
	m := submitText(t, NewModel(fake, "s1", nil), "/agents")
	if !m.agentPicker.open || m.composer.value() != "" || len(fake.sent) != 0 {
		t.Fatalf("/agents state: open=%v composer=%q sent=%v", m.agentPicker.open, m.composer.value(), fake.sent)
	}
	setStart := NewModel(fake, "s1", nil)
	setStart = typeRunes(t, setStart, "/agents review openai gpt-5")
	updated, cmd := setStart.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || len(fake.set) != 0 {
		t.Fatalf("set must be deferred: cmd=%v calls=%#v", cmd != nil, fake.set)
	}
	setModel := apply(t, updated.(Model), cmd())
	if len(fake.set) != 1 || fake.set[0].name != "review" || fake.set[0].selection.Provider != "openai" || fake.set[0].selection.Model != "gpt-5" {
		t.Fatalf("set calls = %#v (entries=%#v)", fake.set, setModel.entries)
	}
	if len(setModel.entries) == 0 || setModel.entries[len(setModel.entries)-1].kind != entryNotice {
		t.Fatalf("successful set entries = %#v", setModel.entries)
	}
	clearModel := submitText(t, NewModel(fake, "s1", nil), "/agents review inherit")
	if len(fake.cleared) != 1 || fake.cleared[0] != "review" {
		t.Fatalf("clear calls = %#v (entries=%#v)", fake.cleared, clearModel.entries)
	}
	unknown := submitText(t, NewModel(fake, "s1", nil), "/agents missing inherit")
	if !strings.Contains(unknown.entries[len(unknown.entries)-1].text, "unknown subagent") {
		t.Fatalf("unknown agent error = %#v", unknown.entries[len(unknown.entries)-1])
	}
	invalid := submitText(t, NewModel(fake, "s1", nil), "/agents review bogus")
	if !strings.Contains(invalid.entries[len(invalid.entries)-1].text, "usage:") {
		t.Fatalf("invalid argument error = %#v", invalid.entries[len(invalid.entries)-1])
	}
	if _, intercepted := parseLocalCommand("/agentset"); intercepted {
		t.Fatal("/agentset was incorrectly intercepted as /agents")
	}
}

func TestAgentPickerModelEffortAndInheritFlows(t *testing.T) {
	fake := &fakeAgentModels{
		fakeAgent: &fakeAgent{models: []providerconfig.ProviderModels{{ID: "openai", Name: "OpenAI", Models: []string{"gpt-5"}}}},
		agents:    []agent.Def{{Name: "review"}},
	}
	m := submitText(t, NewModel(fake, "s1", nil), "/agents")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.modelPicker.open || m.modelPicker.agentName != "review" || len(m.modelPicker.providers) < 2 || m.modelPicker.providers[0].ID != "" {
		t.Fatalf("agent model picker = %#v", m.modelPicker)
	}

	m.modelPicker.selectProvider(1)
	m.modelPicker.modelsFocused = true
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.variantsPicker.open || m.variantsPicker.agentName != "review" {
		t.Fatalf("effort picker not opened: %#v", m.variantsPicker)
	}
	m.variantsPicker.selected = 4 // high
	updated, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || len(fake.set) != 0 {
		t.Fatalf("variant set must be deferred: cmd=%v calls=%#v", cmd != nil, fake.set)
	}
	m = apply(t, updated.(Model), cmd())
	if len(fake.set) != 1 || fake.set[0].selection.ReasoningEffort != llm.ReasoningEffortHigh {
		t.Fatalf("persisted selection = %#v", fake.set)
	}

	m = submitText(t, m, "/agents")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m.modelPicker.modelsFocused = true
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.cleared) != 1 || m.modelPicker.open {
		t.Fatalf("inherit clear calls=%v pickerOpen=%v", fake.cleared, m.modelPicker.open)
	}
}

func TestAgentPickerEmptySanitizedAndWindowedInSmallTerminal(t *testing.T) {
	empty := NewModel(&fakeAgentModels{fakeAgent: &fakeAgent{}}, "s1", nil)
	empty.width, empty.height = 24, 10
	empty.agentPicker = newAgentPicker(nil)
	view := empty.agentPickerView()
	if !strings.Contains(view, "No subagents") || ansi.StringWidth(view) == 0 {
		t.Fatalf("empty picker view = %q", view)
	}

	agents := make([]agent.Def, 20)
	for i := range agents {
		agents[i] = agent.Def{Name: "agent\n\x1b[31m" + string(rune('a'+i)), Description: "desc\rspoof"}
	}
	m := NewModel(&fakeAgentModels{fakeAgent: &fakeAgent{}, agents: agents}, "s1", nil)
	m.width, m.height = 30, 8
	m.agentPicker = newAgentPicker(agents)
	for range 17 {
		m.agentPicker.move(1)
	}
	view = m.agentPickerView()
	if strings.Contains(view, "\x1b[31m") || strings.Contains(view, "\r") {
		t.Fatalf("unsafe terminal text survived: %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > m.width {
			t.Fatalf("picker line width %d exceeds terminal width %d: %q", width, m.width, line)
		}
	}
	if !strings.Contains(view, "r") { // selected agent index 17 remains in the window
		t.Fatalf("selected window is not visible: %q", view)
	}
}

func TestAgentPickerRefreshPreservesInheritSelection(t *testing.T) {
	m := NewModel(&fakeAgentModels{fakeAgent: &fakeAgent{}}, "s1", nil)
	m.modelPicker = newAgentModelPicker([]providerconfig.ProviderModels{{ID: "p", Models: []string{"old"}}}, providerconfig.Active{}, "review")
	m = apply(t, m, ModelsRefreshedMsg{Providers: []providerconfig.ProviderModels{{ID: "p", Models: []string{"new"}}}})
	provider, ok := m.modelPicker.selectedProvider()
	if !ok || provider.ID != "" {
		t.Fatalf("refresh moved selection away from Inherit: %#v", provider)
	}
}

func TestAgentPickerShowsEffectiveProviderForImplicitOverride(t *testing.T) {
	fake := &fakeAgentModels{
		fakeAgent: &fakeAgent{},
		agents:    []agent.Def{{Name: "review"}},
		overrides: map[string]providerconfig.AgentModelSelection{"review": {Model: "gpt-5"}},
		effective: map[string]providerconfig.AgentModelSelection{"review": {Provider: "openai", Model: "gpt-5"}},
	}
	m := NewModel(fake, "s1", nil)
	m.width, m.height = 80, 12
	m.agentPicker = newAgentPicker(fake.agents)
	view := m.agentPickerView()
	if !strings.Contains(view, "openai/gpt-5") || !strings.Contains(view, "(override)") {
		t.Fatalf("implicit override resolution is not explicit: %q", view)
	}
}

func TestAgentPickerPreservesEffortForSameEffectiveSelection(t *testing.T) {
	fake := &fakeAgentModels{
		fakeAgent: &fakeAgent{models: []providerconfig.ProviderModels{{ID: "openai", Models: []string{"gpt-5"}}}},
		agents:    []agent.Def{{Name: "review"}},
		overrides: map[string]providerconfig.AgentModelSelection{"review": {Model: "gpt-5", ReasoningEffort: llm.ReasoningEffortHigh}},
		effective: map[string]providerconfig.AgentModelSelection{"review": {Provider: "openai", Model: "gpt-5", ReasoningEffort: llm.ReasoningEffortHigh}},
	}
	m := NewModel(fake, "s1", nil)
	m.modelPicker = newAgentModelPicker(fake.models, providerconfig.Active{ProviderID: "openai", Model: "gpt-5"}, "review")
	m.modelPicker.modelsFocused = true
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := reasoningVariants[m.variantsPicker.selected]; got != llm.ReasoningEffortHigh {
		t.Fatalf("preloaded effort = %q, want high", got)
	}
}

func TestAgentModelSetResultDisplaysError(t *testing.T) {
	fake := &fakeAgentModels{fakeAgent: &fakeAgent{}, agents: []agent.Def{{Name: "review"}}, setErr: errors.New("credential failed")}
	m := NewModel(fake, "s1", nil)
	cmd := setAgentModelCmd(fake, "review", providerconfig.AgentModelSelection{Provider: "p", Model: "m"})
	m = apply(t, m, cmd())
	if len(m.entries) == 0 || m.entries[len(m.entries)-1].kind != entryError ||
		!strings.Contains(m.entries[len(m.entries)-1].text, "credential failed") {
		t.Fatalf("failed set entries = %#v", m.entries)
	}
}
