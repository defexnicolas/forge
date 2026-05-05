package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forge/internal/gitops"
	"forge/internal/globalconfig"
	"forge/internal/permissions"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	DefaultAgent       string `toml:"default_agent"`
	ApprovalProfile    string `toml:"approval_profile"`
	PermissionsProfile string `toml:"permissions_profile"`
	// OutputStyle is the path to a plugin-shipped output-style markdown
	// file whose body is appended to the agent's system prompt. Set via
	// HUB > Settings > Output Style after the user picks one of the styles
	// the discovered plugins exposed via output-styles/ directories.
	OutputStyle  string             `toml:"output_style"`
	Providers    Providers          `toml:"providers"`
	Context      ContextConfig      `toml:"context"`
	Runtime      RuntimeConfig      `toml:"runtime"`
	Claw         ClawConfig         `toml:"claw"`
	Skills       SkillsConfig       `toml:"skills"`
	Plugins      PluginsConfig      `toml:"plugins"`
	WebSearch    WebSearchConfig    `toml:"web_search"`
	Git          GitConfig          `toml:"git"`
	Models       map[string]string  `toml:"models"`
	ModelLoading ModelLoadingConfig `toml:"model_loading"`
	Build        BuildConfig        `toml:"build"`
	Explore      ExploreConfig      `toml:"explore"`
	Plan         PlanConfig         `toml:"plan"`
	TUI          TUIConfig          `toml:"tui"`
	Update       UpdateConfig       `toml:"update"`
}

// UpdateConfig governs the in-app update checker. The check only runs when
// the binary was built with -ldflags injecting a SourceRepo path; otherwise
// the updater is silently disabled regardless of these flags.
type UpdateConfig struct {
	// CheckOnStartup runs `git fetch` once shortly after the TUI starts.
	// Default true. Set to false on slow networks or to keep the Hub
	// banner from appearing.
	CheckOnStartup bool `toml:"check_on_startup"`
	// CheckIntervalMinutes re-runs the check periodically while forge is
	// running. 0 disables periodic checks (only the startup one runs).
	CheckIntervalMinutes int `toml:"check_interval_minutes"`
}

// WebSearchConfig governs the web_search tool's backend selection. Provider
// names are matched case-insensitively against the registered backends in
// internal/tools/websearch (currently "duckduckgo" and "ollama"). Empty
// provider falls back to DuckDuckGo so the tool stays functional with no
// configuration.
type WebSearchConfig struct {
	Provider  string `toml:"provider"`
	APIKey    string `toml:"api_key"`
	APIKeyEnv string `toml:"api_key_env"`
	BaseURL   string `toml:"base_url"`
}

// TUIConfig holds terminal UI preferences. StreamFlushMs governs how
// frequently streamed tokens are materialized into the viewport — the
// default (33ms ≈ 30fps) is tuned against Ollama at 150+ tk/s so the event
// loop doesn't saturate. Terminals with hardware-accelerated rendering
// (iTerm2, WezTerm, Alacritty) can safely drop to 16ms ≈ 60fps.
type TUIConfig struct {
	StreamFlushMs int `toml:"stream_flush_ms"`
}

type BuildConfig struct {
	Subagents BuildSubagentsConfig `toml:"subagents"`
}

// ExploreConfig parameterizes explore-mode behavior. Currently only governs
// the optional preflight fan-out: off by default so simple reads remain
// zero-latency; flip Subagents.Enabled=true to dispatch an explorer subagent
// before the main response.
type ExploreConfig struct {
	Subagents BuildSubagentsConfig `toml:"subagents"`
}

// PlanConfig parameterizes plan-mode behavior. Mirrors ExploreConfig — the
// preflight is optional and off by default to keep the initial ask_user
// round fast.
type PlanConfig struct {
	Subagents BuildSubagentsConfig `toml:"subagents"`
}

type BuildSubagentsConfig struct {
	Enabled     bool     `toml:"enabled"`
	Concurrency int      `toml:"concurrency"`
	Roles       []string `toml:"roles"`
}

type Providers struct {
	Default          ProviderRef    `toml:"default"`
	OpenAICompatible ProviderConfig `toml:"openai_compatible"`
	LMStudio         ProviderConfig `toml:"lmstudio"`
}

type ProviderRef struct {
	Name string `toml:"name"`
}

type ProviderConfig struct {
	Type          string `toml:"type"`
	BaseURL       string `toml:"base_url"`
	APIKey        string `toml:"api_key"`
	APIKeyEnv     string `toml:"api_key_env"`
	DefaultModel  string `toml:"default_model"`
	SupportsTools bool   `toml:"supports_tools"`
}

type ContextConfig struct {
	Engine              string                     `toml:"engine"`
	BudgetTokens        int                        `toml:"budget_tokens"`
	AutoCompact         bool                       `toml:"auto_compact"`
	ModelContextTokens  int                        `toml:"model_context_tokens"`
	ReserveOutputTokens int                        `toml:"reserve_output_tokens"`
	Yarn                YarnConfig                 `toml:"yarn"`
	Task                TaskContextConfig          `toml:"task"`
	Detected            *DetectedContext           `toml:"detected,omitempty"`
	DetectedByRole      map[string]DetectedContext `toml:"detected_by_role,omitempty"`
}

type RuntimeConfig struct {
	// RequestTimeoutSeconds is the wall-clock deadline applied to a single
	// LLM request (Stream or Chat). 0 disables the deadline — useful for
	// slow local models where prompt processing alone can exceed minutes.
	// When disabled, RequestIdleTimeoutSeconds is the only safety net.
	RequestTimeoutSeconds int `toml:"request_timeout_seconds"`
	// RequestIdleTimeoutSeconds cancels a streaming request if no SSE chunk
	// is received within this window. The timer arms on the first chunk, so
	// long prompt-processing pauses before any token is emitted do not
	// trigger it. 0 disables idle detection entirely.
	RequestIdleTimeoutSeconds int `toml:"request_idle_timeout_seconds"`
	SubagentTimeoutSeconds    int `toml:"subagent_timeout_seconds"`
	TaskTimeoutSeconds        int `toml:"task_timeout_seconds"`
	// MaxSteps is the per-turn cap on (LLM call + tool result) iterations.
	// 0 falls back to the built-in default (40). MaxStepsBuild, when > 0,
	// overrides this in build mode where multi-task implementations
	// legitimately need more steps than a plan-mode interview.
	MaxSteps               int  `toml:"max_steps"`
	MaxStepsBuild          int  `toml:"max_steps_build"`
	MaxNoProgressSteps     int  `toml:"max_no_progress_steps"`
	MaxEmptyResponses      int  `toml:"max_empty_responses"`
	MaxSameToolFailures    int  `toml:"max_same_tool_failures"`
	MaxConsecutiveReadOnly int  `toml:"max_consecutive_read_only"`
	MaxPlannerSummarySteps int  `toml:"max_planner_summary_steps"`
	MaxBuilderReadLoops    int  `toml:"max_builder_read_loops"`
	RetryOnProviderTimeout bool `toml:"retry_on_provider_timeout"`
	InlineBuilder          bool `toml:"inline_builder"`
}

