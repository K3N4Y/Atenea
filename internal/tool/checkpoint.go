package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
)

type CheckpointFunc func(sessionID, callID string) (string, error)
type RewindFunc func(sessionID string) (string, error)

type CheckpointTool struct{ checkpoint CheckpointFunc }
type RewindTool struct{ rewind RewindFunc }

func NewCheckpointTool(checkpoint CheckpointFunc) *CheckpointTool {
	return &CheckpointTool{checkpoint: checkpoint}
}
func NewRewindTool(rewind RewindFunc) *RewindTool { return &RewindTool{rewind: rewind} }

func (*CheckpointTool) Name() string { return "checkpoint" }

//go:embed checkpoint.txt
var checkpointDescription string

func (*CheckpointTool) Description() string     { return checkpointDescription }
func (*CheckpointTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (*CheckpointTool) Effects() Effects        { return NoEffects }
func (t *CheckpointTool) Execute(ctx context.Context, _ json.RawMessage) (Result, error) {
	if t.checkpoint == nil {
		return Result{}, fmt.Errorf("checkpoint: unavailable")
	}
	id, err := t.checkpoint(SessionIDFrom(ctx), CallIDFrom(ctx))
	if err != nil {
		return Result{}, fmt.Errorf("checkpoint: %w", err)
	}
	return Result{Output: "Checkpoint created: " + id}, nil
}

func (*RewindTool) Name() string { return "rewind" }

//go:embed rewind.txt
var rewindDescription string

func (*RewindTool) Description() string     { return rewindDescription }
func (*RewindTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (*RewindTool) Effects() Effects        { return WritesFiles }
func (t *RewindTool) Execute(ctx context.Context, _ json.RawMessage) (Result, error) {
	if t.rewind == nil {
		return Result{}, fmt.Errorf("rewind: unavailable")
	}
	id, err := t.rewind(SessionIDFrom(ctx))
	if err != nil {
		return Result{}, fmt.Errorf("rewind: %w", err)
	}
	return Result{Output: "Rewind scheduled for checkpoint: " + id}, nil
}
