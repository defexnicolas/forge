package tui

import (
	"fmt"
	"strings"
)

// renderMigrationWizard draws the first-run wizard view that proposes
// moving theme/models/yarn from existing workspace tomls into the new
// global config. Read-only listing; the user accepts all (Enter) or
// dismisses (Esc) -- partial accept can be added later.
func (m shellModel) renderMigrationWizard() string {
	if len(m.migrationProposals) == 0 {
		return m.theme.Muted.Render("Nothing to migrate. Press Esc to continue.")
	}
	lines := []string{
		m.theme.Accent.Render("Hub migration wizard"),
		m.theme.Muted.Render("Detected workspace configs that can become Hub defaults so every workspace inherits them."),
		"",
		m.theme.StatusValue.Render("What will move into ~/.codex/forge/global.toml:"),
		"",
	}
	for _, p := range m.migrationProposals {
		lines = append(lines, "  "+p.WorkspacePath)
		if p.Theme != "" {
			lines = append(lines, "    "+m.theme.Muted.Render("theme = "+p.Theme))
		}
		if len(p.Models) > 0 {
			parts := make([]string, 0, len(p.Models))
			for role, model := range p.Models {
				parts = append(parts, fmt.Sprintf("%s=%s", role, model))
			}
			lines = append(lines, "    "+m.theme.Muted.Render("models: "+strings.Join(parts, ", ")))
		}
		if p.YarnProfile != "" {
			lines = append(lines, "    "+m.theme.Muted.Render("yarn.profile = "+p.YarnProfile))
		}
	}
	lines = append(lines,
		"",
		m.theme.Muted.Render("Enter: accept all and remove migrated keys from each workspace toml"),
		m.theme.Muted.Render("Esc:    dismiss without migrating (won't ask again)"),
	)
	return strings.Join(lines, "\n")
}
