package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"atenea/internal/llm"
	"atenea/internal/session"
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

// Marcadores de estado de las entradas de actividad (tools, skills, permisos
// y errores de step): un glifo tras el margen del transcript dice el estado de
// un vistazo; el detalle va debajo como lineas de rail (activityRailPrefix).
const (
	activityRunMarker  = "●" // actividad en ejecucion
	activityOKMarker   = "✓" // actividad terminada con exito
	activityFailMarker = "✗" // actividad terminada con error (tambien errores de step)
	activityAskMarker  = "?" // solicitud de permiso pendiente (aprobar/denegar)
)

// activityNameWidth es el ancho de la columna del nombre en el header de
// actividad: el nombre va alineado a la izquierda a este ancho para que los
// resumenes de headers adyacentes queden en una columna comun legible.
const activityNameWidth = 8

const activityInset = "  "

// activityHeader compone el header de una entrada de actividad: el marcador
// tras activityInset, el nombre alineado a activityNameWidth columnas y el
// resumen (`● bash     ls`). Sin resumen se recortan los espacios colgantes
// de la alineacion para no dejar una cola invisible en la linea.
func activityHeader(marker, name, summary string) string {
	name = sanitizeTerminalText(name)
	summary = sanitizeTerminalText(summary)
	return strings.TrimRight(activityInset+marker+" "+fmt.Sprintf("%-*s", activityNameWidth, name)+" "+summary, " ")
}

// toolOutputPreviewLines es el tope de lineas del preview del output de una
// tool exitosa: acota el detalle para no inundar el transcript; el resto se
// resume en la marca "│ … +N lines".
const toolOutputPreviewLines = 4

// activityRailPrefix es el rail de detalle de las entradas de actividad: cada
// linea bajo el header (output, diff, error, marca de truncado) abre con
// U+2502 + espacio tras activityInset, alineado bajo el marcador del header. En
// el diff los marcadores +/- del propio diff llevan la vista dentro del rail.
const activityRailPrefix = activityInset + "│ "

// toolDiffPreviewLines es el tope de lineas del diff mostrado bajo el header
// de una tool exitosa de edit/write: mas generoso que el preview del output
// (el diff ES el resultado que se quiere revisar) pero acotado igual; el resto
// se resume en la misma marca "│ … +N lines".
const toolDiffPreviewLines = 16

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
	toolDeniedStyle     = lipgloss.NewStyle().Faint(true)
	toolOutputStyle     = lipgloss.NewStyle().Faint(true)                     // preview del output de la tool (detalle, no protagonista)
	diffAddStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // lineas agregadas del diff (+)
	diffDelStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // lineas quitadas del diff (-)
	permissionStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	errorStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	statusStyle         = lipgloss.NewStyle().Faint(true)
	thinkingLabelStyle  = lipgloss.NewStyle().Bold(true) // "◆ Thought"/"◆ Thinking…" label of the thinking block header
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
		text := sanitizeTerminalText(e.text)
		if width > 2*composerOuterMargin {
			contentWidth := max(width-2*composerOuterMargin-userMessageStyle.GetHorizontalFrameSize(), 1)
			text = ansi.Wrap(text, max(contentWidth-ansi.StringWidth(inputPrompt), 1), "")
		}
		lines := strings.Split(text, "\n")
		for index, line := range lines {
			prompt := strings.Repeat(" ", ansi.StringWidth(inputPrompt))
			if index == 0 {
				prompt = userMarkerStyle.Render(inputPrompt)
			}
			lines[index] = prompt + userTextStyle.Render(line)
		}
		return lipgloss.NewStyle().Margin(0, composerOuterMargin).Render(style.Render(strings.Join(lines, "\n")))
	case entryReasoning:
		return e.renderThinking(width)
	case entryTool:
		return e.renderTool()
	case entryPermission:
		return permissionStyle.Render(activityHeader(activityAskMarker, e.tool, summarizeToolInput(e.input)))
	case entryPlanApproval:
		// Misma gramatica de actividad que el permiso (marcador de pregunta y
		// "plan" como nombre), con el gesto que lo resuelve como sufijo.
		return permissionStyle.Render(activityHeader(activityAskMarker, "plan", "presentado") + " (y ejecutar / n seguir en plan)")
	case entryError:
		// El fallo duro del step usa la misma gramatica de actividad, con
		// "error" como nombre y el mensaje como resumen.
		return errorStyle.Render(activityHeader(activityFailMarker, "error", e.text))
	case entryCompaction:
		if e.err != "" {
			return errorStyle.Render("[error] " + sanitizeTerminalText(e.err))
		}
		return statusStyle.Render("[context] " + sanitizeTerminalText(e.text))
	case entryNotice:
		return statusStyle.Render(sanitizeTerminalText(e.text))
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

const thinkingInset = "  "

