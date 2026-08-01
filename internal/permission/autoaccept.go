package permission

import (
	"sync"

	"github.com/K3N4Y/atenea/internal/tool"
)

// AutoAcceptModes is the in-memory, per-session permission mode for one sitting.
type AutoAcceptModes struct {
	mu      sync.RWMutex
	enabled map[string]bool
}

func NewAutoAcceptModes() *AutoAcceptModes {
	return &AutoAcceptModes{enabled: make(map[string]bool)}
}

func (m *AutoAcceptModes) Set(sessionID string, enabled bool) {
	if m == nil || sessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if enabled {
		m.enabled[sessionID] = true
	} else {
		delete(m.enabled, sessionID)
	}
}

func (m *AutoAcceptModes) Enabled(sessionID string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabled[sessionID]
}

// AutoAcceptPolicy upgrades only base Ask decisions for the deliberately narrow
// set of workspace operations proven safe by the live tool catalog.
type AutoAcceptPolicy struct {
	base    Policy
	modes   *AutoAcceptModes
	catalog tool.Catalog
}

func NewAutoAcceptPolicy(base Policy, modes *AutoAcceptModes, catalog tool.Catalog) Policy {
	if modes == nil {
		return base
	}
	return AutoAcceptPolicy{base: base, modes: modes, catalog: catalog}
}

func (p AutoAcceptPolicy) Decide(sessionID string, call tool.Call) Decision {
	decision := p.base.Decide(sessionID, call)
	if decision != Ask || !p.modes.Enabled(sessionID) {
		return decision
	}
	switch call.Name {
	case "write":
		if registered, ok := p.catalog.Lookup(call.Name); ok {
			if trusted, ok := registered.(*tool.WriteTool); ok && trusted.AutoAcceptSafe(call) {
				return Allow
			}
		}
	case "edit":
		if registered, ok := p.catalog.Lookup(call.Name); ok {
			if trusted, ok := registered.(*tool.EditTool); ok && trusted.AutoAcceptSafe(call) {
				return Allow
			}
		}
	case "bash":
		if bash, ok := p.catalog.Lookup(call.Name); ok {
			if validator, ok := bash.(interface{ AutoAcceptSafe(tool.Call) bool }); ok && validator.AutoAcceptSafe(call) {
				return Allow
			}
		}
	}
	return Ask
}
