package cli

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
)

// TestRun_StreamJSONIsTheDurableEventStream is the protocol test. Every line has
// to parse back into the durable event the store wrote, because that stream is the
// contract — the CLI serializes it and invents nothing.
func TestRun_StreamJSONIsTheDurableEventStream(t *testing.T) {
	s := newScenario(t, answer("the answer"))
	s.stdin = strings.NewReader("what is the answer?")
	s.stdinIsTerminal = false

	got := s.run("run", "--output-format", "stream-json")

	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	parsed := events(t, got.stdout)
	seen := kinds(parsed)
	for _, want := range []session.EventKind{
		session.KindSessionCwd,
		session.KindStepStarted,
		session.KindTextStarted,
		session.KindTextDelta,
		session.KindTextEnded,
		session.KindStepEnded,
	} {
		if !contains(seen, want) {
			t.Errorf("the stream has no %s event; kinds = %v", want, seen)
		}
	}
	// Seq is the durable order and has to survive the wire: a consumer that
	// reconnects filters on it.
	for i, ev := range parsed {
		if ev.Seq != session.Seq(i+1) {
			t.Fatalf("events[%d].Seq = %d, want %d — the durable order did not survive serialization", i, ev.Seq, i+1)
		}
		if ev.SessionID == "" {
			t.Fatalf("events[%d] carries no SessionID", i)
		}
	}
	// The prompt is in the stream as the user message the run admitted, which is
	// how a consumer knows what the answer answers.
	if !strings.Contains(got.stdout, "what is the answer?") {
		t.Errorf("the stream does not carry the admitted prompt:\n%s", got.stdout)
	}
	// Nothing but the events reaches stdout: a warning or a progress line there
	// would break every consumer parsing it line by line.
	for _, line := range strings.Split(strings.TrimSpace(got.stdout), "\n") {
		if !strings.HasPrefix(line, "{") {
			t.Errorf("stdout carries a line that is not an event: %q", line)
		}
	}
}

// TestRun_StreamJSONFlushesEachEventWhileTheTurnRuns: NDJSON is only a stream if a
// consumer can read it before the process exits. The turn is held open and the
// events written so far are read from stdout while it is still running.
func TestRun_StreamJSONFlushesEachEventWhileTheTurnRuns(t *testing.T) {
	s := newScenario(t, answer("eventually"))
	s.provider.opened = make(chan struct{})
	s.provider.block = make(chan struct{})
	s.stdin = strings.NewReader("hold on")
	s.stdinIsTerminal = false

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()

	done := make(chan outcomeOf, 1)
	go func() {
		out := s.runWithStdout(writer, "run", "--output-format", "stream-json")
		_ = writer.Close()
		done <- out
	}()

	// The provider has been reached, so the prompt was admitted and its durable
	// events are written — but the turn has not produced anything yet.
	<-s.provider.opened
	first := readLine(t, reader)
	var early session.SessionEvent
	if err := unmarshal(first, &early); err != nil {
		t.Fatalf("first line is not an event: %v\nline: %s", err, first)
	}
	if early.Kind != session.KindSessionCwd {
		t.Fatalf("first streamed event = %s, want %s while the turn is still open", early.Kind, session.KindSessionCwd)
	}

	close(s.provider.block)
	rest := readAll(t, reader)
	out := <-done
	if out.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", out.code, ExitOK, out.stderr)
	}
	if !strings.Contains(rest, "Text.Delta") {
		t.Errorf("the rest of the stream never arrived:\n%s", rest)
	}
}

// TestRun_TextFormatSplitsTheAnswerFromTheActivity: the human format is also the
// pipeable one. `atenea run -p ... > answer` has to keep the answer and nothing
// else, which is why the activity goes to stderr.
func TestRun_TextFormatSplitsTheAnswerFromTheActivity(t *testing.T) {
	file := "notes.txt"
	s := newScenario(t,
		callTool("c1", "write", `{"path":"`+file+`","content":"hello"}`),
		answer("wrote it"),
	)

	got := s.run("run", "-p", "write the notes", "--permission-mode", "allowlist", "--allow-effects", "writes-files")

	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	if strings.TrimSpace(got.stdout) != "wrote it" {
		t.Errorf("stdout = %q, want only the answer", got.stdout)
	}
	// The activity line names the tool as the tool itself describes it (R2's
	// Presentation), not as a switch on its name here.
	if !strings.Contains(got.stderr, "Write") || !strings.Contains(got.stderr, file) {
		t.Errorf("stderr does not report the tool activity:\n%s", got.stderr)
	}
}

