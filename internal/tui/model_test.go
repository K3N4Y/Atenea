package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"atenea/internal/command"
	"atenea/internal/llm"
	"atenea/internal/providerconfig"
	"atenea/internal/session"
)

// fakeAgent implementa Agent y registra las llamadas para asertar sobre ellas.
type fakeAgent struct {
	sent         []struct{ sessionID, text string }
	planSent     []struct{ sessionID, text string }
	newSessionID string
	resolved     []struct {
		sessionID, callID string
		approved          bool
	}
	stopped    []string
	accepted   []string
	models     []providerconfig.ProviderModels
	active     providerconfig.Active
	selected   []struct{ providerID, model string }
	refreshes  int
	undos      []string
	undoResult UndoResult
	undoErr    error
}

func (f *fakeAgent) ModelCatalog() []providerconfig.ProviderModels {
	return cloneProviderModels(f.models)
}
func (f *fakeAgent) CurrentModel() providerconfig.Active { return f.active }
func (f *fakeAgent) SelectModel(providerID, model string) (providerconfig.Active, error) {
	f.selected = append(f.selected, struct{ providerID, model string }{providerID, model})
	for _, provider := range f.models {
		if provider.ID == providerID {
			f.active = providerconfig.Active{ProviderID: providerID, ProviderName: provider.Name, Model: model}
			return f.active, nil
		}
	}
	return providerconfig.Active{}, fmt.Errorf("unknown provider")
}
func (f *fakeAgent) RefreshModels() { f.refreshes++ }

func (f *fakeAgent) SendPrompt(sessionID, text string) (string, error) {
	f.sent = append(f.sent, struct{ sessionID, text string }{sessionID, text})
	if text == "/new" && f.newSessionID != "" {
		return f.newSessionID, nil
	}
	return sessionID, nil
}

func (f *fakeAgent) SendPlanPrompt(sessionID, text string) error {
	f.planSent = append(f.planSent, struct{ sessionID, text string }{sessionID, text})
	return nil
}

func (f *fakeAgent) AcceptPlan(sessionID string) error {
	f.accepted = append(f.accepted, sessionID)
	return nil
}

func (f *fakeAgent) ResolvePermission(sessionID, callID string, approved bool) {
	f.resolved = append(f.resolved, struct {
		sessionID, callID string
		approved          bool
	}{sessionID, callID, approved})
}

func (f *fakeAgent) Stop(sessionID string) {
	f.stopped = append(f.stopped, sessionID)
}

func (f *fakeAgent) Undo(sessionID string) (UndoResult, error) {
	f.undos = append(f.undos, sessionID)
	return f.undoResult, f.undoErr
}

func TestModel_UndoIsNativeCommandAndRestoresComposer(t *testing.T) {
	fake := &fakeAgent{undoResult: UndoResult{
		Prompt: "original prompt",
		Events: []session.SessionEvent{{Message: &session.Message{ID: "u0", Role: session.RoleUser, Text: "kept"}}},
	}}
	m := NewModel(fake, "s1", nil)
	m.entries = []entry{{kind: entryUser, text: "old"}, {kind: entryAssistant, text: "answer"}}
	m.history = []string{"old prompt"}
	m.histIdx = len(m.history)
	m = typeRunes(t, m, "/undo")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("undo must run asynchronously")
	}
	m = apply(t, m, cmd())
	if len(fake.undos) != 1 || fake.undos[0] != "s1" {
		t.Fatalf("Undo calls = %v", fake.undos)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt calls = %v", fake.sent)
	}
	if len(m.entries) != 1 || m.entries[0].kind != entryUser || m.entries[0].text != "kept" {
		t.Fatalf("entries = %+v", m.entries)
	}
	if m.input.Value() != "original prompt" || m.input.Position() != len([]rune("original prompt")) {
		t.Fatalf("composer = %q cursor=%d", m.input.Value(), m.input.Position())
	}
	if len(m.history) != 1 || m.history[0] != "old prompt" {
		t.Fatalf("history = %v", m.history)
	}
}

func TestModel_UndoFailureKeepsTranscriptAndComposer(t *testing.T) {
	fake := &fakeAgent{undoErr: errors.New("undo failed")}
	m := NewModel(fake, "s1", nil)
	m.entries = []entry{{kind: entryUser, text: "old"}}
	m = typeRunes(t, m, "/undo")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	m = apply(t, m, cmd())
	if m.input.Value() != "/undo" {
		t.Fatalf("composer = %q", m.input.Value())
	}
	if len(m.entries) != 2 || m.entries[0].text != "old" || m.entries[1].kind != entryError || m.entries[1].text != "undo failed" {
		t.Fatalf("entries = %+v", m.entries)
	}
}

func TestModel_UndoAppearsInSlashCompletion(t *testing.T) {
	engine := NewEngine(EngineConfig{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})
	commands := engine.Commands()
	found := false
	for _, item := range commands {
		if item.Name == "undo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Commands = %+v", commands)
	}
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(commands, nil)
	m = typeRunes(t, m, "/un")
	if len(m.menuItems) == 0 || m.menuItems[0].label != "/undo" {
		t.Fatalf("menuItems = %+v", m.menuItems)
	}
}

func TestModel_UndoWithArgumentsIsRejectedLocally(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = typeRunes(t, m, "/undo now")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.undos) != 0 || len(fake.sent) != 0 {
		t.Fatalf("undos=%v sent=%v", fake.undos, fake.sent)
	}
	if len(m.entries) != 1 || m.entries[0].kind != entryError || m.entries[0].text != "usage: /undo" {
		t.Fatalf("entries = %+v", m.entries)
	}
}

// apply pasa un mensaje por Update y devuelve el Model concreto.
func apply(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	return next
}

// drainReveal aplica ticks de reveal hasta agotar el backlog del smooth
// streaming: los tests cuya asercion presupone el texto ya revelado lo usan
// para no depender del ritmo de la animacion. El tope de iteraciones evita
// colgar el test si el reveal dejara de avanzar.
func drainReveal(t *testing.T, m Model) Model {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if !m.hasBacklog() {
			return m
		}
		m = apply(t, m, revealTickMsg{})
	}
	t.Fatalf("el backlog del reveal no se agoto tras 1000 ticks")
	return m
}

func TestModel_ModelCommandCompletesInlineThenSelects(t *testing.T) {
	agent := &fakeAgent{
		models: []providerconfig.ProviderModels{
			{ID: "openrouter", Name: "OpenRouter", Models: []string{"old", "openai/chatgpt5.5"}},
			{ID: "openai", Name: "OpenAI", Models: []string{"chatgpt5.5"}},
		},
		active: providerconfig.Active{ProviderID: "openrouter", ProviderName: "OpenRouter", Model: "old"},
	}
	m := NewModel(agent, "s1", nil).WithStatus("build", "old")
	m = typeRunes(t, m, "/model chatgpt5.5")
	view := m.View()
	lineWith(t, view, "openai/chatgpt5.5")
	lineWith(t, view, "OpenRouter")
	lineWith(t, view, "OpenAI")
	lineWith(t, view, "chatgpt5.5")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "/model openrouter openai/chatgpt5.5 " {
		t.Fatalf("first Enter completed %q", got)
	}
	m = apply(t, m, ModelsRefreshedMsg{Providers: agent.models})
	if len(m.menuItems) != 0 {
		t.Fatalf("refresh reopened popup over canonical command: %#v", m.menuItems)
	}
	if len(agent.sent) != 0 || len(m.history) != 0 {
		t.Fatalf("/model leaked to prompts/history: sent=%v history=%v", agent.sent, m.history)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.model; got != "openai/chatgpt5.5" {
		t.Fatalf("footer model = %q", got)
	}
	if len(agent.selected) != 1 || agent.selected[0].providerID != "openrouter" || agent.selected[0].model != "openai/chatgpt5.5" {
		t.Fatalf("selected = %#v", agent.selected)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input after selection = %q", got)
	}
}

func TestModel_ModelPopupKeepsDistinctProviderNameAndID(t *testing.T) {
	agent := &fakeAgent{models: []providerconfig.ProviderModels{{ID: "ollama", Name: "Local", Models: []string{"qwen"}}}}
	m := NewModel(agent, "s1", nil)
	m = typeRunes(t, m, "/model qwen")
	if view := m.View(); !strings.Contains(view, "Local · ollama") {
		t.Fatalf("distinct provider identity missing:\n%s", view)
	}
}

func TestModel_OpenRouterCuratedModelsShowContext(t *testing.T) {
	agent := &fakeAgent{models: []providerconfig.ProviderModels{{ID: "openrouter", Name: "OpenRouter", Models: []string{"tencent/hy3:free", "poolside/laguna-xs-2.1:free", "cohere/north-mini-code:free"}}}}
	m := NewModel(agent, "s1", nil)
	m = typeRunes(t, m, "/model ")
	view := m.View()
	for _, want := range []string{"tencent/hy3:free", "262K context", "poolside/laguna-xs-2.1:free", "cohere/north-mini-code:free", "256K context"} {
		if !strings.Contains(view, want) {
			t.Fatalf("model popup missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(strings.ToLower(view), "openrouter · openrouter") {
		t.Fatalf("provider should be shown once:\n%s", view)
	}
}

// lineWith devuelve la primera linea de view que contiene needle, o falla.
func lineWith(t *testing.T, view, needle string) string {
	t.Helper()
	return strings.Split(view, "\n")[lineIndexWith(t, view, needle)]
}

// lineIndexWith devuelve el indice (base 0) de la primera linea de view que
// contiene needle; falla el test si ninguna lo contiene.
func lineIndexWith(t *testing.T, view, needle string) int {
	t.Helper()
	for i, line := range strings.Split(view, "\n") {
		if strings.Contains(line, needle) {
			return i
		}
	}
	t.Fatalf("View() = %q, no contiene ninguna linea con %q", view, needle)
	return -1
}

// assertNoLineWiderThan falla si alguna linea de view excede width celdas
// visibles (ancho de la terminal); mide con lipgloss.Width para ignorar ANSI.
func assertNoLineWiderThan(t *testing.T, view string, width int) {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Fatalf("View() = %q, la linea %q mide %d celdas visibles, ninguna linea debe exceder el ancho de la terminal (%d)", view, line, w, width)
		}
	}
}

// assertBoxLinesExactWidth falla si alguna linea de la caja del composer (las
// que contienen un caracter de borde ╭/│/╰ tras el margen) no mide exactamente width
// celdas visibles, o si la vista no contiene ninguna. Mide con
// ansi.StringWidth para ignorar codigos ANSI.
func assertBoxLinesExactWidth(t *testing.T, view string, width int) {
	t.Helper()
	found := false
	for _, line := range strings.Split(view, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		for _, prefix := range []string{"╭", "│", "╰"} {
			if strings.HasPrefix(trimmed, prefix) {
				found = true
				if w := ansi.StringWidth(line); w != width {
					t.Fatalf("View() = %q, la linea de la caja %q mide %d celdas visibles, cada linea de la caja debe medir exactamente el ancho de la terminal (%d)", view, line, w, width)
				}
			}
		}
	}
	if !found {
		t.Fatalf("View() = %q, no contiene ninguna linea de la caja del composer (bordes ╭/│/╰)", view)
	}
}

// forceANSI256Profile fija el perfil de color ANSI256 durante el test: sin TTY
// el perfil es Ascii y ningun color se emite, asi que los tests que asertan
// sobre secuencias SGR lo necesitan para que los colores sean observables.
// El renderer de glamour se memoiza en markdownRendererCache keyed solo por
// wrap y queda clavado al perfil con que se construyo: hay que invalidarlo al
// cambiar el perfil (si no el test reusa un renderer Ascii construido por otro
// test y pasa en falso) y tambien al limpiar (si no un renderer ANSI256
// envenena a los tests siguientes).
func forceANSI256Profile(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	markdownRendererCache.renderer = nil
	t.Cleanup(func() {
		lipgloss.SetColorProfile(prev)
		markdownRendererCache.renderer = nil
	})
}

func TestEntry_UserMessageMatchesReferenceWithoutTimestamp(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	view := entry{kind: entryUser, text: "quien eres y que eres capaz de hacer?"}.render(80)
	plain := ansi.Strip(view)
	lines := strings.Split(plain, "\n")

	if len(lines) != 3 {
		t.Fatalf("user message lines = %d, want 3:\n%q", len(lines), plain)
	}
	for _, line := range strings.Split(view, "\n") {
		if got := ansi.StringWidth(line); got != 80 {
			t.Fatalf("user message line width = %d, want 80:\n%q", got, view)
		}
	}
	if got, want := lines[1], "     ❯ quien eres y que eres capaz de hacer?"; !strings.HasPrefix(got, want) {
		t.Fatalf("middle line = %q, want prefix %q", got, want)
	}
	if !strings.Contains(view, "\x1b[48;2;36;36;36m") {
		t.Fatalf("user message must use reference background #242424:\n%q", view)
	}
	if strings.Contains(view, "\x1b[1m") {
		t.Fatalf("user message text must not be bold:\n%q", view)
	}
	if strings.Contains(plain, "12:50 AM") {
		t.Fatalf("user message must not render a timestamp:\n%q", plain)
	}
}

func TestModel_UserMessageWrapsInsideReferenceBlock(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 32, Height: 12})
	m = apply(t, m, EventMsg{Message: &session.Message{
		ID:   "u1",
		Role: session.RoleUser,
		Text: "un mensaje suficientemente largo para envolver dentro del bloque",
	}})

	view := m.View()
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 32 {
			t.Fatalf("user message line width = %d, want <= 32:\n%q", width, view)
		}
	}
	if got := strings.Count(view, "\x1b[48;2;36;36;36m"); got < 3 {
		t.Fatalf("reference background rows = %d, want at least 3:\n%q", got, view)
	}
}

func TestModel_UserMessageKeepsGrayBackgroundAfterFaintMarker(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}})

	view := m.View()
	grayText := "\x1b[48;2;36;36;36mhola"
	if !strings.Contains(view, grayText) {
		t.Fatalf("user text must restore #242424 after the faint marker; want %q in:\n%q", grayText, view)
	}
}

func TestModel_FoldsStreamingAssistantText(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "Hola "})
	m = drainReveal(t, m)
	if got := ansi.Strip(m.View()); !strings.Contains(got, "Hola") {
		t.Fatalf("View() sin ANSI = %q, debe contener %q tras el primer delta", got, "Hola")
	}

	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "mundo"})
	m = drainReveal(t, m)
	if got := ansi.Strip(m.View()); !strings.Contains(got, "Hola mundo") {
		t.Fatalf("View() sin ANSI = %q, debe contener %q tras acumular deltas", got, "Hola mundo")
	}

	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "Hola mundo"},
	})
	if got, count := m.View(), strings.Count(m.View(), "Hola mundo"); count != 1 {
		t.Fatalf("View() = %q, %q debe aparecer exactamente una vez (count=%d): cerrar el turno no debe duplicar el bloque en vivo con el Message coalescido", got, "Hola mundo", count)
	}
}

// Contrato de render del assistant: mientras el bloque esta vivo (solo
// TextStarted/TextDelta, sin StepEnded) el texto se muestra plano tal cual
// llega; al cerrarse el turno (StepEnded pone live = false) el texto se rinde
// como markdown: los marcadores crudos (** y "- ") desaparecen y el contenido
// queda formateado (enfasis aplicado, listas con bullets).
func TestModel_RendersClosedAssistantAsMarkdown(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	text := "Hola **fuerte** dicho.\n\n- item uno\n- item dos"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "fuerte") {
		t.Fatalf("View() = %q, debe contener %q: rendir markdown no debe perder el contenido", view, "fuerte")
	}
	if strings.Contains(view, "**") {
		t.Fatalf("View() = %q, NO debe contener %q: con el bloque cerrado el enfasis markdown se rinde, no se muestra crudo", view, "**")
	}
	if strings.Contains(view, "- item uno") {
		t.Fatalf("View() = %q, NO debe contener %q: con el bloque cerrado el guion crudo de lista se rinde como bullet", view, "- item uno")
	}
	if !strings.Contains(view, "item uno") {
		t.Fatalf("View() = %q, debe contener %q: rendir la lista no debe perder sus items", view, "item uno")
	}
	if !strings.Contains(view, "item dos") {
		t.Fatalf("View() = %q, debe contener %q: rendir la lista no debe perder sus items", view, "item dos")
	}
	if !strings.Contains(view, "•") {
		t.Fatalf("View() = %q, debe contener %q: los items de lista markdown se rinden con bullet", view, "•")
	}
}

func TestModel_LiveAssistantRendersMarkdownBeforeClosed(t *testing.T) {
	// TRIANGULATE: el renderer debe aplicar Markdown tanto al prefijo revelado
	// durante streaming como al contenido completo al asentarse el bloque.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	text := "esto es **fuerte** en vivo"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = drainReveal(t, m)

	view := ansi.Strip(m.View())
	if strings.Contains(view, "**") {
		t.Fatalf("View() sin ANSI = %q, NO debe contener marcadores Markdown crudos mientras el bloque esta vivo", view)
	}
	if !strings.Contains(view, "fuerte") {
		t.Fatalf("View() sin ANSI = %q, debe contener %q mientras el bloque esta vivo", view, "fuerte")
	}

	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})

	view = ansi.Strip(m.View())
	if strings.Contains(view, "**") {
		t.Fatalf("View() = %q, NO debe contener %q tras cerrar el bloque: al cerrarse el turno el enfasis markdown se rinde, no se muestra crudo", view, "**")
	}
	if !strings.Contains(view, "fuerte") {
		t.Fatalf("View() = %q, debe contener %q: rendir el markdown no debe perder el contenido", view, "fuerte")
	}
}

func TestModel_ClosedMarkdownWrapsToTerminalWidth(t *testing.T) {
	// TRIANGULATE: un renderMarkdown que ignora el ancho (WithWordWrap(0)
	// siempre), o que pasa el ancho completo sin descontar el margen del
	// documento de glamour, produce lineas mas anchas que la terminal. El
	// envolvimiento de emergencia del viewport (ansi.Wrap en syncViewport) las
	// re-parte y deja palabras huerfanas sin margen en la columna 0. El markdown
	// cerrado debe envolverse al ancho de la terminal por el propio renderer:
	// todo el texto visible y cada linea envuelta conservando su margen.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 30, Height: 12})

	text := "este parrafo largo con **enfasis** debe envolverse al ancho angosto de la terminal para poder leerse entero hasta el token fin-markdown"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "fin-markdown") {
		t.Fatalf("View() = %q, el final del texto %q debe estar visible: el markdown cerrado debe envolverse al ancho de la terminal, no truncarse", view, "fin-markdown")
	}
	assertNoLineWiderThan(t, view, 30)
	for _, token := range []string{"enfasis", "fin-markdown"} {
		line := ansi.Strip(lineWith(t, view, token))
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("linea con %q = %q, debe conservar el margen del render markdown: una linea rendida mas ancha que la terminal la re-parte el envolvimiento de emergencia del viewport y deja el resto huerfano en la columna 0", token, line)
		}
	}
}

func TestModel_StepEndedMessageRendersAsMarkdown(t *testing.T) {
	// TRIANGULATE: una implementacion pobre solo rendiria markdown si hubo
	// deltas de streaming. Cuando el bloque vivo quedo vacio y es el Message
	// coalescido del StepEnded quien rellena el texto (ver foldEvent,
	// KindStepEnded), ese texto tambien debe rendirse como markdown al cerrar.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "- solo item"},
	})

	view := m.View()
	if strings.Contains(view, "- solo item") {
		t.Fatalf("View() = %q, NO debe contener %q: el guion crudo de lista se rinde como bullet aunque el texto llegue por el Message del StepEnded sin deltas previos", view, "- solo item")
	}
	if !strings.Contains(view, "solo item") {
		t.Fatalf("View() = %q, debe contener %q: rendir la lista no debe perder el item", view, "solo item")
	}
	if !strings.Contains(view, "•") {
		t.Fatalf("View() = %q, debe contener %q: el item de lista markdown se rinde con bullet", view, "•")
	}
}

func TestEntryAssistant_RenderRendersRevealedMarkdownWhileLive(t *testing.T) {
	entry := entry{
		kind:     entryAssistant,
		text:     "**Hola** mundo",
		live:     true,
		revealed: len([]rune("**Hola**")),
	}

	rendered := ansi.Strip(entry.render(80))
	if strings.Contains(rendered, "**Hola**") {
		t.Fatalf("render(80) = %q, no debe contener el marcador markdown crudo mientras el assistant sigue en vivo", rendered)
	}
	if !strings.Contains(rendered, "Hola") {
		t.Fatalf("render(80) = %q, debe contener el texto markdown ya revelado", rendered)
	}
	if strings.Contains(rendered, "mundo") {
		t.Fatalf("render(80) = %q, no debe revelar el backlog pendiente %q", rendered, "mundo")
	}
}

func TestEntryAssistant_RenderRendersRevealedListWhileLiveAndCompleteListWhenSettled(t *testing.T) {
	entry := entry{
		kind:     entryAssistant,
		text:     "- item visible\n- item pendiente",
		live:     true,
		revealed: len([]rune("- item visible\n")),
	}

	live := ansi.Strip(entry.render(80))
	if !strings.Contains(live, "•") || !strings.Contains(live, "item visible") {
		t.Fatalf("render(80) vivo = %q, debe rendir el item revelado como lista Markdown", live)
	}
	if strings.Contains(live, "item pendiente") {
		t.Fatalf("render(80) vivo = %q, no debe filtrar el item pendiente", live)
	}

	entry.live = false
	entry.revealed = len([]rune(entry.text))
	settled := ansi.Strip(entry.render(80))
	for _, want := range []string{"•", "item visible", "item pendiente"} {
		if !strings.Contains(settled, want) {
			t.Fatalf("render(80) asentado = %q, debe contener %q", settled, want)
		}
	}
}

// Contrato del color del texto asentado del assistant: el markdown cerrado se
// rinde con el color por defecto de la terminal, NO con el gris "252" que fija
// Document.Color del estilo "dark" de glamour (el texto se ve apagado frente
// al resto de la vista). El resto del tema (headings con color, etc.) se
// conserva; aqui solo se prohibe el gris del documento. Se fuerza ANSI256
// (forceANSI256Profile) para que el gris sea observable en la salida.
func TestModel_AssistantMarkdownUsesDefaultForeground(t *testing.T) {
	forceANSI256Profile(t)

	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	text := "texto-asentado con **enfasis** del assistant"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})
	m = drainReveal(t, m)

	view := ansi.Strip(m.View())
	if plain := ansi.Strip(view); !strings.Contains(plain, "texto-asentado") {
		t.Fatalf("View() sin ANSI = %q, debe contener %q: quitar el gris del documento no debe perder el contenido del texto", plain, "texto-asentado")
	}
	if strings.Contains(view, "38;5;252") {
		t.Fatalf("View() = %q, NO debe contener la secuencia SGR %q: el texto asentado del assistant se rinde con el color por defecto de la terminal, no con el gris 252 del estilo dark de glamour", view, "38;5;252")
	}
}

func TestModel_ViewPaintsCompleteDarkCanvas(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(previousProfile)
	})

	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 32, Height: 10})

	view := m.View()
	if !strings.Contains(view, "\x1b[48;2;20;20;20m") {
		t.Fatalf("View() = %q, want #141414 true-color background", view)
	}

	lines := strings.Split(ansi.Strip(view), "\n")
	if len(lines) != 10 {
		t.Fatalf("View() has %d lines, want 10", len(lines))
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 32 {
			t.Fatalf("line %d width = %d, want 32", i, got)
		}
	}
}

func TestModel_ViewRestoresDarkCanvasAfterChildStyleResets(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(previousProfile)
	})

	background := "\x1b[48;2;20;20;20m"
	leadingANSI := func(value string) string {
		var prefix strings.Builder
		for strings.HasPrefix(value, "\x1b[") {
			end := -1
			for i := 2; i < len(value); i++ {
				if value[i] >= 0x40 && value[i] <= 0x7e {
					end = i + 1
					break
				}
			}
			if end < 0 {
				break
			}
			prefix.WriteString(value[:end])
			value = value[end:]
		}
		return prefix.String()
	}
	tests := []struct {
		name  string
		model func() Model
	}{
		{name: "chat", model: func() Model { return NewModel(nil, "s1", nil) }},
		{name: "explorer", model: func() Model {
			m := NewModel(nil, "s1", nil)
			m.treeOpen = true
			return m
		}},
		{name: "file viewer", model: func() Model {
			m := NewModel(nil, "s1", nil)
			m.viewer = openFileViewer("example.go", []byte("package example\n"))
			return m
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := apply(t, tt.model(), tea.WindowSizeMsg{Width: 40, Height: 12})
			for lineNumber, line := range strings.Split(m.View(), "\n") {
				for _, reset := range []string{"\x1b[0m", "\x1b[m"} {
					remaining := line
					for {
						_, after, found := strings.Cut(remaining, reset)
						if !found {
							break
						}
						if ansi.Strip(after) != "" && !strings.Contains(leadingANSI(after), background) {
							t.Fatalf("line %d = %q, reset %q must restore the canvas background before rendering more cells", lineNumber+1, line, reset)
						}
						remaining = after
					}
				}
			}
		})
	}
}

func TestModel_ViewPaintsDarkCanvasAcrossLayouts(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(previousProfile)
	})

	tests := []struct {
		name  string
		model func() Model
	}{
		{
			name: "chat",
			model: func() Model {
				return NewModel(nil, "s1", nil)
			},
		},
		{
			name: "explorer",
			model: func() Model {
				m := NewModel(nil, "s1", nil)
				m.treeOpen = true
				return m
			},
		},
		{
			name: "file viewer",
			model: func() Model {
				m := NewModel(nil, "s1", nil)
				m.viewer = openFileViewer("example.go", []byte("package example\n"))
				return m
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := apply(t, tt.model(), tea.WindowSizeMsg{Width: 40, Height: 12})
			view := m.View()
			if !strings.Contains(view, "\x1b[48;2;20;20;20m") {
				t.Fatalf("View() = %q, want #141414 true-color background", view)
			}

			lines := strings.Split(ansi.Strip(view), "\n")
			if len(lines) != 12 {
				t.Fatalf("View() has %d lines, want 12", len(lines))
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got != 40 {
					t.Fatalf("line %d width = %d, want 40", i, got)
				}
			}
		})
	}
}

func TestModel_ViewDarkCanvasWithoutWindowSizeDoesNotPad(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(previousProfile)
	})

	view := NewModel(nil, "s1", nil).View()
	if view == "" {
		t.Fatal("View() must remain non-empty before the first WindowSizeMsg")
	}
	if !strings.Contains(view, "\x1b[48;2;20;20;20m") {
		t.Fatalf("View() = %q, want #141414 true-color background", view)
	}
	for i, line := range strings.Split(ansi.Strip(view), "\n") {
		if got := lipgloss.Width(line); got >= 80 {
			t.Fatalf("line %d width = %d, unknown-size view must not assume an 80-column terminal", i, got)
		}
	}
}

func TestModel_AssistantMarkdownKeepsDarkThemeAccents(t *testing.T) {
	// TRIANGULATE: una implementacion pobre "arregla" el gris del documento
	// cambiando al estilo notty/ascii o quitando TODOS los colores del tema:
	// pasa el test del gris (no queda ningun 38;5;252 porque no queda ningun
	// color) pero apaga los acentos del tema dark. Anular Document.Color debe
	// ser quirurgico: un heading markdown asentado sigue rendiendo el color de
	// headings del tema dark (Color "39" -> SGR 38;5;39 en ANSI256) a la vez
	// que el gris 252 del documento no aparece.
	forceANSI256Profile(t)

	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	text := "## Titulo\n\ntexto"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})
	m = drainReveal(t, m)

	view := m.View()
	if plain := ansi.Strip(view); !strings.Contains(plain, "Titulo") {
		t.Fatalf("View() sin ANSI = %q, debe contener %q: conservar el tema no debe perder el contenido del heading", plain, "Titulo")
	}
	if !strings.Contains(view, "38;5;39") {
		t.Fatalf("View() = %q, debe contener la secuencia SGR %q: el heading markdown asentado conserva el color de headings del tema dark de glamour; quitar TODOS los colores (o caer al estilo notty/ascii) no es la solucion al gris del documento", view, "38;5;39")
	}
	if strings.Contains(view, "38;5;252") {
		t.Fatalf("View() = %q, NO debe contener la secuencia SGR %q: solo se anula el gris del documento, no el resto del tema", view, "38;5;252")
	}
}

func TestModel_RendersUserMessages(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// El mensaje del usuario llega SIN Kind: el runner promueve el prompt como
	// SessionEvent{Message: {Role: user}}.
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola atenea"}})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "hola humano"})
	m = drainReveal(t, m)

	view := m.View()
	userLine := lineWith(t, ansi.Strip(view), "hola atenea")
	if !strings.HasPrefix(userLine, "     ❯ ") {
		t.Fatalf("linea del usuario = %q, debe llevar el marcador %q y la sangria visual de referencia", userLine, "     ❯ ")
	}
	assistantLine := lineWith(t, ansi.Strip(view), "hola humano")
	if strings.Contains(assistantLine, "❯ ") {
		t.Fatalf("linea del assistant = %q, NO debe llevar el marcador de usuario %q", assistantLine, "❯ ")
	}
}

