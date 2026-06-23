package skill

// Tests white-box (mismo package skill) por consistencia con prompt/tool. Uno por
// comportamiento. Empezamos por Parse: separar el frontmatter (name/description)
// del cuerpo Markdown del SKILL.md.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill crea <root>/<rel>/SKILL.md con el frontmatter dado. Helper de los
// tests de Discover.
func writeSkill(t *testing.T, root, rel, name, desc string) string {
	t.Helper()
	dir := filepath.Join(root, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	front := "---\nname: " + name + "\n"
	if desc != "" {
		front += "description: " + desc + "\n"
	}
	front += "---\ncuerpo de " + name + "\n"
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(front), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// Parse extrae name/description del frontmatter delimitado por --- y deja el
// resto como Content (sin el frontmatter). RED: Parse e Info aun no existen.
func TestParse_ExtractsNameDescriptionAndBody(t *testing.T) {
	raw := []byte("---\nname: demo\ndescription: Hace algo util\n---\n# Demo\n\ncuerpo de la skill\n")

	info, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: error inesperado: %v", err)
	}
	if info.Name != "demo" {
		t.Errorf("Name = %q, quiero %q", info.Name, "demo")
	}
	if info.Description != "Hace algo util" {
		t.Errorf("Description = %q, quiero %q", info.Description, "Hace algo util")
	}
	if !strings.Contains(info.Content, "cuerpo de la skill") {
		t.Errorf("Content = %q, quiero que contenga el cuerpo de la skill", info.Content)
	}
	if strings.Contains(info.Content, "name:") {
		t.Errorf("Content no debe incluir el frontmatter; got: %q", info.Content)
	}
}

// TRIANGULATE: una skill sin description es valida (se parsea con Description "");
// Format luego la filtra, pero Parse no debe rechazarla.
func TestParse_MissingDescription(t *testing.T) {
	info, err := Parse([]byte("---\nname: solo-nombre\n---\ncuerpo\n"))
	if err != nil {
		t.Fatalf("Parse: error inesperado: %v", err)
	}
	if info.Name != "solo-nombre" {
		t.Errorf("Name = %q, quiero %q", info.Name, "solo-nombre")
	}
	if info.Description != "" {
		t.Errorf("Description = %q, quiero vacia", info.Description)
	}
}

// TRIANGULATE: un frontmatter sin name es error; una skill sin nombre no es
// referenciable por el modelo.
func TestParse_NameRequired(t *testing.T) {
	if _, err := Parse([]byte("---\ndescription: sin nombre\n---\ncuerpo\n")); err == nil {
		t.Fatal("Parse sin name: quiero error, no obtuve ninguno")
	}
}

// TRIANGULATE: un archivo sin frontmatter (no empieza con ---) es error, no se
// trata todo como cuerpo de una skill anonima.
func TestParse_NoFrontmatter(t *testing.T) {
	if _, err := Parse([]byte("# Solo Markdown\nsin frontmatter\n")); err == nil {
		t.Fatal("Parse sin frontmatter: quiero error, no obtuve ninguno")
	}
}

// Discover escanea SKILL.md bajo el directorio y devuelve cada skill con su
// Location apuntando al SKILL.md. RED: Discover aun no existe.
func TestDiscover_FindsSkillInDir(t *testing.T) {
	root := t.TempDir()
	loc := writeSkill(t, root, "foo", "foo", "una skill")

	list, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: error inesperado: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("Discover devolvio %d skills, quiero 1", len(list))
	}
	if list[0].Name != "foo" {
		t.Errorf("Name = %q, quiero %q", list[0].Name, "foo")
	}
	if list[0].Location != loc {
		t.Errorf("Location = %q, quiero %q", list[0].Location, loc)
	}
}

// TRIANGULATE: encuentra varias skills, incluso anidadas en subdirectorios.
func TestDiscover_MultipleAndNested(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "foo", "foo", "a")
	writeSkill(t, root, filepath.Join("group", "bar"), "bar", "b")

	list, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: error inesperado: %v", err)
	}
	got := map[string]bool{}
	for _, s := range list {
		got[s.Name] = true
	}
	if !got["foo"] || !got["bar"] {
		t.Fatalf("Discover = %v, quiero contener foo y bar", got)
	}
}

