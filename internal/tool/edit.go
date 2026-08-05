package tool

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/K3N4Y/atenea/agentcore/permission"
	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// EditTool is a turn-configurable facade over all supported edit strategies.
type EditTool struct {
	Root             string
	FS               hashline.Filesystem
	Patcher          *hashline.Patcher
	SnapshotProvider SnapshotProvider
	Mode             editmode.Mode
	Fuzzy            bool
	FuzzyThreshold   float64
	EnforceSeenLines bool
	// Config is evaluated when the registry materializes a turn. The returned
	// value is copied into a frozen facade; changing settings affects only the
	// next turn.
	Config     func() (editmode.Config, error)
	TurnConfig func(model, sessionID string) (editmode.Config, error)
	Getenv     func(string) string
	state      *editState
}

type editState struct {
	mu       sync.Mutex
	patchers map[string]*hashline.Patcher
	noops    map[string]noopAttempt
}

// RepeatedNoopError identifies the hard third identical no-op. Callers may
// distinguish this model-actionable edit failure from configuration or system
// failures with errors.As.
type RepeatedNoopError struct {
	Path string
}

func (e *RepeatedNoopError) Error() string {
	return fmt.Sprintf("edit: identical no-op patch attempted three consecutive times for %s; re-read the file and submit a changed patch", e.Path)
}

type noopAttempt struct {
	path, payloadHash string
	count             int
}

// NewEditTool arma un EditTool con un Patcher sobre el FS y el store de
// snapshots dados; Root acota el sandbox de rutas igual que el read.
func NewEditTool(root string, fs hashline.Filesystem, snaps hashline.SnapshotStore) *EditTool {
	return &EditTool{Root: root, FS: fs, Patcher: hashline.NewPatcher(fs, snaps), state: &editState{}}
}

func NewEditToolWithSnapshotProvider(root string, fs hashline.Filesystem, provider SnapshotProvider) *EditTool {
	return &EditTool{Root: root, FS: fs, SnapshotProvider: provider, state: &editState{}}
}

// Freeze implements the registry lifecycle seam. It never mutates or copies a
// live mutex: the returned facade owns its synchronization and fixed strategy.
func (et *EditTool) Freeze() contract.Tool {
	frozen, _ := et.FreezeFor("", "")
	return frozen
}

func (et *EditTool) FreezeFor(model, sessionID string) (contract.Tool, error) {
	frozen := &EditTool{
		Root: et.Root, FS: et.FS, Patcher: et.Patcher, SnapshotProvider: et.SnapshotProvider,
		Mode: et.Mode, Fuzzy: et.Fuzzy, FuzzyThreshold: et.FuzzyThreshold,
		EnforceSeenLines: et.EnforceSeenLines, state: et.state,
	}
	config := editmode.Config{Model: model, Setting: string(et.Mode), Fuzzy: et.Fuzzy, Threshold: et.FuzzyThreshold, EnforceSeenLines: et.EnforceSeenLines}
	var err error
	if et.TurnConfig != nil {
		config, err = et.TurnConfig(model, sessionID)
	} else if et.Config != nil {
		config, err = et.Config()
	}
	if err != nil {
		return nil, err
	}
	if config.Model == "" {
		config.Model = model
	}
	if err := frozen.Configure(config, et.Getenv); err != nil {
		return nil, err
	}
	return frozen, nil
}

func (*EditTool) Name() string { return "edit" }

// PresentationAliases keeps the upstream custom wire name renderable in live
// and historical events regardless of which mode another turn materializes.
// Its presenter is permanently frozen to the apply-patch input format.
func (et *EditTool) PresentationAliases() map[string]contract.Tool {
	presenter := &EditTool{Root: et.Root, FS: et.FS, Patcher: et.Patcher, SnapshotProvider: et.SnapshotProvider, Mode: editmode.ApplyPatch, state: et.state}
	return map[string]contract.Tool{"apply_patch": presenter}
}

//go:embed edit.txt
var editDescription string

func (et *EditTool) Description() string { return et.definition().Description }

