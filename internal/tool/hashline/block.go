package hashline

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

const blockCacheLimit = 512

type blockCacheKey struct {
	path string
	line int
	hash [32]byte
}

type blockCacheValue struct {
	end    int
	reason string
}

// StructuralBlockResolver resolves blocks from tree-sitter syntax trees. Its
// grammar modules are compiled into the binary; parsing never invokes an
// external executable or downloads a runtime.
type StructuralBlockResolver struct {
	state *blockResolverState
}

type blockResolverState struct {
	mu      sync.Mutex
	entries map[blockCacheKey]blockCacheValue
	order   []blockCacheKey
}

var defaultBlockResolverState = &blockResolverState{entries: make(map[blockCacheKey]blockCacheValue)}

func (r StructuralBlockResolver) ResolveBlock(path string, lines []string, start int) (int, error) {
	if start < 1 || start > len(lines) {
		return 0, &UnresolvedBlockError{Path: path, Line: start, Reason: "line is out of range"}
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".md" || ext == ".markdown" || ext == ".mdx" {
		end, ok := markdownBlock(lines, start)
		if !ok {
			return 0, &UnresolvedBlockError{Path: path, Line: start, Reason: "line is not a Markdown heading with a non-empty section"}
		}
		return end, nil
	}
	language := languageForPath(path)
	if language == nil {
		return 0, &UnsupportedBlockLanguageError{Path: path}
	}
	if strings.TrimSpace(lines[start-1]) == "" {
		return 0, &UnresolvedBlockError{Path: path, Line: start, Reason: "line is blank"}
	}
	source := []byte(strings.Join(lines, "\n"))
	key := blockCacheKey{path: path, line: start, hash: sha256.Sum256(source)}
	state := r.state
	if state == nil {
		state = defaultBlockResolverState
	}
	if value, ok := state.get(key); ok {
		if value.reason != "" {
			return 0, &UnresolvedBlockError{Path: path, Line: start, Reason: value.reason}
		}
		return value.end, nil
	}

	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(language); err != nil {
		return 0, fmt.Errorf("hashline: initialize parser for %s: %w", path, err)
	}
	tree := parser.Parse(source, nil)
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		state.put(key, blockCacheValue{reason: "file contains syntax errors"})
		return 0, &UnresolvedBlockError{Path: path, Line: start, Reason: "file contains syntax errors"}
	}
	end, ok := structuralRange(root, uint(start-1))
	if !ok || end <= start {
		reason := "line is a closer, inner statement, or single-line syntax node"
		state.put(key, blockCacheValue{reason: reason})
		return 0, &UnresolvedBlockError{Path: path, Line: start, Reason: reason}
	}
	state.put(key, blockCacheValue{end: end})
	return end, nil
}

func languageForPath(path string) *sitter.Language {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return sitter.NewLanguage(tsgo.Language())
	case ".js", ".jsx", ".mjs", ".cjs":
		return sitter.NewLanguage(javascript.Language())
	case ".ts", ".mts", ".cts":
		return sitter.NewLanguage(typescript.LanguageTypescript())
	case ".tsx":
		return sitter.NewLanguage(typescript.LanguageTSX())
	case ".py", ".pyi":
		return sitter.NewLanguage(python.Language())
	case ".rs":
		return sitter.NewLanguage(rust.Language())
	default:
		return nil
	}
}

func structuralRange(root *sitter.Node, row uint) (int, bool) {
	// The outermost named node beginning on the authored line is the editing
	// unit (for example, a function rather than its same-line body).
	var visit func(*sitter.Node) (int, bool)
	visit = func(node *sitter.Node) (int, bool) {
		start, end := node.StartPosition().Row, node.EndPosition().Row
		if start > row || end < row {
			return 0, false
		}
		if node.Parent() != nil && start == row && end > row && isBlockNode(node.Kind()) {
			return int(end) + 1, true
		}
		// A doc comment opener groups with the declaration immediately following it.
		if start == row && node.Kind() == "comment" {
			for next := node.NextNamedSibling(); next != nil && next.Kind() == "comment"; next = next.NextNamedSibling() {
				node = next
			}
			if next := node.NextNamedSibling(); next != nil && next.EndPosition().Row > row && isBlockNode(next.Kind()) {
				return int(next.EndPosition().Row) + 1, true
			}
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			if end, ok := visit(node.NamedChild(i)); ok {
				return end, true
			}
		}
		return 0, false
	}
	return visit(root)
}

func isBlockNode(kind string) bool {
	switch kind {
	case "function_declaration", "function_definition", "function_item", "method_declaration", "method_definition", "arrow_function",
		"class_declaration", "class_definition", "interface_declaration", "type_declaration", "lexical_declaration",
		"impl_item", "trait_item", "enum_item", "struct_item", "mod_item",
		"if_statement", "for_statement", "while_statement", "switch_statement", "try_statement", "with_statement",
		"decorated_definition":
		return true
	default:
		return false
	}
}

func (s *blockResolverState) get(key blockCacheKey) (blockCacheValue, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.entries[key]
	return v, ok
}

func (s *blockResolverState) put(key blockCacheKey, value blockCacheValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[key]; exists {
		return
	}
	if len(s.order) == blockCacheLimit {
		delete(s.entries, s.order[0])
		copy(s.order, s.order[1:])
		s.order = s.order[:blockCacheLimit-1]
	}
	s.entries[key] = value
	s.order = append(s.order, key)
}

func markdownBlock(lines []string, start int) (int, bool) {
	line := strings.TrimLeft(lines[start-1], " \t")
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || len(line) <= level || line[level] != ' ' {
		return 0, false
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		candidate := strings.TrimLeft(lines[i], " \t")
		n := 0
		for n < len(candidate) && candidate[n] == '#' {
			n++
		}
		if n > 0 && n <= level && len(candidate) > n && candidate[n] == ' ' {
			end = i
			break
		}
	}
	return end, end > start
}
