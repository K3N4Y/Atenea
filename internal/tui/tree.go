package tui

import (
	"path"
	"sort"
	"strings"
)

const (
	iconFolderClosed = "󰉋"
	iconFolderOpen   = "󰝰"
	iconGo           = ""
	iconTypeScript   = ""
	iconJavaScript   = ""
	iconVue          = ""
	iconMarkdown     = ""
	iconJSON         = ""
	iconConfig       = ""
	iconCSS          = ""
	iconHTML         = ""
	iconFile         = "󰈔"
)

type treeNode struct {
	name     string
	path     string
	dir      bool
	children []*treeNode
}

type treeRow struct {
	node  *treeNode
	depth int
}

type fileTree struct {
	roots    []*treeNode
	expanded map[string]bool
}

func newFileTree(paths []string) fileTree {
	root := &treeNode{dir: true}
	for _, rawPath := range paths {
		clean := strings.TrimPrefix(path.Clean(rawPath), "./")
		if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
			continue
		}
		parts := strings.Split(clean, "/")
		parent := root
		for i, part := range parts {
			child := findChild(parent.children, part)
			if child == nil {
				childPath := strings.Join(parts[:i+1], "/")
				child = &treeNode{name: part, path: childPath, dir: i < len(parts)-1}
				parent.children = append(parent.children, child)
			}
			if i < len(parts)-1 {
				child.dir = true
			}
			parent = child
		}
	}
	sortNodes(root.children)
	return fileTree{roots: root.children, expanded: make(map[string]bool)}
}

func findChild(nodes []*treeNode, name string) *treeNode {
	for _, node := range nodes {
		if node.name == name {
			return node
		}
	}
	return nil
}

func sortNodes(nodes []*treeNode) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].dir != nodes[j].dir {
			return nodes[i].dir
		}
		return strings.ToLower(nodes[i].name) < strings.ToLower(nodes[j].name)
	})
	for _, node := range nodes {
		sortNodes(node.children)
	}
}

func (t fileTree) paths() []string {
	var paths []string
	var walk func([]*treeNode)
	walk = func(nodes []*treeNode) {
		for _, node := range nodes {
			paths = append(paths, node.path)
			walk(node.children)
		}
	}
	walk(t.roots)
	return paths
}

func (t fileTree) visibleRows() []treeRow {
	var rows []treeRow
	var walk func([]*treeNode, int)
	walk = func(nodes []*treeNode, depth int) {
		for _, node := range nodes {
			rows = append(rows, treeRow{node: node, depth: depth})
			if node.dir && t.expanded[node.path] {
				walk(node.children, depth+1)
			}
		}
	}
	walk(t.roots, 0)
	return rows
}

func (t *fileTree) toggle(nodePath string) {
	if t.expanded[nodePath] {
		delete(t.expanded, nodePath)
		return
	}
	t.expanded[nodePath] = true
}

func iconForFile(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".go":
		return iconGo
	case ".ts", ".tsx":
		return iconTypeScript
	case ".js", ".jsx":
		return iconJavaScript
	case ".vue":
		return iconVue
	case ".md":
		return iconMarkdown
	case ".json":
		return iconJSON
	case ".yaml", ".yml":
		return iconConfig
	case ".css":
		return iconCSS
	case ".html":
		return iconHTML
	default:
		return iconFile
	}
}
