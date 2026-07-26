package llm

import "context"

// FakeProvider es un Provider determinista para tests sin red. Reproduce un
// guion fijo de eventos en cada llamada a Stream y cierra el channel al
// terminar. Ignora Request (como MemoryStore ignora ctx en M1): el guion es la
// fuente de verdad del turno. Vive fuera de un _test.go a proposito, para que
// los tests del publisher (M3) y del runner (M5+) puedan importarlo.
type FakeProvider struct {
	Script []Event
}

// NewFakeProvider crea un fake que reproducira script en cada Stream.
func NewFakeProvider(script ...Event) *FakeProvider {
	return &FakeProvider{Script: script}
}

// var _ Provider = (*FakeProvider)(nil) asegura en compilacion que FakeProvider
// cumple la interface.
var _ Provider = (*FakeProvider)(nil)

// Stream replays the script on a new channel and closes it when the script ends
// (defer close). If ctx is already cancelled at the start of an iteration it stops
// sending and closes anyway; if the producer blocks on a send, the ctx.Done case
// unblocks it. No goroutine is ever left hanging.
//
// The one thing it does read off the Request is the content: the script renders as
// text, so a message carrying anything else is refused. A fake that accepted
// content it cannot render would hide that failure from every test written against
// it, which is the opposite of what a fake is for.
func (p *FakeProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	for _, message := range req.Messages {
		if _, err := message.TextOnly(); err != nil {
			return nil, err
		}
	}
	out := make(chan Event)
	go func() {
		defer close(out)
		for _, ev := range p.Script {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out, nil
}