func TestModel_RendersToolCallLifecycle(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	if got := m.View(); !strings.Contains(got, "● bash     ls") {
		t.Fatalf("View() = %q, Tool.Called debe mostrar el ToolName con el resumen del Input y el marcador de ejecucion %q", got, "● bash     ls")
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "archivo.txt",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "archivo.txt", ToolCallID: "c1"},
	})
	if got := m.View(); !strings.Contains(got, "✓ bash     ls") {
		t.Fatalf("View() = %q, Tool.Success debe asentar la tool como %q", got, "✓ bash     ls")
	}
	if got := m.View(); strings.Contains(got, "●") {
		t.Fatalf("View() = %q, la tool asentada no debe seguir mostrandose como en ejecucion", got)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "edit", Input: json.RawMessage(`{"path":"a.go"}`)})
	if got := m.View(); !strings.Contains(got, "● edit     a.go") {
		t.Fatalf("View() = %q, el segundo tool call debe mostrarse en ejecucion con el resumen del Input %q", got, "● edit     a.go")
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c2", ToolName: "edit", Error: "permiso denegado"})
	got := m.View()
	for _, want := range []string{"✗ edit     a.go", "│ error: permiso denegado"} {
		if !strings.Contains(got, want) {
			t.Fatalf("View() = %q, Tool.Failed debe mostrar %q: el header con el resumen del Input y el Error como linea de rail", got, want)
		}
	}
	if !strings.Contains(got, "✓ bash     ls") {
		t.Fatalf("View() = %q, el fallo de c2 no debe tocar el estado ok de c1", got)
	}
	if strings.Contains(got, "●") {
		t.Fatalf("View() = %q, no debe quedar ninguna tool en ejecucion", got)
	}
}

// Contrato del render de la tool "skill": usa la gramatica de actividad con
// el nombre de la skill como resumen (`● skill    <nombre>`), donde el nombre
// es el campo "name" del Input JSON, sin filtrar el Input crudo al header. En
// exito el header va SIN preview del output: el cuerpo del SKILL.md que viaja
// en ev.Text es para el modelo, no para el transcript.
func TestModel_RendersSkillToolAsSkillLine(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "skill", Input: json.RawMessage(`{"name":"tdd-cycle-evidence"}`)})
	view := m.View()
	if !strings.Contains(view, "● skill    tdd-cycle-evidence") {
		t.Fatalf("View() = %q, la tool skill en ejecucion debe rendirse como linea dedicada %q (nombre = campo name del Input)", view, "● skill    tdd-cycle-evidence")
	}
	if strings.Contains(view, `{"name"`) {
		t.Fatalf("View() = %q, NO debe filtrar el Input crudo al header: la linea dedicada lleva el nombre pelado como resumen", view)
	}

	body := "<skill_content name=\"tdd-cycle-evidence\">\ncuerpo del skill para el modelo\n</skill_content>"
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "skill", Text: body,
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: body, ToolCallID: "c1"},
	})
	view = m.View()
	if !strings.Contains(view, "✓ skill    tdd-cycle-evidence") {
		t.Fatalf("View() = %q, la tool skill exitosa debe asentarse como %q", view, "✓ skill    tdd-cycle-evidence")
	}
	if strings.Contains(view, "skill_content") {
		t.Fatalf("View() = %q, NO debe contener %q: en exito la linea de skill va sin preview del output, el cuerpo del SKILL.md es para el modelo y no para el transcript", view, "skill_content")
	}
}

func TestModel_SkillToolFailureShowsError(t *testing.T) {
	// TRIANGULATE: una implementacion pobre de renderSkill solo cubre los
	// estados running/ok y ante Tool.Failed deja el marcador ● para siempre.
	// El fallo de la skill (p.ej. nombre inexistente) se asienta en la misma
	// linea dedicada con el marcador ✗ y el error como linea de rail, igual
	// que el resto de tools.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "skill", Input: json.RawMessage(`{"name":"inexistente"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c1", ToolName: "skill", Error: `skill "inexistente" no encontrada`})

	view := m.View()
	for _, want := range []string{"✗ skill    inexistente", `│ error: skill "inexistente" no encontrada`} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, la skill fallida debe asentarse como %q: la linea dedicada tambien cubre el estado de error, no solo running/ok", view, want)
		}
	}
	if strings.Contains(view, "●") {
		t.Fatalf("View() = %q, la skill asentada con error no debe seguir mostrandose como en ejecucion", view)
	}
}

func TestModel_SkillToolWithoutNameRendersBareHeader(t *testing.T) {
	// TRIANGULATE: una implementacion pobre asume que el Input de la skill es
	// JSON valido (panic o basura en el header al parsearlo) cuando no puede
	// extraer el nombre. Con Input no parseable el header queda "● skill"
	// pelado: sin resumen, sin espacios colgantes de la alineacion y sin
	// filtrar el input crudo al transcript.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "skill", Input: json.RawMessage(`no-es-json`)})

	view := m.View()
	if !strings.Contains(view, "● skill") {
		t.Fatalf("View() = %q, con Input no parseable la skill debe rendirse con el header pelado %q", view, "● skill")
	}
	skillLine := lineWith(t, view, "● skill")
	if got := strings.TrimRight(skillLine, " "); got != "● skill" {
		t.Fatalf("linea de la skill = %q, el header pelado no lleva resumen: queda %q sin heredar nada del Input", skillLine, "● skill")
	}
	if strings.Contains(view, "no-es-json") {
		t.Fatalf("View() = %q, NO debe filtrar el Input crudo %q al transcript", view, "no-es-json")
	}
}

// Contrato del detalle de tool calls: el header lleva el resumen del Input
// (`✓ <name>     <resumen>`; con un solo campo string el resumen es su valor)
// y Tool.Success trae el output en ev.Text, que se muestra bajo el header con
// cada linea de rail `│ ` hasta 4 lineas; con mas lineas aparece una marca
// final `│ … +N lines`. Con 3 lineas de output caben todas: no debe aparecer
// ninguna marca de truncado.
func TestModel_ToolSuccessShowsOutputPreview(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls -la"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "uno\ndos\ntres",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "uno\ndos\ntres", ToolCallID: "c1"},
	})

	view := m.View()
	if !strings.Contains(view, "✓ bash     ls -la") {
		t.Fatalf("View() = %q, el header debe llevar el resumen del Input %q: con un solo campo string el resumen es su valor", view, "✓ bash     ls -la")
	}
	for _, want := range []string{"│ uno", "│ dos", "│ tres"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, debe contener %q: cada linea del output de Tool.Success se muestra bajo el header prefijada con la barra", view, want)
		}
	}
	if strings.Contains(view, "lines") {
		t.Fatalf("View() = %q, NO debe contener la marca de truncado %q: 3 lineas de output caben en el tope de 4 y se muestran completas", view, "lines")
	}
}

// Contrato del diff en Tool.Success: cuando el evento trae Diff (edit/write),
// el detalle bajo el header muestra EL DIFF en lugar del preview del output:
// cada linea del diff con el rail `│ ` en la columna 0, las lineas `+` en
// verde, las `-` en rojo y el resto tenue (cada linea un segmento contiguo
// estilizado).
func TestModel_ToolSuccessShowsEditDiff(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "edit", Input: json.RawMessage(`{"path":"a.go"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "edit", Text: "ok",
		Diff:    "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-viejo\n+nuevo",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	view := m.View()
	for _, want := range []string{"│ -viejo", "│ +nuevo"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, debe contener %q: con Diff en Tool.Success cada linea del diff se muestra bajo el header como linea de rail", view, want)
		}
	}
	if strings.Contains(view, "│ ok") {
		t.Fatalf("View() = %q, NO debe contener %q: el diff desplaza al preview del output", view, "│ ok")
	}
}

// TRIANGULATE: tumba un preview del output sin tope, que volcaria las 6 lineas
// enteras al transcript en vez de cortar en 4 y resumir el resto en la marca.
func TestModel_ToolOutputPreviewTruncatesLongOutput(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"cat f"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "l1\nl2\nl3\nl4\nl5\nl6",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "l1\nl2\nl3\nl4\nl5\nl6", ToolCallID: "c1"},
	})

	view := m.View()
	for _, want := range []string{"│ l1", "│ l2", "│ l3", "│ l4"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, debe contener %q: las primeras 4 lineas del output se muestran bajo el header", view, want)
		}
	}
	if !strings.Contains(view, "+2 lines") {
		t.Fatalf("View() = %q, debe contener la marca %q: las 2 lineas que exceden el tope se resumen", view, "+2 lines")
	}
	for _, banned := range []string{"│ l5", "│ l6"} {
		if strings.Contains(view, banned) {
			t.Fatalf("View() = %q, NO debe contener %q: el preview corta en el tope de 4 lineas", view, banned)
		}
	}
}

// TRIANGULATE: tumba un resumen del Input que vuelque el input entero sin
// truncar, o que con varios campos elija un campo suelto en vez del JSON
// compacto completo.
func TestModel_ToolInputSummaryCompactsMultiField(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 200, Height: 24})

	// Dos campos: el resumen es el JSON compacto, no el valor de un campo suelto.
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "edit", Input: json.RawMessage(`{"path":"a.go","texto":"x"}`)})
	view := m.View()
	if want := `● edit     {"path":"a.go","texto":"x"}`; !strings.Contains(view, want) {
		t.Fatalf("View() = %q, el header debe contener %q: con varios campos el resumen es el JSON compacto", view, want)
	}

	// Un solo campo string mas largo que el tope de 48 celdas: el resumen se
	// trunca con la elipsis y la cola del input no aparece.
	long := strings.Repeat("x", 60) + "-cola-final"
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "bash", Input: json.RawMessage(`{"command":"` + long + `"}`)})
	view = m.View()
	if !strings.Contains(view, "…") {
		t.Fatalf("View() = %q, debe contener la elipsis %q: un input mas largo que el tope se trunca en el header", view, "…")
	}
	if strings.Contains(view, "cola-final") {
		t.Fatalf("View() = %q, NO debe contener %q: la cola de un input largo queda fuera del resumen truncado", view, "cola-final")
	}
}

func TestModel_ShowsPendingPermissionAndClearsOnOutcome(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// Orden real del runner: Tool.Called y despues Tool.Permission.Requested
	// mientras bloquea en el gate (ask-before-run).
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"rm -rf /tmp/x"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"rm -rf /tmp/x"}`)})

	view := m.View()
	permLine := lineWith(t, view, "(aprobar/denegar)")
	for _, want := range []string{"? bash", "rm -rf /tmp/x", "aprobar", "denegar"} {
		if !strings.Contains(permLine, want) {
			t.Fatalf("solicitud pendiente = %q, debe contener %q (marcador ?, ToolName, resumen del Input y aprobar/denegar)", permLine, want)
		}
	}
	if callID, ok := m.PendingPermission(); !ok || callID != "c1" {
		t.Fatalf("PendingPermission() = (%q, %v), debe exponer la solicitud pendiente c1", callID, ok)
	}

	// El desenlace llega como Tool.Success del MISMO CallID: la solicitud desaparece.
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "hecho",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "hecho", ToolCallID: "c1"},
	})
	if got := m.View(); strings.Contains(got, "(aprobar/denegar)") {
		t.Fatalf("View() = %q, Tool.Success de c1 debe retirar la solicitud pendiente", got)
	}
	if callID, ok := m.PendingPermission(); ok {
		t.Fatalf("PendingPermission() = (%q, %v), no debe quedar solicitud tras el desenlace", callID, ok)
	}

	// Tool.Failed tambien resuelve la solicitud (p.ej. denegada por el usuario).
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"b.go"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"b.go"}`)})
	if callID, ok := m.PendingPermission(); !ok || callID != "c2" {
		t.Fatalf("PendingPermission() = (%q, %v), debe exponer la solicitud pendiente c2", callID, ok)
	}
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c2", ToolName: "write", Error: "denegada por el usuario"})
	if got := m.View(); strings.Contains(got, "(aprobar/denegar)") {
		t.Fatalf("View() = %q, Tool.Failed de c2 debe retirar la solicitud pendiente", got)
	}
	if callID, ok := m.PendingPermission(); ok {
		t.Fatalf("PendingPermission() = (%q, %v), no debe quedar solicitud tras Tool.Failed", callID, ok)
	}
}

func TestModel_ShowsStepFailedError(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindStepFailed, Error: "contexto agotado: limite de tokens"})

	view := m.View()
	errLine := lineWith(t, view, "contexto agotado: limite de tokens")
	if !strings.Contains(errLine, "✗ error") {
		t.Fatalf("linea del fallo = %q, debe llevar el marcador %q para distinguirse del texto normal", errLine, "✗ error")
	}
}

// Contrato de la jerarquia visual de actividad: el header de cada tool lleva
// un marcador de estado en la columna 0 (`●` corriendo, `✓` exito, `✗` fallo),
// el nombre de la tool alineado a 8 columnas (`%-8s`) y el resumen del Input
// (`● bash     ls`); el detalle va debajo como lineas de rail `│ ` en columna
// 0 (`│ 18 matches`, `│ error: exit 1`). El formato viejo `[tool] ...`
// desaparece del transcript.
func TestModel_RendersActivityMarkersThroughToolLifecycle(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	plain := ansi.Strip(m.View())
	if want := "● bash     ls"; !strings.Contains(plain, want) {
		t.Fatalf("View() sin ANSI = %q, la tool en ejecucion debe rendirse como %q: marcador ● en columna 0, nombre alineado a 8 columnas y resumen del Input", plain, want)
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "18 matches",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "18 matches", ToolCallID: "c1"},
	})
	plain = ansi.Strip(m.View())
	if want := "✓ bash     ls"; !strings.Contains(plain, want) {
		t.Fatalf("View() sin ANSI = %q, la tool exitosa debe asentarse como %q: el marcador ✓ reemplaza al ● en la misma columna", plain, want)
	}
	railLine := lineWith(t, plain, "18 matches")
	if want := "│ 18 matches"; !strings.HasPrefix(railLine, want) {
		t.Fatalf("linea del output = %q, debe llevar el rail en columna 0 como %q: el detalle bajo el header usa `│ ` sin la sangria vieja", railLine, want)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "bash", Input: json.RawMessage(`{"command":"false"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c2", ToolName: "bash", Error: "exit 1"})
	plain = ansi.Strip(m.View())
	if want := "✗ bash     false"; !strings.Contains(plain, want) {
		t.Fatalf("View() sin ANSI = %q, la tool fallida debe asentarse como %q: marcador ✗ con la misma columna de nombre", plain, want)
	}
	failLine := lineWith(t, plain, "error: exit 1")
	if want := "│ error: exit 1"; !strings.HasPrefix(failLine, want) {
		t.Fatalf("linea del fallo = %q, el error de la tool va debajo del header como linea de rail %q, no pegado al header", failLine, want)
	}
	if strings.Contains(plain, "[tool]") {
		t.Fatalf("View() sin ANSI = %q, NO debe contener el formato viejo %q: los marcadores de estado lo reemplazan", plain, "[tool]")
	}
}

// Contrato del agrupado compacto: las entradas de actividad adyacentes (tools,
// permisos, errores de step) se unen SIN linea en blanco entre si (separador
// "\n"), mientras la narrativa del assistant conserva su parrafo propio
// ("\n\n") y rompe el grupo: dos tools consecutivas quedan en lineas
// fisicamente contiguas y la narrativa va rodeada de lineas en blanco.
func TestModel_GroupsAdjacentActivityEntriesWithoutBlankLine(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, ToolCallID: "c1"},
	})
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "grep", Input: json.RawMessage(`{"pattern":"foo"}`)})

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "listo el analisis"})
	m = drainReveal(t, m)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c3", ToolName: "bash", Input: json.RawMessage(`{"command":"pwd"}`)})

	plain := ansi.Strip(m.View())
	if want := "✓ bash     ls\n● grep     foo"; !strings.Contains(plain, want) {
		t.Fatalf("View() sin ANSI = %q, debe contener %q: dos entradas de actividad adyacentes se agrupan en lineas fisicamente contiguas, sin linea en blanco entre si", plain, want)
	}

	lines := strings.Split(plain, "\n")
	narrIdx := lineIndexWith(t, plain, "listo el analisis")
	if narrIdx == 0 || strings.TrimSpace(lines[narrIdx-1]) != "" {
		t.Fatalf("linea previa a la narrativa = %q, la narrativa del assistant rompe el grupo de actividad: se separa con linea en blanco", lines[narrIdx-1])
	}
	toolIdx := lineIndexWith(t, plain, "pwd")
	if toolIdx == 0 || strings.TrimSpace(lines[toolIdx-1]) != "" {
		t.Fatalf("linea previa a la tool posterior a la narrativa = %q, la actividad tras la narrativa abre grupo nuevo separado por linea en blanco", lines[toolIdx-1])
	}
}

// Contrato del stat de diff: el exito de edit/write lleva en el header el
// conteo `+N -M` de lineas agregadas/quitadas del unified diff, contando las
// que empiezan con +/- pero excluyendo las cabeceras `+++`/`---`, separado del
// resumen por dos espacios (`✓ edit     main.go  +2 -1`); las lineas del diff
// van debajo con el rail `│ ` en columna 0 (`│ +nueva`).
func TestModel_EditSuccessShowsDiffStatInHeader(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "edit", Input: json.RawMessage(`{"path":"main.go"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "edit", Text: "ok",
		Diff:    "--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,3 @@\n-vieja\n+nueva\n+extra",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	if want := "✓ edit     main.go  +2 -1"; !strings.Contains(plain, want) {
		t.Fatalf("View() sin ANSI = %q, el header de la edit exitosa debe contener %q: el stat cuenta las lineas +/- de contenido del diff y excluye las cabeceras +++/---", plain, want)
	}
	for _, needle := range []string{"+nueva", "+extra", "-vieja"} {
		line := lineWith(t, plain, needle)
		if want := "│ " + needle; !strings.HasPrefix(line, want) {
			t.Fatalf("linea del diff = %q, debe llevar el rail en columna 0 como %q: el diff bajo el header usa `│ ` en vez de la sangria vieja de dos espacios", line, want)
		}
	}
}

// TRIANGULATE: tumba un header que trunque el nombre de la tool al ancho de la
// columna de alineacion (8) o que padee de mas: con un nombre mas largo que la
// columna, el nombre queda entero y el resumen a UN espacio del nombre.
func TestModel_ActivityHeaderKeepsLongToolNameReadable(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "present_plan", Input: json.RawMessage(`{"plan":"migrar el runner"}`)})

	plain := ansi.Strip(m.View())
	line := lineWith(t, plain, "present_plan")
	if want := "● present_plan migrar el runner"; line != want {
		t.Fatalf("header = %q, want exactamente %q: un nombre mas largo que la columna de 8 no se trunca y el resumen queda a UN espacio del nombre", line, want)
	}
}

// TRIANGULATE: tumba un header que deje la cola invisible de la alineacion
// cuando no hay resumen (sin Input o con Input `{}`): la linea es exactamente
// el marcador y el nombre, sin espacios colgantes.
func TestModel_ActivityHeaderWithoutSummaryHasNoTrailingSpaces(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// Sin Input y con Input `{}`: en ambos el resumen queda vacio y el header
	// debe recortar los espacios de la alineacion del nombre.
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash"})
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "grep", Input: json.RawMessage(`{}`)})

	plain := ansi.Strip(m.View())
	if line := lineWith(t, plain, "● bash"); line != "● bash" {
		t.Fatalf("header sin Input = %q, want exactamente %q: sin resumen no quedan espacios colgantes de la alineacion", line, "● bash")
	}
	if line := lineWith(t, plain, "● grep"); line != "● grep" {
		t.Fatalf("header con Input {} = %q, want exactamente %q: el objeto vacio no produce resumen ni espacios colgantes", line, "● grep")
	}
}

// TRIANGULATE: tumba un diffStat hardcodeado al diff del test de arriba o que
// cuente las cabeceras de archivo +++/--- como contenido; la tabla cubre
// tambien el diff vacio y las lineas +/- peladas (sin texto detras).
func TestEntry_DiffStatIgnoresFileHeadersAndEmptyDiff(t *testing.T) {
	tests := []struct {
		name    string
		diff    string
		added   int
		removed int
	}{
		{
			name:    "cabeceras, hunk y contenido",
			diff:    "--- a/x\n+++ b/x\n@@ -1,2 +1,3 @@\n contexto\n-vieja\n+nueva\n+extra",
			added:   2,
			removed: 1,
		},
		{
			name:    "solo cabeceras y contexto",
			diff:    "--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n contexto",
			added:   0,
			removed: 0,
		},
		{name: "diff vacio", diff: "", added: 0, removed: 0},
		{name: "lineas + y - peladas cuentan", diff: "+\n-", added: 1, removed: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, removed := diffStat(tt.diff)
			if added != tt.added || removed != tt.removed {
				t.Fatalf("diffStat(%q) = (+%d, -%d), want (+%d, -%d)", tt.diff, added, removed, tt.added, tt.removed)
			}
		})
	}
}

// TRIANGULATE: tumba un header que pegue el stat siempre (`✓ bash     ls  +0 -0`)
// en vez de solo cuando hay diff: una tool exitosa SIN diff lleva el header
// pelado y su output como lineas de rail.
func TestModel_SuccessWithoutDiffShowsNoStat(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "main.go\nview.go",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "main.go\nview.go", ToolCallID: "c1"},
	})

	plain := ansi.Strip(m.View())
	header := lineWith(t, plain, "✓ bash")
	if want := "✓ bash     ls"; header != want {
		t.Fatalf("header = %q, want exactamente %q: el exito sin diff no agrega nada tras el resumen", header, want)
	}
	for _, banned := range []string{"+0 -0", " +"} {
		if strings.Contains(header, banned) {
			t.Fatalf("header = %q, NO debe contener %q: el stat +N -M solo aplica cuando hay diff", header, banned)
		}
	}
	for _, needle := range []string{"main.go", "view.go"} {
		if line := lineWith(t, plain, needle); !strings.HasPrefix(line, "│ ") {
			t.Fatalf("linea del output = %q, el output de la tool exitosa va con el rail %q en columna 0", line, "│ ")
		}
	}
}

// TRIANGULATE: tumba un agrupado compacto que solo contemple tools: el permiso
// pendiente (entryPermission) y el fallo duro del step (entryError) tambien son
// actividad y se unen al grupo sin lineas en blanco; la narrativa del assistant
// que sigue conserva su parrafo propio.
func TestModel_PermissionAndErrorJoinActivityGroup(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, ToolCallID: "c1"},
	})
	// Orden real del runner: Tool.Called y despues Tool.Permission.Requested
	// mientras bloquea en el gate (ask-before-run).
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"b.go"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"b.go"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindStepFailed, Error: "boom"})

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "sigo con el resto"})
	m = drainReveal(t, m)

	plain := ansi.Strip(m.View())
	want := "✓ bash     ls\n● write    b.go\n? write    b.go (aprobar/denegar)\n✗ error    boom"
	if !strings.Contains(plain, want) {
		t.Fatalf("View() sin ANSI = %q, debe contener %q: la tool exitosa, el permiso pendiente y el error de step quedan fisicamente contiguos, sin lineas en blanco entre si", plain, want)
	}

	lines := strings.Split(plain, "\n")
	narrIdx := lineIndexWith(t, plain, "sigo con el resto")
	if narrIdx == 0 || strings.TrimSpace(lines[narrIdx-1]) != "" {
		t.Fatalf("linea previa a la narrativa = %q, la narrativa del assistant tras el grupo de actividad se separa con linea en blanco", lines[narrIdx-1])
	}
}

func TestModel_ToolInputDeltasAreNotTranscript(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// Reasoning: el bloque de pensamiento se muestra mientras fluye, pero al
	// cerrarse colapsa al resumen "[penso ...]": drenado el reveal, su texto
	// NO queda como texto plano del transcript.
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pienso en secreto"})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pienso en secreto"})

	// Tool input: los fragmentos crudos viajan en Text y el JSON completo en
	// Input; ninguno es texto de conversacion, NUNCA.
	m = apply(t, m, EventMsg{Kind: session.KindToolInputStarted, CallID: "c1"})
	m = apply(t, m, EventMsg{Kind: session.KindToolInputDelta, CallID: "c1", Text: `{"cmd":"ls`})
	m = apply(t, m, EventMsg{Kind: session.KindToolInputEnded, CallID: "c1", Input: json.RawMessage(`{"cmd":"ls"}`)})

	// El texto normal del assistant si es transcript: contrasta con lo anterior.
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "respuesta visible"})
	m = drainReveal(t, m)

	view := m.View()
	for _, leak := range []string{"pienso en secreto", `{"cmd":"ls`} {
		if strings.Contains(view, leak) {
			t.Fatalf("View() = %q, no debe filtrar %q como texto de la conversacion", view, leak)
		}
	}
	if !strings.Contains(view, "respuesta visible") {
		t.Fatalf("View() = %q, el texto del assistant si debe verse", view)
	}
}

// Paridad con el ThinkingBlock del escritorio: el reasoning se muestra como un
// bloque colapsable del transcript. Mientras fluye, la vista lleva la cabecera
// "[pensando]" y debajo SOLO las ultimas 4 lineas no vacias del texto revelado
// (ventana deslizante); Reasoning.Ended, con el backlog ya drenado, colapsa el
// bloque a una unica linea de resumen con el prefijo "[penso " (duracion
// legible), y la cabecera y el preview desaparecen.
func TestModel_ShowsReasoningAsCollapsibleThinkingBlock(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	text := "razon-1\nrazon-2\nrazon-3\nrazon-4\nrazon-5\nrazon-6"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "[pensando]") {
		t.Fatalf("View() = %q, el reasoning en curso debe mostrar la cabecera %q", view, "[pensando]")
	}
	for _, want := range []string{"razon-3", "razon-4", "razon-5", "razon-6"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, el preview debe mostrar %q (las ultimas 4 lineas no vacias del texto revelado)", view, want)
		}
	}
	for _, gone := range []string{"razon-1", "razon-2"} {
		if strings.Contains(view, gone) {
			t.Fatalf("View() = %q, %q ya salio de la ventana deslizante: solo se muestran las ultimas 4 lineas no vacias", view, gone)
		}
	}

	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)

	view = m.View()
	if !strings.Contains(view, "[penso ") {
		t.Fatalf("View() = %q, el reasoning terminado debe colapsar a una linea de resumen con el prefijo %q", view, "[penso ")
	}
	if strings.Contains(view, "[pensando]") {
		t.Fatalf("View() = %q, la cabecera %q debe desaparecer al colapsar el bloque", view, "[pensando]")
	}
	if strings.Contains(view, "razon-6") {
		t.Fatalf("View() = %q, las lineas del preview deben desaparecer al colapsar el bloque", view)
	}
}

func TestModel_ThinkingRevealsProgressivelyLikeAssistant(t *testing.T) {
	// TRIANGULATE: un fold que appendea el delta del pensamiento y lo revela
	// de golpe (revealed = total) pasa el test principal, que drena antes de
	// asertar. El pensamiento participa del mismo reveal suave que el texto
	// del assistant: sin ticks no se ve nada, cada tick avanza un prefijo y
	// solo al drenar se ve el final.
	m := NewModel(nil, "s1", nil)

	// Dos lineas largas (300+ runas) para que el token final quede dentro de
	// la ventana de 4 lineas del preview una vez drenado.
	text := "inicio-marca " + strings.Repeat("a", 150) + "\n" + strings.Repeat("b", 150) + " token-final"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})

	view := m.View()
	if strings.Contains(view, "token-final") {
		t.Fatalf("View() = %q, %q NO debe verse sin ticks de reveal: el delta del pensamiento se revela progresivamente, no de golpe", view, "token-final")
	}
	if strings.Contains(view, "inicio-marca") {
		t.Fatalf("View() = %q, %q NO debe verse sin ticks de reveal: tambien el prefijo espera su tick", view, "inicio-marca")
	}

	m = apply(t, m, revealTickMsg{})
	view = m.View()
	if !strings.Contains(view, "inicio-marca") {
		t.Fatalf("View() = %q, tras UN tick debe verse el prefijo %q del pensamiento", view, "inicio-marca")
	}
	if strings.Contains(view, "token-final") {
		t.Fatalf("View() = %q, tras UN tick el final %q aun NO debe verse: un tick revela un paso, no todo el backlog", view, "token-final")
	}

	m = drainReveal(t, m)
	if view := m.View(); !strings.Contains(view, "token-final") {
		t.Fatalf("View() = %q, con el backlog drenado el final %q debe verse en la ventana del preview", view, "token-final")
	}
}

