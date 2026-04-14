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
		} else if !looksLikePlugin(path) {
			continue
		}
		plugins = append(plugins, plugin)
	}
	return plugins, nil
}

type claudeManifest struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
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

func looksLikePlugin(path string) bool {
	candidates := []string{
		"skills",
		"commands",
		"agents",
		"hooks",
		"output-styles",
		"bin",
		".mcp.json",
		".lsp.json",
		"settings.json",
		".forge/plugin.toml",
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(path, candidate)); err == nil {
			return true
		}
	}
	return strings.HasSuffix(path, ".claude-plugin")
}
