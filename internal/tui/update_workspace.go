package tui

import (
	"os"

	"forge/internal/buildinfo"
)

// handleUpdateCommand backs the workspace `/update` slash command. The
// flow mirrors shellModel.triggerUpdate but plumbs the resulting tea.Cmd
// through model.pendingCommand so the workspace transcript shows progress
// and the eventual outcome message inline with chat history.
func (m *model) handleUpdateCommand() string {
	if !buildinfo.HasSourceRepo() {
		return m.theme.Muted.Render("update is disabled: this binary was not built with scripts/build.{ps1,sh}.")
	}
	if m.updateRunning {
		return m.theme.Muted.Render("update already in progress.")
	}
	exePath, err := os.Executable()
	if err != nil {
		return m.theme.ErrorStyle.Render("cannot resolve current executable: " + err.Error())
	}
	m.updateRunning = true
	m.pendingCommand = runUpdateCmd(exePath)
	return m.theme.Muted.Render("Running /update: git pull --ff-only && go build ./cmd/forge ...")
}
