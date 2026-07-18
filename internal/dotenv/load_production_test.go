//go:build production

package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoad_ProductionBuildIgnoresDotEnv pins the release contract: a binary
// built with -tags production must never import secrets from a .env file.
func TestLoad_ProductionBuildIgnoresDotEnv(t *testing.T) {
	t.Setenv("ATENEA_TEST_PROD", "") // declare so t.Setenv restores it
	os.Unsetenv("ATENEA_TEST_PROD")

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("ATENEA_TEST_PROD=leaked\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	Load(path)

	if got, ok := os.LookupEnv("ATENEA_TEST_PROD"); ok {
		t.Fatalf("production Load loaded %q from .env; it must be a no-op", got)
	}
}