type ClawConfig struct {
	Enabled                  bool   `toml:"enabled"`
	Autostart                bool   `toml:"autostart"`
	HeartbeatIntervalSeconds int    `toml:"heartbeat_interval_seconds"`
	DreamIntervalMinutes     int    `toml:"dream_interval_minutes"`
	AutonomyPolicy           string `toml:"autonomy_policy"`
	DefaultChannel           string `toml:"default_channel"`
	PersonaName              string `toml:"persona_name"`
	PersonaTone              string `toml:"persona_tone"`
	IdentitySeed             string `toml:"identity_seed"`
	// ToolsEnabled gates whether Claw chat advertises web_search,
	// web_fetch, whatsapp_send, claw_save_contact, and other tools to
	// the model. Defaults to true via Defaults() — without tools the
	// assistant is conversation-only and can't save contacts, send
	// WhatsApp, or research on the user's behalf, which is most of
	// the value. Set claw.tools_enabled = false explicitly in
	// global.toml if you're on a metered provider and want to gate
	// over-eager web_search calls.
	ToolsEnabled bool `toml:"tools_enabled"`
}

type GitConfig struct {
	AutoInit               bool   `toml:"auto_init"`
	CreateBaselineCommit   bool   `toml:"create_baseline_commit"`
	RequireCleanOrSnapshot bool   `toml:"require_clean_or_snapshot"`
	AutoStageMutations     bool   `toml:"auto_stage_mutations"`
	AutoCommit             bool   `toml:"auto_commit"`
	BaselineCommitMessage  string `toml:"baseline_commit_message"`
	SnapshotCommitMessage  string `toml:"snapshot_commit_message"`
}

type TaskContextConfig struct {
	BudgetTokens  int `toml:"budget_tokens"`
	MaxNodes      int `toml:"max_nodes"`
	MaxFileBytes  int `toml:"max_file_bytes"`
	HistoryEvents int `toml:"history_events"`
}

// DetectedContext captures the actual context window reported by the provider
// for the currently-loaded model (e.g. LM Studio's loaded_context_length after
// YaRN extension). When present it overrides the static profile caps via
// EffectiveBudgets.
type DetectedContext struct {
	ModelID             string    `toml:"model_id"`
	LoadedContextLength int       `toml:"loaded_context_length"`
	MaxContextLength    int       `toml:"max_context_length"`
	ProbedAt            time.Time `toml:"probed_at"`
}

type YarnConfig struct {
	Profile                string `toml:"profile"`
	MaxNodes               int    `toml:"max_nodes"`
	MaxFileBytes           int    `toml:"max_file_bytes"`
	HistoryEvents          int    `toml:"history_events"`
	Pins                   string `toml:"pins"`
	Mentions               string `toml:"mentions"`
	CompactEvents          int    `toml:"compact_events"`
	CompactTranscriptChars int    `toml:"compact_transcript_chars"`
	// RenderMode controls how scored yarn nodes are materialized into the
	// prompt. "full" emits Summary + entire Content (legacy behavior).
	// "head" (default) emits Summary + the first RenderHeadLines lines of
	// Content. "summary" emits only the Summary. Smaller rendering means
	// the model re-reads detail via read_file when needed, saving tokens on
	// every turn where the detail was not actually required.
	RenderMode      string `toml:"render_mode"`
	RenderHeadLines int    `toml:"render_head_lines"`
}

type YarnProfile struct {
	Name                   string
	LMContextMin           int
	LMContextMax           int
	BudgetTokens           int
	ReserveOutputTokens    int
	MaxNodes               int
	MaxFileBytes           int
	HistoryEvents          int
	CompactEvents          int
	CompactTranscriptChars int
}

type SkillsConfig struct {
	CLI          string   `toml:"cli"`
	DirectoryURL string   `toml:"directory_url"`
	Repositories []string `toml:"repositories"`
	Agent        string   `toml:"agent"`
	InstallScope string   `toml:"install_scope"`
	Copy         bool     `toml:"copy"`
	Installer    string   `toml:"installer"` // legacy
}

// PluginsConfig governs plugin discovery + activation. Forge always
// understands the Claude Code plugin layout (.claude-plugin/plugin.json
// + commands/agents/hooks/skills/output-styles/.lsp.json/.mcp.json/
// settings.json). The historical claude_compatible toggle was removed —
// it never gated any code path and was confusing in the settings UI.
type PluginsConfig struct {
	Enabled      bool     `toml:"enabled"`
	Marketplaces []string `toml:"marketplaces"`
}

type ModelLoadingConfig struct {
	Enabled  bool   `toml:"enabled"`
	Strategy string `toml:"strategy"`
	// ParallelSlots is the number of concurrent generation slots to request
	// when loading a model. 0 means "leave the backend default"; set to 2+
	// on LM Studio to serve parallel tool calls / /btw / subagents without
	// queueing. Applied per model-load call.
	ParallelSlots int `toml:"parallel_slots"`
}

func Load(cwd string) (Config, error) {
	cfg := Defaults()
	path := filepath.Join(cwd, ".forge", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	Normalize(&cfg)
	return cfg, nil
}

// LoadWithGlobal reads the workspace config and overlays the user's global
// defaults from globalconfig.Load(). Resolution order, low to high:
//
//  1. Defaults() (built-in)
//  2. ~/.codex/forge/global.toml -- only fills slots the workspace toml does
//     not mention at all
//  3. Workspace .forge/config.toml -- whatever the user wrote, even if it
//     equals a built-in default
//
// "Did the workspace set this key?" is answered by re-reading the workspace
// toml as a generic map and checking key presence, not by comparing against
// Defaults() (which would mistakenly treat "I want the default value" as
// "I didn't set this").
//
// A missing global file is not an error; only a malformed one is.
func LoadWithGlobal(cwd string) (Config, error) {
	cfg, err := Load(cwd)
	if err != nil {
		return cfg, err
	}
	keys := loadWorkspaceKeys(cwd)
	migrateLegacyScaffoldRuntime(&cfg, keys)
	g, gerr := globalconfig.Load()
	if gerr != nil {
		// Surface the error but still hand back the workspace-only config so
		// the user is not locked out by a typo in the global file.
		return cfg, gerr
	}
	applyGlobalDefaults(&cfg, g, keys)
	return cfg, nil
}

// ApplyGlobalConfig overlays a GlobalConfig onto cfg exactly like
// LoadWithGlobal would do for a workspace that has not written any keys yet.
// Used by the Hub to edit a global-defaults-backed view of Config.
func ApplyGlobalConfig(cfg *Config, g globalconfig.GlobalConfig) {
	if cfg == nil {
		return
	}
	applyGlobalDefaults(cfg, g, map[string]bool{})
	Normalize(cfg)
}

// loadWorkspaceKeys returns the set of dotted TOML keys actually present in
// the workspace's .forge/config.toml. Used by LoadWithGlobal so we can tell
// "user explicitly wrote this" from "user didn't mention this section". A
// missing or malformed file yields an empty set, which makes every global
// default applicable.
func loadWorkspaceKeys(cwd string) map[string]bool {
	path := filepath.Join(cwd, ".forge", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]bool{}
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return map[string]bool{}
	}
	out := map[string]bool{}
	collectTOMLKeys("", raw, out)
	return out
}

