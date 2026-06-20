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

	mu     sync.Mutex
	text   strings.Builder   // buffer del bloque de texto en curso
	reason strings.Builder   // buffer del bloque de razonamiento en curso
	input  map[string][]byte // input JSON acumulado por callID
	tools  map[string]string // callID -> toolName (mapa de tool calls del turno)
}

// NewPublisher crea el publisher de un turno. assistantMessageID es el ID con el
// que se materializa el Message coalescido del asistente (lo genera el runner en
// M5; en los tests se pasa fijo para poder afirmarlo).
func NewPublisher(store eventAppender, sessionID, assistantMessageID string) *Publisher {
	return &Publisher{
		store:     store,
		sessionID: sessionID,
		asstMsgID: assistantMessageID,
		input:     make(map[string][]byte),
		tools:     make(map[string]string),
	}
}

// Publish traduce un evento del stream a un SessionEvent durable y lo persiste.
// Bufferiza los deltas: en cada *.Ended emite el bloque completo concatenado, y
// en Text.Ended ademas materializa el Message del asistente para la proyeccion.
// Devuelve el error del store si AppendEvent falla.
func (p *Publisher) Publish(ctx context.Context, ev llm.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch ev.Kind {
	case llm.StepStarted:
		return p.emit(ctx, session.SessionEvent{Kind: session.KindStepStarted})
	case llm.StepEnded:
		return p.emit(ctx, session.SessionEvent{Kind: session.KindStepEnded, Usage: toUsage(ev.Usage)})

	case llm.TextStarted:
		p.text.Reset()
		return p.emit(ctx, session.SessionEvent{Kind: session.KindTextStarted})
	case llm.TextDelta:
		p.text.WriteString(ev.Text)
		return p.emit(ctx, session.SessionEvent{Kind: session.KindTextDelta, Text: ev.Text})
	case llm.TextEnded:
		full := p.text.String()
		p.text.Reset()
		return p.emit(ctx, session.SessionEvent{
			Kind:    session.KindTextEnded,
			Text:    full,
			Message: &session.Message{ID: p.asstMsgID, Role: session.RoleAssistant, Text: full},
		})

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
		p.tools[ev.CallID] = ev.ToolName
		p.input[ev.CallID] = append([]byte(nil), ev.Input...)
		return p.emit(ctx, session.SessionEvent{
			Kind: session.KindToolCalled, CallID: ev.CallID, ToolName: ev.ToolName, Input: ev.Input,
		})
	case llm.ToolInputStarted:
		p.input[ev.CallID] = nil
		return p.emit(ctx, session.SessionEvent{Kind: session.KindToolInputStarted, CallID: ev.CallID})
	case llm.ToolInputDelta:
		p.input[ev.CallID] = append(p.input[ev.CallID], ev.Input...)
		return p.emit(ctx, session.SessionEvent{Kind: session.KindToolInputDelta, CallID: ev.CallID, Input: ev.Input})
	case llm.ToolInputEnded:
		return p.emit(ctx, session.SessionEvent{
			Kind: session.KindToolInputEnded, CallID: ev.CallID, Input: p.input[ev.CallID],
		})
	}
	return nil // StepFailed (M8) y kinds sin semantica de sesion en M3 se ignoran
}

// ToolSuccess publica el resultado de una tool local asentada: persiste un
// Tool.Success con el output acotado (lo que vera el modelo) y materializa un
// Message{Role: tool, ID: callID} para que el resultado entre en la proyeccion y
// el modelo lo vea en el siguiente turno. ToolName sale del mapa del turno.
func (p *Publisher) ToolSuccess(ctx context.Context, callID, output string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.emit(ctx, session.SessionEvent{
		Kind:     session.KindToolSuccess,
		CallID:   callID,
		ToolName: p.tools[callID],
		Text:     output,
		Message:  &session.Message{ID: callID, Role: session.RoleTool, Text: output},
	})
}

// ToolFailed publica el fallo de una tool: persiste un Tool.Failed con el mensaje
// del error en Error y materializa un Message{Role: tool} con ese texto, para que
// el modelo vea que la tool fallo y pueda reaccionar. Cubre el fallo de Execute y
// la tool denegada/desconocida (UnknownToolError de M4).
func (p *Publisher) ToolFailed(ctx context.Context, callID string, cause error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	msg := cause.Error()
	return p.emit(ctx, session.SessionEvent{
		Kind:     session.KindToolFailed,
		CallID:   callID,
		ToolName: p.tools[callID],
		Error:    msg,
		Message:  &session.Message{ID: callID, Role: session.RoleTool, Text: msg},
	})
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
