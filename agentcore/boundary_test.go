package agentcore

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// modulePath is this module's import path. Renaming the module makes this test
// fail with a clear message rather than passing vacuously.
const modulePath = "github.com/K3N4Y/atenea"

// TestContracts_DependOnNothingPrivate walks every Go file under agentcore/ and
// checks its imports directly, because a direct import is the only way a
// contract can reach the private side: a transitive path would have to pass
// through another agentcore package, which this same walk covers.
//
// Scanning the source instead of asking the build for a dependency graph keeps
// the guard hermetic — no toolchain call, no build tags to reason about — and the
// failure names the file and the import that broke the rule.
func TestContracts_DependOnNothingPrivate(t *testing.T) {
	fset := token.NewFileSet()
	files := 0

	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		files++

		parsed, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		for _, spec := range parsed.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("%s: unquote import %s: %v", path, spec.Path.Value, err)
				continue
			}
			switch {
			case isStandardLibrary(imported):
			case imported == modulePath+"/agentcore" || strings.HasPrefix(imported, modulePath+"/agentcore/"):
			case strings.HasPrefix(imported, modulePath+"/internal/"):
				t.Errorf("%s imports %s: a published contract must not depend on the private side", path, imported)
			default:
				t.Errorf("%s imports %s: a published contract must depend on the standard library only", path, imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk agentcore: %v", err)
	}
	// A guard that silently stops finding files stops guarding anything.
	if files == 0 {
		t.Fatal("found no Go files under agentcore/")
	}
}

// isStandardLibrary reports whether the import path names a standard library
// package: only module paths carry a dot in their first segment.
func isStandardLibrary(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}
