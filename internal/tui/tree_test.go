package tui

import (
	"reflect"
	"testing"
)

func TestTree_BuildsFromPaths(t *testing.T) {
	tree := newFileTree([]string{
		"go.mod",
		"internal/tui/model.go",
		"internal/tui/tree.go",
		"cmd/atenea/main.go",
	})

	got := tree.paths()
	want := []string{
		"cmd",
		"cmd/atenea",
		"cmd/atenea/main.go",
		"internal",
		"internal/tui",
		"internal/tui/model.go",
		"internal/tui/tree.go",
		"go.mod",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tree.paths() = %#v, want %#v", got, want)
	}
}

func TestTree_ExpandCollapseVisibleRows(t *testing.T) {
	tree := newFileTree([]string{"internal/tui/model.go", "go.mod"})

	if got, want := rowPaths(tree.visibleRows()), []string{"internal", "go.mod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("visible rows collapsed = %#v, want %#v", got, want)
	}

	tree.toggle("internal")
	if got, want := rowPaths(tree.visibleRows()), []string{"internal", "internal/tui", "go.mod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("visible rows expanded = %#v, want %#v", got, want)
	}

	tree.toggle("internal")
	if got, want := rowPaths(tree.visibleRows()), []string{"internal", "go.mod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("visible rows collapsed again = %#v, want %#v", got, want)
	}
}

func TestTree_IconForExtension(t *testing.T) {
	tests := map[string]string{
		"main.go":      iconGo,
		"app.ts":       iconTypeScript,
		"view.tsx":     iconTypeScript,
		"script.js":    iconJavaScript,
		"widget.jsx":   iconJavaScript,
		"ChatView.vue": iconVue,
		"README.md":    iconMarkdown,
		"config.json":  iconJSON,
		"config.yaml":  iconConfig,
		"config.yml":   iconConfig,
		"main.css":     iconCSS,
		"index.html":   iconHTML,
		"LICENSE":      iconFile,
	}

	for path, want := range tests {
		t.Run(path, func(t *testing.T) {
			if got := iconForFile(path); got != want {
				t.Fatalf("iconForFile(%q) = %q, want %q", path, got, want)
			}
		})
	}
}

func rowPaths(rows []treeRow) []string {
	paths := make([]string, len(rows))
	for i, row := range rows {
		paths[i] = row.node.path
	}
	return paths
}
