package learning

import (
	"context"
	"errors"
	"fmt"
	"strings"

	coresession "github.com/K3N4Y/atenea/agentcore/session"
	"github.com/K3N4Y/atenea/internal/session"
)

var ErrNoDurableEvidence = errors.New("session has no durable evidence")

const contextBudget = 24000

// Capture materializes an immutable, bounded cut of effective durable history.
func Capture(ctx context.Context, store session.Store, sessionID string) (Input, int64, error) {
	events, err := store.Events(ctx, sessionID, 0)
	if err != nil {
		return Input{}, 0, err
	}
	var in Input
	cut := int64(0)
	for _, ev := range events {
		if ev.Kind == coresession.KindStepEnded || ev.Kind == coresession.KindStepFailed {
			cut = int64(ev.Seq)
		}
	}
	if cut == 0 {
		return Input{}, 0, ErrNoDurableEvidence
	}
	var compact *coresession.CompactionCheckpoint
	var compactSeq int64
	for _, ev := range events {
		if int64(ev.Seq) <= cut && ev.Kind == coresession.KindContextCompacted && ev.Compaction != nil {
			compact, compactSeq = ev.Compaction, int64(ev.Seq)
		}
	}
	var messages []InputMessage
	for _, ev := range events {
		if int64(ev.Seq) > cut {
			break
		}
		var role, text string
		if compact != nil && int64(ev.Seq) < compactSeq && ev.Seq != compact.AnchorUserSeq && ev.Seq < compact.PreservedFromSeq {
			continue
		}
		switch ev.Kind {
		case coresession.KindStepEnded:
			if ev.Message != nil {
				role, text = string(ev.Message.Role), renderMessage(*ev.Message)
			}
		case coresession.KindToolSuccess, coresession.KindToolFailed:
			role = "tool"
			text = ev.ToolName + ": "
			if ev.Message != nil {
				text += ev.Message.Text
			} else {
				text += ev.Error
			}
			if ev.Diff != "" {
				text += "\nDiff:\n" + ev.Diff
			}
		case coresession.KindStepFailed:
			role, text = "error", ev.Error
		case coresession.KindContextCompacted:
			if ev.Compaction != nil {
				role, text = "summary", fmt.Sprint(ev.Compaction.Summary)
			}
		}
		if ev.Kind == "" && ev.Message != nil {
			role, text = string(ev.Message.Role), renderMessage(*ev.Message)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		messages = append(messages, InputMessage{Seq: int64(ev.Seq), Role: role, Text: text})
	}
	remaining := contextBudget
	if compact != nil {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "summary" {
				r := []rune(messages[i].Text)
				if len(r) > 4000 {
					r = r[:4000]
					in.Truncated = true
				}
				in.Messages = append(in.Messages, InputMessage{Seq: messages[i].Seq, Role: "summary", Text: string(r)})
				remaining -= len(r)
				messages = append(messages[:i], messages[i+1:]...)
				break
			}
		}
	}
	var newest []InputMessage
	for i := len(messages) - 1; i >= 0; i-- {
		r := []rune(messages[i].Text)
		if len(r) > remaining {
			r = r[len(r)-remaining:]
			in.Truncated = true
		}
		messages[i].Text = string(r)
		newest = append(newest, messages[i])
		remaining -= len(r)
		if remaining == 0 {
			if i > 0 {
				in.Truncated = true
			}
			break
		}
	}
	for i, j := 0, len(newest)-1; i < j; i, j = i+1, j-1 {
		newest[i], newest[j] = newest[j], newest[i]
	}
	in.Messages = append(in.Messages, newest...)
	if len(in.Messages) == 0 {
		return Input{}, cut, ErrNoDurableEvidence
	}
	return in, cut, nil
}

func renderMessage(m coresession.Message) string {
	var b strings.Builder
	b.WriteString(m.Text)
	for _, c := range m.ToolCalls {
		fmt.Fprintf(&b, "\nTool %s(%s)", c.Name, c.Arguments)
	}
	return b.String()
}
