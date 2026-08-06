package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/host"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
)

// The harness below is the point of the feature: a run is driven end to end
// through the real dispatch, the real host and the real wiring, with only the
// model and the database replaced. No terminal, no PTY, no subprocess.

// scenario is one invocation under test.
type scenario struct {
	t *testing.T
	// root is the workspace the agent is anchored to. Every scenario gets its own,
	// because the run reads it (skills, repo instructions) and the tools write to it.
	root string
	// store is shared across the runs of one scenario, which is what lets a test
	// resume a session the way a second `atenea run --session` would.
	store    session.Store
	provider *scriptedProvider
	// offline assembles the host on the provider it lands on with no credential
	// anywhere, which is what `atenea run` refuses to answer from.
	offline bool

	// stdin is what an invocation reads when it reads stdin at all. It is an
	// io.Reader rather than a string so a test can hand it an os.Pipe whose write
	// end stays open — the shape of the hang a strings.Reader can never reproduce.
	stdin           io.Reader
	stdinIsTerminal bool
	interrupts      chan os.Signal
	// configs records the host.Config each run resolved, so a test can assert what
	// the run asked the bootstrap for rather than only what came out of it.
	configs []host.Config
}

func newScenario(t *testing.T, turns ...[]llm.Event) *scenario {
	t.Helper()
	// Skill discovery scans $HOME, so a developer's installed skills would
	// otherwise reach this run's system prompt. The same isolation wiring's own
	// tests apply.
	t.Setenv("HOME", t.TempDir())
	return &scenario{
		t:               t,
		root:            t.TempDir(),
		store:           session.NewMemoryStore(),
		provider:        &scriptedProvider{turns: turns},
		stdinIsTerminal: true,
		interrupts:      make(chan os.Signal, 2),
	}
}

// outcome is what one invocation produced.
type outcomeOf struct {
	code   int
	stdout string
	stderr string
}

// run drives Main with the scenario's environment. The arguments are exactly what
// a shell would pass.
func (s *scenario) run(args ...string) outcomeOf {
	s.t.Helper()
	var stdout bytes.Buffer
	out := s.runWithStdout(&stdout, args...)
	out.stdout = stdout.String()
	return out
}

// runWithStdout is the same invocation writing to a caller's stdout, which is what
// the streaming test needs: a pipe it can read while the run is still going.
func (s *scenario) runWithStdout(stdout io.Writer, args ...string) outcomeOf {
	var stderr bytes.Buffer
	stdin := s.stdin
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	code := Main(Env{
		Args:            args,
		Stdin:           stdin,
		Stdout:          stdout,
		Stderr:          &stderr,
		Version:         "atenea test",
		StdinIsTerminal: s.stdinIsTerminal,
		Interrupts:      s.interrupts,
		Host:            s.assemble,
	})
	return outcomeOf{code: code, stderr: stderr.String()}
}

// readLine reads one NDJSON line, failing rather than hanging if the run never
// writes it.
func readLine(t *testing.T, r io.Reader) string {
	t.Helper()
	line := make([]byte, 0, 256)
	one := make([]byte, 1)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		n, err := r.Read(one)
		if err != nil {
			t.Fatalf("reading a streamed line: %v (read %q so far)", err, line)
		}
		if n == 0 {
			continue
		}
		if one[0] == '\n' {
			return string(line)
		}
		line = append(line, one[0])
	}
	t.Fatalf("no complete line arrived within the deadline (read %q)", line)
	return ""
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("draining the stream: %v", err)
	}
	return string(rest)
}

func unmarshal(line string, into any) error { return json.Unmarshal([]byte(line), into) }

