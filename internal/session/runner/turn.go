package runner

import (
	"context"

	"golang.org/x/sync/errgroup"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// runTurn ejecuta UN turno feliz: arma el Request desde el historial proyectado y
// las tools materializadas, llama Provider.Stream una sola vez, crea el Publisher
// del turno y consume el stream. Devuelve needsContinuation: true si el turno hizo
// al menos una tool call local (el loop de M6 hara otro turno), false si fue solo
// texto. M7 envuelve esto con el retry de senales de control; M5 es el intento.
func (r *Runner) runTurn(ctx context.Context, sessionID string) (bool, error) {
	msgs, err := r.store.Messages(ctx, sessionID, 0)
	if err != nil {
		return false, err
	}
	mat := r.registry.Materialize(r.perms)
	req := llm.Request{Messages: toLLMMessages(msgs), Tools: mat.Definitions}

	in, err := r.provider.Stream(ctx, req)
	if err != nil {
		return false, err
	}
	pub := NewPublisher(r.store, sessionID, r.nextID())
	return r.consume(ctx, in, pub, mat.Settle)
}

// consume drena el stream del turno. Publica cada evento como SessionEvent durable
// (orden total del log) y, por cada tool call LOCAL, lanza una goroutine que la
// asienta y publica su resultado. El turno ESPERA a que todas asienten (g.Wait)
// antes de decidir la continuacion. Una tool provider-executed solo se persiste
// (Publish), no se asienta. El error de una tool se registra como Tool.Failed y NO
// corta el turno; solo un fallo del store (de Publish/ToolSuccess/ToolFailed) lo
// hace.
func (r *Runner) consume(ctx context.Context, in <-chan llm.Event,
	pub *Publisher, settle tool.SettleFunc) (bool, error) {

	g, gctx := errgroup.WithContext(ctx)
	needsContinuation := false
	for ev := range in {
		if err := pub.Publish(ctx, ev); err != nil {
			return false, err
		}
		if ev.Kind == llm.ToolCall && !ev.ProviderExecuted {
			ev := ev // captura para la goroutine
			needsContinuation = true
			g.Go(func() error {
				res, err := settle(gctx, tool.Call{ID: ev.CallID, Name: ev.ToolName, Input: ev.Input})
				if err != nil {
					return pub.ToolFailed(ctx, ev.CallID, err)
				}
				return pub.ToolSuccess(ctx, ev.CallID, res.Output)
			})
		}
	}
	if err := g.Wait(); err != nil {
		return false, err
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
