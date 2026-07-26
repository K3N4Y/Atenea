package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"

	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

// The output formats. They are values rather than a Go enum because they are
// user-facing spellings first: the flag help, the usage error and the switch all
// read the same string.
const (
	formatText       = "text"
	formatJSON       = "json"
	formatStreamJSON = "stream-json"
)

// outputFormats lists them in the order the help shows, with the default first.
var outputFormats = []string{formatText, formatJSON, formatStreamJSON}

// sink is one output format. Its methods are called with the stream's lock held,
// so an implementation never locks and never has to think about ordering: what it
// sees is the durable order of the session log.
type sink interface {
	event(ev session.SessionEvent)
	// finish writes what the format owes at the end of a run. It is called
	// exactly once, after which no further event reaches the sink.
	finish(doc resultDocument) error
}

// stream is the single place a durable event becomes output. The bus calls
// observe from the runner's goroutines — a turn settles its tool calls in
// parallel — so it serializes on one lock.
//
// That lock does more than protect the writer. It makes stdout order the durable
// order, and it makes a slow consumer apply backpressure to the turn instead of
// having its events buffered: a caller reading NDJSON line by line is throttling
// the agent, which is what a streaming protocol is supposed to allow.
type stream struct {
	mu       sync.Mutex
	sink     sink
	fold     fold
	finished bool
}

// attach installs the sink. It is separate from construction because the text
// format asks each tool how its calls should read, and the catalog that answers
// only exists after the assembly, while the bus this stream feeds has to exist
// before it. Nothing can emit before the first prompt is admitted, which is after
// both.
func (s *stream) attach(sink sink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sink = sink
}

// observe folds the event into the run's result and hands it to the format.
//
// After finish it drops the event instead of writing it. That is the guarantee a
// consumer needs when a run is interrupted: whatever the abandoned runner still
// writes to the store cannot appear after the closing document, so a stream is
// either complete or truncated at a line boundary, never interleaved.
func (s *stream) observe(ev session.SessionEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	s.fold.apply(ev)
	if s.sink != nil {
		s.sink.event(ev)
	}
}

// close folds the outcome into the final document, writes it and seals the
// stream.
func (s *stream) close(res result) (resultDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.fold.document(res)
	s.finished = true
	if s.sink == nil {
		return doc, nil
	}
	return doc, s.sink.finish(doc)
}

// fold is what the run's result is derived from: the event stream itself, read
// once as it goes past. There is no second bookkeeping path — every field below
// answers a question the durable log already answers, which is what keeps the
// closing document from becoming a parallel account of the run that can disagree
// with the events a consumer just read.
//
// The one thing it cannot answer is how many calls the permission mode refused.
// A denial reaches the stream as a Tool.Failed whose Error is a message, and
// recognizing it would mean matching that message — the string coupling that
// already exists in the TUI's transcript and that R5 is meant to remove. The
// count comes from the policy that made the decision instead. See countingPolicy.
type fold struct {
	// text is the assistant's answer: the coalesced Message of the last step that
	// closed. A turn with tool calls runs several steps, and the last one is the
	// one that answers the prompt.
	text      string
	usage     session.Usage
	toolCalls int
	// turnError is the provider or store failure that closed the turn. The run
	// result carries the same failure, but not always: a store error surfaces here
	// first, and a caller that only reads the stream must still see it.
	turnError string
}

func (f *fold) apply(ev session.SessionEvent) {
	switch ev.Kind {
	case session.KindStepEnded:
		if ev.Message != nil && ev.Message.Text != "" {
			f.text = ev.Message.Text
		}
		if ev.Usage != nil {
			f.usage.InputTokens += ev.Usage.InputTokens
			f.usage.OutputTokens += ev.Usage.OutputTokens
			f.usage.ReasoningTokens += ev.Usage.ReasoningTokens
			f.usage.CacheReadTokens += ev.Usage.CacheReadTokens
			f.usage.CacheWriteTokens += ev.Usage.CacheWriteTokens
			f.usage.CacheableInputTokens += ev.Usage.CacheableInputTokens
		}
	case session.KindStepFailed:
		f.turnError = ev.Error
	case session.KindToolCalled:
		f.toolCalls++
	}
}

