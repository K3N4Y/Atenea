package session

import (
	"context"
	"errors"
)

// ErrSessionNotFound se devuelve al leer una sesion que nunca recibio un evento.
var ErrSessionNotFound = errors.New("session not found")

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
}