// renderThinking renders the thinking block (parity with the desktop
// ThinkingBlock). While live or with backlog left to reveal: the header
// "◆ Thinking…" and below it only the last thinkingPreviewLines non-empty
// lines of the revealed text, each line ONE segment (plain assertable
// content); never markdown, it is a glimpse of the thought, not an answer.
// Settled (closed and drained) it collapses to a single summary line
// "◆ Thought for <duration>" — the "◆ Thought" label as one bold segment and
// the duration as a faint one; with expanded set the view renders instead the
// full thinking text under that same header (faint, wrapped to width; see
// toggleThinking). width is the usable viewport width (0 = no wrapping); only
// the expanded body uses it, the other shapes ignore it.
func (e entry) renderThinking(width int) string {
	if !e.settled() {
		lines := []string{thinkingLabelStyle.Render("◆ Thinking…")}
		for _, line := range lastNonEmptyLines(sanitizeTerminalText(e.revealedText()), thinkingPreviewLines) {
			lines = append(lines, statusStyle.Render(line))
		}
		return insetThinking(strings.Join(lines, "\n"))
	}
	summary := thinkingLabelStyle.Render("◆ Thought") + statusStyle.Render(" for "+formatThinkingDuration(e.duration))
	if !e.expanded {
		// Collapsed summary: one line with the hint of the key that expands
		// it. The "◆ Thought" label is stable for the tests; the " ⇧Tab" hint
		// goes at the end so substring asserts keep working.
		return insetThinking(summary + statusStyle.Render(" ⇧Tab"))
	}
	// Expandido: cabecera de resumen seguida del texto completo del
	// pensamiento, envuelto al ancho del viewport (0 = sin envolver) y en
	// estilo tenue, con cada linea como UN segmento asertable.
	body := sanitizeTerminalText(e.revealedText())
	if width > len(thinkingInset) {
		body = ansi.Wrap(body, width-len(thinkingInset), "")
	}
	return insetThinking(strings.Join([]string{summary, statusStyle.Render(body)}, "\n"))
}

func insetThinking(text string) string {
	return thinkingInset + strings.ReplaceAll(text, "\n", "\n"+thinkingInset)
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

// formatThinkingDuration renders the thinking duration short and readable:
// seconds with one decimal under a minute ("0.0s", "3.4s"), otherwise the
// duration rounded to seconds ("1m5s").
func formatThinkingDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// renderTool rinde el bloque de una tool call como entrada de actividad
// (renderActivity): el resumen del Input dice QUE corrio la tool de un
// vistazo; sin resumen el header queda en marcador y nombre pelados. La tool
// "skill" comparte la misma gramatica pero con el nombre de la skill como
// resumen (`● skill    demo`, el campo "name" del Input JSON; sin nombre
// parseable el header queda pelado) y en exito SIN detalle: el cuerpo del
// SKILL.md que viaja en el output es para el modelo, no para el transcript.
func (e entry) renderTool() string {
	if e.tool == "read" {
		label := "Reading"
		if e.status != toolRunning {
			label = "Read"
		}
		return e.renderActivity(label, readFileName(e.input), false)
	}
	if e.tool == "skill" {
		return e.renderActivity("skill", skillName(e.input), false)
	}
	if e.tool == "task" {
		// Subagent launches read as `SubAgent <type>` (the subagent_type of
		// the Input) instead of the raw JSON; success keeps the output
		// preview, which is the subagent's report.
		return e.renderActivity("SubAgent", subagentType(e.input), true)
	}
	return e.renderActivity(e.tool, summarizeToolInput(e.input), true)
}

func readFileName(raw string) string {
	var input struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(raw), &input) != nil || input.Path == "" {
		return ""
	}
	displayPath := input.Path
	if i := strings.LastIndex(displayPath, ":"); i >= 0 {
		selector := displayPath[i+1:]
		if selector != "" && strings.Trim(selector, "0123456789-") == "" {
			displayPath = displayPath[:i]
		}
	}
	return sanitizeTerminalText(path.Base(strings.ReplaceAll(displayPath, "\\", "/")))
}

// renderActivity es el render comun de las entradas de actividad de tool: el
// header `<marcador> <nombre> <resumen>` (ver activityHeader) con el marcador
// de estado tras activityInset y, con showDetail y exito, el detalle (diff u
// output) debajo como lineas de rail. Cada linea es UN segmento para que el
// contenido plano siga siendo asertable.
func (e entry) renderActivity(name, summary string, showDetail bool) string {
	switch e.status {
	case toolOK:
		detail := ""
		if showDetail {
			// Con diff (edit/write) el detalle es el diff, no el output: el
			// output de esas tools es un "ok" sin informacion frente al
			// cambio. El header suma ademas el stat `+N -M` del diff (ver
			// diffStat), separado del resumen por dos espacios.
			detail = renderDiffPreview(e.diff)
			if detail != "" {
				added, removed := diffStat(e.diff)
				summary += "  +" + strconv.Itoa(added) + " -" + strconv.Itoa(removed)
			} else {
				detail = renderOutputPreview(e.output)
			}
		}
		out := toolOKStyle.Render(activityHeader(activityOKMarker, name, summary))
		if detail != "" {
			out += "\n" + detail
		}
		return out
	case toolFailed:
		// El error va debajo del header como linea de rail, no pegado a el.
		return toolFailedStyle.Render(activityHeader(activityFailMarker, name, summary)) +
			"\n" + toolFailedStyle.Render(activityRailPrefix+"error: "+sanitizeTerminalText(e.err))
	case toolDenied:
		return toolDeniedStyle.Render(activityHeader("–", name, "Denied by user"))
	default:
		// A running entry with a live spinner frame (subagents) animates its
		// marker; the rest keep the static run marker.
		marker := activityRunMarker
		if e.spin != "" {
			marker = e.spin
		}
		return toolRunningStyle.Render(activityHeader(marker, name, summary))
	}
}

// diffStat cuenta las lineas agregadas y quitadas de un unified diff: las que
// empiezan con "+"/"-", excluyendo las cabeceras de archivo "+++"/"---". Es
// el stat `+N -M` que el header de una edit/write exitosa muestra junto al
// resumen del Input.
func diffStat(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

// subagentType extracts the "subagent_type" field from the task tool Input
// JSON; invalid JSON or a missing field yields "" and the header stays bare.
func subagentType(raw string) string {
	var input struct {
		Type string `json:"subagent_type"`
	}
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return ""
	}
	return sanitizeTerminalText(input.Type)
}

