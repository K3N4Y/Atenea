package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	defaultGrepLimit = 100
	maxGrepLineRunes = 2000
	maxRgStderrRunes = 1000
)

type Searcher interface {
	Grep(ctx context.Context, req GrepRequest) (GrepResult, error)
}

type GrepRequest struct {
	Root    string
	Path    string
	Pattern string
	Include string
	Limit   int
}

type GrepMatch struct {
	Path string
	Line int
	Text string
}

type GrepResult struct {
	Matches   []GrepMatch
	Truncated bool
}

type RgSearcher struct {
	Binary string
}

func NewRgSearcher() *RgSearcher {
	return &RgSearcher{Binary: "rg"}
}

type GrepInvalidPatternError struct {
	Pattern string
	Detail  string
}

func (e *GrepInvalidPatternError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("grep: patron regex invalido: %q", e.Pattern)
	}
	return fmt.Sprintf("grep: patron regex invalido %q: %s", e.Pattern, e.Detail)
}

type GrepUnavailableError struct {
	Binary string
	Err    error
}

func (e *GrepUnavailableError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("grep: ripgrep no disponible: %s", e.Binary)
	}
	return fmt.Sprintf("grep: ripgrep no disponible %q: %v", e.Binary, e.Err)
}

func (e *GrepUnavailableError) Unwrap() error { return e.Err }

func (s *RgSearcher) Grep(ctx context.Context, req GrepRequest) (GrepResult, error) {
	binary := s.Binary
	if binary == "" {
		binary = "rg"
	}

	cmd := exec.CommandContext(ctx, binary, buildRipgrepArgs(req)...)
	cmd.Dir = req.Root
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return GrepResult{}, err
	}
	stderr := &boundedWriter{limit: maxRgStderrRunes * 4}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		if isUnavailableError(err) || errors.Is(err, exec.ErrNotFound) {
			return GrepResult{}, &GrepUnavailableError{Binary: binary, Err: err}
		}
		return GrepResult{}, err
	}

	result, scanErr := parseRipgrepJSON(stdout, req.Limit, true)
	if result.Truncated && cmd.Process != nil {
		_ = killProcessGroup(cmd)
	}
	if scanErr != nil && cmd.Process != nil {
		_ = killProcessGroup(cmd)
	}
	waitErr := cmd.Wait()
	stderrText := boundedString(strings.TrimSpace(stderr.String()), maxRgStderrRunes)

	if scanErr != nil {
		return GrepResult{}, scanErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil && !result.Truncated {
		return GrepResult{}, ctxErr
	}
	if result.Truncated {
		return result, nil
	}
	if waitErr == nil {
		return result, nil
	}
	return handleRipgrepWaitError(waitErr, stderrText, req.Pattern, binary)
}

type boundedWriter struct {
	buf   bytes.Buffer
	limit int
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	written := len(p)
	remaining := w.limit - w.buf.Len()
	if remaining > 0 {
		_, _ = w.buf.Write(p[:min(len(p), remaining)])
	}
	return written, nil
}

func (w *boundedWriter) String() string {
	return w.buf.String()
}

func ParseRipgrepJSON(stdout []byte, limit int) (GrepResult, error) {
	return parseRipgrepJSON(bytes.NewReader(stdout), limit, false)
}

func parseRipgrepJSON(r io.Reader, limit int, stopOnTruncated bool) (GrepResult, error) {
	limit = normalizeGrepLimit(limit)
	result := GrepResult{Matches: make([]GrepMatch, 0, min(limit, 16))}

	reader := bufio.NewReader(r)
	for {
		raw, err := reader.ReadBytes('\n')
		if len(raw) == 0 && err == io.EOF {
			break
		}
		if err != nil && err != io.EOF {
			return GrepResult{}, fmt.Errorf("grep: leyendo salida de rg: %w", err)
		}
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			if err == io.EOF {
				break
			}
			continue
		}

		var record ripgrepRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return GrepResult{}, fmt.Errorf("grep: salida JSON de rg invalida: %w", err)
		}
		if record.Type != "match" {
			continue
		}
		if len(result.Matches) >= limit {
			result.Truncated = true
			if stopOnTruncated {
				return result, nil
			}
			continue
		}
		result.Matches = append(result.Matches, GrepMatch{
			Path: normalizeRipgrepPath(record.Data.Path.Text),
			Line: record.Data.LineNumber,
			Text: truncateGrepLine(record.Data.Lines.Text),
		})
		if err == io.EOF {
			break
		}
	}
	return result, nil
}

func handleRipgrepWaitError(err error, stderrText, pattern, binary string) (GrepResult, error) {
	if errors.Is(err, exec.ErrNotFound) || isUnavailableError(err) {
		return GrepResult{}, &GrepUnavailableError{Binary: binary, Err: err}
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case 1:
			return GrepResult{}, nil
		case 2:
			if isRegexParseError(stderrText) {
				return GrepResult{}, &GrepInvalidPatternError{Pattern: pattern, Detail: stderrText}
			}
		}
	}

	detail := stderrText
	if detail == "" {
		detail = err.Error()
	}
	return GrepResult{}, fmt.Errorf("grep: rg failed: %s", boundedString(detail, maxRgStderrRunes))
}

func buildRipgrepArgs(req GrepRequest) []string {
	path := req.Path
	if path == "" {
		path = "."
	}

	args := []string{"--no-config", "--json", "--hidden", "--no-messages"}
	if req.Include != "" {
		args = append(args, "--glob="+req.Include)
	}
	args = append(args, "--glob=!**/.git/**", "--", req.Pattern, path)
	return args
}

type ripgrepRecord struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		LineNumber int `json:"line_number"`
		Lines      struct {
			Text string `json:"text"`
		} `json:"lines"`
	} `json:"data"`
}

func normalizeGrepLimit(limit int) int {
	if limit <= 0 {
		return defaultGrepLimit
	}
	return limit
}

func normalizeRipgrepPath(path string) string {
	path = strings.ReplaceAll(path, `\`, `/`)
	path = filepath.ToSlash(path)
	path = strings.TrimPrefix(path, "./")
	for strings.HasPrefix(path, "/") {
		path = strings.TrimPrefix(path, "/")
	}
	return path
}

func truncateGrepLine(text string) string {
	if utf8.RuneCountInString(text) <= maxGrepLineRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxGrepLineRunes]) + "..."
}

func isRegexParseError(stderr string) bool {
	return strings.Contains(stderr, "regex parse error") || strings.Contains(stderr, "error parsing regex")
}

func isUnavailableError(err error) bool {
	var execErr *exec.Error
	var pathErr *os.PathError
	return errors.As(err, &execErr) || errors.As(err, &pathErr) || errors.Is(err, os.ErrNotExist)
}

func boundedString(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "..."
}