// Effects: an edit rewrites an existing file in place.
func (*EditTool) Effects() Effects { return WritesFiles }

// GrantRule grants the tool itself, like write: the user authorizes editing
// files in this workspace for the rest of the session. The patch of this one call
// cannot narrow that — the next edit patches another file.
func (et *EditTool) GrantRule(Call) (permission.Rule, bool) {
	return permission.Rule{Tool: et.Name()}, true
}

func (et *EditTool) Schema() json.RawMessage { return et.definition().Schema }

func (et *EditTool) Definition() contract.ToolDefinition { return et.definition() }

func (et *EditTool) definition() contract.ToolDefinition {
	mode := et.Mode
	if mode == "" {
		mode = editmode.Hashline
	}
	def := contract.ToolDefinition{Name: "edit", WireName: "edit"}
	switch mode {
	case editmode.Replace:
		def.Description = strings.TrimSpace(replaceDescription)
		def.Schema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["path","old_string","new_string"],"additionalProperties":false}`)
	case editmode.Patch:
		def.Description = strings.TrimSpace(patchDescription)
		def.Schema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"edits":{"type":"array","items":{"type":"object","properties":{"op":{"enum":["create","delete","update"]},"rename":{"type":"string"},"diff":{"type":"string"}},"additionalProperties":false}}},"required":["path","edits"],"additionalProperties":false}`)
	case editmode.ApplyPatch:
		def.WireName = "apply_patch"
		def.Description = strings.TrimSpace(applyPatchDescription)
		def.Schema = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}`)
		def.CustomFormat = &contract.CustomFormat{Syntax: "lark", Definition: applyPatchGrammar}
	default:
		def.Description = editDescription
		def.Schema = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":true}`)
		def.CustomFormat = &contract.CustomFormat{Syntax: "lark", Definition: hashlineGrammar}
	}
	return def
}

//go:embed editmode/patch.txt
var patchDescription string

//go:embed editmode/replace.txt
var replaceDescription string

//go:embed editmode/apply_patch.txt
var applyPatchDescription string

//go:embed editmode/apply-patch.lark
var applyPatchGrammar string

//go:embed editmode/hashline.lark
var hashlineGrammar string

// AutoAcceptSafe proves every target is an existing, non-aliased workspace file.
func (et *EditTool) AutoAcceptSafe(call Call) bool {
	paths, safe := et.targetPaths(call.Input, true)
	if !safe || len(paths) == 0 {
		return false
	}
	if _, ok := et.FS.(hashline.OSFilesystem); !ok {
		return false
	}
	for _, rel := range paths {
		abs, err := sandboxJoin(et.Root, rel, "edit")
		if err != nil || rejectRealPathOutside(et.Root, abs, rel, "edit") != nil || rejectMutableAlias(et.Root, abs, rel, "edit") != nil {
			return false
		}
	}
	return true
}

func (et *EditTool) targetPaths(raw json.RawMessage, safeOnly bool) ([]string, bool) {
	switch et.Mode {
	case editmode.Replace:
		p := field(raw, "path")
		return []string{p}, p != ""
	case editmode.Patch:
		var in patchJSON
		if json.Unmarshal(raw, &in) != nil || in.Path == "" {
			return nil, false
		}
		for _, e := range in.Edits {
			if safeOnly && (e.Op == "create" || e.Op == "delete" || e.Rename != "") {
				return nil, false
			}
		}
		return []string{in.Path}, true
	case editmode.ApplyPatch:
		entries, err := editmode.ParseApplyPatch(field(raw, "input"))
		if err != nil {
			return nil, false
		}
		paths := make([]string, 0, len(entries))
		for _, e := range entries {
			if safeOnly && (e.Op != "update" || e.Rename != "") {
				return nil, false
			}
			paths = append(paths, e.Path)
		}
		return paths, len(paths) > 0
	default:
		input := field(raw, "input")
		patch, err := hashline.ParsePatch(input)
		if err != nil {
			if path := patchPath(input); path != "" {
				return []string{path}, !safeOnly
			}
			return nil, false
		}
		paths := make([]string, 0, len(patch.Sections))
		for _, s := range patch.Sections {
			if safeOnly && (s.FileOp.Remove || s.FileOp.MoveTo != "") {
				return nil, false
			}
			paths = append(paths, s.Path)
		}
		return paths, len(paths) > 0
	}
}

