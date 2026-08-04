package llm

import (
	"fmt"
	"strings"
)

// Part is one piece of a message's content. A message's content is the ordered
// list of its parts. Kind determines whether Text or the image fields carry the
// content.
//
// Kind decides which fields are relevant; the rest stay zero. That is the same
// discriminated shape Event and tool.Presentation already use, and it is chosen
// over a sealed interface on purpose: an interface would make an image part with
// no image unrepresentable, but it would also turn every future kind into a new
// exported type and put the set of kinds beyond a host that builds a Part from
// data it decoded. A struct with a discriminator grows by addition, which is what
// "additive" has to mean for a type third parties both read and write.
//
// A kind a reader does not recognize is content it cannot express. The honest
// answer is to refuse the turn rather than skip the part — see TextOnly and
// UnsupportedPartError.
//
// An assistant's tool calls are deliberately NOT parts. They are not content the
// model produced for a reader: they are a request the host answers by id, they
// are typed rather than free-form, and the wire formats that keep them beside the
// content (OpenAI's `tool_calls`) would only have to take them apart again. They
// stay in Message.ToolCalls.
type Part struct {
	// Kind is what this part carries. See the constants.
	Kind PartKind
	// Text is what a TextPart contributes to the message.
	Text string
	// MediaType and Data carry an ImagePart's MIME type and raw file bytes.
	MediaType string
	Data      []byte
}

// PartKind is what a Part carries. It is an open set from an adapter's point of
// view: a build that does not know a kind cannot express it, and the two are the
// same statement.
type PartKind int

const (
	// TextPart carries text in Part.Text. It is the zero value, so a Part nobody
	// filled in is an empty piece of text rather than content of an unknown kind
	// that every adapter would then have to refuse.
	TextPart PartKind = iota
	// ImagePart carries image bytes in Part.Data, described by Part.MediaType.
	ImagePart
)

// String renders the kind for an error or a test failure, with anything this
// build does not recognize shown as its number so the failure names the actual
// value instead of hiding it behind a word.
func (k PartKind) String() string {
	switch k {
	case TextPart:
		return "text"
	case ImagePart:
		return "image"
	}
	return fmt.Sprintf("unknown(%d)", int(k))
}

// TextMessage is a message whose whole content is one piece of text — what almost
// every message a host builds by hand looks like.
//
// An empty text produces a message with no parts at all, because that is the same
// statement made without a part that carries nothing: an assistant message that is
// only tool calls has no content, and an adapter must not send it an empty block.
func TextMessage(role, text string) Message {
	if text == "" {
		return Message{Role: role}
	}
	return Message{Role: role, Parts: []Part{{Kind: TextPart, Text: text}}}
}

// TextOnly is the message's content as text, for an adapter whose wire format
// carries nothing else. The text is every text part concatenated in order.
//
// The error is what makes this safe to call. An adapter that took the text and
// ignored the rest would send the model a conversation with a hole in it — the
// user attached an image, the model is asked about an image it never received,
// and the answer that comes back is inexplicable from every side, because nothing
// anywhere recorded the drop. So there is no way to get the text without also
// being handed the reason it is not the whole of the message: an
// *UnsupportedPartError naming the first part this build cannot express, which a
// host classifies with errors.As and turns into "this model cannot read that".
//
// A message with no parts is text-only and its text is empty. That is not an
// error and never was one: it is how a message that is only tool calls, or only a
// tool result with nothing to say, has always been expressed.
func (m Message) TextOnly() (string, error) {
	var text strings.Builder
	for _, part := range m.Parts {
		if part.Kind != TextPart {
			return "", &UnsupportedPartError{Kind: part.Kind}
		}
		text.WriteString(part.Text)
	}
	return text.String(), nil
}

// UnsupportedPartError is what an adapter reports when a message carries content
// of a kind its wire format cannot express.
//
// It is published here, rather than left as an error string inside each adapter,
// because it is the one turn failure a host can act on without knowing which
// adapter it is talking to. The model is fine, the credentials are fine, the
// network is fine — the content is what cannot travel, and the recovery is to
// pick a model that reads it. A host that cannot tell this apart from any other
// failure can only show the user a stack of words.
//
// An adapter returns it from Stream before the turn opens, or fails the turn with
// it as the Err of StepFailed. What it must never do is drop the part and stream
// a turn as if the conversation were complete.
type UnsupportedPartError struct {
	// Kind is the first part of the message the adapter could not express.
	Kind PartKind
}

func (e *UnsupportedPartError) Error() string {
	return fmt.Sprintf("message content of kind %s is not supported", e.Kind)
}
