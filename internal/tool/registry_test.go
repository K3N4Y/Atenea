package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"atenea/internal/llm"
)

// spyTool es una tool de prueba que cuenta sus ejecuciones por puntero (Tool se
// pasa por valor, asi que el contador debe ser compartido) y devuelve un output
// y un error configurables. Sirve para verificar "sin efectos laterales": si el
// registry rechaza la Call antes de actuar, el contador queda en 0.
type spyTool struct {
	name    string
	calls   *int
	out     string
	diff    string
	execErr error
}

func (s spyTool) Name() string            { return s.name }
func (s spyTool) Description() string     { return s.name + " spy" }
func (s spyTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s spyTool) Execute(ctx context.Context, in json.RawMessage) (Result, error) {
	*s.calls++
	return Result{Output: s.out, Diff: s.diff}, s.execErr
}

// TestRegistry_SettlePreservesDiffThroughCap afirma que el Diff (solo-UI) sobrevive
// al acotado del OutputStore: aunque el Output se trunque, el Diff pasa entero (no
// se acota).
func TestRegistry_SettlePreservesDiffThroughCap(t *testing.T) {
	calls := 0
	reg := NewRegistry(NewOutputStore(5), spyTool{name: "edit", calls: &calls, out: "0123456789", diff: "DIFF"})
	mat := reg.Materialize(Permissions{"edit": true})

	res, err := mat.Settle(context.Background(), Call{ID: "c1", Name: "edit"})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if !res.Truncated {
		t.Fatalf("se esperaba Output truncado")
	}
	if res.Output != "01234" {
		t.Fatalf("Output: se esperaba %q, se obtuvo %q", "01234", res.Output)
	}
	if res.Diff != "DIFF" {
		t.Fatalf("Diff: se esperaba %q (no acotado), se obtuvo %q", "DIFF", res.Diff)
	}
}

// TestRegistry_SettleNoDiffStaysEmpty: una tool sin diff (p.ej. bash) deja Diff vacio.
func TestRegistry_SettleNoDiffStaysEmpty(t *testing.T) {
	calls := 0
	reg := NewRegistry(NewOutputStore(0), spyTool{name: "bash", calls: &calls, out: "ok"})
	mat := reg.Materialize(Permissions{"bash": true})

	res, err := mat.Settle(context.Background(), Call{ID: "c1", Name: "bash"})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if res.Diff != "" {
		t.Fatalf("Diff: se esperaba vacio, se obtuvo %q", res.Diff)
	}
}

// TestRegistry_SettleExecutesAllowedTool es el happy path del registry: arma el
// catalogo con el builtin echo, materializa con permisos que lo anuncian y
// asienta una Call conocida. Afirma que Definitions lista solo echo (con su
// llm.ToolDef) y que Settle ejecuta la tool y devuelve su Result sin error.
func TestRegistry_SettleExecutesAllowedTool(t *testing.T) {
	reg := NewRegistry(NewOutputStore(0), Echo{})
	mat := reg.Materialize(Permissions{"echo": true})

	if len(mat.Definitions) != 1 {
		t.Fatalf("Definitions: se esperaba 1 def, se obtuvieron %d", len(mat.Definitions))
	}
	if got := mat.Definitions[0].Name; got != "echo" {
		t.Fatalf("Definitions[0].Name: se esperaba %q, se obtuvo %q", "echo", got)
	}
	var _ llm.ToolDef = mat.Definitions[0]

	call := Call{ID: "c1", Name: "echo", Input: json.RawMessage([]byte(`{"text":"hola"}`))}
	res, err := mat.Settle(context.Background(), call)
	if err != nil {
		t.Fatalf("Settle: error inesperado: %v", err)
	}
	if want := (Result{Output: "hola"}); res != want {
		t.Fatalf("Settle: se esperaba %+v, se obtuvo %+v", want, res)
	}
}

// TestRegistry_DeniedToolAbsentFromDefinitions afirma que una tool registrada
// pero no permitida no se anuncia: Definitions lista solo la permitida. El set
// anunciado es la compuerta; lo denegado no aparece para el modelo.
func TestRegistry_DeniedToolAbsentFromDefinitions(t *testing.T) {
	var calls int
	reg := NewRegistry(NewOutputStore(0), Echo{}, spyTool{name: "secret", calls: &calls})
	mat := reg.Materialize(Permissions{"echo": true})

	if len(mat.Definitions) != 1 {
		t.Fatalf("Definitions: se esperaba 1 def, se obtuvieron %d", len(mat.Definitions))
	}
	if got := mat.Definitions[0].Name; got != "echo" {
		t.Fatalf("Definitions[0].Name: se esperaba %q, se obtuvo %q", "echo", got)
	}
	for _, d := range mat.Definitions {
		if d.Name == "secret" {
			t.Fatalf("Definitions: la tool denegada %q no debia aparecer", "secret")
		}
	}
}

