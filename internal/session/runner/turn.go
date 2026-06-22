package runner

import (
	"context"
	"errors"

	"golang.org/x/sync/errgroup"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// errRebuildTurn y errContinueAfterCompaction son senales de control internas del
// turno: nunca escapan de runTurn (el retry loop las traga). En vez de excepciones,
// Go usa sentinels envueltos y errors.Is.
//
//   - errRebuildTurn: el contexto cambio mientras se preparaba el turno (cambio de
//     agente/modelo o mismatch de la revision del epoch). El request quedo viejo: se
//     descarta y se reconstruye desde el store, SIN haber llamado al proveedor.
//   - errContinueAfterCompaction: hubo overflow de contexto antes de empezar el
//     mensaje del asistente. Se compacto el historial y se reintenta una vez por la
//     ruta post-compaction.
var (
	errRebuildTurn             = errors.New("rebuild prepared turn")
	errContinueAfterCompaction = errors.New("continue after overflow compaction")
)

// ProviderError envuelve un StepFailed del proveedor para devolverlo con
// errors.As y persistir la misma causa como Step.Failed.
type ProviderError struct {
	Message string
}

func (e *ProviderError) Error() string {
	if e.Message == "" {
		return "provider stream failed"
	}
	return "provider stream failed: " + e.Message
}

// runTurn ejecuta un turno reintentando ante las senales de control internas. El
// cuerpo del turno vive en runTurnAttempt; runTurn solo decide que hacer con su
// resultado: ante errRebuildTurn o errContinueAfterCompaction reintenta (el attempt
// se reconstruye desde el store, ya con el epoch reconciliado o el historial
// compactado); cualquier otro error, o el exito, se devuelve. La terminacion la
// garantiza el contrato de cada senal: el rebuild solo se dispara mientras el epoch
// siga cambiando (se estabiliza) y la compaction debe hacer progreso (el request
// deja de exceder el contexto).
func (r *Runner) runTurn(ctx context.Context, sessionID string) (bool, error) {
	for {
		cont, err := r.runTurnAttempt(ctx, sessionID)
		switch {
		case errors.Is(err, errRebuildTurn):
			continue // algo cambio mientras se preparaba: reconstruir desde el store
		case errors.Is(err, errContinueAfterCompaction):
			continue // hubo overflow: se compacto, reintentar por la ruta post-compaction
		default:
			return cont, err
		}
	}
}

// runTurnAttempt es UN intento del turno. Snapshotea el epoch al empezar a preparar,
// arma el Request desde el historial proyectado (a partir del BaselineSeq del epoch)
// y las tools materializadas, resolviendo el modelo del epoch. Si el request excede
// el contexto, compacta y devuelve errContinueAfterCompaction. Re-lee el epoch antes
// de llamar al proveedor: si cambio (agente/modelo/revision), devuelve errRebuildTurn
// SIN llamar a Stream. Si el epoch sigue vigente, llama Stream UNA vez y consume el
// stream igual que M5. Devuelve needsContinuation.
func (r *Runner) runTurnAttempt(ctx context.Context, sessionID string) (bool, error) {
	// Snapshot del contexto al empezar la preparacion.
	before, err := r.store.Epoch(ctx, sessionID)
	if err != nil {
		return false, err
	}

	// Historial proyectado desde el baseline del epoch y tools materializadas.
	msgs, err := r.store.Messages(ctx, sessionID, before.BaselineSeq)
	if err != nil {
		return false, err
	}
	mat := r.registry.Materialize(r.perms)
	req := llm.Request{Model: before.Model, Messages: toLLMMessages(msgs), Tools: mat.Definitions}

	// Overflow antes del mensaje del asistente: compactar y reintentar una vez.
	if r.compactor != nil && r.compactor.NeedsCompaction(req) {
		if err := r.compactor.Compact(ctx, sessionID); err != nil {
			return false, err
		}
		return false, errContinueAfterCompaction
	}

	// Re-leer el epoch antes de llamar al proveedor: si cambio, el request quedo
	// viejo. Se descarta y se reconstruye SIN haber llamado a Stream.
	after, err := r.store.Epoch(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if after != before {
		return false, errRebuildTurn
	}

	// Epoch vigente: una sola llamada al proveedor y consumo del stream (M5).
	in, err := r.provider.Stream(ctx, req)
	if err != nil {
		return false, err
	}
	pub := NewPublisher(r.store, sessionID, r.nextID())
	return r.consume(ctx, sessionID, in, pub, mat.Settle)
}

// consume drena el stream del turno. Publica cada evento como SessionEvent durable
// (orden total del log) y, por cada tool call LOCAL, lanza una goroutine que la
// asienta y publica su resultado. El turno ESPERA a que todas asienten (g.Wait)
// antes de decidir la continuacion. Una tool provider-executed solo se persiste
// (Publish), no se asienta. El error de una tool se registra como Tool.Failed y NO
// corta el turno; solo un fallo del store (de Publish/ToolSuccess/ToolFailed) lo
// hace. Ademas del Tool.Failed durable, el error se escribe por r.logf (stderr en
// `wails dev`): ni el log durable ni el mensaje al modelo son visibles para el dev.
func (r *Runner) consume(ctx context.Context, sessionID string, in <-chan llm.Event,
	pub *Publisher, settle tool.SettleFunc) (bool, error) {

	g, gctx := errgroup.WithContext(ctx)
	cleanupCtx := context.WithoutCancel(ctx)
	needsContinuation := false
	var streamErr *ProviderError
	for ev := range in {
		if ev.Kind == llm.StepFailed {
			streamErr = &ProviderError{Message: ev.Text}
			continue
		}
		if err := pub.Publish(ctx, ev); err != nil {
			return false, err
		}
		if ev.Kind == llm.ToolCall && !ev.ProviderExecuted {
			ev := ev // captura para la goroutine
			needsContinuation = true
			g.Go(func() error {
				res, err := settle(tool.WithSessionID(gctx, sessionID), tool.Call{ID: ev.CallID, Name: ev.ToolName, Input: ev.Input})
				if err != nil {
					r.logf("atenea: tool %q (call %s) fallo: %v", ev.ToolName, ev.CallID, err)
					return pub.ToolFailed(cleanupCtx, ev.CallID, err)
				}
				return pub.ToolSuccess(cleanupCtx, ev.CallID, res.Output)
			})
		}
	}
	if err := g.Wait(); err != nil {
		return false, err
	}
	var cause error
	if streamErr != nil {
		cause = streamErr
	} else {
		cause = ctx.Err()
	}
	if cause != nil {
		if err := pub.FailUnresolvedTools(cleanupCtx, cause); err != nil {
			return false, err
		}
		if err := pub.StepFailed(cleanupCtx, cause); err != nil {
			return false, err
		}
		return false, cause
	}
	return needsContinuation, nil
}

// toLLMMessages convierte el historial proyectado al formato del proveedor.
func toLLMMessages(msgs []session.Message) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = llm.Message{Role: string(m.Role), Text: m.Text}
	}
	return out
}
