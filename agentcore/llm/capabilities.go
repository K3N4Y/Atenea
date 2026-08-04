package llm

// Capabilities is what an adapter says about itself beyond streaming a turn:
// which parts of this contract it actually exercises, and the facts about the
// models it serves that no host can read off the stream.
//
// It exists because the alternative is core guessing — a model-to-window table
// compiled into the agent loop, a switch on the provider's name — and a guess is
// wrong the moment someone plugs in an adapter this build has never heard of.
// The adapter states what it does; the host decides what that is worth. Neither
// side gets to do the other's job: nothing here is a request, a preference or a
// rendering, only a fact about the adapter.
//
// The zero value declares nothing useful, which is why the answer always travels
// with whether the adapter answered at all — see CapabilitiesOf.
type Capabilities struct {
	// Streaming: the turn arrives incrementally, as the model produces it. False
	// means the adapter fronts a non-streaming endpoint and delivers the whole
	// turn at the end — still a legal Provider, since the bracketing holds either
	// way, but a host may want to say so rather than leave a cursor blinking.
	Streaming bool

	// Tools: the adapter forwards Request.Tools and reports what the model calls
	// as ToolCall events. False means an agent loop has nothing to settle and the
	// model will answer in prose; a host should say that rather than look broken.
	Tools bool

	// Reasoning: the adapter ASKS the endpoint for the model's reasoning and
	// reports it as Reasoning* events. False does not promise the stream never
	// carries them — an endpoint may volunteer reasoning the adapter forwards
	// anyway — it means a host should not expect them.
	// For catalogs serving multiple model families, use ReasoningModels to avoid
	// turning a per-model fact into an unsafe global claim.
	Reasoning bool

	// ReasoningModels records model-specific reasoning behavior when Reasoning
	// cannot honestly describe every model in the catalog. An absent model is
	// unknown, not unsupported.
	ReasoningModels map[string]bool

	// Vision: the adapter translates a Message part that is an image or a
	// document, not only text. Message carries content parts now, so the seam is
	// no longer what stands in the way — this is the flag an adapter flips when it
	// can actually put that content on the wire, and until it does the honest
	// answer is false. An adapter that says false and then meets such a part
	// refuses the turn with an UnsupportedPartError rather than dropping it.
	Vision bool

	// PromptCaching is how, if at all, the adapter gets its endpoint to cache the
	// prompt — which is what decides whether Request.SessionKey buys anything.
	// For catalogs serving multiple model families, use PromptCachingModels to
	// avoid turning a per-model fact into an unsafe global claim.
	PromptCaching PromptCaching

	// PromptCachingModels records model-specific caching behavior when
	// PromptCaching cannot honestly describe every model in the catalog. An
	// absent model is unknown, not uncached.
	PromptCachingModels map[string]PromptCaching

	// RetryTelemetry: the adapter reports its transient retries as StepRetrying.
	// False means a pause in the stream is a retry the host cannot see and must
	// not present as a stall.
	RetryTelemetry bool

	// DefaultMaxOutputTokens is the ceiling the adapter applies when a Request
	// leaves MaxOutputTokens at zero. Zero means it imposes none and lets the
	// endpoint decide. A host that measures how close a request sits to the
	// context window has to reserve this, or it under-counts by exactly the
	// output the adapter is about to ask for.
	DefaultMaxOutputTokens int

	// ContextWindows maps a model id this adapter serves to that model's total
	// token window. The ids are the adapter's own: the same model reached through
	// two gateways is two ids, and only the adapter knows which one it speaks — a
	// single table keyed by id, shared by every provider, cannot say that. A model
	// that is absent is unknown, never unbounded; read it through ContextWindow.
	ContextWindows map[string]int
}

// ContextWindow is the total token window of a model this adapter serves, and
// whether the adapter knows it. An absent, zero or negative entry all read as
// unknown: a window a host would divide by is worse than no window at all.
func (c Capabilities) ContextWindow(model string) (int, bool) {
	window, ok := c.ContextWindows[model]
	if !ok || window <= 0 {
		return 0, false
	}
	return window, true
}

// PromptCaching is how an adapter obtains prompt caching from its endpoint. The
// distinction that matters to a host is not whether caching happens but who keys
// it: only KeyedPromptCaching gives Request.SessionKey a job.
type PromptCaching int

const (
	// NoPromptCaching: the adapter does nothing about caching. Whatever the
	// endpoint does on its own is invisible from here.
	NoPromptCaching PromptCaching = iota
	// ImplicitPromptCaching: the adapter marks every request cacheable itself,
	// with no key and no opt-out. SessionKey changes nothing.
	ImplicitPromptCaching
	// KeyedPromptCaching: the adapter forwards Request.SessionKey as the cache
	// affinity key, so a host that keeps it stable across a conversation gets the
	// hits and one that varies it gets none.
	KeyedPromptCaching
)

// String renders the mode for a log line or a test failure, with anything this
// build does not recognize as "unknown" so a new mode is visible, not dropped.
func (p PromptCaching) String() string {
	switch p {
	case NoPromptCaching:
		return "none"
	case ImplicitPromptCaching:
		return "implicit"
	case KeyedPromptCaching:
		return "keyed"
	}
	return "unknown"
}

// Describing is the optional interface an adapter implements to declare its
// Capabilities.
//
// It is optional so Provider stays a one-method contract — the minimum to be an
// adapter is still to stream a turn — and it is discovered by type assertion:
//
//	if d, ok := p.(llm.Describing); ok { caps = d.Capabilities() }
//
// A provider that does not implement it has declared nothing, which is not the
// same as declaring the zero Capabilities: silence gives a host no basis to
// conclude that the adapter does not stream, or that it serves no model it knows
// the window of. Resolve it through CapabilitiesOf so the two never get
// flattened.
//
// Capabilities is read while drawing, possibly on every frame, and from a host's
// own goroutines. It must be cheap, pure and safe for concurrent use, and it must
// answer the same thing every time: a value that changes under a reader is a UI
// flickering between two truths. The returned map belongs to the adapter and a
// caller must not write to it.
type Describing interface {
	Provider
	Capabilities() Capabilities
}

// CapabilitiesOf returns what p declares about itself, and whether p declared
// anything at all. It is the one place a host should resolve the optional
// interface, so "said nothing" and "said no" never collapse into one value by
// accident.
func CapabilitiesOf(p Provider) (Capabilities, bool) {
	d, ok := p.(Describing)
	if !ok {
		return Capabilities{}, false
	}
	return d.Capabilities(), true
}
