package session

import (
	"os"
	"path/filepath"
)

// DefaultDBPath resuelve la ruta del SQLite COMPARTIDO por la app Wails y la
// TUI: ambas abren el mismo archivo y ven las mismas sesiones. ATENEA_DB gana
// si esta seteada (util en dev); si no <UserConfigDir>/atenea/atenea.db,
// creando el directorio. Cae a "atenea.db" en el cwd si no hay directorio de
// config o no se pudo crear.
func DefaultDBPath() string {
	if p := os.Getenv("ATENEA_DB"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "atenea.db"
	}
	appDir := filepath.Join(dir, "atenea")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return "atenea.db"
	}
	return filepath.Join(appDir, "atenea.db")
}

// OpenDefault abre el store durable en DefaultDBPath. Si SQLite falla devuelve
// el error JUNTO a un store en memoria usable: un error no-nil NO significa
// store inutilizable, el caller sigue funcionando sin persistencia y decide
// como avisar. Con error nil el store es el SQLite compartido.
func OpenDefault() (Store, error) {
	store, err := NewSQLiteStore(DefaultDBPath())
	if err != nil {
		return NewMemoryStore(), err
	}
	return store, nil
}
