// Package editmode contains the model-facing edit mode contracts and their
// resolution rules. Behavior is ported from oh-my-pi@5af71dc9cf132538e072806424f71f43f734d9ae
// packages/coding-agent/src/utils/edit-mode.ts.
package editmode

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Mode string

const (
	Hashline   Mode = "hashline"
	ApplyPatch Mode = "apply_patch"
	Patch      Mode = "patch"
	Replace    Mode = "replace"
)

type Config struct {
	Model            string
	ModelVariants    map[string]Mode
	ModelVariant     string
	Setting          string
	Fuzzy            bool
	Threshold        float64
	EnforceSeenLines bool
}

func Normalize(value string) (Mode, bool) {
	if value == "auto" || value == "" {
		return "", false
	}
	switch Mode(value) {
	case Hashline, ApplyPatch, Patch, Replace:
		return Mode(value), true
	default:
		return "", false
	}
}

// Resolve applies upstream precedence: model variant, PI_EDIT_VARIANT,
// edit.mode, hashline default, then the non-strict model fallback.
func Resolve(c Config, getenv func(string) string) (Mode, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	var mode Mode
	var ok, explicit bool
	variant := c.ModelVariant
	if configured, exists := c.ModelVariants[c.Model]; exists {
		variant = string(configured)
	}
	if variant != "" && variant != "auto" {
		mode, ok = Normalize(variant)
		if !ok {
			return "", fmt.Errorf("invalid model edit variant: %s", variant)
		}
		explicit = true
	}
	if !ok && getenv("PI_EDIT_VARIANT") != "" && getenv("PI_EDIT_VARIANT") != "auto" {
		mode, ok = Normalize(getenv("PI_EDIT_VARIANT"))
		if !ok {
			return "", fmt.Errorf("Invalid PI_EDIT_VARIANT: %s", getenv("PI_EDIT_VARIANT"))
		}
		explicit = true
	}
	if !ok && c.Setting != "" && c.Setting != "auto" {
		mode, ok = Normalize(c.Setting)
		if !ok {
			return "", fmt.Errorf("invalid edit.mode: %s", c.Setting)
		}
	}
	if !ok {
		mode = Hashline
	}
	strict, err := parseStrict(getenv("PI_STRICT_EDIT_MODE"))
	if err != nil {
		return "", err
	}
	if mode == Hashline && !strict && !explicit {
		model := strings.ToLower(c.Model)
		for _, excluded := range []string{"kimi", "mimo", "deepseek-v4-flash", "step-3.7-flash"} {
			if strings.Contains(model, excluded) {
				return Replace, nil
			}
		}
	}
	return mode, nil
}

func ResolveFuzzy(c Config, getenv func(string) string) (bool, float64, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if c.Threshold < 0 || c.Threshold > 1 {
		return false, 0, fmt.Errorf("invalid edit.fuzzyThreshold: %v", c.Threshold)
	}
	fuzzy := c.Fuzzy
	switch value := getenv("PI_EDIT_FUZZY"); value {
	case "", "auto":
	case "1", "true":
		fuzzy = true
	case "0", "false":
		fuzzy = false
	default:
		return false, 0, fmt.Errorf("Invalid PI_EDIT_FUZZY: %s", value)
	}
	threshold := c.Threshold
	if threshold == 0 {
		threshold = 0.95
	}
	if value := getenv("PI_EDIT_FUZZY_THRESHOLD"); value != "" && value != "auto" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || parsed < 0 || parsed > 1 {
			return false, 0, fmt.Errorf("Invalid PI_EDIT_FUZZY_THRESHOLD: %s", value)
		}
		threshold = parsed
	}
	return fuzzy, threshold, nil
}

func parseStrict(value string) (bool, error) {
	switch value {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, fmt.Errorf("Invalid PI_STRICT_EDIT_MODE: %s", value)
	}
}