func collectTOMLKeys(prefix string, m map[string]any, out map[string]bool) {
	for k, v := range m {
		full := k
		if prefix != "" {
			full = prefix + "." + k
		}
		out[full] = true
		if sub, ok := v.(map[string]any); ok {
			collectTOMLKeys(full, sub, out)
		}
	}
}

// migrateLegacyScaffoldRuntime erases the fingerprint of the pre-bump
// init.go scaffold from a freshly loaded workspace config so global
// defaults can flow through.
//
// Background: an older scaffold wrote max_consecutive_read_only = 6 and
// max_builder_read_loops = 4 into every new workspace. After Defaults()
// were bumped to 10 and 12, those scaffolded values stopped matching the
// built-in defaults — so applyRuntimeDefaults' "value still equals default"
// escape clause stopped firing for them, and the global was permanently
// shadowed. The other 8 keys the old scaffold wrote already match the
// current defaults, so they don't need migrating; only the two divergent
// ones do.
//
// We require BOTH divergent values to match the old scaffold simultaneously
// (6 AND 4) before treating them as leftover scaffold. The conjunction
// guards against a user who legitimately wants one of those numbers — a
// dual-coincidence is overwhelmingly likely to be a stale scaffold, since
// 4 is below the runtime floor of 8 and meaningless in isolation anyway.
//
// Mutates `keys` so applyRuntimeDefaults sees the keys as unset; resets
// the in-memory values to Defaults() so behaviour is correct even when
// the global doesn't define a runtime block. The on-disk file is left
// untouched — the next PersistWorkspaceConfig() call will naturally drop
// the now-redundant keys.
func migrateLegacyScaffoldRuntime(cfg *Config, keys map[string]bool) {
	const legacyReadOnly = 6
	const legacyBuilderLoops = 4

	if !keys["runtime.max_consecutive_read_only"] || !keys["runtime.max_builder_read_loops"] {
		return
	}
	if cfg.Runtime.MaxConsecutiveReadOnly != legacyReadOnly {
		return
	}
	if cfg.Runtime.MaxBuilderReadLoops != legacyBuilderLoops {
		return
	}
	defaults := Defaults().Runtime
	cfg.Runtime.MaxConsecutiveReadOnly = defaults.MaxConsecutiveReadOnly
	cfg.Runtime.MaxBuilderReadLoops = defaults.MaxBuilderReadLoops
	delete(keys, "runtime.max_consecutive_read_only")
	delete(keys, "runtime.max_builder_read_loops")
}

// applyGlobalDefaults overlays a globalconfig.GlobalConfig onto an in-place
// Config. Each pointer field in the global is applied only when its matching
// dotted TOML key is absent from `keys` (i.e. the workspace did not write
// it), OR when the workspace value still equals the built-in default — the
// "scaffolded escape clause", since init.go materializes every default into
// .forge/config.toml and we don't want those untouched scaffold values to
// shadow the Hub's settings forever.
func applyGlobalDefaults(cfg *Config, g globalconfig.GlobalConfig, keys map[string]bool) {
	defaults := Defaults()
	if g.Models != nil {
		if cfg.Models == nil {
			cfg.Models = map[string]string{}
		}
		for role, gModel := range g.Models {
			if gModel == "" {
				continue
			}
			defModel := defaults.Models[role]
			if !keys["models."+role] || cfg.Models[role] == defModel {
				cfg.Models[role] = gModel
			}
		}
	}
	if len(g.DetectedByRole) > 0 {
		if cfg.Context.DetectedByRole == nil {
			cfg.Context.DetectedByRole = map[string]DetectedContext{}
		}
		for role, detected := range g.DetectedByRole {
			key := "context.detected_by_role." + role
			if keys[key] || detected.LoadedContextLength <= 0 {
				continue
			}
			cfg.Context.DetectedByRole[role] = DetectedContext{
				ModelID:             detected.ModelID,
				LoadedContextLength: detected.LoadedContextLength,
				MaxContextLength:    detected.MaxContextLength,
				ProbedAt:            detected.ProbedAt,
			}
			if role == "chat" && !keys["context.detected"] {
				copyDetected := cfg.Context.DetectedByRole[role]
				cfg.Context.Detected = &copyDetected
			}
		}
	}
	if g.ModelLoading != nil {
		// Fresh scaffolded workspaces currently materialize the built-in
		// model_loading defaults into .forge/config.toml. Treat those untouched
		// built-in values as inheritable so Hub global model-multi settings can
		// still flow into newly opened workspaces.
		if g.ModelLoading.Enabled != nil && (!keys["model_loading.enabled"] || cfg.ModelLoading.Enabled == defaults.ModelLoading.Enabled) {
			cfg.ModelLoading.Enabled = *g.ModelLoading.Enabled
		}
		if g.ModelLoading.Strategy != nil && (!keys["model_loading.strategy"] || cfg.ModelLoading.Strategy == defaults.ModelLoading.Strategy) {
			cfg.ModelLoading.Strategy = *g.ModelLoading.Strategy
		}
		if g.ModelLoading.ParallelSlots != nil && (!keys["model_loading.parallel_slots"] || cfg.ModelLoading.ParallelSlots == defaults.ModelLoading.ParallelSlots) {
			cfg.ModelLoading.ParallelSlots = *g.ModelLoading.ParallelSlots
		}
	}
	if g.Yarn != nil {
		applyYarnDefaults(&cfg.Context, g.Yarn, keys)
	}
	if g.Runtime != nil {
		applyRuntimeDefaults(&cfg.Runtime, g.Runtime, keys)
	}
	if g.WebSearch != nil {
		applyWebSearchDefaults(&cfg.WebSearch, g.WebSearch, keys)
	}
	if g.Claw != nil {
		applyClawDefaults(&cfg.Claw, g.Claw, keys)
	}
	if g.ApprovalProfile != nil && (!keys["approval_profile"] || cfg.ApprovalProfile == Defaults().ApprovalProfile) {
		cfg.ApprovalProfile = *g.ApprovalProfile
	}
	if g.PermissionsProfile != nil && (!keys["permissions_profile"] || cfg.PermissionsProfile == Defaults().PermissionsProfile) {
		cfg.PermissionsProfile = *g.PermissionsProfile
	}
	if g.OutputStyle != nil && (!keys["output_style"] || cfg.OutputStyle == "") {
		cfg.OutputStyle = *g.OutputStyle
	}
	if g.Skills != nil {
		applySkillsDefaults(&cfg.Skills, g.Skills, keys)
	}
	if g.Plugins != nil {
		applyPluginsDefaults(&cfg.Plugins, g.Plugins, keys)
	}
	if g.Providers != nil {
		applyProviderEntry(&cfg.Providers.OpenAICompatible, g.Providers["openai_compatible"], keys, "providers.openai_compatible", defaults.Providers.OpenAICompatible)
		applyProviderEntry(&cfg.Providers.LMStudio, g.Providers["lmstudio"], keys, "providers.lmstudio", defaults.Providers.LMStudio)
	}
	// Active provider name. globalconfig.DefaultProvider is the only place
	// the "which provider to use" picker can persist globally — there is no
	// providers.default sub-entry on the global side because ProviderEntry
	// has no Name field.
	if g.DefaultProvider != nil && strings.TrimSpace(*g.DefaultProvider) != "" {
		if !keys["providers.default.name"] || cfg.Providers.Default.Name == defaults.Providers.Default.Name {
			cfg.Providers.Default.Name = strings.TrimSpace(*g.DefaultProvider)
		}
	}
}

