package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"atenea/internal/llm"
	"atenea/internal/tool/repair"
)

// spyTool es una tool de prueba que cuenta sus ejecuciones por puntero (Tool se
// pasa por valor, asi que el contador debe ser compartido) y devuelve un output
// y un error configurables. Sirve para verificar "sin efectos laterales": si el
// registry rechaza la Call antes de actuar, el contador queda en 0.
type spyTool struct {
	name    string
	calls   *int
	out     string
	diff    string
	execErr error
}

func (s spyTool) Name() string            { return s.name }
func (s spyTool) Description() string     { return s.name + " spy" }
func (s spyTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s spyTool) Execute(ctx context.Context, in json.RawMessage) (Result, error) {
	*s.calls++
	return Result{Output: s.out, Diff: s.diff}, s.execErr
}

// recorderTool is a test tool with a configurable schema that records through
// a pointer the input Execute received (Tool is passed by value, so the
// recording must be shared) and returns a fixed output. It serves to verify
// that the registry repairs the input BEFORE executing: the recording is what
// the tool actually saw.
type recorderTool struct {
	name   string
	schema string
	got    *json.RawMessage
	out    string
}

func (r recorderTool) Name() string            { return r.name }
func (r recorderTool) Description() string     { return r.name + " recorder" }
func (r recorderTool) Schema() json.RawMessage { return json.RawMessage(r.schema) }
func (r recorderTool) Execute(ctx context.Context, in json.RawMessage) (Result, error) {
	*r.got = append(json.RawMessage(nil), in...)
	return Result{Output: r.out}, nil
}

// mirrorTool is a stateless test tool: it returns as Output the input Execute
// received. Since it shares nothing between executions it is safe to settle
// from several goroutines at once (concurrency test with -race).
type mirrorTool struct {
	name   string
	schema string
}

func (m mirrorTool) Name() string            { return m.name }
func (m mirrorTool) Description() string     { return m.name + " mirror" }
func (m mirrorTool) Schema() json.RawMessage { return json.RawMessage(m.schema) }
func (m mirrorTool) Execute(ctx context.Context, in json.RawMessage) (Result, error) {
	return Result{Output: string(in)}, nil
}

// listerSchema is the schema shared by the repair-layer tests: a single
// required items field of type array, enough to trigger a repair
// (JSON-stringified array) and to fail when items is missing.
const listerSchema = `{"type":"object","properties":{"items":{"type":"array"}},"required":["items"]}`

// materializeLister builds a registry with a "lister" recorderTool (schema
// listerSchema, fixed output out) and an OutputStore with the given limit
// (0 = no capping), materializes it with permission and returns the
// Materialized along with the pointer where the tool records the input
// Execute received.
func materializeLister(limit int, out string) (Materialized, *json.RawMessage) {
	var got json.RawMessage
	reg := NewRegistry(NewOutputStore(limit), recorderTool{
		name:   "lister",
		schema: listerSchema,
		got:    &got,
		out:    out,
	})
	return reg.Materialize(Permissions{"lister": true}), &got
}

// cutRepairNote splits off the first line of the output and asserts that it
// is a complete <repair_note>...</repair_note> note; it returns the rest of
// the output (what follows the note).
func cutRepairNote(t *testing.T, output string) string {
	t.Helper()
	note, rest, found := strings.Cut(output, "\n")
	if !found || !strings.HasPrefix(note, "<repair_note>") || !strings.HasSuffix(note, "</repair_note>") {
		t.Fatalf("Output: expected a complete <repair_note>...</repair_note> line at the start, got %q", output)
	}
	return rest
}

