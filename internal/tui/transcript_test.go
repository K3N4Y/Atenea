package tui

import (
	"strings"
	"testing"
	"time"

	"atenea/internal/session"
)

// These tests exercise the Transcript module directly through its own
// interface: they fold events into a zero-value Transcript and assert on the
// returned Transcript state (entries, usage, reveal cursor, gating), never on a
// rendered View() string. This is the payoff of carving the module out — the
// fold/reveal/usage/gating contract is now pinned on the value it produces, not
// on the whole Update/View loop.

const testSession = "s1"

// fold is a small helper: fold one event into the transcript for the test
// session. Keeps the call sites terse.
func fold(t Transcript, ev EventMsg) Transcript {
	return t.foldEvent(ev, testSession)
}

func TestTranscript_FoldsUserPromptWithoutKind(t *testing.T) {
	var tr Transcript
	tr = fold(tr, EventMsg{Message: &session.Message{Role: session.RoleUser, Text: "hello"}})
	if len(tr.entries) != 1 {
		t.Fatalf("entries = %d, want 1 user entry", len(tr.entries))
	}
	if got := tr.entries[0]; got.kind != entryUser || got.text != "hello" {
		t.Fatalf("entry = %+v, want a user entry with text %q", got, "hello")
	}
}

func TestTranscript_StreamsAssistantTextIntoOneLiveBlock(t *testing.T) {
	var tr Transcript
	tr = fold(tr, EventMsg{Kind: session.KindTextStarted})
	tr = fold(tr, EventMsg{Kind: session.KindTextDelta, Text: "Hi "})
	tr = fold(tr, EventMsg{Kind: session.KindTextDelta, Text: "there"})
	if len(tr.entries) != 1 {
		t.Fatalf("entries = %d, want a single assistant block", len(tr.entries))
	}
	e := tr.entries[0]
	if e.kind != entryAssistant || !e.live || e.text != "Hi there" {
		t.Fatalf("assistant block = %+v, want live with accumulated text %q", e, "Hi there")
	}
	if !tr.assistantOpen() {
		t.Fatal("assistantOpen() = false, want true while the block streams")
	}
	tr = fold(tr, EventMsg{Kind: session.KindStepEnded})
	if tr.entries[0].live {
		t.Fatal("Step.Ended left the assistant block live, want it closed")
	}
	if tr.assistantOpen() {
		t.Fatal("assistantOpen() = true after Step.Ended, want false")
	}
}

func TestTranscript_TextDeltaOpensAssistantBlockDefensively(t *testing.T) {
	var tr Transcript
	// A delta arriving without Text.Started must still open the block.
	tr = fold(tr, EventMsg{Kind: session.KindTextDelta, Text: "x"})
	if len(tr.entries) != 1 || tr.entries[0].kind != entryAssistant || tr.entries[0].text != "x" {
		t.Fatalf("entries = %+v, want a defensively-opened assistant block", tr.entries)
	}
}

func TestTranscript_ReasoningBlockClosesWithDuration(t *testing.T) {
	var tr Transcript
	tr = fold(tr, EventMsg{Kind: session.KindReasoningStarted})
	tr = fold(tr, EventMsg{Kind: session.KindReasoningDelta, Text: "thinking"})
	if !tr.reasoningOpen() {
		t.Fatal("reasoningOpen() = false while thought streams, want true")
	}
	tr = fold(tr, EventMsg{Kind: session.KindReasoningEnded, Text: "thinking"})
	e := tr.entries[0]
	if e.kind != entryReasoning || e.live {
		t.Fatalf("thought block = %+v, want closed reasoning block", e)
	}
	if tr.reasoningOpen() {
		t.Fatal("reasoningOpen() = true after Reasoning.Ended, want false")
	}
}

func TestTranscript_StepEndedClosesLingeringThought(t *testing.T) {
	var tr Transcript
	// A step may die thinking without a Reasoning.Ended; Step.Ended must close it.
	tr = fold(tr, EventMsg{Kind: session.KindReasoningStarted})
	tr = fold(tr, EventMsg{Kind: session.KindReasoningDelta, Text: "hmm"})
	tr = fold(tr, EventMsg{Kind: session.KindStepEnded})
	if tr.entries[0].live {
		t.Fatal("Step.Ended left the thought live, want the defensive close")
	}
}

func TestTranscript_ToolCallSettlesOnSuccess(t *testing.T) {
	var tr Transcript
	tr = fold(tr, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: []byte(`{"cmd":"ls"}`)})
	if tr.entries[0].status != toolRunning {
		t.Fatalf("status = %v, want toolRunning after Tool.Called", tr.entries[0].status)
	}
	tr = fold(tr, EventMsg{Kind: session.KindToolSuccess, CallID: "c1", Text: "file.txt"})
	e := tr.entries[0]
	if e.status != toolOK || e.output != "file.txt" {
		t.Fatalf("settled tool = %+v, want toolOK with output preserved", e)
	}
}

