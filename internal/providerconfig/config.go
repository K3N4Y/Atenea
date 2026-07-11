package providerconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const OpenAICompatible = "openai-compatible"

type Provider struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Type                string   `json:"type"`
	BaseURL             string   `json:"base_url"`
	APIKeyEnv           string   `json:"api_key_env,omitempty"`
	OpenRouterReasoning bool     `json:"openrouter_reasoning,omitempty"`
	Models              []string `json:"models,omitempty"`
}

type Selection struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type Config struct {
	Providers []Provider `json:"providers"`
	Selected  Selection  `json:"selected,omitempty"`
}

func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".", "atenea", "providers.json")
	}
	return filepath.Join(dir, "atenea", "providers.json")
}

func DefaultCachePath() string {
	return filepath.Join(filepath.Dir(DefaultPath()), "models-cache.json")
}

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode provider config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Config{}, errors.New("decode provider config: multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode provider config: %w", err)
	}
	if err := normalizeAndValidate(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := normalizeAndValidate(&cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode provider config: %w", err)
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".providers-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace provider config: %w", err)
	}
	return nil
}

func normalizeAndValidate(cfg *Config) error {
	seen := make(map[string]struct{}, len(cfg.Providers))
	for i := range cfg.Providers {
		provider := &cfg.Providers[i]
		provider.ID = strings.TrimSpace(provider.ID)
		provider.Name = strings.TrimSpace(provider.Name)
		provider.Type = strings.TrimSpace(provider.Type)
		provider.BaseURL = strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
		provider.APIKeyEnv = strings.TrimSpace(provider.APIKeyEnv)
		if provider.ID == "" || provider.Name == "" || provider.BaseURL == "" {
			return fmt.Errorf("provider %d requires id, name, and base_url", i)
		}
		if provider.Type != OpenAICompatible {
			return fmt.Errorf("provider %q has unsupported type %q", provider.ID, provider.Type)
		}
		if _, ok := seen[provider.ID]; ok {
			return fmt.Errorf("duplicate provider id %q", provider.ID)
		}
		seen[provider.ID] = struct{}{}
		models := make([]string, 0, len(provider.Models))
		modelSeen := map[string]struct{}{}
		for _, model := range provider.Models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, ok := modelSeen[model]; ok {
				continue
			}
			modelSeen[model] = struct{}{}
			models = append(models, model)
		}
		provider.Models = models
	}
	cfg.Selected.Provider = strings.TrimSpace(cfg.Selected.Provider)
	cfg.Selected.Model = strings.TrimSpace(cfg.Selected.Model)
	if (cfg.Selected.Provider == "") != (cfg.Selected.Model == "") {
		return errors.New("selected provider and model must both be set")
	}
	if cfg.Selected.Provider != "" {
		if _, ok := seen[cfg.Selected.Provider]; !ok {
			return fmt.Errorf("selected provider %q is not configured", cfg.Selected.Provider)
		}
	}
	return nil
}
