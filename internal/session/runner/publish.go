package runner

import (
	"context"
	"strings"
	"sync"

	"atenea/internal/llm"
	"atenea/internal/session"
)

// eventAppender es lo unico que el Publisher necesita del Store: agregar eventos
// durables. Aceptar la interface minima (no el Store completo) deja el publisher
// testeable con un spy de un solo metodo y honra "acepta interfaces chicas". El
// session.Store real la cumple; en M5 el runner le pasa el Store de la sesion.
type eventAppender interface {
	AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error)
}

// Publisher traduce el stream de un turno (llm.Event) a eventos durables de
// sesion (SessionEvent) con la taxonomia del contrato, y bufferiza los deltas
// para emitir tambien el bloque cerrado con el contenido completo. Es de un solo
// turno: el runner (M5) crea uno por turno con el assistantMessageID de ese
// turno. En M5 el loop de consumo publica desde la goroutine principal (Publish)
// mientras las goroutines de settle publican el resultado (ToolSuccess/
// ToolFailed); el candado serializa los appends en un orden total y protege los
// buffers y mapas. M3 anticipo "el candado llega en M5 con su test -race".
type Publisher struct {
	store     eventAppender
	sessionID string
	asstMsgID string // assistantMessageID del turno

	mu                   sync.Mutex
	text                 strings.Builder   // buffer del bloque de texto en curso
	assistantText        strings.Builder   // texto del assistant acumulado del turno (se materializa en Step.Ended)
	reason               strings.Builder   // buffer del bloque de razonamiento en curso
	input                map[string][]byte // input JSON acumulado por callID
	tools                map[string]string // callID -> toolName (mapa de tool calls del turno)
	order                []string          // orden de Tool.Called del turno
	settled              map[string]bool   // callID -> ya tiene Tool.Success/Tool.Failed
	estimatedInputTokens int
}

// NewPublisher crea el publisher de un turno. assistantMessageID es el ID con el
// que se materializa el Message coalescido del asistente (lo genera el runner en
// M5; en los tests se pasa fijo para poder afirmarlo).
func NewPublisher(store eventAppender, sessionID, assistantMessageID string, estimatedInputTokens int) *Publisher {
	return &Publisher{
		store:                store,
		sessionID:            sessionID,
		asstMsgID:            assistantMessageID,
		input:                make(map[string][]byte),
		tools:                make(map[string]string),
		settled:              make(map[string]bool),
		estimatedInputTokens: estimatedInputTokens,
	}
}

// Publish traduce un evento del stream a un SessionEvent durable y lo persiste.
// Bufferiza los deltas: en cada *.Ended emite el bloque completo concatenado, y
// al cerrar el turno (Step.Ended) materializa el unico Message del asistente,
// coalesciendo el texto acumulado con sus tool_calls. Devuelve el error del
// store si AppendEvent falla.
func (p *Publisher) Publish(ctx context.Context, ev llm.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch ev.Kind {
	case llm.StepStarted:
		return p.emit(ctx, session.SessionEvent{
			Kind:  session.KindStepStarted,
			Usage: &session.Usage{InputTokens: p.estimatedInputTokens},
		})
	case llm.StepRetrying:
		return p.emit(ctx, session.SessionEvent{Kind: session.KindStepRetrying, Text: ev.Text})
	case llm.StepEnded:
		// Materializa aqui el unico Message del assistant del turno, coalesciendo el
		// texto acumulado con los tool_calls (en orden de Tool.Called). Si no hubo
		// texto ni tool calls es un turno vacio y no se materializa Message.
		var toolCalls []session.ToolCall
		for _, callID := range p.order {
			toolCalls = append(toolCalls, session.ToolCall{
				ID:        callID,
				Name:      p.tools[callID],
				Arguments: string(p.input[callID]),
			})
		}
		out := session.SessionEvent{Kind: session.KindStepEnded, Usage: toUsage(ev.Usage)}
		text := p.assistantText.String()
		if text != "" || len(toolCalls) > 0 {
			out.Message = &session.Message{
				ID:        p.asstMsgID,
				Role:      session.RoleAssistant,
				Text:      text,
				ToolCalls: toolCalls,
			}
		}
		return p.emit(ctx, out)

	case llm.TextStarted:
		p.text.Reset()
		return p.emit(ctx, session.SessionEvent{Kind: session.KindTextStarted})
	case llm.TextDelta:
		p.text.WriteString(ev.Text)
		return p.emit(ctx, session.SessionEvent{Kind: session.KindTextDelta, Text: ev.Text})
	case llm.TextEnded:
		full := p.text.String()
		p.text.Reset()
		// El Message del assistant ya no se materializa aqui: se acumula el texto y
		// se coalesce con los tool_calls al cerrar el turno (Step.Ended). Asi un turno
		// produce un solo Message de assistant con texto + tool calls juntos.
		p.assistantText.WriteString(full)
		return p.emit(ctx, session.SessionEvent{Kind: session.KindTextEnded, Text: full})

	case llm.ReasoningStarted:
		p.reason.Reset()
		return p.emit(ctx, session.SessionEvent{Kind: session.KindReasoningStarted})
	case llm.ReasoningDelta:
		p.reason.WriteString(ev.Text)
		return p.emit(ctx, session.SessionEvent{Kind: session.KindReasoningDelta, Text: ev.Text})
	case llm.ReasoningEnded:
		full := p.reason.String()
		p.reason.Reset()
		return p.emit(ctx, session.SessionEvent{Kind: session.KindReasoningEnded, Text: full})

	case llm.ToolCall:
		if _, ok := p.tools[ev.CallID]; !ok {
			p.order = append(p.order, ev.CallID)
		}
		p.tools[ev.CallID] = ev.ToolName
		p.input[ev.CallID] = append([]byte(nil), ev.Input...)
		return p.emit(ctx, session.SessionEvent{
			Kind: session.KindToolCalled, CallID: ev.CallID, ToolName: ev.ToolName, Input: ev.Input,
		})
	case llm.ToolInputStarted:
		p.input[ev.CallID] = nil
		return p.emit(ctx, session.SessionEvent{Kind: session.KindToolInputStarted, CallID: ev.CallID})
	case llm.ToolInputDelta:
		// El fragmento de input llega crudo y partido: JSON invalido por si solo. Va
		// en Text (string), no en Input (json.RawMessage), porque la frontera Wails
		// marshalea el evento y json.RawMessage exige JSON valido. Input se reserva
		// para el JSON completo (Tool.Called / Tool.Input.Ended). El coalescido sigue
		// acumulando los bytes crudos.
		p.input[ev.CallID] = append(p.input[ev.CallID], ev.Input...)
		return p.emit(ctx, session.SessionEvent{Kind: session.KindToolInputDelta, CallID: ev.CallID, Text: string(ev.Input)})
	case llm.ToolInputEnded:
		return p.emit(ctx, session.SessionEvent{
			Kind: session.KindToolInputEnded, CallID: ev.CallID, Input: p.input[ev.CallID],
		})
	}
	return nil // StepFailed (M8) y kinds sin semantica de sesion en M3 se ignoran
}