// TestRegistry_SettleRepairsToolInputBeforeExecute asserts that Settle passes
// the raw input through the repair layer (repair.Repair) BEFORE executing the
// tool: an array field that arrives JSON-stringified is repaired into the real
// array before Execute, and the Output the model sees carries the
// <repair_note> line prepended to the tool's fixed output.
func TestRegistry_SettleRepairsToolInputBeforeExecute(t *testing.T) {
	mat, got := materializeLister(0, "done")

	// The items field arrives JSON-stringified: repairable, not valid as is.
	call := Call{ID: "c1", Name: "lister", Input: json.RawMessage(`{"items":"[\"a\"]"}`)}
	res, err := mat.Settle(context.Background(), call)
	if err != nil {
		t.Fatalf("Settle: unexpected error: %v", err)
	}

	// (1) Execute received the repaired input: items is now a real array.
	var fields struct {
		Items []string `json:"items"`
	}
	if err := json.Unmarshal(*got, &fields); err != nil {
		t.Fatalf("Execute: the recorded input does not parse with items as an array: %v (input: %s)", err, *got)
	}
	if len(fields.Items) != 1 || fields.Items[0] != "a" {
		t.Fatalf("Execute: expected repaired items [\"a\"], got %v", fields.Items)
	}

	// (2) The Output starts with a <repair_note> line, then the fixed output.
	if rest := cutRepairNote(t, res.Output); rest != "done" {
		t.Fatalf("Output: expected the fixed output %q after the note, got %q", "done", rest)
	}
}

// TestRegistry_SettleIrreparableInputHasNoSideEffects asserts that an input
// that cannot be repaired against the schema (a required field is missing with
// no possible alias) returns *repair.InvalidInputError with the field's
// bullet, and that the tool is NOT executed: the recorder's recording stays
// nil.
func TestRegistry_SettleIrreparableInputHasNoSideEffects(t *testing.T) {
	mat, got := materializeLister(0, "done")

	// "something_else" is not an alias of "items": the required field is
	// missing and there is no repair for it.
	call := Call{ID: "c1", Name: "lister", Input: json.RawMessage(`{"something_else":1}`)}
	_, err := mat.Settle(context.Background(), call)
	if err == nil {
		t.Fatalf("Settle: expected an error for an irreparable input, got nil")
	}
	var invalid *repair.InvalidInputError
	if !errors.As(err, &invalid) {
		t.Fatalf("Settle: expected *repair.InvalidInputError, got %v", err)
	}
	if !strings.Contains(err.Error(), "• items") {
		t.Fatalf("Error: expected the bullet for field %q in the message, got %q", "items", err.Error())
	}
	// The tool must not have executed: the recorder never recorded an input.
	if *got != nil {
		t.Fatalf("Execute: the tool must not execute on an irreparable input, it recorded %s", *got)
	}
}

// TestRegistry_SettleValidInputPassesThroughUntouched asserts that an
// already-valid input crosses the repair layer without noise: Execute receives
// the input byte for byte, the Output carries no <repair_note> and there is no
// error.
func TestRegistry_SettleValidInputPassesThroughUntouched(t *testing.T) {
	mat, got := materializeLister(0, "done")

	// Input valid as is, with its own spacing: if the registry re-serialized
	// it, the bytes would change.
	call := Call{ID: "c1", Name: "lister", Input: json.RawMessage(`{ "items": ["a"] }`)}
	res, err := mat.Settle(context.Background(), call)
	if err != nil {
		t.Fatalf("Settle: unexpected error: %v", err)
	}
	if !bytes.Equal(*got, call.Input) {
		t.Fatalf("Execute: expected the input byte for byte %s, got %s", call.Input, *got)
	}
	if strings.Contains(res.Output, "<repair_note>") {
		t.Fatalf("Output: expected no <repair_note> for a valid input, got %q", res.Output)
	}
	if res.Output != "done" {
		t.Fatalf("Output: expected the fixed output %q, got %q", "done", res.Output)
	}
}

// TestRegistry_SettlePreservesDiffThroughCap afirma que el Diff (solo-UI) sobrevive
// al acotado del OutputStore: aunque el Output se trunque, el Diff pasa entero (no
// se acota).
func TestRegistry_SettlePreservesDiffThroughCap(t *testing.T) {
	calls := 0
	reg := NewRegistry(NewOutputStore(5), spyTool{name: "edit", calls: &calls, out: "0123456789", diff: "DIFF"})
	mat := reg.Materialize(Permissions{"edit": true})

	res, err := mat.Settle(context.Background(), Call{ID: "c1", Name: "edit"})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if !res.Truncated {
		t.Fatalf("se esperaba Output truncado")
	}
	if res.Output != "01234" {
		t.Fatalf("Output: se esperaba %q, se obtuvo %q", "01234", res.Output)
	}
	if res.Diff != "DIFF" {
		t.Fatalf("Diff: se esperaba %q (no acotado), se obtuvo %q", "DIFF", res.Diff)
	}
}