// TestRegistry_SettleUnknownToolHasNoSideEffects afirma que asentar una tool
// fuera del set materializado (denegada por permisos o no registrada) devuelve
// *UnknownToolError sin ejecutar nada: el contador del spy queda en 0.
func TestRegistry_SettleUnknownToolHasNoSideEffects(t *testing.T) {
	var calls int
	reg := NewRegistry(NewOutputStore(0), Echo{}, spyTool{name: "secret", calls: &calls})
	// La tool secret esta registrada pero denegada por permisos.
	mat := reg.Materialize(Permissions{"echo": true})

	// Caso 1: tool registrada pero denegada.
	_, err := mat.Settle(context.Background(), Call{ID: "c1", Name: "secret"})
	var unknown *UnknownToolError
	if !errors.As(err, &unknown) {
		t.Fatalf("Settle(secret): se esperaba *UnknownToolError, se obtuvo %v", err)
	}
	if calls != 0 {
		t.Fatalf("Settle(secret): la tool denegada no debia ejecutarse, contador = %d", calls)
	}

	// Caso 2: nombre no registrado en absoluto; no debe entrar en panico.
	_, err = mat.Settle(context.Background(), Call{ID: "c2", Name: "ghost"})
	var unknown2 *UnknownToolError
	if !errors.As(err, &unknown2) {
		t.Fatalf("Settle(ghost): se esperaba *UnknownToolError, se obtuvo %v", err)
	}
	if calls != 0 {
		t.Fatalf("Settle(ghost): no se debia ejecutar nada, contador = %d", calls)
	}
}

// TestRegistry_LargeOutputCappedViaOutputStore afirma que un output mayor al
// limite del OutputStore se devuelve acotado (Truncated) y que el completo queda
// recuperable por callID via Full.
func TestRegistry_LargeOutputCappedViaOutputStore(t *testing.T) {
	const limit = 10
	full := strings.Repeat("x", 100)
	var calls int
	store := NewOutputStore(limit)
	reg := NewRegistry(store, spyTool{name: "big", calls: &calls, out: full})
	mat := reg.Materialize(Permissions{"big": true})

	res, err := mat.Settle(context.Background(), Call{ID: "c1", Name: "big"})
	if err != nil {
		t.Fatalf("Settle: error inesperado: %v", err)
	}
	if len(res.Output) != limit {
		t.Fatalf("Output: se esperaba largo %d, se obtuvo %d", limit, len(res.Output))
	}
	if !res.Truncated {
		t.Fatalf("Truncated: se esperaba true para output mayor al limite")
	}
	got, ok := store.Full("c1")
	if !ok {
		t.Fatalf("Full(c1): no se encontro el output completo")
	}
	if len(got) != len(full) {
		t.Fatalf("Full(c1): se esperaba largo %d, se obtuvo %d", len(full), len(got))
	}
}

// TestRegistry_DefinitionsSortedByName afirma que Definitions sale ordenado por
// Name aunque las tools se registren desordenadas: el request es determinista.
func TestRegistry_DefinitionsSortedByName(t *testing.T) {
	var z, a, e int
	reg := NewRegistry(NewOutputStore(0),
		spyTool{name: "zeta", calls: &z},
		spyTool{name: "alpha", calls: &a},
		spyTool{name: "echo", calls: &e},
	)
	mat := reg.Materialize(Permissions{"zeta": true, "alpha": true, "echo": true})

	want := []string{"alpha", "echo", "zeta"}
	if len(mat.Definitions) != len(want) {
		t.Fatalf("Definitions: se esperaban %d defs, se obtuvieron %d", len(want), len(mat.Definitions))
	}
	for i, name := range want {
		if got := mat.Definitions[i].Name; got != name {
			t.Fatalf("Definitions[%d].Name: se esperaba %q, se obtuvo %q", i, name, got)
		}
	}
}

// TestRegistry_SettleToolExecuteErrorPropagates afirma que un error de Execute se
// propaga tal cual por Settle y NO se confunde con UnknownToolError: M5 distingue
// "no permitida" de "fallo al ejecutar".
func TestRegistry_SettleToolExecuteErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	var calls int
	reg := NewRegistry(NewOutputStore(0), spyTool{name: "fail", calls: &calls, execErr: boom})
	mat := reg.Materialize(Permissions{"fail": true})

	_, err := mat.Settle(context.Background(), Call{ID: "c1", Name: "fail"})
	if !errors.Is(err, boom) {
		t.Fatalf("Settle: se esperaba el error de Execute %v, se obtuvo %v", boom, err)
	}
	var unknown *UnknownToolError
	if errors.As(err, &unknown) {
		t.Fatalf("Settle: un error de Execute no debe ser *UnknownToolError, se obtuvo %v", err)
	}
	if calls != 1 {
		t.Fatalf("Settle: la tool permitida debia ejecutarse una vez, contador = %d", calls)
	}
}
