package wiring

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/tool"
)

func TestWorktreeResolverCreatesRetainedIsolatedWorkspace(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "base.txt")
	runGit(t, root, "commit", "-m", "base")

	env, err := worktreeResolver(root, 0, nil)(context.Background(), agent.Def{Name: "coder"})
	if err != nil {
		t.Fatal(err)
	}
	if env.Workspace == "" || env.Registry == nil {
		t.Fatalf("environment = %#v", env)
	}
	settle := env.Registry.Materialize(tool.Permissions{"write": true}).Settle
	_, err = settle(context.Background(), tool.Call{ID: "w", Name: "write", Input: json.RawMessage(`{"path":"isolated.txt","content":"only here\n"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, "isolated.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "isolated.txt")); !os.IsNotExist(err) {
		t.Fatalf("parent workspace was modified: %v", err)
	}
	if _, err := os.Stat(env.Workspace); err != nil {
		t.Fatalf("worktree was not retained: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "-C", root, "worktree", "remove", "--force", env.Workspace)
		_ = cmd.Run()
	})
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