// TestRun_JSONFormatReportsOneResultDocument: the format for a caller that wants
// an answer rather than a conversation.
func TestRun_JSONFormatReportsOneResultDocument(t *testing.T) {
	s := newScenario(t, answer("42"))

	got := s.run("run", "-p", "the answer?", "--output-format", "json")

	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	doc := decodeResult(t, got.stdout)
	if doc.Result != "42" {
		t.Errorf("result = %q, want the assistant's answer", doc.Result)
	}
	if doc.Status != statusOK || doc.ExitCode != ExitOK {
		t.Errorf("status/exit_code = %q/%d, want %q/%d", doc.Status, doc.ExitCode, statusOK, ExitOK)
	}
	if doc.SessionID == "" || !strings.HasPrefix(doc.SessionID, "cli-") {
		t.Errorf("session_id = %q, want a generated cli- id", doc.SessionID)
	}
	if doc.DeniedToolCalls != 0 || doc.ToolCalls != 0 {
		t.Errorf("tool counts = %d/%d, want 0/0", doc.ToolCalls, doc.DeniedToolCalls)
	}
}

// TestRun_DenyModeRefusesAGatedCallAndSaysSo is the default mode's contract, and
// the security property of the whole feature: a run nobody authorized does not get
// to write to the workspace, the model is told, and the exit code makes the refusal
// visible to whatever invoked it.
func TestRun_DenyModeRefusesAGatedCallAndSaysSo(t *testing.T) {
	s := newScenario(t,
		callTool("c1", "write", `{"path":"forbidden.txt","content":"nope"}`),
		answer("I could not write the file"),
	)

	got := s.run("run", "-p", "write a file", "--output-format", "json")

	if got.code != ExitPermissionDenied {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitPermissionDenied, got.stderr)
	}
	doc := decodeResult(t, got.stdout)
	if doc.Status != statusPermissionDenied {
		t.Errorf("status = %q, want %q", doc.Status, statusPermissionDenied)
	}
	if doc.DeniedToolCalls != 1 {
		t.Errorf("denied_tool_calls = %d, want 1", doc.DeniedToolCalls)
	}
	if _, err := os.Stat(filepath.Join(s.root, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatalf("stat forbidden.txt = %v, want the refused call never to have run", err)
	}
	// The refusal has to reach the model as the tool's result, or it cannot adapt.
	if s.provider.requestCount() < 2 {
		t.Fatalf("the turn did not continue after the refusal (%d requests)", s.provider.requestCount())
	}
	second := s.provider.request(1)
	if !mentionsToolResult(second, "c1") {
		t.Errorf("the model was not told about the refused call: %#v", second.Messages)
	}
}