func applyYarnDefaults(ctx *ContextConfig, g *globalconfig.YarnDefaults, keys map[string]bool) {
	if g.Profile != nil && !keys["context.yarn.profile"] {
		ctx.Yarn.Profile = *g.Profile
	}
	if g.BudgetTokens != nil && !keys["context.budget_tokens"] {
		ctx.BudgetTokens = *g.BudgetTokens
	}
	if g.ModelContextTokens != nil && !keys["context.model_context_tokens"] {
		ctx.ModelContextTokens = *g.ModelContextTokens
	}
	if g.ReserveOutputTokens != nil && !keys["context.reserve_output_tokens"] {
		ctx.ReserveOutputTokens = *g.ReserveOutputTokens
	}
	if g.MaxNodes != nil && !keys["context.yarn.max_nodes"] {
		ctx.Yarn.MaxNodes = *g.MaxNodes
	}
	if g.MaxFileBytes != nil && !keys["context.yarn.max_file_bytes"] {
		ctx.Yarn.MaxFileBytes = *g.MaxFileBytes
	}
	if g.HistoryEvents != nil && !keys["context.yarn.history_events"] {
		ctx.Yarn.HistoryEvents = *g.HistoryEvents
	}
	if g.Pins != nil && !keys["context.yarn.pins"] {
		ctx.Yarn.Pins = *g.Pins
	}
	if g.Mentions != nil && !keys["context.yarn.mentions"] {
		ctx.Yarn.Mentions = *g.Mentions
	}
	if g.CompactEvents != nil && !keys["context.yarn.compact_events"] {
		ctx.Yarn.CompactEvents = *g.CompactEvents
	}
	if g.CompactTranscriptChars != nil && !keys["context.yarn.compact_transcript_chars"] {
		ctx.Yarn.CompactTranscriptChars = *g.CompactTranscriptChars
	}
	if g.RenderMode != nil && !keys["context.yarn.render_mode"] {
		ctx.Yarn.RenderMode = *g.RenderMode
	}
	if g.RenderHeadLine != nil && !keys["context.yarn.render_head_lines"] {
		ctx.Yarn.RenderHeadLines = *g.RenderHeadLine
	}
}

// applyRuntimeDefaults overlays a global runtime block onto the workspace
// runtime config. A workspace value wins UNLESS it still matches the
// built-in default (the fresh-scaffold case where init.go materialised
// every default into .forge/config.toml — without this escape, the global
// would be permanently shadowed by untouched scaffold values).
//
// Runtime fields use 0 / negative as semantically valid ("no deadline",
// "use built-in"), so the apply helpers explicitly use pointer-non-nil as
// the "user set this" signal — never zero-value comparison.
func applyRuntimeDefaults(rt *RuntimeConfig, g *globalconfig.RuntimeDefaults, keys map[string]bool) {
	defaults := Defaults().Runtime
	applyInt := func(name string, target *int, val *int, def int) {
		if val == nil {
			return
		}
		if !keys["runtime."+name] || *target == def {
			*target = *val
		}
	}
	applyBool := func(name string, target *bool, val *bool, def bool) {
		if val == nil {
			return
		}
		if !keys["runtime."+name] || *target == def {
			*target = *val
		}
	}
	applyInt("request_timeout_seconds", &rt.RequestTimeoutSeconds, g.RequestTimeoutSeconds, defaults.RequestTimeoutSeconds)
	applyInt("request_idle_timeout_seconds", &rt.RequestIdleTimeoutSeconds, g.RequestIdleTimeoutSeconds, defaults.RequestIdleTimeoutSeconds)
	applyInt("subagent_timeout_seconds", &rt.SubagentTimeoutSeconds, g.SubagentTimeoutSeconds, defaults.SubagentTimeoutSeconds)
	applyInt("task_timeout_seconds", &rt.TaskTimeoutSeconds, g.TaskTimeoutSeconds, defaults.TaskTimeoutSeconds)
	applyInt("max_steps", &rt.MaxSteps, g.MaxSteps, defaults.MaxSteps)
	applyInt("max_steps_build", &rt.MaxStepsBuild, g.MaxStepsBuild, defaults.MaxStepsBuild)
	applyInt("max_no_progress_steps", &rt.MaxNoProgressSteps, g.MaxNoProgressSteps, defaults.MaxNoProgressSteps)
	applyInt("max_empty_responses", &rt.MaxEmptyResponses, g.MaxEmptyResponses, defaults.MaxEmptyResponses)
	applyInt("max_same_tool_failures", &rt.MaxSameToolFailures, g.MaxSameToolFailures, defaults.MaxSameToolFailures)
	applyInt("max_consecutive_read_only", &rt.MaxConsecutiveReadOnly, g.MaxConsecutiveReadOnly, defaults.MaxConsecutiveReadOnly)
	applyInt("max_planner_summary_steps", &rt.MaxPlannerSummarySteps, g.MaxPlannerSummarySteps, defaults.MaxPlannerSummarySteps)
	applyInt("max_builder_read_loops", &rt.MaxBuilderReadLoops, g.MaxBuilderReadLoops, defaults.MaxBuilderReadLoops)
	applyBool("retry_on_provider_timeout", &rt.RetryOnProviderTimeout, g.RetryOnProviderTimeout, defaults.RetryOnProviderTimeout)
	applyBool("inline_builder", &rt.InlineBuilder, g.InlineBuilder, defaults.InlineBuilder)
}

// applyClawDefaults overlays the user's global claw block onto the
// workspace ClawConfig. Workspace values win unless they still match
// the built-in default — same pattern as applyRuntimeDefaults.
func applyClawDefaults(c *ClawConfig, g *globalconfig.ClawDefaults, keys map[string]bool) {
	defaults := Defaults().Claw
	if g.HeartbeatIntervalSeconds != nil && (!keys["claw.heartbeat_interval_seconds"] || c.HeartbeatIntervalSeconds == defaults.HeartbeatIntervalSeconds) {
		c.HeartbeatIntervalSeconds = *g.HeartbeatIntervalSeconds
	}
	if g.DreamIntervalMinutes != nil && (!keys["claw.dream_interval_minutes"] || c.DreamIntervalMinutes == defaults.DreamIntervalMinutes) {
		c.DreamIntervalMinutes = *g.DreamIntervalMinutes
	}
	if g.PersonaName != nil && (!keys["claw.persona_name"] || c.PersonaName == defaults.PersonaName) {
		c.PersonaName = *g.PersonaName
	}
	if g.PersonaTone != nil && (!keys["claw.persona_tone"] || c.PersonaTone == defaults.PersonaTone) {
		c.PersonaTone = *g.PersonaTone
	}
	if g.AutonomyPolicy != nil && (!keys["claw.autonomy_policy"] || c.AutonomyPolicy == defaults.AutonomyPolicy) {
		c.AutonomyPolicy = *g.AutonomyPolicy
	}
	if g.ToolsEnabled != nil && !keys["claw.tools_enabled"] {
		c.ToolsEnabled = *g.ToolsEnabled
	}
}

