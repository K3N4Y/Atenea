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

	// KindToolPermissionRequested asks the user for approval before settling a
	// gated tool call (ask-before-run). The runner emits it before blocking on
	// the PermissionGate; the UI shows it as an Approve/Deny prompt. It carries
	// no Message: the projection ignores it. The outcome is expressed by the
	// subsequent Tool.Success or Tool.Failed, not by a separate resolution event.
	KindToolPermissionRequested EventKind = "Tool.Permission.Requested"

	// KindSessionTitle lleva en Text el titulo generado de la sesion (resumen
	// corto del primer mensaje). La proyeccion Sessions lo prefiere sobre el
	// primer mensaje del usuario; el ultimo Session.Title es el vigente. No
	// materializa Message: no aporta a la conversacion, solo a la sidebar.
	KindSessionTitle EventKind = "Session.Title"

	// KindSessionCwd lleva en Text la carpeta de trabajo en que se creo la sesion.
	// La app lo emite al primer prompt; la proyeccion Sessions lo expone en
	// SessionSummary.Cwd para que la sidebar agrupe los chats por carpeta. El
	// ultimo Session.Cwd es el vigente. No materializa Message.
	KindSessionCwd EventKind = "Session.Cwd"

	// KindComposerPrompt lleva en Text el prompt literal enviado desde el
	// composer TUI. No materializa Message ni entra al contexto del modelo: solo
	// permite rehidratar el historial de Up/Down entre procesos.
	KindComposerPrompt EventKind = "Composer.Prompt"

	KindContextCompacted EventKind = "Context.Compacted"
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
