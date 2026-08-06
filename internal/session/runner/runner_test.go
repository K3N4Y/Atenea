package runner

import (
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

// newRunner keeps behavior-focused tests compact while production callers use
// the complete Config interface.
func newRunner(store session.Store, inbox session.Inbox, provider llm.Provider, registry *tool.Registry, perms tool.Permissions, nextID func() string) *Runner {
	return New(Config{
		Store: store, Inbox: inbox, Provider: provider, Registry: registry,
		Permissions: perms, NextID: nextID,
	})
}
