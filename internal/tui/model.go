// Package tui implementa la interfaz de terminal estilo chat sobre Bubble Tea.
// El Model folda los SessionEvents durables al estado de la conversacion.
//
// El paquete se organiza por responsabilidad: model.go (tipos, estado y
// teclado), fold.go (fold de eventos a entradas) y view.go (render, estilos
// y viewport).
package tui

import (
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"atenea/internal/command"
	"atenea/internal/session"
)

// EventMsg es el evento durable de sesion que llega del engine al Model.
type EventMsg session.SessionEvent

// RunDoneMsg marca el fin de una corrida; Err == "" significa terminada limpia.
type RunDoneMsg struct{ Err string }

// Agent es la superficie del engine que la TUI necesita para operar la sesion.
type Agent interface {
	SendPrompt(sessionID, text string) error
	// SendPlanPrompt envia el prompt por el camino de plan-mode.
	SendPlanPrompt(sessionID, text string) error
	// AcceptPlan acepta el plan presentado: vuelve a modo normal y ejecuta.
	AcceptPlan(sessionID string) error
	ResolvePermission(sessionID, callID string, approved bool)
	Stop(sessionID string)
}

// entryKind distingue los tipos de bloque de la conversacion.
type entryKind int

const (
	entryAssistant    entryKind = iota // texto del assistant (streaming o coalescido)
	entryUser                          // mensaje del usuario
	entryTool                          // tool call y su desenlace
	entryPermission                    // solicitud de aprobacion pendiente (ask-before-run)
	entryPlanApproval                  // oferta de aprobacion del plan presentado (present_plan)
	entryError                         // fallo duro del step (Step.Failed)
)

// toolStatus es el estado observable de un tool call en la conversacion.
type toolStatus int

const (
	toolRunning toolStatus = iota // Tool.Called sin desenlace todavia
	toolOK                        // Tool.Success del mismo CallID
	toolFailed                    // Tool.Failed del mismo CallID
)

// entry es un bloque de la conversacion.
type entry struct {
	kind entryKind
	text string
	live bool // true mientras el streaming del bloque sigue abierto

	// Campos de tool call y de permiso (kind == entryTool / entryPermission):
	callID string
	tool   string
	status toolStatus
	err    string // mensaje de Tool.Failed
	// input es el JSON crudo del Input de la tool: lo llena la solicitud de
	// permiso (se rinde tal cual en la linea [permiso]) y tambien Tool.Called,
	// donde alimenta el resumen del header (`bash(ls)`) via summarizeToolInput.
	input string
	// output es el resultado de Tool.Success (ev.Text): alimenta el preview de
	// hasta 4 lineas bajo el header (ver renderOutputPreview).
	output string
	// diff es el diff unificado que Tool.Success de edit/write trae en ev.Diff:
	// cuando existe, el detalle bajo el header muestra el diff en lugar del
	// preview del output (ver renderDiffPreview).
	diff string
	// sessionID es la sesion duena de la solicitud de permiso. Un subagente
	// (sesion hija) surfacea su evento por el bus del padre conservando el
	// SessionID del hijo: la resolucion debe ir a ESA sesion. Vacio en eventos
	// sin SessionID: se resuelve con la sesion del Model (fallback).
	sessionID string
}

