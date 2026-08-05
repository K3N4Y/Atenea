package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// Provenance: oh-my-pi@5af71dc9 edit/streaming.ts character chunking,
// incomplete JSON object, matcher digest, and no-preview-write contracts.
func TestEditPreviewReplaceCharacterChunksArePure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("old\ntail\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	edit.Mode = editmode.Replace
	raw := `{"path":"a.txt","old_string":"old","new_string":"new"}`
	var final string
	for i := 1; i <= len(raw); i++ {
		preview := edit.Preview(context.Background(), json.RawMessage(raw[:i]))
		if len(preview.Files) > 0 {
			final = preview.Files[0].NewText
		}
		if got, _ := os.ReadFile(path); string(got) != "old\ntail\n" {
			t.Fatalf("preview wrote disk at chunk %d: %q", i, got)
		}
	}
	if final != "new\ntail\n" {
		t.Fatalf("final preview = %q", final)
	}
	entries := edit.MatcherEntries(json.RawMessage(raw))
	if len(entries) != 1 || entries[0].Path != "a.txt" || entries[0].Digest != "new" {
		t.Fatalf("entries = %+v", entries)
	}
}

// Provenance: oh-my-pi@5af71dc9 streaming partial aggregate tests. The active
// trailing edit object is dropped while completed objects remain renderable.
func TestEditPreviewPatchDropsIncompleteTrailingObject(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	edit.Mode = editmode.Patch
	raw := `{"path":"a.txt","edits":[{"op":"update","diff":"@@\n-one\n+ONE"},{"op":"update","diff":"@@\n-two`
	preview := edit.Preview(context.Background(), json.RawMessage(raw))
	if len(preview.Files) != 1 || preview.Files[0].NewText != "ONE\ntwo\n" {
		t.Fatalf("preview = %+v", preview)
	}
	entries := edit.MatcherEntries(json.RawMessage(raw))
	if len(entries) != 1 || entries[0].Digest != "ONE" {
		t.Fatalf("entries = %+v", entries)
	}
}

// Provenance: hashline streaming clipboard purity through the current input field.
func TestEditPreviewHashlineForksClipboard(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	edit.Mode = editmode.Hashline
	raw := `{"input":"[a.txt#DEAD]\nCUT 1 @r\nPUT >$ @r"}`
	preview := edit.Preview(context.Background(), json.RawMessage(raw))
	if len(preview.Files) != 1 || preview.Files[0].NewText != "b\na" {
		t.Fatalf("preview = %+v", preview)
	}
	if _, exists := edit.Patcher.Clipboard.Named["r"]; exists {
		t.Fatal("preview leaked clipboard register")
	}
}

func TestEditPreviewRejectsExternalAliasesInEveryMode(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	secret := "ROUND7-EXTERNAL-SECRET"
	external := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(external, []byte(secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "alias.txt")
	if err := os.Symlink(external, symlink); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(root, "hard.txt")
	if err := os.Link(external, hardlink); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "linked")
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		mode editmode.Mode
		raw  string
	}{
		{"replace-symlink", editmode.Replace, `{"path":"alias.txt","old_string":"ROUND7","new_string":"changed"}`},
		{"patch-hardlink", editmode.Patch, `{"path":"hard.txt","edits":[{"op":"update","diff":"@@\n-ROUND7\n+changed"}]}`},
		{"apply-parent", editmode.ApplyPatch, `{"input":"*** Add File: linked/new.txt\n+created\n*** End Patch"}`},
		{"hashline-symlink", editmode.Hashline, `{"input":"[alias.txt#DEAD]\nPUT 1:\n+changed"}`},
		{"hashline-destination", editmode.Hashline, `{"input":"[normal.txt#DEAD]\nMOVE linked/new.txt"}`},
	}
	if err := os.WriteFile(filepath.Join(root, "normal.txt"), []byte("normal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
			edit.Mode = tc.mode
			before, _ := os.ReadFile(external)
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					preview := edit.Preview(context.Background(), json.RawMessage(tc.raw))
					encoded, _ := json.Marshal(preview)
					if strings.Contains(string(encoded), secret) {
						t.Errorf("preview exposed external content: %s", encoded)
					}
				}()
			}
			wg.Wait()
			after, _ := os.ReadFile(external)
			if string(after) != string(before) {
				t.Fatalf("preview mutated external file: %q", after)
			}
			if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
				t.Fatalf("preview created destination: %v", err)
			}
		})
	}
}

