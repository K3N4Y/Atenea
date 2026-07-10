package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"atenea/internal/command"
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
	stopped  []string
	accepted []string
}

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

// lineWith devuelve la primera linea de view que contiene needle, o falla.
func lineWith(t *testing.T, view, needle string) string {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("View() = %q, no contiene ninguna linea con %q", view, needle)
	return ""
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
// que empiezan con un caracter de borde ╭/│/╰) no mide exactamente width
// celdas visibles, o si la vista no contiene ninguna. Mide con
// ansi.StringWidth para ignorar codigos ANSI.
func assertBoxLinesExactWidth(t *testing.T, view string, width int) {
	t.Helper()
	found := false
	for _, line := range strings.Split(view, "\n") {
		for _, prefix := range []string{"╭", "│", "╰"} {
			if strings.HasPrefix(line, prefix) {
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

func TestModel_FoldsStreamingAssistantText(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "Hola "})
	m = drainReveal(t, m)
	if got := m.View(); !strings.Contains(got, "Hola ") {
		t.Fatalf("View() = %q, debe contener %q tras el primer delta", got, "Hola ")
	}

	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "mundo"})
	m = drainReveal(t, m)
	if got := m.View(); !strings.Contains(got, "Hola mundo") {
		t.Fatalf("View() = %q, debe contener %q tras acumular deltas", got, "Hola mundo")
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

func TestModel_LiveAssistantStaysPlainUntilClosed(t *testing.T) {
	// TRIANGULATE: una implementacion pobre rendiria markdown SIEMPRE, tambien
	// sobre el bloque en vivo: el markdown parcial de un stream flickea (un **
	// a medio llegar cambia de sentido con cada delta). Mientras el bloque esta
	// vivo el texto debe verse plano tal cual llega; solo al cerrarse se rinde.
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	text := "esto es **fuerte** en vivo"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})
	m = drainReveal(t, m)

	view := m.View()
	if !strings.Contains(view, "**fuerte**") {
		t.Fatalf("View() = %q, debe contener el marcador crudo %q mientras el bloque esta vivo: el streaming se muestra plano, no se rinde markdown parcial", view, "**fuerte**")
	}

	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})

	view = m.View()
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

	view := m.View()
	if plain := ansi.Strip(view); !strings.Contains(plain, "texto-asentado") {
		t.Fatalf("View() sin ANSI = %q, debe contener %q: quitar el gris del documento no debe perder el contenido del texto", plain, "texto-asentado")
	}
	if strings.Contains(view, "38;5;252") {
		t.Fatalf("View() = %q, NO debe contener la secuencia SGR %q: el texto asentado del assistant se rinde con el color por defecto de la terminal, no con el gris 252 del estilo dark de glamour", view, "38;5;252")
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
	userLine := lineWith(t, view, "hola atenea")
	if !strings.HasPrefix(userLine, "> ") {
		t.Fatalf("linea del usuario = %q, debe llevar el marcador %q para distinguirse del assistant", userLine, "> ")
	}
	assistantLine := lineWith(t, view, "hola humano")
	if strings.HasPrefix(assistantLine, "> ") {
		t.Fatalf("linea del assistant = %q, NO debe llevar el marcador de usuario %q", assistantLine, "> ")
	}
}

