package hashline

import "fmt"

// ParseWarning describes a safe, typed recovery from common model-authored syntax.
type ParseWarning struct {
	Code    string
	Message string
	Line    int
}

type EditKind int

const MaxExpandedLines = 100000
const (
	Replace EditKind = iota
	Delete
	Insert
	Cut
	Paste
)

type Cursor int

const (
	BeforeAnchor Cursor = iota
	AfterAnchor
	BOF
	EOF
)

type Range struct{ Start, End int }
type Edit struct {
	Kind       EditKind
	Range      Range
	Cursor     Cursor
	Anchor     int
	Text       string
	Register   string
	Block      bool
	AfterBlock bool
	// BlockStart identifies the authored opener after an after-block edit is lowered.
	// It is zero for literal after-line inserts.
	BlockStart int
}
type FileOp struct {
	Remove bool
	MoveTo string
}
type Section struct {
	Path, Hash string
	Edits      []Edit
	FileOp     FileOp
	Warnings   []ParseWarning
}
type Patch struct {
	Sections []Section
	Warnings []ParseWarning
}
type ApplyResult struct {
	Text             string
	FirstChangedLine int
	Warnings         []string
}

// Clipboard is mutable host-owned state. Named registers persist between
// batches; anonymous state is intentionally useful only within one batch.
type Clipboard struct {
	Anonymous       []string
	Named           map[string][]string
	PendingAnonCuts []string
}

// BlockResolver maps a source line to the inclusive multiline syntactic block
// beginning there. It returns a typed error when the language is unsupported
// or the line is not a valid block opener.
type BlockResolver interface {
	ResolveBlock(path string, lines []string, start int) (end int, err error)
}

// UnsupportedBlockLanguageError reports a path for which no structural grammar exists.
type UnsupportedBlockLanguageError struct{ Path string }

func (e *UnsupportedBlockLanguageError) Error() string {
	return "hashline: unsupported block language for " + e.Path
}

// UnresolvedBlockError reports a line which is not a multiline block opener.
type UnresolvedBlockError struct {
	Path   string
	Line   int
	Reason string
}

func (e *UnresolvedBlockError) Error() string {
	return fmt.Sprintf("hashline: no multiline block starts at %s:%d (%s); anchor the block's opening line without blank lines, closing delimiters, or inner statements", e.Path, e.Line, e.Reason)
}

type MissingTagError struct{ Detail string }

func (e *MissingTagError) Error() string { return "missing [path#HASH] header: " + e.Detail }

type MismatchError struct {
	Path, Expected, Live string
	Recognized           bool
	Context              string
}

func (e *MismatchError) Error() string {
	var msg string
	if e.Recognized {
		msg = fmt.Sprintf("edit: %s changed since it was read (hash %s -> %s); use the latest edit header or read it again", e.Path, e.Expected, e.Live)
	} else {
		msg = fmt.Sprintf("edit: hash #%s is not a snapshot from this session for %s; read the file and use its header", e.Expected, e.Path)
	}
	if e.Context != "" {
		msg += "; " + e.Context
	}
	return msg
}