func TestModel_ThinkingPreviewSkipsBlankLines(t *testing.T) {
	// TRIANGULATE: una ventana que corta las ultimas 4 lineas CRUDAS (sin
	// filtrar vacias) muestra blancos y pierde contenido: de
	// "r1\n\nr2\n\nr3\n\nr4\n\nr5" mostraria ["", "r4", "", "r5"]. La ventana
	// filtra primero las vacias y recien ahi corta: r2..r5 pegadas a la
	// cabecera, sin blancos intercalados.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "r1\n\nr2\n\nr3\n\nr4\n\nr5"})
	m = drainReveal(t, m)

	lines := strings.Split(ansi.Strip(m.View()), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	view := strings.Join(lines, "\n")
	if !strings.Contains(view, "  [pensando]\n  r2\n  r3\n  r4\n  r5") {
		t.Fatalf("View() = %q, el preview debe contener las ultimas 4 lineas NO vacias con el inset uniforme (%q): ni blancos intercalados ni lineas de contenido perdidas", view, "  [pensando]\n  r2\n  r3\n  r4\n  r5")
	}
	if strings.Contains(view, "r1") {
		t.Fatalf("View() = %q, %q ya salio de la ventana de 4 lineas no vacias", view, "r1")
	}
}

func TestModel_ThinkingKeepsChatInsetWhileStreamingAndExpanded(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 44, Height: 18})

	text := "streaming-inset-a\nstreaming-inset-b"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)

	assertThinkingInset(t, m.View(), "[pensando]", "streaming-inset-a", "streaming-inset-b")

	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})

	assertThinkingInset(t, m.View(), "[penso ", "streaming-inset-a", "streaming-inset-b")
}

func TestEntry_RenderThinkingInsetsEveryWrappedLine(t *testing.T) {
	e := entry{
		kind:     entryReasoning,
		text:     strings.Repeat("pensamiento-largo ", 8),
		revealed: len(strings.Repeat("pensamiento-largo ", 8)),
		expanded: true,
	}

	lines := strings.Split(ansi.Strip(e.renderThinking(24)), "\n")
	if len(lines) < 3 {
		t.Fatalf("renderThinking() produjo %d lineas, want cabecera y multiples lineas envueltas: %q", len(lines), lines)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, thinkingInset) {
			t.Fatalf("linea envuelta = %q, want inset %q", line, thinkingInset)
		}
		if got, want := ansi.StringWidth(line), 24; got > want {
			t.Fatalf("ancho de linea envuelta = %d, want <= %d: %q", got, want, line)
		}
	}
}

func assertThinkingInset(t *testing.T, view string, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		line := strings.TrimRight(lineWith(t, ansi.Strip(view), needle), " ")
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("linea de pensamiento %q = %q, want inset de dos celdas", needle, line)
		}
	}
}

func TestModel_TextStartedClosesLiveThinking(t *testing.T) {
	// TRIANGULATE: si el runner nunca emite Reasoning.Ended, un fold ingenuo
	// deja la cabecera "[pensando]" viva para siempre mientras la respuesta
	// streamea debajo. Que arranque el texto implica que el pensamiento
	// termino: Text.Started lo cierra defensivamente.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "sopeso opciones"})
	m = drainReveal(t, m)

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "respuesta"})
	m = drainReveal(t, m)

	view := m.View()
	if strings.Contains(view, "[pensando]") {
		t.Fatalf("View() = %q, Text.Started debe cerrar el pensamiento en vivo: la cabecera %q no puede sobrevivir al arranque de la respuesta", view, "[pensando]")
	}
	if !strings.Contains(view, "[penso ") {
		t.Fatalf("View() = %q, el pensamiento cerrado defensivamente debe colapsar al resumen %q", view, "[penso ")
	}
	if !strings.Contains(view, "respuesta") {
		t.Fatalf("View() = %q, la respuesta %q debe verse tras el pensamiento colapsado", view, "respuesta")
	}
}

func TestModel_StepEndedClosesLiveThinking(t *testing.T) {
	// TRIANGULATE: un step puede morir pensando (cancelacion, error del
	// proveedor) sin Reasoning.Ended ni Text.Started de por medio. Step.Ended
	// cierra el pensamiento defensivamente igual que Text.Started; sin ese
	// cierre la cabecera "[pensando]" quedaria viva para siempre.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pienso y el step muere"})
	m = drainReveal(t, m)

	m = apply(t, m, EventMsg{Kind: session.KindStepEnded})

	view := m.View()
	if strings.Contains(view, "[pensando]") {
		t.Fatalf("View() = %q, Step.Ended debe cerrar el pensamiento en vivo: la cabecera %q no puede sobrevivir al fin del step", view, "[pensando]")
	}
	if !strings.Contains(view, "[penso ") {
		t.Fatalf("View() = %q, el pensamiento cerrado por el fin del step debe colapsar al resumen %q", view, "[penso ")
	}
}

func TestModel_ReasoningEndedTextCollapsesWithoutAnimation(t *testing.T) {
	// TRIANGULATE: cuando Reasoning.Ended trae el texto completo sin deltas
	// previos (proveedor que no streamea el pensamiento), el relleno NO se
	// anima: se revela completo y colapsado en el mismo fold, sin ticks de por
	// medio. Un fold que solo asigna el texto sin marcarlo revelado dejaria el
	// bloque "escribiendose" despues de cerrado.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "relleno-final-sin-stream"})

	view := m.View()
	if !strings.Contains(view, "[penso ") {
		t.Fatalf("View() = %q, sin deltas previos el Ended con texto debe colapsar de inmediato al resumen %q, sin ticks de por medio", view, "[penso ")
	}
	if strings.Contains(view, "[pensando]") {
		t.Fatalf("View() = %q, la cabecera %q no debe verse tras el Ended: el texto de relleno no se anima", view, "[pensando]")
	}
	if strings.Contains(view, "relleno-final-sin-stream") {
		t.Fatalf("View() = %q, el texto de relleno del Ended jamas debe verse plano, ni siquiera antes de drenar", view)
	}

	m = drainReveal(t, m)
	if view := m.View(); strings.Contains(view, "relleno-final-sin-stream") {
		t.Fatalf("View() = %q, el texto de relleno tampoco debe aparecer tras drenar: no quedo backlog que animar", view)
	}
}

func TestModel_TwoThinkingBlocksInSameRunStaySeparate(t *testing.T) {
	// TRIANGULATE: un fold que reusa el bloque de pensamiento anterior en vez
	// de abrir uno nuevo mezclaria las lineas del primero en el preview del
	// segundo y colapsaria ambos en UNA sola linea de resumen. Cada
	// Reasoning.Started abre un bloque propio.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "primero-a\nprimero-b"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "primero-a\nprimero-b"})
	m = drainReveal(t, m)

	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "segundo-a\nsegundo-b"})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "segundo-a") || !strings.Contains(view, "segundo-b") {
		t.Fatalf("View() = %q, el preview del segundo pensamiento debe mostrar sus lineas", view)
	}
	if strings.Contains(view, "primero-a") || strings.Contains(view, "primero-b") {
		t.Fatalf("View() = %q, el preview del segundo pensamiento NO debe mezclar lineas del primero (ya colapsado)", view)
	}

	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "segundo-a\nsegundo-b"})
	m = drainReveal(t, m)

	view = m.View()
	if count := strings.Count(view, "[penso "); count < 2 {
		t.Fatalf("View() = %q, dos pensamientos en la misma corrida deben colapsar a DOS resumenes %q (count=%d)", view, "[penso ", count)
	}
}

func TestModel_ThinkingCollapseWaitsForRevealDrain(t *testing.T) {
	// TRIANGULATE: un colapso instantaneo al recibir Ended con backlog
	// pendiente cortaria la animacion a mitad de frase. Paridad con el done
	// del escritorio: el bloque se sigue "escribiendo" hasta drenar el reveal
	// y recien ahi colapsa al resumen.
	m := NewModel(nil, "s1", nil)

	text := "inicio-fluye " + strings.Repeat("c", 150) + "\n" + strings.Repeat("d", 150) + " final-tardio"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	// Un tick para que haya un prefijo visible que asertar antes del Ended.
	m = apply(t, m, revealTickMsg{})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})

	view := m.View()
	if !strings.Contains(view, "[pensando]") {
		t.Fatalf("View() = %q, con backlog pendiente el Ended NO colapsa todavia: la cabecera %q debe seguir mientras se drena", view, "[pensando]")
	}
	if !strings.Contains(view, "inicio-fluye") {
		t.Fatalf("View() = %q, el prefijo ya revelado %q debe seguir visible mientras el pensamiento termina de escribirse", view, "inicio-fluye")
	}
	if strings.Contains(view, "[penso ") {
		t.Fatalf("View() = %q, el resumen %q no debe aparecer hasta drenar el backlog del reveal", view, "[penso ")
	}

	m = drainReveal(t, m)
	view = m.View()
	if !strings.Contains(view, "[penso ") {
		t.Fatalf("View() = %q, con el backlog drenado el pensamiento cerrado debe colapsar al resumen %q", view, "[penso ")
	}
	if strings.Contains(view, "[pensando]") {
		t.Fatalf("View() = %q, la cabecera %q debe desaparecer al colapsar", view, "[pensando]")
	}
}

func TestModel_SecondTurnOpensNewBlock(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// Primer turno completo: streaming, cierre del bloque y cierre del step.
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "Primera respuesta"})
	m = apply(t, m, EventMsg{Kind: session.KindTextEnded, Text: "Primera respuesta"})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "Primera respuesta"},
	})

	// Segundo turno: el nuevo streaming abre un bloque NUEVO.
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "Segunda respuesta"})
	m = drainReveal(t, m)

	view := m.View()
	if strings.Contains(view, "Primera respuestaSegunda respuesta") {
		t.Fatalf("View() = %q, el segundo turno NO debe concatenar al bloque anterior", view)
	}
	first := strings.Index(view, "Primera respuesta")
	second := strings.Index(view, "Segunda respuesta")
	if first < 0 || second < 0 || first >= second {
		t.Fatalf("View() sin ANSI = %q, ambos textos deben verse como bloques separados y ordenados", view)
	}
	if count := strings.Count(view, "Primera respuesta"); count != 1 {
		t.Fatalf("View() = %q, %q debe aparecer exactamente una vez (count=%d)", view, "Primera respuesta", count)
	}
}

func TestModel_EnterWithEmptyInputDoesNotSend(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, Enter con input vacio no debe enviar nada", len(fake.sent))
	}
	if m.Working() {
		t.Fatalf("Working() = true, Enter con input vacio no debe marcar el modelo como trabajando")
	}
}

