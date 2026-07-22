package tui

// The Transcript module owns the conversation log and the pure logic over it.
// It is the projection of durable SessionEvents onto renderable entries plus
// the derived state the UI reads back: exact and estimated token usage, the
// smooth-reveal cursor, and the query predicates that gate the keyboard
// (pending permission, pending plan approval).
//
// The module is deliberately pure and I/O-free: value-in / value-out, no
// globals, no Bubble Tea commands, no session I/O. Session identity is not
// transcript state, so the two mutators that need it (compaction upsert and
// event fold) receive the current session ID as an argument rather than
// reaching for it. Per-entry string rendering (markdown, glamour, diff cards)
// stays in view.go and only reads entries and reveal state from here.
//
// Model embeds Transcript anonymously, so `m.entries`, `m.usage`,
// `m.foldEvent(...)`, `m.hasBacklog()` and friends read as if they were the
// Model's own — the same idiom the overlay pickers use with overlayList.

import (
	"time"
	"unicode/utf8"

	"atenea/internal/session"
)

// Transcript is the conversation log and the pure state derived from it. Model
// holds exactly one, embedded, so the field and method names below are promoted
// onto Model unchanged.
type Transcript struct {
	// entries is the whole ordered conversation log: the projection of durable
	// SessionEvents onto renderable blocks.
	entries []entry

	// usage is the token usage shown in the composer border: exact from
	// Step.Ended, or estimated live while a step streams. liveUsage marks the
	// estimate so the label can carry the `~` prefix. outputBytes/reasoningBytes/
	// toolInputBytes accumulate the streamed byte counts that feed the live
	// estimate (see updateLiveUsage / estimatedTokens).
	usage          *session.Usage
	liveUsage      bool
	outputBytes    int
	reasoningBytes int
	toolInputBytes int

	// revealing marks that the smooth-streaming tick loop is running. Mirror of
	// the spinner loop: it is set when an event leaves backlog with no loop
	// active, re-armed on each tick while backlog remains, and cleared when the
	// backlog drains; a later delta restarts it. The flag prevents duplicating
	// tick chains when several deltas arrive before the next tick. The tick
	// scheduling (revealTick, which produces a tea.Cmd) stays near Update.
	revealing bool
}

// foldEvent applies a durable event to the conversation entries. sessionID is
// the Model's current session, used to scope the compaction upsert (a durable
// event carries no session context of its own here).
func (t Transcript) foldEvent(ev EventMsg, sessionID string) Transcript {
	switch ev.Kind {
	case session.KindStepStarted:
		if ev.Usage != nil {
			usage := *ev.Usage
			t.usage = &usage
			t.liveUsage = true
			t.outputBytes = 0
			t.reasoningBytes = 0
			t.toolInputBytes = 0
		}
	case session.KindTextStarted:
		t = t.openAssistantBlock()
	case session.KindTextDelta:
		t.outputBytes += len(ev.Text)
		t = t.updateLiveUsage()
		// Defensive open: the delta may arrive without Text.Started.
		if !t.assistantOpen() {
			t = t.openAssistantBlock()
		}
		t.lastEntry().text += ev.Text
	case session.KindReasoningStarted:
		t = t.openReasoningBlock()
	case session.KindReasoningDelta:
		t.reasoningBytes += len(ev.Text)
		t = t.updateLiveUsage()
		// Defensive open: the delta may arrive without Reasoning.Started.
		if !t.reasoningOpen() {
			t = t.openReasoningBlock()
		}
		t.lastEntry().text += ev.Text
	case session.KindReasoningEnded:
		if t.reasoningOpen() {
			last := t.lastEntry()
			last.fillCoalesced(ev.Text)
			last.closeThinking()
		}
	case session.KindStepEnded:
		if ev.Usage != nil {
			usage := *ev.Usage
			t.usage = &usage
		}
		t.liveUsage = false
		// The end of the step also closes a thought still live (defensive close:
		// the step may die thinking, from cancellation or a provider error,
		// without a Reasoning.Ended in between).
		t = t.closeThinkingBlocks()
		if t.assistantOpen() {
			last := t.lastEntry()
			if ev.Message != nil {
				last.fillCoalesced(ev.Message.Text)
			}
			last.live = false
		}
	case session.KindToolCalled:
		t.entries = append(t.entries, entry{
			kind: entryTool, callID: ev.CallID, tool: ev.ToolName, status: toolRunning,
			input: string(ev.Input), sessionID: ev.SessionID,
		})
	case session.KindToolSuccess:
		t = t.settleTool(ev.CallID, toolOK, "", ev.Text, ev.Diff)
	case session.KindToolFailed:
		t = t.settleTool(ev.CallID, toolFailed, ev.Error, "", "")
	case session.KindToolPermissionRequested:
		input := string(ev.Input)
		if input == "" {
			input = t.toolCallInput(ev.CallID, ev.SessionID)
		}
		t.entries = append(t.entries, entry{
			kind: entryPermission, callID: ev.CallID, tool: ev.ToolName,
			input: input, sessionID: ev.SessionID,
		})
	case session.KindStepFailed:
		t.liveUsage = false
		t = t.appendError(ev.Error)
	case session.KindToolInputDelta:
		t.toolInputBytes += len(ev.Text)
		t = t.updateLiveUsage()
	case session.KindContextCompacted:
		t = t.resolveCompaction("Context compacted", false, sessionID)
	case "":
		// Event without taxonomy: the runner promotes the user's prompt as
		// Message{Role: user} with no Kind.
		if ev.Message != nil && ev.Message.Role == session.RoleUser {
			t.entries = append(t.entries, entry{kind: entryUser, text: ev.Message.Text})
		}
	}
	return t
}

