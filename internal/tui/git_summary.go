package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type gitSummary struct {
	Files     int
	Additions int
	Deletions int
}

func toolMayModifyWorkspace(name string) bool {
	switch name {
	case "bash", "edit", "write":
		return true
	default:
		return false
	}
}

func gitBranch(root string) (string, error) {
	output, err := runWorkspaceGit(root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func summarizeGitWorkspace(root string) (gitSummary, error) {
	if _, err := runWorkspaceGit(root, "rev-parse", "--is-inside-work-tree"); err != nil {
		return gitSummary{}, err
	}

	status, err := runWorkspaceGit(root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return gitSummary{}, err
	}
	summary := gitSummary{Files: porcelainEntryCount(status)}
	if summary.Files == 0 {
		return summary, nil
	}

	_, headErr := runWorkspaceGit(root, "rev-parse", "--verify", "HEAD")
	if headErr == nil {
		numstat, err := runWorkspaceGit(root, "diff", "--numstat", "HEAD", "--")
		if err != nil {
			return gitSummary{}, err
		}
		summary.Additions, summary.Deletions = parseNumstat(numstat)
	}

	args := []string{"ls-files", "--others", "--exclude-standard", "-z"}
	if headErr != nil {
		args = []string{"ls-files", "--cached", "--others", "--exclude-standard", "-z"}
	}
	paths, err := runWorkspaceGit(root, args...)
	if err != nil {
		return gitSummary{}, err
	}
	additions, err := countTextFileLines(root, paths)
	if err != nil {
		return gitSummary{}, err
	}
	summary.Additions += additions
	return summary, nil
}

func runWorkspaceGit(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}

func porcelainEntryCount(output []byte) int {
	records := bytes.Split(output, []byte{0})
	count := 0
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) < 3 {
			continue
		}
		count++
		if record[0] == 'R' || record[0] == 'C' || record[1] == 'R' || record[1] == 'C' {
			index++
		}
	}
	return count
}

func parseNumstat(output []byte) (int, int) {
	additions := 0
	deletions := 0
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 || fields[0] == "-" || fields[1] == "-" {
			continue
		}
		added, addErr := strconv.Atoi(fields[0])
		deleted, deleteErr := strconv.Atoi(fields[1])
		if addErr == nil && deleteErr == nil {
			additions += added
			deletions += deleted
		}
	}
	return additions, deletions
}

func countTextFileLines(root string, output []byte) (int, error) {
	total := 0
	for _, rawPath := range bytes.Split(output, []byte{0}) {
		if len(rawPath) == 0 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, string(rawPath)))
		if err != nil {
			return 0, err
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		total += bytes.Count(data, []byte{'\n'})
		if len(data) > 0 && data[len(data)-1] != '\n' {
			total++
		}
	}
	return total, nil
}