func TestMatcherEntriesAggregateNormalizedPathsInAuthoredOrder(t *testing.T) {
	edit := NewEditTool(t.TempDir(), hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	cases := []struct {
		name string
		mode editmode.Mode
		raw  string
		want []string
	}{
		{
			name: "hashline repeated interleaved sections",
			mode: editmode.Hashline,
			raw:  `{"input":"[src/./a.txt#AAAA]\nPUT 1:\n+one\n[sibling.txt#BBBB]\nPUT 1:\n+other\n[src/x/../a.txt#CCCC]\nPUT 1:\n+two\n"}`,
			want: []string{"src/a.txt\x00one\ntwo", "sibling.txt\x00other"},
		},
		{
			name: "apply patch repeated interleaved entries",
			mode: editmode.ApplyPatch,
			raw:  `{"input":"*** Begin Patch\n*** Update File: src/./a.txt\n@@\n-one\n+ONE\n*** Add File: sibling.txt\n+isolated\n*** Update File: src/x/../a.txt\n@@\n-two\n+TWO\n*** End Patch\n"}`,
			want: []string{"src/a.txt\x00ONE\nTWO", "sibling.txt\x00isolated\n"},
		},
		{
			name: "patch skips edits without introduced content",
			mode: editmode.Patch,
			raw:  `{"path":"src/./a.txt","edits":[{"op":"delete"},{"op":"update","diff":"-removed"}]}`,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edit.Mode = tc.mode
			got := edit.MatcherEntries(json.RawMessage(tc.raw))
			if len(got) != len(tc.want) {
				t.Fatalf("entries=%+v", got)
			}
			for i, want := range tc.want {
				if got[i].Path+"\x00"+got[i].Digest != want {
					t.Fatalf("entry %d=%q digest=%q want=%q", i, got[i].Path, got[i].Digest, want)
				}
			}
		})
	}
}

