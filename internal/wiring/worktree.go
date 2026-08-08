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
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// workspaceTools assembles the file and shell tools that every agent receives,
// whether it runs in the main workspace, as a child, or in an isolated
// worktree. Keeping this policy here makes the shared snapshot state and
// per-turn edit configuration invariants local to one module.
func workspaceTools(root string, snapshots *tool.SessionSnapshots, settings func(model, sessionID string) (editmode.Config, error), batchEnv func(context.Context) []string) []tool.Tool {
	edit := tool.NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, snapshots)
	edit.TurnConfig = settings
	return []tool.Tool{
		tool.NewReadToolWithSnapshotProvider(root, snapshots),
		tool.NewWriteToolWithSnapshotProvider(root, snapshots),
		edit,
		tool.NewGlobTool(root),
		tool.NewGrepToolWithSnapshotProvider(root, snapshots),
		newBashTool(root, batchEnv),
	}
}

func newBashTool(root string, batchEnv func(context.Context) []string) *tool.BashTool {
	bash := tool.NewBashTool(root)
	bash.ExtraEnv = batchEnv
	return bash
}

type childRegistryKey struct{}

func withChildRegistry(ctx context.Context, registry *tool.Registry) context.Context {
	return context.WithValue(ctx, childRegistryKey{}, registry)
}

func childRegistryFrom(ctx context.Context) *tool.Registry {
	registry, _ := ctx.Value(childRegistryKey{}).(*tool.Registry)
	return registry
}

func worktreeResolver(root string, outputLimit int, settings func(model, sessionID string) (editmode.Config, error), task tool.Tool, batchEnv func(context.Context) []string) subagent.EnvironmentResolver {
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
		tools := workspaceTools(path, snapshots, settings, batchEnv)
		if task != nil {
			tools = append(tools, task)
		}
		registry := tool.NewRegistry(tool.NewOutputStore(outputLimit), tools...)
		if registry == nil {
			_ = discard()
			return subagent.ChildEnvironment{}, fmt.Errorf("create tools for worktree %s", filepath.Base(path))
		}
		return subagent.ChildEnvironment{
			Store: session.NewMemoryStore(), Inbox: session.NewMemoryInbox(),
			Registry: registry, Workspace: path, Discard: discard,
			Context: func(ctx context.Context) context.Context {
				return withChildRegistry(ctx, registry)
			},
		}, nil
	}
}
