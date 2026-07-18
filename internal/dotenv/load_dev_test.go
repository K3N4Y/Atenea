//go:build !production

package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoad_DoesNotOverrideExistingEnv: las env vars REALES tienen prioridad
// sobre el .env (no se pisan); las ausentes se cargan desde el archivo.
func TestLoad_DoesNotOverrideExistingEnv(t *testing.T) {
	t.Setenv("ATENEA_TEST_EXISTING", "del-entorno")
	t.Setenv("ATENEA_TEST_NEW", "") // declarar para que t.Setenv lo restaure
	os.Unsetenv("ATENEA_TEST_NEW")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("ATENEA_TEST_EXISTING=del-archivo\nATENEA_TEST_NEW=del-archivo\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	Load(path)

	if got := os.Getenv("ATENEA_TEST_EXISTING"); got != "del-entorno" {
		t.Fatalf("Load piso una env var real: got %q, want %q", got, "del-entorno")
	}
	if got := os.Getenv("ATENEA_TEST_NEW"); got != "del-archivo" {
		t.Fatalf("Load no cargo la var ausente: got %q, want %q", got, "del-archivo")
	}
}

// TestLoad_MissingFileIsNoOp: la ausencia de .env no es error ni panic.
func TestLoad_MissingFileIsNoOp(t *testing.T) {
	Load(filepath.Join(t.TempDir(), "no-existe.env")) // no debe panicar ni fallar
}
