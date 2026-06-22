package hashline

import (
	"fmt"
	"hash/crc32"
	"regexp"
)

// trailingWS captura el separador de linea (\n o fin de texto) precedido por
// espacios/tabs/CR, para poder re-emitirlo con $1. RE2 no soporta lookahead, asi
// que capturamos el separador en vez de mirarlo hacia adelante.
var trailingWS = regexp.MustCompile(`[ \t\r]+(\n|$)`)

// normalizeForHash quita el whitespace al final de cada linea y los CR, de modo
// que textos que solo difieren en eso (por ejemplo CRLF vs LF) hasheen igual.
func normalizeForHash(text string) string {
	return trailingWS.ReplaceAllString(text, "$1")
}

// ComputeFileHash devuelve el tag de 4 hex MAYUSCULAS de un texto, tras
// normalizarlo. El hash solo necesita consistencia interna en Atenea: el read
// produce el tag y el edit lo verifica con esta misma funcion. Por eso alcanza
// con crc32 de la stdlib tomando los 16 bits bajos, y se mantiene el formato de
// 4 hex.
func ComputeFileHash(text string) string {
	sum := crc32.ChecksumIEEE([]byte(normalizeForHash(text)))
	return fmt.Sprintf("%04X", sum&0xFFFF)
}
