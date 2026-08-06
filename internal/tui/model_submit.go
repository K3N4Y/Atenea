package tui

import (
	"fmt"
	"strings"

	"github.com/K3N4Y/atenea/internal/llm"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tui/engine"
)

type localCommandKind uint8

const (
	localCommandMode localCommandKind = iota
	localCommandReasoning
	localCommandCacheStats
	localCommandUndo
	localCommandCheckpoint
	localCommandRewind
	localCommandResume
	localCommandMCP
	localCommandSkills
	localCommandConnect
	localCommandModel
	localCommandNew
	localCommandCompact
)

type localCommand struct {
	kind     localCommandKind
	text     string
	original string
}

type checkpointAgent interface {
	Checkpoint(sessionID string) (CheckpointResult, error)
	Rewind(sessionID string) (RewindResult, error)
}

func parseLocalCommand(text string) (localCommand, bool) {
	trimmed := strings.TrimSpace(text)
	fields := strings.Fields(trimmed)
	switch {
	case trimmed == "/mode", trimmed == "/mode:auto-accept", trimmed == "/mode:ask", trimmed == "/mode:yolo":
		return localCommand{kind: localCommandMode, text: trimmed}, true
	case trimmed == "/reasoning" || strings.HasPrefix(trimmed, "/reasoning:"):
		return localCommand{kind: localCommandReasoning, text: trimmed}, true
	case trimmed == "/cache-stats":
		return localCommand{kind: localCommandCacheStats, text: trimmed, original: text}, true
	case strings.HasPrefix(trimmed, "/undo"):
		return localCommand{kind: localCommandUndo, text: trimmed}, true
	case trimmed == "/checkpoint":
		return localCommand{kind: localCommandCheckpoint, text: trimmed}, true
	case trimmed == "/rewind":
		return localCommand{kind: localCommandRewind, text: trimmed}, true
	case strings.HasPrefix(trimmed, "/resume"):
		return localCommand{kind: localCommandResume, text: trimmed}, true
	case strings.HasPrefix(trimmed, "/mcp"):
		return localCommand{kind: localCommandMCP, text: trimmed}, true
	case len(fields) > 0 && fields[0] == "/skills":
		return localCommand{kind: localCommandSkills, text: trimmed}, true
	case strings.HasPrefix(trimmed, "/connect"):
		return localCommand{kind: localCommandConnect, text: trimmed}, true
	case strings.HasPrefix(trimmed, "/model"):
		return localCommand{kind: localCommandModel, text: text}, true
	case text == "/new":
		return localCommand{kind: localCommandNew, text: text}, true
	case text == "/compact":
		return localCommand{kind: localCommandCompact, text: text}, true
	default:
		return localCommand{}, false
	}
}

func (m Model) submitPrompt() (Model, tea.Cmd) {
	text := m.composer.value()
	if text == "" || m.agent == nil {
		return m, nil
	}
	if command, ok := parseLocalCommand(text); ok {
		return m.executeLocalCommand(command)
	}
	return m.submitAgentPrompt(text)
}

func (m Model) executeLocalCommand(command localCommand) (Model, tea.Cmd) {
	switch command.kind {
	case localCommandMode:
		return m.submitModeCommand(command.text)
	case localCommandReasoning:
		next, _ := m.handleReasoningCommand(command.text)
		return next, nil
	case localCommandCacheStats:
		next, handled := m.handleCacheStatsCommand(command.text)
		if handled {
			return next, nil
		}
		return m.submitAgentPrompt(command.original)
	case localCommandUndo:
		return m.submitUndoCommand(command.text)
	case localCommandCheckpoint:
		return m.submitCheckpointCommand()
	case localCommandRewind:
		return m.submitRewindCommand()
	case localCommandResume:
		return m.submitResumeCommand(command.text)
	case localCommandMCP:
		return m.submitMCPCommand(command.text)
	case localCommandSkills:
		return m.submitSkillsCommand(command.text)
	case localCommandConnect:
		return m.submitConnectCommand(command.text)
	case localCommandModel:
		return m.submitModelCommand(command.text)
	case localCommandNew:
		return m.startNewSession()
	case localCommandCompact:
		return m.submitCompactCommand()
	default:
		return m, nil
	}
}

