package tui

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/llm"
)

// composerBoxBorderWidth es el ancho que los dos bordes laterales de la caja
// suman al contenido (Style.Width de lipgloss fija el ancho del CONTENIDO).
const composerBoxBorderWidth = 2

// composerBoxPadding es el padding horizontal de la caja del composer: una
// celda de espacio entre cada borde lateral y el contenido, para que la linea
// interior renda "│ ❯" (estilo Claude Code) en vez del prompt pegado al
// borde. Style.Width de lipgloss INCLUYE el padding, asi que composerBox no
// lo descuenta del ancho, pero resizeViewport si resta las 2*composerBoxPadding
// celdas al fijar el ancho del textarea.
const composerBoxPadding = 1

const composerOuterMargin = 2

// inputCursorWidth es la celda extra que bubbles/textarea reserva para el
// cursor cuando tiene Width fijado.
const inputCursorWidth = 1

const canvasBackground = "#141414"

const userMessageBackground = "#242424"

// inputPrompt es el caracter de prompt de la linea de input; el historial usa
// el mismo glifo dentro de un bloque gris para distinguir ambos contextos.
const inputPrompt = "❯ "

// toolInputSummaryWidth es el ancho maximo (en celdas) del resumen del Input
// en el header de la tool: suficiente para leer QUE corrio de un vistazo sin
// que un input largo desborde la linea del header.
const toolInputSummaryWidth = 48

// toolOutputPreviewLines es el tope de lineas del preview del output de una
// tool exitosa: acota el detalle para no inundar el transcript; el resto se
// resume en la marca "  … +N lineas".
const toolOutputPreviewLines = 4

// toolOutputPrefix marca cada linea del preview del output bajo el header de
// la tool (dos espacios + U+2502 + espacio), estilo bloque citado.
const toolOutputPrefix = "  │ "

// toolDiffPreviewLines es el tope de lineas del diff mostrado bajo el header
// de una tool exitosa de edit/write: mas generoso que el preview del output
// (el diff ES el resultado que se quiere revisar) pero acotado igual; el resto
// se resume en la misma marca "  … +N lineas".
const toolDiffPreviewLines = 16

// toolDiffPrefix indenta cada linea del diff bajo el header de la tool (dos
// espacios, sin barra: los marcadores +/- del propio diff llevan la vista).
const toolDiffPrefix = "  "

// Estilos de presentacion. Solo envuelven lineas o segmentos ya renderizados,
// sin margenes ni padding, para no alterar el conteo de lineas de la vista.
// Cada linea con marcador se estiliza como UN segmento (o cortando solo donde
// ningun assert busca substrings contiguos), asi el contenido plano que fijan
// los tests nunca se parte con codigos ANSI.
var (
	canvasStyle         = lipgloss.NewStyle().Background(lipgloss.Color(canvasBackground))
	accentStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("6")) // marcador de usuario y prompt del input
	userMessageStyle    = lipgloss.NewStyle().Background(lipgloss.Color(userMessageBackground)).Padding(1, 3)
	userMarkerStyle     = lipgloss.NewStyle().Faint(true)
	userTextStyle       = lipgloss.NewStyle().Background(lipgloss.Color(userMessageBackground))
	toolRunningStyle    = lipgloss.NewStyle().Faint(true)
	toolOKStyle         = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("2"))
	toolFailedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	toolOutputStyle     = lipgloss.NewStyle().Faint(true)                     // preview del output de la tool (detalle, no protagonista)
	diffAddStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // lineas agregadas del diff (+)
	diffDelStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // lineas quitadas del diff (-)
	permissionStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	errorStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	statusStyle         = lipgloss.NewStyle().Faint(true)
	composerBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	treeCursorStyle     = lipgloss.NewStyle().Reverse(true)
	treeBorderStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder())

	// composerBoxStyle es la caja de borde redondeado del composer (estilo
	// Claude Code). Es la excepcion deliberada a la regla de arriba: agrega las
	// dos lineas de borde que reservedLines ya descuenta (composerBoxLines) y
	// el padding horizontal (composerBoxPadding) a cada lado del contenido. El
	// borde queda sin color para que sus caracteres (╭/│/╰) sigan siendo
	// contenido plano asertable por los tests.
	composerBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("8")).
				Padding(0, composerBoxPadding)
)