// Execute parsea el patch, resuelve la ruta relativa dentro de Root (compuerta
// de sandbox), reescribe la seccion para que el Patcher lea/escriba/snapshotee
// por la ruta absoluta, aplica el patch y devuelve el header resultante con la
// ruta RELATIVA (la que el modelo encadena en el siguiente edit).
// Configure freezes mode and fuzzy settings on this facade instance.
func (et *EditTool) Configure(config editmode.Config, getenv func(string) string) error {
	mode, err := editmode.Resolve(config, getenv)
	if err != nil {
		return err
	}
	fuzzy, threshold, err := editmode.ResolveFuzzy(config, getenv)
	if err != nil {
		return err
	}
	et.Mode, et.Fuzzy, et.FuzzyThreshold, et.EnforceSeenLines = mode, fuzzy, threshold, config.EnforceSeenLines
	return nil
}

func (et *EditTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	switch et.Mode {
	case editmode.Replace:
		return et.executeReplace(ctx, input)
	case editmode.Patch:
		return et.executePatch(ctx, input)
	case editmode.ApplyPatch:
		return et.executeApplyPatch(ctx, input)
	}
	var in struct {
		Input string `json:"input"`
	}
	if err := decodeJSON(input, &in, false); err != nil {
		return Result{}, fmt.Errorf("edit: invalid input: %w", err)
	}

	patch, err := hashline.ParsePatch(in.Input)
	if err != nil {
		return Result{}, err
	}

	p := et.patcher(ctx)
	var pathRecoveryWarnings []string
	relPaths := make([]string, len(patch.Sections))
	authoredDestinations := make([]string, len(patch.Sections))
	targets := make(map[string]struct{}, len(patch.Sections))
	for i := range patch.Sections {
		s := &patch.Sections[i]
		relPaths[i] = s.Path
		abs, joinErr := sandboxJoin(et.Root, s.Path, "edit")
		if joinErr != nil {
			return Result{}, joinErr
		}
		if _, readErr := et.FS.ReadFile(abs); errors.Is(readErr, os.ErrNotExist) && !strings.Contains(s.Path, "://") {
			var matches []*hashline.Snapshot
			for _, candidate := range p.Snapshots.FindByHash(s.Hash) {
				if filepath.Base(candidate.Path) != filepath.Base(s.Path) || filepath.Clean(candidate.Path) == filepath.Clean(abs) {
					continue
				}
				rel, relErr := filepath.Rel(et.Root, candidate.Path)
				if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					matches = append(matches, candidate)
				}
			}
			if len(matches) == 1 {
				abs = matches[0].Path
				pathRecoveryWarnings = append(pathRecoveryWarnings, fmt.Sprintf("authored path %s was missing; recovered unique workspace snapshot path %s", s.Path, abs))
			}
		}
		canonical := filepath.Clean(abs)
		if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
			canonical = resolved
		}
		if _, duplicate := targets[canonical]; duplicate {
			return Result{}, fmt.Errorf("edit: duplicate canonical target %s", s.Path)
		}
		targets[canonical] = struct{}{}
		if _, ok := et.FS.(hashline.OSFilesystem); ok {
			if err := rejectRealPathOutside(et.Root, abs, s.Path, "edit"); err != nil {
				return Result{}, err
			}
			if err := rejectMutableAlias(et.Root, abs, s.Path, "edit"); err != nil {
				return Result{}, err
			}
		}
		s.Path = abs
		if s.FileOp.MoveTo != "" {
			authoredDestinations[i] = s.FileOp.MoveTo
			dest, destErr := sandboxJoin(et.Root, s.FileOp.MoveTo, "edit")
			if destErr != nil {
				return Result{}, destErr
			}
			destCanonical := filepath.Clean(dest)
			if resolved, resolveErr := filepath.EvalSymlinks(dest); resolveErr == nil {
				destCanonical = resolved
			}
			if _, duplicate := targets[destCanonical]; duplicate {
				return Result{}, fmt.Errorf("edit: duplicate canonical target %s", s.FileOp.MoveTo)
			}
			targets[destCanonical] = struct{}{}
			if _, ok := et.FS.(hashline.OSFilesystem); ok {
				if aliasErr := rejectCreateAlias(et.Root, dest, authoredDestinations[i], "edit"); aliasErr != nil {
					message := aliasErr.Error()
					return Result{Files: []contract.FileResult{{Path: dest, SourcePath: abs, Destination: dest, Operation: contract.FileError, Committed: false, Error: message, DisplayError: message}}}, aliasErr
				}
			}
			if _, statErr := et.FS.ReadFile(dest); statErr == nil {
				message := fmt.Sprintf("edit: move destination already exists: %s", s.FileOp.MoveTo)
				return Result{Files: []contract.FileResult{{Path: dest, SourcePath: abs, Destination: dest, Operation: contract.FileError, Committed: false, Error: message, DisplayError: message}}}, errors.New(message)
			} else if !errors.Is(statErr, os.ErrNotExist) {
				message := fmt.Sprintf("edit: cannot check move destination %s: %v", s.FileOp.MoveTo, statErr)
				return Result{Files: []contract.FileResult{{Path: dest, SourcePath: abs, Destination: dest, Operation: contract.FileError, Committed: false, Error: message, DisplayError: message}}}, fmt.Errorf("%s: %w", message, statErr)
			}
			s.FileOp.MoveTo = dest
		}
	}
	// p was selected before path recovery so lookup and commit share one session store.
	if _, previewErr := p.PreflightConfiguredResults(patch, et.EnforceSeenLines); previewErr != nil && strings.Contains(previewErr.Error(), "makes no changes") {
		if len(patch.Sections) != 1 {
			return Result{}, fmt.Errorf("edit: multi-section patch contains a no-op; no files were written: %w", previewErr)
		}
		path := filepath.Clean(patch.Sections[0].Path)
		sum := sha256.Sum256([]byte(in.Input))
		count := et.recordNoop(ctx, path, hex.EncodeToString(sum[:]))
		message := fmt.Sprintf("No changes were made to %s. Revise the patch before retrying.", relPaths[0])
		if count < 3 {
			return Result{Output: message, Files: []contract.FileResult{{Path: path, SourcePath: path, Operation: contract.FileNoop, Committed: false, Error: message}}}, nil
		}
		return Result{}, &RepeatedNoopError{Path: relPaths[0]}
	} else if previewErr != nil {
		return Result{}, previewErr
	}
	// Destination parents are created only after Apply's final live preflight,
	// while its mutex and complete path-lock ownership are still held.
	prepareMoveParents := func() error {
		if _, ok := et.FS.(hashline.OSFilesystem); !ok {
			return nil
		}
		for i := range patch.Sections {
			s := &patch.Sections[i]
			if s.FileOp.MoveTo == "" {
				continue
			}
			if err := rejectCreateAlias(et.Root, s.FileOp.MoveTo, authoredDestinations[i], "edit"); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(s.FileOp.MoveTo), 0o755); err != nil {
				return fmt.Errorf("edit: create move destination parent %s: %w", authoredDestinations[i], err)
			}
		}
		return nil
	}
	results, err := p.ApplyConfiguredResultsWithOptions(patch, et.EnforceSeenLines, hashline.ApplyOptions{PostPreflight: prepareMoveParents})
	var committed *hashline.CommittedError
	if err != nil && len(results) > 0 {
		var uncertain *hashline.CommitUncertainError
		var destinationCommitted *hashline.DestinationCommittedError
		if errors.As(err, &uncertain) || errors.As(err, &destinationCommitted) {
			committed = &hashline.CommittedError{Result: results[len(results)-1], Err: err}
		}
	}
	et.resetNoop(ctx)
	var output Result
	for i, res := range results {
		res.Warnings = append(pathRecoveryWarnings, res.Warnings...)
		section, relPath := patch.Sections[i], relPaths[i]
		displayPath := relPath
		if section.FileOp.MoveTo != "" {
			if rel, relErr := filepath.Rel(et.Root, section.FileOp.MoveTo); relErr == nil {
				displayPath = rel
			}
		}
		diff := hashline.UnifiedDiff(relPath, res.OldText, res.NewText, 3)
		header := ""
		if res.Header != "" {
			header = hashline.FormatHeader(displayPath, hashline.ComputeFileHash(res.NewText))
		} else {
			header = "[Edit committed, but " + displayPath + " has no new hashline header.]"
		}
		for _, warning := range res.Warnings {
			header += "\n[warning: " + warning + "]"
		}
		operation, destination, resultPath := contract.FileUpdated, "", section.Path
		if section.FileOp.Remove {
			operation = contract.FileDeleted
		} else if section.FileOp.MoveTo != "" {
			operation, destination, resultPath = contract.FileMoved, section.FileOp.MoveTo, section.FileOp.MoveTo
		}
		file := contract.FileResult{Path: resultPath, SourcePath: section.Path, Destination: destination, Operation: operation, OldText: res.OldText, NewText: res.NewText, Diff: diff, Warnings: append([]string(nil), res.Warnings...), Header: header, FirstChangedLine: res.FirstChangedLine, Committed: true}
		if committed != nil && i == len(results)-1 {
			continuation := "Continue with the header above or re-read."
			if res.Header == "" {
				continuation = "No editable header is available; change or reduce the content or start a new session, then use read."
			}
			file.Error, file.DisplayError = committed.Error(), "committed; durability uncertain; do not retry"
			header += "\n[COMMITTED: replacement is visible, but directory durability is uncertain; do not retry this patch. " + continuation + " " + committed.Error() + "]"
			file.Header = header
		}
		output.Output = appendPatchOutput(output.Output, header)
		output.Diff = appendPatchOutput(output.Diff, diff)
		output.Files = append(output.Files, file)
	}
	if err != nil {
		for i := len(results); i < len(patch.Sections); i++ {
			message := "not applied because an earlier section failed"
			if committed == nil && i == len(results) {
				message = err.Error()
			}
			section := patch.Sections[i]
			output.Files = append(output.Files, contract.FileResult{Path: section.Path, SourcePath: section.Path, Operation: contract.FileError, Committed: false, Error: message, DisplayError: message})
		}
		if committed == nil {
			return output, err
		}
	}
	return output, nil
}

