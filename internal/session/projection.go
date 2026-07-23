package session

import "fmt"

// foldMessages projects message-bearing events after sinceSeq. Keeping this
// rule independent of either adapter makes their histories identical.
func foldMessages(events []SessionEvent, sinceSeq Seq) []Message {
	out := make([]Message, 0)
	for _, event := range events {
		if event.Seq <= sinceSeq || event.Message == nil {
			continue
		}
		message := cloneMessage(*event.Message)
		message.Seq = event.Seq
		out = append(out, message)
	}
	return out
}

// validCompactionReferences verifies that a checkpoint identifies a coherent
// user-anchored suffix. Both Store adapters use this same commit rule.
func validCompactionReferences(events []SessionEvent, checkpoint CompactionCheckpoint) bool {
	if checkpoint.CoveredThroughSeq >= checkpoint.PreservedFromSeq || checkpoint.AnchorUserSeq > checkpoint.PreservedFromSeq {
		return false
	}

	anchorIndex := -1
	preservedIndex := -1
	for index, event := range events {
		if event.Message == nil {
			continue
		}
		if event.Seq == checkpoint.AnchorUserSeq && event.Message.Role == RoleUser {
			anchorIndex = index
		}
		if event.Seq == checkpoint.PreservedFromSeq {
			preservedIndex = index
		}
	}
	if anchorIndex < 0 || preservedIndex < 0 {
		return false
	}
	for index := anchorIndex + 1; index < len(events); index++ {
		if events[index].Message != nil && events[index].Message.Role == RoleUser {
			return false
		}
	}
	return validPreservedSuffix(events[preservedIndex:])
}

func validPreservedSuffix(events []SessionEvent) bool {
	for index := 0; index < len(events); {
		message := events[index].Message
		if message == nil {
			index++
			continue
		}
		if message.Role == RoleTool {
			return false
		}
		index++
		if message.Role != RoleAssistant || len(message.ToolCalls) == 0 {
			continue
		}

		pending, ok := pendingToolCallIDs(message.ToolCalls)
		if !ok {
			return false
		}
		for len(pending) > 0 {
			for index < len(events) && events[index].Message == nil {
				index++
			}
			if index >= len(events) || events[index].Message.Role != RoleTool {
				return false
			}
			toolCallID := events[index].Message.ToolCallID
			if _, ok := pending[toolCallID]; !ok {
				return false
			}
			delete(pending, toolCallID)
			index++
		}
	}
	return true
}

func pendingToolCallIDs(calls []ToolCall) (map[string]struct{}, bool) {
	pending := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if call.ID == "" {
			return nil, false
		}
		pending[call.ID] = struct{}{}
	}
	if len(pending) != len(calls) {
		return nil, false
	}
	return pending, true
}

func compactionConflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCompactionConflict, fmt.Sprintf(format, args...))
}
