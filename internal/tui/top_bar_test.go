package tui

import (
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/session"
)

func TestModel_TopBarRefreshesBranchAfterSuccessfulTool(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	git("init", "-b", "main")
	git("-c", "user.name=Atenea Test", "-c", "user.email=atenea@example.test", "commit", "--allow-empty", "-m", "initial")

	m := NewModel(&fakeAgent{}, "s1", nil).WithWorkspaceRoot("main", root, root)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	git("checkout", "-b", "feature/live-branch")

	updated, cmd := m.update(EventMsg{Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash"})
	m = updated.(Model)
	if cmd != nil {
		m = apply(t, m, cmd())
	}

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "feature/live-branch") {
		t.Fatalf("the top bar must refresh the branch after Tool.Success; View() = %q", view)
	}
	if strings.Contains(view, branchGlyph+" main") {
		t.Fatalf("the top bar retains the initial branch after checkout; View() = %q", view)

	}
	git("checkout", "-b", "fix/second-refresh")
	updated, cmd = m.update(EventMsg{Kind: session.KindToolSuccess, CallID: "c2", ToolName: "bash"})
	m = updated.(Model)
	if cmd != nil {
		m = apply(t, m, cmd())
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "fix/second-refresh") {
		t.Fatalf("the branch must refresh after every successful bash; View() = %q", view)
	}
}

func TestModel_TopBarRefreshesBranchFromHomeAbbreviatedWorkspace(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	git("init", "-b", "main")
	git("-c", "user.name=Atenea Test", "-c", "user.email=atenea@example.test", "commit", "--allow-empty", "-m", "initial")

	m := NewModel(&fakeAgent{}, "s1", nil).WithWorkspaceRoot("main", "~/workspace", root)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	git("checkout", "-b", "feature/home-path")
	updated, cmd := m.update(EventMsg{Kind: session.KindToolSuccess, CallID: "c1", ToolName: "bash"})
	m = updated.(Model)
	if cmd != nil {
		m = apply(t, m, cmd())
	}

	if view := ansi.Strip(m.View()); !strings.Contains(view, "feature/home-path") {
		t.Fatalf("the branch must refresh when the workspace uses ~; View() = %q", view)
	}
}

// TestModel_TopBarShowsBranchDirectoryAndContextUsage verifies that, once the
// model is ready, the first line of View() is the top bar with the git branch,
// working directory, and context usage (used / window).
func TestModel_TopBarShowsBranchDirectoryAndContextUsage(t *testing.T) {
	m := NewModel(declaringAgent("anthropic/claude-opus-4.8", 200_000), "s1", nil).
		WithWorkspace("main", "~/dev/atenea").
		WithStatus("build", "anthropic/claude-opus-4.8")

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{InputTokens: 16000}})

	first := lineWith(t, ansi.Strip(m.View()), "main")

	if !strings.Contains(first, "main") {
		t.Fatalf("the top bar must show branch %q; first line = %q", "main", first)
	}
	if !strings.Contains(first, "~/dev/atenea") {
		t.Fatalf("the top bar must show directory %q; first line = %q", "~/dev/atenea", first)
	}
	if !strings.Contains(first, "16k / 200k") {
		t.Fatalf("the top bar must show context usage %q; first line = %q", "16k / 200k", first)
	}
}

func TestModel_TopBarTreatsBranchAsMetadataNotSuccess(t *testing.T) {
	forceANSI256Profile(t)
	m := NewModel(nil, "s1", nil).WithWorkspace("main", "~/dev/atenea")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})

	line := lineWith(t, m.View(), "main")
	if strings.Contains(line, successStyle.Render("main")) {
		t.Fatalf("top bar = %q, Git branch is metadata and must not use the success role", line)
	}
	if !strings.Contains(line, metadataStyle.Render(branchGlyph+" main")) {
		t.Fatalf("top bar = %q, Git branch must use the metadata role", line)
	}
}

// TestModel_TopBarBranchLeadsWithGlyph verifies that the branch name is
// preceded by the branch glyph (branchGlyph): an empty glyph would leave the
// branch bare, so this case anchors that the icon is emitted before the name.
func TestModel_TopBarBranchLeadsWithGlyph(t *testing.T) {
	if branchGlyph == "" {
		t.Fatal("branchGlyph must not be empty: the top bar branch needs its icon")
	}
	m := NewModel(nil, "s1", nil).WithWorkspace("main", "~/dev/atenea")
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})

	first := lineWith(t, ansi.Strip(m.View()), "main")
	want := branchGlyph + " main"
	if !strings.Contains(first, want) {
		t.Fatalf("the top bar must show the branch glyph before the name (%q); first line = %q", want, first)
	}
}

