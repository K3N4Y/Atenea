package tool

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/K3N4Y/atenea/agentcore/permission"
)

const (
	defaultASTMaxResults = 50
	maxASTResults        = 500
)

// ASTTool delegates language parsing and structural matching to ast-grep while
// retaining Atenea's workspace and permission boundaries.
type ASTTool struct {
	root       string
	mu         sync.Mutex
	commandFor func() (string, error)
}

type astInput struct {
	Operation   string `json:"operation"`
	Path        string `json:"path"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	Language    string `json:"language"`
	MaxResults  int    `json:"max_results"`
	Apply       bool   `json:"apply"`
}

type astMatch struct {
	Text        string `json:"text"`
	File        string `json:"file"`
	Replacement string `json:"replacement"`
	Range       struct {
		Start struct {
			Line   int `json:"line"`
			Column int `json:"column"`
		} `json:"start"`
		End struct {
			Line   int `json:"line"`
			Column int `json:"column"`
		} `json:"end"`
	} `json:"range"`
}

func NewASTTool(root string) *ASTTool {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = filepath.Clean(root)
	}
	return &ASTTool{root: abs, commandFor: astGrepCommand}
}

func (*ASTTool) Name() string { return "ast" }

//go:embed ast.txt
var astDescription string

func (*ASTTool) Description() string { return astDescription }
func (*ASTTool) Effects() Effects    { return NoEffects }
func (*ASTTool) CallEffects(call Call) Effects {
	var in astInput
	if json.Unmarshal(call.Input, &in) == nil && in.Operation == "rewrite" && in.Apply {
		return WritesFiles
	}
	return NoEffects
}
func (t *ASTTool) GrantRule(call Call) (permission.Rule, bool) {
	if t.CallEffects(call) != WritesFiles {
		return permission.Rule{}, false
	}
	return permission.Rule{Tool: t.Name(), Prefix: "rewrite"}, true
}
func (*ASTTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"operation":{"type":"string","enum":["search","rewrite"]},"path":{"type":"string","minLength":1,"description":"Workspace-relative file or directory."},"pattern":{"type":"string","minLength":1,"description":"ast-grep structural pattern, including metavariables such as $X."},"replacement":{"type":"string","description":"Structural replacement for rewrite."},"language":{"type":"string","description":"Optional ast-grep language when it cannot be inferred."},"max_results":{"type":"integer","minimum":1,"maximum":500},"apply":{"type":"boolean","description":"For rewrite: false previews; true applies every proposed replacement."}},"required":["operation","path","pattern"],"additionalProperties":false}`)
}

func (t *ASTTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var in astInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{}, fmt.Errorf("ast: invalid input: %w", err)
	}
	if err := validateASTInput(in); err != nil {
		return Result{}, err
	}
	path, err := sandboxJoin(t.root, in.Path, "ast")
	if err != nil {
		return Result{}, err
	}
	if err := rejectRealPathOutside(t.root, path, in.Path, "ast"); err != nil {
		return Result{}, err
	}
	if _, err := os.Stat(path); err != nil {
		return Result{}, fmt.Errorf("ast: path: %w", err)
	}
	if in.MaxResults == 0 {
		in.MaxResults = defaultASTMaxResults
	}
	if in.Operation == "rewrite" && in.Apply {
		t.mu.Lock()
		defer t.mu.Unlock()
	}
	matches, stderr, err := t.run(ctx, in, path, false)
	if err != nil {
		return Result{}, formatASTError(err, stderr)
	}
	if err := t.validateMatches(matches); err != nil {
		return Result{}, err
	}
	visible := matches
	truncated := len(visible) > in.MaxResults
	if truncated {
		visible = visible[:in.MaxResults]
	}
	if in.Operation == "search" {
		return Result{Output: t.formatMatches(visible, len(matches), truncated, false)}, nil
	}
	if !in.Apply || len(matches) == 0 {
		return Result{Output: t.formatMatches(visible, len(matches), truncated, true)}, nil
	}
	_, stderr, err = t.run(ctx, in, path, true)
	if err != nil {
		return Result{}, formatASTError(err, stderr)
	}
	files := uniqueASTFiles(t.root, matches)
	return Result{Output: fmt.Sprintf("Applied %d structural replacement(s) in %d file(s):\n%s", len(matches), len(files), strings.Join(files, "\n"))}, nil
}

