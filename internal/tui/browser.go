package tui

import (
	"os/exec"
	"runtime"
)

// openBrowser launches the user's browser at url, detached: the TUI must not
// block on, or adopt the lifetime of, whatever the desktop decides to run. It
// is a package variable so tests observe the URL instead of spawning a browser.
var openBrowser = func(url string) error {
	command := browserCommand(runtime.GOOS, url)
	if err := command.Start(); err != nil {
		return err
	}
	// Reap in the background so the launcher never lingers as a zombie.
	go func() { _ = command.Wait() }()
	return nil
}

// browserCommand is the platform's own "open this URL" runner. Windows goes
// through rundll32 rather than `cmd /c start` because start is a shell
// builtin: an authorize URL always carries `&`, which cmd would read as a
// command separator instead of as part of the argument.
func browserCommand(goos, url string) *exec.Cmd {
	switch goos {
	case "darwin":
		return exec.Command("open", url)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return exec.Command("xdg-open", url)
	}
}
