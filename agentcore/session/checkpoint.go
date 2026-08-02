package session

// ContextEpoch is a snapshot of a session's context, used to detect a
// concurrent change between preparing a turn and calling the provider. Agent and
// Model identify the turn's active configuration (the epoch's model is the one
// that goes into the request). Revision increases whenever the context changes
// in a way that invalidates an already-prepared request. BaselineSeq marks where
// the turn's projected history starts, which a compaction advances to leave out
// what it already summarized.
//
// It is comparable on purpose (comparable fields only): a host decides whether
// to rebuild a turn with a plain after != before.
type ContextEpoch struct {
	Agent       string
	Model       string
	BaselineSeq Seq
	Revision    int
}

// CompactionReason says why a compaction happened: ahead of the limit, or
// because the provider already rejected the turn for overflowing the context.
type CompactionReason string

const (
	CompactionPreventive CompactionReason = "preventive"
	CompactionOverflow   CompactionReason = "overflow"
)

// StructuredSummary is the compaction summary the model produces: fixed
// sections instead of free prose, so what survives a compaction is predictable
// and every section can be checked for presence. CurrentGoal is the only
// scalar; every other field is a list and must be present, empty rather than
// absent.
type StructuredSummary struct {
	CurrentGoal string   `json:"current_goal"`
	Constraints []string `json:"constraints_and_instructions"`
	Decisions   []string `json:"decisions"`
	Completed   []string `json:"completed_work"`
	Files       []string `json:"files_and_changes"`
	ToolResults []string `json:"relevant_tool_results"`
	Failures    []string `json:"failures_and_attempts"`
	Pending     []string `json:"pending_and_next_step"`
	Invariants  []string `json:"facts_not_to_reinterpret"`
}

// CompactionCheckpoint is the durable payload of a Context.Compacted event: the
// summary that replaces the covered history, plus the sequences that say exactly
// what it replaces. ExpectedEpoch is the context the compaction was computed
// against, so a store can reject a checkpoint that raced with another change.
// The history is then read as: the summary, the anchoring user message
// (AnchorUserSeq), and everything from PreservedFromSeq onwards.
type CompactionCheckpoint struct {
	Summary              StructuredSummary `json:"summary"`
	ExpectedEpoch        ContextEpoch      `json:"expected_epoch"`
	CoveredThroughSeq    Seq               `json:"covered_through_seq"`
	AnchorUserSeq        Seq               `json:"anchor_user_seq"`
	PreservedFromSeq     Seq               `json:"preserved_from_seq"`
	Model                string            `json:"model"`
	Reason               CompactionReason  `json:"reason"`
	InputTokensBefore    int               `json:"input_tokens_before"`
	EstimatedTokensAfter int               `json:"estimated_tokens_after"`
}

// PromptCheckpoint is the durable payload of the Prompt.Checkpoint.* events: the
// prompt a turn started from and the workspace tree before and after it, which
// is what makes undoing that turn's file changes possible.
type PromptCheckpoint struct {
	ID         string
	Prompt     string
	BeforeTree string
	AfterTree  string
	// OriginCallID identifies a model-created checkpoint. Rewind preserves that
	// tool call and its settlement, then prunes subsequent conversation events.
	OriginCallID string
}
