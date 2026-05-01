package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PluginSettings is the safe subset of a Claude-style settings.json that
// forge will honor when it lives inside a plugin directory. Anything outside
// these fields is silently ignored: a plugin must not be able to twist
// runtime behavior by shipping arbitrary keys.
type PluginSettings struct {
	// Permissions.Allow lists tool names (or tool patterns like "Bash(go *)")
	// that the user has pre-authorized for any plugin that ships this file.
	// Merged into the project's allow list; Permissions.Deny is honored too
	// for symmetry but rarely useful from a plugin.
	Permissions PluginPermissions `json:"permissions,omitempty"`
	// Env values are applied to commands run via run_command/PowerShell.
	// Plugin-shipped env never overrides values the user already set in
	// .forge/config.toml -- the project file always wins.
	Env map[string]string `json:"env,omitempty"`
}

// PluginPermissions mirrors the Claude `permissions` block to the extent
// forge actually understands today.
type PluginPermissions struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	Ask   []string `json:"ask,omitempty"`
}

// LoadPluginSettings reads <plugin>/settings.json and returns the parsed
// safe-subset. Missing file -> empty PluginSettings, nil error. Malformed
// file -> error so the user notices the typo.
func (p Plugin) LoadSettings() (PluginSettings, error) {
	path := p.SettingsPath()
	if path == "" {
		return PluginSettings{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return PluginSettings{}, fmt.Errorf("read %s: %w", path, err)
	}
	var s PluginSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return PluginSettings{}, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return s, nil
}

// MergedSettings represents the union of every enabled plugin's settings.json
// after passing through the safe-subset filter and ExpandVars on string
// values that reference ${CLAUDE_PLUGIN_ROOT} or ${user_config.KEY}.
type MergedSettings struct {
	AllowTools []string
	DenyTools  []string
	AskTools   []string
	Env        map[string]string
}

// MergePluginSettings walks the given plugins, loads each one's settings.json,
// runs ExpandVars over the string fields, and concatenates the lists. The
// caller decides how to apply the merged values to the runtime.
func MergePluginSettings(ps []Plugin) (MergedSettings, []error) {
	out := MergedSettings{Env: map[string]string{}}
	var errs []error
	for _, p := range ps {
		s, err := p.LoadSettings()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, t := range s.Permissions.Allow {
			out.AllowTools = append(out.AllowTools, ExpandVars(p, t))
		}
		for _, t := range s.Permissions.Deny {
			out.DenyTools = append(out.DenyTools, ExpandVars(p, t))
		}
		for _, t := range s.Permissions.Ask {
			out.AskTools = append(out.AskTools, ExpandVars(p, t))
		}
		for k, v := range s.Env {
			// First-write wins so the order of `ps` matters: callers should
			// pass higher-priority plugins first.
			if _, exists := out.Env[k]; !exists {
				out.Env[k] = ExpandVars(p, v)
			}
		}
	}
	return out, errs
}