// render produce la linea del bloque; los marcadores y el contenido son
// estables para que los tests puedan asertar sobre ellos, los estilos solo
// los envuelven. width es el ancho util del viewport (0 = sin envolver): solo
// lo usa el render markdown del assistant, el resto de bloques deja
// el envolvimiento a syncViewport.
func (e entry) render(width int) string {
	switch e.kind {
	case entryUser:
		style := userMessageStyle
		if width > 2*composerOuterMargin {
			style = style.Width(width - 2*composerOuterMargin)
		}
		return lipgloss.NewStyle().Margin(0, composerOuterMargin).Render(style.Render(userMarkerStyle.Render("❯ ") + userTextStyle.Render(e.text)))
	case entryReasoning:
		return e.renderThinking(width)
	case entryTool:
		return e.renderTool()
	case entryPermission:
		return permissionStyle.Render("[permiso] " + e.tool + " " + e.input + " (aprobar/denegar)")
	case entryPlanApproval:
		return permissionStyle.Render("[plan] plan presentado (y ejecutar / n seguir en plan)")
	case entryError:
		return errorStyle.Render("[error] " + e.text)
	default: // entryAssistant: texto plano sin marcador
		// Los bloques asentados se rinden completos; durante el streaming se
		// rinde solo el prefijo revelado para no filtrar el backlog pendiente.
		if e.settled() {
			return renderMarkdown(e.text, width)
		}
		return renderMarkdown(e.revealedText(), width)
	}
}

// thinkingPreviewLines es el tope de lineas del preview del pensamiento en
// curso: una ventana deslizante con las ULTIMAS lineas no vacias del texto
// revelado (paridad con el ThinkingBlock del escritorio).
const thinkingPreviewLines = 4

// renderThinking rinde el bloque de pensamiento (paridad con el ThinkingBlock
// del escritorio). Mientras esta en vivo o queda backlog por revelar: la
// cabecera "[pensando]" y debajo solo las ultimas thinkingPreviewLines lineas
// no vacias del texto revelado, todo en estilo tenue con cada linea como UN
// segmento (contenido plano asertable); nunca markdown, es un vistazo al
// pensamiento y no una respuesta. Asentado (cerrado y drenado) colapsa a una
// unica linea de resumen "[penso <duracion>]"; con expanded en true la vista
// rinde en su lugar el texto completo del pensamiento bajo la cabecera
// "[penso <duracion>]" (tambien tenue, envuelto a width; ver toggleThinking).
// width es el ancho util del viewport (0 = sin envolver); solo lo usa el
// render del pensamiento expandido, el resto de formas ignora el ancho.
func (e entry) renderThinking(width int) string {
	if !e.settled() {
		lines := []string{statusStyle.Render("[pensando]")}
		for _, line := range lastNonEmptyLines(e.revealedText(), thinkingPreviewLines) {
			lines = append(lines, statusStyle.Render(line))
		}
		return strings.Join(lines, "\n")
	}
	summary := statusStyle.Render("[penso " + formatThinkingDuration(e.duration) + "]")
	if !e.expanded {
		// Resumen colapsado: una linea con el hint de la tecla que lo expande.
		// El prefijo "[penso " es estable para los tests; el hint " ⇧Tab" va al
		// final para no romperlos (asertan por substring).
		return summary + statusStyle.Render(" ⇧Tab")
	}
	// Expandido: cabecera de resumen seguida del texto completo del
	// pensamiento, envuelto al ancho del viewport (0 = sin envolver) y en
	// estilo tenue, con cada linea como UN segmento asertable.
	body := e.revealedText()
	if width > 0 {
		body = ansi.Wrap(body, width, "")
	}
	return strings.Join([]string{summary, statusStyle.Render(body)}, "\n")
}

// lastNonEmptyLines devuelve las ultimas limit lineas no vacias (ignorando las
// de solo whitespace) del texto: la ventana deslizante del preview.
func lastNonEmptyLines(text string, limit int) []string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) > limit {
		kept = kept[len(kept)-limit:]
	}
	return kept
}

// formatThinkingDuration rinde la duracion del pensamiento legible y corta:
// "<1s" para menos de un segundo, si no la duracion redondeada a segundos
// (p.ej. "12s", "1m5s").
func formatThinkingDuration(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	return d.Round(time.Second).String()
}

