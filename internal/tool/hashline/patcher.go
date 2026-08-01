package hashline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Filesystem abstrae el acceso a disco que necesita el Patcher: leer el archivo
// vivo y escribir el resultado. El test usa un fake en memoria.
type Filesystem interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
}

// OSFilesystem is the real atomic replacement implementation. ReplaceHook is
// intentionally optional and primarily supports fault-injection tests.
type OSFilesystem struct {
	ReplaceHook func(name string, data []byte, perm os.FileMode) error
}

func (f OSFilesystem) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
func (f OSFilesystem) WriteFile(name string, data []byte, perm os.FileMode) error {
	if f.ReplaceHook != nil {
		return f.ReplaceHook(name, data, perm)
	}
	return atomicReplace(name, data, perm)
}

// CommitUncertainError means rename completed, but syncing or closing the
// containing directory failed. The new bytes are visible and must not be
// retried blindly; durability across a crash is the only uncertain property.
type CommitUncertainError struct{ Err error }

func (e *CommitUncertainError) Error() string {
	return "edit committed, but durability is uncertain: " + e.Err.Error()
}
func (e *CommitUncertainError) Unwrap() error { return e.Err }

func atomicReplace(name string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(name)
	tmp, err := os.CreateTemp(dir, ".atenea-edit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpName)
		}
	}()
	_, err = tmp.Write(data)
	// Writing may clear set-ID bits, so restore the complete original mode only
	// after all bytes have been written.
	if err == nil {
		err = tmp.Chmod(perm)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpName, name)
	}
	if err != nil {
		return err
	}
	renamed = true
	d, err := os.Open(dir)
	if err != nil {
		return &CommitUncertainError{Err: err}
	}
	err = d.Sync()
	closeErr := d.Close()
	if err != nil {
		return &CommitUncertainError{Err: err}
	}
	if closeErr != nil {
		return &CommitUncertainError{Err: closeErr}
	}
	return nil
}

var patchPathLocks = struct {
	sync.Mutex
	m map[string]*pathLock
}{m: make(map[string]*pathLock)}

type pathLock struct {
	mu    sync.Mutex
	users int
}

func lockPatchPath(path string) func() {
	path = filepath.Clean(path)
	patchPathLocks.Lock()
	l := patchPathLocks.m[path]
	if l == nil {
		l = &pathLock{}
		patchPathLocks.m[path] = l
	}
	l.users++
	patchPathLocks.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		patchPathLocks.Lock()
		l.users--
		if l.users == 0 {
			delete(patchPathLocks.m, path)
		}
		patchPathLocks.Unlock()
	}
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

// CommittedError carries the actual committed result when only post-rename
// durability reporting failed.
type CommittedError struct {
	Result PatchResult
	Err    error
}

func (e *CommittedError) Error() string { return e.Err.Error() }
func (e *CommittedError) Unwrap() error { return e.Err }