// TestRun_DenyModeStillRunsWhatDeclaresNoEffects: deny is not "refuse
// everything". The read-only half of the catalog declares it affects nothing
// outside the conversation and runs, which is what makes an unattended
// investigation useful with nothing granted.
func TestRun_DenyModeStillRunsWhatDeclaresNoEffects(t *testing.T) {
	s := newScenario(t,
		callTool("c1", "glob", `{"pattern":"*"}`),
		answer("looked around"),
	)
	if err := os.WriteFile(filepath.Join(s.root, "visible.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := s.run("run", "-p", "look around", "--output-format", "json")

	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	if doc := decodeResult(t, got.stdout); doc.DeniedToolCalls != 0 || doc.ToolCalls != 1 {
		t.Errorf("tool counts = %d called / %d denied, want 1/0", doc.ToolCalls, doc.DeniedToolCalls)
	}
}

// TestRun_AllowlistModeIsAnEffectBudget: what the mode allows is stated as
// consequences, so the tool that writes files runs and the tool that executes
// commands does not — without either being named on the command line.
func TestRun_AllowlistModeIsAnEffectBudget(t *testing.T) {
	s := newScenario(t,
		callTool("c1", "write", `{"path":"allowed.txt","content":"yes"}`),
		callTool("c2", "bash", `{"command":"touch refused.txt"}`),
		answer("done what I could"),
	)

	got := s.run("run", "-p", "write and run", "--permission-mode", "allowlist",
		"--allow-effects", "writes-files", "--output-format", "json")

	if got.code != ExitPermissionDenied {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitPermissionDenied, got.stderr)
	}
	if doc := decodeResult(t, got.stdout); doc.DeniedToolCalls != 1 {
		t.Errorf("denied_tool_calls = %d, want 1 (bash refused, write allowed)", doc.DeniedToolCalls)
	}
	if _, err := os.Stat(filepath.Join(s.root, "allowed.txt")); err != nil {
		t.Errorf("stat allowed.txt = %v, want the allowed call to have run", err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "refused.txt")); !os.IsNotExist(err) {
		t.Errorf("stat refused.txt = %v, want the command never to have run", err)
	}
}

// TestRun_AutoModeRunsEverythingAndWarnsAboutIt: the dangerous mode. It has to
// work, and the run has to say what it is doing where a person will see it.
func TestRun_AutoModeRunsEverythingAndWarnsAboutIt(t *testing.T) {
	s := newScenario(t,
		callTool("c1", "bash", `{"command":"echo unattended > ran.txt"}`),
		answer("ran it"),
	)

	got := s.run("run", "-p", "run something", "--permission-mode", "auto", "--output-format", "json")

	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	if doc := decodeResult(t, got.stdout); doc.DeniedToolCalls != 0 {
		t.Errorf("denied_tool_calls = %d, want 0 in auto", doc.DeniedToolCalls)
	}
	if _, err := os.Stat(filepath.Join(s.root, "ran.txt")); err != nil {
		t.Errorf("stat ran.txt = %v, want the command to have run unattended", err)
	}
	if !strings.Contains(got.stderr, "permission-mode auto") || !strings.Contains(got.stderr, "unattended") {
		t.Errorf("auto ran without warning anyone:\n%s", got.stderr)
	}
	// The warning must not be on stdout, whatever the format: a consumer parsing
	// the document would choke on it.
	if strings.Contains(got.stdout, "unattended") {
		t.Errorf("the warning reached stdout:\n%s", got.stdout)
	}
}

// TestRun_TurnFailureExitsTurnFailed: a provider that fails mid-stream is not a
// usage problem and not a denial, and the difference is what a caller branches on.
func TestRun_TurnFailureExitsTurnFailed(t *testing.T) {
	s := newScenario(t, []llm.Event{
		{Kind: llm.StepStarted},
		{Kind: llm.StepFailed, Text: "upstream exploded"},
	})

	got := s.run("run", "-p", "try", "--output-format", "json")

	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitFailure, got.stderr)
	}
	doc := decodeResult(t, got.stdout)
	if doc.Status != statusTurnFailed {
		t.Errorf("status = %q, want %q", doc.Status, statusTurnFailed)
	}
	if !strings.Contains(doc.Error, "upstream exploded") {
		t.Errorf("error = %q, want the provider's cause", doc.Error)
	}
}

// TestRun_SessionFlagContinuesTheConversation: the point of --session. The second
// run's request has to carry the first run's exchange, which is the only way to
// tell from outside that the store was resumed rather than a fresh session
// started.
func TestRun_SessionFlagContinuesTheConversation(t *testing.T) {
	s := newScenario(t, answer("first"), answer("second"))

	if got := s.run("run", "--session", "ci-42", "-p", "remember this"); got.code != ExitOK {
		t.Fatalf("first run exit code = %d\nstderr: %s", got.code, got.stderr)
	}
	if got := s.run("run", "--session", "ci-42", "-p", "and now this"); got.code != ExitOK {
		t.Fatalf("second run exit code = %d\nstderr: %s", got.code, got.stderr)
	}

	if s.provider.requestCount() != 2 {
		t.Fatalf("provider saw %d requests, want 2", s.provider.requestCount())
	}
	history := texts(t, s.provider.request(1))
	for _, want := range []string{"remember this", "first", "and now this"} {
		if !containsString(history, want) {
			t.Errorf("the second turn's history is missing %q: %v", want, history)
		}
	}
}

