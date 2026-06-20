package tool

import (
	"context"
	"encoding/json"
	"testing"
)

// TestEcho_ExecuteReturnsText afirma que el builtin parsea el JSON y devuelve el
// campo text tal cual, sin error: el happy path de la tool aislada.
func TestEcho_ExecuteReturnsText(t *testing.T) {
	res, err := Echo{}.Execute(context.Background(), json.RawMessage(`{"text":"hola"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if want := (Result{Output: "hola"}); res != want {
		t.Fatalf("Execute: se esperaba %+v, se obtuvo %+v", want, res)
	}
}

// TestEcho_InvalidInputErrors afirma que un input que no es el JSON esperado es
// un error de la tool (no del registry): Execute lo devuelve y Settle lo propaga.
func TestEcho_InvalidInputErrors(t *testing.T) {
	_, err := Echo{}.Execute(context.Background(), json.RawMessage(`{`))
	if err == nil {
		t.Fatalf("Execute: se esperaba error con input invalido, se obtuvo nil")
	}
}
