package event

import (
	"context"
	"testing"

	"github.com/K3N4Y/atenea/internal/session"
)

func TestChildActivityStore_ForwardsOnlyEventsEnabledForHost(t *testing.T) {
	tests := []struct {
		name            string
		includeActivity bool
		wantKinds       []session.EventKind
	}{
		{
			name:            "permissions only",
			includeActivity: false,
			wantKinds:       []session.EventKind{session.KindToolPermissionRequested, session.KindToolFailed},
		},
		{
			name:            "TUI live activity",
			includeActivity: true,
			wantKinds: []session.EventKind{
				session.KindStepStarted, session.KindToolCalled,
				session.KindToolPermissionRequested, session.KindToolFailed, session.KindStepEnded,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeEmit{}
			store := NewChildActivityStore(
				"parent", "task-call", session.NewMemoryStore(), NewBus(fake.emit), tc.includeActivity,
			)
			events := []session.SessionEvent{
				{Kind: session.KindStepStarted},
				{Kind: session.KindToolCalled, CallID: "b1", ToolName: "bash"},
				{Kind: session.KindToolPermissionRequested, CallID: "b1", ToolName: "bash"},
				{Kind: session.KindToolFailed, CallID: "b1", ToolName: "bash", Error: "denied"},
				{Kind: session.KindStepEnded},
				{Kind: session.KindTextDelta, Text: "private child text"},
			}
			for _, event := range events {
				if _, err := store.AppendEvent(context.Background(), "child", event); err != nil {
					t.Fatalf("AppendEvent(%s): %v", event.Kind, err)
				}
			}

			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.payloads) != len(tc.wantKinds) {
				t.Fatalf("emitted %d events, want %d", len(fake.payloads), len(tc.wantKinds))
			}
			for i, wantKind := range tc.wantKinds {
				event, ok := fake.payloads[i].(session.SessionEvent)
				if !ok {
					t.Fatalf("payload %d has type %T, want session.SessionEvent", i, fake.payloads[i])
				}
				if fake.channels[i] != "session:parent" || event.Kind != wantKind {
					t.Errorf("event %d = (%q, %q), want (%q, %q)", i, fake.channels[i], event.Kind, "session:parent", wantKind)
				}
				if event.SessionID != "child" || session.ParentTaskCallID(event) != "task-call" {
					t.Errorf("event %d identity = child %q parent call %q", i, event.SessionID, session.ParentTaskCallID(event))
				}
			}
		})
	}
}