// Model es el modelo raiz de Bubble Tea para la TUI de la sesion.
type Model struct {
	agent     Agent
	sessionID string
	events    <-chan tea.Msg
	entries   []entry
	input     textinput.Model
	working   bool // true desde que arranca una corrida (Enter o aceptar el plan) hasta RunDoneMsg

	// spinner anima el glifo del indicador de trabajo. Su loop de ticks nace
	// donde la corrida arranca (submitPrompt y resolvePlanKey devuelven
	// spinner.Tick como cmd) y muere solo cuando working se apaga: el caso
	// spinner.TickMsg de Update corta el reagendado con !working.
	spinner spinner.Model

	// viewport acota el transcript al alto de la terminal siguiendo la cola;
	// ready se activa con el primer tea.WindowSizeMsg (sin tamano conocido la
	// vista usa el render completo como fallback). width/height guardan el
	// ultimo tamano anunciado para recalcular el alto cuando cambian las
	// lineas reservadas bajo el transcript (caja del composer, indicador de
	// trabajo y pie con agente/modelo).
	viewport viewport.Model
	ready    bool
	width    int
	height   int

	// agentName y model alimentan el pie del composer (agente y modelo de IA);
	// entran una sola vez via WithStatus. El modelo sigue fijo por corrida,
	// pero el agente MOSTRADO cambia con Tab: en plan-mode el pie rinde "plan"
	// en lugar de agentName (ver statusFooter y planMode).
	agentName string
	model     string

	// planMode indica el modo del agente: Tab lo alterna entre build (false)
	// y plan (true). Es pegajoso entre envios: cada Enter envia por el camino
	// del modo activo (SendPrompt en build, SendPlanPrompt en plan) sin
	// resetearlo.
	planMode bool

	// commands y listFiles son las fuentes del autocompletado del composer
	// (entran via WithCompletions): los slash-commands del menu "/" y el
	// listado de archivos del @-menu. menuItems y menuSelected son el estado
	// del popup: refreshMenu los recomputa tras cada tecla que alimenta el
	// input, y la vista rinde una linea por item encima de la caja del
	// composer (reservedLines las descuenta del viewport). files/filesLoaded
	// cachean el resultado de listFiles mientras el token "@" siga activo
	// (loadFilesOnce/dropFileCache).
	commands     []command.Command
	listFiles    func() ([]string, error)
	menuItems    []menuItem
	menuSelected int
	files        []string
	filesLoaded  bool
}

// NewModel construye el Model raiz de la TUI.
func NewModel(agent Agent, sessionID string, events <-chan tea.Msg) Model {
	input := textinput.New()
	input.Prompt = inputPrompt
	input.PromptStyle = accentStyle
	input.Focus()
	// El spinner comparte el estilo tenue de la linea de estado: el glifo es
	// parte del indicador de trabajo, no un protagonista aparte.
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(statusStyle))
	return Model{agent: agent, sessionID: sessionID, events: events, input: input, spinner: sp}
}

// WithStatus fija el agente base y el modelo de IA a mostrar en el pie del
// composer. Builder de valor: la info entra una sola vez al construir el Model
// (en plan-mode el pie muestra "plan" en lugar del agente base).
func (m Model) WithStatus(agentName, model string) Model {
	m.agentName = agentName
	m.model = model
	return m
}

// WithCompletions fija las fuentes del autocompletado del composer: los
// slash-commands del menu "/" y el listado de archivos del @-menu. Builder de
// valor (espejo de WithStatus): la info entra una sola vez al construir el Model.
func (m Model) WithCompletions(commands []command.Command, listFiles func() ([]string, error)) Model {
	m.commands = commands
	m.listFiles = listFiles
	return m
}

// PendingPermission devuelve el CallID de la solicitud de aprobacion pendiente
// (ask-before-run) y true si hay una; el proximo ciclo la usa para resolverla.
func (m Model) PendingPermission() (string, bool) {
	if e, ok := m.pendingPermission(); ok {
		return e.callID, true
	}
	return "", false
}

// Working indica si hay una corrida en curso (desde que arranca, por Enter o
// por aceptar el plan, hasta RunDoneMsg).
func (m Model) Working() bool {
	return m.working
}

// waitForEvent arma la bomba de eventos: un tea.Cmd que hace receive del canal
// dentro del runtime de Bubble Tea (sin goroutines propias). Con canal nil no
// hay bomba; con canal cerrado el cmd devuelve nil y la bomba muere.
func waitForEvent(ch <-chan tea.Msg) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// Init arma la bomba de eventos del canal del engine.
func (m Model) Init() tea.Cmd {
	return waitForEvent(m.events)
}

