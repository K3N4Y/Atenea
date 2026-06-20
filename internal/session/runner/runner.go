package runner

import (
	"context"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// Runner ensambla el turno: lee el historial del Store, materializa tools del
// Registry con los permisos del agente, llama al Provider y publica los eventos.
// En M5 expone runTurn (un turno aislado); M6 sumo el loop externo Run (run.go,
// drenar el Inbox y MaxSteps) sobre esta misma estructura. nextID genera el
// assistantMessageID de cada turno (determinista en tests; un generador real en
// M9).
type Runner struct {
	store     session.Store
	inbox     session.Inbox
	provider  llm.Provider
	registry  *tool.Registry
	perms     tool.Permissions
	nextID    func() string
	compactor Compactor // opcional; nil = nunca compacta (camino feliz de M5/M6)
}

// Compactor decide si un Request excede el contexto del modelo y, si pasa, compacta
// el historial durable de la sesion para que el siguiente intento entre. nil en el
// Runner significa "nunca compacta". M7 lo inyecta con un fake en tests; el real
// (que mide tokens contra el limite del modelo y resume el historial) llega en M10.
type Compactor interface {
	// NeedsCompaction informa si req excede el contexto y hay que compactar antes de
	// llamar al proveedor.
	NeedsCompaction(req llm.Request) bool
	// Compact reduce el historial durable de la sesion (resumen/baseline) para que el
	// siguiente intento arme un request que entre. Debe hacer progreso: tras Compact,
	// NeedsCompaction del nuevo request debe terminar siendo false.
	Compact(ctx context.Context, sessionID string) error
}

// NewRunner arma el Runner con sus dependencias. nextID genera el
// assistantMessageID de cada turno: inyectado para dejar los tests deterministas
// sin arrastrar una dependencia de UUID/tiempo.
func NewRunner(store session.Store, inbox session.Inbox, provider llm.Provider,
	registry *tool.Registry, perms tool.Permissions, nextID func() string) *Runner {
	return &Runner{
		store: store, inbox: inbox, provider: provider,
		registry: registry, perms: perms, nextID: nextID,
	}
}
