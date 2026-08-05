package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// Preview implements the frozen edit strategy's pure streaming capability.
func (et *EditTool) Preview(ctx context.Context, partial json.RawMessage) contract.Preview {
	if err := ctx.Err(); err != nil {
		return previewError(err)
	}
	var files []contract.FileResult
	var err error
	switch et.Mode {
	case editmode.Replace:
		files, err = et.previewReplace(partial)
	case editmode.Patch:
		files, err = et.previewPatch(partial)
	case editmode.ApplyPatch:
		input, _ := streamingInput(partial)
		files, err = et.previewPatchEntries(editmode.ParseApplyPatchStreaming(editmode.TrimUnfinishedTrailingLine(input)), false)
	default:
		input, _ := streamingInput(partial)
		files, err = et.previewHashline(ctx, input)
	}
	preview := contract.Preview{Files: files, Pending: true}
	if err != nil {
		preview.Error = err.Error()
	}
	preview.Digest = previewDigest(files, preview.Error)
	return preview
}

func (et *EditTool) MatcherEntries(partial json.RawMessage) []contract.MatcherEntry {
	var entries []contract.MatcherEntry
	switch et.Mode {
	case editmode.Replace:
		path, _ := partialStringField(partial, "path", false)
		text, _ := partialStringField(partial, "new_string", true)
		for _, entry := range editmode.ReplaceMatcherEntries(editmode.ReplaceInput{Path: path, NewString: text}) {
			entries = append(entries, contract.MatcherEntry{Path: entry.Path, Digest: entry.Digest})
		}
	case editmode.Patch:
		path, _ := partialStringField(partial, "path", false)
		for _, entry := range editmode.PatchMatcherEntries(path, partialPatchEntries(partial, path)) {
			entries = append(entries, contract.MatcherEntry{Path: entry.Path, Digest: entry.Digest})
		}
	case editmode.ApplyPatch:
		input, _ := streamingInput(partial)
		for _, entry := range editmode.ApplyPatchMatcherEntries(input) {
			entries = append(entries, contract.MatcherEntry{Path: entry.Path, Digest: entry.Digest})
		}
	default:
		input, _ := streamingInput(partial)
		entries = append(entries, hashlineMatcherEntries(input)...)
	}
	if et.Mode != editmode.Replace {
		nonempty := entries[:0]
		for _, entry := range entries {
			if entry.Digest != "" {
				nonempty = append(nonempty, entry)
			}
		}
		entries = nonempty
	}
	return aggregateMatcherEntries(entries)
}

func hashlineMatcherEntries(input string) []contract.MatcherEntry {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(input, "\r\n", "\n"), "\r", "\n"), "\n")
	var entries []contract.MatcherEntry
	current := -1
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "#") && strings.HasSuffix(trimmed, "]") {
			body := strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			hashAt := strings.LastIndexByte(body, '#')
			if hashAt <= 0 {
				current = -1
				continue
			}
			entries = append(entries, contract.MatcherEntry{Path: body[:hashAt]})
			current = len(entries) - 1
			continue
		}
		// Hashline payload escaping removes exactly one leading '+'. Headers,
		// operations, deleted diff rows, and repaired bare rows are excluded.
		if current >= 0 && strings.HasPrefix(line, "+") {
			if entries[current].Digest != "" {
				entries[current].Digest += "\n"
			}
			entries[current].Digest += line[1:]
		}
	}
	return entries
}

func aggregateMatcherEntries(entries []contract.MatcherEntry) []contract.MatcherEntry {
	indices := make(map[string]int, len(entries))
	out := make([]contract.MatcherEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Path == "" {
			continue
		}
		entry.Path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Path)))
		if index, ok := indices[entry.Path]; ok {
			out[index].Digest += "\n" + entry.Digest
			continue
		}
		indices[entry.Path] = len(out)
		out = append(out, entry)
	}
	return out
}

