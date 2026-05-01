package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"forge/internal/globalconfig"

	"github.com/pelletier/go-toml/v2"
)

// migrationProposal collects the fields a single workspace would contribute
// to the global config, plus the path of the workspace toml so we know
// where to remove them on accept.
type migrationProposal struct {
	WorkspacePath string
	Theme         string            // empty = no proposal for theme from this workspace
	Models        map[string]string // role -> model
	YarnProfile   string            // empty = no yarn proposal
}

// HasContent reports whether the proposal would actually move anything.
// Used to skip workspaces that have nothing migration-worthy.
func (p migrationProposal) HasContent() bool {
	return p.Theme != "" || len(p.Models) > 0 || p.YarnProfile != ""
}

// scanWorkspacesForMigration walks each Recent + Pinned workspace and reads
// its raw .forge/config.toml looking for keys we now support globally
// (theme, models, yarn.profile). The returned list only contains workspaces
// with at least one migratable field.
func scanWorkspacesForMigration(state HubState) []migrationProposal {
	seen := map[string]bool{}
	paths := []string{}
	for _, p := range state.Pinned {
		if !seen[p] {
			paths = append(paths, p)
			seen[p] = true
		}
	}
	for _, r := range state.RecentWorkspaces {
		if !seen[r.Path] {
			paths = append(paths, r.Path)
			seen[r.Path] = true
		}
	}

	out := make([]migrationProposal, 0, len(paths))
	for _, path := range paths {
		prop, err := proposalFromWorkspace(path)
		if err != nil {
			continue
		}
		if prop.HasContent() {
			out = append(out, prop)
		}
	}
	// Stable order so the rendered list does not jump around between
	// paint cycles.
	sort.Slice(out, func(i, j int) bool {
		return out[i].WorkspacePath < out[j].WorkspacePath
	})
	return out
}

func proposalFromWorkspace(path string) (migrationProposal, error) {
	tomlPath := filepath.Join(path, ".forge", "config.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return migrationProposal{}, err
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return migrationProposal{}, err
	}
	prop := migrationProposal{WorkspacePath: path}

	// theme is normally outside any section.
	if v, ok := raw["theme"].(string); ok && v != "" {
		prop.Theme = v
	}
	if models, ok := raw["models"].(map[string]any); ok {
		got := map[string]string{}
		for role, val := range models {
			if s, ok := val.(string); ok && s != "" {
				got[role] = s
			}
		}
		if len(got) > 0 {
			prop.Models = got
		}
	}
	if yarn, ok := raw["yarn"].(map[string]any); ok {
		if v, ok := yarn["profile"].(string); ok && v != "" {
			prop.YarnProfile = v
		}
	} else if context, ok := raw["context"].(map[string]any); ok {
		if yarn, ok := context["yarn"].(map[string]any); ok {
			if v, ok := yarn["profile"].(string); ok && v != "" {
				prop.YarnProfile = v
			}
		}
	}
	return prop, nil
}

// applyMigrationProposals merges the accepted proposals into the global
// config (writing only fields the user accepted) and rewrites each
// affected workspace toml to drop the migrated keys. Workspaces become
// minimal: they keep whatever they had that we didn't migrate (provider
// settings, plugin overrides, whatever).
//
// The wizard logic in the caller decides "accepted" -- accept-all is the
// sensible default; per-workspace accept/reject can be added later.
func applyMigrationProposals(accepted []migrationProposal) error {
	if len(accepted) == 0 {
		return nil
	}
	g, err := globalconfig.Load()
	if err != nil {
		return err
	}
	// First proposal that has the field wins -- we don't try to merge
	// conflicts across workspaces because the user can edit the global
	// file later via the Hub Theme form anyway.
	if g.Theme == nil {
		for _, p := range accepted {
			if p.Theme != "" {
				t := p.Theme
				g.Theme = &t
				break
			}
		}
	}
	if g.Models == nil {
		g.Models = map[string]string{}
	}
	for _, p := range accepted {
		for role, model := range p.Models {
			if _, exists := g.Models[role]; !exists {
				g.Models[role] = model
			}
		}
	}
	if g.Yarn == nil {
		g.Yarn = &globalconfig.YarnDefaults{}
	}
	if g.Yarn.Profile == nil {
		for _, p := range accepted {
			if p.YarnProfile != "" {
				profile := p.YarnProfile
				g.Yarn.Profile = &profile
				break
			}
		}
	}
	if err := globalconfig.Save(g); err != nil {
		return err
	}

	// Strip migrated keys from each workspace's toml so the resolution
	// order genuinely picks them up from global. We rewrite the file
	// rather than mutate in place to avoid edge cases with TOML comments.
	for _, p := range accepted {
		if err := scrubWorkspaceConfig(p); err != nil {
			// Don't fail the whole migration on one workspace that's
			// unwriteable (read-only project, perhaps). Continue.
			continue
		}
	}
	return nil
}

// acceptMigration applies all current proposals, marks the wizard done,
// and snaps the view back to the explorer so the user lands somewhere
// useful. Status message reflects what was migrated.
func (m *shellModel) acceptMigration() {
	if err := applyMigrationProposals(m.migrationProposals); err != nil {
		m.statusMessage = "Migration failed: " + err.Error()
	} else {
		m.statusMessage = fmt.Sprintf("Migrated %d workspace(s) into the global config.", len(m.migrationProposals))
	}
	m.migrationProposals = nil
	m.hubState.MigrationDone = true
	m.saveHubState()
	m.activeView = viewExplorer
	m.selectSidebarView(viewExplorer)
}

// dismissMigration flips MigrationDone without applying, so the wizard
// won't reappear next launch. Idempotent.
func (m *shellModel) dismissMigration() {
	m.migrationProposals = nil
	m.hubState.MigrationDone = true
	m.saveHubState()
	m.activeView = viewExplorer
	m.selectSidebarView(viewExplorer)
	m.statusMessage = "Migration skipped. Open Settings to manage Hub defaults."
}

func scrubWorkspaceConfig(p migrationProposal) error {
	tomlPath := filepath.Join(p.WorkspacePath, ".forge", "config.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return err
	}
	if p.Theme != "" {
		delete(raw, "theme")
	}
	if len(p.Models) > 0 {
		if models, ok := raw["models"].(map[string]any); ok {
			for role := range p.Models {
				delete(models, role)
			}
			if len(models) == 0 {
				delete(raw, "models")
			} else {
				raw["models"] = models
			}
		}
	}
	if p.YarnProfile != "" {
		if yarn, ok := raw["yarn"].(map[string]any); ok {
			delete(yarn, "profile")
			if len(yarn) == 0 {
				delete(raw, "yarn")
			}
		}
		if context, ok := raw["context"].(map[string]any); ok {
			if yarn, ok := context["yarn"].(map[string]any); ok {
				delete(yarn, "profile")
				if len(yarn) == 0 {
					delete(context, "yarn")
				}
			}
			if len(context) == 0 {
				delete(raw, "context")
			}
		}
	}
	out, err := toml.Marshal(raw)
	if err != nil {
		return err
	}
	return os.WriteFile(tomlPath, out, 0o644)
}
