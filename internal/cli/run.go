package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/K3N4Y/atenea/internal/agent"
	composer "github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/event"
	"github.com/K3N4Y/atenea/internal/host"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/mcpclient"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/wiring"
)

// result is what the run itself observed, as opposed to what the stream said: the
// session it ran under, whether a signal stopped it, how many calls the permission
// mode refused, and the failure the run handle reported.
type result struct {
	SessionID       string
	Canceled        bool
	DeniedToolCalls int
	Error           string
}

// runCommand is `atenea run`: one non-interactive turn over the same agent both
// UIs drive.
func runCommand(env Env, args []string) int {
	fs := flags(env, "atenea run", runUsage)

	var (
		prompt       = ""
		readStdin    = false
		format       = formatText
		mode         = modeDeny
		allowEffects = ""
		sessionID    = ""
		cwd          = ""
	)
	fs.StringVar(&prompt, "p", "", "the prompt to run; with no -p the prompt is read from stdin")
	fs.StringVar(&prompt, "prompt", "", "the prompt to run; with no -p the prompt is read from stdin")
	fs.BoolVar(&readStdin, "stdin", false,
		"also read stdin and append it to -p, for `git diff | atenea run -p \"review this\" --stdin`")
	fs.StringVar(&format, "output-format", formatText,
		"how to report the run: "+strings.Join(outputFormats, " | "))
	fs.StringVar(&mode, "permission-mode", modeDeny,
		"what a gated tool call may do with nobody to ask: "+strings.Join(permissionModes, " | "))
	fs.StringVar(&allowEffects, "allow-effects", "",
		"with --permission-mode allowlist, the effects a tool may declare and still run: "+
			strings.Join(effectNames(), ", "))
	fs.StringVar(&sessionID, "session", "",
		"run under this session id, continuing it when the shared store already has it")
	fs.StringVar(&cwd, "cwd", "", "the workspace root the agent is anchored to (default: the working directory)")

	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "atenea run: unexpected argument %q; the prompt is given with -p or on stdin\n", fs.Arg(0))
		return ExitUsage
	}

	permissions, err := resolvePermissionMode(mode, allowEffects)
	if err != nil {
		fmt.Fprintln(env.Stderr, "atenea run:", err)
		return ExitUsage
	}
	if !validFormat(format) {
		fmt.Fprintf(env.Stderr, "atenea run: unknown output format %q; valid formats are %s\n",
			format, strings.Join(outputFormats, ", "))
		return ExitUsage
	}
	root, err := resolveRoot(cwd)
	if err != nil {
		fmt.Fprintln(env.Stderr, "atenea run:", err)
		return ExitUsage
	}
	text, err := resolvePrompt(prompt, readStdin, env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "atenea run:", err)
		if errors.Is(err, errStdinUnreadable) {
			return ExitStartup
		}
		return ExitUsage
	}

	return execute(env, turn{
		prompt:      text,
		format:      format,
		permissions: permissions,
		sessionID:   sessionID,
		root:        root,
	})
}

// turn is a resolved invocation: everything decided from the arguments, with
// nothing left to parse and nothing yet touched on disk.
type turn struct {
	prompt      string
	format      string
	permissions permissionMode
	sessionID   string
	root        string
}

