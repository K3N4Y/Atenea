package tool

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	defaultGlobLimit = 200
	maxGlobLimit     = 5000
	ripgrepStderrMax = 8 * 1024
)

type GlobTool struct {
	Root         string
	Searcher     GlobSearcher
	DefaultLimit int
	MaxLimit     int
}

type GlobSearch struct {
	Cwd     string
	Pattern string
	Limit   int
}

type GlobEntry struct {
	Path string
}

type GlobSearchResult struct {
	Entries   []GlobEntry
	Truncated bool
}

type GlobSearcher interface {
	Glob(ctx context.Context, input GlobSearch) (GlobSearchResult, error)
}

func NewGlobTool(root string) *GlobTool {
	return &GlobTool{
		Root:         root,
		Searcher:     &RipgrepGlobSearcher{},
		DefaultLimit: defaultGlobLimit,
		MaxLimit:     maxGlobLimit,
	}
}

func (*GlobTool) Name() string { return "glob" }

//go:embed glob.txt
var globDescription string

func (*GlobTool) Description() string { return globDescription }

func (*GlobTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Patron glob para encontrar archivos, con semantica de ripgrep (por ejemplo \"*.go\", \"**/*.go\" o \"internal/**/*.go\")."},"path":{"type":"string","description":"Directorio relativo al workspace donde buscar. Default: \".\"."},"limit":{"type":"integer","minimum":1,"description":"Maximo de resultados a devolver."}},"required":["pattern"]}`)
}

func (gt *GlobTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Limit   *int   `json:"limit"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("glob: input invalido: %w", err)
	}
	if in.Pattern == "" {
		return Result{}, fmt.Errorf("glob: pattern requerido")
	}

	limit := gt.defaultLimit()
	if in.Limit != nil {
		limit = *in.Limit
	}
	if limit <= 0 {
		return Result{}, fmt.Errorf("glob: limit debe ser positivo")
	}
	maxLimit := gt.maxLimit()
	if limit > maxLimit {
		return Result{}, fmt.Errorf("glob: limit no puede exceder %d", maxLimit)
	}

	paths, truncated, err := gt.Files(ctx, in.Pattern, in.Path, limit)
	if err != nil {
		return Result{}, err
	}
	return Result{Output: formatGlobOutput(paths, truncated, limit)}, nil
}

// Files ejecuta la busqueda y devuelve las rutas relativas al workspace mas si
// el resultado quedo truncado. Execute las formatea para el modelo; el binding
// ListProjectFiles (el @-menu de archivos del composer) consume el slice tal
// cual. Un pattern vacio lista todos los archivos (respetando .gitignore); un
// limit <= 0 usa el default del tool. Comparte el sandbox y la relativizacion
// con Execute, que ademas valida pattern y rango de limit antes de llamar aqui.
func (gt *GlobTool) Files(ctx context.Context, pattern, path string, limit int) ([]string, bool, error) {
	if limit <= 0 {
		limit = gt.defaultLimit()
	}
	searchPath := path
	if searchPath == "" {
		searchPath = "."
	}
	cwd, err := sandboxJoin(gt.Root, searchPath, "glob")
	if err != nil {
		return nil, false, err
	}

	searcher := gt.searcher()
	if isRipgrepGlobSearcher(searcher) {
		if err := rejectRealPathOutside(gt.Root, cwd, searchPath, "glob"); err != nil {
			return nil, false, err
		}
	}

	result, err := searcher.Glob(ctx, GlobSearch{Cwd: cwd, Pattern: pattern, Limit: limit})
	if err != nil {
		return nil, false, fmt.Errorf("glob: %w", err)
	}

	paths, err := gt.workspaceRelativePaths(cwd, result.Entries)
	if err != nil {
		return nil, false, err
	}
	return paths, result.Truncated, nil
}

func (gt *GlobTool) defaultLimit() int {
	if gt.DefaultLimit > 0 {
		return gt.DefaultLimit
	}
	return defaultGlobLimit
}

func (gt *GlobTool) maxLimit() int {
	if gt.MaxLimit > 0 {
		return gt.MaxLimit
	}
	return maxGlobLimit
}

func (gt *GlobTool) searcher() GlobSearcher {
	if gt.Searcher != nil {
		return gt.Searcher
	}
	return &RipgrepGlobSearcher{}
}

func isRipgrepGlobSearcher(searcher GlobSearcher) bool {
	switch searcher.(type) {
	case *RipgrepGlobSearcher:
		return true
	default:
		return false
	}
}

