package hashline

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
)

// Filesystem abstrae el acceso a disco que necesita el Patcher: leer el archivo
// vivo y escribir el resultado. El test usa un fake en memoria.
type Filesystem interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
}

type removingFilesystem interface{ Remove(string) error }
type renamingFilesystem interface{ Rename(string, string) error }
type contentMovingFilesystem interface {
	MoveWithContent(from, to string, data []byte, perm os.FileMode) error
}

// OSFilesystem is the production filesystem adapter. Its mutation machinery is
// deliberately kept behind package-private ops so tests can fail every syscall
// boundary without exposing fault-injection complexity to callers.
type OSFilesystem struct {
	ReplaceHook func(name string, data []byte, perm os.FileMode) error
}

func (f OSFilesystem) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
func (f OSFilesystem) WriteFile(name string, data []byte, perm os.FileMode) error {
	if f.ReplaceHook != nil {
		return f.ReplaceHook(name, data, perm)
	}
	return atomicReplaceWithOps(osFilesystemOps{}, name, data, perm)
}
func (OSFilesystem) Remove(name string) error { return durableRemoveWithOps(osFilesystemOps{}, name) }
func (OSFilesystem) Rename(from, to string) error {
	return durableMoveWithOps(osFilesystemOps{}, from, to)
}
func (OSFilesystem) MoveWithContent(from, to string, data []byte, perm os.FileMode) error {
	return durableMoveContentWithOps(osFilesystemOps{}, from, to, data, perm)
}

// CreateExclusive durably publishes a new file without replacing an existing path.
func (OSFilesystem) CreateExclusive(name string, data []byte, perm os.FileMode) error {
	return atomicCreateWithOps(osFilesystemOps{}, name, data, perm)
}

type filesystemFile interface {
	io.Reader
	Write([]byte) (int, error)
	Stat() (os.FileInfo, error)
	Chmod(os.FileMode) error
	Sync() error
	Close() error
	Name() string
}

type filesystemOps interface {
	CreateTemp(string, string) (filesystemFile, error)
	Open(string) (filesystemFile, error)
	Lstat(string) (os.FileInfo, error)
	Remove(string) error
	Rename(string, string) error
	Link(string, string) error
}

type osFilesystemOps struct{}

func (osFilesystemOps) CreateTemp(d, p string) (filesystemFile, error) { return os.CreateTemp(d, p) }
func (osFilesystemOps) Open(name string) (filesystemFile, error)       { return os.Open(name) }
func (osFilesystemOps) Lstat(name string) (os.FileInfo, error)         { return os.Lstat(name) }
func (osFilesystemOps) Remove(name string) error                       { return os.Remove(name) }
func (osFilesystemOps) Rename(a, b string) error                       { return os.Rename(a, b) }
func (osFilesystemOps) Link(a, b string) error                         { return os.Link(a, b) }

