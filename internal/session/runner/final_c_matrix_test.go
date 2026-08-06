package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

type finalCFS struct {
	base hashline.OSFilesystem
	kind string
	wrap bool
}

func (f *finalCFS) ReadFile(path string) ([]byte, error) { return f.base.ReadFile(path) }
func (f *finalCFS) WriteFile(path string, data []byte, mode os.FileMode) error {
	if err := f.base.WriteFile(path, data, mode); err != nil {
		return err
	}
	if filepath.Base(path) == "a" && f.kind == "write" {
		err := error(&hashline.CommitUncertainError{Err: errors.New("injected replacement directory sync")})
		if f.wrap {
			err = fmt.Errorf("wrapped partial settlement: %w", err)
		}
		return err
	}
	return nil
}
func (f *finalCFS) Remove(path string) error {
	if err := f.base.Remove(path); err != nil {
		return err
	}
	if filepath.Base(path) == "a" && f.kind == "remove" {
		err := error(&hashline.CommitUncertainError{Err: errors.New("injected remove directory sync")})
		if f.wrap {
			err = fmt.Errorf("wrapped partial settlement: %w", err)
		}
		return err
	}
	return nil
}
func (f *finalCFS) Rename(source, destination string) error {
	if filepath.Base(source) == "a" && strings.HasPrefix(f.kind, "destination-") {
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := f.base.WriteFile(destination, data, 0o600); err != nil {
			return err
		}
		remains := f.kind == "destination-remains"
		if !remains {
			if err := f.base.Remove(source); err != nil {
				return err
			}
		}
		return &hashline.DestinationCommittedError{Err: errors.New("injected source settlement failure"), SourceRemains: remains}
	}
	if err := f.base.Rename(source, destination); err != nil {
		return err
	}
	if filepath.Base(source) == "a" && f.kind == "rename" {
		err := error(&hashline.CommitUncertainError{Err: errors.New("injected rename directory sync")})
		if f.wrap {
			err = fmt.Errorf("wrapped partial settlement: %w", err)
		}
		return err
	}
	return nil
}
func (f *finalCFS) MoveWithContent(source, destination string, data []byte, mode os.FileMode) error {
	if err := f.base.MoveWithContent(source, destination, data, mode); err != nil {
		return err
	}
	if filepath.Base(source) != "a" {
		return nil
	}
	if strings.HasPrefix(f.kind, "destination-") {
		remains := f.kind == "destination-remains"
		if remains {
			if err := os.WriteFile(source, []byte("old\n"), mode); err != nil {
				return err
			}
		}
		return &hashline.DestinationCommittedError{Err: errors.New("injected source settlement failure"), SourceRemains: remains}
	}
	if f.kind == "rename" {
		err := error(&hashline.CommitUncertainError{Err: errors.New("injected rename directory sync")})
		if f.wrap {
			err = fmt.Errorf("wrapped partial settlement: %w", err)
		}
		return err
	}
	return nil
}

type finalCCase struct {
	name, mode, op, fault string
	partial               bool
}

func finalCOperation(op string) contract.FileOperation {
	switch op {
	case "create":
		return contract.FileCreated
	case "delete":
		return contract.FileDeleted
	case "move":
		return contract.FileMoved
	default:
		return contract.FileUpdated
	}
}

func finalCSingleInput(mode, op string) json.RawMessage {
	if mode == "replace" {
		return json.RawMessage(`{"path":"a","old_string":"old","new_string":"new"}`)
	}
	if mode == "patch" {
		var item string
		switch op {
		case "create":
			item = `{"op":"create","diff":"+new\n"}`
		case "delete":
			item = `{"op":"delete"}`
		case "move":
			item = `{"op":"update","rename":"b","diff":"@@\n-old\n+new"}`
		default:
			item = `{"op":"update","diff":"@@\n-old\n+new"}`
		}
		return json.RawMessage(`{"path":"a","edits":[` + item + `]}`)
	}
	var section string
	switch op {
	case "create":
		section = "*** Add File: a\n+new"
	case "delete":
		section = "*** Delete File: a"
	case "move":
		section = "*** Update File: a\n*** Move to: b\n@@\n-old\n+new"
	default:
		section = "*** Update File: a\n@@\n-old\n+new"
	}
	b, _ := json.Marshal(map[string]string{"input": "*** Begin Patch\n" + section + "\n*** End Patch"})
	return b
}

