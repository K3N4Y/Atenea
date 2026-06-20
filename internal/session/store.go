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

	// Epoch devuelve la foto del contexto vigente de la sesion. El runner la
	// snapshotea al preparar un turno y la re-lee antes de llamar al proveedor: si
	// cambio, descarta el request y reconstruye. ErrSessionNotFound si la sesion no
	// existe.
	Epoch(ctx context.Context, sessionID string) (ContextEpoch, error)

	// PendingToolCalls reconstruye la proyeccion durable de Tool.Called sin
	// Tool.Success/Tool.Failed posterior, en orden de llamada. ErrSessionNotFound
	// si la sesion no existe.
	PendingToolCalls(ctx context.Context, sessionID string) ([]PendingTool, error)
}