func (m Model) submitModeCommand(command string) (Model, tea.Cmd) {
	controller, ok := m.agent.(autoAcceptAgent)
	if !ok {
		return m.appendError("permission mode is unavailable"), nil
	}
	if command == "/mode:auto-accept" {
		if yolo, ok := m.agent.(yoloAgent); ok {
			yolo.SetYolo(false)
		}
		controller.SetAutoAccept(m.sessionID, true)
	}
	if command == "/mode:ask" {
		if yolo, ok := m.agent.(yoloAgent); ok {
			yolo.SetYolo(false)
		}
		controller.SetAutoAccept(m.sessionID, false)
	}
	if command == "/mode:yolo" {
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
	m.composer = m.composer.clear()
	m.Transcript = m.Transcript.appendNotice("permission mode: " + mode)
	return m.syncViewport(), nil
}

func (m Model) submitUndoCommand(command string) (Model, tea.Cmd) {
	if command != "/undo" {
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

func (m Model) submitCheckpointCommand() (Model, tea.Cmd) {
	controller, ok := m.agent.(checkpointAgent)
	if !ok {
		return m.appendError("checkpoint management is unavailable"), nil
	}
	sessionID := m.sessionID
	m.composer = m.composer.clear()
	return m, func() tea.Msg {
		result, err := controller.Checkpoint(sessionID)
		if err != nil {
			return CheckpointDoneMsg{Err: err.Error()}
		}
		return CheckpointDoneMsg{Result: result}
	}
}

func (m Model) submitRewindCommand() (Model, tea.Cmd) {
	controller, ok := m.agent.(checkpointAgent)
	if !ok {
		return m.appendError("checkpoint management is unavailable"), nil
	}
	sessionID := m.sessionID
	m.composer = m.composer.clear()
	return m, func() tea.Msg {
		result, err := controller.Rewind(sessionID)
		if err != nil {
			return RewindDoneMsg{Err: err.Error()}
		}
		return RewindDoneMsg{Result: result}
	}
}

func (m Model) submitResumeCommand(command string) (Model, tea.Cmd) {
	if command != "/resume" {
		return m.appendError("usage: /resume"), nil
	}
	if m.working {
		return m.appendError(engine.ErrResumeActiveRun.Error()), nil
	}
	sessionID := m.sessionID
	agent := m.agent
	m.composer = m.composer.clear()
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

func (m Model) submitMCPCommand(command string) (Model, tea.Cmd) {
	if command != "/mcp" {
		return m.appendError("usage: /mcp"), nil
	}
	if _, ok := m.agent.(mcpAgent); !ok {
		return m.appendError("MCP management is unavailable"), nil
	}
	m.composer = m.composer.clear()
	m.mcpGen++
	m.mcpPicker = newMCPPicker()
	m.mcpPicker.refreshFromAgent(m.agent)
	return m.resizeViewport(), nil
}

func (m Model) submitConnectCommand(command string) (Model, tea.Cmd) {
	parts := strings.Fields(command)
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
	m.composer = m.composer.clear()
	m.connectGen++
	m.connectPanel = panel
	if jump < 0 {
		return m.resizeViewport(), nil
	}
	m.connectPanel.selected = jump
	next, cmd := m.beginConnect()
	return next.(Model).resizeViewport(), cmd
}

func (m Model) submitModelCommand(command string) (Model, tea.Cmd) {
	controller, ok := m.agent.(modelAgent)
	if !ok {
		return m.appendError("model selection is unavailable"), nil
	}
	parts := strings.Fields(command)
	if len(parts) == 1 && parts[0] == "/model" {
		m.composer = m.composer.clear()
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
	m.composer = m.composer.clear()
	return m.resizeViewport(), nil
}

func (m Model) startNewSession() (Model, tea.Cmd) {
	run, err := m.agent.SendPrompt(m.sessionID, session.Prompt{Text: "/new"})
	if err != nil {
		return m.appendError(err.Error()).syncViewport(), nil
	}
	m = m.freshSession(run.SessionID)
	return m.resizeViewport(), nil
}

func (m Model) submitCompactCommand() (Model, tea.Cmd) {
	if _, err := m.agent.SendPrompt(m.sessionID, session.Prompt{Text: "/compact"}); err != nil {
		return m.appendError(err.Error()).syncViewport(), nil
	}
	m.composer = m.composer.clear()
	return m.resizeViewport(), nil
}

func (m Model) submitAgentPrompt(text string) (Model, tea.Cmd) {
	prompt := m.composer.prompt()
	prompt.Text = text
	var run RunHandle
	var err error
	if m.planMode {
		run, err = m.agent.SendPlanPrompt(m.sessionID, prompt)
	} else {
		run, err = m.agent.SendPrompt(m.sessionID, prompt)
	}
	if err != nil {
		return m.appendError(err.Error()).syncViewport(), nil
	}
	m.composer = m.composer.clear().pushHistory(text)
	m.activeRun = run.RunID
	m.working = run.RunID != 0
	return m.resizeViewport(), m.spinner.Tick
}

func (m Model) handleReasoningCommand(trimmed string) (Model, bool) {
	if trimmed != "/reasoning" && !strings.HasPrefix(trimmed, "/reasoning:") {
		return m, false
	}
	agent, ok := m.agent.(reasoningAgent)
	if !ok {
		return m.appendError("reasoning selection is unavailable"), true
	}
	if trimmed == "/reasoning" {
		m.composer = m.composer.clear()
		m.Transcript = m.Transcript.appendNotice(llm.ReasoningHelp(agent.ReasoningEffort()))
		return m.syncViewport(), true
	}
	effortText := strings.TrimPrefix(trimmed, "/reasoning:")
	if effortText == "default" {
		effortText = ""
	}
	effort := llm.ReasoningEffort(effortText)
	if err := agent.SetReasoningEffort(effort); err != nil {
		return m.appendError(err.Error()), true
	}
	m.composer = m.composer.clear()
	label := string(effort)
	if label == "" {
		label = "default"
	}
	m.Transcript = m.Transcript.appendNotice("reasoning effort: " + label)
	return m.syncViewport(), true
}

func (m Model) stopRun() {
	if m.agent != nil {
		m.agent.Stop(m.sessionID)
	}
}
