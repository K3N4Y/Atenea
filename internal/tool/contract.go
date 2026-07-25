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
	Tool   = contract.Tool
	Call   = contract.Call
	Result = contract.Result
)
