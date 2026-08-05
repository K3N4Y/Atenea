package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

func executeAtBarrier(t *testing.T, calls ...func() (Result, error)) ([]Result, []error) {
	t.Helper()
	start := make(chan struct{})
	results, errs := make([]Result, len(calls)), make([]error, len(calls))
	var ready, done sync.WaitGroup
	ready.Add(len(calls))
	done.Add(len(calls))
	for i, call := range calls {
		i, call := i, call
		go func() { defer done.Done(); ready.Done(); <-start; results[i], errs[i] = call() }()
	}
	ready.Wait()
	close(start)
	finished := make(chan struct{})
	go func() { done.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent edit calls deadlocked")
	}
	return results, errs
}

func assertOneStructuredWinner(t *testing.T, results []Result, errs []error) int {
	t.Helper()
	winner := -1
	for i := range errs {
		if errs[i] == nil {
			if winner >= 0 || len(results[i].Files) != 1 || !results[i].Files[0].Committed {
				t.Fatalf("outcomes results=%+v errs=%v", results, errs)
			}
			winner = i
		} else if len(results[i].Files) > 0 && results[i].Files[0].Committed {
			t.Fatalf("loser reported committed: result=%+v err=%v", results[i], errs[i])
		}
	}
	if winner < 0 {
		t.Fatalf("no winner: results=%+v errs=%v", results, errs)
	}
	return winner
}

func TestFinalA_PublicPatchVersusHashlineSameFileSerializable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	matrixMustWrite(t, path, "A\n")
	snaps := hashline.NewMemSnapshotStore()
	h, _ := snaps.Record(path, "A\n")
	hash := NewEditTool(root, hashline.OSFilesystem{}, snaps)
	patch := NewEditTool(root, hashline.OSFilesystem{}, snaps)
	patch.Mode = editmode.Patch
	hraw, _ := json.Marshal(map[string]string{"input": "[x.txt#" + h + "]\nPUT 1:\n+H"})
	praw := json.RawMessage(`{"path":"x.txt","edits":[{"op":"update","diff":"@@\n-A\n+P"}]}`)
	results, errs := executeAtBarrier(t, func() (Result, error) { return hash.Execute(context.Background(), hraw) }, func() (Result, error) { return patch.Execute(context.Background(), praw) })
	winner := assertOneStructuredWinner(t, results, errs)
	got, _ := os.ReadFile(path)
	want := "H\n"
	if winner == 1 {
		want = "P\n"
	}
	if string(got) != want {
		t.Fatalf("winner=%d disk=%q want=%q", winner, got, want)
	}
	assertSnapshotMatchesDisk(t, snaps, path)
}

func TestFinalA_PublicApplyPatchVersusReplaceSameFileSerializable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	matrixMustWrite(t, path, "old\n")
	snaps := hashline.NewMemSnapshotStore()
	apply := NewEditTool(root, hashline.OSFilesystem{}, snaps)
	apply.Mode = editmode.ApplyPatch
	replace := NewEditTool(root, hashline.OSFilesystem{}, snaps)
	replace.Mode = editmode.Replace
	a := json.RawMessage(`{"input":"*** Begin Patch\n*** Update File: x.txt\n@@\n-old\n+apply\n*** End Patch"}`)
	r := json.RawMessage(`{"path":"x.txt","old_string":"old","new_string":"replace"}`)
	results, errs := executeAtBarrier(t, func() (Result, error) { return apply.Execute(context.Background(), a) }, func() (Result, error) { return replace.Execute(context.Background(), r) })
	winner := assertOneStructuredWinner(t, results, errs)
	got, _ := os.ReadFile(path)
	want := "apply\n"
	if winner == 1 {
		want = "replace\n"
	}
	if string(got) != want {
		t.Fatalf("winner=%d disk=%q want=%q", winner, got, want)
	}
	assertSnapshotMatchesDisk(t, snaps, path)
}

