package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// panelReader builds a FileReader over an in-memory map, mirroring the
// package's viewerReader helper but usable from this focused test file without
// depending on model_test.go internals.
func panelReader(files map[string][]byte) FileReader {
	return func(path string) ([]byte, error) {
		content, ok := files[path]
		if !ok {
			return nil, errors.New("no existe: " + path)
		}
		return content, nil
	}
}

// runOpen executes the async load command a real runtime would run and folds
// the resulting message back through the panel, returning the loaded panel.
func runOpen(t *testing.T, panel fileViewerPanel, cmd tea.Cmd, height int) fileViewerPanel {
	t.Helper()
	if cmd == nil {
		return panel
	}
	msg, ok := cmd().(fileOpenedMsg)
	if !ok {
		t.Fatalf("open command produced %T, want fileOpenedMsg", cmd())
	}
	return panel.applyOpened(msg, height)
}

func TestFileViewerPanel_OpenLoadsContentAndReportsOpen(t *testing.T) {
	panel := fileViewerPanel{fileReader: panelReader(map[string][]byte{
		"main.go": []byte("package main\nfunc main() {}\n"),
	})}
	panel, cmd := panel.open("main.go", 7)
	if !panel.isOpen() {
		t.Fatal("open must report the panel as open immediately")
	}
	if !panel.viewerLoading || panel.viewer.message != "cargando archivo…" {
		t.Fatalf("pre-load state = loading:%v message:%q, want loading with placeholder", panel.viewerLoading, panel.viewer.message)
	}
	if panel.viewerReturnY != 7 {
		t.Fatalf("returnY = %d, want 7 (transcript offset captured at open)", panel.viewerReturnY)
	}
	panel = runOpen(t, panel, cmd, 4)
	if panel.viewerLoading {
		t.Fatal("loaded panel must clear the loading flag")
	}
	if panel.viewer.path != "main.go" || panel.viewer.lineCount != 2 {
		t.Fatalf("loaded viewer = path:%q lines:%d, want main.go with 2 lines", panel.viewer.path, panel.viewer.lineCount)
	}
}

func TestFileViewerPanel_OpenWithoutReaderShowsConfigError(t *testing.T) {
	panel := fileViewerPanel{}
	panel, cmd := panel.open("main.go", 0)
	if cmd != nil {
		t.Fatal("nil reader must not schedule an async load")
	}
	if panel.viewerLoading {
		t.Fatal("nil reader resolves synchronously, so loading must be false")
	}
	if got := ansi.Strip(panel.viewer.render(80, 4)); !strings.Contains(got, "no se puede abrir main.go") {
		t.Fatalf("render = %q, want configuration error", got)
	}
}

func TestFileViewerPanel_StaleLoadDoesNotOverwriteNewerFile(t *testing.T) {
	panel := fileViewerPanel{fileReader: panelReader(map[string][]byte{
		"first.txt":  []byte("first"),
		"second.txt": []byte("second"),
	})}
	panel, firstCmd := panel.open("first.txt", 0)
	panel, secondCmd := panel.open("second.txt", 0)

	// The slower first load lands after the newer open. Its generation and path
	// no longer match, so it must be discarded — the newer file wins.
	stale := firstCmd().(fileOpenedMsg)
	panel = panel.applyOpened(stale, 4)
	if panel.viewer.path != "second.txt" || panel.viewer.message != "cargando archivo…" {
		t.Fatalf("stale result changed viewer = %+v", panel.viewer)
	}
	if !panel.viewerLoading {
		t.Fatal("stale result must not clear the loading flag of the newer open")
	}

	fresh := secondCmd().(fileOpenedMsg)
	panel = panel.applyOpened(fresh, 4)
	if panel.viewer.path != "second.txt" || panel.viewerLoading {
		t.Fatalf("newer result not applied = path:%q loading:%v", panel.viewer.path, panel.viewerLoading)
	}
	if got := ansi.Strip(strings.Join(panel.viewer.lines, "\n")); got != "second" {
		t.Fatalf("loaded lines = %q, want second", got)
	}
}

func TestFileViewerPanel_ScrollQueuedWhileLoadingReplaysOnLanding(t *testing.T) {
	var body strings.Builder
	for line := 0; line < 40; line++ {
		body.WriteString("line\n")
	}
	panel := fileViewerPanel{fileReader: panelReader(map[string][]byte{"long.txt": []byte(body.String())})}
	panel, cmd := panel.open("long.txt", 0)

	const height = 9
	panel, intent := panel.handleKey(tea.KeyMsg{Type: tea.KeyPgDown}, height)
	if intent != (viewerIntent{}) {
		t.Fatalf("PgDown intent = %+v, want empty", intent)
	}
	if panel.viewerPending == 0 {
		t.Fatal("PgDown while loading must be queued into viewerPending")
	}
	if panel.viewer.offset != 0 {
		t.Fatal("queued scroll must not move the not-yet-loaded content")
	}
	panel = runOpen(t, panel, cmd, height)
	if panel.viewerLoading || panel.viewer.offset == 0 {
		t.Fatalf("landed viewer = loading:%v offset:%d, want queued scroll applied", panel.viewerLoading, panel.viewer.offset)
	}
	if panel.viewerPending != 0 {
		t.Fatalf("viewerPending = %d, want 0 after replay", panel.viewerPending)
	}
}

