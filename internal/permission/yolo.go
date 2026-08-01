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
		words = unwrapShellCommand(words)
		if len(words) < 2 || filepath.Base(words[0].value()) != "rm" {
			continue
		}
		recursive := false
		var operands []shellWord
		options := true
		for _, word := range words[1:] {
			value := word.value()
			if options && value == "--" {
				options = false
				continue
			}
			if options && strings.HasPrefix(value, "-") {
				if strings.Contains(strings.TrimLeft(value, "-"), "r") || strings.Contains(strings.TrimLeft(value, "-"), "R") || value == "--recursive" {
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

func protectedRMOperand(word shellWord, root, home string) bool {
	value := word.expandHome(home)
	if word.tildeExpandable() && (value == "~" || strings.HasPrefix(value, "~/")) {
		if value == "~" {
			value = home
		} else {
			value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
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

func unwrapShellCommand(words []shellWord) []shellWord {
	for {
		for len(words) > 0 && shellAssignment(words[0].value()) {
			words = words[1:]
		}
		if len(words) == 0 {
			return nil
		}
		wrapper := filepath.Base(words[0].value())
		if wrapper != "env" && wrapper != "sudo" && wrapper != "command" {
			return words
		}
		words = consumeWrapperOptions(words[1:], wrapper)
	}
}

func consumeWrapperOptions(words []shellWord, wrapper string) []shellWord {
	for len(words) > 0 {
		option := words[0].value()
		if option == "--" {
			return words[1:]
		}
		if option == "-" || !strings.HasPrefix(option, "-") {
			return words
		}
		words = words[1:]
		if wrapperOptionTakesArgument(wrapper, option) && len(words) > 0 {
			words = words[1:]
		}
	}
	return nil
}

func wrapperOptionTakesArgument(wrapper, option string) bool {
	if strings.Contains(option, "=") || (strings.HasPrefix(option, "-") && !strings.HasPrefix(option, "--") && len(option) > 2) {
		return false
	}
	withArgument := map[string]map[string]bool{
		"env": {
			"-u": true, "--unset": true, "-C": true, "--chdir": true,
			"-S": true, "--split-string": true, "--argv0": true,
		},
		"sudo": {
			"-u": true, "--user": true, "-g": true, "--group": true,
			"-h": true, "--host": true, "-p": true, "--prompt": true,
			"-C": true, "--chdir": true, "-T": true, "--command-timeout": true,
			"-r": true, "--role": true, "-t": true, "--type": true,
		},
	}
	return withArgument[wrapper][option]
}

func shellAssignment(value string) bool {
	equals := strings.IndexByte(value, '=')
	if equals <= 0 {
		return false
	}
	for i, r := range value[:equals] {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
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
type shellWordPart struct {
	text                string
	parameterExpandable bool
}

type shellWord struct {
	parts            []shellWordPart
	tildeExpansionOK bool
	started          bool
}

func (w shellWord) value() string {
	var b strings.Builder
	for _, part := range w.parts {
		b.WriteString(part.text)
	}
	return b.String()
}

func (w shellWord) expandHome(home string) string {
	var b strings.Builder
	for _, part := range w.parts {
		value := part.text
		if part.parameterExpandable {
			value = strings.ReplaceAll(value, "${HOME}", home)
			value = strings.ReplaceAll(value, "$HOME", home)
		}
		b.WriteString(value)
	}
	return b.String()
}

func (w shellWord) tildeExpandable() bool {
	return w.tildeExpansionOK
}

func shellWords(s string) []shellWord {
	var words []shellWord
	var word shellWord
	var quote rune
	escaped := false
	partBoundary := false
	appendRune := func(r rune, parameterExpandable, tildeExpandable bool) {
		if !word.started {
			word.tildeExpansionOK = tildeExpandable
		}
		word.started = true
		if !partBoundary && len(word.parts) > 0 && word.parts[len(word.parts)-1].parameterExpandable == parameterExpandable {
			word.parts[len(word.parts)-1].text += string(r)
			return
		}
		partBoundary = false
		word.parts = append(word.parts, shellWordPart{
			text:                string(r),
			parameterExpandable: parameterExpandable,
		})
	}
	flush := func() {
		if word.started {
			words = append(words, word)
			word = shellWord{}
			partBoundary = false
		}
	}
	for _, r := range s {
		if escaped {
			appendRune(r, false, false)
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
				partBoundary = true
			} else {
				appendRune(r, quote == '"', false)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			word.started = true
			partBoundary = true
			continue
		}
		if r == ' ' || r == '\t' {
			flush()
			continue
		}
		appendRune(r, true, !word.started)
	}
	flush()
	return words
}
