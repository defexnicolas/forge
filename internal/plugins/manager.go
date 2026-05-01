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
// plugin if one exists. Forge surfaces them through ListOutputStyles so the
// user can see what is available, but does not auto-apply them: applying an
// output style means injecting prose into the system prompt, which is the
// user's call.
func (p Plugin) OutputStylesDir() string {
	path := filepath.Join(p.Path, "output-styles")
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	return ""
}

// OutputStyle describes one style file shipped by a plugin.
type OutputStyle struct {
	Plugin string
	Name   string
	Path   string
}

// ListOutputStyles enumerates *.md and *.json files inside the plugin's
// output-styles/ dir. Best-effort: unreadable directories are silently
// skipped (the .Components() call already told the user the dir exists).
func (p Plugin) ListOutputStyles() []OutputStyle {
	dir := p.OutputStylesDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []OutputStyle
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".md" && ext != ".json" {
			continue
		}
		out = append(out, OutputStyle{
			Plugin: p.Name,
			Name:   e.Name()[:len(e.Name())-len(ext)],
			Path:   filepath.Join(dir, e.Name()),
		})
	}
	return out
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
	// "skills"        loaded via Options.PluginSkillDirs (item 4 of roadmap).
	// ".lsp.json"     loaded via lsp.LoadConfig + lsp.NewRouter (item 6).
	// "settings.json" loaded via PluginSettings.LoadSettings (safe subset:
	//                 permissions.allow/deny/ask + env). Anything else in
	//                 the file is silently ignored.
	// "output-styles" surfaced via ListOutputStyles -- forge does not
	//                 auto-apply styles but the user can inspect them.
	for _, candidate := range []string{"commands", "agents", "hooks", ".mcp.json", "skills", ".lsp.json", "settings.json", "output-styles"} {
		if _, err := os.Stat(filepath.Join(p.Path, candidate)); err == nil {
			out = append(out, candidate)
		}
	}
	return out
}

// IgnoredComponents are recognized component names that forge will never
// auto-load, by policy. They are listed separately so the user understands
// the plugin works and the missing component is intentional.
func (p Plugin) IgnoredComponents() []string {
	var out []string
	// "bin": running arbitrary binaries from a discovered plugin would let
	// any plugin author execute code on the user's machine the first time
	// the plugin is enabled. Disabled by design; users can install the
	// binary themselves and reference it from .forge/config.toml.
	for _, candidate := range []string{"bin"} {
		if _, err := os.Stat(filepath.Join(p.Path, candidate)); err == nil {
			out = append(out, candidate)
		}
	}
	return out
}

func (p Plugin) PendingComponents() []string {
	// All previously-pending components now have loaders. Kept around as an
	// extension point for any future component type that lands in discovery
	// before its loader.
	return nil
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
