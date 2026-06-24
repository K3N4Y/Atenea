package agent

// Tests white-box (mismo package agent) por consistencia con internal/skill. Uno
// por comportamiento. Empezamos por Parse: separar el frontmatter
// (name/description/tools/model) del cuerpo Markdown de la definicion de
// subagente.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeAgent crea <root>/<rel>.md con el frontmatter dado. Helper de los tests de
// Discover. A diferencia de skill, el archivo de agente es cualquier *.md (no un
// nombre fijo como SKILL.md), por eso <rel> incluye el nombre del archivo.
func writeAgent(t *testing.T, root, rel, name, desc string) string {
	t.Helper()
	path := filepath.Join(root, rel+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	front := "---\nname: " + name + "\n"
	if desc != "" {
		front += "description: " + desc + "\n"
	}
	front += "---\ncuerpo de " + name + "\n"
	if err := os.WriteFile(path, []byte(front), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// Parse extrae name/description/model/tools del frontmatter delimitado por --- y
// deja el resto como Prompt (sin el frontmatter). Los tools vienen en una sola
// linea separados por comas y se devuelven sin espacios alrededor. RED: Parse y
// Def aun no existen.
func TestParse_ExtractsFrontmatterAndPrompt(t *testing.T) {
	raw := []byte("---\nname: reviewer\ndescription: Revisa codigo\nmodel: claude-opus-4-8\ntools: read, grep, glob\n---\n# Reviewer\n\nEres un revisor de codigo.\n")

	def, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: error inesperado: %v", err)
	}
	if def.Name != "reviewer" {
		t.Errorf("Name = %q, quiero %q", def.Name, "reviewer")
	}
	if def.Description != "Revisa codigo" {
		t.Errorf("Description = %q, quiero %q", def.Description, "Revisa codigo")
	}
	if def.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q, quiero %q", def.Model, "claude-opus-4-8")
	}
	want := []string{"read", "grep", "glob"}
	if len(def.Tools) != len(want) {
		t.Fatalf("Tools = %v (len %d), quiero %v (len %d)", def.Tools, len(def.Tools), want, len(want))
	}
	for i, w := range want {
		if def.Tools[i] != w {
			t.Errorf("Tools[%d] = %q, quiero %q", i, def.Tools[i], w)
		}
	}
	if !strings.Contains(def.Prompt, "Eres un revisor de codigo") {
		t.Errorf("Prompt = %q, quiero que contenga el cuerpo del subagente", def.Prompt)
	}
	if strings.Contains(def.Prompt, "name:") {
		t.Errorf("Prompt no debe incluir el frontmatter; got: %q", def.Prompt)
	}
}

// TRIANGULATE: un frontmatter sin name es error; un subagente sin nombre no es
// referenciable.
func TestParse_MissingName(t *testing.T) {
	if _, err := Parse([]byte("---\ndescription: sin nombre\n---\ncuerpo\n")); err == nil {
		t.Fatal("Parse sin name: quiero error, no obtuve ninguno")
	}
}

// TRIANGULATE: un archivo sin frontmatter (no empieza con ---) es error, no se
// trata todo como cuerpo de un subagente anonimo.
func TestParse_NoFrontmatter(t *testing.T) {
	if _, err := Parse([]byte("# Solo Markdown\nsin frontmatter\n")); err == nil {
		t.Fatal("Parse sin frontmatter: quiero error, no obtuve ninguno")
	}
}

// TRIANGULATE: un frontmatter que abre con --- pero no cierra con --- es error.
func TestParse_NoClosing(t *testing.T) {
	if _, err := Parse([]byte("---\nname: roto\ndescription: sin cierre\n")); err == nil {
		t.Fatal("Parse sin cierre de frontmatter: quiero error, no obtuve ninguno")
	}
}

// TRIANGULATE: un frontmatter con name y description pero sin linea tools es
// valido y devuelve Tools vacio (no nil-panic ni error).
func TestParse_NoTools(t *testing.T) {
	def, err := Parse([]byte("---\nname: simple\ndescription: sin tools\n---\ncuerpo\n"))
	if err != nil {
		t.Fatalf("Parse: error inesperado: %v", err)
	}
	if len(def.Tools) != 0 {
		t.Errorf("Tools = %v (len %d), quiero vacia", def.Tools, len(def.Tools))
	}
}

// Discover escanea cualquier *.md bajo el directorio y devuelve cada subagente
// con su Location apuntando al archivo. RED: Discover aun no existe.
func TestDiscover_FindsAgentInDir(t *testing.T) {
	root := t.TempDir()
	loc := writeAgent(t, root, "reviewer", "reviewer", "una def")

	list, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: error inesperado: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("Discover devolvio %d defs, quiero 1", len(list))
	}
	if list[0].Name != "reviewer" {
		t.Errorf("Name = %q, quiero %q", list[0].Name, "reviewer")
	}
	if list[0].Location != loc {
		t.Errorf("Location = %q, quiero %q", list[0].Location, loc)
	}
}

