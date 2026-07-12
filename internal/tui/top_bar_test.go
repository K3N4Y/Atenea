package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/llm"
	"atenea/internal/session"
)

// TestModel_TopBarShowsBranchDirectoryAndContextUsage verifica que, una vez
// listo el modelo, la primera linea de View() es la barra superior con la rama
// git, el directorio de trabajo y el uso de contexto (usado / ventana).
func TestModel_TopBarShowsBranchDirectoryAndContextUsage(t *testing.T) {
	m := NewModel(nil, "s1", nil).
		WithWorkspace("main", "~/dev/atenea").
		WithStatus("build", "anthropic/claude-opus-4.8")

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{InputTokens: 16000}})

	first := lineWith(t, ansi.Strip(m.View()), "main")

	if !strings.Contains(first, "main") {
		t.Fatalf("la barra superior debe mostrar la rama %q; primera linea = %q", "main", first)
	}
	if !strings.Contains(first, "~/dev/atenea") {
		t.Fatalf("la barra superior debe mostrar el directorio %q; primera linea = %q", "~/dev/atenea", first)
	}
	if !strings.Contains(first, "16k / 200k") {
		t.Fatalf("la barra superior debe mostrar el uso de contexto %q; primera linea = %q", "16k / 200k", first)
	}
}

// TestModel_TopBarBranchLeadsWithGlyph verifica que el nombre de la rama va
// precedido por el glifo de rama (branchGlyph): un glifo vacio dejaria la rama
// pelada, asi que este caso ancla que el icono se emite antes del nombre.
func TestModel_TopBarBranchLeadsWithGlyph(t *testing.T) {
	if branchGlyph == "" {
		t.Fatal("branchGlyph no debe ser vacio: la rama de la barra superior necesita su icono")
	}
	m := NewModel(nil, "s1", nil).WithWorkspace("main", "~/dev/atenea")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})

	first := lineWith(t, ansi.Strip(m.View()), "main")
	want := branchGlyph + " main"
	if !strings.Contains(first, want) {
		t.Fatalf("la barra superior debe mostrar el glifo de rama antes del nombre (%q); primera linea = %q", want, first)
	}
}

// TestModel_TopBarKeepsTotalHeight verifica que la barra superior se dibuja
// dentro del chrome y que no aumenta la altura total: View() sigue midiendo
// exactamente Height lineas (el chrome de la barra sale del cuerpo, no anade
// filas extra).
func TestModel_TopBarKeepsTotalHeight(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithWorkspace("main", "~/dev/atenea")

	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	first := lineWith(t, ansi.Strip(m.View()), "main")
	if !strings.Contains(first, "main") {
		t.Fatalf("la barra superior debe mostrar la rama %q; linea de la barra = %q", "main", first)
	}

	if got := strings.Count(m.View(), "\n") + 1; got != 12 {
		t.Fatalf("View() debe medir 12 lineas (la barra no aumenta la altura); midio %d", got)
	}
}

// TestModel_TopBarContextFallsBackToCuratedWindow verifica que, cuando el modelo
// no esta en el registro canonico de llm.ContextWindow (p.ej. los modelos de
// OpenRouter), la barra usa la ventana curada del menu de modelos
// (curatedModelContext) y la muestra en minusculas ("9k / 256k"), en vez de
// mostrar solo los tokens usados.
func TestModel_TopBarContextFallsBackToCuratedWindow(t *testing.T) {
	const curatedModel = "cohere/north-mini-code:free"
	if _, ok := llm.ContextWindow(curatedModel); ok {
		t.Fatalf("precondicion: %q no debe estar en llm.ContextWindow (debe caer al curado)", curatedModel)
	}
	if curatedModelContext[curatedModel] == "" {
		t.Fatalf("precondicion: %q debe tener contexto curado registrado", curatedModel)
	}

	m := NewModel(nil, "s1", nil).
		WithWorkspace("main", "~/x").
		WithStatus("build", curatedModel)

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{InputTokens: 9000}})

	first := lineWith(t, ansi.Strip(m.View()), "256k")

	if !strings.Contains(first, "9k / 256k") {
		t.Fatalf("con ventana curada la barra debe mostrar %q; primera linea = %q", "9k / 256k", first)
	}
}