// skillName extrae el campo "name" del Input JSON de la tool skill; con JSON
// invalido o sin campo devuelve "" y el header de la skill queda pelado.
func skillName(raw string) string {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return ""
	}
	return sanitizeTerminalText(input.Name)
}

// summarizeToolInput resume el JSON del Input de la tool para el header del
// transcript. Con un objeto de EXACTAMENTE un campo con valor string, el
// resumen es ese valor pelado (el caso comun: `{"command":"ls -la"}` se lee
// mejor como `ls -la` que como JSON); en cualquier otro caso es el JSON
// compacto. Sin Input, con JSON invalido o con objeto vacio devuelve "" y el
// header queda en marcador y nombre pelados. Los saltos de linea colapsan a
// espacio y el resultado se trunca a toolInputSummaryWidth celdas: el resumen
// es una pista de una linea, no el input completo.
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
	summary = sanitizeTerminalText(summary)
	summary = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(summary)
	return ansi.Truncate(summary, toolInputSummaryWidth, "…")
}

// renderCappedLines es el esqueleto comun de los previews de detalle de una
// tool: parte el texto en lineas, rinde cada una con renderLine (UN segmento
// contiguo por linea, siguiendo la convencion de los estilos de arriba) hasta
// maxLines lineas y, con mas, cierra con la marca de rail "│ … +N lines"
// (N = ocultas) que acota el detalle para no inundar el transcript. Texto
// vacio o solo whitespace devuelve "" (sin preview).
func renderCappedLines(text string, maxLines int, renderLine func(line string) string) string {
	text = sanitizeTerminalText(text)
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
		rendered = append(rendered, toolOutputStyle.Render(activityRailPrefix+"… +"+strconv.Itoa(hidden)+" lines"))
	}
	return strings.Join(rendered, "\n")
}

// renderOutputPreview rinde el output de una tool exitosa como lineas de rail
// bajo el header: cada linea prefijada con activityRailPrefix y en estilo
// tenue, hasta toolOutputPreviewLines lineas (mas alla, la marca de
// renderCappedLines).
func renderOutputPreview(output string) string {
	return renderCappedLines(output, toolOutputPreviewLines, func(line string) string {
		return toolOutputStyle.Render(activityRailPrefix + line)
	})
}

// renderDiffPreview rinde el diff unificado de Tool.Success (edit/write) bajo
// el header: cada linea con el rail activityRailPrefix tras el margen, las
// lineas "+" en verde, las "-" en rojo y el resto tenue, hasta
// toolDiffPreviewLines lineas (mas generoso que el preview del output: el
// diff ES el resultado que se quiere revisar). Diff vacio o solo whitespace
// devuelve "" (sin detalle, la vista cae al output).
func renderDiffPreview(diff string) string {
	return renderCappedLines(diff, toolDiffPreviewLines, func(line string) string {
		style := toolOutputStyle
		switch {
		case strings.HasPrefix(line, "+"):
			style = diffAddStyle
		case strings.HasPrefix(line, "-"):
			style = diffDelStyle
		}
		return style.Render(activityRailPrefix + line)
	})
}

// markdownRuleWidth is the fixed width of the horizontal rule glyph run:
// glamour renders HR as a literal format string, so the rule cannot follow
// the terminal width. 40 cells reads as a deliberate separator at the usual
// widths without overflowing narrow terminals.
const markdownRuleWidth = 40

// markdownCodeBackground is the subtle background that separates code from
// the conversational flow (visual identity §8), shared by inline code and
// code blocks. ANSI 236 for termenv-styled parts; chroma styles only take
// hex, so markdownCodeBackgroundHex is its truecolor twin.
const (
	markdownCodeBackground    = "236"
	markdownCodeBackgroundHex = "#303030"
)

// markdownCodeBlockMarker brackets each rendered code block so
// paintCodeBlockBackgrounds can find it in glamour's output. NUL bytes never
// survive sanitizeTerminalText, so assistant content cannot forge a marker;
// the marker lines themselves are removed from the final render.
const markdownCodeBlockMarker = "\x00code\x00"

