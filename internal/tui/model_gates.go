package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/permission"
)

func (m Model) handlePermissionKey(msg tea.KeyMsg, perm entry) Model {
	choices := m.permissionChoiceCount(perm)
	switch msg.Type {
	case tea.KeyLeft:
		m.permissionChoice = max(m.permissionSelection(perm)-1, permissionDeny)
		return m
	case tea.KeyRight:
		m.permissionChoice = min(m.permissionSelection(perm)+1, choices-1)
		return m
	case tea.KeyTab:
		m.permissionChoice = (m.permissionSelection(perm) + 1) % choices
		return m
	case tea.KeyUp:
		m.permissionScroll = max(m.permissionScroll-1, 0)
		return m
	case tea.KeyDown:
		m.permissionScroll++
		return m
	case tea.KeyEsc:
		return m.resolvePermission(perm, permission.Denied)
	case tea.KeyEnter:
		return m.resolvePermission(perm, permissionVerdict(m.permissionSelection(perm)))
	case tea.KeyRunes:
		switch strings.ToLower(string(msg.Runes)) {
		case "y":
			return m.resolvePermission(perm, permission.AllowedOnce)
		case "n":
			return m.resolvePermission(perm, permission.Denied)
		case "a":
			if choices > permissionAllowSession {
				return m.resolvePermission(perm, permission.AllowedSession)
			}
		}
	}
	return m
}

// permissionVerdict translates the selected action into the verdict the engine
// understands.
func permissionVerdict(choice permissionChoice) permission.Verdict {
	switch choice {
	case permissionAllowOnce:
		return permission.AllowedOnce
	case permissionAllowSession:
		return permission.AllowedSession
	}
	return permission.Denied
}

func (m Model) resolvePermission(perm entry, verdict permission.Verdict) Model {
	if m.agent == nil {
		return m
	}
	sessionID := perm.sessionID
	if sessionID == "" {
		sessionID = m.sessionID
	}
	m.agent.ResolvePermission(sessionID, perm.callID, verdict)
	m = m.applyPermissionDecision(perm, verdict.Approved())
	m.permissionChoice = permissionDeny
	m.permissionScroll = 0
	return m.resizeViewport()
}

func (m Model) resolvePlanKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if msg.Type != tea.KeyRunes || m.agent == nil {
		return m, nil
	}
	switch string(msg.Runes) {
	case "y":
		run, err := m.agent.AcceptPlan(m.sessionID)
		if err != nil {
			return m.appendError(err.Error()).syncViewport(), nil
		}
		m = m.removePendingPlan()
		m.planMode = false
		m.activeRun = run.RunID
		m.working = run.RunID != 0
		return m.resizeViewport(), m.spinner.Tick
	case "n":
		return m.removePendingPlan().syncViewport(), nil
	}
	return m, nil
}