// document builds the closing document from the fold and the run's own outcome.
func (f *fold) document(res result) resultDocument {
	status, code := outcome(res)
	message := res.Error
	if message == "" {
		message = f.turnError
	}
	if status == statusOK && message != "" {
		// The stream reported a failure the run handle did not. The stream is the
		// source of truth about what happened, so the document follows it rather
		// than reporting a success the events contradict.
		status, code = statusTurnFailed, ExitFailure
	}
	return resultDocument{
		SessionID:       res.SessionID,
		Status:          status,
		ExitCode:        code,
		Result:          f.text,
		ToolCalls:       f.toolCalls,
		DeniedToolCalls: res.DeniedToolCalls,
		Usage: usageDocument{
			InputTokens:          f.usage.InputTokens,
			OutputTokens:         f.usage.OutputTokens,
			ReasoningTokens:      f.usage.ReasoningTokens,
			CacheReadTokens:      f.usage.CacheReadTokens,
			CacheWriteTokens:     f.usage.CacheWriteTokens,
			CacheableInputTokens: f.usage.CacheableInputTokens,
		},
		Error: message,
	}
}

// resultDocument is what --output-format json prints, and what every format
// derives its exit code from. It is snake_case and tagged, unlike the events:
// this document is the CLI's own, so it owes nothing to the Wails boundary that
// fixes their shape.
//
// It stays small on purpose. Everything in it is either a fold of the stream or
// the outcome, so it adds no facts a consumer could not have computed — it exists
// so a caller that wants one answer does not have to consume the whole stream to
// get it.
type resultDocument struct {
	SessionID       string        `json:"session_id"`
	Status          string        `json:"status"`
	ExitCode        int           `json:"exit_code"`
	Result          string        `json:"result"`
	ToolCalls       int           `json:"tool_calls"`
	DeniedToolCalls int           `json:"denied_tool_calls"`
	Usage           usageDocument `json:"usage"`
	Error           string        `json:"error,omitempty"`
}

type usageDocument struct {
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	ReasoningTokens      int `json:"reasoning_tokens"`
	CacheReadTokens      int `json:"cache_read_tokens"`
	CacheWriteTokens     int `json:"cache_write_tokens"`
	CacheableInputTokens int `json:"cacheable_input_tokens"`
}

// newSink builds the sink for the format. catalog answers how a tool's call
// should read and may be nil.
func newSink(format string, stdout, stderr io.Writer, catalog tool.Catalog) sink {
	switch format {
	case formatJSON:
		return &jsonSink{out: stdout}
	case formatStreamJSON:
		return newStreamSink(stdout)
	default:
		return &textSink{out: stdout, progress: stderr, catalog: catalog}
	}
}

// streamSink writes the durable events as NDJSON: one session.SessionEvent per
// line, in durable order.
//
// The events are marshaled as they are — Go field names, no tags — because that
// is already the wire shape of this stream. The desktop frontend consumes exactly
// these keys over Wails (`ev.Kind`, `ev.ToolName`, `ev.Input`), so tagging the
// struct to make the NDJSON prettier would either break a shipped consumer or
// give the same events two spellings depending on which host emitted them. One
// serialization of one contract is worth more than snake_case. Stabilizing the
// taxonomy itself is R5's job, not this format's.
type streamSink struct {
	out io.Writer
	enc *json.Encoder
}

func newStreamSink(out io.Writer) *streamSink {
	enc := json.NewEncoder(out)
	// Escaping < > & would corrupt nothing, but it makes a diff or a command in a
	// tool's input unreadable to a human tailing the stream, and no JSON parser
	// needs it.
	enc.SetEscapeHTML(false)
	return &streamSink{out: out, enc: enc}
}

// event writes one line and flushes it. The flush is the point of the format: a
// consumer reading line by line has to see an event while the turn is still
// running, not when the process exits.
func (s *streamSink) event(ev session.SessionEvent) {
	if err := s.enc.Encode(ev); err != nil {
		// stdout is gone (a closed pipe, a full disk). There is nowhere left to
		// report it, and failing the turn over a broken reader would be worse than
		// finishing it: the events are already durable in the store.
		return
	}
	flush(s.out)
}

func (s *streamSink) finish(resultDocument) error { return nil }

// jsonSink prints one document when the run ends and nothing while it runs. It is
// the format for a caller that wants an answer rather than a conversation.
type jsonSink struct{ out io.Writer }

func (jsonSink) event(session.SessionEvent) {}

