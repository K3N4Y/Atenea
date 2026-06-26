// Package command implementa los slash-commands del composer: "/<name> args" que
// el usuario escribe se resuelve a un prompt expandido antes de enviarse al
// agente. Hoy los comandos se derivan de las skills descubiertas (FromSkills),
// pero el modelo es general: un comando es solo un Name + Description (para el
// menu) y una Template de prompt con el placeholder $ARGUMENTS. Agregar un comando
// nuevo (p.ej. /commit) es agregar otro Command con su plantilla, sin tocar el
// resto del cableado.
package command

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"atenea/internal/skill"
)

// argumentsPlaceholder es el marcador que Expand sustituye por los args que el
// usuario escribe tras el nombre del comando.
const argumentsPlaceholder = "$ARGUMENTS"

// Command es un slash-command: Name lo invoca ("/"+Name), Description lo describe
// en el menu del composer, y Template es el prompt que se envia al agente con
// $ARGUMENTS reemplazado por los args del usuario.
type Command struct {
	Name        string
	Description string
	Template    string
}

// FromSkills deriva un comando por cada skill descubierta: "/<name>" referencia la
// skill. La plantilla instruye al agente a usarla por su nombre (la carga via su
// tool skill, manteniendo el disclosure progresivo) y anexa los args del usuario.
func FromSkills(skills []skill.Info) []Command {
	cmds := make([]Command, 0, len(skills))
	for _, s := range skills {
		cmds = append(cmds, Command{
			Name:        s.Name,
			Description: s.Description,
			Template:    fmt.Sprintf("Usa la skill %q.\n\n%s", s.Name, argumentsPlaceholder),
		})
	}
	return cmds
}

// Expand produce el prompt final de una plantilla y los args. Si la plantilla
// contiene $ARGUMENTS, lo reemplaza por los args; si no, anexa los args al final
// (separados por una linea en blanco) cuando los hay. El resultado se recorta para
// no arrastrar saltos de linea sueltos cuando no hay args.
func Expand(template, args string) string {
	args = strings.TrimSpace(args)
	if strings.Contains(template, argumentsPlaceholder) {
		return strings.TrimSpace(strings.ReplaceAll(template, argumentsPlaceholder, args))
	}
	if args == "" {
		return strings.TrimSpace(template)
	}
	return strings.TrimSpace(template) + "\n\n" + args
}

// Set indexa comandos por nombre y conserva la lista ordenada para el menu. Es de
// solo lectura tras construirse, asi que Resolve/List son seguros concurrentemente.
type Set struct {
	list   []Command
	byName map[string]Command
}

// New indexa los comandos por nombre (ante un nombre duplicado gana el ultimo,
// config del programa) y memoriza la lista ordenada por nombre para el menu.
func New(cmds []Command) *Set {
	byName := make(map[string]Command, len(cmds))
	for _, c := range cmds {
		byName[c.Name] = c
	}
	list := make([]Command, 0, len(byName))
	for _, c := range byName {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return &Set{list: list, byName: byName}
}

// List devuelve los comandos ordenados por nombre para el menu del composer.
func (s *Set) List() []Command { return s.list }

// Resolve interpreta input como un slash-command: si empieza con "/" y su primer
// token nombra un comando registrado, devuelve el prompt expandido y true. Si no
// es un comando (no empieza con "/", "/" sin nombre, o nombre desconocido) devuelve
// ("", false) para que el texto se envie sin transformar.
func (s *Set) Resolve(input string) (string, bool) {
	name, args, ok := parse(input)
	if !ok {
		return "", false
	}
	cmd, ok := s.byName[name]
	if !ok {
		return "", false
	}
	return Expand(cmd.Template, args), true
}

// parse separa "/name args" en (name, args, true). El nombre va del "/" inicial al
// primer espacio; el resto son los args (recortados). No es comando si no empieza
// con "/" o si el nombre queda vacio.
func parse(input string) (name, args string, ok bool) {
	rest, found := strings.CutPrefix(input, "/")
	if !found {
		return "", "", false
	}
	// El nombre va hasta el primer espacio en blanco (espacio, tab o salto): el
	// resto son los args. Asi un salto de Shift+Enter tambien separa nombre de args.
	cut := strings.IndexFunc(rest, unicode.IsSpace)
	if cut < 0 {
		name = rest
	} else {
		name, args = rest[:cut], rest[cut:]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", false
	}
	return name, strings.TrimSpace(args), true
}