// markdownStyle is the TUI's own glamour theme for assistant markdown. The
// stock dark theme clashes with the TUI identity (indigo H1 chip, literal
// ##/### prefixes, "--------" rules, double-styled links, code blocks with
// no background and an extra indent). This theme sticks to the ANSI palette
// the rest of view.go already uses — accent 6, gray 8 — plus neutral gray
// 236 for subtle backgrounds over the #141414 canvas. Heading hierarchy
// comes from weight (H1 bold accent, H2 bold, H3-H6 bold gray; gray instead
// of faint because glamour's renderText ignores the Faint field), never from
// prefixes or background chips. The document color stays nil so text
// inherits the terminal default. In the Ascii profile (tests, no TTY) all of
// this degrades to plain contiguous text, keeping the content assertable.
var markdownStyle = func() glamouransi.StyleConfig {
	str := func(v string) *string { return &v }
	yes := func() *bool { v := true; return &v }
	num := func(v uint) *uint { return &v }

	// Syntax colors reuse the stock dark chroma set (already curated for a
	// dark background), with the block background on EVERY token entry:
	// chroma's TTY formatters clear the style-level Background before
	// formatting, so a background only renders when each token carries its
	// own. The reflection loop covers every entry so none is left as a hole
	// in the block.
	chromaTheme := *styles.DarkStyleConfig.CodeBlock.Chroma
	entries := reflect.ValueOf(&chromaTheme).Elem()
	for i := 0; i < entries.NumField(); i++ {
		entry := entries.Field(i).Addr().Interface().(*glamouransi.StylePrimitive)
		entry.BackgroundColor = str(markdownCodeBackgroundHex)
	}

	return glamouransi.StyleConfig{
		Document: glamouransi.StyleBlock{
			StylePrimitive: glamouransi.StylePrimitive{BlockPrefix: "\n", BlockSuffix: "\n"},
			Margin:         num(2),
		},
		BlockQuote: glamouransi.StyleBlock{
			Indent:      num(1),
			IndentToken: str("│ "),
		},
		List: glamouransi.StyleList{LevelIndent: 2},
		Heading: glamouransi.StyleBlock{
			StylePrimitive: glamouransi.StylePrimitive{BlockSuffix: "\n\n", Bold: yes()},
		},
		H1:            glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: str("6")}},
		H3:            glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: str("8")}},
		H4:            glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: str("8")}},
		H5:            glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: str("8")}},
		H6:            glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{Color: str("8")}},
		Strikethrough: glamouransi.StylePrimitive{CrossedOut: yes()},
		Emph:          glamouransi.StylePrimitive{Italic: yes()},
		Strong:        glamouransi.StylePrimitive{Bold: yes()},
		HorizontalRule: glamouransi.StylePrimitive{
			Color:  str("8"),
			Format: "\n" + strings.Repeat("─", markdownRuleWidth) + "\n",
		},
		Item:        glamouransi.StylePrimitive{BlockPrefix: "• "},
		Enumeration: glamouransi.StylePrimitive{BlockPrefix: ". "},
		Task:        glamouransi.StyleTask{Ticked: "[✓] ", Unticked: "[ ] "},
		// One single quiet link style: text and URL both underlined accent,
		// instead of the stock green-bold text next to a cyan URL.
		Link:      glamouransi.StylePrimitive{Color: str("6"), Underline: yes()},
		LinkText:  glamouransi.StylePrimitive{Color: str("6"), Underline: yes()},
		Image:     glamouransi.StylePrimitive{Color: str("6"), Underline: yes()},
		ImageText: glamouransi.StylePrimitive{Color: str("8"), Format: "Image: {{.text}} →"},
		Code: glamouransi.StyleBlock{
			StylePrimitive: glamouransi.StylePrimitive{
				Color: str("8"),
			},
		},
		// No CodeBlock margin: the block aligns with the body at the document
		// margin (column 2) instead of the stock extra indent (column 4). The
		// marker lines bracket the block for paintCodeBlockBackgrounds.
		CodeBlock: glamouransi.StyleCodeBlock{
			StyleBlock: glamouransi.StyleBlock{
				StylePrimitive: glamouransi.StylePrimitive{
					BlockPrefix: markdownCodeBlockMarker + "\n",
					BlockSuffix: markdownCodeBlockMarker + "\n",
				},
			},
			Chroma: &chromaTheme,
		},
	}
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

// markdownCodeBlockPadStyle paints the spaces that square a code block line
// up to the width of the block's widest line, in the same background the
// chroma tokens carry.
var markdownCodeBlockPadStyle = lipgloss.NewStyle().Background(lipgloss.Color(markdownCodeBackground))

// paintCodeBlockBackgrounds squares up the background of every code block in
// glamour's rendered output. Chroma's TTY formatters only paint background
// behind the tokens they emit (the style-level background is cleared before
// formatting), which leaves each line's background ragged on the right; this
// pass pads every line of a block — bracketed by markdownCodeBlockMarker
// lines, which are dropped — with background-styled spaces up to the block's
// widest line. Glamour's own right-padding on wrapped code lines is unstyled,
// so it is trimmed before measuring and repainted by the pad; blank code
// lines lose their unstyled document margin to the same trim and get it back
// before the pad so the background never starts at column 0.
func paintCodeBlockBackgrounds(rendered string) string {
	if !strings.Contains(rendered, markdownCodeBlockMarker) {
		return rendered
	}
	isMarker := func(line string) bool { return strings.Contains(line, markdownCodeBlockMarker) }
	lines := strings.Split(rendered, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if !isMarker(lines[i]) {
			out = append(out, lines[i])
			continue
		}
		start := i + 1
		end := start
		for end < len(lines) && !isMarker(lines[end]) {
			end++
		}
		block := lines[start:end]
		width := 0
		for j, line := range block {
			line = strings.TrimRight(line, " ")
			if w := lipgloss.Width(line); w < markdownDocMargin {
				line += strings.Repeat(" ", markdownDocMargin-w)
			}
			block[j] = line
			if w := lipgloss.Width(line); w > width {
				width = w
			}
		}
		for _, line := range block {
			if pad := width - lipgloss.Width(line); pad > 0 {
				line += markdownCodeBlockPadStyle.Render(strings.Repeat(" ", pad))
			}
			out = append(out, line)
		}
		i = end // skip the closing marker; the loop increment moves past it
	}
	return strings.Join(out, "\n")
}

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
	profile  termenv.Profile
	renderer *glamour.TermRenderer
}

