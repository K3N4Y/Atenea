package hashline

import "fmt"

// EditKind distingue las operaciones de un Edit: reemplazo de rango (SWAP),
// borrado (DEL) e insercion (INS.*).
type EditKind int

const (
	Replace EditKind = iota // SWAP: reemplaza [Range.Start, Range.End]
	Delete                  // DEL: borra [Range.Start, Range.End]
	Insert                  // INS.*: inserta respecto a un ancla
)

// Cursor indica donde inserta un Edit de tipo Insert: antes o despues del ancla,
// al inicio (BOF) o al final (EOF) del archivo.
type Cursor int

const (
	BeforeAnchor Cursor = iota // INS.PRE
	AfterAnchor                // INS.POST
	BOF                        // INS.HEAD
	EOF                        // INS.TAIL
)

// Range es un rango de lineas 1-indexed inclusive [Start, End].
type Range struct {
	Start, End int
}

// Edit es una operacion sobre un archivo. Kind elige la semantica: Replace y
// Delete usan Range; Insert usa Cursor y Anchor. Text es el payload (lineas
// unidas por "\n") para Replace e Insert.
type Edit struct {
	Kind   EditKind
	Range  Range
	Cursor Cursor
	Anchor int
	Text   string
}

// Section es un bloque del patch: un archivo (Path), el hash esperado (Hash) y
// sus ediciones (Edits).
type Section struct {
	Path, Hash string
	Edits      []Edit
}

// Patch es el conjunto de secciones parseadas de un patch hashline.
type Patch struct {
	Sections []Section
}

// ApplyResult es el resultado de aplicar las ediciones a un archivo: el texto
// final (lineas unidas por "\n", sin "\n" final), la primera linea cambiada y
// las advertencias acumuladas.
type ApplyResult struct {
	Text             string
	FirstChangedLine int
	Warnings         []string
}

// MissingTagError indica que el patch (o una seccion) no trae el header
// [ruta#HASH]. Detail agrega contexto accionable (que se encontro en su lugar).
type MissingTagError struct {
	Detail string
}

func (e *MissingTagError) Error() string {
	return "falta el header [ruta#HASH]: " + e.Detail
}

// MismatchError indica que el archivo vivo no corresponde al hash que el edit
// trae: o cambio entre el read y el edit (Recognized, el hash si es de la sesion),
// o el hash no es de esta sesion (no Recognized, posible tag inventado). Context
// agrega detalle accionable opcional.
type MismatchError struct {
	Path, Expected, Live string
	Recognized           bool
	Context              string
}

func (e *MismatchError) Error() string {
	if e.Recognized {
		return fmt.Sprintf("edit: el archivo %s cambio entre el read y el edit (hash %s -> %s); copia el header [path#newhash] del edit previo o re-lee", e.Path, e.Expected, e.Live)
	}
	return fmt.Sprintf("edit: hash #%s no es de esta sesion para %s; re-lee el archivo y nunca inventes el tag", e.Expected, e.Path)
}
