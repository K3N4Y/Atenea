package repair

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestRepair_ReturnsValidInputByteForByte verifies that an input that already
// validates against the schema is returned intact (exactly the same bytes),
// without notes and without error: Repair must not re-serialize or normalize
// what is already valid.
func TestRepair_ReturnsValidInputByteForByte(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"limit": {"type": "integer"}
		},
		"required": ["path"]
	}`)
	// Deliberately irregular spacing and key order: if Repair re-serialized
	// the input, the bytes would change and the test would fail.
	input := json.RawMessage(`{ "limit": 3,  "path": "src/main.go" }`)

	repaired, notes, err := Repair("Read", schema, input)
	if err != nil {
		t.Fatalf("Repair returned an unexpected error: %v", err)
	}
	if string(repaired) != string(input) {
		t.Errorf("a valid input must be returned byte for byte\nwant: %s\ngot: %s", input, repaired)
	}
	if len(notes) != 0 {
		t.Errorf("a valid input must not produce notes, got %d: %#v", len(notes), notes)
	}
}

// TestRepair_EscapesLiteralControlCharsInsideStrings verifies that an input
// with literal control characters (newline, tab) inside a JSON string —
// invalid JSON per the spec — is repaired by escaping them, keeps the
// original string content and produces a repair note.
func TestRepair_EscapesLiteralControlCharsInsideStrings(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"content": {"type": "string"}},
		"required": ["content"]
	}`)
	// Literal newline and tab (bytes 0x0a and 0x09) inside the JSON string.
	input := json.RawMessage("{\"content\":\"line1\nline2\tend\"}")

	repaired, notes, err := Repair("Write", schema, input)
	if err != nil {
		t.Fatalf("Repair returned an unexpected error: %v", err)
	}

	var got struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(repaired, &got); err != nil {
		t.Fatalf("the repaired input does not parse: %v\nrepaired input: %s", err, repaired)
	}
	if want := "line1\nline2\tend"; got.Content != want {
		t.Errorf("repaired content = %q, want %q", got.Content, want)
	}
	if len(notes) != 1 {
		t.Fatalf("expected exactly 1 repair note, got %d: %#v", len(notes), notes)
	}
	if !strings.Contains(notes[0], "control") {
		t.Errorf("the note must mention the control characters: %q", notes[0])
	}
}