// renderTool rinde el bloque de una tool call: el header con nombre y resumen
// del Input, el estado, y en exito el detalle (diff u output) debajo. La tool
// "skill" tiene su linea dedicada (renderSkill), no el header generico.
func (e entry) renderTool() string {
	if e.tool == "skill" {
		return e.renderSkill()
	}
	// El header lleva el resumen del Input entre parens (`bash(ls)`) para
	// decir QUE corrio la tool de un vistazo; sin resumen queda el nombre
	// pelado, como antes.
	head := "[tool] " + e.tool
	if summary := summarizeToolInput(e.input); summary != "" {
		head += "(" + summary + ")"
	}
	switch e.status {
	case toolOK:
		out := toolOKStyle.Render(head + ": ok")
		// Con diff (edit/write) el detalle es el diff, no el output: el
		// output de esas tools es un "ok" sin informacion frente al cambio.
		detail := renderDiffPreview(e.diff)
		if detail == "" {
			detail = renderOutputPreview(e.output)
		}
		if detail != "" {
			out += "\n" + detail
		}
		return out
	case toolFailed:
		return toolFailedStyle.Render(head + ": error: " + e.err)
	default:
		return toolRunningStyle.Render(head + ": ejecutando")
	}
}

// renderSkill rinde la tool "skill" como linea dedicada `[skill] <nombre>`:
// el nombre es el campo "name" del Input JSON (sin nombre parseable el header
// queda "[skill]" pelado, sin parens). Los sufijos de estado y los estilos son
// los mismos que el resto de tools, con la linea entera como UN segmento para
// que el contenido plano siga siendo asertable. En exito NO se muestra preview
// del output ni diff: el cuerpo del SKILL.md que viaja en el output es para el
// modelo, no para el transcript.
func (e entry) renderSkill() string {
	head := "[skill]"
	if name := skillName(e.input); name != "" {
		head += " " + name
	}
	switch e.status {
	case toolOK:
		return toolOKStyle.Render(head + ": ok")
	case toolFailed:
		return toolFailedStyle.Render(head + ": error: " + e.err)
	default:
		return toolRunningStyle.Render(head + ": ejecutando")
	}
}

// skillName extrae el campo "name" del Input JSON de la tool skill; con JSON
// invalido o sin campo devuelve "" y el header de renderSkill queda pelado.
func skillName(raw string) string {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return ""
	}
	return input.Name
}

// summarizeToolInput resume el JSON del Input de la tool para el header del
// transcript. Con un objeto de EXACTAMENTE un campo con valor string, el
// resumen es ese valor pelado (el caso comun: `{"command":"ls -la"}` se lee
// mejor como `ls -la` que como JSON); en cualquier otro caso es el JSON
// compacto. Sin Input, con JSON invalido o con objeto vacio devuelve "" y el
// header queda sin parens. Los saltos de linea colapsan a espacio y el
// resultado se trunca a toolInputSummaryWidth celdas: el resumen es una
// pista de una linea, no el input completo.
func summarizeToolInput(raw string) string {
	if raw == "" {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || len(fields) == 0 {
		return ""
	}
	summary := ""
	if len(fields) == 1 {
		for _, v := range fields {
			var s string
			if err := json.Unmarshal(v, &s); err == nil {
				summary = s
			}
		}
	}
	if summary == "" {
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(raw)); err != nil {
			return ""
		}
		summary = buf.String()
	}
	summary = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(summary)
	return ansi.Truncate(summary, toolInputSummaryWidth, "…")
}

// renderCappedLines es el esqueleto comun de los previews de detalle de una
// tool: parte el texto en lineas, rinde cada una con renderLine (UN segmento
// contiguo por linea, siguiendo la convencion de los estilos de arriba) hasta
// maxLines lineas y, con mas, cierra con la marca "  … +N lineas" (N =
// ocultas) que acota el detalle para no inundar el transcript. Texto vacio o
// solo whitespace devuelve "" (sin preview).
func renderCappedLines(text string, maxLines int, renderLine func(line string) string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	shown := lines
	if len(shown) > maxLines {
		shown = shown[:maxLines]
	}
	rendered := make([]string, 0, len(shown)+1)
	for _, line := range shown {
		rendered = append(rendered, renderLine(line))
	}
	if hidden := len(lines) - len(shown); hidden > 0 {
		rendered = append(rendered, toolOutputStyle.Render("  … +"+strconv.Itoa(hidden)+" lineas"))
	}
	return strings.Join(rendered, "\n")
}

// renderOutputPreview rinde el output de una tool exitosa como bloque citado
// bajo el header: cada linea prefijada con toolOutputPrefix y en estilo tenue,
// hasta toolOutputPreviewLines lineas (mas alla, la marca de renderCappedLines).
func renderOutputPreview(output string) string {
	return renderCappedLines(output, toolOutputPreviewLines, func(line string) string {
		return toolOutputStyle.Render(toolOutputPrefix + line)
	})
}