func TestModel_PermissionKeysResolveViaAgent(t *testing.T) {
	// Escenario 1: 'y' aprueba la solicitud pendiente c1.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(fake.resolved) != 1 {
		t.Fatalf("ResolvePermission fue llamado %d veces, 'y' debe resolver exactamente una vez", len(fake.resolved))
	}
	if got := fake.resolved[0]; got.sessionID != "s1" || got.callID != "c1" || !got.approved {
		t.Fatalf("ResolvePermission(%q, %q, %v), se esperaba ResolvePermission(%q, %q, true)", got.sessionID, got.callID, got.approved, "s1", "c1")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, la runa 'y' NO debe entrar al input mientras hay permiso pendiente", got)
	}
	// Resolver NO limpia la solicitud localmente: la limpieza llega por el
	// desenlace de la tool (contrato del ciclo 1).
	if callID, ok := m.PendingPermission(); !ok || callID != "c1" {
		t.Fatalf("PendingPermission() = (%q, %v), resolver no debe limpiar la solicitud localmente", callID, ok)
	}
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "ok",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "ok", ToolCallID: "c1"},
	})
	if callID, ok := m.PendingPermission(); ok {
		t.Fatalf("PendingPermission() = (%q, %v), Tool.Success debe retirar la solicitud", callID, ok)
	}

	// Escenario 2: 'n' deniega la solicitud pendiente c2; ademas las runas no
	// entran al input y Enter no envia prompt mientras hay permiso pendiente.
	fake2 := &fakeAgent{}
	m2 := NewModel(fake2, "s1", nil)
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"a.go"}`)})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c2", ToolName: "write", Input: json.RawMessage(`{"path":"a.go"}`)})

	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	if got := m2.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, las runas NO deben entrar al input mientras hay permiso pendiente", got)
	}
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake2.sent) != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, Enter NO debe enviar prompt mientras hay permiso pendiente", len(fake2.sent))
	}
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if len(fake2.resolved) != 1 {
		t.Fatalf("ResolvePermission fue llamado %d veces, 'n' debe resolver exactamente una vez", len(fake2.resolved))
	}
	if got := fake2.resolved[0]; got.sessionID != "s1" || got.callID != "c2" || got.approved {
		t.Fatalf("ResolvePermission(%q, %q, %v), se esperaba ResolvePermission(%q, %q, false)", got.sessionID, got.callID, got.approved, "s1", "c2")
	}
}

func TestModel_PermissionResolvesWithEventSessionID(t *testing.T) {
	// El evento de permiso puede venir de una sesion HIJA (subagente): el bus
	// del padre surfacea el evento del hijo conservando SessionID = childID.
	// La tecla 'y' debe resolver con ESE SessionID, no con el de la TUI.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, EventMsg{SessionID: "child-1", Kind: session.KindToolCalled, CallID: "c9", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	m = apply(t, m, EventMsg{SessionID: "child-1", Kind: session.KindToolPermissionRequested, CallID: "c9", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	if len(fake.resolved) != 1 {
		t.Fatalf("ResolvePermission fue llamado %d veces, 'y' debe resolver exactamente una vez", len(fake.resolved))
	}
	if got := fake.resolved[0]; got.sessionID != "child-1" || got.callID != "c9" || !got.approved {
		t.Fatalf("ResolvePermission(%q, %q, %v), se esperaba ResolvePermission(%q, %q, true): el permiso del subagente se resuelve con el SessionID del evento", got.sessionID, got.callID, got.approved, "child-1", "c9")
	}
}

func TestModel_CtrlCStopsAndQuits(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if len(fake.stopped) != 1 || fake.stopped[0] != "s1" {
		t.Fatalf("Stop = %v, Ctrl+C debe llamar Stop(%q) exactamente una vez", fake.stopped, "s1")
	}
	if cmd == nil {
		t.Fatalf("cmd = nil, Ctrl+C debe devolver un tea.Cmd que produzca tea.QuitMsg")
	}
	if msg := cmd(); msg == nil {
		t.Fatalf("cmd() = nil, se esperaba tea.QuitMsg")
	} else if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, se esperaba tea.QuitMsg", msg)
	}

	// Con permiso pendiente Ctrl+C sigue funcionando igual.
	fake2 := &fakeAgent{}
	m2 := NewModel(fake2, "s1", nil)
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})

	_, cmd2 := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if len(fake2.stopped) != 1 || fake2.stopped[0] != "s1" {
		t.Fatalf("Stop = %v, Ctrl+C con permiso pendiente debe llamar Stop(%q)", fake2.stopped, "s1")
	}
	if cmd2 == nil {
		t.Fatalf("cmd = nil, Ctrl+C con permiso pendiente debe devolver un tea.Cmd que produzca tea.QuitMsg")
	}
	if msg := cmd2(); msg == nil {
		t.Fatalf("cmd() = nil, se esperaba tea.QuitMsg con permiso pendiente")
	} else if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, se esperaba tea.QuitMsg con permiso pendiente", msg)
	}
}

func TestModel_EscStopsWithoutQuitting(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if len(fake.stopped) != 1 || fake.stopped[0] != "s1" {
		t.Fatalf("Stop = %v, Esc debe llamar Stop(%q) exactamente una vez", fake.stopped, "s1")
	}
	if cmd != nil {
		if _, quits := cmd().(tea.QuitMsg); quits {
			t.Fatalf("cmd() produjo tea.QuitMsg, Esc debe detener la corrida SIN salir de la TUI")
		}
	}
}

func TestModel_RunDoneStopsWorkingAndShowsError(t *testing.T) {
	// Corrida limpia: RunDoneMsg{Err: ""} solo apaga Working.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.Working() {
		t.Fatalf("Working() = false, el modelo debe quedar trabajando tras enviar el prompt")
	}

	m = apply(t, m, RunDoneMsg{Err: ""})
	if m.Working() {
		t.Fatalf("Working() = true, RunDoneMsg debe apagar el estado de trabajo")
	}
	if got := m.View(); strings.Contains(got, "✗ error") {
		t.Fatalf("View() = %q, una corrida limpia no debe mostrar error", got)
	}

	// Corrida fallida: RunDoneMsg{Err: "boom"} ademas muestra el error.
	m2 := NewModel(fake, "s1", nil)
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyEnter})

	m2 = apply(t, m2, RunDoneMsg{Err: "boom"})
	if m2.Working() {
		t.Fatalf("Working() = true, RunDoneMsg con error tambien debe apagar el estado de trabajo")
	}
	errLine := lineWith(t, m2.View(), "boom")
	if !strings.Contains(errLine, "✗ error") {
		t.Fatalf("linea del fallo = %q, debe llevar el marcador %q", errLine, "✗ error")
	}
}

func TestModel_EventPumpDeliversFromChannel(t *testing.T) {
	ch := make(chan tea.Msg, 2)
	first := EventMsg{Kind: session.KindTextStarted}
	second := EventMsg{Kind: session.KindTextDelta, Text: "hola"}
	ch <- first
	ch <- second

	m := NewModel(nil, "s1", ch)

	// Init arma la bomba: el cmd hace receive y entrega el primer msg.
	cmd := m.Init()
	if cmd == nil {
		t.Fatalf("Init() = nil, con canal de eventos debe devolver el cmd de la bomba")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("Init command = %#v, want event pump and composer cursor commands", batch)
	}
	msg := batch[0]()
	if got, ok := msg.(EventMsg); !ok || got.Kind != first.Kind {
		t.Fatalf("cmd() = %#v, se esperaba el primer EventMsg %#v", msg, first)
	}

	// Consumir un evento rearma la bomba: el nuevo cmd entrega el segundo msg.
	updated, cmd2 := m.Update(msg)
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd2 == nil {
		t.Fatalf("Update(EventMsg) devolvio cmd nil, la bomba debe rearmarse tras cada evento")
	}
	msg2 := cmd2()
	if got, ok := msg2.(EventMsg); !ok || got.Kind != second.Kind || got.Text != second.Text {
		t.Fatalf("cmd() = %#v, se esperaba el segundo EventMsg %#v", msg2, second)
	}

	// RunDoneMsg tambien rearma la bomba.
	_, cmd3 := m.Update(RunDoneMsg{Err: ""})
	if cmd3 == nil {
		t.Fatalf("Update(RunDoneMsg) devolvio cmd nil, la bomba debe rearmarse tras el fin de corrida")
	}

	// Canal cerrado: el cmd devuelve nil en vez de bloquearse o entregar basura.
	close(ch)
	if got := cmd3(); got != nil {
		t.Fatalf("cmd() = %#v con canal cerrado, se esperaba nil", got)
	}

	// Canal nil (tests del fold): solo queda el comando del cursor.
	if cmd := NewModel(nil, "s1", nil).Init(); cmd == nil {
		t.Fatal("Init() = nil con canal nil, se esperaba el comando del cursor")
	}
}

func TestModel_ViewportFollowsTailOnNewEvents(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// La terminal anuncia su tamano: la conversacion debe vivir en un viewport
	// de alto acotado que sigue la cola (auto-scroll al fondo).
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	// Muchas mas entradas de las que caben en 10 lineas.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	view := m.View()
	if !strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, la ultima entrada %q debe estar visible: la vista sigue la cola", view, "mensaje-29")
	}
	if strings.Contains(view, "mensaje-00") {
		t.Fatalf("View() = %q, la primera entrada %q NO debe estar visible: el alto esta acotado por el viewport", view, "mensaje-00")
	}
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("View() tiene %d lineas, debe respetar el alto de la terminal (<= 10)", lines)
	}
}

func TestModel_PgUpScrollsHistoryBack(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	// Muchas mas entradas de las que caben: la vista arranca siguiendo la cola.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	// PgUp retrocede una pagina: la cola deja de verse y aparece historial previo.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	view := m.View()
	if strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, tras PgUp la cola %q NO debe seguir visible", view, "mensaje-29")
	}
	if !strings.Contains(view, "mensaje-") {
		t.Fatalf("View() = %q, tras PgUp debe verse algun mensaje anterior del historial", view)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, PgUp NO debe escribir en el textinput", got)
	}

	// Varios PgDn consecutivos devuelven la vista a la cola.
	for i := 0; i < 5; i++ {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if got := m.View(); !strings.Contains(got, "mensaje-29") {
		t.Fatalf("View() = %q, tras varios PgDn la cola %q debe volver a verse", got, "mensaje-29")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, PgDn NO debe escribir en el textinput", got)
	}

	// Con permiso pendiente PgUp sigue siendo scroll: no dispara el gate.
	fake := &fakeAgent{}
	m2 := NewModel(fake, "s1", nil)
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 40, Height: 10})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, tea.KeyMsg{Type: tea.KeyPgUp})
	if len(fake.resolved) != 0 {
		t.Fatalf("ResolvePermission fue llamado %d veces, PgUp NO debe disparar el gate de permisos", len(fake.resolved))
	}
	if got := m2.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, PgUp con permiso pendiente NO debe escribir en el textinput", got)
	}
}

// Eventos de rueda del mouse compartidos por los tests de scroll.
var (
	wheelUp   = tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}
	wheelDown = tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}
)

func TestModel_MouseWheelScrollsHistory(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	// Muchas mas entradas de las que caben: la vista arranca siguiendo la cola.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	// Dos ruedas arriba retroceden en el historial: la cola deja de verse.
	m = apply(t, m, wheelUp)
	m = apply(t, m, wheelUp)
	view := m.View()
	if strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, tras rueda arriba la cola %q NO debe seguir visible", view, "mensaje-29")
	}
	if !strings.Contains(view, "mensaje-") {
		t.Fatalf("View() = %q, tras rueda arriba debe verse algun mensaje anterior del historial", view)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, la rueda NO debe escribir en el textinput", got)
	}

	// Varias ruedas abajo devuelven la vista a la cola.
	for i := 0; i < 5; i++ {
		m = apply(t, m, wheelDown)
	}
	if got := m.View(); !strings.Contains(got, "mensaje-29") {
		t.Fatalf("View() = %q, tras varias ruedas abajo la cola %q debe volver a verse", got, "mensaje-29")
	}

	// Con permiso pendiente la rueda sigue siendo scroll: no dispara el gate.
	fake := &fakeAgent{}
	m2 := NewModel(fake, "s1", nil)
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 40, Height: 10})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m2 = apply(t, m2, wheelUp)
	if len(fake.resolved) != 0 {
		t.Fatalf("ResolvePermission fue llamado %d veces, la rueda NO debe disparar el gate de permisos", len(fake.resolved))
	}
	if got := m2.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, la rueda con permiso pendiente NO debe escribir en el textinput", got)
	}
}

func TestModel_MouseWheelSurvivesTinyOrUnsizedTerminal(t *testing.T) {
	// TRIANGULATE: un fix pobre podria asumir un viewport ya dimensionado al
	// reenviar la rueda. Sin WindowSizeMsg previo (ready == false) o con pty
	// 0x0, un evento de rueda no debe paniquear y View() debe seguir
	// devolviendo un string aunque sea degradado.
	t.Run("sin WindowSizeMsg previo", func(t *testing.T) {
		m := NewModel(nil, "s1", nil)

		m = apply(t, m, wheelUp)
		if got := m.View(); got == "" {
			t.Fatalf("View() = %q, sin tamano de terminal conocido debe devolver un string aunque sea degradado", got)
		}
	})

	t.Run("pty 0x0 con mensaje foldeado", func(t *testing.T) {
		m := NewModel(nil, "s1", nil)

		m = apply(t, m, tea.WindowSizeMsg{Width: 0, Height: 0})
		m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}})

		m = apply(t, m, wheelUp)
		if got := m.View(); got == "" {
			t.Fatalf("View() = %q, con terminal 0x0 la rueda no debe tumbar la TUI y View debe devolver un string aunque sea degradado", got)
		}
	})
}

func TestModel_NewEventPreservesReadingPositionWhileScrolledUp(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	// Dos ruedas arriba: la cola deja de verse (precondicion del caso).
	m = apply(t, m, wheelUp)
	m = apply(t, m, wheelUp)
	offset := m.viewport.YOffset
	if got := m.View(); strings.Contains(got, "mensaje-29") {
		t.Fatalf("View() = %q, tras rueda arriba la cola %q NO debe seguir visible", got, "mensaje-29")
	}

	// Llega actividad nueva: conserva la posicion de lectura y muestra una
	// flecha pasiva en vez de arrastrar al usuario hacia la cola.
	m = apply(t, m, EventMsg{Message: &session.Message{
		ID:   "u30",
		Role: session.RoleUser,
		Text: "mensaje-30",
	}})
	if got := m.viewport.YOffset; got != offset {
		t.Fatalf("viewport.YOffset = %d, want %d: la actividad nueva no debe mover la posicion de lectura", got, offset)
	}
	view := ansi.Strip(m.View())
	if strings.Contains(view, "mensaje-30") {
		t.Fatalf("View() = %q, la actividad nueva no debe volver a mostrar la cola", view)
	}
	if !strings.Contains(view, "↓") {
		t.Fatalf("View() = %q, debe mostrar una flecha pasiva cuando hay actividad nueva fuera de vista", view)
	}
}

func TestModel_StreamingRevealPreservesReadingPositionAndMarksActivity(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID: fmt.Sprintf("u%02d", i), Role: session.RoleUser, Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: strings.Repeat("stream ", 20)})
	m = apply(t, m, wheelUp)
	m = apply(t, m, wheelUp)
	offset := m.viewport.YOffset

	m = apply(t, m, revealTickMsg{})

	if got := m.viewport.YOffset; got != offset {
		t.Fatalf("viewport.YOffset = %d, want %d: el reveal no debe arrastrar la lectura", got, offset)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↓") {
		t.Fatalf("View() = %q, el reveal fuera de vista debe marcar actividad nueva", view)
	}
}

func TestModel_ReturningToBottomClearsNewActivityIndicatorAndResumesFollowing(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID: fmt.Sprintf("u%02d", i), Role: session.RoleUser, Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}
	m = apply(t, m, wheelUp)
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u30", Role: session.RoleUser, Text: "mensaje-30"}})

	for !m.viewport.AtBottom() {
		m = apply(t, m, wheelDown)
	}
	if view := ansi.Strip(m.View()); strings.Contains(view, "↓") {
		t.Fatalf("View() = %q, al volver al fondo debe ocultar el indicador", view)
	}

	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u31", Role: session.RoleUser, Text: "mensaje-31"}})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "mensaje-31") {
		t.Fatalf("View() = %q, al volver al fondo debe reanudar el seguimiento", view)
	}
}

func TestModel_NewActivityIndicatorIsPassiveAndAgentOnly(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID: fmt.Sprintf("u%02d", i), Role: session.RoleUser, Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}
	m = apply(t, m, wheelUp)

	// Un cambio local de presentacion no es actividad nueva del agente.
	m = m.syncViewport()
	if view := ansi.Strip(m.View()); strings.Contains(view, "↓") {
		t.Fatalf("View() = %q, un cambio local no debe mostrar el indicador", view)
	}

	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u30", Role: session.RoleUser, Text: "mensaje-30"}})
	beforeOffset := m.viewport.YOffset
	beforeView := m.View()
	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.viewport.Width - 1,
		Y:      m.viewport.Height - 1,
	})
	if got := m.viewport.YOffset; got != beforeOffset {
		t.Fatalf("viewport.YOffset = %d, want %d: la flecha debe ser pasiva", got, beforeOffset)
	}
	if got := m.View(); got != beforeView {
		t.Fatalf("View() cambio tras clicar la flecha pasiva:\nantes=%q\ndespues=%q", beforeView, got)
	}
}

func TestModel_MouseClickIsInert(t *testing.T) {
	// TRIANGULATE: con tea.WithMouseCellMotion la terminal manda TAMBIEN clicks
	// y arrastres, no solo rueda. Un click izquierdo o un movimiento deben ser
	// inertes: no resuelven el permiso pendiente, no escriben en el input y no
	// cambian la vista.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	for i := 0; i < 5; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})

	before := m.View()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionMotion})

	if len(fake.resolved) != 0 {
		t.Fatalf("ResolvePermission fue llamado %d veces, un click NO debe disparar el gate de permisos", len(fake.resolved))
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, el click y el movimiento NO deben escribir en el textinput", got)
	}
	if got := m.View(); got != before {
		t.Fatalf("View() cambio tras el click/movimiento:\nantes = %q\ndespues = %q, los eventos de mouse que no son rueda deben ser inertes", before, got)
	}
}

func TestModel_WorkingIndicatorVisibleWhileRunning(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	// Sin corrida en curso no hay indicador.
	if got := m.View(); strings.Contains(got, "trabajando") {
		t.Fatalf("View() = %q, sin corrida en curso NO debe verse el indicador de trabajo", got)
	}

	// El usuario envia un prompt: aparece el indicador estable.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.View(); !strings.Contains(got, "trabajando") {
		t.Fatalf("View() = %q, debe mostrar el indicador %q mientras la corrida sigue", got, "trabajando")
	}

	// Con ready (tamano de terminal conocido) el indicador tambien se ve.
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	if got := m.View(); !strings.Contains(got, "trabajando") {
		t.Fatalf("View() = %q, con ready el indicador %q tambien debe verse", got, "trabajando")
	}

	// Fin de corrida limpio: el indicador desaparece.
	m = apply(t, m, RunDoneMsg{Err: ""})
	if got := m.View(); strings.Contains(got, "trabajando") {
		t.Fatalf("View() = %q, RunDoneMsg debe retirar el indicador de trabajo", got)
	}
}

func TestModel_ViewFitsHeightWithIndicator(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	// Muchas mas entradas de las que caben en 12 lineas.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	// Enviar un prompt enciende working: aparece la linea de estado.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	if !strings.Contains(view, "trabajando") {
		t.Fatalf("View() = %q, con corrida en curso debe verse el indicador %q", view, "trabajando")
	}
	if lines := strings.Count(view, "\n") + 1; lines > 12 {
		t.Fatalf("View() tiene %d lineas, la linea de estado NO debe romper el alto acotado (<= 12)", lines)
	}
	if !strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, la vista debe seguir la cola (%q visible) aun con la linea de estado", view, "mensaje-29")
	}
}

// TestModel_WorkingIndicatorAlignsWithComposerLeftMargin cubre el margen
// izquierdo de la linea de estado "trabajando": el resto de la vista (la caja
// del composer y la barra superior) arranca a composerOuterMargin columnas del
// borde izquierdo de la terminal, pero la linea del spinner arranca en la
// columna 0 (pegada al borde). El glifo del spinner debe alinearse con el
// borde "╭" de la caja del composer, ambos a composerOuterMargin columnas.
func TestModel_WorkingIndicatorAlignsWithComposerLeftMargin(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "hola")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	// La linea de estado se ubica por el glifo del spinner, no por la palabra
	// "trabajando": esta prueba es sobre alineacion de columnas, no sobre el
	// texto exacto de la linea (ese es un bug preexistente no relacionado, se
	// arregla aparte en la fase GREEN).
	lines := strings.Split(view, "\n")
	spinnerCol := -1
	for _, line := range lines {
		plain := ansi.Strip(line)
		if strings.Contains(plain, m.spinner.View()) {
			spinnerCol = len(plain) - len(strings.TrimLeft(plain, " "))
			break
		}
	}
	if spinnerCol == -1 {
		t.Fatalf("View() = %q, no se encontro ninguna linea con el spinner %q", view, m.spinner.View())
	}

	composerCol := -1
	for _, line := range lines {
		plain := ansi.Strip(line)
		trimmed := strings.TrimLeft(plain, " ")
		if strings.HasPrefix(trimmed, "╭") {
			composerCol = len(plain) - len(trimmed)
			break
		}
	}
	if composerCol == -1 {
		t.Fatalf("View() = %q, no se encontro la linea del borde superior (╭) de la caja del composer", view)
	}

	if spinnerCol != composerCol {
		t.Fatalf("columna del spinner = %d, columna del borde ╭ del composer = %d; ambas deben coincidir (mismo margen izquierdo)", spinnerCol, composerCol)
	}
	if spinnerCol != composerOuterMargin {
		t.Fatalf("columna del spinner+%q = %d, se esperaba composerOuterMargin (%d)", "trabajando", spinnerCol, composerOuterMargin)
	}
}

// TestModel_WorkingIndicatorAlignsWithComposerLeftMargin_WiderTerminal repite
// la aserción de alineación de columnas con un ancho de terminal distinto
// (100, no 40/80) para descartar que el margen observado sea un valor
// hardcodeado que solo coincide por casualidad con una corrida particular:
// si la implementación calculara la columna del spinner a partir del ancho de
// la terminal (p.ej. relativa o proporcional) en lugar de un prefijo fijo de
// composerOuterMargin espacios, este caso lo detectaría porque seguiria
// esperando exactamente composerOuterMargin sin importar el ancho. También
// confirma que ninguna línea de la vista excede el ancho de la terminal.
func TestModel_WorkingIndicatorAlignsWithComposerLeftMargin_WiderTerminal(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

	m = typeRunes(t, m, "hola")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	assertNoLineWiderThan(t, view, 100)

	lines := strings.Split(view, "\n")
	spinnerCol := -1
	for _, line := range lines {
		plain := ansi.Strip(line)
		if strings.Contains(plain, m.spinner.View()) {
			spinnerCol = len(plain) - len(strings.TrimLeft(plain, " "))
			break
		}
	}
	if spinnerCol == -1 {
		t.Fatalf("View() = %q, no se encontro ninguna linea con el spinner %q", view, m.spinner.View())
	}

	composerCol := -1
	for _, line := range lines {
		plain := ansi.Strip(line)
		trimmed := strings.TrimLeft(plain, " ")
		if strings.HasPrefix(trimmed, "╭") {
			composerCol = len(plain) - len(trimmed)
			break
		}
	}
	if composerCol == -1 {
		t.Fatalf("View() = %q, no se encontro la linea del borde superior (╭) de la caja del composer", view)
	}

	if spinnerCol != composerCol {
		t.Fatalf("columna del spinner = %d, columna del borde ╭ del composer = %d; ambas deben coincidir con ancho 100", spinnerCol, composerCol)
	}
	if spinnerCol != composerOuterMargin {
		t.Fatalf("columna del spinner+%q con ancho 100 = %d, se esperaba composerOuterMargin (%d), no un valor dependiente del ancho", "trabajando", spinnerCol, composerOuterMargin)
	}
}

// TestModel_WorkingIndicatorDoesNotOverflowTinyTerminal cubre una terminal muy
// chica (Width 10): chatContent() acota el margen de la línea de estado con
// `min(composerOuterMargin, m.chatContentWidth()/2)`, el mismo patrón que
// topBarLine usa para su margen, así que ninguna línea de View() debe exceder
// el ancho de la terminal (10 celdas) ni producir una sangría negativa. Si una
// implementación futura volviera a un prefijo fijo sin acotar, este test lo
// detectaría.
func TestModel_WorkingIndicatorDoesNotOverflowTinyTerminal(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 10, Height: 24})

	m = typeRunes(t, m, "hola")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	assertNoLineWiderThan(t, view, 10)

	lines := strings.Split(view, "\n")
	for _, line := range lines {
		plain := ansi.Strip(line)
		if strings.Contains(plain, m.spinner.View()) {
			indent := len(plain) - len(strings.TrimLeft(plain, " "))
			if indent < 0 {
				t.Fatalf("View() = %q, la linea de estado tiene sangria negativa/corrupta (%d) en terminal minuscula", view, indent)
			}
		}
	}
}

func TestModel_SurvivesTinyTerminal(t *testing.T) {
	// Bug real (E2E bajo pty): una terminal diminuta (0x0 al crear el pty, o de
	// 1 linea) deja el alto del viewport NEGATIVO en resizeViewport
	// (m.height - m.reservedLines()) y bubbles/viewport paniquea (slice out of
	// range en visibleLines) al hacer SetContent/GotoBottom en syncViewport.
	// Comportamiento esperado (GREEN): sin panic, View() devuelve un string
	// (puede ser degradado) y el programa sigue vivo.

	t.Run("pty 0x0", func(t *testing.T) {
		m := NewModel(nil, "s1", nil)

		// El pty recien creado anuncia tamano 0x0.
		m = apply(t, m, tea.WindowSizeMsg{Width: 0, Height: 0})

		// Un evento que toca el viewport y el render no deben tumbar la TUI.
		m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}})
		if got := m.View(); got == "" {
			t.Fatalf("View() = %q, con terminal 0x0 debe devolver un string aunque sea degradado", got)
		}
	})

	t.Run("terminal de 1 linea con corrida en curso", func(t *testing.T) {
		fake := &fakeAgent{}
		m := NewModel(fake, "s1", nil)

		// Con 1 linea de alto, encender working (input + linea de estado) deja
		// las lineas reservadas por encima del alto: viewport negativo.
		m = apply(t, m, tea.WindowSizeMsg{Width: 20, Height: 1})
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

		if len(fake.sent) != 1 {
			t.Fatalf("SendPrompt fue llamado %d veces, Enter debe enviar el prompt exactamente una vez", len(fake.sent))
		}
		m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}})
		if got := m.View(); got == "" {
			t.Fatalf("View() = %q, con terminal de 1 linea debe devolver un string aunque sea degradado", got)
		}
	})
}

func TestModel_RecoversAfterResizeFromTiny(t *testing.T) {
	// TRIANGULATE: un fix pobre podria "sobrevivir" a la terminal diminuta
	// congelando el viewport o dejando ready = false para siempre. Este caso
	// exige que, tras crecer la terminal, el viewport vuelva a mostrar la cola
	// del transcript y siga acotando el alto al de la terminal.
	m := NewModel(nil, "s1", nil)

	// El pty recien creado anuncia 0x0.
	m = apply(t, m, tea.WindowSizeMsg{Width: 0, Height: 0})

	// Foldear 30 mensajes de usuario con la terminal aun 0x0 no debe paniquear.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	// La terminal crece a un tamano utilizable.
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	view := m.View()
	if !strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, tras crecer la terminal debe volver a verse la cola del transcript (mensaje-29)", view)
	}
	if strings.Contains(view, "mensaje-00") {
		t.Fatalf("View() = %q, el alto debe seguir acotado: mensaje-00 no cabe en 10 lineas", view)
	}
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("View() tiene %d lineas, no debe exceder el alto de la terminal (10)", lines)
	}
}

func TestModel_WrapsLongAssistantTextToTerminalWidth(t *testing.T) {
	// Bug real (reproducido E2E): en una terminal angosta la respuesta del
	// assistant se ve como UNA sola linea truncada. El transcript se vuelca
	// crudo al viewport de bubbles, que corta horizontalmente cada linea al
	// ancho de la terminal (ansi.Cut) en vez de hacer word-wrap: el final del
	// texto desaparece de la vista.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	long := "esta es una respuesta larga del assistant que en una terminal angosta debe hacer wrap a varias lineas para leerse entera fin-de-respuesta"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: long})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "fin-de-respuesta") {
		t.Fatalf("View() = %q, el final del texto %q debe estar visible: el texto mas ancho que la terminal debe hacer wrap a varias lineas, no truncarse", view, "fin-de-respuesta")
	}
	assertNoLineWiderThan(t, view, 40)
}

func TestModel_RewrapsOnResize(t *testing.T) {
	// TRIANGULATE: un fix pobre podria envolver el transcript UNA sola vez al
	// primer ancho anunciado. Cuando la terminal se angosta, el texto debe
	// re-envolverse al ancho nuevo, no quedar cortado al ancho viejo.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	long := "esta respuesta larga del assistant debe re-envolverse cuando la terminal cambia de ancho fin-de-respuesta"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: long})
	m = drainReveal(t, m)

	// La terminal se angosta: el transcript debe re-envolverse a 24 celdas.
	m = apply(t, m, tea.WindowSizeMsg{Width: 24, Height: 10})

	view := m.View()
	if !strings.Contains(view, "fin-de-respuesta") {
		t.Fatalf("View() = %q, el final del texto %q debe seguir visible tras el resize: el transcript debe re-envolverse al ancho nuevo", view, "fin-de-respuesta")
	}
	assertNoLineWiderThan(t, view, 24)
}

func TestModel_WrapsUnbreakableLongToken(t *testing.T) {
	// TRIANGULATE: una implementacion de solo word-wrap no parte tokens sin
	// espacios mas largos que el ancho: una URL larga quedaria en una sola
	// linea que el viewport trunca. El token debe partirse en varias lineas
	// para leerse entero.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	// Un solo token de 92 celdas sin espacios: cortado duro a 40 da lineas de
	// 40 + 40 + 12, y el sufijo distintivo cae entero en la ultima linea.
	url := "https://example.com/" + strings.Repeat("x", 60) + "sufijo-final"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: url})
	m = drainReveal(t, m)

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "sufijo-") || !strings.Contains(view, "final") {
		t.Fatalf("View() sin ANSI = %q, el sufijo final debe seguir visible aunque el renderer Markdown lo envuelva", view)
	}
	assertNoLineWiderThan(t, view, 40)
}

func TestModel_FollowsTailOfWrappedResponse(t *testing.T) {
	// TRIANGULATE: GotoBottom cuenta lineas sobre el contenido ya cargado en el
	// viewport. Si el transcript se envolviera DESPUES de SetContent, el conteo
	// de lineas quedaria corto y la vista no seguiria la cola de una respuesta
	// que envuelta ocupa mas lineas que el alto del viewport.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	// ~500 celdas de palabras: envuelto a 40 ocupa ~14 lineas, mas que el alto
	// del viewport (9). Token distintivo al inicio y otro al final.
	long := "inicio-de-respuesta " + strings.Repeat("palabra ", 60) + "fin-de-respuesta"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: long})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "fin-de-respuesta") {
		t.Fatalf("View() = %q, la vista debe seguir la cola: el final %q de la respuesta envuelta debe estar visible", view, "fin-de-respuesta")
	}
	if strings.Contains(view, "inicio-de-respuesta") {
		t.Fatalf("View() = %q, el inicio %q NO debe verse: la respuesta envuelta ocupa mas lineas que el viewport y la vista debe seguir la cola", view, "inicio-de-respuesta")
	}
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("View() tiene %d lineas, no debe exceder el alto de la terminal (10)", lines)
	}
}

func TestModel_ComposerBottomBorderShowsModel(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "openrouter/free")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})

	plain := ansi.Strip(m.View())
	lines := strings.Split(plain, "\n")
	var bottomBorder string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "╰") {
			bottomBorder = line
		}
	}
	if bottomBorder == "" {
		t.Fatalf("View() = %q, want a composer bottom border", plain)
	}
	if !strings.Contains(bottomBorder, "openrouter/free") {
		t.Fatalf("composer bottom border = %q, want model label %q", bottomBorder, "openrouter/free")
	}
	if strings.Contains(plain, "\nbuild · openrouter/free") {
		t.Fatalf("View() = %q, agent/model status must not render as a standalone footer", plain)
	}
	assertBoxLinesExactWidth(t, m.View(), 60)
}

func TestModel_ComposerHasTwoCellOuterMargin(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "model")
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 8})

	lines := strings.Split(ansi.Strip(m.View()), "\n")
	bottom := -1
	for index, line := range lines {
		if strings.Contains(line, "╰") {
			bottom = index
			if !strings.HasPrefix(line, "  ") || !strings.HasSuffix(line, "  ") {
				t.Fatalf("composer border = %q, want two-cell left and right margins", line)
			}
		}
	}
	if bottom == -1 {
		t.Fatalf("View() = %q, want composer bottom border", ansi.Strip(m.View()))
	}
	if bottom+2 >= len(lines) || strings.TrimSpace(lines[bottom+1]) != "" || strings.TrimSpace(lines[bottom+2]) != "" {
		t.Fatalf("lines after composer = %q, want two empty bottom rows", lines[bottom+1:])
	}
}

func TestModel_ComposerBottomBorderTruncatesLongModel(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5-very-long-model-name")
	m = apply(t, m, tea.WindowSizeMsg{Width: 32, Height: 12})

	plain := ansi.Strip(m.composerBox())
	lines := strings.Split(plain, "\n")
	bottomBorder := lines[len(lines)-1]
	if !strings.Contains(bottomBorder, "…") {
		t.Fatalf("composer bottom border = %q, want a truncated model label", bottomBorder)
	}
	if strings.Contains(bottomBorder, "very-long-model-name") {
		t.Fatalf("composer bottom border = %q, long model label must be truncated", bottomBorder)
	}
	assertBoxLinesExactWidth(t, m.composerBox(), 32)
}

func TestModel_ComposerBottomBorderKeepsPlanVisibleWhenModelIsTruncated(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5-very-long-model-name")
	m = apply(t, m, tea.WindowSizeMsg{Width: 32, Height: 12})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	plain := ansi.Strip(m.composerBox())
	bottomBorder := strings.Split(plain, "\n")[2]
	if !strings.Contains(bottomBorder, "… · plan") {
		t.Fatalf("composer bottom border = %q, truncated model must keep the active plan mode visible", bottomBorder)
	}
	assertBoxLinesExactWidth(t, m.composerBox(), 32)
}

func TestModel_ComposerBottomBorderOmitsModelWhenTerminalIsTooNarrow(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "model")
	m = apply(t, m, tea.WindowSizeMsg{Width: 6, Height: 8})

	plain := ansi.Strip(m.composerBox())
	lines := strings.Split(plain, "\n")
	bottomBorder := lines[len(lines)-1]
	if strings.Contains(bottomBorder, "model") || strings.Contains(bottomBorder, "…") {
		t.Fatalf("composer bottom border = %q, terminal too narrow must omit the model label", bottomBorder)
	}
	if !strings.HasPrefix(bottomBorder, "╰") || !strings.HasSuffix(bottomBorder, "╯") {
		t.Fatalf("composer bottom border = %q, rounded corners must remain intact", bottomBorder)
	}
	assertBoxLinesExactWidth(t, m.composerBox(), 6)
}

func TestModel_ComposerBordersKeepTokensAndModelWithoutShortcuts(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "openrouter/free")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	m = apply(t, m, EventMsg{
		Kind: session.KindStepEnded,
		Usage: &session.Usage{
			InputTokens:  1_234,
			OutputTokens: 345,
		},
	})

	plain := ansi.Strip(m.composerBox())
	lines := strings.Split(plain, "\n")
	if !strings.Contains(lines[0], "↑ 1.2k ↓ 345") {
		t.Fatalf("composer top border = %q, want token usage", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "openrouter/free") {
		t.Fatalf("composer bottom border = %q, want model label", lines[len(lines)-1])
	}
	for _, shortcut := range []string{"Shift+Tab", "Ctrl+.", "shortcuts"} {
		if strings.Contains(plain, shortcut) {
			t.Fatalf("composerBox() = %q, must not add shortcut hint %q", plain, shortcut)
		}
	}
	assertBoxLinesExactWidth(t, m.composerBox(), 60)
}

func TestModel_ComposerCtrlJInsertsNewlineAndEnterSubmitsMultilinePrompt(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m = typeRunes(t, m, "primera linea")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = typeRunes(t, m, "segunda linea")

	if got, want := m.input.Value(), "primera linea\nsegunda linea"; got != want {
		t.Fatalf("input.Value() = %q, Ctrl+J debe insertar un salto de linea y conservar el borrador %q", got, want)
	}
	if got := strings.Count(ansi.Strip(m.composerBox()), "\n"); got != 3 {
		t.Fatalf("composerBox() tiene %d saltos, con dos lineas debe crecer a cuatro lineas incluyendo bordes", got)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.sent) != 1 {
		t.Fatalf("SendPrompt fue llamado %d veces, Enter debe enviar el prompt multilinea exactamente una vez", len(fake.sent))
	}
	if got, want := fake.sent[0].text, "primera linea\nsegunda linea"; got != want {
		t.Fatalf("SendPrompt text = %q, want %q", got, want)
	}
}

func TestModel_ComposerGrowthStopsAtFiveLines(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	for line := 0; line < composerMaxLines+2; line++ {
		m = typeRunes(t, m, fmt.Sprintf("linea %d", line))
		if line < composerMaxLines+1 {
			m = apply(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
		}
	}

	if got := m.input.Height(); got != composerMaxLines {
		t.Fatalf("input.Height() = %d, el composer debe dejar de crecer en %d lineas", got, composerMaxLines)
	}
	if got := strings.Count(ansi.Strip(m.composerBox()), "\n"); got != composerMaxLines+1 {
		t.Fatalf("composerBox() tiene %d saltos, con el limite debe renderizar %d lineas incluyendo bordes", got, composerMaxLines+1)
	}
}

func TestModel_ComposerTopBorderShowsTokenUsage(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	m = apply(t, m, EventMsg{
		Kind: session.KindStepEnded,
		Usage: &session.Usage{
			InputTokens:  1_234,
			OutputTokens: 345,
		},
	})

	plain := ansi.Strip(m.View())
	var topBorder string
	for _, line := range strings.Split(plain, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "╭") {
			topBorder = line
			break
		}
	}
	if topBorder == "" {
		t.Fatalf("View() = %q, want a composer top border", plain)
	}
	for _, want := range []string{"↑ 1.2k", "↓ 345", "ctx 1.2k/200k"} {
		if !strings.Contains(topBorder, want) {
			t.Fatalf("composer top border = %q, want it to contain %q", topBorder, want)
		}
	}
	assertBoxLinesExactWidth(t, m.View(), 60)
}

func TestModel_ComposerTokenUsageUpdatesDuringStreaming(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	m = apply(t, m, EventMsg{
		Kind:  session.KindStepStarted,
		Usage: &session.Usage{InputTokens: 1_200},
	})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~1.2k ↓ 0 ctx ~1.2k/200k") {
		t.Fatalf("View() = %q, want live input usage at step start", view)
	}

	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: strings.Repeat("a", 3_000)})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~1.2k ↓ ~1k ctx ~1.2k/200k") {
		t.Fatalf("View() = %q, want live output usage after text delta", view)
	}

	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: strings.Repeat("b", 1_500)})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~1.2k ↓ ~1.5k ctx ~1.2k/200k") {
		t.Fatalf("View() = %q, want live output usage after reasoning delta", view)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolInputDelta, Text: strings.Repeat("c", 1_500)})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~1.2k ↓ ~2k ctx ~1.2k/200k") {
		t.Fatalf("View() = %q, want live output usage after tool input delta", view)
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindStepEnded,
		Usage: &session.Usage{
			InputTokens:     1_300,
			OutputTokens:    900,
			ReasoningTokens: 100,
		},
	})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ 1.3k ↓ 900 ctx 1.3k/200k") {
		t.Fatalf("View() = %q, want exact provider usage after step end", view)
	}
}

func TestModel_LiveUsageTransitions(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m.outputBytes = 9
	m.reasoningBytes = 12
	m.toolInputBytes = 15
	m = m.foldEvent(EventMsg{Kind: session.KindStepStarted, Usage: &session.Usage{InputTokens: 20}})
	if !m.liveUsage || m.outputBytes != 0 || m.reasoningBytes != 0 || m.toolInputBytes != 0 {
		t.Fatalf("StepStarted = live:%v bytes:%d/%d/%d, quiero uso vivo con contadores reiniciados", m.liveUsage, m.outputBytes, m.reasoningBytes, m.toolInputBytes)
	}

	m = m.foldEvent(EventMsg{Kind: session.KindTextDelta, Text: "abcdef"})
	estimated := *m.usage
	m = m.foldEvent(EventMsg{Kind: session.KindStepEnded})
	if m.liveUsage || *m.usage != estimated {
		t.Fatalf("StepEnded sin Usage = live:%v usage:%+v, quiero conservar estimacion %+v y cerrar uso vivo", m.liveUsage, *m.usage, estimated)
	}

	m.liveUsage = true
	m = m.foldEvent(EventMsg{Kind: session.KindStepFailed, Error: "boom"})
	if m.liveUsage {
		t.Fatal("StepFailed debe cerrar el uso vivo")
	}
}

func TestModel_UpdateLiveUsageRequiresActiveUsage(t *testing.T) {
	for _, m := range []Model{
		{liveUsage: false, usage: &session.Usage{OutputTokens: 7}, outputBytes: 30},
		{liveUsage: true, usage: nil, outputBytes: 30},
	} {
		beforeUsage := m.usage
		m = m.updateLiveUsage()
		if m.usage != beforeUsage || m.outputBytes != 30 {
			t.Fatalf("updateLiveUsage() modifico un modelo sin uso activo: %+v", m)
		}
	}
}

func TestEstimatedTokens(t *testing.T) {
	for _, tc := range []struct{ bytes, want int }{{0, 0}, {1, 1}, {2, 1}, {3, 1}, {30_000, 10_000}} {
		if got := estimatedTokens(tc.bytes); got != tc.want {
			t.Errorf("estimatedTokens(%d) = %d, quiero %d", tc.bytes, got, tc.want)
		}
	}
}

func TestModel_ComposerDistinguishesEstimatedAndExactInputUsage(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	m = apply(t, m, EventMsg{
		Kind:  session.KindStepStarted,
		Usage: &session.Usage{InputTokens: 10_000},
	})

	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ ~10k") || !strings.Contains(view, "ctx ~10k/200k") {
		t.Fatalf("live View() = %q, want the conservative 10k estimate marked as approximate", view)
	}

	m = apply(t, m, EventMsg{
		Kind:  session.KindStepEnded,
		Usage: &session.Usage{InputTokens: 9_100, OutputTokens: 250},
	})

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "↑ 9.1k") || !strings.Contains(view, "ctx 9.1k/200k") {
		t.Fatalf("completed View() = %q, want exact provider usage 9.1k", view)
	}
	if strings.Contains(view, "~9.1k") {
		t.Fatalf("completed View() = %q, exact provider usage must not be marked approximate", view)
	}
}

func TestModel_ComposerTokenUsageHandlesUnknownModelAndNarrowWidth(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("build", "custom/model")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	m = apply(t, m, EventMsg{
		Kind:  session.KindStepEnded,
		Usage: &session.Usage{InputTokens: 10_000, OutputTokens: 2_500},
	})

	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "↑ 10k ↓ 2.5k") {
		t.Fatalf("View() = %q, want compact input and output token counts", plain)
	}
	if strings.Contains(plain, "ctx") {
		t.Fatalf("View() = %q, unknown models must not show a made-up context window", plain)
	}

	m = apply(t, m, EventMsg{
		Kind:  session.KindStepEnded,
		Usage: &session.Usage{InputTokens: 20_000, OutputTokens: 3_000},
	})
	if plain = ansi.Strip(m.View()); strings.Contains(plain, "↑ 10k") || !strings.Contains(plain, "↑ 20k ↓ 3k") {
		t.Fatalf("View() = %q, want the latest completed step usage", plain)
	}

	m = apply(t, m, tea.WindowSizeMsg{Width: 10, Height: 8})
	assertBoxLinesExactWidth(t, m.View(), 10)
	if plain := ansi.Strip(m.composerBox()); strings.Contains(plain, "↑") {
		t.Fatalf("composerBox() = %q, una caja demasiado estrecha debe omitir la etiqueta", plain)
	}
}

func TestModel_ComposerBoxWithoutUsageHasNoTokenLabel(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	if plain := ansi.Strip(m.composerBox()); strings.Contains(plain, "↑") || strings.Contains(plain, "↓") {
		t.Fatalf("composerBox() = %q, sin usage no debe mostrar tokens", plain)
	}
}

func TestFormatTokenCount(t *testing.T) {
	for _, tc := range []struct {
		tokens int
		want   string
	}{
		{0, "0"}, {999, "999"}, {1_000, "1k"}, {1_500, "1.5k"},
		{9_999, "10k"}, {10_000, "10k"}, {128_000, "128k"},
	} {
		if got := formatTokenCount(tc.tokens); got != tc.want {
			t.Errorf("formatTokenCount(%d) = %q, quiero %q", tc.tokens, got, tc.want)
		}
	}
}

func TestModel_ComposerBoxWrapsInput(t *testing.T) {
	// TRIANGULATE: el input vive SIEMPRE dentro de una caja de borde redondeado
	// que abarca el ancho de la terminal (estilo Claude Code), este o no fijado
	// el status del composer.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	view := m.View()
	for _, want := range []string{"╭", "╰"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, el input debe renderizarse dentro de una caja de borde redondeado: falta %q", view, want)
		}
	}
	assertBoxLinesExactWidth(t, view, 40)

	// La caja tiene padding horizontal (estilo Claude Code): la linea interior
	// arranca con "│ ❯" (borde, espacio, prompt), no con el prompt pegado al
	// borde. Se mide sin ANSI porque el prompt va estilizado.
	if plain := ansi.Strip(view); !strings.Contains(plain, "│ ❯") {
		t.Fatalf("View() sin ANSI = %q, la linea interior de la caja debe tener padding horizontal: debe contener %q (borde, espacio, prompt), no el prompt pegado al borde", plain, "│ ❯")
	}

	topAt, inputAt, bottomAt := -1, -1, -1
	for i, line := range strings.Split(view, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		switch {
		case strings.HasPrefix(trimmed, "╭"):
			topAt = i
		case strings.HasPrefix(trimmed, "╰"):
			bottomAt = i
		case strings.Contains(line, inputPrompt):
			inputAt = i
		}
	}
	if topAt == -1 || inputAt == -1 || bottomAt == -1 || topAt >= inputAt || inputAt >= bottomAt {
		t.Fatalf("View() = %q, la linea del input (%q en %d) debe quedar ENTRE el borde superior (╭ en %d) y el inferior (╰ en %d)", view, inputPrompt, inputAt, topAt, bottomAt)
	}

	// Con status fijado el pie queda DEBAJO del borde inferior de la caja.
	m2 := NewModel(nil, "s1", nil).WithStatus("build", "openrouter/free")
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 40, Height: 12})
	view2 := m2.View()
	bottomAt2 := strings.Index(view2, "╰")
	footerAt := strings.Index(view2, "openrouter/free")
	if bottomAt2 == -1 || footerAt == -1 || footerAt < bottomAt2 {
		t.Fatalf("View() = %q, el pie de status (openrouter/free en %d) debe aparecer DESPUES del borde inferior de la caja (╰ en %d)", view2, footerAt, bottomAt2)
	}
}

func TestModel_ComposerBoxFollowsResize(t *testing.T) {
	// TRIANGULATE: una caja hardcodeada al primer ancho anunciado no sirve;
	// tras redimensionar la terminal cada linea de la caja debe medir el ancho
	// nuevo.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
	assertBoxLinesExactWidth(t, m.View(), 40)

	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	assertBoxLinesExactWidth(t, m.View(), 60)
}

func TestModel_ViewFitsHeightWithBoxModelAndIndicator(t *testing.T) {
	// TRIANGULATE: con la caja (3 lineas), el modelo en el borde y el indicador de
	// trabajo encendidos a la vez, el alto sigue acotado al de la terminal y la
	// vista sigue la cola del transcript.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "openrouter/free")
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	// Muchas mas entradas de las que caben en 12 lineas.
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	// Enviar un prompt enciende working: aparece el indicador sobre la caja.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	if lines := strings.Count(view, "\n") + 1; lines > 12 {
		t.Fatalf("View() tiene %d lineas, caja + pie + indicador no deben romper el alto acotado (<= 12)", lines)
	}
	for _, want := range []string{"mensaje-29", "trabajando", "openrouter/free"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, debe contener %q (cola del transcript, indicador de trabajo y pie de status)", view, want)
		}
	}
	assertBoxLinesExactWidth(t, view, 40)
}

func TestModel_LongTypedPromptKeepsBoxSingleLine(t *testing.T) {
	// TRIANGULATE: un prompt tecleado mas largo que el ancho de la terminal NO
	// debe envolver la caja a mas lineas: el textinput scrollea horizontal y la
	// caja se mantiene en 3 lineas (borde, UNA linea de input, borde).
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 24, Height: 10})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("a", 80))})

	view := m.View()
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("View() tiene %d lineas, un prompt largo no debe romper el alto acotado (<= 10)", lines)
	}
	if got := strings.Count(view, "❯"); got != 1 {
		t.Fatalf("View() = %q, el prompt %q debe aparecer exactamente una vez (count=%d): el prompt largo no debe envolver la caja", view, "❯", got)
	}
	interior := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "│") {
			interior++
		}
	}
	if interior != 1 {
		t.Fatalf("View() = %q, la caja debe seguir siendo de 3 lineas con UNA sola linea interior (lineas │ = %d)", view, interior)
	}
	assertNoLineWiderThan(t, view, 24)
	assertBoxLinesExactWidth(t, view, 24)
}

func TestModel_TabTogglesAgentModeToPlan(t *testing.T) {
	// Tab alterna el modo del agente entre "build" y "plan" (estilo Claude
	// Code): el pie del composer refleja el modo en vivo y Enter envia el
	// prompt por el camino del modo activo (SendPlanPrompt en plan).
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "openrouter/free")

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	view := m.View()
	if !strings.Contains(view, "openrouter/free · plan") {
		t.Fatalf("View() = %q, tras Tab el pie del composer debe mostrar %q", view, "openrouter/free · plan")
	}
	if strings.Contains(view, "build ·") {
		t.Fatalf("View() = %q, tras Tab el pie NO debe seguir mostrando %q", view, "build ·")
	}

	// En modo plan, Enter envia el prompt via SendPlanPrompt, no via SendPrompt.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("investiga x")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.planSent) != 1 {
		t.Fatalf("SendPlanPrompt fue llamado %d veces, Enter en modo plan debe enviar el prompt exactamente una vez por el camino de plan", len(fake.planSent))
	}
	if got := fake.planSent[0]; got.sessionID != "s1" || got.text != "investiga x" {
		t.Fatalf("SendPlanPrompt(%q, %q), se esperaba SendPlanPrompt(%q, %q)", got.sessionID, got.text, "s1", "investiga x")
	}
	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, en modo plan el prompt NO debe ir por el camino de build", len(fake.sent))
	}
}

func TestModel_TabTogglesBackToBuild(t *testing.T) {
	// TRIANGULATE: Tab ALTERNA el modo, no solo lo enciende. Dos Tab devuelven
	// el pie del composer a build y Enter vuelve a enviar por SendPrompt (el
	// camino normal), no por SendPlanPrompt.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	view := m.View()
	if !strings.Contains(view, " m ─╯") {
		t.Fatalf("View() = %q, tras Tab Tab el borde del composer debe volver a mostrar solo el modelo %q", view, "m")
	}
	if strings.Contains(view, "· plan") {
		t.Fatalf("View() = %q, tras Tab Tab el pie NO debe seguir mostrando %q", view, "· plan")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hazlo")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 1 {
		t.Fatalf("SendPrompt fue llamado %d veces, de vuelta en build Enter debe enviar exactamente una vez por el camino normal", len(fake.sent))
	}
	if got := fake.sent[0]; got.sessionID != "s1" || got.text != "hazlo" {
		t.Fatalf("SendPrompt(%q, %q), se esperaba SendPrompt(%q, %q)", got.sessionID, got.text, "s1", "hazlo")
	}
	if len(fake.planSent) != 0 {
		t.Fatalf("SendPlanPrompt fue llamado %d veces, tras volver a build el prompt NO debe ir por el camino de plan", len(fake.planSent))
	}
}

func TestModel_TabIsInertWhilePermissionPending(t *testing.T) {
	// TRIANGULATE: con un permiso pendiente el teclado esta en modo aprobacion
	// (solo y/n hacen algo): Tab NO debe alternar el modo del agente ni cambiar
	// el pie del composer.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	view := m.View()
	if !strings.Contains(view, " m ─╯") {
		t.Fatalf("View() = %q, con permiso pendiente Tab NO debe cambiar el borde: debe seguir mostrando el modelo %q", view, "m")
	}
	if strings.Contains(view, "· plan") {
		t.Fatalf("View() = %q, con permiso pendiente Tab NO debe activar el modo plan", view)
	}
}

func TestModel_PresentPlanOffersAcceptAndYExecutes(t *testing.T) {
	// Cuando el agente presenta un plan (tool present_plan asentada con exito),
	// la conversacion muestra una oferta de aprobacion pendiente; la tecla 'y'
	// acepta el plan via Agent.AcceptPlan y retira la oferta.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "p1"})

	view := m.View()
	planLine := lineWith(t, view, "? plan")
	if !strings.Contains(planLine, "(y ejecutar / n seguir en plan)") {
		t.Fatalf("oferta de aprobacion = %q, debe contener %q", planLine, "(y ejecutar / n seguir en plan)")
	}

	// 'y' acepta el plan: UNA llamada a AcceptPlan con la sesion de la TUI.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	if len(fake.accepted) != 1 {
		t.Fatalf("AcceptPlan fue llamado %d veces, 'y' debe aceptar el plan exactamente una vez", len(fake.accepted))
	}
	if got := fake.accepted[0]; got != "s1" {
		t.Fatalf("AcceptPlan(%q), se esperaba AcceptPlan(%q)", got, "s1")
	}
	if got := m.View(); strings.Contains(got, "? plan") {
		t.Fatalf("View() = %q, aceptar el plan debe retirar la oferta de aprobacion", got)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, aceptar el plan NO debe enviar un prompt por el camino de build", len(fake.sent))
	}
	if len(fake.planSent) != 0 {
		t.Fatalf("SendPlanPrompt fue llamado %d veces, aceptar el plan NO debe enviar un prompt por el camino de plan", len(fake.planSent))
	}
}

func TestModel_PlanApprovalNRejectsAndStaysInPlanMode(t *testing.T) {
	// TRIANGULATE: 'n' descarta la oferta de aprobacion SIN tocar el modo ni
	// aceptar nada: el pie sigue en plan y el proximo Enter sigue yendo por
	// SendPlanPrompt. Una implementacion rota que apague planMode (o llame
	// AcceptPlan) al rechazar debe caer aqui.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab}) // a plan-mode
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "p1"})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})

	if got := m.View(); strings.Contains(got, "? plan") {
		t.Fatalf("View() = %q, 'n' debe retirar la oferta de aprobacion del plan", got)
	}
	if len(fake.accepted) != 0 {
		t.Fatalf("AcceptPlan fue llamado %d veces, 'n' NO debe aceptar el plan", len(fake.accepted))
	}
	if got := m.View(); !strings.Contains(got, "m · plan") {
		t.Fatalf("View() = %q, tras 'n' el pie debe seguir mostrando %q: rechazar la oferta no cambia el modo", got, "m · plan")
	}

	// El siguiente envio sigue yendo por el camino de plan: el modo no cambio.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ajusta el plan")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.planSent) != 1 {
		t.Fatalf("SendPlanPrompt fue llamado %d veces, tras 'n' Enter debe seguir enviando por el camino de plan exactamente una vez", len(fake.planSent))
	}
	if got := fake.planSent[0]; got.sessionID != "s1" || got.text != "ajusta el plan" {
		t.Fatalf("SendPlanPrompt(%q, %q), se esperaba SendPlanPrompt(%q, %q)", got.sessionID, got.text, "s1", "ajusta el plan")
	}
	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, tras 'n' el prompt NO debe ir por el camino de build", len(fake.sent))
	}
}

func TestModel_PlanApprovalCapturesKeyboard(t *testing.T) {
	// TRIANGULATE: con la oferta de plan pendiente el teclado esta en modo
	// aprobacion: las runas normales NO alimentan el input y Enter NO envia
	// nada. 'y' despues acepta: el pie vuelve a build y la corrida queda
	// trabajando hasta RunDoneMsg.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab}) // a plan-mode
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "p1"})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, las runas normales NO deben entrar al input mientras hay plan pendiente", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.sent) != 0 || len(fake.planSent) != 0 || len(fake.accepted) != 0 {
		t.Fatalf("sent=%d planSent=%d accepted=%d, ni Enter ni las runas normales deben enviar o aceptar nada con plan pendiente", len(fake.sent), len(fake.planSent), len(fake.accepted))
	}

	// 'y' acepta: vuelve a build y la corrida queda en curso.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(fake.accepted) != 1 || fake.accepted[0] != "s1" {
		t.Fatalf("accepted = %v, 'y' debe llamar AcceptPlan(%q) exactamente una vez", fake.accepted, "s1")
	}
	view := m.View()
	if !strings.Contains(view, " m ─╯") {
		t.Fatalf("View() = %q, tras aceptar el plan el borde debe volver a mostrar solo el modelo %q", view, "m")
	}
	if strings.Contains(view, "· plan") {
		t.Fatalf("View() = %q, tras aceptar el plan el pie NO debe seguir mostrando %q", view, "· plan")
	}
	if !strings.Contains(view, "trabajando") {
		t.Fatalf("View() = %q, tras aceptar el plan la corrida queda en curso: debe verse el indicador %q", view, "trabajando")
	}
}

func TestModel_PresentPlanFailedDoesNotOfferApproval(t *testing.T) {
	// Punto fino: un present_plan asentado con Tool.Failed NO ofrece aprobacion
	// y el teclado sigue normal (la runa va al input y 'y' no acepta nada).
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m")

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "p1", Error: "plan invalido"})

	if got := m.View(); strings.Contains(got, "? plan") {
		t.Fatalf("View() = %q, un present_plan fallido NO debe ofrecer la aprobacion del plan", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(fake.accepted) != 0 {
		t.Fatalf("AcceptPlan fue llamado %d veces, sin oferta pendiente 'y' NO debe aceptar nada", len(fake.accepted))
	}
	if got := m.input.Value(); got != "y" {
		t.Fatalf("input.Value() = %q, sin oferta pendiente la runa 'y' debe ir al input normal", got)
	}
}

func TestModel_EnterSendsTypedPromptViaAgent(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	// El usuario teclea "hola" y pulsa Enter.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 1 {
		t.Fatalf("SendPrompt fue llamado %d veces, Enter debe enviar el prompt exactamente una vez", len(fake.sent))
	}
	if got := fake.sent[0]; got.sessionID != "s1" || got.text != "hola" {
		t.Fatalf("SendPrompt(%q, %q), se esperaba SendPrompt(%q, %q)", got.sessionID, got.text, "s1", "hola")
	}
	if !m.Working() {
		t.Fatalf("Working() = false, el modelo debe quedar trabajando tras enviar el prompt hasta RunDoneMsg")
	}
}

// menuCommands son los comandos compartidos por los tests del menu "/".
var menuCommands = []command.Command{
	{Name: "commit", Description: "genera un commit"},
	{Name: "review", Description: "revisa el diff"},
}

// typeRunes alimenta el input runa por runa, como tecleos reales.
func typeRunes(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			msg.Type = tea.KeySpace // bubbletea reporta el espacio como KeySpace
		}
		m = apply(t, m, msg)
	}
	return m
}

// menuSelectedLine devuelve la linea del menu marcada con "❯ " (prefijo al
// inicio de linea, sin ANSI), o "" si no hay ninguna. La linea del composer no
// confunde: arranca con el borde "│", no con el marcador.
func menuSelectedLine(view string) string {
	for _, line := range strings.Split(view, "\n") {
		if plain := ansi.Strip(line); strings.HasPrefix(plain, "❯ ") {
			return plain
		}
	}
	return ""
}

func TestModel_CommandMenuFiltersAsYouType(t *testing.T) {
	// El menu se recomputa con cada tecla: teclear "/", "c", "o" filtra los
	// candidatos con el ranking de filterCommands (prefijo del nombre primero):
	// queda solo /commit y /review desaparece del popup.
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/")
	view := m.View()
	lineWith(t, view, "/commit")
	lineWith(t, view, "/review")

	m = typeRunes(t, m, "co")
	view = m.View()
	commitLine := lineWith(t, view, "/commit")
	if !strings.Contains(commitLine, "genera un commit") {
		t.Fatalf("linea de /commit = %q, el item filtrado debe conservar su descripcion", commitLine)
	}
	if strings.Contains(view, "/review") {
		t.Fatalf("View() = %q, tras teclear %q el menu NO debe seguir mostrando %q", view, "/co", "/review")
	}
	if got := menuSelectedLine(view); !strings.Contains(got, "/commit") {
		t.Fatalf("linea seleccionada del menu = %q, el unico candidato /commit debe quedar seleccionado", got)
	}
}

func TestModel_SlashMenuIncludesCompactBuiltin(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeRunes(t, m, "/comp")
	line := lineWith(t, m.View(), "/compact")
	if !strings.Contains(line, "Compact conversation context") {
		t.Fatalf("compact line = %q", line)
	}
}

func TestModel_CompactSubmitsWithoutPromptHistoryOrWorkingSpinner(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeRunes(t, m, "/compact")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 1 || fake.sent[0].text != "/compact" {
		t.Fatalf("sent = %+v", fake.sent)
	}
	if len(m.history) != 0 {
		t.Fatalf("history = %v, /compact must not enter prompt history", m.history)
	}
	if m.Working() {
		t.Fatal("Working() = true, compact status must own progress")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input = %q, want cleared", got)
	}
}

func TestModel_CompactStatusDeduplicatesAndResolves(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, CompactionStatusMsg{SessionID: "s1", State: CompactionQueued})
	m = apply(t, m, CompactionStatusMsg{SessionID: "s1", State: CompactionQueued})
	if got := strings.Count(ansi.Strip(m.View()), "Compaction queued"); got != 1 {
		t.Fatalf("queued count = %d, view = %q", got, m.View())
	}
	m = apply(t, m, CompactionStatusMsg{SessionID: "s1", State: CompactionRunning})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Compacting context") || strings.Contains(view, "Compaction queued") {
		t.Fatalf("running view = %q", view)
	}
	m = apply(t, m, EventMsg{SessionID: "s1", Kind: session.KindContextCompacted, Compaction: &session.CompactionCheckpoint{
		Summary: session.StructuredSummary{CurrentGoal: "continue"},
	}})
	view = ansi.Strip(m.View())
	if !strings.Contains(view, "Context compacted") || strings.Contains(view, "Compacting context") {
		t.Fatalf("completed view = %q", view)
	}
}

func TestModel_SeparateDurableCompactionsRemainSeparateEntries(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, EventMsg{SessionID: "s1", Kind: session.KindContextCompacted})
	m = apply(t, m, EventMsg{SessionID: "s1", Message: &session.Message{Role: session.RoleUser, Text: "later work"}})
	m = apply(t, m, EventMsg{SessionID: "s1", Kind: session.KindContextCompacted})

	count := 0
	for _, entry := range m.entries {
		if entry.kind == entryCompaction {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("compaction entries = %d, want 2", count)
	}
}

func TestModel_NewCompactionAfterResolvedNoopCreatesNewEntry(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, CompactionStatusMsg{SessionID: "s1", State: CompactionQueued})
	m = apply(t, m, CompactionStatusMsg{SessionID: "s1", State: CompactionNotNeeded})
	m = apply(t, m, CompactionStatusMsg{SessionID: "s1", State: CompactionQueued})

	var states []string
	for _, entry := range m.entries {
		if entry.kind == entryCompaction {
			states = append(states, entry.text)
		}
	}
	want := []string{"Not enough context to compact", "Compaction queued"}
	if !slices.Equal(states, want) {
		t.Fatalf("compaction states = %v, want %v", states, want)
	}
}

func TestModel_CompactStatusNotNeededAndFailure(t *testing.T) {
	notNeeded := NewModel(nil, "s1", nil)
	notNeeded = apply(t, notNeeded, CompactionStatusMsg{SessionID: "s1", State: CompactionQueued})
	notNeeded = apply(t, notNeeded, CompactionStatusMsg{SessionID: "s1", State: CompactionNotNeeded})
	if view := ansi.Strip(notNeeded.View()); !strings.Contains(view, "Not enough context to compact") {
		t.Fatalf("not-needed view = %q", view)
	}

	failed := NewModel(nil, "s1", nil)
	failed = apply(t, failed, CompactionStatusMsg{SessionID: "s1", State: CompactionRunning})
	failed = apply(t, failed, CompactionStatusMsg{SessionID: "s1", State: CompactionFailed, Err: "provider unavailable"})
	if view := ansi.Strip(failed.View()); !strings.Contains(view, "provider unavailable") || !strings.Contains(view, "[error]") {
		t.Fatalf("failed view = %q", view)
	}
}

func TestModel_CompactStatusForOtherSessionIsIgnored(t *testing.T) {
	m := NewModel(nil, "visible", nil)
	m = apply(t, m, CompactionStatusMsg{SessionID: "other", State: CompactionQueued})
	if view := ansi.Strip(m.View()); strings.Contains(view, "Compaction queued") {
		t.Fatalf("other session status leaked into view: %q", view)
	}
}

func TestModel_CommandMenuClosesOnSpace(t *testing.T) {
	// El primer espacio cierra el menu: lo que sigue al nombre son los args del
	// comando y el popup ya no debe tapar la conversacion.
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/commit")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/commit") {
		t.Fatalf("linea seleccionada del menu = %q, con %q tecleado el menu debe estar abierto sobre /commit", got, "/commit")
	}

	m = typeRunes(t, m, " ")
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, el espacio debe cerrar el menu (lo que sigue son los args)", got)
	}
}

func TestModel_MenuKeysNavigateSelection(t *testing.T) {
	// Con el menu abierto, Up/Down mueven el marcador "❯ " de forma ciclica y
	// quedan capturados por el popup: no scrollean el viewport ni escriben en
	// el input.
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Transcript mas largo que el viewport: la vista sigue la cola (mensaje-29).
	for i := 0; i < 30; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%02d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("mensaje-%02d", i),
		}})
	}

	m = typeRunes(t, m, "/")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/new") {
		t.Fatalf("linea seleccionada del menu = %q, el comando integrado /new debe arrancar seleccionado", got)
	}

	// Down baja a la primera skill.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/commit") {
		t.Fatalf("linea seleccionada del menu = %q, Down debe mover el marcador a la skill /commit", got)
	}

	// Enter sobre una skill conserva el flujo de completar con espacio.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "/commit " {
		t.Fatalf("input.Value() = %q, Enter sobre una skill debe completarla con espacio para argumentos", got)
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, completar una skill debe cerrar el menu", got)
	}
	if got := len(m.agent.(*fakeAgent).sent); got != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, Enter sobre una skill solo debe completarla", got)
	}

	// En un menu fresco, Up desde /new cicla al ultimo item.
	mCycle := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(menuCommands, nil)
	mCycle = apply(t, mCycle, tea.WindowSizeMsg{Width: 80, Height: 24})
	mCycle = typeRunes(t, mCycle, "/")
	mCycle = apply(t, mCycle, tea.KeyMsg{Type: tea.KeyUp})
	if got := menuSelectedLine(mCycle.View()); !strings.Contains(got, "/review") {
		t.Fatalf("linea seleccionada del menu = %q, Up en /new debe ciclar al ultimo item (/review)", got)
	}

	// Down en el ultimo vuelve al comando integrado.
	mCycle = apply(t, mCycle, tea.KeyMsg{Type: tea.KeyDown})
	if got := menuSelectedLine(mCycle.View()); !strings.Contains(got, "/new") {
		t.Fatalf("linea seleccionada del menu = %q, Down en el ultimo item debe ciclar al primero (/new)", got)
	}

	// Las flechas quedaron en el segundo popup: no escriben en el input.
	view := m.View()
	if !strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, con menu abierto Up/Down NO deben scrollear el viewport: la cola (mensaje-29) debe seguir visible", view)
	}
	if got := mCycle.input.Value(); got != "/" {
		t.Fatalf("input.Value() = %q, Up/Down con menu abierto NO deben escribir en el input", got)
	}
}

func TestModel_TabAppliesSelectedCommand(t *testing.T) {
	// Con el menu abierto, Tab aplica la seleccion (espejo de applyCommand en
	// command.ts): reemplaza el token "/co" por "/commit " con el caret tras el
	// espacio, listo para los args. El recomputo ve el espacio y cierra el
	// menu. Tab con menu abierto NO alterna el plan-mode.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithStatus("build", "m").WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/co")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/commit") {
		t.Fatalf("linea seleccionada del menu = %q, con %q tecleado /commit debe estar seleccionado", got, "/co")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	if got := m.input.Value(); got != "/commit " {
		t.Fatalf("input.Value() = %q, Tab debe reemplazar el token por %q (comando + espacio para los args)", got, "/commit ")
	}
	if got := m.input.Position(); got != len("/commit ") {
		t.Fatalf("input.Position() = %d, el caret debe quedar tras el espacio (%d)", got, len("/commit "))
	}
	view := m.View()
	if got := menuSelectedLine(view); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, aplicar el comando debe cerrar el menu (el recomputo ve el espacio)", got)
	}
	if !strings.Contains(view, " m ─╯") || strings.Contains(view, "· plan") {
		t.Fatalf("View() = %q, Tab con menu abierto NO debe alternar el plan-mode: el borde debe seguir mostrando el modelo %q", view, "m")
	}
}

func TestModel_EnterAppliesSelectionInsteadOfSending(t *testing.T) {
	// Con el menu abierto, Enter aplica la seleccion igual que Tab y NO envia
	// nada; el segundo Enter (menu ya cerrado) si envia el texto tal cual.
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/co")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fake.sent) != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, Enter con menu abierto debe aplicar la seleccion, NO enviar", len(fake.sent))
	}
	if got := m.input.Value(); got != "/commit " {
		t.Fatalf("input.Value() = %q, Enter con menu abierto debe aplicar la seleccion (%q)", got, "/commit ")
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, aplicar la seleccion debe cerrar el menu", got)
	}

	// Menu cerrado: el segundo Enter envia el texto tal cual via SendPrompt.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.sent) != 1 {
		t.Fatalf("SendPrompt fue llamado %d veces, con el menu cerrado Enter debe enviar exactamente una vez", len(fake.sent))
	}
	if got := fake.sent[0]; got.sessionID != "s1" || got.text != "/commit " {
		t.Fatalf("SendPrompt(%q, %q), se esperaba SendPrompt(%q, %q): el texto se envia tal cual", got.sessionID, got.text, "s1", "/commit ")
	}
}

func TestModel_EscClosesMenuWithoutStopping(t *testing.T) {
	// Con el menu abierto, Esc cierra el popup SIN detener la corrida y sin
	// tocar el texto del input; teclear otra runa recomputa y reabre el menu.
	// Con menu cerrado, Esc conserva su comportamiento actual (detiene).
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/c")
	if got := menuSelectedLine(m.View()); got == "" {
		t.Fatalf("View() = %q, con %q tecleado el menu debe estar abierto", m.View(), "/c")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, Esc debe cerrar el popup", got)
	}
	if len(fake.stopped) != 0 {
		t.Fatalf("Stop fue llamado %d veces, Esc con menu abierto NO debe detener la corrida", len(fake.stopped))
	}
	if got := m.input.Value(); got != "/c" {
		t.Fatalf("input.Value() = %q, Esc solo cierra el popup: el texto %q debe quedar intacto", got, "/c")
	}

	// Otra runa recomputa el menu desde el token aun vigente: se reabre.
	m = typeRunes(t, m, "o")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/commit") {
		t.Fatalf("linea seleccionada del menu = %q, teclear otra runa debe reabrir el menu sobre /commit", got)
	}

	// Con menu cerrado Esc sigue deteniendo la corrida.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc}) // cierra el popup reabierto
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc}) // menu cerrado: detiene
	if len(fake.stopped) != 1 || fake.stopped[0] != "s1" {
		t.Fatalf("Stop = %v, con menu cerrado Esc debe detener la corrida (Stop(%q) una vez)", fake.stopped, "s1")
	}
}

func TestModel_AtOpensFileMenu(t *testing.T) {
	// Un "@" que inicia palabra abre el @-menu de archivos (espejo de
	// detectMention/filterFiles en mention.ts): el label es la ruta, sin
	// descripcion; el filtro rankea el basename (prefijo antes que subcadena)
	// antes que el match en la ruta. listFiles se llama UNA vez al activarse el
	// token y se cachea mientras siga activo.
	calls := 0
	listFiles := func() ([]string, error) {
		calls++
		return []string{"internal/tui/model.go", "app.go", "README.md"}, nil
	}
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, listFiles)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "hola @")
	view := m.View()
	for _, want := range []string{"internal/tui/model.go", "app.go", "README.md"} {
		lineWith(t, view, want)
	}
	if got := menuSelectedLine(view); !strings.Contains(got, "internal/tui/model.go") {
		t.Fatalf("linea seleccionada del menu = %q, el primer archivo del listado debe arrancar seleccionado", got)
	}

	// "mo" filtra por basename: solo model.go arranca con "mo".
	m = typeRunes(t, m, "mo")
	view = m.View()
	lineWith(t, view, "internal/tui/model.go")
	for _, drop := range []string{"app.go", "README.md"} {
		if strings.Contains(view, drop) {
			t.Fatalf("View() = %q, tras filtrar por %q el menu NO debe seguir mostrando %q", view, "mo", drop)
		}
	}
	if calls != 1 {
		t.Fatalf("listFiles fue llamado %d veces, debe llamarse UNA vez al activarse el token y cachearse mientras siga activo", calls)
	}

	// Con listFiles nil el menu simplemente no abre.
	m2 := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, nil)
	m2 = apply(t, m2, tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 = typeRunes(t, m2, "hola @")
	if got := menuSelectedLine(m2.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, sin listFiles el @-menu no debe abrir", got)
	}

	// Con listFiles fallando el menu tampoco abre.
	m3 := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return nil, fmt.Errorf("rg no disponible")
	})
	m3 = apply(t, m3, tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 = typeRunes(t, m3, "hola @")
	if got := menuSelectedLine(m3.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, con listFiles fallando el @-menu no debe abrir", got)
	}
}

func TestModel_AtInsideWordDoesNotOpenMenu(t *testing.T) {
	// El "@" debe iniciar palabra (inicio del texto o precedido de espacio):
	// un email como a@b NO dispara el @-menu (espejo de detectMention).
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"app.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "a@b")
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, un @ dentro de palabra (email) NO debe abrir el @-menu", got)
	}
}

func TestModel_TabAppliesSelectedMention(t *testing.T) {
	// Con el @-menu abierto, Tab reemplaza el token por "@<ruta> " conservando
	// el texto alrededor (espejo de applyMention: text[:start] + "@<ruta> " +
	// text[end:]) y deja el caret tras el espacio. El recomputo cierra el menu.
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go", "app.go", "README.md"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "hola @mo")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "internal/tui/model.go") {
		t.Fatalf("linea seleccionada del menu = %q, con %q tecleado model.go debe estar seleccionado", got, "@mo")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})

	want := "hola @internal/tui/model.go "
	if got := m.input.Value(); got != want {
		t.Fatalf("input.Value() = %q, Tab debe reemplazar el token por la mencion conservando el texto alrededor (%q)", got, want)
	}
	if got := m.input.Position(); got != len([]rune(want)) {
		t.Fatalf("input.Position() = %d, el caret debe quedar tras el espacio (%d)", got, len([]rune(want)))
	}
	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, aplicar la mencion debe cerrar el menu", got)
	}
}

func TestModel_SlashOpensCommandMenu(t *testing.T) {
	// Con comandos configurados via WithCompletions, teclear "/" como primer
	// caracter del composer abre un popup de menu encima de la caja: una linea
	// por comando con "/<name>" y su descripcion. El primer item arranca
	// seleccionado y se marca con el prefijo "❯ " (los no seleccionados llevan
	// dos espacios de prefijo).
	cmds := []command.Command{
		{Name: "commit", Description: "genera un commit"},
		{Name: "review", Description: "revisa el diff"},
	}
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(cmds, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})

	view := m.View()
	commitLine := lineWith(t, view, "/commit")
	if !strings.Contains(commitLine, "genera un commit") {
		t.Fatalf("linea de /commit = %q, el menu debe mostrar la descripcion %q junto al comando", commitLine, "genera un commit")
	}
	lineWith(t, view, "/review")
	newLine := lineWith(t, view, "/new")
	if plain := ansi.Strip(newLine); !strings.HasPrefix(plain, "❯ ") {
		t.Fatalf("linea de /new sin ANSI = %q, el comando integrado debe arrancar seleccionado con el prefijo %q", plain, "❯ ")
	}
}

func TestModel_CommandMenuAlwaysIncludesModelBuiltin(t *testing.T) {
	commands := make([]command.Command, 10)
	for i := range commands {
		commands[i] = command.Command{Name: fmt.Sprintf("skill-%02d", i), Description: "skill"}
	}
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(commands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeRunes(t, m, "/")
	if view := m.View(); !strings.Contains(view, "/model") {
		t.Fatalf("slash menu must reserve a row for /model even when skills fill the limit:\n%s", view)
	}
}

func TestModel_CommandMenuPrioritizesNewAndEnterCreatesSession(t *testing.T) {
	// /new es un comando integrado, no una skill fuzzy: debe aparecer primero
	// y Enter sobre su seleccion crea y activa la sesion sin insertar espacio.
	fake := &fakeAgent{newSessionID: "s2"}
	m := NewModel(fake, "s1", nil).WithCompletions([]command.Command{
		{Name: "renew", Description: "skill con coincidencia fuzzy"},
	}, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/new") {
		t.Fatalf("linea seleccionada del menu = %q, /new debe ser el comando integrado seleccionado por encima de skills", got)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.sessionID; got != "s2" {
		t.Fatalf("sessionID = %q, Enter sobre /new debe activar la sesion nueva %q", got, "s2")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, ejecutar /new desde el menu debe limpiar el composer sin dejar un espacio", got)
	}
	if got := fake.sent; len(got) != 1 || got[0].text != "/new" {
		t.Fatalf("SendPrompt llamadas = %#v, Enter sobre /new debe ejecutar el comando reservado exactamente una vez", got)
	}
}

func TestModel_NewSessionClearsTokenUsage(t *testing.T) {
	fake := &fakeAgent{newSessionID: "s2"}
	m := NewModel(fake, "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{
		Kind: session.KindStepEnded,
		Usage: &session.Usage{
			InputTokens:  1_234,
			OutputTokens: 345,
		},
	})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "↑ 1.2k ↓ 345") {
		t.Fatalf("View() antes de /new = %q, debe mostrar el uso de la sesion anterior", view)
	}

	m = typeRunes(t, m, "/new")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if view := ansi.Strip(m.View()); strings.Contains(view, "↑") || strings.Contains(view, "↓") {
		t.Fatalf("View() despues de /new = %q, la sesion nueva no debe heredar tokens de subida ni bajada", view)
	}
}

func TestModel_NewSessionClearsLiveTokenEstimates(t *testing.T) {
	fake := &fakeAgent{newSessionID: "s2"}
	m := NewModel(fake, "s1", nil).WithStatus("build", "anthropic/claude-sonnet-4.5")
	m = apply(t, m, EventMsg{
		Kind:  session.KindStepStarted,
		Usage: &session.Usage{InputTokens: 1_200},
	})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: strings.Repeat("a", 900)})

	m = typeRunes(t, m, "/new")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.usage != nil || m.liveUsage || m.outputBytes != 0 || m.reasoningBytes != 0 || m.toolInputBytes != 0 {
		t.Fatalf("estado de uso despues de /new = usage:%+v live:%v bytes:%d/%d/%d, debe arrancar limpio", m.usage, m.liveUsage, m.outputBytes, m.reasoningBytes, m.toolInputBytes)
	}
}

func TestModel_ExactNewEnterBeatsFuzzySkillSelection(t *testing.T) {
	// Aunque una skill fuzzy este seleccionada, escribir exactamente /new y
	// pulsar Enter debe ejecutar el reservado, no completar la skill.
	fake := &fakeAgent{newSessionID: "s2"}
	m := NewModel(fake, "s1", nil).WithCompletions([]command.Command{
		{Name: "renew", Description: "skill con coincidencia fuzzy"},
	}, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/new")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got := m.sessionID; got != "s2" {
		t.Fatalf("sessionID = %q, Enter con /new escrito debe activar la sesion nueva %q aunque haya una skill fuzzy", got, "s2")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, Enter con /new escrito debe ejecutarlo, no completar una skill", got)
	}
}

func TestModel_NewWithTrailingSpaceKeepsComposerForArguments(t *testing.T) {
	// El espacio cierra el menu y desactiva solo el comando reservado: el texto
	// queda intacto para que el usuario pueda continuar escribiendo argumentos.
	fake := &fakeAgent{newSessionID: "s2"}
	m := NewModel(fake, "s1", nil).WithCompletions([]command.Command{
		{Name: "renew", Description: "skill con coincidencia fuzzy"},
	}, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = typeRunes(t, m, "/new ")

	if got := menuSelectedLine(m.View()); got != "" {
		t.Fatalf("linea seleccionada del menu = %q, /new con espacio final debe cerrar el menu", got)
	}
	if got := m.input.Value(); got != "/new " {
		t.Fatalf("input.Value() = %q, /new con espacio final debe conservarse para argumentos", got)
	}
	if got := m.sessionID; got != "s1" {
		t.Fatalf("sessionID = %q, escribir /new con espacio final no debe ejecutar el reservado", got)
	}
	if got := len(fake.sent); got != 0 {
		t.Fatalf("SendPrompt fue llamado %d veces, escribir /new con espacio final no debe ejecutar el reservado", got)
	}
}

func TestModel_MenuLinesTruncateToTerminalWidth(t *testing.T) {
	// Una linea del menu mas ancha que la terminal la envolveria el terminal a
	// dos lineas reales, pero reservedLines solo descuenta UNA por item: el
	// layout se rompe. El menu debe truncar cada linea al ancho de la terminal,
	// como ya hace el resto de la vista (el transcript envuelve con ansi.Wrap,
	// el textinput scrollea horizontal).
	longPath := strings.Repeat("sub/", 30) + "archivo-de-nombre-largo.go"
	listFiles := func() ([]string, error) {
		return []string{longPath}, nil
	}
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, listFiles)
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})

	view := m.View()
	lineWith(t, view, "sub/") // la linea del menu sigue presente, truncada
	assertNoLineWiderThan(t, view, 40)
}

// Contrato del indicador animado: mientras hay una corrida en curso la linea
// de estado muestra un glifo de spinner seguido de " trabajando"; el prefijo
// estatico "... " desaparece. Arrancar la corrida (Enter con texto) devuelve
// un tea.Cmd no nil que bombea la animacion: ejecutarlo produce un mensaje
// que, aplicado a Update, avanza el glifo del spinner (la linea de estado
// cambia) y devuelve a su vez el siguiente cmd del loop.
func TestModel_WorkingIndicatorAnimatesOnTicks(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// El usuario teclea "hola" y pulsa Enter; el cmd del Enter se conserva
	// (el helper apply lo descarta y aqui es el corazon del contrato).
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}

	// a) Arrancar la corrida debe devolver el cmd que bombea la animacion:
	// sin cmd nadie produce ticks y el spinner queda congelado.
	if cmd == nil {
		t.Fatalf("Update(Enter) devolvio cmd nil, arrancar la corrida debe devolver el cmd que bombea la animacion: sin cmd el spinner queda congelado")
	}

	// b) La linea de estado conserva "trabajando" pero sin el marcador
	// estatico viejo "... trabajando": ahora el prefijo es el glifo animado.
	view := m.View()
	if !strings.Contains(view, "trabajando") {
		t.Fatalf("View() = %q, con corrida en curso debe verse la linea de estado con %q", view, "trabajando")
	}
	if strings.Contains(view, "... trabajando") {
		t.Fatalf("View() = %q, NO debe contener el marcador estatico %q: el prefijo fijo se reemplaza por el glifo del spinner", view, "... trabajando")
	}

	// c) Ejecutar el cmd produce el mensaje de tick; aplicarlo a Update debe
	// avanzar el glifo del spinner: la linea de estado cambia.
	before := lineWith(t, view, "trabajando")
	msg := cmd()
	if msg == nil {
		t.Fatalf("cmd() = nil, el cmd de la animacion debe producir un mensaje aplicable a Update")
	}
	updated, tickCmd := m.Update(msg)
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	after := lineWith(t, m.View(), "trabajando")
	if after == before {
		t.Fatalf("linea de estado tras el tick = %q, identica a la previa: el tick debe avanzar el frame del spinner, una linea identica significa animacion congelada", after)
	}

	// d) El loop sigue: el Update del tick debe agendar el proximo tick.
	if tickCmd == nil {
		t.Fatalf("Update(tick) devolvio cmd nil, el loop de animacion debe agendar el proximo tick")
	}
}

// TRIANGULATE: el loop de ticks debe morir cuando la corrida termina. Un case
// de tick que siempre re-agenda sin mirar working deja la TUI despertando para
// siempre: un tick viejo que llega DESPUES de RunDoneMsg no debe re-agendar el
// loop (cmd nil) ni revivir la linea de estado.
func TestModel_SpinnerTickDiesAfterRunDone(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Arranca la corrida y queda un tick en vuelo (el cmd ya produjo su msg).
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(Enter) devolvio cmd nil, arrancar la corrida debe devolver el cmd que bombea la animacion")
	}
	msg := cmd()

	// La corrida termina; recien entonces llega el tick viejo.
	m = apply(t, m, RunDoneMsg{})
	updated, tickCmd := m.Update(msg)
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}

	if tickCmd != nil {
		t.Fatalf("Update(tick) tras RunDoneMsg devolvio cmd no nil, el loop de animacion NO debe re-agendarse cuando la corrida termino: sin este corte la TUI queda despertando para siempre")
	}
	if got := m.View(); strings.Contains(got, "trabajando") {
		t.Fatalf("View() = %q, tras RunDoneMsg el tick viejo NO debe revivir la linea de estado %q", got, "trabajando")
	}
}

// TRIANGULATE: el camino del plan tambien anima. Una implementacion pobre que
// cablee el tick solo en el camino Enter deja el spinner congelado cuando la
// corrida arranca aceptando un plan con 'y': aceptar el plan debe devolver el
// cmd que bombea la animacion y su tick debe avanzar el glifo.
func TestModel_AcceptPlanStartsSpinner(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// El agente presenta un plan asentado (present_plan llamada y exitosa).
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	m = apply(t, m, EventMsg{Kind: session.KindToolSuccess, CallID: "p1"})

	// 'y' acepta el plan: arranca la corrida y debe bombear el spinner.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update('y') devolvio cmd nil, aceptar el plan arranca la corrida y debe devolver el cmd que bombea la animacion: sin cmd el spinner queda congelado en el camino del plan")
	}

	before := lineWith(t, m.View(), "trabajando")
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, batched := range batch {
			if candidate := batched(); candidate != nil {
				if _, ok := candidate.(spinner.TickMsg); ok {
					msg = candidate
					break
				}
			}
		}
	}
	if msg == nil {
		t.Fatalf("cmd() = nil, el cmd de la animacion debe producir un mensaje aplicable a Update")
	}
	m = apply(t, m, msg)
	after := lineWith(t, m.View(), "trabajando")
	if after == before {
		t.Fatalf("linea de estado tras el tick = %q, identica a la previa: el tick del camino del plan debe avanzar el frame del spinner", after)
	}
}

// TRIANGULATE: la animacion no es de un solo uso. Una implementacion pobre con
// estado del loop que no se reinicia (arranca solo en la primera corrida) deja
// el spinner muerto en la segunda: tras RunDoneMsg, un nuevo Enter debe volver
// a devolver el cmd de la animacion y su tick debe avanzar el glifo.
func TestModel_SecondRunRestartsSpinner(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Primera corrida: Enter arranca el loop y RunDoneMsg lo apaga.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	updated, cmd1 := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd1 == nil {
		t.Fatalf("Update(Enter) devolvio cmd nil, arrancar la primera corrida debe devolver el cmd que bombea la animacion")
	}
	m = apply(t, m, RunDoneMsg{})

	// Segunda corrida: el loop debe renacer con el nuevo Enter.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("otra vez")})
	updated, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd2 == nil {
		t.Fatalf("Update(Enter) de la segunda corrida devolvio cmd nil, cada corrida debe reencender la animacion: un loop de un solo uso deja el spinner muerto en la segunda corrida")
	}

	before := lineWith(t, m.View(), "trabajando")
	msg := cmd2()
	if msg == nil {
		t.Fatalf("cmd() = nil, el cmd de la animacion de la segunda corrida debe producir un mensaje aplicable a Update")
	}
	m = apply(t, m, msg)
	after := lineWith(t, m.View(), "trabajando")
	if after == before {
		t.Fatalf("linea de estado tras el tick = %q, identica a la previa: el tick de la segunda corrida debe avanzar el frame del spinner", after)
	}
}

// Contrato del historial de prompts: cada prompt ENVIADO (Enter con texto,
// camino build o plan) se guarda en un historial en memoria de la sesion de
// TUI, en orden de envio. Con el menu de autocompletado CERRADO y sin
// permiso/plan pendientes, la flecha ARRIBA recorre el historial hacia atras
// (el mas reciente primero) poniendo cada prompt en el input; en el tope, otra
// flecha arriba se queda ahi (no cicla ni se vacia). La flecha ABAJO deshace
// hacia adelante y, pasado el mas reciente, deja el input como estaba antes de
// empezar a navegar. Sin historial, la flecha arriba no hace nada.
func TestModel_UpArrowRecallsPromptHistory(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Dos prompts enviados: quedan en el historial en orden de envio.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("primero")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("segundo")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "segundo" {
		t.Fatalf("input.Value() = %q, la flecha arriba debe recuperar el ultimo prompt enviado (%q)", got, "segundo")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "primero" {
		t.Fatalf("input.Value() = %q, la segunda flecha arriba debe retroceder al prompt anterior (%q)", got, "primero")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "primero" {
		t.Fatalf("input.Value() = %q, en el tope del historial otra flecha arriba se queda en %q: no cicla ni se vacia", got, "primero")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.input.Value(); got != "segundo" {
		t.Fatalf("input.Value() = %q, la flecha abajo debe deshacer hacia adelante y volver al prompt mas reciente (%q)", got, "segundo")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, pasado el mas reciente la flecha abajo debe dejar el input como estaba antes de empezar a navegar (vacio tras el Enter)", got)
	}
}

// Con texto ya escrito, Up/Down no deben abrir el historial: el usuario debe
// vaciar el composer antes de explorar prompts anteriores. Una vez dentro del
// historial, Down sigue avanzando y al pasar el mas reciente deja el input
// limpio.
func TestModel_NonEmptyInputBlocksHistoryExploration(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("primero")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("borrador")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.input.Value(); got != "borrador" {
		t.Fatalf("input.Value() = %q, con texto escrito la flecha abajo no debe abrir ni reemplazar con el historial", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "borrador" {
		t.Fatalf("input.Value() = %q, con texto escrito la flecha arriba no debe abrir ni reemplazar con el historial", got)
	}

	// Vaciando el composer se habilita la navegacion.
	m.input.SetValue("")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "primero" {
		t.Fatalf("input.Value() = %q, con el composer vacio la flecha arriba debe recuperar %q", got, "primero")
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, al avanzar despues del prompt mas reciente el composer debe quedar limpio", got)
	}
}

func TestModel_HistoryKeepsOnlyLatestHundredPrompts(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	for i := 1; i <= 102; i++ {
		m = typeRunes(t, m, fmt.Sprintf("prompt-%03d", i))
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	}

	for range 101 {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	}
	if got := m.input.Value(); got != "prompt-003" {
		t.Fatalf("input.Value() = %q, tras 102 envios el historial debe conservar solo los 100 mas recientes y detenerse en %q", got, "prompt-003")
	}
}

// TRIANGULATE: con el menu de autocompletado abierto, Up/Down pertenecen a la
// seleccion del popup, no al historial de prompts. Tumba un handler de
// historial colocado ANTES del gate del menu en handleKey.
func TestModel_MenuOpenKeepsUpDownForSelection(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil).WithCompletions(menuCommands, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Hay historial: sin el gate del menu, la flecha arriba lo recuperaria.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("primero")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	m = typeRunes(t, m, "/")
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/new") {
		t.Fatalf("linea seleccionada del menu = %q, con %q tecleado el menu debe estar abierto sobre /new", got, "/")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "/" {
		t.Fatalf("input.Value() = %q, con menu abierto la flecha arriba NO debe tocar el input: la seleccion del menu es quien navega", got)
	}
	if got := menuSelectedLine(m.View()); !strings.Contains(got, "/review") {
		t.Fatalf("linea seleccionada del menu = %q, con menu abierto la flecha arriba debe mover la seleccion (ciclica al ultimo, /review)", got)
	}
}

// TRIANGULATE: los prompts enviados en plan-mode (camino SendPlanPrompt)
// tambien se apilan en el historial. Tumba una implementacion que apila solo
// en el camino SendPrompt.
func TestModel_HistoryRecordsPlanPrompts(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Tab pasa a plan-mode: Enter envia por SendPlanPrompt.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyTab})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("plan-uno")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(fake.planSent) != 1 {
		t.Fatalf("SendPlanPrompt fue llamado %d veces, Enter en plan-mode debe enviar el prompt exactamente una vez por el camino de plan", len(fake.planSent))
	}
	m = apply(t, m, RunDoneMsg{})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "plan-uno" {
		t.Fatalf("input.Value() = %q, la flecha arriba debe recuperar el prompt de plan enviado (%q): los prompts de plan tambien se apilan en el historial", got, "plan-uno")
	}
}

// TRIANGULATE: Enter con input vacio no envia (cubierto aparte) y tampoco debe
// apilar nada. Tumba una implementacion que apila todos los submits y deja un
// "" colandose en el historial.
func TestModel_EmptySubmitDoesNotPolluteHistory(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("unico")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// Enter con input vacio: no envia y no debe tocar el historial.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "unico" {
		t.Fatalf("input.Value() = %q, la primera flecha arriba debe recuperar el unico prompt enviado (%q), sin un submit vacio colado en el historial", got, "unico")
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "unico" {
		t.Fatalf("input.Value() = %q, en el tope del historial la flecha arriba se queda en %q: el submit vacio no debe haberse apilado", got, "unico")
	}
}

// Contrato del smooth streaming (paridad con el frontend de escritorio,
// frontend/src/lib/reveal.ts): los deltas del assistant se ACUMULAN en la
// entrada pero la vista NO los muestra completos de inmediato. Un loop de
// ticks de reveal (revealTickMsg, analogo al spinner.TickMsg) avanza el texto
// revelado: en cada tick se revelan ~max(base, ceil(backlog/8)) runas, con
// base ~6-7 runas por tick (el ritmo del escritorio: 1 char cada 5ms a ticks
// de ~33ms). Con suficientes ticks el texto completo queda visible.
func TestModel_SmoothRevealsAssistantTextOnTicks(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	text := strings.Repeat("palabra ", 40) + "final-del-texto"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	// (a) El delta NO aparece completo de golpe: la cola del texto todavia no
	// esta revelada justo despues de acumular el delta.
	if got := m.View(); strings.Contains(got, "final-del-texto") {
		t.Fatalf("View() = %q, NO debe contener %q inmediatamente tras el delta: el texto se revela progresivamente con los ticks de reveal, no aparece completo de golpe", got, "final-del-texto")
	}

	// (b) Un tick de reveal avanza el texto visible: un prefijo ya se ve, pero
	// la cola todavia no (revelado progresivo, no todo de golpe).
	m = apply(t, m, revealTickMsg{})
	view := m.View()
	if !strings.Contains(view, "palabra") {
		t.Fatalf("View() = %q, debe contener %q tras un tick de reveal: cada tick revela un tramo del texto acumulado", view, "palabra")
	}
	if strings.Contains(view, "final-del-texto") {
		t.Fatalf("View() = %q, NO debe contener %q tras UN solo tick: un tick revela ~max(base, ceil(backlog/8)) runas, no el texto entero", view, "final-del-texto")
	}

	// (c) Con suficientes ticks el texto completo queda visible.
	for i := 0; i < 200; i++ {
		m = apply(t, m, revealTickMsg{})
		if strings.Contains(m.View(), "final-del-texto") {
			break
		}
	}
	if got := m.View(); !strings.Contains(got, "final-del-texto") {
		t.Fatalf("View() = %q, debe contener %q tras suficientes ticks de reveal: el loop de reveal termina mostrando el texto completo", got, "final-del-texto")
	}
}

// TRIANGULATE: el catch-up acota la latencia. Con paso constante puro (~7
// runas por tick) un delta de ~4000 runas tardaria ~570 ticks (~19 segundos a
// 33ms) en drenarse: el texto visible quedaria eternamente por detras de un
// modelo rapido. El paso proporcional al backlog (ceil(backlog/8)) debe dejar
// el texto completo visible en una cantidad acotada de ticks.
func TestModel_RevealCatchUpDrainsHugeDeltaInBoundedTicks(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// ~4011 runas en un solo delta (modelo rapido volcando texto de golpe).
	text := strings.Repeat("palabra ", 500) + "fin-catchup"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	// El primer tick no lo revela todo: el catch-up acelera el ritmo, no lo
	// convierte en un reveal instantaneo (eso mataria la animacion).
	m = apply(t, m, revealTickMsg{})
	if got := m.View(); strings.Contains(got, "fin-catchup") {
		t.Fatalf("View() = %q, NO debe contener %q tras UN solo tick de un delta de ~4000 runas: el catch-up acota la latencia sin volverse un reveal instantaneo", got, "fin-catchup")
	}

	// A lo sumo 64 ticks en total (~2 segundos a 33ms) dejan visible el texto
	// completo: el paso proporcional drena geometricamente el backlog.
	for i := 0; i < 63 && m.hasBacklog(); i++ {
		m = apply(t, m, revealTickMsg{})
	}
	if got := m.View(); !strings.Contains(got, "fin-catchup") {
		t.Fatalf("View() = %q, debe contener %q tras 64 ticks: el catch-up proporcional al backlog debe drenar un delta enorme en una cantidad acotada de ticks (un paso constante puro tardaria ~570)", got, "fin-catchup")
	}
}

// TRIANGULATE: el swap a markdown espera a que el reveal drene. Una
// implementacion pobre rendiria markdown apenas el bloque se cierra (StepEnded)
// aunque quede backlog: el texto completo flashearia de golpe a mitad de la
// animacion. Cerrado el turno con backlog pendiente la vista debe seguir
// mostrando solo el prefijo revelado, ya rendido como Markdown; recien al
// drenar se muestra el contenido completo.
func TestModel_RevealMarkdownSwapWaitsForDrain(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// ~502 runas: el primer tick revela ~63 (los ** iniciales incluidos) y
	// deja mucha cola sin revelar.
	text := "**fuerte** " + strings.Repeat("relleno ", 60) + "fin-drenado"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	// Un tick antes del cierre: el prefijo revelado ya se rinde como Markdown.
	m = apply(t, m, revealTickMsg{})
	if got := ansi.Strip(m.View()); strings.Contains(got, "**") || !strings.Contains(got, "fuerte") {
		t.Fatalf("View() sin ANSI = %q, debe rendir el Markdown revelado durante streaming", got)
	}

	// El turno se cierra con backlog pendiente.
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})

	view := ansi.Strip(m.View())
	if strings.Contains(view, "fin-drenado") {
		t.Fatalf("View() = %q, NO debe contener %q inmediatamente tras StepEnded: cerrar el turno no debe revelar de golpe la cola pendiente, el reveal sigue su ritmo de ticks", view, "fin-drenado")
	}
	if strings.Contains(view, "**") || !strings.Contains(view, "fuerte") {
		t.Fatalf("View() sin ANSI = %q, debe conservar el Markdown del prefijo revelado tras StepEnded", view)
	}

	// Drenado el backlog, el bloque cerrado se rinde como markdown.
	m = drainReveal(t, m)
	view = ansi.Strip(m.View())
	if strings.Contains(view, "**") {
		t.Fatalf("View() = %q, NO debe contener %q tras drenar: con el bloque cerrado y drenado el enfasis markdown se rinde, no se muestra crudo", view, "**")
	}
	if !strings.Contains(view, "fuerte") {
		t.Fatalf("View() = %q, debe contener %q: rendir el markdown no debe perder el contenido", view, "fuerte")
	}
	if !strings.Contains(view, "fin-drenado") {
		t.Fatalf("View() = %q, debe contener %q: drenar el backlog debe terminar mostrando el texto completo rendido", view, "fin-drenado")
	}
}

// TRIANGULATE: el corte del reveal es por runas, nunca por bytes. Una
// implementacion que corte el prefijo con e.text[:n] parte los caracteres
// multibyte a la mitad: la vista intermedia queda con UTF-8 invalido o con el
// caracter de reemplazo U+FFFD. Tras cada tick intermedio la vista debe ser
// UTF-8 valido y sin U+FFFD; al drenar, el texto multibyte completo intacto.
func TestModel_RevealCutsByRunesNotBytes(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// ~256 runas con acentos ausentes pero kanji y emoji de 3-4 bytes: casi
	// cualquier corte por bytes cae a mitad de un caracter.
	text := strings.Repeat("cancion nunca japon 日本語テキスト 🚀🚀🚀 ", 8)
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	ticks := 0
	for m.hasBacklog() {
		ticks++
		if ticks > 1000 {
			t.Fatalf("el backlog del reveal no se agoto tras 1000 ticks")
		}
		m = apply(t, m, revealTickMsg{})
		view := m.View()
		if !utf8.ValidString(view) {
			t.Fatalf("View() = %q tras el tick %d, no es UTF-8 valido: el corte del reveal debe ser por runas, un corte por bytes parte los caracteres multibyte", view, ticks)
		}
		if strings.ContainsRune(view, '�') {
			t.Fatalf("View() = %q tras el tick %d, contiene el caracter de reemplazo U+FFFD: un caracter multibyte quedo partido por un corte por bytes", view, ticks)
		}
	}
	// El drenado debe haber pasado por cortes intermedios: un reveal
	// instantaneo pasaria las aserciones de arriba sin ejercitar nada.
	if ticks < 2 {
		t.Fatalf("el backlog (%d runas) se dreno en %d tick(s), debe drenar en varios ticks para ejercitar los cortes intermedios", utf8.RuneCountInString(text), ticks)
	}
	plain := ansi.Strip(m.View())
	if got, want := strings.Count(plain, "日本語テキスト"), 8; got != want {
		t.Fatalf("View() sin ANSI = %q, contiene %d ocurrencias de texto japonés, se esperaban %d tras drenar", plain, got, want)
	}
	if got, want := strings.Count(plain, "🚀🚀🚀"), 8; got != want {
		t.Fatalf("View() sin ANSI = %q, contiene %d grupos de emoji, se esperaban %d tras drenar", plain, got, want)
	}
}

// TRIANGULATE (espejo del ciclo de vida del spinner): el loop de ticks del
// reveal nace con el primer delta que deja backlog, no se duplica con deltas
// siguientes, se rearma mientras quede backlog, muere al drenarse y renace con
// un delta nuevo. Con canal de eventos nil la bomba es nil y el cmd devuelto
// por Update es SOLO el tick del reveal: cada transicion es asertable directa.
func TestModel_RevealTickLoopLifecycle(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// 200 runas por delta: ningun tick individual drena el backlog completo.
	delta := strings.Repeat("palabra ", 25)
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})

	// a) El primer delta con backlog arranca el loop: el cmd produce el tick.
	updated, cmd := m.Update(EventMsg{Kind: session.KindTextDelta, Text: delta})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(delta) devolvio cmd nil, el primer delta con backlog debe devolver el cmd que arranca el loop de reveal: sin cmd nadie produce ticks y el texto queda congelado")
	}
	msg := cmd()
	if _, ok := msg.(revealTickMsg); !ok {
		t.Fatalf("cmd() = %T, el cmd del arranque del loop debe producir un revealTickMsg", msg)
	}

	// b) Un segundo delta con el loop ya corriendo NO duplica la cadena de
	// ticks: dos cadenas doblarian el ritmo del reveal.
	updated, cmd = m.Update(EventMsg{Kind: session.KindTextDelta, Text: delta})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd != nil {
		t.Fatalf("Update(delta) con el loop de reveal corriendo devolvio cmd no nil, un segundo delta NO debe arrancar otra cadena de ticks: cadenas duplicadas aceleran el reveal con cada delta")
	}

	// c) Un tick con backlog restante se rearma: el cmd produce el proximo tick.
	updated, cmd = m.Update(revealTickMsg{})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(tick) con backlog restante devolvio cmd nil, el loop debe reagendar el proximo tick mientras quede texto sin revelar")
	}
	msg = cmd()
	if _, ok := msg.(revealTickMsg); !ok {
		t.Fatalf("cmd() = %T, el cmd del rearme del loop debe producir el proximo revealTickMsg", msg)
	}

	// d) Con el backlog drenado el siguiente tick no se reagenda: el loop muere.
	m = drainReveal(t, m)
	updated, cmd = m.Update(revealTickMsg{})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd != nil {
		t.Fatalf("Update(tick) sin backlog devolvio cmd no nil, el loop de reveal debe morir al drenarse: sin este corte la TUI queda despertando cada 33ms para siempre")
	}

	// e) Un delta nuevo tras el drenado reenciende el loop.
	updated, cmd = m.Update(EventMsg{Kind: session.KindTextDelta, Text: delta})
	if _, ok = updated.(Model); !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(delta) tras drenar devolvio cmd nil, un delta nuevo debe reencender el loop de reveal: un loop de un solo uso deja el texto congelado en el segundo turno de streaming")
	}
	msg = cmd()
	if _, ok := msg.(revealTickMsg); !ok {
		t.Fatalf("cmd() = %T, el cmd del reencendido del loop debe producir un revealTickMsg", msg)
	}
}

// TRIANGULATE: el loop de reveal NO esta atado a working como el del spinner.
// Una implementacion que copie el corte del caso spinner.TickMsg (!working =>
// cmd nil) deja el texto congelado a medio revelar cuando la corrida termina
// antes de drenar el backlog: los ticks posteriores a RunDoneMsg deben seguir
// revelando hasta drenar.
func TestModel_RevealSurvivesRunDone(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)

	// Corrida real en curso: working encendido via Enter con texto.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	text := strings.Repeat("palabra ", 40) + "fin-tras-run-done"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	// La corrida termina con backlog pendiente: working se apaga pero la cola
	// del texto sigue sin revelar.
	m = apply(t, m, RunDoneMsg{})
	if m.Working() {
		t.Fatalf("Working() = true, RunDoneMsg debe apagar el estado de trabajo")
	}
	if got := m.View(); strings.Contains(got, "fin-tras-run-done") {
		t.Fatalf("View() = %q, RunDoneMsg NO debe revelar la cola de golpe: el reveal sigue su ritmo de ticks tambien al terminar la corrida", got)
	}

	// El tick posterior al fin de la corrida sigue avanzando y reagendando.
	updated, cmd := m.Update(revealTickMsg{})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update devolvio %T, se esperaba tui.Model", updated)
	}
	if cmd == nil {
		t.Fatalf("Update(tick) tras RunDoneMsg devolvio cmd nil con backlog pendiente, el loop de reveal no debe morir con working: debe seguir drenando el texto restante")
	}

	m = drainReveal(t, m)
	if got := m.View(); !strings.Contains(got, "fin-tras-run-done") {
		t.Fatalf("View() = %q, debe contener %q tras drenar: los ticks posteriores a RunDoneMsg deben terminar mostrando el texto completo", got, "fin-tras-run-done")
	}
}

// Contrato del toggle de pensamiento (tecla Shift+Tab, ver handleKey y
// toggleThinking): un pensamiento asentado (cerrado y con el reveal drenado)
// colapsa a la linea de resumen "[penso <dur>]"; Shift+Tab lo expande al
// texto completo y un segundo Shift+Tab lo colapsa de nuevo. El hint " ⇧Tab"
// acompana al resumen colapsado para descubrir la tecla.
func TestModel_ShiftTabExpandsAndCollapsesSettledThinking(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	text := "razon-1\nrazon-2\nrazon-3"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)

	// Asentado: colapsado por defecto.
	view := m.View()
	if !strings.Contains(view, "[penso ") {
		t.Fatalf("View() = %q, el pensamiento asentado debe colapsar a %q", view, "[penso ")
	}
	if !strings.Contains(view, " ⇧Tab") {
		t.Fatalf("View() = %q, el resumen colapsado debe llevar el hint %q para descubrir el toggle", view, " ⇧Tab")
	}
	if strings.Contains(view, "razon-2") {
		t.Fatalf("View() = %q, el pensamiento colapsado NO debe mostrar el texto completo", view)
	}

	// Shift+Tab expande.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	for _, want := range []string{"[penso ", "razon-1", "razon-2", "razon-3"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, tras Shift+Tab el pensamiento expandido debe mostrar %q", view, want)
		}
	}

	// Shift+Tab colapsa de nuevo.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	if !strings.Contains(view, "[penso ") {
		t.Fatalf("View() = %q, el segundo Shift+Tab debe volver al resumen colapsado %q", view, "[penso ")
	}
	if strings.Contains(view, "razon-2") {
		t.Fatalf("View() = %q, el segundo Shift+Tab debe colapsar el texto otra vez", view)
	}
}

func TestModel_SettledThinkingSummaryAlignsWithAssistantContent(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "respuesta-asistente"})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "respuesta-asistente"},
	})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pensamiento-asentado"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pensamiento-asentado"})
	m = drainReveal(t, m)

	assistantLine := ansi.Strip(lineWith(t, m.View(), "respuesta-asistente"))
	thinkingLine := ansi.Strip(lineWith(t, m.View(), "[penso "))
	assistantIndent := assistantLine[:len(assistantLine)-len(strings.TrimLeft(assistantLine, " "))]

	if got, want := assistantIndent, "  "; got != want {
		t.Fatalf("prefijo del contenido assistant = %q, want %q", got, want)
	}
	if !strings.HasPrefix(thinkingLine, assistantIndent) {
		t.Fatalf("linea del resumen de pensamiento = %q, debe alinearse con el contenido assistant %q", thinkingLine, assistantLine)
	}
}

func TestModel_LiveThinkingHeaderAlignsWithAssistantContent(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "respuesta-asistente"})
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "respuesta-asistente"},
	})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pensamiento-vivo"})
	m = drainReveal(t, m)

	assistantLine := ansi.Strip(lineWith(t, m.View(), "respuesta-asistente"))
	thinkingLine := ansi.Strip(lineWith(t, m.View(), "[pensando]"))
	assistantIndent := assistantLine[:len(assistantLine)-len(strings.TrimLeft(assistantLine, " "))]

	if got, want := assistantIndent, "  "; got != want {
		t.Fatalf("prefijo del contenido assistant = %q, want %q", got, want)
	}
	if !strings.HasPrefix(thinkingLine, assistantIndent) {
		t.Fatalf("linea del encabezado de pensamiento vivo = %q, debe alinearse con el contenido assistant %q", thinkingLine, assistantLine)
	}
}

func TestModel_LiveThinkingHeaderKeepsChatIndentWhenSettledWithExplorer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "respuesta-visible"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: "respuesta-visible"},
	})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "preview-sin-indentacion-adicional"})
	m = drainReveal(t, m)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after opening explorer = %v, want %v", got, want)
	}

	liveView := ansi.Strip(m.View())
	assistantLine := lineWith(t, liveView, "respuesta-visible")
	liveHeaderLine := lineWith(t, liveView, "[pensando]")
	chatContentColumn := strings.Index(assistantLine, "respuesta-visible")
	liveHeaderColumn := strings.Index(liveHeaderLine, "[pensando]")
	if chatContentColumn < 0 || liveHeaderColumn < 0 {
		t.Fatalf("View() sin ANSI = %q, deben verse el contenido assistant y el encabezado vivo", liveView)
	}
	if got, want := liveHeaderColumn, chatContentColumn; got != want {
		t.Fatalf("columna de [pensando] = %d, want %d: debe alinearse con el contenido visible del chat", got, want)
	}
	if got, want := liveHeaderLine[:liveHeaderColumn], "│  "; !strings.HasSuffix(got, want) {
		t.Fatalf("prefijo antes de [pensando] = %q, want suffix %q: el panel chat debe aportar exactamente dos espacios", got, want)
	}

	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "preview-sin-indentacion-adicional"})
	m = drainReveal(t, m)

	settledView := ansi.Strip(m.View())
	settledHeaderLine := lineWith(t, settledView, "[penso ")
	settledHeaderColumn := strings.Index(settledHeaderLine, "[penso ")
	if got, want := settledHeaderColumn, liveHeaderColumn; got != want {
		t.Fatalf("columna del resumen asentado = %d, want %d: ReasoningEnded no debe desplazar horizontalmente el encabezado", got, want)
	}
}

func TestModel_ShiftTabExpandsSettledThinkingWithExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pensamiento-completo"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pensamiento-completo"})
	m = drainReveal(t, m)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after opening explorer = %v, want %v", got, want)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := ansi.Strip(m.View()); !strings.Contains(got, "pensamiento-completo") {
		t.Fatalf("View() sin ANSI = %q, Shift+Tab con foco del explorador debe expandir el pensamiento asentado", got)
	}
}

func TestModel_ShiftTabExpandsSettledThinkingWithViewerFocusWithoutScrollingFile(t *testing.T) {
	file := strings.Repeat("archivo\n", 80)
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"archivo.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"archivo.txt": []byte(file)}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pensamiento-del-visor"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pensamiento-del-visor"})
	m = drainReveal(t, m)

	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus after opening file = %v, want %v", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	viewerPath, viewerOffset := m.viewer.path, m.viewer.offset
	if viewerOffset == 0 {
		t.Fatal("viewer precondition: PgDown must move the long file")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if !m.entries[0].expanded {
		t.Fatal("Shift+Tab con foco del visor debe expandir el pensamiento asentado")
	}
	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus after Shift+Tab = %v, want %v", got, want)
	}
	if got, want := m.viewer.path, viewerPath; got != want {
		t.Fatalf("viewer.path after Shift+Tab = %q, want %q", got, want)
	}
	if got, want := m.viewer.offset, viewerOffset; got != want {
		t.Fatalf("viewer.offset after Shift+Tab = %d, want %d: global thinking toggle must not scroll the file", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if got := ansi.Strip(m.View()); !strings.Contains(got, "pensamiento-del-visor") {
		t.Fatalf("View() sin ANSI = %q, el pensamiento expandido debe verse al cerrar el visor", got)
	}
}

// Contrato: el toggle es inerte mientras el pensamiento sigue en vivo (preview
// de las ultimas lineas, no el texto completo). Un Shift+Tab durante el stream
// no debe fijar expanded ni revelar el texto entero antes de tiempo.
func TestModel_ShiftTabIsInertWhileThinkingLive(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "vivo-1\nvivo-2\nvivo-3\nvivo-4\nvivo-5"})
	m = drainReveal(t, m)

	// El preview en vivo muestra la cabecera y las ultimas lineas, no el
	// resumen ni el texto completo expandido.
	view := m.View()
	if !strings.Contains(view, "[pensando]") {
		t.Fatalf("View() = %q, en vivo debe mostrar %q", view, "[pensando]")
	}
	if strings.Contains(view, "[penso ") {
		t.Fatalf("View() = %q, en vivo NO debe mostrar el resumen colapsado %q", view, "[penso ")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	if strings.Contains(view, "[penso ") {
		t.Fatalf("View() = %q, Shift+Tab durante el stream vivo no debe colapsar todavia", view)
	}
	if strings.Contains(view, "vivo-1") {
		t.Fatalf("View() = %q, Shift+Tab durante el stream vivo no debe expandir el texto entero", view)
	}
}

func TestModel_ShiftTabIsInertForLiveThinkingWithExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"archivo.txt", "otro.txt"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "vivo-uno\nvivo-dos\nvivo-tres\nvivo-cuatro\nvivo-cinco"})
	m = drainReveal(t, m)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after opening explorer = %v, want %v", got, want)
	}

	beforeView, beforeCursor := ansi.Strip(m.View()), m.treeCursor
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	afterView := ansi.Strip(m.View())
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after Shift+Tab = %v, want %v", got, want)
	}
	if got, want := m.treeCursor, beforeCursor; got != want {
		t.Fatalf("treeCursor after Shift+Tab = %d, want %d", got, want)
	}
	if got, want := afterView, beforeView; got != want {
		t.Fatalf("View() after Shift+Tab = %q, want unchanged live-thinking preview %q", got, want)
	}
	if strings.Contains(afterView, "vivo-uno") || strings.Contains(afterView, "[penso ") {
		t.Fatalf("View() = %q, Shift+Tab con pensamiento vivo no debe revelar todo ni mostrar el resumen asentado", afterView)
	}
}

// Contrato: Shift+Tab alterna TODOS los bloques de pensamiento asentados a la
// vez. Con dos pensamientos terminados, un solo golpe los expande ambos y un
// segundo los colapsa ambos.
func TestModel_ShiftTabTogglesAllSettledThinkingBlocks(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	for _, tag := range []string{"primero", "segundo"} {
		m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
		m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: tag + "-a\n" + tag + "-b"})
		m = drainReveal(t, m)
		m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: tag + "-a\n" + tag + "-b"})
		m = drainReveal(t, m)
	}

	// Ambos colapsados por defecto: dos resumenes, sin texto.
	view := m.View()
	if n := strings.Count(view, "[penso "); n != 2 {
		t.Fatalf("View() = %q, dos pensamientos asentados deben colapsar a dos resumenes %q (n=%d)", view, "[penso ", n)
	}
	if strings.Contains(view, "primero-a") || strings.Contains(view, "segundo-a") {
		t.Fatalf("View() = %q, ambos colapsados no deben mostrar texto", view)
	}

	// Un Shift+Tab expande ambos.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	if !strings.Contains(view, "primero-a") || !strings.Contains(view, "segundo-a") {
		t.Fatalf("View() = %q, un solo Shift+Tab debe expandir AMBOS pensamientos", view)
	}
	if n := strings.Count(view, "[penso "); n != 2 {
		t.Fatalf("View() = %q, tras expandir siguen habiendo dos resumenes de cabecera (n=%d)", view, n)
	}

	// Un segundo Shift+Tab colapsa ambos.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	view = m.View()
	if strings.Contains(view, "primero-a") || strings.Contains(view, "segundo-a") {
		t.Fatalf("View() = %q, el segundo Shift+Tab debe colapsar AMBOS", view)
	}
}

// Contrato del toggle por clic (ver toggleThinkingAt y el caso tea.MouseMsg de
// Update): un clic izquierdo sobre la linea del resumen de un pensamiento
// asentado lo expande al texto completo, igual que Shift+Tab pero sobre el
// bloque concreto bajo el cursor. El clic se mapea a la entrada via entryLines,
// asi que la fila clicada debe caer sobre la linea del resumen.
func TestModel_ClickExpandsSettledThinking(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	text := "razon-1\nrazon-2\nrazon-3"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)

	// Localiza la fila del resumen "[penso " en el contenido del viewport.
	lines := m.entryLines()
	summaryRow := -1
	for i, l := range lines {
		if strings.Contains(l.line, "[penso ") {
			summaryRow = i
			break
		}
	}
	if summaryRow < 0 {
		t.Fatalf("entryLines() no contiene el resumen %q: %v", "[penso ", lines)
	}
	// La fila en pantalla es la del contenido menos el desplazamiento visible,
	// mas la fila de la top bar que corre el cuerpo una fila hacia abajo.
	clickY := topBarHeight + summaryRow - m.viewport.YOffset
	if clickY < topBarHeight {
		t.Fatalf("summaryRow=%d YOffset=%d, el resumen no esta visible para clicar", summaryRow, m.viewport.YOffset)
	}

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: clickY})
	view := m.View()
	for _, want := range []string{"[penso ", "razon-1", "razon-2", "razon-3"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, el clic sobre el resumen debe expandir el pensamiento mostrando %q", view, want)
		}
	}
}

// TRIANGULATE de la condicion compartida compactActivityJoin (su razon de
// ser): con un grupo compacto de DOS tools antes de un pensamiento colapsado,
// la fila del resumen se localiza en lo que el usuario VE (View), no en
// entryLines, y el clic sobre esa fila debe expandir el pensamiento. Si
// entryLines siguiera emitiendo el separador entre actividades, su numeracion
// divergeria de la del viewport y el clic caeria en la linea equivocada.
func TestModel_ClickTargetingStaysAlignedWithCompactGroups(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})

	// Grupo compacto: dos tools asentadas contiguas (sin linea en blanco).
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, ToolCallID: "c1"},
	})
	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "grep", Input: json.RawMessage(`{"pattern":"foo"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c2", ToolName: "grep",
		Message: &session.Message{ID: "c2", Role: session.RoleTool, ToolCallID: "c2"},
	})

	text := "razon-1\nrazon-2"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)
	target := len(m.entries) - 1

	// La fila se busca en la pantalla real: el transcript corto se muestra
	// desde arriba (sin scroll) y el viewport abre la vista, asi que la fila Y
	// de la pantalla es la linea absoluta del contenido.
	if m.viewport.YOffset != 0 {
		t.Fatalf("viewport.YOffset = %d, want 0: el transcript corto se muestra desde arriba", m.viewport.YOffset)
	}
	summaryY := lineIndexWith(t, ansi.Strip(m.View()), "[penso ")

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: summaryY})
	if !m.entries[target].expanded {
		t.Fatal("el clic sobre la fila visible del resumen debe expandir el pensamiento pese al grupo compacto de tools encima")
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"razon-1", "razon-2"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() sin ANSI = %q, el pensamiento expandido debe mostrar %q", view, want)
		}
	}
}