func validateASTInput(in astInput) error {
	if strings.TrimSpace(in.Pattern) == "" {
		return errors.New("ast: pattern is required")
	}
	if in.Path == "" {
		return errors.New("ast: path is required")
	}
	if in.MaxResults < 0 || in.MaxResults > maxASTResults {
		return fmt.Errorf("ast: max_results must be between 1 and %d", maxASTResults)
	}
	switch in.Operation {
	case "search":
		if in.Replacement != "" || in.Apply {
			return errors.New("ast: replacement and apply are only valid for rewrite")
		}
	case "rewrite":
		if in.Replacement == "" {
			return errors.New("ast: rewrite requires replacement")
		}
	default:
		return fmt.Errorf("ast: unsupported operation %q", in.Operation)
	}
	return nil
}

func astGrepCommand() (string, error) {
	for _, name := range []string{"ast-grep", "sg"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("ast-grep is not installed; install @ast-grep/cli or ast-grep")
}

func (t *ASTTool) run(ctx context.Context, in astInput, path string, apply bool) ([]astMatch, string, error) {
	binary, err := t.commandFor()
	if err != nil {
		return nil, "", fmt.Errorf("ast: %w", err)
	}
	args := []string{"run", "--pattern", in.Pattern, "--color", "never"}
	if !apply {
		args = append(args, "--json=compact")
	}
	if in.Operation == "rewrite" {
		args = append(args, "--rewrite", in.Replacement)
		if apply {
			args = append(args, "--update-all")
		}
	}
	if in.Language != "" {
		args = append(args, "--lang", in.Language)
	}
	args = append(args, path)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = t.root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, stderr.String(), ctx.Err()
		}
		return nil, stderr.String(), err
	}
	if apply {
		return nil, stderr.String(), nil
	}
	var matches []astMatch
	if err := json.Unmarshal(stdout.Bytes(), &matches); err != nil {
		return nil, stderr.String(), fmt.Errorf("ast: decode ast-grep output: %w", err)
	}
	return matches, stderr.String(), nil
}

func formatASTError(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, stderr)
}

func (t *ASTTool) validateMatches(matches []astMatch) error {
	for _, match := range matches {
		abs, err := sandboxJoin(t.root, match.File, "ast")
		if err != nil {
			return err
		}
		if err := rejectMutableAlias(t.root, abs, match.File, "ast"); err != nil {
			return err
		}
	}
	return nil
}

func (t *ASTTool) formatMatches(matches []astMatch, total int, truncated, rewrite bool) string {
	if total == 0 {
		return "No structural matches found."
	}
	var b strings.Builder
	if rewrite {
		fmt.Fprintf(&b, "Proposed %d structural replacement(s) (dry run):\n", total)
	} else {
		fmt.Fprintf(&b, "%d structural match(es):\n", total)
	}
	for _, match := range matches {
		rel, _ := filepath.Rel(t.root, match.File)
		fmt.Fprintf(&b, "%s:%d:%d: %s", filepath.ToSlash(rel), match.Range.Start.Line+1, match.Range.Start.Column+1, strings.TrimSpace(match.Text))
		if rewrite {
			fmt.Fprintf(&b, "\n  => %s", strings.TrimSpace(match.Replacement))
		}
		b.WriteByte('\n')
	}
	if truncated {
		fmt.Fprintf(&b, "Result limit reached; showing %d of %d matches. Narrow path or increase max_results.\n", len(matches), total)
	}
	return strings.TrimSpace(b.String())
}

func uniqueASTFiles(root string, matches []astMatch) []string {
	set := make(map[string]struct{})
	for _, match := range matches {
		rel, _ := filepath.Rel(root, match.File)
		set[filepath.ToSlash(rel)] = struct{}{}
	}
	files := make([]string, 0, len(set))
	for file := range set {
		files = append(files, file)
	}
	sort.Strings(files)
	return files
}
