package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// applyPatchEntries settles entries in authored order. Paths are resolved before
// the first mutation, while contents are read before each entry so repeated
// operations observe the preceding post-state.
func (et *EditTool) applyPatchEntries(ctx context.Context, entries []editmode.PatchEntry, allowCreateOverwrite bool) (Result, error) {
	var result Result
	targets := make(map[string]string)
	destinations := make(map[int]string)
	creationTargets := make(map[int]string)
	lockPaths := make([]string, 0, len(entries)*2)
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			return et.failPatchEntries(result, entries, targets, i, 0, err)
		}
		if entry.Op == "create" {
			abs, err := et.resolvePatchTarget(entry.Path, true)
			if err != nil {
				return et.failPatchEntries(result, entries, targets, i, 0, err)
			}
			creationTargets[i] = abs
			lockPaths = append(lockPaths, abs)
		} else {
			abs, ok := targets[entry.Path]
			if !ok {
				var err error
				abs, err = et.resolvePatchTarget(entry.Path, false)
				if err != nil {
					return et.failPatchEntries(result, entries, targets, i, 0, err)
				}
				targets[entry.Path] = abs
				lockPaths = append(lockPaths, abs)
			}
		}
		if entry.Rename != "" {
			dest, err := sandboxJoin(et.Root, entry.Rename, "edit")
			if err != nil {
				return et.failPatchEntries(result, entries, targets, i, 0, err)
			}
			if _, ok := et.FS.(hashline.OSFilesystem); ok {
				if err := rejectCreateAlias(et.Root, dest, entry.Rename, "edit"); err != nil {
					return et.failPatchEntries(result, entries, targets, i, 0, err)
				}
			}
			destinations[i] = dest
			lockPaths = append(lockPaths, dest)
		}
	}
	unlock := hashline.LockPaths(lockPaths...)
	defer unlock()
	filesByAuthoredPath := make(map[string]int)
	for entryIndex, entry := range entries {
		if err := ctx.Err(); err != nil {
			return et.failPatchEntries(result, entries, targets, entryIndex, entryIndex, err)
		}
		abs := targets[entry.Path]
		if created := creationTargets[entryIndex]; created != "" {
			abs = created
		}
		oldBytes, readErr := et.FS.ReadFile(abs)
		old := string(oldBytes)
		var next string
		var warnings []string
		var err error
		switch entry.Op {
		case "create":
			if readErr == nil && !allowCreateOverwrite {
				err = fmt.Errorf("Cannot create %s: file already exists. Use *** Update File to modify it in place.", entry.Path)
			} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				err = fmt.Errorf("Cannot create %s: existence check failed: %w", entry.Path, readErr)
			} else if entry.Diff == "" {
				err = fmt.Errorf("Create operation requires diff (file content)")
			} else {
				next = editmode.NormalizePatchCreateContent(entry.Diff)
			}
		case "delete":
			if readErr != nil {
				err = fmt.Errorf("File not found: %s", entry.Path)
			} else if remover, ok := et.FS.(interface{ Remove(string) error }); !ok {
				err = fmt.Errorf("filesystem does not support delete")
			} else {
				removeErr := remover.Remove(abs)
				var uncertain *hashline.CommitUncertainError
				if removeErr != nil && !errors.As(removeErr, &uncertain) {
					err = removeErr
				} else if visible, verifyErr := et.FS.ReadFile(abs); verifyErr == nil {
					err = fmt.Errorf("delete reported success but file remains visible with %d bytes", len(visible))
				} else if !errors.Is(verifyErr, os.ErrNotExist) && uncertain == nil {
					uncertain = &hashline.CommitUncertainError{Err: fmt.Errorf("delete visibility check failed: %w", verifyErr)}
				}
				if err == nil {
					diff := hashline.UnifiedDiff(entry.Path, old, "", 3)
					file := contract.FileResult{Path: abs, SourcePath: abs, Destination: abs, Operation: contract.FileDeleted, OldText: old, Diff: diff, FirstChangedLine: 1, Committed: true}
					if p := et.patcher(ctx); p != nil && p.Snapshots != nil {
						p.Snapshots.Invalidate(abs)
					}
					if uncertain != nil {
						file.Error, file.DisplayError = uncertain.Error(), "committed; durability uncertain; do not retry"
					}
					result.Output = appendPatchOutput(result.Output, "Deleted "+entry.Path)
					result.Diff = appendPatchOutput(result.Diff, diff)
					addLandedPatchFile(&result, filesByAuthoredPath, entry.Path, file, entry.Path)
					if uncertain != nil {
						return et.settleCommittedPatch(result, entries, targets, entryIndex+1), nil
					}
					continue
				}
			}
		case "update":
			if readErr != nil {
				err = fmt.Errorf("File not found: %s", entry.Path)
			} else if entry.Diff == "" {
				err = fmt.Errorf("Update operation requires diff (hunks)")
			} else {
				bom, body := splitBOM(old)
				eol := detectEOL(body)
				normalized := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
				next, warnings, err = editmode.ApplyContextPatch(normalized, entry.Path, entry.Diff, et.Fuzzy, et.threshold())
				next = bom + restoreEOL(next, eol)
			}
		default:
			err = fmt.Errorf("invalid patch operation %q", entry.Op)
		}
		if err != nil {
			return et.failPatchEntries(result, entries, targets, entryIndex, entryIndex, err)
		}

		target, displayTarget := abs, entry.Path
		if entry.Rename != "" {
			target = destinations[entryIndex]
			if target == "" {
				err = fmt.Errorf("rename destination was not resolved")
			} else if target == abs {
				err = fmt.Errorf("rename path is the same as source path")
			}
			if err == nil {
				if _, destinationErr := et.FS.ReadFile(target); destinationErr == nil {
					err = fmt.Errorf("Cannot rename %s to %s: destination already exists.", entry.Path, entry.Rename)
				} else if !errors.Is(destinationErr, os.ErrNotExist) {
					err = fmt.Errorf("Cannot rename %s to %s: destination existence check failed: %w", entry.Path, entry.Rename, destinationErr)
				}
			}
			if err == nil {
				if _, ok := et.FS.(hashline.OSFilesystem); ok {
					err = rejectCreateAlias(et.Root, target, entry.Rename, "edit")
				}
			}
			displayTarget = entry.Rename
		}
		if err != nil {
			return et.failPatchEntries(result, entries, targets, entryIndex, entryIndex, err)
		}
		if err = ctx.Err(); err != nil {
			return et.failPatchEntries(result, entries, targets, entryIndex, entryIndex, err)
		}
		writePath := abs
		if entry.Op == "create" && entry.Rename != "" {
			writePath = target
		}
		if _, ok := et.FS.(hashline.OSFilesystem); ok {
			if err = os.MkdirAll(filepath.Dir(writePath), 0755); err != nil {
				return et.failPatchEntries(result, entries, targets, entryIndex, entryIndex, err)
			}
			if entry.Rename != "" && entry.Op != "create" {
				if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					return et.failPatchEntries(result, entries, targets, entryIndex, entryIndex, err)
				}
			}
		}
		var writeErr error
		var uncertain *hashline.CommitUncertainError
		if entry.Rename != "" && entry.Op != "create" {
			mover, ok := et.FS.(interface {
				MoveWithContent(string, string, []byte, os.FileMode) error
			})
			if !ok {
				return et.failPatchEntries(result, entries, targets, entryIndex, entryIndex, fmt.Errorf("filesystem does not support secure move-with-content"))
			}
			writeErr = mover.MoveWithContent(abs, target, []byte(next), fileMode(abs))
		} else if entry.Op == "create" && !allowCreateOverwrite {
			creator, ok := et.FS.(interface {
				CreateExclusive(string, []byte, os.FileMode) error
			})
			if ok {
				writeErr = creator.CreateExclusive(writePath, []byte(next), fileMode(abs))
			} else {
				// Test and virtual adapters are serialized by LockPaths; OSFilesystem
				// always takes the exclusive publication path above.
				writeErr = et.FS.WriteFile(writePath, []byte(next), fileMode(abs))
			}
		} else {
			writeErr = et.FS.WriteFile(writePath, []byte(next), fileMode(abs))
		}
		var destinationCommitted *hashline.DestinationCommittedError
		if writeErr != nil && !errors.As(writeErr, &uncertain) && !errors.As(writeErr, &destinationCommitted) {
			return et.failPatchEntries(result, entries, targets, entryIndex, entryIndex, writeErr)
		}
		if destinationCommitted != nil {
			warnings = append(warnings, destinationCommitted.Error())
			uncertain = &hashline.CommitUncertainError{Err: destinationCommitted}
		}
		if entry.Rename != "" {
			targets[entry.Path] = target
		}
		landed, readbackErr := et.FS.ReadFile(target)
		if readbackErr != nil {
			unchanged := entry.Op == "create" && errors.Is(readbackErr, os.ErrNotExist)
			if entry.Rename != "" {
				if sourceNow, sourceErr := et.FS.ReadFile(abs); sourceErr == nil && string(sourceNow) == old {
					unchanged = true
				}
			}
			if unchanged {
				return et.failPatchEntries(result, entries, targets, entryIndex, entryIndex, fmt.Errorf("mutation reported success but no destination change was visible"))
			}
			file := contract.FileResult{Path: target, SourcePath: abs, Destination: target, Operation: contract.FileUpdated, OldText: old, NewText: next, Committed: true, Error: "committed but readback failed; state uncertain; do not retry", DisplayError: "committed; state uncertain; do not retry"}
			addLandedPatchFile(&result, filesByAuthoredPath, entry.Path, file, displayTarget)
			return et.settleCommittedPatch(result, entries, targets, entryIndex+1), nil
		}
		if string(landed) == old && entry.Op != "create" {
			return et.failPatchEntries(result, entries, targets, entryIndex, entryIndex, fmt.Errorf("write reported success but no file change was visible"))
		}
		if actual := string(landed); actual != next {
			next = actual
			warnings = append(warnings, "content changed during write; result and snapshot reflect bytes read back from disk")
		}
		diff := hashline.UnifiedDiff(displayTarget, old, next, 3)
		message, operation := "Updated "+entry.Path, contract.FileUpdated
		if entry.Op == "create" {
			message, operation = "Created "+entry.Path, contract.FileCreated
		}
		if entry.Rename != "" {
			message, operation = "Updated and moved "+entry.Path+" to "+entry.Rename, contract.FileMoved
		}
		result.Output = appendPatchOutput(result.Output, message)
		result.Diff = appendPatchOutput(result.Diff, diff)
		file := contract.FileResult{Path: target, SourcePath: abs, Destination: target, Operation: operation, OldText: old, NewText: next, Diff: diff, Warnings: warnings, FirstChangedLine: firstChangedLine(strings.TrimPrefix(old, "\ufeff"), strings.TrimPrefix(next, "\ufeff")), Committed: true}
		if p := et.patcher(ctx); p != nil && p.Snapshots != nil {
			if entry.Rename != "" && (destinationCommitted == nil || !destinationCommitted.SourceRemains) {
				p.Snapshots.Relocate(abs, target)
			}
			if hash, recorded := p.Snapshots.Record(target, strings.TrimPrefix(next, "\ufeff")); recorded {
				file.Header = hashline.FormatHeader(displayTarget, hash)
			}
		}
		if uncertain != nil {
			file.Error, file.DisplayError = uncertain.Error(), "committed; durability uncertain; do not retry"
		}
		if entry.Op == "create" && readErr != nil {
			file.OldText = ""
		}
		addLandedPatchFile(&result, filesByAuthoredPath, entry.Path, file, displayTarget)
		if uncertain != nil {
			return et.settleCommittedPatch(result, entries, targets, entryIndex+1), nil
		}
	}
	return result, nil
}

