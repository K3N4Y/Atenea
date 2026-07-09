package tui

// Autocompletado del composer: la logica pura del popup, espejo de
// frontend/src/lib. El menu "/" de slash-commands espeja command.ts
// (detectCommand/filterCommands) y el @-menu de archivos espeja mention.ts
// (detectMention/filterFiles); a diferencia del @, un comando es TODO el
// mensaje: solo dispara cuando "/" es el primer caracter del input. Los
// helpers de Model (applySelection/refreshMenu/closeMenu y el cache de
// listFiles) cablean esos tokens al estado del popup.

import (
	"path"
	"sort"
	"strings"
	"unicode"

	"atenea/internal/command"
)

// menuLimit acota cuantos items muestra el popup de autocompletado.
const menuLimit = 6

// menuItem es una fila del popup, agnostica de la fuente: el menu "/" la
// puebla con "/<name>" y la descripcion de la skill en estilo tenue; el
// @-menu de archivos, con la ruta como label y sin descripcion.
type menuItem struct {
	label       string
	description string
	builtin     bool
}

// tokenQuery es el token de autocompletado vigente bajo el caret, la forma
// comun que devuelven detectCommand y detectMention (espejo de CommandQuery
// en command.ts y MentionQuery en mention.ts): query es el texto entre el
// disparador ("/" o "@") y el caret (lo que filtra), start el indice del
// disparador ("/" siempre en 0; el "@" donde arranca el token) y end la
// posicion del caret. Los indices son por runa, igual que el caret del
// textinput de bubbles.
type tokenQuery struct {
	active     bool
	query      string
	start, end int
}

// inactiveToken es el resultado neutro cuando no hay token vigente (espejo
// del INACTIVE de command.ts y mention.ts).
var inactiveToken = tokenQuery{start: -1, end: -1}

// detectCommand reconoce un comando solo cuando "/" es el primer caracter del
// texto y el caret sigue dentro del nombre (sin ningun espacio en blanco entre
// el "/" y el caret). Al teclear el primer espacio el menu se cierra: lo que
// sigue son los argumentos del comando. Caret fuera de rango = inactivo.
// Opera sobre []rune porque el caret del textinput es por runa, no por byte.
func detectCommand(text string, caret int) tokenQuery {
	runes := []rune(text)
	if caret < 0 || caret > len(runes) {
		return inactiveToken
	}
	if len(runes) == 0 || runes[0] != '/' {
		return inactiveToken
	}
	for i := 1; i < caret; i++ {
		if unicode.IsSpace(runes[i]) {
			return inactiveToken
		}
	}
	return tokenQuery{active: true, query: string(runes[1:caret]), start: 0, end: caret}
}

