package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"forge/internal/globalconfig"
)

// maxStderrLogBytes caps the size of .forge/forge.log at the start of a
// session. On boot, if the existing file exceeds this, it is rotated to
// forge.log.old before we open the fresh handle. Rotation is a one-shot at
// startup — mid-run growth is left alone to avoid reopen/lock complexity on
// Windows.
const maxStderrLogBytes = 1 << 20 // 1 MB

// redirectStderrToLog opens .forge/forge.log and reassigns os.Stderr to it.
// Forge is a bubbletea TUI: anything written to stderr mid-session gets
// scribbled across the rendered frame and is effectively invisible to the
// user. Routing stderr to a file makes our diagnostic writes (LM Studio load
// echoes, patch.Apply traces, approval failures) readable via `tail -f`.
//
// Returns the opened log file so the caller can close it on shutdown, or
// nil on failure. A failure here is non-fatal — we just keep stderr pointing
// at the TTY.
func redirectStderrToLog(cwd string) *os.File {
	forgeDir := filepath.Join(cwd, ".forge")
	return redirectStderrToPath(filepath.Join(forgeDir, "forge.log"))
}

// RedirectStderrToHome opens ~/.forge/forge.log and reassigns os.Stderr.
// Used at Shell startup so Hub-only sessions (no workspace) still get a
// readable log. When a workspace opens later, it'll redirect again to
// the workspace-local forge.log.
func RedirectStderrToHome() *os.File {
	return redirectStderrToPath(filepath.Join(globalconfig.HomeDir(), "forge.log"))
}

func redirectStderrToPath(logPath string) *os.File {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil
	}

	if info, err := os.Stat(logPath); err == nil && info.Size() > maxStderrLogBytes {
		_ = os.Rename(logPath, logPath+".old")
	}

	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	fmt.Fprintf(file, "\n=== forge stderr session start: %s ===\n", time.Now().Format(time.RFC3339))
	os.Stderr = file
	return file
}
