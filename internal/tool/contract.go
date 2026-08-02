package tool

import contract "github.com/K3N4Y/atenea/agentcore/tool"

// The tool contract lives in agentcore/tool, the published surface a third
// party implements. This package is its private side: the registry, the output
// capping and the tools atenea ships. The aliases below re-export the contract
// so implementation code keeps one spelling (tool.Tool, tool.Result) whichever
// side of the boundary a type is defined on.
//
// A new contract type belongs in agentcore/tool and gets an alias here; a new
// implementation detail belongs here and nowhere else.

type (
	Tool             = contract.Tool
	Call             = contract.Call
	Result           = contract.Result
	Effects          = contract.Effects
	Declaring        = contract.Declaring
	CallDeclaring    = contract.CallDeclaring
	Presentation     = contract.Presentation
	PresentationKind = contract.PresentationKind
	Presenter        = contract.Presenter
)

const (
	NoEffects      = contract.NoEffects
	WritesFiles    = contract.WritesFiles
	RunsCommands   = contract.RunsCommands
	ReachesNetwork = contract.ReachesNetwork

	Activity     = contract.Activity
	FileChange   = contract.FileChange
	FileCreation = contract.FileCreation
)

// EffectsOf, EffectsForCall and PresentationOf resolve optional capability
// interfaces. They are the one place "the tool said nothing" is told apart
// from "the tool said nothing of substance"; see the contract for why a host
// must not flatten them.
var (
	EffectsOf      = contract.EffectsOf
	EffectsForCall = contract.EffectsForCall
	PresentationOf = contract.PresentationOf
)
