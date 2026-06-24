package hashline

import (
	"fmt"
	"os"
	"strings"
)

// Filesystem abstrae el acceso a disco que necesita el Patcher: leer el archivo
// vivo y escribir el resultado. El test usa un fake en memoria.
type Filesystem interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
}

// OSFilesystem es el Filesystem por defecto: lee y escribe en el disco real.
type OSFilesystem struct{}

func (OSFilesystem) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
func (OSFilesystem) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}

// Patcher aplica un Patch a los archivos del Filesystem y regraba snapshots.
type Patcher struct {
	FS        Filesystem
	Snapshots SnapshotStore
}

// NewPatcher crea un Patcher con su Filesystem y su SnapshotStore.
func NewPatcher(fs Filesystem, snaps SnapshotStore) *Patcher {
	return &Patcher{FS: fs, Snapshots: snaps}
}

// PatchResult es el resultado de aplicar un patch: el header del archivo escrito
// (para encadenar edits), la primera linea cambiada, las advertencias y el texto
// viejo/nuevo (normalizados a LF) para que el tool arme el diff sin re-leer.
type PatchResult struct {
	Header           string
	FirstChangedLine int
	Warnings         []string
	OldText          string
	NewText          string
}

// Apply aplica la primera seccion del patch (v1: una seccion), escribe el
// archivo, regraba el snapshot con el hash nuevo y devuelve el header resultante.
func (p *Patcher) Apply(patch Patch) (PatchResult, error) {
	s := patch.Sections[0]

	b, err := p.FS.ReadFile(s.Path)
	if err != nil {
		return PatchResult{}, err
	}

	// Normaliza el contenido leido: quita BOM UTF-8 inicial y unifica los saltos
	// de linea a "\n".
	norm := strings.TrimPrefix(string(b), "\uFEFF")
	norm = strings.ReplaceAll(norm, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")

	lines := SplitLines(norm)

	// Hash del archivo vivo: si difiere del esperado por la seccion, hubo drift
	// (el archivo cambio desde el read).
	liveHash := ComputeFileHash(norm)
	snap := p.Snapshots.ByHash(s.Path, s.Hash)
	if snap == nil {
		return PatchResult{}, &MismatchError{
			Path:       s.Path,
			Expected:   s.Hash,
			Live:       liveHash,
			Recognized: false,
		}
	}

	var warnings []string
	if liveHash != s.Hash {
		// Drift. Un edit que solo inserta en posiciones estables (BOF/EOF) no
		// depende de los numeros de linea del read, asi que se aplica igual con un
		// warning. Cualquier otro edit anclado se rechaza sin escribir.
		if allStablePositionInserts(s.Edits) {
			warnings = append(warnings, "el archivo cambio desde el read; aplicado igual por ser insercion de posicion estable")
		} else {
			return PatchResult{}, &MismatchError{
				Path:       s.Path,
				Expected:   s.Hash,
				Live:       liveHash,
				Recognized: true,
			}
		}
	} else {
		// Sin drift: exigimos que las lineas que el edit ancla hayan sido vistas por
		// el read/write/edit previo (estan en Seen).
		if line, ok := firstUnseenAnchoredLine(s.Edits, snap.Seen); !ok {
			return PatchResult{}, fmt.Errorf("edit: no edites lineas que no leiste (linea %d no fue mostrada por read en %s)", line, s.Path)
		}
	}

	ar, err := ApplyEdits(lines, s.Edits)
	if err != nil {
		return PatchResult{}, err
	}

	// Preserva el newline final del original.
	newText := ar.Text
	if strings.HasSuffix(norm, "\n") {
		newText += "\n"
	}

	if err := p.FS.WriteFile(s.Path, []byte(newText), 0o644); err != nil {
		return PatchResult{}, err
	}

	newHash := p.Snapshots.Record(s.Path, newText)
	if ar.FirstChangedLine > 0 {
		p.Snapshots.RecordSeenLines(s.Path, newHash, []int{ar.FirstChangedLine})
	}

	return PatchResult{
		Header:           FormatHeader(s.Path, newHash),
		FirstChangedLine: ar.FirstChangedLine,
		Warnings:         warnings,
		OldText:          norm,
		NewText:          newText,
	}, nil
}

// allStablePositionInserts reporta si todos los edits son Insert con cursor en una
// posicion estable (BOF/EOF), que no depende de los numeros de linea del read.
func allStablePositionInserts(edits []Edit) bool {
	if len(edits) == 0 {
		return false
	}
	for _, e := range edits {
		if e.Kind != Insert || (e.Cursor != BOF && e.Cursor != EOF) {
			return false
		}
	}
	return true
}

// firstUnseenAnchoredLine recorre los edits y devuelve la primera linea anclada que
// no esta en seen (ok=false). Replace/Delete anclan todo su rango; Insert
// BeforeAnchor/AfterAnchor ancla la linea Anchor; Insert BOF/EOF no ancla ninguna.
// Si todas las lineas ancladas fueron vistas devuelve ok=true.
func firstUnseenAnchoredLine(edits []Edit, seen map[int]struct{}) (int, bool) {
	for _, e := range edits {
		switch e.Kind {
		case Replace, Delete:
			for n := e.Range.Start; n <= e.Range.End; n++ {
				if _, ok := seen[n]; !ok {
					return n, false
				}
			}
		case Insert:
			if e.Cursor == BeforeAnchor || e.Cursor == AfterAnchor {
				if _, ok := seen[e.Anchor]; !ok {
					return e.Anchor, false
				}
			}
		}
	}
	return 0, true
}