func TestFinalA_PublicSuffixReplaceVersusDirectHashlineSerializable(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, "deep"), 0755)
	path := filepath.Join(root, "deep", "x.txt")
	matrixMustWrite(t, path, "old\n")
	snaps := hashline.NewMemSnapshotStore()
	h, _ := snaps.Record(path, "old\n")
	replace := NewEditTool(root, hashline.OSFilesystem{}, snaps)
	replace.Mode = editmode.Replace
	hash := NewEditTool(root, hashline.OSFilesystem{}, snaps)
	r := json.RawMessage(`{"path":"x.txt","old_string":"old","new_string":"suffix"}`)
	hraw, _ := json.Marshal(map[string]string{"input": "[deep/x.txt#" + h + "]\nPUT 1:\n+direct"})
	results, errs := executeAtBarrier(t, func() (Result, error) { return replace.Execute(context.Background(), r) }, func() (Result, error) { return hash.Execute(context.Background(), hraw) })
	winner := assertOneStructuredWinner(t, results, errs)
	got, _ := os.ReadFile(path)
	want := "suffix\n"
	if winner == 1 {
		want = "direct\n"
	}
	if string(got) != want {
		t.Fatalf("winner=%d disk=%q", winner, got)
	}
	assertSnapshotMatchesDisk(t, snaps, path)
}

func TestFinalA_PublicOverlappingPatchApplyMoveSetsSerializable(t *testing.T) {
	root := t.TempDir()
	source, middle, destination := filepath.Join(root, "source.txt"), filepath.Join(root, "middle.txt"), filepath.Join(root, "destination.txt")
	matrixMustWrite(t, source, "base\n")
	snaps := hashline.NewMemSnapshotStore()
	patch := NewEditTool(root, hashline.OSFilesystem{}, snaps)
	patch.Mode = editmode.Patch
	apply := NewEditTool(root, hashline.OSFilesystem{}, snaps)
	apply.Mode = editmode.ApplyPatch
	moveToMiddle := json.RawMessage(`{"path":"source.txt","edits":[{"op":"update","rename":"middle.txt","diff":"@@\n-base\n+patch"}]}`)
	moveToDestination := json.RawMessage(`{"input":"*** Begin Patch\n*** Update File: middle.txt\n*** Move to: destination.txt\n@@\n-patch\n+apply\n*** End Patch"}`)
	results, errs := executeAtBarrier(t,
		func() (Result, error) { return patch.Execute(context.Background(), moveToMiddle) },
		func() (Result, error) { return apply.Execute(context.Background(), moveToDestination) },
	)
	if errs[0] != nil || len(results[0].Files) != 1 || !results[0].Files[0].Committed {
		t.Fatalf("source move did not settle structurally: result=%+v err=%v", results[0], errs[0])
	}
	if errs[1] == nil {
		if len(results[1].Files) != 1 || !results[1].Files[0].Committed {
			t.Fatalf("destination move success is unstructured: %+v", results[1])
		}
		got, err := os.ReadFile(destination)
		if err != nil || string(got) != "apply\n" {
			t.Fatalf("successful serial order destination=%q err=%v", got, err)
		}
		if _, err := os.Stat(middle); !os.IsNotExist(err) {
			t.Fatalf("move source remains: %v", err)
		}
		assertSnapshotMatchesDisk(t, snaps, destination)
	} else {
		got, err := os.ReadFile(middle)
		if err != nil || string(got) != "patch\n" {
			t.Fatalf("failed-first serial order middle=%q err=%v; apply err=%v", got, err, errs[1])
		}
		if len(results[1].Files) != 1 || results[1].Files[0].Committed {
			t.Fatalf("failed call outcome is not structured: %+v", results[1])
		}
		assertSnapshotMatchesDisk(t, snaps, middle)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("original source remains: %v", err)
	}
	for _, entry := range mustReadDir(t, root) {
		if strings.HasPrefix(entry.Name(), ".atenea-") {
			t.Fatalf("temporary artifact leaked: %s", entry.Name())
		}
	}
}