func finalCPartialInput(op string) json.RawMessage {
	var middle string
	switch op {
	case "create":
		middle = "*** Add File: a\n+new"
	case "delete":
		middle = "*** Delete File: a"
	case "move":
		middle = "*** Update File: a\n*** Move to: b\n@@\n-old\n+new"
	default:
		middle = "*** Update File: a\n@@\n-old\n+new"
	}
	patch := "*** Begin Patch\n*** Update File: p\n@@\n-P\n+PP\n" + middle + "\n*** Update File: z\n@@\n-Z\n+ZZ\n*** End Patch"
	b, _ := json.Marshal(map[string]string{"input": patch})
	return b
}

// TestFinalC_PublicCommittedUncertainSettlementAllOperations is the public
// committed-uncertain settlement table. Every cell enters through Registry,
// runner settlement, and durable Publisher persistence rather than a patch helper.
func TestFinalC_PublicCommittedUncertainSettlementAllOperations(t *testing.T) {
	cases := []finalCCase{
		{"replace/update/commit_uncertain", "replace", "update", "write", false},
		{"patch/create/commit_uncertain", "patch", "create", "write", false},
		{"patch/update/commit_uncertain", "patch", "update", "write", false},
		{"patch/delete/commit_uncertain", "patch", "delete", "remove", false},
		{"patch/move/commit_uncertain", "patch", "move", "rename", false},
		{"apply_patch/create/commit_uncertain", "apply_patch", "create", "write", false},
		{"apply_patch/update/commit_uncertain", "apply_patch", "update", "write", false},
		{"apply_patch/delete/commit_uncertain", "apply_patch", "delete", "remove", false},
		{"apply_patch/move/commit_uncertain", "apply_patch", "move", "rename", false},
		{"patch/move/destination_committed_source_remains", "patch", "move", "destination-remains", false},
		{"patch/move/destination_committed_source_uncertain", "patch", "move", "destination-uncertain", false},
		{"apply_patch/move/destination_committed_source_remains", "apply_patch", "move", "destination-remains", false},
		{"apply_patch/move/destination_committed_source_uncertain", "apply_patch", "move", "destination-uncertain", false},
		{"apply_patch/create/wrapped_partial_1", "apply_patch", "create", "write", true},
		{"apply_patch/create/wrapped_partial_2", "apply_patch", "create", "write", true},
		{"apply_patch/update/wrapped_partial_1", "apply_patch", "update", "write", true},
		{"apply_patch/update/wrapped_partial_2", "apply_patch", "update", "write", true},
		{"apply_patch/delete/wrapped_partial_1", "apply_patch", "delete", "remove", true},
		{"apply_patch/delete/wrapped_partial_2", "apply_patch", "delete", "remove", true},
		{"apply_patch/move/wrapped_partial", "apply_patch", "move", "rename", true},
	}
	if len(cases) != 20 {
		t.Fatalf("matrix rows=%d, want exactly 20", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, text := range map[string]string{"p": "P\n", "z": "Z\n"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.op != "create" {
				if err := os.WriteFile(filepath.Join(root, "a"), []byte("old\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fs := &finalCFS{kind: tc.fault, wrap: tc.partial}
			snaps := hashline.NewMemSnapshotStore()
			for name, text := range map[string]string{"p": "P\n", "z": "Z\n"} {
				snaps.Record(filepath.Join(root, name), text)
			}
			if tc.op != "create" {
				snaps.Record(filepath.Join(root, "a"), "old\n")
			}
			edit := tool.NewEditTool(root, fs, snaps)
			switch tc.mode {
			case "replace":
				edit.Mode = editmode.Replace
			case "patch":
				edit.Mode = editmode.Patch
			default:
				edit.Mode = editmode.ApplyPatch
			}
			input := finalCSingleInput(tc.mode, tc.op)
			if tc.partial {
				input = finalCPartialInput(tc.op)
			}
			provider := &editE2EProvider{call: llm.Event{Kind: llm.ToolCall, CallID: "final-c", ToolName: map[bool]string{true: "apply_patch", false: "edit"}[tc.mode == "apply_patch"], Input: input}}
			store := newRecordingStore()
			seedUser(t, store, "s")
			registry := tool.NewRegistry(tool.NewOutputStore(0), edit)
			r := newRunner(store, session.NewMemoryInbox(), provider, registry, registry.Permissions(), func() string { return "assistant" })
			continued, err := r.runTurn(context.Background(), "s")
			if err != nil || !continued {
				t.Fatalf("runner continued=%v err=%v", continued, err)
			}
			var success *session.SessionEvent
			for _, event := range store.snapshot() {
				if event.CallID == "final-c" && event.Kind == session.KindToolFailed {
					t.Fatalf("committed uncertainty published Tool.Failed: %+v", event)
				}
				if event.CallID == "final-c" && event.Kind == session.KindToolSuccess {
					copy := event
					success = &copy
				}
			}
			if success == nil || success.Message == nil || !strings.Contains(strings.ToLower(success.Text), "do not retry") {
				t.Fatalf("durable Tool.Success lacks model no-retry guidance: %+v", success)
			}
			var files []contract.FileResult
			if err := json.Unmarshal([]byte(success.Attrs["tool.files"]), &files); err != nil {
				t.Fatal(err)
			}
			wantLen := 1
			if tc.partial {
				wantLen = 3
			}
			if len(files) != wantLen {
				t.Fatalf("files=%+v want length %d", files, wantLen)
			}
			landed := files[0]
			if tc.partial {
				landed = files[1]
			}
			source, destination := filepath.Join(root, "a"), filepath.Join(root, "a")
			if tc.op == "move" {
				destination = filepath.Join(root, "b")
			}
			if landed.Operation != finalCOperation(tc.op) || landed.Path != destination || landed.SourcePath != source || landed.Destination != destination || !landed.Committed || !strings.Contains(strings.ToLower(landed.Error+landed.DisplayError), "do not retry") {
				t.Fatalf("committed row=%+v", landed)
			}
			if tc.op == "delete" {
				if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) || landed.OldText != "old\n" || landed.NewText != "" || landed.Header != "" || snaps.Head(source) != nil {
					t.Fatalf("delete disk/result/snapshot row=%+v stat=%v head=%+v", landed, err, snaps.Head(source))
				}
			} else {
				got, err := os.ReadFile(destination)
				if err != nil || string(got) != "new\n" || landed.NewText != "new\n" || (tc.op != "create" && landed.OldText != "old\n") || landed.Header == "" || snaps.Head(destination) == nil || snaps.Head(destination).Text != "new\n" {
					t.Fatalf("disk/result/snapshot row=%+v bytes=%q err=%v head=%+v", landed, got, err, snaps.Head(destination))
				}
			}
			if tc.op == "move" {
				_, sourceErr := os.Stat(source)
				remains := tc.fault == "destination-remains"
				if remains != (sourceErr == nil) || (!remains && !errors.Is(sourceErr, os.ErrNotExist)) || (!remains && snaps.Head(source) != nil) {
					t.Fatalf("move source remains=%v stat=%v sourceHead=%+v", remains, sourceErr, snaps.Head(source))
				}
				if strings.HasPrefix(tc.fault, "destination-") && (len(landed.Warnings) != 1 || !strings.Contains(landed.Warnings[0], "destination committed")) {
					t.Fatalf("destination warning=%v", landed.Warnings)
				}
			}
			if tc.partial {
				if files[0].Path != filepath.Join(root, "p") || !files[0].Committed || files[0].NewText != "PP\n" || files[2].Path != filepath.Join(root, "z") || files[2].Committed || files[2].Operation != contract.FileError || files[2].Error != "not applied because an earlier patch entry committed with uncertain settlement; do not retry the committed entry" || files[2].DisplayError != files[2].Error {
					t.Fatalf("ordered committed/uncertain/skipped partition=%+v", files)
				}
				if got, _ := os.ReadFile(filepath.Join(root, "z")); string(got) != "Z\n" {
					t.Fatalf("trailing skipped bytes=%q", got)
				}
			}
			encoded, _ := json.Marshal(files)
			var roundTrip []contract.FileResult
			if err := json.Unmarshal(encoded, &roundTrip); err != nil || !reflect.DeepEqual(roundTrip, files) {
				t.Fatalf("publisher rehydration changed files: %+v err=%v", roundTrip, err)
			}
		})
	}
}
