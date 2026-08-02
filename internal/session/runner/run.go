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

// failInterruptedTools cierra al inicio de Run las tools que quedaron llamadas
// sin resultado en una corrida anterior. Usa PendingToolCalls y agrega
// Tool.Failed + Message{Role: tool} para que la reanudacion no deje calls
// colgadas.
func (r *Runner) failInterruptedTools(ctx context.Context, sessionID string) error {
	pending, err := r.store.PendingToolCalls(ctx, sessionID)
	if errors.Is(err, session.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, p := range pending {
		event := session.SessionEvent{
			Kind:     session.KindToolFailed,
			CallID:   p.CallID,
			ToolName: p.ToolName,
			Error:    interruptedToolMessage,
			Message:  &session.Message{ID: p.CallID, Role: session.RoleTool, Text: interruptedToolMessage},
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

// promote saca del inbox los prompts de la entrega d y los materializa como
// mensajes Role:user en el Store, en orden de admision, para que el proximo
// runTurn los vea en el historial. DeliveryNone (o sin pendientes) no agrega
// nada: el turno corre con el historial existente (p.ej. una continuacion tras
// asentar tools). Usa el generador de IDs del runner para el ID del mensaje.
func (r *Runner) promote(ctx context.Context, sessionID string, d session.Delivery) error {
	prompts, err := r.inbox.Promote(ctx, sessionID, d)
	if err != nil {
		return err
	}
	for _, p := range prompts {
		if _, err := r.store.AppendEvent(ctx, sessionID, session.SessionEvent{
			Message: &session.Message{ID: r.nextID(), Role: session.RoleUser, Text: p.Text},
		}); err != nil {
			return err
		}
	}
	return nil
}
