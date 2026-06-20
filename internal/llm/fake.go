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

// Stream emite el guion por un channel nuevo y lo cierra al terminar (defer
// close). Si ctx ya esta cancelado al inicio de una iteracion, corta el envio y
// cierra igual; si el productor queda bloqueado en un envio, el case ctx.Done lo
// desbloquea. En ningun caso queda una goroutine colgada.
func (p *FakeProvider) Stream(ctx context.Context, _ Request) (<-chan Event, error) {
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