func TestModel_ClickExpandsSettledThinkingInChatWithExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 120, Height: 32})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pensamiento-del-chat"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pensamiento-del-chat"})
	m = drainReveal(t, m)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after opening explorer = %v, want %v", got, want)
	}

	view := ansi.Strip(m.View())
	summaryX, summaryY := -1, -1
	for y, line := range strings.Split(view, "\n") {
		if x := strings.Index(line, "[penso "); x >= 0 {
			summaryX, summaryY = x, y
			break
		}
	}
	if summaryX < m.treePanelWidth() || summaryY < 0 {
		t.Fatalf("View() = %q, no contiene el resumen de pensamiento en el panel chat", view)
	}

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      summaryX,
		Y:      summaryY,
	})
	if got := ansi.Strip(m.View()); !strings.Contains(got, "pensamiento-del-chat") {
		t.Fatalf("View() sin ANSI = %q, el clic sobre el resumen del chat debe expandir el pensamiento", got)
	}
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after chat thinking click = %v, want %v: el clic no debe cambiar el foco del explorador", got, want)
	}
}

func TestModel_ClickExpandsScrolledThinkingInChatWithExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 120, Height: 18})
	for i := 0; i < 8; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{
			ID:   fmt.Sprintf("u%d", i),
			Role: session.RoleUser,
			Text: fmt.Sprintf("relleno-%d", i),
		}})
	}
	for _, text := range []string{"pensamiento-primero", "pensamiento-objetivo"} {
		m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
		m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
		m = drainReveal(t, m)
		m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
		m = drainReveal(t, m)
	}
	target := len(m.entries) - 1

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after opening explorer = %v, want %v", got, want)
	}
	summaryRow := -1
	for row, line := range m.entryLines() {
		if line.idx == target && strings.Contains(line.line, "[penso ") {
			summaryRow = row
			break
		}
	}
	if summaryRow < 0 {
		t.Fatal("entryLines() no contiene el resumen del pensamiento objetivo")
	}
	m.viewport.SetYOffset(max(summaryRow-m.viewport.Height+1, 1))
	if m.viewport.YOffset <= 0 {
		t.Fatalf("viewport.YOffset = %d, want > 0 for a scrolled transcript", m.viewport.YOffset)
	}
	clickY := topBarHeight + 2 + summaryRow - m.viewport.YOffset
	if clickY < topBarHeight+2 || clickY >= m.viewport.Height+topBarHeight+2 {
		t.Fatalf("target summary row=%d, offset=%d, clickY=%d, viewport height=%d: el resumen objetivo debe estar visible dentro del transcript derecho", summaryRow, m.viewport.YOffset, clickY, m.viewport.Height)
	}

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() + 2,
		Y:      clickY,
	})
	if !m.entries[target].expanded {
		t.Fatal("el clic sobre el resumen desplazado debe expandir el bloque de pensamiento objetivo")
	}
	if m.entries[target-1].expanded {
		t.Fatal("el clic sobre el resumen desplazado no debe expandir otro bloque de pensamiento")
	}
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after scrolled chat thinking click = %v, want %v", got, want)
	}
}

