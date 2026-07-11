// Package tui implementa la interfaz de terminal estilo chat sobre Bubble Tea.
// El Model folda los SessionEvents durables al estado de la conversacion.
//
// El paquete se organiza por responsabilidad: model.go (tipos, estado y
// teclado), fold.go (fold de eventos a entradas), view.go (render, estilos
// y viewport) y reveal.go (smooth streaming del texto del assistant y del
// bloque de pensamiento).
package tui

import (
	"errors"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"atenea/internal/command"
	"atenea/internal/session"
)

// EventMsg es el evento durable de sesion que llega del engine al Model.
type EventMsg session.SessionEvent

// RunDoneMsg marca el fin de una corrida; Err == "" significa terminada limpia.
type RunDoneMsg struct{ Err string }

type leaderTimeoutMsg struct{ generation uint64 }

// Agent es la superficie del engine que la TUI necesita para operar la sesion.
type Agent interface {
	// SendPrompt devuelve el ID de la sesion activa; /new exacto devuelve una
	// sesion durable distinta de la actual.
	SendPrompt(sessionID, text string) (string, error)
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
	entryReasoning                     // bloque de pensamiento del assistant (colapsable)
	entryUser                          // mensaje del usuario
	entryTool                          // tool call y su desenlace
	entryPermission                    // solicitud de aprobacion pendiente (ask-before-run)
	entryPlanApproval                  // oferta de aprobacion del plan presentado (present_plan)
	entryError                         // fallo duro del step (Step.Failed)
)

const historyLimit = 100

type panelFocus int

const (
	chatFocus panelFocus = iota
	explorerFocus
	viewerFocus
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

	// revealed es cuantas runas de text ya revelo el smooth streaming (la
	// vista muestra solo ese prefijo mientras quede backlog; ver reveal.go).
	// Solo participa en las entradas cuyo texto llega por streaming (assistant
	// y pensamiento); el resto se rinde completo sin mirar este campo.
	revealed int

	// expanded solo aplica a los bloques de pensamiento asentados (kind ==
	// entryReasoning y settled): cuando true la vista rinde el texto completo
	// del pensamiento en lugar de la linea de resumen colapsado. Lo alterna
	// la tecla Shift+Tab (ver handleKey), que lo conmuta en todos los bloques
	// de pensamiento asentados a la vez. Inerte mientras el bloque esta en
	// vivo o con backlog: el preview del pensamiento en curso nunca se fija en
	// este campo.
	expanded bool

	// Campos del bloque de pensamiento (kind == entryReasoning): startedAt es
	// el instante en que Reasoning.Started abrio el bloque y duration la que
	// tomo pensar, computada al cerrarlo; el resumen colapsado "[penso <dur>]"
	// la rinde legible (ver renderThinking).
	startedAt time.Time
	duration  time.Duration

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
	input     composerInput
	working   bool // true desde que arranca una corrida (Enter o aceptar el plan) hasta RunDoneMsg

	// spinner anima el glifo del indicador de trabajo. Su loop de ticks nace
	// donde la corrida arranca (submitPrompt y resolvePlanKey devuelven
	// spinner.Tick como cmd) y muere solo cuando working se apaga: el caso
	// spinner.TickMsg de Update corta el reagendado con !working.
	spinner spinner.Model

	// revealing indica si el loop de ticks del reveal (smooth streaming) esta
	// corriendo. Espejo del loop del spinner: nace cuando un EventMsg deja
	// backlog sin loop activo, se rearma en cada tick mientras quede backlog
	// y muere (cmd nil) cuando se agota; un delta posterior lo reinicia. El
	// flag evita duplicar cadenas de ticks cuando llegan varios deltas antes
	// del proximo tick.
	revealing bool

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
	agentName      string
	model          string
	usage          *session.Usage
	liveUsage      bool
	outputBytes    int
	reasoningBytes int
	toolInputBytes int

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

	// history guarda los ultimos historyLimit prompts enviados (Enter con
	// texto, camino build o plan). Con el composer vacio, Arriba/Abajo los
	// recorren; histIdx == len(history) significa "no navegando". Enviar un
	// prompt resetea la navegacion y bajar despues del mas reciente limpia el
	// composer.
	history []string
	histIdx int

	leaderPending    bool
	leaderGeneration uint64
	treeOpen         bool
	treeLoaded       bool
	tree             fileTree
	treeCursor       int
	treeOffset       int
	treeError        string
	fileReader       FileReader
	viewer           fileViewer
	viewerReturnY    int
	focus            panelFocus
}

