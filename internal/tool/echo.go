package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
)

// Echo es el primer builtin: devuelve tal cual el texto recibido. No tiene
// efectos laterales ni toca el FS, asi que da algo ejecutable y determinista para
// probar el registry de punta a punta (materializar -> Settle -> Result) sin
// arrastrar la maquinaria de read/edit (hashline, ver
// docs/atenea-read-edit-tools.md), que llega despues con su propio plan.
type Echo struct{}

//go:embed echo.txt
var echoDescription string

func (Echo) Name() string        { return "echo" }
func (Echo) Description() string { return echoDescription }

func (Echo) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
}

// Execute parsea el input JSON (nunca por match de string) y devuelve el campo
// text. Un input que no es el JSON esperado es un error de la tool, no del
// registry: Settle lo propaga y M5 lo asienta como Tool.Failed.
func (Echo) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("echo: input invalido: %w", err)
	}
	return Result{Output: in.Text}, nil
}