func (et *EditTool) previewReplace(raw json.RawMessage) ([]contract.FileResult, error) {
	path, _ := partialStringField(raw, "path", false)
	old, oldComplete := partialStringField(raw, "old_string", true)
	newText, _ := partialStringField(raw, "new_string", true)
	if path == "" || !oldComplete || old == "" {
		return nil, nil
	}
	abs, err := et.resolvePreviewTarget(path, false, true)
	if err != nil {
		return []contract.FileResult{previewFileError(path, err)}, err
	}
	data, err := et.FS.ReadFile(abs)
	if err != nil {
		return []contract.FileResult{previewFileError(path, err)}, err
	}
	content := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
	updated, _, err := editmode.ReplaceText(content, editmode.ReplaceInput{Path: path, OldString: old, NewString: newText}, et.Fuzzy, et.threshold())
	if err != nil {
		return []contract.FileResult{previewFileError(path, err)}, err
	}
	return []contract.FileResult{previewFile(path, "", contract.FileUpdated, content, updated, nil)}, nil
}

func (et *EditTool) previewPatch(raw json.RawMessage) ([]contract.FileResult, error) {
	path, _ := partialStringField(raw, "path", false)
	return et.previewPatchEntries(partialPatchEntries(raw, path), true)
}

func (et *EditTool) previewPatchEntries(entries []editmode.PatchEntry, overwrite bool) ([]contract.FileResult, error) {
	type virtualFile struct {
		content string
		exists  bool
		loaded  bool
	}
	virtual := map[string]virtualFile{}
	indices := map[string]int{}
	var files []contract.FileResult
	load := func(authored string, isCreate bool) (string, virtualFile, error) {
		key := filepath.ToSlash(filepath.Clean(filepath.FromSlash(authored)))
		if state, ok := virtual[key]; ok && state.loaded {
			return key, state, nil
		}
		abs, err := et.resolvePreviewTarget(authored, isCreate, true)
		if err != nil {
			return key, virtualFile{}, err
		}
		data, readErr := et.FS.ReadFile(abs)
		state := virtualFile{loaded: true}
		if readErr == nil {
			state.content, state.exists = string(data), true
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return key, virtualFile{}, readErr
		}
		virtual[key] = state
		return key, state, nil
	}
	for _, entry := range entries {
		if entry.Path == "" {
			continue
		}
		key, state, err := load(entry.Path, entry.Op == "create")
		if err != nil {
			return append(files, previewFileError(entry.Path, err)), err
		}
		old := state.content
		next, op := old, contract.FileUpdated
		switch entry.Op {
		case "create":
			if state.exists && !overwrite {
				err = errors.New("file already exists")
			} else {
				next, op = editmode.NormalizePatchCreateContent(entry.Diff), contract.FileCreated
			}
		case "delete":
			if !state.exists {
				err = errors.New("file not found")
			} else {
				next, op = "", contract.FileDeleted
			}
		case "update", "":
			if !state.exists {
				err = errors.New("file not found")
			} else {
				next, _, err = editmode.ApplyContextPatch(old, entry.Path, entry.Diff, et.Fuzzy, et.threshold())
			}
		default:
			err = errors.New("invalid patch operation")
		}
		if err != nil {
			return append(files, previewFileError(entry.Path, err)), err
		}
		virtual[key] = virtualFile{content: next, exists: entry.Op != "delete", loaded: true}
		if entry.Rename != "" {
			destinationKey, destination, destErr := load(entry.Rename, true)
			if destErr != nil {
				return append(files, previewFileError(entry.Rename, destErr)), destErr
			}
			if destination.exists {
				err = errors.New("move destination already exists")
				return append(files, previewFileError(entry.Rename, err)), err
			}
			virtual[key] = virtualFile{loaded: true}
			virtual[destinationKey] = virtualFile{content: next, exists: true, loaded: true}
			op = contract.FileMoved
		}
		file := previewFile(entry.Path, entry.Rename, op, old, next, nil)
		if index, exists := indices[key]; exists {
			file.OldText = files[index].OldText
			file.Diff = hashline.UnifiedDiff(entry.Path, file.OldText, next, 3)
			file.FirstChangedLine = firstChangedLine(file.OldText, next)
			files[index] = file
		} else {
			indices[key] = len(files)
			files = append(files, file)
		}
	}
	return files, nil
}