func TestTranscript_ToolCallSettlesOnFailure(t *testing.T) {
	var tr Transcript
	tr = fold(tr, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash"})
	tr = fold(tr, EventMsg{Kind: session.KindToolFailed, CallID: "c1", Error: "boom"})
	e := tr.entries[0]
	if e.status != toolFailed || e.err != "boom" {
		t.Fatalf("settled tool = %+v, want toolFailed carrying the error", e)
	}
}

func TestTranscript_PresentPlanSuccessAppendsPlanApproval(t *testing.T) {
	var tr Transcript
	tr = fold(tr, EventMsg{Kind: session.KindToolCalled, CallID: "p1", ToolName: "present_plan"})
	if tr.hasPendingPlan() {
		t.Fatal("hasPendingPlan() = true before present_plan settled, want false")
	}
	tr = fold(tr, EventMsg{Kind: session.KindToolSuccess, CallID: "p1"})
	if !tr.hasPendingPlan() {
		t.Fatal("hasPendingPlan() = false after present_plan succeeded, want true")
	}
	tr = tr.removePendingPlan()
	if tr.hasPendingPlan() {
		t.Fatal("hasPendingPlan() = true after removePendingPlan(), want false")
	}
}

func TestTranscript_StepFailedAppendsErrorAndClosesLiveUsage(t *testing.T) {
	var tr Transcript
	tr = fold(tr, EventMsg{Kind: session.KindStepStarted, Usage: &session.Usage{InputTokens: 10}})
	if !tr.liveUsage {
		t.Fatal("liveUsage = false after Step.Started with usage, want true")
	}
	tr = fold(tr, EventMsg{Kind: session.KindStepFailed, Error: "kaboom"})
	if tr.liveUsage {
		t.Fatal("liveUsage = true after Step.Failed, want it closed")
	}
	last := tr.entries[len(tr.entries)-1]
	if last.kind != entryError || last.text != "kaboom" {
		t.Fatalf("last entry = %+v, want an error block carrying the message", last)
	}
}

// Permission gating: pendingPermission + the gate that hides a running tool
// header while its permission is pending.
func TestTranscript_PendingPermissionAndGate(t *testing.T) {
	var tr Transcript
	tr = fold(tr, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", SessionID: "child"})
	tr = fold(tr, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", SessionID: "child"})

	perm, ok := tr.pendingPermission()
	if !ok || perm.callID != "c1" || perm.sessionID != "child" {
		t.Fatalf("pendingPermission() = %+v,%v, want the pending entry with call/session ids", perm, ok)
	}

	// The running tool header for the same call+session must be gated out of the
	// visible projection while the permission is pending.
	gated := tr.permissionGatedTools()
	running := tr.entries[0]
	if running.kind != entryTool || !toolGatedByPermission(running, gated) {
		t.Fatalf("running tool = %+v gated=%v, want it hidden while permission pending", running, toolGatedByPermission(running, gated))
	}
	if got := len(tr.visibleEntries()); got != 1 {
		t.Fatalf("visibleEntries() = %d, want only the permission ask visible", got)
	}
}

func TestTranscript_PermissionInputBackfillsFromToolCall(t *testing.T) {
	var tr Transcript
	tr = fold(tr, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", Input: []byte(`{"cmd":"ls"}`), SessionID: "child"})
	tr = fold(tr, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", SessionID: "child"})
	perm, _ := tr.pendingPermission()
	if perm.input != `{"cmd":"ls"}` {
		t.Fatalf("permission input = %q, want it backfilled from the Tool.Called input", perm.input)
	}
}

func TestTranscript_ApplyPermissionDecisionApprovedRevealsRunningTool(t *testing.T) {
	var tr Transcript
	tr = fold(tr, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", SessionID: "child"})
	tr = fold(tr, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", SessionID: "child"})
	perm, _ := tr.pendingPermission()
	tr = tr.applyPermissionDecision(perm, true)

	if _, ok := tr.pendingPermission(); ok {
		t.Fatal("pendingPermission() still true after approval, want it cleared")
	}
	// Approval leaves the running tool untouched (it proceeds); the gate is gone
	// because there is no pending permission, so the tool becomes visible.
	if got := len(tr.visibleEntries()); got != 1 {
		t.Fatalf("visibleEntries() = %d, want the running tool now visible", got)
	}
	if tr.entries[0].status != toolRunning {
		t.Fatalf("tool status = %v after approval, want it still running", tr.entries[0].status)
	}
}

func TestTranscript_ApplyPermissionDecisionDeniedSettlesTool(t *testing.T) {
	var tr Transcript
	tr = fold(tr, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", SessionID: "child"})
	tr = fold(tr, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", SessionID: "child"})
	perm, _ := tr.pendingPermission()
	tr = tr.applyPermissionDecision(perm, false)

	if _, ok := tr.pendingPermission(); ok {
		t.Fatal("pendingPermission() still true after denial, want it cleared")
	}
	e := tr.entries[0]
	if e.status != toolDenied || e.err != "Denied by user" {
		t.Fatalf("denied tool = %+v, want toolDenied with the neutral reason", e)
	}
}

// Live usage estimation: StepStarted resets counters, deltas accumulate bytes,
// updateLiveUsage projects them to an estimate, StepEnded fixes exact usage.
func TestTranscript_LiveUsageEstimatesFromStreamedBytes(t *testing.T) {
	var tr Transcript
	tr.outputBytes, tr.reasoningBytes, tr.toolInputBytes = 9, 12, 15
	tr = fold(tr, EventMsg{Kind: session.KindStepStarted, Usage: &session.Usage{InputTokens: 20}})
	if !tr.liveUsage || tr.outputBytes != 0 || tr.reasoningBytes != 0 || tr.toolInputBytes != 0 {
		t.Fatalf("after Step.Started: live=%v bytes=%d/%d/%d, want live with counters reset",
			tr.liveUsage, tr.outputBytes, tr.reasoningBytes, tr.toolInputBytes)
	}

	tr = fold(tr, EventMsg{Kind: session.KindTextDelta, Text: "abcdef"}) // 6 bytes -> estimatedTokens(6)=2
	if tr.usage == nil || tr.usage.OutputTokens != estimatedTokens(6) {
		t.Fatalf("output estimate = %v, want %d after a 6-byte delta", tr.usage, estimatedTokens(6))
	}

	estimated := *tr.usage
	tr = fold(tr, EventMsg{Kind: session.KindStepEnded})
	if tr.liveUsage || *tr.usage != estimated {
		t.Fatalf("after Step.Ended (no usage): live=%v usage=%+v, want the estimate frozen and live off",
			tr.liveUsage, *tr.usage)
	}
}

func TestTranscript_UpdateLiveUsageIsNoOpWithoutActiveUsage(t *testing.T) {
	for _, tr := range []Transcript{
		{liveUsage: false, usage: &session.Usage{OutputTokens: 7}, outputBytes: 30},
		{liveUsage: true, usage: nil, outputBytes: 30},
	} {
		before := tr.usage
		got := tr.updateLiveUsage()
		if got.usage != before || got.outputBytes != 30 {
			t.Fatalf("updateLiveUsage() mutated an inactive-usage transcript: %+v", got)
		}
	}
}

func TestEstimatedTokensRounding(t *testing.T) {
	for _, tc := range []struct{ bytes, want int }{{0, 0}, {1, 1}, {2, 1}, {3, 1}, {30_000, 10_000}} {
		if got := estimatedTokens(tc.bytes); got != tc.want {
			t.Errorf("estimatedTokens(%d) = %d, want %d", tc.bytes, got, tc.want)
		}
	}
}

// Smooth reveal: coalesced fills reveal at once, streamed deltas leave backlog
// that advanceReveal drains rune by rune.
func TestTranscript_CoalescedTextRevealsAtOnce(t *testing.T) {
	var tr Transcript
	// Step.Ended with a coalesced Message and no prior streamed delta: the block
	// fills complete and is fully revealed (no backlog).
	tr = fold(tr, EventMsg{Kind: session.KindTextStarted})
	tr = fold(tr, EventMsg{Kind: session.KindStepEnded, Message: &session.Message{Text: "coalesced"}})
	if tr.hasBacklog() {
		t.Fatal("hasBacklog() = true for coalesced text, want it revealed at once")
	}
	if !tr.entries[0].settled() {
		t.Fatal("coalesced assistant block not settled, want settled")
	}
}

func TestTranscript_StreamedTextLeavesBacklogThatAdvanceRevealDrains(t *testing.T) {
	var tr Transcript
	tr = fold(tr, EventMsg{Kind: session.KindTextStarted})
	tr = fold(tr, EventMsg{Kind: session.KindTextDelta, Text: strings.Repeat("x", 100)})
	if !tr.hasBacklog() {
		t.Fatal("hasBacklog() = false right after a streamed delta, want backlog")
	}
	// Reveal advances but does not finish in one tick for a 100-rune backlog.
	tr = tr.advanceReveal()
	if tr.entries[0].revealed == 0 {
		t.Fatal("advanceReveal() revealed nothing, want it to advance")
	}
	if tr.entries[0].revealed >= 100 {
		t.Fatalf("advanceReveal() revealed %d of 100 in one tick, want a partial step", tr.entries[0].revealed)
	}
	// Drain to completion.
	for tr.hasBacklog() {
		tr = tr.advanceReveal()
	}
	if tr.entries[0].revealed != 100 {
		t.Fatalf("revealed = %d after draining, want 100", tr.entries[0].revealed)
	}
}

func TestRevealStepBounds(t *testing.T) {
	if got := revealStep(0); got != 0 {
		t.Fatalf("revealStep(0) = %d, want 0", got)
	}
	if got := revealStep(3); got != 3 {
		t.Fatalf("revealStep(3) = %d, want bounded to remaining", got)
	}
	// A large backlog drains in at most revealCatchUpFrames ticks.
	big := 800
	if got := revealStep(big); got < (big+revealCatchUpFrames-1)/revealCatchUpFrames {
		t.Fatalf("revealStep(%d) = %d, want at least the catch-up share", big, got)
	}
	// The base pace floors small-but-nonzero catch-up backlogs.
	if got := revealStep(revealBaseRunes + 1); got < revealBaseRunes {
		t.Fatalf("revealStep never dips below the base pace, got %d", got)
	}
}

// toggleThinking flips only settled thought blocks; live/backlogged ones and
// non-reasoning entries are inert.
func TestTranscript_ToggleThinkingFlipsOnlySettledThoughts(t *testing.T) {
	var tr Transcript
	// A settled thought block plus a live one and a user entry.
	tr.entries = []entry{
		{kind: entryReasoning, text: "done", revealed: 4, duration: time.Second},
		{kind: entryUser, text: "hi"},
		{kind: entryReasoning, text: "live", live: true},
	}
	if !tr.entries[0].settled() {
		t.Fatal("first thought not settled, test setup wrong")
	}
	tr = tr.toggleThinking()
	if !tr.entries[0].expanded {
		t.Fatal("settled thought not expanded after toggleThinking()")
	}
	if tr.entries[2].expanded {
		t.Fatal("live thought expanded after toggleThinking(), want it inert")
	}
	tr = tr.toggleThinking()
	if tr.entries[0].expanded {
		t.Fatal("settled thought still expanded after second toggle, want collapsed")
	}
}

func TestTranscript_ToggleThinkingAtMapsLineToEntry(t *testing.T) {
	var tr Transcript
	tr.entries = []entry{
		{kind: entryUser, text: "hi"},
		{kind: entryReasoning, text: "done", revealed: 4},
	}
	// Line list: a user line, a paragraph separator, then the thought line.
	lines := []entryLine{
		{idx: 0, line: "hi"},
		{idx: -1, line: ""},
		{idx: 1, line: "done"},
	}
	// Clicking the thought line toggles it.
	next, ok := tr.toggleThinkingAt(lines, 2)
	if !ok || !next.entries[1].expanded {
		t.Fatalf("toggleThinkingAt(thought line) = ok:%v expanded:%v, want it toggled", ok, next.entries[1].expanded)
	}
	// Clicking the user line or the separator toggles nothing.
	if _, ok := tr.toggleThinkingAt(lines, 0); ok {
		t.Fatal("toggleThinkingAt(user line) = true, want false")
	}
	if _, ok := tr.toggleThinkingAt(lines, 1); ok {
		t.Fatal("toggleThinkingAt(separator) = true, want false")
	}
	// Out of range is inert.
	if _, ok := tr.toggleThinkingAt(lines, 99); ok {
		t.Fatal("toggleThinkingAt(out of range) = true, want false")
	}
}

// visibleEntries is the single ordered projection the render path and the
// click-targeting path share. Its join flags and index mapping are the lockstep
// contract, enforced here directly.
func TestTranscript_VisibleEntriesJoinsAdjacentActivity(t *testing.T) {
	var tr Transcript
	tr.entries = []entry{
		{kind: entryUser, text: "hi"},
		{kind: entryTool, callID: "c1", tool: "bash", status: toolOK},
		{kind: entryTool, callID: "c2", tool: "grep", status: toolOK},
		{kind: entryAssistant, text: "reply"},
	}
	vis := tr.visibleEntries()
	if len(vis) != 4 {
		t.Fatalf("visibleEntries() = %d, want 4", len(vis))
	}
	wantJoin := []bool{false, false, true, false}
	for i, ve := range vis {
		if ve.joinCompact != wantJoin[i] {
			t.Fatalf("visibleEntries()[%d].joinCompact = %v, want %v", i, ve.joinCompact, wantJoin[i])
		}
		if ve.idx != i {
			t.Fatalf("visibleEntries()[%d].idx = %d, want %d", i, ve.idx, i)
		}
	}
}

func TestTranscript_VisibleEntriesPreservesIndexAcrossGatedGap(t *testing.T) {
	var tr Transcript
	// A gated running tool sits at index 1 but must not appear; the permission
	// ask at index 2 does. The visible entry must still carry the underlying
	// index so click targeting maps to the right entry.
	tr = fold(tr, EventMsg{Message: &session.Message{Role: session.RoleUser, Text: "hi"}})
	tr = fold(tr, EventMsg{Kind: session.KindToolCalled, CallID: "c1", ToolName: "bash", SessionID: "child"})
	tr = fold(tr, EventMsg{Kind: session.KindToolPermissionRequested, CallID: "c1", ToolName: "bash", SessionID: "child"})

	vis := tr.visibleEntries()
	if len(vis) != 2 {
		t.Fatalf("visibleEntries() = %d, want the user entry and the permission ask", len(vis))
	}
	if vis[0].idx != 0 || vis[1].idx != 2 {
		t.Fatalf("visible indices = %d,%d, want 0 and 2 (skipping the gated tool at 1)", vis[0].idx, vis[1].idx)
	}
}

// Compaction upsert is scoped to the session id passed in: a live compaction
// status for the same session is updated in place, not duplicated.
func TestTranscript_CompactionStatusUpsertsInPlace(t *testing.T) {
	var tr Transcript
	tr = tr.foldCompactionStatus(CompactionStatusMsg{State: CompactionQueued}, testSession)
	if len(tr.entries) != 1 || tr.entries[0].kind != entryCompaction || !tr.entries[0].live {
		t.Fatalf("after queued: entries=%+v, want one live compaction entry", tr.entries)
	}
	tr = tr.foldCompactionStatus(CompactionStatusMsg{State: CompactionRunning}, testSession)
	if len(tr.entries) != 1 || tr.entries[0].text != "Compacting context" {
		t.Fatalf("after running: entries=%+v, want the same entry updated in place", tr.entries)
	}
	tr = fold(tr, EventMsg{Kind: session.KindContextCompacted})
	if len(tr.entries) != 1 || tr.entries[0].live {
		t.Fatalf("after Context.Compacted: entries=%+v, want the compaction resolved (not live)", tr.entries)
	}
}

func TestTranscript_CompactionFailureCarriesError(t *testing.T) {
	var tr Transcript
	tr = tr.foldCompactionStatus(CompactionStatusMsg{State: CompactionQueued}, testSession)
	tr = tr.foldCompactionStatus(CompactionStatusMsg{State: CompactionFailed, Err: "compaction failed"}, testSession)
	e := tr.entries[0]
	if e.live || e.err != "compaction failed" {
		t.Fatalf("failed compaction = %+v, want resolved carrying the error", e)
	}
}

// replaceEvents rebuilds from a full log and resets derived state.
func TestTranscript_ReplaceEventsRebuildsAndResets(t *testing.T) {
	var tr Transcript
	// Seed some state that must be wiped by a rebuild.
	tr.outputBytes = 999
	tr.liveUsage = true
	tr = tr.appendError("stale")

	events := []session.SessionEvent{
		{Message: &session.Message{Role: session.RoleUser, Text: "q"}},
		{Kind: session.KindTextStarted},
		{Kind: session.KindTextDelta, Text: "a"},
		{Kind: session.KindStepEnded},
	}
	tr = tr.replaceEvents(events, testSession)

	// The rebuild wipes the stale seed: counters reset before folding (so
	// outputBytes reflects only the one streamed byte of the new log, not the
	// seeded 999), liveUsage is off (Step.Ended closed it), and the stale error
	// entry is gone.
	if tr.outputBytes != 1 || tr.liveUsage {
		t.Fatalf("replaceEvents left stale derived state: outputBytes=%d liveUsage=%v, want 1 and false", tr.outputBytes, tr.liveUsage)
	}
	if len(tr.entries) != 2 {
		t.Fatalf("entries = %d, want a user entry and the assistant reply (stale error dropped)", len(tr.entries))
	}
	if tr.entries[0].kind != entryUser || tr.entries[1].kind != entryAssistant || tr.entries[1].text != "a" {
		t.Fatalf("rebuilt entries = %+v, want [user, assistant 'a']", tr.entries)
	}
}
