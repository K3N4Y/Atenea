package session

import (
	"os"
	"path/filepath"

	"github.com/K3N4Y/atenea/internal/paths"
)

// DefaultDBPath resolves the SQLite file shared by the Wails app and the TUI.
// ATENEA_DB takes precedence. Otherwise it creates the data directory and falls
// back to "atenea.db" in the working directory when that is not possible.
func DefaultDBPath() string {
	if p := os.Getenv("ATENEA_DB"); p != "" {
		return p
	}
	path, err := paths.DB()
	if err != nil {
		return "atenea.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "atenea.db"
	}
	return path
}

func DefaultCheckpointPath() string {
	if path := os.Getenv("ATENEA_CHECKPOINTS"); path != "" {
		return path
	}
	path, err := paths.Checkpoints()
	if err != nil {
		return filepath.Join(os.TempDir(), "atenea", "checkpoints")
	}
	_ = os.MkdirAll(path, 0o700)
	return path
}

// OpenDefault opens the durable store at DefaultDBPath. If SQLite fails, it
// returns the error together with a usable in-memory store so the caller can
// continue without persistence and decide how to report the failure.
func OpenDefault() (Store, error) {
	store, err := NewSQLiteStore(DefaultDBPath())
	if err != nil {
		return NewMemoryStore(), err
	}
	return store, nil
}
