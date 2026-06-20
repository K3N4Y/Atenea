package runner

import (
	"context"
	"errors"
	"sync"
	"testing"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// epochFlipStore es un decorador de test que embebe el MemoryStore real y
// sobre-escribe Epoch: devuelve before en su PRIMERA lectura y after en las
// siguientes, contando las llamadas con un candado. Asi, en el primer attempt el
// snapshot ve "viejo" y el recheck ve "nuevo" (mismatch -> rebuild); en el segundo
// attempt ambas lecturas ven "nuevo" (coinciden -> streamea). AppendEvent/Messages/
// LoadSession se heredan del MemoryStore embebido, asi que sigue cumpliendo
// session.Store (incluido el propio Epoch, que aca se reemplaza).
type epochFlipStore struct {
	*session.MemoryStore

	mu     sync.Mutex
	calls  int
	before session.ContextEpoch
	after  session.ContextEpoch
}

// var _ session.Store = (*epochFlipStore)(nil) asegura que el decorador sigue
// cumpliendo la interface Store que runTurn espera.
var _ session.Store = (*epochFlipStore)(nil)

// Epoch devuelve before la primera vez y after a partir de la segunda lectura.
func (s *epochFlipStore) Epoch(ctx context.Context, sessionID string) (session.ContextEpoch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls == 1 {
		return s.before, nil
	}
	return s.after, nil
}

// textTurnScript devuelve el guion de SOLO TEXTO que comparten los tests de
// senales de control: un solo step que coalesce "ok" y no lanza tool calls, asi el
// turno no continua (needsContinuation == false). Lo devuelve fresco en cada
// llamada porque NewFakeProvider consume su guion al streamear.
func textTurnScript() []llm.Event {
	return []llm.Event{
		{Kind: llm.StepStarted},
		{Kind: llm.TextStarted},
		{Kind: llm.TextDelta, Text: "ok"},
		{Kind: llm.TextEnded},
		{Kind: llm.StepEnded},
	}
}

// TestRunner_RebuildsTurnWhenModelChangesBeforeStream es el RED de M7: si el
// ContextEpoch cambia de modelo entre que el runner snapshotea el epoch al preparar
// el turno y lo re-lee antes de llamar al proveedor, el turno se reconstruye SIN
// haber streameado el request viejo; el request que llega al proveedor lleva el
// modelo NUEVO. El epochFlipStore devuelve {Model:"viejo"} en la primera lectura y
// {Model:"nuevo"} en las siguientes: el primer attempt detecta el mismatch antes de
// Stream (rebuild) y el segundo attempt streamea con el modelo nuevo.
//
// El test referencia simbolos que aun NO existen: session.ContextEpoch y el metodo
// Epoch del Store (mas el snapshot/recheck del epoch en el runner). En Go eso es un
// fallo de compilacion del paquete de test, y ese es el RED honesto de este
// milestone (igual que M5 y M6): el fallo se demuestra corriendo este test nuevo.
func TestRunner_RebuildsTurnWhenModelChangesBeforeStream(t *testing.T) {
	ctx := context.Background()
	store := &epochFlipStore{
		MemoryStore: session.NewMemoryStore(),
		before:      session.ContextEpoch{Model: "viejo"},
		after:       session.ContextEpoch{Model: "nuevo"},
	}

	// Semilla: un mensaje de usuario en la sesion "s1", asi runTurn tiene historial.
	if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{
		Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"},
	}); err != nil {
		t.Fatalf("AppendEvent (semilla usuario) error inesperado: %v", err)
	}

	// recordingProvider (de turn_test.go) captura el llm.Request y delega en un
	// FakeProvider con guion de SOLO TEXTO: el turno coalesce "ok" y no continua.
	prov := &recordingProvider{FakeProvider: llm.NewFakeProvider(textTurnScript()...)}

	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), prov, reg, tool.Permissions{"echo": true}, idCounter())

	cont, err := r.runTurn(ctx, "s1")
	if err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}
	// Un turno de solo texto no continua: no hubo tool call local.
	if cont {
		t.Errorf("runTurn cont = true, quiero false (turno de solo texto no continua)")
	}

	// El request streameado lleva el modelo nuevo: el viejo se descarto antes de Stream.
	if got := prov.captured().Model; got != "nuevo" {
		t.Errorf("Request.Model streameado = %q, quiero %q (el viejo se descarto antes de Stream)", got, "nuevo")
	}

	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	// El rebuild descarto el primer attempt antes de streamear, no encadeno dos
	// turnos: la proyeccion tiene EXACTAMENTE un mensaje de asistente con Text "ok".
	var asst []session.Message
	for _, m := range msgs {
		if m.Role == session.RoleAssistant {
			asst = append(asst, m)
		}
	}
	if len(asst) != 1 {
		t.Fatalf("mensajes de asistente = %d, quiero 1 (el rebuild no encadeno dos turnos); mensajes = %+v", len(asst), msgs)
	}
	if asst[0].Text != "ok" {
		t.Errorf("asistente.Text = %q, quiero %q", asst[0].Text, "ok")
	}
}