// TestModel_TopBarContextShowsUsedOnlyWhenWindowUnknown verifica que, cuando el
// modelo no tiene ventana de contexto conocida, la etiqueta de la derecha
// muestra solo los tokens usados (p.ej. "16k") sin la forma "usado / ventana".
// Cae contra una implementacion que asuma siempre ventana conocida o que
// concatene ciegamente " / ".
func TestModel_TopBarContextShowsUsedOnlyWhenWindowUnknown(t *testing.T) {
	const unknownModel = "demo"
	if _, ok := llm.ContextWindow(unknownModel); ok {
		t.Fatalf("precondicion: %q debe tener ventana desconocida (ContextWindow ok=false)", unknownModel)
	}

	m := NewModel(nil, "s1", nil).
		WithWorkspace("main", "~/x").
		WithStatus("build", unknownModel)

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{InputTokens: 16000}})

	first := lineWith(t, ansi.Strip(m.View()), "16k")

	if !strings.Contains(first, "16k") {
		t.Fatalf("con ventana desconocida la barra debe mostrar los tokens usados %q; primera linea = %q", "16k", first)
	}
	if strings.Contains(first, "16k /") {
		t.Fatalf("con ventana desconocida la barra NO debe mostrar la forma %q (no hay ventana); primera linea = %q", "16k /", first)
	}
}

// TestModel_TopBarWithoutUsageHasNoContextLabel verifica que, sin evento de
// usage (m.usage == nil), topBarContext() devuelve "" y la barra no pinta
// ninguna etiqueta de contexto a la derecha: ni la forma "usado / ventana" ni un
// conteo de tokens suelto. Cae contra una implementacion que muestre un valor
// por defecto (p.ej. "0" o "0 / 200k") cuando aun no hay uso.
func TestModel_TopBarWithoutUsageHasNoContextLabel(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithWorkspace("main", "~/x")

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})

	first := strings.TrimRight(lineWith(t, ansi.Strip(m.View()), "main"), " ")

	if !strings.Contains(first, "main") {
		t.Fatalf("la barra debe mostrar la rama %q sin usage; primera linea = %q", "main", first)
	}
	if !strings.Contains(first, "~/x") {
		t.Fatalf("la barra debe mostrar el directorio %q sin usage; primera linea = %q", "~/x", first)
	}
	if strings.Contains(first, " / ") {
		t.Fatalf("sin usage la barra NO debe mostrar la forma %q; primera linea = %q", " / ", first)
	}
	if strings.Contains(first, "k ") {
		t.Fatalf("sin usage la barra NO debe mostrar un conteo de tokens %q; primera linea = %q", "k ", first)
	}
}

// TestModel_TopBarWithoutBranchOrDirStillFillsWidth verifica que, sin rama ni
// directorio (WithWorkspace("", "")), la barra sigue siendo un lienzo del ancho
// completo y no rompe la invariante del canvas: View() mide exactamente Height
// lineas y todas (incluida la barra) miden Width celdas. Cae contra una barra
// que colapse a ancho 0 cuando la izquierda esta vacia.
func TestModel_TopBarWithoutBranchOrDirStillFillsWidth(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithWorkspace("", "")

	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	lines := strings.Split(m.View(), "\n")
	if len(lines) != 12 {
		t.Fatalf("View() debe medir 12 lineas sin rama ni directorio; midio %d", len(lines))
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w != 40 {
			t.Fatalf("la linea %d debe medir exactamente 40 celdas (invariante del lienzo); midio %d (%q)", i, w, ansi.Strip(line))
		}
	}
}