// TRIANGULATE: un directorio inexistente no es error, devuelve cero skills.
func TestDiscover_DirMissing(t *testing.T) {
	list, err := Discover(filepath.Join(t.TempDir(), "no", "existe"))
	if err != nil {
		t.Fatalf("Discover dir ausente: error inesperado: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("Discover dir ausente = %d skills, quiero 0", len(list))
	}
}

// TRIANGULATE: una skill ilegible (frontmatter sin name) se omite sin romper el
// descubrimiento de las validas.
func TestDiscover_SkipsUnparseable(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "buena", "buena", "ok")
	// SKILL.md sin name: ilegible para Parse.
	badDir := filepath.Join(root, "mala")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte("---\ndescription: sin name\n---\nx\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	list, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: error inesperado: %v", err)
	}
	if len(list) != 1 || list[0].Name != "buena" {
		t.Fatalf("Discover = %v, quiero solo la skill 'buena'", list)
	}
}

// Discover acepta varios directorios y fusiona sus skills (p.ej. .atenea/skills y
// el estandar .agents/skills). RED: hoy Discover solo acepta un directorio.
func TestDiscover_MergesMultipleDirs(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeSkill(t, dirA, "propia", "propia", "a")
	writeSkill(t, dirB, "estandar", "estandar", "b")

	list, err := Discover(dirA, dirB)
	if err != nil {
		t.Fatalf("Discover: error inesperado: %v", err)
	}
	got := map[string]bool{}
	for _, s := range list {
		got[s.Name] = true
	}
	if !got["propia"] || !got["estandar"] {
		t.Fatalf("Discover = %v, quiero skills de ambos directorios (propia, estandar)", got)
	}
}

// TRIANGULATE: ante una skill con el mismo nombre en dos directorios, gana la del
// directorio listado primero (override local sobre el estandar). Lo verificamos
// por Location, que apunta al SKILL.md del directorio ganador.
func TestDiscover_DedupesByNameFirstWins(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	locA := writeSkill(t, dirA, "dup", "dup", "de A")
	writeSkill(t, dirB, "dup", "dup", "de B")

	list, err := Discover(dirA, dirB)
	if err != nil {
		t.Fatalf("Discover: error inesperado: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("Discover = %d skills, quiero 1 (deduplicada por nombre)", len(list))
	}
	if list[0].Location != locA {
		t.Errorf("Location = %q, quiero la del primer directorio %q", list[0].Location, locA)
	}
}

// Format arma el bloque verbose <available_skills> (name + description +
// location) que viaja en el system prompt. RED: Format aun no existe.
func TestFormat_RendersAvailableSkillsBlock(t *testing.T) {
	got := Format([]Info{{Name: "foo", Description: "hace foo", Location: "/abs/foo/SKILL.md"}})

	for _, want := range []string{
		"<available_skills>",
		"<name>foo</name>",
		"<description>hace foo</description>",
		"<location>/abs/foo/SKILL.md</location>",
		"</available_skills>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Format no contiene %q; got:\n%s", want, got)
		}
	}
}

// TRIANGULATE: sin skills (o ninguna con description) el bloque es "" para que el
// system prompt no anexe una seccion vacia.
func TestFormat_Empty(t *testing.T) {
	if got := Format(nil); got != "" {
		t.Errorf("Format(nil) = %q, quiero vacia", got)
	}
	if got := Format([]Info{{Name: "x"}}); got != "" {
		t.Errorf("Format(sin description) = %q, quiero vacia", got)
	}
}

// TRIANGULATE: una skill con description se incluye y una sin description se
// filtra, en la misma lista.
func TestFormat_FiltersWithoutDescription(t *testing.T) {
	got := Format([]Info{
		{Name: "visible", Description: "si"},
		{Name: "oculta", Description: ""},
	})
	if !strings.Contains(got, "<name>visible</name>") {
		t.Errorf("Format debe incluir la skill con description; got:\n%s", got)
	}
	if strings.Contains(got, "<name>oculta</name>") {
		t.Errorf("Format no debe incluir la skill sin description; got:\n%s", got)
	}
}

// TRIANGULATE: el orden de salida es por nombre, no el de entrada.
func TestFormat_SortedByName(t *testing.T) {
	got := Format([]Info{
		{Name: "bravo", Description: "b"},
		{Name: "alfa", Description: "a"},
	})
	if strings.Index(got, "alfa") > strings.Index(got, "bravo") {
		t.Errorf("Format debe ordenar alfa antes que bravo; got:\n%s", got)
	}
}
