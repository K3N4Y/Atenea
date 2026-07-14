package command

import (
	"reflect"
	"testing"

	"atenea/internal/skill"
)

// TestExpand_SubstituyeArguments: $ARGUMENTS se reemplaza por los args; el
// resultado se recorta para no arrastrar espacios sobrantes.
func TestExpand_SubstituyeArguments(t *testing.T) {
	got := Expand(`Usa la skill "x".`+"\n\n$ARGUMENTS", "implementa foo")
	want := `Usa la skill "x".` + "\n\nimplementa foo"
	if got != want {
		t.Fatalf("Expand = %q, want %q", got, want)
	}
}

// TestExpand_SinArgsRecortaPlaceholder: sin args, el placeholder y su separador
// quedan vacios; el resultado no termina con saltos de linea sueltos.
func TestExpand_SinArgsRecortaPlaceholder(t *testing.T) {
	got := Expand(`Usa la skill "x".`+"\n\n$ARGUMENTS", "")
	want := `Usa la skill "x".`
	if got != want {
		t.Fatalf("Expand = %q, want %q", got, want)
	}
}

// TestExpand_SinPlaceholderAnexaArgs: una plantilla sin $ARGUMENTS anexa los args
// al final (separados por linea en blanco) cuando los hay.
func TestExpand_SinPlaceholderAnexaArgs(t *testing.T) {
	if got := Expand("Hace algo", "contexto"); got != "Hace algo\n\ncontexto" {
		t.Fatalf("con args: Expand = %q", got)
	}
	if got := Expand("Hace algo", ""); got != "Hace algo" {
		t.Fatalf("sin args: Expand = %q", got)
	}
}

// TestFromSkills_DerivaUnComandoPorSkill: cada skill descubierta produce un
// comando /<name> con su descripcion y una plantilla que referencia la skill.
func TestFromSkills_DerivaUnComandoPorSkill(t *testing.T) {
	skills := []skill.Info{
		{Name: "code-review", Description: "Revision de codigo"},
		{Name: "deep-research", Description: "investigacion profunda"},
	}
	cmds := FromSkills(skills)
	if len(cmds) != 2 {
		t.Fatalf("FromSkills devolvio %d comandos, want 2", len(cmds))
	}
	if cmds[0].Name != "code-review" || cmds[0].Description != "Revision de codigo" {
		t.Fatalf("comando[0] = %+v", cmds[0])
	}
	// La plantilla debe nombrar la skill para que el agente la cargue por su tool.
	exp := Expand(cmds[0].Template, "")
	if exp == "" || !contains(exp, "code-review") {
		t.Fatalf("la plantilla no referencia la skill: %q", exp)
	}
}

// TestSet_ListOrdenaPorNombre: List devuelve los comandos ordenados por nombre,
// estable para el menu del composer.
func TestSet_ListOrdenaPorNombre(t *testing.T) {
	s := New([]Command{
		{Name: "commit", Description: "b"},
		{Name: "abc", Description: "a"},
	})
	got := []string{s.List()[0].Name, s.List()[1].Name}
	if !reflect.DeepEqual(got, []string{"abc", "commit"}) {
		t.Fatalf("List orden = %v, want [abc commit]", got)
	}
}

// TestSet_ResolveExpandeComandoRegistrado: una entrada "/name args" de un comando
// registrado se resuelve a la plantilla expandida con los args.
func TestSet_ResolveExpandeComandoRegistrado(t *testing.T) {
	s := New([]Command{{Name: "foo", Template: "Hace foo.\n\n$ARGUMENTS"}})
	out, ok := s.Resolve("/foo hola mundo")
	if !ok {
		t.Fatalf("Resolve no reconocio el comando")
	}
	if out != "Hace foo.\n\nhola mundo" {
		t.Fatalf("Resolve = %q", out)
	}
}

// TestSet_ResolveSinArgs: "/name" sin args expande la plantilla sin el placeholder.
func TestSet_ResolveSinArgs(t *testing.T) {
	s := New([]Command{{Name: "foo", Template: "Hace foo.\n\n$ARGUMENTS"}})
	out, ok := s.Resolve("/foo")
	if !ok || out != "Hace foo." {
		t.Fatalf("Resolve sin args = %q, ok=%v", out, ok)
	}
}

// TestSet_ResolveTextoNormalNoEsComando: texto que no empieza con "/" pasa de
// largo (no es comando), para no transformar prompts normales.
func TestSet_ResolveTextoNormalNoEsComando(t *testing.T) {
	s := New([]Command{{Name: "foo", Template: "x"}})
	if _, ok := s.Resolve("hola foo"); ok {
		t.Fatalf("texto normal no debe resolverse como comando")
	}
}

// TestSet_ResolveComandoDesconocidoPasaDeLargo: "/desconocido" que no esta en el
// registro no se transforma; se envia literal (ok=false).
func TestSet_ResolveComandoDesconocidoPasaDeLargo(t *testing.T) {
	s := New([]Command{{Name: "foo", Template: "x"}})
	if _, ok := s.Resolve("/desconocido algo"); ok {
		t.Fatalf("un comando no registrado no debe resolverse")
	}
}

// TestSet_ResolveNombreTerminaEnSaltoDeLinea: el nombre termina en el primer
// espacio en blanco (un salto de linea de Shift+Enter separa nombre de args).
func TestSet_ResolveNombreTerminaEnSaltoDeLinea(t *testing.T) {
	s := New([]Command{{Name: "foo", Template: "Hace foo.\n\n$ARGUMENTS"}})
	out, ok := s.Resolve("/foo\nhola")
	if !ok || out != "Hace foo.\n\nhola" {
		t.Fatalf("Resolve con salto = %q, ok=%v", out, ok)
	}
}

// TestSet_ResolveBarraSolaNoEsComando: "/" sin nombre no es comando.
func TestSet_ResolveBarraSolaNoEsComando(t *testing.T) {
	s := New([]Command{{Name: "foo", Template: "x"}})
	if _, ok := s.Resolve("/"); ok {
		t.Fatalf("'/' sin nombre no debe resolverse")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