func TestModel_RendersToolCallLifecycle(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)})
	if got := m.View(); !strings.Contains(got, "[tool] bash(ls): ejecutando") {
		t.Fatalf("View() = %q, Tool.Called debe mostrar el ToolName con el resumen del Input y estado de ejecucion %q", got, "[tool] bash(ls): ejecutando")
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "archivo.txt",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "archivo.txt", ToolCallID: "c1"},
	})
	if got := m.View(); !strings.Contains(got, "[tool] bash(ls): ok") {
		t.Fatalf("View() = %q, Tool.Success debe asentar la tool como %q", got, "[tool] bash(ls): ok")
	}
	if got := m.View(); strings.Contains(got, "ejecutando") {
		t.Fatalf("View() = %q, la tool asentada no debe seguir mostrandose como en ejecucion", got)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "edit", Input: json.RawMessage(`{"path":"a.go"}`)})
	if got := m.View(); !strings.Contains(got, "[tool] edit(a.go): ejecutando") {
		t.Fatalf("View() = %q, el segundo tool call debe mostrarse en ejecucion con el resumen del Input %q", got, "[tool] edit(a.go): ejecutando")
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c2", ToolName: "edit", Error: "permiso denegado"})
	got := m.View()
	if !strings.Contains(got, "[tool] edit(a.go): error: permiso denegado") {
		t.Fatalf("View() = %q, Tool.Failed debe mostrar el Error de la tool con el resumen del Input", got)
	}
	if !strings.Contains(got, "[tool] bash(ls): ok") {
		t.Fatalf("View() = %q, el fallo de c2 no debe tocar el estado ok de c1", got)
	}
	if strings.Contains(got, "ejecutando") {
		t.Fatalf("View() = %q, no debe quedar ninguna tool en ejecucion", got)
	}
}

// Contrato del render de la tool "skill": no usa el header generico
// `[tool] skill(...)` sino una linea dedicada `[skill] <nombre>: <estado>`,
// donde el nombre es el campo "name" del Input JSON. En exito la linea va SIN
// preview del output: el cuerpo del SKILL.md que viaja en ev.Text es para el
// modelo, no para el transcript.
func TestModel_RendersSkillToolAsSkillLine(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "skill", Input: json.RawMessage(`{"name":"tdd-cycle-evidence"}`)})
	view := m.View()
	if !strings.Contains(view, "[skill] tdd-cycle-evidence: ejecutando") {
		t.Fatalf("View() = %q, la tool skill en ejecucion debe rendirse como linea dedicada %q (nombre = campo name del Input)", view, "[skill] tdd-cycle-evidence: ejecutando")
	}
	if strings.Contains(view, "[tool] skill") {
		t.Fatalf("View() = %q, NO debe contener el header generico %q: la tool skill tiene su linea dedicada [skill]", view, "[tool] skill")
	}

	body := "<skill_content name=\"tdd-cycle-evidence\">\ncuerpo del skill para el modelo\n</skill_content>"
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "skill", Text: body,
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: body, ToolCallID: "c1"},
	})
	view = m.View()
	if !strings.Contains(view, "[skill] tdd-cycle-evidence: ok") {
		t.Fatalf("View() = %q, la tool skill exitosa debe asentarse como %q", view, "[skill] tdd-cycle-evidence: ok")
	}
	if strings.Contains(view, "[tool] skill") {
		t.Fatalf("View() = %q, NO debe contener el header generico %q tras el exito", view, "[tool] skill")
	}
	if strings.Contains(view, "skill_content") {
		t.Fatalf("View() = %q, NO debe contener %q: en exito la linea de skill va sin preview del output, el cuerpo del SKILL.md es para el modelo y no para el transcript", view, "skill_content")
	}
}

func TestModel_SkillToolFailureShowsError(t *testing.T) {
	// TRIANGULATE: una implementacion pobre de renderSkill solo cubre los
	// estados running/ok y ante Tool.Failed cae al header generico [tool] o
	// deja la linea "ejecutando" para siempre. El fallo de la skill (p.ej.
	// nombre inexistente) se asienta en la misma linea dedicada [skill] con el
	// sufijo de error, igual que el resto de tools.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "skill", Input: json.RawMessage(`{"name":"inexistente"}`)})
	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c1", ToolName: "skill", Error: `skill "inexistente" no encontrada`})

	view := m.View()
	if want := `[skill] inexistente: error: skill "inexistente" no encontrada`; !strings.Contains(view, want) {
		t.Fatalf("View() = %q, la skill fallida debe asentarse como %q: la linea dedicada [skill] tambien cubre el estado de error, no solo running/ok", view, want)
	}
	if strings.Contains(view, "ejecutando") {
		t.Fatalf("View() = %q, la skill asentada con error no debe seguir mostrandose como en ejecucion", view)
	}
	if strings.Contains(view, "[tool] skill") {
		t.Fatalf("View() = %q, NO debe contener el header generico %q: el fallo no debe hacer caer a la skill al render generico de tools", view, "[tool] skill")
	}
}

