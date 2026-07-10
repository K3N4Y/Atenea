package tui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
)

const maxFileViewerBytes = 1 << 20

var (
	ErrFileViewerBinary   = errors.New("archivo binario")
	ErrFileViewerTooLarge = errors.New("archivo demasiado grande")
)

type FileReader func(path string) ([]byte, error)

type fileViewer struct {
	path      string
	lines     []string
	offset    int
	message   string
	empty     bool
	lineCount int
}

func WorkspaceFileReader(root string) FileReader {
	cleanRoot, rootErr := filepath.Abs(root)
	return func(path string) ([]byte, error) {
		if rootErr != nil {
			return nil, rootErr
		}
		cleanPath := filepath.Clean(filepath.FromSlash(path))
		if filepath.IsAbs(cleanPath) || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("ruta fuera del workspace: %s", path)
		}
		candidate := filepath.Join(cleanRoot, cleanPath)
		relative, err := filepath.Rel(cleanRoot, candidate)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("ruta fuera del workspace: %s", path)
		}
		return os.ReadFile(candidate)
	}
}

func openFileViewer(path string, content []byte) fileViewer {
	if len(content) > maxFileViewerBytes {
		return openFileViewerError(path, ErrFileViewerTooLarge)
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return openFileViewerError(path, ErrFileViewerBinary)
	}
	if len(content) == 0 {
		return fileViewer{path: path, empty: true}
	}

	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if strings.HasSuffix(normalized, "\n") {
		lines = lines[:len(lines)-1]
	}
	return fileViewer{path: path, lines: highlightFile(path, lines), lineCount: len(lines)}
}

func openFileViewerError(path string, err error) fileViewer {
	message := fmt.Sprintf("no se puede abrir %s: %v", path, err)
	switch {
	case errors.Is(err, ErrFileViewerBinary):
		message = "archivo binario: " + path
	case errors.Is(err, ErrFileViewerTooLarge):
		message = "archivo demasiado grande (> 1 MiB): " + path
	}
	return fileViewer{path: path, message: message}
}

func highlightFile(path string, lines []string) []string {
	lexer := lexers.Match(path)
	if lexer == nil {
		lexer = lexers.Get("plaintext")
	}
	iterator, err := lexer.Tokenise(nil, strings.Join(lines, "\n"))
	if err != nil {
		return lines
	}
	var output bytes.Buffer
	if err := formatters.TTY16m.Format(&output, styles.Monokai, iterator); err != nil {
		return lines
	}
	highlighted := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(highlighted) != len(lines) {
		return lines
	}
	return highlighted
}

func (v fileViewer) active() bool {
	return v.path != ""
}

func (v fileViewer) visibleRange(height int) (int, int) {
	if height <= 0 || v.lineCount == 0 {
		return 0, 0
	}
	first := min(max(v.offset, 0), max(v.lineCount-height, 0))
	return first, min(first+height, v.lineCount)
}

func (v *fileViewer) clamp(height int) {
	if height <= 0 || v.lineCount == 0 {
		v.offset = 0
		return
	}
	v.offset = min(max(v.offset, 0), max(v.lineCount-height, 0))
}

func (v *fileViewer) scroll(delta, height int) {
	v.offset += delta
	v.clamp(height)
}

func (v fileViewer) header(width, height int) string {
	if v.message != "" || v.empty {
		return ansi.Truncate(v.path, max(width, 0), "…")
	}
	first, last := v.visibleRange(height)
	return ansi.Truncate(fmt.Sprintf("%s · %d-%d/%d", v.path, first+1, last, v.lineCount), max(width, 0), "…")
}

func (v fileViewer) render(width, height int) string {
	width = max(width, 0)
	if v.message != "" {
		return ansi.Truncate(v.message, width, "…")
	}
	if v.empty {
		return ansi.Truncate("archivo vacio: "+v.path, width, "…")
	}
	first, last := v.visibleRange(height)
	gutterWidth := len(strconv.Itoa(v.lineCount))
	rows := make([]string, 0, last-first)
	for index := first; index < last; index++ {
		gutter := fmt.Sprintf("%*d  ", gutterWidth, index+1)
		rows = append(rows, ansi.Truncate(gutter+v.lines[index], width, "…"))
	}
	return strings.Join(rows, "\n")
}
