package session

import (
	"context"
	"errors"
)

// ErrSessionNotFound se devuelve al leer una sesion que nunca recibio un evento.
var ErrSessionNotFound = errors.New("session not found")

// PendingTool representa una tool call durable que sigue abierta en la
// proyeccion PendingToolCalls.
type PendingTool struct {
	CallID   string
	ToolName string
}

// SessionSummary es la fila del historial de chats para la sidebar: el ID de la
// sesion y un Title derivado del primer mensaje del usuario. Title queda "" si la
// sesion aun no tiene mensaje de usuario (el frontend cae a un placeholder). Cwd
// es la carpeta de trabajo en que se creo la sesion (del ultimo Session.Cwd); ""
// en sesiones viejas anteriores a la captura de carpeta. La sidebar agrupa por Cwd.
type SessionSummary struct {
	ID    string
	Title string
	Cwd   string
}

// titleMaxRunes es el largo maximo del Title de una sesion. Un corte por rune
// (no por byte) mantiene la sidebar legible sin romper caracteres multibyte.
const titleMaxRunes = 80

// truncateTitle recorta el texto a titleMaxRunes runes. Es un corte simple: no
// busca limites de palabra, basta para la sidebar. Lo comparten MemoryStore y
// SQLiteStore para que el Title sea identico en ambos.
func truncateTitle(text string) string {
	r := []rune(text)
	if len(r) <= titleMaxRunes {
		return text
	}
	return string(r[:titleMaxRunes])
}

// Store es la persistencia durable de la sesion. El log de eventos es la fuente
// de verdad; los mensajes son una proyeccion derivada. M1 implementa una version
// en memoria; M10 agrega SQLite detras de esta misma interface.
type Store interface {
	// AppendEvent agrega ev al log durable de sessionID, le asigna el siguiente
	// Seq monotonico y lo devuelve. Crea la sesion si es su primer evento. El
	// SessionID y Seq que traiga ev se ignoran: los fija el Store.
	AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error)

	// LoadSession devuelve el agregado de la sesion. ErrSessionNotFound si la
	// sesion nunca recibio un evento.
	LoadSession(ctx context.Context, sessionID string) (Session, error)

	// Messages reproyecta los mensajes de la sesion en orden de Seq y devuelve
	// solo los materializados por eventos con Seq > sinceSeq. sinceSeq = 0
	// reconstruye el historial completo desde cero. ErrSessionNotFound si la
	// sesion no existe.
	Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error)

	// Sessions devuelve un resumen por sesion con al menos un evento, ordenado
	// por actividad mas reciente primero. El Title es el primer mensaje del
	// usuario de la sesion (truncado); "" si aun no hay uno. Lista vacia si no
	// hay sesiones. Alimenta el historial de chats de la sidebar.
	Sessions(ctx context.Context) ([]SessionSummary, error)

	// Events devuelve todos los SessionEvent durables de la sesion en orden de
	// Seq con Seq > sinceSeq, reconstruidos fielmente (Kind, Message, Usage,
	// payload de streaming). sinceSeq = 0 trae el log completo. ErrSessionNotFound
	// si la sesion no existe. Rehidrata la conversacion en el frontend.
	Events(ctx context.Context, sessionID string, sinceSeq Seq) ([]SessionEvent, error)

	// Epoch devuelve la foto del contexto vigente de la sesion. El runner la
	// snapshotea al preparar un turno y la re-lee antes de llamar al proveedor: si
	// cambio, descarta el request y reconstruye. ErrSessionNotFound si la sesion no
	// existe.
	Epoch(ctx context.Context, sessionID string) (ContextEpoch, error)

	// PendingToolCalls reconstruye la proyeccion durable de Tool.Called sin
	// Tool.Success/Tool.Failed posterior, en orden de llamada. ErrSessionNotFound
	// si la sesion no existe.
	PendingToolCalls(ctx context.Context, sessionID string) ([]PendingTool, error)

	// DeleteSession borra todos los eventos durables de la sesion. ErrSessionNotFound
	// si la sesion no existe. Las demas sesiones quedan intactas.
	DeleteSession(ctx context.Context, sessionID string) error
}

// CompactionStore amplía Store con la proyeccion efectiva que consume el runner
// y el commit atomico de checkpoints de compactacion. Se mantiene separado
// hasta que todos los stores y decoradores implementen el contrato.
type CompactionStore interface {
	Store
	ContextForRunner(ctx context.Context, sessionID string) (RunnerContext, error)
	CommitCompaction(ctx context.Context, sessionID string, checkpoint CompactionCheckpoint) (Seq, error)
}
