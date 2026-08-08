package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

type finalBFS struct {
	files   map[string][]byte
	fault   string
	mutated map[string]bool
}

func (f *finalBFS) ReadFile(p string) ([]byte, error) {
	if f.fault == "readback" && f.mutated[p] {
		return nil, errors.New("injected readback failure")
	}
	b, ok := f.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}
func (f *finalBFS) WriteFile(p string, b []byte, _ os.FileMode) error {
	if f.fault == "noop" {
		return nil
	}
	if f.fault == "transform" {
		b = append(append([]byte(nil), b...), []byte("DRIFT\n")...)
	}
	f.files[p] = append([]byte(nil), b...)
	f.mutated[p] = true
	return nil
}
func (f *finalBFS) Remove(p string) error {
	if f.fault == "noop" {
		return nil
	}
	delete(f.files, p)
	f.mutated[p] = true
	if f.fault == "readback" {
		return &hashline.CommitUncertainError{Err: errors.New("injected remove settlement failure")}
	}
	return nil
}
func (f *finalBFS) Rename(a, b string) error {
	if f.fault == "noop" {
		return nil
	}
	f.files[b] = f.files[a]
	delete(f.files, a)
	f.mutated[b] = true
	if f.fault == "readback" {
		return &hashline.CommitUncertainError{Err: errors.New("injected move settlement failure")}
	}
	return nil
}
func (f *finalBFS) MoveWithContent(a, b string, data []byte, _ os.FileMode) error {
	if f.fault == "noop" {
		return nil
	}
	if f.fault == "transform" {
		data = append(append([]byte(nil), data...), []byte("DRIFT\n")...)
	}
	f.files[b] = append([]byte(nil), data...)
	delete(f.files, a)
	f.mutated[b] = true
	if f.fault == "readback" {
		return &hashline.CommitUncertainError{Err: errors.New("injected move settlement failure")}
	}
	return nil
}

func finalBRaw(mode editmode.Mode, op string) json.RawMessage {
	switch mode {
	case editmode.Replace:
		return json.RawMessage(`{"path":"a","old_string":"old","new_string":"new"}`)
	case editmode.Patch:
		switch op {
		case "create":
			return json.RawMessage(`{"path":"a","edits":[{"op":"create","diff":"+new\n"}]}`)
		case "update":
			return json.RawMessage(`{"path":"a","edits":[{"op":"update","diff":"@@\n-old\n+new"}]}`)
		case "move":
			return json.RawMessage(`{"path":"a","edits":[{"op":"update","rename":"b","diff":"@@\n-old\n+new"}]}`)
		default:
			return json.RawMessage(`{"path":"a","edits":[{"op":"delete"}]}`)
		}
	default:
		var body string
		switch op {
		case "create":
			body = "*** Add File: a\n+new"
		case "update":
			body = "*** Update File: a\n@@\n-old\n+new"
		case "move":
			body = "*** Update File: a\n*** Move to: b\n@@\n-old\n+new"
		default:
			body = "*** Delete File: a"
		}
		b, _ := json.Marshal(map[string]string{"input": "*** Begin Patch\n" + body + "\n*** End Patch"})
		return b
	}
}

