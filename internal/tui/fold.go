package tui

import (
	"time"
	"unicode/utf8"

	"atenea/internal/session"
)

// foldEvent aplica un evento durable a las entradas de la conversacion.
func (m Model) foldEvent(ev EventMsg) Model {
	switch ev.Kind {
	case session.KindTextStarted:
		m = m.openAssistantBlock()
	case session.KindTextDelta:
		// Apertura defensiva: el delta puede llegar sin Text.Started.
		if !m.assistantOpen() {
			m = m.openAssistantBlock()
		}
		m.lastEntry().text += ev.Text
	case session.KindReasoningStarted:
		m = m.openReasoningBlock()
	case session.KindReasoningDelta:
		// Apertura defensiva: el delta puede llegar sin Reasoning.Started.
		if !m.reasoningOpen() {
			m = m.openReasoningBlock()
		}
		m.lastEntry().text += ev.Text
	case session.KindReasoningEnded:
		if m.reasoningOpen() {
			last := m.lastEntry()
			last.fillCoalesced(ev.Text)
			last.closeThinking()
		}
	case session.KindStepEnded:
		// El fin del step cierra tambien un pensamiento que siga en vivo
		// (cierre defensivo: el step puede morir pensando, por cancelacion o
		// error del proveedor, sin Reasoning.Ended de por medio).
		m = m.closeThinkingBlocks()
		if m.assistantOpen() {
			last := m.lastEntry()
			if ev.Message != nil {
				last.fillCoalesced(ev.Message.Text)
			}
			last.live = false
		}
	case session.KindToolCalled:
		m.entries = append(m.entries, entry{
			kind: entryTool, callID: ev.CallID, tool: ev.ToolName, status: toolRunning,
			input: string(ev.Input),
		})
	case session.KindToolSuccess:
		m = m.settleTool(ev.CallID, toolOK, "", ev.Text, ev.Diff)
	case session.KindToolFailed:
		m = m.settleTool(ev.CallID, toolFailed, ev.Error, "", "")
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
// output es el resultado de Tool.Success (ev.Text) y queda en la entrada para
// el preview del transcript; Tool.Failed pasa "" (su detalle viaja en errMsg).
// diff es el diff unificado de Tool.Success de edit/write (ev.Diff): cuando no
// esta vacio la vista lo muestra en lugar del preview del output.
// Un present_plan asentado con exito agrega al final la oferta de aprobacion
// del plan (y ejecutar / n seguir en plan).
func (m Model) settleTool(callID string, status toolStatus, errMsg, output, diff string) Model {
	planPresented := false
	kept := make([]entry, 0, len(m.entries))
	for _, e := range m.entries {
		if e.kind == entryPermission && e.callID == callID {
			continue
		}
		if e.kind == entryTool && e.callID == callID {
			e.status = status
			e.err = errMsg
			e.output = output
			e.diff = diff
			if e.tool == "present_plan" && status == toolOK {
				planPresented = true
			}
		}
		kept = append(kept, e)
	}
	m.entries = kept
	if planPresented {
		m.entries = append(m.entries, entry{kind: entryPlanApproval})
	}
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

// hasPendingPlan indica si hay una oferta de aprobacion de plan pendiente.
// A diferencia de pendingPermission no devuelve la entrada: la oferta no
// carga datos (ni CallID ni SessionID), solo existe o no.
func (m Model) hasPendingPlan() bool {
	for _, e := range m.entries {
		if e.kind == entryPlanApproval {
			return true
		}
	}
	return false
}

// removePendingPlan retira la oferta de aprobacion de plan de la conversacion.
func (m Model) removePendingPlan() Model {
	kept := make([]entry, 0, len(m.entries))
	for _, e := range m.entries {
		if e.kind == entryPlanApproval {
			continue
		}
		kept = append(kept, e)
	}
	m.entries = kept
	return m
}

// appendError agrega un bloque de error al final de la conversacion; lo
// comparten el fallo duro del step y el fin de corrida con error.
func (m Model) appendError(text string) Model {
	m.entries = append(m.entries, entry{kind: entryError, text: text})
	return m
}

// openAssistantBlock abre un bloque assistant en vivo al final de la
// conversacion. Antes cierra cualquier pensamiento que siga en vivo: que
// arranque la respuesta implica que el pensamiento termino, aunque el runner
// no haya emitido Reasoning.Ended (cierre defensivo).
func (m Model) openAssistantBlock() Model {
	m = m.closeThinkingBlocks()
	m.entries = append(m.entries, entry{kind: entryAssistant, live: true})
	return m
}

// openReasoningBlock abre un bloque de pensamiento en vivo al final de la
// conversacion, capturando el instante de apertura para computar la duracion
// que muestra el resumen colapsado.
func (m Model) openReasoningBlock() Model {
	m.entries = append(m.entries, entry{kind: entryReasoning, live: true, startedAt: time.Now()})
	return m
}

// fillCoalesced rellena el bloque en vivo con el texto coalescido que trae su
// evento de cierre (el Message de Step.Ended, el Text de Reasoning.Ended) SOLO
// si el streaming no trajo nada, y lo revela completo de inmediato: el reveal
// suaviza el ritmo de los deltas, no el de un texto que ya llego entero.
func (e *entry) fillCoalesced(text string) {
	if e.text != "" || text == "" {
		return
	}
	e.text = text
	e.revealed = utf8.RuneCountInString(text)
}

// closeThinking cierra el bloque de pensamiento: apaga live y fija la duracion
// desde el instante de apertura. Con el backlog ya drenado la vista colapsa el
// bloque a la linea de resumen (ver renderThinking).
func (e *entry) closeThinking() {
	e.live = false
	e.duration = time.Since(e.startedAt)
}

// closeThinkingBlocks cierra cualquier bloque de pensamiento que siga en vivo.
// Es el cierre defensivo: el runner podria no emitir Reasoning.Ended, y tanto
// abrir un bloque assistant como cerrar el step implican que el pensamiento
// termino.
func (m Model) closeThinkingBlocks() Model {
	for i := range m.entries {
		if m.entries[i].kind == entryReasoning && m.entries[i].live {
			m.entries[i].closeThinking()
		}
	}
	return m
}

// lastEntry devuelve la ultima entrada para mutarla; el caller garantiza que existe.
func (m Model) lastEntry() *entry {
	return &m.entries[len(m.entries)-1]
}

// lastLiveIs indica si la ultima entrada es un bloque del kind dado que sigue
// en vivo: el fold solo acumula deltas sobre la cola de la conversacion.
func (m Model) lastLiveIs(kind entryKind) bool {
	if len(m.entries) == 0 {
		return false
	}
	last := m.lastEntry()
	return last.kind == kind && last.live
}

// assistantOpen indica si la ultima entrada es un bloque assistant en vivo sin cerrar.
func (m Model) assistantOpen() bool { return m.lastLiveIs(entryAssistant) }

// reasoningOpen indica si la ultima entrada es un bloque de pensamiento en
// vivo sin cerrar (espejo de assistantOpen para entryReasoning).
func (m Model) reasoningOpen() bool { return m.lastLiveIs(entryReasoning) }
