package tui

import "github.com/K3N4Y/atenea/internal/tui/engine"

// The engine owns the protocol it produces (see engine/protocol.go). These
// aliases re-export those types under the tui package so the Model, its
// rendering, and their tests can name them unqualified, while the definitions
// stay in the engine package and the dependency points one way (tui -> engine).
//
// Construction stays explicit: the Engine, its Config, and New are reached
// through the engine package directly (the composition root wires them), so
// they are deliberately not re-exported here.
type (
	EventMsg            = engine.EventMsg
	PreviewMsg          = engine.PreviewMsg
	LearningChangedMsg  = engine.LearningChangedMsg
	RunHandle           = engine.RunHandle
	RunDoneMsg          = engine.RunDoneMsg
	UndoResult          = engine.UndoResult
	CheckpointResult    = engine.CheckpointResult
	RewindResult        = engine.RewindResult
	ResumeResult        = engine.ResumeResult
	CompactionState     = engine.CompactionState
	CompactionStatusMsg = engine.CompactionStatusMsg
	ModelsRefreshedMsg  = engine.ModelsRefreshedMsg
)

const (
	CompactionQueued    = engine.CompactionQueued
	CompactionRunning   = engine.CompactionRunning
	CompactionNotNeeded = engine.CompactionNotNeeded
	CompactionFailed    = engine.CompactionFailed
)

// Compile-time proof that the Engine satisfies the small consumer interfaces
// the presentation layer defines for it (the seam lives with its consumer).
var (
	_ Agent         = (*engine.Engine)(nil)
	_ modelAgent    = (*engine.Engine)(nil)
	_ mcpAgent      = (*engine.Engine)(nil)
	_ connectAgent  = (*engine.Engine)(nil)
	_ learningAgent = (*engine.Engine)(nil)
)
