package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

type round4MoveFaultFS struct {
	files map[string][]byte
	kind  string
}

func (f *round4MoveFaultFS) ReadFile(p string) ([]byte, error) {
	if f.kind == "destination-read-error" && strings.HasSuffix(p, "destination") {
		return nil, errors.New("destination permission denied")
	}
	b, ok := f.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}
func (f *round4MoveFaultFS) WriteFile(p string, b []byte, _ os.FileMode) error {
	f.files[p] = append([]byte(nil), b...)
	return nil
}
func (f *round4MoveFaultFS) MoveWithContent(a, b string, data []byte, _ os.FileMode) error {
	if _, exists := f.files[b]; exists {
		return os.ErrExist
	}
	if f.kind == "before-publish" {
		return errors.New("injected before publish")
	}
	f.files[b] = append([]byte(nil), data...)
	switch f.kind {
	case "destination-committed-source-remains":
		return &hashline.DestinationCommittedError{Err: errors.New("source settlement failed"), SourceRemains: true}
	case "destination-committed-source-removed":
		delete(f.files, a)
		return &hashline.DestinationCommittedError{Err: errors.New("source durability uncertain"), SourceRemains: false}
	}
	delete(f.files, a)
	return nil
}

func TestRound4_HashlineEditMoveFailureStates(t *testing.T) {
	for _, tc := range []struct {
		name, kind                        string
		destination                       bool
		committed, source, dest, separate bool
	}{
		{"destination existing collision", "collision", true, false, true, true, false},
		{"destination read error", "destination-read-error", false, false, true, false, false},
		{"fault before publish", "before-publish", false, false, true, false, false},
		{"destination committed source remains", "destination-committed-source-remains", false, true, true, true, true},
		{"source removed durability uncertain", "destination-committed-source-removed", false, true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Clean("/work")
			source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
			files := map[string][]byte{source: []byte("old\n")}
			if tc.destination {
				files[destination] = []byte("collision winner\n")
			}
			fs := &round4MoveFaultFS{files: files, kind: tc.kind}
			snaps := hashline.NewMemSnapshotStore()
			h, _ := snaps.Record(source, "old\n")
			edit := NewEditTool(root, fs, snaps)
			body, _ := json.Marshal(map[string]string{"input": "[source#" + h + "]\nPUT 1:\n+new\nMV destination"})
			result, err := edit.Execute(context.Background(), body)
			if tc.committed {
				if err != nil || len(result.Files) != 1 || !result.Files[0].Committed || !strings.Contains(result.Files[0].DisplayError, "do not retry") {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil || len(result.Files) != 1 || result.Files[0].Committed || result.Files[0].Header != "" {
				t.Fatalf("uncommitted result=%+v err=%v", result, err)
			}
			_, sourceExists := fs.files[source]
			_, destExists := fs.files[destination]
			if sourceExists != tc.source || destExists != tc.dest {
				t.Fatalf("files=%q", fs.files)
			}
			if tc.destination && string(fs.files[destination]) != "collision winner\n" {
				t.Fatalf("collision overwritten=%q", fs.files[destination])
			}
			if tc.separate {
				if snaps.Head(source) == nil || snaps.Head(source).Text != "old\n" || snaps.Head(destination) == nil || snaps.Head(destination).Text != "new\n" {
					t.Fatalf("source=%+v dest=%+v", snaps.Head(source), snaps.Head(destination))
				}
			} else if tc.committed && (snaps.Head(source) != nil || snaps.Head(destination) == nil) {
				t.Fatalf("source=%+v dest=%+v", snaps.Head(source), snaps.Head(destination))
			}
		})
	}
}
