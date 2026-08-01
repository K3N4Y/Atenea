package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/tui/engine"
)

func (m Model) submitPrompt() (Model, tea.Cmd) {
	text := m.input.Value()
	if text == "" || m.agent == nil {
		return m, nil
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "/mode" || trimmed == "/mode:auto-accept" || trimmed == "/mode:ask" || trimmed == "/mode:yolo" {
		controller, ok := m.agent.(autoAcceptAgent)
		if !ok {
			return m.appendError("permission mode is unavailable"), nil
		}
		if trimmed == "/mode:auto-accept" {
			if yolo, ok := m.agent.(yoloAgent); ok {
				yolo.SetYolo(false)
			}
			controller.SetAutoAccept(m.sessionID, true)
		}
		if trimmed == "/mode:ask" {
			if yolo, ok := m.agent.(yoloAgent); ok {
				yolo.SetYolo(false)
			}
			controller.SetAutoAccept(m.sessionID, false)
		}
		if trimmed == "/mode:yolo" {
			yolo, ok := m.agent.(yoloAgent)
			if !ok || !yolo.YoloAuthorized() || !yolo.SetYolo(true) {
				return m.appendError("YOLO mode requires launching with --yolo"), nil
			}
		}
		mode := "ask"
		if yolo, ok := m.agent.(yoloAgent); ok && yolo.YoloEnabled() {
			mode = "yolo"
		} else if controller.AutoAcceptEnabled(m.sessionID) {
			mode = "auto-accept"
		}
		m.input.SetValue("")
		m.menuItems = nil
		m.Transcript = m.Transcript.appendNotice("permission mode: " + mode)
		return m.syncViewport(), nil
	}
	if next, handled := m.handleCacheStatsCommand(trimmed); handled {
		return next, nil
	}
	if strings.HasPrefix(trimmed, "/undo") {
		if trimmed != "/undo" {
			return m.appendError("usage: /undo"), nil
		}
		sessionID := m.sessionID
		agent := m.agent
		return m, func() tea.Msg {
			result, err := agent.Undo(sessionID)
			if err != nil {
				return UndoDoneMsg{Err: err.Error()}
			}
			return UndoDoneMsg{Result: result}
		}
	}
	if strings.HasPrefix(trimmed, "/resume") {
		if trimmed != "/resume" {
			return m.appendError("usage: /resume"), nil
		}
		if m.working {
			return m.appendError(engine.ErrResumeActiveRun.Error()), nil
		}
		sessionID := m.sessionID
		agent := m.agent
		m.input.SetValue("")
		m.menuItems = nil
		m.resumeGen++
		m.resumePicker = newResumePicker(sessionID)
		generation := m.resumeGen
		return m, func() tea.Msg {
			sessions, err := agent.ListResumeSessions(sessionID)
			if err != nil {
				return ResumeSessionsDoneMsg{Generation: generation, Err: err.Error()}
			}
			return ResumeSessionsDoneMsg{Generation: generation, Sessions: sessions}
		}
	}
	if strings.HasPrefix(trimmed, "/mcp") {
		if trimmed != "/mcp" {
			return m.appendError("usage: /mcp"), nil
		}
		if _, ok := m.agent.(mcpAgent); !ok {
			return m.appendError("MCP management is unavailable"), nil
		}
		m.input.SetValue("")
		m.menuItems = nil
		m.mcpGen++
		m.mcpPicker = newMCPPicker()
		m.mcpPicker.refreshFromAgent(m.agent)
		return m.resizeViewport(), nil
	}
	if strings.HasPrefix(trimmed, "/connect") {
		parts := strings.Fields(trimmed)
		if parts[0] != "/connect" || len(parts) > 2 {
			return m.appendError("usage: /connect [provider-id]").syncViewport(), nil
		}
		controller, ok := m.agent.(connectAgent)
		if !ok {
			return m.appendError("provider connection is unavailable").syncViewport(), nil
		}
		providers := controller.ConnectableProviders()
		if len(providers) == 0 {
			return m.appendError("no connectable providers configured").syncViewport(), nil
		}
		panel := newConnectPanel(providers)
		jump := -1
		if len(parts) == 2 {
			for index, provider := range providers {
				if provider.ID == parts[1] {
					jump = index
					break
				}
			}
			if jump < 0 {
				return m.appendError(fmt.Sprintf("usage: /connect [provider-id]; %q is not connectable", parts[1])).syncViewport(), nil
			}
		}
		m.input.SetValue("")
		m.menuItems = nil
		m.connectGen++
		m.connectPanel = panel
		if jump < 0 {
			return m.resizeViewport(), nil
		}
		// Naming a provider skips the list and starts that provider's own kind of
		// connection, which is the key entry for one and a login for the other.
		m.connectPanel.selected = jump
		next, cmd := m.beginConnect()
		return next.(Model).resizeViewport(), cmd
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/model") {
		controller, ok := m.agent.(modelAgent)
		if !ok {
			return m.appendError("model selection is unavailable"), nil
		}
		parts := strings.Fields(text)
		if len(parts) == 1 && parts[0] == "/model" {
			m.input.SetValue("")
			m.menuItems = nil
			m.modelPicker = newModelPicker(controller.ModelCatalog(), controller.CurrentModel())
			controller.RefreshModels()
			return m.resizeViewport(), nil
		}
		if len(parts) != 3 || parts[0] != "/model" {
			return m.appendError("usage: /model <provider-id> <model-id>"), nil
		}
		active, err := controller.SelectModel(parts[1], parts[2])
		if err != nil {
			return m.appendError(err.Error()), nil
		}
		m.model = active.Model
		m.input.SetValue("")
		m.menuItems = nil
		return m.resizeViewport(), nil
	}
	if text == "/new" {
		run, err := m.agent.SendPrompt(m.sessionID, text)
		if err != nil {
			return m.appendError(err.Error()).syncViewport(), nil
		}
		m.sessionID = run.SessionID
		m.activeRun = 0
		m.entries = nil
		m.history = nil
		m.histIdx = 0
		m.planMode = false
		m.working = false
		m.revealing = false
		m.usage = nil
		m.liveUsage = false
		m.outputBytes = 0
		m.reasoningBytes = 0
		m.toolInputBytes = 0
		m.input.SetValue("")
		m.menuItems = nil
		return m.resizeViewport(), nil
	}
	if text == "/compact" {
		if _, err := m.agent.SendPrompt(m.sessionID, text); err != nil {
			return m.appendError(err.Error()).syncViewport(), nil
		}
		m.input.SetValue("")
		m.menuItems = nil
		return m.resizeViewport(), nil
	}
	var run RunHandle
	var err error
	if m.planMode {
		run, err = m.agent.SendPlanPrompt(m.sessionID, text)
	} else {
		run, err = m.agent.SendPrompt(m.sessionID, text)
	}
	if err != nil {
		return m.appendError(err.Error()).syncViewport(), nil
	}
	m.input.SetValue("")
	m.history = append(m.history, text)
	if len(m.history) > historyLimit {
		m.history = m.history[len(m.history)-historyLimit:]
	}
	m.histIdx = len(m.history)
	m.activeRun = run.RunID
	m.working = run.RunID != 0
	return m.resizeViewport(), m.spinner.Tick
}

func (m Model) stopRun() {
	if m.agent != nil {
		m.agent.Stop(m.sessionID)
	}
}
