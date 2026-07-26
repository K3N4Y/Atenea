package cli

// The exit codes are part of the surface: a CI step, a Makefile or an editor
// plugin branches on them, so each one names a distinct thing a caller can react
// to differently and none of them is reused for two.
//
// They are small integers rather than the 128+signal convention for the
// cancellation case (130 for SIGINT, 143 for SIGTERM). atenea *handles* the
// signal — it stops the turn, flushes the stream and returns — instead of dying
// from it, and a consumer of this surface wants to know "the run was interrupted",
// which is one fact, rather than which of two signals delivered it.
const (
	// ExitOK: the command did what it was asked. For a run, the turn finished and
	// every tool call the model made was permitted.
	ExitOK = 0

	// ExitFailure: the command failed at the thing it exists to do. For `run` that
	// is the turn — the provider errored, the stream broke, the store refused a
	// write. For `mcp add` it is a server that could not be declared, and for
	// `skill validate` a skill that will not load. It is 1 because it is the generic
	// failure, which is what a shell expects from a plain `if ! atenea run ...` and
	// what a linter returns when it has findings.
	//
	// The name is no longer `ExitTurnFailed`: 1 was never turn-specific — the interactive
	// interface failing to start has always returned it — and R4.4's subcommands do
	// not run turns at all. Splitting it per command was the alternative and it
	// buys nothing: a second failure code has to name an outcome a caller reacts to
	// differently, and the two that qualify are already carved out as "the
	// invocation was wrong" (2) and "the environment is wrong" (5). A validation
	// failure is neither. It is the command answering no.
	ExitFailure = 1

	// ExitUsage: the invocation is wrong — an unknown subcommand or flag, an
	// unparseable value, no prompt, a --cwd that is not a directory. 2 is the Unix
	// convention for a usage error and is what the stdlib flag package already
	// exits with on its own, so the two agree instead of the dispatch having to
	// intercept the package's exits to renumber them.
	ExitUsage = 2

	// ExitPermissionDenied: the turn itself completed, but at least one tool call
	// was refused by the permission mode in force. It is its own code because it
	// is the one outcome a caller fixes by changing the invocation rather than the
	// prompt: the agent tried to do something this run was not allowed to do, and
	// widening the budget (or reviewing what it wanted) is the action. Folding it
	// into success would hide it, and folding it into failure would misreport a
	// turn that ran to completion and reported honestly.
	ExitPermissionDenied = 3

	// ExitCanceled: SIGINT or SIGTERM arrived and the run was stopped. It is
	// distinguishable from every other code on purpose — a build cancelled by its
	// operator is not a build that failed.
	ExitCanceled = 4

	// ExitStartup: the run never got as far as a turn. No model provider is
	// configured, prompt admission failed, or stdin could not be read. It is
	// separate from ExitFailure because the two point at different things: this
	// one says the environment is wrong (no credential, a database that will not
	// accept a write, a broken pipe), while a turn failure says the conversation
	// went badly. Retrying is reasonable for one and not for the other.
	ExitStartup = 5
)

// The statuses the closing document reports. There is exactly one per code, and
// outcome below is the only place that decides either, so the number a script
// branches on and the string a log shows cannot disagree.
const (
	statusOK               = "ok"
	statusTurnFailed       = "turn_failed"
	statusPermissionDenied = "permission_denied"
	statusCanceled         = "canceled"
)

// outcome resolves what the run observed into the one status and code that report
// it. The precedence is the order of the checks and is part of the contract: the
// fact that most changes what a caller should do wins.
//
// Cancellation outranks everything the run produced, because an interrupted run
// makes no claim at all about the conversation — the turn was cut mid-flight, and
// reporting its half-finished state as a failure would be a guess. A turn failure
// outranks a denial for the reverse reason: a denial is a report the run made
// deliberately, and a broken turn means there is no report to trust.
func outcome(res result) (status string, code int) {
	switch {
	case res.Canceled:
		return statusCanceled, ExitCanceled
	case res.Error != "":
		return statusTurnFailed, ExitFailure
	case res.DeniedToolCalls > 0:
		return statusPermissionDenied, ExitPermissionDenied
	default:
		return statusOK, ExitOK
	}
}
