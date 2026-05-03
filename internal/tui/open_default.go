package tui

import (
	"os/exec"
	"runtime"
)

// openInDefaultApp launches the OS-default application for the given
// path or URL and returns immediately. Used for any on-disk artifact
// surfaced to the user (QR PNGs, generated HTML reports) where forcing
// a context switch into a file manager would be a worse UX.
func openInDefaultApp(path string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}
