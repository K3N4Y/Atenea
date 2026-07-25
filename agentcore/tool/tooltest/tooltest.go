// Package tooltest is the contract test kit for agentcore/tool: the executable
// half of what the Tool interface promises in prose.
//
// A host cannot read every tool it loads, and a tool author cannot read the turn
// loop to find out what their Execute is expected to survive. Contract closes
// that gap from both sides. Hand it a factory that builds your tool plus one
// input it accepts, and it exercises the promises: a stable name the model can be
// told about, a schema that can be announced, an Execute that parses its input as
// JSON rather than matching strings, tolerates garbage, returns on a cancelled
// context and is safe to call concurrently.
//
// What it deliberately does not check is what the tool DOES. That is the tool's
// own test's job, and it is the half a host does not need to trust. This kit
// answers a narrower question: is this tool safe to put in a registry.
//
// Run it under -race. The concurrency check only means something there.
package tooltest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/agentcore/tool"
)

// Subject is one tool under test together with one input its Execute accepts.
//
// The kit asks for the input because half the contract is about input handling
// and only the tool knows what a valid input looks like. It must be the input a
// user of the tool would consider the happy path: the kit executes it for real.
// Leave it nil for a tool that takes no arguments — the checks that need an input
// are then skipped and the rest still run.
type Subject struct {
	Tool  tool.Tool
	Input json.RawMessage
}

// Contract runs every check of the tool.Tool contract against the tool
// newSubject builds, one subtest per check.
//
// newSubject receives the subtest's *testing.T, so it may use t.TempDir and
// t.Cleanup. It must return a subject in a pristine state every time it is
// called: the checks execute the tool for real and must not observe each other's
// side effects. A check that needs two independent worlds calls it twice.
func Contract(t *testing.T, newSubject func(t *testing.T) Subject) {
	t.Helper()
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			c.run(t, func() Subject { return newSubject(t) })
		})
	}
}

// reporter is the part of *testing.T a check uses. Checks report through this
// interface instead of taking a *testing.T so the kit's own tests can feed it
// deliberately broken tools and assert that it complains: a contract kit that
// silently passes everything is worse than no kit at all.
//
// It has no Fatalf on purpose. A check that cannot continue returns.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// fresh builds a subject in a pristine state. A check calls it once per world it
// needs.
type fresh func() Subject

type check struct {
	name string
	run  func(r reporter, next fresh)
}

var checks = []check{
	{"NameIsStableAndAnnounceable", checkName},
	{"DescriptionIsStableAndNotEmpty", checkDescription},
	{"SchemaIsAStableObjectSchema", checkSchema},
	{"ExecuteAcceptsTheDeclaredInput", checkExecuteAcceptsInput},
	{"ExecuteAcceptsTheSameInputReserialized", checkExecuteAcceptsReserializedInput},
	{"ExecuteSurvivesMalformedInput", checkExecuteSurvivesMalformedInput},
	{"ExecuteReturnsOnCancelledContext", checkExecuteReturnsOnCancelledContext},
	{"ExecuteIsSafeForConcurrentUse", checkExecuteIsSafeForConcurrentUse},
}

// executeTimeout bounds every Execute the kit performs. It is generous on
// purpose: the kit does not measure how fast a tool is, it only refuses to hang
// waiting for one. It is a variable so this package's own tests can lower it and
// still cover the hang they have to provoke.
var executeTimeout = 30 * time.Second

// announceableName is the character set a tool name may use. It is the
// intersection the providers agree on for a name in a tool list; anything else
// is rejected at request time, so a tool that uses it can never be announced.
var announceableName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// maxNameBytes is the ceiling a tool name must stay under: the same one atenea's
// MCP namespacing hashes longer names down to. A provider may be stricter still —
// some cap at 64 — so this is the outer bound, not headroom.
const maxNameBytes = 128

func checkName(r reporter, next fresh) {
	r.Helper()
	subject := next()

	name := subject.Tool.Name()
	if name == "" {
		r.Errorf("Name() is empty: the name is how a tool is addressed everywhere — the registry indexes by it, the model calls by it and the permission policy classifies by it")
		return
	}
	if again := subject.Tool.Name(); again != name {
		r.Errorf("Name() is not stable: got %q, then %q on the next call. A host indexes the tool by the first answer and the model calls with the second", name, again)
	}
	if !announceableName.MatchString(name) {
		r.Errorf("Name() = %q: a name must match %s or the provider rejects the whole tool list, taking the turn down with it", name, announceableName)
	}
	if len(name) > maxNameBytes {
		r.Errorf("Name() is %d bytes long: keep it under %d, the ceiling a host can announce without rewriting the name", len(name), maxNameBytes)
	}
}

func checkDescription(r reporter, next fresh) {
	r.Helper()
	subject := next()

	description := subject.Tool.Description()
	if strings.TrimSpace(description) == "" {
		r.Errorf("Description() is empty: it is the whole of what the model knows about the tool, so an empty one means the tool is announced but never chosen")
		return
	}
	if again := subject.Tool.Description(); again != description {
		r.Errorf("Description() is not stable: the same tool described two different things across two calls")
	}
}

func checkSchema(r reporter, next fresh) {
	r.Helper()
	subject := next()

	schema := subject.Tool.Schema()
	if len(schema) == 0 {
		r.Errorf(`Schema() is empty: a tool that takes no arguments still announces {"type":"object"}, because the provider needs a schema for the arguments object`)
		return
	}
	var decoded map[string]any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		r.Errorf("Schema() is not a JSON object (%s): %v. It travels to the provider as raw JSON Schema, so it is never re-serialized on the way out", schema, err)
		return
	}
	if decoded["type"] != "object" {
		r.Errorf(`Schema() declares "type": %#v, want "object": the schema describes the arguments object the model fills in, not a bare value`, decoded["type"])
	}
	if again := subject.Tool.Schema(); !bytes.Equal(again, schema) {
		r.Errorf("Schema() is not stable: got %s, then %s. A host announces one and validates against the other", schema, again)
	}
}