func (et *EditTool) resolvePatchTarget(authored string, isCreate bool) (string, error) {
	abs, err := sandboxJoin(et.Root, authored, "edit")
	if err != nil {
		return "", err
	}
	_, readErr := et.FS.ReadFile(abs)
	if readErr != nil && !isCreate && !filepath.IsAbs(authored) {
		if _, ok := et.FS.(hashline.OSFilesystem); ok {
			if resolved, resolveErr := resolveUniqueWorkspaceSuffix(et.Root, authored); resolveErr == nil {
				abs, readErr = resolved, nil
			} else if !os.IsNotExist(resolveErr) {
				return "", resolveErr
			}
		}
	}
	if _, ok := et.FS.(hashline.OSFilesystem); ok {
		if readErr == nil {
			if err := rejectRealPathOutside(et.Root, abs, authored, "edit"); err != nil {
				return "", err
			}
			if err := rejectMutableAlias(et.Root, abs, authored, "edit"); err != nil {
				return "", err
			}
		} else {
			if err := rejectRealParentOutside(et.Root, abs, authored, "edit"); err != nil {
				return "", err
			}
			if err := rejectCreateAlias(et.Root, abs, authored, "edit"); err != nil {
				return "", err
			}
		}
	}
	return abs, nil
}

func addLandedPatchFile(r *Result, byPath map[string]int, authored string, file contract.FileResult, _ string) {
	childPruned := len([]rune(file.OldText))+len([]rune(file.NewText)) > contract.SnapshotTextBudget
	if prior, ok := byPath[authored]; ok {
		previous := r.Files[prior]
		combinedDiff := previous.Diff
		if combinedDiff != "" && file.Diff != "" {
			combinedDiff += "\n"
		}
		combinedDiff += file.Diff
		file.OldText = previous.OldText
		file.FirstChangedLine = previous.FirstChangedLine
		file.Diff = combinedDiff
		file.Warnings = append(append([]string(nil), previous.Warnings...), file.Warnings...)
		file.SnapshotsPruned = previous.SnapshotsPruned || file.SnapshotsPruned || childPruned
		if file.SnapshotsPruned {
			file.OldText, file.NewText = "", ""
			markPatchSnapshotsPruned(r)
		}
		r.Files[prior] = file
		return
	}
	byPath[authored] = len(r.Files)
	r.Files = append(r.Files, file)
}