// TestModel_TopBarKeepsTotalHeight verifies that the top bar is drawn inside
// the chrome and does not increase total height: View() still measures exactly
// Height lines (the bar chrome comes from the body; it adds no extra rows).
func TestModel_TopBarKeepsTotalHeight(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithWorkspace("main", "~/dev/atenea")

	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	first := lineWith(t, ansi.Strip(m.View()), "main")
	if !strings.Contains(first, "main") {
		t.Fatalf("the top bar must show branch %q; bar line = %q", "main", first)
	}

	if got := strings.Count(m.View(), "\n") + 1; got != 12 {
		t.Fatalf("View() must measure 12 lines (the bar does not increase height); got %d", got)
	}
}

// TestModel_TopBarContextUsesTheWindowTheAdapterDeclares verifies that the bar
// reads the active model adapter's window—not a TUI-owned table—and displays it
// as "used / window" ("9k / 256k").
func TestModel_TopBarContextUsesTheWindowTheAdapterDeclares(t *testing.T) {
	const model = "cohere/north-mini-code:free"
	agent := &fakeAgent{
		declared:     true,
		capabilities: llm.Capabilities{ContextWindows: map[string]int{model: 256_000}},
	}

	m := NewModel(agent, "s1", nil).
		WithWorkspace("main", "~/x").
		WithStatus("build", model)

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{InputTokens: 9000}})

	first := lineWith(t, ansi.Strip(m.View()), "256k")

	if !strings.Contains(first, "9k / 256k") {
		t.Fatalf("with a declared window the bar must show %q; first line = %q", "9k / 256k", first)
	}
}

// TestModel_TopBarContextCountsTheCachedPrefix pins the bug that made the
// reading jump between absurd values ("2 / 200k", "4 / 200k") between turns:
// under Anthropic prompt caching, which the adapter enables on every request,
// InputTokens is only the suffix after the last cache breakpoint. The context
// used is the whole prompt — cache reads and writes included — so the label must
// stay stable across a cache write followed by a cache hit instead of collapsing
// to the handful of uncached tokens.
func TestModel_TopBarContextCountsTheCachedPrefix(t *testing.T) {
	const model = "claude-opus-4-8"
	m := NewModel(declaringAgent(model, 200_000), "s1", nil).
		WithWorkspace("main", "~/x").
		WithStatus("build", model)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})

	// The turn that writes the prefix to cache: 54k of it, 4 uncached tokens.
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{
		InputTokens: 4, CacheWriteTokens: 54_000, CacheableInputTokens: 54_004,
	}})
	if first := lineWith(t, ansi.Strip(m.View()), "200k"); !strings.Contains(first, "54k / 200k") {
		t.Fatalf("a cache write must count as context used: want %q; first line = %q", "54k / 200k", first)
	}

	// The next turn reads that prefix back and grows past the next k boundary.
	// The reading must grow with the conversation; a collapse to the 2 uncached
	// tokens is the bug.
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{
		InputTokens: 2, CacheReadTokens: 54_004, CacheWriteTokens: 1_800, CacheableInputTokens: 55_806,
	}})
	if first := lineWith(t, ansi.Strip(m.View()), "200k"); !strings.Contains(first, "55k / 200k") {
		t.Fatalf("a cache hit must not shrink the context used: want %q; first line = %q", "55k / 200k", first)
	}
}

// TestModel_TopBarContextFallsBackToBilledInputWithoutCacheAccounting: an
// adapter that reports no cache accounting at all (CacheableInputTokens zero)
// still has its whole input in InputTokens. The bar must show it rather than a
// zero read off the normalized field.
func TestModel_TopBarContextFallsBackToBilledInputWithoutCacheAccounting(t *testing.T) {
	const model = "local/llama"
	m := NewModel(declaringAgent(model, 200_000), "s1", nil).
		WithWorkspace("main", "~/x").
		WithStatus("build", model)

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{InputTokens: 12_000}})

	if first := lineWith(t, ansi.Strip(m.View()), "200k"); !strings.Contains(first, "12k / 200k") {
		t.Fatalf("without cache accounting the bar must show billed input: want %q; first line = %q", "12k / 200k", first)
	}
}

func TestModel_TopBarContextFormatsMillionTokenWindowAsM(t *testing.T) {
	const model = "openai/gpt-4.1"
	m := NewModel(declaringAgent(model, 1_000_000), "s1", nil).
		WithWorkspace("main", "~/x").
		WithStatus("build", model)

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{InputTokens: 16_000}})

	first := lineWith(t, ansi.Strip(m.View()), "1M")
	if !strings.Contains(first, "16k / 1M") {
		t.Fatalf("with a million-token window the bar must show %q; first line = %q", "16k / 1M", first)
	}
	if strings.Contains(first, "1000k") {
		t.Fatalf("the bar must not show one million as %q; first line = %q", "1000k", first)
	}
}