// TestRepair_ClosesTruncatedJSONObject verifies that a truncated input that
// starts with '{' is repaired by trying closing suffixes until it parses as
// an object, with cases of different nesting depth, and leaves a note.
func TestRepair_ClosesTruncatedJSONObject(t *testing.T) {
	cases := []struct {
		name   string
		schema json.RawMessage
		input  json.RawMessage
		clave  string
		check  func(t *testing.T, repaired json.RawMessage)
	}{
		{
			name: "string truncated at root level",
			schema: json.RawMessage(`{
				"type": "object",
				"properties": {"path": {"type": "string"}},
				"required": ["path"]
			}`),
			input: json.RawMessage(`{"path":"foo`),
			check: func(t *testing.T, repaired json.RawMessage) {
				var got struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(repaired, &got); err != nil {
					t.Fatalf("the repaired input does not parse: %v\nrepaired input: %s", err, repaired)
				}
				if got.Path != "foo" {
					t.Errorf("repaired path = %q, want %q", got.Path, "foo")
				}
			},
		},
		{
			name: "object truncated inside an array",
			schema: json.RawMessage(`{
				"type": "object",
				"properties": {"todos": {"type": "array"}},
				"required": ["todos"]
			}`),
			input: json.RawMessage(`{"todos":[{"content":"a"`),
			check: func(t *testing.T, repaired json.RawMessage) {
				var got struct {
					Todos []map[string]string `json:"todos"`
				}
				if err := json.Unmarshal(repaired, &got); err != nil {
					t.Fatalf("the repaired input does not parse: %v\nrepaired input: %s", err, repaired)
				}
				if len(got.Todos) != 1 || got.Todos[0]["content"] != "a" {
					t.Errorf("repaired todos = %#v, want one element with content=a", got.Todos)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repaired, notes, err := Repair("X", tc.schema, tc.input)
			if err != nil {
				t.Fatalf("Repair returned an unexpected error: %v", err)
			}
			tc.check(t, repaired)
			if len(notes) != 1 {
				t.Fatalf("expected exactly 1 repair note, got %d: %#v", len(notes), notes)
			}
			if !strings.Contains(notes[0], "truncated") {
				t.Errorf("the note must mention that the JSON arrived truncated: %q", notes[0])
			}
		})
	}
}

// TestRepair_UnwrapsSingleObjectRootArray verifies that an input arriving as
// a root array containing a single object ([{...}]) when the schema expects
// an object is unwrapped to that object, with its repair note.
func TestRepair_UnwrapsSingleObjectRootArray(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"path": {"type": "string"}},
		"required": ["path"]
	}`)
	input := json.RawMessage(`[{"path":"src/main.go"}]`)

	repaired, notes, err := Repair("Read", schema, input)
	if err != nil {
		t.Fatalf("Repair returned an unexpected error: %v", err)
	}

	var got struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(repaired, &got); err != nil {
		t.Fatalf("the repaired input does not parse as an object: %v\nrepaired input: %s", err, repaired)
	}
	if got.Path != "src/main.go" {
		t.Errorf("repaired path = %q, want %q", got.Path, "src/main.go")
	}
	if len(notes) != 1 {
		t.Fatalf("expected exactly 1 repair note, got %d: %#v", len(notes), notes)
	}
	if !strings.Contains(notes[0], "array") {
		t.Errorf("the note must mention that the input arrived wrapped in an array: %q", notes[0])
	}
}

// TestRepair_WrapsBareRootStringIntoSingleRequiredStringProperty verifies
// that an input arriving as a bare JSON string at the root, when the schema
// has exactly one required property of type string, is wrapped into an object
// under that property, with its repair note.
func TestRepair_WrapsBareRootStringIntoSingleRequiredStringProperty(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"limit": {"type": "integer"}
		},
		"required": ["path"]
	}`)
	input := json.RawMessage(`"src/main.go"`)

	repaired, notes, err := Repair("Read", schema, input)
	if err != nil {
		t.Fatalf("Repair returned an unexpected error: %v", err)
	}

	var got struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(repaired, &got); err != nil {
		t.Fatalf("the repaired input does not parse as an object: %v\nrepaired input: %s", err, repaired)
	}
	if got.Path != "src/main.go" {
		t.Errorf("repaired path = %q, want %q", got.Path, "src/main.go")
	}
	if len(notes) != 1 {
		t.Fatalf("expected exactly 1 repair note, got %d: %#v", len(notes), notes)
	}
	if !strings.Contains(notes[0], "path") {
		t.Errorf("the note must mention the 'path' property used for wrapping: %q", notes[0])
	}
}

// TestRepair_RenamesAliasKeyToCanonical verifies that an input key that is
// not in the schema but is a known alias of a canonical key that is declared
// (and missing from the input) is renamed to the canonical key, deleting the
// alias, with a note asking to use the canonical key next time.
func TestRepair_RenamesAliasKeyToCanonical(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"content": {"type": "string"}
		},
		"required": ["path", "content"]
	}`)
	input := json.RawMessage(`{"file_path":"a.go","content":"x"}`)

	repaired, notes, err := Repair("Write", schema, input)
	if err != nil {
		t.Fatalf("Repair returned an unexpected error: %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal(repaired, &got); err != nil {
		t.Fatalf("the repaired input does not parse: %v\nrepaired input: %s", err, repaired)
	}
	if got["path"] != "a.go" {
		t.Errorf("repaired path = %q, want %q", got["path"], "a.go")
	}
	if _, present := got["file_path"]; present {
		t.Errorf("the alias key 'file_path' must be deleted after the rename, repaired input: %s", repaired)
	}
	if len(notes) != 1 {
		t.Fatalf("expected exactly 1 repair note, got %d: %#v", len(notes), notes)
	}
	if !strings.Contains(notes[0], "path") || !strings.Contains(notes[0], "Next time") {
		t.Errorf("the note must ask to use the canonical key 'path' next time: %q", notes[0])
	}
}

// TestRepair_KeepsAliasKeyWhenCanonicalAlreadyPresent verifies that when the
// canonical key is already present in the input, the alias is NOT renamed (it
// must not overwrite the canonical value): the valid input passes through
// intact and without notes.
func TestRepair_KeepsAliasKeyWhenCanonicalAlreadyPresent(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"path": {"type": "string"}},
		"required": ["path"]
	}`)
	input := json.RawMessage(`{"path":"a.go","file_path":"b.go"}`)

	repaired, notes, err := Repair("Read", schema, input)
	if err != nil {
		t.Fatalf("Repair returned an unexpected error: %v", err)
	}
	if string(repaired) != string(input) {
		t.Errorf("with the canonical key present the input must pass through intact\nwant: %s\ngot: %s", input, repaired)
	}
	if len(notes) != 0 {
		t.Errorf("there must be no repair notes, got %d: %#v", len(notes), notes)
	}
}

