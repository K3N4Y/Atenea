package tui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"atenea/internal/session"
)

func TestRefreshWorkspace_SummarizesTrackedAndUntrackedChanges(t *testing.T) {
	root := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	runGit("init", "-b", "main")
	write("tracked.txt", "one\ntwo\nthree\n")
	runGit("add", "tracked.txt")
	runGit("-c", "user.name=Atenea Test", "-c", "user.email=atenea@example.test", "commit", "-m", "initial")

	write("tracked.txt", "one changed\ntwo\nthree\nfour\n")
	runGit("add", "tracked.txt")
	write("tracked.txt", "zero\none changed\ntwo\nthree\nfour\nfive\n")
	write("new.txt", "alpha\nbeta\n")

	msg := refreshWorkspace(root, 0)().(workspaceRefreshedMsg)
	if msg.summary != (gitSummary{Files: 2, Additions: 6, Deletions: 1}) {
		t.Fatalf("summary = %+v, want 2 files, +6, -1", msg.summary)
	}
}

func TestRefreshWorkspace_CountsUnbornTextFilesButNotBinaryLines(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("one\ntwo"), 0o600); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "image.bin"), []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatalf("write image.bin: %v", err)
	}

	msg := refreshWorkspace(root, 0)().(workspaceRefreshedMsg)
	if msg.summary != (gitSummary{Files: 2, Additions: 2}) {
		t.Fatalf("summary = %+v, want 2 files, +2, -0", msg.summary)
	}
}

func TestModel_ViewShowsGitSummaryBelowComposerAlignedRight(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("", "gpt-test")
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 16})
	m = apply(t, m, workspaceRefreshedMsg{summary: gitSummary{Files: 2, Additions: 4, Deletions: 1}})

	lines := strings.Split(ansi.Strip(m.View()), "\n")
	summaryLine := ""
	for _, line := range lines {
		if strings.Contains(line, "2 files changed") {
			summaryLine = line
			break
		}
	}
	if summaryLine == "" {
		t.Fatalf("View() = %q, want git summary below composer", ansi.Strip(m.View()))
	}
	if !strings.HasSuffix(summaryLine, "2 files changed  +4  −1  ") {
		t.Fatalf("summary line = %q, want right margin aligned with composer", summaryLine)
	}
	if ansi.StringWidth(summaryLine) != 60 {
		t.Fatalf("summary line width = %d, want terminal width 60", ansi.StringWidth(summaryLine))
	}
	if lines[len(lines)-1] != strings.Repeat(" ", 60) {
		t.Fatalf("last line = %q, want one blank bottom-margin row", lines[len(lines)-1])
	}
}

func TestModel_ViewAdaptsGitSummaryToAvailableWidth(t *testing.T) {
	tests := []struct {
		name  string
		width int
		want  string
	}{
		{name: "full singular", width: 40, want: "1 file changed  +12  −3"},
		{name: "compact", width: 24, want: "1 file  +12  −3"},
		{name: "stats only", width: 16, want: "+12  −3"},
		{name: "hidden", width: 10, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel(nil, "s1", nil).WithStatus("", "model")
			m = apply(t, m, tea.WindowSizeMsg{Width: tt.width, Height: 12})
			m = apply(t, m, workspaceRefreshedMsg{summary: gitSummary{Files: 1, Additions: 12, Deletions: 3}})
			view := ansi.Strip(m.View())
			if tt.want == "" {
				if strings.Contains(view, "+12") {
					t.Fatalf("View() = %q, want summary hidden", view)
				}
				return
			}
			if !strings.Contains(view, tt.want) {
				t.Fatalf("View() = %q, want %q", view, tt.want)
			}
		})
	}
}

func TestModel_ViewHidesEmptyGitSummary(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	m = apply(t, m, workspaceRefreshedMsg{})
	if view := ansi.Strip(m.View()); strings.Contains(view, "files changed") || strings.Contains(view, "+0") {
		t.Fatalf("View() = %q, want no git summary for clean workspace", view)
	}
}

func TestModel_IgnoresStaleWorkspaceSummaryRefresh(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m = apply(t, m, workspaceRefreshedMsg{
		generation: 2,
		summary:    gitSummary{Files: 2, Additions: 20},
	})
	m = apply(t, m, workspaceRefreshedMsg{
		generation: 1,
		summary:    gitSummary{Files: 1, Additions: 10},
	})
	if m.gitSummary != (gitSummary{Files: 2, Additions: 20}) {
		t.Fatalf("git summary = %+v, want newest generation retained", m.gitSummary)
	}
}

func TestModel_ViewAlignsGitSummaryWithComposerWhenExplorerIsOpen(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithStatus("", "model")
	m.treeOpen = true
	m.treeLoaded = true
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 16})
	m = apply(t, m, workspaceRefreshedMsg{summary: gitSummary{Files: 2, Additions: 4, Deletions: 1}})

	for _, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if !strings.Contains(line, "2 files changed") {
			continue
		}
		// Sin caja el panel de chat ya no tiene borde derecho: la linea del
		// resumen termina justo tras las estadisticas y el margen del composer
		// (composerOuterMargin celdas), que es la columna derecha del chat.
		statEnd := strings.Index(line, "−1") + len("−1")
		if gap := line[statEnd:]; gap != strings.Repeat(" ", composerOuterMargin) {
			t.Fatalf("summary right gap = %q, want composer margin of %d cells with no panel border", gap, composerOuterMargin)
		}
		return
	}
	t.Fatalf("View() = %q, want git summary in split chat panel", ansi.Strip(m.View()))
}

func TestModel_ToolMutationRequestsWorkspaceSummaryRefresh(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}

	m := NewModel(nil, "s1", nil).WithWorkspaceRoot("main", root, root)
	_, refreshCmd := m.Update(EventMsg{
		Kind:     session.KindToolSuccess,
		ToolName: "edit",
		CallID:   "c1",
		Input:    json.RawMessage(`{"path":"main.go"}`),
		Diff:     "diff",
	})
	if refreshCmd == nil {
		t.Fatal("Update(Tool.Success edit) returned nil command, want workspace refresh")
	}
	if !commandProducesWorkspaceRefresh(refreshCmd) {
		t.Fatal("Update(Tool.Success edit) did not produce workspaceRefreshedMsg")
	}
}

func commandProducesWorkspaceRefresh(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	if _, ok := msg.(workspaceRefreshedMsg); ok {
		return true
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return false
	}
	for _, child := range batch {
		if commandProducesWorkspaceRefresh(child) {
			return true
		}
	}
	return false
}
