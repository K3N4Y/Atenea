package event

import (
	"context"
	"testing"

	"atenea/internal/session"
)

// TestChildPermissionStore_SurfacesPermissionEventsOnParentChannel describe el
// surfacing del permiso de un subagente: el store del runner hijo se decora con
// ChildPermissionStore (parentSessionID = el canal que atiende la UI). Sus eventos
// de permiso (Tool.Permission.Requested y la resolucion Tool.Success/Tool.Failed de
// esa call) se emiten al canal del PADRE (session:<parentSessionID>), conservando en
// el payload el SessionID del hijo para que la UI resuelva con (childID, callID). El
// resto de los eventos del hijo NO se emiten (no contaminan el log del padre).
func TestChildPermissionStore_SurfacesPermissionEventsOnParentChannel(t *testing.T) {
	fake := &fakeEmit{}
	bus := NewBus(fake.emit)
	store := NewChildPermissionStore("parent", session.NewMemoryStore(), bus)

	ctx := context.Background()
	// Un evento de turno normal del hijo NO debe emitirse al padre.
	if _, err := store.AppendEvent(ctx, "child", session.SessionEvent{Kind: session.KindStepStarted}); err != nil {
		t.Fatalf("AppendEvent StepStarted: %v", err)
	}
	// El permiso del hijo SI debe emitirse al canal del padre.
	if _, err := store.AppendEvent(ctx, "child", session.SessionEvent{
		Kind: session.KindToolPermissionRequested, CallID: "b1", ToolName: "bash",
	}); err != nil {
		t.Fatalf("AppendEvent permission: %v", err)
	}
	// Y la resolucion (Tool.Failed) de esa call tambien, para cerrar la tarjeta.
	if _, err := store.AppendEvent(ctx, "child", session.SessionEvent{
		Kind: session.KindToolFailed, CallID: "b1", ToolName: "bash", Error: "denied",
	}); err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	type emitted struct {
		channel string
		ev      session.SessionEvent
	}
	var got []emitted
	for i, ch := range fake.channels {
		ev, _ := fake.payloads[i].(session.SessionEvent)
		got = append(got, emitted{channel: ch, ev: ev})
	}

	if len(got) != 2 {
		t.Fatalf("emisiones = %d, quiero 2 (Permission.Requested + Tool.Failed; StepStarted no se emite); got = %+v", len(got), got)
	}
	for _, e := range got {
		if e.channel != "session:parent" {
			t.Errorf("canal = %q, quiero session:parent (el canal del padre que atiende la UI)", e.channel)
		}
		if e.ev.SessionID != "child" {
			t.Errorf("ev.SessionID = %q, quiero %q (la UI resuelve con el id del hijo)", e.ev.SessionID, "child")
		}
		if e.ev.CallID != "b1" {
			t.Errorf("ev.CallID = %q, quiero b1", e.ev.CallID)
		}
	}
	if got[0].ev.Kind != session.KindToolPermissionRequested {
		t.Errorf("primer evento = %q, quiero Tool.Permission.Requested", got[0].ev.Kind)
	}
	if got[1].ev.Kind != session.KindToolFailed {
		t.Errorf("segundo evento = %q, quiero Tool.Failed", got[1].ev.Kind)
	}
}