// TestRepair_DropsNullFields verifies that a field present with a null value
// is removed from the input, with a note explaining that optional fields are
// omitted instead of being sent as null.
func TestRepair_DropsNullFields(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"limit": {"type": "integer"}
		},
		"required": ["path"]
	}`)
	input := json.RawMessage(`{"path":"a.go","limit":null}`)

	repaired, notes, err := Repair("Read", schema, input)
	if err != nil {
		t.Fatalf("Repair returned an unexpected error: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(repaired, &got); err != nil {
		t.Fatalf("the repaired input does not parse: %v\nrepaired input: %s", err, repaired)
	}
	if _, present := got["limit"]; present {
		t.Errorf("the 'limit' field with a null value must be removed, repaired input: %s", repaired)
	}
	if string(got["path"]) != `"a.go"` {
		t.Errorf("the 'path' field must be kept, repaired input: %s", repaired)
	}
	if len(notes) != 1 {
		t.Fatalf("expected exactly 1 repair note, got %d: %#v", len(notes), notes)
	}
	if !strings.Contains(notes[0], "null") || !strings.Contains(notes[0], "omitted") {
		t.Errorf("the note must explain that optional fields are omitted, not sent as null: %q", notes[0])
	}
}

// TestRepair_CoercesStringifiedValuesToDeclaredType verifies that a value
// sent as a string whose content parses as the type declared in the schema
// (object, boolean, number, integer) is replaced by the real value, with its
// repair note. It generalizes the stringified-array rule.
func TestRepair_CoercesStringifiedValuesToDeclaredType(t *testing.T) {
	cases := []struct {
		name  string
		typ   string
		value string // stringified value as it arrives in the input
		want  string // raw JSON expected after the coercion
	}{
		{name: "stringified object", typ: "object", value: `"{\"a\":1}"`, want: `{"a":1}`},
		{name: "stringified boolean", typ: "boolean", value: `"true"`, want: `true`},
		{name: "stringified number", typ: "number", value: `"42.5"`, want: `42.5`},
		{name: "stringified integer", typ: "integer", value: `"42"`, want: `42`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := json.RawMessage(`{
				"type": "object",
				"properties": {"x": {"type": "` + tc.typ + `"}},
				"required": ["x"]
			}`)
			input := json.RawMessage(`{"x":` + tc.value + `}`)

			repaired, notes, err := Repair("X", schema, input)
			if err != nil {
				t.Fatalf("Repair returned an unexpected error: %v", err)
			}

			var got map[string]json.RawMessage
			if err := json.Unmarshal(repaired, &got); err != nil {
				t.Fatalf("the repaired input does not parse: %v\nrepaired input: %s", err, repaired)
			}
			if string(got["x"]) != tc.want {
				t.Errorf("repaired x = %s, want %s", got["x"], tc.want)
			}
			if len(notes) != 1 {
				t.Fatalf("expected exactly 1 repair note, got %d: %#v", len(notes), notes)
			}
			if !strings.Contains(notes[0], "string") {
				t.Errorf("the note must mention that the value arrived stringified: %q", notes[0])
			}
		})
	}
}

// TestRepair_WrapsPlainStringIntoExpectedArray verifies that a string value
// where the schema expects an array, whose content does NOT parse as a JSON
// array, is wrapped in a single-element array, with its repair note. (The
// stringified-array coercion is tried first; this is the fallback.)
func TestRepair_WrapsPlainStringIntoExpectedArray(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"paths": {"type": "array"}},
		"required": ["paths"]
	}`)
	input := json.RawMessage(`{"paths":"src/main.go"}`)

	repaired, notes, err := Repair("MultiRead", schema, input)
	if err != nil {
		t.Fatalf("Repair returned an unexpected error: %v", err)
	}

	var got struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(repaired, &got); err != nil {
		t.Fatalf("the repaired input does not have 'paths' as an array: %v\nrepaired input: %s", err, repaired)
	}
	if len(got.Paths) != 1 || got.Paths[0] != "src/main.go" {
		t.Errorf("repaired paths = %#v, want [\"src/main.go\"]", got.Paths)
	}
	if len(notes) != 1 {
		t.Fatalf("expected exactly 1 repair note, got %d: %#v", len(notes), notes)
	}
	if !strings.Contains(notes[0], "paths") {
		t.Errorf("the note must mention the 'paths' field: %q", notes[0])
	}
}

