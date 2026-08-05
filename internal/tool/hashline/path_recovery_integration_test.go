package hashline_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// Provenance: behavior ported from oh-my-pi packages/coding-agent/test/core/
// hashline.test.ts filename+tag recovery cases @ 5af71dc9cf132538e072806424f71f43f734d9ae.
func TestMissingBasenameTagRecoveryUniqueAndAmbiguous(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(map[bool]string{false: "unique", true: "ambiguous"}[ambiguous], func(t *testing.T) {
			root := t.TempDir()
			source := "a\nb\nc"
			snaps := hashline.NewMemSnapshotStore()
			paths := []string{filepath.Join(root, "one", "target.txt")}
			if ambiguous {
				paths = append(paths, filepath.Join(root, "two", "target.txt"))
			}
			for _, p := range paths {
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(source), 0o644); err != nil {
					t.Fatal(err)
				}
				h, _ := snaps.Record(p, source)
				snaps.RecordSeenLines(p, h, []int{2})
			}
			h := hashline.ComputeFileHash(source)
			et := tool.NewEditTool(root, hashline.OSFilesystem{}, snaps)
			input, _ := json.Marshal(map[string]string{"input": "[target.txt#" + h + "]\nPUT 2:\n+B"})
			res, err := et.Execute(context.Background(), input)
			if ambiguous {
				if err == nil || !strings.Contains(err.Error(), "no such file") {
					t.Fatalf("ambiguous err=%v", err)
				}
				for _, p := range paths {
					b, _ := os.ReadFile(p)
					if string(b) != source {
						t.Fatal("ambiguous recovery changed disk")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(paths[0])
			if string(b) != "a\nB\nc" || !strings.Contains(res.Output, "recovered unique workspace snapshot path") {
				t.Fatalf("disk=%q output=%q", b, res.Output)
			}
		})
	}
}

func TestMissingPathRecoveryConfinedAndInternalURLNotRedirected(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target.txt")
	source := "a\nb"
	if err := os.WriteFile(outside, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, authored := range []string{"target.txt", "local://target.txt"} {
		t.Run(authored, func(t *testing.T) {
			snaps := hashline.NewMemSnapshotStore()
			h, _ := snaps.Record(outside, source)
			snaps.RecordSeenLines(outside, h, []int{1})
			et := tool.NewEditTool(root, hashline.OSFilesystem{}, snaps)
			raw, _ := json.Marshal(map[string]string{"input": "[" + authored + "#" + h + "]\nPUT 1:\n+X"})
			if _, err := et.Execute(context.Background(), raw); err == nil {
				t.Fatal("out-of-workspace/internal URL recovery succeeded")
			}
			b, _ := os.ReadFile(outside)
			if string(b) != source {
				t.Fatal("rejection changed outside file")
			}
		})
	}
}