func markPatchSnapshotsPruned(r *Result) {
	if r.Metadata == nil {
		r.Metadata = make(map[string]any)
	}
	r.Metadata["snapshot_text_pruned"] = true
}

func (et *EditTool) settleCommittedPatch(result Result, entries []editmode.PatchEntry, targets map[string]string, applied int) Result {
	const guidance = "Committed, but settlement is uncertain; do not retry the committed operation."
	result.Output = appendPatchOutput(result.Output, guidance)
	for i := applied; i < len(entries); i++ {
		entry := entries[i]
		path := patchEntryPath(et.Root, entry, targets)
		message := "not applied because an earlier patch entry committed with uncertain settlement; do not retry the committed entry"
		mergeTerminalPatchFile(&result, path, contract.FileResult{Path: path, SourcePath: path, Operation: contract.FileError, Error: message, DisplayError: message})
	}
	return result
}

func (et *EditTool) failPatchEntries(result Result, entries []editmode.PatchEntry, targets map[string]string, failedIndex, applied int, cause error) (Result, error) {
	for i := applied; i < len(entries); i++ {
		entry := entries[i]
		path := patchEntryPath(et.Root, entry, targets)
		message := "not applied because an earlier patch entry failed"
		if i == failedIndex {
			message = cause.Error()
		}
		mergeTerminalPatchFile(&result, path, contract.FileResult{Path: path, SourcePath: path, Operation: contract.FileError, Error: message, DisplayError: message})
	}
	return result, patchEntryError(failedIndex, len(entries), applied, cause)
}

