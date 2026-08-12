package runner

import (
	"context"
	"errors"

	"github.com/K3N4Y/atenea/internal/session"
)

// FinalTurnInstruction tells a finitely bounded activity to conclude from the
// work already completed. It is stable so providers and tests see one policy.
const FinalTurnInstruction = "This is your final turn. Do not call tools. Summarize the work completed and provide your final answer."

const interruptedToolMessage = "tool interrumpida antes de completar"

// Run drains the session inbox until it is idle. A non-positive maxSteps is
// unlimited; a positive value reserves its final turn for a tool-free summary.
// Text alone never continues an activity: only a local tool call or admitted
// steer does. Every turn rebuilds its request from the durable store.
func (r *Runner) Run(ctx context.Context, sessionID string, force bool, maxSteps int) error {
	hasSteer, err := r.inbox.HasPending(ctx, sessionID, session.DeliverySteer)
	if err != nil {
		return err
	}
	hasQueue := false
	if !hasSteer {
		if hasQueue, err = r.inbox.HasPending(ctx, sessionID, session.DeliveryQueue); err != nil {
			return err
		}
	}
	if !force && !hasSteer && !hasQueue {
		return nil // sesion idle, nada que hacer
	}

	if err := r.failInterruptedTools(ctx, sessionID); err != nil {
		return err
	}

	promotion := session.DeliveryNone
	switch {
	case hasSteer:
		promotion = session.DeliverySteer
	case hasQueue:
		promotion = session.DeliveryQueue
	}
	openActivity := force || hasSteer || hasQueue

	for openActivity {
		needsContinuation := true

		for step := 1; maxSteps <= 0 || step <= maxSteps; step++ {
			if err := r.promote(ctx, sessionID, promotion); err != nil {
				return err
			}
			finalTurn := maxSteps > 0 && step == maxSteps
			needsContinuation, err = r.runTurnWithFinal(ctx, sessionID, finalTurn)
			if err != nil {
				return err
			}
			promotion = session.DeliverySteer // tras el primer turno solo se promueve steer

			if finalTurn {
				needsContinuation = false
			} else if !needsContinuation {
				if needsContinuation, err = r.inbox.HasPending(ctx, sessionID, session.DeliverySteer); err != nil {
					return err
				}
			}
			if !needsContinuation {
				break
			}
		}
		if openActivity, err = r.inbox.HasPending(ctx, sessionID, session.DeliverySteer); err != nil {
			return err
		}
		if openActivity {
			promotion = session.DeliverySteer
			continue
		}
		if openActivity, err = r.inbox.HasPending(ctx, sessionID, session.DeliveryQueue); err != nil {
			return err
		}
		if openActivity {
			promotion = session.DeliveryQueue
		}
	}
	return nil
}

// failInterruptedTools closes tools that remained open after an interrupted run.
// If the crash happened before StepEnded materialized the assistant message, it
// first reconstructs that declaration so every tool result has a matching call.
func (r *Runner) failInterruptedTools(ctx context.Context, sessionID string) error {
	pending, err := r.store.PendingToolCalls(ctx, sessionID)
	if errors.Is(err, session.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	messages, err := r.store.Messages(ctx, sessionID, 0)
	if err != nil {
		return err
	}
	declared := make(map[string]struct{})
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			declared[call.ID] = struct{}{}
		}
	}
	var missing []session.ToolCall
	for _, p := range pending {
		if _, ok := declared[p.CallID]; ok {
			continue
		}
		arguments := p.Arguments
		if arguments == "" {
			arguments = "{}"
		}
		missing = append(missing, session.ToolCall{ID: p.CallID, Name: p.ToolName, Arguments: arguments})
	}
	if len(missing) > 0 {
		if _, err := r.store.AppendEvent(ctx, sessionID, session.SessionEvent{
			Message: &session.Message{ID: r.nextID(), Role: session.RoleAssistant, ToolCalls: missing},
		}); err != nil {
			return err
		}
	}
	for _, p := range pending {
		event := session.SessionEvent{
			Kind:     session.KindToolFailed,
			CallID:   p.CallID,
			ToolName: p.ToolName,
			Error:    interruptedToolMessage,
			Message:  &session.Message{ID: p.CallID, Role: session.RoleTool, Text: interruptedToolMessage, ToolCallID: p.CallID, IsError: true},
		}
		if p.ToolName == "task" {
			event = session.WithSubagentToolCalls(event, 0)
		}
		if _, err := r.store.AppendEvent(ctx, sessionID, event); err != nil {
			return err
		}
	}
	return nil
}

// promote drains from the inbox the prompts of delivery d and materializes them
// as Role:user messages in the Store, in admission order, so the next runTurn
// sees them in the history. DeliveryNone (or nothing pending) adds nothing: the
// turn runs with the existing history (e.g. a continuation after settling
// tools). Uses the runner's ID generator for the message ID.
// If the append fails, the already drained prompts go back to the inbox: user
// input is not lost to a broken store or a cancelled context.
func (r *Runner) promote(ctx context.Context, sessionID string, d session.Delivery) error {
	prompts, err := r.inbox.Promote(ctx, sessionID, d)
	if err != nil {
		return err
	}
	for i, p := range prompts {
		if _, err := r.store.AppendEvent(ctx, sessionID, session.SessionEvent{
			Message: &session.Message{ID: r.nextID(), Role: session.RoleUser, Text: p.Text, Images: p.Images},
		}); err != nil {
			return errors.Join(err, r.readmit(ctx, sessionID, prompts[i:], d))
		}
	}
	return nil
}

// readmit returns to the inbox prompts that left it but never reached the store.
// It uses a context without cancellation because the typical failure case is
// precisely the run's cancelled ctx.
// ponytail: they re-enter at the back of the queue; if another prompt arrived
// meanwhile, FIFO order is altered. If that matters, the Inbox needs
// promote/ack instead of a direct drain.
func (r *Runner) readmit(ctx context.Context, sessionID string, prompts []session.Prompt, d session.Delivery) error {
	ctx = context.WithoutCancel(ctx)
	var errs error
	for _, p := range prompts {
		errs = errors.Join(errs, r.inbox.Admit(ctx, sessionID, p, d))
	}
	return errs
}
