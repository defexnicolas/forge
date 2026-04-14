package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	DefaultAgent    string            `toml:"default_agent"`
	ApprovalProfile string            `toml:"approval_profile"`
	Providers       Providers         `toml:"providers"`
	Context         ContextConfig     `toml:"context"`
	Skills          SkillsConfig      `toml:"skills"`
	Plugins         PluginsConfig     `toml:"plugins"`
	Models          map[string]string `toml:"models"`
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
	Engine              string     `toml:"engine"`
	BudgetTokens        int        `toml:"budget_tokens"`
	AutoCompact         bool       `toml:"auto_compact"`
	ModelContextTokens  int        `toml:"model_context_tokens"`
	ReserveOutputTokens int        `toml:"reserve_output_tokens"`
	Yarn                YarnConfig `toml:"yarn"`
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
			BudgetTokens:        4500,
			AutoCompact:         true,
			ModelContextTokens:  8192,
			ReserveOutputTokens: 2000,
			Yarn: YarnConfig{
				Profile:                "9B",
				MaxNodes:               8,
				MaxFileBytes:           12000,
				HistoryEvents:          12,
				Pins:                   "always",
				Mentions:               "always",
				CompactEvents:          80,
				CompactTranscriptChars: 50000,
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
		Models: map[string]string{
			"chat":       "local-model",
			"planner":    "local-model",
			"editor":     "local-model",
			"reviewer":   "local-model",
			"summarizer": "local-model",
		},
	}
}

func Normalize(cfg *Config) {
	defaults := Defaults()
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
}

func YarnProfiles() []YarnProfile {
	return []YarnProfile{
		{Name: "2B", LMContextMin: 4096, LMContextMax: 8192, BudgetTokens: 2200, ReserveOutputTokens: 1200, MaxNodes: 4, MaxFileBytes: 6000, HistoryEvents: 6, CompactEvents: 40, CompactTranscriptChars: 24000},
		{Name: "4B", LMContextMin: 6144, LMContextMax: 8192, BudgetTokens: 3200, ReserveOutputTokens: 1500, MaxNodes: 6, MaxFileBytes: 8000, HistoryEvents: 8, CompactEvents: 60, CompactTranscriptChars: 32000},
		{Name: "9B", LMContextMin: 8192, LMContextMax: 12000, BudgetTokens: 4500, ReserveOutputTokens: 2000, MaxNodes: 8, MaxFileBytes: 12000, HistoryEvents: 12, CompactEvents: 80, CompactTranscriptChars: 50000},
		{Name: "14B", LMContextMin: 12000, LMContextMax: 20000, BudgetTokens: 7000, ReserveOutputTokens: 2500, MaxNodes: 12, MaxFileBytes: 16000, HistoryEvents: 16, CompactEvents: 100, CompactTranscriptChars: 70000},
		{Name: "26B", LMContextMin: 16000, LMContextMax: 30000, BudgetTokens: 10000, ReserveOutputTokens: 3500, MaxNodes: 18, MaxFileBytes: 22000, HistoryEvents: 24, CompactEvents: 140, CompactTranscriptChars: 100000},
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