// assemble is the test's host: the real host.New over a memory store and a
// scripted provider, with the two startup side effects off so nothing is written
// outside the scenario. The Config it was given is recorded first, because what the
// run asks for is part of the behaviour under test.
func (s *scenario) assemble(ctx context.Context, cfg host.Config) *host.Host {
	s.t.Helper()
	s.configs = append(s.configs, cfg)
	snapshot := llm.ProviderSnapshot{
		ProviderID:   "scripted",
		ProviderName: "Scripted",
		BaseURL:      "scripted://local",
		Model:        "scripted-model",
		Provider:     s.provider,
	}
	if s.offline {
		// What host.New falls back to with no key in the environment and no stored
		// credential. Only the id matters: it is what both UIs and the headless run
		// recognize as "there is no model here".
		snapshot.ProviderID = host.OfflineProviderID
		snapshot.ProviderName = "Demo"
		snapshot.BaseURL = "demo://local"
		snapshot.Model = "demo"
	}
	providers, err := providerconfig.Open(ctx, "", "", snapshot,
		func(string) string { return "" }, nil, nil, nil, nil)
	if err != nil {
		s.t.Fatalf("open the provider service: %v", err)
	}
	cfg.Dotenv = ""
	cfg.Store = s.store
	cfg.Providers = providers
	if cfg.Root == "" {
		cfg.Root = s.root
	}
	return host.New(ctx, cfg)
}

// scriptedProvider plays one script per turn instead of the same one on every
// call, which is what a multi-step turn needs: the step that calls a tool and the
// step that answers afterwards are two different streams.
//
// It records every Request, so a test can assert what history the model was given
// — the only way to check from outside that --session actually resumed something.
type scriptedProvider struct {
	mu       sync.Mutex
	turns    [][]llm.Event
	next     int
	requests []llm.Request
	// gate, when set, is closed by the provider before it streams anything and
	// waited on by it, so a test can hold a turn open (cancellation, streaming).
	opened chan struct{}
	block  chan struct{}
}