// TestNonHashlineSettlementUsesLandedStateAndCrossTurnSnapshots covers the
// common non-hashline settlement matrix by mode, operation, and fault.
func TestNonHashlineSettlementUsesLandedStateAndCrossTurnSnapshots(t *testing.T) {
	cases := []struct {
		name string
		mode editmode.Mode
		op   string
	}{
		{"replace", editmode.Replace, "update"},
		{"patch", editmode.Patch, "create"}, {"patch", editmode.Patch, "update"}, {"patch", editmode.Patch, "move"}, {"patch", editmode.Patch, "delete"},
		{"apply_patch", editmode.ApplyPatch, "create"}, {"apply_patch", editmode.ApplyPatch, "update"}, {"apply_patch", editmode.ApplyPatch, "move"}, {"apply_patch", editmode.ApplyPatch, "delete"},
	}
	for _, tc := range cases {
		faults := []string{"transform", "noop", "readback", "normal"}
		if tc.op == "delete" {
			faults = []string{"noop", "readback", "normal"}
		}
		for _, fault := range faults {
			t.Run(tc.name+"/"+tc.op+"/"+fault, func(t *testing.T) {
				root := t.TempDir()
				source, destination := filepath.Join(root, "a"), filepath.Join(root, "b")
				files := map[string][]byte{}
				if tc.op != "create" {
					files[source] = []byte("old\n")
				}
				fs := &finalBFS{files: files, fault: fault, mutated: map[string]bool{}}
				snaps := hashline.NewMemSnapshotStore()
				oldHash := ""
				if tc.op != "create" {
					oldHash, _ = snaps.Record(source, "old\n")
				}
				edit := NewEditTool(root, fs, snaps)
				edit.Mode = tc.mode
				res, err := edit.Execute(context.Background(), finalBRaw(tc.mode, tc.op))
				target := source
				if tc.op == "move" {
					target = destination
				}
				f, has := contract.FileResult{}, len(res.Files) == 1
				if has {
					f = res.Files[0]
				}
				switch fault {
				case "transform":
					if err != nil || !has || !f.Committed || f.Path != target || f.SourcePath != source || f.Destination != target || string(files[target]) != f.NewText || !strings.Contains(f.NewText, "DRIFT") || !strings.Contains(strings.Join(f.Warnings, " "), "changed during write") || f.Diff == "" || f.Header == "" || f.OldText != map[bool]string{true: "", false: "old\n"}[tc.op == "create"] || snaps.Head(target) == nil || snaps.Head(target).Text != f.NewText {
						t.Fatalf("result=%+v err=%v files=%q head=%+v", res, err, files, snaps.Head(target))
					}
				case "noop":
					if err == nil || !has || f.Committed || f.Header != "" || f.Operation != contract.FileError || f.Error == "" {
						t.Fatalf("swallowed mutation falsely settled: result=%+v err=%v files=%q", res, err, files)
					}
					if tc.op == "create" && snaps.Head(source) != nil {
						t.Fatal("missing create acquired snapshot")
					}
					if tc.op == "delete" && snaps.Head(source) == nil {
						t.Fatal("visible file lost provenance")
					}
				case "readback":
					if err != nil || !has || !f.Committed || (!strings.Contains(f.Error, "do not retry") && !strings.Contains(f.DisplayError, "do not retry")) || f.Header != "" {
						t.Fatalf("uncertain result=%+v err=%v", res, err)
					}
					if tc.op == "move" {
						if _, src := files[source]; src || len(files[destination]) == 0 {
							t.Fatalf("move state=%q", files)
						}
					}
					if tc.op == "delete" {
						if _, exists := files[source]; exists || snaps.Head(source) != nil {
							t.Fatalf("delete state=%q head=%+v", files, snaps.Head("a"))
						}
					}
				case "normal":
					if err != nil || !has || !f.Committed {
						t.Fatalf("result=%+v err=%v", res, err)
					}
					if tc.op == "delete" {
						if snaps.Head(source) != nil {
							t.Fatal("delete retained snapshot")
						}
						edit.Mode = editmode.Hashline
						raw, _ := json.Marshal(map[string]string{"input": "[a#" + oldHash + "]\nPUT 1:\n+again"})
						if r, e := edit.Execute(context.Background(), raw); e == nil || len(r.Files) > 0 && r.Files[0].Committed {
							t.Fatalf("stale delete editable: %+v %v", r, e)
						}
						return
					}
					if f.Header == "" || snaps.Head(target) == nil {
						t.Fatalf("missing provenance: %+v", f)
					}
					if tc.op == "move" && snaps.Head(source) != nil {
						t.Fatal("move history not relocated")
					}
					edit.Mode = editmode.Hashline
					h := snaps.Head(target).Hash
					raw, _ := json.Marshal(map[string]string{"input": "[" + target + "#" + h + "]\nPUT 1:\n+again"})
					r, e := edit.Execute(context.Background(), raw)
					if e != nil || len(r.Files) != 1 || !r.Files[0].Committed {
						t.Fatalf("cross-turn result=%+v err=%v", r, e)
					}
				}
			})
		}
	}
}
