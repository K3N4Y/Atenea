package session_test

import (
	"path/filepath"
	"testing"

	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/session/sessiontest"
)

// The contract itself lives in internal/session/sessiontest, so the decorators
// that wrap a Store from other packages run the same one. Here it is applied to
// the two implementations this package ships.

func TestMemoryStore_Contract(t *testing.T) {
	sessiontest.StoreContract(t, func(t *testing.T) session.Store { return session.NewMemoryStore() })
}

func TestSQLiteStore_Contract(t *testing.T) {
	sessiontest.StoreContract(t, func(t *testing.T) session.Store {
		s, err := session.NewSQLiteStore(":memory:")
		if err != nil {
			t.Fatalf("NewSQLiteStore: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

// TestSQLiteStore_FileBacked_Contract runs the same contract against a
// FILE-backed SQLiteStore: the file DSN adds pragmas (journal_mode WAL,
// busy_timeout) that ":memory:" never exercises, and none of them may change the
// contract's behavior (ErrSessionNotFound, Seq order, projections, delete,
// concurrent appends). Each subtest gets its own file in a tempdir: the t the
// factory receives is the subtest's.
func TestSQLiteStore_FileBacked_Contract(t *testing.T) {
	sessiontest.StoreContract(t, func(t *testing.T) session.Store {
		s, err := session.NewSQLiteStore(filepath.Join(t.TempDir(), "atenea.db"))
		if err != nil {
			t.Fatalf("NewSQLiteStore (file): %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}