func (p *scriptedProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	// The discipline llm.FakeProvider follows, for the reason it documents: a fake
	// that silently swallows content it cannot render hides the failure from every
	// test written against it.
	for _, message := range req.Messages {
		if _, err := message.TextOnly(); err != nil {
			return nil, err
		}
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	var script []llm.Event
	if p.next < len(p.turns) {
		script = p.turns[p.next]
	} else {
		// Out of script: close the turn without asking for anything more, so a
		// runaway continuation loop shows up as a missing answer rather than as
		// MaxSteps worth of noise.
		script = []llm.Event{{Kind: llm.StepStarted}, {Kind: llm.StepEnded}}
	}
	p.next++
	opened, block := p.opened, p.block
	p.mu.Unlock()

	out := make(chan llm.Event)
	go func() {
		defer close(out)
		if opened != nil {
			select {
			case opened <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
		if block != nil {
			select {
			case <-block:
			case <-ctx.Done():
				return
			}
		}
		for _, ev := range script {
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out, nil
}

func (p *scriptedProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func (p *scriptedProvider) request(i int) llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[i]
}

// answer is the shortest complete turn: one text block and a clean close.
func answer(text string) []llm.Event {
	return []llm.Event{
		{Kind: llm.StepStarted},
		{Kind: llm.TextStarted},
		{Kind: llm.TextDelta, Text: text},
		{Kind: llm.TextEnded},
		{Kind: llm.StepEnded},
	}
}

// callTool is a turn that asks for one tool call and nothing else.
func callTool(callID, name, input string) []llm.Event {
	return []llm.Event{
		{Kind: llm.StepStarted},
		{Kind: llm.ToolCall, CallID: callID, ToolName: name, Input: json.RawMessage(input)},
		{Kind: llm.StepEnded},
	}
}

// events parses NDJSON back into the durable events, which is exactly what a
// consumer of --output-format stream-json does.
func events(t *testing.T, ndjson string) []session.SessionEvent {
	t.Helper()
	var parsed []session.SessionEvent
	for _, line := range strings.Split(strings.TrimSpace(ndjson), "\n") {
		if line == "" {
			continue
		}
		var ev session.SessionEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("stream-json line is not a SessionEvent: %v\nline: %s", err, line)
		}
		parsed = append(parsed, ev)
	}
	return parsed
}

func kinds(evs []session.SessionEvent) []session.EventKind {
	out := make([]session.EventKind, len(evs))
	for i, ev := range evs {
		out[i] = ev.Kind
	}
	return out
}

func contains(haystack []session.EventKind, needle session.EventKind) bool {
	for _, kind := range haystack {
		if kind == needle {
			return true
		}
	}
	return false
}

func decodeResult(t *testing.T, stdout string) resultDocument {
	t.Helper()
	var doc resultDocument
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil {
		t.Fatalf("--output-format json did not print one document: %v\nstdout: %s", err, stdout)
	}
	return doc
}

// invoke drives one configuration command through the real dispatch with no host
// at all: Env.Host fails the test if anything reaches it.
//
// That is an assertion, not setup. `atenea mcp list` and `atenea skill list` read
// files, and routing them through the bootstrap would hand them a provider they
// do not need — which is how `atenea mcp list` would come to refuse to run
// without an API key: through a shared setup helper, rather than through anybody
// deciding it should.
//
// stdin is an os.Pipe whose write end is never closed, so a command that read it
// would hang rather than pass. That is the shape of the deadlock R4.3 shipped in
// review, and these commands share its entrypoint.
func invoke(t *testing.T, args ...string) outcomeOf {
	t.Helper()
	stdin, hold, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		hold.Close()
	})
	var stdout, stderr bytes.Buffer
	code := Main(Env{
		Args:   args,
		Stdin:  stdin,
		Stdout: &stdout,
		Stderr: &stderr,
		Host: func(context.Context, host.Config) *host.Host {
			t.Errorf("%v assembled a host: a configuration command must not need a provider or a store", args)
			return host.New(context.Background(), host.Config{Store: session.NewMemoryStore()})
		},
	})
	return outcomeOf{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// isolateConfig points the user config directory and the home at empty
// directories of the test's own, so a developer's real ~/.config/atenea/mcp.json
// and installed skills cannot reach an assertion.
func isolateConfig(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skipf("XDG_CONFIG_HOME is not the UserConfigDir override on %s", runtime.GOOS)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

// TestMain_NoArgumentsStartsTheInteractiveInterface: the bare command keeps
// meaning what it has always meant. Adding subcommands must not make a user type
// one to get the terminal UI.
func TestMain_NoArgumentsStartsTheInteractiveInterface(t *testing.T) {
	started := false
	code := Main(Env{
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
		Interactive: func(InteractiveOptions) error { started = true; return nil },
	})
	if !started {
		t.Error("a bare `atenea` did not start the interactive interface")
	}
	if code != ExitOK {
		t.Errorf("exit code = %d, want %d", code, ExitOK)
	}
}

func TestMain_YoloAliasesAuthorizeOnlyInteractiveLaunch(t *testing.T) {
	for _, alias := range []string{"--yolo", "--dangerously-skip-permissions"} {
		t.Run(alias, func(t *testing.T) {
			var got InteractiveOptions
			code := Main(Env{Args: []string{alias}, Stdout: io.Discard, Stderr: io.Discard, Interactive: func(options InteractiveOptions) error { got = options; return nil }})
			if code != ExitOK || !got.Yolo {
				t.Fatalf("code=%d options=%+v", code, got)
			}
		})
	}
}

// TestMain_UnknownCommandIsAUsageError: before the dispatch, `atenea whatever`
// ignored the argument and opened the TUI. A typo that silently runs the wrong
// thing is the failure worth closing here.
func TestMain_UnknownCommandIsAUsageError(t *testing.T) {
	var stderr bytes.Buffer
	started := false
	code := Main(Env{
		Args:        []string{"rnu"},
		Stdout:      &bytes.Buffer{},
		Stderr:      &stderr,
		Interactive: func(InteractiveOptions) error { started = true; return nil },
	})
	if started {
		t.Error("an unknown command started the interactive interface")
	}
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), `unknown command "rnu"`) {
		t.Errorf("stderr does not name the unknown command:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "run") {
		t.Errorf("stderr does not list the commands that do exist:\n%s", stderr.String())
	}
}

// TestMain_VersionSubcommandAndFlagAgree: --version is what install.sh verifies an
// installation with, so it survives as an alias rather than as a second
// implementation that can drift.
func TestMain_VersionSubcommandAndFlagAgree(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-version"}} {
		var stdout bytes.Buffer
		code := Main(Env{Args: args, Stdout: &stdout, Stderr: &bytes.Buffer{}, Version: "atenea v9.9.9 (commit abc, built now)"})
		if code != ExitOK {
			t.Errorf("%v exit code = %d, want %d", args, code, ExitOK)
		}
		if got := stdout.String(); got != "atenea v9.9.9 (commit abc, built now)\n" {
			t.Errorf("%v printed %q", args, got)
		}
	}
}

// TestMain_HelpListsEverySubcommand: the help is generated from the dispatch table,
// so a subcommand cannot be added without appearing in it.
func TestMain_HelpListsEverySubcommand(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		var stdout bytes.Buffer
		code := Main(Env{Args: args, Stdout: &stdout, Stderr: &bytes.Buffer{}})
		if code != ExitOK {
			t.Errorf("%v exit code = %d, want %d", args, code, ExitOK)
		}
		for _, candidate := range commands {
			if !strings.Contains(stdout.String(), candidate.name) {
				t.Errorf("%v help does not list %q:\n%s", args, candidate.name, stdout.String())
			}
		}
	}
}

// TestMain_GroupWithoutAVerbIsAUsageError: `atenea mcp` names no operation, and
// picking one for the user would make the command mean something nobody typed. A
// group's mistakes get the same answer the top level's do — the error, the
// generated list, exit 2 — because they are the same dispatch.
func TestMain_GroupWithoutAVerbIsAUsageError(t *testing.T) {
	for _, group := range []struct {
		name  string
		verbs []command
	}{
		{"mcp", mcpCommands},
		{"skill", skillCommands},
	} {
		got := invoke(t, group.name)
		if got.code != ExitUsage {
			t.Errorf("`atenea %s` exit code = %d, want %d", group.name, got.code, ExitUsage)
		}
		if got.stdout != "" {
			t.Errorf("`atenea %s` wrote to stdout: %q", group.name, got.stdout)
		}
		for _, verb := range group.verbs {
			if !strings.Contains(got.stderr, verb.name) {
				t.Errorf("`atenea %s` help does not list %q:\n%s", group.name, verb.name, got.stderr)
			}
		}
	}
}

// TestMain_UnknownVerbIsAUsageError: the second level names the typo the way the
// first one does.
func TestMain_UnknownVerbIsAUsageError(t *testing.T) {
	got := invoke(t, "mcp", "lst")
	if got.code != ExitUsage {
		t.Errorf("exit code = %d, want %d", got.code, ExitUsage)
	}
	if !strings.Contains(got.stderr, `unknown command "lst"`) {
		t.Errorf("stderr does not name the unknown verb:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "list") {
		t.Errorf("stderr does not list the verbs that do exist:\n%s", got.stderr)
	}
}

// TestMain_GroupHelpGoesToStdout: asked for, the help is the output; volunteered
// after a mistake, it is a diagnostic. The top level splits them that way and so
// does the second.
func TestMain_GroupHelpGoesToStdout(t *testing.T) {
	for _, args := range [][]string{{"mcp", "-h"}, {"mcp", "--help"}, {"mcp", "help"}} {
		got := invoke(t, args...)
		if got.code != ExitOK {
			t.Errorf("%v exit code = %d, want %d", args, got.code, ExitOK)
		}
		for _, verb := range mcpCommands {
			if !strings.Contains(got.stdout, verb.name) {
				t.Errorf("%v help does not list %q on stdout:\n%s", args, verb.name, got.stdout)
			}
		}
	}
}

// TestMain_InteractiveUnavailableSaysSo: a build that wires no interactive
// interface reports it instead of exiting successfully having done nothing.
func TestMain_InteractiveUnavailableSaysSo(t *testing.T) {
	var stderr bytes.Buffer
	if code := Main(Env{Stdout: &bytes.Buffer{}, Stderr: &stderr}); code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "unavailable") {
		t.Errorf("stderr = %q, want it to say the interface is unavailable", stderr.String())
	}
}