func (et *EditTool) executeReplace(ctx context.Context, raw json.RawMessage) (Result, error) {
	var in editmode.ReplaceInput
	if err := decodeStrict(raw, &in); err != nil {
		return Result{}, fmt.Errorf("edit: invalid input: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	abs, err := sandboxJoin(et.Root, in.Path, "edit")
	if err != nil {
		return Result{}, err
	}
	// Suffix recovery changes the ownership identity, so it must complete before
	// entering the shared hashline lock domain.
	if !filepath.IsAbs(in.Path) {
		if _, ok := et.FS.(hashline.OSFilesystem); ok {
			if _, readErr := et.FS.ReadFile(abs); errors.Is(readErr, os.ErrNotExist) {
				if resolved, resolveErr := resolveUniqueWorkspaceSuffix(et.Root, in.Path); resolveErr == nil {
					abs = resolved
				} else if !errors.Is(resolveErr, os.ErrNotExist) {
					return Result{}, resolveErr
				}
			}
		}
	}
	unlock := hashline.LockPaths(abs)
	defer unlock()
	data, err := et.FS.ReadFile(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("File not found: %s", in.Path)
		}
		return Result{}, err
	}
	if _, ok := et.FS.(hashline.OSFilesystem); ok {
		if err := rejectRealPathOutside(et.Root, abs, in.Path, "edit"); err != nil {
			return Result{}, err
		}
		if err := rejectMutableAlias(et.Root, abs, in.Path, "edit"); err != nil {
			return Result{}, err
		}
	}
	bom, content := splitBOM(string(data))
	eol := detectEOL(content)
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	updated, count, err := editmode.ReplaceText(normalized, in, et.Fuzzy, et.threshold())
	if err != nil {
		return Result{}, err
	}
	if updated == normalized {
		return Result{}, fmt.Errorf("Edits to %s resulted in no changes being made.", in.Path)
	}
	updated = bom + restoreEOL(updated, eol)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	writeErr := et.FS.WriteFile(abs, []byte(updated), fileMode(abs))
	var uncertain *hashline.CommitUncertainError
	if writeErr != nil && !errors.As(writeErr, &uncertain) {
		return Result{}, writeErr
	}
	landed, readErr := et.FS.ReadFile(abs)
	if readErr != nil {
		file := contract.FileResult{Path: abs, Operation: contract.FileEdited, OldText: string(data), NewText: updated, Committed: true, Error: "committed but readback failed; state uncertain; do not retry", DisplayError: "committed; state uncertain; do not retry"}
		return Result{Output: file.Error, Files: []contract.FileResult{file}}, nil
	}
	if actual := string(landed); actual == string(data) {
		message := "edit: write reported success but no file change was visible"
		file := contract.FileResult{Path: abs, SourcePath: abs, Destination: abs, Operation: contract.FileError, OldText: string(data), NewText: actual, Committed: false, Error: message, DisplayError: message}
		return Result{Output: message, Files: []contract.FileResult{file}}, errors.New(message)
	}
	actual := string(landed)
	warnings := []string(nil)
	if actual != updated {
		warnings = append(warnings, "content changed during write; result and snapshot reflect bytes read back from disk")
	}
	actualNormalized := strings.TrimPrefix(actual, bom)
	header := ""
	if p := et.patcher(ctx); p != nil && p.Snapshots != nil {
		if hash, recorded := p.Snapshots.Record(abs, actualNormalized); recorded {
			header = hashline.FormatHeader(in.Path, hash)
		}
	}
	diff := hashline.UnifiedDiff(in.Path, normalized, actualNormalized, 3)
	output := "Successfully replaced text in " + in.Path + "."
	if count > 1 {
		output = fmt.Sprintf("Successfully replaced %d occurrences in %s.", count, in.Path)
	}
	file := contract.FileResult{Path: abs, SourcePath: abs, Destination: abs, Operation: contract.FileEdited, OldText: string(data), NewText: actual, Diff: diff, Header: header, Warnings: warnings, FirstChangedLine: firstChangedLine(normalized, actualNormalized), Committed: true}
	if uncertain != nil {
		file.Error, file.DisplayError = uncertain.Error(), "committed; durability uncertain; do not retry"
		output += " Committed, but durability is uncertain; do not retry."
	}
	return Result{Output: output, Diff: diff, Files: []contract.FileResult{file}}, nil
}

type patchJSON struct {
	Path  string `json:"path"`
	Edits []struct {
		Op     string `json:"op"`
		Rename string `json:"rename"`
		Diff   string `json:"diff"`
	} `json:"edits"`
}

func (et *EditTool) executePatch(ctx context.Context, raw json.RawMessage) (Result, error) {
	var in patchJSON
	if err := decodeStrict(raw, &in); err != nil {
		return Result{}, fmt.Errorf("edit: invalid input: %w", err)
	}
	if in.Path == "" || len(in.Edits) == 0 {
		return Result{}, fmt.Errorf("No files were modified.")
	}
	entries := make([]editmode.PatchEntry, 0, len(in.Edits))
	for _, e := range in.Edits {
		op := e.Op
		if op == "" {
			op = "update"
		}
		entries = append(entries, editmode.PatchEntry{Path: in.Path, Op: op, Rename: e.Rename, Diff: e.Diff})
	}
	return et.applyPatchEntries(ctx, entries, false)
}
func (et *EditTool) executeApplyPatch(ctx context.Context, raw json.RawMessage) (Result, error) {
	var in struct {
		Input string `json:"input"`
	}
	if err := decodeStrict(raw, &in); err != nil {
		return Result{}, fmt.Errorf("edit: invalid input: %w", err)
	}
	entries, err := editmode.ParseApplyPatch(in.Input)
	if err != nil {
		return Result{}, err
	}
	if len(entries) == 0 {
		return Result{}, fmt.Errorf("No files were modified.")
	}
	// Preflight every validation that does not depend on effects of an earlier
	// entry. Execution remains deliberately ordered and non-atomic upstream.
	for _, entry := range entries {
		if _, joinErr := sandboxJoin(et.Root, entry.Path, "edit"); joinErr != nil {
			return Result{}, joinErr
		}
		if entry.Rename != "" {
			if _, joinErr := sandboxJoin(et.Root, entry.Rename, "edit"); joinErr != nil {
				return Result{}, joinErr
			}
		}
		if entry.Op == "update" && entry.Diff != "" {
			if _, parseErr := editmode.ParsePatchHunks(entry.Diff); parseErr != nil {
				return Result{}, parseErr
			}
		}
	}
	return et.applyPatchEntries(ctx, entries, false)
}
func (et *EditTool) applyEntries(entries []editmode.PatchEntry, createOverwrite bool) (Result, error) {
	var outputs, diffs []string
	for _, e := range entries {
		abs, err := sandboxJoin(et.Root, e.Path, "edit")
		if err != nil {
			return Result{}, err
		}
		oldBytes, readErr := et.FS.ReadFile(abs)
		old := string(oldBytes)
		var next string
		switch e.Op {
		case "create":
			if readErr == nil && !createOverwrite {
				return Result{}, fmt.Errorf("Cannot create %s: file already exists. Use *** Update File to modify it in place.", e.Path)
			}
			next = e.Diff
			if !strings.HasSuffix(next, "\n") {
				next += "\n"
			}
		case "delete":
			if readErr != nil {
				return Result{}, readErr
			}
			remover, ok := et.FS.(interface{ Remove(string) error })
			if !ok {
				return Result{}, fmt.Errorf("filesystem does not support delete")
			}
			if err := remover.Remove(abs); err != nil {
				return Result{}, err
			}
			outputs = append(outputs, "Deleted "+e.Path)
			diffs = append(diffs, hashline.UnifiedDiff(e.Path, old, "", 3))
			continue
		case "update":
			if readErr != nil {
				return Result{}, readErr
			}
			next, err = editmode.ApplyUnified(old, e.Diff)
			if err != nil {
				return Result{}, err
			}
		default:
			return Result{}, fmt.Errorf("invalid patch operation %q", e.Op)
		}
		target := abs
		if e.Rename != "" {
			target, err = sandboxJoin(et.Root, e.Rename, "edit")
			if err != nil {
				return Result{}, err
			}
			if _, existsErr := et.FS.ReadFile(target); existsErr == nil {
				return Result{}, fmt.Errorf("Cannot rename %s to %s: destination already exists.", e.Path, e.Rename)
			}
		}
		if err := et.FS.WriteFile(target, []byte(next), fileMode(abs)); err != nil {
			return Result{}, err
		}
		if target != abs {
			remover, ok := et.FS.(interface{ Remove(string) error })
			if !ok {
				return Result{}, fmt.Errorf("filesystem does not support move")
			}
			if err := remover.Remove(abs); err != nil {
				return Result{}, err
			}
		}
		outputs = append(outputs, "Updated "+e.Path)
		diffs = append(diffs, hashline.UnifiedDiff(e.Path, old, next, 3))
	}
	return Result{Output: strings.Join(outputs, "\n"), Diff: strings.Join(diffs, "\n")}, nil
}
func decodeStrict(raw []byte, dst any) error {
	return decodeJSON(raw, dst, true)
}

func decodeJSON(raw []byte, dst any, rejectUnknown bool) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if rejectUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return fmt.Errorf("unexpected trailing data: %w", err)
	}
	return nil
}
func splitBOM(s string) (string, string) {
	if strings.HasPrefix(s, "\ufeff") {
		return "\ufeff", strings.TrimPrefix(s, "\ufeff")
	}
	return "", s
}
func detectEOL(s string) string {
	if strings.Contains(s, "\r\n") {
		return "\r\n"
	}
	return "\n"
}
func restoreEOL(s, eol string) string {
	if eol == "\r\n" {
		return strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s
}
func fileMode(path string) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode()
	}
	return 0o644
}
func (et *EditTool) threshold() float64 {
	if et.FuzzyThreshold == 0 {
		return .95
	}
	return et.FuzzyThreshold
}

