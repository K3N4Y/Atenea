// Package tui implementa la interfaz de terminal estilo chat sobre Bubble Tea.
// El Model folda los SessionEvents durables al estado de la conversacion.
//
// El paquete se organiza por responsabilidad: model.go (tipos, estado y
// teclado), fold.go (fold de eventos a entradas), view.go (render, estilos
// y viewport) y reveal.go (smooth streaming del texto del assistant y del
// bloque de pensamiento). El agente headless vive en el subpaquete engine/
// (su contrato en engine/protocol.go, re-exportado aca con alias en
// engine_protocol.go) y la paleta de colores en el subpaquete theme/.
package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/command"
	"atenea/internal/providerconfig"
	"atenea/internal/session"
	"atenea/internal/tui/engine"
)

type UndoDoneMsg struct {
	Result UndoResult
	Err    string
}

type ResumeDoneMsg struct {
	Generation uint64
	SessionID  string
	Result     ResumeResult
	Err        string
}

type ResumeSessionsDoneMsg struct {
	Generation uint64
	Sessions   []session.SessionSummary
	Err        string
}

const resumeResultSessionMismatch = "resume result session mismatch"

type leaderTimeoutMsg struct{ generation uint64 }
type cancelConfirmationExpiredMsg struct{ generation uint64 }

type fileListTarget uint8

const (
	fileListMenu fileListTarget = iota
	fileListTree
)

type filesListedMsg struct {
	target     fileListTarget
	generation uint64
	files      []string
	err        error
}

type fileOpenedMsg struct {
	generation uint64
	path       string
	viewer     fileViewer
}

type workspaceRefreshedMsg struct {
	generation uint64
	branch     string
	summary    gitSummary
}

// Agent es la superficie del engine que la TUI necesita para operar la sesion.
type Agent interface {
	// SendPrompt devuelve la sesion activa y la identidad de la corrida iniciada;
	// /new exacto devuelve una sesion durable distinta sin corrida.
	SendPrompt(sessionID, text string) (RunHandle, error)
	// SendPlanPrompt envia el prompt por el camino de plan-mode.
	SendPlanPrompt(sessionID, text string) (RunHandle, error)
	// AcceptPlan acepta el plan presentado: vuelve a modo normal y ejecuta.
	AcceptPlan(sessionID string) (RunHandle, error)
	Undo(sessionID string) (UndoResult, error)
	ListResumeSessions(currentSessionID string) ([]session.SessionSummary, error)
	ResumeSessionByID(currentSessionID, targetSessionID string) (ResumeResult, error)
	ResolvePermission(sessionID, callID string, approved bool)
	Stop(sessionID string)
}

type modelAgent interface {
	ModelCatalog() []providerconfig.ProviderModels
	CurrentModel() providerconfig.Active
	SelectModel(providerID, model string) (providerconfig.Active, error)
	RefreshModels()
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
	entryCompaction                    // estado transitorio o resultado de compactacion manual
	entryNotice                        // informational line (connected provider, first-run hint)
)

const historyLimit = engine.HistoryLimit

const cancelConfirmationWindow = 2 * time.Second

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
	toolDenied                    // permiso denegado por el usuario; no es fallo del sistema
)

type permissionChoice int

