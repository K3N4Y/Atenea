// Package workspacegit owns Git operations for an Atenea workspace.
package workspacegit

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Change describes a path and its short porcelain status.
type Change struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// Status separates index, worktree, and untracked changes.
type Status struct {
	IsRepo    bool     `json:"isRepo"`
	Staged    []Change `json:"staged"`
	Unstaged  []Change `json:"unstaged"`
	Untracked []Change `json:"untracked"`
}

// Repository provides Git operations rooted at one workspace.
type Repository struct {
	root string
}

func Open(root string) *Repository {
	return &Repository{root: root}
}

func (r *Repository) Status() (Status, error) {
	out, err := r.run("status", "--porcelain")
	if err != nil {
		if !r.IsRepo() {
			return Status{IsRepo: false}, nil
		}
		return Status{}, err
	}

	status := Status{IsRepo: true}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		x, y := line[0], line[1]
		path := strings.TrimSpace(line[3:])
		if x == '?' && y == '?' {
			status.Untracked = append(status.Untracked, Change{Path: path, Status: "??"})
			continue
		}
		if x != ' ' {
			status.Staged = append(status.Staged, Change{Path: path, Status: string(x)})
		}
		if y != ' ' {
			status.Unstaged = append(status.Unstaged, Change{Path: path, Status: string(y)})
		}
	}
	return status, nil
}

func (r *Repository) IsRepo() bool {
	_, err := r.run("rev-parse", "--is-inside-work-tree")
	return err == nil
}

func (r *Repository) FileDiff(path string) (string, error) {
	if out, err := r.run("diff", "--cached", "--", path); err == nil && strings.TrimSpace(out) != "" {
		return out, nil
	}
	if out, err := r.run("diff", "--", path); err == nil && strings.TrimSpace(out) != "" {
		return out, nil
	}
	return r.newFileDiff(path)
}

func (r *Repository) Init() error {
	_, err := r.run("init")
	return err
}

func (r *Repository) Commit(message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("git commit: el mensaje no puede estar vacio")
	}
	_, err := r.run("commit", "-m", message)
	return err
}

func (r *Repository) StagedDiff() (string, error) {
	return r.run("diff", "--cached")
}

func (r *Repository) newFileDiff(path string) (string, error) {
	data, err := os.ReadFile(filepath.Join(r.root, path))
	if err != nil {
		return "", err
	}
	content := string(data)
	var lines []string
	if content != "" {
		lines = strings.Split(content, "\n")
		if strings.HasSuffix(content, "\n") {
			lines = lines[:len(lines)-1]
		}
	}
	var b strings.Builder
	b.WriteString("--- /dev/null\n")
	b.WriteString("+++ b/" + path + "\n")
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, line := range lines {
		b.WriteString("+" + line + "\n")
	}
	return b.String(), nil
}

func (r *Repository) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return stdout.String(), nil
}
