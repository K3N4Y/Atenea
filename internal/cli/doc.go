// Package cli is atenea's command-line surface: the subcommand dispatch, the
// non-interactive `atenea run`, and the two commands that configure what a run
// finds when it starts — `atenea mcp` and `atenea skill`.
//
// It is the programmatic way in. Everything a third party needs in order to drive
// the agent — a CI step, an editor plugin, another agent, a TTY-free end-to-end
// test — goes through here, and it is language-agnostic on purpose: the protocol is
// a process, its arguments, its streams and its exit code.
//
// A run needs a model provider and refuses to start without one. The
// configuration commands need nothing at all: they read and write the same files
// the desktop panel does, assemble no host, and start no server. That separation
// is worth keeping — the moment `atenea mcp list` needs an API key to print a
// config file, it has stopped being the command a fresh CI image can call.
//
// # What it is not
//
// It adds no agent behavior. The turn lifecycle is [github.com/K3N4Y/atenea/internal/agent].Service,
// the assembly is [github.com/K3N4Y/atenea/internal/wiring].Build, the bootstrap is
// [github.com/K3N4Y/atenea/internal/host].New, and the protocol is the durable
// session.SessionEvent stream that already exists. This package is argument
// parsing, one policy choice and a serializer. If something here starts deciding
// what a turn does, it is in the wrong package.
//
// A run is therefore the third caller of host.New, next to the desktop app
// (main.go) and the terminal interface (internal/tui/engine), and it composes the
// same pieces in the same order. See .okf/architecture/headless-cli.md for the
// surface an integrator reads, and .okf/architecture/composition-root.md for the
// two layers it assembles.
//
// # The two things it decides
//
// A headless host cannot ask, which is not a missing feature but a different
// question. The interactive hosts answer "is this call worth interrupting the user
// for"; this one answers "may this call run on nobody's authority", and the answer
// comes from what each tool declares about its own effects measured against a
// budget the operator stated up front. See permission.UnattendedPolicy and
// permissionmode.go.
//
// And an event has to become a byte. The durable stream is the protocol as it
// stands, so `--output-format stream-json` serializes it and invents nothing: no
// second taxonomy, no envelope, no renaming. The other two formats are folds of the
// same stream.
//
// # Testability
//
// Nothing reads os.Args, os.Stdout, os.Stdin or installs a signal handler outside
// [Env]. [Main] returns the exit code instead of calling os.Exit. That is what lets
// the tests drive the real dispatch over the real assembly with a scripted
// provider, and assert the bytes a consumer would parse.
package cli