func (t Transcript) toolCallInput(callID, sessionID string) string {
	for index := len(t.entries) - 1; index >= 0; index-- {
		e := t.entries[index]
		if e.kind != entryTool || e.callID != callID {
			continue
		}
		if sessionID != "" && e.sessionID != "" && e.sessionID != sessionID {
			continue
		}
		return e.input
	}
	return ""
}

// replaceEvents rebuilds the transcript from a full durable log, resetting the
// derived state (usage, reveal loop) before folding each event.
func (t Transcript) replaceEvents(events []session.SessionEvent, sessionID string) Transcript {
	t.entries = nil
	t.revealing = false
	t.usage = nil
	t.liveUsage = false
	t.outputBytes = 0
	t.reasoningBytes = 0
	t.toolInputBytes = 0
	for _, event := range events {
		t = t.foldEvent(EventMsg(event), sessionID)
	}
	return t
}

func (t Transcript) foldCompactionStatus(status CompactionStatusMsg, sessionID string) Transcript {
	switch status.State {
	case CompactionQueued:
		return t.upsertCompaction("Compaction queued", false, true, sessionID)
	case CompactionRunning:
		return t.upsertCompaction("Compacting context", false, true, sessionID)
	case CompactionNotNeeded:
		return t.resolveCompaction("Not enough context to compact", false, sessionID)
	case CompactionFailed:
		return t.resolveCompaction(status.Err, true, sessionID)
	default:
		return t
	}
}

func (t Transcript) upsertCompaction(text string, failed, live bool, sessionID string) Transcript {
	for i := len(t.entries) - 1; i >= 0; i-- {
		if t.entries[i].kind == entryCompaction && t.entries[i].sessionID == sessionID && t.entries[i].live {
			t.entries[i].text = text
			t.entries[i].err = ""
			t.entries[i].live = live
			if failed {
				t.entries[i].err = text
			}
			return t
		}
	}
	entry := entry{kind: entryCompaction, text: text, sessionID: sessionID, live: live}
	if failed {
		entry.err = text
	}
	t.entries = append(t.entries, entry)
	return t
}

func (t Transcript) resolveCompaction(text string, failed bool, sessionID string) Transcript {
	return t.upsertCompaction(text, failed, false, sessionID)
}

func (t Transcript) updateLiveUsage() Transcript {
	if !t.liveUsage || t.usage == nil {
		return t
	}
	t.usage.OutputTokens = estimatedTokens(t.outputBytes + t.reasoningBytes + t.toolInputBytes)
	t.usage.ReasoningTokens = 0
	return t
}

func estimatedTokens(bytes int) int {
	return (bytes + 2) / 3
}

