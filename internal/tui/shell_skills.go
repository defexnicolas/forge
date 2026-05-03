package tui

import (
	"forge/internal/skills"

	tea "github.com/charmbracelet/bubbletea"
)

// openHubSkillsBrowser opens the skills browser in Hub mode (no workspace
// open). The browser is the same form used by /skills inside a workspace,
// constructed with a skills.Manager that targets the global cache + install
// dirs (~/.forge/cache/skills, ~/.forge/skills) so installed entries land
// outside any workspace. Skills already installed under the legacy
// ~/.codex/skills/ path stay readable — manager's searchDirs scans both.
//
// Returns the form's initial tea.Cmd (the async cache load) so the caller
// can hand it to Bubble Tea. Dropping that cmd was the bug behind "remote
// skills don't load in the Hub view": loading=true would never resolve
// because the load goroutine never started.
//
// On any error (manager construction, etc.) the entry just sets a status
// message and returns nil; the user can still browse Recent / Pinned
// without disruption.
func (m *shellModel) openHubSkillsBrowser() tea.Cmd {
	if err := skills.EnsureGlobalDirs(); err != nil {
		m.statusMessage = "Hub skills: " + err.Error()
		return nil
	}
	mgr := skills.NewGlobalManager(skills.Options{})
	form, cmd := newSkillsForm(mgr.Cwd(), mgr, m.theme, nil, false)
	m.skillsForm = form
	m.activeHubForm = hubFormSkills
	m.statusMessage = "Browsing global skills (~/.forge/skills)"
	return cmd
}
