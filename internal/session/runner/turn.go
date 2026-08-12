package runner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/session/prompt"
	"github.com/K3N4Y/atenea/internal/session/tasksettlement"
	"github.com/K3N4Y/atenea/internal/tool"
)

// errRebuildTurn, errContinueAfterCompaction, errRetryTransientStream and
// errRetryAfterOverflow are internal turn-control signals consumed by runTurn's
// retry loop.
//
//   - errRebuildTurn: context changed while preparing the turn. The stale request
//     is discarded and rebuilt without calling the provider.
//   - errContinueAfterCompaction: context overflow was compacted before the
//     assistant message began, so preparation retries against the new history.
//   - errRetryTransientStream: the provider stream died on a transient transport
//     interruption. The failure was published as Step.Retrying, the backoff wait
//     already happened, and the attempt repeats against the durable store — the
//     same rebuild a manual retry performs.
//   - errRetryAfterOverflow: the provider rejected the request for exceeding its
//     context window. History was compacted durably and the attempt repeats
//     against the shortened store.
var (
	errRebuildTurn             = errors.New("rebuild prepared turn")
	errContinueAfterCompaction = errors.New("continue after overflow compaction")
	errRetryTransientStream    = errors.New("retry after transient stream failure")
	errRetryAfterOverflow      = errors.New("retry after provider context overflow")
)

const finalTurnToolError = "tool calls are unavailable during the final summary turn"

// ProviderError wraps a provider StepFailed for errors.As and durable failure.
type ProviderError struct {
	Message string
	Err     error // the typed cause, when the stream reported one
}

func (e *ProviderError) Error() string {
	if e.Message == "" {
		return "provider stream failed"
	}
	return "provider stream failed: " + e.Message
}

// Unwrap exposes the typed cause so callers can classify it with errors.As.
func (e *ProviderError) Unwrap() error { return e.Err }

// runTurn retries a turn for internal control signals and returns every other
// error or successful result. Each retry rebuilds state from the store.
func (r *Runner) runTurn(ctx context.Context, sessionID string) (bool, error) {
	return r.runTurnWithFinal(ctx, sessionID, false)
}

// turnBudget tracks what one logical turn has already spent across its retries.
// The two counters are independent budgets: a transport interruption and a
// context overflow are different failures with different remedies, and spending
// one must not consume the other.
type turnBudget struct {
	transientRetries int
	overflows        int
}

func (r *Runner) runTurnWithFinal(ctx context.Context, sessionID string, final bool) (bool, error) {
	var budget turnBudget
	for {
		cont, err := r.runTurnAttempt(ctx, sessionID, final, budget)
		switch {
		case errors.Is(err, errRebuildTurn):
			continue // preparation changed; rebuild from the store
		case errors.Is(err, errContinueAfterCompaction):
			continue // overflow was compacted; retry the post-compaction path
		case errors.Is(err, errRetryTransientStream):
			budget.transientRetries++
			continue // the backoff already happened; rebuild and call again
		case errors.Is(err, errRetryAfterOverflow):
			budget.overflows++
			continue // history was compacted durably; rebuild and call again
		default:
			return cont, err
		}
	}
}

