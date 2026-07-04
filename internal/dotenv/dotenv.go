// Package dotenv carga un archivo .env al entorno del proceso. Es la
// conveniencia de desarrollo compartida por los binarios (app Wails y TUI):
// deja OPENROUTER_API_KEY y demas a mano en dev sin exportarlas.
package dotenv

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// parse lee pares KEY=VALUE de r y los devuelve en un mapa. Es el parser
// minimo de un .env para cargar secretos de prueba sin una dependencia: ignora
// lineas vacias y comentarios (#), recorta espacios y quita comillas envolventes
// (dobles o simples). Corta solo en el primer '=', asi un valor con '=' se
// conserva entero.
func parse(r io.Reader) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = unquote(strings.TrimSpace(val))
	}
	return out
}

// unquote quita un par de comillas envolventes iguales (dobles o simples); deja
// el valor intacto si no esta entrecomillado.
func unquote(s string) string {
	if len(s) >= 2 {
		if c := s[0]; (c == '"' || c == '\'') && s[len(s)-1] == c {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// Load carga el .env de path al entorno SIN pisar variables ya seteadas: las
// env vars reales tienen prioridad sobre el archivo. La ausencia del archivo no
// es error (corre en silencio). Es una conveniencia de desarrollo; en produccion
// las claves siguen viniendo del entorno.
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