func (et *EditTool) previewHashline(ctx context.Context, input string) ([]contract.FileResult, error) {
	patch, err := hashline.ParsePatchPartial(input)
	if err != nil {
		return nil, err
	}
	if len(patch.Sections) == 0 {
		return nil, nil
	}
	displayPaths := make([]string, len(patch.Sections))
	for i := range patch.Sections {
		displayPaths[i] = patch.Sections[i].Path
		abs, joinErr := et.resolvePreviewTarget(patch.Sections[i].Path, false, true)
		if joinErr != nil {
			return []contract.FileResult{previewFileError(displayPaths[i], joinErr)}, joinErr
		}
		patch.Sections[i].Path = abs
		if patch.Sections[i].FileOp.MoveTo != "" {
			displayDestination := patch.Sections[i].FileOp.MoveTo
			dest, joinErr := et.resolvePreviewTarget(displayDestination, true, false)
			if joinErr != nil {
				return []contract.FileResult{previewFileError(displayDestination, joinErr)}, joinErr
			}
			patch.Sections[i].FileOp.MoveTo = dest
		}
	}
	p := et.existingPatcher(ctx)
	if p == nil {
		// No live session registers exist yet. A temporary patcher is pure and
		// still provides the same block-resolution and snapshot semantics.
		var snaps hashline.SnapshotStore = hashline.NewMemSnapshotStore()
		if et.SnapshotProvider != nil {
			snaps = et.SnapshotProvider.Snapshots(ctx)
		}
		p = hashline.NewPatcher(et.FS, snaps)
	}
	results, previewErr := p.PreviewResults(patch)
	files := make([]contract.FileResult, 0, len(results)+1)
	for i, result := range results {
		section := patch.Sections[i]
		op := contract.FileUpdated
		if section.FileOp.Remove {
			op = contract.FileDeleted
		} else if section.FileOp.MoveTo != "" {
			op = contract.FileMoved
		}
		files = append(files, previewFile(displayPaths[i], section.FileOp.MoveTo, op, result.OldText, result.NewText, result.Warnings))
	}
	if previewErr != nil && len(results) == 0 {
		// Streaming may carry a placeholder header before a read result arrives.
		// Keep active-input tolerance while forking session registers safely.
		section := patch.Sections[0]
		data, readErr := et.FS.ReadFile(section.Path)
		if readErr == nil {
			clipboard := hashline.NewClipboard()
			if live := et.existingPatcher(ctx); live != nil {
				clipboard = live.ForkClipboard()
			}
			applied, applyErr := hashline.ApplyEditsWithClipboard(hashline.SplitLines(string(data)), section.Edits, clipboard)
			if applyErr == nil {
				return []contract.FileResult{previewFile(displayPaths[0], section.FileOp.MoveTo, contract.FileUpdated, string(data), applied.Text, applied.Warnings)}, nil
			}
		}
	}
	if previewErr != nil {
		path := displayPaths[min(len(results), len(displayPaths)-1)]
		files = append(files, previewFileError(path, previewErr))
	}
	return files, previewErr
}

