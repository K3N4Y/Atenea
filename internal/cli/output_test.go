package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

// TestExitCodeFor_PrecedenceIsTheContract: several things can be true about one
// run, and which one the exit code reports is what a caller branches on. It is
// pinned here so the order cannot drift silently.
func TestExitCodeFor_PrecedenceIsTheContract(t *testing.T) {
	cases := []struct {
		name   string
		res    result
		want   int
		status string
	}{
		{"clean", result{}, ExitOK, statusOK},
		{"denied", result{DeniedToolCalls: 2}, ExitPermissionDenied, statusPermissionDenied},
		{"failed", result{Error: "boom"}, ExitTurnFailed, statusTurnFailed},
		{"failed outranks denied", result{Error: "boom", DeniedToolCalls: 1}, ExitTurnFailed, statusTurnFailed},
		{"cancel outranks everything", result{Canceled: true, Error: "boom", DeniedToolCalls: 3}, ExitCanceled, statusCanceled},
	}
	for _, tc := range cases {
		status, code := outcome(tc.res)
		if code != tc.want || status != tc.status {
			t.Errorf("%s: outcome = %q/%d, want %q/%d", tc.name, status, code, tc.status, tc.want)
		}
	}
}

// TestExitCodes_AreAllDistinct: each code names one thing a caller reacts to
// differently, so reusing one would make two outcomes indistinguishable.
func TestExitCodes_AreAllDistinct(t *testing.T) {
	codes := []int{ExitOK, ExitTurnFailed, ExitUsage, ExitPermissionDenied, ExitCanceled, ExitStartup}
	sorted := append([]int(nil), codes...)
	sort.Ints(sorted)
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1] {
			t.Fatalf("two exit codes share the value %d: %v", sorted[i], codes)
		}
	}
}

// TestFold_SumsTheUsageOfEveryStep: a turn with tool calls runs several steps, and
// the run's cost is all of them rather than the last one.
func TestFold_SumsTheUsageOfEveryStep(t *testing.T) {
	var f fold
	f.apply(session.SessionEvent{Kind: session.KindStepEnded,
		Usage:   &session.Usage{InputTokens: 100, OutputTokens: 10},
		Message: &session.Message{Text: "thinking"}})
	f.apply(session.SessionEvent{Kind: session.KindToolCalled, ToolName: "read"})
	f.apply(session.SessionEvent{Kind: session.KindStepEnded,
		Usage:   &session.Usage{InputTokens: 150, OutputTokens: 20, CacheReadTokens: 5},
		Message: &session.Message{Text: "the answer"}})

	doc := f.document(result{SessionID: "s1"})
	if doc.Usage.InputTokens != 250 || doc.Usage.OutputTokens != 30 || doc.Usage.CacheReadTokens != 5 {
		t.Errorf("usage = %+v, want the sum of both steps", doc.Usage)
	}
	if doc.Result != "the answer" {
		t.Errorf("result = %q, want the last step's message", doc.Result)
	}
	if doc.ToolCalls != 1 {
		t.Errorf("tool_calls = %d, want 1", doc.ToolCalls)
	}
}

// TestFold_AStreamFailureOutranksASilentRunHandle: a store failure surfaces as a
// Step.Failed in the stream and may never reach the run handle. Reporting success
// while the events a caller just read say otherwise is the one contradiction the
// document must not produce.
func TestFold_AStreamFailureOutranksASilentRunHandle(t *testing.T) {
	var f fold
	f.apply(session.SessionEvent{Kind: session.KindStepFailed, Error: "the provider gave up"})

	doc := f.document(result{SessionID: "s1"})
	if doc.Status != statusTurnFailed || doc.ExitCode != ExitTurnFailed {
		t.Errorf("status/exit_code = %q/%d, want %q/%d", doc.Status, doc.ExitCode, statusTurnFailed, ExitTurnFailed)
	}
	if !strings.Contains(doc.Error, "the provider gave up") {
		t.Errorf("error = %q, want the cause from the stream", doc.Error)
	}
}

