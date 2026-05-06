package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"forge/internal/buildinfo"
	"forge/internal/updater"

	tea "github.com/charmbracelet/bubbletea"
)

// Messages flowing through the bubbletea Update loop. Each is dispatched
// from a tea.Cmd that runs git/go in a goroutine so the TUI never blocks.

type updateCheckResultMsg struct {
	status updater.Status
}

type updateRunResultMsg struct {
	pull        updater.PullResult
	pullErr     error
	build       updater.BuildResult
	buildErr    error
	statusAfter updater.Status
}

type updateTickMsg struct{}

// updateCheckCmd runs updater.Check in a goroutine and emits the result.
// Wrapped to honor a 30s timeout so a hung `git fetch` doesn't strand the
// banner in "checking..." forever.
func updateCheckCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return updateCheckResultMsg{status: updater.Check(ctx)}
	}
}

// scheduleUpdateTick returns a tea.Cmd that fires after the configured
// interval. Returning nil disables the periodic check.
func scheduleUpdateTick(intervalMinutes int) tea.Cmd {
	if intervalMinutes <= 0 {
		return nil
	}
	return tea.Tick(time.Duration(intervalMinutes)*time.Minute, func(time.Time) tea.Msg {
		return updateTickMsg{}
	})
}

// runUpdateCmd runs Pull then Build and returns a single message with the
// outcome of both. We deliberately do not interleave UI updates between
// pull and build — for the user it's a single "update" action.
func runUpdateCmd(exePath string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		msg := updateRunResultMsg{}
		msg.pull, msg.pullErr = updater.Pull(ctx)
		// Only attempt the rebuild if the pull actually advanced HEAD.
		// A dirty-worktree result reports as Pulled=false with no error
		// and DirtyMsg populated — that's surfaced in the UI.
		if msg.pullErr == nil && msg.pull.Pulled {
			msg.build, msg.buildErr = updater.Build(ctx, exePath)
		}
		// Re-check so the banner reflects the new state immediately.
		msg.statusAfter = updater.Check(ctx)
		return msg
	}
}

// updateBannerLine renders the single-line banner for the Hub footer when
// the running binary is behind origin. Returns "" when no banner should
// show (disabled, up-to-date, or check hasn't completed yet).
func (m shellModel) updateBannerLine() string {
	if !buildinfo.HasSourceRepo() {
		return ""
	}
	t := m.theme
	st := m.updateStatus
	switch st.State {
	case updater.StateBehind:
		count := fmt.Sprintf("%d commit", st.CommitsBehind)
		if st.CommitsBehind != 1 {
			count += "s"
		}
		text := fmt.Sprintf(" update: %s behind on %s — press u to /update", count, st.Branch)
		return t.Warning.Render(text)
	case updater.StateDiverged:
		text := fmt.Sprintf(" update: diverged from origin/%s (%d behind, %d ahead) — resolve manually", st.Branch, st.CommitsBehind, st.CommitsAhead)
		return t.ErrorStyle.Render(text)
	case updater.StateError:
		// Surface transient failures briefly so users notice the banner
		// is not stuck pretending all is well, but truncate so a long
		// stderr from `git fetch` doesn't blow out the panel width.
		msg := strings.TrimSpace(st.Error)
		if len(msg) > 120 {
			msg = msg[:117] + "..."
		}
		return t.Muted.Render(" update check failed: " + msg)
	}
	return ""
}

// triggerUpdate kicks off the pull + build flow. Returns nil + sets
// statusMessage when the action can't run (no source repo, already
// running, no new commits to pull). Returns a tea.Cmd otherwise.
//
// Used both by the Hub 'u' keybind and the /update slash command. The
// behaviour is the same in both entry points: pull --ff-only, then go
// build, then re-check the status.
func (m *shellModel) triggerUpdate() tea.Cmd {
	if !buildinfo.HasSourceRepo() {
		m.statusMessage = "update is disabled: rebuild with scripts/build.sh, scripts/build.ps1, or bash forgetui.sh to embed the source-repo path"
		return nil
	}
	if m.updateRunning {
		m.statusMessage = "update already in progress"
		return nil
	}
	exePath, err := os.Executable()
	if err != nil {
		m.statusMessage = "cannot resolve current executable: " + err.Error()
		return nil
	}
	m.updateRunning = true
	m.statusMessage = "running /update: git pull --ff-only && go build ..."
	return runUpdateCmd(exePath)
}

// describeUpdateRun produces the human-readable status message after a
// /update invocation. Routes through statusMessage so the user sees it in
// the hub status bar and the Forge log.
func (m shellModel) describeUpdateRun(msg updateRunResultMsg) string {
	if msg.pullErr != nil {
		return "update failed during git pull: " + msg.pullErr.Error()
	}
	if !msg.pull.Pulled {
		if strings.TrimSpace(msg.pull.DirtyMsg) != "" {
			return "update aborted: working tree has uncommitted changes. Commit or stash, then run /update again."
		}
		return "already up to date"
	}
	if msg.buildErr != nil {
		manual := updater.ManualCommand()
		base := "git pull succeeded but go build failed: " + msg.buildErr.Error()
		if manual != "" {
			base += "\n  manual rebuild: " + manual
		}
		return base
	}
	if msg.build.StagedPath != "" {
		// Windows: new binary written next to the current one with .new suffix.
		// Forge can't replace the running .exe, so the user must restart and
		// the launcher script (or shell alias) must rename on next start.
		return "update applied: pull succeeded and build wrote " + msg.build.StagedPath + ". Quit forge and rename it over the running .exe to finish."
	}
	return "update applied: pull + build succeeded. Restart forge to load the new binary."
}
