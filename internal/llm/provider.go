package llm

import (
	"context"
	"encoding/json"
)

// Provider es la frontera con el modelo. Stream produce exactamente UN turno:
// emite los eventos del turno por el channel y lo CIERRA al terminar. El runner
// (M5) lo consume con `for ev := range out`. Cancelar ctx interrumpe el turno
// (equivale a una interrupcion de usuario) y tambien cierra el channel: ningun
// receptor queda colgado. M2 implementa un fake en memoria; el adaptador real
// (Claude/Anthropic) entra en M10 detras de esta misma interface.
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

// ToolCallPart es una tool call del assistant proyectada en el historial: el id
// que empareja con el tool_call_id del resultado, el nombre de la tool y los
// arguments JSON crudos tal como los emitio el modelo (no se re-serializan).
type ToolCallPart struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Message es un mensaje del historial proyectado en el formato del proveedor.
// M5 lo construye desde session.Message (Role/Text) al armar el Request; el
// adaptador real (M10) lo traduce a los bloques de su SDK.
type Message struct {
	Role       string
	Text       string
	ToolCalls  []ToolCallPart // role=assistant: tool calls del modelo
	ToolCallID string         // role=tool: empareja con la tool call del assistant
}

// Request es la entrada de un turno. En M2 lleva solo el modelo; el fake lo
// ignora (el guion es la fuente de verdad del turno). El runner (M5) lo puebla
// con el historial proyectado (Messages) y las tools materializadas (Tools) al
// construir el turno. System (system context/baseline) y ProviderOpts (prompt
// cache key) llegan en M7. Crece sin cambiar la interface Provider.
type Request struct {
	Model    string
	Messages []Message // historial proyectado convertido al formato del proveedor
	Tools    []ToolDef // schemas materializados por el registry (M4)
}

// EventKind clasifica cada evento del stream del proveedor. El conjunto refleja
// 1:1 los eventos de sesion del contrato del loop (ver "Eventos publicados" en
// docs/atenea-agent-loop.md), menos los que produce el runner y no el proveedor
// (Tool.Success / Tool.Failed). El publisher (M3) mapea estos kinds a eventos
// durables de sesion; M2 solo los define y el fake los reproduce.
type EventKind int

const (
	StepStarted EventKind = iota // arranca el turno            -> Step.Started
	StepEnded                    // cierra el turno con tokens  -> Step.Ended (lleva Usage)
	StepFailed                   // fallo del stream            -> Step.Failed

	TextStarted // abre un bloque de texto      -> Text.Started
	TextDelta   // fragmento de texto           -> Text.Delta   (lleva Text)
	TextEnded   // cierra el bloque de texto    -> Text.Ended

	ReasoningStarted // abre razonamiento        -> Reasoning.Started
	ReasoningDelta   // fragmento de razonamiento -> Reasoning.Delta (lleva Text)
	ReasoningEnded   // cierra razonamiento      -> Reasoning.Ended

	ToolCall // el modelo invoca una tool       -> Tool.Called (lleva CallID, ToolName, Input)

	ToolInputStarted // abre el input de la tool -> Tool.Input.Started (lleva CallID)
	ToolInputDelta   // fragmento del input JSON -> Tool.Input.Delta   (lleva CallID, Input)
	ToolInputEnded   // cierra el input de la tool -> Tool.Input.Ended (lleva CallID)
)

// Event es un evento del stream de un turno. Kind decide que campos son
// relevantes; el resto queda en cero. Input es el JSON crudo del input de una
// tool: se parsea con json.Unmarshal, nunca por match de string (el modelo puede
// escapar el JSON distinto entre turnos). Usage solo viene en StepEnded.
type Event struct {
	Kind     EventKind
	CallID   string          // ToolCall / ToolInput*
	ToolName string          // ToolCall
	Input    json.RawMessage // ToolCall / ToolInputDelta: input JSON (crudo)
	Text     string          // TextDelta / ReasoningDelta
	Usage    *Usage          // solo StepEnded
	// ProviderExecuted marca una ToolCall que el proveedor ejecuto el mismo: el
	// runner NO la asienta localmente, solo la persiste.
	ProviderExecuted bool
}

// Usage son los tokens reportados al cerrar el turno (StepEnded). El proveedor
// real (M10) los completa; el fake los guiona; el publisher (M3) los persiste en
// Step.Ended.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	ReasoningTokens  int
	CacheReadTokens  int
	CacheWriteTokens int
}