// TestRun_UnnamedSessionsAreIndependent is the other half of that contract: two
// runs with no --session must not accidentally share a conversation.
func TestRun_UnnamedSessionsAreIndependent(t *testing.T) {
	s := newScenario(t, answer("first"), answer("second"))

	first := decodeResult(t, s.run("run", "-p", "one", "--output-format", "json").stdout)
	second := decodeResult(t, s.run("run", "-p", "two", "--output-format", "json").stdout)

	if first.SessionID == second.SessionID {
		t.Fatalf("both runs used session %q", first.SessionID)
	}
	if history := texts(t, s.provider.request(1)); containsString(history, "one") {
		t.Errorf("the second run inherited the first run's history: %v", history)
	}
}

// TestRun_CancellationStopsTheTurn: a signal has to stop the run and report it as
// its own outcome, and the stream must end cleanly rather than half-written.
func TestRun_CancellationStopsTheTurn(t *testing.T) {
	s := newScenario(t, answer("never finished"))
	s.provider.opened = make(chan struct{})
	s.provider.block = make(chan struct{})

	done := make(chan outcomeOf, 1)
	go func() { done <- s.run("run", "-p", "take your time", "--output-format", "json") }()

	<-s.provider.opened
	s.interrupts <- syscall.SIGINT

	got := <-done
	if got.code != ExitCanceled {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitCanceled, got.stderr)
	}
	if doc := decodeResult(t, got.stdout); doc.Status != statusCanceled {
		t.Errorf("status = %q, want %q", doc.Status, statusCanceled)
	}
	close(s.provider.block)
}

// TestRun_AsksTheHostForTheSameBootstrapAsTheInteractiveInterface verifies that
// --cwd is the only bootstrap value a headless run varies.
func TestRun_AsksTheHostForTheSameBootstrapAsTheInteractiveInterface(t *testing.T) {
	s := newScenario(t, answer("ok"))
	workspace := t.TempDir()

	if got := s.run("run", "-p", "hello", "--cwd", workspace); got.code != ExitOK {
		t.Fatalf("exit code = %d\nstderr: %s", got.code, got.stderr)
	}

	if len(s.configs) != 1 {
		t.Fatalf("the run assembled %d hosts, want 1", len(s.configs))
	}
	cfg := s.configs[0]
	if cfg.Dotenv != ".env" {
		t.Errorf("Dotenv = %q, want the same .env the interactive interface loads", cfg.Dotenv)
	}
	if cfg.Root != workspace {
		t.Errorf("Root = %q, want the --cwd %q", cfg.Root, workspace)
	}
}

// TestRun_CwdIsResolvedAgainstTheWorkspace: the workspace the run reports is the
// one --cwd named, and the tools resolve their relative paths inside it.
func TestRun_CwdIsResolvedAgainstTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	s := newScenario(t,
		callTool("c1", "write", `{"path":"inside.txt","content":"here"}`),
		answer("written"),
	)

	got := s.run("run", "-p", "write inside", "--cwd", workspace,
		"--permission-mode", "allowlist", "--allow-effects", "writes-files", "--output-format", "stream-json")

	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	if _, err := os.Stat(filepath.Join(workspace, "inside.txt")); err != nil {
		t.Errorf("stat inside.txt under --cwd = %v, want the file written there", err)
	}
	for _, ev := range events(t, got.stdout) {
		if ev.Kind == session.KindSessionCwd && ev.Text != workspace {
			t.Errorf("Session.Cwd = %q, want the --cwd %q", ev.Text, workspace)
		}
	}
}

// TestRun_MissingCwdIsAUsageError: everything downstream of a bad root degrades
// quietly — no skills, file tools resolving against nothing — so the run would
// produce a plausible answer to a question about the wrong directory.
func TestRun_MissingCwdIsAUsageError(t *testing.T) {
	s := newScenario(t, answer("ok"))

	got := s.run("run", "-p", "hello", "--cwd", filepath.Join(t.TempDir(), "nope"))

	if got.code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", got.code, ExitUsage)
	}
	if len(s.configs) != 0 {
		t.Error("the run assembled a host despite an invalid --cwd")
	}
}

