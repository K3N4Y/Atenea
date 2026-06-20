package event

import (
	"context"
	"sync"
	"testing"

	"atenea/internal/session"
)

// fakeEmit registra cada emision (canal y payload) de forma segura ante
// concurrencia, para poder afirmar que el EmittingStore reenvia al Bus.
type fakeEmit struct {
	mu       sync.Mutex
	channels []string
	payloads []interface{}
}

func (f *fakeEmit) emit(eventName string, optionalData ...interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels = append(f.channels, eventName)
	if len(optionalData) > 0 {
		f.payloads = append(f.payloads, optionalData[0])
	} else {
		f.payloads = append(f.payloads, nil)
	}
}

// TestEmittingStore_ForwardsAppendedEventToBusWithSeq describe que el
// EmittingStore, tras un AppendEvent exitoso, reenvia el evento ya sellado por
// el Store (con SessionID y Seq) al Bus, que lo emite al canal de la sesion.
func TestEmittingStore_ForwardsAppendedEventToBusWithSeq(t *testing.T) {
	fake := &fakeEmit{}
	bus := NewBus(fake.emit)
	store := NewEmittingStore(session.NewMemoryStore(), bus)

	seq, err := store.AppendEvent(context.Background(), "s1", session.SessionEvent{Kind: session.KindStepStarted})
	if err != nil {
		t.Fatalf("AppendEvent devolvio error: %v", err)
	}
	if seq != 1 {
		t.Fatalf("seq = %d, quiero 1", seq)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	if len(fake.channels) != 1 {
		t.Fatalf("emisiones = %d, quiero exactamente 1", len(fake.channels))
	}
	if got, want := fake.channels[0], "session:s1"; got != want {
		t.Errorf("canal = %q, quiero %q", got, want)
	}

	ev, ok := fake.payloads[0].(session.SessionEvent)
	if !ok {
		t.Fatalf("payload[0] = %T, quiero session.SessionEvent", fake.payloads[0])
	}
	if ev.SessionID != "s1" {
		t.Errorf("ev.SessionID = %q, quiero %q", ev.SessionID, "s1")
	}
	if ev.Seq != seq {
		t.Errorf("ev.Seq = %d, quiero %d", ev.Seq, seq)
	}
	if ev.Kind != session.KindStepStarted {
		t.Errorf("ev.Kind = %q, quiero %q", ev.Kind, session.KindStepStarted)
	}
}
