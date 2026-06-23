package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPresentPlanTool_SavesPlanFileUnderRoot afirma el caso base: Execute guarda
// el plan en <root>/.atenea/plans/plan-<sessionID>.md con el contenido exacto y
// devuelve un Output que menciona la ruta relativa.
func TestPresentPlanTool_SavesPlanFileUnderRoot(t *testing.T) {
	root := t.TempDir()
	tool := NewPresentPlanTool(root)
	ctx := WithSessionID(context.Background(), "s1")

	plan := "# Plan\n- paso 1\n"
	in := json.RawMessage(`{"title":"T","plan":"# Plan\n- paso 1\n"}`)
	res, err := tool.Execute(ctx, in)
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}

	path := filepath.Join(root, ".atenea", "plans", "plan-s1.md")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("leer plan guardado: %v", err)
	}
	if string(got) != plan {
		t.Errorf("contenido del plan = %q, quiero %q", string(got), plan)
	}

	if !strings.Contains(res.Output, ".atenea/plans/plan-s1.md") {
		t.Errorf("Output = %q, quiero que contenga la ruta relativa .atenea/plans/plan-s1.md", res.Output)
	}
}

// TestPresentPlanTool_OverwritesExistingPlan afirma que un plan se revisa en
// sitio: dos Execute para la misma sesion dejan el SEGUNDO plan en disco.
func TestPresentPlanTool_OverwritesExistingPlan(t *testing.T) {
	root := t.TempDir()
	tool := NewPresentPlanTool(root)
	ctx := WithSessionID(context.Background(), "s1")

	if _, err := tool.Execute(ctx, json.RawMessage(`{"plan":"primero"}`)); err != nil {
		t.Fatalf("Execute primero: %v", err)
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"plan":"segundo"}`)); err != nil {
		t.Fatalf("Execute segundo: %v", err)
	}

	path := filepath.Join(root, ".atenea", "plans", "plan-s1.md")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("leer plan guardado: %v", err)
	}
	if string(got) != "segundo" {
		t.Errorf("contenido del plan = %q, quiero %q (la revision sobrescribe)", string(got), "segundo")
	}
}

// TestPresentPlanTool_RejectsEmptyPlan afirma que un plan vacio/espacios es error
// y no escribe ningun archivo.
func TestPresentPlanTool_RejectsEmptyPlan(t *testing.T) {
	root := t.TempDir()
	tool := NewPresentPlanTool(root)
	ctx := WithSessionID(context.Background(), "s1")

	if _, err := tool.Execute(ctx, json.RawMessage(`{"plan":"  "}`)); err == nil {
		t.Fatal("Execute con plan vacio: quiero error, no obtuve ninguno")
	}

	path := filepath.Join(root, ".atenea", "plans", "plan-s1.md")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("no debe escribirse archivo con plan vacio; stat err = %v", err)
	}
}

// TestPresentPlanTool_DefaultsSessionWhenMissing afirma que sin sesion en el
// contexto se usa "default" en el nombre del archivo.
func TestPresentPlanTool_DefaultsSessionWhenMissing(t *testing.T) {
	root := t.TempDir()
	tool := NewPresentPlanTool(root)

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"plan":"x"}`)); err != nil {
		t.Fatalf("Execute sin sesion: %v", err)
	}

	path := filepath.Join(root, ".atenea", "plans", "plan-default.md")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("quiero plan-default.md; stat err = %v", err)
	}
}

// TestPresentPlanTool_RejectsInvalidJSON afirma que un input que ni siquiera
// parsea (el modelo emitio una tool call truncada/rota) es error y no escribe
// archivo. Distinto de plan vacio: aqui falla el Unmarshal, no la validacion.
func TestPresentPlanTool_RejectsInvalidJSON(t *testing.T) {
	root := t.TempDir()
	tool := NewPresentPlanTool(root)
	ctx := WithSessionID(context.Background(), "s1")

	if _, err := tool.Execute(ctx, json.RawMessage(`{"plan":`)); err == nil {
		t.Fatal("Execute con JSON invalido: quiero error, no obtuve ninguno")
	}

	path := filepath.Join(root, ".atenea", "plans", "plan-s1.md")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("no debe escribirse archivo con JSON invalido; stat err = %v", err)
	}
}

// TestPresentPlanTool_RejectsMissingPlanField afirma que un input bien formado
// pero SIN el campo plan (el modelo mando solo el titulo) es error: plan queda en
// "" y la validacion lo rechaza, sin escribir archivo.
func TestPresentPlanTool_RejectsMissingPlanField(t *testing.T) {
	root := t.TempDir()
	tool := NewPresentPlanTool(root)
	ctx := WithSessionID(context.Background(), "s1")

	if _, err := tool.Execute(ctx, json.RawMessage(`{"title":"solo titulo"}`)); err == nil {
		t.Fatal("Execute sin campo plan: quiero error, no obtuve ninguno")
	}

	path := filepath.Join(root, ".atenea", "plans", "plan-s1.md")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("no debe escribirse archivo sin campo plan; stat err = %v", err)
	}
}

// TestPresentPlanTool_SanitizesSessionIDSeparators afirma que un sessionID con
// separadores de path no escapa del directorio de planes: los "/" se reemplazan y
// el archivo queda DENTRO de <root>/.atenea/plans. Defensivo contra traversal.
func TestPresentPlanTool_SanitizesSessionIDSeparators(t *testing.T) {
	root := t.TempDir()
	tool := NewPresentPlanTool(root)
	ctx := WithSessionID(context.Background(), "../../etc/evil")

	if _, err := tool.Execute(ctx, json.RawMessage(`{"plan":"x"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	dir := filepath.Join(root, ".atenea", "plans")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("leer dir de planes: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("quiero exactamente 1 plan en %s, hay %d", dir, len(entries))
	}
	name := entries[0].Name()
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, os.PathSeparator) {
		t.Errorf("el nombre del plan no debe conservar separadores: %q", name)
	}
	// El plan NO debe haberse escrito fuera del sandbox de planes.
	if _, err := os.Stat(filepath.Join(root, "etc", "evil")); !os.IsNotExist(err) {
		t.Errorf("el plan escapo del directorio de planes; stat err = %v", err)
	}
}
