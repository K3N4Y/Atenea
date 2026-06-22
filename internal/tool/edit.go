package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"atenea/internal/tool/hashline"
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

func (*EditTool) Description() string {
	return "Aplica un patch hashline a un archivo EXISTENTE: ancla en el header [ruta#HASH] que devolvio read o write y describe los cambios con hunks SWAP/DEL/INS.* cuyo payload nuevo va en lineas '+...'. Para crear un archivo nuevo usa write, no inventes el header."
}

func (*EditTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"patch":{"type":"string"}},"required":["patch"]}`)
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
	}

	// El Patcher lee/escribe/snapshotea por ruta absoluta.
	s.Path = abs

	res, err := et.patcher(ctx).Apply(patch)
	if err != nil {
		return Result{}, err
	}

	// res.Header trae la ruta ABS; el modelo encadena por la ruta RELATIVA.
	header := strings.Replace(res.Header, abs, relPath, 1)
	return Result{Output: header}, nil
}

func (et *EditTool) patcher(ctx context.Context) *hashline.Patcher {
	if et.SnapshotProvider != nil {
		return hashline.NewPatcher(et.FS, et.SnapshotProvider.Snapshots(ctx))
	}
	return et.Patcher
}