// runTurnAttempt performs one turn attempt. It snapshots the epoch, builds the
// request from projected history and materialized tools, compacts on overflow,
// and rechecks the epoch before calling Stream exactly once.
func (r *Runner) runTurnAttempt(ctx context.Context, sessionID string, final bool, budget turnBudget) (bool, error) {
	before, err := r.store.Epoch(ctx, sessionID)
	if err != nil {
		return false, err
	}
	compactionStore, supportsCompaction := r.store.(session.CompactionStore)
	var runnerContext session.RunnerContext
	if supportsCompaction {
		runnerContext, err = compactionStore.ContextForRunner(ctx, sessionID)
		if err != nil {
			return false, err
		}
	}

	// Project history from the epoch baseline and materialize tools.
	msgs := runnerContext.Messages
	if !supportsCompaction {
		msgs, err = r.store.Messages(ctx, sessionID, before.BaselineSeq)
		if err != nil {
			return false, err
		}
	} else if runnerContext.Anchor != nil {
		msgs = append([]session.Message{*runnerContext.Anchor}, msgs...)
	}
	// Alternate modes select their own system prompt and permissions. A nil mode
	// hook preserves ordinary behavior.
	sys := r.system
	perms := r.perms
	if r.mode != nil {
		switch r.mode(sessionID) {
		case session.ModePlan:
			if r.planSystem != nil {
				sys = r.planSystem
			}
			if r.planPerms != nil {
				perms = r.planPerms
			}
		case session.ModeRAH:
			if r.rahSystem != nil {
				sys = r.rahSystem
			}
			if r.rahPerms != nil {
				perms = r.rahPerms
			}
		}
	}
	// Acquire first so model-dependent definitions and execution freeze together.
	providerSnapshot := llm.Acquire(r.provider)
	model := before.Model
	if providerSnapshot.Model != "" {
		model = providerSnapshot.Model
	}
	mat := r.registry.MaterializeFor(perms, model, sessionID)
	if mat.Err != nil {
		return false, mat.Err
	}
	req := llm.Request{Model: model, SessionKey: sessionKey(sessionID), Messages: toLLMMessages(msgs), Tools: mat.Definitions}
	if r.reasoning != nil {
		req.Reasoning = r.reasoning()
	}
	if sys != nil {
		req.System = sys(model)
	}
	if r.lessonSection != nil {
		latest := ""
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == session.RoleUser {
				latest = msgs[i].Text
				break
			}
		}
		req.System = prompt.InsertLessons(req.System, r.lessonSection(sessionID, latest))
	}
	if final {
		req.Tools = nil
		if req.System != "" {
			req.System += "\n\n"
		}
		req.System += FinalTurnInstruction
	}
	if runnerContext.Checkpoint != nil {
		req.System = renderCompactedSystem(req.System, runnerContext.Checkpoint.Summary)
	}

	// Compact preventively before the assistant message, then retry preparation.
	// A request that already crosses the threshold with nothing compactable left
	// is sent anyway: the estimate is an approximation, and refusing a turn the
	// provider might still accept would be worse than letting it answer. The
	// provider's own rejection is handled reactively after the stream.
	// The observation is the last completed turn's estimate/reported pair, which
	// calibrates away the estimator's systematic bias. It is empty until a turn
	// completes, and for a store without the compaction projection, which leaves
	// the raw estimate deciding — the behavior before calibration existed.
	observed := llm.TokenObservation{
		EstimatedTokens: runnerContext.LastTokens.EstimatedTokens,
		ReportedTokens:  runnerContext.LastTokens.ReportedTokens,
	}
	if r.compactor != nil && r.compactor.NeedsCompaction(req, observed) {
		switch err := r.compactor.Compact(ctx, sessionID, session.CompactionPreventive); {
		case err == nil:
			return false, errContinueAfterCompaction
		case errors.Is(err, session.ErrNoCompactableHistory):
			// Nothing to summarize away; let the provider judge the request.
		default:
			return false, err
		}
	}

	// Recheck the epoch before the provider call and discard a stale request.
	after, err := r.store.Epoch(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if after != before {
		return false, errRebuildTurn
	}

	// The epoch is current: call the provider once and consume its stream.
	in, err := providerSnapshot.Provider.Stream(ctx, req)
	if err != nil {
		return false, err
	}
	usageRequest := req
	usageRequest.MaxOutputTokens = 0
	pub := NewPublisher(r.store, sessionID, r.nextID(), llm.EstimateRequestTokens(usageRequest))
	return r.consume(ctx, sessionID, in, pub, mat.Settle, mat.Preview, final, budget)
}

func sessionKey(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return "atenea-" + base64.RawURLEncoding.EncodeToString(digest[:])
}