// NewModel construye el Model raiz de la TUI.
func NewModel(agent Agent, sessionID string, events <-chan tea.Msg) Model {
	input := newComposerInput()
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

// WithFileReader fija el lector de workspace para el visor de solo lectura.
// Builder de valor para que los tests inyecten un filesystem controlado.
func (m Model) WithFileReader(read FileReader) Model {
	m.fileReader = read
	return m
}

// WithHistory precarga el historial durable del composer, conservando solo el
// limite que la navegacion puede exponer.
func (m Model) WithHistory(history []string) Model {
	if len(history) > historyLimit {
		history = history[len(history)-historyLimit:]
	}
	m.history = append([]string(nil), history...)
	m.histIdx = len(m.history)
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
		pump := waitForEvent(m.events)
		// Un evento que deja texto sin revelar arranca el loop de ticks del
		// reveal si no hay uno corriendo (ver revealing); el tick viaja
		// batcheado con la bomba de eventos.
		if !m.revealing && m.hasBacklog() {
			m.revealing = true
			return m.syncViewport(), tea.Batch(pump, revealTick())
		}
		return m.syncViewport(), pump
	case RunDoneMsg:
		m.working = false
		if ev.Err != "" {
			m = m.appendError(ev.Err)
		}
		// Al apagar working la linea de estado desaparece: recalcular el alto.
		return m.resizeViewport(), waitForEvent(m.events)
	case revealTickMsg:
		// El loop de reveal muere solo: con el backlog agotado el tick no se
		// reagenda (cmd nil) y un delta posterior lo reinicia. Siempre se
		// re-sincroniza el viewport para que el texto recien revelado se vea.
		m = m.advanceReveal()
		if !m.hasBacklog() {
			m.revealing = false
			return m.syncViewport(), nil
		}
		m.revealing = true
		return m.syncViewport(), revealTick()
	case leaderTimeoutMsg:
		if ev.generation == 0 || ev.generation == m.leaderGeneration {
			m.leaderPending = false
		}
		return m, nil
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
		m = m.resizeViewport()
		m.syncTreeViewport()
		m.viewer.clamp(m.fileViewerHeight())
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(ev)
	case tea.MouseMsg:
		if ev.Action == tea.MouseActionPress && (ev.Button == tea.MouseButtonWheelUp || ev.Button == tea.MouseButtonWheelDown) {
			if m.treeOpen && m.treeMouseOverPanel(ev) {
				if ev.Button == tea.MouseButtonWheelUp {
					m.moveTreeCursor(-3)
				} else {
					m.moveTreeCursor(3)
				}
				return m, nil
			}
			if m.viewer.active() && ev.Y >= 0 && ev.Y < m.fileViewerHeight() {
				m.scrollFileViewerMouse(ev)
				return m, nil
			}
			return m.scrollViewport(ev)
		}
		m.focus = m.normalizedFocus()
		if m.treeOpen && m.handleTreeMouse(ev) {
			return m, nil
		}
		if ev.Action == tea.MouseActionPress && ev.Button == tea.MouseButtonLeft {
			m.focus = m.focusAtMouse(ev)
		}
		if m.focus == viewerFocus {
			m.scrollFileViewerMouse(ev)
			return m, nil
		}
		if m.viewer.active() && ev.Button == tea.MouseButtonLeft {
			return m, nil
		}
		// El clic izquierdo sobre un bloque de pensamiento asentado alterna su
		// estado expandido (ver toggleThinkingAt): paridad con el ThinkingBlock
		// del escritorio, que se expande/colapsa con un clic. Con WithMouseCellMotion
		// la TUI recibe tambien eventos de movimiento y rueda: solo el press del
		// boton izquierdo hace toggle; la rueda y el resto se delegan al scroll
		// del viewport (scrollViewport) como hasta ahora. Sin tamano conocido
		// (ready == false) el viewport no tiene coordenadas estables: el clic se
		// ignora y la rueda igual va al scroll.
		if m.ready && ev.Action == tea.MouseActionPress && ev.Button == tea.MouseButtonLeft {
			// La fila clicada es global (0 = arriba de la terminal); el viewport
			// es la primera seccion de View(), asi que esa fila es la del
			// viewport, y la linea absoluta del contenido es YOffset + fila.
			viewportLine := m.viewport.YOffset + ev.Y
			if next, ok := m.toggleThinkingAt(viewportLine); ok {
				return next.syncViewport(), nil
			}
		}
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

func (m *Model) scrollFileViewerMouse(msg tea.MouseMsg) {
	if msg.Action != tea.MouseActionPress {
		return
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.viewer.scroll(-3, m.fileViewerHeight())
	case tea.MouseButtonWheelDown:
		m.viewer.scroll(3, m.fileViewerHeight())
	}
}

// handleKey procesa el teclado en orden de prioridad: Ctrl+C detiene y sale
// siempre; PgUp/PgDn son scroll del transcript (nunca escriben en el input ni
// tocan el gate de permisos); un permiso pendiente pone el teclado en modo
// aprobacion (solo y/n hacen algo; Tab incluido queda inerte); tras el gate de
// permisos, un plan pendiente pone el teclado en modo aprobacion de plan (y
// acepta y ejecuta, n descarta la oferta); tras esos gates, el arbol abierto
// captura su navegacion y Space+e lo cierra; con el arbol cerrado el menu de
// autocompletado abierto captura Up/Down (seleccion ciclica, sin tocar el
// viewport ni el input), Tab/Enter (aplican la seleccion) y Esc (cierra el
// popup); con menu cerrado y composer vacio Space arma el leader de un segundo
// (e abre el explorer, otra tecla o timeout lo cancelan sin insertar); despues
// Esc detiene la corrida, Enter envia el prompt tecleado, Tab alterna el modo
// build/plan, Up/Down navegan el historial de prompts enviados (ver
// recallHistory) y el resto de teclas alimenta el input (y recomputa el popup).
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		m.stopRun()
		return m, tea.Quit
	}
	if perm, ok := m.pendingPermission(); ok {
		m.resolvePermissionKey(msg, perm)
		return m, nil
	}
	if m.hasPendingPlan() {
		return m.resolvePlanKey(msg)
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		return m.handleKeyRuneBatch(msg)
	}
	m.focus = m.normalizedFocus()
	if m.focus == viewerFocus {
		return m.handleFileViewerKey(msg)
	}
	if m.focus == explorerFocus {
		return m.handleTreeKey(msg)
	}
	if msg.Type == tea.KeyPgUp || msg.Type == tea.KeyPgDown {
		return m.scrollViewport(msg)
	}
	if msg.Type == tea.KeyEnter && m.input.Value() == "/new" {
		return m.submitPrompt()
	}
	if len(m.menuItems) > 0 {
		switch msg.Type {
		case tea.KeyUp:
			m.menuSelected = (m.menuSelected - 1 + len(m.menuItems)) % len(m.menuItems)
			return m, nil
		case tea.KeyDown:
			m.menuSelected = (m.menuSelected + 1) % len(m.menuItems)
			return m, nil
		case tea.KeyTab:
			// Tab aplica la seleccion; no alterna el modo build/plan.
			return m.applySelection(), nil
		case tea.KeyEnter:
			if m.menuItems[m.menuSelected].builtin {
				m.input.SetValue(m.menuItems[m.menuSelected].label)
				m.input.SetCursor(len([]rune(m.menuItems[m.menuSelected].label)))
				return m.closeMenu().submitPrompt()
			}
			return m.applySelection(), nil
		case tea.KeyEsc:
			// Esc cierra el popup sin detener la corrida ni tocar el input; la
			// proxima tecla que alimente el input recomputa y puede reabrirlo.
			return m.closeMenu(), nil
		}
		// El resto de teclas sigue alimentando el input (rama default de abajo).
	}
	if m.leaderPending {
		m.leaderPending = false
		if keyRune(msg) == "e" {
			return m.toggleTree(), nil
		}
		return m, nil
	}
	if m.input.Value() == "" && (msg.Type == tea.KeySpace || keyRune(msg) == " ") {
		return m.startLeader()
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
	case tea.KeyShiftTab:
		// Shift+Tab alterna el estado expandido de TODOS los bloques de
		// pensamiento asentados a la vez (ver toggleThinking): colapsa el
		// resumen a la linea "[penso <dur>] ⇧Tab" o, si estaba expandido,
		// vuelve a mostrar el texto completo. No toca el input ni el gate de
		// permisos/plan/menu (esos gates ya capturaron la tecla arriva) y es
		// inerte frente a un pensamiento en vivo: el preview del pensamiento
		// en curso no participa del toggle.
		m = m.toggleThinking()
		return m.syncViewport(), nil
	case tea.KeyUp:
		if next, ok := m.recallHistory(-1); ok {
			return next, nil
		}
		// Sin paso aplicable la tecla sigue al textinput (que la ignora).
	case tea.KeyDown:
		if next, ok := m.recallHistory(1); ok {
			return next, nil
		}
		// Sin paso aplicable la tecla sigue al textinput (que la ignora).
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// La tecla pudo cambiar el texto o el caret: recomputar el popup de
	// autocompletado desde el estado nuevo del input.
	return m.refreshMenu(), cmd
}

func (m Model) handleKeyRuneBatch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	for _, key := range msg.Runes {
		next, _ := m.handleKey(tea.KeyMsg{
			Type:  tea.KeyRunes,
			Runes: []rune{key},
			Alt:   msg.Alt,
		})
		nextModel, ok := next.(Model)
		if !ok {
			return next, nil
		}
		m = nextModel
	}
	if m.leaderPending {
		return m, leaderTimeout(m.leaderGeneration)
	}
	return m, nil
}

