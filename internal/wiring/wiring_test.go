package wiring

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestSkillDirs_ProjectBeforeGlobalDeduped: skillDirs lista primero las rutas del
// proyecto (root) y luego las globales (home), en el orden .atenea/.agents/.claude,
// para que una skill del proyecto override a una global homonima. Rutas identicas
// (root == home) se deduplican.
func TestSkillDirs_ProjectBeforeGlobalDeduped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := skillDirs("/proj")
	want := []string{
		filepath.Join("/proj", ".atenea", "skills"),
		filepath.Join("/proj", ".agents", "skills"),
		filepath.Join("/proj", ".claude", "skills"),
		filepath.Join(home, ".atenea", "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skillDirs orden = %v,\n want %v", got, want)
	}
	// root == home: las rutas coinciden, deben deduplicarse a las 3 del home.
	if d := skillDirs(home); len(d) != 3 {
		t.Fatalf("root==home debe deduplicar a 3 dirs, got %v", d)
	}
}