// applyWebSearchDefaults overlays the user's global web_search block onto
// the workspace WebSearchConfig. Workspace-set values win unless they
// equal the empty/zero default — Provider in particular often arrives
// empty from a workspace that never thought about search, and we want
// those workspaces to inherit the global pick.
func applyWebSearchDefaults(ws *WebSearchConfig, g *globalconfig.WebSearchDefaults, keys map[string]bool) {
	if g.Provider != nil && (!keys["web_search.provider"] || ws.Provider == "") {
		ws.Provider = *g.Provider
	}
	if g.APIKey != nil && (!keys["web_search.api_key"] || ws.APIKey == "") {
		ws.APIKey = *g.APIKey
	}
	if g.APIKeyEnv != nil && (!keys["web_search.api_key_env"] || ws.APIKeyEnv == "") {
		ws.APIKeyEnv = *g.APIKeyEnv
	}
	if g.BaseURL != nil && (!keys["web_search.base_url"] || ws.BaseURL == "") {
		ws.BaseURL = *g.BaseURL
	}
}

func applySkillsDefaults(s *SkillsConfig, g *globalconfig.SkillsDefaults, keys map[string]bool) {
	if g.CLI != nil && !keys["skills.cli"] {
		s.CLI = *g.CLI
	}
	if g.DirectoryURL != nil && !keys["skills.directory_url"] {
		s.DirectoryURL = *g.DirectoryURL
	}
	if len(g.Repositories) > 0 && !keys["skills.repositories"] {
		s.Repositories = append([]string(nil), g.Repositories...)
	}
	if g.Agent != nil && !keys["skills.agent"] {
		s.Agent = *g.Agent
	}
	if g.InstallScope != nil && !keys["skills.install_scope"] {
		s.InstallScope = *g.InstallScope
	}
	// CacheDir is read directly from globalconfig.CacheDir() at the call
	// site (workspace.go) -- not exposed through SkillsConfig today.
	_ = g.CacheDir
}

func applyPluginsDefaults(p *PluginsConfig, g *globalconfig.PluginsDefaults, keys map[string]bool) {
	if g.Enabled != nil && !keys["plugins.enabled"] {
		p.Enabled = *g.Enabled
	}
	// EnabledByDefault is purely additive: workspace marketplaces stay,
	// plus any unique entries from the global list.
	for _, name := range g.EnabledByDefault {
		if name == "" {
			continue
		}
		seen := false
		for _, existing := range p.Marketplaces {
			if existing == name {
				seen = true
				break
			}
		}
		if !seen {
			p.Marketplaces = append(p.Marketplaces, name)
		}
	}
}

// applyProviderEntry overlays a global provider block onto the workspace
// provider config. Workspace value wins UNLESS it still equals the built-in
// default (the fresh-scaffold case where init.go materialised every default
// into .forge/config.toml — without this escape, untouched scaffold values
// would permanently shadow the Hub's pick).
func applyProviderEntry(p *ProviderConfig, g globalconfig.ProviderEntry, keys map[string]bool, sect string, def ProviderConfig) {
	if g.BaseURL != nil && (!keys[sect+".base_url"] || p.BaseURL == def.BaseURL) {
		p.BaseURL = *g.BaseURL
	}
	if g.APIKey != nil && (!keys[sect+".api_key"] || p.APIKey == def.APIKey) {
		p.APIKey = *g.APIKey
	}
	if g.APIKeyEnv != nil && (!keys[sect+".api_key_env"] || p.APIKeyEnv == def.APIKeyEnv) {
		p.APIKeyEnv = *g.APIKeyEnv
	}
	if g.DefaultModel != nil && (!keys[sect+".default_model"] || p.DefaultModel == def.DefaultModel) {
		p.DefaultModel = *g.DefaultModel
	}
	if g.SupportsTools != nil && (!keys[sect+".supports_tools"] || p.SupportsTools == def.SupportsTools) {
		p.SupportsTools = *g.SupportsTools
	}
}