// execute assembles the agent and runs one turn. It is the third caller of
// host.New, next to the desktop app and the terminal UI, and it adds no turn
// behavior of its own: the lifecycle is agent.Service's, the assembly is
// wiring.Build's, and what is written here is the boundary between a durable event
// and a byte on stdout.
func execute(env Env, t turn) int {
	interrupts, stopSignals := env.interrupts()
	defer stopSignals()

	ctx := context.Background()
	// The same bootstrap both UIs perform. In a release build the .env is compiled
	// out, so it only ever loads during development.
	h := env.assemble(ctx, host.Config{
		Identity: env.Identity,
		Root:     t.root,
		Dotenv:   ".env",
	})
	defer func() { _ = h.Close() }()

	if active := h.Providers.Active(); active.ProviderID == host.OfflineProviderID {
		// With no credential anywhere the host lands on the offline demo, whose
		// replies are canned. Interactively that is right: a person sees the notice
		// and runs /connect. Here there is nobody to read a warning, and a run that
		// answered from the fake would exit 0 with a fabricated answer on stdout —
		// so a job whose key merely expired would look exactly like one that worked.
		// This codebase has refused that trade three times on the record (R2, R3.5,
		// R3.6); refusing to start is the only honest answer without a user.
		fmt.Fprintln(env.Stderr, "atenea run: no model provider is configured, so there is nothing "+
			"to answer this prompt.")
		fmt.Fprintf(env.Stderr, "  Export one of %s, or run `atenea` and use /connect.\n",
			strings.Join(providerKeyNames(), ", "))
		return ExitStartup
	}
	if t.permissions.warning != "" {
		warn(env.Stderr, t.permissions.warning)
	}

	// The output boundary. The stream exists before the assembly because the store
	// the runner writes to is decorated with the bus that feeds it; the sink is
	// attached after, because the text format asks the catalog how a call reads.
	out := &stream{}
	bus := event.NewBus(func(_ string, data ...interface{}) {
		if len(data) == 0 {
			return
		}
		if ev, ok := data[0].(session.SessionEvent); ok {
			out.observe(ev)
		}
	})
	store := event.NewEmittingStore(h.Store, bus)
	mcp := mcpclient.NewManagerWithRuntime(h.Root, h.Identity, h.Providers.Provider(), func() string { return h.Providers.Active().Model })
	defer mcp.Close()
	configs, err := mcpclient.LoadConfig(h.Root)
	if err != nil {
		fmt.Fprintln(env.Stderr, "atenea run: load MCP configuration:", err)
		return ExitStartup
	}
	for _, config := range configs {
		if !config.AutoConnect {
			continue
		}
		if _, err := mcp.Connect(ctx, config); err != nil {
			fmt.Fprintln(env.Stderr, "atenea run: auto-connect:", err)
			return ExitStartup
		}
	}

	refused := &denials{}
	built := wiring.Build(wiring.Config{
		Root:     h.Root,
		Provider: h.Providers.Provider(),
		Store:    store,
		Inbox:    h.Inbox,
		// The gate of a host with nobody to ask. It is not nil, because the runner
		// consults its policy only when a gate is present, so a nil gate would settle
		// every call whatever the mode decided. See permission.UnattendedGate.
		Gate: permission.UnattendedGate{},
		// No session grants: a grant is an answer a user gave, and there is no user.
		// wiring leaves the classification untouched for a nil store, which is the
		// honest description of a run that cannot be asked anything.
		Grants: nil,
		Snaps:  h.Snapshots,
		Bus:    bus,
		LocalPrompt: func() bool {
			return h.Providers.Active().LocalModels
		},
		RoleProvider: func(ctx context.Context, def agent.Def) (llm.Provider, error) {
			if def.Model == "" {
				return nil, nil
			}
			return h.Providers.ResolveModel(ctx, def.Model)
		},
		NextID:           wiring.NewIDGen(),
		Mode:             h.Agent.Mode,
		Policy:           refused.over(t.permissions.policy),
		MCPTools:         mcp.Tools(),
		PersistentGrants: mcp.PermissionRules(),
		LSP:              true,
		EditSettings:     h.Providers.EditSettings,
	})
	defer built.Close()
	commands := append(built.Commands.List(), mcp.Commands()...)
	h.Agent.Configure(built.Runner, composer.New(commands, mcp.Mentions()...))
	out.attach(newSink(t.format, env.Stdout, env.Stderr, built.Tools))

	sessionID := t.sessionID
	if sessionID == "" {
		sessionID = newSessionID()
	}
	res := result{SessionID: sessionID}

	// The completion hook runs on the service's goroutine. On the normal path the
	// closing of the run handle orders it before the read below, but the second-signal
	// path deliberately stops waiting for that handle — so the failure is passed
	// under a lock rather than relying on an ordering the interrupted path does not
	// provide.
	var (
		turnMu  sync.Mutex
		turnErr error
	)
	handle, err := h.Agent.Send(sessionID, session.Prompt{Text: t.prompt}, agent.Hooks{
		BeforeAdmit: func() error { return recordWorkspace(ctx, store, sessionID, h.Root) },
		AfterRun: func(r agent.RunResult) {
			turnMu.Lock()
			defer turnMu.Unlock()
			turnErr = r.Err
		},
	})
	if err != nil {
		// The turn never started: the prompt was not admitted. That is the
		// environment being wrong rather than the conversation going badly, which is
		// why it is not ExitFailure.
		fmt.Fprintln(env.Stderr, "atenea run:", err)
		return ExitStartup
	}

	res.Canceled = wait(handle, interrupts, h.Agent, sessionID)
	res.DeniedToolCalls = refused.count()
	turnMu.Lock()
	if turnErr != nil {
		res.Error = turnErr.Error()
	}
	turnMu.Unlock()

	doc, closeErr := out.close(res)
	if closeErr != nil {
		// The run happened; only its delivery failed. Its real outcome is still what a
		// caller wants from the exit code, so the write failure is reported and the
		// code is not overwritten with one that would misdescribe the run. (Go lets
		// SIGPIPE kill the process on a closed stdout, so this is mostly a full disk.)
		fmt.Fprintln(env.Stderr, "atenea run: could not write the result:", closeErr)
	}
	return doc.ExitCode
}

