package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// reveal.go owns only the Bubble Tea side of the smooth-streaming loop: the
// tick message and the tea.Cmd that schedules it. The pure pacing math and the
// per-entry reveal advance live on the Transcript module (transcript.go), which
// owns the state they mutate; this file is the seam to the command loop, kept
// out of the pure module so Transcript takes no dependency on Bubble Tea.

// revealTickMsg is the tick of the smooth-streaming reveal loop: each tick
// advances the text the view reveals progressively (analogous to the
// spinner.TickMsg of the working indicator).
type revealTickMsg struct{}

// revealTickInterval is the period of the reveal loop (~30 fps): smooth to the
// eye without a rerender per network delta.
const revealTickInterval = 33 * time.Millisecond

// revealTick schedules the next tick of the reveal loop.
func revealTick() tea.Cmd {
	return tea.Tick(revealTickInterval, func(time.Time) tea.Msg {
		return revealTickMsg{}
	})
}