// TRIANGULATE: encuentra varias defs, incluso anidadas en subdirectorios.
func TestDiscover_MultipleAndNested(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, root, "foo", "foo", "a")
	writeAgent(t, root, filepath.Join("group", "bar"), "bar", "b")

	list, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: error inesperado: %v", err)
	}
	got := map[string]bool{}
	for _, d := range list {
		got[d.Name] = true
	}
	if !got["foo"] || !got["bar"] {
		t.Fatalf("Discover = %v, quiero contener foo y bar", got)
	}
}

// TRIANGULATE: un directorio inexistente no es error, devuelve cero defs.
func TestDiscover_DirMissing(t *testing.T) {
	list, err := Discover(filepath.Join(t.TempDir(), "no", "existe"))
	if err != nil {
		t.Fatalf("Discover dir ausente: error inesperado: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("Discover dir ausente = %d defs, quiero 0", len(list))
	}
}

// TRIANGULATE: una def ilegible (frontmatter sin name) se omite sin romper el
// descubrimiento de las validas.
func TestDiscover_SkipsUnparseable(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, root, "buena", "buena", "ok")
	// *.md sin name: ilegible para Parse.
	if err := os.WriteFile(filepath.Join(root, "mala.md"), []byte("---\ndescription: sin name\n---\nx\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	list, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: error inesperado: %v", err)
	}
	if len(list) != 1 || list[0].Name != "buena" {
		t.Fatalf("Discover = %v, quiero solo la def 'buena'", list)
	}
}

// Discover acepta varios directorios y fusiona sus defs. RED: Discover debe
// recorrer todos los dirs en orden.
func TestDiscover_MergesMultipleDirs(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeAgent(t, dirA, "propia", "propia", "a")
	writeAgent(t, dirB, "estandar", "estandar", "b")

	list, err := Discover(dirA, dirB)
	if err != nil {
		t.Fatalf("Discover: error inesperado: %v", err)
	}
	got := map[string]bool{}
	for _, d := range list {
		got[d.Name] = true
	}
	if !got["propia"] || !got["estandar"] {
		t.Fatalf("Discover = %v, quiero defs de ambos directorios (propia, estandar)", got)
	}
}

