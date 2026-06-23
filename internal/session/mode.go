package session

// Mode es el modo de operacion de una sesion: normal o plan. En plan-mode el
// runner arma el turno con un system prompt y un set de permisos de solo lectura
// (mas present_plan) en vez de los normales. El valor cero (ModeNormal) deja el
// comportamiento normal sin cambios.
type Mode string

const (
	ModeNormal Mode = ""     // valor cero = modo normal
	ModePlan   Mode = "plan" // plan-mode: prompt y permisos de planificacion
)