func (m Model) startLeader() (Model, tea.Cmd) {
	m.leaderPending = true
	m.leaderGeneration++
	return m, leaderTimeout(m.leaderGeneration)
}

func leaderTimeout(generation uint64) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return leaderTimeoutMsg{generation: generation}
	})
}

func keyRune(msg tea.KeyMsg) string {
	if msg.Type != tea.KeyRunes {
		return ""
	}
	return string(msg.Runes)
}

func (m Model) toggleTree() Model {
	m.leaderPending = false
	viewportOffset := m.viewport.YOffset
	resizeViewport := func() Model {
		m = m.resizeViewport()
		m.viewport.SetYOffset(viewportOffset)
		return m
	}
	m.treeOpen = !m.treeOpen
	if !m.treeOpen {
		m.focus = m.normalizedFocus()
		return resizeViewport()
	}
	m.focus = explorerFocus
	m.treeCursor = 0
	m.treeOffset = 0
	m = m.loadTreeOnce()
	return resizeViewport()
}

func (m Model) loadTreeOnce() Model {
	if m.treeLoaded {
		return m
	}
	m.treeError = ""
	if m.listFiles == nil {
		m.tree = newFileTree(nil)
		m.treeLoaded = true
		return m
	}
	files, err := m.listFiles()
	if err != nil {
		m.tree = newFileTree(nil)
		m.treeError = err.Error()
		return m
	}
	m.tree = newFileTree(files)
	m.treeLoaded = true
	return m
}