// ToolPermissionRequested publishes the approval request of a gated tool call
// (ask-before-run): it persists a Tool.Permission.Requested with the callID and
// the turn's toolName so the UI can show the Approve/Deny prompt. It does not
// materialize a Message (it does not feed the projection) nor mark settled: the
// outcome is persisted by the subsequent Tool.Success/Tool.Failed. The toolName
// comes from the turn's map (already populated by the prior Tool.Called in the
// consumption loop).
func (p *Publisher) ToolPermissionRequested(ctx context.Context, callID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.emit(ctx, session.SessionEvent{
		Kind:     session.KindToolPermissionRequested,
		CallID:   callID,
		ToolName: p.tools[callID],
	})
}

// ToolSuccess publica el resultado de una tool local asentada: persiste un
// Tool.Success con el output acotado (lo que vera el modelo) y materializa un
// Message{Role: tool, ID: callID} para que el resultado entre en la proyeccion y
// el modelo lo vea en el siguiente turno. ToolName sale del mapa del turno. diff
// es solo para la UI (edit/write): viaja en SessionEvent.Diff y NUNCA en el
// Message, asi el modelo no lo ve.
func (p *Publisher) ToolSuccess(ctx context.Context, callID, output, diff string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.settled[callID] {
		return nil
	}
	if err := p.emit(ctx, session.SessionEvent{
		Kind:     session.KindToolSuccess,
		CallID:   callID,
		ToolName: p.tools[callID],
		Text:     output,
		Diff:     diff,
		Message:  &session.Message{ID: callID, Role: session.RoleTool, Text: output, ToolCallID: callID},
	}); err != nil {
		return err
	}
	p.settled[callID] = true
	return nil
}

// ToolFailed publica el fallo de una tool: persiste un Tool.Failed con el mensaje
// del error en Error y materializa un Message{Role: tool} con ese texto, para que
// el modelo vea que la tool fallo y pueda reaccionar. Cubre el fallo de Execute y
// la tool denegada/desconocida (UnknownToolError de M4).
func (p *Publisher) ToolFailed(ctx context.Context, callID string, cause error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failTool(ctx, callID, cause)
}

// StepFailed publica Step.Failed para cerrar el step actual con la causa
// observable.
func (p *Publisher) StepFailed(ctx context.Context, cause error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	msg := cause.Error()
	return p.emit(ctx, session.SessionEvent{
		Kind:  session.KindStepFailed,
		Error: msg,
	})
}

// FailUnresolvedTools cierra con Tool.Failed las Tool.Called del turno actual
// que aun no tienen Tool.Success/Tool.Failed persistido.
func (p *Publisher) FailUnresolvedTools(ctx context.Context, cause error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, callID := range p.order {
		if p.settled[callID] {
			continue
		}
		if err := p.failTool(ctx, callID, cause); err != nil {
			return err
		}
	}
	return nil
}

// failTool persiste Tool.Failed. El llamador debe tener p.mu tomado.
func (p *Publisher) failTool(ctx context.Context, callID string, cause error) error {
	if p.settled[callID] {
		return nil
	}
	msg := cause.Error()
	if err := p.emit(ctx, session.SessionEvent{
		Kind:     session.KindToolFailed,
		CallID:   callID,
		ToolName: p.tools[callID],
		Error:    msg,
		Message:  &session.Message{ID: callID, Role: session.RoleTool, Text: msg, ToolCallID: callID},
	}); err != nil {
		return err
	}
	p.settled[callID] = true
	return nil
}

// emit fija el SessionID del turno y persiste el evento. Aisla el unico punto que
// toca el store.
func (p *Publisher) emit(ctx context.Context, ev session.SessionEvent) error {
	_, err := p.store.AppendEvent(ctx, p.sessionID, ev)
	return err
}

// toUsage copia los tokens de llm.Usage al espejo de session, cruzando la
// frontera sin acoplar session a llm. nil -> nil (un Step sin tokens).
func toUsage(u *llm.Usage) *session.Usage {
	if u == nil {
		return nil
	}
	return &session.Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		ReasoningTokens:  u.ReasoningTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
}
