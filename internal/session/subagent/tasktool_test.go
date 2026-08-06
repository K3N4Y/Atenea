package subagent

import (
	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/tool"
)

// newTaskTool keeps behavior-focused tests compact while production callers
// use the complete Config interface.
func newTaskTool(defs []agent.Def, provider llm.Provider, children *tool.Registry, nextID func() string) *TaskTool {
	return NewTaskTool(Config{
		Definitions: defs, Provider: provider, Children: children, NextID: nextID,
	})
}