// stopper is the part of agent.Service a finished run needs: cancel the run of
// this session. Naming it keeps wait testable without a whole service.
type stopper interface {
	Stop(sessionID string) (agent.RunHandle, bool)
}

// wait blocks until the turn finishes or a signal arrives, and reports whether it
// was interrupted.
//
// The first signal stops the run and waits for it: the runner unwinds on a
// cancelled context — it fails the tool calls still in flight and closes the turn —
// so the events already written stay consistent and the stream ends where the run
// did. A second signal abandons that wait, because an operator pressing Ctrl-C
// twice is asking to stop now, and the store is durable either way.
func wait(handle agent.RunHandle, interrupts <-chan os.Signal, runs stopper, sessionID string) bool {
	select {
	case <-handle.Done():
		return false
	case <-interrupts:
		runs.Stop(sessionID)
		select {
		case <-handle.Done():
		case <-interrupts:
		}
		return true
	}
}

// recordWorkspace stamps the session with the workspace it ran in, once, on its
// first event — the same thing both UIs do before admitting a prompt, and what
// makes a headless session show up in the desktop sidebar grouped under the right
// folder instead of an unknown one.
func recordWorkspace(ctx context.Context, store session.Store, sessionID, root string) error {
	if _, err := store.LoadSession(ctx, sessionID); err == nil {
		return nil
	}
	_, err := store.AppendEvent(ctx, sessionID, session.SessionEvent{
		Kind: session.KindSessionCwd,
		Text: root,
	})
	return err
}

// newSessionID mints the id of a run that was not given one. The prefix says which
// host created the session, following the terminal UI's `tui-` — a listing that
// wants only its own sessions filters on it, and everything that reads the shared
// store sees them all.
func newSessionID() string {
	return "cli-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func validFormat(format string) bool {
	for _, candidate := range outputFormats {
		if format == candidate {
			return true
		}
	}
	return false
}

