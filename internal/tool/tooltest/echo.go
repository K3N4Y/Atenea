// Package tooltest provides shared tool fixtures for integration tests.
package tooltest

import (
	"context"
	"encoding/json"
	"fmt"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
)

// Echo is a deterministic no-effect tool for exercising registry and runner
// behavior without filesystem or process dependencies.
type Echo struct{}

func (Echo) Name() string              { return "echo" }
func (Echo) Description() string       { return "Echoes the provided text for tests." }
func (Echo) Effects() contract.Effects { return contract.NoEffects }
func (Echo) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
}
func (Echo) Execute(_ context.Context, input json.RawMessage) (contract.Result, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return contract.Result{}, fmt.Errorf("echo: invalid input: %w", err)
	}
	return contract.Result{Output: in.Text}, nil
}
