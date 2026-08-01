package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/K3N4Y/atenea/agentcore/permission"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// EditTool aplica un patch hashline a un archivo bajo Root. El patch ancla
// contra el header [ruta#HASH] que devolvio el read y describe los cambios con
// hunks (SWAP/DEL/INS.*), llevando el payload nuevo con lineas '+...'. Patcher
// resuelve la lectura, el chequeo de drift, la escritura y el regrabado del
// snapshot por ruta absoluta.
type EditTool struct {
	Root             string
	FS               hashline.Filesystem
	Patcher          *hashline.Patcher
	SnapshotProvider SnapshotProvider
}

// NewEditTool arma un EditTool con un Patcher sobre el FS y el store de
// snapshots dados; Root acota el sandbox de rutas igual que el read.
func NewEditTool(root string, fs hashline.Filesystem, snaps hashline.SnapshotStore) *EditTool {
	return &EditTool{Root: root, FS: fs, Patcher: hashline.NewPatcher(fs, snaps)}
}

func NewEditToolWithSnapshotProvider(root string, fs hashline.Filesystem, provider SnapshotProvider) *EditTool {
	return &EditTool{Root: root, FS: fs, SnapshotProvider: provider}
}

func (*EditTool) Name() string { return "edit" }

//go:embed edit.txt
var editDescription string

func (*EditTool) Description() string { return editDescription }

// Effects: an edit rewrites an existing file in place.
func (*EditTool) Effects() Effects { return WritesFiles }

// GrantRule grants the tool itself, like write: the user authorizes editing
// files in this workspace for the rest of the session. The patch of this one call
// cannot narrow that — the next edit patches another file.
func (et *EditTool) GrantRule(Call) (permission.Rule, bool) {
	return permission.Rule{Tool: et.Name()}, true
}

func (*EditTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"patch":{"type":"string"}},"required":["patch"]}`)
}

// AutoAcceptSafe rejects aliases: edit writes through the existing inode.
func (et *EditTool) AutoAcceptSafe(call Call) bool {
	var in struct {
		Patch string `json:"patch"`
	}
	if json.Unmarshal(call.Input, &in) != nil {
		return false
	}
	patch, err := hashline.ParsePatch(in.Patch)
	if err != nil || len(patch.Sections) != 1 {
		return false
	}
	rel := patch.Sections[0].Path
	abs, err := sandboxJoin(et.Root, rel, "edit")
	if err != nil {
		return false
	}
	if _, ok := et.FS.(hashline.OSFilesystem); !ok {
		return false
	}
	return rejectRealPathOutside(et.Root, abs, rel, "edit") == nil &&
		rejectMutableAlias(et.Root, abs, rel, "edit") == nil
}

// Execute parsea el patch, resuelve la ruta relativa dentro de Root (compuerta
// de sandbox), reescribe la seccion para que el Patcher lea/escriba/snapshotee
// por la ruta absoluta, aplica el patch y devuelve el header resultante con la
// ruta RELATIVA (la que el modelo encadena en el siguiente edit).
func (et *EditTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("edit: input invalido: %w", err)
	}

	patch, err := hashline.ParsePatch(in.Patch)
	if err != nil {
		return Result{}, err
	}

	// v1: una sola seccion. La ruta del patch es relativa (display path).
	s := &patch.Sections[0]
	relPath := s.Path

	abs, err := sandboxJoin(et.Root, relPath, "edit")
	if err != nil {
		return Result{}, err
	}
	if _, ok := et.FS.(hashline.OSFilesystem); ok {
		if err := rejectRealPathOutside(et.Root, abs, relPath, "edit"); err != nil {
			return Result{}, err
		}
		if err := rejectMutableAlias(et.Root, abs, relPath, "edit"); err != nil {
			return Result{}, err
		}
	}

	// El Patcher lee/escribe/snapshotea por ruta absoluta.
	s.Path = abs

	res, err := et.patcher(ctx).Apply(patch)
	var committed *hashline.CommittedError
	if err != nil && !errors.As(err, &committed) {
		return Result{}, err
	}
	if committed != nil {
		res = committed.Result
	}

	diff := hashline.UnifiedDiff(relPath, res.OldText, res.NewText, 3)
	header := ""
	if res.Header != "" {
		// res.Header trae la ruta ABS; el modelo encadena por la ruta RELATIVA.
		header = strings.Replace(res.Header, abs, relPath, 1)
	} else {
		header = "[Edit committed, but " + relPath + " could not be retained as an unambiguous editable snapshot; no hashline header was issued. Change or reduce the content or start a new session, then use read before editing again.]"
	}
	if committed != nil {
		// Tool middleware discards Result whenever error is non-nil, so expose the
		// committed result as an actionable successful settlement and forbid retry.
		continuation := "Continue with the header above or re-read."
		if res.Header == "" {
			continuation = "No editable header is available; change or reduce the content or start a new session, then use read."
		}
		header += "\n[COMMITTED: replacement is visible, but directory durability is uncertain; do not retry this patch. " + continuation + " " + committed.Error() + "]"
	}
	return Result{Output: header, Diff: diff}, nil
}

func (et *EditTool) patcher(ctx context.Context) *hashline.Patcher {
	if et.SnapshotProvider != nil {
		return hashline.NewPatcher(et.FS, et.SnapshotProvider.Snapshots(ctx))
	}
	return et.Patcher
}
