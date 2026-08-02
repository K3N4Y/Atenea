package session

import (
	"errors"
	"strings"

	contract "github.com/K3N4Y/atenea/agentcore/memory"
)

const (
	DefaultMemoryRecallLimit = contract.DefaultRecallLimit
	MaxMemoryRecallLimit     = contract.MaxRecallLimit
)

var ErrInvalidMemory = errors.New("invalid project memory")

type MemoryFact = contract.Fact
type ProjectMemory = contract.Store

func normalizeMemoryInput(project, text, source string) (string, string, string, error) {
	project = strings.TrimSpace(project)
	text = strings.TrimSpace(text)
	source = strings.TrimSpace(source)
	if project == "" || text == "" || source == "" {
		return "", "", "", ErrInvalidMemory
	}
	return project, text, source, nil
}

func normalizeRecallLimit(limit int) int {
	if limit <= 0 {
		return DefaultMemoryRecallLimit
	}
	if limit > MaxMemoryRecallLimit {
		return MaxMemoryRecallLimit
	}
	return limit
}