// TestRepair_ReturnsReadableErrorWhenRequiredFieldMissing verifies that an
// input missing a required field that no repair covers returns a
// model-readable *InvalidInputError, with the "Fix it and retry" header and
// one bullet per issue.
func TestRepair_ReturnsReadableErrorWhenRequiredFieldMissing(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"content": {"type": "string"}
		},
		"required": ["path", "content"]
	}`)
	input := json.RawMessage(`{"content":"x"}`)

	_, _, err := Repair("Write", schema, input)
	if err == nil {
		t.Fatal("Repair must return an error when an irreparable required field is missing")
	}
	var invalidErr *InvalidInputError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("the error must be *InvalidInputError, got %T: %v", err, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `Invalid input for tool "Write". Fix it and retry:`) {
		t.Errorf("the error must carry the model-readable header: %q", msg)
	}
	if !strings.Contains(msg, "• path:") {
		t.Errorf("the error must carry a bullet for the 'path' field: %q", msg)
	}
}

// TestRepair_ReturnsReadableErrorForUnparseableInput verifies that an input
// that does not parse as JSON in any repairable way returns the readable
// error with a "(root)" issue, never a raw decoder error.
func TestRepair_ReturnsReadableErrorForUnparseableInput(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"path": {"type": "string"}},
		"required": ["path"]
	}`)
	input := json.RawMessage(`this is not JSON ~~~`)

	_, _, err := Repair("Read", schema, input)
	if err == nil {
		t.Fatal("Repair must return an error for an unparseable input")
	}
	var invalidErr *InvalidInputError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("the error must be *InvalidInputError, got %T: %v", err, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "• (root):") {
		t.Errorf("the root issue must use the '(root)' field: %q", msg)
	}
	if strings.Contains(msg, "invalid character") || strings.Contains(msg, "unmarshal") {
		t.Errorf("the error must not leak raw Go decoder messages: %q", msg)
	}
}