// renderDiffPreview rinde el diff unificado de Tool.Success (edit/write) bajo
// el header: cada linea indentada con toolDiffPrefix, las lineas "+" en verde,
// las "-" en rojo y el resto tenue, hasta toolDiffPreviewLines lineas (mas
// generoso que el preview del output: el diff ES el resultado que se quiere
// revisar). Diff vacio o solo whitespace devuelve "" (sin detalle, la vista
// cae al output).
func renderDiffPreview(diff string) string {
	return renderCappedLines(diff, toolDiffPreviewLines, func(line string) string {
		style := toolOutputStyle
		switch {
		case strings.HasPrefix(line, "+"):
			style = diffAddStyle
		case strings.HasPrefix(line, "-"):
			style = diffDelStyle
		}
		return style.Render(toolDiffPrefix + line)
	})
}

// markdownStyle es el estilo del markdown asentado del assistant: el tema
// "dark" de glamour con el color del documento anulado. El gris 252 que fija
// Document.Color en el tema apaga el texto del assistant frente al resto de
// la vista; con nil el documento hereda el color por defecto de la terminal.
// El resto del tema (headings, code, etc.) queda igual.
var markdownStyle = func() glamouransi.StyleConfig {
	s := styles.DarkStyleConfig
	s.Document.StylePrimitive.Color = nil
	return s
}()

// markdownDocMargin es el margen izquierdo del documento del estilo del
// markdown. glamour pade cada linea rendida a WordWrap + este margen:
// renderMarkdown lo descuenta del ancho pedido para que ninguna linea exceda
// el ancho util del viewport. Se lee del propio estilo (no un 2 a mano) para
// seguir cualquier cambio del estilo o de la libreria.
var markdownDocMargin = func() int {
	if m := markdownStyle.Document.Margin; m != nil {
		return int(*m)
	}
	return 0
}()

// markdownRendererCache memoiza el ultimo TermRenderer construido, clavado al
// ancho de envolvimiento con el que se creo. renderTranscript corre en cada
// Update (cada delta del streaming) y rinde cada bloque assistant cerrado:
// construir un renderer de glamour por bloque en cada render es O(bloques)
// construcciones por tecla y lag visible en conversaciones largas. Un solo
// slot alcanza porque el ancho solo cambia con un resize de la terminal. Sin
// lock a proposito: la TUI es una sola instancia y Bubble Tea corre
// Update/View en una sola goroutine, asi que el cache nunca se accede
// concurrentemente.
var markdownRendererCache struct {
	wrap     int
	renderer *glamour.TermRenderer
}

// markdownRenderer devuelve el renderer de glamour para el ancho de
// envolvimiento dado (ya descontado el margen del documento), reusando el
// cacheado mientras el ancho no cambie. Reusar el renderer es seguro: cada
// Render de glamour convierte sobre un buffer nuevo, sin estado entre
// llamadas. El perfil de COLOR sigue al de lipgloss, igual que el resto de
// estilos de la vista: sin TTY (tests) es Ascii y el contenido rendido queda
// como texto plano contiguo asertable (glamour con colores parte cada palabra
// en su propio segmento ANSI); en terminal real colorea.
func markdownRenderer(wrap int) (*glamour.TermRenderer, error) {
	if markdownRendererCache.renderer != nil && markdownRendererCache.wrap == wrap {
		return markdownRendererCache.renderer, nil
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle),
		glamour.WithWordWrap(wrap),
		glamour.WithColorProfile(lipgloss.ColorProfile()),
	)
	if err != nil {
		return nil, err
	}
	markdownRendererCache.wrap = wrap
	markdownRendererCache.renderer = r
	return r, nil
}