// TestModel_TopBarContextShowsUsedOnlyWhenWindowUnknown verifies that, when the
// model has no known context window, the right-hand label
// shows only used tokens (e.g. "16k") rather than "used / window".
// It catches an implementation that always assumes a known window or
// blindly concatenates " / ".
func TestModel_TopBarContextShowsUsedOnlyWhenWindowUnknown(t *testing.T) {
	const unknownModel = "demo"
	agent := &fakeAgent{declared: true, capabilities: llm.Capabilities{ContextWindows: map[string]int{"other": 200_000}}}

	m := NewModel(agent, "s1", nil).
		WithWorkspace("main", "~/x").
		WithStatus("build", unknownModel)

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{InputTokens: 16000}})

	first := lineWith(t, ansi.Strip(m.View()), "16k")

	if !strings.Contains(first, "16k") {
		t.Fatalf("with unknown window the bar must show used tokens %q; first line = %q", "16k", first)
	}
	if strings.Contains(first, "16k /") {
		t.Fatalf("with unknown window the bar must NOT show the form %q (there is no window); first line = %q", "16k /", first)
	}
}

// TestModel_TopBarContextTreatsSilenceAsUnknown: an agent that declares nothing
// (the adapter does not implement llm.Describing) is not an adapter without windows.
// The bar must show only usage, never invent a window.
func TestModel_TopBarContextTreatsSilenceAsUnknown(t *testing.T) {
	const model = "cohere/north-mini-code:free"
	agent := &fakeAgent{capabilities: llm.Capabilities{ContextWindows: map[string]int{model: 256_000}}}

	m := NewModel(agent, "s1", nil).
		WithWorkspace("main", "~/x").
		WithStatus("build", model)

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{InputTokens: 9000}})

	first := lineWith(t, ansi.Strip(m.View()), "9k")
	if strings.Contains(first, "9k /") {
		t.Fatalf("without a declaration there is no window to show; first line = %q", first)
	}
}

// TestModel_TopBarWithoutUsageHasNoContextLabel verifies that, without a usage
// event (m.usage == nil), topBarContext() returns "" and the bar draws no
// context label on the right: neither "used / window" nor a loose token count.
// It catches an implementation that displays a default value
// (e.g. "0" or "0 / 200k") before usage exists.
func TestModel_TopBarWithoutUsageHasNoContextLabel(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithWorkspace("main", "~/x")

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})

	first := strings.TrimRight(lineWith(t, ansi.Strip(m.View()), "main"), " ")

	if !strings.Contains(first, "main") {
		t.Fatalf("the bar must show branch %q without usage; first line = %q", "main", first)
	}
	if !strings.Contains(first, "~/x") {
		t.Fatalf("the bar must show directory %q without usage; first line = %q", "~/x", first)
	}
	if strings.Contains(first, " / ") {
		t.Fatalf("without usage the bar must NOT show the form %q; first line = %q", " / ", first)
	}
	if strings.Contains(first, "k ") {
		t.Fatalf("without usage the bar must NOT show a token count %q; first line = %q", "k ", first)
	}
}

// TestModel_TopBarWithoutBranchOrDirStillFillsWidth verifies that, without a branch or
// directory (WithWorkspace("", "")), the bar remains a full-width canvas
// and preserves the canvas invariant: View() measures exactly Height
// lines and all (including the bar) measure Width cells. It catches a bar
// that collapses to width 0 when the left side is empty.
func TestModel_TopBarWithoutBranchOrDirStillFillsWidth(t *testing.T) {
	m := NewModel(nil, "s1", nil).WithWorkspace("", "")

	m = apply(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})

	lines := strings.Split(m.View(), "\n")
	if len(lines) != 12 {
		t.Fatalf("View() must measure 12 lines without branch or directory; got %d", len(lines))
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w != 40 {
			t.Fatalf("line %d must measure exactly 40 cells (canvas invariant); got %d (%q)", i, w, ansi.Strip(line))
		}
	}
}

