package runner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"

	"golang.org/x/sync/errgroup"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

// errRebuildTurn and errContinueAfterCompaction are internal turn-control
// signals consumed by runTurn's retry loop.
//
//   - errRebuildTurn: context changed while preparing the turn. The stale request
//     is discarded and rebuilt without calling the provider.
//   - errContinueAfterCompaction: context overflow was compacted before the
//     assistant message began, so preparation retries against the new history.
var (
	errRebuildTurn             = errors.New("rebuild prepared turn")
	errContinueAfterCompaction = errors.New("continue after overflow compaction")
)

// ProviderError wraps a provider StepFailed for errors.As and durable failure.
type ProviderError struct {
	Message string
}

func (e *ProviderError) Error() string {
	if e.Message == "" {
		return "provider stream failed"
	}
	return "provider stream failed: " + e.Message
}

// runTurn retries a turn for internal control signals and returns every other
// error or successful result. Each retry rebuilds state from the store.
func (r *Runner) runTurn(ctx context.Context, sessionID string) (bool, error) {
	for {
		cont, err := r.runTurnAttempt(ctx, sessionID)
		switch {
		case errors.Is(err, errRebuildTurn):
			continue // preparation changed; rebuild from the store
		case errors.Is(err, errContinueAfterCompaction):
			continue // overflow was compacted; retry the post-compaction path
		default:
			return cont, err
		}
	}
}

// runTurnAttempt performs one turn attempt. It snapshots the epoch, builds the
// request from projected history and materialized tools, compacts on overflow,
// and rechecks the epoch before calling Stream exactly once.
func (r *Runner) runTurnAttempt(ctx context.Context, sessionID string) (bool, error) {
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
	// Materialization snapshots execution policy along with the allowed tools.
	// Tests may configure the runner fields directly, while production uses the
	// setter; synchronizing here keeps both paths on the same registry seam.
	r.registry.SetPermissionGate(r.gate, r.policy)
	mat := r.registry.Materialize(perms)
	providerSnapshot := llm.Acquire(r.provider)
	model := before.Model
	if providerSnapshot.Model != "" {
		model = providerSnapshot.Model
	}
	req := llm.Request{Model: model, SessionKey: sessionKey(sessionID), Messages: toLLMMessages(msgs), Tools: mat.Definitions}
	if sys != nil {
		req.System = sys(model)
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
	return r.consume(ctx, sessionID, in, pub, mat.Settle)
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
	pub *Publisher, settle tool.SettleFunc) (bool, error) {

	g, gctx := errgroup.WithContext(ctx)
	cleanupCtx := context.WithoutCancel(ctx)
	needsContinuation := false
	var streamErr *ProviderError
	var calls []llm.Event
	for ev := range in {
		if ev.Kind == llm.StepFailed {
			streamErr = &ProviderError{Message: ev.Text}
			continue
		}
		if err := pub.Publish(ctx, ev); err != nil {
			return false, err
		}
		if ev.Kind == llm.ToolCall && !ev.ProviderExecuted {
			needsContinuation = true
			calls = append(calls, ev)
		}
	}
	// The assistant message is durable; local tools may now settle concurrently.
	for _, ev := range calls {
		ev := ev // capture for the goroutine
		g.Go(func() error {
			call := tool.Call{ID: ev.CallID, Name: ev.ToolName, Input: ev.Input}
			settleCtx := tool.WithSessionID(gctx, sessionID)
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
				return pub.ToolFailed(cleanupCtx, ev.CallID, err)
			}
			return pub.ToolSuccess(cleanupCtx, ev.CallID, res.Output, res.Diff)
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
	if cause != nil {
		if err := pub.FailUnresolvedTools(cleanupCtx, cause); err != nil {
			return false, err
		}
		if err := pub.StepFailed(cleanupCtx, cause); err != nil {
			return false, err
		}
		return false, cause
	}
	return needsContinuation, nil
}

// toLLMMessages projects the durable history into the provider's format. It
// carries over the pairing the provider needs: the assistant's tool calls (as
// llm.ToolCallPart, with Arguments kept as raw JSON) and the tool_call_id its
// result answers.
//
// A durable message is text, so it becomes a message whose content is one text
// part — and one with nothing to say, an assistant turn that only called tools,
// becomes a message with no content at all rather than an empty block. The day
// the durable stream carries an image (audit R5), this is the one place that has
// to learn to project it.
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
		message := llm.TextMessage(string(m.Role), m.Text)
		message.ToolCalls = calls
		message.ToolCallID = m.ToolCallID
		message.IsError = m.IsError
		out[i] = message
	}
	return out
}