func TestModel_ClickChatPanelTitleIsInertWithExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pensamiento-del-titulo"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pensamiento-del-titulo"})
	m = drainReveal(t, m)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after opening explorer = %v, want %v", got, want)
	}

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() + 2,
		Y:      0,
	})
	if m.entries[0].expanded {
		t.Fatal("el clic sobre el titulo del panel derecho no debe alternar pensamientos")
	}
	if got, want := m.focus, chatFocus; got != want {
		t.Fatalf("focus after chat panel title click = %v, want %v: el clic derecho fuera del transcript debe enfocar el chat", got, want)
	}
}

// Contrato: un clic sobre el texto de un pensamiento YA expandido lo colapsa
// de nuevo (toggle de ida y vuelta sobre el mismo bloque).
func TestModel_ClickCollapsesExpandedThinking(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	text := "razon-1\nrazon-2"
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
	m = drainReveal(t, m)

	// Expandir primero con Shift+Tab.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := m.View(); !strings.Contains(got, "razon-1") {
		t.Fatalf("View() = %q, precondicion: Shift+Tab debe expandir", got)
	}

	// Clic sobre la primera linea del texto expandido (la cabecera "[penso ").
	lines := m.entryLines()
	headerRow := -1
	for i, l := range lines {
		if strings.Contains(l.line, "[penso ") {
			headerRow = i
			break
		}
	}
	clickY := topBarHeight + headerRow - m.viewport.YOffset
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: clickY})
	view := m.View()
	if strings.Contains(view, "razon-1") {
		t.Fatalf("View() = %q, el clic sobre el bloque expandido debe colapsarlo", view)
	}
	if !strings.Contains(view, "[penso ") {
		t.Fatalf("View() = %q, tras colapsar debe volver el resumen %q", view, "[penso ")
	}
}

