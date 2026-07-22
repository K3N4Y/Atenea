package tui

// inputTarget names the context that owns input right now. It is the single
// source of truth for the precedence ORDER between overlays, gates, and panels:
// the keyboard router, the composer-focus sync, and the mouse router all read
// this ordering instead of re-listing the chain independently.
//
// The values are declared in strict priority order (highest first). The
// resolver returns the first target whose state is active; ties are broken by
// this declaration order, which mirrors the original hand-written chains.
type inputTarget int

const (
	// targetResumePicker: the resume session picker overlay is open. It wins
	// over every other overlay and gate.
	targetResumePicker inputTarget = iota
	// targetModelPicker: the model/provider picker overlay is open.
	targetModelPicker
	// targetMCPPicker: the MCP server picker overlay is open.
	targetMCPPicker
	// targetConnectPanel: the provider connect panel overlay is open.
	targetConnectPanel
	// targetPermissionGate: a tool permission decision is pending. The keyboard
	// enters approval mode; only context-specific exceptions (PgUp/PgDn scroll)
	// escape it, and those exceptions live in the leaf handler, not here.
	targetPermissionGate
	// targetPlanGate: a plan approval is pending (no permission pending).
	targetPlanGate
	// targetViewer: no overlay/gate active and the file viewer holds focus.
	targetViewer
	// targetExplorer: no overlay/gate active and the file tree holds focus.
	targetExplorer
	// targetComposer: nothing above claims input; the chat composer owns it.
	targetComposer
)

// activeInputTarget resolves which context owns input given the current Model
// state, encoding the precedence order EXACTLY ONCE. Callers consult it to
// dispatch to the right leaf handler (keyboard), to decide composer focus, and
// to short-circuit modal overlays before pointer-specific handling (mouse).
//
// The order is: the four overlay pickers (resume, model, mcp, connect), then
// the permission gate, then the plan gate, then the focused panel derived from
// normalizedFocus (viewer, explorer, otherwise the composer).
func (m Model) activeInputTarget() inputTarget {
	switch {
	case m.resumePicker.open:
		return targetResumePicker
	case m.modelPicker.open:
		return targetModelPicker
	case m.mcpPicker.open:
		return targetMCPPicker
	case m.connectPanel.open:
		return targetConnectPanel
	}
	if _, ok := m.pendingPermission(); ok {
		return targetPermissionGate
	}
	if m.hasPendingPlan() {
		return targetPlanGate
	}
	switch m.normalizedFocus() {
	case viewerFocus:
		return targetViewer
	case explorerFocus:
		return targetExplorer
	default:
		return targetComposer
	}
}

// modalActive reports whether an overlay or gate currently claims input, i.e.
// the active target is one of the pickers, the permission gate, or the plan
// gate. The mouse router uses this shared notion (with each modal's own
// pointer-specific short-circuit) instead of re-listing the overlay chain.
func (t inputTarget) modalActive() bool {
	switch t {
	case targetResumePicker, targetModelPicker, targetMCPPicker,
		targetConnectPanel, targetPermissionGate, targetPlanGate:
		return true
	default:
		return false
	}
}