// TestRegistry_SettleRepairNotesSurviveCapping asserts that the repair note is
// prepended BEFORE the OutputStore capping: with an output larger than the
// limit, the capped Output (Truncated) still starts with the complete
// <repair_note> line. If the registry capped first, the note could be lost.
func TestRegistry_SettleRepairNotesSurviveCapping(t *testing.T) {
	// The limit leaves room for the note line (~220 bytes) but cuts the tool
	// output (2000 bytes): the capping is real and the whole note fits.
	const limit = 512
	mat, _ := materializeLister(limit, strings.Repeat("x", 2000))

	// The items field arrives JSON-stringified: requires a repair (leaves a note).
	call := Call{ID: "c1", Name: "lister", Input: json.RawMessage(`{"items":"[\"a\"]"}`)}
	res, err := mat.Settle(context.Background(), call)
	if err != nil {
		t.Fatalf("Settle: unexpected error: %v", err)
	}
	if !res.Truncated {
		t.Fatalf("Truncated: expected true for an output larger than the limit")
	}
	if len(res.Output) != limit {
		t.Fatalf("Output: expected length %d, got %d", limit, len(res.Output))
	}
	// The first line of the capped Output is the complete note; the tool
	// output follows.
	if rest := cutRepairNote(t, res.Output); !strings.HasPrefix(rest, "x") {
		t.Fatalf("Output: expected the tool output after the note, got %q", rest)
	}
}

// TestRegistry_SettleNoDiffStaysEmpty: una tool sin diff (p.ej. bash) deja Diff vacio.
func TestRegistry_SettleNoDiffStaysEmpty(t *testing.T) {
	calls := 0
	reg := NewRegistry(NewOutputStore(0), spyTool{name: "bash", calls: &calls, out: "ok"})
	mat := reg.Materialize(Permissions{"bash": true})

	res, err := mat.Settle(context.Background(), Call{ID: "c1", Name: "bash"})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if res.Diff != "" {
		t.Fatalf("Diff: se esperaba vacio, se obtuvo %q", res.Diff)
	}
}

// TestRegistry_SettleNilInputSkipsRepairAndValidation explicitly pins that a
// Call with a nil Input (tool with no arguments) does NOT go through the
// repair layer: it executes normally, without notes and without error. If the
// settle passed the nil through repair.Repair, the parse would fail (nil is
// not JSON) and this test would see a *repair.InvalidInputError instead of the
// execution.
func TestRegistry_SettleNilInputSkipsRepairAndValidation(t *testing.T) {
	var calls int
	reg := NewRegistry(NewOutputStore(0), spyTool{name: "noargs", calls: &calls, out: "ok"})
	mat := reg.Materialize(Permissions{"noargs": true})

	res, err := mat.Settle(context.Background(), Call{ID: "c1", Name: "noargs", Input: nil})
	if err != nil {
		t.Fatalf("Settle: unexpected error with a nil Input: %v", err)
	}
	if calls != 1 {
		t.Fatalf("Execute: the tool must execute exactly once, counter = %d", calls)
	}
	if strings.Contains(res.Output, "<repair_note>") {
		t.Fatalf("Output: expected no <repair_note> with a nil Input, got %q", res.Output)
	}
	if res.Output != "ok" {
		t.Fatalf("Output: expected %q, got %q", "ok", res.Output)
	}
}

// TestRegistry_SettleExecutesAllowedTool es el happy path del registry: arma el
// catalogo con el builtin echo, materializa con permisos que lo anuncian y
// asienta una Call conocida. Afirma que Definitions lista solo echo (con su
// llm.ToolDef) y que Settle ejecuta la tool y devuelve su Result sin error.
func TestRegistry_SettleExecutesAllowedTool(t *testing.T) {
	reg := NewRegistry(NewOutputStore(0), Echo{})
	mat := reg.Materialize(Permissions{"echo": true})

	if len(mat.Definitions) != 1 {
		t.Fatalf("Definitions: se esperaba 1 def, se obtuvieron %d", len(mat.Definitions))
	}
	if got := mat.Definitions[0].Name; got != "echo" {
		t.Fatalf("Definitions[0].Name: se esperaba %q, se obtuvo %q", "echo", got)
	}
	var _ llm.ToolDef = mat.Definitions[0]

	call := Call{ID: "c1", Name: "echo", Input: json.RawMessage([]byte(`{"text":"hola"}`))}
	res, err := mat.Settle(context.Background(), call)
	if err != nil {
		t.Fatalf("Settle: error inesperado: %v", err)
	}
	if want := (Result{Output: "hola"}); res != want {
		t.Fatalf("Settle: se esperaba %+v, se obtuvo %+v", want, res)
	}
}

