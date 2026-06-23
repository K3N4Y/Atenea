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