func patchEntryPath(root string, entry editmode.PatchEntry, targets map[string]string) string {
	path := targets[entry.Path]
	if path == "" {
		path, _ = sandboxJoin(root, entry.Path, "edit")
	}
	return path
}

// mergeTerminalPatchFile augments a landed aggregate instead of appending a
// second row. The landed row keeps its complete durable post-state and header.
func mergeTerminalPatchFile(result *Result, path string, terminal contract.FileResult) {
	effective := filepath.Clean(path)
	for i := range result.Files {
		file := result.Files[i]
		if filepath.Clean(file.Path) != effective && filepath.Clean(file.SourcePath) != effective && filepath.Clean(file.Destination) != effective {
			continue
		}
		if file.Committed {
			if file.Error == "" {
				file.Error, file.DisplayError = terminal.Error, terminal.DisplayError
				result.Files[i] = file
			}
		} else if file.Error == "" || strings.HasPrefix(file.Error, "not applied because") {
			result.Files[i] = terminal
		}
		return
	}
	result.Files = append(result.Files, terminal)
}

func patchEntryError(index, total, applied int, err error) error {
	return fmt.Errorf("patch entry %d failed (%d applied, %d unapplied): %w", index+1, applied, total-applied, err)
}
func appendPatchOutput(existing, value string) string {
	if existing == "" {
		return value
	}
	if value == "" {
		return existing
	}
	return existing + "\n" + value
}
