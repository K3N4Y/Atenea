package session

import (
	"maps"
	"strconv"
)

const subagentToolCallsAttr = "atenea.internal.subagent_tool_calls"

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
