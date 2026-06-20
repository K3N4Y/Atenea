package session

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
// Store asigna SessionID y Seq al agregarlo; el llamador los deja en cero. En M1
// un evento puede materializar un mensaje (Message != nil) o no aportar a la
// proyeccion (Message == nil). La taxonomia de streaming
// (Step.* / Text.* / Reasoning.* / Tool.*) la agrega el publisher en M3 sobre
// esta misma estructura.
type SessionEvent struct {
	SessionID string
	Seq       Seq
	Message   *Message
}

// Session es el agregado durable de una conversacion. En M1 es minimo: solo el
// ID. Agente, modelo y workspace se agregan cuando el runner los necesita
// (M5/M7), no antes.
type Session struct {
	ID string
}