// TestModel_TopBarTruncatesLeftToFitContextOnNarrowWidth verifies that, when the
// width is insufficient, the right context label ("16k / 200k") always
// survives intact and the left side (long branch + directory) is what gets
// ellipsized, without the bar exceeding the width. It catches an
// implementation that truncates on the right or leaves the bar wider than the
// terminal.
func TestModel_TopBarTruncatesLeftToFitContextOnNarrowWidth(t *testing.T) {
	m := NewModel(declaringAgent("anthropic/claude-opus-4.8", 200_000), "s1", nil).
		WithWorkspace("main", "~/some/very/long/working/directory/path/that/will/not/fit").
		WithStatus("build", "anthropic/claude-opus-4.8")

	m = apply(t, m, tea.WindowSizeMsg{Width: 30, Height: 12})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{InputTokens: 16000}})

	idx := lineIndexWith(t, ansi.Strip(m.View()), "/ 200k")
	rendered := strings.Split(m.View(), "\n")[idx]
	if w := lipgloss.Width(rendered); w != 30 {
		t.Fatalf("the bar must not exceed the width: got %d cells, expected 30 (%q)", w, ansi.Strip(rendered))
	}

	first := ansi.Strip(rendered)
	if !strings.Contains(first, "16k / 200k") {
		t.Fatalf("context label %q must survive truncation intact; first line = %q", "16k / 200k", first)
	}
	if !strings.Contains(first, "…") {
		t.Fatalf("the long left side must be truncated with ellipsis %q; first line = %q", "…", first)
	}
}

// TestModel_TopBarRowClickIsInertBodyRowClickHits verifies the bar offset
// in mouse handling: a click on row 0 (the bar) is inert, while
// a click on the body row containing the reasoning summary
// expands it. With the bar occupying row 0, an off-by-one (without subtracting
// topBarHeight) would mistakenly toggle the first body content. Two independent
// expands it. With the bar occupying row 0, an off-by-one (without subtracting
// topBarHeight) would mistakenly toggle the first body content. Two independent
// submodels are used so part A does not contaminate B.
func TestModel_TopBarRowClickIsInertBodyRowClickHits(t *testing.T) {
	build := func(t *testing.T) Model {
		t.Helper()
		m := NewModel(nil, "s1", nil)
		m = apply(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
		text := "reason-1\nreason-2\nreason-3"
		m = apply(t, m, EventMsg{Kind: session.KindReasoningStarted})
		m = apply(t, m, EventMsg{Kind: session.KindReasoningDelta, Text: text})
		m = drainReveal(t, m)
		m = apply(t, m, EventMsg{Kind: session.KindReasoningEnded, Text: text})
		m = drainReveal(t, m)
		// Short transcript shown from the top: the screen Y row matches
		// the absolute content line.
		if m.viewport.YOffset != 0 {
			t.Fatalf("viewport.YOffset = %d, want 0: the short transcript is shown from the top", m.viewport.YOffset)
		}
		return m
	}

	// Part A (inert): a click on row 0 (the bar) must not expand the
	// reasoning. The summary remains collapsed ("● Thought") and the body does NOT appear.
	mA := build(t)
	if !strings.Contains(ansi.Strip(mA.View()), "● Thought") {
		t.Fatalf("precondition: settled reasoning must collapse to %q", "● Thought")
	}
	mA = apply(t, mA, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: 0})
	viewA := ansi.Strip(mA.View())
	if !strings.Contains(viewA, "● Thought") {
		t.Fatalf("a click on the bar row (Y=0) must be inert: summary %q must remain; View = %q", "● Thought", viewA)
	}
	for _, body := range []string{"reason-2", "reason-3"} {
		if strings.Contains(viewA, body) {
			t.Fatalf("a click on the bar row (Y=0) must NOT expand reasoning; View = %q contains %q", viewA, body)
		}
	}

	// Part B (impact): a click on the visible summary row expands it.
	mB := build(t)
	summaryY := lineIndexWith(t, ansi.Strip(mB.View()), "● Thought")
	mB = apply(t, mB, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: summaryY})
	viewB := ansi.Strip(mB.View())
	for _, want := range []string{"reason-1", "reason-2", "reason-3"} {
		if !strings.Contains(viewB, want) {
			t.Fatalf("a click on summary row %d must expand reasoning showing %q; View = %q", summaryY, want, viewB)
		}
	}
}

func TestModel_TopBarContextPreservesTwoDecimalMillionWindow(t *testing.T) {
	const model = "openai/gpt-4.1"
	m := NewModel(declaringAgent(model, 1_050_000), "s1", nil).
		WithWorkspace("main", "~/x").
		WithStatus("build", model)

	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m = apply(t, m, EventMsg{Kind: session.KindStepEnded, Usage: &session.Usage{InputTokens: 16_000}})

	first := lineWith(t, ansi.Strip(m.View()), "1.05M")
	if !strings.Contains(first, "16k / 1.05M") {
		t.Fatalf("the top bar must preserve precision for million-token windows; first line = %q", first)
	}
	if strings.Contains(first, "1.1M") {
		t.Fatalf("the top bar must not round 1.05M to 1.1M; first line = %q", first)
	}
}
