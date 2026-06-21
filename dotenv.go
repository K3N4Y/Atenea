package main

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// parseDotEnv lee pares KEY=VALUE de r y los devuelve en un mapa. Es el parser
// minimo de un .env para cargar secretos de prueba sin una dependencia: ignora
// lineas vacias y comentarios (#), recorta espacios y quita comillas envolventes
// (dobles o simples). Corta solo en el primer '=', asi un valor con '=' se
// conserva entero.
func parseDotEnv(r io.Reader) map[string]string {
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

// loadDotEnv carga el .env de path al entorno SIN pisar variables ya seteadas: las
// env vars reales tienen prioridad sobre el archivo. La ausencia del archivo no es
// error (corre en silencio). Es una conveniencia de desarrollo; en produccion las
// claves siguen viniendo del entorno.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // no hay .env: no es error
	}
	defer f.Close()
	for k, v := range parseDotEnv(f) {
		if _, ok := os.LookupEnv(k); !ok {
			os.Setenv(k, v)
		}
	}
}
