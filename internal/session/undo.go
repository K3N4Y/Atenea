package session

import (
	"context"
	"errors"
)

var ErrNothingToUndo = errors.New("nothing to undo")

type PromptCheckpoint struct {
	ID         string
	Prompt     string
	BeforeTree string
	AfterTree  string
}

type EffectiveCheckpoint struct {
	ID         string
	Prompt     string
	BeforeTree string
	AfterTree  string
	StartSeq   Seq
	FinishSeq  Seq
}

type UndoStore interface {
	Store
	LatestPromptCheckpoint(ctx context.Context, sessionID string) (EffectiveCheckpoint, error)
}

func EffectiveEvents(events []SessionEvent) []SessionEvent {
	reverted := make(map[string]struct{})
	ranges := make(map[string][2]Seq)
	for _, event := range events {
		if event.Checkpoint == nil {
			continue
		}
		switch event.Kind {
		case KindPromptCheckpointStarted:
			rangeSeq := ranges[event.Checkpoint.ID]
			rangeSeq[0] = event.Seq
			ranges[event.Checkpoint.ID] = rangeSeq
		case KindPromptCheckpointFinished:
			rangeSeq := ranges[event.Checkpoint.ID]
			rangeSeq[1] = event.Seq
			ranges[event.Checkpoint.ID] = rangeSeq
		case KindPromptCheckpointReverted:
			reverted[event.Checkpoint.ID] = struct{}{}
		}
	}
	out := make([]SessionEvent, 0, len(events))
	for _, event := range events {
		if event.Kind == KindPromptCheckpointReverted {
			continue
		}
		hidden := false
		for id := range reverted {
			rangeSeq := ranges[id]
			if rangeSeq[0] > 0 && event.Seq >= rangeSeq[0] && (rangeSeq[1] == 0 || event.Seq <= rangeSeq[1]) {
				hidden = true
				break
			}
		}
		if !hidden {
			out = append(out, cloneSessionEvent(event))
		}
	}
	return out
}

func LatestEffectiveCheckpoint(events []SessionEvent) (EffectiveCheckpoint, error) {
	reverted := make(map[string]struct{})
	checkpoints := make(map[string]EffectiveCheckpoint)
	order := make([]string, 0)
	for _, event := range events {
		if event.Checkpoint == nil {
			continue
		}
		checkpoint := event.Checkpoint
		switch event.Kind {
		case KindPromptCheckpointStarted:
			checkpoints[checkpoint.ID] = EffectiveCheckpoint{ID: checkpoint.ID, Prompt: checkpoint.Prompt, BeforeTree: checkpoint.BeforeTree, StartSeq: event.Seq}
			order = append(order, checkpoint.ID)
		case KindPromptCheckpointFinished:
			current := checkpoints[checkpoint.ID]
			current.AfterTree = checkpoint.AfterTree
			current.FinishSeq = event.Seq
			checkpoints[checkpoint.ID] = current
		case KindPromptCheckpointReverted:
			reverted[checkpoint.ID] = struct{}{}
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		id := order[i]
		if _, ok := reverted[id]; ok {
			continue
		}
		checkpoint := checkpoints[id]
		if checkpoint.StartSeq > 0 {
			return checkpoint, nil
		}
	}
	return EffectiveCheckpoint{}, ErrNothingToUndo
}