func firstChangedLine(oldText, newText string) int {
	oldLines, newLines := strings.Split(oldText, "\n"), strings.Split(newText, "\n")
	for i := 0; i < min(len(oldLines), len(newLines)); i++ {
		if oldLines[i] != newLines[i] {
			return i + 1
		}
	}
	if len(oldLines) != len(newLines) {
		return min(len(oldLines), len(newLines)) + 1
	}
	return 0
}
func resolveUniqueWorkspaceSuffix(root, authored string) (string, error) {
	wanted := filepath.Clean(authored)
	var match string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || (rel != wanted && !strings.HasSuffix(rel, string(filepath.Separator)+wanted)) {
			return err
		}
		if match != "" {
			return fmt.Errorf("edit: path suffix is ambiguous: %s", authored)
		}
		match = path
		return nil
	})
	if err != nil {
		return "", err
	}
	if match == "" {
		return "", os.ErrNotExist
	}
	return match, nil
}

func (et *EditTool) sessionKey(ctx context.Context) string {
	if id := SessionIDFrom(ctx); id != "" {
		return id
	}
	return "default"
}

func (et *EditTool) recordNoop(ctx context.Context, path, payloadHash string) int {
	state := et.state
	if state == nil {
		state = &editState{}
		et.state = state
	}
	key := et.sessionKey(ctx)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.noops == nil {
		state.noops = make(map[string]noopAttempt)
	}
	attempt := state.noops[key]
	if attempt.path == path && attempt.payloadHash == payloadHash {
		attempt.count++
	} else {
		attempt = noopAttempt{path: path, payloadHash: payloadHash, count: 1}
	}
	state.noops[key] = attempt
	return attempt.count
}

