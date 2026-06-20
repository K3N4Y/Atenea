package runner

import (
	"context"
	"fmt"

	"atenea/internal/session"
)

// MaxSteps corta loops improductivos de modelo/tool/continuacion: 25 pasos por
// actividad, igual que el loop de referencia (OpenCode).
const MaxSteps = 25

// StepLimitExceededError lo devuelve Run cuando una actividad agota MaxSteps sin
// dejar la sesion estable (el modelo siguio pidiendo continuacion). Tipo (no
// sentinel) para que la UI (M9) lo distinga de un fallo de proveedor con
// errors.As y muestre el limite.
type StepLimitExceededError struct {
	Max int
}

func (e *StepLimitExceededError) Error() string {
	return fmt.Sprintf("step limit exceeded: %d steps", e.Max)
}

// Run es el loop externo del agente: drena el Inbox de la sesion hasta dejarla
// idle. Si no hay steer ni queue pendiente (y no es force), retorna sin hacer
// nada. Mientras haya una actividad abierta corre el loop de pasos (hasta
// MaxSteps): promueve el input pendiente, ejecuta un turno (runTurn de M5) y
// decide si continuar. El loop NO continua por texto del asistente; solo por una
// tool call local (needsContinuation del turno) o por un steer admitido durante
// la corrida. Al cerrar una actividad revisa si hay otro queue y, si lo hay, abre
// una nueva. El request se reconstruye del Store en cada turno: Run no guarda
// estado vivo entre turnos.
func (r *Runner) Run(ctx context.Context, sessionID string, force bool) error {
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

	// failInterruptedTools (limpieza de tools colgadas tras crash) entra en M8.

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

		for step := 0; step < MaxSteps; step++ {
			if err := r.promote(ctx, sessionID, promotion); err != nil {
				return err
			}
			needsContinuation, err = r.runTurn(ctx, sessionID)
			if err != nil {
				return err
			}
			promotion = session.DeliverySteer // tras el primer turno solo se promueve steer

			if !needsContinuation {
				if needsContinuation, err = r.inbox.HasPending(ctx, sessionID, session.DeliverySteer); err != nil {
					return err
				}
			}
			if !needsContinuation {
				break
			}
		}
		if needsContinuation {
			return &StepLimitExceededError{Max: MaxSteps}
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