func syncDirectoryWithOps(ops filesystemOps, dir string) error {
	d, err := ops.Open(dir)
	if err != nil {
		return err
	}
	if err = d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

func durableRemoveWithOps(ops filesystemOps, name string) error {
	if err := ops.Remove(name); err != nil {
		return err
	}
	if err := syncDirectoryWithOps(ops, filepath.Dir(name)); err != nil {
		return &CommitUncertainError{Err: err}
	}
	return nil
}

func durableMoveWithOps(ops filesystemOps, from, to string) error {
	if _, err := ops.Lstat(to); err == nil {
		return fmt.Errorf("move destination already exists: %s", to)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// Link is an atomic, no-overwrite publication on one filesystem. Unlike a
	// check followed by Rename it cannot clobber a destination created by a race.
	if err := ops.Link(from, to); err == nil {
		sourceDir, destinationDir := filepath.Dir(from), filepath.Dir(to)
		if filepath.Clean(sourceDir) == filepath.Clean(destinationDir) {
			if err := ops.Remove(from); err != nil {
				return &DestinationCommittedError{Err: err, SourceRemains: true}
			}
			if err := syncDirectoryWithOps(ops, sourceDir); err != nil {
				return &CommitUncertainError{Err: err}
			}
			return nil
		}
		if err := syncDirectoryWithOps(ops, destinationDir); err != nil {
			return &DestinationCommittedError{Err: err, SourceRemains: true}
		}
		if err := ops.Remove(from); err != nil {
			return &DestinationCommittedError{Err: err, SourceRemains: true}
		}
		if err := syncDirectoryWithOps(ops, sourceDir); err != nil {
			return &CommitUncertainError{Err: err}
		}
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	source, err := ops.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	tmp, err := ops.CreateTemp(filepath.Dir(to), ".atenea-move-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	published := false
	defer func() {
		if !published {
			_ = ops.Remove(tmpName)
		}
	}()
	if _, err = io.Copy(tmp, source); err == nil {
		err = tmp.Chmod(info.Mode())
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = ops.Link(tmpName, to)
	}
	if err != nil {
		return err
	}
	published = true
	_ = ops.Remove(tmpName)
	if err = syncDirectoryWithOps(ops, filepath.Dir(to)); err != nil {
		return &DestinationCommittedError{Err: err, SourceRemains: true}
	}
	if err = ops.Remove(from); err != nil {
		return &DestinationCommittedError{Err: err, SourceRemains: true}
	}
	if err = syncDirectoryWithOps(ops, filepath.Dir(from)); err != nil {
		return &CommitUncertainError{Err: err}
	}
	return nil
}

// DestinationCommittedError reports a cross-device move whose destination is
// durably visible but whose source could not be durably removed.
type DestinationCommittedError struct {
	Err           error
	SourceRemains bool
}

func (e *DestinationCommittedError) Error() string {
	return "move destination committed; source remains; do not retry: " + e.Err.Error()
}
func (e *DestinationCommittedError) Unwrap() error { return e.Err }

// CommitUncertainError means rename completed, but syncing or closing the
// containing directory failed. The new bytes are visible and must not be
// retried blindly; durability across a crash is the only uncertain property.
type CommitUncertainError struct{ Err error }

func (e *CommitUncertainError) Error() string {
	return "edit committed, but durability is uncertain: " + e.Err.Error()
}
func (e *CommitUncertainError) Unwrap() error { return e.Err }

// durableMoveContentWithOps publishes computed destination bytes without
// overwriting a racing destination, then removes the still-untouched source.
func durableMoveContentWithOps(ops filesystemOps, from, to string, data []byte, perm os.FileMode) error {
	if _, err := ops.Lstat(to); err == nil {
		return fmt.Errorf("move destination already exists: %s", to)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := ops.CreateTemp(filepath.Dir(to), ".atenea-move-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	published := false
	defer func() {
		if !published {
			_ = ops.Remove(tmpName)
		}
	}()
	n, err := tmp.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
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
		err = ops.Link(tmpName, to)
	}
	if err != nil {
		return err
	}
	published = true
	_ = ops.Remove(tmpName)
	destinationDir, sourceDir := filepath.Dir(to), filepath.Dir(from)
	if err = syncDirectoryWithOps(ops, destinationDir); err != nil {
		return &DestinationCommittedError{Err: err, SourceRemains: true}
	}
	if err = ops.Remove(from); err != nil {
		return &DestinationCommittedError{Err: err, SourceRemains: true}
	}
	if err = syncDirectoryWithOps(ops, sourceDir); err != nil {
		return &CommitUncertainError{Err: err}
	}
	return nil
}

func atomicCreateWithOps(ops filesystemOps, name string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(name)
	tmp, err := ops.CreateTemp(dir, ".atenea-create-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	published := false
	defer func() {
		if !published {
			_ = ops.Remove(tmpName)
		}
	}()
	n, err := tmp.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
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
		err = ops.Link(tmpName, name)
	}
	if err != nil {
		return err
	}
	published = true
	_ = ops.Remove(tmpName)
	if err := syncDirectoryWithOps(ops, dir); err != nil {
		return &CommitUncertainError{Err: err}
	}
	return nil
}

func atomicReplaceWithOps(ops filesystemOps, name string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(name)
	tmp, err := ops.CreateTemp(dir, ".atenea-edit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = ops.Remove(tmpName)
		}
	}()
	n, err := tmp.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
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
		err = ops.Rename(tmpName, name)
	}
	if err != nil {
		return err
	}
	renamed = true
	if err := syncDirectoryWithOps(ops, dir); err != nil {
		return &CommitUncertainError{Err: err}
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

// LockPaths acquires the process-wide ownership locks used by patch commits.
// Other public edit strategies use it to share the same ownership domain.
func LockPaths(paths ...string) func() { return lockPatchPaths(paths...) }

func lockPatchPaths(paths ...string) func() {
	clean := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		clean = append(clean, path)
	}
	sort.Strings(clean)
	unlocks := make([]func(), 0, len(clean))
	for _, path := range clean {
		unlocks = append(unlocks, lockPatchPath(path))
	}
	return func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
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
	FS               Filesystem
	Snapshots        SnapshotStore
	Clipboard        *Clipboard
	Blocks           BlockResolver
	EnforceSeenLines bool
	mu               sync.Mutex
}

// NewPatcher crea un Patcher con su Filesystem y su SnapshotStore.
func NewPatcher(fs Filesystem, snaps SnapshotStore) *Patcher {
	return &Patcher{FS: fs, Snapshots: snaps, Clipboard: NewClipboard(), Blocks: StructuralBlockResolver{}}
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

type preflightFilesystem struct{ Filesystem }

func (f preflightFilesystem) WriteFile(string, []byte, os.FileMode) error             { return nil }
func (f preflightFilesystem) Remove(string) error                                     { return nil }
func (f preflightFilesystem) Rename(string, string) error                             { return nil }
func (preflightFilesystem) MoveWithContent(string, string, []byte, os.FileMode) error { return nil }

type preflightSnapshots struct{ SnapshotStore }

func (s preflightSnapshots) Record(_ string, text string) (string, bool) {
	return ComputeFileHash(text), true
}
func (preflightSnapshots) RecordSeenLines(string, string, []int)    {}
func (preflightSnapshots) RecordSeenSnapshot(string, uint64, []int) {}
func (preflightSnapshots) RecordSeenContent(string, string, []int)  {}
func (s preflightSnapshots) Candidates(path, hash string) []*Snapshot {
	return s.SnapshotStore.Candidates(path, hash)
}
func (s preflightSnapshots) ByContent(text string) []*Snapshot {
	return s.SnapshotStore.ByContent(text)
}
func (s preflightSnapshots) FindByHash(hash string) []*Snapshot {
	return s.SnapshotStore.FindByHash(hash)
}
func (preflightSnapshots) Clear()                  {}
func (preflightSnapshots) Invalidate(string)       {}
func (preflightSnapshots) Relocate(string, string) {}
func (s preflightSnapshots) ReadOnly() SnapshotStore {
	return preflightSnapshots{s.SnapshotStore.ReadOnly()}
}

// Preview applies a patch against the current filesystem and forked register
// state without writing files, snapshots, or live clipboard state.
func (p *Patcher) Preview(patch Patch) (PatchResult, error) {
	results, err := p.PreviewResults(patch)
	if len(results) == 0 {
		return PatchResult{}, err
	}
	return results[len(results)-1], err
}

// PreviewResults is the read-only, mutex-protected preview seam. It preserves
// one result per section and runs the exact structural resolution used by Apply.
func (p *Patcher) PreviewResults(patch Patch) ([]PatchResult, error) {
	if len(patch.Sections) == 0 || p.FS == nil || p.Snapshots == nil {
		return nil, fmt.Errorf("edit: patcher is not configured or patch is empty")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	preview := &Patcher{FS: preflightFilesystem{p.FS}, Snapshots: preflightSnapshots{p.Snapshots.ReadOnly()}, Clipboard: p.Clipboard.Clone(), Blocks: p.Blocks, EnforceSeenLines: p.EnforceSeenLines}
	results := make([]PatchResult, 0, len(patch.Sections))
	for _, section := range patch.Sections {
		result, err := preview.applySection(section)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

// PreflightConfiguredResults validates every section with the requested
// provenance policy while keeping files, snapshots, and registers read-only.
func (p *Patcher) PreflightConfiguredResults(patch Patch, enforceSeenLines bool) ([]PatchResult, error) {
	if len(patch.Sections) == 0 || p.FS == nil || p.Snapshots == nil {
		return nil, fmt.Errorf("edit: patcher is not configured or patch is empty")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	preview := &Patcher{FS: preflightFilesystem{p.FS}, Snapshots: preflightSnapshots{p.Snapshots.ReadOnly()}, Clipboard: p.Clipboard.Clone(), Blocks: p.Blocks, EnforceSeenLines: enforceSeenLines}
	results := make([]PatchResult, 0, len(patch.Sections))
	for _, section := range patch.Sections {
		result, err := preview.applySection(section)
		if err != nil {
			return results, fmt.Errorf("edit: preflight %s: %w", section.Path, err)
		}
		results = append(results, result)
	}
	return results, nil
}

// Apply prepares every section against forked registers before the first write.
// During commit, register changes are published only after their file commits.
// ForkClipboard returns an isolated register snapshot for read-only projections.
func (p *Patcher) ForkClipboard() *Clipboard {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Clipboard.Clone()
}

func (p *Patcher) Apply(patch Patch) (PatchResult, error) {
	results, err := p.ApplyConfiguredResults(patch, p.EnforceSeenLines)
	if len(results) == 0 {
		return PatchResult{}, err
	}
	return results[len(results)-1], err
}

// ApplyConfigured applies one call with an immutable turn's provenance policy.
// It retains one ordered result per section; Apply remains the compatibility
// facade for callers that only operate on one section.
func (p *Patcher) ApplyConfigured(patch Patch, enforceSeenLines bool) (PatchResult, error) {
	results, err := p.ApplyConfiguredResults(patch, enforceSeenLines)
	if len(results) == 0 {
		return PatchResult{}, err
	}
	return results[len(results)-1], err
}

// ApplyOptions carries transaction-local work that must run after the final
// semantic preflight and while all patch paths remain exclusively owned.
// Failure aborts before the first file commit.
type ApplyOptions struct {
	PostPreflight func() error
}

func (p *Patcher) ApplyConfiguredResults(patch Patch, enforceSeenLines bool) ([]PatchResult, error) {
	return p.ApplyConfiguredResultsWithOptions(patch, enforceSeenLines, ApplyOptions{})
}

func (p *Patcher) ApplyConfiguredResultsWithOptions(patch Patch, enforceSeenLines bool, options ApplyOptions) ([]PatchResult, error) {
	if len(patch.Sections) == 0 {
		return nil, fmt.Errorf("edit: patch has no sections")
	}
	if p.FS == nil || p.Snapshots == nil {
		return nil, fmt.Errorf("edit: patcher is not configured")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	paths := make([]string, 0, len(patch.Sections)*2)
	for _, section := range patch.Sections {
		paths = append(paths, section.Path)
		if section.FileOp.MoveTo != "" {
			paths = append(paths, section.FileOp.MoveTo)
		}
	}
	unlock := lockPatchPaths(paths...)
	defer unlock()
	previous := p.EnforceSeenLines
	p.EnforceSeenLines = enforceSeenLines
	defer func() { p.EnforceSeenLines = previous }()

	// A complete unseen-line reveal intentionally grants provenance for an
	// identical retry. Keep preflight side-effect-free and enforce this guard
	// against the live store during apply, where that grant can be published.
	preview := &Patcher{
		FS:               preflightFilesystem{p.FS},
		Snapshots:        preflightSnapshots{p.Snapshots},
		Clipboard:        p.Clipboard.Clone(),
		Blocks:           p.Blocks,
		EnforceSeenLines: false,
	}
	for _, section := range patch.Sections {
		if _, err := preview.applySection(section); err != nil {
			return nil, fmt.Errorf("edit: preflight %s: %w", section.Path, err)
		}
	}
	if options.PostPreflight != nil {
		if err := options.PostPreflight(); err != nil {
			return nil, fmt.Errorf("edit: post-preflight preparation: %w", err)
		}
	}

	results := make([]PatchResult, 0, len(patch.Sections))
	for i, section := range patch.Sections {
		committedRegisters := p.Clipboard
		p.Clipboard = committedRegisters.Clone()
		result, err := p.applySection(section)
		if err != nil {
			var committed *CommittedError
			if errors.As(err, &committed) {
				// The mutation is visible, so register effects are part of the
				// landed prefix even if durability reporting is uncertain.
				results = append(results, committed.Result)
			} else {
				p.Clipboard = committedRegisters
			}
			if i > 0 {
				return results, fmt.Errorf("edit: partial commit after %d of %d sections: %w", i, len(patch.Sections), err)
			}
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (p *Patcher) applySection(s Section) (PatchResult, error) {
	if s.Path == "" || s.Hash == "" || len(s.Edits) == 0 && !s.FileOp.Remove && s.FileOp.MoveTo == "" {
		return PatchResult{}, fmt.Errorf("edit: seccion incompleta")
	}
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
	candidates := p.Snapshots.Candidates(s.Path, s.Hash)
	if len(candidates) == 0 {
		return PatchResult{}, &MismatchError{Path: s.Path, Expected: s.Hash, Live: liveHash, Recognized: false}
	}
	snap := selectSnapshotCandidate(candidates, norm, s.Edits)
	if snap == nil {
		return PatchResult{}, &MismatchError{Path: s.Path, Expected: s.Hash, Live: liveHash, Recognized: true, Context: "multiple colliding snapshots match; read the file again to establish an unambiguous header"}
	}
	if s.FileOp.Remove {
		if len(s.Edits) != 0 {
			return PatchResult{}, fmt.Errorf("hashline: remove cannot be combined with edits")
		}
		if liveHash != s.Hash {
			return PatchResult{}, &MismatchError{Path: s.Path, Expected: s.Hash, Live: liveHash, Recognized: true}
		}
		fs, ok := p.FS.(removingFilesystem)
		if !ok {
			return PatchResult{}, fmt.Errorf("edit: filesystem does not support remove")
		}
		removeErr := fs.Remove(s.Path)
		var uncertain *CommitUncertainError
		if removeErr != nil && !errors.As(removeErr, &uncertain) {
			return PatchResult{}, removeErr
		}
		p.Snapshots.Invalidate(s.Path)
		result := PatchResult{OldText: norm}
		if uncertain != nil {
			return result, &CommittedError{Result: result, Err: uncertain}
		}
		return result, nil
	}

	var warnings []string
	if snap.Text != norm {
		if allStablePositionInserts(s.Edits) {
			warnings = append(warnings, "file changed since the snapshot; applied a stable head/tail insertion")
		} else {
			mapped, ok := recoverEdits(snap.Text, norm, s.Edits)
			if !ok {
				return PatchResult{}, &MismatchError{Path: s.Path, Expected: s.Hash, Live: liveHash, Recognized: true, Context: "anchored region moved ambiguously, was split, or changed"}
			}
			s.Edits = mapped
			warnings = append(warnings, "file changed since the snapshot; unchanged anchored lines were relocated by a uniform offset")
		}
	}

	if p.EnforceSeenLines {
		if line, ok := firstUnseenAnchoredLine(s.Edits, snap.Seen); !ok {
			return PatchResult{}, revealUnseenLines(p.Snapshots, s.Path, snap, lines, s.Edits, line)
		}
	}
	for i := range s.Edits {
		e := &s.Edits[i]
		if !e.Block && !e.AfterBlock {
			continue
		}
		start := e.Range.Start
		if e.AfterBlock {
			start = e.Anchor
		}
		if p.Blocks == nil {
			if e.AfterBlock {
				e.AfterBlock = false
				warnings = append(warnings, "block resolver unavailable; inserted after anchor line")
				continue
			}
			return PatchResult{}, fmt.Errorf("hashline: block operation requires a structural resolver")
		}
		end, err := p.Blocks.ResolveBlock(s.Path, lines, start)
		if err != nil {
			if e.AfterBlock {
				e.AfterBlock = false
				e.BlockStart = start
				warnings = append(warnings, err.Error()+"; inserted after anchor line")
				continue
			}
			return PatchResult{}, err
		}
		if e.AfterBlock {
			e.BlockStart = start
			e.Anchor, e.AfterBlock = end, false
		} else {
			e.Range.End, e.Block = end, false
		}
	}
	ar := ApplyResult{Text: strings.TrimSuffix(norm, "\n")}
	if len(s.Edits) != 0 {
		var err error
		ar, err = ApplyEditsWithClipboard(lines, s.Edits, p.Clipboard)
		if err != nil {
			return PatchResult{}, err
		}
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
	writePath := s.Path
	if s.FileOp.MoveTo != "" {
		if filepath.Clean(s.Path) == filepath.Clean(s.FileOp.MoveTo) {
			return PatchResult{}, fmt.Errorf("hashline: move destination equals source")
		}
		writePath = s.FileOp.MoveTo
	}
	var writeErr error
	if s.FileOp.MoveTo != "" {
		if fs, ok := p.FS.(contentMovingFilesystem); ok {
			writeErr = fs.MoveWithContent(s.Path, writePath, []byte(stored), perm)
		} else if len(s.Edits) == 0 {
			fs, ok := p.FS.(renamingFilesystem)
			if !ok {
				return PatchResult{}, fmt.Errorf("edit: filesystem does not support move")
			}
			writeErr = fs.Rename(s.Path, writePath)
		} else {
			return PatchResult{}, fmt.Errorf("edit: filesystem does not support secure move-with-content")
		}
	} else {
		writeErr = p.FS.WriteFile(writePath, []byte(stored), perm)
	}
	var uncertain *CommitUncertainError
	var destinationCommitted *DestinationCommittedError
	if writeErr != nil && !errors.As(writeErr, &uncertain) && !errors.As(writeErr, &destinationCommitted) {
		return PatchResult{}, writeErr
	}

	if _, preflight := p.FS.(preflightFilesystem); preflight {
		return PatchResult{FirstChangedLine: ar.FirstChangedLine, Warnings: warnings, OldText: norm, NewText: newText}, nil
	}
	landed, readErr := p.FS.ReadFile(writePath)
	if readErr != nil {
		result := PatchResult{FirstChangedLine: ar.FirstChangedLine, Warnings: warnings, OldText: norm, NewText: newText}
		return result, &CommittedError{Result: result, Err: fmt.Errorf("edit: committed but readback failed; commit state is uncertain; do not retry: %w", readErr)}
	}
	landedText := strings.TrimPrefix(string(landed), "\uFEFF")
	landedText = strings.ReplaceAll(strings.ReplaceAll(landedText, "\r\n", "\n"), "\r", "\n")
	if landedText != newText {
		warnings = append(warnings, "content changed during write; snapshot reflects bytes read back from disk")
	}
	if s.FileOp.MoveTo != "" && (destinationCommitted == nil || !destinationCommitted.SourceRemains) {
		p.Snapshots.Relocate(s.Path, writePath)
	}
	newHash, recorded := p.Snapshots.Record(writePath, landedText)
	if recorded {
		// Carry visible diff provenance into the fresh destination snapshot.
		p.Snapshots.RecordSeenContent(writePath, landedText, diffVisibleNewLines(norm, landedText, 3))
	}

	result := PatchResult{
		FirstChangedLine: ar.FirstChangedLine,
		Warnings:         warnings,
		OldText:          norm,
		NewText:          landedText,
	}
	if recorded {
		result.Header = FormatHeader(writePath, newHash)
	} else {
		result.Warnings = append(result.Warnings, "committed content could not be retained as an unambiguous snapshot; no hashline header was issued. Change or reduce the content or start a new session, then read before editing again")
	}
	if destinationCommitted != nil {
		result.Warnings = append(result.Warnings, destinationCommitted.Error())
		return result, &CommittedError{Result: result, Err: destinationCommitted}
	}
	if uncertain != nil {
		return result, &CommittedError{Result: result, Err: uncertain}
	}
	return result, nil
}

func dominantEOL(s string) string {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			if i > 0 && s[i-1] == '\r' {
				return "\r\n"
			}
			return "\n"
		case '\r':
			if i+1 >= len(s) || s[i+1] != '\n' {
				return "\r"
			}
		}
	}
	return "\n"
}

func selectSnapshotCandidate(candidates []*Snapshot, live string, edits []Edit) *Snapshot {
	var exact []*Snapshot
	for _, candidate := range candidates {
		if candidate.Text == live {
			exact = append(exact, candidate)
		}
	}
	if len(exact) == 1 {
		return exact[0]
	}
	if len(exact) > 1 {
		return nil
	}
	var valid []*Snapshot
	for _, candidate := range candidates {
		if allStablePositionInserts(edits) {
			valid = append(valid, candidate)
			continue
		}
		if _, ok := recoverEdits(candidate.Text, live, edits); ok {
			valid = append(valid, candidate)
		}
	}
	if len(valid) == 1 {
		return valid[0]
	}
	return nil
}

func revealUnseenLines(store SnapshotStore, path string, snap *Snapshot, live []string, edits []Edit, first int) error {
	start, end := first, first
	for _, e := range edits {
		if e.Kind == Insert && (e.Cursor == BOF || e.Cursor == EOF) {
			continue
		}
		a, b := e.Anchor, e.Anchor
		if e.Kind != Insert {
			a, b = e.Range.Start, e.Range.End
		}
		if a < start {
			start = a
		}
		if b > end {
			end = b
		}
	}
	complete := start >= 1 && end <= len(live) && end-start+1 <= 40
	shownEnd := min(end, min(len(live), start+39))
	var body strings.Builder
	for n := start; n <= shownEnd && n >= 1; n++ {
		line := live[n-1]
		if len([]rune(line)) > 512 {
			line = string([]rune(line)[:512])
			complete = false
		}
		fmt.Fprintf(&body, "\n%d:%s", n, line)
	}
	if complete {
		seen := make([]int, 0, end-start+1)
		for n := start; n <= end; n++ {
			seen = append(seen, n)
		}
		store.RecordSeenSnapshot(path, snap.Version, seen)
		return fmt.Errorf("hashline: line %d was not seen; actual anchored content is:%s\nReview it, then retry the identical patch", first, body.String())
	}
	return fmt.Errorf("hashline: line %d was not seen; preview was truncated and no lines were marked seen:%s\nRead an explicit range covering every anchored line before editing", first, body.String())
}

func diffVisibleNewLines(oldText, newText string, context int) []int {
	oldLines, newLines := SplitLines(oldText), SplitLines(newText)
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	oldEnd, newEnd := len(oldLines), len(newLines)
	for oldEnd > prefix && newEnd > prefix && oldLines[oldEnd-1] == newLines[newEnd-1] {
		oldEnd--
		newEnd--
	}
	start, end := max(1, prefix+1-context), min(len(newLines), newEnd+context)
	out := make([]int, 0, max(0, end-start+1))
	for n := start; n <= end; n++ {
		out = append(out, n)
	}
	return out
}

func anchoredSpan(e Edit) (start, end int, stable bool) {
	if (e.Kind == Insert || e.Kind == Paste) && e.Range.Start == 0 {
		if e.Cursor == BOF || e.Cursor == EOF {
			return 0, 0, true
		}
		return e.Anchor, e.Anchor, false
	}
	return e.Range.Start, e.Range.End, false
}

// recoverEdits relocates each unchanged anchored region by exact line content.
// Every region must occur exactly once in live and all edits must share one
// offset. This intentionally fails closed rather than guessing at duplicates.
func recoverEdits(snapshotText, liveText string, edits []Edit) ([]Edit, bool) {
	base, live := SplitLines(strings.ReplaceAll(strings.ReplaceAll(snapshotText, "\r\n", "\n"), "\r", "\n")), SplitLines(liveText)
	out := append([]Edit(nil), edits...)
	offsetSet, offset := false, 0
	for i, e := range out {
		start, end, stable := anchoredSpan(e)
		if stable {
			continue
		}
		if start < 1 || end > len(base) {
			return nil, false
		}
		needle := base[start-1 : end]
		found, bestContext, tied := -1, -1, false
		for at := 0; at+len(needle) <= len(live); at++ {
			match := true
			for j := range needle {
				if live[at+j] != needle[j] {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			context := 0
			for b, l := start-2, at-1; b >= 0 && l >= 0 && base[b] == live[l]; b, l = b-1, l-1 {
				context++
			}
			for b, l := end, at+len(needle); b < len(base) && l < len(live) && base[b] == live[l]; b, l = b+1, l+1 {
				context++
			}
			if context > bestContext {
				found, bestContext, tied = at, context, false
			} else if context == bestContext {
				tied = true
			}
		}
		if found < 0 || tied {
			return nil, false
		}
		delta := found + 1 - start
		if offsetSet && delta != offset {
			return nil, false
		}
		offset, offsetSet = delta, true
		if (e.Kind == Insert || e.Kind == Paste) && e.Range.Start == 0 {
			out[i].Anchor += delta
		} else {
			out[i].Range.Start += delta
			out[i].Range.End += delta
		}
		if out[i].BlockStart > 0 {
			out[i].BlockStart += delta
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
		if (e.Kind != Insert && e.Kind != Paste) || e.Range.Start != 0 || (e.Cursor != BOF && e.Cursor != EOF) {
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
		start, end, stable := anchoredSpan(e)
		if stable {
			continue
		}
		for n := start; n <= end; n++ {
			if _, ok := seen[n]; !ok {
				return n, false
			}
		}
	}
	return 0, true
}
