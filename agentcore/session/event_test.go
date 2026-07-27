package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSessionEventAttrsRoundTripAcrossJSONBoundaries(t *testing.T) {
	want := map[string]string{
		"ext.example.trace_id": "trace-123",
		"ext.example.unicode":  "café ☕",
	}
	raw, err := json.Marshal(SessionEvent{
		Kind:  EventKind("ext.example.Observed"),
		Attrs: want,
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode JSON envelope: %v", err)
	}
	if _, ok := envelope["Attrs"]; !ok {
		t.Fatalf("JSON event has no Attrs field: %s", raw)
	}

	var got SessionEvent
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got.Attrs, want) {
		t.Fatalf("Attrs = %#v, want %#v", got.Attrs, want)
	}
}
