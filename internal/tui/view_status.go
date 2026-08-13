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
	return strings.Repeat(" ", margin) + m.spinner.View() + secondaryTextStyle.Render(" "+m.workingStatusLabel()) + "\n"
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

// gitSummaryLine renders the row under the composer, shared by three tenants in
// descending priority: an armed confirmation, the git working-tree summary, and
// the arrival hint. The hint only takes the space the other two leave, and only
// in a variant that fits whole: a truncated hint teaches nothing.
func (m Model) gitSummaryLine(width, margin int) string {
	innerWidth := max(width-2*margin, 0)
	left := ""
	right := ""
	switch m.armedConfirm() {
	case confirmCancelRun:
		left = ansi.Truncate(metadataStyle.Render("Esc again to cancel"), innerWidth, "…")
	case confirmQuit:
		left = ansi.Truncate(metadataStyle.Render("Ctrl+C again to quit"), innerWidth, "…")
	}
	if left != "" {
		right = m.gitSummaryLabel(max(innerWidth-ansi.StringWidth(left)-1, 0))
	} else {
		right = m.gitSummaryLabel(innerWidth)
		left = m.composerHintLabel(max(innerWidth-ansi.StringWidth(right)-1, 0))
	}
	gap := max(innerWidth-ansi.StringWidth(left)-ansi.StringWidth(right), 0)
	return strings.Repeat(" ", margin) + left + strings.Repeat(" ", gap) + right + strings.Repeat(" ", margin)
}

// composerHintLabel is the arrival hint, shown until the conversation starts:
// the gestures that open every other surface, in the longest variant that fits
// the given width. It shares the row the confirmations and the git summary
// already occupy, so pointing at the rest of the UI costs no permanent line.
func (m Model) composerHintLabel(width int) string {
	if width <= 0 || m.startedConversation() {
		return ""
	}
	for _, variant := range composerHints {
		if ansi.StringWidth(variant) <= width {
			return metadataStyle.Render(variant)
		}
	}
	return ""
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
		return metadataStyle.Render(prefix) + styledStats
	}
	return ""
}
