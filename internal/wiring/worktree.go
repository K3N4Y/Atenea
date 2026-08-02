package wiring

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/session/subagent"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

func worktreeResolver(root string, outputLimit int) subagent.EnvironmentResolver {
	return func(ctx context.Context, _ agent.Def) (subagent.ChildEnvironment, error) {
		path, err := os.MkdirTemp("", "atenea-worktree-")
		if err != nil {
			return subagent.ChildEnvironment{}, err
		}
		if err := os.Remove(path); err != nil {
			return subagent.ChildEnvironment{}, err
		}
		cmd := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "--detach", path, "HEAD")
		if output, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(path)
			return subagent.ChildEnvironment{}, fmt.Errorf("git worktree add: %w: %s", err, output)
		}
		discard := func() error {
			remove := exec.Command("git", "-C", root, "worktree", "remove", "--force", path)
			output, removeErr := remove.CombinedOutput()
			filesystemErr := os.RemoveAll(path)
			if removeErr != nil {
				removeErr = fmt.Errorf("git worktree remove: %w: %s", removeErr, output)
			}
			return errors.Join(removeErr, filesystemErr)
		}
		snapshots := tool.NewSessionSnapshots()
		registry := tool.NewRegistry(tool.NewOutputStore(outputLimit),
			tool.NewReadToolWithSnapshotProvider(path, snapshots),
			tool.NewWriteToolWithSnapshotProvider(path, snapshots),
			tool.NewEditToolWithSnapshotProvider(path, hashline.OSFilesystem{}, snapshots),
			tool.NewGlobTool(path), tool.NewGrepToolWithSnapshotProvider(path, snapshots),
			tool.NewBashTool(path),
		)
		if registry == nil {
			_ = discard()
			return subagent.ChildEnvironment{}, fmt.Errorf("create tools for worktree %s", filepath.Base(path))
		}
		return subagent.ChildEnvironment{
			Store: session.NewMemoryStore(), Inbox: session.NewMemoryInbox(),
			Registry: registry, Workspace: path, Discard: discard,
		}, nil
	}
}