func TestRegistry_PermissionsReflectCatalogAndAreIndependent(t *testing.T) {
	var calls int
	reg := NewRegistry(NewOutputStore(0), Echo{}, spyTool{name: "secret", calls: &calls})

	permissions := reg.Permissions()
	if !permissions["echo"] || !permissions["secret"] || len(permissions) != 2 {
		t.Fatalf("Permissions() = %v, want exactly the registered tools", permissions)
	}

	delete(permissions, "secret")
	if fresh := reg.Permissions(); !fresh["secret"] {
		t.Fatalf("mutating returned permissions changed Registry: %v", fresh)
	}
}

// TestRegistry_DeniedToolAbsentFromDefinitions afirma que una tool registrada
// pero no permitida no se anuncia: Definitions lista solo la permitida. El set
// anunciado es la compuerta; lo denegado no aparece para el modelo.
func TestRegistry_DeniedToolAbsentFromDefinitions(t *testing.T) {
	var calls int
	reg := NewRegistry(NewOutputStore(0), Echo{}, spyTool{name: "secret", calls: &calls})
	mat := reg.Materialize(Permissions{"echo": true})

	if len(mat.Definitions) != 1 {
		t.Fatalf("Definitions: se esperaba 1 def, se obtuvieron %d", len(mat.Definitions))
	}
	if got := mat.Definitions[0].Name; got != "echo" {
		t.Fatalf("Definitions[0].Name: se esperaba %q, se obtuvo %q", "echo", got)
	}
	for _, d := range mat.Definitions {
		if d.Name == "secret" {
			t.Fatalf("Definitions: la tool denegada %q no debia aparecer", "secret")
		}
	}
}

// TestRegistry_SettleUnknownToolHasNoSideEffects afirma que asentar una tool
// fuera del set materializado (denegada por permisos o no registrada) devuelve
// *UnknownToolError sin ejecutar nada: el contador del spy queda en 0.
func TestRegistry_SettleUnknownToolHasNoSideEffects(t *testing.T) {
	var calls int
	reg := NewRegistry(NewOutputStore(0), Echo{}, spyTool{name: "secret", calls: &calls})
	// La tool secret esta registrada pero denegada por permisos.
	mat := reg.Materialize(Permissions{"echo": true})

	// Caso 1: tool registrada pero denegada.
	_, err := mat.Settle(context.Background(), Call{ID: "c1", Name: "secret"})
	var unknown *UnknownToolError
	if !errors.As(err, &unknown) {
		t.Fatalf("Settle(secret): se esperaba *UnknownToolError, se obtuvo %v", err)
	}
	if calls != 0 {
		t.Fatalf("Settle(secret): la tool denegada no debia ejecutarse, contador = %d", calls)
	}

	// Caso 2: nombre no registrado en absoluto; no debe entrar en panico.
	_, err = mat.Settle(context.Background(), Call{ID: "c2", Name: "ghost"})
	var unknown2 *UnknownToolError
	if !errors.As(err, &unknown2) {
		t.Fatalf("Settle(ghost): se esperaba *UnknownToolError, se obtuvo %v", err)
	}
	if calls != 0 {
		t.Fatalf("Settle(ghost): no se debia ejecutar nada, contador = %d", calls)
	}
}

