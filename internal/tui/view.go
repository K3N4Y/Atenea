package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// composerBoxLines es el alto de la caja del composer bajo el viewport: borde
// superior, la linea de input y borde inferior. La caja nunca crece: un prompt
// mas largo que el ancho scrollea horizontal dentro del textinput (ver
// resizeViewport) en vez de envolver a mas lineas.
const composerBoxLines = 3

// composerBoxBorderWidth es el ancho que los dos bordes laterales de la caja
// suman al contenido (Style.Width de lipgloss fija el ancho del CONTENIDO).
const composerBoxBorderWidth = 2

// composerBoxPadding es el padding horizontal de la caja del composer: una
// celda de espacio entre cada borde lateral y el contenido, para que la linea
// interior renda "│ ❯" (estilo Claude Code) en vez del prompt pegado al
// borde. Style.Width de lipgloss INCLUYE el padding, asi que composerBox no
// lo descuenta del ancho, pero resizeViewport si resta las 2*composerBoxPadding
// celdas al fijar el ancho del textinput.
const composerBoxPadding = 1

// inputCursorWidth es la celda extra que bubbles/textinput rende siempre para
// el cursor cuando tiene Width fijado (ademas del prompt y el texto visible).
const inputCursorWidth = 1

// workingIndicator es la linea de estado estable mostrada mientras hay una
// corrida en curso (marcador estatico; la animacion es polish posterior).
const workingIndicator = "... trabajando"

// inputPrompt es el caracter de prompt de la linea de input; distingue a
// simple vista donde se teclea frente al marcador "> " del historial.
const inputPrompt = "❯ "

// Estilos de presentacion. Solo envuelven lineas o segmentos ya renderizados,
// sin margenes ni padding, para no alterar el conteo de lineas de la vista.
// Cada linea con marcador se estiliza como UN segmento (o cortando solo donde
// ningun assert busca substrings contiguos), asi el contenido plano que fijan
// los tests nunca se parte con codigos ANSI.
var (
	accentStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("6")) // marcador de usuario y prompt del input
	userTextStyle    = lipgloss.NewStyle().Bold(true)
	toolRunningStyle = lipgloss.NewStyle().Faint(true)
	toolOKStyle      = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("2"))
	toolFailedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	permissionStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	errorStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	statusStyle      = lipgloss.NewStyle().Faint(true)

	// composerBoxStyle es la caja de borde redondeado del composer (estilo
	// Claude Code). Es la excepcion deliberada a la regla de arriba: agrega las
	// dos lineas de borde que reservedLines ya descuenta (composerBoxLines) y
	// el padding horizontal (composerBoxPadding) a cada lado del contenido. El
	// borde queda sin color para que sus caracteres (╭/│/╰) sigan siendo
	// contenido plano asertable por los tests.
	composerBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				Padding(0, composerBoxPadding)
)

// render produce la linea del bloque; los marcadores y el contenido son
// estables para que los tests puedan asertar sobre ellos, los estilos solo
// los envuelven. width es el ancho util del viewport (0 = sin envolver): solo
// lo usa el render markdown del assistant cerrado, el resto de bloques deja
// el envolvimiento a syncViewport.
func (e entry) render(width int) string {
	switch e.kind {
	case entryUser:
		return accentStyle.Render("> ") + userTextStyle.Render(e.text)
	case entryTool:
		switch e.status {
		case toolOK:
			return toolOKStyle.Render("[tool] " + e.tool + ": ok")
		case toolFailed:
			return toolFailedStyle.Render("[tool] " + e.tool + ": error: " + e.err)
		default:
			return toolRunningStyle.Render("[tool] " + e.tool + ": ejecutando")
		}
	case entryPermission:
		return permissionStyle.Render("[permiso] " + e.tool + " " + e.input + " (aprobar/denegar)")
	case entryPlanApproval:
		return permissionStyle.Render("[plan] plan presentado (y ejecutar / n seguir en plan)")
	case entryError:
		return errorStyle.Render("[error] " + e.text)
	default: // entryAssistant: texto plano sin marcador
		// Solo los bloques CERRADOS se rinden como markdown: el streaming en
		// vivo queda plano porque el markdown parcial de un stream flickea
		// (un ** o un guion a medio llegar cambia de sentido con cada delta)
		// y los tests de streaming asertan el texto plano tal cual llega.
		if !e.live {
			return renderMarkdown(e.text, width)
		}
		return e.text
	}
}

