package llm

import (
	"fmt"
	"sync"
)

const ReasoningCommandDescription = "Set reasoning effort: /reasoning:<level> (default, minimal, low, medium, high, xhigh, max)"

func ReasoningHelp(effort ReasoningEffort) string {
	label := string(effort)
	if label == "" {
		label = "default"
	}
	return fmt.Sprintf("reasoning effort: %s; choose with /reasoning:<level> (default, minimal, low, medium, high, xhigh, max)", label)
}

// ReasoningSelection stores the process-local user preference shared by hosts
// and survives provider/workspace rewires. The zero value means provider default.
type ReasoningSelection struct {
	mu     sync.RWMutex
	effort ReasoningEffort
}

func (s *ReasoningSelection) Set(effort ReasoningEffort) error {
	if effort != "" && effort != ReasoningEffortMinimal && effort != ReasoningEffortLow && effort != ReasoningEffortMedium && effort != ReasoningEffortHigh && effort != ReasoningEffortXHigh && effort != ReasoningEffortMax {
		return fmt.Errorf("unsupported reasoning effort %q (supported: default, minimal, low, medium, high, xhigh, max)", effort)
	}
	s.mu.Lock()
	s.effort = effort
	s.mu.Unlock()
	return nil
}

func (s *ReasoningSelection) Get() *ReasoningPreference {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	effort := s.effort
	s.mu.RUnlock()
	if effort == "" {
		return nil
	}
	return &ReasoningPreference{Effort: effort}
}

func (s *ReasoningSelection) Effort() ReasoningEffort {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effort
}
