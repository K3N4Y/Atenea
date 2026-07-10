package runner

import (
	"context"
	"encoding/json"
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

// errPermissionDenied is the cause of the Tool.Failed when the user denies a
// gated tool call (ask-before-run). The message travels to the model as the
// tool's result so it understands the rejection and reacts.
var errPermissionDenied = errors.New("tool denied by the user")

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
	// Modo del turno: en plan-mode se arma el Request con el system y los permisos
	// de plan; si no hay hook de modo (nil) el modo es normal e identico a hoy.
	sys := r.system
	perms := r.perms
	if r.mode != nil && r.mode(sessionID) == session.ModePlan {
		if r.planSystem != nil {
			sys = r.planSystem
		}
		if r.planPerms != nil {
			perms = r.planPerms
		}
	}
	mat := r.registry.Materialize(perms)
	req := llm.Request{Model: before.Model, Messages: toLLMMessages(msgs), Tools: mat.Definitions}
	if sys != nil {
		req.System = sys(before.Model)
	}

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
	usageRequest := req
	usageRequest.MaxOutputTokens = 0
	pub := NewPublisher(r.store, sessionID, r.nextID(), llm.EstimateRequestTokens(usageRequest))
	return r.consume(ctx, sessionID, in, pub, mat.Settle)
}

// consume drena el stream del turno. Publica cada evento como SessionEvent durable
// (orden total del log) y acumula las tool calls LOCALES; recien cuando el stream
// TERMINO (todos sus eventos publicados, incluido Step.Ended, que materializa el
// Message assistant con los tool_calls) lanza las goroutines que asientan cada tool
// y publican su resultado. Ese orden es un invariante del historial: el Message
// role=tool de un resultado nunca puede preceder al Message assistant que declara
// su tool_call (los providers rechazan con 400 un historial `user, tool, assistant`).
// Diferir el settle no cambia nada para el modelo: el resultado de una tool solo
// alimenta el turno SIGUIENTE, y el adapter emite los ToolCall al final del stream
// de todos modos. Las tools siguen asentandose en paralelo ENTRE SI y el turno
// ESPERA a que todas asienten (g.Wait) antes de decidir la continuacion. Una tool
// provider-executed solo se persiste (Publish), no se asienta. El error de una tool
// se registra como Tool.Failed y NO corta el turno; solo un fallo del store (de
// Publish/ToolSuccess/ToolFailed) lo hace. Ademas del Tool.Failed durable, el error
// se escribe por r.logf (stderr en `wails dev`): ni el log durable ni el mensaje al
// modelo son visibles para el dev.
func (r *Runner) consume(ctx context.Context, sessionID string, in <-chan llm.Event,
	pub *Publisher, settle tool.SettleFunc) (bool, error) {

	g, gctx := errgroup.WithContext(ctx)
	cleanupCtx := context.WithoutCancel(ctx)
	needsContinuation := false
	var streamErr *ProviderError
	var calls []llm.Event
	for ev := range in {
		if ev.Kind == llm.StepFailed {
			streamErr = &ProviderError{Message: ev.Text}
			continue
		}
		if err := pub.Publish(ctx, ev); err != nil {
			return false, err
		}
		if ev.Kind == llm.ToolCall && !ev.ProviderExecuted {
			needsContinuation = true
			calls = append(calls, ev)
		}
	}
	// El stream termino: el Message assistant ya esta persistido. Recien ahora se
	// asientan las tools, en paralelo entre si.
	for _, ev := range calls {
		ev := ev // capture for the goroutine
		g.Go(func() error {
			call := tool.Call{ID: ev.CallID, Name: ev.ToolName, Input: ev.Input}
			// Ask-before-run: if the tool is gated, ask for approval before
			// settling. The request is persisted (the UI shows the prompt) and
			// Ask blocks until the decision. Deny publishes Tool.Failed and does
			// NOT run the tool.
			if r.gate != nil && r.needsApproval != nil && r.needsApproval(call) {
				if err := pub.ToolPermissionRequested(cleanupCtx, ev.CallID); err != nil {
					return err
				}
				approved, askErr := r.gate.Ask(gctx, session.PermissionRequest{
					SessionID: sessionID, CallID: ev.CallID, ToolName: ev.ToolName, Input: ev.Input,
				})
				if askErr != nil {
					// ctx cancelled or other gate failure: leave the call unsettled;
					// the turn close (FailUnresolvedTools) marks it Tool.Failed.
					return nil
				}
				if !approved {
					return pub.ToolFailed(cleanupCtx, ev.CallID, errPermissionDenied)
				}
			}
			res, err := settle(tool.WithSessionID(gctx, sessionID), call)
			if err != nil {
				r.logf("atenea: tool %q (call %s) fallo: %v", ev.ToolName, ev.CallID, err)
				return pub.ToolFailed(cleanupCtx, ev.CallID, err)
			}
			return pub.ToolSuccess(cleanupCtx, ev.CallID, res.Output, res.Diff)
		})
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
// Propaga las partes ricas que el proveedor necesita para emparejar: las tool
// calls del assistant (mapeadas a llm.ToolCallPart con Arguments como
// json.RawMessage) y el tool_call_id del resultado de la tool.
func toLLMMessages(msgs []session.Message) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		var calls []llm.ToolCallPart
		if len(m.ToolCalls) > 0 {
			calls = make([]llm.ToolCallPart, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				calls[j] = llm.ToolCallPart{ID: tc.ID, Name: tc.Name, Arguments: json.RawMessage(tc.Arguments)}
			}
		}
		out[i] = llm.Message{Role: string(m.Role), Text: m.Text, ToolCalls: calls, ToolCallID: m.ToolCallID}
	}
	return out
}