// TestModel_TopBarTruncatesLeftToFitContextOnNarrowWidth verifica que, cuando el
// ancho no basta, la etiqueta de contexto de la derecha ("16k / 200k") siempre
// sobrevive intacta y es la izquierda (rama + directorio largo) la que se recorta
// con una elipsis, sin que la barra desborde el ancho. Cae contra una
// implementacion que recorte por la derecha o deje la barra mas ancha que la
// terminal.
func TestModel_TopBarTruncatesLeftToFitContextOnNarrowWidth(t *testing.T) {
	m := NewModel(nil, "s1", nil).
		WithWorkspace("main", "~/some/very/long/working/directory/path/that/will/not/fit").
		WithStatus("build", "anthropic/claude-opus-4.8")

	m = apply(t, m, tea.WindowSizeMsg{Width: 30, Height: 12})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{InputTokens: 16000}})

	idx := lineIndexWith(t, ansi.Strip(m.View()), "/ 200k")
	rendered := strings.Split(m.View(), "\n")[idx]
	if w := lipgloss.Width(rendered); w != 30 {
		t.Fatalf("la barra no debe desbordar el ancho: midio %d celdas, se esperaban 30 (%q)", w, ansi.Strip(rendered))
	}

	first := ansi.Strip(rendered)
	if !strings.Contains(first, "16k / 200k") {
		t.Fatalf("la etiqueta de contexto %q debe sobrevivir entera al recorte; primera linea = %q", "16k / 200k", first)
	}
	if !strings.Contains(first, "…") {
		t.Fatalf("la izquierda larga debe recortarse con elipsis %q; primera linea = %q", "…", first)
	}
}

// TestModel_TopBarRowClickIsInertBodyRowClickHits verifica el offset de la barra
// en el manejo del raton: un clic sobre la fila 0 (la barra) es inerte, mientras
// que un clic sobre la fila del cuerpo donde vive el resumen del pensamiento lo
// expande. Con la barra ocupando la fila 0, un off-by-one (sin restar
// topBarHeight) haria que el clic sobre la barra togglee por error el primer
// contenido del cuerpo. Se usan dos sub-modelos independientes para que la parte
// A no contamine la B.
func TestModel_TopBarRowClickIsInertBodyRowClickHits(t *testing.T) {
	build := func(t *testing.T) Model {
		t.Helper()
		m := NewModel(nil, "s1", nil)
		m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
		text := "razon-1\nrazon-2\nrazon-3"
		m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
		m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
		m = drainReveal(t, m)
		m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
		m = drainReveal(t, m)
		// Transcript corto mostrado desde arriba: la fila Y en pantalla coincide
		// con la linea absoluta del contenido.
		if m.viewport.YOffset != 0 {
			t.Fatalf("viewport.YOffset = %d, want 0: el transcript corto se muestra desde arriba", m.viewport.YOffset)
		}
		return m
	}

	// Parte A (inerte): un clic sobre la fila 0 (la barra) no debe expandir el
	// pensamiento. El resumen sigue colapsado ("[penso ") y NO aparece el cuerpo.
	mA := build(t)
	if !strings.Contains(ansi.Strip(mA.View()), "[penso ") {
		t.Fatalf("precondicion: el pensamiento asentado debe colapsar a %q", "[penso ")
	}
	mA = apply(t, mA, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: 0})
	viewA := ansi.Strip(mA.View())
	if !strings.Contains(viewA, "[penso ") {
		t.Fatalf("un clic sobre la fila de la barra (Y=0) debe ser inerte: el resumen %q debe seguir; View = %q", "[penso ", viewA)
	}
	for _, body := range []string{"razon-2", "razon-3"} {
		if strings.Contains(viewA, body) {
			t.Fatalf("un clic sobre la fila de la barra (Y=0) NO debe expandir el pensamiento; View = %q contiene %q", viewA, body)
		}
	}

	// Parte B (impacta): el clic sobre la fila visible del resumen si expande.
	mB := build(t)
	summaryY := lineIndexWith(t, ansi.Strip(mB.View()), "[penso ")
	mB = apply(t, mB, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: summaryY})
	viewB := ansi.Strip(mB.View())
	for _, want := range []string{"razon-1", "razon-2", "razon-3"} {
		if !strings.Contains(viewB, want) {
			t.Fatalf("el clic sobre la fila %d del resumen debe expandir el pensamiento mostrando %q; View = %q", summaryY, want, viewB)
		}
	}
}
