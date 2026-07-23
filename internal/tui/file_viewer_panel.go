package tui

// The fileViewerPanel module owns the read-only file viewer: the main-area
// view the user opens from the explorer to read a workspace file. It is the
// second Model panel extracted into a self-contained sub-model behind the input
// router (see input_router.go), following the explorer idiom: the root Model
// embeds one panel, routes keyboard input to it when the active target is
// targetViewer, routes wheel events when the pointer is over the viewer, and
// asks it for its column/full-screen body in the layout.
//
// The module owns everything about the viewer EXCEPT the cross-panel routing
// state it must not reach into:
//
//   - focus (panelFocus) stays on Model. Closing the viewer does not flip focus
//     here; it surfaces an intent (viewerIntent{closeToChat: true}) that the
//     root applies by returning focus to the chat and restoring the transcript's
//     saved scroll offset. This keeps the coupling explicit and one-directional,
//     mirroring how the explorer surfaces openPath/closePanel.
//   - The transcript scroll offset lives on Model's viewport. The panel only
//     remembers the offset to restore on close (returnY, captured at open); the
//     root reads it back when it applies the close intent.
//
// Model embeds fileViewerPanel anonymously, so the state fields below (viewer,
// viewerLoading, viewerGen, viewerPending, viewerReturnY, fileReader) promote
// onto Model unchanged — the same idiom the explorer, the Transcript module,
// and the overlay pickers use. Field names are preserved so the Model-level
// layout helpers and the existing behavior tests keep reading them as the
// Model's own.

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// fileViewerPanel is the read-only file viewer sub-model. It holds exactly the
// viewer-specific state that used to live scattered on Model plus the behavior
// over it. Value-in / value-out: mutating methods take a value receiver and
// return the updated panel, mirroring the package idiom.
type fileViewerPanel struct {
	// fileReader is the injected workspace reader that loads a file's bytes for
	// the async open. A nil reader renders an explicit "not configured" error.
	fileReader FileReader
	// viewer is the content/scroll model (path, highlighted lines, offset,
	// status message). Its type and pure logic live in file_viewer.go; the panel
	// wraps it with the reader, load state, generation guard, pending scroll, and
	// the transcript offset to restore on close.
	viewer fileViewer
	// viewerLoading marks an in-flight async load: scroll keys/wheel queue their
	// deltas into viewerPending instead of moving the not-yet-loaded content.
	viewerLoading bool
	// viewerGen guards async load results: a stale fileOpenedMsg (generation !=
	// viewerGen) is ignored so slow disk work never overwrites a newer request.
	viewerGen uint64
	// viewerPending accumulates scroll deltas requested while loading; they are
	// applied to the freshly loaded content when the load lands.
	viewerPending int
	// viewerReturnY is the transcript scroll offset captured at open, restored by
	// the root when the panel closes so the chat resumes exactly where it was.
	viewerReturnY int
}

// viewerIntent is what a key handler asks the root Model to do on the viewer's
// behalf, keeping the panel from reaching into focus/viewport state. The zero
// value means "nothing outward, the returned panel already reflects the change".
type viewerIntent struct {
	// closeToChat asks the root to return focus to the chat and restore the
	// transcript scroll offset (the returned panel's returnY, exposed via
	// returnY()). The panel has already reset its own content on close.
	closeToChat bool
}

// isOpen reports whether a file is currently shown. Callers that hold a panel
// value (the layout, the router seams) use this instead of touching the field.
func (v fileViewerPanel) isOpen() bool { return v.viewer.active() }

// returnY is the transcript scroll offset to restore when the panel closes; the
// root reads it while applying a closeToChat intent.
func (v fileViewerPanel) returnY() int { return v.viewerReturnY }

// open loads (or replaces) the viewer on a workspace-relative path. It captures
// the transcript offset to restore on close, bumps the generation, resets any
// queued scroll, and shows a loading placeholder while the async read runs. With
// no reader configured it renders the "not configured" error synchronously. It
// returns the load command; the caller owns focus, which is a Model-level
// concern. returnY is the transcript offset to remember (Model layout state).
func (v fileViewerPanel) open(path string, returnY int) (fileViewerPanel, tea.Cmd) {
	v.viewerReturnY = returnY
	v.viewerGen++
	v.viewerLoading = true
	v.viewerPending = 0
	v.viewer = fileViewer{path: path, message: "cargando archivo…"}
	if v.fileReader == nil {
		v.viewer = openFileViewerError(path, errors.New("lector de archivos no configurado"))
		v.viewerLoading = false
		return v, nil
	}
	generation := v.viewerGen
	reader := v.fileReader
	return v, func() tea.Msg {
		content, err := reader(path)
		if err != nil {
			return fileOpenedMsg{generation: generation, path: path, viewer: openFileViewerError(path, err)}
		}
		return fileOpenedMsg{generation: generation, path: path, viewer: openFileViewer(path, content)}
	}
}