// TestRunner_RebuildsTurnWhenEpochRevisionChanges fija que un campo del epoch
// DISTINTO de agente/modelo (la Revision) tambien fuerza el rebuild: el runner
// compara el epoch entero (after != before), no solo el modelo. El epochFlipStore
// devuelve {Revision:1} en la primera lectura y {Revision:2} en las siguientes
// (mismo modelo vacio); el primer attempt detecta el mismatch de revision antes de
// Stream (rebuild) y el segundo attempt streamea con ambas lecturas iguales. El
// turno corre una SOLA vez: la proyeccion tiene exactamente un mensaje de
// asistente (el rebuild descarto el primer attempt sin streamear, no encadeno dos
// turnos).
func TestRunner_RebuildsTurnWhenEpochRevisionChanges(t *testing.T) {
	ctx := context.Background()
	store := &epochFlipStore{
		MemoryStore: session.NewMemoryStore(),
		before:      session.ContextEpoch{Revision: 1},
		after:       session.ContextEpoch{Revision: 2},
	}
	seedUser(t, store, "s1")

	// recordingProvider con guion de SOLO TEXTO: el turno coalesce "ok" y no continua.
	prov := &recordingProvider{FakeProvider: llm.NewFakeProvider(textTurnScript()...)}

	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), prov, reg, tool.Permissions{"echo": true}, idCounter())

	cont, err := r.runTurn(ctx, "s1")
	if err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}
	if cont {
		t.Errorf("runTurn cont = true, quiero false (turno de solo texto no continua)")
	}

	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	// El cambio de revision forzo el rebuild, no un segundo turno encadenado: la
	// proyeccion tiene EXACTAMENTE un mensaje de asistente (el provider corrio una vez).
	var asst []session.Message
	for _, m := range msgs {
		if m.Role == session.RoleAssistant {
			asst = append(asst, m)
		}
	}
	if len(asst) != 1 {
		t.Fatalf("mensajes de asistente = %d, quiero 1 (mismatch de revision rebuildea, no encadena); mensajes = %+v", len(asst), msgs)
	}
	if asst[0].Text != "ok" {
		t.Errorf("asistente.Text = %q, quiero %q", asst[0].Text, "ok")
	}
}

// TestRunnerAttempt_ReturnsErrRebuildTurnWhenEpochChanges es el test white-box que
// fija la SENAL interna directamente: con un epochFlipStore (before != after), una
// sola llamada a runTurnAttempt detecta el mismatch del epoch en el recheck previo
// a Stream y devuelve un error que errors.Is(err, errRebuildTurn) reconoce, con
// cont == false. Aisla el sentinel del retry loop: el comportamiento observable lo
// cubren los otros tests, este ancla el contrato del attempt.
func TestRunnerAttempt_ReturnsErrRebuildTurnWhenEpochChanges(t *testing.T) {
	ctx := context.Background()
	store := &epochFlipStore{
		MemoryStore: session.NewMemoryStore(),
		before:      session.ContextEpoch{Model: "viejo"},
		after:       session.ContextEpoch{Model: "nuevo"},
	}
	// Sembrar usuario asi Messages no devuelve ErrSessionNotFound al armar el request.
	seedUser(t, store, "s1")

	prov := &recordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), prov, reg, tool.Permissions{"echo": true}, idCounter())

	cont, err := r.runTurnAttempt(ctx, "s1")
	if !errors.Is(err, errRebuildTurn) {
		t.Fatalf("runTurnAttempt error = %v, quiero errRebuildTurn (errors.Is)", err)
	}
	if cont {
		t.Errorf("runTurnAttempt cont = true, quiero false (el rebuild corta antes de Stream)")
	}
}

// fakeCompactor es un Compactor de test que simula overflow exactamente una vez:
// NeedsCompaction devuelve true mientras no se haya compactado y false despues, asi
// que el primer attempt compacta y el segundo (post-compaction) entra. Compact marca
// compacted, cuenta sus llamadas y apendea un marcador {Role: system, Text:
// "[compactado]"} al store durable, observable en la proyeccion del turno que
// streamea. El candado deja el fake seguro bajo -race (los decoradores de turn
// asientan tools concurrentemente).
type fakeCompactor struct {
	store session.Store

	mu           sync.Mutex
	compacted    bool
	compactCalls int
}

// NeedsCompaction pide compactar mientras no se haya compactado todavia; tras el
// primer Compact deja de pedirlo, asi el retry post-compaction converge.
func (c *fakeCompactor) NeedsCompaction(req llm.Request) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.compacted
}

