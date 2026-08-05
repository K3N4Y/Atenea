package tool

import "sort"

// Catalog is the read side of the registry: it resolves canonical names and
// immutable presentation aliases. It is not an execution router; settlement
// always uses the turn-local routes returned by Materialize.
//
// Every question asked over a Catalog therefore has an answer for "not
// registered". A name travels a long way: the model produced it, a durable event
// carried it, a UI renders it several turns later, and by then the tool can be
// gone (a workspace rewire, a disconnected MCP server). A caller degrades rather
// than fails, and each of the helpers below documents which way it degrades.
//
// *Registry implements it. A Registry is immutable after NewRegistry, so it can
// be handed to a UI and read from any goroutine.
type Catalog interface {
	Lookup(name string) (Tool, bool)
	// Names lists every registered tool, sorted, so a caller can validate a name
	// it was given against what actually exists and say what the alternatives are.
	Names() []string
}

// Lookup returns presentation metadata for canonical and durable historical
// names. It cannot execute a tool out of band: settlement still goes through
// Materialize, which applies turn-local routing, permissions, repair and capping.
func (r *Registry) Lookup(name string) (Tool, bool) {
	if t, ok := r.tools[name]; ok {
		return t, true
	}
	t, ok := r.presentationAliases[name]
	return t, ok
}

// Names lists the registered tools in name order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MayChangeFiles reports whether a settled call to the named tool could have left
// the workspace different from how it found it — the question behind "should the
// git summary be re-read now that this call finished".
//
// It answers yes for a tool that is unknown or that declared nothing, because the
// cost of the two mistakes is not symmetric: re-reading git needlessly is a few
// milliseconds, while missing a change leaves the UI showing a stale diff stat.
func MayChangeFiles(catalog Catalog, name string) bool {
	if catalog == nil {
		return true
	}
	t, ok := catalog.Lookup(name)
	if !ok {
		return true
	}
	effects, declared := EffectsOf(t)
	if !declared {
		return true
	}
	return effects.Any(WritesFiles | RunsCommands)
}

// PresentationFor asks the tool that would settle the call how it should read, and
// reports whether an answer came back. False — an unknown tool, or one that does
// not implement tool.Presenter — means the caller applies its own default: a tool
// that says nothing still has a name and an input to summarize.
func PresentationFor(catalog Catalog, call Call, result Result) (Presentation, bool) {
	if catalog == nil {
		return Presentation{}, false
	}
	t, ok := catalog.Lookup(call.Name)
	if !ok {
		return Presentation{}, false
	}
	return PresentationOf(t, call, result)
}
