package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestTodoWriteTool_ReturnsAck afirma el caso base: Execute con una lista valida
// no falla, devuelve un Output con la cuenta y NO le dice al modelo que se
// detenga (a diferencia de present_plan): el agente sigue trabajando tras
// actualizar su checklist.
func TestTodoWriteTool_ReturnsAck(t *testing.T) {
	in := json.RawMessage(`{"todos":[{"content":"a","status":"completed"},{"content":"b","status":"in_progress"}]}`)
	res, err := (TodoWriteTool{}).Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if !strings.Contains(res.Output, "2") {
		t.Errorf("Output = %q, quiero que mencione la cuenta (2)", res.Output)
	}
	if strings.Contains(strings.ToUpper(res.Output), "DETENTE") {
		t.Errorf("Output = %q, no debe pedir al modelo detenerse", res.Output)
	}
}

// TestTodoWriteTool_RejectsInvalidStatus triangula: un status fuera del enum es
// un error de tool, para que el modelo se autocorrija en el siguiente turno.
func TestTodoWriteTool_RejectsInvalidStatus(t *testing.T) {
	in := json.RawMessage(`{"todos":[{"content":"a","status":"doing"}]}`)
	if _, err := (TodoWriteTool{}).Execute(context.Background(), in); err == nil {
		t.Fatal("Execute: se esperaba error por status invalido, no hubo")
	}
}

// TestTodoWriteTool_RejectsInvalidJSON triangula el borde: input que no es el
// JSON esperado es error de tool (igual que el resto de builtins).
func TestTodoWriteTool_RejectsInvalidJSON(t *testing.T) {
	if _, err := (TodoWriteTool{}).Execute(context.Background(), json.RawMessage(`no-json`)); err == nil {
		t.Fatal("Execute: se esperaba error por JSON invalido, no hubo")
	}
}
