package runner

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

func TestCategoryH_SessionEventPersistenceRehydratesEachSettlementShape(t *testing.T) {
	store := session.NewMemoryStore()
	p := NewPublisher(store, "s", "assistant", 0)
	files := []contract.FileResult{
		{Path: "update", SourcePath: "update", Operation: contract.FileUpdated, OldText: "a", NewText: "b", Diff: "du", Warnings: []string{"w"}, Diagnostics: []contract.Diagnostic{{Severity: "warning", Message: "d", Line: 3}}, Header: "[u#h]", FirstChangedLine: 3, Committed: true},
		{Path: "create", SourcePath: "create", Operation: contract.FileCreated, NewText: "c", Committed: true},
		{Path: "delete", SourcePath: "delete", Operation: contract.FileDeleted, OldText: "d", Committed: true},
		{Path: "move", SourcePath: "move", Destination: "moved", Operation: contract.FileMoved, OldText: "m", NewText: "m", Committed: true},
		{Path: "noop", SourcePath: "noop", Operation: contract.FileNoop},
		{Path: "error", SourcePath: "error", Operation: contract.FileError, Error: "failed", DisplayError: "display"},
		{Path: "uncertain", SourcePath: "uncertain", Destination: "landed", Operation: contract.FileMoved, Committed: true, Error: "durability uncertain; do not retry", DisplayError: "committed uncertain", Header: "[uncertain#h]"},
	}
	for i, file := range files {
		result := tool.Result{Output: "bounded model guidance", Truncated: true, Diff: "aggregate", Files: []contract.FileResult{file}, Metadata: map[string]any{"partial": i == len(files)-1}}
		if err := p.ToolSuccessResult(context.Background(), "call"+string(rune('0'+i)), result); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.Events(context.Background(), "s", 0)
	if err != nil || len(events) != len(files) {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	for i, ev := range events {
		if ev.Text != "bounded model guidance" || ev.Diff != "aggregate" || ev.Message == nil || ev.Message.Text != ev.Text || ev.Attrs["tool.truncated"] != "true" {
			t.Fatalf("event[%d]=%+v", i, ev)
		}
		var got []contract.FileResult
		if err := json.Unmarshal([]byte(ev.Attrs["tool.files"]), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, []contract.FileResult{files[i]}) {
			t.Fatalf("event[%d] rehydrated files=%+v want=%+v", i, got, files[i])
		}
		var metadata map[string]any
		if err := json.Unmarshal([]byte(ev.Attrs["tool.metadata"]), &metadata); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(metadata, map[string]any{"partial": i == len(files)-1}) {
			t.Fatalf("event[%d] metadata=%v", i, metadata)
		}
	}
}

func TestCategoryH_PartialAggregatePersistsOrderedMixedOutcomes(t *testing.T) {
	store := session.NewMemoryStore()
	publisher := NewPublisher(store, "s", "assistant", 0)
	files := []contract.FileResult{
		{Path: "a", SourcePath: "a", Operation: contract.FileUpdated, OldText: "A", NewText: "AA", Diff: "file-diff", Warnings: []string{"warning"}, Diagnostics: []contract.Diagnostic{{Severity: "warning", Message: "diagnostic", Line: 1}}, Header: "[a#hash]", FirstChangedLine: 1, Committed: true},
		{Path: "b", SourcePath: "b", Operation: contract.FileError, Error: "commit failed", DisplayError: "commit failed"},
		{Path: "c", SourcePath: "c", Operation: contract.FileError, Error: "not applied because an earlier section failed", DisplayError: "not applied because an earlier section failed"},
	}
	result := tool.Result{Output: "bounded guidance", Truncated: true, Diff: "aggregate-diff", Files: files, Metadata: map[string]any{"partial": true, "warnings": []string{"aggregate warning"}, "diagnostics": []string{"aggregate diagnostic"}}}
	if err := publisher.ToolSuccessResult(context.Background(), "call", result); err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(context.Background(), "s", 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	event := events[0]
	if event.Text != result.Output || event.Diff != result.Diff || event.Attrs["tool.truncated"] != "true" {
		t.Fatalf("event=%+v", event)
	}
	var rehydrated []contract.FileResult
	if err := json.Unmarshal([]byte(event.Attrs["tool.files"]), &rehydrated); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rehydrated, files) {
		t.Fatalf("files=%+v want=%+v", rehydrated, files)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.Attrs["tool.metadata"]), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["partial"] != true || len(metadata["warnings"].([]any)) != 1 || len(metadata["diagnostics"].([]any)) != 1 {
		t.Fatalf("metadata=%+v", metadata)
	}
}
