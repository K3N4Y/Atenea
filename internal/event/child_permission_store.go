package event

import (
	"context"
	"sync"

	"atenea/internal/session"
)

// ChildPermissionStore decora el store en memoria del runner hijo de un subagente
// para hacer surfacing del ask-before-run en la UI: tras un AppendEvent exitoso, si
// el evento es del ciclo de vida de un permiso (Tool.Permission.Requested y la
// resolucion Tool.Success/Tool.Failed), lo publica en el canal del PADRE
// (session:<parentSessionID>, el que ya escucha la UI), conservando en el payload el
// SessionID del hijo y el Seq que el store le asigno. El resto de los eventos del
// hijo no se emiten, asi el log del padre no se contamina con el turno completo del
// subagente. La UI ve la solicitud, muestra Aprobar/Denegar y resuelve con
// (childID, callID) via ResolveToolPermission (el gate compartido keyea por ese par).
type ChildPermissionStore struct {
	inner           session.Store
	bus             *Bus
	parentSessionID string
	mu              sync.Mutex
}

// NewChildPermissionStore envuelve inner para surfacing de los eventos de permiso
// del hijo en el canal del padre.
func NewChildPermissionStore(parentSessionID string, inner session.Store, bus *Bus) *ChildPermissionStore {
	return &ChildPermissionStore{inner: inner, bus: bus, parentSessionID: parentSessionID}
}

// var _ session.Store = (*ChildPermissionStore)(nil) asegura en compilacion que
// el decorador cumple la interface.
var _ session.Store = (*ChildPermissionStore)(nil)

// isPermissionEvent decide si un evento del hijo se surfacing al padre: la solicitud
// de permiso y su resolucion (exito/fallo de la tool gateada). El callID empareja la
// solicitud con su resolucion en la UI.
func isPermissionEvent(kind session.EventKind) bool {
	switch kind {
	case session.KindToolPermissionRequested, session.KindToolSuccess, session.KindToolFailed:
		return true
	default:
		return false
	}
}

// AppendEvent delega en inner bajo candado y, si el append fue exitoso y el evento
// es de permiso, lo sella con el SessionID del hijo y el Seq, y lo publica en el
// canal del padre. Si inner falla, devuelve el error sin emitir.
func (s *ChildPermissionStore) AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	seq, err := s.inner.AppendEvent(ctx, sessionID, ev)
	if err != nil {
		return seq, err
	}
	if isPermissionEvent(ev.Kind) {
		ev.SessionID = sessionID
		ev.Seq = seq
		s.bus.PublishOn(s.parentSessionID, ev)
	}
	return seq, nil
}

// LoadSession delega sin candado ni emision.
func (s *ChildPermissionStore) LoadSession(ctx context.Context, sessionID string) (session.Session, error) {
	return s.inner.LoadSession(ctx, sessionID)
}

// Messages delega sin candado ni emision.
func (s *ChildPermissionStore) Messages(ctx context.Context, sessionID string, sinceSeq session.Seq) ([]session.Message, error) {
	return s.inner.Messages(ctx, sessionID, sinceSeq)
}

// Sessions delega sin candado ni emision.
func (s *ChildPermissionStore) Sessions(ctx context.Context) ([]session.SessionSummary, error) {
	return s.inner.Sessions(ctx)
}

// Events delega sin candado ni emision.
func (s *ChildPermissionStore) Events(ctx context.Context, sessionID string, sinceSeq session.Seq) ([]session.SessionEvent, error) {
	return s.inner.Events(ctx, sessionID, sinceSeq)
}

// Epoch delega sin candado ni emision.
func (s *ChildPermissionStore) Epoch(ctx context.Context, sessionID string) (session.ContextEpoch, error) {
	return s.inner.Epoch(ctx, sessionID)
}

// PendingToolCalls delega sin candado ni emision.
func (s *ChildPermissionStore) PendingToolCalls(ctx context.Context, sessionID string) ([]session.PendingTool, error) {
	return s.inner.PendingToolCalls(ctx, sessionID)
}

// DeleteSession delega sin candado ni emision.
func (s *ChildPermissionStore) DeleteSession(ctx context.Context, sessionID string) error {
	return s.inner.DeleteSession(ctx, sessionID)
}