// Contrato: un clic izquierdo sobre una linea que NO es de un pensamiento
// asentado (una linea vacia de separacion o el texto de un mensaje user) es
// inerte: no expande nada ni cambia la vista.
func TestModel_ClickOutsideThinkingIsInert(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"}})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "penso-a\npenso-b"})
	m = drainReveal(t, m)
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "penso-a\npenso-b"})
	m = drainReveal(t, m)

	before := m.View()
	// Clic sobre la linea del mensaje user (primera entrada, fila 0).
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: 0})
	if got := m.View(); got != before {
		t.Fatalf("View() cambio tras clic fuera del pensamiento:\nantes = %q\ndespues = %q, el clic solo alterna bloques de pensamiento asentados", before, got)
	}
	if strings.Contains(m.View(), "penso-a") {
		t.Fatalf("View() = %q, el clic fuera del pensamiento no debe expandirlo", m.View())
	}
}

func TestModel_LeaderSpaceE_OpensTree(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go", "go.mod"}, nil
	})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q after leader Space, want empty", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	if !m.treeOpen {
		t.Fatal("Space then e must open the file tree")
	}
	if got := m.View(); !strings.Contains(got, "explorer") {
		t.Fatalf("View() = %q, open tree must render explorer title", got)
	}
}

func TestModel_LeaderSpaceE_TogglesClosed(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"go.mod"}, nil
	})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	if m.treeOpen {
		t.Fatal("second Space+e must close the file tree")
	}
}

func TestModel_KeyRunesBatch_LeaderSpaceEOpensTree(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"go.mod", "internal/tui/model.go"}, nil
	})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" e")})

	if !m.treeOpen {
		t.Fatal(`single KeyRunes batch " e" must open the file tree`)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, leader batch must not insert into composer", got)
	}
	if got := m.View(); !strings.Contains(got, "explorer") {
		t.Fatalf("View() = %q, open tree must render explorer title", got)
	}
}

func TestModel_KeyRunesBatch_LeaderSpaceEParity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		batch    string
		wantOpen bool
	}{
		{name: "two pairs close", batch: " e e", wantOpen: false},
		{name: "three pairs open", batch: " e e e", wantOpen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
				return []string{"go.mod", "internal/tui/model.go"}, nil
			})

			m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.batch)})

			if got := m.treeOpen; got != tc.wantOpen {
				t.Fatalf("treeOpen = %v, want %v after batch %q", got, tc.wantOpen, tc.batch)
			}
			if got := m.input.Value(); got != "" {
				t.Fatalf("input.Value() = %q, repeated leader pairs must not insert into composer", got)
			}
		})
	}
}

func TestModel_KeyRunesBatch_NormalTextInsertsIntoComposer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola mundo")})

	if got, want := m.input.Value(), "hola mundo"; got != want {
		t.Fatalf("input.Value() = %q, want %q: normal text batch must preserve every rune in order", got, want)
	}
	if m.treeOpen || m.leaderPending {
		t.Fatalf("treeOpen=%v leaderPending=%v, normal text batch must not trigger leader state", m.treeOpen, m.leaderPending)
	}
}

func TestModel_TreeKeys_NavigateAndOpenFileViewer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) {
			return []string{"internal/tui/model.go", "go.mod"}, nil
		}).
		WithFileReader(viewerReader(map[string][]byte{"internal/tui/model.go": []byte("package tui\n")}))
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	// Directories sort before files. Expand internal, move to internal/tui,
	// expand it, move to model.go and select the file.
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune{'j'}},
		{Type: tea.KeyRunes, Runes: []rune{'l'}},
		{Type: tea.KeyDown},
		{Type: tea.KeyEnter},
	} {
		m = apply(t, m, msg)
	}

	if !m.treeOpen || !m.viewer.active() {
		t.Fatal("selecting a file must open the viewer and keep the tree open")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, opening a file must not insert a mention", got)
	}
}

func TestModel_TreeOpen_CapturesKeyboard(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"go.mod"}, nil
	})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, tree keyboard must not feed textinput", got)
	}
}

func TestModel_TreeNavigationScrollsSelectedRowIntoView(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{
			"file-00.go",
			"file-01.go",
			"file-02.go",
			"file-03.go",
			"file-04.go",
		}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 8})
	m = m.toggleTree()

	for range 3 {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}

	if got := m.View(); strings.Contains(got, "file-00.go") {
		t.Fatalf("View() = %q, rows above the viewport must scroll away with the selection", got)
	}
}

func TestModel_TreeNavigationScrollsBackAtTopAndAfterResize(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{
			"file-00.go",
			"file-01.go",
			"file-02.go",
			"file-03.go",
			"file-04.go",
		}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 8 + topBarHeight})
	m = m.toggleTree()

	for range 4 {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}
	if got, want := m.treeOffset, 1; got != want {
		t.Fatalf("treeOffset at bottom = %d, want %d", got, want)
	}

	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 6 + topBarHeight})
	if got, want := m.treeOffset, 3; got != want {
		t.Fatalf("treeOffset after shrinking = %d, want %d", got, want)
	}

	for range 4 {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	}
	if got, want := m.treeOffset, 0; got != want {
		t.Fatalf("treeOffset after returning to top = %d, want %d", got, want)
	}
	if got := m.View(); !strings.Contains(got, "file-00.go") {
		t.Fatalf("View() = %q, first row must be visible after moving to top", got)
	}
}

func TestModel_TreeMouseWheelScrollsSelectionWithoutMovingTranscript(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{
			"file-00.go",
			"file-01.go",
			"file-02.go",
			"file-03.go",
			"file-04.go",
		}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 6 + topBarHeight})
	m = m.toggleTree()
	for i := 0; i < 20; i++ {
		m.entries = append(m.entries, entry{kind: entryUser, text: fmt.Sprintf("message-%d", i)})
	}
	m = m.syncViewport()
	m.viewport.SetYOffset(2)

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelDown,
		X:      0,
		Y:      3,
	})

	if got, want := m.treeCursor, 3; got != want {
		t.Fatalf("treeCursor = %d, want %d", got, want)
	}
	if got, want := m.treeOffset, 2; got != want {
		t.Fatalf("treeOffset = %d, want %d", got, want)
	}
	if got, want := m.viewport.YOffset, 2; got != want {
		t.Fatalf("viewport.YOffset = %d, want %d", got, want)
	}
}

func TestModel_ViewerFocusWheelAtPointerOriginScrollsSplitExplorer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) {
			return []string{"file-00.go", "file-01.go", "file-02.go", "file-03.go", "file-04.go"}, nil
		}).
		WithFileReader(viewerReader(map[string][]byte{
			"file-00.go": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n"),
		}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus before wheel = %v, want %v", got, want)
	}
	treeCursor, viewerOffset := m.treeCursor, m.viewer.offset

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelDown,
		X:      0,
		Y:      0,
	})

	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus after wheel = %v, want %v: wheel must not change keyboard focus", got, want)
	}
	if got := m.treeCursor; got <= treeCursor {
		t.Fatalf("treeCursor = %d, want greater than %d: wheel at explorer origin must scroll the tree", got, treeCursor)
	}
	if got, want := m.viewer.offset, viewerOffset; got != want {
		t.Fatalf("viewer offset = %d, want %d: wheel at explorer origin must not scroll the viewer", got, want)
	}
}

func TestModel_NarrowFullWidthViewerWheelNeverScrollsHiddenTree(t *testing.T) {
	const width, height = 20, 8

	for _, x := range []int{0, width - 1} {
		t.Run(fmt.Sprintf("x=%d", x), func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil).
				WithCompletions(nil, func() ([]string, error) {
					return []string{"file-00.go", "file-01.go", "file-02.go", "file-03.go", "file-04.go"}, nil
				}).
				WithFileReader(viewerReader(map[string][]byte{
					"file-00.go": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n"),
				}))
			m = apply(t, m, tea.WindowSizeMsg{Width: width, Height: height})
			m = m.toggleTree()
			m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

			if got, want := m.treePanelWidth(), width; got != want {
				t.Fatalf("treePanelWidth() = %d, want %d: narrow terminal must hide the tree", got, want)
			}
			if view := ansi.Strip(m.View()); !strings.Contains(view, "file-00.go") {
				t.Fatalf("View() = %q, want the active viewer visibly rendered full-width", view)
			}
			if got, want := m.focus, viewerFocus; got != want {
				t.Fatalf("focus before wheel = %v, want %v", got, want)
			}
			treeCursor, viewerOffset := m.treeCursor, m.viewer.offset

			m = apply(t, m, tea.MouseMsg{
				Action: tea.MouseActionPress,
				Button: tea.MouseButtonWheelDown,
				X:      x,
				Y:      topBarHeight,
			})

			if got, want := m.focus, viewerFocus; got != want {
				t.Fatalf("focus after wheel = %v, want %v: wheel must not change keyboard focus", got, want)
			}
			if got := m.viewer.offset; got <= viewerOffset {
				t.Fatalf("viewer offset = %d, want greater than %d: wheel over full-width viewer must scroll the viewer", got, viewerOffset)
			}
			if got, want := m.treeCursor, treeCursor; got != want {
				t.Fatalf("treeCursor = %d, want %d: hidden tree must not receive wheel events", got, want)
			}
		})
	}
}