// TestRegistry_LargeOutputCappedViaOutputStore afirma que un output mayor al
// limite del OutputStore se devuelve acotado (Truncated) y que el completo queda
// recuperable por callID via Full.
func TestRegistry_LargeOutputCappedViaOutputStore(t *testing.T) {
	const limit = 10
	full := strings.Repeat("x", 100)
	var calls int
	store := NewOutputStore(limit)
	reg := NewRegistry(store, spyTool{name: "big", calls: &calls, out: full})
	mat := reg.Materialize(Permissions{"big": true})

	res, err := mat.Settle(context.Background(), Call{ID: "c1", Name: "big"})
	if err != nil {
		t.Fatalf("Settle: error inesperado: %v", err)
	}
	if len(res.Output) != limit {
		t.Fatalf("Output: se esperaba largo %d, se obtuvo %d", limit, len(res.Output))
	}
	if !res.Truncated {
		t.Fatalf("Truncated: se esperaba true para output mayor al limite")
	}
	got, ok := store.Full("c1")
	if !ok {
		t.Fatalf("Full(c1): no se encontro el output completo")
	}
	if len(got) != len(full) {
		t.Fatalf("Full(c1): se esperaba largo %d, se obtuvo %d", len(full), len(got))
	}
}

// TestRegistry_SettleConcurrentCallsWithRepairAreIndependent asserts that
// Settle is safe when invoked concurrently on the same Materialized (M5 calls
// it from an errgroup): several Calls that require repair settle in parallel
// and each one receives ITS repaired input, its note and its output, without
// mixing across goroutines. It runs with -race to catch data races.
func TestRegistry_SettleConcurrentCallsWithRepairAreIndependent(t *testing.T) {
	reg := NewRegistry(NewOutputStore(0), mirrorTool{name: "mirror", schema: listerSchema})
	mat := reg.Materialize(Permissions{"mirror": true})

	const n = 16
	results := make([]Result, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each Call carries a JSON-stringified items with its own value:
			// it requires a repair and lets us verify they do not cross.
			input := fmt.Sprintf(`{"items":"[\"x%d\"]"}`, i)
			results[i], errs[i] = mat.Settle(context.Background(), Call{
				ID:    fmt.Sprintf("c%d", i),
				Name:  "mirror",
				Input: json.RawMessage(input),
			})
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("Settle(c%d): unexpected error: %v", i, errs[i])
		}
		rest := cutRepairNote(t, results[i].Output)
		// The mirror returns the input Execute received: it must be THIS
		// Call's repaired input, with its own value.
		want := fmt.Sprintf(`{"items":["x%d"]}`, i)
		if rest != want {
			t.Fatalf("Output(c%d): expected the repaired input %q, got %q", i, want, rest)
		}
	}
}

// TestRegistry_DefinitionsSortedByName afirma que Definitions sale ordenado por
// Name aunque las tools se registren desordenadas: el request es determinista.
func TestRegistry_DefinitionsSortedByName(t *testing.T) {
	var z, a, e int
	reg := NewRegistry(NewOutputStore(0),
		spyTool{name: "zeta", calls: &z},
		spyTool{name: "alpha", calls: &a},
		spyTool{name: "echo", calls: &e},
	)
	mat := reg.Materialize(Permissions{"zeta": true, "alpha": true, "echo": true})

	want := []string{"alpha", "echo", "zeta"}
	if len(mat.Definitions) != len(want) {
		t.Fatalf("Definitions: se esperaban %d defs, se obtuvieron %d", len(want), len(mat.Definitions))
	}
	for i, name := range want {
		if got := mat.Definitions[i].Name; got != name {
			t.Fatalf("Definitions[%d].Name: se esperaba %q, se obtuvo %q", i, name, got)
		}
	}
}

// TestRegistry_SettleToolExecuteErrorPropagates afirma que un error de Execute se
// propaga tal cual por Settle y NO se confunde con UnknownToolError: M5 distingue
// "no permitida" de "fallo al ejecutar".
func TestRegistry_SettleToolExecuteErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	var calls int
	reg := NewRegistry(NewOutputStore(0), spyTool{name: "fail", calls: &calls, execErr: boom})
	mat := reg.Materialize(Permissions{"fail": true})

	_, err := mat.Settle(context.Background(), Call{ID: "c1", Name: "fail"})
	if !errors.Is(err, boom) {
		t.Fatalf("Settle: se esperaba el error de Execute %v, se obtuvo %v", boom, err)
	}
	var unknown *UnknownToolError
	if errors.As(err, &unknown) {
		t.Fatalf("Settle: un error de Execute no debe ser *UnknownToolError, se obtuvo %v", err)
	}
	if calls != 1 {
		t.Fatalf("Settle: la tool permitida debia ejecutarse una vez, contador = %d", calls)
	}
}