func Defaults() Config {
	return Config{
		DefaultAgent:       "build",
		ApprovalProfile:    "normal",
		PermissionsProfile: "normal",
		Providers: Providers{
			Default: ProviderRef{Name: "lmstudio"},
			OpenAICompatible: ProviderConfig{
				Type:          "openai-compatible",
				BaseURL:       "https://api.openai.com/v1",
				APIKeyEnv:     "OPENAI_API_KEY",
				DefaultModel:  "gpt-5.4-mini",
				SupportsTools: true,
			},
			LMStudio: ProviderConfig{
				Type:          "openai-compatible",
				BaseURL:       "http://localhost:1234/v1",
				APIKey:        "lm-studio",
				DefaultModel:  "local-model",
				SupportsTools: true,
			},
		},
		Context: ContextConfig{
			Engine:              "yarn",
			BudgetTokens:        8000,
			AutoCompact:         true,
			ModelContextTokens:  16384,
			ReserveOutputTokens: 2000,
			Yarn: YarnConfig{
				Profile:                "9B",
				MaxNodes:               12,
				MaxFileBytes:           14000,
				HistoryEvents:          14,
				Pins:                   "always",
				Mentions:               "always",
				CompactEvents:          100,
				CompactTranscriptChars: 60000,
				RenderMode:             "head",
				RenderHeadLines:        40,
			},
			Task: TaskContextConfig{
				BudgetTokens:  4000,
				MaxNodes:      6,
				MaxFileBytes:  8000,
				HistoryEvents: 4,
			},
		},
		Runtime: RuntimeConfig{
			RequestTimeoutSeconds:     45,
			RequestIdleTimeoutSeconds: 120,
			SubagentTimeoutSeconds:    90,
			TaskTimeoutSeconds:        180,
			MaxSteps:                  40,
			MaxStepsBuild:             80,
			MaxNoProgressSteps:        3,
			MaxEmptyResponses:         2,
			MaxSameToolFailures:       2,
			// Plan-mode interviews legitimately read several files before
			// planning (README, entry point, test, config). Bumped from
			// 6 -> 10 so moderate codebases don't trip the guard before
			// the planner can dispatch plan_write / todo_write.
			MaxConsecutiveReadOnly: 10,
			MaxPlannerSummarySteps: 2,
			// Build-mode reads (read → analyze → edit → verify per task)
			// also need more headroom than the original 8.
			MaxBuilderReadLoops: 12,
		},
		Claw: ClawConfig{
			Enabled:                  false,
			Autostart:                false,
			HeartbeatIntervalSeconds: 30,
			DreamIntervalMinutes:     180,
			AutonomyPolicy:           "supervised",
			DefaultChannel:           "mock",
			PersonaName:              "Claw",
			PersonaTone:              "warm",
			IdentitySeed:             "A resident Forge companion with memory, initiative, and restraint.",
			// Tools default to enabled. The original false-by-default
			// targeted hosted Ollama users worried about web_search
			// firing on every chitchat turn. Local LM Studio (the
			// realistic Claw deployment) has no per-call cost, and
			// without tools the assistant can't save contacts or send
			// WhatsApp on the user's behalf — which is most of the
			// value. Users on metered providers can still set
			// claw.tools_enabled = false explicitly in global.toml.
			ToolsEnabled: true,
		},
		Git: GitConfig{
			AutoInit:               true,
			CreateBaselineCommit:   true,
			RequireCleanOrSnapshot: true,
			AutoStageMutations:     true,
			AutoCommit:             false,
			BaselineCommitMessage:  gitops.DefaultBaselineCommitMessage,
			SnapshotCommitMessage:  gitops.DefaultSnapshotCommitMessage,
		},
		Skills: SkillsConfig{
			CLI:          "npx",
			DirectoryURL: "https://skills.sh/",
			Repositories: []string{"vercel-labs/agent-skills", "vercel-labs/skills"},
			Agent:        "codex",
			InstallScope: "project",
			Copy:         true,
		},
		Plugins: PluginsConfig{
			Enabled: true,
		},
		ModelLoading: ModelLoadingConfig{
			// Enabled=false keeps per-role model routing off so all main
			// modes stay on "chat". Slot application is gated separately by
			// ParallelSlots below and runs regardless of this flag, so the
			// user gets parallel GEN slots on LM Studio even without opting
			// into model-multi.
			Strategy:      "single",
			ParallelSlots: 2,
		},
		TUI: TUIConfig{
			// 33ms ≈ 30fps — conservative default that survives Ollama at
			// 150+ tk/s without saturating the event loop. Override to 16ms
			// on modern terminals for 60fps streaming.
			StreamFlushMs: 33,
		},
		Build: BuildConfig{
			Subagents: BuildSubagentsConfig{
				// Enabled by default: request concurrency is independent from
				// model-loading strategy. With single-model loading, workers
				// reuse the current model and consume LM Studio GEN slots.
				Enabled:     true,
				Concurrency: 2,
				Roles:       []string{"explorer", "reviewer", "debug"},
			},
		},
		// Explore/Plan preflight default OFF. Build fan-out is universally
		// useful before an edit; read-only analysis turns are cheap enough
		// that auto-preflight would add more latency than value. Flip
		// [explore.subagents] enabled=true / [plan.subagents] enabled=true
		// in .forge/config.toml when you want the fan-out.
		Explore: ExploreConfig{
			Subagents: BuildSubagentsConfig{
				Enabled:     false,
				Concurrency: 1,
				Roles:       []string{"explorer"},
			},
		},
		Plan: PlanConfig{
			Subagents: BuildSubagentsConfig{
				Enabled:     false,
				Concurrency: 1,
				Roles:       []string{"explorer"},
			},
		},
		Models: map[string]string{
			"chat":       "local-model",
			"explorer":   "local-model",
			"planner":    "local-model",
			"editor":     "local-model",
			"reviewer":   "local-model",
			"summarizer": "local-model",
		},
		Update: UpdateConfig{
			CheckOnStartup:       true,
			CheckIntervalMinutes: 60,
		},
	}
}

// normalizePermissionsProfile coerces empty / unknown profile names back to
// the default. A typo in `permissions_profile` should not lock the user out
// of run_command — it falls back to "normal" silently and the surrounding
// runtime code logs the chosen policy.
func normalizePermissionsProfile(name, fallback string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fallback
	}
	for _, valid := range permissions.ProfileNames() {
		if strings.EqualFold(trimmed, valid) {
			return valid
		}
	}
	return fallback
}

