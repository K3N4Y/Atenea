package tui

// The /connect panel: a full-screen modal (mirroring the /model and /mcp
// pickers) that connects a provider by API key. Two stages: pick a provider
// from the connectable list, then type or paste the key into a masked input.
// The key lives only in the panel state — it never touches the composer input
// nor its persisted history. Validation runs as an asynchronous command so a
// slow endpoint never freezes the UI.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"atenea/internal/providerconfig"
)

// connectAgent is the engine surface the panel needs. ConnectProvider blocks
// on the network validation, so the Model always calls it from a tea.Cmd.
type connectAgent interface {
	ConnectableProviders() []providerconfig.ConnectableProvider
	ConnectProvider(providerID, apiKey string) (providerconfig.Active, error)
}

// connectDoneMsg reports the outcome of one connect attempt. generation
// invalidates results from a panel that was closed and reopened meanwhile.
type connectDoneMsg struct {
	generation uint64
	providerID string
	active     providerconfig.Active
	err        string
}

type connectPanel struct {
	open      bool
	providers []providerconfig.ConnectableProvider
	selected  int
	// entering is the key input stage for providers[selected]; key holds the
	// typed runes, rendered masked.
	entering bool
	key      []rune
	// busy marks a validation in flight; the panel ignores edits until the
	// connectDoneMsg lands.
	busy bool
	err  string
}

func newConnectPanel(providers []providerconfig.ConnectableProvider) connectPanel {
	return connectPanel{open: true, providers: append([]providerconfig.ConnectableProvider(nil), providers...)}
}

func (p *connectPanel) move(delta int) {
	if len(p.providers) == 0 {
		return
	}
	p.selected = wrapSelection(p.selected+delta, len(p.providers))
}

func (p connectPanel) selectedProvider() (providerconfig.ConnectableProvider, bool) {
	if p.selected < 0 || p.selected >= len(p.providers) {
		return providerconfig.ConnectableProvider{}, false
	}
	return p.providers[p.selected], true
}

// handleConnectPanelKey routes the keyboard while the panel is open. On the
// list stage arrows move and enter opens the key entry; on the key entry runes
// (including pastes, which arrive as rune batches) feed the masked key,
// backspace deletes, ctrl+u clears, and enter submits. Esc steps back one
// stage. While a validation is in flight only esc works.
func (m Model) handleConnectPanelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.connectPanel.busy {
		if msg.Type == tea.KeyEsc {
			m.connectPanel.open = false
			return m.resizeViewport(), nil
		}
		return m, nil
	}
	if msg.Type == tea.KeyEsc {
		if m.connectPanel.entering {
			m.connectPanel.entering = false
			m.connectPanel.key = nil
			m.connectPanel.err = ""
			return m, nil
		}
		m.connectPanel.open = false
		return m.resizeViewport(), nil
	}
	if !m.connectPanel.entering {
		switch msg.Type {
		case tea.KeyUp:
			m.connectPanel.move(-1)
		case tea.KeyDown:
			m.connectPanel.move(1)
		case tea.KeyEnter:
			if _, ok := m.connectPanel.selectedProvider(); ok {
				m.connectPanel.entering = true
				m.connectPanel.err = ""
			}
		}
		return m, nil
	}
	switch msg.Type {
	case tea.KeyRunes:
		m.connectPanel.key = append(m.connectPanel.key, msg.Runes...)
	case tea.KeyBackspace:
		if len(m.connectPanel.key) > 0 {
			m.connectPanel.key = m.connectPanel.key[:len(m.connectPanel.key)-1]
		}
	case tea.KeyCtrlU:
		m.connectPanel.key = nil
	case tea.KeyEnter:
		return m.submitConnectKey()
	}
	return m, nil
}

