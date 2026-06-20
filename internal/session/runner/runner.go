package runner

import (
	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// Runner ensambla el turno: lee el historial del Store, materializa tools del
// Registry con los permisos del agente, llama al Provider y publica los eventos.
// En M5 expone runTurn (un turno aislado); el loop externo Run (drenar el Inbox,
// MaxSteps) lo agrega M6 sobre esta misma estructura. nextID genera el
// assistantMessageID de cada turno (determinista en tests; un generador real en
// M9).
type Runner struct {
	store    session.Store
	provider llm.Provider
	registry *tool.Registry
	perms    tool.Permissions
	nextID   func() string
}

// NewRunner arma el Runner con sus dependencias. nextID genera el
// assistantMessageID de cada turno: inyectado para dejar los tests deterministas
// sin arrastrar una dependencia de UUID/tiempo.
func NewRunner(store session.Store, provider llm.Provider, registry *tool.Registry,
	perms tool.Permissions, nextID func() string) *Runner {
	return &Runner{store: store, provider: provider, registry: registry, perms: perms, nextID: nextID}
}
