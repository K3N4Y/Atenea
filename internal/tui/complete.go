package tui

// Autocompletado del composer: la logica pura del popup, espejo de
// frontend/src/lib. El menu "/" de slash-commands espeja command.ts
// (detectCommand/filterCommands) y el @-menu de archivos espeja mention.ts
// (detectMention/filterFiles); a diferencia del @, un comando es TODO el
// mensaje: solo dispara cuando "/" es el primer caracter del input. Los
// metodos del composer (applySelection/refreshMenu/closeMenu y el cache de
// listFiles) cablean esos tokens al estado del popup; ver composer.go.

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"atenea/internal/command"
	"atenea/internal/providerconfig"
)

// menuLimit acota cuantos items muestra el popup de autocompletado. It grows
// with the builtin set (/new, /compact, /model, /mcp, /connect) so builtins
// never crowd skill commands out of the popup.
const menuLimit = 8

// menuItem es una fila del popup, agnostica de la fuente: el menu "/" la
// puebla con "/<name>" y la descripcion de la skill en estilo tenue; el
// @-menu de archivos, con la ruta como label y sin descripcion.
type menuItem struct {
	label       string
	description string
	builtin     bool
	providerID  string
	model       string
	empty       bool
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

func detectModelQuery(text string, caret int) tokenQuery {
	runes := []rune(text)
	const prefix = "/model "
	if caret < len([]rune(prefix)) || caret > len(runes) || !strings.HasPrefix(string(runes[:caret]), prefix) {
		return inactiveToken
	}
	return tokenQuery{active: true, query: strings.TrimSpace(string(runes[len([]rune(prefix)):caret])), start: 0, end: caret}
}

func filterModels(providers []providerconfig.ProviderModels, query string, limit int) []menuItem {
	query = strings.ToLower(strings.ReplaceAll(query, " ", ""))
	var items []menuItem
	for _, provider := range providers {
		for _, model := range provider.Models {
			haystack := strings.ToLower(strings.ReplaceAll(provider.ID+provider.Name+model, " ", ""))
			if query != "" && !strings.Contains(haystack, query) {
				continue
			}
			description := provider.Name
			if !strings.EqualFold(strings.ReplaceAll(provider.Name, " ", ""), strings.ReplaceAll(provider.ID, "-", "")) {
				description = fmt.Sprintf("%s · %s", provider.Name, provider.ID)
			}
			if context := curatedModelContext[model]; context != "" {
				description += " · " + context + " context"
			}
			items = append(items, menuItem{label: model, description: description, providerID: provider.ID, model: model})
			if len(items) == limit {
				return items
			}
		}
	}
	return items
}

var curatedModelContext = map[string]string{
	"tencent/hy3:free":            "262K",
	"poolside/laguna-xs-2.1:free": "262K",
	"cohere/north-mini-code:free": "256K",
}

func isCanonicalModelCommand(text string, providers []providerconfig.ProviderModels) bool {
	parts := strings.Fields(text)
	if len(parts) != 3 || parts[0] != "/model" {
		return false
	}
	for _, provider := range providers {
		if provider.ID != parts[1] {
			continue
		}
		for _, model := range provider.Models {
			if model == parts[2] {
				return true
			}
		}
	}
	return false
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
func (c composer) applySelection(commands []command.Command, listFiles func() ([]string, error), models modelSource) (composer, tea.Cmd) {
	if len(c.menuItems) == 0 {
		return c, nil
	}
	item := c.menuItems[c.menuSelected]
	if item.empty {
		return c, nil
	}
	if item.model != "" {
		value := "/model " + item.providerID + " " + item.model + " "
		c.input.SetValue(value)
		c.input.SetCursor(len([]rune(value)))
		return c.closeMenu(), nil
	}
	runes := []rune(c.input.Value())
	if q := detectCommand(c.input.Value(), c.input.Position()); q.active {
		insert := item.label + " "
		c.input.SetValue(insert + string(runes[q.end:]))
		c.input.SetCursor(len([]rune(insert)))
	} else if q := detectMention(c.input.Value(), c.input.Position()); q.active {
		insert := "@" + item.label + " "
		c.input.SetValue(string(runes[:q.start]) + insert + string(runes[q.end:]))
		c.input.SetCursor(q.start + len([]rune(insert)))
	}
	return c.refreshMenu(commands, listFiles, models)
}

// refreshMenu recomputa el popup de autocompletado desde el texto y el caret
// actuales del input: con token "/" vigente puebla los items con los comandos
// filtrados; con token "@" vigente, con los archivos del workspace filtrados
// (listFiles se agenda UNA vez al activarse el token y se cachea mientras siga
// activo; mientras corre o falla, el menu muestra el estado correspondiente).
// Sin token vigente lo cierra, invalida resultados pendientes y descarta el
// cache. En todos los casos el primer item queda seleccionado. Se llama tras
// cada tecla que alimenta el input. commands es la fuente de slash-commands,
// listFiles la del @-menu y models la busqueda inline "/model" (inyectados por
// la raiz, como el listFiles del explorer). Value-in / value-out: el llamador
// (el seam de Model refreshMenu) recalcula el alto del viewport, que es una
// preocupacion de layout del Model.
func (c composer) refreshMenu(commands []command.Command, listFiles func() ([]string, error), models modelSource) (composer, tea.Cmd) {
	c.menuItems = nil
	c.menuSelected = 0
	text, caret := c.input.Value(), c.input.Position()
	if q := detectModelQuery(text, caret); q.active {
		c = c.dropFileCache()
		catalog, ok := models.catalog()
		if ok && isCanonicalModelCommand(text, catalog) {
			c.modelSearch = false
			return c, nil
		}
		if ok {
			c.menuItems = filterModels(catalog, q.query, menuLimit)
			if !c.modelSearch && models.refresh != nil {
				models.refresh()
			}
		}
		c.modelSearch = true
		if len(c.menuItems) == 0 {
			label := "No matches"
			if ok && len(catalog) == 0 {
				label = "No models available"
			}
			c.menuItems = []menuItem{{label: label, empty: true}}
		}
	} else if q := detectCommand(text, caret); q.active {
		c.modelSearch = false
		c = c.dropFileCache()
		query := strings.ToLower(q.query)
		includeNew := strings.HasPrefix("new", query)
		includeCompact := strings.HasPrefix("compact", query)
		includeModel := strings.HasPrefix("model", query)
		includeMcp := strings.HasPrefix("mcp", query)
		includeConnect := strings.HasPrefix("connect", query)
		developmentItems := developmentBuiltinCommands(query)
		if includeNew {
			c.menuItems = append(c.menuItems, menuItem{label: "/new", builtin: true})
		}
		reserved := len(c.menuItems)
		if includeCompact {
			reserved++
		}
		if includeModel {
			reserved++
		}
		if includeMcp {
			reserved++
		}
		if includeConnect {
			reserved++
		}
		reserved += len(developmentItems)
		for _, cmd := range filterCommands(commands, q.query, menuLimit-reserved) {
			c.menuItems = append(c.menuItems, menuItem{label: "/" + cmd.Name, description: cmd.Description, builtin: cmd.Name == "resume"})
		}
		if includeCompact {
			item := menuItem{label: "/compact", description: "Compact conversation context", builtin: true}
			if query == "" && len(c.menuItems) > 1 {
				insertAt := len(c.menuItems) - 1
				c.menuItems = append(c.menuItems, menuItem{})
				copy(c.menuItems[insertAt+1:], c.menuItems[insertAt:])
				c.menuItems[insertAt] = item
			} else {
				c.menuItems = append(c.menuItems, item)
			}
		}
		if includeModel {
			item := menuItem{label: "/model", description: "Select provider and model", builtin: true}
			if query != "" {
				insertAt := 0
				if includeNew {
					insertAt = 1
				}
				c.menuItems = append(c.menuItems, menuItem{})
				copy(c.menuItems[insertAt+1:], c.menuItems[insertAt:])
				c.menuItems[insertAt] = item
			} else if len(c.menuItems) > 1 {
				last := c.menuItems[len(c.menuItems)-1]
				c.menuItems[len(c.menuItems)-1] = item
				c.menuItems = append(c.menuItems, last)
			} else {
				c.menuItems = append(c.menuItems, item)
			}
		}
		if includeMcp {
			item := menuItem{label: "/mcp", description: "Toggle MCP servers on or off", builtin: true}
			if query == "" && len(c.menuItems) > 1 {
				insertAt := len(c.menuItems) - 1
				c.menuItems = append(c.menuItems, menuItem{})
				copy(c.menuItems[insertAt+1:], c.menuItems[insertAt:])
				c.menuItems[insertAt] = item
			} else {
				c.menuItems = append(c.menuItems, item)
			}
		}
		if includeConnect {
			item := menuItem{label: "/connect", description: "Connect a provider with an API key", builtin: true}
			if query == "" && len(c.menuItems) > 1 {
				insertAt := len(c.menuItems) - 1
				c.menuItems = append(c.menuItems, menuItem{})
				copy(c.menuItems[insertAt+1:], c.menuItems[insertAt:])
				c.menuItems[insertAt] = item
			} else {
				c.menuItems = append(c.menuItems, item)
			}
		}
		c.menuItems = append(c.menuItems, developmentItems...)
	} else if q := detectMention(text, caret); q.active {
		c.modelSearch = false
		var cmd tea.Cmd
		c, cmd = c.loadFilesOnce(listFiles)
		if c.filesLoading {
			c.menuItems = []menuItem{{label: "Loading files…", empty: true}}
			return c, cmd
		}
		if c.filesError != "" {
			c.menuItems = []menuItem{{label: "Could not list files: " + c.filesError, empty: true}}
			return c, cmd
		}
		for _, f := range filterFiles(c.files, q.query, menuLimit) {
			c.menuItems = append(c.menuItems, menuItem{label: f})
		}
		return c, cmd
	} else {
		c.modelSearch = false
		c = c.dropFileCache()
	}
	return c, nil
}

// closeMenu cierra el popup descartando items y seleccion, sin tocar el input
// ni el cache de archivos (la proxima tecla que alimente el input recomputa el
// token y puede reabrirlo). El llamador recalcula el alto del viewport porque
// el popup ocupaba lineas bajo el transcript (reservedLines las descontaba).
// refreshMenu no lo reusa: alli el reset precede al repoblado.
func (c composer) closeMenu() composer {
	c.menuItems = nil
	c.menuSelected = 0
	return c
}

// loadFilesOnce agenda listFiles la primera vez que el token "@" esta vigente y
// cachea el resultado mientras lo siga estando (dropFileCache lo descarta al
// desactivarse). La generacion permite ignorar respuestas de tokens anteriores.
func (c composer) loadFilesOnce(listFiles func() ([]string, error)) (composer, tea.Cmd) {
	if c.filesLoaded || c.filesLoading {
		return c, nil
	}
	c.files = nil
	c.filesError = ""
	if listFiles == nil {
		c.filesLoaded = true
		return c, nil
	}
	c.filesLoading = true
	c.filesGen++
	return c, listFilesCmd(listFiles, fileListMenu, c.filesGen)
}

// dropFileCache descarta el listado cacheado del @-menu: la proxima activacion
// del token vuelve a llamar listFiles (el workspace pudo cambiar entre tokens).
func (c composer) dropFileCache() composer {
	c.files = nil
	c.filesLoaded = false
	c.filesLoading = false
	c.filesError = ""
	c.filesGen++
	return c
}

// applyListedFiles folds an async "@"-menu listing result into the file cache,
// guarded by the generation so a stale result is ignored, then rebuilds the
// popup from the current input. It mirrors the explorer's applyListed but feeds
// the mention menu instead of the tree. The caller (the Model refreshMenu seam)
// recomputes the viewport height.
func (c composer) applyListedFiles(msg filesListedMsg, commands []command.Command, listFiles func() ([]string, error), models modelSource) (composer, tea.Cmd, bool) {
	if msg.generation != c.filesGen {
		return c, nil, false
	}
	c.filesLoading = false
	c.filesLoaded = true
	c.files = msg.files
	if msg.err != nil {
		c.files = nil
		c.filesError = msg.err.Error()
	}
	c, cmd := c.refreshMenu(commands, listFiles, models)
	return c, cmd, true
}