// markdownDocMargin es el margen izquierdo del documento del estilo "dark" de
// glamour. glamour pade cada linea rendida a WordWrap + este margen:
// renderMarkdown lo descuenta del ancho pedido para que ninguna linea exceda
// el ancho util del viewport. Se lee del propio estilo (no un 2 a mano) para
// seguir cualquier cambio del estilo o de la libreria.
var markdownDocMargin = func() int {
	if m := styles.DarkStyleConfig.Document.Margin; m != nil {
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
		glamour.WithStandardStyle("dark"),
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

// renderMarkdown rinde texto markdown al ancho dado (0 = sin envolver) con el
// estilo fijo "dark": deterministico, sin detectar el fondo de la terminal.
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
// (3 lineas), con menu de autocompletado abierto una linea por item, con
// corrida en curso la linea de estado del indicador de trabajo, y con status
// fijado la linea de pie del composer (agente y modelo).
func (m Model) reservedLines() int {
	reserved := composerBoxLines + len(m.menuItems)
	if m.working {
		reserved++
	}
	if m.statusFooter() != "" {
		reserved++
	}
	return reserved
}

// statusFooter es la linea de pie del composer con el agente activo y el
// modelo de IA (formato "<agente> · <modelo>"). En modo plan el agente
// mostrado es "plan" (el pie refleja en vivo el modo alternado con Tab). Sin
// status fijado devuelve "" y la vista no agrega ninguna linea.
func (m Model) statusFooter() string {
	if m.agentName == "" && m.model == "" {
		return ""
	}
	agent := m.agentName
	if m.planMode {
		agent = "plan"
	}
	return agent + " · " + m.model
}

// resizeViewport recalcula el alto del viewport con el ultimo tamano anunciado
// y las lineas reservadas actuales, y re-sincroniza el contenido. Las
// dimensiones se acotan a un minimo de 0: bajo pty el tamano inicial puede ser
// 0x0, o el alto anunciado puede ser menor que las lineas reservadas (terminal
// minuscula), y un alto negativo hace paniquear a bubbles/viewport (slice out
// of range en visibleLines al hacer GotoBottom); con 0 el corte queda vacio y
// no paniquea. Sin tamano conocido (ready == false) es no-op.
//
// Tambien fija el ancho visible del textinput al interior de la caja del
// composer: ancho de la terminal menos los bordes laterales, el padding
// horizontal, el prompt y la celda del cursor que bubbles agrega siempre al
// final. Con Width > 0 el textinput scrollea horizontal en vez de crecer, y la
// caja se mantiene en 3 lineas. Acotado a >= 0: en terminales minusculas
// Width 0 desactiva el scroll (textinput a ancho natural, vista degradada pero
// sin panic).
func (m Model) resizeViewport() Model {
	if !m.ready {
		return m
	}
	m.input.Width = max(m.width-composerBoxBorderWidth-2*composerBoxPadding-ansi.StringWidth(inputPrompt)-inputCursorWidth, 0)
	m.viewport.Width = max(m.width, 0)
	m.viewport.Height = max(m.height-m.reservedLines(), 0)
	return m.syncViewport()
}

// syncViewport vuelca el transcript al viewport y sigue la cola (auto-scroll
// al fondo). El transcript se envuelve al ancho del viewport antes de
// SetContent porque el viewport trunca horizontalmente cada linea (ansi.Cut),
// no envuelve; pre-envolver ademas deja correcto el conteo de lineas que usa
// GotoBottom. Con ancho <= 0 (terminal minuscula) no se envuelve. Sin tamano
// conocido (ready == false) es no-op.
func (m Model) syncViewport() Model {
	if !m.ready {
		return m
	}
	transcript := m.renderTranscript()
	if m.viewport.Width > 0 {
		transcript = ansi.Wrap(transcript, m.viewport.Width, "")
	}
	m.viewport.SetContent(transcript)
	m.viewport.GotoBottom()
	return m
}

// View renderiza la conversacion con la caja del composer al final. Con menu
// de autocompletado abierto sus lineas van entre el transcript y la caja
// (antes de la linea de estado); con corrida en curso una linea de estado con
// el indicador de trabajo precede a la caja; con status fijado una linea de
// pie tenue con el agente y el modelo la sigue. El alto sigue acotado porque
// reservedLines ya las descuenta del viewport.
func (m Model) View() string {
	status := ""
	if m.working {
		status = statusStyle.Render(workingIndicator) + "\n"
	}
	footer := ""
	if f := m.statusFooter(); f != "" {
		footer = "\n" + statusStyle.Render(f)
	}
	return m.transcriptView() + m.menuView() + status + m.composerBox() + footer
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
		if m.ready && m.width > 0 {
			line = ansi.Truncate(line, m.width, "…")
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
	style := composerBoxStyle
	if m.ready {
		style = style.Width(max(m.width-composerBoxBorderWidth, 0))
	}
	return style.Render(m.input.View())
}

// transcriptView devuelve el transcript con su separador hacia el resto de la
// vista: el viewport de alto acotado cuando el tamano de la terminal es
// conocido, o el render completo como fallback mientras no lo es.
func (m Model) transcriptView() string {
	if m.ready {
		return m.viewport.View() + "\n"
	}
	if transcript := m.renderTranscript(); transcript != "" {
		return transcript + "\n\n"
	}
	return ""
}
