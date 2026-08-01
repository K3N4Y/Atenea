package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// AutoAcceptSafe proves that call is one command from the narrow filesystem
// grammar. Anything it cannot prove remains subject to the normal permission gate.
func (bt *BashTool) AutoAcceptSafe(call Call) bool {
	var in struct {
		Command string `json:"command"`
		SlowOK  bool   `json:"slow_ok"`
	}
	if json.Unmarshal(call.Input, &in) != nil || in.Command == "" {
		return false
	}
	argv, ok := safeShellFields(in.Command)
	if !ok || len(argv) < 2 || strings.Contains(argv[0], "/") {
		return false
	}
	args := argv[1:]
	switch argv[0] {
	case "mkdir":
		args, ok = consumeOnlyFlags(args, "-p")
		return ok && len(args) > 0 && bt.safePaths(args, true, false)
	case "touch":
		for _, arg := range args {
			if !bt.safeMutableDestination(arg) {
				return false
			}
		}
		return len(args) > 0
	case "cp":
		return len(args) == 2 && bt.safeRegular(args[0]) && bt.safeDestination(args[1])
	case "mv":
		return len(args) == 2 && bt.safeExistingNoSymlink(args[0]) && bt.safeDestination(args[1])
	case "rm":
		args, ok = consumeOnlyFlags(args, "-f")
		if !ok || len(args) == 0 {
			return false
		}
		for _, arg := range args {
			if !bt.safeRegular(arg) {
				return false
			}
		}
		return true
	case "rmdir":
		for _, arg := range args {
			if !bt.safeDirectory(arg) {
				return false
			}
		}
		return len(args) > 0
	case "sed":
		return bt.safeSed(args)
	default:
		return false
	}
}

func safeShellFields(command string) ([]string, bool) {
	// This is intentionally smaller than Bash's word grammar. Every rejected
	// byte below has syntax or expansion meaning to Bash, even inside some word
	// positions. Keeping it out globally makes the argv proven here identical to
	// the argv bash -c will later pass to the executable.
	const unprovenShellSyntax = "~*?[]{}#"
	if strings.TrimSpace(command) != command || strings.ContainsAny(command, shellMetacharacters+unprovenShellSyntax) {
		return nil, false
	}
	var out []string
	var b strings.Builder
	var quote rune
	for _, r := range command {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ' ', '\t':
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, false
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out, len(out) > 0 && !strings.Contains(out[0], "=")
}

func consumeOnlyFlags(args []string, allowed string) ([]string, bool) {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] != allowed {
			return nil, false
		}
		args = args[1:]
	}
	return args, true
}

func (bt *BashTool) resolveSafe(path string, creation bool) (string, bool) {
	if path == "" || strings.HasPrefix(path, "-") || filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return "", false
	}
	root, err := filepath.EvalSymlinks(bt.Root)
	if err != nil {
		return "", false
	}
	current := root
	parts := strings.Split(path, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if creation && os.IsNotExist(err) {
				return filepath.Join(root, path), true
			}
			return "", false
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", false
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", false
		}
	}
	return current, true
}

func (bt *BashTool) safePaths(paths []string, creation, requireDir bool) bool {
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		resolved, ok := bt.resolveSafe(path, creation)
		if !ok {
			return false
		}
		if requireDir {
			info, err := os.Stat(resolved)
			if err != nil || !info.IsDir() {
				return false
			}
		}
	}
	return true
}
func (bt *BashTool) safeRegular(path string) bool {
	p, ok := bt.resolveSafe(path, false)
	if !ok {
		return false
	}
	i, e := os.Stat(p)
	return e == nil && i.Mode().IsRegular()
}
func (bt *BashTool) safeExistingNoSymlink(path string) bool {
	_, ok := bt.resolveSafe(path, false)
	return ok
}
func (bt *BashTool) safeDirectory(path string) bool {
	p, ok := bt.resolveSafe(path, false)
	if !ok {
		return false
	}
	i, e := os.Stat(p)
	if e != nil || !i.IsDir() {
		return false
	}
	entries, e := os.ReadDir(p)
	return e == nil && len(entries) == 0
}
func (bt *BashTool) safeDestination(path string) bool {
	p, ok := bt.resolveSafe(path, true)
	if !ok {
		return false
	}
	i, e := os.Stat(p)
	return os.IsNotExist(e) || (e == nil && !i.IsDir() && i.Mode().IsRegular() && hasSingleLink(i))
}

func (bt *BashTool) safeMutableDestination(path string) bool {
	p, ok := bt.resolveSafe(path, true)
	if !ok {
		return false
	}
	i, err := os.Stat(p)
	if os.IsNotExist(err) {
		return true
	}
	return err == nil && i.Mode().IsRegular() && hasSingleLink(i)
}

func (bt *BashTool) safeSed(args []string) bool {
	inPlace := false
	if len(args) > 0 && args[0] == "-i" {
		inPlace = true
		args = args[1:]
	}
	if len(args) < 2 || strings.HasPrefix(args[0], "-") {
		return false
	}
	script := args[0]
	if len(script) < 4 || script[0] != 's' {
		return false
	}
	d := script[1]
	if d == '\\' || d == '\n' || d == '\r' {
		return false
	}
	parts := strings.Split(script[2:], string(d))
	if len(parts) != 3 || (parts[2] != "" && parts[2] != "g") || strings.ContainsAny(parts[0]+parts[1], "\n\r") {
		return false
	}
	_ = inPlace
	for _, file := range args[1:] {
		if inPlace {
			if !bt.safeMutableExisting(file) {
				return false
			}
		} else if !bt.safeRegular(file) {
			return false
		}
	}
	return true
}

func (bt *BashTool) safeMutableExisting(path string) bool {
	p, ok := bt.resolveSafe(path, false)
	if !ok {
		return false
	}
	i, err := os.Stat(p)
	return err == nil && i.Mode().IsRegular() && hasSingleLink(i)
}