const (
	permissionDeny permissionChoice = iota
	permissionAllowOnce
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
	// tomo pensar, computada al cerrarlo; el resumen colapsado
	// "◆ Thought for <dur>" la rinde legible (ver renderThinking).
	startedAt time.Time
	duration  time.Duration

	// Campos de tool call y de permiso (kind == entryTool / entryPermission):
	callID string
	tool   string
	status toolStatus
	err    string // mensaje de Tool.Failed
	// input es el JSON crudo del Input de la tool: lo llenan la solicitud de
	// permiso y Tool.Called, y en ambos alimenta el resumen del header de
	// actividad (`● bash     ls`, `? bash     ls`) via summarizeToolInput.
	input string
	// spin is the live spinner frame that animates the run marker of a
	// running "task" (subagent) entry; the spinner tick refreshes it while
	// the subagent runs. Empty means the static run marker.
	spin string
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
	activeRun uint64
	events    <-chan tea.Msg
	entries   []entry
	input     composerInput
	working   bool // true desde que arranca una corrida (Enter o aceptar el plan) hasta RunDoneMsg

	cancelPending    bool
	cancelDeadline   time.Time
	cancelGeneration uint64

	// followAgent mantiene el viewport pegado a la cola solo mientras el
	// usuario siga al fondo. Al desplazarse hacia arriba se apaga hasta que el
	// usuario vuelve manualmente al final; hasNewActivity enciende entonces la
	// flecha pasiva que avisa que el transcript siguio creciendo fuera de vista.
	followAgent    bool
	hasNewActivity bool
	lastTranscript string

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
	// lineas reservadas bajo el transcript (caja del composer e indicador de
	// trabajo).
	viewport viewport.Model
	ready    bool
	width    int
	height   int

	// model alimenta la etiqueta del borde inferior del composer; entra una
	// sola vez via WithStatus y sigue fijo por corrida. planMode alterna con
	// Tab y agrega el sufijo "· plan" a esa etiqueta.
	model          string
	usage          *session.Usage
	liveUsage      bool
	outputBytes    int
	reasoningBytes int
	toolInputBytes int

	// branch es la rama git actual que la top bar muestra a la izquierda;
	// "" la oculta. workDir es el directorio de trabajo ya listo para mostrar
	// (home abreviado a ~); "" lo oculta. workspaceRoot conserva la ruta real
	// para refrescar la rama y el resumen de cambios despues de comandos del
	// agente. gitSummary alimenta la fila derecha bajo el composer.
	branch        string
	workDir       string
	workspaceRoot string
	gitSummary    gitSummary
	workspaceGen  uint64

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
	modelSearch  bool
	files        []string
	filesLoaded  bool
	filesLoading bool
	filesError   string
	filesGen     uint64

	// history guarda los ultimos historyLimit prompts enviados (Enter con
	// texto, camino build o plan). Con el composer vacio, Arriba/Abajo los
	// recorren; histIdx == len(history) significa "no navegando". Enviar un
	// prompt resetea la navegacion y bajar despues del mas reciente limpia el
	// composer.
	history []string
	histIdx int

	resumePicker resumePicker
	resumeGen    uint64
	modelPicker  modelPicker
	mcpPicker    mcpPicker
	mcpGen       uint64
	connectPanel connectPanel
	connectGen   uint64

	leaderPending    bool
	leaderGeneration uint64
	treeOpen         bool
	treeLoaded       bool
	tree             fileTree
	treeCursor       int
	treeOffset       int
	treeError        string
	treeLoading      bool
	treeGen          uint64
	fileReader       FileReader
	viewer           fileViewer
	viewerLoading    bool
	viewerGen        uint64
	viewerPending    int
	viewerReturnY    int
	focus            panelFocus
	terminalFocused  bool

	permissionChoice permissionChoice
	permissionScroll int
}

// NewModel construye el Model raiz de la TUI.
func NewModel(agent Agent, sessionID string, events <-chan tea.Msg) Model {
	input := newComposerInput()
	// El spinner comparte el estilo tenue de la linea de estado: el glifo es
	// parte del indicador de trabajo, no un protagonista aparte.
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(statusStyle))
	return Model{agent: agent, sessionID: sessionID, events: events, input: input, spinner: sp, followAgent: true, terminalFocused: true}
}

// WithStatus fija el modelo de IA a mostrar en el borde inferior del composer.
// El nombre del agente se conserva en la firma por compatibilidad con quienes
// construyen el modelo, pero el modo normal ya no agrega una etiqueta propia.
func (m Model) WithStatus(_ string, model string) Model {
	m.model = model
	return m
}

// WithNotice seeds the transcript with a dim informational line shown before
// any conversation. The launcher uses it to point at /connect when the TUI
// starts on the demo provider (no key anywhere).
func (m Model) WithNotice(text string) Model {
	return m.appendNotice(text)
}

// WithWorkspace fija la rama de git y el directorio (ya listo para mostrar,
// con el home abreviado a ~) que la top bar muestra a la izquierda.
func (m Model) WithWorkspace(branch, dir string) Model {
	m.branch = branch
	m.workDir = dir
	return m
}

// WithWorkspaceRoot conserva ademas la ruta real usada para refrescar git.
func (m Model) WithWorkspaceRoot(branch, dir, root string) Model {
	m = m.WithWorkspace(branch, dir)
	m.workspaceRoot = root
	return m
}

func refreshWorkspace(root string, generation uint64) tea.Cmd {
	if root == "" {
		return nil
	}
	return func() tea.Msg {
		branch, _ := gitBranch(root)
		summary, _ := summarizeGitWorkspace(root)
		return workspaceRefreshedMsg{generation: generation, branch: branch, summary: summary}
	}
}

