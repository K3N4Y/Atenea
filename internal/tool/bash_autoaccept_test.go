package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

func TestBashTool_AutoAcceptSafe(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	bt := NewBashTool(root)
	tests := []struct {
		command string
		want    bool
	}{
		{"mkdir nested", true}, {"mkdir -p nested/dir", true}, {"touch new.txt", true},
		{"cp a.txt b.txt", true}, {"mv a.txt b.txt", true}, {"rm a.txt", true},
		{"rm -f a.txt", true}, {"rmdir empty", true}, {"sed 's/a/b/' a.txt", true}, {"sed -i 's/a/b/g' a.txt", true},
		{"rm -rf .", false}, {"touch ../escape", false}, {"touch /tmp/escape", false},
		{"rm -fr a.txt", false}, {"rm --recursive a.txt", false}, {"rm --force a.txt", false},
		{"touch -r a.txt b.txt", false}, {"touch ~/escaped", false}, {"touch *.txt", false},
		{"touch file?.txt", false}, {"touch file[0]", false}, {"touch file{1,2}", false},
		{"touch file # comment", false}, {"touch escaped\\ name", false},
		{"sh -c 'touch x'", false}, {"touch x && curl bad", false}, {"sed -e 'e id' a.txt", false},
		{"sed 's/a/$HOME/' a.txt", false}, {"sed 's/[a]/b/' a.txt", false}, {"sed 's/a/b/' *.txt", false},
	}
	for _, tt := range tests {
		call := Call{Name: "bash", Input: json.RawMessage(`{"command":` + string(mustJSON(t, tt.command)) + `}`)}
		if got := bt.AutoAcceptSafe(call); got != tt.want {
			t.Errorf("%q = %v, want %v", tt.command, got, tt.want)
		}
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBashTool_AutoAcceptSafeRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	call := Call{Name: "bash", Input: json.RawMessage(`{"command":"touch link/escape"}`)}
	if NewBashTool(root).AutoAcceptSafe(call) {
		t.Fatal("symlink traversal accepted")
	}
}

func TestAutoAcceptRejectsHardLinkedMutableTargets(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim.txt")
	if err := os.WriteFile(victim, []byte("outside must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.Link(victim, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	bt := NewBashTool(root)
	for _, command := range []string{"touch victim.txt", "cp source.txt victim.txt", "sed -i 's/outside/changed/' victim.txt"} {
		call := Call{Name: "bash", Input: json.RawMessage(`{"command":` + string(mustJSON(t, command)) + `}`)}
		if bt.AutoAcceptSafe(call) {
			t.Errorf("hard-linked target auto-accepted: %s", command)
		}
	}
	edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	call := Call{Name: "edit", Input: json.RawMessage(`{"patch":"[victim.txt#XXXX]\\nSWAP 1.=1:\\n+changed"}`)}
	if edit.AutoAcceptSafe(call) {
		t.Error("edit auto-accepted a hard-linked inode")
	}
	got, err := os.ReadFile(alias)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside must survive" {
		t.Fatalf("outside alias changed to %q", got)
	}
}