// markdownRenderer devuelve el renderer de glamour para el ancho de
// envolvimiento dado (ya descontado el margen del documento), reusando el
// cacheado mientras el ancho no cambie. Reusar el renderer es seguro: cada
// Render de glamour convierte sobre un buffer nuevo, sin estado entre
// llamadas. El perfil de COLOR sigue al de lipgloss, igual que el resto de
// estilos de la vista: sin TTY (tests) es Ascii y el contenido rendido queda
// como texto plano contiguo asertable (glamour con colores parte cada palabra
// en su propio segmento ANSI); en terminal real colorea. El renderer captura
// el perfil al construirse, asi que el cache tambien se clava al perfil: los
// tests que fuerzan un perfil no deben reusar un renderer construido con otro.
func markdownRenderer(wrap int) (*glamour.TermRenderer, error) {
	profile := lipgloss.ColorProfile()
	if markdownRendererCache.renderer != nil && markdownRendererCache.wrap == wrap && markdownRendererCache.profile == profile {
		return markdownRendererCache.renderer, nil
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle),
		glamour.WithWordWrap(wrap),
		glamour.WithColorProfile(profile),
	)
	if err != nil {
		return nil, err
	}
	markdownRendererCache.wrap = wrap
	markdownRendererCache.profile = profile
	markdownRendererCache.renderer = r
	return r, nil
}

// hardWrapOverflow hard-breaks only the lines whose display width exceeds the
// limit, leaving every other line — and its layout and color — byte-identical.
// glamour word-wraps but never splits a token longer than the wrap width, so a
// long URL, path, or code identifier overflows the viewport as a single line;
// the stock emergency word-wrap then re-broke it at column 0 with a blank line
// in front, orphaning the continuation out of rhythm. This breaks the overflow
// inside the line and re-indents every continuation to the line's own leading
// margin, so a wrapped long token stays aligned like any other wrapped line.
// ANSI-aware throughout: widths and breaks count display cells, and
// ansi.Hardwrap carries the active SGR state onto each continuation. limit <= 0
// disables wrapping.
func hardWrapOverflow(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	changed := false
	for i, line := range lines {
		if ansi.StringWidth(line) <= limit {
			continue
		}
		indent, body := splitLeadingSpaces(line)
		if indent >= limit {
			indent = 0
		}
		pad := strings.Repeat(" ", indent)
		segs := strings.Split(ansi.Hardwrap(body, limit-indent, false), "\n")
		for j := range segs {
			segs[j] = pad + segs[j]
		}
		lines[i] = strings.Join(segs, "\n")
		changed = true
	}
	if !changed {
		return s
	}
	return strings.Join(lines, "\n")
}

// splitLeadingSpaces peels a line's leading margin — the run of spaces before
// the first visible cell — off the rest, returning the margin's display width
// and the body with those spaces removed but every ANSI escape kept in place
// (so the body's color state, and thus each continuation's, is unchanged).
func splitLeadingSpaces(line string) (spaces int, body string) {
	var b strings.Builder
	i := 0
	for i < len(line) {
		if line[i] == 0x1b { // ESC: copy a CSI/SGR sequence through to its final byte
			j := i + 1
			if j < len(line) && line[j] == '[' { // skip the CSI intro before the params
				j++
			}
			for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) { // params/intermediates
				j++
			}
			if j < len(line) { // include the final byte (0x40–0x7e)
				j++
			}
			b.WriteString(line[i:j])
			i = j
			continue
		}
		if line[i] == ' ' {
			spaces++
			i++
			continue
		}
		break
	}
	b.WriteString(line[i:])
	return spaces, b.String()
}

// renderMarkdown rinde texto markdown al ancho dado (0 = sin envolver) con
// markdownStyle, fijo: deterministico, sin detectar el fondo de la terminal.
// El envolvimiento se pide al ancho MENOS el margen del documento del estilo:
// glamour pade cada linea a WordWrap + margen. Un token mas ancho que ese hueco
// (URL, ruta o identificador largo) glamour no lo parte y desborda el viewport;
// hardWrapOverflow lo corta antes de pintar los fondos de codigo para que estos
// cuadren dentro del ancho util en vez de dejar palabras huerfanas.
// Ante cualquier error se devuelve el texto tal cual: mejor markdown crudo
// que perder contenido. Los saltos de linea de borde se recortan porque
// renderTranscript ya separa los bloques con "\n\n".
func renderMarkdown(text string, width int) string {
	text = sanitizeTerminalText(text)
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
	out = hardWrapOverflow(out, width)
	return strings.Trim(paintCodeBlockBackgrounds(out), "\n")
}

// compactActivityJoin decide si la entrada cur se une a prev SIN linea en
// blanco: ambas deben ser de actividad (tool, permiso o error de step), que
// forman un grupo compacto de headers fisicamente contiguos; cualquier otra
// vecindad (narrativa del assistant, pensamiento, usuario, compaction)
// conserva el parrafo propio ("\n\n"). renderTranscript y entryLines DEBEN
// compartir esta condicion: entryLines replica el contenido del viewport
// linea a linea para mapear clics a entradas, y si las condiciones
// divergieran la fila clicada dejaria de corresponder a la entrada real.
func compactActivityJoin(prev, cur entry) bool {
	isActivity := func(kind entryKind) bool {
		return kind == entryTool || kind == entryPermission || kind == entryError
	}
	return isActivity(prev.kind) && isActivity(cur.kind)
}