func Normalize(cfg *Config) {
	defaults := Defaults()
	cfg.PermissionsProfile = normalizePermissionsProfile(cfg.PermissionsProfile, defaults.PermissionsProfile)
	if cfg.Models == nil {
		cfg.Models = map[string]string{}
	}
	for role, model := range defaults.Models {
		if cfg.Models[role] == "" {
			cfg.Models[role] = model
		}
	}
	if cfg.ModelLoading.Strategy == "" {
		cfg.ModelLoading.Strategy = defaults.ModelLoading.Strategy
	}
	if cfg.Context.Engine == "" {
		cfg.Context.Engine = defaults.Context.Engine
	}
	if cfg.Context.BudgetTokens <= 0 {
		cfg.Context.BudgetTokens = defaults.Context.BudgetTokens
	}
	if cfg.Context.ModelContextTokens <= 0 {
		cfg.Context.ModelContextTokens = defaults.Context.ModelContextTokens
	}
	if cfg.Context.ReserveOutputTokens <= 0 {
		cfg.Context.ReserveOutputTokens = defaults.Context.ReserveOutputTokens
	}
	if cfg.Context.Yarn.Profile == "" {
		cfg.Context.Yarn.Profile = defaults.Context.Yarn.Profile
	}
	if cfg.Context.Yarn.MaxNodes <= 0 {
		cfg.Context.Yarn.MaxNodes = defaults.Context.Yarn.MaxNodes
	}
	if cfg.Context.Yarn.MaxFileBytes <= 0 {
		cfg.Context.Yarn.MaxFileBytes = defaults.Context.Yarn.MaxFileBytes
	}
	if cfg.Context.Yarn.HistoryEvents <= 0 {
		cfg.Context.Yarn.HistoryEvents = defaults.Context.Yarn.HistoryEvents
	}
	if cfg.Context.Yarn.Pins == "" {
		cfg.Context.Yarn.Pins = defaults.Context.Yarn.Pins
	}
	if cfg.Context.Yarn.Mentions == "" {
		cfg.Context.Yarn.Mentions = defaults.Context.Yarn.Mentions
	}
	if cfg.Context.Yarn.CompactEvents <= 0 {
		cfg.Context.Yarn.CompactEvents = defaults.Context.Yarn.CompactEvents
	}
	if cfg.Context.Yarn.CompactTranscriptChars <= 0 {
		cfg.Context.Yarn.CompactTranscriptChars = defaults.Context.Yarn.CompactTranscriptChars
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Context.Yarn.RenderMode)) {
	case "summary", "head", "full":
		// valid
	default:
		cfg.Context.Yarn.RenderMode = defaults.Context.Yarn.RenderMode
	}
	if cfg.Context.Yarn.RenderHeadLines <= 0 {
		cfg.Context.Yarn.RenderHeadLines = defaults.Context.Yarn.RenderHeadLines
	}
	if cfg.Context.Task.BudgetTokens <= 0 {
		cfg.Context.Task.BudgetTokens = defaults.Context.Task.BudgetTokens
	}
	if cfg.Context.Task.MaxNodes <= 0 {
		cfg.Context.Task.MaxNodes = defaults.Context.Task.MaxNodes
	}
	if cfg.Context.Task.MaxFileBytes <= 0 {
		cfg.Context.Task.MaxFileBytes = defaults.Context.Task.MaxFileBytes
	}
	if cfg.Context.Task.HistoryEvents <= 0 {
		cfg.Context.Task.HistoryEvents = defaults.Context.Task.HistoryEvents
	}
	// RequestTimeoutSeconds, SubagentTimeoutSeconds, TaskTimeoutSeconds:
	// 0 (or negative) is a valid, intentional setting meaning "no deadline".
	// Do not coerce to defaults — that change of contract is what blocked
	// users from disabling the wall-clock timeout when running slow local
	// models. Defaults() still seeds 45/90/180 for fresh configs; the user
	// must explicitly write 0 to opt out.
	if cfg.Runtime.RequestIdleTimeoutSeconds < 0 {
		cfg.Runtime.RequestIdleTimeoutSeconds = defaults.Runtime.RequestIdleTimeoutSeconds
	}
	if cfg.Runtime.MaxNoProgressSteps <= 0 {
		cfg.Runtime.MaxNoProgressSteps = defaults.Runtime.MaxNoProgressSteps
	}
	if cfg.Runtime.MaxEmptyResponses <= 0 {
		cfg.Runtime.MaxEmptyResponses = defaults.Runtime.MaxEmptyResponses
	}
	if cfg.Runtime.MaxSameToolFailures <= 0 {
		cfg.Runtime.MaxSameToolFailures = defaults.Runtime.MaxSameToolFailures
	}
	// Negative is reserved as the "unlimited" opt-out sentinel; only
	// coerce literal zero (the unset default) back to the built-in.
	if cfg.Runtime.MaxConsecutiveReadOnly == 0 {
		cfg.Runtime.MaxConsecutiveReadOnly = defaults.Runtime.MaxConsecutiveReadOnly
	}
	if cfg.Runtime.MaxPlannerSummarySteps <= 0 {
		cfg.Runtime.MaxPlannerSummarySteps = defaults.Runtime.MaxPlannerSummarySteps
	}
	if cfg.Runtime.MaxBuilderReadLoops == 0 {
		cfg.Runtime.MaxBuilderReadLoops = defaults.Runtime.MaxBuilderReadLoops
	}
	if cfg.Claw.HeartbeatIntervalSeconds <= 0 {
		cfg.Claw.HeartbeatIntervalSeconds = defaults.Claw.HeartbeatIntervalSeconds
	}
	if cfg.Claw.DreamIntervalMinutes <= 0 {
		cfg.Claw.DreamIntervalMinutes = defaults.Claw.DreamIntervalMinutes
	}
	if cfg.Claw.AutonomyPolicy == "" {
		cfg.Claw.AutonomyPolicy = defaults.Claw.AutonomyPolicy
	}
	if cfg.Claw.DefaultChannel == "" {
		cfg.Claw.DefaultChannel = defaults.Claw.DefaultChannel
	}
	if cfg.Claw.PersonaName == "" {
		cfg.Claw.PersonaName = defaults.Claw.PersonaName
	}
	if cfg.Claw.PersonaTone == "" {
		cfg.Claw.PersonaTone = defaults.Claw.PersonaTone
	}
	if cfg.Claw.IdentitySeed == "" {
		cfg.Claw.IdentitySeed = defaults.Claw.IdentitySeed
	}
	if cfg.Git.BaselineCommitMessage == "" {
		cfg.Git.BaselineCommitMessage = defaults.Git.BaselineCommitMessage
	}
	if cfg.Git.SnapshotCommitMessage == "" {
		cfg.Git.SnapshotCommitMessage = defaults.Git.SnapshotCommitMessage
	}
	if cfg.ModelLoading.ParallelSlots <= 0 {
		cfg.ModelLoading.ParallelSlots = defaults.ModelLoading.ParallelSlots
	}
	// Subagent concurrency follows ParallelSlots when the user has not set
	// it explicitly. The old behaviour capped at the built-in default of 2,
	// which silently wasted backend slots whenever the user bumped
	// parallel_slots to 4+. The user-facing contract is now: "however many
	// slots LM Studio has, that's how many subagents run concurrently".
	if cfg.Build.Subagents.Concurrency <= 0 {
		cfg.Build.Subagents.Concurrency = cfg.ModelLoading.ParallelSlots
	}
	if len(cfg.Build.Subagents.Roles) == 0 {
		cfg.Build.Subagents.Roles = append([]string(nil), defaults.Build.Subagents.Roles...)
	}
	if cfg.Explore.Subagents.Concurrency <= 0 {
		cfg.Explore.Subagents.Concurrency = cfg.ModelLoading.ParallelSlots
	}
	if len(cfg.Explore.Subagents.Roles) == 0 {
		cfg.Explore.Subagents.Roles = append([]string(nil), defaults.Explore.Subagents.Roles...)
	}
	if cfg.Plan.Subagents.Concurrency <= 0 {
		cfg.Plan.Subagents.Concurrency = cfg.ModelLoading.ParallelSlots
	}
	if len(cfg.Plan.Subagents.Roles) == 0 {
		cfg.Plan.Subagents.Roles = append([]string(nil), defaults.Plan.Subagents.Roles...)
	}
}

// InheritChatModelDefaults applies a pragmatic fallback for role-based model
// loading: if chat has been configured explicitly but per-role models are
// still blank or at their untouched defaults, treat them as inheriting chat.
// This keeps a freshly configured workspace usable without forcing the user to
// walk through /model-multi before the first message. Explicit role models
// still win and are never overwritten.
func InheritChatModelDefaults(cfg *Config) {
	if cfg == nil || cfg.Models == nil {
		return
	}
	chatModel := strings.TrimSpace(cfg.Models["chat"])
	if chatModel == "" {
		return
	}
	defaults := Defaults()
	roles := []string{"explorer", "planner", "editor", "reviewer", "summarizer"}
	chatDetected := cfg.Context.Detected
	for _, role := range roles {
		roleModel := strings.TrimSpace(cfg.Models[role])
		if roleModel != "" && roleModel != defaults.Models[role] {
			continue
		}
		cfg.Models[role] = chatModel
		if DetectedForRole(*cfg, role, chatModel) == nil && chatDetected != nil && chatDetected.LoadedContextLength > 0 {
			copyDetected := *chatDetected
			if strings.TrimSpace(copyDetected.ModelID) == "" {
				copyDetected.ModelID = chatModel
			}
			SetDetectedForRole(cfg, role, &copyDetected)
		}
	}
}