// settleTool settles the outcome of the tool call with that callID (ok or
// failure) and drops its pending permission request: the contract carries no
// resolution event of its own, the Tool.Success/Tool.Failed of the same CallID
// expresses it. output is the Tool.Success result (ev.Text) and stays on the
// entry for the transcript preview; Tool.Failed passes "" (its detail travels
// in errMsg). diff is the unified diff of Tool.Success for edit/write (ev.Diff):
// when non-empty the view shows it instead of the output preview. A present_plan
// settled with success appends, at the end, the plan approval offer (y execute
// / n stay in plan).
func (t Transcript) settleTool(callID string, status toolStatus, errMsg, output, diff string) Transcript {
	planPresented := false
	kept := make([]entry, 0, len(t.entries))
	for _, e := range t.entries {
		if e.kind == entryPermission && e.callID == callID {
			continue
		}
		if e.kind == entryTool && e.callID == callID {
			if !(e.status == toolDenied && status == toolFailed && errMsg == "tool denied by the user") {
				e.status = status
				e.err = errMsg
			}
			e.output = output
			e.diff = diff
			if e.tool == "present_plan" && status == toolOK {
				planPresented = true
			}
		}
		kept = append(kept, e)
	}
	t.entries = kept
	if planPresented {
		t.entries = append(t.entries, entry{kind: entryPlanApproval})
	}
	return t
}

func (t Transcript) applyPermissionDecision(permission entry, approved bool) Transcript {
	kept := make([]entry, 0, len(t.entries))
	for _, e := range t.entries {
		if e.kind == entryPermission && e.callID == permission.callID && e.sessionID == permission.sessionID {
			continue
		}
		if !approved && e.kind == entryTool && e.callID == permission.callID && e.sessionID == permission.sessionID {
			e.status = toolDenied
			e.err = "Denied by user"
		}
		kept = append(kept, e)
	}
	t.entries = kept
	return t
}

func (t Transcript) pendingPermissionCount() int {
	count := 0
	for _, e := range t.entries {
		if e.kind == entryPermission {
			count++
		}
	}
	return count
}

// pendingPermission returns the full entry of the pending request (with its
// CallID and the SessionID the event carried) and true if there is one.
func (t Transcript) pendingPermission() (entry, bool) {
	for _, e := range t.entries {
		if e.kind == entryPermission {
			return e, true
		}
	}
	return entry{}, false
}

// hasPendingPlan reports whether a pending plan approval offer exists. Unlike
// pendingPermission it does not return the entry: the offer carries no data
// (neither CallID nor SessionID), it only exists or not.
func (t Transcript) hasPendingPlan() bool {
	for _, e := range t.entries {
		if e.kind == entryPlanApproval {
			return true
		}
	}
	return false
}

// removePendingPlan drops the plan approval offer from the conversation.
func (t Transcript) removePendingPlan() Transcript {
	kept := make([]entry, 0, len(t.entries))
	for _, e := range t.entries {
		if e.kind == entryPlanApproval {
			continue
		}
		kept = append(kept, e)
	}
	t.entries = kept
	return t
}

// appendError appends an error block to the end of the conversation; shared by
// the step's hard failure and the end-of-run with error.
func (t Transcript) appendError(text string) Transcript {
	t.entries = append(t.entries, entry{kind: entryError, text: text})
	return t
}

// appendNotice adds a dim informational line to the conversation (a provider
// connection confirmation, the first-run hint).
func (t Transcript) appendNotice(text string) Transcript {
	t.entries = append(t.entries, entry{kind: entryNotice, text: text})
	return t
}

// openAssistantBlock opens a live assistant block at the end of the
// conversation. It first closes any thought still live: the answer starting
// implies the thought ended, even if the runner did not emit Reasoning.Ended
// (defensive close).
func (t Transcript) openAssistantBlock() Transcript {
	t = t.closeThinkingBlocks()
	t.entries = append(t.entries, entry{kind: entryAssistant, live: true})
	return t
}

// openReasoningBlock opens a live thought block at the end of the conversation,
// capturing the open instant to compute the duration the collapsed summary
// shows.
func (t Transcript) openReasoningBlock() Transcript {
	t.entries = append(t.entries, entry{kind: entryReasoning, live: true, startedAt: time.Now()})
	return t
}

// closeThinkingBlocks closes any thought block still live. It is the defensive
// close: the runner might not emit Reasoning.Ended, and both opening an
// assistant block and closing the step imply the thought ended.
func (t Transcript) closeThinkingBlocks() Transcript {
	for i := range t.entries {
		if t.entries[i].kind == entryReasoning && t.entries[i].live {
			t.entries[i].closeThinking()
		}
	}
	return t
}

// toggleThinking flips the expanded state of every settled thought block
// (closed and with the reveal drained). Live blocks or blocks with backlog do
// not take part: the in-progress thought preview is governed by renderThinking,
// not by expanded. Toggling all at once is the Shift+Tab semantics (see
// handleKey): a single stroke expands or collapses the reasoning of the whole
// conversation.
func (t Transcript) toggleThinking() Transcript {
	for i := range t.entries {
		if t.entries[i].kind == entryReasoning && t.entries[i].settled() {
			t.entries[i].expanded = !t.entries[i].expanded
		}
	}
	return t
}

