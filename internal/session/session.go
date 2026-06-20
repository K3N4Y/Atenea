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

// Message es la proyeccion de la conversacion para un turno. En M1 lleva texto
// plano y el Seq del evento que lo materializo (ordena y filtra por sinceSeq).
// Las partes ricas (tool calls, reasoning) y el coalescing de deltas de
// streaming llegan con el publisher en M3, sin cambiar esta forma minima.
type Message struct {
	ID   string
	Role Role
	Text string
	Seq  Seq
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
// proyeccion; solo el evento que cierra un bloque de texto del asistente
// (Text.Ended) materializa el Message ya coalescido. Asi la proyeccion de M1
// (Messages) sigue funcionando sin cambiar el fold ni la interface Store.
type SessionEvent struct {
	SessionID string
	Seq       Seq
	Kind      EventKind // taxonomia del contrato; "" en eventos sin taxonomia

	Message *Message // proyeccion: set solo al cerrar un bloque de texto del asistente

	// Payload de streaming, relevante segun Kind:
	Text     string          // Text.Delta/Ended, Reasoning.Delta/Ended (fragmento o texto completo)
	CallID   string          // Tool.*
	ToolName string          // Tool.Called (y Tool.Success/Tool.Failed en M5)
	Input    json.RawMessage // Tool.Called / Tool.Input.* (input JSON crudo o coalescido)
	Usage    *Usage          // solo Step.Ended
	Error    string          // mensaje de fallo de una tool (Tool.Failed); M8 lo reutiliza para Step.Failed
}

// Session es el agregado durable de una conversacion. En M1 es minimo: solo el
// ID. Agente, modelo y workspace se agregan cuando el runner los necesita
// (M5/M7), no antes.
type Session struct {
	ID string
}
