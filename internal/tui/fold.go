package tui

import "atenea/internal/session"

// foldEvent aplica un evento durable a las entradas de la conversacion.
func (m Model) foldEvent(ev EventMsg) Model {
	switch ev.Kind {
	case session.KindTextStarted:
		m = m.openAssistantBlock()
	case session.KindTextDelta:
		if !m.liveOpen() {
			m = m.openAssistantBlock()
		}
		m.lastEntry().text += ev.Text
	case session.KindStepEnded:
		if m.liveOpen() {
			last := m.lastEntry()
			// El bloque en vivo ya tiene el texto streameado: el Message
			// coalescido solo rellena si el streaming no trajo nada.
			if last.text == "" && ev.Message != nil {
				last.text = ev.Message.Text
			}
			last.live = false
		}
	case session.KindToolCalled:
		m.entries = append(m.entries, entry{
			kind: entryTool, callID: ev.CallID, tool: ev.ToolName, status: toolRunning,
		})
	case session.KindToolSuccess:
		m = m.settleTool(ev.CallID, toolOK, "")
	case session.KindToolFailed:
		m = m.settleTool(ev.CallID, toolFailed, ev.Error)
	case session.KindToolPermissionRequested:
		m.entries = append(m.entries, entry{
			kind: entryPermission, callID: ev.CallID, tool: ev.ToolName,
			input: string(ev.Input), sessionID: ev.SessionID,
		})
	case session.KindStepFailed:
		m = m.appendError(ev.Error)
	case "":
		// Evento sin taxonomia: el runner promueve el prompt del usuario como
		// Message{Role: user} sin Kind.
		if ev.Message != nil && ev.Message.Role == session.RoleUser {
			m.entries = append(m.entries, entry{kind: entryUser, text: ev.Message.Text})
		}
	}
	return m
}

// settleTool asienta el desenlace del tool call con ese callID (ok o fallo) y
// retira su solicitud de permiso pendiente: el contrato no trae un evento de
// resolucion propio, el Tool.Success/Tool.Failed del mismo CallID la expresa.
func (m Model) settleTool(callID string, status toolStatus, errMsg string) Model {
	kept := make([]entry, 0, len(m.entries))
	for _, e := range m.entries {
		if e.kind == entryPermission && e.callID == callID {
			continue
		}
		if e.kind == entryTool && e.callID == callID {
			e.status = status
			e.err = errMsg
		}
		kept = append(kept, e)
	}
	m.entries = kept
	return m
}

// pendingPermission devuelve la entrada completa de la solicitud pendiente
// (con CallID y el SessionID que trajo el evento) y true si hay una.
func (m Model) pendingPermission() (entry, bool) {
	for _, e := range m.entries {
		if e.kind == entryPermission {
			return e, true
		}
	}
	return entry{}, false
}

// appendError agrega un bloque de error al final de la conversacion; lo
// comparten el fallo duro del step y el fin de corrida con error.
func (m Model) appendError(text string) Model {
	m.entries = append(m.entries, entry{kind: entryError, text: text})
	return m
}

// openAssistantBlock abre un bloque assistant en vivo al final de la conversacion.
func (m Model) openAssistantBlock() Model {
	m.entries = append(m.entries, entry{kind: entryAssistant, live: true})
	return m
}

// lastEntry devuelve la ultima entrada para mutarla; el caller garantiza que existe.
func (m Model) lastEntry() *entry {
	return &m.entries[len(m.entries)-1]
}

// liveOpen indica si la ultima entrada es un bloque assistant en vivo sin cerrar.
func (m Model) liveOpen() bool {
	if len(m.entries) == 0 {
		return false
	}
	last := m.lastEntry()
	return last.kind == entryAssistant && last.live
}
