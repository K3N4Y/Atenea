package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/K3N4Y/atenea/internal/tool"
)

func (m Model) showsWorking() bool {
	if _, pending := m.pendingPermission(); pending {
		return false
	}
	return m.working
}

func (m Model) workingStatusView() string {
	if !m.showsWorking() {
		return ""
	}
	margin := composerOuterMargin
	if m.ready {
		margin = m.baseLayout().chatMargin
	}
	return strings.Repeat(" ", margin) + m.spinner.View() + statusStyle.Render(" "+m.workingStatusLabel()) + "\n"
}

func (m Model) workingStatusLabel() string {
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := m.entries[i]
		switch e.kind {
		case entryAssistant:
			if !e.settled() {
				return "Preparing response"
			}
		case entryReasoning:
			if !e.settled() {
				return "Checking context"
			}
		case entryTool:
			p := m.presentationOf(e)
			if e.status == toolRunning {
				return workingToolStatusLabel(e, p)
			}
			if e.status == toolOK && toolReviewsChanges(e, p) {
				return "Reviewing changes"
			}
		case entryRetry:
			return "Still working"
		case entryCompaction:
			if e.live {
				return "Checking context"
			}
		case entryUser:
			return "Checking context"
		}
	}
	return "Checking context"
}

func workingToolStatusLabel(e entry, p tool.Presentation) string {
	if toolReviewsChanges(e, p) {
		return "Reviewing changes"
	}
	switch e.tool {
	case "read", "grep", "glob", "web_fetch", "skill":
		return "Checking context"
	case "present_plan":
		return "Preparing response"
	default:
		return "Still working"
	}
}

func toolReviewsChanges(e entry, p tool.Presentation) bool {
	if p.Kind == tool.FileChange || p.Kind == tool.FileCreation {
		return true
	}
	return e.tool == "edit" || e.tool == "write"
}

func (m Model) gitSummaryLine(width, margin int) string {
	innerWidth := max(width-2*margin, 0)
	left := ""
	if m.cancelPending {
		left = ansi.Truncate(statusStyle.Render("Esc again to cancel"), innerWidth, "…")
	}
	leftWidth := ansi.StringWidth(left)
	separatorWidth := 0
	if left != "" {
		separatorWidth = 1
	}
	rightWidth := max(innerWidth-leftWidth-separatorWidth, 0)
	right := m.gitSummaryLabel(rightWidth)
	gap := max(innerWidth-leftWidth-ansi.StringWidth(right), 0)
	return strings.Repeat(" ", margin) + left + strings.Repeat(" ", gap) + right + strings.Repeat(" ", margin)
}

func (m Model) gitSummaryLabel(width int) string {
	if m.gitSummary.Files == 0 || width <= 0 {
		return ""
	}
	fileWord := "files"
	if m.gitSummary.Files == 1 {
		fileWord = "file"
	}
	stats := fmt.Sprintf("+%d  −%d", m.gitSummary.Additions, m.gitSummary.Deletions)
	variants := []string{
		fmt.Sprintf("%d %s changed  %s", m.gitSummary.Files, fileWord, stats),
		fmt.Sprintf("%d %s  %s", m.gitSummary.Files, fileWord, stats),
		stats,
	}
	for index, variant := range variants {
		if ansi.StringWidth(variant) > width {
			continue
		}
		prefix := strings.TrimSuffix(variant, stats)
		styledStats := diffAddStyle.Render(fmt.Sprintf("+%d", m.gitSummary.Additions)) + "  " + diffDelStyle.Render(fmt.Sprintf("−%d", m.gitSummary.Deletions))
		if index == len(variants)-1 {
			return styledStats
		}
		return statusStyle.Render(prefix) + styledStats
	}
	return ""
}
