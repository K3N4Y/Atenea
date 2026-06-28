package skill

// Test white-box (mismo package skill) por consistencia con skill_test.go. Cubre
// el comportamiento de ExtractBuiltins: materializar en disco las skills built-in
// embebidas y que queden descubribles por Discover. RED: ExtractBuiltins aun no
// existe (falla de compilacion esperada: undefined: ExtractBuiltins).

import (
	"os"
	"path/filepath"
	"testing"
)

// ExtractBuiltins escribe las skills built-in embebidas bajo destDir, una de
// ellas llamada "ponytail", conservando la estructura <destDir>/<name>/SKILL.md.
// Tras extraerlas, Discover(destDir) debe verlas como skills validas.
func TestExtractBuiltins_WritesDiscoverableSkill(t *testing.T) {
	dest := t.TempDir()

	if err := ExtractBuiltins(dest); err != nil {
		t.Fatalf("ExtractBuiltins: error inesperado: %v", err)
	}

	// La skill built-in queda en disco con la estructura esperada.
	skillPath := filepath.Join(dest, "ponytail", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Fatalf("os.Stat %s: %v", skillPath, err)
	}

	// Y queda descubrible por Discover, con el nombre correcto.
	list, err := Discover(dest)
	if err != nil {
		t.Fatalf("Discover: error inesperado: %v", err)
	}
	got := map[string]bool{}
	for _, s := range list {
		got[s.Name] = true
	}
	if !got["ponytail"] {
		t.Fatalf("Discover = %v, quiero contener la skill built-in 'ponytail'", got)
	}
}

// Extraer dos veces sobre el mismo destino no debe fallar ni perder la skill:
// ExtractBuiltins es seguro de repetir (idempotente), util cuando corre en cada
// arranque de la app.
func TestExtractBuiltins_IsIdempotent(t *testing.T) {
	dest := t.TempDir()

	if err := ExtractBuiltins(dest); err != nil {
		t.Fatalf("ExtractBuiltins (1ra): error inesperado: %v", err)
	}
	if err := ExtractBuiltins(dest); err != nil {
		t.Fatalf("ExtractBuiltins (2da): error inesperado: %v", err)
	}

	// Tras la segunda extraccion la skill built-in sigue descubrible.
	list, err := Discover(dest)
	if err != nil {
		t.Fatalf("Discover: error inesperado: %v", err)
	}
	got := map[string]bool{}
	for _, s := range list {
		got[s.Name] = true
	}
	if !got["ponytail"] {
		t.Fatalf("Discover = %v, quiero contener la skill built-in 'ponytail' tras 2da extraccion", got)
	}
}

// Si el archivo destino ya existe, ExtractBuiltins no lo pisa: respeta ediciones
// locales del usuario sobre una skill built-in ya materializada.
func TestExtractBuiltins_PreservesExistingFile(t *testing.T) {
	dest := t.TempDir()

	skillPath := filepath.Join(dest, "ponytail", "SKILL.md")
	const centinela = "---\nname: ponytail\ndescription: edicion local\n---\nEDICION-LOCAL-CENTINELA\n"
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatalf("os.MkdirAll: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(centinela), 0o644); err != nil {
		t.Fatalf("os.WriteFile centinela: %v", err)
	}

	if err := ExtractBuiltins(dest); err != nil {
		t.Fatalf("ExtractBuiltins: error inesperado: %v", err)
	}

	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("os.ReadFile %s: %v", skillPath, err)
	}
	if string(got) != centinela {
		t.Fatalf("ExtractBuiltins piso un archivo existente.\ngot = %q\nwant = %q", string(got), centinela)
	}
}