// submitConnectKey launches the asynchronous validate-and-store. The panel
// stays open showing the in-flight state until its connectDoneMsg lands.
func (m Model) submitConnectKey() (tea.Model, tea.Cmd) {
	provider, ok := m.connectPanel.selectedProvider()
	apiKey := strings.TrimSpace(string(m.connectPanel.key))
	if !ok || apiKey == "" {
		return m, nil
	}
	controller, ok := m.agent.(connectAgent)
	if !ok {
		m.connectPanel.err = "provider connection is unavailable"
		return m, nil
	}
	m.connectPanel.busy = true
	m.connectPanel.err = ""
	generation := m.connectGen
	providerID := provider.ID
	return m, func() tea.Msg {
		active, err := controller.ConnectProvider(providerID, apiKey)
		done := connectDoneMsg{generation: generation, providerID: providerID, active: active}
		if err != nil {
			done.err = err.Error()
		}
		return done
	}
}

// finishConnect lands the outcome of a connect attempt. Success closes the
// panel, records the confirmation in the transcript, and refreshes the model
// catalog (discovery works now that a key exists); failure keeps the key
// entry open with the error, ready to retype. The credential may have been
// stored even if the user closed the panel early, so the success path also
// runs with the panel closed.
func (m Model) finishConnect(done connectDoneMsg) (Model, tea.Cmd) {
	m.connectPanel.busy = false
	if done.err != "" {
		if m.connectPanel.open {
			m.connectPanel.err = done.err
			return m, nil
		}
		return m.appendError(done.err).syncViewport(), nil
	}
	m.connectPanel.open = false
	m.connectPanel.key = nil
	return m.applyConnectSuccess(done).resizeViewport().syncViewport(), nil
}

// applyStaleConnectSuccess lands a success whose panel was closed and reopened
// meanwhile (the generation moved on). The credential was stored and the
// provider possibly activated, so the globally-true effects still apply; the
// current panel only gets its connected flag updated — its busy/err state
// belongs to whatever attempt it is running now.
func (m Model) applyStaleConnectSuccess(done connectDoneMsg) (Model, tea.Cmd) {
	for index := range m.connectPanel.providers {
		if m.connectPanel.providers[index].ID == done.providerID {
			m.connectPanel.providers[index].Connected = true
		}
	}
	return m.applyConnectSuccess(done).syncViewport(), nil
}

// applyConnectSuccess applies the shared effects of a successful connect:
// footer model when the connected provider is the active one, a transcript
// confirmation, and a model-catalog refresh (discovery works now that a key
// exists). The active model is only attributed to the provider when it really
// belongs to it — connecting while another provider stays selected must not
// label that provider's model as the connected one.
func (m Model) applyConnectSuccess(done connectDoneMsg) Model {
	name := done.providerID
	for _, provider := range m.connectPanel.providers {
		if provider.ID == done.providerID {
			name = provider.Name
		}
	}
	notice := "Connected to " + name
	if done.active.ProviderID == done.providerID && done.active.Model != "" {
		m.model = done.active.Model
		notice += " · " + done.active.Model
	}
	if controller, ok := m.agent.(modelAgent); ok {
		controller.RefreshModels()
	}
	return m.appendNotice(notice)
}

// handleConnectPanelMouse mirrors the keyboard on the list stage: the wheel
// moves the selection and a left click on a provider row opens its key entry.
// The key entry stage is keyboard-only.
func (m Model) handleConnectPanelMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	if m.connectPanel.busy || m.connectPanel.entering || msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.connectPanel.move(-1)
	case tea.MouseButtonWheelDown:
		m.connectPanel.move(1)
	case tea.MouseButtonLeft:
		layout := m.modelPickerLayout()
		// Same screen geometry as the model picker: blank row, top border,
		// header, separator, then the item rows.
		row := msg.Y - 4
		x := msg.X - layout.marginLeft - 1
		if row < 0 || row >= layout.itemRows || x < 0 || x >= layout.innerWidth {
			return m, nil
		}
		errRows := 0
		if m.connectPanel.err != "" {
			errRows = 1
		}
		start, end := modelPickerWindow(len(m.connectPanel.providers), m.connectPanel.selected, layout.itemRows-errRows)
		index := start + row - errRows
		if row < errRows || index >= end {
			return m, nil
		}
		m.connectPanel.selected = index
		m.connectPanel.entering = true
		m.connectPanel.err = ""
	}
	return m, nil
}

