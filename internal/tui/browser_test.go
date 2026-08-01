package tui

import (
	"strings"
	"testing"
)

// TestBrowserCommand_PerPlatform pins the launcher argv. Windows matters most:
// `cmd /c start` is a shell builtin and an authorize URL always carries `&`,
// which cmd would read as a command separator — rundll32 takes the URL as one
// argument.
func TestBrowserCommand_PerPlatform(t *testing.T) {
	url := "https://us.posthog.com/oauth/authorize?a=1&b=2"
	for goos, want := range map[string]string{
		"linux":   "xdg-open " + url,
		"darwin":  "open " + url,
		"windows": "rundll32 url.dll,FileProtocolHandler " + url,
	} {
		command := browserCommand(goos, url)
		if got := strings.Join(command.Args, " "); got != want {
			t.Errorf("%s: argv = %q, want %q", goos, got, want)
		}
	}
}