// Compact marca el progreso (compacted = true), cuenta la llamada y deja un marcador
// durable en el store para verificar que la compaction corrio antes del turno.
func (c *fakeCompactor) Compact(ctx context.Context, sessionID string) error {
	c.mu.Lock()
	c.compacted = true
	c.compactCalls++
	c.mu.Unlock()
	_, err := c.store.AppendEvent(ctx, sessionID, session.SessionEvent{
		Message: &session.Message{ID: "compact", Role: session.RoleSystem, Text: "[compactado]"},
	})
	return err
}

// TestRunner_CompactsAndRetriesOnceWhenRequestOverflows fija la ruta de overflow:
// con un MemoryStore (epoch estable, sin rebuild) y un fakeCompactor inyectado
// white-box, el primer attempt detecta overflow, compacta y devuelve
// errContinueAfterCompaction; el retry arma un request que ya entra y lo streamea. La
// compaction corre UNA SOLA vez (compactCalls == 1), el provider corre UNA vez (un
// mensaje de asistente) y el marcador {Role: system, Text: "[compactado]"} que dejo
// Compact aparece en la proyeccion (la compaction corrio antes del turno que streameo).
func TestRunner_CompactsAndRetriesOnceWhenRequestOverflows(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	seedUser(t, store, "s1")

	prov := &recordingProvider{FakeProvider: llm.NewFakeProvider(textTurnScript()...)}

	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), prov, reg, tool.Permissions{"echo": true}, idCounter())
	c := &fakeCompactor{store: store}
	r.compactor = c // inyeccion white-box: NewRunner no cablea el compactor

	cont, err := r.runTurn(ctx, "s1")
	if err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}
	if cont {
		t.Errorf("runTurn cont = true, quiero false (turno de solo texto no continua)")
	}

	// La compaction corrio exactamente una vez: el fake deja de pedir overflow tras
	// el primer Compact, asi el segundo attempt no recompacta.
	if got := c.compactCalls; got != 1 {
		t.Errorf("compactCalls = %d, quiero 1 (compacto una sola vez)", got)
	}

	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	// El provider corrio una vez: exactamente un mensaje de asistente "ok".
	var asst []session.Message
	for _, m := range msgs {
		if m.Role == session.RoleAssistant {
			asst = append(asst, m)
		}
	}
	if len(asst) != 1 {
		t.Fatalf("mensajes de asistente = %d, quiero 1 (la compaction no encadeno turnos); mensajes = %+v", len(asst), msgs)
	}
	if asst[0].Text != "ok" {
		t.Errorf("asistente.Text = %q, quiero %q", asst[0].Text, "ok")
	}

	// El marcador de compaction quedo durable: la compaction corrio antes del turno.
	var foundMarker bool
	for _, m := range msgs {
		if m.Role == session.RoleSystem && m.Text == "[compactado]" {
			foundMarker = true
		}
	}
	if !foundMarker {
		t.Errorf("la proyeccion no contiene Message{Role:system, Text:[compactado]} (la compaction no dejo marca); mensajes = %+v", msgs)
	}
}

// TestRunner_HappyPathDoesNotRebuildOrCompact es la regresion explicita del camino
// feliz: con un MemoryStore (epoch cero estable) y el compactor nil (NewRunner
// normal), el turno snapshotea y re-lee el MISMO epoch, no reconstruye ni compacta y
// se comporta como M5/M6. Afirma que runTurn corre sin error, que el request
// streameado lleva Model == "" (el epoch cero, identico al request de M5/M6) y que la
// proyeccion es exactamente usuario + asistente. Ancla que ninguna senal se dispara
// en el camino normal (tumbaria una implementacion que siempre rebuildea o compacta).
func TestRunner_HappyPathDoesNotRebuildOrCompact(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	seedUser(t, store, "s1")

	prov := &recordingProvider{FakeProvider: llm.NewFakeProvider(textTurnScript()...)}

	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), prov, reg, tool.Permissions{"echo": true}, idCounter())

	cont, err := r.runTurn(ctx, "s1")
	if err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}
	if cont {
		t.Errorf("runTurn cont = true, quiero false (turno de solo texto no continua)")
	}

	// El epoch cero deja Model == "": el camino feliz no toca el modelo (igual a M5/M6).
	if got := prov.captured().Model; got != "" {
		t.Errorf("Request.Model streameado = %q, quiero \"\" (epoch cero, sin rebuild)", got)
	}

	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	// Sin rebuild ni compaction: la proyeccion es exactamente usuario + asistente.
	if len(msgs) != 2 {
		t.Fatalf("mensajes proyectados = %d, quiero 2 (usuario + asistente, sin senales); mensajes = %+v", len(msgs), msgs)
	}
	if msgs[0].Role != session.RoleUser {
		t.Errorf("msgs[0].Role = %q, quiero %q", msgs[0].Role, session.RoleUser)
	}
	if msgs[1].Role != session.RoleAssistant || msgs[1].Text != "ok" {
		t.Errorf("msgs[1] = {Role:%q Text:%q}, quiero {Role:%q Text:%q}", msgs[1].Role, msgs[1].Text, session.RoleAssistant, "ok")
	}
}
