package tui

import (
	"runtime"
	"testing"
)

// TestIsHeadlessSessionDetectsSSH locks the SSH detection contract: when
// the user reaches the TUI through SSH, the openInDefaultApp helper must
// short-circuit so callers can fall back to printing the artifact path.
func TestIsHeadlessSessionDetectsSSH(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.0.0.1 22 10.0.0.2 33333")
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("DISPLAY", ":0")
	if !isHeadlessSession() {
		t.Fatal("SSH_CONNECTION set should mark the session headless")
	}
}

// TestIsHeadlessSessionDetectsMissingDisplayLinux verifies that on
// Linux/BSD without an X11 or Wayland server (the typical
// systemd-managed VM case) we treat the session as headless even when
// SSH env vars are absent.
func TestIsHeadlessSessionDetectsMissingDisplayLinux(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skipf("DISPLAY check is Linux/BSD-only; skipping on %s", runtime.GOOS)
	}
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if !isHeadlessSession() {
		t.Fatal("Linux without DISPLAY/WAYLAND_DISPLAY should be headless")
	}
}

// TestIsHeadlessSessionDesktopNotHeadless verifies the negative case:
// a Linux desktop with DISPLAY set, or any macOS/Windows session that
// is not over SSH, should NOT be treated as headless. Otherwise we
// would refuse to launch viewers for users who have a perfectly good
// GUI sitting in front of them.
func TestIsHeadlessSessionDesktopNotHeadless(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	if isHeadlessSession() {
		t.Fatal("desktop session with DISPLAY set should not be headless")
	}
}
