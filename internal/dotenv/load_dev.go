//go:build !production

package dotenv

import "os"

// Load carga el .env de path al entorno SIN pisar variables ya seteadas: las
// env vars reales tienen prioridad sobre el archivo. La ausencia del archivo no
// es error (corre en silencio). Solo existe en builds de desarrollo: la
// variante -tags production es un no-op (ver load_production.go).
func Load(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // no hay .env: no es error
	}
	defer f.Close()
	for k, v := range parse(f) {
		if _, ok := os.LookupEnv(k); !ok {
			os.Setenv(k, v)
		}
	}
}