func TestModel_SkillToolWithoutNameRendersBareHeader(t *testing.T) {
	// TRIANGULATE: una implementacion pobre asume que el Input de la skill es
	// JSON valido (panic o basura en el header al parsearlo) o cae al header
	// generico [tool] skill cuando no puede extraer el nombre. Con Input no
	// parseable el header queda "[skill]" pelado: sin nombre, sin parens y sin
	// filtrar el input crudo al transcript.
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "skill", Input: json.RawMessage(`no-es-json`)})

	view := m.View()
	if !strings.Contains(view, "[skill]: ejecutando") {
		t.Fatalf("View() = %q, con Input no parseable la skill debe rendirse con el header pelado %q", view, "[skill]: ejecutando")
	}
	if strings.Contains(view, "[tool] skill") {
		t.Fatalf("View() = %q, NO debe contener el header generico %q: sin nombre parseable la skill sigue en su linea dedicada [skill]", view, "[tool] skill")
	}
	skillLine := lineWith(t, view, "[skill]")
	if strings.Contains(skillLine, "(") {
		t.Fatalf("linea de la skill = %q, NO debe llevar parens: el header pelado no hereda el resumen del Input del render generico", skillLine)
	}
	if strings.Contains(view, "no-es-json") {
		t.Fatalf("View() = %q, NO debe filtrar el Input crudo %q al transcript", view, "no-es-json")
	}
}

// Contrato del detalle de tool calls: el header lleva el resumen del Input
// (`[tool] <name>(<resumen>): <estado>`; con un solo campo string el resumen
// es su valor) y Tool.Success trae el output en ev.Text, que se muestra bajo
// el header con cada linea prefijada `  │ ` hasta 4 lineas; con mas lineas
// aparece una marca final `  … +N lineas`. Con 3 lineas de output caben todas:
// no debe aparecer ninguna marca de truncado.
func TestModel_ToolSuccessShowsOutputPreview(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls -la"}`)})
	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "uno\ndos\ntres",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "uno\ndos\ntres", ToolCallID: "c1"},
	})

	view := m.View()
	if !strings.Contains(view, "bash(ls -la)") {
		t.Fatalf("View() = %q, el header debe llevar el resumen del Input %q: con un solo campo string el resumen es su valor", view, "bash(ls -la)")
	}
	for _, want := range []string{"│ uno", "│ dos", "│ tres"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, debe contener %q: cada linea del output de Tool.Success se muestra bajo el header prefijada con la barra", view, want)
		}
	}
	if strings.Contains(view, "lineas") {
		t.Fatalf("View() = %q, NO debe contener la marca de truncado %q: 3 lineas de output caben en el tope de 4 y se muestran completas", view, "lineas")
	}
}