// resolvePreviewTarget applies the OS execution path gates before any content
// read. It performs no creation and only permits suffix recovery to a target
// which independently passes the same gates.
func (et *EditTool) resolvePreviewTarget(authored string, isCreate bool, recoverSuffix bool) (string, error) {
	abs, err := sandboxJoin(et.Root, authored, "edit")
	if err != nil {
		return "", err
	}
	if _, ok := et.FS.(hashline.OSFilesystem); !ok {
		return abs, nil
	}
	_, statErr := os.Lstat(abs)
	if errors.Is(statErr, os.ErrNotExist) && recoverSuffix && !isCreate && !filepath.IsAbs(authored) {
		if resolved, resolveErr := resolveUniqueWorkspaceSuffix(et.Root, authored); resolveErr == nil {
			abs, statErr = resolved, nil
		} else if !errors.Is(resolveErr, os.ErrNotExist) {
			return "", resolveErr
		}
	}
	if statErr == nil {
		if err := rejectRealPathOutside(et.Root, abs, authored, "edit"); err != nil {
			return "", err
		}
		if err := rejectMutableAlias(et.Root, abs, authored, "edit"); err != nil {
			return "", err
		}
		return abs, nil
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	if !isCreate {
		return abs, nil
	}
	if err := rejectRealParentOutside(et.Root, abs, authored, "edit"); err != nil {
		return "", err
	}
	if err := rejectCreateAlias(et.Root, abs, authored, "edit"); err != nil {
		return "", err
	}
	return abs, nil
}
func previewFile(path, destination string, op contract.FileOperation, old, next string, warnings []string) contract.FileResult {
	if old == next && op == contract.FileUpdated {
		op = contract.FileNoop
	}
	return contract.FileResult{Path: path, SourcePath: path, Destination: destination, Operation: op, OldText: old, NewText: next, Preview: next, Diff: hashline.UnifiedDiff(path, old, next, 3), Warnings: warnings, FirstChangedLine: firstChangedLine(old, next)}
}

func previewFileError(path string, err error) contract.FileResult {
	return contract.FileResult{Path: path, SourcePath: path, Operation: contract.FileError, Error: err.Error(), DisplayError: err.Error()}
}
func previewError(err error) contract.Preview {
	return contract.Preview{Pending: true, Error: err.Error(), Digest: previewDigest(nil, err.Error())}
}
func previewDigest(files []contract.FileResult, errText string) string {
	encoded, _ := json.Marshal(struct {
		Files []contract.FileResult
		Error string
	}{files, errText})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// partialStringField extracts a complete or actively growing JSON string value.
func streamingInput(raw []byte) (string, bool) {
	return partialStringField(raw, "input", true)
}

func partialStringField(raw []byte, key string, growing bool) (string, bool) {
	needle := `"` + key + `"`
	at := strings.Index(string(raw), needle)
	if at < 0 {
		return "", false
	}
	rest := string(raw[at+len(needle):])
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return "", false
	}
	rest = strings.TrimLeft(rest[colon+1:], " \t\r\n")
	if rest == "" || rest[0] != '"' {
		return "", false
	}
	escaped := false
	for i := 1; i < len(rest); i++ {
		if rest[i] == '"' && !escaped {
			value, err := strconv.Unquote(rest[:i+1])
			return value, err == nil
		}
		if rest[i] == '\\' {
			escaped = !escaped
		} else {
			escaped = false
		}
	}
	if !growing {
		return "", false
	}
	candidate := rest
	if escaped {
		candidate += `\\`
	}
	candidate += `"`
	value, _ := strconv.Unquote(candidate)
	return value, false
}

func partialPatchEntries(raw []byte, path string) []editmode.PatchEntry {
	text := string(raw)
	at := strings.Index(text, `"edits"`)
	if at < 0 {
		return nil
	}
	start := strings.IndexByte(text[at:], '[')
	if start < 0 {
		return nil
	}
	text = text[at+start+1:]
	depth, inString, escaped, objectStart := 0, false, false, -1
	var out []editmode.PatchEntry
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			if c == '"' && !escaped {
				inString = false
			}
			if c == '\\' {
				escaped = !escaped
			} else {
				escaped = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c == '{' {
			if depth == 0 {
				objectStart = i
			}
			depth++
		}
		if c == '}' && depth > 0 {
			depth--
			if depth == 0 && objectStart >= 0 {
				var e struct{ Op, Rename, Diff string }
				if json.Unmarshal([]byte(text[objectStart:i+1]), &e) == nil {
					if e.Op == "" {
						e.Op = "update"
					}
					out = append(out, editmode.PatchEntry{Path: path, Op: e.Op, Rename: e.Rename, Diff: e.Diff})
				}
				objectStart = -1
			}
		}
	}
	return out
}

var _ contract.Previewer = (*EditTool)(nil)