// Update folda cada EventMsg al estado de la conversacion, maneja el teclado y
// rearma la bomba tras cada mensaje consumido del canal.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch ev := msg.(type) {
	case EventMsg:
		m = m.foldEvent(ev)
		return m.syncViewport(), waitForEvent(m.events)
	case RunDoneMsg:
		m.working = false
		if ev.Err != "" {
			m = m.appendError(ev.Err)
		}
		// Al apagar working la linea de estado desaparece: recalcular el alto.
		return m.resizeViewport(), waitForEvent(m.events)
	case spinner.TickMsg:
		// El loop de animacion muere solo: cuando RunDoneMsg apago working, el
		// tick pendiente llega aqui y no se reagenda (cmd nil).
		if !m.working {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(ev)
		return m, cmd
	case tea.WindowSizeMsg:
		m.ready = true
		m.width = ev.Width
		m.height = ev.Height
		return m.resizeViewport(), nil
	case tea.KeyMsg:
		return m.handleKey(ev)
	case tea.MouseMsg:
		// La rueda scrollea el historial de a MouseWheelDelta lineas (espejo de
		// PgUp/PgDn); el resto del mouse (clicks, movimiento) es inerte porque el
		// viewport lo ignora.
		return m.scrollViewport(ev)
	}
	return m, nil
}

// scrollViewport reenvia msg al viewport para paginar el historial (rueda o
// PgUp/PgDn): nunca escribe en el input ni toca el gate de permisos; los
// eventos nuevos re-siguen la cola via GotoBottom en syncViewport (v1).
func (m Model) scrollViewport(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// handleKey procesa el teclado en orden de prioridad: Ctrl+C detiene y sale
// siempre; PgUp/PgDn son scroll del transcript (nunca escriben en el input ni
// tocan el gate de permisos); un permiso pendiente pone el teclado en modo
// aprobacion (solo y/n hacen algo; Tab incluido queda inerte); tras el gate de
// permisos, un plan pendiente pone el teclado en modo aprobacion de plan (y
// acepta y ejecuta, n descarta la oferta); tras esos gates, el menu de
// autocompletado abierto captura Up/Down (seleccion ciclica, sin tocar el
// viewport ni el input), Tab/Enter (aplican la seleccion) y Esc (cierra el
// popup); con menu cerrado y sin nada pendiente Esc detiene la corrida, Enter
// envia el prompt tecleado, Tab alterna el modo build/plan y el resto de
// teclas alimenta el input (y recomputa el popup).
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		m.stopRun()
		return m, tea.Quit
	}
	if msg.Type == tea.KeyPgUp || msg.Type == tea.KeyPgDown {
		return m.scrollViewport(msg)
	}
	if perm, ok := m.pendingPermission(); ok {
		m.resolvePermissionKey(msg, perm)
		return m, nil
	}
	if m.hasPendingPlan() {
		return m.resolvePlanKey(msg)
	}
	if len(m.menuItems) > 0 {
		switch msg.Type {
		case tea.KeyUp:
			m.menuSelected = (m.menuSelected - 1 + len(m.menuItems)) % len(m.menuItems)
			return m, nil
		case tea.KeyDown:
			m.menuSelected = (m.menuSelected + 1) % len(m.menuItems)
			return m, nil
		case tea.KeyTab, tea.KeyEnter:
			// Tab y Enter aplican la seleccion; ni alternan el modo build/plan
			// ni envian el prompt (enviar exige un segundo Enter con menu cerrado).
			return m.applySelection(), nil
		case tea.KeyEsc:
			// Esc cierra el popup sin detener la corrida ni tocar el input; la
			// proxima tecla que alimente el input recomputa y puede reabrirlo.
			return m.closeMenu(), nil
		}
		// El resto de teclas sigue alimentando el input (rama default de abajo).
	}
	switch msg.Type {
	case tea.KeyEsc:
		// Esc detiene la corrida en curso pero deja la TUI abierta.
		m.stopRun()
		return m, nil
	case tea.KeyEnter:
		return m.submitPrompt()
	case tea.KeyTab:
		// Tab alterna el modo del agente build/plan; nunca llega al textinput
		// (no inserta el caracter de tabulacion en el prompt).
		m.planMode = !m.planMode
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// La tecla pudo cambiar el texto o el caret: recomputar el popup de
	// autocompletado desde el estado nuevo del input.
	return m.refreshMenu(), cmd
}