func (et *EditTool) resetNoop(ctx context.Context) {
	if et.state == nil {
		return
	}
	et.state.mu.Lock()
	delete(et.state.noops, et.sessionKey(ctx))
	et.state.mu.Unlock()
}

func (et *EditTool) patcher(ctx context.Context) *hashline.Patcher {
	if et.SnapshotProvider == nil {
		return et.Patcher
	}
	sessionID := SessionIDFrom(ctx)
	if sessionID == "" {
		sessionID = "default"
	}
	state := et.state
	if state == nil {
		state = &editState{}
		et.state = state
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.patchers == nil {
		state.patchers = make(map[string]*hashline.Patcher)
	}
	p := state.patchers[sessionID]
	if p == nil {
		p = hashline.NewPatcher(et.FS, et.SnapshotProvider.Snapshots(ctx))
		state.patchers[sessionID] = p
	}
	return p
}

// existingPatcher returns session edit state without creating a patcher or a
// snapshot store. Streaming preview must remain observationally pure.
func (et *EditTool) existingPatcher(ctx context.Context) *hashline.Patcher {
	if et.SnapshotProvider == nil {
		return et.Patcher
	}
	if et.state == nil {
		return nil
	}
	et.state.mu.Lock()
	defer et.state.mu.Unlock()
	if et.state.patchers == nil {
		return nil
	}
	return et.state.patchers[et.sessionKey(ctx)]
}
