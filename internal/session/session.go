package session

import "encoding/json"

// Seq es la secuencia monotonica que el Store asigna a cada evento de una
// sesion. Empieza en 1 y crece de a 1 por sesion. Define el orden total durable
// del historial; el filtro sinceSeq de Messages se expresa contra este valor.
type Seq int64

// Role es el autor de un mensaje proyectado.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// ToolCall es una tool call del assistant en la proyeccion: el id que empareja
// con el tool_call_id del resultado, el nombre de la tool y los arguments JSON
// crudos (string, para que la frontera Wails lo marshalee sin romperse aunque
// venga vacio).
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Message es la proyeccion de la conversacion para un turno: lleva el texto y el
// Seq del evento que lo materializo (ordena y filtra por sinceSeq). Para el
// round-trip de tool calls tambien lleva ToolCalls (las tool calls que pidio el
// assistant, en orden) y ToolCallID (en el tool result, empareja con la tool
// call del assistant que lo origino). El Message del assistant se materializa
// coalescido (texto + ToolCalls) al cerrar el turno.
type Message struct {
	ID         string
	Role       Role
	Text       string
	ToolCalls  []ToolCall // role=assistant: tool calls del modelo
	ToolCallID string     // role=tool: empareja con la tool call del assistant
	Seq        Seq
}

// SessionEvent es el evento durable: la unica fuente de verdad de la sesion. El
// Store asigna SessionID y Seq al agregarlo; el llamador los deja en cero.
//
// M1 dejo solo SessionID/Seq/Message: un evento podia materializar un mensaje
// (Message != nil) o no aportar a la proyeccion (Message == nil). M3 lo enriquece
// de forma ADITIVA con la taxonomia de streaming. Kind nombra el evento del
// contrato; los campos de payload (Text, CallID, ToolName, Input, Usage) llevan
// el dato segun el Kind y el resto queda en cero. Los eventos delta (Text.Delta,
// Reasoning.Delta, Tool.Input.*) dejan Message == nil para no aportar a la
// proyeccion; el Message del assistant se materializa al cerrar el turno
// (Step.Ended), coalesciendo el texto acumulado con sus tool_calls en un solo
// Message. Asi la proyeccion de M1 (Messages) sigue funcionando sin cambiar el
// fold ni la interface Store.
type SessionEvent struct {
	SessionID string
	Seq       Seq
	Kind      EventKind // taxonomia del contrato; "" en eventos sin taxonomia

	Message *Message // proyeccion: set al cerrar el turno del asistente (Step.Ended, texto + tool_calls) o un tool result

	// Payload de streaming, relevante segun Kind:
	Text     string // Text/Reasoning.Delta/Ended y Tool.Input.Delta (fragmento o texto completo)
	CallID   string // Tool.*
	ToolName string // Tool.Called (y Tool.Success/Tool.Failed en M5)
	// Input lleva el JSON completo y valido de Tool.Called / Tool.Input.Ended. El
	// fragmento crudo de Tool.Input.Delta NO va aqui: json.RawMessage exige JSON
	// valido y la frontera Wails marshalea el evento, asi que ese fragmento viaja en Text.
	Input json.RawMessage
	Usage *Usage // solo Step.Ended
	Error string // mensaje de fallo de una tool (Tool.Failed); M8 lo reutiliza para Step.Failed
	// Diff es un diff unificado SOLO para la UI (Tool.Success de edit/write). No
	// entra en Message, asi que el modelo no lo ve ni consume tokens; se persiste y
	// se replica al rehidratar la sesion.
	Diff string
}

// Session es el agregado durable de una conversacion. En M1 es minimo: solo el
// ID. Agente, modelo y workspace se agregan cuando el runner los necesita
// (M5/M7), no antes.
type Session struct {
	ID string
}
