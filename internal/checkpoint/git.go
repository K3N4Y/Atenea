package checkpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Tree string

type Store interface {
	Capture(ctx context.Context, workspace string) (Tree, error)
	Restore(ctx context.Context, workspace string, tree Tree) error
}

type GitStore struct {
	root string
	mu   sync.Mutex
}

func NewGitStore(root string) *GitStore { return &GitStore{root: root} }

func (s *GitStore) Capture(ctx context.Context, workspace string) (Tree, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capture(ctx, workspace)
}

func (s *GitStore) Restore(ctx context.Context, workspace string, tree Tree) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	abs, private, err := s.prepare(ctx, workspace)
	if err != nil {
		return err
	}
	current, err := s.capturePrepared(ctx, abs, private)
	if err != nil {
		return err
	}
	currentPaths, err := treePaths(ctx, private, abs, current)
	if err != nil {
		return err
	}
	targetPaths, err := treePaths(ctx, private, abs, tree)
	if err != nil {
		return err
	}
	for name := range currentPaths {
		if _, ok := targetPaths[name]; ok {
			continue
		}
		if err := os.RemoveAll(filepath.Join(abs, filepath.FromSlash(name))); err != nil {
			return err
		}
	}
	removeEmptyDirs(abs)
	if _, err := privateGit(ctx, private, abs, nil, "read-tree", string(tree)); err != nil {
		return err
	}
	_, err = privateGit(ctx, private, abs, nil, "checkout-index", "-a", "-f")
	return err
}

func (s *GitStore) capture(ctx context.Context, workspace string) (Tree, error) {
	abs, private, err := s.prepare(ctx, workspace)
	if err != nil {
		return "", err
	}
	return s.capturePrepared(ctx, abs, private)
}

func (s *GitStore) capturePrepared(ctx context.Context, workspace, private string) (Tree, error) {
	paths, err := nonIgnoredPaths(ctx, workspace)
	if err != nil {
		return "", err
	}
	if _, err := privateGit(ctx, private, workspace, nil, "read-tree", "--empty"); err != nil {
		return "", err
	}
	if len(paths) > 0 {
		stdin := []byte(strings.Join(paths, "\x00") + "\x00")
		if _, err := privateGit(ctx, private, workspace, stdin, "add", "-A", "-f", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return "", err
		}
	}
	out, err := privateGit(ctx, private, workspace, nil, "write-tree")
	if err != nil {
		return "", err
	}
	return Tree(strings.TrimSpace(string(out))), nil
}

func (s *GitStore) prepare(ctx context.Context, workspace string) (string, string, error) {
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", "", err
	}
	abs = filepath.Clean(abs)
	cmd := exec.CommandContext(ctx, "git", "-C", abs, "rev-parse", "--is-inside-work-tree")
	if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		return "", "", errors.New("checkpoint requires a Git workspace")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(abs)))
	private := filepath.Join(s.root, digest)
	if _, err := os.Stat(private); os.IsNotExist(err) {
		if err := os.MkdirAll(s.root, 0o755); err != nil {
			return "", "", err
		}
		cmd := exec.CommandContext(ctx, "git", "init", "--bare", private)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("git init checkpoint: %w: %s", err, out)
		}
	} else if err != nil {
		return "", "", err
	}
	return abs, private, nil
}

func nonIgnoredPaths(ctx context.Context, workspace string) ([]string, error) {
	tracked, err := trackedPaths(ctx, workspace)
	if err != nil {
		return nil, err
	}
	var paths []string
	err = filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == workspace {
			return nil
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil {
			return err
		}
		if rel == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	candidates := make([]string, 0, len(paths))
	for _, name := range paths {
		if _, ok := tracked[name]; !ok {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		sort.Strings(paths)
		return paths, nil
	}
	stdin := []byte(strings.Join(candidates, "\x00") + "\x00")
	cmd := exec.CommandContext(ctx, "git", "-C", workspace, "-c", "core.quotepath=false", "check-ignore", "--no-index", "--stdin", "-z")
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, err
		}
	}
	ignored := map[string]struct{}{}
	for _, name := range bytes.Split(out, []byte{0}) {
		if len(name) > 0 {
			ignored[string(name)] = struct{}{}
		}
	}
	filtered := paths[:0]
	for _, name := range paths {
		if _, ok := ignored[name]; !ok {
			filtered = append(filtered, name)
		}
	}
	sort.Strings(filtered)
	return filtered, nil
}

func trackedPaths(ctx context.Context, workspace string) (map[string]struct{}, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", workspace, "-c", "core.quotepath=false", "ls-files", "-z")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	tracked := make(map[string]struct{})
	for _, name := range bytes.Split(out, []byte{0}) {
		if len(name) > 0 {
			tracked[string(name)] = struct{}{}
		}
	}
	return tracked, nil
}

func treePaths(ctx context.Context, private, workspace string, tree Tree) (map[string]struct{}, error) {
	out, err := privateGit(ctx, private, workspace, nil, "ls-tree", "-r", "-z", "--name-only", string(tree))
	if err != nil {
		return nil, err
	}
	paths := map[string]struct{}{}
	for _, name := range bytes.Split(out, []byte{0}) {
		if len(name) > 0 {
			paths[string(name)] = struct{}{}
		}
	}
	return paths, nil
}

func privateGit(ctx context.Context, private, workspace string, stdin []byte, args ...string) ([]byte, error) {
	base := []string{
		"-c", "core.autocrlf=false",
		"-c", "core.symlinks=true",
		"-c", "core.quotepath=false",
		"-c", "core.bare=false",
		"--git-dir", private,
		"--work-tree", workspace,
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git checkpoint %s: %w: %s", args[0], err, out)
	}
	return out, nil
}

func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, dir := range dirs {
		_ = os.Remove(dir)
	}
}
