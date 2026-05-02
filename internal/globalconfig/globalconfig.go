// Package globalconfig is the user-level (cross-workspace) defaults layer.
// Whatever the user sets in ~/.forge/global.toml becomes the default for
// every workspace; an individual workspace's .forge/config.toml still wins
// for any field it sets.
//
// Path history: forge originally lived under ~/.codex/forge/ to coexist
// with the Codex CLI. Since the product is forge, the home moved to
// ~/.forge/. Migrate() copies the legacy paths over on first launch so
// existing users do not lose state. Skills are intentionally still scanned
// from ~/.codex/skills/ as a secondary read source — the skills CLI
// installs there when invoked with --agent codex, and we want the user to
// keep any skills they already share with the Codex CLI ecosystem.
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

// GlobalConfig is the on-disk shape of ~/.forge/global.toml.
type GlobalConfig struct {
	Theme           *string                  `toml:"theme,omitempty"`
	ApprovalProfile *string                  `toml:"approval_profile,omitempty"`
	Providers       map[string]ProviderEntry `toml:"providers,omitempty"`
	Models          map[string]string        `toml:"models,omitempty"`
	DetectedByRole  map[string]DetectedModel `toml:"detected_by_role,omitempty"`
	ModelLoading    *ModelLoadingDefaults    `toml:"model_loading,omitempty"`
	Yarn            *YarnDefaults            `toml:"yarn,omitempty"`
	Skills          *SkillsDefaults          `toml:"skills,omitempty"`
	Plugins         *PluginsDefaults         `toml:"plugins,omitempty"`
	Runtime         *RuntimeDefaults         `toml:"runtime,omitempty"`
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

// RuntimeDefaults captures cross-workspace agent-runtime defaults: timeouts,
// step caps, and the read-only / no-progress safety guards. Setting these
// globally lets a slow-local-model setup (or any other tuning) apply to
// every workspace the user opens, without editing each .forge/config.toml.
//
// Pointers everywhere so "field unset" is distinguishable from "field
// deliberately set to zero" — important for timeouts where 0 means "no
// deadline" rather than "use default".
type RuntimeDefaults struct {
	RequestTimeoutSeconds     *int  `toml:"request_timeout_seconds,omitempty"`
	RequestIdleTimeoutSeconds *int  `toml:"request_idle_timeout_seconds,omitempty"`
	SubagentTimeoutSeconds    *int  `toml:"subagent_timeout_seconds,omitempty"`
	TaskTimeoutSeconds        *int  `toml:"task_timeout_seconds,omitempty"`
	MaxSteps                  *int  `toml:"max_steps,omitempty"`
	MaxStepsBuild             *int  `toml:"max_steps_build,omitempty"`
	MaxNoProgressSteps        *int  `toml:"max_no_progress_steps,omitempty"`
	MaxEmptyResponses         *int  `toml:"max_empty_responses,omitempty"`
	MaxSameToolFailures       *int  `toml:"max_same_tool_failures,omitempty"`
	MaxConsecutiveReadOnly    *int  `toml:"max_consecutive_read_only,omitempty"`
	MaxPlannerSummarySteps    *int  `toml:"max_planner_summary_steps,omitempty"`
	MaxBuilderReadLoops       *int  `toml:"max_builder_read_loops,omitempty"`
	RetryOnProviderTimeout    *bool `toml:"retry_on_provider_timeout,omitempty"`
	InlineBuilder             *bool `toml:"inline_builder,omitempty"`
}

// HomeDir returns the user-level forge home (~/.forge by default). All
// forge-owned state — global config, hub state, internal cache — lives
// here. FORGE_GLOBAL_HOME overrides for tests.
func HomeDir() string {
	if env := os.Getenv("FORGE_GLOBAL_HOME"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Last-resort fallback: relative to the cwd. Better than panicking
		// on a system without a home dir.
		return ".forge_global"
	}
	return filepath.Join(home, ".forge")
}

// Path returns the absolute path of the global config file.
func Path() string {
	return filepath.Join(HomeDir(), "global.toml")
}

// CacheDir returns the global skills cache directory.
func CacheDir() string {
	return filepath.Join(HomeDir(), "cache", "skills")
}

// SkillsInstallDir returns where Hub-triggered skill installs land. Note
// that skills installed via the external `npx skills` CLI with --agent
// codex still go to ~/.codex/skills/ — that's the CLI's choice, and the
// Manager's searchDirs() reads from both locations so either works.
func SkillsInstallDir() string {
	return filepath.Join(HomeDir(), "skills")
}

// LegacyHomeDir returns the pre-migration ~/.codex/forge home. Used by
// Migrate() to detect and copy old state on first launch.
func LegacyHomeDir() string {
	if env := os.Getenv("FORGE_LEGACY_HOME"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// Migrate copies pre-existing files from ~/.codex/forge/ into ~/.forge/
// the first time forge runs after the home directory move. Idempotent:
// only copies when the destination is missing. Quietly succeeds when there
// is nothing to migrate (fresh install, or already migrated).
func Migrate() error {
	legacy := LegacyHomeDir()
	if legacy == "" {
		return nil
	}
	target := HomeDir()
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	moves := []struct{ from, to string }{
		{filepath.Join(legacy, "forge", "global.toml"), filepath.Join(target, "global.toml")},
		// Hub state lived at ~/.codex/memories/forge_hub_state.json; it
		// moves to ~/.forge/hub_state.json (no "forge_" prefix needed —
		// it is already inside the forge home).
		{filepath.Join(legacy, "memories", "forge_hub_state.json"), filepath.Join(target, "hub_state.json")},
	}
	for _, m := range moves {
		if _, err := os.Stat(m.to); err == nil {
			continue // already migrated
		}
		data, err := os.ReadFile(m.from)
		if err != nil {
			continue // legacy file missing — nothing to do
		}
		if err := os.WriteFile(m.to, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// LoadedKeys returns the set of dotted TOML keys explicitly present in the
// user's global config. Mirrors config.WorkspaceKeys so callers (e.g. the
// settings panel) can tell "value matches builtin coincidentally" from
// "user explicitly set this in ~/.codex/forge/global.toml". A missing or
// malformed file yields an empty set, not an error — the file being missing
// is the normal "user has not customized anything" case.
func LoadedKeys() map[string]bool {
	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]bool{}
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return map[string]bool{}
	}
	out := map[string]bool{}
	collectKeys(raw, "", out)
	return out
}

func collectKeys(value any, prefix string, out map[string]bool) {
	m, ok := value.(map[string]any)
	if !ok {
		if prefix != "" {
			out[prefix] = true
		}
		return
	}
	if prefix != "" {
		out[prefix] = true
	}
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		collectKeys(v, key, out)
	}
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
