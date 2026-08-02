package session

import "testing"

func TestSubagentToolCallsStrictRoundTripAndClone(t *testing.T) {
	attrs := map[string]string{"keep": "yes"}
	ev := WithSubagentToolCalls(SessionEvent{Attrs: attrs}, 12)
	if total, ok := SubagentToolCalls(ev); !ok || total != 12 {
		t.Fatalf("read = %d, %v", total, ok)
	}
	if _, changed := attrs[subagentToolCallsAttr]; changed {
		t.Fatal("input attrs mutated")
	}
	for _, invalid := range []string{"", "+1", "-1", " 1", "01", "1 ", "x", "999999999999999999999999999999999999"} {
		if _, ok := SubagentToolCalls(SessionEvent{Attrs: map[string]string{subagentToolCallsAttr: invalid}}); ok {
			t.Errorf("accepted %q", invalid)
		}
	}
	if total, ok := SubagentToolCalls(WithSubagentToolCalls(SessionEvent{}, 0)); !ok || total != 0 {
		t.Fatal("zero did not round trip")
	}
}
