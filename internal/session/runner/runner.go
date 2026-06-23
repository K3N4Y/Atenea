package runner

import (
	"context"
	"log"

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

	// system builds the turn baseline prompt from the epoch's model. nil (default)
	// = no system prompt. SetSystemPrompt wires it from the real assembly
	// (app.go); tests inject it directly or via the setter. It receives the model
	// so internal/session/prompt picks the base prompt by family.
	system func(model string) string

	// mode looks up the session's Mode per turn. nil (default) => always
	// ModeNormal: behavior is identical to today. In ModePlan the runner builds the
	// Request with planSystem/planPerms instead of system/perms.
	mode func(sessionID string) session.Mode

	// planSystem and planPerms are the plan-mode counterparts of system/perms,
	// used when mode reports ModePlan. planSystem nil => fall back to system;
	// planPerms nil => fall back to perms. SetPlanMode wires them from app.go;
	// tests inject them via the setter.
	planSystem func(model string) string
	planPerms  tool.Permissions

	// gate and needsApproval implement ask-before-run: before settling a tool
	// call for which needsApproval returns true, the runner asks the gate for
	// approval (which blocks until the user's decision). Both nil (default) =
	// no gating: every tool call is settled directly (M5 behavior). Set
	// from app.go via SetPermissionGate; tests inject a fakeGate.
	gate          session.PermissionGate
	needsApproval func(call tool.Call) bool

	// logf registra a stderr los fallos de tool para visibilidad en desarrollo:
	// hoy un fallo solo vive en el log durable y en el mensaje al modelo, asi que
	// corriendo `wails dev` no hay como enterarse de que las tools fallan. Default
	// log.Printf; los tests lo inyectan para capturar la salida sin tocar stderr.
	logf func(format string, args ...any)
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
		logf: log.Printf,
	}
}

// SetSystemPrompt injects the turn system prompt builder. It receives the epoch's
// model and returns the baseline prompt that travels in Request.System. nil
// (default) = no system prompt. This is the exported entry point for the real
// assembly (app.go, in package main, cannot touch the unexported field).
func (r *Runner) SetSystemPrompt(build func(model string) string) {
	r.system = build
}

// SetPermissionGate wires ask-before-run: gate resolves the user's approval and
// needsApproval decides which tool calls require it (e.g. only "bash"). If
// either is nil the runner gates nothing. Exported entry point for app.go
// (package main); tests inject the fields directly.
func (r *Runner) SetPermissionGate(gate session.PermissionGate, needsApproval func(call tool.Call) bool) {
	r.gate = gate
	r.needsApproval = needsApproval
}

// SetMode injects the per-session Mode lookup. It receives the session id and
// returns its Mode; the runner consults it each turn to pick the normal or
// plan-mode system prompt and permissions. nil (default) = always ModeNormal,
// so behavior is identical to today. Exported entry point for app.go (package
// main); tests inject the field via this setter.
func (r *Runner) SetMode(mode func(sessionID string) session.Mode) {
	r.mode = mode
}

// SetPlanMode wires the plan-mode turn baseline: system builds the plan-mode
// system prompt and perms is the plan-mode permission set (read-only +
// present_plan). They take effect only when SetMode reports ModePlan. A nil
// system falls back to the normal SetSystemPrompt builder; nil perms fall back
// to the normal permissions. Exported entry point for app.go (package main);
// tests inject the fields via this setter.
func (r *Runner) SetPlanMode(system func(model string) string, perms tool.Permissions) {
	r.planSystem = system
	r.planPerms = perms
}
