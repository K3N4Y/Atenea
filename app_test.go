package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"atenea/internal/llm"
	"atenea/internal/session"
)

// recordingEmit registra cada emision (canal, payload[0]) de forma segura ante
// concurrencia: el wiring de App emite desde la goroutine de Run mientras el test
// inspecciona. Es el emit fake que sustituye a runtime.EventsEmit en los tests.
type recordingEmit struct {
	mu       sync.Mutex
	channels []string
	payloads []interface{}
}

func (r *recordingEmit) emit(name string, data ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels = append(r.channels, name)
	if len(data) > 0 {
		r.payloads = append(r.payloads, data[0])
	} else {
		r.payloads = append(r.payloads, nil)
	}
}

func (r *recordingEmit) eventsOn(channel string) []session.SessionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []session.SessionEvent
	for i, ch := range r.channels {
		if ch != channel {
			continue
		}
		if ev, ok := r.payloads[i].(session.SessionEvent); ok {
			out = append(out, ev)
		}
	}
	return out
}

func (r *recordingEmit) errorsOn(channel string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for i, ch := range r.channels {
		if ch != channel {
			continue
		}
		if s, ok := r.payloads[i].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

type requestRecordingProvider struct {
	*llm.FakeProvider
	mu  sync.Mutex
	req llm.Request
}

func (p *requestRecordingProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.req = req
	p.mu.Unlock()
	return p.FakeProvider.Stream(ctx, req)
}

func (p *requestRecordingProvider) captured() llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.req
}

// TestApp_SendPromptStreamsTurnToBus: SendPrompt admite el prompt y arranca Run; el
// turno completo viaja por el bus al canal de la sesion. El prompt se promueve como
// Message{Role:user} y el texto del asistente se materializa coalescido en
// Step.Ended con su Message.
func TestApp_SendPromptStreamsTurnToBus(t *testing.T) {
	rec := &recordingEmit{}
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	app := newApp(fake, rec.emit)

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	evs := rec.eventsOn("session:s1")
	if len(evs) == 0 {
		t.Fatal("el bus no recibio ningun evento")
	}

	// Seq estrictamente crecientes.
	for i := 1; i < len(evs); i++ {
		if evs[i].Seq <= evs[i-1].Seq {
			t.Fatalf("Seq no estrictamente creciente: %d tras %d", evs[i].Seq, evs[i-1].Seq)
		}
	}

	hasUser := false
	hasStepEnded := false
	for _, ev := range evs {
		if ev.Message != nil && ev.Message.Role == session.RoleUser && ev.Message.Text == "hola" {
			hasUser = true
		}
		if ev.Kind == session.KindStepEnded && ev.Message != nil && ev.Message.Text == "ok" {
			hasStepEnded = true
		}
	}
	if !hasUser {
		t.Error("falta el evento con Message.Role==user y Text=='hola'")
	}
	if !hasStepEnded {
		t.Error("falta Step.Ended con Message.Text=='ok'")
	}
}

// TestApp_RequestAdvertisesGrepTool afirma el wiring de app.go: el registry de la
// app anuncia grep cuando arma el Request del proveedor, junto con las file tools
// existentes.
func TestApp_RequestAdvertisesGrepTool(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "busca Execute"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	req := prov.captured()
	if !requestHasTool(req, "grep") {
		t.Fatalf("Request.Tools no contiene grep; tools = %+v", req.Tools)
	}
}

// TestApp_RequestAdvertisesGlobTool afirma el wiring de app.go para glob: el
// registry de la app anuncia la tool de busqueda de archivos cuando arma el
// Request del proveedor.
func TestApp_RequestAdvertisesGlobTool(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "busca archivos go"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	req := prov.captured()
	if !requestHasTool(req, "glob") {
		t.Fatalf("Request.Tools no contiene glob; tools = %+v", req.Tools)
	}
}

func requestHasTool(req llm.Request, name string) bool {
	for _, def := range req.Tools {
		if def.Name == name {
			return true
		}
	}
	return false
}

// blockingProvider entrega un StepStarted (cuando el runner lo consume), avisa por
// started y luego bloquea hasta que se cancele el ctx, simulando un turno en vuelo
// que solo termina por interrupcion. Cierra out al salir: ningun receptor cuelga.
type blockingProvider struct {
	started chan struct{}
	once    sync.Once
}

var _ llm.Provider = (*blockingProvider)(nil)

func (p *blockingProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		select {
		case <-ctx.Done():
			return
		case out <- llm.Event{Kind: llm.StepStarted}:
		}
		p.once.Do(func() { close(p.started) })
		<-ctx.Done()
	}()
	return out, nil
}

// TestApp_StopInterruptsInflightTurn: Stop cancela la corrida en vuelo; la
// interrupcion viaja por el cableado como Step.Failed y el error duro con que Run
// corta se reenvia por el canal de error. Correr con -race.
func TestApp_StopInterruptsInflightTurn(t *testing.T) {
	rec := &recordingEmit{}
	prov := &blockingProvider{started: make(chan struct{})}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("el turno no arranco a tiempo")
	}

	app.Stop("s1")
	app.wait()

	evs := rec.eventsOn("session:s1")
	hasStepFailed := false
	for _, ev := range evs {
		if ev.Kind == session.KindStepFailed {
			hasStepFailed = true
		}
	}
	if !hasStepFailed {
		t.Error("falta Step.Failed: la interrupcion no viajo por el cableado")
	}

	if errs := rec.errorsOn("session:s1:error"); len(errs) == 0 {
		t.Error("no se reenvio el error duro por session:s1:error")
	}
}
