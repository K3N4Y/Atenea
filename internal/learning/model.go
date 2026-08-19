// Package learning implements workspace-scoped, human-approved lessons.
package learning

import (
	"errors"
	"strings"
	"time"
)

// AgentName is the configurable model role shown by the TUI's /agents picker.
// It is not a tool-capable subagent: the learning service still owns its bounded,
// tool-free extraction request.
const AgentName = "learn"

type Status string

const (
	Queued      Status = "queued"
	Running     Status = "running"
	Ready       Status = "ready"
	NoCandidate Status = "no_candidate"
	Failed      Status = "failed"
	Cancelling  Status = "cancelling"
	Cancelled   Status = "cancelled"
	Approved    Status = "approved"
	Rejected    Status = "rejected"
	Interrupted Status = "interrupted"
)

type Evidence struct {
	Seq     int64  `json:"seq"`
	Summary string `json:"summary"`
}
type Candidate struct {
	Statement  string     `json:"statement"`
	Scope      string     `json:"scope"`
	Exceptions string     `json:"exceptions"`
	Evidence   []Evidence `json:"evidence"`
}
type Usage struct {
	InputTokens     int `json:"inputTokens"`
	OutputTokens    int `json:"outputTokens"`
	ReasoningTokens int `json:"reasoningTokens"`
}
type Input struct {
	Messages  []InputMessage `json:"messages"`
	Truncated bool           `json:"truncated"`
}
type InputMessage struct {
	Seq  int64  `json:"seq"`
	Role string `json:"role"`
	Text string `json:"text"`
}

type Run struct {
	ID                string     `json:"id"`
	Workspace         string     `json:"workspace"`
	SessionID         string     `json:"sessionID"`
	CutSeq            int64      `json:"cutSeq"`
	Status            Status     `json:"status"`
	Input             Input      `json:"input"`
	Candidate         *Candidate `json:"candidate,omitempty"`
	NoCandidateReason string     `json:"noCandidateReason,omitempty"`
	ProviderID        string     `json:"providerID"`
	Model             string     `json:"model"`
	Usage             Usage      `json:"usage"`
	Error             string     `json:"error,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	DecidedAt         *time.Time `json:"decidedAt,omitempty"`
}
type Lesson struct {
	ID        string    `json:"id"`
	Workspace string    `json:"workspace"`
	RunID     string    `json:"runID"`
	Candidate Candidate `json:"candidate"`
	Enabled   bool      `json:"enabled"`
	Deleted   bool      `json:"deleted"`
	CreatedAt time.Time `json:"createdAt"`
}

var ErrNotFound = errors.New("learning record not found")
var ErrInvalidTransition = errors.New("invalid learning run transition")

func CanTransition(from, to Status) bool {
	allowed := map[Status]map[Status]bool{
		Queued:     {Running: true, Cancelled: true, Interrupted: true},
		Running:    {Ready: true, NoCandidate: true, Failed: true, Cancelling: true, Cancelled: true, Interrupted: true},
		Cancelling: {Cancelled: true, Interrupted: true},
		Ready:      {Approved: true, Rejected: true},
	}
	return from == to || allowed[from][to]
}

func ValidateCandidate(c Candidate) error {
	if strings.TrimSpace(c.Statement) == "" || len([]rune(c.Statement)) > 500 {
		return errors.New("statement must contain 1..500 characters")
	}
	if strings.TrimSpace(c.Scope) == "" || len([]rune(c.Scope)) > 500 {
		return errors.New("scope must contain 1..500 characters")
	}
	if len([]rune(c.Exceptions)) > 500 {
		return errors.New("exceptions exceeds 500 characters")
	}
	if len(c.Evidence) == 0 || len(c.Evidence) > 8 {
		return errors.New("candidate requires 1..8 evidence references")
	}
	for _, e := range c.Evidence {
		if e.Seq <= 0 || strings.TrimSpace(e.Summary) == "" || len([]rune(e.Summary)) > 300 {
			return errors.New("invalid evidence reference")
		}
	}
	return nil
}
