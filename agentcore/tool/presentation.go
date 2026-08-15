package tool

// Presentation is how a call should read to the person watching, described by the
// tool that would settle it. It exists so a host can render any tool — including
// one it has never heard of — without a switch on tool names: the tool answers
// what it is called, what this particular call is about, and what text the reader
// needs; the host owns every pixel.
//
// That division is the whole point. A tool must not return markup, colors or
// widths, because it cannot know whether it is being drawn in a terminal, in a
// browser or read aloud; a host must not decide that "the second field of a write
// call is the content", because that is the tool's schema and it changes when the
// tool does.
//
// Every string is raw text taken from the call. A host sanitizes and truncates it
// for its own surface — a tool's Subject can contain control characters, because
// the model wrote it.
//
// The zero value is a usable presentation of nothing: an Activity with no label,
// showing the settled output. A host therefore treats a tool that says nothing and
// one that returns the zero value the same way, which is what makes Presenter
// optional rather than load-bearing.
type Presentation struct {
	// Kind is what became of the call, which is what decides the shape it is drawn
	// in. See the constants.
	Kind PresentationKind
	// Label names the tool as the reader should see it ("Read", "Bash",
	// "SubAgent"), not as the model calls it. Empty falls back to the tool's name.
	Label string
	// Running replaces Label while the call is still in flight ("Reading" for
	// "Read"). Empty means Label reads correctly in both tenses.
	Running string
	// Subject is the one thing this call is about, short enough to sit on a header
	// line beside the label: a file name, a command, a URL, a subagent type. It is
	// what makes two calls to the same tool tell each other apart. Empty means the
	// label stands alone.
	Subject string
	// Body is the full text the call amounts to, for a surface that shows the whole
	// of it rather than a header — above all the permission prompt, where it is
	// what the user is being asked to authorize. It must therefore be faithful and
	// complete: the command that will run, the patch that will be applied, the
	// content that will be written. Empty means the tool cannot state its call as
	// text, and a host falls back to showing the raw input.
	Body string
	// Detail says whether the settled output belongs in the transcript. The zero
	// value previews it, which is the useful default for a tool that has not
	// thought about presentation. Hidden keeps model-only output off screen;
	// OnDemand lets the reader expand it without paying its vertical cost by
	// default.
	Detail DetailMode
}

// PresentationKind is what a call turned out to be, from the reader's point of
// view. It is deliberately coarse: a host maps each one to a shape it knows how to
// draw, and a kind it does not recognize falls back to Activity.
type PresentationKind int

const (
	// Activity is a call that reads as one line of what the agent is doing: the
	// label, the subject, and the output when it is worth reading. It is the zero
	// value and the right answer for almost everything, including a call that has
	// not finished, failed, or was denied.
	Activity PresentationKind = iota
	// FileChange is a call that rewrote a file that already existed. Result.Diff is
	// the before and after of the change, and a host with room for it should show
	// the change rather than the label.
	FileChange
	// FileCreation is a call that produced a file that was not there. Result.Diff
	// is the whole of the new content, with nothing to compare it against.
	FileCreation
)

// DetailMode controls how a host exposes a settled call's output.
type DetailMode int

const (
	// DetailPreview shows a bounded preview of settled output. It is the zero
	// value and the right default for tools unknown to the host.
	DetailPreview DetailMode = iota
	// DetailHidden suppresses output that exists for the model rather than the
	// person watching, such as a file body or loaded skill instructions.
	DetailHidden
	// DetailOnDemand keeps output collapsed until the person asks to see it.
	DetailOnDemand
)

// Presenter is the optional interface a tool implements to describe how its calls
// should read. Without it a host falls back to the tool's name and a generic
// summary of its input, which works but never reads well.
//
// Present is called for a call in any state, so it must not assume the call
// succeeded. An unsettled or failed call arrives with the zero Result: that is the
// case where Kind stays Activity, since there is no diff to show. It is called
// while drawing, possibly many times for the same call and from a UI's goroutine,
// so it must be cheap, pure and safe for concurrent use — and it must survive an
// input the model malformed, exactly like Execute.
type Presenter interface {
	Tool
	Present(call Call, result Result) Presentation
}

// PresentationOf asks t how the call should read, and reports whether t answered.
// A false means the host applies its own default rather than a blank
// presentation — a tool that says nothing still has a name.
func PresentationOf(t Tool, call Call, result Result) (Presentation, bool) {
	p, ok := t.(Presenter)
	if !ok {
		return Presentation{}, false
	}
	return p.Present(call, result), true
}
