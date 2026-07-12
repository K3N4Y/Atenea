package checkpoint

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGitStore_CaptureAndRestoreNonIgnoredWorkspace(t *testing.T) {
	root := newGitWorkspace(t)
	writeFile(t, root, ".gitignore", "ignored.txt\n")
	writeFile(t, root, "tracked.txt", "committed\n")
	writeFile(t, root, "deleted.txt", "delete me\n")
	runGit(t, root, "add", ".gitignore", "tracked.txt", "deleted.txt")
	runGit(t, root, "commit", "-m", "base")

	writeFile(t, root, "tracked.txt", "captured tracked\n")
	writeFile(t, root, "notes.txt", "captured untracked\n")
	writeFile(t, root, "ignored.txt", "captured ignored\n")
	writeFile(t, root, "script.sh", "#!/bin/sh\necho captured\n")
	if err := os.Chmod(filepath.Join(root, "script.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink("tracked.txt", filepath.Join(root, "tracked-link")); err != nil {
			t.Fatal(err)
		}
	}

	store := NewGitStore(t.TempDir())
	tree, err := store.Capture(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, root, "tracked.txt", "later tracked\n")
	writeFile(t, root, "notes.txt", "later notes\n")
	writeFile(t, root, "ignored.txt", "later ignored\n")
	writeFile(t, root, "created.txt", "remove me\n")
	if err := os.Remove(filepath.Join(root, "script.sh")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Remove(filepath.Join(root, "tracked-link")); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.Restore(context.Background(), root, tree); err != nil {
		t.Fatal(err)
	}
	assertFile(t, root, "tracked.txt", "captured tracked\n")
	assertFile(t, root, "notes.txt", "captured untracked\n")
	assertFile(t, root, "ignored.txt", "later ignored\n")
	assertFile(t, root, "script.sh", "#!/bin/sh\necho captured\n")
	if info, err := os.Stat(filepath.Join(root, "script.sh")); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("script mode = %v, err = %v", info, err)
	}
	assertMissing(t, root, "deleted.txt")
	assertMissing(t, root, "created.txt")
	if runtime.GOOS != "windows" {
		if target, err := os.Readlink(filepath.Join(root, "tracked-link")); err != nil || target != "tracked.txt" {
			t.Fatalf("symlink = %q, err = %v", target, err)
		}
	}
}

func TestGitStore_RestorePreservesMainGitMetadata(t *testing.T) {
	root := newGitWorkspace(t)
	writeFile(t, root, "a.txt", "base\n")
	runGit(t, root, "add", "a.txt")
	runGit(t, root, "commit", "-m", "base")
	runGit(t, root, "branch", "local-ref")
	writeFile(t, root, "binary.bin", string([]byte{0, 1, 2, 3}))
	runGit(t, root, "add", "binary.bin")
	writeFile(t, root, "a.txt", "captured\n")

	beforeBranch := gitOutput(t, root, "branch", "--show-current")
	beforeHead := gitOutput(t, root, "rev-parse", "HEAD")
	beforeRefs := gitOutput(t, root, "show-ref")
	beforeStaged := gitOutput(t, root, "diff", "--cached", "--binary")
	beforeStatus := gitOutput(t, root, "status", "--porcelain=v1")

	store := NewGitStore(t.TempDir())
	tree, err := store.Capture(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "a.txt", "later\n")
	writeFile(t, root, "new.txt", "later\n")
	if err := store.Restore(context.Background(), root, tree); err != nil {
		t.Fatal(err)
	}

	for name, pair := range map[string][2]string{
		"branch":      {beforeBranch, gitOutput(t, root, "branch", "--show-current")},
		"HEAD":        {beforeHead, gitOutput(t, root, "rev-parse", "HEAD")},
		"refs":        {beforeRefs, gitOutput(t, root, "show-ref")},
		"staged diff": {beforeStaged, gitOutput(t, root, "diff", "--cached", "--binary")},
		"status":      {beforeStatus, gitOutput(t, root, "status", "--porcelain=v1")},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("%s changed\nbefore: %q\nafter:  %q", name, pair[0], pair[1])
		}
	}
}

func newGitWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.name", "Atenea Test")
	runGit(t, root, "config", "user.email", "atenea@example.test")
	return root
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	_ = gitOutput(t, root, args...)
}

func gitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, root, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}

func assertMissing(t *testing.T, root, name string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
		t.Fatalf("%s exists or lstat failed: %v", name, err)
	}
}
