package llm

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// unknownPart stands in for a kind this build does not define.
const unknownPart = PartKind(1 << 30)

func TestTextMessage_CarriesTheTextAsItsOnlyPart(t *testing.T) {
	message := TextMessage("user", "read foo.go")

	if message.Role != "user" {
		t.Errorf("Role = %q, want %q", message.Role, "user")
	}
	if len(message.Parts) != 1 {
		t.Fatalf("Parts = %+v, want exactly one", message.Parts)
	}
	if !reflect.DeepEqual(message.Parts[0], Part{Kind: TextPart, Text: "read foo.go"}) {
		t.Errorf("Parts[0] = %+v, want a text part carrying the text", message.Parts[0])
	}
}

// An empty text is a message with nothing to say, and a part that carries nothing
// is not a way of saying it: an assistant message that is only tool calls must not
// push an empty block onto the wire.
func TestTextMessage_EmptyTextIsNoContentAtAll(t *testing.T) {
	message := TextMessage("assistant", "")

	if len(message.Parts) != 0 {
		t.Errorf("Parts = %+v, want none", message.Parts)
	}
	text, err := message.TextOnly()
	if err != nil || text != "" {
		t.Errorf("TextOnly() = (%q, %v), want (\"\", nil)", text, err)
	}
}

func TestMessageTextOnly_ConcatenatesTextPartsInOrder(t *testing.T) {
	message := Message{Role: "user", Parts: []Part{
		{Kind: TextPart, Text: "first"},
		{Kind: TextPart, Text: ""},
		{Kind: TextPart, Text: " second"},
	}}

	text, err := message.TextOnly()
	if err != nil {
		t.Fatalf("TextOnly() error = %v", err)
	}
	if text != "first second" {
		t.Errorf("TextOnly() = %q, want %q", text, "first second")
	}
}

// A message with no parts is the shape of an assistant turn that only called
// tools, and of a tool result with nothing to report. Neither is an error.
func TestMessageTextOnly_NoPartsIsEmptyTextNotAnError(t *testing.T) {
	message := Message{Role: "assistant", ToolCalls: []ToolCallPart{{ID: "c1", Name: "read"}}}

	text, err := message.TextOnly()
	if err != nil {
		t.Fatalf("TextOnly() error = %v", err)
	}
	if text != "" {
		t.Errorf("TextOnly() = %q, want empty", text)
	}
}

// The point of the two-value shape: the text of a message that is not text-only
// cannot be obtained without the reason it is incomplete.
func TestMessageTextOnly_RefusesAPartItCannotExpress(t *testing.T) {
	message := Message{Role: "user", Parts: []Part{
		{Kind: TextPart, Text: "what is in this?"},
		{Kind: ImagePart},
	}}

	text, err := message.TextOnly()
	if text != "" {
		t.Errorf("TextOnly() returned %q alongside the error: a partial rendering is what the error exists to prevent", text)
	}
	var unsupported *UnsupportedPartError
	if !errors.As(err, &unsupported) {
		t.Fatalf("TextOnly() error = %v, want an *UnsupportedPartError a host can classify", err)
	}
	if unsupported.Kind != ImagePart {
		t.Errorf("Kind = %s, want the part that could not be expressed (%s)", unsupported.Kind, ImagePart)
	}
}

// The error names the first offending part, so an adapter reports the one a
// reader can go and look at rather than whichever came last.
func TestMessageTextOnly_NamesTheFirstUnexpressiblePart(t *testing.T) {
	message := Message{Role: "user", Parts: []Part{
		{Kind: ImagePart},
		{Kind: PartKind(1 << 29)},
	}}

	_, err := message.TextOnly()
	var unsupported *UnsupportedPartError
	if !errors.As(err, &unsupported) {
		t.Fatalf("TextOnly() error = %v, want an *UnsupportedPartError", err)
	}
	if unsupported.Kind != ImagePart {
		t.Errorf("Kind = %s, want the first unexpressible part (%s)", unsupported.Kind, ImagePart)
	}
}

// A wrapped error still classifies: adapters prefix the failure with their own
// name, and a host reads through that with errors.As.
func TestUnsupportedPartError_SurvivesWrapping(t *testing.T) {
	wrapped := errors.Join(errors.New("anthropic"), &UnsupportedPartError{Kind: unknownPart})

	var unsupported *UnsupportedPartError
	if !errors.As(wrapped, &unsupported) {
		t.Fatalf("errors.As did not find the cause in %v", wrapped)
	}
	if !strings.Contains(unsupported.Error(), unknownPart.String()) {
		t.Errorf("Error() = %q, want it to name the kind (%s)", unsupported.Error(), unknownPart)
	}
}

func TestPartKindString_NamesKnownKindsAndShowsAnUnknownKindsValue(t *testing.T) {
	if got := TextPart.String(); got != "text" {
		t.Errorf("TextPart.String() = %q, want %q", got, "text")
	}
	if got := ImagePart.String(); got != "image" {
		t.Errorf("ImagePart.String() = %q, want %q", got, "image")
	}
	if got := unknownPart.String(); !strings.Contains(got, "1073741824") {
		t.Errorf("PartKind(1<<30).String() = %q, want the value itself so a failure names it", got)
	}
}
