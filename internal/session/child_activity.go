package session

import "maps"

const parentTaskCallAttr = "atenea.internal.parent_task_call_id"

// WithParentTaskCall returns an event copy attributed to its parent task call.
// Attrs is cloned so decorating a bus envelope cannot mutate the durable event.
func WithParentTaskCall(event SessionEvent, callID string) SessionEvent {
	event.Attrs = maps.Clone(event.Attrs)
	if event.Attrs == nil {
		event.Attrs = make(map[string]string, 1)
	}
	event.Attrs[parentTaskCallAttr] = callID
	return event
}

// ParentTaskCallID identifies attributed child activity. The metadata key stays
// private so it cannot become part of the public agentcore event contract.
func ParentTaskCallID(event SessionEvent) string {
	return event.Attrs[parentTaskCallAttr]
}