func (m Model) handleTreeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.leaderPending {
		m.leaderPending = false
		if keyRune(msg) == "e" {
			return m.toggleTree(), nil
		}
		return m, nil
	}
	if msg.Type == tea.KeySpace || keyRune(msg) == " " {
		return m.startLeader()
	}
	rows := m.tree.visibleRows()
	switch {
	case msg.Type == tea.KeyEsc || keyRune(msg) == "q":
		m.treeOpen = false
	case msg.Type == tea.KeyDown || keyRune(msg) == "j":
		if m.treeCursor < len(rows)-1 {
			m.treeCursor++
		}
	case msg.Type == tea.KeyUp || keyRune(msg) == "k":
		if m.treeCursor > 0 {
			m.treeCursor--
		}
	case msg.Type == tea.KeyEnter || keyRune(msg) == "l":
		if len(rows) == 0 {
			break
		}
		node := rows[m.treeCursor].node
		if node.dir {
			m.tree.toggle(node.path)
			m.clampTreeCursor()
			break
		}
		m = m.openTreeFile(node.path)
		m.focus = viewerFocus
	case keyRune(msg) == "h":
		if len(rows) == 0 {
			break
		}
		node := rows[m.treeCursor].node
		if node.dir && m.tree.expanded[node.path] {
			m.tree.toggle(node.path)
			m.clampTreeCursor()
			break
		}
		parent := pathParent(node.path)
		for i, row := range rows {
			if row.node.path == parent {
				m.treeCursor = i
				break
			}
		}
	}
	m.syncTreeViewport()
	return m, nil
}

