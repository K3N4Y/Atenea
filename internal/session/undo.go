package session

import (
	"context"
	"errors"
	"strings"
)

var ErrNothingToUndo = errors.New("nothing to undo")

type EffectiveCheckpoint struct {
	ID           string
	Prompt       string
	PromptImages []Image
	BeforeTree   string
	AfterTree    string
	StartSeq     Seq
	FinishSeq    Seq
}

type UndoStore interface {
	Store
	LatestPromptCheckpoint(ctx context.Context, sessionID string) (EffectiveCheckpoint, error)
}

func EffectiveEvents(events []SessionEvent) []SessionEvent {
	reverted := make(map[string]struct{})
	ranges := make(map[string][2]Seq)
	origins := make(map[string]string)
	for _, event := range events {
		if event.Kind == KindToolSuccess || event.Kind == KindToolFailed {
			if id := origins[event.CallID]; id != "" {
				rangeSeq := ranges[id]
				rangeSeq[0] = event.Seq + 1
				ranges[id] = rangeSeq
				delete(origins, event.CallID)
			}
			continue
		}
		if event.Checkpoint == nil {
			continue
		}
		switch event.Kind {
		case KindPromptCheckpointStarted:
			rangeSeq := ranges[event.Checkpoint.ID]
			rangeSeq[0] = event.Seq
			ranges[event.Checkpoint.ID] = rangeSeq
			if event.Checkpoint.OriginCallID != "" {
				origins[event.Checkpoint.OriginCallID] = event.Checkpoint.ID
			}
		case KindPromptCheckpointFinished:
			rangeSeq := ranges[event.Checkpoint.ID]
			rangeSeq[1] = event.Seq
			ranges[event.Checkpoint.ID] = rangeSeq
		case KindPromptCheckpointReverted:
			rangeSeq := ranges[event.Checkpoint.ID]
			rangeSeq[1] = event.Seq
			ranges[event.Checkpoint.ID] = rangeSeq
			reverted[event.Checkpoint.ID] = struct{}{}
		}
	}
	out := make([]SessionEvent, 0, len(events))
	for _, event := range events {
		if event.Kind == KindPromptCheckpointReverted {
			continue
		}
		if event.Checkpoint != nil {
			if _, ok := reverted[event.Checkpoint.ID]; ok {
				continue
			}
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
	return latestEffectiveCheckpoint(events, false)
}

func LatestExplicitCheckpoint(events []SessionEvent) (EffectiveCheckpoint, error) {
	return latestEffectiveCheckpoint(events, true)
}

func latestEffectiveCheckpoint(events []SessionEvent, explicit bool) (EffectiveCheckpoint, error) {
	reverted := make(map[string]struct{})
	checkpoints := make(map[string]EffectiveCheckpoint)
	order := make([]string, 0)
	for _, event := range events {
		if event.Checkpoint == nil || IsExplicitCheckpointID(event.Checkpoint.ID) != explicit {
			continue
		}
		checkpoint := event.Checkpoint
		switch event.Kind {
		case KindPromptCheckpointStarted:
			checkpoints[checkpoint.ID] = EffectiveCheckpoint{ID: checkpoint.ID, Prompt: checkpoint.Prompt, PromptImages: cloneImages(checkpoint.PromptImages), BeforeTree: checkpoint.BeforeTree, StartSeq: event.Seq}
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

const explicitCheckpointPrefix = "explicit-checkpoint-"

func ExplicitCheckpointID(suffix string) string { return explicitCheckpointPrefix + suffix }
func IsExplicitCheckpointID(id string) bool     { return strings.HasPrefix(id, explicitCheckpointPrefix) }
