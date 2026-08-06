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
	"github.com/K3N4Y/atenea/internal/tool"
)

// errRebuildTurn, errContinueAfterCompaction and errRetryTransientStream are
// internal turn-control signals consumed by runTurn's retry loop.
//
//   - errRebuildTurn: context changed while preparing the turn. The stale request
//     is discarded and rebuilt without calling the provider.
//   - errContinueAfterCompaction: context overflow was compacted before the
//     assistant message began, so preparation retries against the new history.
//   - errRetryTransientStream: the provider stream died on a transient transport
//     interruption. The failure was published as Step.Retrying, the backoff wait
//     already happened, and the attempt repeats against the durable store — the
//     same rebuild a manual retry performs.
var (
	errRebuildTurn             = errors.New("rebuild prepared turn")
	errContinueAfterCompaction = errors.New("continue after overflow compaction")
	errRetryTransientStream    = errors.New("retry after transient stream failure")
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

func (r *Runner) runTurnWithFinal(ctx context.Context, sessionID string, final bool) (bool, error) {
	retriesUsed := 0
	for {
		cont, err := r.runTurnAttempt(ctx, sessionID, final, retriesUsed)
		switch {
		case errors.Is(err, errRebuildTurn):
			continue // preparation changed; rebuild from the store
		case errors.Is(err, errContinueAfterCompaction):
			continue // overflow was compacted; retry the post-compaction path
		case errors.Is(err, errRetryTransientStream):
			retriesUsed++
			continue // the backoff already happened; rebuild and call again
		default:
			return cont, err
		}
	}
}

// runTurnAttempt performs one turn attempt. It snapshots the epoch, builds the
// request from projected history and materialized tools, compacts on overflow,
// and rechecks the epoch before calling Stream exactly once.
func (r *Runner) runTurnAttempt(ctx context.Context, sessionID string, final bool, retriesUsed int) (bool, error) {
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
	// Plan mode selects its own system prompt and permissions. A nil mode hook
	// preserves normal-mode behavior.
	sys := r.system
	perms := r.perms
	if r.mode != nil && r.mode(sessionID) == session.ModePlan {
		if r.planSystem != nil {
			sys = r.planSystem
		}
		if r.planPerms != nil {
			perms = r.planPerms
		}
	}
	// Acquire first so model-dependent definitions and execution freeze together.
	providerSnapshot := llm.Acquire(r.provider)
	model := before.Model
	if providerSnapshot.Model != "" {
		model = providerSnapshot.Model
	}
	r.registry.SetPermissionGate(r.gate, r.policy)
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

	// Compact overflow before the assistant message, then retry preparation.
	if r.compactor != nil && r.compactor.NeedsCompaction(req) {
		if err := r.compactor.Compact(ctx, sessionID); err != nil && !errors.Is(err, session.ErrNoCompactableHistory) {
			return false, err
		} else if err == nil {
			return false, errContinueAfterCompaction
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
	return r.consume(ctx, sessionID, in, pub, mat.Settle, mat.Preview, final, retriesUsed)
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
	pub *Publisher, settle tool.SettleFunc, preview tool.PreviewFunc, rejectLocalTools bool, retriesUsed int) (bool, error) {

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
	// The assistant message is durable; local tools may now settle concurrently.
	for _, ev := range calls {
		ev := ev // capture for the goroutine
		recorder := tool.NewSettlementRecorder()
		pub.RegisterSettlementRecorder(ev.CallID, recorder)
		g.Go(func() error {
			if rejectLocalTools {
				return pub.ToolFailed(cleanupCtx, ev.CallID, errors.New(finalTurnToolError))
			}
			call := tool.Call{ID: ev.CallID, Name: ev.ToolName, Input: ev.Input}
			settleCtx := tool.WithSessionID(gctx, sessionID)
			settleCtx = tool.WithCallID(settleCtx, ev.CallID)
			settleCtx = tool.WithSettlementRecorder(settleCtx, recorder)
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
	// A transient transport interruption retries the turn instead of failing
	// it: the request rebuilds from the durable store, exactly like a manual
	// retry. Anything else — or an exhausted budget — closes the step.
	if streamErr != nil && ctx.Err() == nil && retriesUsed < len(r.transientRetryDelays) && llm.IsTransientStreamError(streamErr) {
		delay := r.transientRetryDelays[retriesUsed]
		notice := fmt.Sprintf("%s — retrying in %s (attempt %d of %d)",
			streamErr.Message, delay, retriesUsed+1, len(r.transientRetryDelays))
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