func TestFileViewerPanel_ScrollKeysMoveLoadedContent(t *testing.T) {
	panel := fileViewerPanel{fileReader: panelReader(map[string][]byte{
		"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"),
	})}
	const height = 3
	panel, cmd := panel.open("many.txt", 0)
	panel = runOpen(t, panel, cmd, height)
	if panel.viewer.offset != 0 {
		t.Fatalf("fresh offset = %d, want 0", panel.viewer.offset)
	}

	panel, _ = panel.handleKey(tea.KeyMsg{Type: tea.KeyDown}, height)
	if panel.viewer.offset != 1 {
		t.Fatalf("j/Down offset = %d, want 1", panel.viewer.offset)
	}
	panel, _ = panel.handleKey(tea.KeyMsg{Type: tea.KeyPgDown}, height)
	if panel.viewer.offset != 4 {
		t.Fatalf("PgDown offset = %d, want 1+height=4", panel.viewer.offset)
	}
	panel, _ = panel.handleKey(tea.KeyMsg{Type: tea.KeyUp}, height)
	if panel.viewer.offset != 3 {
		t.Fatalf("k/Up offset = %d, want 3", panel.viewer.offset)
	}
	panel, _ = panel.handleKey(tea.KeyMsg{Type: tea.KeyPgUp}, height)
	if panel.viewer.offset != 0 {
		t.Fatalf("PgUp offset = %d, want 0", panel.viewer.offset)
	}
}

func TestFileViewerPanel_CloseYieldsIntentAndKeepsReturnY(t *testing.T) {
	panel := fileViewerPanel{fileReader: panelReader(map[string][]byte{"a.txt": []byte("hello\nworld\n")})}
	const height = 4
	panel, cmd := panel.open("a.txt", 12)
	panel = runOpen(t, panel, cmd, height)

	panel, intent := panel.handleKey(tea.KeyMsg{Type: tea.KeyEsc}, height)
	if !intent.closeToChat {
		t.Fatal("Esc must yield a closeToChat intent so the root returns focus to chat")
	}
	if panel.isOpen() {
		t.Fatal("closed panel must no longer report open")
	}
	if panel.returnY() != 12 {
		t.Fatalf("returnY() = %d, want 12 so the root can restore the transcript scroll", panel.returnY())
	}
}

func TestFileViewerPanel_WheelScrollsLoadedAndQueuesLoading(t *testing.T) {
	panel := fileViewerPanel{fileReader: panelReader(map[string][]byte{
		"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"),
	})}
	const height = 3

	// While loading, the wheel queues its delta instead of moving content.
	panel, cmd := panel.open("many.txt", 0)
	panel = panel.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}, height)
	if panel.viewerPending != 3 || panel.viewer.offset != 0 {
		t.Fatalf("wheel while loading = pending:%d offset:%d, want pending 3, offset 0", panel.viewerPending, panel.viewer.offset)
	}
	panel = runOpen(t, panel, cmd, height)

	before := panel.viewer.offset
	panel = panel.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}, height)
	if panel.viewer.offset <= before {
		t.Fatalf("wheel down offset = %d, want greater than %d", panel.viewer.offset, before)
	}
	afterDown := panel.viewer.offset
	panel = panel.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}, height)
	if panel.viewer.offset >= afterDown {
		t.Fatalf("wheel up offset = %d, want less than %d", panel.viewer.offset, afterDown)
	}
	// A non-press wheel event (release) is inert.
	steady := panel.viewer.offset
	panel = panel.handleMouse(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonWheelDown}, height)
	if panel.viewer.offset != steady {
		t.Fatalf("non-press wheel offset = %d, want unchanged %d", panel.viewer.offset, steady)
	}
}

func TestFileViewerPanel_StatusStatesRenderExplicitly(t *testing.T) {
	oversized := make([]byte, maxFileViewerBytes+1)
	for _, test := range []struct {
		name string
		data []byte
		want string
	}{
		{"empty.txt", nil, "archivo vacio: empty.txt"},
		{"image.bin", []byte{'a', 0, 'b'}, "archivo binario: image.bin"},
		{"large.txt", oversized, "archivo demasiado grande (> 1 MiB): large.txt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			panel := fileViewerPanel{fileReader: panelReader(map[string][]byte{test.name: test.data})}
			panel, cmd := panel.open(test.name, 0)
			panel = runOpen(t, panel, cmd, 4)
			if got := ansi.Strip(panel.view(100, 5)); !strings.Contains(got, test.want) {
				t.Fatalf("view = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFileViewerPanel_ReadErrorRendersStatus(t *testing.T) {
	panel := fileViewerPanel{fileReader: panelReader(nil)} // any path is missing
	panel, cmd := panel.open("gone.txt", 0)
	panel = runOpen(t, panel, cmd, 4)
	if got := ansi.Strip(panel.view(100, 5)); !strings.Contains(got, "no se puede abrir gone.txt") {
		t.Fatalf("view = %q, want read-error status", got)
	}
}

func TestFileViewerPanel_ResizeReclampsOffset(t *testing.T) {
	panel := fileViewerPanel{fileReader: panelReader(map[string][]byte{
		"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n"),
	})}
	panel, cmd := panel.open("many.txt", 0)
	panel = runOpen(t, panel, cmd, 3)
	panel, _ = panel.handleKey(tea.KeyMsg{Type: tea.KeyPgDown}, 3)
	if panel.viewer.offset == 0 {
		t.Fatal("precondition: scroll should have moved the offset")
	}
	// A resize to a window taller than the file must pull the offset back to 0.
	panel = panel.resize(20)
	if panel.viewer.offset != 0 {
		t.Fatalf("offset after tall resize = %d, want 0", panel.viewer.offset)
	}
}
