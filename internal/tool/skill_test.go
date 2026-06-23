package tool

// Tests white-box de la tool skill: carga bajo demanda del cuerpo de una skill
// (disclosure progresivo). El catalogo se inyecta ya descubierto (skill.Discover
// corre una vez al ensamblar). Uno por comportamiento.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"atenea/internal/skill"
)

// newSkillOnDisk crea <root>/<name>/SKILL.md y devuelve la Info correspondiente
// (con Content y Location), como la entregaria skill.Discover.
func newSkillOnDisk(t *testing.T, root, name, content string) skill.Info {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	loc := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(loc, []byte("---\nname: "+name+"\n---\n"+content+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return skill.Info{Name: name, Description: "desc " + name, Location: loc, Content: content}
}

// Execute con un nombre del catalogo inyecta el cuerpo de la skill envuelto en
// <skill_content> junto con su directorio base. RED: SkillTool aun no existe.
func TestSkillTool_Execute_LoadsSkillContent(t *testing.T) {
	root := t.TempDir()
	info := newSkillOnDisk(t, root, "demo", "instrucciones de la skill demo")
	st := NewSkillTool([]skill.Info{info})

	res, err := st.Execute(context.Background(), json.RawMessage(`{"name":"demo"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	for _, want := range []string{
		`<skill_content name="demo">`,
		"instrucciones de la skill demo",
		filepath.Dir(info.Location),
		"</skill_content>",
	} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("Output no contiene %q; got:\n%s", want, res.Output)
		}
	}
}

// TRIANGULATE: un nombre fuera del catalogo es error y enumera las disponibles,
// para que el modelo reintente con un nombre valido.
func TestSkillTool_Execute_UnknownSkill(t *testing.T) {
	st := NewSkillTool([]skill.Info{{Name: "alfa", Description: "a"}, {Name: "bravo", Description: "b"}})

	_, err := st.Execute(context.Background(), json.RawMessage(`{"name":"zeta"}`))
	if err == nil {
		t.Fatal("Execute con skill inexistente: quiero error, no obtuve ninguno")
	}
	msg := err.Error()
	if !strings.Contains(msg, "alfa") || !strings.Contains(msg, "bravo") {
		t.Errorf("el error debe enumerar las skills disponibles; got: %q", msg)
	}
}

// TRIANGULATE: la salida lista los archivos del directorio de la skill como
// <file>...</file>, excluyendo el propio SKILL.md.
func TestSkillTool_Execute_ListsFiles(t *testing.T) {
	root := t.TempDir()
	info := newSkillOnDisk(t, root, "demo", "cuerpo")
	dir := filepath.Dir(info.Location)
	resource := filepath.Join(dir, "guia.md")
	if err := os.WriteFile(resource, []byte("recurso"), 0o644); err != nil {
		t.Fatalf("write recurso: %v", err)
	}
	st := NewSkillTool([]skill.Info{info})

	res, err := st.Execute(context.Background(), json.RawMessage(`{"name":"demo"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if !strings.Contains(res.Output, "<file>"+resource+"</file>") {
		t.Errorf("Output debe listar %q; got:\n%s", resource, res.Output)
	}
	if strings.Contains(res.Output, "<file>"+info.Location+"</file>") {
		t.Errorf("Output no debe listar el propio SKILL.md; got:\n%s", res.Output)
	}
}

// TRIANGULATE: un input que ni siquiera parsea (tool call truncada) es error.
func TestSkillTool_Execute_InvalidInput(t *testing.T) {
	st := NewSkillTool(nil)
	if _, err := st.Execute(context.Background(), json.RawMessage(`{"name":`)); err == nil {
		t.Fatal("Execute con JSON invalido: quiero error, no obtuve ninguno")
	}
}