// filterCommands ordena comandos contra una query (sin distinguir mayusculas),
// espejo del ranking de filterCommands en command.ts. Query vacia devuelve la
// cabeza de la lista. Si no, conserva los comandos cuyo nombre (o, en su
// defecto, descripcion) contiene la query, rankeando el prefijo del nombre (0)
// antes que la subcadena del nombre (1) y antes que el match en la descripcion
// (2); desempata por nombre mas corto y luego alfabetico. Sin match se
// descarta. Acota a limit; limit <= 0 devuelve vacio.
func filterCommands(cmds []command.Command, query string, limit int) []command.Command {
	if limit <= 0 {
		return nil
	}
	q := strings.ToLower(query)
	if q == "" {
		if len(cmds) > limit {
			return cmds[:limit]
		}
		return cmds
	}
	type scoredCmd struct {
		cmd   command.Command
		score int
	}
	var matches []scoredCmd
	for _, cmd := range cmds {
		name := strings.ToLower(cmd.Name)
		var score int
		switch {
		case strings.HasPrefix(name, q):
			score = 0
		case strings.Contains(name, q):
			score = 1
		case strings.Contains(strings.ToLower(cmd.Description), q):
			score = 2
		default:
			continue
		}
		matches = append(matches, scoredCmd{cmd: cmd, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.score != b.score {
			return a.score < b.score
		}
		if len(a.cmd.Name) != len(b.cmd.Name) {
			return len(a.cmd.Name) < len(b.cmd.Name)
		}
		return a.cmd.Name < b.cmd.Name
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]command.Command, len(matches))
	for i, s := range matches {
		out[i] = s.cmd
	}
	return out
}

// detectMention busca un token "@" que termina en el caret (espejo de
// detectMention en mention.ts). Es activo cuando hay un "@" antes del caret
// sin espacios en medio y el "@" inicia palabra (inicio del texto o precedido
// por espacio), para que un email como a@b no dispare. La query es el texto
// entre el "@" y el caret; conserva las barras de una ruta. Opera sobre []rune
// porque el caret del textinput es por runa, no por byte.
func detectMention(text string, caret int) tokenQuery {
	runes := []rune(text)
	if caret < 0 || caret > len(runes) {
		return inactiveToken
	}
	i := caret - 1
	for i >= 0 {
		if runes[i] == '@' {
			break
		}
		if unicode.IsSpace(runes[i]) {
			return inactiveToken
		}
		i--
	}
	if i < 0 || runes[i] != '@' {
		return inactiveToken
	}
	if i > 0 && !unicode.IsSpace(runes[i-1]) {
		return inactiveToken
	}
	return tokenQuery{active: true, query: string(runes[i+1 : caret]), start: i, end: caret}
}

// filterFiles ordena rutas contra una query (sin distinguir mayusculas),
// espejo del ranking de filterFiles en mention.ts. Query vacia devuelve la
// cabeza de la lista. Si no, conserva las rutas que contienen la query,
// rankeando el prefijo del basename (0) antes que la subcadena del basename
// (1) y antes que el match en la ruta completa (2); desempata por ruta mas
// corta. Sin match se descarta. Acota a limit; limit <= 0 devuelve vacio.
func filterFiles(files []string, query string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	q := strings.ToLower(query)
	if q == "" {
		if len(files) > limit {
			return files[:limit]
		}
		return files
	}
	type scoredFile struct {
		path  string
		score int
	}
	var matches []scoredFile
	for _, p := range files {
		lower := strings.ToLower(p)
		base := path.Base(lower)
		var score int
		switch {
		case strings.HasPrefix(base, q):
			score = 0
		case strings.Contains(base, q):
			score = 1
		case strings.Contains(lower, q):
			score = 2
		default:
			continue
		}
		matches = append(matches, scoredFile{path: p, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.score != b.score {
			return a.score < b.score
		}
		return len(a.path) < len(b.path)
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]string, len(matches))
	for i, s := range matches {
		out[i] = s.path
	}
	return out
}

// applySelection aplica el item seleccionado del popup sobre el input,
// recomputando el token vigente (la misma prioridad que refreshMenu): con
// token "/" reemplaza "/co" por "/<name> " (espejo de applyCommand: el label
// ya es "/<name>", conserva lo que hubiera despues del caret); con token "@"
// reemplaza el token por "@<ruta> " conservando el texto alrededor (espejo de
// applyMention: text[:start] + insert + text[end:]). En ambos el caret queda
// tras el espacio, listo para seguir escribiendo, y el recomputo final cierra
// el menu (el token ya no es vigente por el espacio). Sin menu abierto es no-op.
func (m Model) applySelection() Model {
	if len(m.menuItems) == 0 {
		return m
	}
	item := m.menuItems[m.menuSelected]
	runes := []rune(m.input.Value())
	if q := detectCommand(m.input.Value(), m.input.Position()); q.active {
		insert := item.label + " "
		m.input.SetValue(insert + string(runes[q.end:]))
		m.input.SetCursor(len([]rune(insert)))
	} else if q := detectMention(m.input.Value(), m.input.Position()); q.active {
		insert := "@" + item.label + " "
		m.input.SetValue(string(runes[:q.start]) + insert + string(runes[q.end:]))
		m.input.SetCursor(q.start + len([]rune(insert)))
	}
	return m.refreshMenu()
}

// refreshMenu recomputa el popup de autocompletado desde el texto y el caret
// actuales del input: con token "/" vigente puebla los items con los comandos
// filtrados; con token "@" vigente, con los archivos del workspace filtrados
// (listFiles se llama UNA vez al activarse el token y se cachea mientras siga
// activo; con listFiles nil o con error el menu no abre). Sin token vigente lo
// cierra y descarta el cache. En todos los casos el primer item queda
// seleccionado. Se llama tras cada tecla que alimenta el input. El popup ocupa
// lineas bajo el transcript (reservedLines las descuenta), asi que recalcula
// el alto del viewport.
func (m Model) refreshMenu() Model {
	m.menuItems = nil
	m.menuSelected = 0
	text, caret := m.input.Value(), m.input.Position()
	if q := detectCommand(text, caret); q.active {
		m = m.dropFileCache()
		if strings.HasPrefix("new", strings.ToLower(q.query)) {
			m.menuItems = append(m.menuItems, menuItem{label: "/new", builtin: true})
		}
		for _, cmd := range filterCommands(m.commands, q.query, menuLimit) {
			if len(m.menuItems) == menuLimit {
				break
			}
			m.menuItems = append(m.menuItems, menuItem{label: "/" + cmd.Name, description: cmd.Description})
		}
	} else if q := detectMention(text, caret); q.active {
		m = m.loadFilesOnce()
		for _, f := range filterFiles(m.files, q.query, menuLimit) {
			m.menuItems = append(m.menuItems, menuItem{label: f})
		}
	} else {
		m = m.dropFileCache()
	}
	return m.resizeViewport()
}

// closeMenu cierra el popup descartando items y seleccion, sin tocar el input
// ni el cache de archivos (la proxima tecla que alimente el input recomputa el
// token y puede reabrirlo). El popup ocupaba lineas bajo el transcript
// (reservedLines las descontaba), asi que recalcula el alto del viewport.
// refreshMenu no lo reusa: alli el reset precede al repoblado y el viewport se
// recalcula una sola vez al final.
func (m Model) closeMenu() Model {
	m.menuItems = nil
	m.menuSelected = 0
	return m.resizeViewport()
}

// loadFilesOnce llama listFiles la primera vez que el token "@" esta vigente y
// cachea el resultado mientras lo siga estando (dropFileCache lo descarta al
// desactivarse). Un error (o listFiles nil) deja el cache vacio: el menu
// simplemente no abre.
func (m Model) loadFilesOnce() Model {
	if m.filesLoaded {
		return m
	}
	m.filesLoaded = true
	m.files = nil
	if m.listFiles == nil {
		return m
	}
	if files, err := m.listFiles(); err == nil {
		m.files = files
	}
	return m
}

// dropFileCache descarta el listado cacheado del @-menu: la proxima activacion
// del token vuelve a llamar listFiles (el workspace pudo cambiar entre tokens).
func (m Model) dropFileCache() Model {
	m.files = nil
	m.filesLoaded = false
	return m
}
