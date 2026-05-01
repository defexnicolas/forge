package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type Plugin struct {
	Name        string
	Path        string
	Source      string
	Description string
	Version     string
	UserConfig  map[string]string
}

type Manager struct {
	cwd string
}

func NewManager(cwd string) *Manager {
	return &Manager{cwd: cwd}
}

func (m *Manager) Discover() ([]Plugin, error) {
	var plugins []Plugin
	roots := []struct {
		path   string
		source string
	}{
		{filepath.Join(m.cwd, ".forge", "plugins"), "forge-local"},
		{filepath.Join(m.cwd, ".claude", "plugins"), "claude-local"},
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots,
			struct {
				path   string
				source string
			}{filepath.Join(home, ".forge", "plugins"), "forge-global"},
			struct {
				path   string
				source string
			}{filepath.Join(home, ".claude", "plugins"), "claude-global"},
		)
	}

	for _, root := range roots {
		found, err := discoverRoot(root.path, root.source)
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, found...)
	}
	return plugins, nil
}

func discoverRoot(root, source string) ([]Plugin, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var plugins []Plugin
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		plugin := Plugin{Name: entry.Name(), Path: path, Source: source}
		if manifest, err := readClaudeManifest(path); err == nil {
			if manifest.Name != "" {
				plugin.Name = manifest.Name
			}
			plugin.Description = manifest.Description
			plugin.Version = manifest.Version
			plugin.UserConfig = manifest.UserConfig
		} else if !looksLikePlugin(path) {
			continue
		}
		plugins = append(plugins, plugin)
	}
	return plugins, nil
}

type claudeManifest struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description"`
	UserConfig  map[string]string `json:"user_config,omitempty"`
}

func readClaudeManifest(path string) (claudeManifest, error) {
	data, err := os.ReadFile(filepath.Join(path, ".claude-plugin", "plugin.json"))
	if err != nil {
		return claudeManifest{}, err
	}
	var manifest claudeManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return claudeManifest{}, err
	}
	return manifest, nil
}

// Enabled represents a persistent enable/disable state for plugins.
type EnabledState struct {
	Disabled map[string]bool `json:"disabled"`
}

// LoadEnabledState reads the plugin state from .forge/plugins.json.
func LoadEnabledState(cwd string) EnabledState {
	data, err := os.ReadFile(filepath.Join(cwd, ".forge", "plugins.json"))
	if err != nil {
		return EnabledState{Disabled: map[string]bool{}}
	}
	var state EnabledState
	if err := json.Unmarshal(data, &state); err != nil {
		return EnabledState{Disabled: map[string]bool{}}
	}
	if state.Disabled == nil {
		state.Disabled = map[string]bool{}
	}
	return state
}

// SaveEnabledState persists the plugin state.
func SaveEnabledState(cwd string, state EnabledState) error {
	_ = os.MkdirAll(filepath.Join(cwd, ".forge"), 0o755)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cwd, ".forge", "plugins.json"), data, 0o644)
}

// MCPConfigPath returns the path to .mcp.json inside a plugin, if it exists.
func (p Plugin) MCPConfigPath() string {
	path := filepath.Join(p.Path, ".mcp.json")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

// HooksPath returns the path to hooks.json inside a plugin, if it exists.
func (p Plugin) HooksPath() string {
	for _, candidate := range []string{
		filepath.Join(p.Path, "hooks", "hooks.json"),
		filepath.Join(p.Path, "hooks.json"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// SkillsDir returns the path to the skills/ subdirectory of a plugin if one
// exists, so the skills manager can include it in its search dirs.
func (p Plugin) SkillsDir() string {
	path := filepath.Join(p.Path, "skills")
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	return ""
}

// LSPConfigPath returns the path to a plugin-shipped .lsp.json. Reading and
// merging is the LSP runtime's responsibility (item 6 of the roadmap).
func (p Plugin) LSPConfigPath() string {
	path := filepath.Join(p.Path, ".lsp.json")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

// SettingsPath returns the path to a plugin-shipped settings.json (claude-style
// permissions/env block). Reading and merging is intentionally NOT done here
// -- the app loader decides which keys are safe to apply.
func (p Plugin) SettingsPath() string {
	path := filepath.Join(p.Path, "settings.json")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

// OutputStylesDir returns the path to the output-styles/ subdirectory of a
// plugin if one exists. Forge does not yet have an output-styles runtime, so
// for now it stays in PendingComponents -- the path getter exists so future
// loaders don't need to re-grow this stat boilerplate.
func (p Plugin) OutputStylesDir() string {
	path := filepath.Join(p.Path, "output-styles")
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	return ""
}

func (p Plugin) Components() []string {
	var out []string
	for _, candidate := range []string{"skills", "commands", "agents", "hooks", ".mcp.json", "bin", "output-styles", ".lsp.json", "settings.json"} {
		if _, err := os.Stat(filepath.Join(p.Path, candidate)); err == nil {
			out = append(out, candidate)
		}
	}
	return out
}

func (p Plugin) SupportedComponents() []string {
	var out []string
	// "skills" moved here once skillSearchDirs honors plugin paths.
	for _, candidate := range []string{"commands", "agents", "hooks", ".mcp.json", "skills"} {
		if _, err := os.Stat(filepath.Join(p.Path, candidate)); err == nil {
			out = append(out, candidate)
		}
	}
	return out
}

func (p Plugin) PendingComponents() []string {
	var out []string
	// "bin": Forge does not run plugin binaries.
	// "output-styles": no output-styles runtime yet.
	// ".lsp.json": LSP config merge is item 6 of the roadmap.
	// "settings.json": no merger that is safe enough to enable by default yet.
	for _, candidate := range []string{"bin", "output-styles", ".lsp.json", "settings.json"} {
		if _, err := os.Stat(filepath.Join(p.Path, candidate)); err == nil {
			out = append(out, candidate)
		}
	}
	return out
}

func (p Plugin) CompatibilityStatus() string {
	pending := p.PendingComponents()
	supported := p.SupportedComponents()
	if len(pending) > 0 {
		return "partial"
	}
	if len(supported) > 0 {
		return "ready"
	}
	return "discovered"
}

func looksLikePlugin(path string) bool {
	candidates := []string{"skills", "commands", "agents", "hooks", "output-styles", "bin", ".mcp.json", ".lsp.json", "settings.json", ".forge/plugin.toml"}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(path, candidate)); err == nil {
			return true
		}
	}
	return strings.HasSuffix(path, ".claude-plugin")
}
