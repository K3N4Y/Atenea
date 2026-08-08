package tooltest

import (
	"context"
	"encoding/json"
	"testing"
)

// TestEchoExecuteReturnsText verifies that the fixture parses JSON and returns
// the text field unchanged.
func TestEcho_ExecuteReturnsText(t *testing.T) {
	res, err := Echo{}.Execute(context.Background(), json.RawMessage(`{"text":"hola"}`))
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if res.Output != "hola" || res.Truncated || res.Diff != "" || len(res.Files) != 0 {
		t.Fatalf("Execute returned unexpected result: %+v", res)
	}
}

// TestEchoInvalidInputErrors verifies malformed JSON is rejected by the
// fixture itself.
func TestEcho_InvalidInputErrors(t *testing.T) {
	_, err := Echo{}.Execute(context.Background(), json.RawMessage(`{`))
	if err == nil {
		t.Fatal("Execute accepted malformed input")
	}
}