// Apply validates the complete single-file shape before any filesystem access.
func (p *Patcher) Apply(patch Patch) (PatchResult, error) {
	if len(patch.Sections) != 1 {
		return PatchResult{}, fmt.Errorf("edit: patch debe contener exactamente una seccion")
	}
	s := patch.Sections[0]
	if s.Path == "" || s.Hash == "" || len(s.Edits) == 0 {
		return PatchResult{}, fmt.Errorf("edit: seccion incompleta")
	}
	if p.FS == nil || p.Snapshots == nil {
		return PatchResult{}, fmt.Errorf("edit: patcher no configurado")
	}
	unlock := lockPatchPath(s.Path)
	defer unlock()
	b, err := p.FS.ReadFile(s.Path)
	if err != nil {
		return PatchResult{}, err
	}

	// Normalize for editing while remembering the exact storage convention.
	hadBOM := strings.HasPrefix(string(b), "\uFEFF")
	raw := strings.TrimPrefix(string(b), "\uFEFF")
	eol := dominantEOL(raw)
	norm := strings.ReplaceAll(raw, "\r\n", "\n")
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
		if allStablePositionInserts(s.Edits) {
			warnings = append(warnings, "el archivo cambio desde el read; aplicado igual por ser insercion de posicion estable")
		} else {
			mapped, ok := recoverEdits(snap.Text, norm, s.Edits)
			if !ok {
				return PatchResult{}, &MismatchError{Path: s.Path, Expected: s.Hash, Live: liveHash, Recognized: true, Context: "anchored region moved ambiguously or changed"}
			}
			s.Edits = mapped
		}
	}
	if line, ok := firstUnseenAnchoredLine(patch.Sections[0].Edits, snap.Seen); !ok {
		return PatchResult{}, fmt.Errorf("edit: no edites lineas que no leiste (linea %d no fue mostrada por read en %s)", line, s.Path)
	}

	ar, err := ApplyEdits(lines, s.Edits)
	if err != nil {
		return PatchResult{}, err
	}

	// Preserve final newline, BOM, and dominant/original line-ending style.
	newText := ar.Text
	if strings.HasSuffix(norm, "\n") {
		newText += "\n"
	}
	stored := strings.ReplaceAll(newText, "\n", eol)
	if hadBOM {
		stored = "\uFEFF" + stored
	}
	perm := os.FileMode(0o644)
	if _, ok := p.FS.(OSFilesystem); ok {
		info, statErr := os.Stat(s.Path)
		if statErr != nil {
			return PatchResult{}, statErr
		}
		perm = info.Mode()
	}
	writeErr := p.FS.WriteFile(s.Path, []byte(stored), perm)
	var uncertain *CommitUncertainError
	if writeErr != nil && !errors.As(writeErr, &uncertain) {
		return PatchResult{}, writeErr
	}

	newHash, recorded := p.Snapshots.Record(s.Path, newText)
	if recorded && ar.FirstChangedLine > 0 {
		p.Snapshots.RecordSeenLines(s.Path, newHash, []int{ar.FirstChangedLine})
	}

	result := PatchResult{
		FirstChangedLine: ar.FirstChangedLine,
		Warnings:         warnings,
		OldText:          norm,
		NewText:          newText,
	}
	if recorded {
		result.Header = FormatHeader(s.Path, newHash)
	} else {
		result.Warnings = append(result.Warnings, "committed content could not be retained as an unambiguous snapshot; no hashline header was issued. Change or reduce the content or start a new session, then read before editing again")
	}
	if uncertain != nil {
		return result, &CommittedError{Result: result, Err: uncertain}
	}
	return result, nil
}

func dominantEOL(s string) string {
	crlf := strings.Count(s, "\r\n")
	lf := strings.Count(s, "\n") - crlf
	cr := strings.Count(strings.ReplaceAll(s, "\r\n", ""), "\r")
	if crlf > 0 && crlf >= lf && crlf >= cr {
		return "\r\n"
	}
	if cr > 0 && cr > lf {
		return "\r"
	}
	return "\n"
}

// recoverEdits relocates each unchanged anchored region by exact line content.
// Every region must occur exactly once in live and all edits must share one
// offset. This intentionally fails closed rather than guessing at duplicates.
func recoverEdits(snapshotText, liveText string, edits []Edit) ([]Edit, bool) {
	base, live := SplitLines(strings.ReplaceAll(strings.ReplaceAll(snapshotText, "\r\n", "\n"), "\r", "\n")), SplitLines(liveText)
	out := append([]Edit(nil), edits...)
	offsetSet, offset := false, 0
	for i, e := range out {
		if e.Kind == Insert && (e.Cursor == BOF || e.Cursor == EOF) {
			continue
		}
		start, end := e.Anchor, e.Anchor
		if e.Kind != Insert {
			start, end = e.Range.Start, e.Range.End
		}
		if start < 1 || end > len(base) {
			return nil, false
		}
		needle := base[start-1 : end]
		found := -1
		for at := 0; at+len(needle) <= len(live); at++ {
			match := true
			for j := range needle {
				if live[at+j] != needle[j] {
					match = false
					break
				}
			}
			if match {
				if found >= 0 {
					return nil, false
				}
				found = at
			}
		}
		if found < 0 {
			return nil, false
		}
		delta := found + 1 - start
		if offsetSet && delta != offset {
			return nil, false
		}
		offset, offsetSet = delta, true
		if e.Kind == Insert {
			out[i].Anchor += delta
		} else {
			out[i].Range.Start += delta
			out[i].Range.End += delta
		}
	}
	return out, offsetSet
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
