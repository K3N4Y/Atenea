package event

import "atenea/internal/session"

// EmitFunc es la frontera con Wails. En produccion envuelve runtime.EventsEmit;
// en tests es un fake que registra. Su forma copia la de runtime.EventsEmit
// (eventName + payload variadico) para no acoplar event/ ni los tests a Wails.
type EmitFunc func(eventName string, optionalData ...interface{})

// Bus reenvia eventos de sesion al frontend. Es la unica pieza que conoce el
// nombre del canal (session:<id>).
type Bus struct {
	emit EmitFunc
}

// NewBus crea el bus sobre una EmitFunc. emit nil deja un bus inerte (no-op),
// util en produccion antes de que Wails entregue el ctx en startup.
func NewBus(emit EmitFunc) *Bus { return &Bus{emit: emit} }

// Publish reenvia un evento durable de sesion al canal session:<id>. El evento
// ya trae Seq y SessionID asignados por el Store.
func (b *Bus) Publish(ev session.SessionEvent) {
	if b.emit == nil {
		return
	}
	b.emit("session:"+ev.SessionID, ev)
}

// PublishOn reenvia el evento a un canal de sesion explicito (session:<channel>),
// sin derivarlo de ev.SessionID. Lo usa el surfacing del permiso de un subagente:
// el evento del hijo (ev.SessionID = childID) se emite en el canal del PADRE para
// que la UI, que ya escucha ese canal, vea la solicitud y resuelva con el childID
// del payload.
func (b *Bus) PublishOn(channel string, ev session.SessionEvent) {
	if b.emit == nil {
		return
	}
	b.emit("session:"+channel, ev)
}

// PublishError reenvia al canal session:<id>:error un error duro que corto una
// corrida de Run. No es un SessionEvent durable: es el cierre observable de una
// actividad que fallo.
func (b *Bus) PublishError(sessionID string, err error) {
	if b.emit == nil {
		return
	}
	b.emit("session:"+sessionID+":error", err.Error())
}