// TestRun_UnknownOutputFormatIsAUsageError, and the message lists the ones that
// exist: a caller who guessed "ndjson" is one word away from the right answer.
func TestRun_UnknownOutputFormatIsAUsageError(t *testing.T) {
	s := newScenario(t, answer("ok"))

	got := s.run("run", "-p", "hello", "--output-format", "ndjson")

	if got.code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", got.code, ExitUsage)
	}
	if !strings.Contains(got.stderr, "stream-json") {
		t.Errorf("stderr does not list the valid formats:\n%s", got.stderr)
	}
}

// TestRun_PositionalArgumentIsAUsageError: `atenea run "do the thing"` is a
// plausible mistake, and accepting it would add a third place a prompt can come
// from. Naming the flag is more useful than guessing.
func TestRun_PositionalArgumentIsAUsageError(t *testing.T) {
	s := newScenario(t, answer("ok"))

	got := s.run("run", "do the thing")

	if got.code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", got.code, ExitUsage)
	}
	if !strings.Contains(got.stderr, "-p") {
		t.Errorf("stderr does not point at -p:\n%s", got.stderr)
	}
}

// TestRun_HelpExitsSuccessfully: -h is a question, not a mistake.
func TestRun_HelpExitsSuccessfully(t *testing.T) {
	s := newScenario(t, answer("ok"))

	got := s.run("run", "-h")

	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d", got.code, ExitOK)
	}
	for _, want := range []string{"--permission-mode", "--output-format", "Exit codes"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("`atenea run -h` does not document %q:\n%s", want, got.stderr)
		}
	}
}

// The prompt resolution rules, tested on the function rather than only through a
// whole run: the precedence is the part an integrator has to be able to rely on.

// TestRun_FlagPromptNeverWaitsOnAnOpenStdin is the regression test for the hang
// this shipped with first. The write end of the pipe is deliberately left open,
// which is the normal state of stdin under a CI runner, `ssh` without -n,
// `docker run -i`, or an editor plugin that spawns the process with stdin=PIPE and
// reads the answer before closing it — a true deadlock, since the plugin waits for
// output while atenea waits for EOF. A strings.Reader or /dev/null cannot reproduce
// it, which is exactly why the first version of these tests missed it.
func TestRun_FlagPromptNeverWaitsOnAnOpenStdin(t *testing.T) {
	s := newScenario(t, answer("done"))
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// Never closed on purpose: nothing may make the run depend on the other end.
	defer func() { _ = writer.Close(); _ = reader.Close() }()
	s.stdin = reader
	// A pipe is not a terminal, which is precisely what the broken rule keyed on:
	// without this line the scenario's default hides the bug rather than showing it.
	s.stdinIsTerminal = false

	done := make(chan outcomeOf, 1)
	go func() { done <- s.run("run", "-p", "hi", "--output-format", "json") }()

	select {
	case got := <-done:
		if got.code != ExitOK {
			t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
		}
		if doc := decodeResult(t, got.stdout); doc.Result != "done" {
			t.Errorf("result = %q, want the turn to have run normally", doc.Result)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("`atenea run -p ...` blocked on an open stdin nobody closes — the deadlock an editor plugin hits")
	}
}

func TestResolvePrompt_FlagAloneNeverTouchesStdin(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		got, err := resolvePrompt("just the flag", false, Env{StdinIsTerminal: terminal, Stdin: neverReadable{}})
		if err != nil || got != "just the flag" {
			t.Fatalf("StdinIsTerminal=%v: prompt = %q, err = %v, want the flag alone", terminal, got, err)
		}
	}
}

func TestResolvePrompt_NoFlagReadsStdin(t *testing.T) {
	got, err := resolvePrompt("", false, Env{Stdin: strings.NewReader("just the pipe\n")})
	if err != nil || got != "just the pipe" {
		t.Fatalf("prompt = %q, err = %v, want the piped text alone", got, err)
	}
}

