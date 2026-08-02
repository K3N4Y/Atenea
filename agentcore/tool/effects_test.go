package tool

import (
	"context"
	"encoding/json"
	"testing"
)

type callDeclaringStub struct{}

func (callDeclaringStub) Name() string            { return "deep" }
func (callDeclaringStub) Description() string     { return "Deep tool." }
func (callDeclaringStub) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (callDeclaringStub) Execute(context.Context, json.RawMessage) (Result, error) {
	return Result{}, nil
}
func (callDeclaringStub) Effects() Effects { return ReachesNetwork }
func (callDeclaringStub) CallEffects(call Call) Effects {
	if string(call.Input) == "write" {
		return WritesFiles
	}
	return NoEffects
}

func TestEffectsForCall_PrefersPerCallDeclaration(t *testing.T) {
	subject := callDeclaringStub{}
	if got, declared := EffectsForCall(subject, Call{Input: []byte("write")}); !declared || got != WritesFiles {
		t.Fatalf("EffectsForCall = %v, %v; want writes-files, true", got, declared)
	}
	if got, declared := EffectsForCall(subject, Call{}); !declared || got != NoEffects {
		t.Fatalf("EffectsForCall = %v, %v; want none, true", got, declared)
	}
}