func (gt *GlobTool) workspaceRelativePaths(cwd string, entries []GlobEntry) ([]string, error) {
	rootAbs, err := filepath.Abs(gt.Root)
	if err != nil {
		rootAbs = filepath.Clean(gt.Root)
	}
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		cwdAbs = filepath.Clean(cwd)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		rel := cleanGlobEntryPath(entry.Path)
		abs := filepath.Clean(filepath.Join(cwdAbs, filepath.FromSlash(rel)))
		if !insideRoot(rootAbs, abs) {
			return nil, fmt.Errorf("glob: resultado fuera del workspace: %s", entry.Path)
		}
		workspaceRel, err := filepath.Rel(rootAbs, abs)
		if err != nil {
			return nil, fmt.Errorf("glob: no se pudo relativizar %s: %w", entry.Path, err)
		}
		paths = append(paths, filepath.ToSlash(workspaceRel))
	}
	return paths, nil
}

func formatGlobOutput(paths []string, truncated bool, limit int) string {
	if len(paths) == 0 {
		return "No files found"
	}
	output := strings.Join(paths, "\n")
	if truncated {
		output += fmt.Sprintf("\n\n[Limit reached: showing first %d files. Use a narrower pattern or higher limit.]", limit)
	}
	return output
}

type RipgrepGlobSearcher struct {
	Binary string
	Runner lineRunner
}

type lineRunner interface {
	RunLines(ctx context.Context, cwd, binary string, args []string, limit int) (lines []string, truncated bool, err error)
}

func (s *RipgrepGlobSearcher) Glob(ctx context.Context, input GlobSearch) (GlobSearchResult, error) {
	binary := s.Binary
	if binary == "" {
		binary = "rg"
	}
	runner := s.Runner
	if runner == nil {
		runner = execLineRunner{}
	}
	// Un pattern vacio lista todos los archivos: se omite el include --glob
	// porque un include (p. ej. "*") des-ignoraria lo que .gitignore excluye.
	// --hidden: without it rg silently skips dot-dirs, so patterns like
	// ".okf/*.md" match nothing. With it, .git must be excluded explicitly
	// (git never ignores itself); node_modules se excluye porque solo esta en
	// .gitignore por convencion, no garantizado.
	args := []string{"--no-config", "--files", "--hidden", "--glob=!**/.git/**", "--glob=!**/node_modules/**", "."}
	if input.Pattern != "" {
		args = []string{"--no-config", "--files", "--hidden", "--glob=" + input.Pattern, "--glob=!**/.git/**", "--glob=!**/node_modules/**", "."}
	}
	lines, truncated, err := runner.RunLines(ctx, input.Cwd, binary, args, input.Limit)
	if err != nil {
		var exitErr *ripgrepExitError
		if errors.As(err, &exitErr) && exitErr.Code == 1 {
			return GlobSearchResult{}, nil
		}
		return GlobSearchResult{}, fmt.Errorf("ripgrep failed: %w", err)
	}

	entries := make([]GlobEntry, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, GlobEntry{Path: cleanGlobEntryPath(line)})
	}
	return GlobSearchResult{Entries: entries, Truncated: truncated}, nil
}

type execLineRunner struct{}

func (execLineRunner) RunLines(ctx context.Context, cwd, binary string, args []string, limit int) ([]string, bool, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = cwd

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}

	stderrCh := make(chan string, 1)
	go func() {
		stderrCh <- readCappedString(stderr, ripgrepStderrMax)
	}()

	lines, truncated, scanErr := readLimitedLines(stdout, limit)
	if truncated && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	stderrText := <-stderrCh
	if scanErr != nil {
		return nil, false, scanErr
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if truncated {
		return lines, true, nil
	}
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			return nil, false, &ripgrepExitError{Code: exitErr.ExitCode(), Stderr: stderrText, Err: waitErr}
		}
		return nil, false, waitErr
	}
	return lines, false, nil
}

func readLimitedLines(r io.Reader, limit int) ([]string, bool, error) {
	scanner := bufio.NewScanner(r)
	lines := make([]string, 0, limit)
	for scanner.Scan() {
		if len(lines) >= limit {
			return lines, true, nil
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	return lines, false, nil
}

type ripgrepExitError struct {
	Code   int
	Stderr string
	Err    error
}

func (e *ripgrepExitError) Error() string {
	detail := strings.TrimSpace(limitString(e.Stderr, ripgrepStderrMax))
	if detail == "" {
		return fmt.Sprintf("exit %d", e.Code)
	}
	return fmt.Sprintf("exit %d: %s", e.Code, detail)
}

func (e *ripgrepExitError) Unwrap() error { return e.Err }

func cleanGlobEntryPath(path string) string {
	path = strings.ReplaceAll(path, `\`, `/`)
	for {
		next := strings.TrimPrefix(path, "./")
		next = strings.TrimPrefix(next, "/")
		if next == path {
			return next
		}
		path = next
	}
}

func limitString(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

func readCappedString(r io.Reader, n int) string {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		read, err := r.Read(buf)
		if read > 0 && b.Len() < n {
			remaining := n - b.Len()
			if read > remaining {
				read = remaining
			}
			b.Write(buf[:read])
		}
		if err != nil {
			return b.String()
		}
	}
}
