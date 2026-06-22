package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"atenea/internal/tool/hashline"
)

const defaultGrepMaxMatches = 100

// GrepTool busca contenido bajo Root con un Searcher (ripgrep en produccion) y
// renderiza las lineas encontradas en formato hashline, grabando snapshots para
// que un edit posterior pueda anclar sobre esas lineas vistas.
type GrepTool struct {
	Root             string
	Searcher         Searcher
	FS               FileReader
	Snapshots        hashline.SnapshotStore
	SnapshotProvider SnapshotProvider
	MaxMatches       int
}

func NewGrepTool(root string, snaps hashline.SnapshotStore) *GrepTool {
	return &GrepTool{Root: root, Searcher: NewRgSearcher(), FS: osFS{}, Snapshots: snaps, MaxMatches: defaultGrepMaxMatches}
}

func NewGrepToolWithSnapshotProvider(root string, provider SnapshotProvider) *GrepTool {
	return &GrepTool{Root: root, Searcher: NewRgSearcher(), FS: osFS{}, SnapshotProvider: provider, MaxMatches: defaultGrepMaxMatches}
}

func (*GrepTool) Name() string { return "grep" }

//go:embed grep.txt
var grepDescription string

func (*GrepTool) Description() string { return grepDescription }

func (*GrepTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Patron regex para buscar en el contenido de archivos."},"path":{"type":"string","description":"Archivo o directorio relativo al workspace donde buscar. Default: '.'."},"include":{"type":"string","description":"Glob de archivos a incluir, por ejemplo '*.go' o '*.{ts,tsx}'."}},"required":["pattern"]}`)
}

func (gt *GrepTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("grep: input invalido: %w", err)
	}
	if in.Pattern == "" {
		return Result{}, fmt.Errorf("grep: pattern requerido")
	}
	if in.Path == "" {
		in.Path = "."
	}

	abs, err := sandboxJoin(gt.Root, in.Path, "grep")
	if err != nil {
		return Result{}, err
	}
	if _, ok := gt.fileReader().(osFS); ok {
		if err := rejectRealPathOutside(gt.Root, abs, in.Path, "grep"); err != nil {
			return Result{}, err
		}
	}

	searcher := gt.Searcher
	if searcher == nil {
		searcher = NewRgSearcher()
	}
	limit := gt.MaxMatches
	if limit <= 0 {
		limit = defaultGrepMaxMatches
	}
	found, err := searcher.Grep(ctx, GrepRequest{
		Root:    gt.Root,
		Path:    in.Path,
		Pattern: in.Pattern,
		Include: in.Include,
		Limit:   limit,
	})
	if err != nil {
		return Result{}, err
	}
	if len(found.Matches) == 0 {
		return Result{Output: "No files found"}, nil
	}

	groups := groupGrepMatches(found.Matches)
	if len(groups) == 0 {
		return Result{Output: "No files found"}, nil
	}

	snaps := gt.snapshots(ctx)
	fs := gt.fileReader()
	parts := make([]grepOutputPart, 0, len(groups))
	for _, group := range groups {
		absMatch, err := sandboxJoin(gt.Root, group.path, "grep")
		if err != nil {
			return Result{}, err
		}
		if _, ok := fs.(osFS); ok {
			if err := rejectRealPathOutside(gt.Root, absMatch, group.path, "grep"); err != nil {
				return Result{}, err
			}
		}
		b, err := fs.ReadFile(absMatch)
		if err != nil {
			return Result{}, fmt.Errorf("grep: no se pudo leer archivo encontrado %s: %w", group.path, err)
		}
		if containsNUL(b) {
			parts = append(parts, grepOutputPart{text: "[Cannot grep binary file " + group.path + "; content contains NUL bytes]"})
			continue
		}

		text := normalizeToolText(string(b))
		tag := snaps.Record(absMatch, text)
		lines := hashline.SplitLines(text)
		emitted := existingLines(group.lines, len(lines))
		if len(emitted) == 0 {
			continue
		}

		var part strings.Builder
		part.WriteString(hashline.FormatHeader(group.path, tag))
		for _, line := range emitted {
			part.WriteString("\n")
			part.WriteString(strconv.Itoa(line))
			part.WriteString(":")
			part.WriteString(lines[line-1])
		}
		snaps.RecordSeenLines(absMatch, tag, emitted)
		parts = append(parts, grepOutputPart{text: part.String(), matches: len(emitted)})
	}

	if len(parts) == 0 {
		return Result{Output: "No files found"}, nil
	}

	var emittedMatches int
	for _, part := range parts {
		emittedMatches += part.matches
	}

	var out strings.Builder
	fmt.Fprintf(&out, "Found %d matches", emittedMatches)
	if found.Truncated {
		out.WriteString(" (more matches available)")
	}
	for i, part := range parts {
		if i == 0 {
			out.WriteString("\n")
		} else {
			out.WriteString("\n\n")
		}
		out.WriteString(part.text)
	}

	if found.Truncated {
		out.WriteString("\n\n(Results truncated. Consider using a more specific path or pattern.)")
	}
	return Result{Output: out.String()}, nil
}

func (gt *GrepTool) fileReader() FileReader {
	if gt.FS != nil {
		return gt.FS
	}
	return osFS{}
}

func (gt *GrepTool) snapshots(ctx context.Context) hashline.SnapshotStore {
	if gt.SnapshotProvider != nil {
		return gt.SnapshotProvider.Snapshots(ctx)
	}
	if gt.Snapshots != nil {
		return gt.Snapshots
	}
	return hashline.NewMemSnapshotStore()
}

type grepMatchGroup struct {
	path  string
	lines []int
}

type grepOutputPart struct {
	text    string
	matches int
}

func groupGrepMatches(matches []GrepMatch) []grepMatchGroup {
	type state struct {
		seen map[int]struct{}
	}
	order := make([]string, 0)
	byPath := make(map[string]*state)
	for _, match := range matches {
		if match.Path == "" || match.Line < 1 {
			continue
		}
		st := byPath[match.Path]
		if st == nil {
			st = &state{seen: map[int]struct{}{}}
			byPath[match.Path] = st
			order = append(order, match.Path)
		}
		st.seen[match.Line] = struct{}{}
	}

	groups := make([]grepMatchGroup, 0, len(order))
	for _, path := range order {
		st := byPath[path]
		lines := make([]int, 0, len(st.seen))
		for line := range st.seen {
			lines = append(lines, line)
		}
		sort.Ints(lines)
		groups = append(groups, grepMatchGroup{path: path, lines: lines})
	}
	return groups
}

func existingLines(lines []int, total int) []int {
	out := make([]int, 0, len(lines))
	for _, line := range lines {
		if line >= 1 && line <= total {
			out = append(out, line)
		}
	}
	return out
}

func containsNUL(b []byte) bool {
	for _, by := range b {
		if by == 0 {
			return true
		}
	}
	return false
}

func normalizeToolText(text string) string {
	text = strings.TrimPrefix(text, "\uFEFF")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}
