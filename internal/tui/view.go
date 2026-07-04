package tui

import (
	"strings"

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
// los envuelven.
func (e entry) render() string {
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
	case entryError:
		return errorStyle.Render("[error] " + e.text)
	default: // entryAssistant: texto plano sin marcador
		return e.text
	}
}

// renderTranscript une los bloques de la conversacion, un parrafo por entrada.
func (m Model) renderTranscript() string {
	parts := make([]string, len(m.entries))
	for i, e := range m.entries {
		parts[i] = e.render()
	}
	return strings.Join(parts, "\n\n")
}

// reservedLines es el alto reservado bajo el transcript: la caja del composer
// (3 lineas), con corrida en curso la linea de estado del indicador de
// trabajo, y con status fijado la linea de pie del composer (agente y modelo).
func (m Model) reservedLines() int {
	reserved := composerBoxLines
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

// View renderiza la conversacion con la caja del composer al final. Con
// corrida en curso una linea de estado con el indicador de trabajo precede a
// la caja; con status fijado una linea de pie tenue con el agente y el modelo
// la sigue. El alto sigue acotado porque reservedLines ya las descuenta del
// viewport.
func (m Model) View() string {
	status := ""
	if m.working {
		status = statusStyle.Render(workingIndicator) + "\n"
	}
	footer := ""
	if f := m.statusFooter(); f != "" {
		footer = "\n" + statusStyle.Render(f)
	}
	return m.transcriptView() + status + m.composerBox() + footer
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