// resolveRoot turns --cwd into the workspace root. An empty value leaves it to the
// host, which resolves the process working directory.
//
// A path that is not a directory is refused here rather than left to fail later,
// because everything downstream degrades quietly instead: skills are discovered
// under a root that does not exist, the file tools resolve against it, and the run
// would produce a plausible answer to a question about nothing.
func resolveRoot(cwd string) (string, error) {
	if cwd == "" {
		return "", nil
	}
	root, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("--cwd %q: %w", cwd, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("--cwd %q: %w", cwd, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("--cwd %q is not a directory", cwd)
	}
	return root, nil
}

// errStdinUnreadable separates "the pipe broke" from "you did not give me a
// prompt". They exit differently: one is the environment failing, the other is the
// invocation being wrong.
var errStdinUnreadable = errors.New("could not read the prompt from stdin")

// resolvePrompt composes the prompt from the two places it can come from, under
// one rule: **stdin is read only when the caller asked for it**, either by giving
// no -p (so stdin is the only source there is) or by saying --stdin out loud.
//
// The rule exists because "not a terminal" is not the same as "will reach EOF".
// Reading stdin whenever it is not a terminal looks equivalent and hangs forever
// on an open pipe nobody closes — which is the *normal* state of stdin under a CI
// runner, `ssh` without -n, `docker run -i`, or a wrapper script. Worst of all it
// deadlocks the case R4.3 exists for: an editor plugin spawning the process with
// stdin=PIPE and stdout=PIPE waits for the answer while atenea waits for the EOF
// the plugin will only send after it has one. A prompt given with -p is complete
// on its own, so nothing justifies making it depend on the other end of a pipe.
//
// Composition survives, spelled out: `git diff | atenea run -p "review this diff"
// --stdin` puts -p first and the piped text after a blank line. Both parts are
// kept, because dropping one is the worst kind of failure — the run succeeds,
// answering a question the caller did not ask, with nothing anywhere saying that
// half the input was discarded.
//
// A terminal on stdin is never read, whoever asked. With no -p that is a usage
// error and not a wait; with --stdin it is a usage error too, since there is
// nothing on the other end to wait for. A job that blocks forever on an input
// nobody is going to type is the one outcome a pipeline cannot recover from.
func resolvePrompt(flagPrompt string, readStdin bool, env Env) (string, error) {
	given := strings.TrimSpace(flagPrompt)
	// With no -p, stdin is the only source the invocation has left, so reading it
	// is the only thing the caller can have meant — the contract every filter has.
	wantStdin := readStdin || given == ""

	if wantStdin && env.StdinIsTerminal {
		if readStdin {
			return "", errors.New("--stdin: stdin is a terminal, so there is nothing to read from it")
		}
		return "", errors.New("no prompt: pass -p PROMPT, or pipe one in on stdin")
	}

	parts := make([]string, 0, 2)
	if given != "" {
		parts = append(parts, given)
	}
	if wantStdin && env.Stdin != nil {
		piped, err := io.ReadAll(env.Stdin)
		if err != nil {
			return "", fmt.Errorf("%w: %v", errStdinUnreadable, err)
		}
		if text := strings.TrimSpace(string(piped)); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return "", errors.New("no prompt: stdin was empty")
	}
	return strings.Join(parts, "\n\n"), nil
}

// warn writes a line a person has to see whatever the output format is, which is
// why it goes to stderr: stdout belongs to the format, and a warning that
// corrupted a consumer's NDJSON would be a worse problem than the one it reports.
func warn(w io.Writer, message string) {
	fmt.Fprintln(w, "! "+message)
}

// providerKeyNames lists the environment variables that would give this run a
// model, read off the shipped catalog rather than typed out here — so a provider
// added to the catalog appears in the advice on the same commit, and one removed
// stops being suggested.
func providerKeyNames() []string {
	seen := map[string]bool{}
	names := make([]string, 0, 4)
	for _, provider := range providerconfig.DefaultCatalog().Providers {
		if provider.APIKeyEnv == "" || seen[provider.APIKeyEnv] {
			continue
		}
		seen[provider.APIKeyEnv] = true
		names = append(names, provider.APIKeyEnv)
	}
	return names
}

const runUsage = `atenea run — run one non-interactive turn and report the result.

Usage:
  atenea run -p PROMPT [flags]
  ... | atenea run [flags]
  ... | atenea run -p PROMPT --stdin [flags]

The prompt comes from -p, or from stdin when there is no -p, or from both when
--stdin says so (-p first, stdin after a blank line). stdin is never read unless
one of those asked for it, so -p alone cannot wait on a pipe nobody closes. A
terminal on stdin is a usage error rather than a wait.

Exit codes:
  0  the turn finished and every tool call was permitted
  1  the turn failed
  2  the invocation was wrong
  3  the turn finished but a tool call was refused by the permission mode
  4  a signal stopped the run
  5  the run never started

Flags:
`