// Builtins devuelve las definiciones de agente canonicas (al estilo opencode).
// El agente 'explore' es de SOLO LECTURA: solo puede read/grep/glob, nunca
// edit/write/bash. Esto encodea el scoping de tools por agente del milestone S4.
// RED: Builtins aun no existe.
func TestBuiltins_ExploreIsReadOnly(t *testing.T) {
	defs := Builtins()

	var explore Def
	found := false
	for _, d := range defs {
		if d.Name == "explore" {
			explore = d
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Builtins no incluye un agente 'explore'; got %v", defs)
	}

	// Read-only: debe poder leer y buscar.
	for _, want := range []string{"read", "grep", "glob"} {
		if !slices.Contains(explore.Tools, want) {
			t.Errorf("explore debe poder %s (read-only); Tools = %v", want, explore.Tools)
		}
	}
	// Read-only: no debe poder modificar ni ejecutar.
	for _, deny := range []string{"edit", "write", "bash"} {
		if slices.Contains(explore.Tools, deny) {
			t.Errorf("explore no debe poder %s (read-only)", deny)
		}
	}
	// Un agente usable necesita descripcion y system prompt.
	if explore.Description == "" {
		t.Errorf("explore.Description vacio; un agente usable necesita descripcion")
	}
	if explore.Prompt == "" {
		t.Errorf("explore.Prompt vacio; un agente usable necesita system prompt")
	}
}

// TRIANGULATE: contraste full vs read-only. El agente 'general' es de proposito
// general y SI puede modificar y ejecutar: sus Tools deben incluir edit, write y
// bash. Tumba una version donde 'general' tambien fuera de solo lectura.
func TestBuiltins_GeneralHasFullTools(t *testing.T) {
	defs := Builtins()

	var general Def
	found := false
	for _, d := range defs {
		if d.Name == "general" {
			general = d
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Builtins no incluye un agente 'general'; got %v", defs)
	}

	for _, want := range []string{"edit", "write", "bash"} {
		if !slices.Contains(general.Tools, want) {
			t.Errorf("general debe poder %s (acceso completo); Tools = %v", want, general.Tools)
		}
	}
}

// TRIANGULATE: cada built-in debe estar bien formado (Name, Description y Prompt no
// vacios) y los nombres no se repiten. Tumba un built-in incompleto o con nombre
// duplicado (que haria ambigua la seleccion por subagent_type).
func TestBuiltins_AllWellFormed(t *testing.T) {
	defs := Builtins()
	if len(defs) == 0 {
		t.Fatalf("Builtins no devolvio ninguna def")
	}

	seen := map[string]bool{}
	for i, d := range defs {
		if d.Name == "" {
			t.Errorf("def[%d].Name vacio; un built-in sin nombre no es referenciable", i)
		}
		if d.Description == "" {
			t.Errorf("def[%d] (%q).Description vacio; un built-in usable necesita descripcion", i, d.Name)
		}
		if d.Prompt == "" {
			t.Errorf("def[%d] (%q).Prompt vacio; un built-in usable necesita system prompt", i, d.Name)
		}
		if seen[d.Name] {
			t.Errorf("def[%d].Name = %q duplicado; los nombres de built-in deben ser unicos", i, d.Name)
		}
		seen[d.Name] = true
	}
}

// Catalog fusiona las defs descubiertas en dirs (que ganan) con los built-in no
// sobreescritos. Una def del workspace con el mismo nombre que un built-in lo
// OVERRIDE (igual que las skills propias ganan sobre las estandar); un built-in
// sin choque se conserva. RED: Catalog aun no existe.
func TestCatalog_WorkspaceOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()
	// def propia llamada 'explore' que choca con el built-in homonimo.
	writeAgent(t, dir, "explore", "explore", "explore del workspace")

	defs, err := Catalog(dir)
	if err != nil {
		t.Fatalf("Catalog: error inesperado: %v", err)
	}

	// El 'explore' del workspace gano sobre el built-in.
	var explore Def
	foundExplore := false
	for _, d := range defs {
		if d.Name == "explore" {
			explore = d
			foundExplore = true
			break
		}
	}
	if !foundExplore {
		t.Fatalf("Catalog no incluye un agente 'explore'; got %v", defs)
	}
	if explore.Description != "explore del workspace" {
		t.Errorf("explore.Description = %q, quiero %q (la del workspace gano)", explore.Description, "explore del workspace")
	}

	// Un built-in no sobreescrito se conserva en el catalogo.
	foundGeneral := false
	for _, d := range defs {
		if d.Name == "general" {
			foundGeneral = true
			break
		}
	}
	if !foundGeneral {
		t.Fatalf("Catalog no incluye el built-in 'general' no sobreescrito; got %v", defs)
	}
}

// TRIANGULATE: ante una def con el mismo nombre en dos directorios, gana la del
// directorio listado primero. Lo verificamos por Location, que apunta al archivo
// del directorio ganador.
func TestDiscover_DedupesByNameFirstWins(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	locA := writeAgent(t, dirA, "dup", "dup", "de A")
	writeAgent(t, dirB, "dup", "dup", "de B")

	list, err := Discover(dirA, dirB)
	if err != nil {
		t.Fatalf("Discover: error inesperado: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("Discover = %d defs, quiero 1 (deduplicada por nombre)", len(list))
	}
	if list[0].Location != locA {
		t.Errorf("Location = %q, quiero la del primer directorio %q", list[0].Location, locA)
	}
}

// TRIANGULATE: una def del workspace que NO choca con ningun built-in se anade al
// catalogo junto a todos los built-in. El catalogo debe contener el agente nuevo
// 'auditor' Y los built-in 'explore' y 'general'. Tumba una version que devuelva
// solo lo descubierto (perdiendo los built-in) o solo los built-in (ignorando el
// workspace).
func TestCatalog_AddsWorkspaceAgentsAlongsideBuiltins(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "auditor", "auditor", "agente del workspace")

	defs, err := Catalog(dir)
	if err != nil {
		t.Fatalf("Catalog: error inesperado: %v", err)
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"auditor", "explore", "general"} {
		if !names[want] {
			t.Errorf("Catalog no incluye %q; got %v", want, names)
		}
	}
}

// TRIANGULATE: sin dirs, Catalog devuelve exactamente los built-in. Contiene
// 'explore' y 'general' y su longitud coincide con Builtins(). Tumba una version
// que pierda los built-in cuando no se pasan directorios.
func TestCatalog_NoDirsReturnsBuiltins(t *testing.T) {
	defs, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: error inesperado: %v", err)
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"explore", "general"} {
		if !names[want] {
			t.Errorf("Catalog() no incluye el built-in %q; got %v", want, names)
		}
	}
	if len(defs) != len(Builtins()) {
		t.Errorf("len(Catalog()) = %d, quiero %d (solo los built-in)", len(defs), len(Builtins()))
	}
}

// TRIANGULATE: un directorio inexistente no es error ni vacia el catalogo; Catalog
// devuelve los built-in igualmente. Confirma que un dir ausente no rompe el
// descubrimiento ni descarta los built-in.
func TestCatalog_MissingDirReturnsBuiltins(t *testing.T) {
	defs, err := Catalog(filepath.Join(t.TempDir(), "no", "existe"))
	if err != nil {
		t.Fatalf("Catalog dir ausente: error inesperado: %v", err)
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"explore", "general"} {
		if !names[want] {
			t.Errorf("Catalog(dir ausente) no incluye el built-in %q; got %v", want, names)
		}
	}
}
