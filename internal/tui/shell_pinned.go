package tui

import (
	"fmt"
	"strings"
)

// renderPinned draws the Pinned view in the Hub main pane. Identical layout
// to renderRecent so the user can scan both lists with one mental model.
// Pinned entries are rendered exactly in the order the user pinned them --
// no MRU sort.
func (m shellModel) renderPinned() string {
	lines := []string{
		m.theme.Muted.Render("Pinned workspaces. Use P in Recent or Pinned to toggle."),
		"",
	}
	if len(m.hubState.Pinned) == 0 {
		lines = append(lines, m.theme.Muted.Render("Nothing pinned yet. Select a workspace in Recent and press P."))
		return strings.Join(lines, "\n")
	}
	for i, path := range m.hubState.Pinned {
		prefix := "  "
		if i == m.pinnedIndex {
			prefix = "> "
		}
		lines = append(lines, fmt.Sprintf("%s%s", prefix, path))
	}
	return strings.Join(lines, "\n")
}

// openPinnedWorkspace opens the workspace at the current pinnedIndex.
func (m *shellModel) openPinnedWorkspace() {
	if m.pinnedIndex < 0 || m.pinnedIndex >= len(m.hubState.Pinned) {
		return
	}
	if err := m.OpenWorkspace(m.hubState.Pinned[m.pinnedIndex]); err != nil {
		m.statusMessage = "Open workspace failed: " + err.Error()
	}
}

// openHubSkillsBrowser is wired in shell_skills.go (commit that lands the
// global skills manager) so this file just declares the entry point.
// Currently a status message until the next commit fills it in.

// togglePinForActiveSelection pins or unpins the workspace currently
// selected in Recent or Pinned. From Pinned: toggle current pinned entry.
// From Recent: toggle the recent entry. Anywhere else: no-op (the user
// pressed P somewhere it doesn't apply).
func (m *shellModel) togglePinForActiveSelection() {
	var target string
	switch m.activeView {
	case viewPinned:
		if m.pinnedIndex < 0 || m.pinnedIndex >= len(m.hubState.Pinned) {
			return
		}
		target = m.hubState.Pinned[m.pinnedIndex]
	case viewRecent:
		if m.recentIndex < 0 || m.recentIndex >= len(m.hubState.RecentWorkspaces) {
			return
		}
		target = m.hubState.RecentWorkspaces[m.recentIndex].Path
	default:
		return
	}

	if m.hubState.IsPinned(target) {
		if m.hubState.Unpin(target) {
			m.statusMessage = "Unpinned " + target
		}
		// Snap pinnedIndex back into range so the cursor doesn't dangle
		// off the end of the now-shorter list.
		if m.pinnedIndex >= len(m.hubState.Pinned) && m.pinnedIndex > 0 {
			m.pinnedIndex = len(m.hubState.Pinned) - 1
		}
	} else {
		if m.hubState.Pin(target) {
			m.statusMessage = "Pinned " + target
		}
	}
	m.saveHubState()
}