func checkExecuteAcceptsInput(r reporter, next fresh) {
	r.Helper()
	subject := next()

	// The declared input is the tool's own happy path, so a failure here is not
	// the model's fault: it means the tool rejects what it says it accepts.
	if _, err := execute(r, context.Background(), subject.Tool, subject.Input); err != nil {
		r.Errorf("Execute rejected the input the subject declares as valid (%s): %v", declared(subject.Input), err)
	}
}

func checkExecuteAcceptsReserializedInput(r reporter, next fresh) {
	r.Helper()
	subject := next()
	if len(subject.Input) == 0 {
		return // nothing to re-serialize
	}

	reserialized, err := reserialize(subject.Input)
	if err != nil {
		r.Errorf("the input the subject declares is not valid JSON (%s): %v. A host only ever hands Execute what the model emitted, which is JSON", subject.Input, err)
		return
	}
	if _, err := execute(r, context.Background(), subject.Tool, reserialized); err != nil {
		r.Errorf("Execute rejected the same input re-serialized (%s): %v.\nParse the input with json.Unmarshal, never by matching on its bytes: the same model escapes and spaces the same JSON differently between turns", reserialized, err)
	}
}

// malformedInputs are the inputs a tool eventually receives from a model, in the
// order they show up in practice: a stream that got cut, a value the model never
// finished, and JSON that is valid but not the object the schema asked for.
var malformedInputs = []json.RawMessage{
	json.RawMessage(`{`),
	json.RawMessage(`{"unterminated": `),
	json.RawMessage(`[]`),
	json.RawMessage(`"a string"`),
	json.RawMessage(`null`),
}

func checkExecuteSurvivesMalformedInput(r reporter, next fresh) {
	r.Helper()
	// Each input gets its own world: a tool that half-applied a malformed call
	// must not make the next case look like a failure of the input.
	//
	// Either outcome is contractual — an error means the call failed, a Result
	// means the model gets a chance to correct itself — so nothing is asserted
	// about which one comes back. What is asserted is that Execute comes back at
	// all: execute() reports a panic and a hang, and both are fatal to a host
	// that settles tools in the goroutines of a turn.
	for _, malformed := range malformedInputs {
		execute(r, context.Background(), next().Tool, malformed)
	}
}

func checkExecuteReturnsOnCancelledContext(r reporter, next fresh) {
	r.Helper()
	subject := next()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A cancelled context is how a user interruption reaches a tool mid-turn.
	// Returning an error is the honest answer and returning a Result is fine for
	// a tool that had already finished; hanging is what a host cannot survive,
	// because the turn waits for every tool it settled.
	execute(r, ctx, subject.Tool, subject.Input)
}

func checkExecuteIsSafeForConcurrentUse(r reporter, next fresh) {
	r.Helper()
	subject := next()

	// The same instance, called from several goroutines with the same input: the
	// shape of a turn in which the model asked for the same tool more than once.
	// Errors are not asserted — two concurrent calls doing the same work may
	// legitimately conflict — the point is that neither panics nor corrupts
	// shared state, which is what -race is here to see.
	const concurrent = 8
	var wg sync.WaitGroup
	wg.Add(concurrent)
	for i := 0; i < concurrent; i++ {
		go func() {
			defer wg.Done()
			execute(r, context.Background(), subject.Tool, subject.Input)
		}()
	}
	wg.Wait()
}

// outcome is what one settled call produced, plus the panic it may have died of.
type outcome struct {
	result     tool.Result
	err        error
	panicValue any
	stack      []byte
}

// execute settles one call, bounded in time and shielded from a panic, and
// reports both of those as contract failures: a host settles tools in the
// goroutines of a turn, so a panic takes the whole agent down with it and a call
// that never returns blocks the turn forever.
//
// The panic travels back through the channel rather than being reported from
// inside the goroutine, so nothing reports after the test that owns the reporter
// has finished.
func execute(r reporter, ctx context.Context, subject tool.Tool, input json.RawMessage) (tool.Result, error) {
	r.Helper()

	settled := make(chan outcome, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				settled <- outcome{panicValue: p, stack: debug.Stack()}
			}
		}()
		result, err := subject.Execute(ctx, input)
		settled <- outcome{result: result, err: err}
	}()

	select {
	case o := <-settled:
		if o.panicValue != nil {
			r.Errorf("Execute panicked on input %s: %v\nA tool is settled in a goroutine of the turn, so a panic takes the whole agent down. Return an error instead.\n%s",
				declared(input), o.panicValue, o.stack)
			return tool.Result{}, fmt.Errorf("panic: %v", o.panicValue)
		}
		return o.result, o.err
	case <-time.After(executeTimeout):
		r.Errorf("Execute did not return within %s on input %s: the turn waits for every tool it settled, so a call that never comes back hangs the session",
			executeTimeout, declared(input))
		return tool.Result{}, fmt.Errorf("execute timed out after %s", executeTimeout)
	}
}

// declared renders an input for a failure message, naming the empty one instead
// of printing nothing.
func declared(input json.RawMessage) string {
	if len(input) == 0 {
		return "(empty)"
	}
	return string(input)
}

// reserialize returns the same JSON value written differently: re-indented, with
// object keys in Go's map order and escapes normalized. Byte-for-byte it is
// another input; semantically it is the one the subject declared.
func reserialize(input json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(input, &value); err != nil {
		return nil, err
	}
	return json.MarshalIndent(value, "", "  ")
}