// handleTreeMouse captura los eventos que caen dentro del panel del explorer.
// La rueda mueve la seleccion para conservar el mismo foco visual que el
// teclado; un clic izquierdo selecciona la fila completa y la activa, igual
// que Enter (carpeta expande/colapsa, archivo abre el visor).
func (m *Model) handleTreeMouse(msg tea.MouseMsg) bool {
	if !m.treeMouseOverPanel(msg) {
		return false
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if msg.Action != tea.MouseActionPress {
			return true
		}
		m.moveTreeCursor(-3)
		return true
	case tea.MouseButtonWheelDown:
		if msg.Action != tea.MouseActionPress {
			return true
		}
		m.moveTreeCursor(3)
		return true
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return true
		}
		m.focus = explorerFocus
		row, ok := m.treeRowAtMouse(msg.Y)
		if !ok {
			return true
		}
		m.treeCursor = row
		rows := m.tree.visibleRows()
		node := rows[row].node
		if node.dir {
			m.tree.toggle(node.path)
			m.clampTreeCursor()
			return true
		}
		*m = m.openTreeFile(node.path)
		return true
	}
	return true
}

func (m Model) normalizedFocus() panelFocus {
	if m.treeOpen && m.ready && m.treePanelWidth() >= m.width {
		return explorerFocus
	}
	if m.focus == explorerFocus && !m.treeOpen {
		return chatFocus
	}
	if m.focus == viewerFocus && !m.viewer.active() {
		return chatFocus
	}
	return m.focus
}

func (m Model) focusAtMouse(msg tea.MouseMsg) panelFocus {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return m.normalizedFocus()
	}
	if m.viewer.active() && msg.Y >= 0 && msg.Y < m.fileViewerHeight() {
		return viewerFocus
	}
	return chatFocus
}

func (m Model) treeMouseOverPanel(msg tea.MouseMsg) bool {
	return m.ready && m.treeVisible() && msg.X >= 0 && msg.X < m.treePanelWidth()
}

func (m Model) treeVisible() bool {
	return m.treeOpen && (!m.viewer.active() || m.treePanelWidth() < m.width)
}

func (m Model) treeRowAtMouse(y int) (int, bool) {
	const treeRowsStartY = 3
	if y < treeRowsStartY {
		return 0, false
	}
	row := m.treeOffset + y - treeRowsStartY
	rows := m.tree.visibleRows()
	if row < 0 || row >= len(rows) || row >= m.treeOffset+m.treeVisibleRowCount() {
		return 0, false
	}
	return row, true
}

func (m *Model) moveTreeCursor(delta int) {
	rows := m.tree.visibleRows()
	if len(rows) == 0 {
		return
	}
	m.treeCursor = min(max(m.treeCursor+delta, 0), len(rows)-1)
	m.syncTreeViewport()
}

func (m Model) fileViewerHeight() int {
	return max(m.height-1, 0)
}

func (m Model) openTreeFile(path string) Model {
	m.viewerReturnY = m.viewport.YOffset
	if m.fileReader == nil {
		m.viewer = openFileViewerError(path, errors.New("lector de archivos no configurado"))
		return m
	}
	content, err := m.fileReader(path)
	if err != nil {
		m.viewer = openFileViewerError(path, err)
		return m
	}
	m.viewer = openFileViewer(path, content)
	m.viewer.clamp(m.fileViewerHeight())
	return m
}

func (m Model) handleFileViewerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	height := m.fileViewerHeight()
	switch {
	case msg.Type == tea.KeyEsc:
		m.viewer = fileViewer{}
		m.viewport.SetYOffset(m.viewerReturnY)
		m.focus = chatFocus
	case msg.Type == tea.KeyDown || keyRune(msg) == "j":
		m.viewer.scroll(1, height)
	case msg.Type == tea.KeyUp || keyRune(msg) == "k":
		m.viewer.scroll(-1, height)
	case msg.Type == tea.KeyPgDown:
		m.viewer.scroll(max(height, 1), height)
	case msg.Type == tea.KeyPgUp:
		m.viewer.scroll(-max(height, 1), height)
	}
	return m, nil
}

