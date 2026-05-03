package tui

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// errHeadlessSession is returned by openInDefaultApp when the current
// session has no GUI to launch into (typical SSH-into-Linux setup).
// Callers handle it by showing the artifact path instead of pretending
// the launch succeeded.
var errHeadlessSession = errors.New("headless session: no GUI available")

// isHeadlessSession reports whether the current process is unlikely to
// have a desktop environment available for xdg-open/rundll32/open. The
// detection is intentionally conservative — false positives are worse
// than a failed launch, since macOS / Windows users almost never SSH
// into their desktops.
//
// Triggers:
//   - SSH_CONNECTION or SSH_CLIENT set (we are inside an SSH session)
//   - On Linux/BSD: DISPLAY and WAYLAND_DISPLAY both empty (no X11/Wayland
//     server reachable)
//
// macOS and Windows are assumed to have a GUI unless we are inside SSH.
func isHeadlessSession() bool {
	if strings.TrimSpace(os.Getenv("SSH_CONNECTION")) != "" || strings.TrimSpace(os.Getenv("SSH_CLIENT")) != "" {
		return true
	}
	switch runtime.GOOS {
	case "windows", "darwin":
		return false
	default:
		return strings.TrimSpace(os.Getenv("DISPLAY")) == "" && strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) == ""
	}
}

// openInDefaultApp launches the OS-default application for the given
// path or URL and returns immediately. Used for any on-disk artifact
// surfaced to the user (QR PNGs, generated HTML reports) where forcing
// a context switch into a file manager would be a worse UX.
//
// Returns errHeadlessSession when the current process is detected as
// SSH-headless; callers should fall back to printing the path.
func openInDefaultApp(path string) error {
	if isHeadlessSession() {
		return errHeadlessSession
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}
