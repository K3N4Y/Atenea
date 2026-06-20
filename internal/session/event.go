package session

// EventKind nombra cada evento durable de sesion dentro de la taxonomia del
// contrato de streaming (ver "Eventos publicados" en docs/atenea-agent-loop.md).
// Es un string estable, no un int: el frontend (M9) lo mapea 1:1 a la UI y un
// nombre legible se entiende en logs y en el store. El publisher (M3) es el
// unico productor. Los eventos de M1 sin taxonomia quedan con Kind == "".
type EventKind string

const (
	KindStepStarted EventKind = "Step.Started"
	KindStepEnded   EventKind = "Step.Ended"
	KindStepFailed  EventKind = "Step.Failed" // lo emite M8 (manejo de fallos)

	KindTextStarted EventKind = "Text.Started"
	KindTextDelta   EventKind = "Text.Delta"
	KindTextEnded   EventKind = "Text.Ended"

	KindReasoningStarted EventKind = "Reasoning.Started"
	KindReasoningDelta   EventKind = "Reasoning.Delta"
	KindReasoningEnded   EventKind = "Reasoning.Ended"

	KindToolInputStarted EventKind = "Tool.Input.Started"
	KindToolInputDelta   EventKind = "Tool.Input.Delta"
	KindToolInputEnded   EventKind = "Tool.Input.Ended"

	KindToolCalled  EventKind = "Tool.Called"
	KindToolSuccess EventKind = "Tool.Success" // lo emite el runner en M5
	KindToolFailed  EventKind = "Tool.Failed"  // lo emite el runner en M5/M8
)

// Usage son los tokens del turno que el publisher persiste en Step.Ended. Es un
// espejo de llm.Usage: la direccion de import es runner -> {session, llm}, asi
// que session no depende de llm; el publisher copia los campos al cruzar la
// frontera. Solo viene en eventos Step.Ended.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	ReasoningTokens  int
	CacheReadTokens  int
	CacheWriteTokens int
}
