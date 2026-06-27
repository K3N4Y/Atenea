package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
)

// TodoWriteTool deja que el agente lleve su checklist de tareas en vivo: cada
// call REEMPLAZA la lista entera (idempotente, como Claude Code/Codex), asi el
// agente no se pierde en tareas de varios pasos. Sin estado ni FS: la lista
// viaja en el Input de Tool.Called y la UI la pinta; la rehidratacion la
// reconstruye reproduciendo el ultimo todo_write del historial. A diferencia de
// present_plan (que detiene al modelo y vive solo en plan-mode), esta es de modo
// normal y el agente sigue trabajando tras actualizarla.
type TodoWriteTool struct{}

//go:embed todo.txt
var todoDescription string

func (TodoWriteTool) Name() string        { return "todo_write" }
func (TodoWriteTool) Description() string { return todoDescription }

func (TodoWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"todos":{"type":"array","items":{"type":"object","properties":{"content":{"type":"string","description":"Tarea, en imperativo y corta."},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["content","status"]}}},"required":["todos"]}`)
}

// Execute parsea {todos}, valida que cada status este en el enum (un status
// invalido es error de tool para que el modelo se autocorrija) y devuelve un ack
// corto con la cuenta. No persiste nada: la UI es la fuente de display.
func (TodoWriteTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("todo_write: input invalido: %w", err)
	}
	for _, t := range in.Todos {
		switch t.Status {
		case "pending", "in_progress", "completed":
		default:
			return Result{}, fmt.Errorf("todo_write: status invalido %q (usa pending|in_progress|completed)", t.Status)
		}
	}
	return Result{Output: fmt.Sprintf("Lista de tareas actualizada (%d).", len(in.Todos))}, nil
}