// TestResolvePrompt_StdinFlagComposesBothSources: the composition an integrator
// wants — `git diff | atenea run -p "review this diff" --stdin` — with neither part
// dropped, because dropping one answers a question the caller did not ask and
// leaves no trace that it did.
func TestResolvePrompt_StdinFlagComposesBothSources(t *testing.T) {
	got, err := resolvePrompt("review this diff", true, Env{Stdin: strings.NewReader("--- a/x\n+++ b/x\n")})
	if err != nil {
		t.Fatalf("resolvePrompt: %v", err)
	}
	want := "review this diff\n\n--- a/x\n+++ b/x"
	if got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

// TestResolvePrompt_TerminalStdinFailsInsteadOfWaiting, whichever way stdin was
// asked for. Blocking forever on an input nobody is going to type is the failure a
// pipeline cannot recover from.
func TestResolvePrompt_TerminalStdinFailsInsteadOfWaiting(t *testing.T) {
	_, err := resolvePrompt("", false, Env{StdinIsTerminal: true, Stdin: neverReadable{}})
	if err == nil {
		t.Fatal("resolvePrompt accepted an invocation with no prompt at all")
	}
	if !strings.Contains(err.Error(), "-p") {
		t.Errorf("err = %v, want it to name the flag that fixes it", err)
	}

	_, err = resolvePrompt("something", true, Env{StdinIsTerminal: true, Stdin: neverReadable{}})
	if err == nil {
		t.Fatal("--stdin against a terminal was accepted; there is nothing on the other end to wait for")
	}
	if !strings.Contains(err.Error(), "--stdin") {
		t.Errorf("err = %v, want it to name --stdin", err)
	}
}

func TestResolvePrompt_EmptyPipeIsNoPrompt(t *testing.T) {
	if _, err := resolvePrompt("   ", false, Env{Stdin: strings.NewReader("  \n")}); err == nil {
		t.Fatal("resolvePrompt accepted whitespace as a prompt")
	}
}

// neverReadable fails loudly if it is read, which is how the tests above prove the
// cases that must not touch stdin do not touch it.
type neverReadable struct{}

func (neverReadable) Read([]byte) (int, error) { panic("stdin was read when nothing asked for it") }

// TestRun_RefusesToRunWithoutAProvider: with no credential anywhere the host lands
// on the offline demo, whose replies are canned. Interactively that is a notice a
// person acts on; here it would be a fabricated answer on stdout and a green build,
// so the run refuses before a turn starts.
func TestRun_RefusesToRunWithoutAProvider(t *testing.T) {
	s := newScenario(t, answer("this must never be reached"))
	s.offline = true

	got := s.run("run", "-p", "hello", "--output-format", "json")

	if got.code != ExitStartup {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitStartup, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing: a canned answer on stdout is what this refuses", got.stdout)
	}
	if s.provider.requestCount() != 0 {
		t.Errorf("the run reached a provider %d times, want 0", s.provider.requestCount())
	}
	if !strings.Contains(got.stderr, "no model provider is configured") {
		t.Errorf("stderr does not say what is wrong:\n%s", got.stderr)
	}
	// The advice is read off the shipped catalog, so it names keys that really work.
	for _, key := range providerKeyNames() {
		if !strings.Contains(got.stderr, key) {
			t.Errorf("stderr does not offer %s as a fix:\n%s", key, got.stderr)
		}
	}
}

// TestProviderKeyNames_ComeFromTheShippedCatalog: the advice is derived rather than
// typed out, so a provider added to the catalog is suggested on the same commit.
func TestProviderKeyNames_ComeFromTheShippedCatalog(t *testing.T) {
	names := providerKeyNames()
	if len(names) == 0 {
		t.Fatal("no provider key names; the advice would be empty")
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			t.Errorf("%q is suggested twice: %v", name, names)
		}
		seen[name] = true
	}
	for _, provider := range providerconfig.DefaultCatalog().Providers {
		if provider.APIKeyEnv != "" && !seen[provider.APIKeyEnv] {
			t.Errorf("the catalog offers %s but the advice does not name it", provider.APIKeyEnv)
		}
	}
}

// texts projects a request's history into the strings the model was given.
func texts(t *testing.T, req llm.Request) []string {
	t.Helper()
	out := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		text, err := message.TextOnly()
		if err != nil {
			t.Fatalf("message content is not text: %v", err)
		}
		out = append(out, text)
	}
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if strings.Contains(candidate, needle) {
			return true
		}
	}
	return false
}

// mentionsToolResult reports whether the request carries the result of the tool
// call, which is how the model learns a call was refused.
func mentionsToolResult(req llm.Request, callID string) bool {
	for _, message := range req.Messages {
		if message.ToolCallID == callID {
			return true
		}
	}
	return false
}