func (s *jsonSink) finish(doc resultDocument) error {
	enc := json.NewEncoder(s.out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	flush(s.out)
	return nil
}

// textSink is the format for a person. It splits the run across the two streams
// the way a Unix tool does: the answer goes to stdout, the activity that produced
// it goes to stderr.
//
// That split is what makes the human format usable in a pipeline too —
// `atenea run -p "..." > answer.md` keeps only the answer, `2>/dev/null` silences
// the progress — so `text` does not have to be traded for a machine format just to
// redirect the result somewhere.
type textSink struct {
	out      io.Writer
	progress io.Writer
	catalog  tool.Catalog
	// wroteText tracks whether the current text block reached stdout as deltas. A
	// provider that streams no deltas still emits the whole block on Text.Ended,
	// and printing nothing at all for it would be the worst possible failure of a
	// format whose whole job is showing the answer.
	wroteText bool
}

func (s *textSink) event(ev session.SessionEvent) {
	switch ev.Kind {
	case session.KindTextStarted:
		s.wroteText = false
	case session.KindTextDelta:
		if ev.Text == "" {
			return
		}
		fmt.Fprint(s.out, ev.Text)
		flush(s.out)
		s.wroteText = true
	case session.KindTextEnded:
		if !s.wroteText && ev.Text != "" {
			fmt.Fprint(s.out, ev.Text)
			flush(s.out)
			s.wroteText = true
		}
	case session.KindToolCalled:
		s.note("·", ev)
	case session.KindToolFailed:
		s.noteError(ev)
	case session.KindStepFailed:
		if ev.Error != "" {
			fmt.Fprintf(s.progress, "✗ turn failed: %s\n", oneLine(ev.Error, errorLimit))
			flush(s.progress)
		}
	}
}

// finish closes the answer with a newline when the model's last text did not end
// with one, so a shell prompt does not land in the middle of it.
func (s *textSink) finish(doc resultDocument) error {
	if s.wroteText && !strings.HasSuffix(doc.Result, "\n") {
		fmt.Fprintln(s.out)
		flush(s.out)
	}
	return nil
}

func (s *textSink) note(mark string, ev session.SessionEvent) {
	label, subject := s.describe(ev)
	if subject == "" {
		fmt.Fprintf(s.progress, "%s %s\n", mark, label)
	} else {
		fmt.Fprintf(s.progress, "%s %s %s\n", mark, label, subject)
	}
	flush(s.progress)
}

func (s *textSink) noteError(ev session.SessionEvent) {
	label, subject := s.describe(ev)
	if subject != "" {
		label += " " + subject
	}
	fmt.Fprintf(s.progress, "✗ %s: %s\n", label, oneLine(ev.Error, errorLimit))
	flush(s.progress)
}

// describe asks the tool that settled the call how it should read, and falls back
// to the name the model used plus a summary of the input it sent. Nothing here
// switches on a tool name: a tool atenea has never heard of is described by
// whatever it declares, and one that declares nothing still reads honestly.
//
// Both strings are text the model wrote, so both are sanitized before they reach a
// terminal. The answer on stdout is not: that is the data the caller asked for,
// and rewriting it would corrupt a redirected file.
func (s *textSink) describe(ev session.SessionEvent) (label, subject string) {
	label = ev.ToolName
	if label == "" {
		label = "tool"
	}
	subject = summarize(ev.Input)
	if s.catalog == nil {
		return oneLine(label, labelLimit), subject
	}
	call := tool.Call{ID: ev.CallID, Name: ev.ToolName, Input: ev.Input}
	presentation, answered := tool.PresentationFor(s.catalog, call, tool.Result{})
	if !answered {
		return oneLine(label, labelLimit), subject
	}
	if presentation.Label != "" {
		label = presentation.Label
	}
	if presentation.Subject != "" {
		subject = oneLine(presentation.Subject, subjectLimit)
	}
	return oneLine(label, labelLimit), subject
}

const (
	labelLimit   = 40
	subjectLimit = 120
	errorLimit   = 200
)

// summarize renders a tool call's raw input as one short line, for a tool that
// does not describe its own calls. It compacts the JSON rather than reading any
// field of it: which field of an input is the interesting one is the tool's
// business, and guessing it by name is what tool.Presenter exists to replace.
func summarize(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, input); err != nil {
		return oneLine(string(input), subjectLimit)
	}
	return oneLine(compact.String(), subjectLimit)
}

// oneLine makes untrusted text safe to put on a terminal line: control characters
// go (which includes the escape that introduces an ANSI sequence), newlines and
// tabs become spaces, and the result is truncated to limit runes.
func oneLine(text string, limit int) string {
	mapped := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	mapped = strings.Join(strings.Fields(mapped), " ")
	runes := []rune(mapped)
	if len(runes) <= limit {
		return mapped
	}
	return string(runes[:limit]) + "…"
}

// flush pushes a line out of a buffered writer. os.Stdout is unbuffered, so this
// is a no-op in production and what makes the streaming assertions in the tests
// meaningful when the writer is not.
func flush(w io.Writer) {
	if f, ok := w.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
}