func mustReadDir(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestFinalD_PublicPreviewUsesCommittedRegisterWithoutMutatingSession(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	matrixMustWrite(t, path, "one\ntwo\n")
	provider := NewSessionSnapshots()
	edit := NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, provider)
	ctx := WithSessionID(context.Background(), "s")
	h, _ := provider.Snapshots(ctx).Record(path, "one\ntwo\n")
	cut, _ := json.Marshal(map[string]string{"input": "[x.txt#" + h + "]\nCUT 1 @named"})
	if _, err := edit.Execute(ctx, cut); err != nil {
		t.Fatal(err)
	}
	live := edit.existingPatcher(ctx)
	before := live.ForkClipboard()
	head := provider.Snapshots(ctx).Head(path)
	preview := edit.Preview(ctx, json.RawMessage(`{"input":"[x.txt#`+head.Hash+`]\nPUT >$ @named"}`))
	if len(preview.Files) != 1 || preview.Files[0].NewText != "two\none\n" {
		t.Fatalf("preview=%+v", preview)
	}
	after := live.ForkClipboard()
	if strings.Join(before.Named["named"], "\n") != strings.Join(after.Named["named"], "\n") {
		t.Fatal("preview mutated register")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "two\n" {
		t.Fatalf("preview wrote disk: %q", got)
	}
}

func TestFinalD_PreviewLookupDoesNotCreateSessionState(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	matrixMustWrite(t, path, "x\n")
	edit := NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, NewSessionSnapshots())
	ctx := WithSessionID(context.Background(), "absent")
	_ = edit.Preview(ctx, json.RawMessage(`{"input":"[x.txt#DEAD]\nPUT 1:\n+y"}`))
	edit.state.mu.Lock()
	defer edit.state.mu.Unlock()
	if len(edit.state.patchers) != 0 {
		t.Fatalf("preview created state: %+v", edit.state.patchers)
	}
}

func TestFinalD_PublicStructuralPreviewMatchesExecution(t *testing.T) {
	cases := []struct{ name, operation string }{
		{"put block", "PUT 1*:\n+func replacement() {\n+\tprintln(9)\n+}"},
		{"cut block", "CUT 1*"},
		{"put after block", "PUT >1*:\n+func inserted() {}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "x.go")
			original := "func first() {\n\tprintln(1)\n}\n\nfunc second() {}\n"
			matrixMustWrite(t, path, original)
			provider := NewSessionSnapshots()
			ctx := WithSessionID(context.Background(), "structural")
			h, _ := provider.Snapshots(ctx).Record(path, original)
			edit := NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, provider)
			input := "[x.go#" + h + "]\n" + tc.operation
			raw, _ := json.Marshal(map[string]string{"input": input})
			preview := edit.Preview(ctx, raw)
			if preview.Error != "" || len(preview.Files) != 1 || preview.Files[0].Operation == "error" {
				t.Fatalf("preview=%+v", preview)
			}
			if got, _ := os.ReadFile(path); string(got) != original {
				t.Fatalf("preview mutated disk: %q", got)
			}
			if provider.Snapshots(ctx).Head(path).Hash != h {
				t.Fatal("preview mutated snapshot provenance")
			}
			partial, _ := json.Marshal(map[string]string{"input": input + "\nPUT"})
			partialPreview := edit.Preview(ctx, partial)
			if len(partialPreview.Files) == 0 || partialPreview.Files[0].NewText != preview.Files[0].NewText {
				t.Fatalf("incomplete active operation lost completed preview: %+v", partialPreview)
			}
			result, err := edit.Execute(ctx, raw)
			if err != nil || len(result.Files) != 1 {
				t.Fatalf("execute result=%+v err=%v", result, err)
			}
			if preview.Files[0].OldText != result.Files[0].OldText || preview.Files[0].NewText != result.Files[0].NewText || preview.Files[0].Diff != result.Files[0].Diff {
				t.Fatalf("preview/execution mismatch preview=%+v result=%+v", preview.Files[0], result.Files[0])
			}
			got, _ := os.ReadFile(path)
			if string(got) != result.Files[0].NewText {
				t.Fatalf("disk=%q result=%q", got, result.Files[0].NewText)
			}
		})
	}
}