// renderTranscript une los bloques de la conversacion, un parrafo por
// entrada, salvo las entradas de actividad adyacentes, que se agrupan sin
// linea en blanco entre si (ver compactActivityJoin). Pasa el ancho util del
// viewport (0 sin tamano conocido = sin envolver) para que el render markdown
// envuelva al mismo ancho que luego usa syncViewport.
func (m Model) renderTranscript() string {
	width := 0
	if m.ready {
		width = m.viewport.Width
	}
	gated := m.permissionGatedTools()
	var b strings.Builder
	prev := -1
	for i, e := range m.entries {
		if toolGatedByPermission(e, gated) {
			continue
		}
		if prev >= 0 {
			if compactActivityJoin(m.entries[prev], e) {
				b.WriteString("\n")
			} else {
				b.WriteString("\n\n")
			}
		}
		b.WriteString(e.render(width))
		prev = i
	}
	return b.String()
}

// reservedLines es el alto reservado bajo el transcript: la caja del composer
// (alto del textarea + bordes), con menu abierto una linea por item y con
// corrida en curso la linea de estado del indicador de trabajo.
func (m Model) reservedLines() int {
	reserved := m.composerReservedLines() + len(m.menuItems)
	if m.showsWorking() {
		reserved++
	}
	reserved += m.permissionPanelHeight()
	return reserved
}

// showsWorking reports whether the "working" status line is rendered: a run is
// in flight AND no permission is pending. A pending permission blocks on the
// user, so the agent is not working — its panel replaces the line.
func (m Model) showsWorking() bool {
	if _, pending := m.pendingPermission(); pending {
		return false
	}
	return m.working
}

