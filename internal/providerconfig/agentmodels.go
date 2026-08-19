package providerconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/paths"
)

// AgentModelSelection is an optional provider and required model override for
// one named agent role. An empty Provider means the globally active provider.
type AgentModelSelection struct {
	Provider        string              `json:"provider,omitempty"`
	Model           string              `json:"model"`
	ReasoningEffort llm.ReasoningEffort `json:"reasoning_effort,omitempty"`
}

type agentModelsConfig struct {
	Agents map[string]AgentModelSelection `json:"agents"`
}

func DefaultAgentModelsPath() string {
	path, err := paths.AgentModels()
	if err != nil {
		return filepath.Join(".", "atenea", "agent-models.json")
	}
	return path
}

func loadAgentModels(path string) (map[string]AgentModelSelection, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]AgentModelSelection{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var cfg agentModelsConfig
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode agent model config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, errors.New("decode agent model config: multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode agent model config: %w", err)
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]AgentModelSelection{}
	}
	for name, selection := range cfg.Agents {
		if err := validateAgentSelection(name, selection); err != nil {
			return nil, err
		}
	}
	return cfg.Agents, nil
}

func saveAgentModels(path string, agents map[string]AgentModelSelection) error {
	data, err := json.MarshalIndent(agentModelsConfig{Agents: agents}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent model config: %w", err)
	}
	if err := writeFileAtomic(path, append(data, '\n')); err != nil {
		return fmt.Errorf("save agent model config: %w", err)
	}
	return nil
}

func validateAgentSelection(agentName string, selection AgentModelSelection) error {
	if strings.TrimSpace(agentName) == "" {
		return errors.New("agent name is required")
	}
	if agentName != strings.TrimSpace(agentName) {
		return errors.New("agent name must not have surrounding whitespace")
	}
	if selection.Provider != strings.TrimSpace(selection.Provider) {
		return errors.New("agent model provider must not have surrounding whitespace")
	}
	if strings.TrimSpace(selection.Model) == "" {
		return errors.New("agent model is required")
	}
	if selection.Model != strings.TrimSpace(selection.Model) {
		return errors.New("agent model must not have surrounding whitespace")
	}
	if err := validateReasoningEffort(selection.ReasoningEffort); err != nil {
		return fmt.Errorf("agent %q: %w", agentName, err)
	}
	return nil
}

func cloneAgentModels(src map[string]AgentModelSelection) map[string]AgentModelSelection {
	result := make(map[string]AgentModelSelection, len(src))
	for name, selection := range src {
		result[name] = selection
	}
	return result
}
