package skill

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:builtin
var builtinFS embed.FS

// ExtractBuiltins materializa en destDir las skills built-in embebidas en el
// binario, conservando la estructura <destDir>/<name>/SKILL.md. Asi una skill que
// viaja dentro del binario queda en disco bajo una ruta que skillDirs ya escanea
// (~/.atenea/skills), y el descubrimiento normal la levanta sin tocar Discover.
func ExtractBuiltins(destDir string) error {
	return fs.WalkDir(builtinFS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(path, "builtin/")
		dest := filepath.Join(destDir, rel)
		// Si el archivo ya existe no lo pisamos: respeta ediciones locales del usuario y
		// hace idempotente la extraccion (corre en cada arranque). Chequear primero evita
		// leer el embed y crear directorios para nada.
		if _, serr := os.Stat(dest); serr == nil {
			return nil
		}
		data, rerr := builtinFS.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if mderr := os.MkdirAll(filepath.Dir(dest), 0o755); mderr != nil {
			return mderr
		}
		return os.WriteFile(dest, data, 0o644)
	})
}
