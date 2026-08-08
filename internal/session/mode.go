package session

// Mode selects the tool and prompt surface for one session turn.
//
// ModeNormal preserves the ordinary agent and its existing one-level task
// delegation. ModePlan is read-only planning. ModeRAH is an explicit, one-turn
// recursive-harness surface selected only by the local /rah command.
type Mode string

const (
	ModeNormal Mode = ""
	ModePlan   Mode = "plan"
	ModeRAH    Mode = "rah"
)