// resolvePermissionKey atiende el teclado en modo aprobacion: 'y' aprueba y
// 'n' deniega la solicitud pendiente. El resto del teclado (runas, Enter,
// Esc) es no-op: no alimenta el input ni envia prompts mientras se espera la
// decision. Resuelve con el SessionID que trajo el evento de permiso (puede
// ser una sesion hija de un subagente); sin SessionID usa la sesion del Model.
func (m Model) resolvePermissionKey(msg tea.KeyMsg, perm entry) {
	if msg.Type != tea.KeyRunes || m.agent == nil {
		return
	}
	sessionID := perm.sessionID
	if sessionID == "" {
		sessionID = m.sessionID
	}
	switch string(msg.Runes) {
	case "y":
		m.agent.ResolvePermission(sessionID, perm.callID, true)
	case "n":
		m.agent.ResolvePermission(sessionID, perm.callID, false)
	}
}

// resolvePlanKey atiende el teclado en modo aprobacion de plan: 'y' acepta el
// plan via Agent.AcceptPlan (vuelve a modo build y la corrida sigue como
// trabajando hasta RunDoneMsg), 'n' descarta la oferta y deja el plan-mode
// como esta. El resto del teclado es no-op mientras se espera la decision.
// Con agent nil (tests del fold) es no-op. Aceptar el plan arranca la corrida
// (working = true): ahi nace el cmd spinner.Tick que bombea la animacion del
// indicador de trabajo (el spinner solo se bombea mientras hay corrida).
func (m Model) resolvePlanKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if msg.Type != tea.KeyRunes || m.agent == nil {
		return m, nil
	}
	switch string(msg.Runes) {
	case "y":
		m.agent.AcceptPlan(m.sessionID)
		m = m.removePendingPlan()
		m.planMode = false
		m.working = true
		// La linea de estado ocupa una linea bajo el transcript: recalcular el alto.
		return m.resizeViewport(), m.spinner.Tick
	case "n":
		return m.removePendingPlan().syncViewport(), nil
	}
	return m, nil
}

// submitPrompt envia el texto tecleado al Agent por el camino del modo activo
// (SendPrompt en build, SendPlanPrompt en plan) y marca la corrida en curso.
// Con input vacio o agent nil (tests del fold) es no-op: no hay que enviar o
// no hay a quien. Arrancar la corrida devuelve el cmd spinner.Tick que bombea
// la animacion del indicador de trabajo: el cmd nace aqui porque el spinner
// solo se bombea mientras hay corrida (el caso spinner.TickMsg de Update corta
// el loop cuando working se apaga).
func (m Model) submitPrompt() (Model, tea.Cmd) {
	text := m.input.Value()
	if text == "" || m.agent == nil {
		return m, nil
	}
	if m.planMode {
		m.agent.SendPlanPrompt(m.sessionID, text)
	} else {
		m.agent.SendPrompt(m.sessionID, text)
	}
	m.input.SetValue("")
	m.working = true
	// La linea de estado ocupa una linea bajo el transcript: recalcular el alto.
	return m.resizeViewport(), m.spinner.Tick
}

// stopRun detiene la corrida en curso; con agent nil (tests del fold) es no-op.
func (m Model) stopRun() {
	if m.agent != nil {
		m.agent.Stop(m.sessionID)
	}
}
