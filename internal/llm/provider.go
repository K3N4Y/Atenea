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

// Request es la entrada de un turno. En M2 lleva solo el modelo; el fake lo
// ignora (el guion es la fuente de verdad del turno). El runner (M5) le agrega
// System, Messages, Tools y ProviderOpts cuando construye el request desde el
// historial proyectado. Crece sin cambiar la interface Provider.
type Request struct {
	Model string
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