// consume drains and durably publishes the provider stream before settling local
// tool calls concurrently. This ordering guarantees a tool result never precedes
// the assistant message that declared it. Tool failures become Tool.Failed and
// do not abort the turn; durable-store failures do.
func (r *Runner) consume(ctx context.Context, sessionID string, in <-chan llm.Event,
	pub *Publisher, settle tool.SettleFunc, preview tool.PreviewFunc, rejectLocalTools bool, budget turnBudget) (bool, error) {

	g, gctx := errgroup.WithContext(ctx)
	cleanupCtx := context.WithoutCancel(ctx)
	needsContinuation := false
	var streamErr *ProviderError
	var calls []llm.Event
	partialInputs := make(map[string][]byte)
	partialNames := make(map[string]string)
	latestDigest := make(map[string]string)
	emitPreview := func(callID string) {
		if r.preview == nil || preview == nil {
			return
		}
		projected, ok := preview(ctx, tool.Call{ID: callID, Name: partialNames[callID], Input: partialInputs[callID]})
		if !ok || projected.Digest == latestDigest[callID] {
			return
		}
		latestDigest[callID] = projected.Digest
		r.preview(tool.PreviewEvent{SessionID: sessionID, CallID: callID, Preview: projected})
	}
	for ev := range in {
		switch ev.Kind {
		case llm.ToolInputStarted:
			partialInputs[ev.CallID] = nil
			partialNames[ev.CallID] = ev.ToolName
		case llm.ToolInputDelta:
			partialInputs[ev.CallID] = append(partialInputs[ev.CallID], ev.Input...)
			if ev.ToolName != "" {
				partialNames[ev.CallID] = ev.ToolName
			}
			emitPreview(ev.CallID)
		case llm.ToolInputEnded:
			emitPreview(ev.CallID)
		}
		if ev.Kind == llm.StepFailed {
			streamErr = &ProviderError{Message: ev.Text, Err: ev.Err}
			continue
		}
		if err := pub.Publish(ctx, ev); err != nil {
			return false, err
		}
		if ev.Kind == llm.ToolCall && (rejectLocalTools || !ev.ProviderExecuted) {
			needsContinuation = needsContinuation || !ev.ProviderExecuted
			calls = append(calls, ev)
		}
	}
	// A conforming stream has already materialized this on StepEnded. Preserve
	// complete calls from interrupted streams before any result can be appended.
	if err := pub.materializeAssistantForTools(cleanupCtx); err != nil {
		return false, err
	}
	// The assistant message is durable; local tools may now settle concurrently.
	for _, ev := range calls {
		ev := ev // capture for the goroutine
		recorder := tasksettlement.NewRecorder()
		pub.RegisterSettlementRecorder(ev.CallID, recorder)
		g.Go(func() error {
			if rejectLocalTools {
				return pub.ToolFailed(cleanupCtx, ev.CallID, errors.New(finalTurnToolError))
			}
			call := tool.Call{ID: ev.CallID, Name: ev.ToolName, Input: ev.Input}
			settleCtx := tool.WithSessionID(gctx, sessionID)
			settleCtx = tool.WithCallID(settleCtx, ev.CallID)
			settleCtx = tasksettlement.WithRecorder(settleCtx, recorder)
			settleCtx = tool.WithPermissionRequester(settleCtx, func(askCtx context.Context, request permission.Request) (bool, error) {
				if err := pub.ToolPermissionRequested(cleanupCtx, request.CallID); err != nil {
					return false, &tool.SettlementAbortError{Err: err}
				}
				return r.gate.Ask(askCtx, request)
			})
			res, err := settle(settleCtx, call)
			if err != nil {
				var abort *tool.SettlementAbortError
				if errors.As(err, &abort) {
					return abort.Err
				}
				if errors.Is(err, tool.ErrPermissionUnresolved) || (r.gate != nil && errors.Is(err, context.Canceled)) {
					return nil
				}
				r.logf("atenea: tool %q (call %s) failed: %v", ev.ToolName, ev.CallID, err)
				return pub.ToolFailedResult(cleanupCtx, ev.CallID, res, err)
			}
			return pub.ToolSuccessResult(cleanupCtx, ev.CallID, res)
		})
	}
	if err := g.Wait(); err != nil {
		return false, err
	}
	var cause error
	if streamErr != nil {
		cause = streamErr
	} else {
		cause = ctx.Err()
	}
	if cause == nil {
		return needsContinuation, nil
	}
	if err := pub.FailUnresolvedTools(cleanupCtx, cause); err != nil {
		return false, err
	}
	// The provider rejected the turn for overflowing its context. Compacting
	// durable history and retrying is the only thing that can make the same
	// request fit; failing here would lose the turn over a recoverable limit.
	if streamErr != nil && ctx.Err() == nil && llm.IsContextOverflow(streamErr) {
		retry, err := r.compactOverflow(ctx, cleanupCtx, sessionID, pub, streamErr, budget.overflows)
		if err != nil {
			cause = err
		} else if retry {
			return false, errRetryAfterOverflow
		}
	}
	// A transient transport interruption retries the turn instead of failing
	// it: the request rebuilds from the durable store, exactly like a manual
	// retry. Anything else — or an exhausted budget — closes the step.
	if streamErr != nil && ctx.Err() == nil && budget.transientRetries < len(r.transientRetryDelays) && llm.IsTransientStreamError(streamErr) {
		delay := r.transientRetryDelays[budget.transientRetries]
		notice := fmt.Sprintf("%s — retrying in %s (attempt %d of %d)",
			streamErr.Message, delay, budget.transientRetries+1, len(r.transientRetryDelays))
		if err := pub.Publish(cleanupCtx, llm.Event{Kind: llm.StepRetrying, Text: notice}); err != nil {
			return false, err
		}
		if sleepErr := sleepFor(ctx, delay); sleepErr == nil {
			return false, errRetryTransientStream
		}
		// Cancelled while waiting: fall through and close the step durably.
	}
	if err := pub.StepFailed(cleanupCtx, cause); err != nil {
		return false, err
	}
	return false, cause
}

