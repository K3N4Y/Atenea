package session

import (
	"maps"
	"strconv"
	"time"

	"github.com/K3N4Y/atenea/internal/session/tasksettlement"
)

const (
	subagentToolCallsAttr = "atenea.internal.subagent_tool_calls"
	subagentRequestsAttr  = "atenea.internal.subagent_requests"
	subagentTokensAttr    = "atenea.internal.subagent_tokens"
	subagentDurationAttr  = "atenea.internal.subagent_duration_ms"
	subagentDetachedAttr  = "atenea.internal.subagent_detached"
	subagentWorkspaceAttr = "atenea.internal.subagent_workspace"
)

// WithSubagentToolCalls returns an event copy carrying the canonical private
// total. Attrs is cloned so callers' event envelopes are never mutated.
func WithSubagentToolCalls(event SessionEvent, total int) SessionEvent {
	if total < 0 {
		total = 0
	}
	event.Attrs = maps.Clone(event.Attrs)
	if event.Attrs == nil {
		event.Attrs = make(map[string]string, 1)
	}
	event.Attrs[subagentToolCallsAttr] = strconv.Itoa(total)
	return event
}

// SubagentToolCalls strictly reads a canonical nonnegative base-10 integer.
func SubagentToolCalls(event SessionEvent) (int, bool) {
	raw, ok := event.Attrs[subagentToolCallsAttr]
	if !ok || raw == "" || (len(raw) > 1 && raw[0] == '0') {
		return 0, false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	total, err := strconv.Atoi(raw)
	if err != nil || strconv.Itoa(total) != raw {
		return 0, false
	}
	return total, true
}

// WithTaskSettlement returns an event copy carrying canonical compact task usage.
func WithTaskSettlement(event SessionEvent, summary tasksettlement.Summary) SessionEvent {
	event = WithSubagentToolCalls(event, summary.ToolCalls)
	event.Attrs[subagentRequestsAttr] = strconv.Itoa(max(summary.Requests, 0))
	event.Attrs[subagentTokensAttr] = strconv.Itoa(max(summary.Tokens, 0))
	event.Attrs[subagentDurationAttr] = strconv.FormatInt(max(summary.Duration.Milliseconds(), 0), 10)
	if summary.Workspace != "" {
		event.Attrs[subagentWorkspaceAttr] = summary.Workspace
	}
	return event
}

// WithTaskDetached marks the settlement as an acknowledgement for a supervised
// job rather than a completed execution summary.
func WithTaskDetached(event SessionEvent) SessionEvent {
	event = WithSubagentToolCalls(event, 0)
	event.Attrs[subagentDetachedAttr] = "true"
	return event
}

func TaskDetached(event SessionEvent) bool { return event.Attrs[subagentDetachedAttr] == "true" }

// TaskSettlement strictly reads all task usage attributes. Partial or
// non-canonical metadata is rejected rather than silently producing bad totals.
func TaskSettlement(event SessionEvent) (tasksettlement.Summary, bool) {
	requests, ok := strictNonnegative(event.Attrs[subagentRequestsAttr])
	if !ok {
		return tasksettlement.Summary{}, false
	}
	tokens, ok := strictNonnegative(event.Attrs[subagentTokensAttr])
	if !ok {
		return tasksettlement.Summary{}, false
	}
	duration, ok := strictNonnegative(event.Attrs[subagentDurationAttr])
	if !ok {
		return tasksettlement.Summary{}, false
	}
	calls, ok := SubagentToolCalls(event)
	if !ok {
		return tasksettlement.Summary{}, false
	}
	return tasksettlement.Summary{Requests: requests, Tokens: tokens, Duration: time.Duration(duration) * time.Millisecond, ToolCalls: calls, Workspace: event.Attrs[subagentWorkspaceAttr]}, true
}

func strictNonnegative(raw string) (int, bool) {
	if raw == "" || (len(raw) > 1 && raw[0] == '0') {
		return 0, false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(raw)
	return n, err == nil && strconv.Itoa(n) == raw
}