func (m Model) composerReservedLines() int {
	reserved := m.input.Height() + 2
	if _, permissionPending := m.pendingPermission(); !permissionPending {
		reserved += composerOuterMargin
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
	contentHeight := m.bodyHeight()
	if m.chatPanelVisible() {
		contentHeight -= 4
	}
	inputHeight := max(contentHeight-(m.reservedLines()-m.input.Height()), 1)
	m.input.SetHeight(min(m.input.Height(), inputHeight))
	m.viewport.Height = max(contentHeight-m.reservedLines(), 0)
	return m.syncViewport()
}

// syncViewport vuelca el transcript al viewport y sigue la cola solo mientras
// followAgent siga activo. Si el usuario esta leyendo historial, conserva el
// offset y marca actividad nueva cuando cambia el transcript. Antes de
// SetContent el transcript pasa por hardWrapOverflow al ancho del viewport,
// porque el viewport trunca horizontalmente cada linea (ansi.Cut), no envuelve:
// las lineas que exceden el ancho (un token que glamour no supo partir) se
// cortan en su sitio, con la sangria intacta. Ademas deja correcto el conteo de
// lineas que usa GotoBottom. Con ancho <= 0 (terminal minuscula) no se envuelve.
// Sin tamano conocido (ready == false) es no-op.
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
	transcript := hardWrapOverflow(rawTranscript, m.viewport.Width)
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
// con una linea vacia (el "\n\n" de renderTranscript) salvo entre entradas de
// actividad adyacentes, que se agrupan sin separador, y el texto se envuelve al
// ancho del viewport exactamente como syncViewport, de modo que la linea N de
// esta lista es la linea N absoluta del contenido del viewport (la que ocupa la
// fila YOffset+N en pantalla). Sin tamano conocido (ready == false) no envuelve.
func (m Model) entryLines() []entryLine {
	width := 0
	if m.ready {
		width = m.viewport.Width
	}
	gated := m.permissionGatedTools()
	var out []entryLine
	prev := -1
	for i, e := range m.entries {
		// Misma condicion de ocultamiento que renderTranscript: una tool con su
		// permiso pendiente no emite header en ejecucion, y sin esta paridad la
		// numeracion de lineas divergeria y el mapeo de clics se correria.
		if toolGatedByPermission(e, gated) {
			continue
		}
		if prev >= 0 && !compactActivityJoin(m.entries[prev], e) {
			// Separador de parrafo entre bloques (una linea vacia), SOLO
			// cuando renderTranscript separa con "\n\n": la condicion
			// compartida compactActivityJoin evita que ambas numeraciones
			// de lineas diverjan.
			out = append(out, entryLine{idx: -1, line: ""})
		}
		// hardWrapOverflow (mismo que syncViewport) puede partir una linea larga
		// en varias fisicas; cada una es su propio entryLine para que la fila N
		// de esta lista sea la fila N absoluta del viewport y el mapeo de clics
		// no se corra.
		block := hardWrapOverflow(e.render(width), width)
		for _, l := range strings.Split(block, "\n") {
			out = append(out, entryLine{idx: i, line: l})
		}
		prev = i
	}
	return out
}

// View renderiza la conversacion con la caja del composer al final. Con menu
// de autocompletado abierto sus lineas van entre el transcript y la caja
// (antes de la linea de estado); con corrida en curso una linea de estado con
// el indicador de trabajo precede a la caja. El modelo vive en el borde
// inferior y el resumen Git, cuando existe, ocupa la primera fila del margen
// bajo la caja. El alto sigue acotado porque reservedLines ya descuenta todo
// ese chrome del viewport.
func (m Model) View() string {
	if m.modelPicker.open {
		return m.modelPickerView()
	}
	if m.mcpPicker.open {
		return m.mcpPickerView()
	}
	if m.connectPanel.open {
		return m.connectPanelView()
	}
	if m.resumePicker.open {
		return m.resumePickerView()
	}

	var content string
	if m.viewer.active() {
		contentWidth := m.contentWidth()
		if !m.ready {
			contentWidth = -1
		}
		if m.ready && m.treeOpen && m.treePanelWidth() >= m.width {
			contentWidth = m.width
		}
		content = m.renderFileViewer(contentWidth, max(m.bodyHeight(), 0))
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
	canvas := m.renderCanvas(content)
	if !m.ready {
		return canvas
	}
	return m.topBar() + "\n" + canvas
}

func (m Model) resumePickerView() string {
	width := max(m.width, 0)
	height := max(m.height, 0)
	if !m.ready {
		return m.renderCanvas(m.resumePickerSearch(width))
	}

	lines := make([]string, 0, height)
	if height >= 4 {
		lines = append(lines, "")
	}
	for _, line := range strings.Split(m.resumePickerSearch(width), "\n") {
		if len(lines) >= height {
			break
		}
		lines = append(lines, ansi.Truncate(line, width, ""))
	}
	if len(lines) < height {
		lines = append(lines, "")
	}

	visibleRows := max(height-len(lines), 0)
	for _, line := range m.resumePickerBody(visibleRows, max(width-2*composerOuterMargin, 0)) {
		if len(lines) >= height {
			break
		}
		lines = append(lines, strings.Repeat(" ", min(composerOuterMargin, width))+line)
	}

	return m.renderFullCanvas(strings.Join(lines, "\n"))
}

func (m Model) resumePickerSearch(width int) string {
	if width <= 0 {
		return ""
	}

	boxWidth := max(width-2*composerOuterMargin, 0)
	query := m.resumePicker.query
	if boxWidth < 4 {
		query.Width = width
		return ansi.Truncate(query.View(), width, "")
	}

	query.Width = max(boxWidth-4, 0)
	search := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		Width(boxWidth - composerBoxBorderWidth).
		Render(query.View())
	margin := strings.Repeat(" ", composerOuterMargin)
	lines := strings.Split(search, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(margin+line, width, "")
	}
	return strings.Join(lines, "\n")
}

func (m Model) resumePickerBody(visibleRows, width int) []string {
	if visibleRows <= 0 || width <= 0 {
		return nil
	}
	if m.resumePicker.loading {
		return []string{ansi.Truncate(statusStyle.Render("Loading sessions…"), width, "")}
	}
	if m.resumePicker.err != nil {
		message := sanitizeResumePickerLine(m.resumePicker.err.Error())
		return []string{ansi.Truncate(errorStyle.Render(message), width, "")}
	}
	if len(m.resumePicker.filtered) == 0 {
		return []string{ansi.Truncate(statusStyle.Render("No sessions found"), width, "")}
	}

	start := resumePickerWindowStart(len(m.resumePicker.filtered), m.resumePicker.selected, visibleRows)
	end := min(start+visibleRows, len(m.resumePicker.filtered))
	rows := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		rows = append(rows, m.resumePickerRow(m.resumePicker.filtered[index], index == m.resumePicker.selected, width))
	}
	return rows
}

func resumePickerWindowStart(total, selected, visible int) int {
	if total <= visible || visible <= 0 {
		return 0
	}
	selected = min(max(selected, 0), total-1)
	start := selected - visible/2
	return min(max(start, 0), total-visible)
}

func (m Model) resumePickerRow(summary session.SessionSummary, selected bool, width int) string {
	if width <= 0 {
		return ""
	}

	prefix := "  "
	styledPrefix := prefix
	if selected {
		prefix = "❯ "
		styledPrefix = accentStyle.Render("❯") + " "
	}
	prefixWidth := lipgloss.Width(prefix)
	available := max(width-prefixWidth, 0)

	title := sanitizeResumePickerLine(summary.Title)
	if strings.TrimSpace(title) == "" {
		title = "Untitled session"
	}

	date := ""
	if !summary.LastActivity.IsZero() {
		date = formatResumeActivity(summary.LastActivity)
	}
	current := summary.ID == m.resumePicker.currentID
	metadata := date
	if current {
		metadata = "current"
		if date != "" {
			metadata += "  " + date
		}
	}
	if metadata != "" && lipgloss.Width(metadata)+1+min(8, available) > available {
		metadata = ""
	}

	metadataWidth := lipgloss.Width(metadata)
	titleWidth := available
	if metadataWidth > 0 {
		titleWidth = max(available-metadataWidth-1, 0)
	}
	title = ansi.Truncate(title, titleWidth, "…")
	styledTitle := title
	if selected {
		styledTitle = accentStyle.Render(title)
	}

	row := styledPrefix + styledTitle
	if metadataWidth == 0 {
		return ansi.Truncate(row, width, "")
	}
	row += strings.Repeat(" ", max(width-prefixWidth-lipgloss.Width(title)-metadataWidth, 1))
	if current {
		row += statusStyle.Render("current")
		if date != "" {
			row += "  "
		}
	}
	if date != "" {
		if selected {
			row += accentStyle.Render(date)
		} else {
			row += statusStyle.Render(date)
		}
	}
	return ansi.Truncate(row, width, "")
}

func sanitizeResumePickerLine(value string) string {
	return strings.ReplaceAll(sanitizeTerminalText(value), "\n", " ")
}

func (m Model) renderCanvas(content string) string {
	content = restoreCanvasBackground(content)
	if !m.ready {
		// Sin tamano conocido no hay lienzo rectangular que rellenar: el
		// render multi-linea de lipgloss padea cada linea al ancho de la mas
		// larga, un rectangulo arbitrario que ademas cuelga espacios tras los
		// headers de actividad. El fondo se pinta linea a linea (una linea
		// suelta no se padea) hasta que llegue el primer WindowSizeMsg.
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			lines[i] = canvasStyle.Render(line)
		}
		return strings.Join(lines, "\n")
	}
	return canvasStyle.Width(max(m.width, 0)).Height(max(m.bodyHeight(), 0)).Render(content)
}

