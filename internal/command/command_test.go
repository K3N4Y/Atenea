package command

import (
	"reflect"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/skill"
)

func TestNewChecked_RejectsACommandThatShadowsABuiltin(t *testing.T) {
	_, err := NewChecked([]Command{
		{Name: "new", Description: "Start a new session", BuiltIn: true},
		{Name: "new", Description: "A skill trying to shadow the local command"},
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate slash command "new"`) {
		t.Fatalf("NewChecked error = %v, want duplicate command name", err)
	}
}

func TestSet_ListPreservesBuiltinIdentity(t *testing.T) {
	set, err := NewChecked([]Command{{Name: "new", BuiltIn: true}})
	if err != nil {
		t.Fatalf("NewChecked: %v", err)
	}
	if got := set.List(); len(got) != 1 || !got[0].BuiltIn {
		t.Fatalf("List() = %#v, want local /new command", got)
	}
}

// TestExpand_SubstituteArguments: $ARGUMENTS is replaced by the args; The result is cropped to avoid dragging excess spaces.
func TestExpand_SubstituyeArguments(t *testing.T) {
	got := Expand(`Usa la skill "x".`+"\n\n$ARGUMENTS", "implementa foo")
	want := `Usa la skill "x".` + "\n\nimplementa foo"
	if got != want {
		t.Fatalf("Expand = %q, want %q", got, want)
	}
}

// TestExpand_SinArgsRecortaPlaceholder: without args, the placeholder and its separator remain empty; the result does not end with loose line breaks.
func TestExpand_SinArgsRecortaPlaceholder(t *testing.T) {
	got := Expand(`Usa la skill "x".`+"\n\n$ARGUMENTS", "")
	want := `Usa la skill "x".`
	if got != want {
		t.Fatalf("Expand = %q, want %q", got, want)
	}
}

// TestExpand_SinPlaceholderAnexaArgs: a template without $ARGUMENTS appends the args at the end (separated by a blank line) when they exist.
func TestExpand_SinPlaceholderAnexaArgs(t *testing.T) {
	if got := Expand("Hace algo", "contexto"); got != "Hace algo\n\ncontexto" {
		t.Fatalf("con args: Expand = %q", got)
	}
	if got := Expand("Hace algo", ""); got != "Hace algo" {
		t.Fatalf("sin args: Expand = %q", got)
	}
}

// TestFromSkills_DerivaUnComandoPorSkill: each skill discovered produces a /<name> command with its description and a template that references the skill.
func TestFromSkills_DerivaUnComandoPorSkill(t *testing.T) {
	skills := []skill.Info{
		{Name: "code-review", Description: "Revision de codigo"},
		{Name: "deep-research", Description: "investigacion profunda"},
	}
	cmds := FromSkills(skills)
	for _, cmd := range cmds {
		if !cmd.Skill || cmd.BuiltIn {
			t.Fatalf("skill command metadata = Skill %v BuiltIn %v", cmd.Skill, cmd.BuiltIn)
		}
	}
	if len(cmds) != 2 {
		t.Fatalf("FromSkills devolvio %d comandos, want 2", len(cmds))
	}
	if cmds[0].Name != "code-review" || cmds[0].Description != "Revision de codigo" {
		t.Fatalf("comando[0] = %+v", cmds[0])
	}
	// The template must name the skill so that the agent loads it through its tool.
	exp := Expand(cmds[0].Template, "")
	if exp == "" || !contains(exp, "code-review") {
		t.Fatalf("la plantilla no referencia la skill: %q", exp)
	}
}

// TestSet_ListOrdenaByName: List returns commands ordered by name, stable for the composer menu.
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

// TestSet_ResolveExpandRegisteredCommand: A "/name args" entry from a registered command resolves to the template expanded with the args.
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

// TestSet_ResolveSinArgs: "/name" without args expands the template without the placeholder.
func TestSet_ResolveSinArgs(t *testing.T) {
	s := New([]Command{{Name: "foo", Template: "Hace foo.\n\n$ARGUMENTS"}})
	out, ok := s.Resolve("/foo")
	if !ok || out != "Hace foo." {
		t.Fatalf("Resolve sin args = %q, ok=%v", out, ok)
	}
}

// TestSet_ResolveTextoNormalNoEsComando: text that does not begin with "/" is passed over (it is not a command), so as not to transform normal prompts.
func TestSet_ResolveTextoNormalNoEsComando(t *testing.T) {
	s := New([]Command{{Name: "foo", Template: "x"}})
	if _, ok := s.Resolve("hola foo"); ok {
		t.Fatalf("texto normal no debe resolverse como comando")
	}
}

// TestSet_ResolveUnknownCommandPasaDeLargo: "/unknown" that is not in the registry is not transformed; literal is sent (ok=false).
func TestSet_ResolveComandoDesconocidoPasaDeLargo(t *testing.T) {
	s := New([]Command{{Name: "foo", Template: "x"}})
	if _, ok := s.Resolve("/desconocido algo"); ok {
		t.Fatalf("un comando no registrado no debe resolverse")
	}
}

// TestSet_ResolveNameEndsOnLineFeed: the name ends on the first blank space (a line break of Shift+Enter separates name from args).
func TestSet_ResolveNombreTerminaEnSaltoDeLinea(t *testing.T) {
	s := New([]Command{{Name: "foo", Template: "Hace foo.\n\n$ARGUMENTS"}})
	out, ok := s.Resolve("/foo\nhola")
	if !ok || out != "Hace foo.\n\nhola" {
		t.Fatalf("Resolve con salto = %q, ok=%v", out, ok)
	}
}

// TestSet_ResolveBarSolaNoEsCommand: "/" without a name is not a command.
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
