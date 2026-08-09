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
	targetResumePicker inputTarget = iota
	targetAgentPicker
	targetModelPicker
	targetMCPPicker
	targetLearnedPicker
	targetSkillsPicker
	targetVariantsPicker
	targetConnectPanel
	targetPermissionGate
	targetPlanGate
	targetComposer
)

func (m Model) activeInputTarget() inputTarget {
	switch {
	case m.resumePicker.open:
		return targetResumePicker
	case m.agentPicker.open:
		return targetAgentPicker
	case m.modelPicker.open:
		return targetModelPicker
	case m.mcpPicker.open:
		return targetMCPPicker
	case m.learnedPicker.open:
		return targetLearnedPicker
	case m.skillsPicker.open:
		return targetSkillsPicker
	case m.variantsPicker.open:
		return targetVariantsPicker
	case m.connectPanel.open:
		return targetConnectPanel
	}
	if _, ok := m.pendingPermission(); ok {
		return targetPermissionGate
	}
	if m.hasPendingPlan() {
		return targetPlanGate
	}
	return targetComposer
}

func (t inputTarget) modalActive() bool {
	switch t {
	case targetAgentPicker, targetResumePicker, targetModelPicker, targetMCPPicker, targetLearnedPicker,
		targetSkillsPicker, targetVariantsPicker, targetConnectPanel, targetPermissionGate, targetPlanGate:
		return true
	default:
		return false
	}
}
