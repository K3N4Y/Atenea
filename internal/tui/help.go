package tui

// Two help surfaces, both costing zero permanent lines: an arrival hint that
// shares the row the layout already reserves under the composer (the same row
// as the confirmations and the git summary) and disappears once the
// conversation starts, and /help, which prints the whole key map into the
// transcript on demand. A permanent footer of every binding was rejected: it
// costs a row forever and stops discriminating between the keys that matter now
// and the ones that do not.

// composerHints is the arrival hint in decreasing width: gitSummaryLine picks
// the longest variant that fits beside the git summary. It points at the
// gestures that open every other surface, and names /help rather than a bare
// "?" because the composer is a free text field: a lone character cannot be a
// shortcut there without stealing the first keystroke of a real prompt.
var composerHints = []string{
	"enter send · / commands · @ files · /help keys",
	"/ commands · @ files · /help keys",
	"/help keys",
}

// helpNotice is the full key map, printed as one dim transcript block. It lists
// gestures, not commands: the "/" menu already enumerates the commands, and
// repeating them here would rot with the next one added.
const helpNotice = `keys
  enter send · ctrl+j newline · ctrl+v paste image · esc esc cancel run
  / commands · @ files · tab take the menu selection · esc close the menu
  ↑↓ prompt history · pgup/pgdn scroll · drag to select and copy
  tab plan mode · shift+tab fold or unfold every thought · ctrl+c ctrl+c quit
approvals
  y allow once · a allow all session · n deny
  ←→ pick action · enter confirm · esc deny · ↑↓ scroll the request
  plan: y run · n stay in plan · error: r retry · d details
pickers
  ↑↓ move · enter select · space toggle · r reload · esc close`
