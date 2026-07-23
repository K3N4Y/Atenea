package session

import "slices"

func cloneStructuredSummary(summary StructuredSummary) StructuredSummary {
	summary.Constraints = slices.Clone(summary.Constraints)
	summary.Decisions = slices.Clone(summary.Decisions)
	summary.Completed = slices.Clone(summary.Completed)
	summary.Files = slices.Clone(summary.Files)
	summary.ToolResults = slices.Clone(summary.ToolResults)
	summary.Failures = slices.Clone(summary.Failures)
	summary.Pending = slices.Clone(summary.Pending)
	summary.Invariants = slices.Clone(summary.Invariants)
	return summary
}

func cloneCompactionCheckpoint(checkpoint CompactionCheckpoint) CompactionCheckpoint {
	checkpoint.Summary = cloneStructuredSummary(checkpoint.Summary)
	return checkpoint
}

func cloneMessage(message Message) Message {
	message.ToolCalls = slices.Clone(message.ToolCalls)
	return message
}

func cloneSessionEvent(event SessionEvent) SessionEvent {
	if event.Message != nil {
		message := cloneMessage(*event.Message)
		event.Message = &message
	}
	event.Input = append([]byte(nil), event.Input...)
	if event.Usage != nil {
		usage := *event.Usage
		event.Usage = &usage
	}
	if event.Compaction != nil {
		checkpoint := cloneCompactionCheckpoint(*event.Compaction)
		event.Compaction = &checkpoint
	}
	if event.Checkpoint != nil {
		checkpoint := *event.Checkpoint
		event.Checkpoint = &checkpoint
	}
	return event
}
