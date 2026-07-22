package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/llm"
	"atenea/internal/tui/theme"
)

// branchGlyph es el glifo powerline de rama que precede el nombre de la rama
// git en la barra superior (nerd-font PUA, como los iconos del arbol en tree.go).
const branchGlyph = ""

// branchStyle pinta la rama git en verde; el directorio y la etiqueta de
// contexto reutilizan statusStyle (tenue, definido en view.go).
var branchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Success))

// bodyHeight es el espacio vertical del cuerpo (chat/arbol/visor): el alto de
// la terminal menos el chrome de la barra superior. Lo computa el modulo layout
// (computeLayout); este metodo es un seam delgado que lo lee. Usa baseLayout
// (no layout) porque reservedLines depende de bodyHeight: el panel de permiso
// dimensiona contra el, y layout() pide reservedLines — leerlo aca recurse.
func (m Model) bodyHeight() int { return m.baseLayout().bodyHeight }

// topBar rinde el chrome de la barra superior: topBarMargin filas en blanco, la
// fila de la barra y otras topBarMargin filas en blanco, todas con el fondo del
// lienzo. Asi la barra queda separada del borde de la terminal y del cuerpo con
// el mismo margen que el composer usa en sus lados.
func (m Model) topBar() string {
	width := m.baseLayout().width
	blank := restoreCanvasBackground(canvasStyle.Width(width).Render(""))
	rows := make([]string, 0, topBarHeight)
	for range topBarMargin {
		rows = append(rows, blank)
	}
	rows = append(rows, m.topBarLine())
	for range topBarMargin {
		rows = append(rows, blank)
	}
	return strings.Join(rows, "\n")
}

// topBarLine arma la fila de contenido de la barra a todo el ancho: a la
// izquierda la rama git (con su glifo) y el directorio de trabajo, a la derecha
// el uso de contexto (usado / ventana). Lleva el fondo del lienzo compartido
// (#141414) y lo restaura tras los resets de estilo interiores, como el resto
// de la vista.
func (m Model) topBarLine() string {
	left := ""
	if m.branch != "" {
		left = branchStyle.Render(branchGlyph + " " + sanitizeTerminalText(m.branch))
	}
	if m.workDir != "" {
		if left != "" {
			left += "  " + statusStyle.Render(sanitizeTerminalText(m.workDir))
		} else {
			left = statusStyle.Render(sanitizeTerminalText(m.workDir))
		}
	}
	right := m.topBarContext()
	// Mismo margen horizontal externo que el composer y los mensajes de usuario
	// (composerOuterMargin): el contenido no toca los bordes de la terminal y la
	// rama queda alineada con la caja del composer. El clamp a width/2 (para que
	// en terminales minusculas la barra siga midiendo exactamente el ancho) vive
	// en el modulo layout; aca solo se leen el margen y el ancho interior.
	l := m.baseLayout()
	width := l.width
	margin := l.topBarMarginCells
	inner := l.topBarInnerWidth
	if lipgloss.Width(left)+lipgloss.Width(right)+1 > inner {
		// La etiqueta de contexto de la derecha siempre debe caber: se recorta
		// la izquierda (rama + directorio) dejando al menos un espacio de
		// separacion.
		left = ansi.Truncate(left, max(inner-lipgloss.Width(right)-1, 0), "…")
	}
	gap := max(inner-lipgloss.Width(left)-lipgloss.Width(right), 0)
	pad := strings.Repeat(" ", margin)
	line := pad + left + strings.Repeat(" ", gap) + right + pad
	return restoreCanvasBackground(canvasStyle.Width(width).Render(line))
}

// topBarContext arma la etiqueta de uso de contexto de la derecha de la barra:
// los tokens de entrada usados y, si el modelo tiene ventana conocida, la
// ventana total (usado / ventana). Sin usage devuelve "" y la barra no muestra
// nada a la derecha.
func (m Model) topBarContext() string {
	if m.usage == nil {
		return ""
	}
	used := formatTokenCount(m.usage.InputTokens)
	if window := m.contextWindowLabel(); window != "" {
		return statusStyle.Render(used + " / " + window)
	}
	return statusStyle.Render(used)
}

// contextWindowLabel devuelve la ventana de contexto del modelo activo como
// etiqueta ("256k"), o "" si es desconocida. Prefiere el registro canonico de
// llm.ContextWindow; si ahi no esta (los modelos de OpenRouter no lo estan),
// cae al contexto curado del menu de modelos (curatedModelContext), en
// minusculas para casar con el formato de formatTokenCount ("256K" -> "256k").
func (m Model) contextWindowLabel() string {
	if window, ok := llm.ContextWindow(m.model); ok {
		return formatTokenCount(window)
	}
	if curated := curatedModelContext[m.model]; curated != "" {
		return strings.ToLower(curated)
	}
	return ""
}