// toggleThinkingAt flips the expanded state of the settled thought block that
// occupies the absolute line viewportLine of the viewport content (see
// entryLines). It acts only on already-settled entryReasoning blocks: the live
// thought preview does not take part. Returns the Transcript and true when it
// found and toggled a block (so the caller re-syncs the viewport). With
// viewportLine out of range or over a line that is not a settled thought block,
// it returns (t, false) unchanged. lines is the viewport line list from
// entryLines, passed in because computing it requires the render width the
// Model owns.
func (t Transcript) toggleThinkingAt(lines []entryLine, viewportLine int) (Transcript, bool) {
	if viewportLine < 0 || viewportLine >= len(lines) {
		return t, false
	}
	idx := lines[viewportLine].idx
	if idx < 0 || idx >= len(t.entries) {
		return t, false
	}
	e := &t.entries[idx]
	if e.kind != entryReasoning || !e.settled() {
		return t, false
	}
	e.expanded = !e.expanded
	return t, true
}

// lastEntry returns the last entry to mutate it; the caller guarantees it
// exists.
func (t *Transcript) lastEntry() *entry {
	return &t.entries[len(t.entries)-1]
}

// lastLiveIs reports whether the last entry is a block of the given kind still
// live: the fold only accumulates deltas onto the tail of the conversation.
func (t Transcript) lastLiveIs(kind entryKind) bool {
	if len(t.entries) == 0 {
		return false
	}
	last := t.lastEntry()
	return last.kind == kind && last.live
}

// assistantOpen reports whether the last entry is a live, unclosed assistant
// block.
func (t Transcript) assistantOpen() bool { return t.lastLiveIs(entryAssistant) }

// reasoningOpen reports whether the last entry is a live, unclosed thought
// block (mirror of assistantOpen for entryReasoning).
func (t Transcript) reasoningOpen() bool { return t.lastLiveIs(entryReasoning) }

// hasBacklog reports whether any entry has text still to reveal.
func (t Transcript) hasBacklog() bool {
	for _, e := range t.entries {
		if e.backlog() > 0 {
			return true
		}
	}
	return false
}

// advanceReveal advances one tick step of the reveal for each entry with
// backlog.
func (t Transcript) advanceReveal() Transcript {
	for i := range t.entries {
		if b := t.entries[i].backlog(); b > 0 {
			t.entries[i].revealed += revealStep(b)
		}
	}
	return t
}

// Smooth streaming of the text that arrives by deltas (assistant and thought;
// parity with the desktop, frontend/src/lib/reveal.ts): it decouples the pace
// of the network from the pace of reading. The deltas accumulate whole in
// entry.text, but the view reveals only a prefix (entry.revealed runes) that
// advances with each tick, so the text is "typed" smoothly instead of appearing
// in jumps. The tick scheduling (revealTickMsg, revealTick) lives in reveal.go,
// next to the Bubble Tea command loop that consumes it; the pacing math below
// is pure, so it lives here with the state it advances.

const (
	// revealMSPerRune is the base reading pace: ~5ms per rune (~200 rps), the
	// same as the desktop (MS_PER_CHAR in reveal.ts).
	revealMSPerRune = 5

	// revealCatchUpFrames bounds the lag: facing a large backlog it speeds up to
	// drain it in at most this many ticks, so the visible text never falls many
	// ticks behind the backend (with fast models it avoids lagging seconds
	// behind).
	revealCatchUpFrames = 8
)

// revealBaseRunes is how many runes a tick reveals at base pace: the tick
// interval split by the per-rune pace, rounded up (~7 runes).
const revealBaseRunes = (int(revealTickInterval/time.Millisecond) + revealMSPerRune - 1) / revealMSPerRune

// revealStep returns how many runes to reveal this tick: the base pace or the
// catch-up proportional to the backlog (whichever is larger, with the catch-up
// rounded up), bounded to [1, remaining].
func revealStep(remaining int) int {
	if remaining <= 0 {
		return 0
	}
	catchUp := (remaining + revealCatchUpFrames - 1) / revealCatchUpFrames
	return min(max(revealBaseRunes, catchUp), remaining)
}