func (m Model) connectPanelView() string {
	layout := m.modelPickerLayout()
	innerWidth := layout.innerWidth
	itemRows := layout.itemRows

	rows := make([]string, 0, itemRows)
	if m.connectPanel.err != "" {
		rows = append(rows, errorStyle.Render(modelPickerCell(" "+sanitizeTerminalText(m.connectPanel.err), innerWidth)))
	}
	hint := " ↑↓ move · enter select · esc close"
	if m.connectPanel.entering {
		provider, _ := m.connectPanel.selectedProvider()
		rows = append(rows, modelPickerCell(" Connect "+sanitizeTerminalText(provider.Name)+" with an API key", innerWidth), strings.Repeat(" ", max(innerWidth, 0)))
		masked := strings.Repeat("•", len(m.connectPanel.key))
		switch {
		case m.connectPanel.busy:
			rows = append(rows, modelPickerCell(" API key: "+masked, innerWidth), strings.Repeat(" ", max(innerWidth, 0)), statusStyle.Render(modelPickerCell(" validating…", innerWidth)))
			hint = " validating… · esc close"
		case len(m.connectPanel.key) == 0:
			rows = append(rows, modelPickerCell(" API key: ", innerWidth)+"", statusStyle.Render(modelPickerCell(" paste or type the key; it is stored privately, never shown", innerWidth)))
			hint = " enter connect · ctrl+u clear · esc back"
		default:
			rows = append(rows, modelPickerCell(" API key: "+accentStyle.Render(masked+"▌"), innerWidth))
			hint = " enter connect · ctrl+u clear · esc back"
		}
	} else {
		start, end := modelPickerWindow(len(m.connectPanel.providers), m.connectPanel.selected, itemRows-len(rows))
		for index := start; index < end; index++ {
			rows = append(rows, m.connectPanelRow(m.connectPanel.providers[index], index == m.connectPanel.selected, innerWidth))
		}
		if len(m.connectPanel.providers) == 0 {
			rows = append(rows, modelPickerCell("  No connectable providers", innerWidth))
		}
	}
	for len(rows) < itemRows {
		rows = append(rows, strings.Repeat(" ", max(innerWidth, 0)))
	}

	lines := []string{
		modelPickerCell(" Provider", innerWidth),
		strings.Repeat("─", max(innerWidth, 0)),
	}
	for index := 0; index < itemRows; index++ {
		lines = append(lines, modelPickerCell(rows[index], innerWidth))
	}
	lines = append(lines,
		strings.Repeat("─", max(innerWidth, 0)),
		modelPickerCell(hint, innerWidth),
	)

	panelStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("8")).
		Width(innerWidth)
	if layout.innerHeight > 0 {
		panelStyle = panelStyle.Height(layout.innerHeight)
	}
	panel := pickerPanelTitle(panelStyle.Render(strings.Join(lines, "\n")), "Connect Provider")
	panel = lipgloss.NewStyle().MarginLeft(layout.marginLeft).Render(panel)
	return m.renderFullCanvas("\n" + panel)
}

func (m Model) connectPanelRow(provider providerconfig.ConnectableProvider, selected bool, width int) string {
	prefix := "  "
	if selected {
		prefix = "> "
	}
	glyph := "○ "
	status := "not connected"
	if provider.Connected {
		glyph = "● "
		status = "connected"
	}
	statusWidth := min(16, max(width/4, 0))
	nameWidth := max(width-statusWidth, 0)
	row := modelPickerCell(prefix+glyph+sanitizeTerminalText(provider.Name), nameWidth)
	statusCell := modelPickerCell(status, statusWidth)
	if selected {
		return accentStyle.Render(row + statusCell)
	}
	return row + statusStyle.Render(statusCell)
}