func (m Model) renderFullCanvas(content string) string {
	content = restoreCanvasBackground(content)
	if !m.ready {
		return m.renderCanvas(content)
	}
	return canvasStyle.Width(max(m.width, 0)).Height(max(m.height, 0)).Render(content)
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
	if m.showsWorking() {
		// La linea de estado es "<glifo> working": el glifo animado del
		// spinner (ya estilizado por su propio Style) seguido del texto en
		// estilo tenue, con " working" como UN segmento para que el
		// contenido plano siga siendo asertable por los tests.
		// Se antepone composerOuterMargin espacios como prefijo plano (no
		// lipgloss.Margin, que tambien agregaria relleno a la derecha) para
		// que el glifo del spinner arranque en la misma columna que el
		// borde "╭" de la caja del composer. El margen se acota al ancho del
		// panel de chat (mismo patron que topBarLine) para que en terminales
		// minusculas el prefijo no ensanche la linea mas alla de la terminal;
		// sin tamano conocido (m.ready == false) queda el margen fijo.
		margin := composerOuterMargin
		if m.ready {
			margin = min(composerOuterMargin, m.chatContentWidth()/2)
		}
		status = strings.Repeat(" ", margin) + m.spinner.View() + statusStyle.Render(" working") + "\n"
	}
	return m.transcriptView() + m.menuView() + status + m.permissionPanelView() + m.composerView()
}

func (m Model) chatView(content string) string {
	innerWidth := max(m.contentWidth()-2, 0)
	content = panelTitle("chat", m.focus == chatFocus) + "\n" + content
	style := treeBorderStyle
	if m.ready {
		style = style.Width(innerWidth).Height(max(m.bodyHeight()-2, 0))
	}
	return style.Render(content)
}

func (m Model) viewerView(width int) string {
	innerWidth := max(width-2, 0)
	content := panelTitle("viewer", m.focus == viewerFocus)
	if file := m.renderFileViewer(innerWidth, max(m.bodyHeight()-3, 0)); file != "" {
		content += "\n" + file
	}
	style := treeBorderStyle
	if m.ready {
		style = style.Width(innerWidth).Height(max(m.bodyHeight()-2, 0))
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
		line := prefix + sanitizeTerminalText(item.label)
		if item.description != "" {
			line += "  " + statusStyle.Render(sanitizeTerminalText(item.description))
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
	width := m.chatContentWidth()
	margin := min(composerOuterMargin, width/2)
	box := m.composerBoxWithWidth(max(width-2*margin, 0))
	box = lipgloss.NewStyle().Margin(0, margin).Render(box)
	if _, permissionPending := m.pendingPermission(); permissionPending {
		return box
	}
	return strings.Join([]string{
		box,
		m.gitSummaryLine(width, margin),
		strings.Repeat(" ", width),
	}, "\n")
}

func (m Model) gitSummaryLine(width, margin int) string {
	innerWidth := max(width-2*margin, 0)
	left := ""
	if m.cancelPending {
		left = ansi.Truncate(statusStyle.Render("Esc again to cancel"), innerWidth, "…")
	}
	leftWidth := ansi.StringWidth(left)
	separatorWidth := 0
	if left != "" {
		separatorWidth = 1
	}
	rightWidth := max(innerWidth-leftWidth-separatorWidth, 0)
	right := m.gitSummaryLabel(rightWidth)
	gap := max(innerWidth-leftWidth-ansi.StringWidth(right), 0)
	return strings.Repeat(" ", margin) + left + strings.Repeat(" ", gap) + right + strings.Repeat(" ", margin)
}

func (m Model) gitSummaryLabel(width int) string {
	if m.gitSummary.Files == 0 || width <= 0 {
		return ""
	}
	fileWord := "files"
	if m.gitSummary.Files == 1 {
		fileWord = "file"
	}
	stats := fmt.Sprintf("+%d  −%d", m.gitSummary.Additions, m.gitSummary.Deletions)
	variants := []string{
		fmt.Sprintf("%d %s changed  %s", m.gitSummary.Files, fileWord, stats),
		fmt.Sprintf("%d %s  %s", m.gitSummary.Files, fileWord, stats),
		stats,
	}
	for index, variant := range variants {
		if ansi.StringWidth(variant) > width {
			continue
		}
		prefix := strings.TrimSuffix(variant, stats)
		styledStats := diffAddStyle.Render(fmt.Sprintf("+%d", m.gitSummary.Additions)) + "  " + diffDelStyle.Render(fmt.Sprintf("−%d", m.gitSummary.Deletions))
		if index == len(variants)-1 {
			return styledStats
		}
		return statusStyle.Render(prefix) + styledStats
	}
	return ""
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
	if m.treeLoading {
		lines = append(lines, statusStyle.Render("cargando workspace…"))
	} else if m.treeError != "" {
		lines = append(lines, statusStyle.Render(sanitizeTerminalText(m.treeError)))
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
			line := strings.Repeat("  ", row.depth) + icon + " " + sanitizeTerminalText(row.node.name)
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
		style = style.Width(innerWidth).Height(max(m.bodyHeight()-2, 0))
	}
	return style.Render(content)
}

func (m Model) treeVisibleRowCount() int {
	if !m.ready {
		return 0
	}
	return max(m.bodyHeight()-4, 0)
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