func TestFinalD_ConcurrentPreviewExecuteUsesForkedRegisterState(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	matrixMustWrite(t, path, "named\nanonymous\nleft\nright\n")
	provider := NewSessionSnapshots()
	ctx := WithSessionID(context.Background(), "register-race")
	edit := NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, provider)
	h, _ := provider.Snapshots(ctx).Record(path, "named\nanonymous\nleft\nright\n")
	seed, _ := json.Marshal(map[string]string{"input": "[x.txt#" + h + "]\nCUT 1 @saved\nCUT 2"})
	if _, err := edit.Execute(ctx, seed); err != nil {
		t.Fatal(err)
	}
	live := edit.existingPatcher(ctx)
	before := live.ForkClipboard()
	head := provider.Snapshots(ctx).Head(path)
	previewRaw, _ := json.Marshal(map[string]string{"input": "[x.txt#" + head.Hash + "]\nPUT >$ @saved"})
	executeRaw, _ := json.Marshal(map[string]string{"input": "[x.txt#" + head.Hash + "]\nPUT >$"})
	start := make(chan struct{})
	var preview Preview
	var result Result
	var executeErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; preview = edit.Preview(ctx, previewRaw) }()
	go func() { defer wg.Done(); <-start; result, executeErr = edit.Execute(ctx, executeRaw) }()
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("preview/execute deadlocked")
	}
	if executeErr != nil || len(result.Files) != 1 || result.Files[0].NewText != "left\nright\nanonymous\n" {
		t.Fatalf("execute result=%+v err=%v", result, executeErr)
	}
	if len(preview.Files) != 1 || (preview.Files[0].NewText != "left\nright\nnamed\n" && preview.Files[0].NewText != "left\nright\nanonymous\nnamed\n") {
		t.Fatalf("preview is not one of the serial projections: %+v", preview)
	}
	after := live.ForkClipboard()
	if strings.Join(before.Named["saved"], "\n") != strings.Join(after.Named["saved"], "\n") || strings.Join(before.Anonymous, "\n") != strings.Join(after.Anonymous, "\n") || len(after.PendingAnonCuts) != 0 {
		t.Fatalf("register state incorrect before=%+v after=%+v", before, after)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "left\nright\nanonymous\n" {
		t.Fatalf("disk=%q", got)
	}
}

func TestFinalE_StrictJSONRejectsTrailingValuesWithoutMutation(t *testing.T) {
	modes := []struct {
		name  string
		mode  editmode.Mode
		valid string
	}{
		{"replace", editmode.Replace, `{"path":"x.txt","old_string":"old","new_string":"new"}`},
		{"patch", editmode.Patch, `{"path":"x.txt","edits":[{"op":"update","diff":"@@\n-old\n+new"}]}`},
		{"apply_patch", editmode.ApplyPatch, `{"input":"*** Begin Patch\n*** Update File: x.txt\n@@\n-old\n+new\n*** End Patch"}`},
		{"hashline", editmode.Hashline, `{"input":"[x.txt#HASH]\nPUT 1:\n+new"}`},
	}
	trailers := []string{` {}`, ` []`, ` 1`, ` "x"`, ` garbage`}
	for _, tc := range modes {
		for _, trailer := range trailers {
			t.Run(tc.name+trailer, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "x.txt")
				matrixMustWrite(t, path, "old\n")
				snaps := hashline.NewMemSnapshotStore()
				h, _ := snaps.Record(path, "old\n")
				edit := NewEditTool(root, hashline.OSFilesystem{}, snaps)
				edit.Mode = tc.mode
				raw := strings.Replace(tc.valid, "HASH", h, 1) + trailer
				if _, err := edit.Execute(context.Background(), json.RawMessage(raw)); err == nil {
					t.Fatalf("accepted trailing %q", trailer)
				}
				got, _ := os.ReadFile(path)
				if string(got) != "old\n" {
					t.Fatalf("disk changed to %q", got)
				}
			})
		}
	}
	for _, tc := range modes {
		t.Run(tc.name+" whitespace", func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "x.txt")
			matrixMustWrite(t, path, "old\n")
			snaps := hashline.NewMemSnapshotStore()
			h, _ := snaps.Record(path, "old\n")
			edit := NewEditTool(root, hashline.OSFilesystem{}, snaps)
			edit.Mode = tc.mode
			raw := strings.Replace(tc.valid, "HASH", h, 1) + " \n\t"
			res, err := edit.Execute(context.Background(), json.RawMessage(raw))
			if err != nil || len(res.Files) != 1 || !res.Files[0].Committed {
				t.Fatalf("result=%+v err=%v", res, err)
			}
		})
	}
}
