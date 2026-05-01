// Package globalconfig is the user-level (cross-workspace) defaults layer.
// Whatever the user sets in ~/.codex/forge/global.toml becomes the default
// for every workspace; an individual workspace's .forge/config.toml still
// wins for any field it sets.
//
// The package is intentionally small: it only loads/saves the file and
// exposes the raw structure. The merge into `config.Config` lives in
// internal/config so the dependency arrow is `config -> globalconfig`,
// never the other way (which would cycle: app loads config which would
// then need to know about a TUI-adjacent overlay).
//
// Pointers are used everywhere a "field unset" must be distinguishable
// from "field deliberately set to zero". Without that, a workspace toml
// that omits `theme = ""` would inherit the global theme even when the
// user explicitly wants no theme override.
package globalconfig

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// GlobalConfig is the on-disk shape of ~/.codex/forge/global.toml.
type GlobalConfig struct {
	Theme          *string                  `toml:"theme,omitempty"`
	Providers      map[string]ProviderEntry `toml:"providers,omitempty"`
	Models         map[string]string        `toml:"models,omitempty"`
	DetectedByRole map[string]DetectedModel `toml:"detected_by_role,omitempty"`
	ModelLoading   *ModelLoadingDefaults    `toml:"model_loading,omitempty"`
	Yarn           *YarnDefaults            `toml:"yarn,omitempty"`
	Skills         *SkillsDefaults          `toml:"skills,omitempty"`
	Plugins        *PluginsDefaults         `toml:"plugins,omitempty"`
}

// ProviderEntry mirrors config.ProviderConfig but every field is a pointer
// so we can tell "user didn't set this" from "user set it to empty".
type ProviderEntry struct {
	BaseURL       *string `toml:"base_url,omitempty"`
	APIKey        *string `toml:"api_key,omitempty"`
	APIKeyEnv     *string `toml:"api_key_env,omitempty"`
	DefaultModel  *string `toml:"default_model,omitempty"`
	SupportsTools *bool   `toml:"supports_tools,omitempty"`
}

// YarnDefaults captures the YARN context defaults the user wants every
// workspace to inherit.
type YarnDefaults struct {
	Profile                *string `toml:"profile,omitempty"`
	BudgetTokens           *int    `toml:"budget_tokens,omitempty"`
	ModelContextTokens     *int    `toml:"model_context_tokens,omitempty"`
	ReserveOutputTokens    *int    `toml:"reserve_output_tokens,omitempty"`
	MaxNodes               *int    `toml:"max_nodes,omitempty"`
	MaxFileBytes           *int    `toml:"max_file_bytes,omitempty"`
	HistoryEvents          *int    `toml:"history_events,omitempty"`
	Pins                   *string `toml:"pins,omitempty"`
	Mentions               *string `toml:"mentions,omitempty"`
	CompactEvents          *int    `toml:"compact_events,omitempty"`
	CompactTranscriptChars *int    `toml:"compact_transcript_chars,omitempty"`
	RenderMode             *string `toml:"render_mode,omitempty"`
	RenderHeadLine         *int    `toml:"render_head_lines,omitempty"`
}

type ModelLoadingDefaults struct {
	Enabled       *bool   `toml:"enabled,omitempty"`
	Strategy      *string `toml:"strategy,omitempty"`
	ParallelSlots *int    `toml:"parallel_slots,omitempty"`
}

type DetectedModel struct {
	ModelID             string    `toml:"model_id"`
	LoadedContextLength int       `toml:"loaded_context_length"`
	MaxContextLength    int       `toml:"max_context_length"`
	ProbedAt            time.Time `toml:"probed_at"`
}

// SkillsDefaults captures cross-workspace skills config: install scope,
// repository list, the cache dir to share across workspaces.
type SkillsDefaults struct {
	CLI          *string  `toml:"cli,omitempty"`
	DirectoryURL *string  `toml:"directory_url,omitempty"`
	Repositories []string `toml:"repositories,omitempty"`
	Agent        *string  `toml:"agent,omitempty"`
	InstallScope *string  `toml:"install_scope,omitempty"`
	CacheDir     *string  `toml:"cache_dir,omitempty"`
}

// PluginsDefaults captures cross-workspace plugin defaults.
type PluginsDefaults struct {
	Enabled          *bool    `toml:"enabled,omitempty"`
	ClaudeCompatible *bool    `toml:"claude_compatible,omitempty"`
	EnabledByDefault []string `toml:"enabled_by_default,omitempty"`
}

// Path returns the absolute path of the global config file. FORGE_GLOBAL_HOME
// overrides the default location for testing.
func Path() string {
	if env := os.Getenv("FORGE_GLOBAL_HOME"); env != "" {
		return filepath.Join(env, "global.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Last-resort fallback: relative to the cwd. Better than panicking
		// on a system without a home dir.
		return filepath.Join(".forge_global.toml")
	}
	return filepath.Join(home, ".codex", "forge", "global.toml")
}

// CacheDir returns the global skills cache directory. Same env override.
func CacheDir() string {
	if env := os.Getenv("FORGE_GLOBAL_HOME"); env != "" {
		return filepath.Join(env, "cache", "skills")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".forge_global", "cache", "skills")
	}
	return filepath.Join(home, ".codex", "cache", "skills")
}

// SkillsInstallDir returns where globally-installed skills live (target of
// `npx skills add ...` when the Hub triggers it).
func SkillsInstallDir() string {
	if env := os.Getenv("FORGE_GLOBAL_HOME"); env != "" {
		return filepath.Join(env, "skills")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".forge_global", "skills")
	}
	return filepath.Join(home, ".codex", "skills")
}

// Load reads the global config file. A missing file returns an empty
// GlobalConfig with no error -- the user has not customized anything yet.
func Load() (GlobalConfig, error) {
	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return GlobalConfig{}, nil
		}
		return GlobalConfig{}, err
	}
	var g GlobalConfig
	if err := toml.Unmarshal(data, &g); err != nil {
		return GlobalConfig{}, err
	}
	return g, nil
}

// Save writes the global config to disk. Creates the parent directory if
// missing.
func Save(g GlobalConfig) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := toml.Marshal(g)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// SetTheme is a convenience helper for callers that only want to update one
// scalar. It loads, mutates, saves -- if any step fails the error is
// returned without partial state on disk.
func SetTheme(theme string) error {
	g, err := Load()
	if err != nil {
		return err
	}
	g.Theme = &theme
	return Save(g)
}