func (m *Model) clampTreeCursor() {
	rows := m.tree.visibleRows()
	if len(rows) == 0 {
		m.treeCursor = 0
	} else if m.treeCursor >= len(rows) {
		m.treeCursor = len(rows) - 1
	}
	m.syncTreeViewport()
}

func (m *Model) syncTreeViewport() {
	rows := m.tree.visibleRows()
	if len(rows) == 0 {
		m.treeOffset = 0
		return
	}
	visibleRows := m.treeVisibleRowCount()
	if visibleRows == 0 {
		m.treeOffset = 0
		return
	}
	if m.treeCursor < m.treeOffset {
		m.treeOffset = m.treeCursor
	}
	if m.treeCursor >= m.treeOffset+visibleRows {
		m.treeOffset = m.treeCursor - visibleRows + 1
	}
	m.treeOffset = min(m.treeOffset, max(len(rows)-visibleRows, 0))
}

func (m *Model) insertTreeMention(nodePath string) {
	value := m.input.Value()
	if value != "" && !strings.HasSuffix(value, " ") {
		value += " "
	}
	value += "@" + nodePath
	m.input.SetValue(value)
	m.input.CursorEnd()
}

func pathParent(nodePath string) string {
	if index := strings.LastIndex(nodePath, "/"); index >= 0 {
		return nodePath[:index]
	}
	return ""
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
	if text == "/new" {
		newSessionID, err := m.agent.SendPrompt(m.sessionID, text)
		if err != nil {
			return m.appendError(err.Error()).syncViewport(), nil
		}
		m.sessionID = newSessionID
		m.entries = nil
		m.history = nil
		m.histIdx = 0
		m.planMode = false
		m.working = false
		m.revealing = false
		m.usage = nil
		m.liveUsage = false
		m.outputBytes = 0
		m.reasoningBytes = 0
		m.toolInputBytes = 0
		m.input.SetValue("")
		return m.resizeViewport(), nil
	}
	if m.planMode {
		m.agent.SendPlanPrompt(m.sessionID, text)
	} else {
		m.agent.SendPrompt(m.sessionID, text)
	}
	m.input.SetValue("")
	// El prompt enviado se apila en el historial y la navegacion se resetea:
	// la proxima flecha arriba empieza desde el final (el sentinela
	// histIdx == len(history) significa "no navegando").
	m.history = append(m.history, text)
	if len(m.history) > historyLimit {
		m.history = m.history[len(m.history)-historyLimit:]
	}
	m.histIdx = len(m.history)
	m.working = true
	// La linea de estado ocupa una linea bajo el transcript: recalcular el alto.
	return m.resizeViewport(), m.spinner.Tick
}

// recallHistory mueve un paso la navegacion del historial de prompts: dir < 0
// retrocede (el mas reciente primero) y dir > 0 avanza. Solo permite empezar
// a navegar con el composer vacio; avanzar mas alla del prompt mas reciente lo
// limpia. El prompt recuperado entra al input con el cursor al final. Devuelve
// ok=false cuando el paso no aplica.
func (m Model) recallHistory(dir int) (Model, bool) {
	if dir < 0 {
		if m.histIdx == len(m.history) && m.input.Value() != "" {
			return m, false
		}
		if m.histIdx == 0 {
			return m, false
		}
		m.histIdx--
		m.input.SetValue(m.history[m.histIdx])
	} else {
		if m.histIdx >= len(m.history) {
			return m, false
		}
		m.histIdx++
		if m.histIdx == len(m.history) {
			m.input.SetValue("")
		} else {
			m.input.SetValue(m.history[m.histIdx])
		}
	}
	m.input.CursorEnd()
	return m, true
}

// stopRun detiene la corrida en curso; con agent nil (tests del fold) es no-op.
func (m Model) stopRun() {
	if m.agent != nil {
		m.agent.Stop(m.sessionID)
	}
}
