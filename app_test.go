package main

import (
	"context"
	"encoding/json"
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

// TestApp_RequestAdvertisesBashTool asserts that the app's registry advertises
// bash when building the provider Request (ask-before-run gates its execution,
// but the tool must be advertised for the model to request it).
func TestApp_RequestAdvertisesBashTool(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "run a command"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	req := prov.captured()
	if !requestHasTool(req, "bash") {
		t.Fatalf("Request.Tools does not contain bash; tools = %+v", req.Tools)
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

// scriptedProvider returns a DIFFERENT script per turn (the M2 FakeProvider
// always replays the same one): the i-th Stream reproduces turns[i], or an
// empty script if exhausted. It lets us chain "tool in turn 1, text in turn 2"
// without the loop asking permission forever.
type scriptedProvider struct {
	mu    sync.Mutex
	calls int
	turns [][]llm.Event
}

func (p *scriptedProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	var script []llm.Event
	if p.calls < len(p.turns) {
		script = p.turns[p.calls]
	}
	p.calls++
	p.mu.Unlock()
	return llm.NewFakeProvider(script...).Stream(ctx, req)
}

// waitForPermissionRequest waits (with a timeout) for the bus to receive the
// Tool.Permission.Requested for callID: the runner emits it before blocking on
// the gate, so the test knows when to resolve.
func waitForPermissionRequest(t *testing.T, rec *recordingEmit, channel, callID string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		for _, ev := range rec.eventsOn(channel) {
			if ev.Kind == session.KindToolPermissionRequested && ev.CallID == callID {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("Tool.Permission.Requested for %s did not arrive", callID)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// TestApp_BashCallAsksPermissionAndDenyDoesNotRun is the end-to-end integration
// test for ask-before-run wiring: a bash tool call makes the runner emit
// Tool.Permission.Requested and BLOCK; when denied via the ResolveToolPermission
// binding the tool does not run and Tool.Failed is published. It covers
// SetPermissionGate + needsApproval==bash + the end-to-end binding, without
// actually running bash (it denies) or hanging (a text-only turn 2 closes
// activity). Run with -race.
func TestApp_BashCallAsksPermissionAndDenyDoesNotRun(t *testing.T) {
	rec := &recordingEmit{}
	prov := &scriptedProvider{turns: [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"echo should-not-run"}`)},
			{Kind: llm.StepEnded},
		},
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "done"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "run echo"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	waitForPermissionRequest(t, rec, "session:s1", "c1")
	app.ResolveToolPermission("s1", "c1", false) // DENY: bash must not run
	app.wait()

	var sawRequest, sawFailed, sawSuccess bool
	for _, ev := range rec.eventsOn("session:s1") {
		switch {
		case ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1":
			sawRequest = true
		case ev.Kind == session.KindToolFailed && ev.CallID == "c1":
			sawFailed = true
		case ev.Kind == session.KindToolSuccess && ev.CallID == "c1":
			sawSuccess = true
		}
	}
	if !sawRequest {
		t.Error("missing Tool.Permission.Requested of c1: the gate is not wired for bash")
	}
	if !sawFailed {
		t.Error("missing Tool.Failed of c1: the denial did not propagate via the binding")
	}
	if sawSuccess {
		t.Error("Tool.Success of c1 happened despite denying the permission")
	}
}

// TestApp_ResolveToolPermissionWiredToGate verifies the binding in isolation:
// the decision arriving via ResolveToolPermission unblocks a pending Ask on the
// app's gate (proves the gate field exists and the binding invokes it).
func TestApp_ResolveToolPermissionWiredToGate(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})

	done := make(chan bool, 1)
	go func() {
		approved, err := app.gate.Ask(context.Background(), session.PermissionRequest{SessionID: "s1", CallID: "c1"})
		if err != nil {
			t.Errorf("Ask unexpected error: %v", err)
		}
		done <- approved
	}()

	deadline := time.After(2 * time.Second)
	for {
		app.ResolveToolPermission("s1", "c1", true)
		select {
		case got := <-done:
			if !got {
				t.Errorf("the decision did not arrive as approved=true via the binding")
			}
			return
		case <-deadline:
			t.Fatal("the binding did not deliver the decision to the gate")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