// maxOverflowCompactions bounds how many times one turn may be compacted after
// a provider overflow. Compaction shrinks history monotonically, so a couple of
// rounds either make the request fit or prove the current activity alone is too
// large — retrying past that is a loop against a wall.
const maxOverflowCompactions = 2

// compactOverflow compacts durable history after the provider rejected the turn
// for overflowing its context, and reports whether the turn should retry. It
// returns an error only when the overflow is unrecoverable, so the caller fails
// the step with the reason the user needs rather than the raw provider message.
func (r *Runner) compactOverflow(ctx, cleanupCtx context.Context, sessionID string,
	pub *Publisher, streamErr *ProviderError, overflowsUsed int) (bool, error) {

	if r.compactor == nil {
		return false, nil // no compactor wired: the overflow surfaces as-is
	}
	if overflowsUsed >= maxOverflowCompactions {
		return false, session.ErrActivityTooLarge
	}
	notice := fmt.Sprintf("%s — compacting session history and retrying (attempt %d of %d)",
		streamErr.Message, overflowsUsed+1, maxOverflowCompactions)
	if err := pub.Publish(cleanupCtx, llm.Event{Kind: llm.StepRetrying, Text: notice}); err != nil {
		return false, err
	}
	switch err := r.compactor.Compact(ctx, sessionID, session.CompactionOverflow); {
	case err == nil:
		return true, nil
	case errors.Is(err, session.ErrNoCompactableHistory):
		// Nothing before the current activity can be summarized away, so the
		// activity itself does not fit. Say that instead of "context exceeded".
		return false, session.ErrActivityTooLarge
	default:
		return false, err
	}
}

// sleepFor waits d or until ctx cancels, whichever comes first.
func sleepFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// toLLMMessages projects the durable history into the provider's format. It
// carries over the pairing the provider needs: the assistant's tool calls (as
// llm.ToolCallPart, with Arguments kept as raw JSON) and the tool_call_id its
// result answers.
//
// Text, when nonempty, is projected first, followed by images in their durable
// order. Messages with neither remain content-free.
func toLLMMessages(msgs []session.Message) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		var calls []llm.ToolCallPart
		if len(m.ToolCalls) > 0 {
			calls = make([]llm.ToolCallPart, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				calls[j] = llm.ToolCallPart{ID: tc.ID, Name: tc.Name, Arguments: json.RawMessage(tc.Arguments)}
			}
		}
		message := llm.Message{Role: string(m.Role)}
		if m.Text != "" {
			message.Parts = append(message.Parts, llm.Part{Kind: llm.TextPart, Text: m.Text})
		}
		for _, image := range m.Images {
			message.Parts = append(message.Parts, llm.Part{Kind: llm.ImagePart, MediaType: image.MediaType, Data: image.Data})
		}
		message.ToolCalls = calls
		message.ToolCallID = m.ToolCallID
		message.IsError = m.IsError
		out[i] = message
	}
	return out
}
