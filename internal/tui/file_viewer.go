package tui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
const fileViewerTabWidth = 4

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
	if rootErr == nil {
		cleanRoot, rootErr = filepath.EvalSymlinks(cleanRoot)
	}
	return func(path string) ([]byte, error) {
		if rootErr != nil {
			return nil, rootErr
		}
		cleanPath := filepath.Clean(filepath.FromSlash(path))
		if filepath.IsAbs(cleanPath) || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("ruta fuera del workspace: %s", path)
		}
		candidate, err := filepath.EvalSymlinks(filepath.Join(cleanRoot, cleanPath))
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(cleanRoot, candidate)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("ruta fuera del workspace: %s", path)
		}
		info, err := os.Stat(candidate)
		if err != nil {
			return nil, err
		}
		if info.Size() > maxFileViewerBytes {
			return nil, ErrFileViewerTooLarge
		}

		file, err := os.Open(candidate)
		if err != nil {
			return nil, err
		}
		defer file.Close()

		content, err := io.ReadAll(io.LimitReader(file, maxFileViewerBytes+1))
		if err != nil {
			return nil, err
		}
		if len(content) > maxFileViewerBytes {
			return nil, ErrFileViewerTooLarge
		}
		return content, nil
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
	normalized = strings.ReplaceAll(normalized, "\t", strings.Repeat(" ", fileViewerTabWidth))
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
	for index, line := range highlighted {
		if !strings.HasSuffix(line, ansi.ResetStyle) {
			highlighted[index] = line + ansi.ResetStyle
		}
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
		if width <= 0 {
			return v.path
		}
		return ansi.Truncate(v.path, width, "…")
	}
	first, last := v.visibleRange(height)
	value := fmt.Sprintf("%s · %d-%d/%d", v.path, first+1, last, v.lineCount)
	if width <= 0 {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

func (v fileViewer) render(width, height int) string {
	if v.message != "" {
		if width <= 0 {
			return v.message
		}
		return ansi.Truncate(v.message, width, "…")
	}
	if v.empty {
		if width <= 0 {
			return "archivo vacio: " + v.path
		}
		return ansi.Truncate("archivo vacio: "+v.path, width, "…")
	}
	first, last := v.visibleRange(height)
	gutterWidth := len(strconv.Itoa(v.lineCount))
	rows := make([]string, 0, last-first)
	for index := first; index < last; index++ {
		gutter := fmt.Sprintf("%*d  ", gutterWidth, index+1)
		line := gutter + v.lines[index]
		if width > 0 {
			line = ansi.Truncate(line, width, "…")
		} else if width == 0 {
			line = ""
		}
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n")
}
