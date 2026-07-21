// Package theme holds the TUI color palette: the single source of truth for
// every color the presentation layer paints with. Styles themselves stay with
// the views that compose them (view.go and the widgets in package tui); this
// package owns only the raw color values, so changing the visual identity is a
// one-line edit here rather than a hunt across files.
//
// Values are plain strings so both lipgloss (lipgloss.Color(theme.Accent)) and
// glamour's markdown theme (whose color fields take strings) can share them.
// Single-digit ANSI palette indices ("1".."8") stay legible under any terminal
// theme; hex values pin the few colors that must render exactly.
package theme

const (
	// Canvas is the app background over which everything else is drawn.
	Canvas = "#141414"
	// UserMessage is the background of the user's message bubble.
	UserMessage = "#242424"

	// Accent is the interactive cyan: the user marker, the composer prompt, and
	// markdown headings and links. ANSI 6.
	Accent = "6"
	// Success is the green for healthy, positive signals: a clean tool call,
	// added diff lines, and the current git branch. ANSI 2.
	Success = "2"
	// Error is the red for failures: a failed tool call, removed diff lines, and
	// hard step errors. ANSI 1.
	Error = "1"
	// Warning is the yellow for attention-seeking, non-error states such as a
	// pending permission request. ANSI 3.
	Warning = "3"
	// Border is the muted gray for borders and de-emphasized text: the composer
	// and tree borders, markdown rules, and secondary headings. ANSI 8.
	Border = "8"
	// Muted is the dim gray for tertiary labels, such as the bash hint in the
	// permission panel.
	Muted = "#999999"

	// PermissionPanel, PermissionCommand, and PermissionActive are the permission
	// panel's backgrounds: the panel, its command box, and the active affordance.
	PermissionPanel   = "#303030"
	PermissionCommand = "#3A3A3A"
	PermissionActive  = "#B1B86B"

	// CodeBlock is the subtle background that separates code from the
	// conversational flow, shared by inline code and code blocks. It is ANSI 236
	// for termenv-styled parts; CodeBlockHex is its truecolor twin because chroma
	// styles only accept hex.
	CodeBlock    = "236"
	CodeBlockHex = "#303030"

	// DiffHeaderBg is the background of the header bars of the edit diff card:
	// the file-path bar and each hunk's "@@ … @@" bar. A muted gray band that
	// frames the card without competing with the red/green content below.
	DiffHeaderBg = "#2A2A2A"
	// DiffAddBg and DiffDelBg are the full-width band backgrounds of changed
	// rows in the edit diff card: a dim green behind added lines and a dim red
	// behind removed ones. Kept dark so the ANSI green/red text and the rail bar
	// stay legible on top. Context rows carry no band (they read as plain gray).
	DiffAddBg = "#12251A"
	DiffDelBg = "#2C171A"
)
