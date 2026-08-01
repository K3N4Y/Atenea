package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/K3N4Y/atenea/internal/tool"
)

// YoloMode is process-local authority granted only by an explicit interactive
// launch flag. Authorization is immutable; activation may be changed by the UI.
type YoloMode struct {
	mu         sync.RWMutex
	authorized bool
	enabled    bool
}

func NewYoloMode(authorized bool) *YoloMode {
	return &YoloMode{authorized: authorized, enabled: authorized}
}

func (m *YoloMode) Authorized() bool { return m != nil && m.authorized }

func (m *YoloMode) Set(enabled bool) bool {
	if m == nil || (enabled && !m.authorized) {
		return false
	}
	m.mu.Lock()
	m.enabled = enabled
	m.mu.Unlock()
	return true
}

func (m *YoloMode) Enabled() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabled
}

// NewYoloPolicy upgrades Ask to Allow while YOLO is active. Deny remains Deny.
func NewYoloPolicy(base Policy, mode *YoloMode, root, home string) Policy {
	if mode == nil || !mode.Authorized() {
		return base
	}
	return yoloPolicy{base: base, mode: mode, root: root, home: home}
}

type yoloPolicy struct {
	base       Policy
	mode       *YoloMode
	root, home string
}

func (p yoloPolicy) Decide(sessionID string, call tool.Call) Decision {
	if p.mode.Enabled() && call.Name == "bash" && recursiveRMProtectedPath(call.Input, p.root, p.home) {
		return Deny
	}
	decision := p.base.Decide(sessionID, call)
	if decision != Ask || !p.mode.Enabled() {
		return decision
	}
	return Allow
}

func recursiveRMProtectedPath(input []byte, root, home string) bool {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return false
	}
	for _, segment := range shellSegments(in.Command) {
		words := shellWords(segment)
		for len(words) > 0 && (words[0] == "sudo" || words[0] == "command" || words[0] == "env") {
			words = words[1:]
		}
		if len(words) < 2 || filepath.Base(words[0]) != "rm" {
			continue
		}
		recursive := false
		var operands []string
		options := true
		for _, word := range words[1:] {
			if options && word == "--" {
				options = false
				continue
			}
			if options && strings.HasPrefix(word, "-") {
				if strings.Contains(strings.TrimLeft(word, "-"), "r") || strings.Contains(strings.TrimLeft(word, "-"), "R") || word == "--recursive" {
					recursive = true
				}
				continue
			}
			operands = append(operands, word)
		}
		if !recursive {
			continue
		}
		for _, operand := range operands {
			if protectedRMOperand(operand, root, home) {
				return true
			}
		}
	}
	return false
}

func protectedRMOperand(value, root, home string) bool {
	value = strings.ReplaceAll(value, "${HOME}", home)
	value = strings.ReplaceAll(value, "$HOME", home)
	if value == "~" || strings.HasPrefix(value, "~/") {
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	clean := filepath.Clean(value)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		clean = resolved
	}
	home = filepath.Clean(home)
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	return clean == string(os.PathSeparator) || (home != "." && home != "" && clean == home)
}

func shellSegments(command string) []string {
	var segments []string
	var b strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if segment := strings.TrimSpace(b.String()); segment != "" {
			segments = append(segments, segment)
		}
		b.Reset()
	}
	for _, r := range command {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			b.WriteRune(r)
			escaped = true
			continue
		}
		if quote != 0 {
			b.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			b.WriteRune(r)
			continue
		}
		if r == ';' || r == '|' || r == '&' || r == '\n' || r == '\r' {
			flush()
			continue
		}
		b.WriteRune(r)
	}
	flush()
	return segments
}

// shellWords recognizes the ordinary quoting and escaping used in destructive
// commands. Malformed or exotic shell syntax simply fails closed to recognition:
// this is a narrow breaker, not a shell sandbox.
func shellWords(s string) []string {
	var words []string
	var b strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			words = append(words, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' {
			flush()
			continue
		}
		b.WriteRune(r)
	}
	flush()
	return words
}