// TestStream_DropsEventsAfterTheClosingDocument: a run that was interrupted leaves
// a runner still writing to the store. Its events must not appear after the result,
// or an interrupted stream would be interleaved rather than truncated.
func TestStream_DropsEventsAfterTheClosingDocument(t *testing.T) {
	var out bytes.Buffer
	s := &stream{}
	s.attach(newStreamSink(&out))

	s.observe(session.SessionEvent{SessionID: "s1", Seq: 1, Kind: session.KindTextDelta, Text: "before"})
	if _, err := s.close(result{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	s.observe(session.SessionEvent{SessionID: "s1", Seq: 2, Kind: session.KindTextDelta, Text: "after"})

	if strings.Contains(out.String(), "after") {
		t.Errorf("an event written after the close reached the stream:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "before") {
		t.Errorf("the stream lost an event written before the close:\n%s", out.String())
	}
}

// TestStreamSink_SerializesTheEventAsTheDesktopConsumesIt: the NDJSON keys are the
// Go field names because that is already the wire shape of this stream — the
// frontend reads exactly these over Wails. One serialization of one contract, not
// two spellings depending on which host emitted it.
func TestStreamSink_SerializesTheEventAsTheDesktopConsumesIt(t *testing.T) {
	var out bytes.Buffer
	sink := newStreamSink(&out)
	sink.event(session.SessionEvent{
		SessionID: "s1", Seq: 7, Kind: session.KindToolCalled,
		CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`),
	})

	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("the line is not JSON: %v", err)
	}
	for _, key := range []string{"SessionID", "Seq", "Kind", "CallID", "ToolName", "Input"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("the serialized event has no %q key: %s", key, out.String())
		}
	}
	if !strings.HasSuffix(out.String(), "\n") {
		t.Error("the event is not newline-terminated, so it is not a line of NDJSON")
	}
}

// TestStreamSink_DoesNotEscapeHTMLInAToolInput: a diff or a shell command full of
// < is unreadable to a human tailing the stream, and no parser needs it.
func TestStreamSink_DoesNotEscapeHTMLInAToolInput(t *testing.T) {
	var out bytes.Buffer
	newStreamSink(&out).event(session.SessionEvent{
		Kind: session.KindToolCalled, ToolName: "bash",
		Input: json.RawMessage(`{"command":"grep -n \"a < b\" x.go"}`),
	})
	if strings.Contains(out.String(), "\\u003c") {
		t.Errorf("the stream escaped a comparison operator:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "a < b") {
		t.Errorf("the stream did not carry the command as written:\n%s", out.String())
	}
}

// TestTextSink_PrintsTheAnswerEvenWithoutDeltas: a provider that streams no deltas
// still ends its text block with the whole of it, and printing nothing for that
// would be the worst possible failure of the format whose job is showing the answer.
func TestTextSink_PrintsTheAnswerEvenWithoutDeltas(t *testing.T) {
	var stdout, stderr bytes.Buffer
	sink := &textSink{out: &stdout, progress: &stderr}
	sink.event(session.SessionEvent{Kind: session.KindTextStarted})
	sink.event(session.SessionEvent{Kind: session.KindTextEnded, Text: "all at once"})
	if err := sink.finish(resultDocument{Result: "all at once"}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "all at once\n" {
		t.Errorf("stdout = %q, want the answer plus a closing newline", stdout.String())
	}
}

// TestTextSink_DoesNotPrintTheAnswerTwice: with deltas, the block's closing event
// repeats the whole text, and a format that printed both would double every answer.
func TestTextSink_DoesNotPrintTheAnswerTwice(t *testing.T) {
	var stdout, stderr bytes.Buffer
	sink := &textSink{out: &stdout, progress: &stderr}
	sink.event(session.SessionEvent{Kind: session.KindTextStarted})
	sink.event(session.SessionEvent{Kind: session.KindTextDelta, Text: "half "})
	sink.event(session.SessionEvent{Kind: session.KindTextDelta, Text: "and half"})
	sink.event(session.SessionEvent{Kind: session.KindTextEnded, Text: "half and half"})
	if err := sink.finish(resultDocument{Result: "half and half"}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "half and half\n" {
		t.Errorf("stdout = %q, want the answer once", stdout.String())
	}
}

// TestTextSink_DescribesACallAsItsToolDescribesItself: the activity line comes from
// what the tool declares (R2's Presentation), which is what lets a tool atenea has
// never heard of read correctly without a name switch here.
func TestTextSink_DescribesACallAsItsToolDescribesItself(t *testing.T) {
	catalog := fakeCatalog{
		"deploy": presentingTool{
			declaringTool: declaringTool{name: "deploy", effects: tool.RunsCommands},
			presentation:  tool.Presentation{Label: "Deploy", Subject: "production"},
		},
		"quiet": declaringTool{name: "quiet", effects: tool.NoEffects},
	}
	var stdout, stderr bytes.Buffer
	sink := &textSink{out: &stdout, progress: &stderr, catalog: catalog}

	sink.event(session.SessionEvent{Kind: session.KindToolCalled, CallID: "c1", ToolName: "deploy",
		Input: json.RawMessage(`{"target":"production"}`)})
	sink.event(session.SessionEvent{Kind: session.KindToolCalled, CallID: "c2", ToolName: "quiet",
		Input: json.RawMessage(`{"a": 1}`)})

	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr = %q, want one line per call", stderr.String())
	}
	if !strings.Contains(lines[0], "Deploy") || !strings.Contains(lines[0], "production") {
		t.Errorf("line = %q, want the label and subject the tool declared", lines[0])
	}
	// A tool that describes nothing still reads honestly: its own name, and the
	// input the model actually sent.
	if !strings.Contains(lines[1], "quiet") || !strings.Contains(lines[1], `{"a":1}`) {
		t.Errorf("line = %q, want the name and the raw input", lines[1])
	}
	if stdout.Len() != 0 {
		t.Errorf("the activity reached stdout: %q", stdout.String())
	}
}

// TestTextSink_ReportsAFailedCallWithItsCause, because a run whose tool was refused
// has to say so somewhere a person looks.
func TestTextSink_ReportsAFailedCallWithItsCause(t *testing.T) {
	var stdout, stderr bytes.Buffer
	sink := &textSink{out: &stdout, progress: &stderr}
	sink.event(session.SessionEvent{Kind: session.KindToolFailed, CallID: "c1", ToolName: "write",
		Error: "tool denied by the user"})
	if !strings.Contains(stderr.String(), "write") || !strings.Contains(stderr.String(), "denied") {
		t.Errorf("stderr = %q, want the tool and the cause", stderr.String())
	}
}

// TestOneLine_NeutralizesWhatTheModelWrote: every string on an activity line is
// text the model produced, and it reaches a terminal. Escape sequences are stripped
// and the line stays one line.
func TestOneLine_NeutralizesWhatTheModelWrote(t *testing.T) {
	got := oneLine("\x1b[31mred\x1b[0m\nand\ta second line", 200)
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("oneLine kept the escape character: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("oneLine kept a newline: %q", got)
	}
	if !strings.Contains(got, "red") || !strings.Contains(got, "a second line") {
		t.Errorf("oneLine dropped the text itself: %q", got)
	}
}

func TestOneLine_TruncatesByRune(t *testing.T) {
	got := oneLine("ááááá", 3)
	if got != "ááá…" {
		t.Errorf("oneLine = %q, want three runes and an ellipsis", got)
	}
}

// TestSummarize_CompactsWithoutReadingAnyField: which field of an input matters is
// the tool's business, and guessing it by name is what tool.Presenter replaces.
func TestSummarize_CompactsWithoutReadingAnyField(t *testing.T) {
	got := summarize(json.RawMessage("{\n  \"command\": \"ls -la\"\n}"))
	if got != `{"command":"ls -la"}` {
		t.Errorf("summarize = %q, want the compacted input", got)
	}
	if summarize(nil) != "" {
		t.Errorf("summarize(nil) = %q, want nothing", summarize(nil))
	}
}

// The doubles below model the three states a tool can be in with respect to the
// optional capability interfaces, the same way internal/permission's tests do.

type fakeCatalog map[string]tool.Tool

func (c fakeCatalog) Lookup(name string) (tool.Tool, bool) {
	t, ok := c[name]
	return t, ok
}

func (c fakeCatalog) Names() []string {
	names := make([]string, 0, len(c))
	for name := range c {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type declaringTool struct {
	name    string
	effects tool.Effects
}

func (d declaringTool) Name() string            { return d.name }
func (d declaringTool) Description() string     { return d.name + " stub" }
func (d declaringTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (d declaringTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, nil
}
func (d declaringTool) Effects() tool.Effects { return d.effects }

type presentingTool struct {
	declaringTool
	presentation tool.Presentation
}

func (p presentingTool) Present(tool.Call, tool.Result) tool.Presentation { return p.presentation }
