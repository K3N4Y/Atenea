package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/K3N4Y/atenea/internal/tool"
	"mvdan.cc/sh/v3/syntax"
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
	file, err := syntax.NewParser().Parse(strings.NewReader(in.Command), "")
	if err != nil {
		return false
	}

	blocked := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if node == nil || blocked {
			return false
		}
		// Declaring a function does not execute its body. Resolving later function
		// calls would require executing shell state and is outside this breaker.
		if _, ok := node.(*syntax.FuncDecl); ok {
			return false
		}
		call, ok := node.(*syntax.CallExpr)
		if ok && recursiveRMCall(call, in.Command, root, home) {
			blocked = true
			return false
		}
		return true
	})
	return blocked
}

type shellWord struct {
	value           string
	tildeExpandable bool
	static          bool
}

func recursiveRMCall(call *syntax.CallExpr, source, root, home string) bool {
	words := make([]shellWord, 0, len(call.Args))
	for _, arg := range call.Args {
		word, ok := staticShellWord(arg, source, home)
		if !ok {
			words = append(words, shellWord{})
			continue
		}
		words = append(words, word)
	}
	words = unwrapShellCommand(words)
	if len(words) < 2 || !words[0].static || filepath.Base(words[0].value) != "rm" {
		return false
	}

	recursive := false
	options := true
	var operands []shellWord
	for _, word := range words[1:] {
		if !word.static {
			continue
		}
		if options && word.value == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(word.value, "--") {
			recursive = recursive || word.value == "--recursive"
			continue
		}
		if options && len(word.value) > 1 && word.value[0] == '-' {
			recursive = recursive || strings.ContainsAny(word.value[1:], "rR")
			continue
		}
		operands = append(operands, word)
	}
	if !recursive {
		return false
	}
	for _, operand := range operands {
		if protectedRMOperand(operand, root, home) {
			return true
		}
	}
	return false
}

func staticShellWord(word *syntax.Word, source, home string) (shellWord, bool) {
	var b strings.Builder
	for _, part := range word.Parts {
		if !appendStaticWordPart(&b, part, home) {
			return shellWord{}, false
		}
	}
	tilde := false
	if len(word.Parts) > 0 {
		if lit, ok := word.Parts[0].(*syntax.Lit); ok {
			offset := int(lit.ValuePos.Offset())
			tilde = strings.HasPrefix(lit.Value, "~") && offset < len(source) && source[offset] == '~'
		}
	}
	return shellWord{value: b.String(), tildeExpandable: tilde, static: true}, true
}

func appendStaticWordPart(b *strings.Builder, part syntax.WordPart, home string) bool {
	switch part := part.(type) {
	case *syntax.Lit:
		b.WriteString(part.Value)
		return true
	case *syntax.SglQuoted:
		if part.Dollar {
			return false
		}
		b.WriteString(part.Value)
		return true
	case *syntax.DblQuoted:
		if part.Dollar {
			return false
		}
		for _, nested := range part.Parts {
			if !appendStaticWordPart(b, nested, home) {
				return false
			}
		}
		return true
	case *syntax.ParamExp:
		if part.Param == nil || part.Param.Value != "HOME" || part.Flags != nil || part.Excl || part.Length || part.Width || part.IsSet || part.NestedParam != nil || part.Index != nil || len(part.Modifiers) != 0 || part.Slice != nil || part.Repl != nil || part.Names != 0 || part.Exp != nil {
			return false
		}
		b.WriteString(home)
		return true
	default:
		return false
	}
}

func protectedRMOperand(word shellWord, root, home string) bool {
	value := word.value
	if word.tildeExpandable && (value == "~" || strings.HasPrefix(value, "~/")) {
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
	for len(words) > 0 {
		wrapper := filepath.Base(words[0].value)
		if wrapper != "env" && wrapper != "sudo" && wrapper != "command" {
			return words
		}
		var terminal bool
		words, terminal = consumeWrapperOptions(words[1:], wrapper)
		if terminal {
			return nil
		}
		if wrapper == "env" {
			for len(words) > 0 && shellAssignment(words[0].value) {
				words = words[1:]
			}
		}
	}
	return nil
}

func consumeWrapperOptions(words []shellWord, wrapper string) ([]shellWord, bool) {
	for len(words) > 0 {
		option := words[0].value
		if option == "--" {
			return words[1:], false
		}
		if option == "-" || !strings.HasPrefix(option, "-") {
			return words, false
		}
		if wrapperTerminalOption(wrapper, option) {
			return nil, true
		}
		words = words[1:]
		if wrapperOptionTakesArgument(wrapper, option) {
			if len(words) == 0 {
				return nil, false
			}
			words = words[1:]
		}
	}
	return nil, false
}

func wrapperTerminalOption(wrapper, option string) bool {
	switch wrapper {
	case "env":
		return option == "--help" || option == "--version"
	case "command":
		return option == "--help" || strings.HasPrefix(option, "-") && !strings.HasPrefix(option, "--") && strings.ContainsAny(option[1:], "vV")
	case "sudo":
		return option == "--help" || option == "-V" || option == "--version" || option == "-l" || option == "--list" || option == "-v" || option == "--validate" || option == "-k" || option == "--reset-timestamp" || option == "-K" || option == "--remove-timestamp"
	default:
		return false
	}
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