// renderMarkdown rinde texto markdown al ancho dado (0 = sin envolver) con
// markdownStyle, fijo: deterministico, sin detectar el fondo de la terminal.
// El envolvimiento se pide al ancho MENOS el margen del documento del estilo:
// glamour pade cada linea a WordWrap + margen, y una linea mas ancha que el
// viewport la re-parte el envolvimiento de emergencia de syncViewport dejando
// palabras huerfanas sin margen en la columna 0.
// Ante cualquier error se devuelve el texto tal cual: mejor markdown crudo
// que perder contenido. Los saltos de linea de borde se recortan porque
// renderTranscript ya separa los bloques con "\n\n".
func renderMarkdown(text string, width int) string {
	wrap := width
	if wrap > 0 {
		wrap = max(wrap-markdownDocMargin, 0)
	}
	r, err := markdownRenderer(wrap)
	if err != nil {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	return strings.Trim(out, "\n")
}

// renderTranscript une los bloques de la conversacion, un parrafo por entrada.
// Pasa el ancho util del viewport (0 sin tamano conocido = sin envolver) para
// que el render markdown envuelva al mismo ancho que luego usa syncViewport.
func (m Model) renderTranscript() string {
	width := 0
	if m.ready {
		width = m.viewport.Width
	}
	parts := make([]string, len(m.entries))
	for i, e := range m.entries {
		parts[i] = e.render(width)
	}
	return strings.Join(parts, "\n\n")
}

// reservedLines es el alto reservado bajo el transcript: la caja del composer
// (alto del textarea + bordes), con menu abierto una linea por item y con
// corrida en curso la linea de estado del indicador de trabajo.
func (m Model) reservedLines() int {
	reserved := m.input.Height() + 2 + composerOuterMargin + len(m.menuItems)
	if m.working {
		reserved++
	}
	return reserved
}

// resizeViewport recalcula el alto del viewport con el ultimo tamano anunciado
// y las lineas reservadas actuales, y re-sincroniza el contenido. Las
// dimensiones se acotan a un minimo de 0: bajo pty el tamano inicial puede ser
// 0x0, o el alto anunciado puede ser menor que las lineas reservadas (terminal
// minuscula), y un alto negativo hace paniquear a bubbles/viewport (slice out
// of range en visibleLines al hacer GotoBottom); con 0 el corte queda vacio y
// no paniquea. Sin tamano conocido (ready == false) es no-op.
//
// Tambien fija el ancho visible del textarea al interior de la caja del
// composer: ancho de la terminal menos los bordes laterales, el padding
// horizontal, el prompt y la celda del cursor que bubbles agrega siempre al
// final. El textarea crece verticalmente hasta composerMaxLines y luego
// scrollea; el ancho se acota a una celda para terminales minusculas.
func (m Model) resizeViewport() Model {
	if !m.ready {
		return m
	}
	m.focus = m.normalizedFocus()
	contentWidth := m.chatContentWidth()
	m.input.SetWidth(max(contentWidth-2*composerOuterMargin-composerBoxBorderWidth-2*composerBoxPadding-inputCursorWidth, 1))
	m.viewport.Width = max(contentWidth, 0)
	contentHeight := m.height
	if m.chatPanelVisible() {
		contentHeight -= 4
	}
	m.viewport.Height = max(contentHeight-m.reservedLines(), 0)
	return m.syncViewport()
}

// syncViewport vuelca el transcript al viewport y sigue la cola solo mientras
// followAgent siga activo. Si el usuario esta leyendo historial, conserva el
// offset y marca actividad nueva cuando cambia el transcript. El transcript se
// envuelve al ancho del viewport antes de
// SetContent porque el viewport trunca horizontalmente cada linea (ansi.Cut),
// no envuelve; pre-envolver ademas deja correcto el conteo de lineas que usa
// GotoBottom. Con ancho <= 0 (terminal minuscula) no se envuelve. Sin tamano
// conocido (ready == false) es no-op.
func (m Model) syncViewport() Model {
	return m.syncViewportContent(false)
}

func (m Model) syncViewportActivity() Model {
	return m.syncViewportContent(true)
}

func (m Model) syncViewportContent(agentActivity bool) Model {
	if !m.ready {
		return m
	}
	rawTranscript := m.renderTranscript()
	contentChanged := rawTranscript != m.lastTranscript
	offset := m.viewport.YOffset
	transcript := rawTranscript
	if m.viewport.Width > 0 {
		transcript = ansi.Wrap(transcript, m.viewport.Width, "")
	}
	m.viewport.SetContent(transcript)
	if m.followAgent {
		m.viewport.GotoBottom()
		m.hasNewActivity = false
	} else {
		m.viewport.SetYOffset(offset)
		if agentActivity && contentChanged {
			m.hasNewActivity = true
		}
	}
	m.lastTranscript = rawTranscript
	return m
}

// entryLine es una linea fisica del contenido del viewport ya envuelto, con el
// indice de la entrada duena de esa linea. Permite mapear una fila clicada del
// viewport a su bloque de conversacion (ver clickThinkingToggle) sin re-derivar
// el texto: replica exactamente el contenido que syncViewport vuelca (mismo
// renderTranscript + ansi.Wrap), asi la numeracion de lineas coincide con la
// que el viewport muestra.
type entryLine struct {
	idx  int // indice en m.entries de la entrada duena; -1 para las lineas
	line string
}

// entryLines reconstruye el contenido del viewport envuelto linea por linea,
// conservando a que entrada pertenece cada linea fisica. Los bloques se separan
// con una linea vacia (el "\n\n" de renderTranscript) y el texto se envuelve al
// ancho del viewport exactamente como syncViewport, de modo que la linea N de
// esta lista es la linea N absoluta del contenido del viewport (la que ocupa la
// fila YOffset+N en pantalla). Sin tamano conocido (ready == false) no envuelve.
func (m Model) entryLines() []entryLine {
	width := 0
	if m.ready {
		width = m.viewport.Width
	}
	var out []entryLine
	for i, e := range m.entries {
		if i > 0 {
			// Separador de parrafo entre bloques (una linea vacia).
			out = append(out, entryLine{idx: -1, line: ""})
		}
		block := e.render(width)
		for _, l := range strings.Split(block, "\n") {
			if width > 0 {
				l = ansi.Wrap(l, width, "")
			}
			out = append(out, entryLine{idx: i, line: l})
		}
	}
	return out
}

// View renderiza la conversacion con la caja del composer al final. Con menu
// de autocompletado abierto sus lineas van entre el transcript y la caja
// (antes de la linea de estado); con corrida en curso una linea de estado con
// el indicador de trabajo precede a la caja; con status fijado una linea de
// pie tenue con el agente y el modelo la sigue. El alto sigue acotado porque
// reservedLines ya las descuenta del viewport.
func (m Model) View() string {
	var content string
	if m.viewer.active() {
		contentWidth := m.contentWidth()
		if !m.ready {
			contentWidth = -1
		}
		if m.ready && m.treeOpen && m.treePanelWidth() >= m.width {
			contentWidth = m.width
		}
		content = m.renderFileViewer(contentWidth, max(m.height, 0))
		if m.treeOpen && (!m.ready || m.treePanelWidth() < m.width) {
			content = lipgloss.JoinHorizontal(lipgloss.Top, m.treeView(), " ", m.viewerView(contentWidth))
		}
	} else {
		content = m.chatContent()
		if m.treeOpen {
			if m.ready && m.treePanelWidth() >= m.width {
				content = m.treeView()
			} else {
				content = lipgloss.JoinHorizontal(lipgloss.Top, m.treeView(), " ", m.chatView(content))
			}
		}
	}
	return m.renderCanvas(content)
}

func (m Model) renderCanvas(content string) string {
	style := canvasStyle
	if m.ready {
		style = style.Width(max(m.width, 0)).Height(max(m.height, 0))
	}
	return style.Render(restoreCanvasBackground(content))
}

func restoreCanvasBackground(content string) string {
	styledMarker := canvasStyle.Render("x")
	background, _, found := strings.Cut(styledMarker, "x")
	if !found || background == "" {
		return content
	}
	content = strings.ReplaceAll(content, "\x1b[0m", "\x1b[0m"+background)
	return strings.ReplaceAll(content, "\x1b[m", "\x1b[m"+background)
}

func (m Model) chatContent() string {
	status := ""
	if m.working {
		// La linea de estado es "<glifo> trabajando": el glifo animado del
		// spinner (ya estilizado por su propio Style) seguido del texto en
		// estilo tenue, con " trabajando" como UN segmento para que el
		// contenido plano siga siendo asertable por los tests.
		status = m.spinner.View() + statusStyle.Render(" trabajando") + "\n"
	}
	return m.transcriptView() + m.menuView() + status + m.composerView()
}

func (m Model) chatView(content string) string {
	innerWidth := max(m.contentWidth()-2, 0)
	content = panelTitle("chat", m.focus == chatFocus) + "\n" + content
	style := treeBorderStyle
	if m.ready {
		style = style.Width(innerWidth).Height(max(m.height-2, 0))
	}
	return style.Render(content)
}

func (m Model) viewerView(width int) string {
	innerWidth := max(width-2, 0)
	content := panelTitle("viewer", m.focus == viewerFocus)
	if file := m.renderFileViewer(innerWidth, max(m.height-3, 0)); file != "" {
		content += "\n" + file
	}
	style := treeBorderStyle
	if m.ready {
		style = style.Width(innerWidth).Height(max(m.height-2, 0))
	}
	return style.Render(content)
}

func panelTitle(name string, active bool) string {
	if active {
		return name + " " + accentStyle.Render("*")
	}
	return name
}

func (m Model) renderFileViewer(width, height int) string {
	contentHeight := max(height-1, 0)
	header := statusStyle.Render(m.viewer.header(width, contentHeight))
	body := m.viewer.render(width, contentHeight)
	if body == "" {
		return header
	}
	return header + "\n" + body
}

// menuView rinde el popup de autocompletado: una linea por item, con el
// prefijo "❯ " (acento) para el seleccionado y dos espacios para el resto,
// luego el label ("/<name>" o la ruta del archivo) y, tras dos espacios, la
// descripcion en estilo tenue (los archivos no llevan: la linea termina en el
// label, sin espacios colgantes). El label es contenido plano asertable; los
// estilos solo envuelven segmentos. Sin menu abierto devuelve "" y la vista no
// agrega lineas.
//
// Cada linea se trunca al ancho de la terminal: reservedLines descuenta UNA
// linea por item, y una linea mas ancha la envolveria el terminal a dos lineas
// reales, dejando corto el alto reservado y roto el layout. El tail "…" senala
// que la ruta o descripcion sigue mas alla del corte. Sin tamano conocido
// (ready == false) o con ancho <= 0 no se trunca, igual que el resto de la
// vista degradada (syncViewport tampoco envuelve).
func (m Model) menuView() string {
	var b strings.Builder
	for i, item := range m.menuItems {
		prefix := "  "
		if i == m.menuSelected {
			prefix = accentStyle.Render("❯ ")
		}
		line := prefix + item.label
		if item.description != "" {
			line += "  " + statusStyle.Render(item.description)
		}
		if width := m.chatContentWidth(); m.ready && width > 0 {
			line = ansi.Truncate(line, width, "…")
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// composerBox envuelve la linea de input en la caja de borde redondeado del
// composer. Con tamano de terminal conocido cada linea de la caja mide
// exactamente el ancho de la terminal: el interior se fija a width - 2
// (Style.Width de lipgloss INCLUYE el padding pero no el borde, que suma
// composerBoxBorderWidth), acotado a >= 0 para terminales minusculas, donde
// Width 0 de lipgloss significa "sin fijar" y la caja queda a ancho natural
// (con su padding), igual que sin tamano conocido.
func (m Model) composerBox() string {
	return m.composerBoxWithWidth(m.chatContentWidth())
}

func (m Model) composerView() string {
	if !m.ready {
		return m.composerBox()
	}
	box := m.composerBoxWithWidth(max(m.chatContentWidth()-2*composerOuterMargin, 0))
	return lipgloss.NewStyle().Margin(0, composerOuterMargin, composerOuterMargin).Render(box)
}

func (m Model) composerBoxWithWidth(width int) string {
	style := composerBoxStyle
	if m.ready {
		style = style.Width(max(width-composerBoxBorderWidth, 0))
	}
	box := style.Render(m.input.View())
	box = decorateComposerBorder(box, 0, m.tokenUsageLabel(), "╭", "╮", true, false)
	return decorateComposerBorder(box, -1, m.composerModelLabel(), "╰", "╯", false, true)
}

func (m Model) composerModelLabel() string {
	if m.planMode && m.model != "" {
		return m.model + " · plan"
	}
	return m.model
}

func decorateComposerBorder(box string, lineIndex int, label, leftCorner, rightCorner string, alignLeft, truncate bool) string {
	if label == "" {
		return box
	}
	lines := strings.Split(box, "\n")
	if lineIndex < 0 {
		lineIndex = len(lines) + lineIndex
	}
	width := ansi.StringWidth(lines[lineIndex])
	const fixedBorderWidth = 5
	labelWidth := width - fixedBorderWidth - 1
	if labelWidth < 2 {
		return box
	}
	if ansi.StringWidth(label) > labelWidth {
		if !truncate {
			return box
		}
		const planSuffix = " · plan"
		if strings.HasSuffix(label, planSuffix) && labelWidth > ansi.StringWidth(planSuffix)+1 {
			model := strings.TrimSuffix(label, planSuffix)
			label = ansi.Truncate(model, labelWidth-ansi.StringWidth(planSuffix), "…") + planSuffix
		} else {
			label = ansi.Truncate(label, labelWidth, "…")
		}
	}
	styledLabel := statusStyle.Render(label)
	remaining := width - ansi.StringWidth(styledLabel) - fixedBorderWidth
	if remaining < 1 {
		return box
	}
	if alignLeft {
		lines[lineIndex] = composerBorderStyle.Render(leftCorner+"─ ") + styledLabel + composerBorderStyle.Render(" "+strings.Repeat("─", remaining)+rightCorner)
	} else {
		lines[lineIndex] = composerBorderStyle.Render(leftCorner+strings.Repeat("─", remaining)+" ") + styledLabel + composerBorderStyle.Render(" ─"+rightCorner)
	}
	return strings.Join(lines, "\n")
}

func (m Model) tokenUsageLabel() string {
	if m.usage == nil {
		return ""
	}
	input := formatTokenCount(m.usage.InputTokens)
	output := formatTokenCount(m.usage.OutputTokens)
	if m.liveUsage {
		input = "~" + input
		if m.usage.OutputTokens > 0 {
			output = "~" + output
		}
	}
	label := "↑ " + input + " ↓ " + output
	if window, ok := llm.ContextWindow(m.model); ok {
		label += " ctx " + input + "/" + formatTokenCount(window)
	}
	return label
}

func formatTokenCount(tokens int) string {
	if tokens < 1_000 {
		return strconv.Itoa(tokens)
	}
	if tokens%1_000 == 0 || tokens >= 10_000 {
		return strconv.Itoa(tokens/1_000) + "k"
	}
	return strings.TrimSuffix(strconv.FormatFloat(float64(tokens)/1_000, 'f', 1, 64), ".0") + "k"
}

func (m Model) chatContentWidth() int {
	width := m.contentWidth()
	if m.chatPanelVisible() {
		width -= 2
	}
	return max(width, 0)
}

func (m Model) chatPanelVisible() bool {
	return m.ready && m.treeOpen && m.treePanelWidth() < m.width
}

func (m Model) contentWidth() int {
	if !m.ready || !m.treeOpen {
		return m.width
	}
	return max(m.width-m.treePanelWidth()-1, 0)
}

func (m Model) treePanelWidth() int {
	if !m.ready || m.width <= 0 {
		return 28
	}
	width := m.width / 4
	width = max(width, 20)
	width = min(width, 36)
	if width+1 >= m.width {
		return max(m.width, 0)
	}
	return width
}

func (m Model) treeView() string {
	panelWidth := m.treePanelWidth()
	innerWidth := max(panelWidth-2, 0)
	lines := []string{panelTitle("explorer", m.focus == explorerFocus && panelWidth < m.width), ""}
	if m.treeError != "" {
		lines = append(lines, statusStyle.Render(m.treeError))
	} else {
		rows := m.tree.visibleRows()
		if len(rows) == 0 {
			lines = append(lines, statusStyle.Render("workspace vacio"))
		}
		start := min(m.treeOffset, len(rows))
		end := len(rows)
		if visibleRows := m.treeVisibleRowCount(); visibleRows > 0 {
			end = min(start+visibleRows, len(rows))
		}
		for i := start; i < end; i++ {
			row := rows[i]
			icon := iconForFile(row.node.name)
			if row.node.dir {
				icon = iconFolderClosed
				if m.tree.expanded[row.node.path] {
					icon = iconFolderOpen
				}
			}
			line := strings.Repeat("  ", row.depth) + icon + " " + row.node.name
			if innerWidth > 0 {
				line = ansi.Truncate(line, innerWidth, "…")
			}
			if i == m.treeCursor {
				line = treeCursorStyle.Render(line)
			}
			lines = append(lines, line)
		}
	}
	content := strings.Join(lines, "\n")
	style := treeBorderStyle
	if m.ready {
		style = style.Width(innerWidth).Height(max(m.height-2, 0))
	}
	return style.Render(content)
}

func (m Model) treeVisibleRowCount() int {
	if !m.ready {
		return 0
	}
	return max(m.height-4, 0)
}

// transcriptView devuelve el transcript con su separador hacia el resto de la
// vista: el viewport de alto acotado cuando el tamano de la terminal es
// conocido, o el render completo como fallback mientras no lo es.
func (m Model) transcriptView() string {
	if m.ready {
		if m.viewport.Height <= 0 {
			return ""
		}
		view := m.viewport.View()
		if m.hasNewActivity {
			view = renderNewActivityIndicator(view, m.viewport.Width)
		}
		return view + "\n"
	}
	if transcript := m.renderTranscript(); transcript != "" {
		return transcript + "\n\n"
	}
	return ""
}

// renderNewActivityIndicator coloca una flecha pasiva en el borde inferior
// derecho del transcript sin agregar filas ni cambiar el alto del viewport.
func renderNewActivityIndicator(view string, width int) string {
	if view == "" || width <= 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	last := len(lines) - 1
	line := ansi.Truncate(lines[last], max(width-1, 0), "")
	line += strings.Repeat(" ", max(width-1-lipgloss.Width(line), 0)) + "↓"
	lines[last] = line
	return strings.Join(lines, "\n")
}