func TestMatcherDigestProjectsOnlyIntroducedContent(t *testing.T) {
	edit := NewEditTool(t.TempDir(), hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	tests := []struct {
		name string
		mode editmode.Mode
		raw  string
		want []string
	}{
		{"patch excludes grammar deleted and context", editmode.Patch, `{"path":"a","edits":[{"op":"update","diff":"@@ SECRET_HEADER\n-SECRET_DELETED\n SECRET_CONTEXT\n+visible"}]}`, []string{"a\x00visible"}},
		{"patch create is full content", editmode.Patch, `{"path":"a","edits":[{"op":"create","diff":"-literal minus\n+literal plus\nbody"}]}`, []string{"a\x00-literal minus\n+literal plus\nbody"}},
		{"adjacent entries separated", editmode.Patch, `{"path":"a","edits":[{"op":"update","diff":"+foo"},{"op":"update","diff":"+bar"}]}`, []string{"a\x00foo\nbar"}},
		{"hashline escaping and no operations", editmode.Hashline, `{"input":"[a#AAAA]\nPUT 1:\n++literal plus\n+-literal minus\nCUT 2\n-deleted\n[b#BBBB]\nPUT 1:\n+other"}`, []string{"a\x00+literal plus\n-literal minus", "b\x00other"}},
		{"apply sibling isolation", editmode.ApplyPatch, `{"input":"*** Begin Patch\n*** Update File: a\n@@ secret\n-deleted\n+alpha\n*** Update File: b\n@@\n-context\n+beta\n*** End Patch\n"}`, []string{"a\x00alpha", "b\x00beta"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			edit.Mode = tc.mode
			got := edit.MatcherEntries(json.RawMessage(tc.raw))
			if len(got) != len(tc.want) {
				t.Fatalf("entries=%+v want=%q", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i].Path+"\x00"+got[i].Digest != want {
					t.Fatalf("entry %d=%q digest=%q want=%q", i, got[i].Path, got[i].Digest, want)
				}
			}
		})
	}
}

// Provenance: oh-my-pi@5af71dc9 streaming matcher projections. Empty
// introduced-content projections are not matcher entries, except replace's
// explicit empty new_string, which intentionally represents deletion.
func TestMatcherEntriesDiscardEmptyIntroducedContentBeforeAggregation(t *testing.T) {
	edit := NewEditTool(t.TempDir(), hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	tests := []struct {
		name string
		mode editmode.Mode
		raw  string
		want []string
	}{
		{"patch delete-only", editmode.Patch, `{"path":"a","edits":[{"op":"delete"}]}`, nil},
		{"patch removal-only", editmode.Patch, `{"path":"a","edits":[{"op":"update","diff":"@@\n-old"}]}`, nil},
		{"apply patch header-only", editmode.ApplyPatch, `{"input":"*** Begin Patch\n*** Update File: a\n*** End Patch\n"}`, nil},
		{"apply patch delete-only", editmode.ApplyPatch, `{"input":"*** Begin Patch\n*** Delete File: a\n*** End Patch\n"}`, nil},
		{"hashline removal-only", editmode.Hashline, `{"input":"[a#AAAA]\nCUT 1"}`, nil},
		{"hashline header-only", editmode.Hashline, `{"input":"[a#AAAA]\n"}`, nil},
		{"empty then nonempty same path", editmode.ApplyPatch, `{"input":"*** Begin Patch\n*** Delete File: src/./a\n*** Update File: sibling\n@@\n-x\n+isolated\n*** Update File: src/x/../a\n@@\n-old\n+new\n*** End Patch\n"}`, []string{"sibling\x00isolated", "src/a\x00new"}},
		{"all empty and siblings isolated", editmode.Hashline, `{"input":"[a#AAAA]\nREM\n[b#BBBB]\nCUT 1\n[c#CCCC]\n"}`, nil},
		{"repeated nonempty exactly one newline", editmode.Hashline, `{"input":"[a#AAAA]\nPUT 1:\n+one\n[b#BBBB]\nPUT 1:\n+sibling\n[a#CCCC]\nPUT 2:\n+two"}`, []string{"a\x00one\ntwo", "b\x00sibling"}},
		{"replace explicit empty string", editmode.Replace, `{"path":"a","old_string":"old","new_string":""}`, []string{"a\x00"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			edit.Mode = tc.mode
			got := edit.MatcherEntries(json.RawMessage(tc.raw))
			if len(got) != len(tc.want) {
				t.Fatalf("entries=%+v want=%q", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i].Path+"\x00"+got[i].Digest != want {
					t.Fatalf("entry %d=%q digest=%q want=%q", i, got[i].Path, got[i].Digest, want)
				}
			}
		})
	}
}

func TestHashlineMatcherIgnoresRegisterContentAcrossCallsAndPaths(t *testing.T) {
	edit := NewEditTool(t.TempDir(), hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	edit.Mode = editmode.Hashline
	edit.Patcher.Clipboard.Named["saved"] = []string{"secret", "register body"}
	edit.Patcher.Clipboard.Anonymous = []string{"anonymous secret"}

	tests := []struct {
		name, input string
		want        []string
	}{
		{"named only", `{"input":"[a#AAAA]\nPUT 1 @saved"}`, nil},
		{"anonymous only", `{"input":"[a#AAAA]\nPUT 1"}`, nil},
		{"cut and put only", `{"input":"[a#AAAA]\nCUT 1 @saved\nPUT 2 @saved"}`, nil},
		{"register plus literal interleaved", `{"input":"[a#AAAA]\nPUT 1 @saved\nPUT 2:\n+literal a\n[b#BBBB]\nPUT 1 @saved\nPUT 2:\n+literal b\n[a#CCCC]\nPUT 3:\n+literal c"}`, []string{"a\x00literal a\nliteral c", "b\x00literal b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := edit.MatcherEntries(json.RawMessage(tc.input))
			if len(got) != len(tc.want) {
				t.Fatalf("entries=%+v want=%q", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i].Path+"\x00"+got[i].Digest != want {
					t.Fatalf("entry %d=%+v want=%q", i, got[i], want)
				}
			}
		})
	}
}

func TestApplyPatchPreviewUsesOrderedVirtualPostState(t *testing.T) {
	cases := []struct {
		name, initialPath, initial, patch, finalPath, final string
	}{
		{"move then update destination", "a.txt", "one\n", "*** Begin Patch\n*** Update File: a.txt\n*** Move to: nested/b.txt\n@@\n-one\n+two\n*** Update File: nested/b.txt\n@@\n-two\n+three\n*** End Patch\n", "nested/b.txt", "three\n"},
		{"create then update", "", "", "*** Begin Patch\n*** Add File: a.txt\n+one\n*** Update File: a.txt\n@@\n-one\n+two\n*** End Patch\n", "a.txt", "two\n"},
		{"delete then add", "a.txt", "old\n", "*** Begin Patch\n*** Delete File: a.txt\n*** Add File: a.txt\n+new\n*** End Patch\n", "a.txt", "new\n"},
		{"same path updates", "a.txt", "one\ntwo\n", "*** Begin Patch\n*** Update File: a.txt\n@@\n-one\n+ONE\n*** Update File: a.txt\n@@\n-two\n+TWO\n*** End Patch\n", "a.txt", "ONE\nTWO\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.initialPath != "" {
				if err := os.WriteFile(filepath.Join(root, tc.initialPath), []byte(tc.initial), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
			edit.Mode = editmode.ApplyPatch
			raw, _ := json.Marshal(map[string]string{"input": tc.patch})
			preview := edit.Preview(context.Background(), raw)
			if preview.Error != "" || len(preview.Files) == 0 {
				t.Fatalf("preview=%+v", preview)
			}
			if tc.initialPath == "" {
				if _, err := os.Stat(filepath.Join(root, tc.finalPath)); !os.IsNotExist(err) {
					t.Fatalf("preview created file: %v", err)
				}
			} else if got, err := os.ReadFile(filepath.Join(root, tc.initialPath)); err != nil || string(got) != tc.initial {
				t.Fatalf("preview mutated disk: %q %v", got, err)
			}
			last := preview.Files[len(preview.Files)-1]
			if last.NewText != tc.final {
				t.Fatalf("preview final=%q files=%+v", last.NewText, preview.Files)
			}
			if _, err := edit.Execute(context.Background(), raw); err != nil {
				t.Fatalf("execute: %v", err)
			}
			landed, err := os.ReadFile(filepath.Join(root, tc.finalPath))
			if err != nil || string(landed) != last.NewText {
				t.Fatalf("landed=%q err=%v preview=%q", landed, err, last.NewText)
			}
		})
	}
}
