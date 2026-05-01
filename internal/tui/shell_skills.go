package tui

import (
	"forge/internal/skills"
)

// openHubSkillsBrowser opens the skills browser in Hub mode (no workspace
// open). The browser is the same form used by /skills inside a workspace,
// constructed with a skills.Manager that targets the global cache + install
// dirs (~/.codex/cache/skills, ~/.codex/skills) so installed entries land
// outside any workspace.
//
// On any error (manager construction, etc.) the entry just sets a status
// message; the user can still browse Recent / Pinned without disruption.
func (m *shellModel) openHubSkillsBrowser() {
	if err := skills.EnsureGlobalDirs(); err != nil {
		m.statusMessage = "Hub skills: " + err.Error()
		return
	}
	mgr := skills.NewGlobalManager(skills.Options{})
	form, cmd := newSkillsForm(mgr.Cwd(), mgr, m.theme, nil, false)
	m.skillsForm = form
	m.activeHubForm = hubFormSkills
	m.statusMessage = "Browsing global skills (~/.codex/skills)"
	_ = cmd // The form's first cmd typically loads cache async; safe to drop here -- the next Update tick re-issues if needed.
}
