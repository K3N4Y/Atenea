package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// inputReservedLines es el alto reservado para la linea de input bajo el viewport.
const inputReservedLines = 1

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

// reservedLines es el alto reservado bajo el transcript: la linea de input y,
// con corrida en curso, la linea de estado del indicador de trabajo.
func (m Model) reservedLines() int {
	if m.working {
		return inputReservedLines + 1
	}
	return inputReservedLines
}

// resizeViewport recalcula el alto del viewport con el ultimo tamano anunciado
// y las lineas reservadas actuales, y re-sincroniza el contenido. Las
// dimensiones se acotan a un minimo de 0: bajo pty el tamano inicial puede ser
// 0x0, o el alto anunciado puede ser menor que las lineas reservadas (terminal
// minuscula), y un alto negativo hace paniquear a bubbles/viewport (slice out
// of range en visibleLines al hacer GotoBottom); con 0 el corte queda vacio y
// no paniquea. Sin tamano conocido (ready == false) es no-op.
func (m Model) resizeViewport() Model {
	if !m.ready {
		return m
	}
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

// View renderiza la conversacion con el input de texto al final. Con corrida
// en curso una linea de estado con el indicador de trabajo precede al input;
// el alto sigue acotado porque reservedLines ya la descuenta del viewport.
func (m Model) View() string {
	status := ""
	if m.working {
		status = statusStyle.Render(workingIndicator) + "\n"
	}
	return m.transcriptView() + status + m.input.View()
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