func TestModel_TreeMouseClickOpensFileFromAnyColumnInRow(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"hello.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"hello.go": []byte("package main\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() - 1,
		Y:      3 + topBarHeight,
	})

	if !m.viewer.active() {
		t.Fatal("clicking anywhere on a file row must open the file viewer")
	}
	if got, want := m.viewer.path, "hello.go"; got != want {
		t.Fatalf("viewer.path = %q, want %q", got, want)
	}
}

func TestModel_TreeMouseClickReplacesOpenFileViewer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"first.go", "second.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{
			"first.go":  []byte("package first\n"),
			"second.go": []byte("package second\n"),
		}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() - 1, Y: 3 + topBarHeight})

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() - 1, Y: 4 + topBarHeight})

	if got, want := m.viewer.path, "second.go"; got != want {
		t.Fatalf("viewer.path = %q, want %q", got, want)
	}
}

func TestModel_TreeMouseClickFolderRowTogglesExpansion(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() - 1,
		Y:      3 + topBarHeight,
	})

	if !m.tree.expanded["internal"] {
		t.Fatal("clicking anywhere on a folder row must toggle its expansion")
	}
	if got := len(m.tree.visibleRows()); got != 2 {
		t.Fatalf("visible rows = %d, want 2", got)
	}
}

func TestModel_LeaderTimeoutCancelsWithoutInput(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil)
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, leaderTimeoutMsg{})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	if m.treeOpen {
		t.Fatal("leader timeout must not open tree")
	}
	if got := m.input.Value(); got != "x" {
		t.Fatalf("input.Value() = %q, after timeout the next key must reach input", got)
	}
}

func TestModel_TreeListErrorRendersWithoutPanic(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return nil, fmt.Errorf("workspace unavailable")
	})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	if got := m.View(); !strings.Contains(got, "workspace unavailable") {
		t.Fatalf("View() = %q, tree error must be visible", got)
	}
}

func TestModel_TreeKeys_HCollapsesThenMovesToParent(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if got := m.tree.visibleRows()[m.treeCursor].node.path; got != "internal/tui" {
		t.Fatalf("selected path after h from file = %q, want parent internal/tui", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if m.tree.expanded["internal/tui"] {
		t.Fatal("h on expanded directory must collapse it")
	}
}

func TestModel_TreeKeys_EscapeAndQCloseWithoutInsert(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
	} {
		t.Run(key.String(), func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
				return []string{"go.mod"}, nil
			})
			m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
			m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
			m = apply(t, m, key)

			if m.treeOpen {
				t.Fatal("close key must close tree")
			}
			if got := m.input.Value(); got != "" {
				t.Fatalf("input.Value() = %q, closing tree must not insert", got)
			}
		})
	}
}

func TestModel_TreeLeaderDoesNotInterceptPendingGates(t *testing.T) {
	permission := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"go.mod"}, nil
	})
	permission = apply(t, permission, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	permission = apply(t, permission, tea.KeyMsg{Type: tea.KeySpace})
	permission = apply(t, permission, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if permission.treeOpen || permission.leaderPending {
		t.Fatal("pending permission must keep leader and tree inactive")
	}

	plan := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"go.mod"}, nil
	})
	plan.entries = append(plan.entries, entry{kind: entryPlanApproval})
	plan = apply(t, plan, tea.KeyMsg{Type: tea.KeySpace})
	plan = apply(t, plan, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if plan.treeOpen || plan.leaderPending {
		t.Fatal("pending plan must keep leader and tree inactive")
	}
}

func TestModel_TreeViewHandlesNarrowTerminal(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go"}, nil
	})
	m = apply(t, m, tea.WindowSizeMsg{Width: 12, Height: 4})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	if got := m.View(); !strings.Contains(got, "explorer") {
		t.Fatalf("View() = %q, narrow terminal must still render explorer without panic", got)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if width := ansi.StringWidth(line); width > 12 {
			t.Fatalf("narrow View line width = %d, want <= 12: %q", width, line)
		}
	}
}

func TestModel_TreeSelectionPreservesExistingComposer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) {
			return []string{"go.mod"}, nil
		}).
		WithFileReader(viewerReader(map[string][]byte{"go.mod": []byte("module atenea\n")}))
	m.input.SetValue("revisa")
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got, want := m.input.Value(), "revisa"; got != want {
		t.Fatalf("input.Value() = %q, want %q", got, want)
	}
	if !m.viewer.active() || !m.treeOpen {
		t.Fatal("selection must open viewer while preserving tree")
	}
}

func viewerReader(files map[string][]byte) FileReader {
	return func(path string) ([]byte, error) {
		content, ok := files[path]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return content, nil
	}
}

func TestModel_TreeEnterFileOpensViewerWithoutMention(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"go.mod"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"go.mod": []byte("module atenea\n")}))
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.viewer.active() || !m.treeOpen {
		t.Fatalf("viewer active=%v treeOpen=%v", m.viewer.active(), m.treeOpen)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input = %q, must not contain a mention", got)
	}
}

func TestModel_FileViewerEscapePreservesExplorerCursor(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"a.go", "b.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"b.go": []byte("package b\n")}))
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	cursor, offset := m.treeCursor, m.treeOffset
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewer.active() || !m.treeOpen || m.treeCursor != cursor || m.treeOffset != offset {
		t.Fatal("Esc must preserve explorer state")
	}
}

func TestModel_FileViewerScrollCapturesKeysButPermissionWins(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 5 + topBarHeight})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.viewer.offset == 0 {
		t.Fatal("Down must scroll viewer")
	}
	m = apply(t, m, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash"})
	before := m.viewer.offset
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.viewer.offset != before {
		t.Fatal("permission must capture key")
	}
}

func TestModel_FileViewerMouseWheelScrollsFileWithoutMovingHiddenTranscript(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 5 + topBarHeight})
	for i := 0; i < 20; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{ID: fmt.Sprintf("u%d", i), Role: session.RoleUser, Text: fmt.Sprintf("message-%d", i)}})
	}
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	beforeOffset, beforeTranscriptOffset := m.viewer.offset, m.viewport.YOffset
	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelUp,
		X:      m.treePanelWidth() + 2,
		Y:      topBarHeight,
	})
	if m.viewer.offset >= beforeOffset {
		t.Fatalf("wheel up viewer offset = %d, want less than %d", m.viewer.offset, beforeOffset)
	}
	if m.viewport.YOffset != beforeTranscriptOffset {
		t.Fatalf("hidden transcript offset = %d, want %d", m.viewport.YOffset, beforeTranscriptOffset)
	}
}

func TestModel_FileViewerTrackpadScrollKeepsTabbedRowsWithinLayout(t *testing.T) {
	content := strings.Repeat("\tfield string // comment that must not wrap the terminal row\n", 12)
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"tabs.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"tabs.go": []byte(content)}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 6 + topBarHeight})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	for range 12 {
		m = apply(t, m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
			X:      m.treePanelWidth() + 2,
			Y:      topBarHeight,
		})
	}
	if got, want := m.viewer.offset, 10; got != want {
		t.Fatalf("viewer offset after continuous wheel events = %d, want %d", got, want)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "\t") {
			t.Fatalf("View() retains terminal tab: %q", line)
		}
		if width := ansi.StringWidth(line); width > 80 {
			t.Fatalf("View() overflows terminal width %d: %q", width, line)
		}
	}
}

func TestModel_FileViewerHeightMatchesRenderedLayout(t *testing.T) {
	for _, test := range []struct {
		name     string
		width    int
		height   int
		openTree bool
		wantRows int
	}{
		{name: "full screen", width: 12, height: 24 + topBarHeight, openTree: true, wantRows: 23},
		{name: "split panels", width: 80, height: 24 + topBarHeight, openTree: true, wantRows: 20},
		{name: "without explorer", width: 80, height: 24 + topBarHeight, wantRows: 23},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil)
			m = apply(t, m, tea.WindowSizeMsg{Width: test.width, Height: test.height})
			if test.openTree {
				m = m.toggleTree()
			}
			if got := m.fileViewerHeight(); got != test.wantRows {
				t.Fatalf("fileViewerHeight() = %d, want %d", got, test.wantRows)
			}
		})
	}
}

func TestModel_FileViewerMouseClickDoesNotToggleHiddenThinking(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"one.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"one.txt": []byte("one\n")}))
	m.entries = []entry{{kind: entryReasoning, text: "hidden thought", revealed: len("hidden thought"), duration: time.Second}}
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: 0})
	if m.entries[0].expanded {
		t.Fatal("clicking the viewer must not toggle hidden transcript thinking")
	}
}

func TestModel_FileViewerPreservesTranscriptPositionAcrossIncomingEvents(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"one.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"one.txt": []byte("one\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
	for i := 0; i < 20; i++ {
		m = apply(t, m, EventMsg{Message: &session.Message{ID: fmt.Sprintf("u%d", i), Role: session.RoleUser, Text: fmt.Sprintf("message-%d", i)}})
	}
	m = apply(t, m, wheelUp)
	beforeOffset := m.viewport.YOffset
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "later", Role: session.RoleUser, Text: "later message"}})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewport.YOffset != beforeOffset {
		t.Fatalf("transcript offset after viewer = %d, want %d", m.viewport.YOffset, beforeOffset)
	}
}

func TestModel_TreeEnterFileShowsReadFailure(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"gone.go"}, nil }).
		WithFileReader(viewerReader(nil))
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.View(); !strings.Contains(got, "no se puede abrir gone.go") {
		t.Fatalf("View() = %q", got)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewer.active() || !m.treeOpen {
		t.Fatal("Esc must return to chat while preserving explorer")
	}
}

func TestModel_FileViewerReplacesChatWithHeaderAndGutter(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"main.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"main.go": []byte("package main\nfunc main() {}\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 10})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	view := ansi.Strip(m.View())
	for _, want := range []string{"explorer", "main.go · 1-2/2", "1", "package main", "2", "func main() {}"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q: %q", want, view)
		}
	}
	if strings.Contains(view, "build ·") {
		t.Fatalf("composer status rendered: %q", view)
	}
}

func TestModel_FileViewerNarrowTerminalNeverOverflows(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"long.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"long.go": []byte("package extremelylongpackagename\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 12, Height: 6})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "long.go ·") || !strings.Contains(view, "package") {
		t.Fatalf("narrow terminal must show the active file viewer: %q", view)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if ansi.StringWidth(line) > 12 {
			t.Fatalf("overflow: %q", line)
		}
	}
}

func TestModel_FileViewerResizeBetweenSplitAndFullScreenKeepsScroll(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 5})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	beforeOffset := m.viewer.offset
	m = apply(t, m, tea.WindowSizeMsg{Width: 12, Height: 4})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "many.txt ·") || strings.Contains(view, "explorer") {
		t.Fatalf("narrow viewer must fill the screen: %q", view)
	}
	if m.viewer.offset != beforeOffset {
		t.Fatalf("viewer offset after narrow resize = %d, want %d", m.viewer.offset, beforeOffset)
	}
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 5})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "explorer") || !strings.Contains(view, "many.txt ·") {
		t.Fatalf("wide viewer must restore split layout: %q", view)
	}
}

func TestModel_ResizeToFullWidthTreeNormalizesFocusAndKeepsItAfterSplitReturns(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: topBarHeight})
	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus before narrow resize = %v, want %v", got, want)
	}

	m = apply(t, m, tea.WindowSizeMsg{Width: 12, Height: 4})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus in full-width tree = %v, want %v: the tree is the only visible focus target", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got, want := m.treeCursor, 0; got != want {
		t.Fatalf("treeCursor after Down in full-width tree = %d, want %d: narrow-layout keys must not keep scrolling the hidden viewer", got, want)
	}

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after split layout returns = %v, want %v: resize normalization must retain the only visible panel's focus", got, want)
	}
}

func TestModel_SplitFocusChromeMarksExactlyOneActivePanel(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"main.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"main.go": []byte("package main\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()

	view := ansi.Strip(m.View())
	if got, want := strings.Count(view, "*"), 1; got != want {
		t.Fatalf("split explorer view has %d active markers, want %d: %q", got, want, view)
	}
	if !strings.Contains(view, "explorer *") || strings.Contains(view, "chat *") {
		t.Fatalf("split explorer view must mark only explorer active: %q", view)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: topBarHeight})
	view = ansi.Strip(m.View())
	if got, want := strings.Count(view, "*"), 1; got != want {
		t.Fatalf("split viewer view has %d active markers, want %d: %q", got, want, view)
	}
	if !strings.Contains(view, "viewer *") || strings.Contains(view, "explorer *") {
		t.Fatalf("split viewer view must mark only viewer active: %q", view)
	}
}

func TestModel_ClickChatMarksOnlyChatActiveWithoutOverflow(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"main.go"}, nil })
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() + 2,
		Y:      10,
	})

	view := ansi.Strip(m.View())
	if got, want := strings.Count(view, "*"), 1; got != want {
		t.Fatalf("split chat view has %d active markers, want %d: %q", got, want, view)
	}
	if !strings.Contains(view, "chat *") || strings.Contains(view, "explorer *") || strings.Contains(view, "viewer *") {
		t.Fatalf("split chat view must mark only chat active: %q", view)
	}
	assertNoLineWiderThan(t, m.View(), 80)
}

func TestModel_NarrowViewerFocusChromePreservesHeaderAndGutterWithoutOverflow(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"long-file-name.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"long-file-name.go": []byte("package main\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 12, Height: 6})
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	view := ansi.Strip(m.View())
	for _, want := range []string{"long-file", "1", "package"} {
		if !strings.Contains(view, want) {
			t.Fatalf("narrow viewer view must retain %q: %q", want, view)
		}
	}
	assertNoLineWiderThan(t, m.View(), 12)
}

func TestModel_ClickTreeFocusesExplorerAndCapturesKeys(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"first.go", "second.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{
			"first.go":  []byte("package first\n"),
			"second.go": []byte("package second\n"),
		}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 3})

	beforeCursor := m.treeCursor
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 0})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after explorer click = %v, want %v", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})

	if got, want := m.treeCursor, beforeCursor+1; got != want {
		t.Fatalf("treeCursor = %d, want %d: clicking explorer must route tree keys to it", got, want)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input.Value() = %q, explorer keys must not reach the composer", got)
	}
}

func TestModel_ComposerCursorStartsBlinking(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m.input.Cursor.BlinkSpeed = time.Millisecond

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() = nil, want composer cursor blink command")
	}
	if m.input.Cursor.Blink {
		t.Fatal("composer cursor starts hidden, want it visible while chat is focused")
	}
	blinkMsg := cmd()
	updated, next := m.Update(blinkMsg)
	m = updated.(Model)
	if next == nil {
		t.Fatal("initial cursor blink message did not schedule the next blink")
	}
	m = apply(t, m, next())
	if !m.input.Cursor.Blink {
		t.Fatal("composer cursor did not toggle after its blink interval")
	}
}

func TestModel_ComposerCursorFollowsPanelFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"one.go"}, nil })
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if m.input.Focused() {
		t.Fatal("composer remains focused while explorer owns keyboard input")
	}

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() + 2,
		Y:      10,
	})
	if !m.input.Focused() {
		t.Fatal("composer remains blurred after chat regains keyboard focus")
	}
}

func TestModel_ComposerCursorHidesWhileInputGateIsPending(t *testing.T) {
	tests := []struct {
		name    string
		entry   entry
		resolve tea.Msg
	}{
		{
			name:    "permission",
			entry:   entry{kind: entryPermission, sessionID: "s1", callID: "c1"},
			resolve: EventMsg{Kind: session.KindToolFailed, CallID: "c1"},
		},
		{
			name:    "plan approval",
			entry:   entry{kind: entryPlanApproval},
			resolve: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := NewModel(&fakeAgent{}, "s1", nil)
			m.entries = append(m.entries, test.entry)
			m = apply(t, m, struct{}{})
			if m.input.Focused() {
				t.Fatal("composer remains focused while another input gate owns the keyboard")
			}

			m = apply(t, m, test.resolve)
			if !m.input.Focused() {
				t.Fatal("composer remains blurred after the input gate is resolved")
			}
		})
	}
}

func TestModel_ComposerCursorFollowsTerminalFocus(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, tea.BlurMsg{})
	if m.input.Focused() {
		t.Fatal("composer remains focused after the terminal window loses focus")
	}

	m = apply(t, m, tea.FocusMsg{})
	if !m.input.Focused() {
		t.Fatal("composer remains blurred after the terminal window regains focus")
	}
}

func TestModel_TerminalRefocusPreservesExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"one.go"}, nil })
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = apply(t, m, tea.BlurMsg{})
	m = apply(t, m, tea.FocusMsg{})

	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("panel focus after terminal refocus = %v, want %v", got, want)
	}
	if m.input.Focused() {
		t.Fatal("terminal refocus steals keyboard focus from explorer")
	}

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() + 2,
		Y:      10,
	})
	if !m.input.Focused() {
		t.Fatal("composer remains blurred after terminal and chat both regain focus")
	}
}

func TestModel_ClickChatFocusesComposer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"one.go"}, nil })
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()

	m = apply(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      m.treePanelWidth() + 2,
		Y:      10,
	})
	if got, want := m.focus, chatFocus; got != want {
		t.Fatalf("focus after chat click = %v, want %v", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola")})

	if got, want := m.input.Value(), "hola"; got != want {
		t.Fatalf("input.Value() = %q, want %q: clicking chat must route typed runes to the composer", got, want)
	}
}

func TestModel_ClickViewerFocusesFileScroll(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 5 + topBarHeight})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 3 + topBarHeight})

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: 4 + topBarHeight})
	if got, want := m.focus, chatFocus; got != want {
		t.Fatalf("focus after chat click = %v, want %v", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if got := m.viewer.offset; got != 0 {
		t.Fatalf("viewer offset = %d after chat focus, want 0: chat keys must not scroll the file", got)
	}

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: topBarHeight})
	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus after viewer click = %v, want %v", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if got := m.viewer.offset; got == 0 {
		t.Fatal("clicking the viewer must route scroll keys to the file")
	}
}

func TestModel_ClickChatAfterOpeningViewerRoutesMouseWheelToTranscript(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 5 + topBarHeight})
	m = m.toggleTree()
	for i := 0; i < 20; i++ {
		m.entries = append(m.entries, entry{kind: entryUser, text: fmt.Sprintf("message-%d", i)})
	}
	m = m.syncViewport()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp, X: m.treePanelWidth() + 2, Y: 4 + topBarHeight})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 3 + topBarHeight})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: topBarHeight})
	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus after viewer click = %v, want %v", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyPgDown})

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: 4 + topBarHeight})
	if got, want := m.focus, chatFocus; got != want {
		t.Fatalf("focus after chat click = %v, want %v", got, want)
	}
	viewerOffset := m.viewer.offset
	transcriptOffset := m.viewport.YOffset

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: m.treePanelWidth() + 2, Y: 4 + topBarHeight})

	if got, want := m.viewer.offset, viewerOffset; got != want {
		t.Fatalf("viewer offset = %d, want %d: wheel after chat click must not scroll the file", got, want)
	}
	if got := m.viewport.YOffset; got <= transcriptOffset {
		t.Fatalf("transcript offset = %d, want greater than %d: wheel after chat click must scroll chat transcript", got, transcriptOffset)
	}
}

func TestModel_MouseWheelRoutesByPointerWithoutChangingKeyboardFocus(t *testing.T) {
	t.Run("narrow split chat scrolls from its leftmost visible column while explorer keeps keyboard focus", func(t *testing.T) {
		const width, height = 24, 4
		m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
			return []string{"file.go"}, nil
		})
		m = apply(t, m, tea.WindowSizeMsg{Width: width, Height: height})
		m = m.toggleTree()
		for i := 0; i < 20; i++ {
			m.entries = append(m.entries, entry{kind: entryUser, text: fmt.Sprintf("message-%d", i)})
		}
		m = m.syncViewport()
		m.viewport.SetYOffset(0)

		if !m.chatPanelVisible() {
			t.Fatal("narrow split layout must keep a visible chat panel")
		}
		if got, want := m.chatContentWidth(), 1; got != want {
			t.Fatalf("chatContentWidth() = %d, want %d: test must exercise the one-cell-wide chat panel", got, want)
		}
		if got, want := m.focus, explorerFocus; got != want {
			t.Fatalf("focus before wheel = %v, want %v", got, want)
		}
		transcriptOffset := m.viewport.YOffset

		m = apply(t, m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
			X:      m.treePanelWidth() + 1,
			Y:      1,
		})

		if got, want := m.focus, explorerFocus; got != want {
			t.Fatalf("focus after narrow chat wheel = %v, want %v: wheel must not change keyboard focus", got, want)
		}
		if got := m.viewport.YOffset; got <= transcriptOffset {
			t.Fatalf("transcript offset = %d, want greater than %d: wheel over the visible narrow chat panel must scroll chat", got, transcriptOffset)
		}
	})

	t.Run("right transcript scrolls while viewer keeps keyboard focus", func(t *testing.T) {
		m := NewModel(&fakeAgent{}, "s1", nil).
			WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
			WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n")}))
		m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
		m = m.toggleTree()
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		for i := 0; i < 20; i++ {
			m.entries = append(m.entries, entry{kind: entryUser, text: fmt.Sprintf("message-%d", i)})
		}
		m = m.syncViewport()
		m.viewport.SetYOffset(0)

		if got, want := m.focus, viewerFocus; got != want {
			t.Fatalf("focus before wheel = %v, want %v", got, want)
		}
		viewerOffset := m.viewer.offset
		transcriptOffset := m.viewport.YOffset

		m = apply(t, m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
			X:      m.treePanelWidth() + 2,
			Y:      m.fileViewerHeight() + topBarHeight,
		})

		if got, want := m.focus, viewerFocus; got != want {
			t.Fatalf("focus after right transcript wheel = %v, want %v: wheel must not change keyboard focus", got, want)
		}
		if got, want := m.viewer.offset, viewerOffset; got != want {
			t.Fatalf("viewer offset = %d, want %d: wheel over the right transcript must not scroll the focused viewer", got, want)
		}
		if got := m.viewport.YOffset; got <= transcriptOffset {
			t.Fatalf("transcript offset = %d, want greater than %d: wheel over the right transcript must scroll chat", got, transcriptOffset)
		}
	})

	t.Run("right chat scrolls while explorer keeps keyboard focus", func(t *testing.T) {
		const width, height = 80, 8 + topBarHeight
		m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
			return []string{"file-00.go", "file-01.go", "file-02.go", "file-03.go"}, nil
		})
		m = apply(t, m, tea.WindowSizeMsg{Width: width, Height: height})
		m = m.toggleTree()
		for i := 0; i < 20; i++ {
			m.entries = append(m.entries, entry{kind: entryUser, text: fmt.Sprintf("message-%d", i)})
		}
		m = m.syncViewport()
		m.viewport.SetYOffset(0)

		if got, want := m.focus, explorerFocus; got != want {
			t.Fatalf("focus before wheel = %v, want %v", got, want)
		}
		transcriptOffset := m.viewport.YOffset

		m = apply(t, m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
			X:      m.treePanelWidth() + 2,
			Y:      1,
		})

		if got, want := m.focus, explorerFocus; got != want {
			t.Fatalf("focus after right chat wheel = %v, want %v: wheel must not change keyboard focus", got, want)
		}
		if got := m.viewport.YOffset; got <= transcriptOffset {
			t.Fatalf("transcript offset = %d, want greater than %d: wheel over right chat must scroll chat", got, transcriptOffset)
		}
		view := m.View()
		if !strings.Contains(view, "chat") {
			t.Fatalf("View() = %q, split layout must keep the chat panel rendered", view)
		}
		assertNoLineWiderThan(t, view, width)
		if lines := len(strings.Split(view, "\n")); lines > height {
			t.Fatalf("View() has %d lines, want at most terminal height %d: %q", lines, height, view)
		}
	})

	t.Run("right viewer scrolls even while explorer owns keyboard focus", func(t *testing.T) {
		m := NewModel(&fakeAgent{}, "s1", nil).
			WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).
			WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n")}))
		m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 10})
		m = m.toggleTree()
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		m.focus = explorerFocus

		viewerOffset := m.viewer.offset
		m = apply(t, m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
			X:      m.treePanelWidth() + 2,
			Y:      topBarHeight,
		})

		if got, want := m.focus, explorerFocus; got != want {
			t.Fatalf("focus after viewer wheel = %v, want %v: wheel must not change keyboard focus", got, want)
		}
		if got := m.viewer.offset; got <= viewerOffset {
			t.Fatalf("viewer offset = %d, want greater than %d: wheel over viewer must scroll the file even without viewer focus", got, viewerOffset)
		}
	})

	t.Run("explorer scrolls even while viewer owns keyboard focus", func(t *testing.T) {
		m := NewModel(&fakeAgent{}, "s1", nil).
			WithCompletions(nil, func() ([]string, error) {
				return []string{"file-00.go", "file-01.go", "file-02.go", "file-03.go", "file-04.go"}, nil
			}).
			WithFileReader(viewerReader(map[string][]byte{"file-00.go": []byte("package main\n")}))
		m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 8})
		m = m.toggleTree()
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

		if got, want := m.focus, viewerFocus; got != want {
			t.Fatalf("focus before wheel = %v, want %v", got, want)
		}
		treeCursor := m.treeCursor
		m = apply(t, m, tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
			X:      0,
			Y:      3,
		})

		if got, want := m.focus, viewerFocus; got != want {
			t.Fatalf("focus after explorer wheel = %v, want %v: wheel must not change keyboard focus", got, want)
		}
		if got := m.treeCursor; got <= treeCursor {
			t.Fatalf("treeCursor = %d, want greater than %d: wheel over explorer must move the tree even without explorer focus", got, treeCursor)
		}
	})
}

func TestModel_ExplorerSelectionAfterOpeningViewerRoutesTreeKeysAndEsc(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"first.txt", "second.txt", "third.txt"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{
			"first.txt":  []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n"),
			"second.txt": []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n"),
			"third.txt":  []byte("alpha\nbeta\ngamma\ndelta\nepsilon\nzeta\neta\ntheta\niota\n"),
		}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 10})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 3 + topBarHeight})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: topBarHeight})
	if got, want := m.focus, viewerFocus; got != want {
		t.Fatalf("focus after viewer click = %v, want %v", got, want)
	}

	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 4 + topBarHeight})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after explorer file click = %v, want %v", got, want)
	}
	if got, want := m.viewer.path, "second.txt"; got != want {
		t.Fatalf("viewer.path = %q, want %q", got, want)
	}
	viewerOffset := m.viewer.offset

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got, want := m.treeCursor, 2; got != want {
		t.Fatalf("treeCursor after j = %d, want %d: explorer keys must navigate the tree", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if got, want := m.treeCursor, 1; got != want {
		t.Fatalf("treeCursor after k = %d, want %d: explorer keys must navigate the tree", got, want)
	}
	if got, want := m.viewer.offset, viewerOffset; got != want {
		t.Fatalf("viewer offset = %d, want %d: explorer j/k must not scroll the file", got, want)
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.treeOpen {
		t.Fatal("Esc from explorer focus must close the tree")
	}
	if !m.viewer.active() {
		t.Fatal("Esc from explorer focus must not close the viewer")
	}
}

func TestModel_TreeFileClickKeepsExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"first.go", "second.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{
			"first.go":  []byte("package first\n"),
			"second.go": []byte("package second\n"),
		}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 3 + topBarHeight})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 4 + topBarHeight})
	if got, want := m.focus, explorerFocus; got != want {
		t.Fatalf("focus after explorer file click = %v, want %v", got, want)
	}

	if got, want := m.viewer.path, "second.go"; got != want {
		t.Fatalf("viewer.path = %q, want %q", got, want)
	}
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if got, want := m.treeCursor, 0; got != want {
		t.Fatalf("treeCursor = %d, want %d: opening a file from explorer must keep explorer keyboard focus", got, want)
	}
}

func TestModel_ClosingFocusedViewerReturnsChatFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).
		WithCompletions(nil, func() ([]string, error) { return []string{"one.go"}, nil }).
		WithFileReader(viewerReader(map[string][]byte{"one.go": []byte("package one\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 3})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: 0})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("chat")})

	if m.viewer.active() {
		t.Fatal("Esc must close the focused viewer")
	}
	if got, want := m.input.Value(), "chat"; got != want {
		t.Fatalf("input.Value() = %q, want %q: Esc from the focused viewer must return keyboard focus to chat", got, want)
	}
}