// Contrato del diff en Tool.Success: cuando el evento trae Diff (edit/write),
// el detalle bajo el header muestra EL DIFF en lugar del preview del output:
// cada linea del diff indentada con dos espacios, las lineas `+` en verde, las
// `-` en rojo y el resto tenue (cada linea un segmento contiguo estilizado).
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
	for _, want := range []string{"  -viejo", "  +nuevo"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, debe contener %q: con Diff en Tool.Success cada linea del diff se muestra bajo el header indentada con dos espacios", view, want)
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
	if !strings.Contains(view, "+2 lineas") {
		t.Fatalf("View() = %q, debe contener la marca %q: las 2 lineas que exceden el tope se resumen", view, "+2 lineas")
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
	if want := `edit({"path":"a.go","texto":"x"})`; !strings.Contains(view, want) {
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
	permLine := lineWith(t, view, "[permiso]")
	for _, want := range []string{"bash", `{"cmd":"rm -rf /tmp/x"}`, "aprobar", "denegar"} {
		if !strings.Contains(permLine, want) {
			t.Fatalf("solicitud pendiente = %q, debe contener %q (ToolName, Input y aprobar/denegar)", permLine, want)
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
	if got := m.View(); strings.Contains(got, "[permiso]") {
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
	if got := m.View(); strings.Contains(got, "[permiso]") {
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
	if !strings.Contains(errLine, "[error]") {
		t.Fatalf("linea del fallo = %q, debe llevar el marcador %q para distinguirse del texto normal", errLine, "[error]")
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

	view := m.View()
	if !strings.Contains(view, "[pensando]\nr2\nr3\nr4\nr5") {
		t.Fatalf("View() = %q, el preview debe ser exactamente las ultimas 4 lineas NO vacias pegadas a la cabecera (%q): ni blancos intercalados ni lineas de contenido perdidas", view, "[pensando]\nr2\nr3\nr4\nr5")
	}
	if strings.Contains(view, "r1") {
		t.Fatalf("View() = %q, %q ya salio de la ventana de 4 lineas no vacias", view, "r1")
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
	if !strings.Contains(view, "Primera respuesta\n\nSegunda respuesta") {
		t.Fatalf("View() = %q, ambos textos deben verse como bloques separados", view)
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
	if got := m.View(); strings.Contains(got, "[error]") {
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
	if !strings.Contains(errLine, "[error]") {
		t.Fatalf("linea del fallo = %q, debe llevar el marcador %q", errLine, "[error]")
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
	msg := cmd()
	if got, ok := msg.(EventMsg); !ok || got.Kind != first.Kind {
		t.Fatalf("cmd() = %#v, se esperaba el primer EventMsg %#v", msg, first)
	}

	// Consumir un evento rearma la bomba: el nuevo cmd entrega el segundo msg.
	updated, cmd2 := m.Update(msg)
	m, ok := updated.(Model)
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

	// Canal nil (tests del fold): no hay bomba que armar.
	if cmd := NewModel(nil, "s1", nil).Init(); cmd != nil {
		t.Fatalf("Init() = %#v con canal nil, se esperaba nil", cmd)
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
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

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
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

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

func TestModel_NewEventRefollowsTailWhileScrolledUp(t *testing.T) {
	// TRIANGULATE: pin del comportamiento v1 documentado en Update: aunque el
	// usuario haya scrolleado hacia atras con la rueda, cada evento nuevo
	// re-sigue la cola via GotoBottom en syncViewport.
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
	if got := m.View(); strings.Contains(got, "mensaje-29") {
		t.Fatalf("View() = %q, tras rueda arriba la cola %q NO debe seguir visible", got, "mensaje-29")
	}

	// Llega un evento nuevo: la vista vuelve a seguir la cola.
	m = apply(t, m, EventMsg{Message: &session.Message{
		ID:   "u30",
		Role: session.RoleUser,
		Text: "mensaje-30",
	}})
	if got := m.View(); !strings.Contains(got, "mensaje-30") {
		t.Fatalf("View() = %q, un evento nuevo debe re-seguir la cola: %q debe estar visible aunque el usuario haya scrolleado hacia atras", got, "mensaje-30")
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
	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	// Muchas mas entradas de las que caben en 10 lineas.
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
	if lines := strings.Count(view, "\n") + 1; lines > 10 {
		t.Fatalf("View() tiene %d lineas, la linea de estado NO debe romper el alto acotado (<= 10)", lines)
	}
	if !strings.Contains(view, "mensaje-29") {
		t.Fatalf("View() = %q, la vista debe seguir la cola (%q visible) aun con la linea de estado", view, "mensaje-29")
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

	view := m.View()
	if !strings.Contains(view, "sufijo-final") {
		t.Fatalf("View() = %q, el final del token %q debe estar visible: un token sin espacios mas largo que el ancho debe partirse en varias lineas, no truncarse", view, "sufijo-final")
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

func TestModel_ComposerFooterShowsAgentAndModel(t *testing.T) {
	// El composer debe mostrar, en una linea de pie tenue DEBAJO del input,
	// el agente activo y el modelo en uso (estilo Claude Code). La info entra
	// una sola vez al construir el Model via WithStatus; la TUI no la cambia.
	m := NewModel(nil, "s1", nil).WithStatus("build", "openrouter/free")

	// Sin tamano de terminal conocido (fallback sin viewport) el pie ya se ve.
	view := m.View()
	for _, want := range []string{"build", "openrouter/free"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, debe contener %q en el pie del composer (agente y modelo)", view, want)
		}
	}
	if promptAt, footerAt := strings.Index(view, inputPrompt), strings.Index(view, "openrouter/free"); promptAt >= footerAt {
		t.Fatalf("View() = %q, el pie (openrouter/free en %d) debe aparecer DESPUES del input (%q en %d)", view, footerAt, inputPrompt, promptAt)
	}

	// Con tamano de terminal conocido (viewport activo) el pie sigue visible.
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	view = m.View()
	for _, want := range []string{"build", "openrouter/free"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, tras WindowSizeMsg debe seguir conteniendo %q en el pie del composer", view, want)
		}
	}
	if promptAt, footerAt := strings.Index(view, inputPrompt), strings.Index(view, "openrouter/free"); promptAt >= footerAt {
		t.Fatalf("View() = %q, tras WindowSizeMsg el pie (openrouter/free en %d) debe aparecer DESPUES del input (%q en %d)", view, footerAt, inputPrompt, promptAt)
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
		switch {
		case strings.HasPrefix(line, "╭"):
			topAt = i
		case strings.HasPrefix(line, "╰"):
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

func TestModel_ViewFitsHeightWithBoxFooterAndIndicator(t *testing.T) {
	// TRIANGULATE: con la caja (3 lineas), el pie de status y el indicador de
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
	for _, want := range []string{"mensaje-29", "trabajando", "build", "openrouter/free"} {
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
		if strings.HasPrefix(line, "│") {
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
	if !strings.Contains(view, "plan · openrouter/free") {
		t.Fatalf("View() = %q, tras Tab el pie del composer debe mostrar %q", view, "plan · openrouter/free")
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
	if !strings.Contains(view, "build · m") {
		t.Fatalf("View() = %q, tras Tab Tab el pie del composer debe volver a mostrar %q", view, "build · m")
	}
	if strings.Contains(view, "plan ·") {
		t.Fatalf("View() = %q, tras Tab Tab el pie NO debe seguir mostrando %q", view, "plan ·")
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
	if !strings.Contains(view, "build · m") {
		t.Fatalf("View() = %q, con permiso pendiente Tab NO debe cambiar el pie: debe seguir mostrando %q", view, "build · m")
	}
	if strings.Contains(view, "plan ·") {
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
	planLine := lineWith(t, view, "[plan]")
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
	if got := m.View(); strings.Contains(got, "[plan]") {
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

	if got := m.View(); strings.Contains(got, "[plan]") {
		t.Fatalf("View() = %q, 'n' debe retirar la oferta de aprobacion del plan", got)
	}
	if len(fake.accepted) != 0 {
		t.Fatalf("AcceptPlan fue llamado %d veces, 'n' NO debe aceptar el plan", len(fake.accepted))
	}
	if got := m.View(); !strings.Contains(got, "plan · m") {
		t.Fatalf("View() = %q, tras 'n' el pie debe seguir mostrando %q: rechazar la oferta no cambia el modo", got, "plan · m")
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
	if !strings.Contains(view, "build · m") {
		t.Fatalf("View() = %q, tras aceptar el plan el pie debe volver a %q", view, "build · m")
	}
	if strings.Contains(view, "plan ·") {
		t.Fatalf("View() = %q, tras aceptar el plan el pie NO debe seguir mostrando %q", view, "plan ·")
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

	if got := m.View(); strings.Contains(got, "[plan]") {
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
	if !strings.Contains(view, "build · m") || strings.Contains(view, "plan ·") {
		t.Fatalf("View() = %q, Tab con menu abierto NO debe alternar el plan-mode: el pie debe seguir mostrando %q", view, "build · m")
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

// TRIANGULATE: al salir de la navegacion hacia adelante el input debe volver
// al texto que habia ANTES de empezar a navegar. Tumba una implementacion que
// restaura siempre "" en lugar del borrador tecleado sin enviar.
func TestModel_HistoryPreservesDraftOnNavigation(t *testing.T) {
	fake := &fakeAgent{}
	m := NewModel(fake, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("primero")})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// Un borrador tecleado sin enviar: navegar el historial no debe perderlo.
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("borrador")})

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "primero" {
		t.Fatalf("input.Value() = %q, la flecha arriba debe recuperar el prompt enviado (%q)", got, "primero")
	}

	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.input.Value(); got != "borrador" {
		t.Fatalf("input.Value() = %q, al salir de la navegacion la flecha abajo debe restaurar el borrador tecleado (%q), no perderlo ni dejar el input vacio", got, "borrador")
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
// mostrando el prefijo PLANO (sin la cola y con los ** crudos); recien al
// drenar se rinde el markdown.
func TestModel_RevealMarkdownSwapWaitsForDrain(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// ~502 runas: el primer tick revela ~63 (los ** iniciales incluidos) y
	// deja mucha cola sin revelar.
	text := "**fuerte** " + strings.Repeat("relleno ", 60) + "fin-drenado"
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: text})

	// Un tick antes del cierre: el prefijo con los marcadores crudos ya se ve.
	m = apply(t, m, revealTickMsg{})
	if got := m.View(); !strings.Contains(got, "**fuerte**") {
		t.Fatalf("View() = %q, debe contener el marcador crudo %q tras el primer tick: el streaming en vivo se muestra plano", got, "**fuerte**")
	}

	// El turno se cierra con backlog pendiente.
	m = apply(t, m, EventMsg{
		Kind:    session.KindStepEnded,
		Message: &session.Message{ID: "a1", Role: session.RoleAssistant, Text: text},
	})

	view := m.View()
	if strings.Contains(view, "fin-drenado") {
		t.Fatalf("View() = %q, NO debe contener %q inmediatamente tras StepEnded: cerrar el turno no debe revelar de golpe la cola pendiente, el reveal sigue su ritmo de ticks", view, "fin-drenado")
	}
	if !strings.Contains(view, "**") {
		t.Fatalf("View() = %q, debe seguir mostrando los %q crudos tras StepEnded: mientras quede backlog el prefijo se rinde PLANO, saltar a markdown a mitad de la animacion flashearia el texto completo", view, "**")
	}

	// Drenado el backlog, el bloque cerrado se rinde como markdown.
	m = drainReveal(t, m)
	view = m.View()
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
	if got := m.View(); !strings.Contains(got, text) {
		t.Fatalf("View() = %q, debe contener el texto multibyte completo %q tras drenar: revelar por runas no debe perder ni corromper ningun caracter", got, text)
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
	// La fila en pantalla es la del contenido menos el desplazamiento visible.
	clickY := summaryRow - m.viewport.YOffset
	if clickY < 0 {
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
	clickY := headerRow - m.viewport.YOffset
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

func TestModel_TreeKeys_NavigateAndInsertAt(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"internal/tui/model.go", "go.mod"}, nil
	})
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

	if m.treeOpen {
		t.Fatal("selecting a file must close the tree")
	}
	if got, want := m.input.Value(), "@internal/tui/model.go"; got != want {
		t.Fatalf("input.Value() = %q, want %q", got, want)
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
	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 6})
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
	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 8})
	m = m.toggleTree()

	for range 4 {
		m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}
	if got, want := m.treeOffset, 1; got != want {
		t.Fatalf("treeOffset at bottom = %d, want %d", got, want)
	}

	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 6})
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

func TestModel_TreeSelectionAppendsMentionToExistingComposer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) {
		return []string{"go.mod"}, nil
	})
	m.input.SetValue("revisa")
	m = m.toggleTree()
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got, want := m.input.Value(), "revisa @go.mod"; got != want {
		t.Fatalf("input.Value() = %q, want %q", got, want)
	}
}
