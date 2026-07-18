package dotenv

import (
	"strings"
	"testing"
)

// TestParse_ParsesKeyValue fija el caso central: una linea KEY=VALUE se
// parsea a un par en el mapa, con el valor tal cual (sin comillas).
func TestParse_ParsesKeyValue(t *testing.T) {
	got := parse(strings.NewReader("OPENROUTER_API_KEY=sk-123\n"))
	if got["OPENROUTER_API_KEY"] != "sk-123" {
		t.Fatalf("parse: got %q, want %q", got["OPENROUTER_API_KEY"], "sk-123")
	}
}

// TestParse_IgnoresCommentsAndBlanks: las lineas vacias y las que empiezan
// con # se ignoran, incluso si el comentario contiene un '='.
func TestParse_IgnoresCommentsAndBlanks(t *testing.T) {
	got := parse(strings.NewReader("\n# OPENROUTER_MODEL=comentario\n\nFOO=bar\n"))
	if _, ok := got["# OPENROUTER_MODEL"]; ok {
		t.Fatalf("un comentario con '=' se parseo como par: %v", got)
	}
	if len(got) != 1 || got["FOO"] != "bar" {
		t.Fatalf("parse: got %v, want solo FOO=bar", got)
	}
}

// TestParse_StripsSurroundingQuotes: las comillas envolventes (dobles o
// simples) se quitan; el contenido queda intacto.
func TestParse_StripsSurroundingQuotes(t *testing.T) {
	got := parse(strings.NewReader("A=\"sk-1\"\nB='sk-2'\n"))
	if got["A"] != "sk-1" {
		t.Fatalf("comillas dobles: got %q, want %q", got["A"], "sk-1")
	}
	if got["B"] != "sk-2" {
		t.Fatalf("comillas simples: got %q, want %q", got["B"], "sk-2")
	}
}

// TestParse_KeepsEqualsInValue: solo se corta en el primer '=', asi un
// valor con '=' (p.ej. una key con padding base64) se conserva entero.
func TestParse_KeepsEqualsInValue(t *testing.T) {
	got := parse(strings.NewReader("K=a=b=c\n"))
	if got["K"] != "a=b=c" {
		t.Fatalf("parse: got %q, want %q", got["K"], "a=b=c")
	}
}