// permissionGatedTools returns the callID+sessionID keys of tools whose
// permission is still pending. The ask-before-run gate emits Tool.Called
// (running "●") immediately followed by Tool.Permission.Requested ("?") for the
// same call, so both would render on adjacent rows. While a key is present the
// transcript hides the running header and shows only the "? <tool>" ask, so the
// same call is not duplicated while the user decides. Returns nil when nothing
// is pending, so the hot path allocates nothing.
func (t Transcript) permissionGatedTools() map[string]struct{} {
	var gated map[string]struct{}
	for _, e := range t.entries {
		if e.kind != entryPermission {
			continue
		}
		if gated == nil {
			gated = make(map[string]struct{})
		}
		gated[e.callID+"\x00"+e.sessionID] = struct{}{}
	}
	return gated
}

// toolGatedByPermission reports whether a running tool header must be hidden
// because its permission is still pending (see permissionGatedTools).
func toolGatedByPermission(e entry, gated map[string]struct{}) bool {
	if e.kind != entryTool || e.status != toolRunning || gated == nil {
		return false
	}
	_, ok := gated[e.callID+"\x00"+e.sessionID]
	return ok
}

// visibleEntry is one entry that survives the permission gate, tagged with its
// index in the underlying entries slice and whether it joins the previous
// visible entry into a compact activity group (no blank line between them).
type visibleEntry struct {
	entry entry
	// idx is the index of this entry in Transcript.entries, for click targeting.
	idx int
	// joinCompact is true when this entry attaches to the previous VISIBLE entry
	// without a blank line (an adjacent activity group); false starts a new
	// paragraph. Always false for the first visible entry.
	joinCompact bool
}

// visibleEntries is the single ordered source both renderTranscript and
// entryLines consume, so the render path and the click-targeting path cannot
// drift apart: the gating skip (toolGatedByPermission) and the paragraph-join
// decision (compactActivityJoin) are applied once, here, instead of by two
// functions independently calling the same predicates. Returns nil for an empty
// or fully-gated transcript.
func (t Transcript) visibleEntries() []visibleEntry {
	gated := t.permissionGatedTools()
	var out []visibleEntry
	prev := -1
	for i, e := range t.entries {
		if toolGatedByPermission(e, gated) {
			continue
		}
		join := prev >= 0 && compactActivityJoin(t.entries[prev], e)
		out = append(out, visibleEntry{entry: e, idx: i, joinCompact: join})
		prev = i
	}
	return out
}

// compactActivityJoin decides whether entry cur attaches to prev WITHOUT a
// blank line: both must be activity (tool, permission, or step error), which
// form a compact group of physically contiguous headers; any other adjacency
// (assistant narrative, thought, user, compaction) keeps its own paragraph
// ("\n\n"). It is the single predicate visibleEntries applies, so the render
// path and the click-targeting path share one join decision by construction.
func compactActivityJoin(prev, cur entry) bool {
	isActivity := func(kind entryKind) bool {
		return kind == entryTool || kind == entryPermission || kind == entryError
	}
	return isActivity(prev.kind) && isActivity(cur.kind)
}

// backlog returns how many runes of the entry's text are still to reveal. Only
// entries whose text arrives by streaming take part (assistant and thought);
// every other entry shows complete from the moment it exists.
func (e entry) backlog() int {
	if e.kind != entryAssistant && e.kind != entryReasoning {
		return 0
	}
	return max(utf8.RuneCountInString(e.text)-e.revealed, 0)
}

// settled reports that the block reached its final form: streaming closed (live
// off) and the reveal drained all backlog. Only then does the view change shape
// (markdown for the assistant, collapsed summary for the thought): jumping
// earlier would flash the mid-animated text all at once.
func (e entry) settled() bool {
	return !e.live && e.backlog() == 0
}

// revealedText returns the already-revealed prefix of the entry's text. The cut
// is BY RUNES, never by bytes: a multibyte character is never split in half.
func (e entry) revealedText() string {
	runes := []rune(e.text)
	if e.revealed >= len(runes) {
		return e.text
	}
	return string(runes[:e.revealed])
}

// fillCoalesced fills the live block with the coalesced text its close event
// carries (the Message of Step.Ended, the Text of Reasoning.Ended) ONLY if the
// stream carried nothing, and reveals it complete at once: the reveal smooths
// the pace of the deltas, not that of text that already arrived whole.
func (e *entry) fillCoalesced(text string) {
	if e.text != "" || text == "" {
		return
	}
	e.text = text
	e.revealed = utf8.RuneCountInString(text)
}

// closeThinking closes the thought block: turns off live and fixes the duration
// from the open instant. With the backlog already drained the view collapses
// the block to the summary line (see renderThinking).
func (e *entry) closeThinking() {
	e.live = false
	e.duration = time.Since(e.startedAt)
}
