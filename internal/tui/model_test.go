package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"atenea/internal/session"
)

// fakeAgent implementa Agent y registra las llamadas para asertar sobre ellas.
type fakeAgent struct {
	sent     []struct{ sessionID, text string }
	resolved []struct {
		sessionID, callID string
		approved          bool
	}
	stopped []string
}

func (f *fakeAgent) SendPrompt(sessionID, text string) error {
	f.sent = append(f.sent, struct{ sessionID, text string }{sessionID, text})
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

func TestModel_FoldsStreamingAssistantText(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "Hola "})
	if got := m.View(); !strings.Contains(got, "Hola ") {
		t.Fatalf("View() = %q, debe contener %q tras el primer delta", got, "Hola ")
	}

	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "mundo"})
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

func TestModel_RendersUserMessages(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// El mensaje del usuario llega SIN Kind: el runner promueve el prompt como
	// SessionEvent{Message: {Role: user}}.
	m = apply(t, m, EventMsg{Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola atenea"}})
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "hola humano"})

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
	if got := m.View(); !strings.Contains(got, "[tool] bash: ejecutando") {
		t.Fatalf("View() = %q, Tool.Called debe mostrar el ToolName con estado de ejecucion %q", got, "[tool] bash: ejecutando")
	}

	m = apply(t, m, EventMsg{
		Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash", Text: "archivo.txt",
		Message: &session.Message{ID: "c1", Role: session.RoleTool, Text: "archivo.txt", ToolCallID: "c1"},
	})
	if got := m.View(); !strings.Contains(got, "[tool] bash: ok") {
		t.Fatalf("View() = %q, Tool.Success debe asentar la tool como %q", got, "[tool] bash: ok")
	}
	if got := m.View(); strings.Contains(got, "ejecutando") {
		t.Fatalf("View() = %q, la tool asentada no debe seguir mostrandose como en ejecucion", got)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolCalled, CallID: "c2", ToolName: "edit", Input: json.RawMessage(`{"path":"a.go"}`)})
	if got := m.View(); !strings.Contains(got, "[tool] edit: ejecutando") {
		t.Fatalf("View() = %q, el segundo tool call debe mostrarse en ejecucion", got)
	}

	m = apply(t, m, EventMsg{Kind: session.KindToolFailed, CallID: "c2", ToolName: "edit", Error: "permiso denegado"})
	got := m.View()
	if !strings.Contains(got, "[tool] edit: error: permiso denegado") {
		t.Fatalf("View() = %q, Tool.Failed debe mostrar el Error de la tool", got)
	}
	if !strings.Contains(got, "[tool] bash: ok") {
		t.Fatalf("View() = %q, el fallo de c2 no debe tocar el estado ok de c1", got)
	}
	if strings.Contains(got, "ejecutando") {
		t.Fatalf("View() = %q, no debe quedar ninguna tool en ejecucion", got)
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

func TestModel_ReasoningAndToolInputDeltasAreNotTranscript(t *testing.T) {
	m := NewModel(nil, "s1", nil)

	// Reasoning: sus deltas y su texto final NO son transcript.
	m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: "pienso en secreto"})
	m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: "pienso en secreto"})

	// Tool input: los fragmentos crudos viajan en Text y el JSON completo en
	// Input; ninguno es texto de conversacion.
	m = apply(t, m, EventMsg{Kind: session.KindToolInputStarted, CallID: "c1"})
	m = apply(t, m, EventMsg{Kind: session.KindToolInputDelta, CallID: "c1", Text: `{"cmd":"ls`})
	m = apply(t, m, EventMsg{Kind: session.KindToolInputEnded, CallID: "c1", Input: json.RawMessage(`{"cmd":"ls"}`)})

	// El texto normal del assistant si es transcript: contrasta con lo anterior.
	m = apply(t, m, EventMsg{Kind: session.KindTextStarted})
	m = apply(t, m, EventMsg{Kind: session.KindTextDelta, Text: "respuesta visible"})

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