func (m Model) requestWorkspaceRefresh() (Model, tea.Cmd) {
	if m.workspaceRoot == "" {
		return m, nil
	}
	m.workspaceGen++
	return m, refreshWorkspace(m.workspaceRoot, m.workspaceGen)
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

// WithSession restaura el transcript y modo de una sesion durable. El ID de
// sesion y el historial del composer se suministran por separado mediante
// NewModel y WithHistory dentro de la misma cadena de builders.
func (m Model) WithSession(events []session.SessionEvent, mode session.Mode) Model {
	m = m.replaceEvents(events)
	m.planMode = mode == session.ModePlan
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
	return tea.Batch(waitForEvent(m.events), refreshWorkspace(m.workspaceRoot, m.workspaceGen), cursor.Blink)
}

// Update folda cada EventMsg al estado de la conversacion, maneja el teclado y
// rearma la bomba tras cada mensaje consumido del canal.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.update(msg)
	next, ok := updated.(Model)
	if !ok {
		return updated, cmd
	}
	return next, tea.Batch(cmd, next.syncComposerFocus())
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch ev := msg.(type) {
	case EventMsg:
		permissionHeight := m.permissionPanelHeight()
		m = m.foldEvent(ev)
		permissionLayoutChanged := permissionHeight != m.permissionPanelHeight()
		var treeCmd, workspaceCmd tea.Cmd
		if ev.Kind == session.KindToolSuccess && ev.Diff != "" {
			m, treeCmd = m.reloadTree(m.treeOpen)
		}
		if ev.Kind == session.KindToolSuccess && toolMayModifyWorkspace(ev.ToolName) {
			m, workspaceCmd = m.requestWorkspaceRefresh()
		}
		pump := waitForEvent(m.events)
		// Un evento que deja texto sin revelar arranca el loop de ticks del
		// reveal si no hay uno corriendo (ver revealing); el tick viaja
		// batcheado con la bomba de eventos.
		if permissionLayoutChanged {
			m = m.resizeViewport()
		}
		if !m.revealing && m.hasBacklog() {
			m.revealing = true
			return m.syncViewportActivity(), tea.Batch(pump, treeCmd, workspaceCmd, revealTick())
		}
		return m.syncViewportActivity(), tea.Batch(pump, treeCmd, workspaceCmd)
	case workspaceRefreshedMsg:
		if ev.generation < m.workspaceGen {
			return m, nil
		}
		m.workspaceGen = ev.generation
		m.branch = ev.branch
		m.gitSummary = ev.summary
		return m, nil
	case CompactionStatusMsg:
		if ev.SessionID == m.sessionID {
			m = m.foldCompactionStatus(ev)
		}
		return m.syncViewportActivity(), waitForEvent(m.events)
	case RunDoneMsg:
		if ev.SessionID != m.sessionID || ev.RunID != m.activeRun {
			return m, waitForEvent(m.events)
		}
		m.working = false
		m.activeRun = 0
		m.cancelPending = false
		if ev.Err != "" {
			m = m.appendError(ev.Err)
		}
		// Al apagar working la linea de estado desaparece: recalcular el alto.
		return m.resizeViewport(), waitForEvent(m.events)
	case UndoDoneMsg:
		if ev.Err != "" {
			return m.appendError(ev.Err).syncViewport(), nil
		}
		m = m.replaceEvents(ev.Result.Events)
		m.input.SetValue(ev.Result.Prompt)
		m.input.CursorEnd()
		m.menuItems = nil
		m.working = false
		m = m.resizeViewport()
		return m.requestWorkspaceRefresh()
	case ResumeDoneMsg:
		if !m.resumePicker.open || ev.Generation != m.resumeGen || ev.SessionID == "" || ev.SessionID != m.resumePicker.targetID {
			return m, nil
		}
		if ev.Err != "" {
			m.resumePicker.fail(ev.Err)
			return m, nil
		}
		if ev.Result.SessionID != ev.SessionID {
			m.resumePicker.fail(resumeResultSessionMismatch)
			return m, nil
		}
		m.resumePicker.close()
		m.sessionID = ev.Result.SessionID
		m = m.replaceEvents(ev.Result.Events)
		m.planMode = ev.Result.Mode == session.ModePlan
		m.activeRun = 0
		m.working = false
		m.followAgent = true
		m.input.SetValue("")
		m.menuItems = nil
		m.history = append([]string(nil), ev.Result.History...)
		if len(m.history) > historyLimit {
			m.history = m.history[len(m.history)-historyLimit:]
		}
		m.histIdx = len(m.history)
		return m.resizeViewport(), nil
	case ResumeSessionsDoneMsg:
		if !m.resumePicker.open || ev.Generation != m.resumeGen {
			return m, nil
		}
		if ev.Err != "" {
			m.resumePicker.fail(ev.Err)
			return m, nil
		}
		m.resumePicker.setSessions(ev.Sessions)
		return m, nil
	case ModelsRefreshedMsg:
		if m.modelPicker.open {
			m.modelPicker.setProviders(ev.Providers)
			return m, waitForEvent(m.events)
		}
		next, cmd := m.refreshMenu()
		return next, tea.Batch(cmd, waitForEvent(m.events))
	case filesListedMsg:
		switch ev.target {
		case fileListMenu:
			if ev.generation != m.filesGen {
				return m, nil
			}
			m.filesLoading = false
			m.filesLoaded = true
			m.files = ev.files
			if ev.err != nil {
				m.files = nil
				m.filesError = ev.err.Error()
			}
			next, cmd := m.refreshMenu()
			return next, cmd
		case fileListTree:
			if ev.generation != m.treeGen {
				return m, nil
			}
			m.treeLoading = false
			selectedPath := m.selectedTreePath()
			expanded := m.tree.expanded
			m.tree = newFileTree(ev.files)
			for nodePath := range expanded {
				m.tree.expanded[nodePath] = true
			}
			m.treeError = ""
			if ev.err != nil {
				m.tree = newFileTree(nil)
				m.treeError = ev.err.Error()
				return m, nil
			}
			m.treeLoaded = true
			m.selectTreePath(selectedPath)
			m.syncTreeViewport()
			return m, nil
		}
		return m, nil
	case fileOpenedMsg:
		if ev.generation != m.viewerGen || ev.path != m.viewer.path {
			return m, nil
		}
		pendingScroll := m.viewerPending
		m.viewer = ev.viewer
		m.viewerLoading = false
		m.viewerPending = 0
		m.viewer.scroll(pendingScroll, m.fileViewerHeight())
		m.viewer.clamp(m.fileViewerHeight())
		return m, nil
	case revealTickMsg:
		// El loop de reveal muere solo: con el backlog agotado el tick no se
		// reagenda (cmd nil) y un delta posterior lo reinicia. Siempre se
		// re-sincroniza el viewport para que el texto recien revelado se vea.
		m = m.advanceReveal()
		if !m.hasBacklog() {
			m.revealing = false
			return m.syncViewportActivity(), nil
		}
		m.revealing = true
		return m.syncViewportActivity(), revealTick()
	case connectDoneMsg:
		if ev.generation != m.connectGen {
			// A stale failure only mattered to the panel that launched it; a
			// stale success still stored the credential and must land.
			if ev.err == "" {
				return m.applyStaleConnectSuccess(ev)
			}
			return m, nil
		}
		return m.finishConnect(ev)
	case mcpToggleDoneMsg:
		if ev.generation != m.mcpGen {
			return m, nil
		}
		delete(m.mcpPicker.busy, ev.name)
		if ev.err != "" {
			m.mcpPicker.err = ev.err
		}
		if m.mcpPicker.open {
			m.mcpPicker.refreshFromAgent(m.agent)
		}
		return m, nil
	case leaderTimeoutMsg:
		if ev.generation == 0 || ev.generation == m.leaderGeneration {
			m.leaderPending = false
		}
		return m, nil
	case cancelConfirmationExpiredMsg:
		if ev.generation == m.cancelGeneration {
			m.cancelPending = false
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
		// Running subagents animate their transcript marker with the live
		// spinner frame. ponytail: this re-renders the whole transcript per
		// tick while a task runs; cache per-entry renders if CPU ever matters.
		frame := ansi.Strip(m.spinner.View())
		dirty := false
		for i := range m.entries {
			if m.entries[i].kind == entryTool && m.entries[i].tool == "task" && m.entries[i].status == toolRunning && m.entries[i].spin != frame {
				m.entries[i].spin = frame
				dirty = true
			}
		}
		if dirty {
			m = m.syncViewport()
		}
		return m, cmd
	case tea.BlurMsg:
		m.terminalFocused = false
		return m, nil
	case tea.FocusMsg:
		m.terminalFocused = true
		return m, nil
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
		if m.resumePicker.open {
			return m, nil
		}
		if m.modelPicker.open {
			return m.handleModelPickerMouse(ev)
		}
		if m.mcpPicker.open {
			return m.handleMCPPickerMouse(ev)
		}
		if m.connectPanel.open {
			return m.handleConnectPanelMouse(ev)
		}
		// La top bar ocupa la fila 0 de la pantalla, asi que el contenido del
		// cuerpo empieza una fila mas abajo: se traslada el clic a coordenadas
		// del cuerpo antes de leer ev.Y. Los handlers de abajo ya tratan una Y
		// negativa como fallo, asi que un clic sobre la barra queda inerte.
		if m.ready {
			ev.Y -= topBarHeight
		}
		if perm, ok := m.pendingPermission(); ok {
			if next, handled := m.handlePermissionMouse(ev, perm); handled {
				return next, nil
			}
		}
		if m.newActivityIndicatorHit(ev) {
			return m, nil
		}
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
		if m.treeOpen {
			handled, cmd := m.handleTreeMouse(ev)
			if handled {
				return m, cmd
			}
		}
		if ev.Action == tea.MouseActionPress && ev.Button == tea.MouseButtonLeft {
			if viewportLine, ok := m.transcriptLineAtMouse(ev); ok {
				if next, ok := m.toggleThinkingAt(viewportLine); ok {
					return next.syncViewport(), nil
				}
			}
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
		return m.scrollViewport(ev)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) newActivityIndicatorHit(msg tea.MouseMsg) bool {
	if !m.hasNewActivity || !m.ready || msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return false
	}
	if m.treeOpen {
		return false
	}
	return msg.X == m.viewport.Width-1 && msg.Y == m.viewport.Height-1
}

// scrollViewport reenvia msg al viewport para paginar el historial (rueda o
// PgUp/PgDn): nunca escribe en el input ni toca el gate de permisos. Alejarse
// del fondo pausa el seguimiento; volver manualmente al final lo reactiva y
// limpia el indicador de actividad nueva.
func (m Model) scrollViewport(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	m.followAgent = m.viewport.AtBottom()
	if m.followAgent {
		m.hasNewActivity = false
	}
	return m, cmd
}

func (m *Model) scrollFileViewerMouse(msg tea.MouseMsg) {
	if msg.Action != tea.MouseActionPress {
		return
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.viewerLoading {
			m.viewerPending -= 3
			return
		}
		m.viewer.scroll(-3, m.fileViewerHeight())
	case tea.MouseButtonWheelDown:
		if m.viewerLoading {
			m.viewerPending += 3
			return
		}
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
// Esc pide confirmacion y luego detiene la corrida, Enter envia el prompt
// tecleado, Tab alterna el modo build/plan, Up/Down navegan el historial de
// prompts enviados (ver recallHistory) y el resto de teclas alimenta el input
// (y recomputa el popup).
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		m.cancelPending = false
		m.stopRun()
		return m, tea.Quit
	}
	confirmCancel := m.cancelPending && time.Now().Before(m.cancelDeadline)
	m.cancelPending = false
	if m.resumePicker.open {
		return m.handleResumePickerKey(msg)
	}
	if m.modelPicker.open {
		return m.handleModelPickerKey(msg)
	}
	if m.mcpPicker.open {
		return m.handleMCPPickerKey(msg)
	}
	if m.connectPanel.open {
		return m.handleConnectPanelKey(msg)
	}
	if perm, ok := m.pendingPermission(); ok {
		if msg.Type == tea.KeyPgUp || msg.Type == tea.KeyPgDown {
			return m.scrollViewport(msg)
		}
		return m.handlePermissionKey(msg, perm), nil
	}
	if m.hasPendingPlan() {
		return m.resolvePlanKey(msg)
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		return m.handleKeyRuneBatch(msg)
	}
	m.focus = m.normalizedFocus()
	if msg.Type == tea.KeyShiftTab {
		m = m.toggleThinking()
		return m.syncViewport(), nil
	}
	if m.focus == viewerFocus {
		return m.handleFileViewerKey(msg)
	}
	if m.focus == explorerFocus {
		return m.handleTreeKey(msg)
	}
	if msg.Type == tea.KeyPgUp || msg.Type == tea.KeyPgDown {
		return m.scrollViewport(msg)
	}
	if msg.Type == tea.KeyEnter && (m.input.Value() == "/new" || m.input.Value() == "/compact" || m.input.Value() == "/resume") {
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
			return m.applySelection()
		case tea.KeyEnter:
			if m.menuItems[m.menuSelected].builtin && (m.menuItems[m.menuSelected].label == "/new" || m.menuItems[m.menuSelected].label == "/compact" || m.menuItems[m.menuSelected].label == "/resume" || m.menuItems[m.menuSelected].label == "/model" || m.menuItems[m.menuSelected].label == "/mcp" || m.menuItems[m.menuSelected].label == "/connect") {
				m.input.SetValue(m.menuItems[m.menuSelected].label)
				m.input.SetCursor(len([]rune(m.menuItems[m.menuSelected].label)))
				return m.closeMenu().submitPrompt()
			}
			return m.applySelection()
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
			return m.toggleTreeAsync()
		}
		return m, nil
	}
	if m.input.Value() == "" && (msg.Type == tea.KeySpace || keyRune(msg) == " ") {
		return m.startLeader()
	}
	switch msg.Type {
	case tea.KeyEsc:
		if !m.working {
			return m, nil
		}
		if confirmCancel {
			m.stopRun()
			return m, nil
		}
		m.cancelPending = true
		m.cancelDeadline = time.Now().Add(cancelConfirmationWindow)
		m.cancelGeneration++
		generation := m.cancelGeneration
		return m, tea.Tick(cancelConfirmationWindow, func(time.Time) tea.Msg {
			return cancelConfirmationExpiredMsg{generation: generation}
		})
	case tea.KeyEnter:
		return m.submitPrompt()
	case tea.KeyTab:
		// Tab alterna el modo del agente build/plan; nunca llega al textinput
		// (no inserta el caracter de tabulacion en el prompt).
		m.planMode = !m.planMode
		return m, nil
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
	next, refreshCmd := m.refreshMenu()
	return next, tea.Batch(cmd, refreshCmd)
}

func (m Model) handleResumePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.resumePicker.close()
		return m, nil
	case tea.KeyUp:
		m.resumePicker.move(-1)
		return m, nil
	case tea.KeyDown:
		m.resumePicker.move(1)
		return m, nil
	case tea.KeyEnter:
		if m.resumePicker.loading {
			return m, nil
		}
		selected, ok := m.resumePicker.selectedSession()
		if !ok {
			return m, nil
		}
		m.resumePicker.beginLoad(selected.ID)
		currentSessionID := m.sessionID
		targetSessionID := selected.ID
		generation := m.resumeGen
		agent := m.agent
		return m, func() tea.Msg {
			result, err := agent.ResumeSessionByID(currentSessionID, targetSessionID)
			if err != nil {
				return ResumeDoneMsg{Generation: generation, SessionID: targetSessionID, Err: err.Error()}
			}
			return ResumeDoneMsg{Generation: generation, SessionID: targetSessionID, Result: result}
		}
	case tea.KeyPgUp, tea.KeyPgDown:
		return m, nil
	}

	var cmd tea.Cmd
	m.resumePicker.query, cmd = m.resumePicker.query.Update(msg)
	m.resumePicker.filter()
	return m, cmd
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

func (m Model) toggleTreeAsync() (Model, tea.Cmd) {
	m.leaderPending = false
	viewportOffset := m.viewport.YOffset
	m.treeOpen = !m.treeOpen
	if !m.treeOpen {
		m.focus = m.normalizedFocus()
		m = m.resizeViewport()
		m.viewport.SetYOffset(viewportOffset)
		return m, nil
	}
	m.focus = explorerFocus
	m.treeCursor = 0
	m.treeOffset = 0
	m, cmd := m.startTreeLoad()
	m = m.resizeViewport()
	m.viewport.SetYOffset(viewportOffset)
	return m, cmd
}

func (m Model) startTreeLoad() (Model, tea.Cmd) {
	if m.treeLoaded || m.treeLoading {
		return m, nil
	}
	m.treeError = ""
	if m.listFiles == nil {
		m.tree = newFileTree(nil)
		m.treeLoaded = true
		return m, nil
	}
	m.treeLoading = true
	m.treeGen++
	return m, listFilesCmd(m.listFiles, fileListTree, m.treeGen)
}

func (m Model) reloadTree(loadNow bool) (Model, tea.Cmd) {
	m.treeLoaded = false
	if m.treeLoading {
		m.treeLoading = false
		m.treeGen++
	}
	if !loadNow {
		return m, nil
	}
	return m.startTreeLoad()
}

func (m *Model) syncComposerFocus() tea.Cmd {
	if m.modelPicker.open || m.mcpPicker.open {
		m.input.Blur()
		return nil
	}
	if m.resumePicker.open {
		m.input.Blur()
		if m.terminalFocused {
			if !m.resumePicker.query.Focused() {
				return m.resumePicker.query.Focus()
			}
			return nil
		}
		m.resumePicker.query.Blur()
		return nil
	}
	_, permissionPending := m.pendingPermission()
	if m.terminalFocused && m.normalizedFocus() == chatFocus && !permissionPending && !m.hasPendingPlan() {
		if !m.input.Focused() {
			return m.input.Focus()
		}
		return nil
	}
	m.input.Blur()
	return nil
}

func (m Model) handleTreeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.leaderPending {
		m.leaderPending = false
		if keyRune(msg) == "e" {
			return m.toggleTreeAsync()
		}
		return m, nil
	}
	if msg.Type == tea.KeySpace || keyRune(msg) == " " {
		return m.startLeader()
	}
	rows := m.tree.visibleRows()
	switch {
	case msg.Type == tea.KeyEsc || keyRune(msg) == "q":
		// Reuse the toggle's close path so the chat resizes back to full width;
		// setting treeOpen=false alone would freeze the viewport at split width.
		return m.toggleTreeAsync()
	case msg.Type == tea.KeyDown || keyRune(msg) == "j":
		if m.treeCursor < len(rows)-1 {
			m.treeCursor++
		}
	case msg.Type == tea.KeyUp || keyRune(msg) == "k":
		if m.treeCursor > 0 {
			m.treeCursor--
		}
	case keyRune(msg) == "r":
		return m.reloadTree(true)
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
		var cmd tea.Cmd
		m, cmd = m.startOpenTreeFile(node.path)
		m.focus = viewerFocus
		m.syncTreeViewport()
		return m, cmd
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
func (m *Model) handleTreeMouse(msg tea.MouseMsg) (bool, tea.Cmd) {
	if !m.treeMouseOverPanel(msg) {
		return false, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if msg.Action != tea.MouseActionPress {
			return true, nil
		}
		m.moveTreeCursor(-3)
		return true, nil
	case tea.MouseButtonWheelDown:
		if msg.Action != tea.MouseActionPress {
			return true, nil
		}
		m.moveTreeCursor(3)
		return true, nil
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return true, nil
		}
		m.focus = explorerFocus
		row, ok := m.treeRowAtMouse(msg.Y)
		if !ok {
			return true, nil
		}
		m.treeCursor = row
		rows := m.tree.visibleRows()
		node := rows[row].node
		if node.dir {
			m.tree.toggle(node.path)
			m.clampTreeCursor()
			return true, nil
		}
		var cmd tea.Cmd
		*m, cmd = m.startOpenTreeFile(node.path)
		return true, cmd
	}
	return true, nil
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

func (m Model) transcriptLineAtMouse(msg tea.MouseMsg) (int, bool) {
	if !m.ready || m.viewer.active() || msg.Y < 0 {
		return 0, false
	}
	y := msg.Y
	if m.chatPanelVisible() {
		// El chat es la columna derecha: el arbol ocupa [0, treePanelWidth) y una
		// columna de gutter, asi que el transcript arranca en treePanelWidth+1. Sin
		// caja ya no hay borde ni titulo, de modo que la fila 0 del cuerpo es la
		// primera fila del transcript (sin desplazamiento vertical).
		if msg.X < m.treePanelWidth()+1 {
			return 0, false
		}
	}
	if y < 0 || y >= m.viewport.Height {
		return 0, false
	}
	return m.viewport.YOffset + y, true
}

func (m Model) treeMouseOverPanel(msg tea.MouseMsg) bool {
	return m.ready && m.treeVisible() && msg.X >= 0 && msg.X < m.treePanelWidth()
}

func (m Model) treeVisible() bool {
	return m.treeOpen && (!m.viewer.active() || m.treePanelWidth() < m.width)
}

func (m Model) treeRowAtMouse(y int) (int, bool) {
	// Sin caja ni titulo, la primera fila del arbol es la fila 0 del cuerpo.
	const treeRowsStartY = 0
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
	// El visor llena el cuerpo tanto en pantalla completa como en la columna
	// derecha junto al arbol; en ambos casos reserva solo su fila de cabecera.
	return max(m.bodyHeight()-1, 0)
}

func (m Model) startOpenTreeFile(path string) (Model, tea.Cmd) {
	m.viewerReturnY = m.viewport.YOffset
	m.viewerGen++
	m.viewerLoading = true
	m.viewerPending = 0
	m.viewer = fileViewer{path: path, message: "cargando archivo…"}
	if m.fileReader == nil {
		m.viewer = openFileViewerError(path, errors.New("lector de archivos no configurado"))
		m.viewerLoading = false
		return m, nil
	}
	generation := m.viewerGen
	reader := m.fileReader
	return m, func() tea.Msg {
		content, err := reader(path)
		if err != nil {
			return fileOpenedMsg{generation: generation, path: path, viewer: openFileViewerError(path, err)}
		}
		return fileOpenedMsg{generation: generation, path: path, viewer: openFileViewer(path, content)}
	}
}

func listFilesCmd(listFiles func() ([]string, error), target fileListTarget, generation uint64) tea.Cmd {
	return func() tea.Msg {
		files, err := listFiles()
		return filesListedMsg{target: target, generation: generation, files: files, err: err}
	}
}

func (m Model) handleFileViewerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	height := m.fileViewerHeight()
	switch {
	case msg.Type == tea.KeyEsc:
		m.viewer = fileViewer{}
		m.viewerLoading = false
		m.viewerPending = 0
		m.viewport.SetYOffset(m.viewerReturnY)
		m.focus = chatFocus
	case msg.Type == tea.KeyDown || keyRune(msg) == "j":
		if m.viewerLoading {
			m.viewerPending++
			break
		}
		m.viewer.scroll(1, height)
	case msg.Type == tea.KeyUp || keyRune(msg) == "k":
		if m.viewerLoading {
			m.viewerPending--
			break
		}
		m.viewer.scroll(-1, height)
	case msg.Type == tea.KeyPgDown:
		if m.viewerLoading {
			m.viewerPending += max(height, 1)
			break
		}
		m.viewer.scroll(max(height, 1), height)
	case msg.Type == tea.KeyPgUp:
		if m.viewerLoading {
			m.viewerPending -= max(height, 1)
			break
		}
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

func (m Model) selectedTreePath() string {
	rows := m.tree.visibleRows()
	if m.treeCursor < 0 || m.treeCursor >= len(rows) {
		return ""
	}
	return rows[m.treeCursor].node.path
}

func (m *Model) selectTreePath(nodePath string) {
	if nodePath == "" {
		m.clampTreeCursor()
		return
	}
	for i, row := range m.tree.visibleRows() {
		if row.node.path == nodePath {
			m.treeCursor = i
			return
		}
	}
	m.clampTreeCursor()
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
func (m Model) handlePermissionKey(msg tea.KeyMsg, perm entry) Model {
	switch msg.Type {
	case tea.KeyLeft:
		m.permissionChoice = permissionDeny
		return m
	case tea.KeyRight:
		m.permissionChoice = permissionAllowOnce
		return m
	case tea.KeyTab:
		m.permissionChoice = permissionChoice(1 - int(m.permissionChoice))
		return m
	case tea.KeyUp:
		m.permissionScroll = max(m.permissionScroll-1, 0)
		return m
	case tea.KeyDown:
		m.permissionScroll++
		return m
	case tea.KeyEsc:
		return m.resolvePermission(perm, false)
	case tea.KeyEnter:
		return m.resolvePermission(perm, m.permissionChoice == permissionAllowOnce)
	case tea.KeyRunes:
		switch strings.ToLower(string(msg.Runes)) {
		case "y":
			return m.resolvePermission(perm, true)
		case "n":
			return m.resolvePermission(perm, false)
		}
	}
	return m
}

func (m Model) resolvePermission(perm entry, approved bool) Model {
	if m.agent == nil {
		return m
	}
	sessionID := perm.sessionID
	if sessionID == "" {
		sessionID = m.sessionID
	}
	m.agent.ResolvePermission(sessionID, perm.callID, approved)
	m = m.applyPermissionDecision(perm, approved)
	m.permissionChoice = permissionDeny
	m.permissionScroll = 0
	return m.resizeViewport()
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
		run, err := m.agent.AcceptPlan(m.sessionID)
		if err != nil {
			return m.appendError(err.Error()).syncViewport(), nil
		}
		m = m.removePendingPlan()
		m.planMode = false
		m.activeRun = run.RunID
		m.working = run.RunID != 0
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
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "/undo") {
		if trimmed != "/undo" {
			return m.appendError("usage: /undo"), nil
		}
		sessionID := m.sessionID
		agent := m.agent
		return m, func() tea.Msg {
			result, err := agent.Undo(sessionID)
			if err != nil {
				return UndoDoneMsg{Err: err.Error()}
			}
			return UndoDoneMsg{Result: result}
		}
	}
	if strings.HasPrefix(trimmed, "/resume") {
		if trimmed != "/resume" {
			return m.appendError("usage: /resume"), nil
		}
		if m.working {
			return m.appendError(engine.ErrResumeActiveRun.Error()), nil
		}
		sessionID := m.sessionID
		agent := m.agent
		m.input.SetValue("")
		m.menuItems = nil
		m.resumeGen++
		m.resumePicker = newResumePicker(sessionID)
		generation := m.resumeGen
		return m, func() tea.Msg {
			sessions, err := agent.ListResumeSessions(sessionID)
			if err != nil {
				return ResumeSessionsDoneMsg{Generation: generation, Err: err.Error()}
			}
			return ResumeSessionsDoneMsg{Generation: generation, Sessions: sessions}
		}
	}
	if strings.HasPrefix(trimmed, "/mcp") {
		if trimmed != "/mcp" {
			return m.appendError("usage: /mcp"), nil
		}
		if _, ok := m.agent.(mcpAgent); !ok {
			return m.appendError("MCP management is unavailable"), nil
		}
		m.input.SetValue("")
		m.menuItems = nil
		m.mcpGen++
		m.mcpPicker = newMCPPicker()
		m.mcpPicker.refreshFromAgent(m.agent)
		return m.resizeViewport(), nil
	}
	if strings.HasPrefix(trimmed, "/connect") {
		parts := strings.Fields(trimmed)
		if parts[0] != "/connect" || len(parts) > 2 {
			return m.appendError("usage: /connect [provider-id]").syncViewport(), nil
		}
		controller, ok := m.agent.(connectAgent)
		if !ok {
			return m.appendError("provider connection is unavailable").syncViewport(), nil
		}
		providers := controller.ConnectableProviders()
		if len(providers) == 0 {
			return m.appendError("no connectable providers configured").syncViewport(), nil
		}
		panel := newConnectPanel(providers)
		if len(parts) == 2 {
			found := false
			for index, provider := range providers {
				if provider.ID == parts[1] {
					panel.selected = index
					panel.entering = true
					found = true
					break
				}
			}
			if !found {
				return m.appendError(fmt.Sprintf("usage: /connect [provider-id]; %q is not connectable", parts[1])).syncViewport(), nil
			}
		}
		m.input.SetValue("")
		m.menuItems = nil
		m.connectGen++
		m.connectPanel = panel
		return m.resizeViewport(), nil
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/model") {
		controller, ok := m.agent.(modelAgent)
		if !ok {
			return m.appendError("model selection is unavailable"), nil
		}
		parts := strings.Fields(text)
		if len(parts) == 1 && parts[0] == "/model" {
			m.input.SetValue("")
			m.menuItems = nil
			m.modelPicker = newModelPicker(controller.ModelCatalog(), controller.CurrentModel())
			controller.RefreshModels()
			return m.resizeViewport(), nil
		}
		if len(parts) != 3 || parts[0] != "/model" {
			return m.appendError("usage: /model <provider-id> <model-id>"), nil
		}
		active, err := controller.SelectModel(parts[1], parts[2])
		if err != nil {
			return m.appendError(err.Error()), nil
		}
		m.model = active.Model
		m.input.SetValue("")
		m.menuItems = nil
		return m.resizeViewport(), nil
	}
	if text == "/new" {
		run, err := m.agent.SendPrompt(m.sessionID, text)
		if err != nil {
			return m.appendError(err.Error()).syncViewport(), nil
		}
		m.sessionID = run.SessionID
		m.activeRun = 0
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
		m.menuItems = nil
		return m.resizeViewport(), nil
	}
	if text == "/compact" {
		if _, err := m.agent.SendPrompt(m.sessionID, text); err != nil {
			return m.appendError(err.Error()).syncViewport(), nil
		}
		m.input.SetValue("")
		m.menuItems = nil
		return m.resizeViewport(), nil
	}
	var run RunHandle
	var err error
	if m.planMode {
		run, err = m.agent.SendPlanPrompt(m.sessionID, text)
	} else {
		run, err = m.agent.SendPrompt(m.sessionID, text)
	}
	if err != nil {
		return m.appendError(err.Error()).syncViewport(), nil
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
	m.activeRun = run.RunID
	m.working = run.RunID != 0
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
