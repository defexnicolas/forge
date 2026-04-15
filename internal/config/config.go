package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	DefaultAgent    string             `toml:"default_agent"`
	ApprovalProfile string             `toml:"approval_profile"`
	Providers       Providers          `toml:"providers"`
	Context         ContextConfig      `toml:"context"`
	Skills          SkillsConfig       `toml:"skills"`
	Plugins         PluginsConfig      `toml:"plugins"`
	Models          map[string]string  `toml:"models"`
	ModelLoading    ModelLoadingConfig `toml:"model_loading"`
	Build           BuildConfig        `toml:"build"`
}

type BuildConfig struct {
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

type PluginsConfig struct {
	Enabled          bool     `toml:"enabled"`
	ClaudeCompatible bool     `toml:"claude_compatible"`
	Marketplaces     []string `toml:"marketplaces"`
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

func Defaults() Config {
	return Config{
		DefaultAgent:    "build",
		ApprovalProfile: "normal",
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
				SupportsTools: false,
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
			},
			Task: TaskContextConfig{
				BudgetTokens:  4000,
				MaxNodes:      6,
				MaxFileBytes:  8000,
				HistoryEvents: 4,
			},
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
			Enabled:          true,
			ClaudeCompatible: true,
		},
		ModelLoading: ModelLoadingConfig{
			Strategy:      "single",
			ParallelSlots: 2,
		},
		Build: BuildConfig{
			Subagents: BuildSubagentsConfig{
				// Disabled by default: on local single-model setups (LM Studio)
				// running 3 preflight subagents with different role models causes
				// load thrashing and stalls the real build turn. Enable via
				// config when parallel inference capacity is available.
				Enabled:     false,
				Concurrency: 3,
				Roles:       []string{"explorer", "reviewer", "debug"},
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
	}
}

func Normalize(cfg *Config) {
	defaults := Defaults()
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
