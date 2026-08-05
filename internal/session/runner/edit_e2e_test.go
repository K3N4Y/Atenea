package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

type editE2EProvider struct {
	mu      sync.Mutex
	request llm.Request
	call    llm.Event
}

func (p *editE2EProvider) Stream(_ context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.request = req
	p.mu.Unlock()
	out := make(chan llm.Event, 3)
	out <- llm.Event{Kind: llm.StepStarted}
	out <- p.call
	out <- llm.Event{Kind: llm.StepEnded}
	close(out)
	return out, nil
}

func (p *editE2EProvider) captured() llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.request
}

// TestEditModesThroughRunnerProviderPermissionFilesystemAndPublisher protects the
// real local public path shared by providers and the standalone TUI: a turn
// advertises the frozen definition, consumes the provider ToolCall, asks for
// permission, lands bytes, and durably publishes structured result metadata.
func TestEditModesThroughRunnerProviderPermissionFilesystemAndPublisher(t *testing.T) {
	tests := []struct {
		name        string
		mode        editmode.Mode
		wire        string
		input       func(string) json.RawMessage
		want        string
		wantCustom  bool
		description string
	}{
		{name: "hashline", mode: editmode.Hashline, wire: "edit", want: "new\n", wantCustom: true, description: "PUT", input: func(old string) json.RawMessage {
			body, _ := json.Marshal(map[string]string{"input": "[x.txt#" + hashline.ComputeFileHash(old) + "]\nPUT 1:\n+new"})
			return body
		}},
		{name: "apply_patch", mode: editmode.ApplyPatch, wire: "apply_patch", want: "new\n", wantCustom: true, description: "Begin Patch", input: func(string) json.RawMessage {
			body, _ := json.Marshal(map[string]string{"input": "*** Begin Patch\n*** Update File: x.txt\n@@\n-old\n+new\n*** End Patch\n"})
			return body
		}},
		{name: "patch_json", mode: editmode.Patch, wire: "edit", want: "new\n", description: "edits", input: func(string) json.RawMessage {
			return json.RawMessage(`{"path":"x.txt","edits":[{"op":"update","diff":"@@\n-old\n+new"}]}`)
		}},
		{name: "replace_json", mode: editmode.Replace, wire: "edit", want: "new\n", description: "old_string", input: func(string) json.RawMessage {
			return json.RawMessage(`{"path":"x.txt","old_string":"old","new_string":"new"}`)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			const old = "old\n"
			path := filepath.Join(root, "x.txt")
			if err := os.WriteFile(path, []byte(old), 0o640); err != nil {
				t.Fatal(err)
			}
			edit := tool.NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
			if tc.mode == editmode.Hashline {
				edit.Patcher.Snapshots.Record(path, old)
			}
			edit.Mode = tc.mode
			provider := &editE2EProvider{call: llm.Event{Kind: llm.ToolCall, CallID: "edit-1", ToolName: tc.wire, Input: tc.input(old)}}
			store := newRecordingStore()
			seedUser(t, store, "s")
			registry := tool.NewRegistry(tool.NewOutputStore(0), edit)
			r := NewRunner(store, session.NewMemoryInbox(), provider, registry, tool.Permissions{"edit": true}, func() string { return "assistant" })
			gate := &fakeGate{approved: true}
			r.SetPermissionGate(gate, policyFunc(func(string, tool.Call) permission.Decision { return permission.Ask }))

			continued, err := r.runTurn(context.Background(), "s")
			if err != nil || !continued {
				t.Fatalf("runTurn continued=%v err=%v", continued, err)
			}
			req := provider.captured()
			if len(req.Tools) != 1 {
				t.Fatalf("provider tools=%+v", req.Tools)
			}
			def := req.Tools[0]
			if def.Name != "edit" || def.WireName != tc.wire || (def.CustomFormat != nil) != tc.wantCustom || !strings.Contains(def.Description, tc.description) {
				t.Fatalf("selected definition=%+v", def)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != tc.want {
				t.Fatalf("landed bytes=%q err=%v", got, err)
			}
			if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
				t.Fatalf("landed permissions=%v", info.Mode().Perm())
			}
			if calls := gate.calls(); len(calls) != 1 || calls[0].CallID != "edit-1" {
				t.Fatalf("permission calls=%+v", calls)
			}
			var success *session.SessionEvent
			for _, ev := range store.snapshot() {
				if ev.Kind == session.KindToolSuccess && ev.CallID == "edit-1" {
					e := ev
					success = &e
				}
			}
			if success == nil || success.Diff == "" || success.Text == "" || success.Attrs["tool.files"] == "" {
				t.Fatalf("durable success=%+v", success)
			}
			var files []tool.FileResult
			if err := json.Unmarshal([]byte(success.Attrs["tool.files"]), &files); err != nil || len(files) != 1 || !files[0].Committed || files[0].OldText != old || files[0].NewText != tc.want || files[0].FirstChangedLine != 1 {
				t.Fatalf("durable files=%+v err=%v", files, err)
			}
		})
	}
}

func TestEditRunnerDeniedPermissionWritesNothing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	edit := tool.NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	edit.Mode = editmode.Replace
	provider := &editE2EProvider{call: llm.Event{Kind: llm.ToolCall, CallID: "denied", ToolName: "edit", Input: json.RawMessage(`{"path":"x.txt","old_string":"old","new_string":"new"}`)}}
	store := newRecordingStore()
	seedUser(t, store, "s")
	r := NewRunner(store, session.NewMemoryInbox(), provider, tool.NewRegistry(tool.NewOutputStore(0), edit), tool.Permissions{"edit": true}, func() string { return "assistant" })
	r.SetPermissionGate(&fakeGate{approved: false}, policyFunc(func(string, tool.Call) permission.Decision { return permission.Ask }))
	if _, err := r.runTurn(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old\n" {
		t.Fatalf("denied edit wrote %q", got)
	}
	log := store.snapshot()
	if _, ok := seqOfKind(log, session.KindToolPermissionRequested, "denied"); !ok {
		t.Fatal("permission request was not durable")
	}
	if _, ok := seqOfKind(log, session.KindToolFailed, "denied"); !ok {
		t.Fatal("denial was not durable")
	}
	if _, ok := seqOfKind(log, session.KindToolSuccess, "denied"); ok {
		t.Fatal("denied edit published success")
	}
}