func minPositive(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

func DetectedForRole(cfg Config, role, modelID string) *DetectedContext {
	if cfg.Context.DetectedByRole != nil {
		if detected, ok := cfg.Context.DetectedByRole[role]; ok && detected.LoadedContextLength > 0 {
			return &detected
		}
	}
	if cfg.Context.Detected != nil && cfg.Context.Detected.LoadedContextLength > 0 {
		if modelID == "" || cfg.Context.Detected.ModelID == "" || cfg.Context.Detected.ModelID == modelID {
			return cfg.Context.Detected
		}
	}
	return nil
}

func ConfigForModelRole(cfg Config, role, modelID string) Config {
	out := cfg
	out.Models = cloneModels(cfg.Models)
	if out.Models == nil {
		out.Models = map[string]string{}
	}
	if modelID != "" {
		out.Models["chat"] = modelID
	}
	out.Context.Detected = DetectedForRole(cfg, role, modelID)
	return out
}

func ConfigForTaskRole(cfg Config, role, modelID string) Config {
	out := ConfigForModelRole(cfg, role, modelID)
	task := out.Context.Task
	if task.BudgetTokens > 0 {
		out.Context.BudgetTokens = task.BudgetTokens
	}
	if task.MaxNodes > 0 {
		out.Context.Yarn.MaxNodes = task.MaxNodes
	}
	if task.MaxFileBytes > 0 {
		out.Context.Yarn.MaxFileBytes = task.MaxFileBytes
	}
	if task.HistoryEvents > 0 {
		out.Context.Yarn.HistoryEvents = task.HistoryEvents
	}
	// Task snapshots intentionally stay small even when the model was loaded
	// with a larger detected context window.
	out.Context.Detected = nil
	// Subagents don't carry the parent's tier-B context: pins are a
	// user-level signal for the main agent and would consume the task's
	// tight 4k budget with material the worker doesn't need. Mentions in
	// the task prompt itself still flow through normally.
	out.Context.Yarn.Pins = "off"
	// Subagents also get the most compact yarn rendering by default: they
	// are read-only analysis workers and can re-read with read_file if a
	// scored file turns out to matter.
	out.Context.Yarn.RenderMode = "summary"
	return out
}

func SetDetectedForRole(cfg *Config, role string, detected *DetectedContext) {
	if cfg == nil || role == "" || detected == nil || detected.LoadedContextLength <= 0 {
		return
	}
	if cfg.Context.DetectedByRole == nil {
		cfg.Context.DetectedByRole = map[string]DetectedContext{}
	}
	cfg.Context.DetectedByRole[role] = *detected
}

func cloneModels(models map[string]string) map[string]string {
	if models == nil {
		return nil
	}
	out := make(map[string]string, len(models))
	for key, value := range models {
		out[key] = value
	}
	return out
}

func YarnProfiles() []YarnProfile {
	return []YarnProfile{
		{Name: "2B", LMContextMin: 8192, LMContextMax: 32000, BudgetTokens: 5000, ReserveOutputTokens: 1500, MaxNodes: 8, MaxFileBytes: 10000, HistoryEvents: 10, CompactEvents: 60, CompactTranscriptChars: 30000},
		{Name: "4B", LMContextMin: 8192, LMContextMax: 32000, BudgetTokens: 6500, ReserveOutputTokens: 1800, MaxNodes: 10, MaxFileBytes: 12000, HistoryEvents: 12, CompactEvents: 80, CompactTranscriptChars: 40000},
		{Name: "9B", LMContextMin: 12000, LMContextMax: 32000, BudgetTokens: 8000, ReserveOutputTokens: 2000, MaxNodes: 12, MaxFileBytes: 14000, HistoryEvents: 14, CompactEvents: 100, CompactTranscriptChars: 60000},
		{Name: "14B", LMContextMin: 16000, LMContextMax: 65000, BudgetTokens: 12000, ReserveOutputTokens: 2500, MaxNodes: 16, MaxFileBytes: 18000, HistoryEvents: 18, CompactEvents: 140, CompactTranscriptChars: 80000},
		{Name: "26B", LMContextMin: 24000, LMContextMax: 131000, BudgetTokens: 20000, ReserveOutputTokens: 3500, MaxNodes: 22, MaxFileBytes: 24000, HistoryEvents: 26, CompactEvents: 200, CompactTranscriptChars: 120000},
	}
}

func GetYarnProfile(name string) (YarnProfile, bool) {
	normalized := strings.ToUpper(strings.TrimSpace(name))
	for _, profile := range YarnProfiles() {
		if profile.Name == normalized {
			return profile, true
		}
	}
	return YarnProfile{}, false
}

// EffectiveBudgets returns the context window, YARN budget, and reserved
// output for the current config. When a DetectedContext is available and
// larger than the profile's static cap, it scales the budget and reserve by
// the profile's ratios so we actually exploit the extended window that the
// model was loaded with (e.g. YaRN-expanded Qwen).
func EffectiveBudgets(cfg Config) (window, budget, reserve int) {
	window = cfg.Context.ModelContextTokens
	budget = cfg.Context.BudgetTokens
	reserve = cfg.Context.ReserveOutputTokens
	detected := cfg.Context.Detected
	if detected == nil || detected.LoadedContextLength <= 0 {
		return
	}
	profile, ok := GetYarnProfile(cfg.Context.Yarn.Profile)
	if !ok || profile.LMContextMax <= 0 {
		// No ratio reference — expose detected as window but keep budgets.
		window = detected.LoadedContextLength
		return
	}
	effective := detected.LoadedContextLength
	budgetRatio := float64(profile.BudgetTokens) / float64(profile.LMContextMax)
	reserveRatio := float64(profile.ReserveOutputTokens) / float64(profile.LMContextMax)
	scaledBudget := int(float64(effective) * budgetRatio)
	scaledReserve := clampInt(int(float64(effective)*reserveRatio), 2048, 32768)
	// Safety margin for system prompt + tool defs.
	const safety = 2048
	if scaledBudget > effective-scaledReserve-safety {
		scaledBudget = effective - scaledReserve - safety
	}
	if scaledBudget < profile.BudgetTokens {
		scaledBudget = profile.BudgetTokens
	}
	window = effective
	budget = scaledBudget
	reserve = scaledReserve
	return
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func ApplyYarnProfile(cfg *Config, name string) (YarnProfile, bool) {
	profile, ok := GetYarnProfile(name)
	if !ok {
		return YarnProfile{}, false
	}
	cfg.Context.BudgetTokens = profile.BudgetTokens
	cfg.Context.ReserveOutputTokens = profile.ReserveOutputTokens
	cfg.Context.Yarn.Profile = profile.Name
	cfg.Context.Yarn.MaxNodes = profile.MaxNodes
	cfg.Context.Yarn.MaxFileBytes = profile.MaxFileBytes
	cfg.Context.Yarn.HistoryEvents = profile.HistoryEvents
	cfg.Context.Yarn.CompactEvents = profile.CompactEvents
	cfg.Context.Yarn.CompactTranscriptChars = profile.CompactTranscriptChars
	if cfg.Context.Yarn.Pins == "" {
		cfg.Context.Yarn.Pins = "always"
	}
	if cfg.Context.Yarn.Mentions == "" {
		cfg.Context.Yarn.Mentions = "always"
	}
	return profile, true
}
