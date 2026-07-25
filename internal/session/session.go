package session

// Session is the durable aggregate of a conversation. It is minimal on purpose:
// only the id. Agent, model and workspace live in the event log, not here.
type Session struct {
	ID string
}