// TestRepair_LeavesUncoercibleStringAndReportsIssue verifies the edge case of
// a string value that does NOT parse as the expected type and is not a
// wrappable array either: it is left untouched and, since it leaves the type
// invalid, Repair falls through to the readable error with that field's
// bullet.
func TestRepair_LeavesUncoercibleStringAndReportsIssue(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"recursive": {"type": "boolean"}
		},
		"required": ["path"]
	}`)
	input := json.RawMessage(`{"path":"a.go","recursive":"yes"}`)

	_, _, err := Repair("List", schema, input)
	if err == nil {
		t.Fatal("Repair must return an error when a value cannot be coerced to the declared type")
	}
	var invalidErr *InvalidInputError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("the error must be *InvalidInputError, got %T: %v", err, err)
	}
	if msg := err.Error(); !strings.Contains(msg, "• recursive:") {
		t.Errorf("the error must carry a bullet for the 'recursive' field: %q", msg)
	}
}

// TestRepair_AppliesMultipleRepairsWithOneNoteEach verifies that an input
// with several deviations (an alias key and a null field) receives all the
// repairs in a single pass, with one note for each.
func TestRepair_AppliesMultipleRepairsWithOneNoteEach(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"limit": {"type": "integer"}
		},
		"required": ["path"]
	}`)
	input := json.RawMessage(`{"file_path":"a.go","limit":null}`)

	repaired, notes, err := Repair("Read", schema, input)
	if err != nil {
		t.Fatalf("Repair returned an unexpected error: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(repaired, &got); err != nil {
		t.Fatalf("the repaired input does not parse: %v\nrepaired input: %s", err, repaired)
	}
	if string(got["path"]) != `"a.go"` {
		t.Errorf("repaired path = %s, want \"a.go\"", got["path"])
	}
	if _, present := got["limit"]; present {
		t.Errorf("the null 'limit' field must be removed, repaired input: %s", repaired)
	}
	if len(notes) != 2 {
		t.Fatalf("expected 2 repair notes (alias and null), got %d: %#v", len(notes), notes)
	}
	all := strings.Join(notes, "\n")
	if !strings.Contains(all, "file_path") {
		t.Errorf("there must be a note for the 'file_path' rename: %#v", notes)
	}
	if !strings.Contains(all, "limit") {
		t.Errorf("there must be a note for the dropped null 'limit': %#v", notes)
	}
}

// TestWithNotes_PrependsEachNoteAsTaggedLine verifies that WithNotes prepends
// each note as a <repair_note>...</repair_note> line before the output.
func TestWithNotes_PrependsEachNoteAsTaggedLine(t *testing.T) {
	got := WithNotes([]string{"note one", "note two"}, "tool output")
	want := "<repair_note>note one</repair_note>\n<repair_note>note two</repair_note>\ntool output"
	if got != want {
		t.Errorf("WithNotes returned:\n%q\nwant:\n%q", got, want)
	}
}

// TestWithNotes_ReturnsOutputUntouchedWithoutNotes verifies that with empty
// notes (nil or empty slice) WithNotes returns the output unchanged.
func TestWithNotes_ReturnsOutputUntouchedWithoutNotes(t *testing.T) {
	if got := WithNotes(nil, "output"); got != "output" {
		t.Errorf("WithNotes(nil, ...) = %q, want %q", got, "output")
	}
	if got := WithNotes([]string{}, "output"); got != "output" {
		t.Errorf("WithNotes([]string{}, ...) = %q, want %q", got, "output")
	}
}

// TestRepair_CoercesStringifiedArray verifies that when the model sends a
// field as a string containing a JSON-stringified array and the schema
// declares that field as an array, Repair returns the input with the array
// parsed for real and a repair note telling the model how to send it
// correctly.
func TestRepair_CoercesStringifiedArray(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"todos": {
				"type": "array",
				"items": {"type": "object"}
			}
		},
		"required": ["todos"]
	}`)
	input := json.RawMessage(`{"todos":"[{\"content\":\"a\",\"status\":\"pending\"}]"}`)

	repaired, notes, err := Repair("TodoWrite", schema, input)
	if err != nil {
		t.Fatalf("Repair returned an unexpected error: %v", err)
	}

	var got struct {
		Todos []map[string]string `json:"todos"`
	}
	if err := json.Unmarshal(repaired, &got); err != nil {
		t.Fatalf("the repaired input does not have 'todos' as a real array: %v\nrepaired input: %s", err, repaired)
	}
	if len(got.Todos) != 1 {
		t.Fatalf("repaired todos has %d elements, want 1: %#v", len(got.Todos), got.Todos)
	}
	want := map[string]string{"content": "a", "status": "pending"}
	for k, v := range want {
		if got.Todos[0][k] != v {
			t.Errorf("todos[0][%q] = %q, want %q", k, got.Todos[0][k], v)
		}
	}

	if len(notes) != 1 {
		t.Fatalf("expected exactly 1 repair note, got %d: %#v", len(notes), notes)
	}
	if !strings.Contains(notes[0], "todos") {
		t.Errorf("the note must mention the 'todos' field: %q", notes[0])
	}
	if !strings.Contains(notes[0], "TodoWrite") {
		t.Errorf("the note must mention the 'TodoWrite' tool: %q", notes[0])
	}
}