// close clears the viewer content and any in-flight load so the panel reports
// closed. Focus and the transcript scroll restore stay with the root (which
// reads returnY); this method only resets the panel's own state.
func (v fileViewerPanel) close() fileViewerPanel {
	v.viewer = fileViewer{}
	v.viewerLoading = false
	v.viewerPending = 0
	return v
}

// applyOpened folds an async load result into the viewer, guarded by both the
// generation and the requested path so a stale result (a load superseded by a
// newer open) is ignored — slow disk work never overwrites a newer request. On
// a match it stores the loaded content, clears the loading flag, and replays the
// scroll deltas queued while loading. height is the panel's body row capacity
// (a Model-level layout quantity passed in).
func (v fileViewerPanel) applyOpened(msg fileOpenedMsg, height int) fileViewerPanel {
	if msg.generation != v.viewerGen || msg.path != v.viewer.path {
		return v
	}
	pendingScroll := v.viewerPending
	v.viewer = msg.viewer
	v.viewerLoading = false
	v.viewerPending = 0
	v.viewer.scroll(pendingScroll, height)
	v.viewer.clamp(height)
	return v
}

// resize re-clamps the scroll offset after a terminal resize so a newly shorter
// window never scrolls past the end. height is the panel's body row capacity.
func (v fileViewerPanel) resize(height int) fileViewerPanel {
	v.viewer.clamp(height)
	return v
}

// handleKey processes the keyboard while the viewer holds focus. It returns the
// updated panel and an intent for the root: Esc closes and yields closeToChat;
// j/Down, k/Up, PgDn, PgUp scroll the file (queuing into viewerPending while a
// load is in flight so navigation received mid-load applies on landing) and
// yield an empty intent. height is the panel's body row capacity (a Model-level
// layout quantity passed in).
func (v fileViewerPanel) handleKey(msg tea.KeyMsg, height int) (fileViewerPanel, viewerIntent) {
	switch {
	case msg.Type == tea.KeyEsc:
		v = v.close()
		return v, viewerIntent{closeToChat: true}
	case msg.Type == tea.KeyDown || keyRune(msg) == "j":
		v.scrollBy(1, height)
	case msg.Type == tea.KeyUp || keyRune(msg) == "k":
		v.scrollBy(-1, height)
	case msg.Type == tea.KeyPgDown:
		v.scrollBy(max(height, 1), height)
	case msg.Type == tea.KeyPgUp:
		v.scrollBy(-max(height, 1), height)
	}
	return v, viewerIntent{}
}

// handleMouse processes a wheel event known to fall over the viewer (the root
// gates on the pointer before calling). The wheel scrolls the file three rows
// per notch, queuing into viewerPending while a load is in flight. Non-wheel and
// non-press events are ignored. height is the panel's body row capacity.
func (v fileViewerPanel) handleMouse(msg tea.MouseMsg, height int) fileViewerPanel {
	if msg.Action != tea.MouseActionPress {
		return v
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		v.scrollBy(-3, height)
	case tea.MouseButtonWheelDown:
		v.scrollBy(3, height)
	}
	return v
}

// scrollBy moves the file by delta rows. While a load is in flight the delta is
// queued into viewerPending (the content is not there yet) so it replays when
// the load lands; otherwise it scrolls the loaded content immediately.
func (v *fileViewerPanel) scrollBy(delta, height int) {
	if v.viewerLoading {
		v.viewerPending += delta
		return
	}
	v.viewer.scroll(delta, height)
}

// view renders the viewer body: a status header (path + visible line range, or
// the status message for empty/binary/too-large/error states) over the windowed
// lines. width is the panel column width and height the full body height; the
// panel reserves its own header row. This mirrors the former renderFileViewer.
func (v fileViewerPanel) view(width, height int) string {
	margin := composerOuterMargin
	if width >= 0 {
		margin = min(margin, width/2)
		width = max(width-2*margin, 0)
	}
	contentHeight := max(height-1, 0)
	header := statusStyle.Render(v.viewer.header(width, contentHeight))
	body := v.viewer.render(width, contentHeight)
	content := header
	if body == "" {
		return strings.Repeat(" ", margin) + content
	}
	content += "\n" + body
	return strings.Repeat(" ", margin) + strings.ReplaceAll(content, "\n", "\n"+strings.Repeat(" ", margin))
}
